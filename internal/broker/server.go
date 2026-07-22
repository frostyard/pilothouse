package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
)

type Server struct {
	actions       *ActionRegistry
	attempts      *attemptLimiter
	authenticator auth.Authenticator
	logger        *slog.Logger
	loginSlots    chan struct{}
	queries       *QueryRegistry
	resolver      auth.Resolver
	sessions      *SessionStore
	streamActions *StreamActionRegistry
	streamQueries *StreamQueryRegistry
}

type attempt struct {
	failures int
	next     time.Time
}

type attemptLimiter struct {
	attempts map[string]attempt
	mu       sync.Mutex
	now      func() time.Time
}

func NewServer(authenticator auth.Authenticator, resolver auth.Resolver, sessions *SessionStore, actions *ActionRegistry, queries *QueryRegistry, streamActions *StreamActionRegistry, streamQueries *StreamQueryRegistry, logger *slog.Logger) *Server {
	return &Server{
		actions:       actions,
		attempts:      &attemptLimiter{attempts: map[string]attempt{}, now: time.Now},
		authenticator: authenticator,
		logger:        logger,
		loginSlots:    make(chan struct{}, 8),
		queries:       queries,
		resolver:      resolver,
		sessions:      sessions,
		streamActions: streamActions,
		streamQueries: streamQueries,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/login", s.login)
	mux.HandleFunc("POST /v1/logout", s.logout)
	mux.HandleFunc("GET /v1/session", s.currentSession)
	mux.HandleFunc("POST /v1/actions/{id}", s.execute)
	mux.HandleFunc("POST /v1/queries/{id}", s.query)
	mux.HandleFunc("POST /v1/stream-actions/{id}", s.streamAction)
	mux.HandleFunc("POST /v1/stream-queries/{id}", s.streamQuery)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) streamQuery(w http.ResponseWriter, r *http.Request) {
	session, identity, ok := s.authorize(w, r)
	if !ok {
		return
	}
	var request QueryRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if s.streamQueryRequiresAdmin(r.PathValue("id")) && !identity.Admin {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "not authorized"})
		return
	}
	result, err := s.streamQueries.Execute(r.Context(), identity, r.PathValue("id"), request.Parameters)
	if err != nil {
		s.streamError(w, err, "stream query", r.PathValue("id"), session.Identity.Username)
		return
	}
	defer func() { _ = result.Body.Close() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-r.Context().Done():
			_ = result.Body.Close()
		case <-done:
		}
	}()
	w.Header().Set("Content-Length", fmt.Sprintf("%d", result.Size))
	w.Header().Set("Content-Type", result.MediaType)
	w.Header().Set(StreamNameHeader, base64.RawURLEncoding.EncodeToString([]byte(result.Filename)))
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, &requestContextReader{ctx: r.Context(), reader: result.Body}, result.Size); err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Warn("broker stream query copy failed", "error", err, "query", r.PathValue("id"), "user", session.Identity.Username)
	}
}

func (s *Server) streamAction(w http.ResponseWriter, r *http.Request) {
	session, identity, ok := s.authorize(w, r)
	if !ok {
		return
	}
	if s.streamActionRequiresAdmin(r.PathValue("id")) && !identity.Admin {
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "not authorized"})
		return
	}
	metadata := r.Header.Get(StreamMetadataHeader)
	if len(metadata) == 0 || len(metadata) > 8<<10 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request"})
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(metadata)
	if err != nil || len(decoded) > 8<<10 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request"})
		return
	}
	var request QueryRequest
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request"})
		return
	}
	if limit, ok := s.streamActionLimit(r.PathValue("id")); ok && r.ContentLength > limit {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{Error: "stream exceeds registered limit"})
		return
	}
	if err := s.streamActions.Execute(r.Context(), identity, r.PathValue("id"), request.Parameters, r.Body); err != nil {
		s.streamError(w, err, "stream action", r.PathValue("id"), session.Identity.Username)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) streamActionLimit(id string) (int64, bool) {
	s.streamActions.mu.RLock()
	defer s.streamActions.mu.RUnlock()
	definition, ok := s.streamActions.actions[id]
	return definition.Limit, ok
}

func (s *Server) streamActionRequiresAdmin(id string) bool {
	s.streamActions.mu.RLock()
	defer s.streamActions.mu.RUnlock()
	return s.streamActions.actions[id].Admin
}

func (s *Server) streamQueryRequiresAdmin(id string) bool {
	s.streamQueries.mu.RLock()
	defer s.streamQueries.mu.RUnlock()
	return s.streamQueries.queries[id].Admin
}

