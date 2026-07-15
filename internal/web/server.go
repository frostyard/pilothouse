package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

const sessionCookie = "pilothouse_session"

type BrokerClient interface {
	Action(context.Context, string, string, map[string]string) error
	Health(context.Context) error
	Login(context.Context, string, string, string) (broker.LoginResponse, error)
	Logout(context.Context, string) error
	Query(context.Context, string, string, map[string]string, any) error
	Session(context.Context, string) (broker.SessionResponse, error)
}

type Server struct {
	broker       BrokerClient
	crossOrigin  *http.CrossOriginProtection
	logger       *slog.Logger
	loginCSRF    string
	mux          *http.ServeMux
	registry     *platform.Registry
	secureCookie bool
}

type requestSession struct {
	data  broker.SessionResponse
	token string
}

type sessionContextKey struct{}

func NewServer(registry *platform.Registry, brokerClient BrokerClient, logger *slog.Logger, secureCookie bool, allowedOrigins ...string) (*Server, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("generate login csrf token: %w", err)
	}
	crossOrigin := http.NewCrossOriginProtection()
	for _, origin := range allowedOrigins {
		key, err := normalizeOrigin(origin)
		if err != nil {
			return nil, fmt.Errorf("allowed origin %q: %w", origin, err)
		}
		if err := crossOrigin.AddTrustedOrigin(key); err != nil {
			return nil, fmt.Errorf("allowed origin %q: %w", origin, err)
		}
		if strings.HasPrefix(key, "https://") {
			secureCookie = true
		}
	}
	s := &Server{
		broker:       brokerClient,
		crossOrigin:  crossOrigin,
		logger:       logger,
		loginCSRF:    base64.RawURLEncoding.EncodeToString(token),
		mux:          http.NewServeMux(),
		registry:     registry,
		secureCookie: secureCookie,
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", StaticHandler()))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("POST /logout", s.logout)
	s.mux.HandleFunc("GET /{$}", s.dashboard)
	for _, module := range registry.Modules() {
		module.Mount(s.mux, s)
	}
	return s, nil
}

func (s *Server) CSRFToken(r *http.Request) string {
	return sessionFromContext(r.Context()).data.CSRF
}

func (s *Server) Execute(ctx context.Context, r *http.Request, action string, parameters map[string]string) error {
	session := sessionFromContext(r.Context())
	if session.token == "" {
		return broker.ErrUnauthorized
	}
	return s.broker.Action(ctx, session.token, action, parameters)
}

func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.accessLog(s.authenticate(s.mux)))
}

func (s *Server) Identity(r *http.Request) auth.Identity {
	return sessionFromContext(r.Context()).data.Identity
}

func (s *Server) Query(ctx context.Context, id string, parameters map[string]string, target any) error {
	session := sessionFromContext(ctx)
	if session.token == "" {
		return broker.ErrUnauthorized
	}
	return s.broker.Query(ctx, session.token, id, parameters, target)
}

func (s *Server) Render(w http.ResponseWriter, r *http.Request, page platform.Page) error {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return Layout(LayoutData{
		Active:    page.Active,
		CSRF:      s.CSRFToken(r),
		Eyebrow:   page.Eyebrow,
		Flash:     r.URL.Query().Get("notice"),
		FlashKind: r.URL.Query().Get("kind"),
		Identity:  s.Identity(r),
		Modules:   s.registry.Manifests(),
		Path:      r.URL.Path,
		Title:     page.Title,
	}, page.Body).Render(r.Context(), w)
}

