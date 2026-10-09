package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/shu915/better-auth-multi-platform-api/internal/middleware"
	"github.com/shu915/better-auth-multi-platform-api/internal/profile"
)

type profileStore interface {
	Get(ctx context.Context, userID string) (profile.Profile, error)
	Upsert(ctx context.Context, userID, bio string) (profile.Profile, error)
	Delete(ctx context.Context, userID string) error
}

// maxProfileBody caps the request body: a bio is at most 1000 characters (4 bytes each in UTF-8,
// more when escaped as JSON), so this is generous but still bounded.
const maxProfileBody = 16 << 10

var errTrailingData = errors.New("trailing data after JSON object")

type updateProfileRequest struct {
	Bio *string `json:"bio"` // a pointer, so a missing field is not mistaken for an empty bio
}

type profileResponse struct {
	UserID string `json:"user_id"`
	Bio    string `json:"bio"`
}

// getProfile returns the caller's own profile. The user id comes only from the verified
// token, never from the request, so one user cannot read another's profile.
func getProfile(store profileStore) http.Handler {
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

// putProfile replaces the caller's own bio. Like getProfile, the user id comes only from the
// verified token. It is idempotent: the same body always leaves the same profile.
func putProfile(store profileStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := middleware.UserID(r.Context())
		if !ok { // authn did not run: a wiring bug, never trust the request
			internalError(w)
			return
		}
		var req updateProfileRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProfileBody))
		dec.DisallowUnknownFields()
		err := dec.Decode(&req)
		if err == nil { // anything after the object is invalid
			if err = dec.Decode(&struct{}{}); errors.Is(err, io.EOF) {
				err = nil
			} else if err == nil {
				err = errTrailingData
			}
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				middleware.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			middleware.WriteError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Bio == nil {
			middleware.WriteError(w, http.StatusBadRequest, "bio is required")
			return
		}
		p, err := store.Upsert(r.Context(), userID, *req.Bio)
		switch {
		case errors.Is(err, profile.ErrInvalidBio):
			middleware.WriteError(w, http.StatusUnprocessableEntity, "invalid bio")
			return
		case err != nil:
			log.Printf("put profile: %v", err)
			internalError(w)
			return
		}
		writeJSON(w, http.StatusOK, profileResponse{UserID: p.UserID, Bio: p.Bio})
	})
}

// deleteMe removes the caller's own data for an account deletion. Like the other handlers, the
// user id comes only from the verified token. It is idempotent (a second call, or a user with no
// data, still gets 204), so the web app can retry after a partial failure. Deleting the profile row
// is all it does: the other tables follow their declared policy through their foreign keys
// (see package userdata).
func deleteMe(store profileStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := middleware.UserID(r.Context())
		if !ok { // authn did not run: a wiring bug, never trust the request
			internalError(w)
			return
		}
		if err := store.Delete(r.Context(), userID); err != nil {
			log.Printf("delete me: %v", err)
			internalError(w)
			return
		}
		log.Printf("delete me: ok user=%q", userID) // an account deletion should be traceable afterwards
		w.WriteHeader(http.StatusNoContent)
	})
}

func internalError(w http.ResponseWriter) {
	middleware.WriteError(w, http.StatusInternalServerError, "internal error")
}
