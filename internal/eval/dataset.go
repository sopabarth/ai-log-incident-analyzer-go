// Package eval scores the analysis pipeline against hand-labeled ground truth:
// it runs every labeled log through an Analyzer, compares what comes back with
// the expected category, priority and review flag, and reports accuracy,
// precision and recall.
//
// It measures the model and the pipeline around it, not the HTTP or database
// layers, so it calls the Analyzer directly and never dedups: every example
// gets a fresh model call, even if two logs happen to normalize alike.
package eval

import (
	"encoding/json"
	"fmt"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

// Example is one labeled log.
type Example struct {
	Service     string
	Environment domain.Environment
	RawText     string
	Notes       string

	ExpectedCategory         domain.ErrorCategory
	ExpectedPriority         domain.Priority
	ExpectedNeedsHumanReview bool
}

// ParseDataset reads a JSON array of labeled examples and checks every label:
// a typo in the ground truth would silently count against the model.
func ParseDataset(data []byte) ([]Example, error) {
	var wire []struct {
		Service                  string `json:"service"`
		Environment              string `json:"environment"`
		RawText                  string `json:"raw_text"`
		Notes                    string `json:"notes"`
		ExpectedCategory         string `json:"expected_category"`
		ExpectedPriority         string `json:"expected_priority"`
		ExpectedNeedsHumanReview bool   `json:"expected_needs_human_review"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("parse dataset: %w", err)
	}

	examples := make([]Example, 0, len(wire))
	for i, w := range wire {
		env, ok := domain.ParseEnvironment(w.Environment)
		category, priority := domain.ErrorCategory(w.ExpectedCategory), domain.Priority(w.ExpectedPriority)
		switch {
		case !ok:
			return nil, fmt.Errorf("example %d (%s): unknown environment %q", i, w.Service, w.Environment)
		case !category.Valid():
			return nil, fmt.Errorf("example %d (%s): unknown expected_category %q", i, w.Service, w.ExpectedCategory)
		case !priority.Valid():
			return nil, fmt.Errorf("example %d (%s): unknown expected_priority %q", i, w.Service, w.ExpectedPriority)
		}
		examples = append(examples, Example{
			Service: w.Service, Environment: env, RawText: w.RawText, Notes: w.Notes,
			ExpectedCategory: category, ExpectedPriority: priority, ExpectedNeedsHumanReview: w.ExpectedNeedsHumanReview,
		})
	}
	return examples, nil
}
