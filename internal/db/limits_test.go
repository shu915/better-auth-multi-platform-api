package db_test

import (
	"context"
	"testing"

	"github.com/shu915/better-auth-multi-platform-api/internal/db"
	"github.com/shu915/better-auth-multi-platform-api/internal/testdb"
)

func TestPoolIsBounded(t *testing.T) {
	pool := testdb.Pool(t)

	if got := pool.Config().MaxConns; got != db.MaxConns {
		t.Errorf("MaxConns = %d, want %d", got, db.MaxConns)
	}
	var timeout string
	if err := pool.QueryRow(context.Background(), `SHOW statement_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != "5s" {
		t.Errorf("statement_timeout = %q, want 5s", timeout)
	}
}
