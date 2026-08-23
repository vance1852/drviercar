package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// SessionTokenBytes is the entropy of an issued session token.
const SessionTokenBytes = 24

// Session is a server side login session. Only the token digest is persisted so
// that a database dump cannot be replayed against the API.
type Session struct {
	ID         int64
	TokenHash  string
	OperatorID int64
	Role       Role
	IssuedAt   time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastSeenAt time.Time
}

// NewSessionToken generates an opaque bearer token.
func NewSessionToken() (string, error) {
	buffer := make([]byte, SessionTokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", apperr.Wrap(err, apperr.KindInternal, "session_token_failed", "无法生成会话令牌")
	}
	return hex.EncodeToString(buffer), nil
}

// HashSessionToken derives the stored digest of a bearer token.
func HashSessionToken(token string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(digest[:])
}

// Revoked reports whether the session has been explicitly revoked.
func (s *Session) Revoked() bool { return s.RevokedAt != nil }

// Expired reports whether the session validity window has elapsed.
func (s *Session) Expired(now time.Time) bool {
	return !now.Before(s.ExpiresAt)
}

// EnsureUsable validates that the session may authenticate a request.
func (s *Session) EnsureUsable(now time.Time) error {
	if s.Revoked() {
		return apperr.Wrap(apperr.ErrUnauthenticated, apperr.KindUnauthorized,
			"session_revoked", "会话已退出，请重新登录")
	}
	if s.Expired(now) {
		return apperr.Wrap(apperr.ErrSessionExpired, apperr.KindUnauthorized,
			"session_expired", "会话已过期，请重新登录")
	}
	return nil
}

// Clone returns an independent copy of the session including its pointer field.
func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}
	copied := *s
	if s.RevokedAt != nil {
		revoked := *s.RevokedAt
		copied.RevokedAt = &revoked
	}
	return &copied
}

// Principal is the authenticated identity attached to a request context.
type Principal struct {
	OperatorID int64
	Username   string
	Role       Role
	SessionID  int64
	ExpiresAt  time.Time
}

// RequireRole checks that the principal holds one of the accepted roles.
func (p Principal) RequireRole(accepted ...Role) error {
	for _, role := range accepted {
		if p.Role == role {
			return nil
		}
	}
	return apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
		"role_not_permitted", "当前角色无权执行该操作")
}
