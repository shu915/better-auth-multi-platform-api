package middleware

import (
	"net/http"
	"time"
)

// authTimeSkew is how far in the future an auth_time may be (clocks of the web app and this API
// are not exactly equal). Further than this, the claim cannot be true.
const authTimeSkew = 30 * time.Second

// RequireRecentAuth refuses the request with 403 unless the user signed in within maxAge. It
// guards what cannot be undone (deleting the account's data), and must run after Authenticate.
//
// The web app asks for the same thing (a sign-in within 5 minutes) before it deletes an account.
// A token can be minted from a stolen session cookie at any time, and with it the API could be
// called directly, skipping that check; the sign-in time in the token (auth_time) cannot be made
// newer that way. A token without auth_time is refused too: not knowing is not recent.
func RequireRecentAuth(maxAge time.Duration) func(http.Handler) http.Handler {
	return requireRecentAuth(maxAge, time.Now)
}

func requireRecentAuth(maxAge time.Duration, now func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authTime, ok := AuthTime(r.Context())
			age := now().Sub(authTime)
			if !ok || age > maxAge || age < -authTimeSkew {
				WriteError(w, http.StatusForbidden, "recent sign-in required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
