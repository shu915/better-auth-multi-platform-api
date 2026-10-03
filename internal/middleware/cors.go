package middleware

import (
	"net/http"
	"slices"
	"strings"
)

// CORS lets browsers on the listed origins call the API with an Authorization header.
// Put it outside Authenticate so preflight (OPTIONS) requests and 401 responses carry the headers.
// Origins are matched exactly; "*" is deliberately not supported.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			// Responses differ by Origin, so caches must key on it.
			w.Header().Add("Vary", "Origin")

			if origin == "" || !slices.Contains(allowedOrigins, origin) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Add("Vary", "Access-Control-Request-Method")
				w.Header().Add("Vary", "Access-Control-Request-Headers")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ParseOrigins splits a comma-separated list, dropping blanks and trailing slashes.
func ParseOrigins(csv string) []string {
	var out []string
	for _, o := range strings.Split(csv, ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			out = append(out, o)
		}
	}
	return out
}
