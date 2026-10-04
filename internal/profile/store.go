// Package profile reads and writes the profiles table. It does not depend on net/http.
package profile

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means the user has no profile row yet.
var ErrNotFound = errors.New("profile not found")

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
