package middleware

import (
	"log"
	"net/http"
	"runtime/debug"
)

// Recover turns a panic in a handler into a 500 instead of a dropped connection. Only the panic
// value is logged, never the request, which can carry a bearer token. http.ErrAbortHandler is
// passed on because net/http uses it on purpose to abort a response.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			// The stack holds code locations only, so it is safe to log and tells where it happened.
			log.Printf("panic in handler: %v\n%s", rec, debug.Stack())
			WriteError(w, http.StatusInternalServerError, "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}
