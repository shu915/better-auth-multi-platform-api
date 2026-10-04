package profile

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/shu915/better-auth-multi-platform-api/internal/db"
)

// These tests need a migrated database; set TEST_DATABASE_URL (the dev DB from docker-compose.yml works).
func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool), ctx
}

func TestGet(t *testing.T) {
	s, ctx := newTestStore(t)
	const userID = "test-user-get"
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, userID) })

	if _, err := s.Get(ctx, userID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no row: err = %v, want ErrNotFound", err)
	}

	if _, err := s.pool.Exec(ctx, `INSERT INTO profiles (user_id, bio) VALUES ($1, 'hello')`, userID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Profile{UserID: userID, Bio: "hello"}) {
		t.Errorf("got %+v", got)
	}
}
