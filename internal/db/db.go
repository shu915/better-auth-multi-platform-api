// Package db opens the Postgres connection pool.
package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// MaxConns caps the pool. Several tasks share one database, so stay well under its limit.
	MaxConns = 10
	// StatementTimeout is how long Postgres lets one statement run before cancelling it.
	StatementTimeout = 5 * time.Second
)

// RequireTLS rejects a URL that would let the connection fall back to plaintext:
// sslmode=disable and allow, and also prefer, which is pgx's default when sslmode is omitted.
// require or stronger passes (require encrypts but does not verify the server certificate;
// verify-full also does). The error never echoes the URL, which holds the password.
func RequireTLS(databaseURL string) error {
	cfg, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	if cfg.TLSConfig == nil || len(cfg.Fallbacks) > 0 {
		return errors.New("DATABASE_URL must set sslmode=require or stronger")
	}
	return nil
}

// Open connects to Postgres and verifies the connection, so a wrong URL or an unreachable
// database fails at startup instead of on the first request. The caller must Close the pool.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx's parse errors can echo the URL; report only that it is invalid, never the password.
		return nil, fmt.Errorf("invalid DATABASE_URL")
	}
	// Bounds, so a slow query or a burst of requests cannot exhaust the database: a cap on
	// connections and a server-side limit on how long one statement may run.
	cfg.MaxConns = MaxConns
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(int(StatementTimeout / time.Millisecond))
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}
