package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

const (
	classificationJSON = `{"category":"auth_failure","root_cause_summary":"keys rotated","confidence":0.9,"needs_human_review":false}`
	priorityJSON       = `{"priority":"critical","priority_reasoning":"every request is rejected"}`
	analysisJSON       = `{"category":"auth_failure","root_cause_summary":"keys rotated","confidence":0.9,"needs_human_review":false,` +
		`"priority":"critical","priority_reasoning":"every request is rejected"}`
	malformedJSON = `Sure! Here is your JSON: {`
)

// scripted is a Completer that plays back canned replies in order and records
// the prompts it was asked.
type scripted struct {
	t       *testing.T
	script  []scriptedStep
	systems []string
	users   []string
}

type scriptedStep struct {
	reply string
	err   error
}

func (s *scripted) Complete(_ context.Context, system, user string) (string, error) {
	i := len(s.users)
	s.systems = append(s.systems, system)
	s.users = append(s.users, user)
	if i >= len(s.script) {
		s.t.Errorf("unexpected model call #%d", i+1)
		return "", errors.New("script exhausted")
	}
	return s.script[i].reply, s.script[i].err
}

func (s *scripted) calls() int { return len(s.users) }

func newScripted(t *testing.T, steps ...scriptedStep) *scripted {
	return &scripted{t: t, script: steps}
}

func reply(text string) scriptedStep { return scriptedStep{reply: text} }
func fail(err error) scriptedStep    { return scriptedStep{err: err} }

func newAnalyzer(t *testing.T, c Completer, decompose bool) *Analyzer {
	t.Helper()
	return newAnalyzerWithOptions(t, c, Options{MaxAttempts: 3, Backoff: time.Millisecond, Decompose: decompose})
}

