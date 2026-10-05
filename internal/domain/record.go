package domain

import (
	"time"

	"github.com/google/uuid"
)

// IncidentRecord is the response body of POST /analyze-incident: the stored
// incident plus whether this request was a duplicate of an earlier one.
// Nullable values are pointers so they encode as JSON null.
type IncidentRecord struct {
	ID              *uuid.UUID       `json:"id"`
	Service         string           `json:"service"`
	Environment     Environment      `json:"environment"`
	Timestamp       time.Time        `json:"timestamp"`
	RawTextHash     string           `json:"raw_text_hash"`
	Analysis        IncidentAnalysis `json:"analysis"`
	IsDuplicate     bool             `json:"is_duplicate"`
	DuplicateOfID   *uuid.UUID       `json:"duplicate_of_id"`
	OccurrenceCount int              `json:"occurrence_count"`
	LLMRetryCount   int              `json:"llm_retry_count"`
	LLMLatencyMS    *int             `json:"llm_latency_ms"`
}
