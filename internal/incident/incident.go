// Package incident is the service's core flow: given a raw log entry, either
// recognise it as a repeat of an incident already analysed, or have the model
// analyse it and store the result. It knows nothing about HTTP.
package incident

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/llm"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/parser"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/store"
)

// Store is the persistence the Service needs. *store.Store implements it.
type Store interface {
	RecordDuplicate(ctx context.Context, rawTextHash string, window time.Duration) (domain.IncidentRecord, error)
	Create(ctx context.Context, in store.NewIncident) (domain.IncidentRecord, error)
}

// Analyzer is the model-backed analysis the Service needs. *llm.Analyzer
// implements it.
type Analyzer interface {
	Analyze(ctx context.Context, service string, env domain.Environment, normalizedText string) (llm.Result, error)
}

var (
	// ErrEmptyText means the log had nothing in it once normalized.
	ErrEmptyText = errors.New("raw_text has no usable content")
	// ErrAnalysis means the model step failed in a way retrying could not fix
	// (a rejected API key, a bad request...). Callers should treat it as an
	// upstream failure, not as bad input.
	ErrAnalysis = errors.New("analysis failed")
)

// Service analyses incidents.
type Service struct {
	store    Store
	analyzer Analyzer
	window   time.Duration
}

// NewService returns a Service. A repeat of the same error within window of it
// last being seen is counted against the existing incident instead of being
// analysed again.
func NewService(s Store, a Analyzer, window time.Duration) *Service {
	return &Service{store: s, analyzer: a, window: window}
}

// Analyze returns the incident for a log entry.
//
// The error text is normalized and hashed first, and that hash is looked up
// before the model is ever called: a burst of the same error costs one model
// call, not hundreds. A repeat returns the stored incident with its occurrence
// count bumped and is_duplicate set; anything new is analysed, saved and
// returned.
func (s *Service) Analyze(ctx context.Context, entry domain.RawLogEntry) (domain.IncidentRecord, error) {
	normalized := parser.NormalizeRawText(entry.RawText, parser.DefaultMaxFrames)
	if normalized == "" {
		return domain.IncidentRecord{}, ErrEmptyText
	}
	hash := parser.ComputeErrorHash(normalized)

	rec, err := s.store.RecordDuplicate(ctx, hash, s.window)
	if err == nil {
		return rec, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.IncidentRecord{}, fmt.Errorf("check for duplicate: %w", err)
	}

	res, err := s.analyzer.Analyze(ctx, entry.Service, entry.Environment, normalized)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.IncidentRecord{}, err // the caller gave up; not an analysis failure
		}
		return domain.IncidentRecord{}, fmt.Errorf("%w: %w", ErrAnalysis, err)
	}

	rec, err = s.store.Create(ctx, store.NewIncident{
		Service:       entry.Service,
		Environment:   entry.Environment,
		Timestamp:     entry.Timestamp,
		RawTextHash:   hash,
		Analysis:      res.Analysis,
		LLMRetryCount: res.RetryCount,
		LLMLatencyMS:  int(res.Latency.Milliseconds()),
	})
	if err != nil {
		return domain.IncidentRecord{}, fmt.Errorf("save incident: %w", err)
	}
	return rec, nil
}
