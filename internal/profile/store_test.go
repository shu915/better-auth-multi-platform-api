package profile

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
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
		_, _ = s.pool.Exec(ctx, `DELETE FROM deleted_users WHERE user_id = ANY($1)`, []string{userID, otherID})
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

// The point of deleted_users: a JWT issued before the deletion must not bring the profile back.
func TestUpsertIsRefusedRightAfterDelete(t *testing.T) {
	s, ctx := newTestStore(t)
	const userID, otherID = "test-user-tombstone", "test-user-tombstone-other"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = ANY($1)`, []string{userID, otherID})
		_, _ = s.pool.Exec(ctx, `DELETE FROM deleted_users WHERE user_id = ANY($1)`, []string{userID, otherID})
	})
	if _, err := s.Upsert(ctx, userID, "mine"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, userID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Upsert(ctx, userID, "again"); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("upsert after delete: err = %v, want ErrAccountDeleted", err)
	}
	if _, err := s.Get(ctx, userID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the refused write created a row: err = %v", err)
	}
	// someone else is not affected
	if _, err := s.Upsert(ctx, otherID, "fine"); err != nil {
		t.Errorf("another user's write refused: %v", err)
	}
}

func TestDeletionIsForgottenAfterDeletionMemory(t *testing.T) {
	s, ctx := newTestStore(t)
	const userID = "test-user-tombstone-expiry"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, userID)
		_, _ = s.pool.Exec(ctx, `DELETE FROM deleted_users WHERE user_id = $1`, userID)
	})
	if err := s.Delete(ctx, userID); err != nil {
		t.Fatal(err)
	}
	// Just inside the memory: still refused. Just outside: allowed (a user whose deletion stopped
	// half way, with the user still there, must not be refused for good).
	setAge := func(age time.Duration) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `UPDATE deleted_users SET deleted_at = now() - make_interval(secs => $2) WHERE user_id = $1`,
			userID, age.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	setAge(DeletionMemory - time.Minute)
	if _, err := s.Upsert(ctx, userID, "x"); !errors.Is(err, ErrAccountDeleted) {
		t.Errorf("inside the memory: err = %v, want ErrAccountDeleted", err)
	}
	setAge(DeletionMemory + time.Minute)
	if _, err := s.Upsert(ctx, userID, "x"); err != nil {
		t.Errorf("after the memory: err = %v, want the write to go through", err)
	}
}

func TestWritesForgetOldRecordsToo(t *testing.T) {
	s, ctx := newTestStore(t)
	const oldID, writerID = "test-user-tombstone-old-w", "test-user-tombstone-writer"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, writerID)
		_, _ = s.pool.Exec(ctx, `DELETE FROM deleted_users WHERE user_id = ANY($1)`, []string{oldID, writerID})
	})
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_users (user_id, deleted_at) VALUES ($1, now() - interval '1 day')
		ON CONFLICT (user_id) DO UPDATE SET deleted_at = EXCLUDED.deleted_at`, oldID); err != nil {
		t.Fatal(err)
	}
	// Nobody deletes an account here: an ordinary write is what tidies up.
	if _, err := s.Upsert(ctx, writerID, "hi"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM deleted_users WHERE user_id = $1`, oldID).Scan(&n); err != nil || n != 0 {
		t.Errorf("an old record is still there: count = %d, err = %v", n, err)
	}
}

func TestDeleteForgetsOldRecords(t *testing.T) {
	s, ctx := newTestStore(t)
	const oldID, newID = "test-user-tombstone-old", "test-user-tombstone-new"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM deleted_users WHERE user_id = ANY($1)`, []string{oldID, newID})
	})
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_users (user_id, deleted_at) VALUES ($1, now() - interval '1 day')
		ON CONFLICT (user_id) DO UPDATE SET deleted_at = EXCLUDED.deleted_at`, oldID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, newID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM deleted_users WHERE user_id = $1`, oldID).Scan(&n); err != nil || n != 0 {
		t.Errorf("an old record is still there: count = %d, err = %v", n, err)
	}
}

// A write racing a deletion must never leave a row behind: either it lands first and the deletion
// removes it, or it comes after and is refused.
func TestWriteRacingADeletionLeavesNoRow(t *testing.T) {
	s, ctx := newTestStore(t)
	const userID = "test-user-tombstone-race"
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, userID)
		_, _ = s.pool.Exec(ctx, `DELETE FROM deleted_users WHERE user_id = $1`, userID)
	})
	for i := 0; i < 20; i++ {
		_, _ = s.pool.Exec(ctx, `DELETE FROM deleted_users WHERE user_id = $1`, userID)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = s.Upsert(ctx, userID, "x") }()
		go func() { defer wg.Done(); _ = s.Delete(ctx, userID) }()
		wg.Wait()
		if _, err := s.Get(ctx, userID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("round %d: a row survived the deletion: err = %v", i, err)
		}
	}
}
