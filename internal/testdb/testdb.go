// Package testdb gives integration tests a real Postgres pool. Tests that need the database
// are skipped, not failed, when TEST_DATABASE_URL is not set (the Stop hook and CI set it when a
// database is up), the same rule as the profile store tests.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shu915/better-auth-multi-platform-api/internal/db"
)

// Pool returns a pool on the migrated test database and closes it when the test ends.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
