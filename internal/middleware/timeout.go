package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout gives every request a deadline, so a slow database or JWKS server cannot hold a
// request (and its connection from the pool) forever. Handlers see it through r.Context().
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
