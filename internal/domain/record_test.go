package domain

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

// The API contract: every key is always present (nulls included), snake_case.
func TestIncidentRecordJSONShape(t *testing.T) {
	b, err := json.Marshal(IncidentRecord{OccurrenceCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	got := slices.Sorted(maps.Keys(m))
	want := []string{
		"analysis", "duplicate_of_id", "environment", "id", "is_duplicate", "llm_latency_ms",
		"llm_retry_count", "occurrence_count", "raw_text_hash", "service", "timestamp",
	}
	if !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if m["id"] != nil || m["duplicate_of_id"] != nil || m["llm_latency_ms"] != nil {
		t.Errorf("unset nullable fields should encode as null: %v", m)
	}
}
