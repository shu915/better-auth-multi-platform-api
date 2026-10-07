// Package profile reads and writes the profiles table. It does not depend on net/http.
package profile

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// Upsert sets the bio of userID, creating the row on first write, and returns the stored profile.
// It is idempotent: repeating the same call leaves the same state. A concurrent first write by
// the same user is safe because the insert is a single ON CONFLICT statement.
func (s *Store) Upsert(ctx context.Context, userID, bio string) (Profile, error) {
	if !utf8.ValidString(bio) || utf8.RuneCountInString(bio) > MaxBioLength || strings.ContainsRune(bio, 0) {
		return Profile{}, ErrInvalidBio
	}
	p := Profile{UserID: userID}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO profiles (user_id, bio) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET bio = EXCLUDED.bio, updated_at = now()
		RETURNING bio`, userID, bio).Scan(&p.Bio)
	if err != nil {
		return Profile{}, fmt.Errorf("upsert profile: %w", err)
	}
	return p, nil
}