// newAnalyzerWithOptions builds an Analyzer from opts with its logging silenced,
// failing the test if the options are rejected.
func newAnalyzerWithOptions(t *testing.T, c Completer, opts Options) *Analyzer {
	t.Helper()
	opts.Logger = slog.New(slog.DiscardHandler)
	a, err := NewAnalyzer(c, opts)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func analyze(a *Analyzer) (Result, error) {
	return a.Analyze(context.Background(), "auth-gateway", domain.EnvProd, "401 Unauthorized")
}

var (
	rateLimited = &APIError{StatusCode: 429, Message: "slow down"}
	badKey      = &APIError{StatusCode: 401, Message: "Invalid API Key"}
)

func TestSingleCall(t *testing.T) {
	c := newScripted(t, reply(analysisJSON))
	res, err := analyze(newAnalyzer(t, c, false))
	if err != nil {
		t.Fatal(err)
	}

	want := domain.IncidentAnalysis{
		ClassificationResult: domain.ClassificationResult{Category: domain.CategoryAuthFailure, RootCauseSummary: "keys rotated", Confidence: 0.9},
		PriorityResult:       domain.PriorityResult{Priority: domain.PriorityCritical, PriorityReasoning: "every request is rejected"},
	}
	if res.Analysis != want {
		t.Errorf("analysis = %+v, want %+v", res.Analysis, want)
	}
	if res.RetryCount != 0 || c.calls() != 1 {
		t.Errorf("retries=%d calls=%d, want 0 and 1", res.RetryCount, c.calls())
	}
	if c.systems[0] != singleCallSystemPrompt {
		t.Error("single-call mode must use the combined system prompt")
	}
	if want := "service: auth-gateway\nenvironment: prod\nerror:\n401 Unauthorized"; c.users[0] != want {
		t.Errorf("user prompt = %q, want %q", c.users[0], want)
	}
}

func TestMalformedOutputIsRetriedImmediately(t *testing.T) {
	c := newScripted(t, reply(malformedJSON), reply(`{"category":"made_up"}`), reply(analysisJSON))
	a := newAnalyzerWithOptions(t, c, Options{MaxAttempts: 3, Backoff: time.Hour})

	start := time.Now()
	res, err := analyze(a)
	if err != nil {
		t.Fatal(err)
	}
	if res.RetryCount != 2 || c.calls() != 3 || res.Analysis.Category != domain.CategoryAuthFailure {
		t.Errorf("retries=%d calls=%d analysis=%+v", res.RetryCount, c.calls(), res.Analysis)
	}
	if time.Since(start) > time.Minute {
		t.Error("malformed output must be retried without waiting for the backoff")
	}
}

func TestExhaustedMalformedOutputFallsBack(t *testing.T) {
	c := newScripted(t, reply(malformedJSON), reply(malformedJSON), reply(malformedJSON))
	res, err := analyze(newAnalyzer(t, c, false))
	if err != nil {
		t.Fatalf("a model that keeps failing must not fail the request: %v", err)
	}
	if res.Analysis != singleCallFallback {
		t.Errorf("analysis = %+v, want the fallback", res.Analysis)
	}
	if res.RetryCount != 2 || c.calls() != 3 {
		t.Errorf("retries=%d calls=%d, want 2 and 3", res.RetryCount, c.calls())
	}
	if f := res.Analysis; f.Category != domain.CategoryUnknown || f.Confidence != 0 || !f.NeedsHumanReview || f.Priority != domain.PriorityMedium {
		t.Errorf("fallback must be clearly marked as not a real classification: %+v", f)
	}
}

func TestTransientErrorsAreRetriedWithBackoff(t *testing.T) {
	t.Run("recovers", func(t *testing.T) {
		c := newScripted(t, fail(rateLimited), reply(analysisJSON))
		a := newAnalyzerWithOptions(t, c, Options{MaxAttempts: 3, Backoff: 30 * time.Millisecond})

		res, err := analyze(a)
		if err != nil {
			t.Fatal(err)
		}
		if res.RetryCount != 1 || c.calls() != 2 {
			t.Errorf("retries=%d calls=%d, want 1 and 2", res.RetryCount, c.calls())
		}
		if res.Latency < 30*time.Millisecond {
			t.Errorf("latency %v should include the 30ms backoff", res.Latency)
		}
	})

	t.Run("exhausted falls back, waiting n*backoff between attempts but not after the last", func(t *testing.T) {
		c := newScripted(t, fail(rateLimited), fail(rateLimited), fail(&APIError{StatusCode: 503}))
		a := newAnalyzerWithOptions(t, c, Options{MaxAttempts: 3, Backoff: 20 * time.Millisecond})

		res, err := analyze(a)
		if err != nil {
			t.Fatal(err)
		}
		if res.Analysis != singleCallFallback || res.RetryCount != 2 {
			t.Errorf("analysis=%+v retries=%d", res.Analysis, res.RetryCount)
		}
		// 1*20ms after attempt 1 + 2*20ms after attempt 2 = 60ms; nothing after attempt 3.
		if res.Latency < 60*time.Millisecond || res.Latency > 400*time.Millisecond {
			t.Errorf("latency = %v, want roughly 60ms", res.Latency)
		}
	})

	t.Run("network errors count as transient", func(t *testing.T) {
		for name, netFail := range map[string]error{
			"connection refused": &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			"cut-off response":   io.ErrUnexpectedEOF,
		} {
			c := newScripted(t, fail(netFail), reply(analysisJSON))
			res, err := analyze(newAnalyzer(t, c, false))
			if err != nil || res.RetryCount != 1 {
				t.Errorf("%s: err=%v retries=%d, want a retry that succeeds", name, err, res.RetryCount)
			}
		}
	})
}

// Retrying a rejected API key can't work, and answering "unknown incident"
// would hide a broken deployment, so these surface as errors.
func TestNonRetryableErrorsPropagate(t *testing.T) {
	for name, cause := range map[string]error{
		"rejected API key": badKey,
		"bad request":      &APIError{StatusCode: 400, Message: "bad model"},
		"unclassified":     errors.New("boom"),
	} {
		t.Run(name, func(t *testing.T) {
			c := newScripted(t, fail(cause), reply(analysisJSON))
			_, err := analyze(newAnalyzer(t, c, false))
			if !errors.Is(err, cause) {
				t.Fatalf("err = %v, want it to wrap %v", err, cause)
			}
			if c.calls() != 1 {
				t.Errorf("calls = %d, want exactly 1 (no retry)", c.calls())
			}
		})
	}
}

func TestContextCancellationStopsRetrying(t *testing.T) {
	t.Run("before the first call", func(t *testing.T) {
		c := newScripted(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := newAnalyzer(t, c, false).Analyze(ctx, "s", domain.EnvDev, "x")
		if !errors.Is(err, context.Canceled) || c.calls() != 0 {
			t.Errorf("err=%v calls=%d, want context.Canceled and no calls", err, c.calls())
		}
	})

	t.Run("after a failed call", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		c := &cancelling{cancel: cancel}
		a := newAnalyzerWithOptions(t, c, Options{MaxAttempts: 5, Backoff: time.Hour})
		_, err := a.Analyze(ctx, "s", domain.EnvDev, "x")
		if !errors.Is(err, context.Canceled) || c.n != 1 {
			t.Errorf("err=%v calls=%d, want context.Canceled after one call", err, c.n)
		}
	})

	t.Run("during the backoff wait", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		c := newScripted(t, fail(rateLimited), reply(analysisJSON))
		a := newAnalyzerWithOptions(t, c, Options{MaxAttempts: 3, Backoff: time.Hour})

		start := time.Now()
		_, err := a.Analyze(ctx, "s", domain.EnvDev, "x")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
			t.Errorf("err=%v after %v; the wait must end when ctx does", err, time.Since(start))
		}
	})
}

