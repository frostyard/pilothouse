package broker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
)

type session struct {
	absExpiresAt time.Time
	csrf         string
	identity     auth.Identity
	lastActiveAt time.Time
}

type SessionStore struct {
	absoluteTTL time.Duration
	idleTTL     time.Duration
	mu          sync.Mutex
	now         func() time.Time
	sessions    map[[32]byte]session
}

func NewSessionStore(idleTTL, absoluteTTL time.Duration) *SessionStore {
	return &SessionStore{
		absoluteTTL: absoluteTTL,
		idleTTL:     idleTTL,
		now:         time.Now,
		sessions:    map[[32]byte]session{},
	}
}

func (s *SessionStore) Create(identity auth.Identity) (string, SessionResponse, error) {
	token, err := randomToken()
	if err != nil {
		return "", SessionResponse{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", SessionResponse{}, err
	}
	now := s.now()
	entry := session{
		absExpiresAt: now.Add(s.absoluteTTL),
		csrf:         csrf,
		identity:     identity,
		lastActiveAt: now,
	}
	s.mu.Lock()
	for key, existing := range s.sessions {
		if !now.Before(existing.absExpiresAt) || now.Sub(existing.lastActiveAt) >= s.idleTTL {
			delete(s.sessions, key)
		}
	}
	s.sessions[tokenKey(token)] = entry
	s.mu.Unlock()
	return token, sessionResponse(entry, now, s.idleTTL), nil
}

func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, tokenKey(token))
	s.mu.Unlock()
}

func (s *SessionStore) Get(token string) (SessionResponse, bool) {
	if token == "" {
		return SessionResponse{}, false
	}
	now := s.now()
	key := tokenKey(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.sessions[key]
	if !ok || !now.Before(entry.absExpiresAt) || now.Sub(entry.lastActiveAt) >= s.idleTTL {
		delete(s.sessions, key)
		return SessionResponse{}, false
	}
	entry.lastActiveAt = now
	s.sessions[key] = entry
	return sessionResponse(entry, now, s.idleTTL), true
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func sessionResponse(entry session, now time.Time, idleTTL time.Duration) SessionResponse {
	expiresAt := now.Add(idleTTL)
	if entry.absExpiresAt.Before(expiresAt) {
		expiresAt = entry.absExpiresAt
	}
	return SessionResponse{CSRF: entry.csrf, ExpiresAt: expiresAt, Identity: entry.identity}
}

func tokenKey(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}
