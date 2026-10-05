// Package store persists incidents in Postgres: it creates them, records
// repeat occurrences (the dedup path), and owns the schema migrations.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a handle to the incident database. It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to the database at dsn and verifies the connection. It does
// not touch the schema; see Migrate.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }
