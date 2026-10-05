package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

// newTestStore returns a Store on a private, freshly migrated schema, dropped
// when the test ends. Tests that need a database are skipped unless
// TEST_DATABASE_URL points at a Postgres, e.g.
//
//	TEST_DATABASE_URL=postgres://root:root@localhost:5433/incident_analyzer?sslmode=disable go test ./...
func newTestStore(t *testing.T, migrate bool) *Store {
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

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{pool: pool}
	t.Cleanup(s.Close)

	if migrate {
		if _, err := s.Migrate(ctx); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	return s
}

func sampleIncident(hash string) NewIncident {
	return NewIncident{
		Service:     "payments-api",
		Environment: domain.EnvProd,
		Timestamp:   time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		RawTextHash: hash,
		Analysis: domain.IncidentAnalysis{
			ClassificationResult: domain.ClassificationResult{
				Category:         domain.CategoryDatabaseTimeout,
				RootCauseSummary: "pool exhausted",
				Confidence:       0.95,
				NeedsHumanReview: false,
			},
			PriorityResult: domain.PriorityResult{
				Priority:          domain.PriorityCritical,
				PriorityReasoning: "payments blocked",
			},
		},
		LLMRetryCount: 1,
		LLMLatencyMS:  870,
	}
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, false)

	applied, err := s.Migrate(ctx)
	if err != nil || len(applied) == 0 {
		t.Fatalf("first Migrate = (%v, %v), want at least one applied migration", applied, err)
	}
	if again, err := s.Migrate(ctx); err != nil || len(again) != 0 {
		t.Errorf("second Migrate = (%v, %v), want nothing to apply", again, err)
	}

	statuses, err := s.MigrationStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range statuses {
		if !st.Applied {
			t.Errorf("migration %s should be applied", st.Name)
		}
	}

	// The pool must survive migrations: Migrate wraps it, it must not close it.
	if _, err := s.Create(ctx, sampleIncident("h")); err != nil {
		t.Fatalf("store unusable after Migrate: %v", err)
	}

	var exists bool
	check := func() bool {
		if err := s.pool.QueryRow(ctx, "SELECT to_regclass('incidents') IS NOT NULL").Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}
	if !check() {
		t.Fatal("incidents table should exist after migrating")
	}
	if _, err := s.RollbackOne(ctx); err != nil {
		t.Fatal(err)
	}
	if check() {
		t.Error("incidents table should be gone after rolling back")
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-migrate after rollback: %v", err)
	}
	if !check() {
		t.Error("incidents table should exist again")
	}
}

func TestCreate(t *testing.T) {
	s := newTestStore(t, true)

	in := sampleIncident("hash-create")
	rec, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	if rec.ID == nil || *rec.ID == uuid.Nil {
		t.Errorf("expected a generated id, got %v", rec.ID)
	}
	if rec.IsDuplicate || rec.DuplicateOfID != nil {
		t.Errorf("a new incident is not a duplicate: %+v", rec)
	}
	if rec.OccurrenceCount != 1 {
		t.Errorf("occurrence_count = %d, want 1", rec.OccurrenceCount)
	}
	if rec.Service != in.Service || rec.Environment != in.Environment || rec.RawTextHash != in.RawTextHash {
		t.Errorf("identity fields not round-tripped: %+v", rec)
	}
	if rec.Analysis != in.Analysis {
		t.Errorf("analysis = %+v, want %+v", rec.Analysis, in.Analysis)
	}
	if !rec.Timestamp.Equal(in.Timestamp) || rec.Timestamp.Location() != time.UTC {
		t.Errorf("timestamp = %v, want %v in UTC", rec.Timestamp, in.Timestamp)
	}
	if rec.LLMRetryCount != 1 || rec.LLMLatencyMS == nil || *rec.LLMLatencyMS != 870 {
		t.Errorf("llm stats not round-tripped: retries=%d latency=%v", rec.LLMRetryCount, rec.LLMLatencyMS)
	}
}

