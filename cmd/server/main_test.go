package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shu915/better-auth-multi-platform-api/internal/auth"
	"github.com/shu915/better-auth-multi-platform-api/internal/profile"
)

var testOrigins = []string{"http://localhost:3000"}

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, token string) (string, error) {
	if token == "good" {
		return "user-1", nil
	}
	return "", auth.ErrInvalidToken
}

func passThrough(next http.Handler) http.Handler { return next }

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux(passThrough, fakeProfiles{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got, want := rec.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestHealthRejectsPost(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux(passThrough, fakeProfiles{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// These go through the real middleware chain to prove /me is actually protected.
func TestMeRequiresAuth(t *testing.T) {
	h := handler(fakeVerifier{}, testOrigins, fakeProfiles{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer forged")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged token: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "{\"user_id\":\"user-1\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestHealthNeedsNoAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(fakeVerifier{}, testOrigins, fakeProfiles{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestPreflightToProtectedRouteSucceeds(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/me", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	handler(fakeVerifier{}, testOrigins, fakeProfiles{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204 (it carries no credentials)", rec.Code)
	}
}

func TestLoadConfigDefaultsInDevelopment(t *testing.T) {
	cfg, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.Auth.Issuer != "http://localhost:3000" ||
		cfg.Auth.JWKSURL != "http://localhost:3000/api/auth/jwks" ||
		cfg.DatabaseURL != "postgres://postgres:postgres@localhost:5432/app?sslmode=disable" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfigProductionRequiresExplicitSettings(t *testing.T) {
	full := map[string]string{
		"APP_ENV":              "production",
		"AUTH_ISSUER":          "https://web.example.com",
		"AUTH_AUDIENCE":        "api",
		"CORS_ALLOWED_ORIGINS": "https://web.example.com",
		"DATABASE_URL":         "postgres://app:secret@db.example.com:5432/app?sslmode=require",
	}
	lookup := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	if _, err := loadConfig(lookup(full)); err != nil {
		t.Fatalf("complete production config rejected: %v", err)
	}
	for _, missing := range []string{"AUTH_ISSUER", "AUTH_AUDIENCE", "CORS_ALLOWED_ORIGINS", "DATABASE_URL"} {
		m := map[string]string{}
		for k, v := range full {
			if k != missing {
				m[k] = v
			}
		}
		if _, err := loadConfig(lookup(m)); err == nil {
			t.Errorf("production without %s was accepted", missing)
		}
	}
}

func TestLoadConfigProductionRejectsUnsafeValues(t *testing.T) {
	base := map[string]string{
		"APP_ENV":              "production",
		"AUTH_ISSUER":          "https://web.example.com",
		"AUTH_AUDIENCE":        "api",
		"CORS_ALLOWED_ORIGINS": "https://web.example.com",
		"DATABASE_URL":         "postgres://app:secret@db.example.com:5432/app?sslmode=require",
	}
	for name, override := range map[string]map[string]string{
		"DB sslmode=disable":    {"DATABASE_URL": "postgres://app:secret@db.example.com/app?sslmode=disable"},
		"DB sslmode=prefer":     {"DATABASE_URL": "postgres://app:secret@db.example.com/app?sslmode=prefer"},
		"DB sslmode omitted":    {"DATABASE_URL": "postgres://app:secret@db.example.com/app"},
		"JWKS over http":        {"AUTH_JWKS_URL": "http://localhost:3000/api/auth/jwks"},
		"issuer over http":      {"AUTH_ISSUER": "http://web.example.com"},
		"issuer trailing slash": {"AUTH_ISSUER": "https://web.example.com/"},
		"CORS lists no origin":  {"CORS_ALLOWED_ORIGINS": ","},
		"CORS only whitespace":  {"CORS_ALLOWED_ORIGINS": " , "},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range base {
				env[k] = v
			}
			for k, v := range override {
				env[k] = v
			}
			if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadConfigDevelopmentAllowsPlainDatabase(t *testing.T) {
	if _, err := loadConfig(func(string) string { return "" }); err != nil {
		t.Fatalf("development defaults (sslmode=disable) rejected: %v", err)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"APP_ENV typo":  {"APP_ENV": "prod"},
		"APP_ENV case":  {"APP_ENV": "Production"},
		"PORT not num":  {"PORT": "abc"},
		"PORT too big":  {"PORT": "70000"},
		"PORT zero":     {"PORT": "0"},
		"PORT negative": {"PORT": "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// panickyProfiles panics on Get, and notes whether the request carried a deadline.
type panickyProfiles struct {
	fakeProfiles
	sawDeadline *bool
}

func (p panickyProfiles) Get(ctx context.Context, _ string) (profile.Profile, error) {
	_, ok := ctx.Deadline()
	*p.sawDeadline = ok
	panic("store exploded")
}

func TestHandlerRecoversFromAPanicAndGivesRequestsADeadline(t *testing.T) {
	var sawDeadline bool
	store := panickyProfiles{sawDeadline: &sawDeadline}

	rec := getProfileAs(t, store.fakeProfiles, "good", "/me/profile") // sanity: the plain fake works
	if rec.Code != http.StatusOK {
		t.Fatalf("sanity: status = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/me/profile", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec = httptest.NewRecorder()
	handler(fakeVerifier{}, testOrigins, store).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic in the store: status = %d, want 500", rec.Code)
	}
	if !sawDeadline {
		t.Error("the request context given to the store has no deadline")
	}
}
