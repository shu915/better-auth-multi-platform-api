// Package middleware holds net/http middleware, each in the func(http.Handler) http.Handler form.
package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shu915/better-auth-multi-platform-api/internal/auth"
)

// logEvery limits "cannot verify" logs: during a JWKS outage every request fails the same way.
const logEvery = 30 * time.Second

// maxTokenLen caps the bearer token size before any parsing; real tokens are well under 1KB.
const maxTokenLen = 4096

// TokenVerifier turns a bearer token into a user id. It must return an error wrapping
// auth.ErrInvalidToken for a bad token; any other error means "could not check" (503).
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (userID string, err error)
}

type userIDKey struct{}

// UserID returns the authenticated user id set by Authenticate.
func UserID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey{}).(string)
	return id, ok
}

// Authenticate rejects requests without a valid "Authorization: Bearer <jwt>" header.
func Authenticate(v TokenVerifier) func(http.Handler) http.Handler {
	var unavailableLog throttle
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
			if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > maxTokenLen {
				unauthorized(w)
				return
			}
			userID, err := v.Verify(r.Context(), token)
			if err != nil {
				if errors.Is(err, auth.ErrInvalidToken) {
					unauthorized(w)
					return
				}
				// Not the caller's fault (e.g. JWKS unreachable): don't make clients think they are logged out.
				if skipped, ok := unavailableLog.allow(); ok {
					log.Printf("auth: cannot verify token (%d similar errors suppressed): %v", skipped, err)
				}
				writeError(w, http.StatusServiceUnavailable, "service unavailable")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, userID)))
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(`{"error":"` + msg + `"}` + "\n")); err != nil {
		log.Printf("write error response: %v", err)
	}
}

// throttle lets one event through per logEvery and counts the ones it swallowed.
type throttle struct {
	mu      sync.Mutex
	last    time.Time
	skipped int
}

func (t *throttle) allow() (skipped int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.last) < logEvery {
		t.skipped++
		return 0, false
	}
	t.last, skipped, t.skipped = time.Now(), t.skipped, 0
	return skipped, true
}