func TestRecordDuplicate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, true)

	if _, err := s.RecordDuplicate(ctx, "never-seen", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash: err = %v, want ErrNotFound", err)
	}

	created, err := s.Create(ctx, sampleIncident("hash-dup"))
	if err != nil {
		t.Fatal(err)
	}

	for want := 2; want <= 4; want++ {
		dup, err := s.RecordDuplicate(ctx, "hash-dup", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if dup.OccurrenceCount != want {
			t.Errorf("occurrence_count = %d, want %d", dup.OccurrenceCount, want)
		}
		if !dup.IsDuplicate || dup.DuplicateOfID == nil || *dup.DuplicateOfID != *created.ID || *dup.ID != *created.ID {
			t.Errorf("duplicate should point at the original incident: %+v", dup)
		}
		if dup.Analysis != created.Analysis {
			t.Errorf("a duplicate reuses the stored analysis, got %+v", dup.Analysis)
		}
	}

	if _, err := s.RecordDuplicate(ctx, "some-other-hash", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("a different hash must not match: err = %v", err)
	}
}

// The window is measured from last_seen_at, and every duplicate refreshes it:
// a steady stream of repeats keeps one incident alive indefinitely, while a
// gap longer than the window starts a fresh incident.
func TestRecordDuplicateWindowSlidesAndExpires(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, true)
	const window = 700 * time.Millisecond

	if _, err := s.Create(ctx, sampleIncident("hash-window")); err != nil {
		t.Fatal(err)
	}

	time.Sleep(400 * time.Millisecond) // 0.4s since created: inside the window
	if _, err := s.RecordDuplicate(ctx, "hash-window", window); err != nil {
		t.Fatalf("0.4s after creation: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // 0.8s since created, but only 0.4s since last seen
	if _, err := s.RecordDuplicate(ctx, "hash-window", window); err != nil {
		t.Fatalf("window should slide with each duplicate: %v", err)
	}
	time.Sleep(900 * time.Millisecond) // longer than the window since last seen
	if _, err := s.RecordDuplicate(ctx, "hash-window", window); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after a gap longer than the window: err = %v, want ErrNotFound", err)
	}
}

func TestRecordDuplicatePicksMostRecentlySeen(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, true)

	older, err := s.Create(ctx, sampleIncident("hash-two"))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := s.Create(ctx, sampleIncident("hash-two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		"UPDATE incidents SET last_seen_at = now() - interval '30 seconds' WHERE id = $1", *older.ID); err != nil {
		t.Fatal(err)
	}

	dup, err := s.RecordDuplicate(ctx, "hash-two", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if *dup.ID != *newer.ID || dup.OccurrenceCount != 2 {
		t.Errorf("expected the most recently seen incident (%v) to be bumped, got %v with count %d", *newer.ID, *dup.ID, dup.OccurrenceCount)
	}
}

// A burst of identical errors must each be counted exactly once.
func TestRecordDuplicateIsAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, true)
	if _, err := s.Create(ctx, sampleIncident("hash-burst")); err != nil {
		t.Fatal(err)
	}

	const burst = 50
	var wg sync.WaitGroup
	errs := make(chan error, burst)
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.RecordDuplicate(ctx, "hash-burst", time.Minute)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := s.pool.QueryRow(ctx, "SELECT occurrence_count FROM incidents WHERE raw_text_hash = 'hash-burst'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if want := 1 + burst; count != want {
		t.Errorf("occurrence_count = %d, want %d (no lost updates)", count, want)
	}
}

// The schema itself rejects bad data, whatever path it arrives by.
func TestSchemaRejectsInvalidValues(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, true)

	insert := func(env, category, priority string, confidence float64) error {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO incidents (service, environment, timestamp, raw_text_hash, category,
				root_cause_summary, priority, priority_reasoning, confidence, needs_human_review)
			VALUES ('svc', $1, now(), 'h', $2, 'x', $3, 'y', $4, false)`,
			env, category, priority, confidence)
		return err
	}

	if err := insert("prod", "unknown", "low", 0.5); err != nil {
		t.Fatalf("a valid row should insert: %v", err)
	}

	tests := []struct {
		name                string
		env, category, prio string
		confidence          float64
		wantCode            string
	}{
		{"unknown environment", "staging_v2", "unknown", "low", 0.5, "22P02"}, // invalid_text_representation (enum)
		{"unknown priority", "prod", "unknown", "urgent", 0.5, "22P02"},
		{"unknown category", "prod", "made_up_category", "low", 0.5, "23514"}, // check_violation
		{"confidence out of range", "prod", "unknown", "low", 1.5, "23514"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := insert(tt.env, tt.category, tt.prio, tt.confidence)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("expected a Postgres error, got %v", err)
			}
			if pgErr.Code != tt.wantCode {
				t.Errorf("error code = %s (%s), want %s", pgErr.Code, pgErr.Message, tt.wantCode)
			}
		})
	}
}
