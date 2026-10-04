package db

import (
	"context"
	"strings"
	"testing"
)

func TestOpenRejectsInvalidURLWithoutLeakingIt(t *testing.T) {
	_, err := Open(context.Background(), "postgres://user:s3cret@host:notaport/db")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func TestRequireTLS(t *testing.T) {
	const base = "postgres://u:p@h:5432/d"
	for name, tc := range map[string]struct {
		query string
		ok    bool
	}{
		"disable":                  {"?sslmode=disable", false},
		"allow":                    {"?sslmode=allow", false},
		"prefer falls back":        {"?sslmode=prefer", false},
		"sslmode omitted (prefer)": {"", false},
		"require":                  {"?sslmode=require", true},
		"verify-full":              {"?sslmode=verify-full", true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := RequireTLS(base + tc.query); (err == nil) != tc.ok {
				t.Fatalf("RequireTLS err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestRequireTLSDoesNotLeakPassword(t *testing.T) {
	for _, u := range []string{"postgres://user:s3cret@host:notaport/db", "postgres://user:s3cret@host/db?sslmode=disable"} {
		if err := RequireTLS(u); err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("RequireTLS(%q) err = %v, want an error without the password", u, err)
		}
	}
}

func TestOpenFailsWhenDatabaseUnreachable(t *testing.T) {
	// Port 1 is never a Postgres server; the ping must fail rather than hand back a pool.
	pool, err := Open(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err == nil {
		pool.Close()
		t.Fatal("expected an error")
	}
}
