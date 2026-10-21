// Package profile reads and writes the profiles table. It does not depend on net/http.
package profile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means the user has no profile row yet.
var ErrNotFound = errors.New("profile not found")

// MaxBioLength is the longest bio, in characters. It matches the CHECK on profiles.bio.
const MaxBioLength = 1000

// ErrInvalidBio means the bio cannot be stored: too long, invalid UTF-8, or it contains a NUL
// byte (Postgres text columns reject both).
var ErrInvalidBio = errors.New("invalid bio")

// ErrAccountDeleted means the user deleted their account a moment ago, so nothing may be written
// for them: the token that asked was issued before the deletion and has not expired yet.
var ErrAccountDeleted = errors.New("account deleted")

// DeletionMemory is how long a deletion is remembered (table deleted_users). It must outlast the
// longest JWT the web app issues (5 minutes) plus clock skew; it is not longer than needed because
// a user whose deletion stopped half way (the Go data gone, the user still there) is refused until
// it passes.
const DeletionMemory = 10 * time.Minute

// Profile is the app-owned part of a user. The name and image live in the web app's database.
type Profile struct {
	UserID string
	Bio    string
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Get returns the profile of userID, or ErrNotFound.
func (s *Store) Get(ctx context.Context, userID string) (Profile, error) {
	p := Profile{UserID: userID}
	err := s.pool.QueryRow(ctx, `SELECT bio FROM profiles WHERE user_id = $1`, userID).Scan(&p.Bio)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("get profile: %w", err)
	}
	return p, nil
}

// Delete removes the profile row of userID and remembers the deletion for DeletionMemory, so a
// JWT issued before it cannot recreate the row (see Upsert). It is idempotent: a missing row is
// not an error, so the web app can safely retry an account deletion.
//
// profiles is the root of a user's data (see package userdata): rows in other tables go with it,
// stay, or lose their author as each table's foreign key says, so this is the only DELETE needed.
func (s *Store) Delete(ctx context.Context, userID string) error {
	err := s.inTx(ctx, userID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO deleted_users (user_id) VALUES ($1)
			ON CONFLICT (user_id) DO UPDATE SET deleted_at = now()`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM profiles WHERE user_id = $1`, userID); err != nil {
			return err
		}
		return forgetOldDeletions(ctx, tx)
	})
	if err != nil {
		return fmt.Errorf("delete profile: %w", err)
	}
	return nil
}

// forgetOldDeletions drops the records older than DeletionMemory. No job runs for this: every
// deletion and every write tidies up, so a record does not outlive its purpose by more than the
// time to the next request.
// SKIP LOCKED: two requests at the same moment must not wait on each other's rows (the lock taken
// in inTx is per user, not for this clean-up).
func forgetOldDeletions(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM deleted_users WHERE user_id IN (
			SELECT user_id FROM deleted_users
			WHERE deleted_at < now() - make_interval(secs => $1)
			ORDER BY user_id FOR UPDATE SKIP LOCKED)`, DeletionMemory.Seconds())
	return err
}

// inTx runs fn in a transaction that holds a lock for userID, so a deletion and a write for the
// same user cannot interleave (a write that checked "not deleted" just before the deletion
// committed would otherwise recreate the row after it).
func (s *Store) inTx(ctx context.Context, userID string, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no-op after Commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Upsert sets the bio of userID, creating the row on first write, and returns the stored profile.
// It is idempotent: repeating the same call leaves the same state. It refuses with
// ErrAccountDeleted when the user deleted their account within DeletionMemory.
func (s *Store) Upsert(ctx context.Context, userID, bio string) (Profile, error) {
	if !utf8.ValidString(bio) || utf8.RuneCountInString(bio) > MaxBioLength || strings.ContainsRune(bio, 0) {
		return Profile{}, ErrInvalidBio
	}
	p := Profile{UserID: userID}
	err := s.inTx(ctx, userID, func(tx pgx.Tx) error {
		var deleted bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM deleted_users
			               WHERE user_id = $1 AND deleted_at > now() - make_interval(secs => $2))`,
			userID, DeletionMemory.Seconds()).Scan(&deleted); err != nil {
			return err
		}
		if err := forgetOldDeletions(ctx, tx); err != nil {
			return err
		}
		if deleted {
			return ErrAccountDeleted
		}
		return tx.QueryRow(ctx, `
			INSERT INTO profiles (user_id, bio) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET bio = EXCLUDED.bio, updated_at = now()
			RETURNING bio`, userID, bio).Scan(&p.Bio)
	})
	if errors.Is(err, ErrAccountDeleted) {
		return Profile{}, ErrAccountDeleted
	}
	if err != nil {
		return Profile{}, fmt.Errorf("upsert profile: %w", err)
	}
	return p, nil
}