func (s *Server) streamError(w http.ResponseWriter, err error, operation, id, username string) {
	status, message, _ := PublicErrorDetails(err)
	if errors.Is(err, ErrStreamTooLarge) {
		status, message = http.StatusRequestEntityTooLarge, "stream exceeds registered limit"
	}
	s.logger.Warn("broker "+operation+" denied or failed", "error", err, "id", id, "user", username)
	writeJSON(w, status, ErrorResponse{Error: message})
}

type requestContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *requestContextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	session, identity, ok := s.authorize(w, r)
	if !ok {
		return
	}
	var request QueryRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	result, err := s.queries.Execute(r.Context(), identity, r.PathValue("id"), request.Parameters)
	if err != nil {
		s.logger.Warn("broker query denied or failed", "error", err, "query", r.PathValue("id"), "user", session.Identity.Username)
		status, message := http.StatusForbidden, "query denied"
		var public *PublicError
		if errors.As(err, &public) {
			status, message, _ = PublicErrorDetails(err)
		}
		writeJSON(w, status, ErrorResponse{Error: message})
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		s.logger.Error("broker query encoding failed", "error", err, "query", r.PathValue("id"))
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "query result unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, QueryResponse{Result: encoded})
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	session, ok := s.sessions.Get(bearerToken(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "authentication required"})
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request) {
	session, identity, ok := s.authorize(w, r)
	if !ok {
		return
	}
	var request ActionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if err := s.actions.Execute(r.Context(), identity, r.PathValue("id"), request.Parameters, request.Confirmation); err != nil {
		s.logger.Warn("broker action denied or failed", "action", r.PathValue("id"), "error", err, "user", session.Identity.Username)
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: err.Error()})
		return
	}
	s.logger.Info("broker action completed", "action", r.PathValue("id"), "user", session.Identity.Username)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (SessionResponse, auth.Identity, bool) {
	session, ok := s.sessions.Get(bearerToken(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "authentication required"})
		return SessionResponse{}, auth.Identity{}, false
	}
	identity, err := s.resolver.Resolve(session.Identity.Username)
	if err != nil {
		s.logger.Warn("broker identity refresh failed", "error", err, "user", session.Identity.Username)
		writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "account is no longer authorized"})
		return SessionResponse{}, auth.Identity{}, false
	}
	return session, identity, true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var request LoginRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	key := strings.ToLower(request.Username) + "\x00" + request.Remote
	if request.Username == "" || request.Password == "" || len(request.Username) > 128 || len(request.Password) > 1024 {
		s.loginFailed(w, key)
		return
	}
	if delay := s.attempts.delay(key); delay > 0 {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", max(1, int(delay.Seconds()))))
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "authentication temporarily unavailable"})
		clearString(&request.Password)
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "authentication temporarily unavailable"})
		clearString(&request.Password)
		return
	}
	err := s.authenticator.Authenticate(request.Username, request.Password)
	clearString(&request.Password)
	if err != nil {
		s.loginFailed(w, key)
		return
	}
	identity, err := s.resolver.Resolve(request.Username)
	if err != nil {
		s.logger.Warn("authenticated account rejected", "error", err, "user", request.Username)
		s.loginFailed(w, key)
		return
	}
	token, session, err := s.sessions.Create(identity)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "could not create session"})
		return
	}
	s.attempts.success(key)
	s.logger.Info("broker login completed", "admin", identity.Admin, "uid", identity.UID, "user", identity.Username)
	writeJSON(w, http.StatusOK, LoginResponse{Session: session, Token: token})
}

func (s *Server) loginFailed(w http.ResponseWriter, key string) {
	s.attempts.failure(key)
	time.Sleep(250 * time.Millisecond)
	writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid username or password"})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sessions.Delete(bearerToken(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (l *attemptLimiter) delay(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return max(0, l.attempts[key].next.Sub(l.now()))
}

func (l *attemptLimiter) failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.attempts) >= 4096 {
		cutoff := l.now().Add(-10 * time.Minute)
		for existingKey, existing := range l.attempts {
			if existing.next.Before(cutoff) {
				delete(l.attempts, existingKey)
			}
		}
	}
	entry, exists := l.attempts[key]
	if !exists && len(l.attempts) >= 4096 {
		return
	}
	entry.failures++
	delay := time.Second * time.Duration(1<<min(entry.failures-1, 5))
	entry.next = l.now().Add(delay)
	l.attempts[key] = entry
}

func (l *attemptLimiter) success(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

func bearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(value, "Bearer ")
	if !ok {
		return ""
	}
	return token
}

func clearString(value *string) {
	if value == nil {
		return
	}
	buffer := []byte(*value)
	clear(buffer)
	*value = ""
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
