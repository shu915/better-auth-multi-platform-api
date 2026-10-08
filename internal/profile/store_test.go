package profile

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

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

func TestUpsert(t *testing.T) {
	s, ctx := newTestStore(t)
	const userID = "test-user-upsert"
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, userID) })

	// first write creates the row
	got, err := s.Upsert(ctx, userID, "first")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Profile{UserID: userID, Bio: "first"}) {
		t.Errorf("created: got %+v", got)
	}

	// second write updates it and moves updated_at forward, but not created_at
	var created1, updated1 time.Time
	if err := s.pool.QueryRow(ctx, `SELECT created_at, updated_at FROM profiles WHERE user_id = $1`, userID).Scan(&created1, &updated1); err != nil {
		t.Fatal(err)
	}
	// push updated_at into the past so the comparison does not depend on clock resolution
	if _, err := s.pool.Exec(ctx, `UPDATE profiles SET updated_at = updated_at - interval '1 hour' WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	updated1 = updated1.Add(-time.Hour)
	if got, err = s.Upsert(ctx, userID, "second"); err != nil {
		t.Fatal(err)
	}
	if got.Bio != "second" {
		t.Errorf("updated: got %+v", got)
	}
	var created2, updated2 time.Time
	if err := s.pool.QueryRow(ctx, `SELECT created_at, updated_at FROM profiles WHERE user_id = $1`, userID).Scan(&created2, &updated2); err != nil {
		t.Fatal(err)
	}
	if !created2.Equal(created1) {
		t.Errorf("created_at changed: %v -> %v", created1, created2)
	}
	if !updated2.After(updated1) {
		t.Errorf("updated_at not moved forward: %v -> %v", updated1, updated2)
	}

	// repeating the same write is harmless
	if got, err = s.Upsert(ctx, userID, "second"); err != nil || got.Bio != "second" {
		t.Errorf("repeat: got %+v, err %v", got, err)
	}
}

func TestUpsertRejectsInvalidBio(t *testing.T) {
	s, ctx := newTestStore(t)
	const userID = "test-user-upsert-invalid"
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, userID) })

	for name, bio := range map[string]string{
		"too long":      strings.Repeat("a", MaxBioLength+1),
		"NUL byte":      "a\x00b",
		"invalid UTF-8": "a\xffb",
	} {
		if _, err := s.Upsert(ctx, userID, bio); !errors.Is(err, ErrInvalidBio) {
			t.Errorf("%s: err = %v, want ErrInvalidBio", name, err)
		}
	}
	if _, err := s.Get(ctx, userID); !errors.Is(err, ErrNotFound) {
		t.Errorf("invalid input created a row: err = %v", err)
	}
	if _, err := s.Upsert(ctx, userID, strings.Repeat("あ", MaxBioLength)); err != nil {
		t.Errorf("max length bio (in characters) rejected: %v", err)
	}
}

func TestDelete(t *testing.T) {
	s, ctx := newTestStore(t)
	const userID, otherID = "test-user-delete", "test-user-delete-other"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = ANY($1)`, []string{userID, otherID})
	})
	if _, err := s.Upsert(ctx, userID, "mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(ctx, otherID, "theirs"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, userID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, userID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v, want ErrNotFound", err)
	}
	// deleting again, or a user who never had a row, is not an error (the web app retries)
	if err := s.Delete(ctx, userID); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if err := s.Delete(ctx, "test-user-never-existed"); err != nil {
		t.Errorf("delete without a row: %v", err)
	}
	// only the caller's row goes
	if got, err := s.Get(ctx, otherID); err != nil || got.Bio != "theirs" {
		t.Errorf("another user's profile: got %+v, err %v", got, err)
	}
}
