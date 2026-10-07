package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shu915/better-auth-multi-platform-api/internal/profile"
)

// fakeProfiles answers from a map, records the user id it was asked for, and can fail on demand.
type fakeProfiles struct {
	byUser map[string]profile.Profile
	err    error
	asked  *[]string
	puts   *[]profile.Profile // what Upsert was called with
}

func (f fakeProfiles) Upsert(_ context.Context, userID, bio string) (profile.Profile, error) {
	p := profile.Profile{UserID: userID, Bio: bio}
	if f.puts != nil {
		*f.puts = append(*f.puts, p)
	}
	if f.err != nil {
		return profile.Profile{}, f.err
	}
	if utf8.RuneCountInString(bio) > profile.MaxBioLength {
		return profile.Profile{}, profile.ErrInvalidBio
	}
	if f.byUser != nil {
		f.byUser[userID] = p
	}
	return p, nil
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

func putProfileAs(t *testing.T, store fakeProfiles, token, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler(fakeVerifier{}, testOrigins, store).ServeHTTP(rec, req)
	return rec
}

func TestPutProfileRequiresAuth(t *testing.T) {
	var puts []profile.Profile
	store := fakeProfiles{puts: &puts}
	for _, token := range []string{"", "forged"} {
		if rec := putProfileAs(t, store, token, "/me/profile", `{"bio":"x"}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", token, rec.Code)
		}
	}
	if len(puts) != 0 {
		t.Errorf("store was written without authentication: %v", puts)
	}
}

func TestPutProfileUpdatesOwnProfile(t *testing.T) {
	var puts []profile.Profile
	store := fakeProfiles{byUser: map[string]profile.Profile{}, puts: &puts}
	rec := putProfileAs(t, store, "good", "/me/profile", `{"bio":"こんにちは"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "{\"user_id\":\"user-1\",\"bio\":\"こんにちは\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if len(puts) != 1 || puts[0] != (profile.Profile{UserID: "user-1", Bio: "こんにちは"}) {
		t.Errorf("store was written with %v", puts)
	}
	// a following GET sees the change
	if rec := getProfileAs(t, store, "good", "/me/profile"); !strings.Contains(rec.Body.String(), "こんにちは") {
		t.Errorf("GET after PUT = %q", rec.Body.String())
	}
}

func TestPutProfileIsIdempotent(t *testing.T) {
	store := fakeProfiles{byUser: map[string]profile.Profile{}}
	first := putProfileAs(t, store, "good", "/me/profile", `{"bio":"same"}`)
	second := putProfileAs(t, store, "good", "/me/profile", `{"bio":"same"}`)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Errorf("repeat PUT differs: %d %q vs %d %q", first.Code, first.Body, second.Code, second.Body)
	}
}

func TestPutProfileAllowsEmptyBio(t *testing.T) {
	rec := putProfileAs(t, fakeProfiles{}, "good", "/me/profile", `{"bio":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (clearing the bio is allowed)", rec.Code)
	}
}

func TestPutProfileIgnoresUserIDFromRequest(t *testing.T) {
	var puts []profile.Profile
	store := fakeProfiles{puts: &puts}
	putProfileAs(t, store, "good", "/me/profile?user_id=user-2", `{"bio":"x","user_id":"user-2"}`)
	// user_id in the body is an unknown field and is rejected; the query one is ignored.
	putProfileAs(t, store, "good", "/me/profile?user_id=user-2", `{"bio":"x"}`)
	for _, p := range puts {
		if p.UserID != "user-1" {
			t.Errorf("store was written for %q, want only the token's user user-1", p.UserID)
		}
	}
	if len(puts) != 1 {
		t.Errorf("got %d writes, want 1 (the body with user_id must be rejected)", len(puts))
	}
}

func TestPutProfileRejectsBadInput(t *testing.T) {
	tooLong := strings.Repeat("あ", profile.MaxBioLength+1)
	huge := `{"bio":"` + strings.Repeat("a", maxProfileBody) + `"}`
	tests := []struct {
		name string
		body string
		want int
	}{
		{"empty body", ``, http.StatusBadRequest},
		{"not JSON", `bio=x`, http.StatusBadRequest},
		{"truncated JSON", `{"bio":`, http.StatusBadRequest},
		{"JSON array", `["x"]`, http.StatusBadRequest},
		{"missing bio", `{}`, http.StatusBadRequest},
		{"null bio", `{"bio":null}`, http.StatusBadRequest},
		{"bio is a number", `{"bio":1}`, http.StatusBadRequest},
		{"unknown field", `{"bio":"x","admin":true}`, http.StatusBadRequest},
		{"trailing data", `{"bio":"x"} {"bio":"y"}`, http.StatusBadRequest},
		{"stray closing brace", `{"bio":"x"}}`, http.StatusBadRequest},
		{"stray closing bracket", `{"bio":"x"}]`, http.StatusBadRequest},
		{"bio too long", `{"bio":"` + tooLong + `"}`, http.StatusUnprocessableEntity},
		{"body too large", huge, http.StatusRequestEntityTooLarge},
		{"valid object then too much trailing data", `{"bio":"x"}` + strings.Repeat(" ", maxProfileBody) + `x`, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var puts []profile.Profile
			rec := putProfileAs(t, fakeProfiles{puts: &puts}, "good", "/me/profile", tt.body)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.want, rec.Body)
			}
			if tt.want != http.StatusUnprocessableEntity && len(puts) != 0 {
				t.Errorf("store was written despite bad input: %v", puts)
			}
		})
	}
}

func TestPutProfileAcceptsMaxLengthBio(t *testing.T) {
	bio := strings.Repeat("あ", profile.MaxBioLength) // 1000 characters, 3000 bytes
	rec := putProfileAs(t, fakeProfiles{}, "good", "/me/profile", `{"bio":"`+bio+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestPutProfileStoreErrorIsHiddenFromClient(t *testing.T) {
	rec := putProfileAs(t, fakeProfiles{err: errors.New("connection refused to db.internal")}, "good", "/me/profile", `{"bio":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db.internal") {
		t.Errorf("internal error text leaked to the client: %q", rec.Body.String())
	}
}
