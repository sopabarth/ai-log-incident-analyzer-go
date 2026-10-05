package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/sopabarth/ai-log-incident-analyzer-go/migrations"
)

// MigrationStatus is one migration and whether it has been applied.
type MigrationStatus struct {
	Name    string // file name, e.g. 00001_create_incidents.sql
	Applied bool
}

// Migrate applies every pending migration and returns the names of the ones it
// applied. It is safe to run from several instances at once: goose takes a
// Postgres advisory lock for the duration.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	p, closeDB, err := s.provider()
	if err != nil {
		return nil, err
	}
	defer closeDB()

	results, err := p.Up(ctx)
	names := make([]string, 0, len(results))
	for _, r := range results {
		names = append(names, r.Source.Path)
	}
	if err != nil {
		return names, fmt.Errorf("apply migrations: %w", err)
	}
	return names, nil
}

// RollbackOne reverts the most recently applied migration and returns its name.
func (s *Store) RollbackOne(ctx context.Context) (string, error) {
	p, closeDB, err := s.provider()
	if err != nil {
		return "", err
	}
	defer closeDB()

	res, err := p.Down(ctx)
	if err != nil {
		return "", fmt.Errorf("roll back migration: %w", err)
	}
	return res.Source.Path, nil
}

// MigrationStatuses lists all known migrations, oldest first.
func (s *Store) MigrationStatuses(ctx context.Context) ([]MigrationStatus, error) {
	p, closeDB, err := s.provider()
	if err != nil {
		return nil, err
	}
	defer closeDB()

	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read migration status: %w", err)
	}
	out := make([]MigrationStatus, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, MigrationStatus{Name: st.Source.Path, Applied: st.State == goose.StateApplied})
	}
	return out, nil
}

// provider builds a goose provider on top of the store's own pool, so
// migrations use the same connection settings as everything else. The
// returned func releases the database/sql wrapper (not the pool).
func (s *Store) provider() (*goose.Provider, func(), error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, nil, fmt.Errorf("set up migration lock: %w", err)
	}
	db := stdlib.OpenDBFromPool(s.pool)
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("set up migrations: %w", err)
	}
	return p, func() { _ = db.Close() }, nil
}
