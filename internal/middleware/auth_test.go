package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shu915/better-auth-multi-platform-api/internal/auth"
)

type fakeVerifier struct{}

var signedIn = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

func (fakeVerifier) Verify(_ context.Context, token string) (auth.Claims, error) {
	switch token {
	case "good":
		return auth.Claims{UserID: "user-1", AuthTime: signedIn}, nil
	case "no-auth-time":
		return auth.Claims{UserID: "user-1"}, nil
	case "jwks-down":
		return auth.Claims{}, errors.New("jwks unreachable")
	}
	return auth.Claims{}, auth.ErrInvalidToken
}

func TestAuthenticate(t *testing.T) {
	var gotUser string
	h := Authenticate(fakeVerifier{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _ = UserID(r.Context())
	}))

	tests := []struct {
		name, header string
		wantStatus   int
		wantUser     string
	}{
		{"valid", "Bearer good", http.StatusOK, "user-1"},
		{"scheme is case-insensitive", "bearer good", http.StatusOK, "user-1"},
		{"missing header", "", http.StatusUnauthorized, ""},
		{"wrong scheme", "Basic good", http.StatusUnauthorized, ""},
		{"empty token", "Bearer ", http.StatusUnauthorized, ""},
		{"bad token", "Bearer nope", http.StatusUnauthorized, ""},
		{"oversized token", "Bearer " + strings.Repeat("a", maxTokenLen+1), http.StatusUnauthorized, ""},
		{"verifier cannot check (not a 401)", "Bearer jwks-down", http.StatusServiceUnavailable, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser = ""
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if gotUser != tt.wantUser {
				t.Errorf("user = %q, want %q", gotUser, tt.wantUser)
			}
			if tt.wantStatus == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Error("missing WWW-Authenticate header")
			}
			if tt.wantStatus != http.StatusOK {
				if got := rec.Header().Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
			}
		})
	}
}

func TestAuthenticateSetsTheSignInTime(t *testing.T) {
	var gotTime time.Time
	var gotOK bool
	h := Authenticate(fakeVerifier{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTime, gotOK = AuthTime(r.Context())
	}))

	for _, tt := range []struct {
		token  string
		wantOK bool
	}{
		{"good", true},
		{"no-auth-time", false}, // the user is known, but when they signed in is not
	} {
		gotTime, gotOK = time.Time{}, false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tt.token)
		h.ServeHTTP(httptest.NewRecorder(), req)

		if gotOK != tt.wantOK {
			t.Errorf("%s: AuthTime ok = %v, want %v", tt.token, gotOK, tt.wantOK)
		}
		if tt.wantOK && !gotTime.Equal(signedIn) {
			t.Errorf("%s: AuthTime = %v, want %v", tt.token, gotTime, signedIn)
		}
	}
}
