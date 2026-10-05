// Package storetest gives tests in other packages a real, freshly migrated
// store on a private schema, so they can exercise the whole stack against
// Postgres without touching each other's data.
package storetest

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/store"
)

// New returns a Store whose connections are confined to a new schema, with all
// migrations applied; the schema is dropped when the test ends. The test is
// skipped unless TEST_DATABASE_URL (a postgres:// URL) is set.
func New(t testing.TB) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database test")
	}
	ctx := context.Background()
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL must be a postgres:// URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema) // any unknown URL parameter is sent to the server as a setting
	u.RawQuery = q.Encode()

	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}