// cancelling cancels the context during its first call and reports a transient
// error, as if the caller gave up while the request was failing.
type cancelling struct {
	cancel context.CancelFunc
	n      int
}

func (c *cancelling) Complete(context.Context, string, string) (string, error) {
	c.n++
	c.cancel()
	return "", rateLimited
}

func TestDecomposed(t *testing.T) {
	t.Run("classify then prioritize, merged", func(t *testing.T) {
		c := newScripted(t, reply(classificationJSON), reply(priorityJSON))
		res, err := analyze(newAnalyzer(t, c, true))
		if err != nil {
			t.Fatal(err)
		}
		if c.calls() != 2 {
			t.Fatalf("calls = %d, want 2", c.calls())
		}
		if res.Analysis.Category != domain.CategoryAuthFailure || res.Analysis.Priority != domain.PriorityCritical ||
			res.Analysis.RootCauseSummary != "keys rotated" || res.Analysis.PriorityReasoning != "every request is rejected" {
			t.Errorf("analysis = %+v", res.Analysis)
		}
		if c.systems[0] != classifySystemPrompt || c.systems[1] != prioritizeSystemPrompt {
			t.Error("each step must use its own system prompt")
		}
		if !strings.HasPrefix(c.users[1], "category: auth_failure\n") || !strings.Contains(c.users[1], "401 Unauthorized") {
			t.Errorf("the priority step must be told the category and still see the error text: %q", c.users[1])
		}
		if strings.Contains(c.users[0], "category:") {
			t.Errorf("the classify step must not be given a category: %q", c.users[0])
		}
	})

	t.Run("retries are summed over both steps", func(t *testing.T) {
		c := newScripted(t,
			reply(malformedJSON), reply(classificationJSON), // classify: 1 retry
			fail(rateLimited), reply(malformedJSON), reply(priorityJSON), // prioritize: 2 retries
		)
		res, err := analyze(newAnalyzer(t, c, true))
		if err != nil {
			t.Fatal(err)
		}
		if res.RetryCount != 3 || c.calls() != 5 {
			t.Errorf("retries=%d calls=%d, want 3 and 5", res.RetryCount, c.calls())
		}
	})

	t.Run("a failed classify falls back but priority still runs", func(t *testing.T) {
		c := newScripted(t, reply(malformedJSON), reply(malformedJSON), reply(malformedJSON), reply(priorityJSON))
		res, err := analyze(newAnalyzer(t, c, true))
		if err != nil {
			t.Fatal(err)
		}
		if res.Analysis.ClassificationResult != classificationFallback {
			t.Errorf("classification = %+v, want the fallback", res.Analysis.ClassificationResult)
		}
		if res.Analysis.Priority != domain.PriorityCritical {
			t.Errorf("priority = %q, want the real answer", res.Analysis.Priority)
		}
		if !strings.HasPrefix(c.users[3], "category: unknown\n") {
			t.Errorf("priority step should be given the fallback category: %q", c.users[3])
		}
		if res.RetryCount != 2 {
			t.Errorf("retries = %d, want 2", res.RetryCount)
		}
	})

	t.Run("a failed prioritize falls back but keeps the classification", func(t *testing.T) {
		c := newScripted(t, reply(classificationJSON), reply(malformedJSON), reply(malformedJSON), reply(malformedJSON))
		res, err := analyze(newAnalyzer(t, c, true))
		if err != nil {
			t.Fatal(err)
		}
		if res.Analysis.Category != domain.CategoryAuthFailure || res.Analysis.PriorityResult != priorityFallback {
			t.Errorf("analysis = %+v", res.Analysis)
		}
	})

	t.Run("a non-retryable error names the step", func(t *testing.T) {
		c := newScripted(t, reply(classificationJSON), fail(badKey))
		_, err := analyze(newAnalyzer(t, c, true))
		if !errors.Is(err, badKey) || !strings.Contains(err.Error(), "prioritize") {
			t.Errorf("err = %v, want it to wrap the cause and name the prioritize step", err)
		}
	})
}

