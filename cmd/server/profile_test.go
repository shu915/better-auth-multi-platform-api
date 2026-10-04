package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shu915/better-auth-multi-platform-api/internal/profile"
)

// fakeProfiles answers from a map, records the user id it was asked for, and can fail on demand.
type fakeProfiles struct {
	byUser map[string]profile.Profile
	err    error
	asked  *[]string
}

func (f fakeProfiles) Get(_ context.Context, userID string) (profile.Profile, error) {
	if f.asked != nil {
		*f.asked = append(*f.asked, userID)
	}
	if f.err != nil {
		return profile.Profile{}, f.err
	}
	if p, ok := f.byUser[userID]; ok {
		return p, nil
	}
	return profile.Profile{}, profile.ErrNotFound
}

func getProfileAs(t *testing.T, store fakeProfiles, token, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler(fakeVerifier{}, testOrigins, store).ServeHTTP(rec, req)
	return rec
}

func TestPersonalDataIsNotCacheable(t *testing.T) {
	for _, path := range []string{"/me", "/me/profile"} {
		for _, token := range []string{"good", ""} { // the 401 must not be cached either
			rec := getProfileAs(t, fakeProfiles{}, token, path)
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("GET %s (token %q): Cache-Control = %q, want no-store", path, token, got)
			}
		}
	}
}

func TestGetProfileRequiresAuth(t *testing.T) {
	if rec := getProfileAs(t, fakeProfiles{}, "", "/me/profile"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	if rec := getProfileAs(t, fakeProfiles{}, "forged", "/me/profile"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged token: status = %d, want 401", rec.Code)
	}
}

func TestGetProfileReturnsOwnProfile(t *testing.T) {
	store := fakeProfiles{byUser: map[string]profile.Profile{
		"user-1": {UserID: "user-1", Bio: "hello"},
		"user-2": {UserID: "user-2", Bio: "someone else"},
	}}
	rec := getProfileAs(t, store, "good", "/me/profile")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "{\"user_id\":\"user-1\",\"bio\":\"hello\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestGetProfileIgnoresUserIDFromRequest(t *testing.T) {
	var asked []string
	getProfileAs(t, fakeProfiles{asked: &asked}, "good", "/me/profile?user_id=user-2")
	if len(asked) != 1 || asked[0] != "user-1" {
		t.Errorf("store was asked for %v, want only the token's user [user-1]", asked)
	}
}

func TestGetProfileWithoutRowReturnsEmptyBio(t *testing.T) {
	rec := getProfileAs(t, fakeProfiles{}, "good", "/me/profile")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "{\"user_id\":\"user-1\",\"bio\":\"\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestGetProfileStoreErrorIsHiddenFromClient(t *testing.T) {
	rec := getProfileAs(t, fakeProfiles{err: errors.New("connection refused to db.internal")}, "good", "/me/profile")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db.internal") {
		t.Errorf("internal error text leaked to the client: %q", rec.Body.String())
	}
}
