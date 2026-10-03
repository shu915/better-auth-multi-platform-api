package middleware

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func corsHandler() http.Handler {
	return CORS([]string{"http://localhost:3000"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // proves the request reached the inner handler
	}))
}

func TestCORSPreflightAllowedOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/me", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	rec := httptest.NewRecorder()
	corsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	h := rec.Header()
	if got := h.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := h.Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Errorf("Allow-Headers = %q", got)
	}
	if h.Get("Access-Control-Max-Age") == "" {
		t.Error("missing Max-Age")
	}
}

func TestCORSSimpleRequestAllowedOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	corsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the inner handler's", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	for _, origin := range []string{"http://evil.test", "http://localhost:3000.evil.test", ""} {
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		corsHandler().ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q: Allow-Origin = %q, want none", origin, got)
		}
		if rec.Header().Get("Vary") != "Origin" {
			t.Errorf("origin %q: Vary must still be set", origin)
		}
	}
}

func TestParseOrigins(t *testing.T) {
	got := ParseOrigins(" http://a.test/ , ,http://b.test")
	want := []string{"http://a.test", "http://b.test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