func TestFallbacksAreValid(t *testing.T) {
	if err := classificationFallback.Validate(); err != nil {
		t.Errorf("classification fallback: %v", err)
	}
	if err := priorityFallback.Validate(); err != nil {
		t.Errorf("priority fallback: %v", err)
	}
	if err := singleCallFallback.Validate(); err != nil {
		t.Errorf("single-call fallback: %v", err)
	}
}

func TestPrompts(t *testing.T) {
	for _, cat := range domain.ErrorCategories() {
		for name, p := range map[string]string{"classify": classifySystemPrompt, "single-call": singleCallSystemPrompt} {
			if !strings.Contains(p, `"`+string(cat)+`"`) {
				t.Errorf("%s prompt does not offer category %q", name, cat)
			}
		}
	}
	for _, pr := range domain.Priorities() {
		for name, p := range map[string]string{"prioritize": prioritizeSystemPrompt, "single-call": singleCallSystemPrompt} {
			if !strings.Contains(p, `"`+string(pr)+`"`) {
				t.Errorf("%s prompt does not offer priority %q", name, pr)
			}
		}
	}
	if strings.Contains(classifySystemPrompt, "priority") || strings.Contains(classifySystemPrompt, `"critical"`) {
		t.Error("the classify prompt must not ask for a priority")
	}
	if got, want := quotedList(domain.Priorities()), `["critical", "high", "medium", "low"]`; got != want {
		t.Errorf("quotedList = %s, want %s", got, want)
	}
}

func TestNewAnalyzerValidation(t *testing.T) {
	c := newScripted(t)
	if _, err := NewAnalyzer(nil, Options{MaxAttempts: 1}); err == nil {
		t.Error("a nil completer must be rejected")
	}
	if _, err := NewAnalyzer(c, Options{MaxAttempts: 0}); err == nil {
		t.Error("MaxAttempts 0 must be rejected")
	}
	if _, err := NewAnalyzer(c, Options{MaxAttempts: 1, Backoff: -time.Second}); err == nil {
		t.Error("a negative backoff must be rejected")
	}
	if a, err := NewAnalyzer(c, Options{MaxAttempts: 1}); err != nil || a.opts.Logger == nil {
		t.Errorf("a default logger should be filled in: %v", err)
	}
}

func TestSleep(t *testing.T) {
	if err := sleep(context.Background(), 0); err != nil {
		t.Errorf("zero wait: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled wait must end at once, got %v", err)
	}
}

func TestRetryDelay(t *testing.T) {
	const backoff = 500 * time.Millisecond
	tests := []struct {
		name    string
		err     error
		attempt int
		want    time.Duration
	}{
		{"no hint: linear backoff", rateLimited, 1, 500 * time.Millisecond},
		{"no hint: grows with the attempt", rateLimited, 3, 1500 * time.Millisecond},
		{"a longer Retry-After wins", &APIError{StatusCode: 429, RetryAfter: 30 * time.Second}, 1, 30 * time.Second},
		{"a shorter Retry-After does not shorten the backoff", &APIError{StatusCode: 429, RetryAfter: 100 * time.Millisecond}, 2, time.Second},
		{"the limit of 60s is honored", &APIError{StatusCode: 429, RetryAfter: 60 * time.Second}, 1, 60 * time.Second},
		{"beyond 60s is not worth holding the request: backoff instead", &APIError{StatusCode: 429, RetryAfter: 61 * time.Second}, 1, 500 * time.Millisecond},
		{"works through wrapping", fmt.Errorf("groq: %w", &APIError{StatusCode: 503, RetryAfter: 2 * time.Second}), 1, 2 * time.Second},
		{"not an API error: backoff", io.ErrUnexpectedEOF, 2, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryDelay(tt.err, tt.attempt, backoff); got != tt.want {
				t.Errorf("retryDelay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryAfterIsWaitedOut(t *testing.T) {
	c := newScripted(t, fail(&APIError{StatusCode: 429, Message: "slow down", RetryAfter: 150 * time.Millisecond}), reply(analysisJSON))
	a := newAnalyzerWithOptions(t, c, Options{MaxAttempts: 3, Backoff: time.Millisecond})

	res, err := analyze(a)
	if err != nil {
		t.Fatal(err)
	}
	if res.RetryCount != 1 || res.Latency < 150*time.Millisecond {
		t.Errorf("retries=%d latency=%v; the 150ms Retry-After should have been waited out", res.RetryCount, res.Latency)
	}
}
