package domain

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestParseClassificationResult(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		got, err := ParseClassificationResult([]byte(
			`{"category":"auth_failure","root_cause_summary":"keys rotated","confidence":0.9,"needs_human_review":false}`))
		if err != nil {
			t.Fatal(err)
		}
		want := ClassificationResult{CategoryAuthFailure, "keys rotated", 0.9, false}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("extra keys are ignored", func(t *testing.T) {
		body := `{"category":"unknown","root_cause_summary":"x","confidence":0,"needs_human_review":true,"extra":1}`
		if _, err := ParseClassificationResult([]byte(body)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	long := func(n int) string { return strings.Repeat("a", n) }
	bad := map[string]string{
		"not json":            `Sure! Here is the JSON: {`,
		"unknown category":    `{"category":"made_up","root_cause_summary":"x","confidence":0.5,"needs_human_review":false}`,
		"missing confidence":  `{"category":"unknown","root_cause_summary":"x","needs_human_review":false}`,
		"missing review flag": `{"category":"unknown","root_cause_summary":"x","confidence":0.5}`,
		"null category":       `{"category":null,"root_cause_summary":"x","confidence":0.5,"needs_human_review":false}`,
		"confidence > 1":      `{"category":"unknown","root_cause_summary":"x","confidence":1.5,"needs_human_review":false}`,
		"confidence < 0":      `{"category":"unknown","root_cause_summary":"x","confidence":-0.1,"needs_human_review":false}`,
		"summary too long":    `{"category":"unknown","root_cause_summary":"` + long(301) + `","confidence":0.5,"needs_human_review":false}`,
		"confidence a string": `{"category":"unknown","root_cause_summary":"x","confidence":"high","needs_human_review":false}`,
	}
	for name, body := range bad {
		t.Run("rejects "+name, func(t *testing.T) {
			if got, err := ParseClassificationResult([]byte(body)); err == nil {
				t.Errorf("expected an error, got %+v", got)
			}
		})
	}

	t.Run("length limit counts characters, not bytes", func(t *testing.T) {
		build := func(n int) []byte {
			return []byte(`{"category":"unknown","root_cause_summary":"` + strings.Repeat("š", n) +
				`","confidence":0.5,"needs_human_review":false}`)
		}
		if _, err := ParseClassificationResult(build(300)); err != nil { // 600 bytes
			t.Errorf("300 characters should be allowed: %v", err)
		}
		if _, err := ParseClassificationResult(build(301)); err == nil {
			t.Error("301 characters should be rejected")
		}
	})

	t.Run("one error names every problem", func(t *testing.T) {
		body := `{"category":"made_up","root_cause_summary":"` + long(301) + `","confidence":2,"needs_human_review":true}`
		_, err := ParseClassificationResult([]byte(body))
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"category", "root_cause_summary", "confidence"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
	})

	t.Run("missing fields are all listed", func(t *testing.T) {
		_, err := ParseClassificationResult([]byte(`{"category":"unknown"}`))
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"root_cause_summary", "confidence", "needs_human_review"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
	})
}

func TestParsePriorityResult(t *testing.T) {
	got, err := ParsePriorityResult([]byte(`{"priority":"high","priority_reasoning":"checkout is down"}`))
	if err != nil || got != (PriorityResult{PriorityHigh, "checkout is down"}) {
		t.Errorf("got (%+v, %v)", got, err)
	}

	bad := map[string]string{
		"unknown priority": `{"priority":"urgent","priority_reasoning":"x"}`,
		"missing reason":   `{"priority":"low"}`,
		"reason too long":  `{"priority":"low","priority_reasoning":"` + strings.Repeat("a", 201) + `"}`,
	}
	for name, body := range bad {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := ParsePriorityResult([]byte(body)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestParseIncidentAnalysis(t *testing.T) {
	body := `{"category":"database_timeout","root_cause_summary":"pool exhausted","priority":"critical",
		"priority_reasoning":"payments blocked","confidence":0.95,"needs_human_review":false}`
	got, err := ParseIncidentAnalysis([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := IncidentAnalysis{
		ClassificationResult{CategoryDatabaseTimeout, "pool exhausted", 0.95, false},
		PriorityResult{PriorityCritical, "payments blocked"},
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Promoted fields read like a flat struct.
	if got.Category != CategoryDatabaseTimeout || got.Priority != PriorityCritical {
		t.Errorf("promoted fields wrong: %+v", got)
	}

	t.Run("a partial answer is rejected", func(t *testing.T) {
		if _, err := ParseIncidentAnalysis([]byte(`{"category":"database_timeout"}`)); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("problems in both halves are reported together", func(t *testing.T) {
		_, err := ParseIncidentAnalysis([]byte(
			`{"category":"made_up","root_cause_summary":"x","priority":"urgent","priority_reasoning":"x","confidence":0.5,"needs_human_review":false}`))
		if err == nil || !strings.Contains(err.Error(), "category") || !strings.Contains(err.Error(), "priority") {
			t.Errorf("error should mention both category and priority, got %v", err)
		}
	})
}

// The API response embeds the analysis, so its JSON must stay flat: exactly
// these six keys, no nesting from the embedded structs.
func TestIncidentAnalysisJSONIsFlat(t *testing.T) {
	a := IncidentAnalysis{
		ClassificationResult{CategoryUnknown, "x", 0.5, true},
		PriorityResult{PriorityLow, "y"},
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	got := slices.Sorted(maps.Keys(m))
	want := []string{"category", "confidence", "needs_human_review", "priority", "priority_reasoning", "root_cause_summary"}
	if !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}

	var back IncidentAnalysis
	if err := json.Unmarshal(b, &back); err != nil || back != a {
		t.Errorf("round trip failed: got %+v, err %v", back, err)
	}
}
