package domain

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	maxRootCauseLen      = 300 // characters
	maxPriorityReasonLen = 200 // characters
)

// ClassificationResult is the answer to step 1: what kind of error this is
// and why it happened.
type ClassificationResult struct {
	Category         ErrorCategory `json:"category"`
	RootCauseSummary string        `json:"root_cause_summary"`
	Confidence       float64       `json:"confidence"`
	NeedsHumanReview bool          `json:"needs_human_review"`
}

// Validate checks enum membership, length and range.
func (r ClassificationResult) Validate() error {
	var errs []error
	if !r.Category.Valid() {
		errs = append(errs, fmt.Errorf("invalid category %q, expected one of %v", r.Category, errorCategories))
	}
	errs = append(errs,
		checkMaxLen("root_cause_summary", r.RootCauseSummary, maxRootCauseLen),
		checkConfidence(r.Confidence),
	)
	return errors.Join(errs...)
}

// PriorityResult is the answer to step 2: how urgent the incident is.
type PriorityResult struct {
	Priority          Priority `json:"priority"`
	PriorityReasoning string   `json:"priority_reasoning"`
}

// Validate checks enum membership and length.
func (r PriorityResult) Validate() error {
	var errs []error
	if !r.Priority.Valid() {
		errs = append(errs, fmt.Errorf("invalid priority %q, expected one of %v", r.Priority, priorities))
	}
	errs = append(errs, checkMaxLen("priority_reasoning", r.PriorityReasoning, maxPriorityReasonLen))
	return errors.Join(errs...)
}

// IncidentAnalysis is the full analysis of an incident: a classification plus
// a priority. With task decomposition it is assembled from the two steps; in
// single-call mode the model returns it directly. Embedding keeps the JSON
// flat (all six fields side by side), which is the API's response shape.
type IncidentAnalysis struct {
	ClassificationResult
	PriorityResult
}

// Validate checks both halves. It is declared explicitly because both embedded
// types have a Validate method, so the promoted name would be ambiguous.
func (a IncidentAnalysis) Validate() error {
	return errors.Join(a.ClassificationResult.Validate(), a.PriorityResult.Validate())
}

// ParseClassificationResult decodes and validates the model's step-1 JSON.
// Unknown keys are ignored; every known key is required.
func ParseClassificationResult(data []byte) (ClassificationResult, error) {
	var r ClassificationResult
	if err := decodeRequired(data, &r, "category", "root_cause_summary", "confidence", "needs_human_review"); err != nil {
		return ClassificationResult{}, err
	}
	if err := r.Validate(); err != nil {
		return ClassificationResult{}, err
	}
	return r, nil
}

// ParsePriorityResult decodes and validates the model's step-2 JSON.
func ParsePriorityResult(data []byte) (PriorityResult, error) {
	var r PriorityResult
	if err := decodeRequired(data, &r, "priority", "priority_reasoning"); err != nil {
		return PriorityResult{}, err
	}
	if err := r.Validate(); err != nil {
		return PriorityResult{}, err
	}
	return r, nil
}

// ParseIncidentAnalysis decodes and validates a single-call answer, which
// carries both halves in one object. Problems in either half are reported
// together.
func ParseIncidentAnalysis(data []byte) (IncidentAnalysis, error) {
	c, cerr := ParseClassificationResult(data)
	p, perr := ParsePriorityResult(data)
	if err := errors.Join(cerr, perr); err != nil {
		return IncidentAnalysis{}, err
	}
	return IncidentAnalysis{ClassificationResult: c, PriorityResult: p}, nil
}

// Reply is the set of types a model's JSON reply can be decoded into: one per
// pipeline step (classify, prioritize) and the combined single-call answer.
type Reply interface {
	ClassificationResult | PriorityResult | IncidentAnalysis
}

// ParseReply decodes and validates a model reply as T, using the matching
// Parse function. It lets generic code ask for a result by type alone, without
// being handed a decoder.
func ParseReply[T Reply](data []byte) (T, error) {
	var out T
	var err error
	switch p := any(&out).(type) {
	case *ClassificationResult:
		*p, err = ParseClassificationResult(data)
	case *PriorityResult:
		*p, err = ParsePriorityResult(data)
	case *IncidentAnalysis:
		*p, err = ParseIncidentAnalysis(data)
	}
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// checkMaxLen counts characters (runes), not bytes.
func checkMaxLen(field, s string, limit int) error {
	if n := utf8.RuneCountInString(s); n > limit {
		return fmt.Errorf("%s is %d characters, max is %d", field, n, limit)
	}
	return nil
}

func checkConfidence(c float64) error {
	if !(c >= 0 && c <= 1) { // written this way so NaN is rejected too
		return fmt.Errorf("confidence %v is outside [0, 1]", c)
	}
	return nil
}
