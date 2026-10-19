package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequireRecentAuth(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	const maxAge = 5 * time.Minute

	tests := []struct {
		name     string
		authTime *time.Time // nil: the token had no auth_time
		want     int
	}{
		{"just signed in", ptr(now), http.StatusOK},
		{"a minute ago", ptr(now.Add(-time.Minute)), http.StatusOK},
		{"exactly at the limit", ptr(now.Add(-maxAge)), http.StatusOK},
		{"one second over the limit", ptr(now.Add(-maxAge - time.Second)), http.StatusForbidden},
		{"an hour ago", ptr(now.Add(-time.Hour)), http.StatusForbidden},
		{"no auth_time: not knowing is not recent", nil, http.StatusForbidden},
		{"a little ahead (clock skew)", ptr(now.Add(10 * time.Second)), http.StatusOK},
		{"far in the future cannot be true", ptr(now.Add(time.Hour)), http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			h := requireRecentAuth(maxAge, func() time.Time { return now })(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }),
			)
			req := httptest.NewRequest(http.MethodDelete, "/", nil)
			if tt.authTime != nil {
				req = req.WithContext(context.WithValue(req.Context(), authTimeKey{}, *tt.authTime))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if reached != (tt.want == http.StatusOK) {
				t.Errorf("handler reached = %v, want %v", reached, tt.want == http.StatusOK)
			}
			if tt.want == http.StatusForbidden {
				if got := rec.Header().Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }
