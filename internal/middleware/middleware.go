// Package middleware holds the HTTP cross-cutting concerns: request identity,
// panic recovery, structured access logging, request timeouts and bearer token
// authentication.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/logging"
)

// RequestIDHeader is the header carrying the correlation identifier.
const RequestIDHeader = "X-Request-Id"

type principalKey struct{}

// Authenticator resolves a bearer token into a principal.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (domain.Principal, error)
}

// ErrorWriter renders an error response. The HTTP layer supplies it so the
// middleware does not depend on the response encoder.
type ErrorWriter func(w http.ResponseWriter, r *http.Request, err error)

// WithPrincipal stores the authenticated principal in ctx.
func WithPrincipal(ctx context.Context, principal domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFrom reads the authenticated principal from ctx.
func PrincipalFrom(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(domain.Principal)
	return principal, ok
}

// NewRequestID generates a correlation identifier.
func NewRequestID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "req-unknown"
	}
	return "req-" + hex.EncodeToString(buffer)
}

// RequestID assigns or reuses the correlation identifier and echoes it back.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get(RequestIDHeader))
		if requestID == "" {
			requestID = NewRequestID()
		}
		w.Header().Set(RequestIDHeader, requestID)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), requestID)))
	})
}

// statusRecorder captures the response status for access logs.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(payload []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	written, err := s.ResponseWriter.Write(payload)
	s.bytes += written
	return written, err
}

// AccessLog emits one structured record per request.
func AccessLog(logger *logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(recorder, r)
			if recorder.status == 0 {
				recorder.status = http.StatusOK
			}
			logger.Info(r.Context(), "http request", map[string]any{
				"method":      r.Method,
				"path":        r.URL.Path,
				"status":      recorder.status,
				"bytes":       recorder.bytes,
				"duration_ms": time.Since(started).Milliseconds(),
			})
		})
	}
}

// Recover converts a panic into a 500 response without killing the server.
func Recover(logger *logging.Logger, writeError ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error(r.Context(), "panic recovered", map[string]any{
						"panic": recovered,
						"path":  r.URL.Path,
					})
					writeError(w, r, apperr.Internalf("internal_panic", "服务内部错误"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds the lifetime of a request context so that slow downstream work
// is cancelled instead of accumulating.
func Timeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if timeout <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Authenticate resolves the bearer token of protected routes.
func Authenticate(authenticator Authenticator, writeError ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				writeError(w, r, apperr.Wrap(apperr.ErrUnauthenticated, apperr.KindUnauthorized,
					"session_token_missing", "缺少会话令牌"))
				return
			}
			principal, err := authenticator.Authenticate(r.Context(), token)
			if err != nil {
				writeError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// Chain composes middlewares so that the first entry runs first.
func Chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for index := len(middlewares) - 1; index >= 0; index-- {
		handler = middlewares[index](handler)
	}
	return handler
}
