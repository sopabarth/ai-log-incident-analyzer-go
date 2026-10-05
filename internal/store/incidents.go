package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

// ErrNotFound is returned by RecordDuplicate when no incident with that hash
// was seen within the dedup window.
var ErrNotFound = errors.New("incident not found")

// NewIncident is what is stored after a real LLM analysis.
type NewIncident struct {
	Service       string
	Environment   domain.Environment
	Timestamp     time.Time
	RawTextHash   string
	Analysis      domain.IncidentAnalysis
	LLMRetryCount int
	LLMLatencyMS  int
}

const incidentColumns = `id, service, environment, timestamp, raw_text_hash,
	category, root_cause_summary, priority, priority_reasoning, confidence, needs_human_review,
	occurrence_count, llm_retry_count, llm_latency_ms`

// Create stores a new incident and returns it as saved.
func (s *Store) Create(ctx context.Context, in NewIncident) (domain.IncidentRecord, error) {
	const query = `
		INSERT INTO incidents (
			service, environment, timestamp, raw_text_hash,
			category, root_cause_summary, priority, priority_reasoning, confidence, needs_human_review,
			llm_retry_count, llm_latency_ms
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING ` + incidentColumns

	a := in.Analysis
	rec, err := scanIncident(s.pool.QueryRow(ctx, query,
		in.Service, string(in.Environment), in.Timestamp, in.RawTextHash,
		string(a.Category), a.RootCauseSummary, string(a.Priority), a.PriorityReasoning, a.Confidence, a.NeedsHumanReview,
		in.LLMRetryCount, in.LLMLatencyMS,
	))
	if err != nil {
		return domain.IncidentRecord{}, fmt.Errorf("create incident: %w", err)
	}
	return rec, nil
}

// RecordDuplicate registers one more occurrence of an error that was already
// analysed. If an incident with this hash was last seen within window, it
// counts the occurrence, refreshes last_seen_at and returns that incident,
// marked as a duplicate of itself; otherwise it returns ErrNotFound and the
// caller should analyse the error and Create a new incident.
//
// Finding the incident and bumping it is a single UPDATE, so concurrent
// duplicates each increment the counter exactly once - a read-then-write
// would lose updates under a burst of identical errors.
func (s *Store) RecordDuplicate(ctx context.Context, rawTextHash string, window time.Duration) (domain.IncidentRecord, error) {
	const query = `
		UPDATE incidents
		SET occurrence_count = occurrence_count + 1,
		    last_seen_at     = now()
		WHERE id = (
			SELECT id FROM incidents
			WHERE raw_text_hash = $1
			  AND last_seen_at >= now() - make_interval(secs => $2)
			ORDER BY last_seen_at DESC
			LIMIT 1
		)
		RETURNING ` + incidentColumns

	rec, err := scanIncident(s.pool.QueryRow(ctx, query, rawTextHash, window.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IncidentRecord{}, ErrNotFound
	}
	if err != nil {
		return domain.IncidentRecord{}, fmt.Errorf("record duplicate: %w", err)
	}
	rec.IsDuplicate = true
	rec.DuplicateOfID = rec.ID
	return rec, nil
}

// scanIncident reads one row selected with incidentColumns.
func scanIncident(row pgx.Row) (domain.IncidentRecord, error) {
	var (
		rec domain.IncidentRecord
		id  uuid.UUID
	)
	err := row.Scan(
		&id, &rec.Service, &rec.Environment, &rec.Timestamp, &rec.RawTextHash,
		&rec.Analysis.Category, &rec.Analysis.RootCauseSummary, &rec.Analysis.Priority,
		&rec.Analysis.PriorityReasoning, &rec.Analysis.Confidence, &rec.Analysis.NeedsHumanReview,
		&rec.OccurrenceCount, &rec.LLMRetryCount, &rec.LLMLatencyMS,
	)
	if err != nil {
		return domain.IncidentRecord{}, err
	}
	rec.ID = &id
	rec.Timestamp = rec.Timestamp.UTC()
	return rec, nil
}
