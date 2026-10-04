package middleware

import "net/http"

// NoStore marks every response as uncacheable, so personal data is never kept by the
// browser or a shared cache.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