func (s *Server) ValidateAction(w http.ResponseWriter, r *http.Request) bool {
	if sessionFromContext(r.Context()).token == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	provided := r.FormValue("csrf")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.CSRFToken(r))) != 1 {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return false
	}
	return s.validateOrigin(w, r)
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("request", "duration", time.Since(started), "method", r.Method, "path", r.URL.Path)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			s.redirectToLogin(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		session, err := s.broker.Session(ctx, cookie.Value)
		if err != nil {
			s.clearSessionCookie(w, r)
			if errors.Is(err, broker.ErrUnauthorized) {
				s.redirectToLogin(w, r)
				return
			}
			http.Error(w, "privileged broker unavailable", http.StatusServiceUnavailable)
			return
		}
		requestContext := context.WithValue(r.Context(), sessionContextKey{}, requestSession{data: session, token: cookie.Value})
		next.ServeHTTP(w, r.WithContext(requestContext))
	})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	cards := make([]platform.DashboardCard, 0)
	for _, module := range s.registry.Modules() {
		moduleCards, err := module.Dashboard(ctx, s)
		if err != nil {
			s.logger.Warn("dashboard module unavailable", "error", err, "module", module.Manifest().ID)
			cards = append(cards, platform.DashboardCard{Component: ModuleErrorCard(module.Manifest().Name, err.Error()), Order: module.Manifest().Order, Span: platform.SpanHalf})
			continue
		}
		cards = append(cards, moduleCards...)
	}
	slices.SortStableFunc(cards, func(a, b platform.DashboardCard) int { return a.Order - b.Order })
	if err := s.Render(w, r, platform.Page{Active: "dashboard", Body: Dashboard(cards), Eyebrow: "System overview", Title: greeting()}); err != nil {
		s.logger.Error("render dashboard", "error", err)
	}
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		_, sessionErr := s.broker.Session(ctx, cookie.Value)
		cancel()
		if sessionErr == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	s.renderLogin(w, r, "", "")
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.broker.Health(ctx); err != nil {
		http.Error(w, "privileged broker unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ready\n"))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(s.loginCSRF)) != 1 {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if !s.validateOrigin(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	response, err := s.broker.Login(ctx, username, password, remoteHost(r.RemoteAddr))
	cancel()
	r.Form.Set("password", "")
	if err != nil {
		if errors.Is(err, broker.ErrUnavailable) {
			s.renderLoginStatus(w, r, "Sign-in service is temporarily unavailable.", username, http.StatusServiceUnavailable)
			return
		}
		s.renderLogin(w, r, "Invalid username or password.", username)
		return
	}
	s.setSessionCookie(w, r, response.Token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.ValidateAction(w, r) {
		return
	}
	session := sessionFromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	_ = s.broker.Logout(ctx, session.token)
	cancel()
	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, message, username string) {
	status := http.StatusOK
	if message != "" {
		status = http.StatusUnauthorized
	}
	s.renderLoginStatus(w, r, message, username, status)
}

func (s *Server) renderLoginStatus(w http.ResponseWriter, r *http.Request, message, username string, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = Login(message, username, s.loginCSRF).Render(r.Context(), w)
}

func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie || r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookie || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func greeting() string {
	hour := time.Now().Hour()
	if hour < 12 {
		return "Good morning"
	}
	if hour < 18 {
		return "Good afternoon"
	}
	return "Good evening"
}

func publicPath(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/login" || strings.HasPrefix(path, "/static/")
}

func remoteHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return address
}

func sessionFromContext(ctx context.Context) requestSession {
	session, _ := ctx.Value(sessionContextKey{}).(requestSession)
	return session
}

func (s *Server) validateOrigin(w http.ResponseWriter, r *http.Request) bool {
	if err := s.crossOrigin.Check(r); err == nil {
		return true
	}
	s.logger.Warn(
		"cross-origin action rejected",
		"fetch_site", r.Header.Get("Sec-Fetch-Site"),
		"origin", r.Header.Get("Origin"),
		"request_host", r.Host,
	)
	http.Error(w, "cross-origin action rejected", http.StatusForbidden)
	return false
}

func normalizeOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("must be an absolute HTTP(S) origin")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("must not contain a path")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	parsed.Path = ""
	return parsed.Scheme + "://" + parsed.Host, nil
}
