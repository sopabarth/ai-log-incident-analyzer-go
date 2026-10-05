// Package llm turns a normalized log into an IncidentAnalysis by asking a chat
// model, and makes that dependable: malformed answers and transient provider
// errors are retried, and a model that keeps failing yields a clearly marked
// fallback result instead of failing the whole request.
//
// The provider sits behind the small Completer interface (Groq is the first
// implementation), so the retry/fallback/decomposition logic here is tested
// with a scripted fake and a different provider can be added without touching
// it.
package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

// Completer sends one system+user prompt pair to a chat model and returns the
// text of its reply, which the prompts ask to be a single JSON object.
//
// Implementations should return *APIError for an HTTP-level failure and let
// network errors through unchanged; the Analyzer uses those to decide what is
// worth retrying.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// ErrMalformedOutput marks a reply that could not be used: not valid JSON, a
// required field missing, a value out of range. It is a one-off bad generation
// rather than a provider problem, so it is retried immediately.
var ErrMalformedOutput = errors.New("model returned unusable output")

// Options configure an Analyzer.
type Options struct {
	// MaxAttempts is how many times each step may ask the model before
	// falling back. Must be at least 1.
	MaxAttempts int
	// Backoff is the base wait after a transient provider error; the wait
	// before retry n is n*Backoff. Malformed output is retried without waiting.
	Backoff time.Duration
	// Decompose splits the work into two focused model calls (classify, then
	// prioritize) instead of one call that does both.
	Decompose bool
	// Logger receives a line per failed attempt. Defaults to slog.Default().
	Logger *slog.Logger
}

// Analyzer analyses incidents using a Completer. It is safe for concurrent use.
type Analyzer struct {
	completer Completer
	opts      Options
}

// NewAnalyzer returns an Analyzer that asks c.
func NewAnalyzer(c Completer, opts Options) (*Analyzer, error) {
	if c == nil {
		return nil, errors.New("llm: completer is required")
	}
	if opts.MaxAttempts < 1 {
		return nil, fmt.Errorf("llm: MaxAttempts must be at least 1, got %d", opts.MaxAttempts)
	}
	if opts.Backoff < 0 {
		return nil, fmt.Errorf("llm: Backoff must not be negative, got %v", opts.Backoff)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Analyzer{completer: c, opts: opts}, nil
}

// Result is a finished analysis plus how much effort it took.
type Result struct {
	Analysis domain.IncidentAnalysis
	// Latency is the wall-clock time spent, including retries and backoff
	// waits, summed over the steps.
	Latency time.Duration
	// RetryCount is how many attempts beyond the first were needed, summed
	// over the steps; 0 means every step succeeded first time.
	RetryCount int
	// FellBack is true if any step ran out of attempts and its part of
	// Analysis is the fallback rather than a real answer.
	FellBack bool
}

// Analyze classifies and prioritizes an incident from its normalized error
// text. It returns a fallback analysis (category unknown, confidence 0, flagged
// for human review) when the model keeps failing in ways worth retrying. It
// returns an error for failures a retry cannot fix - a rejected API key, a bad
// request - and when ctx ends, rather than hiding a broken deployment behind a
// fake "unknown incident".
func (a *Analyzer) Analyze(ctx context.Context, service string, env domain.Environment, normalizedText string) (Result, error) {
	if a.opts.Decompose {
		return a.analyzeDecomposed(ctx, service, env, normalizedText)
	}
	return a.analyzeSingleCall(ctx, service, env, normalizedText)
}

// analyzeSingleCall asks for the whole analysis in one call.
func (a *Analyzer) analyzeSingleCall(ctx context.Context, service string, env domain.Environment, normalizedText string) (Result, error) {
	out, err := ask(ctx, a, "analyze", singleCallSystemPrompt, classifyUserPrompt(service, env, normalizedText), singleCallFallback)
	if err != nil {
		return Result{}, err
	}
	return Result{Analysis: out.value, Latency: out.latency, RetryCount: out.retries, FellBack: out.fellBack}, nil
}

// analyzeDecomposed asks two focused questions: what kind of error is this,
// then, knowing that, how urgent is it. Each step retries and falls back on its
// own, and priority still reads the error text - the blast radius described
// there, not the category, is what decides it.
func (a *Analyzer) analyzeDecomposed(ctx context.Context, service string, env domain.Environment, normalizedText string) (Result, error) {
	c, err := ask(ctx, a, "classify", classifySystemPrompt, classifyUserPrompt(service, env, normalizedText), classificationFallback)
	if err != nil {
		return Result{}, err
	}
	p, err := ask(ctx, a, "prioritize", prioritizeSystemPrompt,
		prioritizeUserPrompt(service, env, normalizedText, c.value.Category), priorityFallback)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Analysis:   domain.IncidentAnalysis{ClassificationResult: c.value, PriorityResult: p.value},
		Latency:    c.latency + p.latency,
		RetryCount: c.retries + p.retries,
		FellBack:   c.fellBack || p.fellBack,
	}, nil
}

// The results used when a step runs out of attempts. They are deliberately not
// guesses: confidence 0 and needs_human_review make it unmistakable that no
// real classification happened, and "medium" is a middle ground - "low" risks
// ignoring something serious, "critical" risks paging someone over a bad run.
var (
	classificationFallback = domain.ClassificationResult{
		Category:         domain.CategoryUnknown,
		RootCauseSummary: "Automated classification failed after repeated attempts - the model did not return a usable result for this error.",
		Confidence:       0,
		NeedsHumanReview: true,
	}
	priorityFallback = domain.PriorityResult{
		Priority:          domain.PriorityMedium,
		PriorityReasoning: "Priority could not be determined automatically; needs manual triage.",
	}
	singleCallFallback = domain.IncidentAnalysis{
		ClassificationResult: domain.ClassificationResult{
			Category:         domain.CategoryUnknown,
			RootCauseSummary: "Automated analysis failed after repeated attempts - the model did not return a usable classification for this error.",
			Confidence:       0,
			NeedsHumanReview: true,
		},
		PriorityResult: priorityFallback,
	}
)
