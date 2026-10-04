package main

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/shu915/better-auth-multi-platform-api/internal/middleware"
	"github.com/shu915/better-auth-multi-platform-api/internal/profile"
)

type profileGetter interface {
	Get(ctx context.Context, userID string) (profile.Profile, error)
}

type profileResponse struct {
	UserID string `json:"user_id"`
	Bio    string `json:"bio"`
}

// getProfile returns the caller's own profile. The user id comes only from the verified
// token, never from the request, so one user cannot read another's profile.
func getProfile(store profileGetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := middleware.UserID(r.Context())
		if !ok { // authn did not run: a wiring bug, never trust the request
			internalError(w)
			return
		}
		p, err := store.Get(r.Context(), userID)
		switch {
		case errors.Is(err, profile.ErrNotFound):
			p = profile.Profile{UserID: userID} // no row yet: an empty profile, not an error
		case err != nil:
			log.Printf("get profile: %v", err)
			internalError(w)
			return
		}
		writeJSON(w, http.StatusOK, profileResponse{UserID: p.UserID, Bio: p.Bio})
	})
}

func internalError(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}
