package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/data"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/llm"
)

func TestEmbeddedDatasetIsValid(t *testing.T) {
	examples, err := ParseDataset(data.SyntheticLogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 41 {
		t.Errorf("got %d examples, want 41", len(examples))
	}
	perCategory := map[domain.ErrorCategory]int{}
	for _, ex := range examples {
		perCategory[ex.ExpectedCategory]++
		if ex.RawText == "" || ex.Service == "" {
			t.Errorf("incomplete example: %+v", ex)
		}
	}
	for _, c := range domain.ErrorCategories() {
		if perCategory[c] < 5 {
			t.Errorf("category %q has only %d examples", c, perCategory[c])
		}
	}
}

func TestParseDatasetRejectsBadLabels(t *testing.T) {
	good := `{"service":"s","environment":"prod","raw_text":"x","expected_category":"unknown","expected_priority":"low","expected_needs_human_review":false}`
	if got, err := ParseDataset([]byte("[" + good + "]")); err != nil || len(got) != 1 {
		t.Fatalf("valid dataset rejected: %v", err)
	}
	bad := map[string]string{
		"not json":     `nope`,
		"bad category": strings.Replace(good, `"unknown"`, `"made_up"`, 1),
		"bad priority": strings.Replace(good, `"low"`, `"urgent"`, 1),
		"bad env":      strings.Replace(good, `"prod"`, `"qa"`, 1),
	}
	for name, body := range bad {
		if _, err := ParseDataset([]byte("[" + body + "]")); err == nil && name != "not json" {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := ParseDataset([]byte(bad["not json"])); err == nil {
		t.Error("not json: expected an error")
	}
}

// outcome builds an Outcome for a labeled example and what the pipeline said.
func outcome(wantCat domain.ErrorCategory, wantPrio domain.Priority, wantReview bool,
	gotCat domain.ErrorCategory, gotPrio domain.Priority, gotReview bool, latency time.Duration, retries int, fellBack bool) Outcome {
	return Outcome{
		Example: Example{
			Service: "svc", Environment: domain.EnvProd,
			ExpectedCategory: wantCat, ExpectedPriority: wantPrio, ExpectedNeedsHumanReview: wantReview,
		},
		Result: llm.Result{
			Analysis: domain.IncidentAnalysis{
				ClassificationResult: domain.ClassificationResult{Category: gotCat, Confidence: 0.9, NeedsHumanReview: gotReview},
				PriorityResult:       domain.PriorityResult{Priority: gotPrio},
			},
			Latency: latency, RetryCount: retries, FellBack: fellBack,
		},
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A small report whose every number is worked out by hand below.
func sampleOutcomes() []Outcome {
	const (
		db, auth, unk = domain.CategoryDatabaseTimeout, domain.CategoryAuthFailure, domain.CategoryUnknown
		crit, high    = domain.PriorityCritical, domain.PriorityHigh
		med, low      = domain.PriorityMedium, domain.PriorityLow
		s             = time.Second
	)
	return []Outcome{
		// expected cat/prio/review          got cat/prio/review      latency   retries fellback
		outcome(db, crit, false /**/, db, crit, false /**/, 1*s, 0, false),   // all correct
		outcome(db, crit, false /**/, db, high, false /**/, 2*s, 1, false),   // priority 1 level off
		outcome(auth, low, false /**/, db, low, false /**/, 3*s, 0, false),   // wrong category (auth -> db)
		outcome(auth, crit, false /**/, auth, low, true /**/, 4*s, 2, false), // priority 3 off, review wrong
		outcome(unk, med, true /**/, unk, med, true /**/, 10*s, 2, true),     // correct but via fallback
	}
}

func TestNewReport(t *testing.T) {
	r := NewReport(sampleOutcomes())

	if r.Total != 5 {
		t.Fatalf("Total = %d", r.Total)
	}
	// Category: 4 of 5 right (example 3 is wrong).
	if r.CategoryCorrect != 4 || !approx(r.CategoryAccuracy, 0.8) {
		t.Errorf("category %d (%v), want 4 (0.8)", r.CategoryCorrect, r.CategoryAccuracy)
	}
	// Priority: exact in examples 1, 3, 5 -> 3/5; distances 0,1,0,3,0 -> sum 4 -> mean 0.8.
	if r.PriorityExact != 3 || !approx(r.PriorityAccuracy(), 0.6) || !approx(r.PriorityMeanDistance, 0.8) {
		t.Errorf("priority exact=%d acc=%v meanDist=%v, want 3, 0.6, 0.8", r.PriorityExact, r.PriorityAccuracy(), r.PriorityMeanDistance)
	}
	if want := map[int]int{0: 3, 1: 1, 3: 1}; len(r.PriorityDistances) != 3 || r.PriorityDistances[0] != 3 || r.PriorityDistances[1] != 1 || r.PriorityDistances[3] != 1 {
		t.Errorf("distances = %v, want %v", r.PriorityDistances, want)
	}
	// Review flag: right in 1,2,3,5; wrong in 4 -> 4/5.
	if !approx(r.ReviewAccuracy, 0.8) {
		t.Errorf("review accuracy = %v, want 0.8", r.ReviewAccuracy)
	}
	// Latency 1,2,3,4,10 -> mean 4s, median 3s, max 10s. Retries 0+1+0+2+2.
	if r.MeanLatency != 4*time.Second || r.MedianLatency != 3*time.Second || r.MaxLatency != 10*time.Second {
		t.Errorf("latency mean/median/max = %v/%v/%v", r.MeanLatency, r.MedianLatency, r.MaxLatency)
	}
	if r.Retries != 5 || r.Fallbacks != 1 {
		t.Errorf("retries=%d fallbacks=%d, want 5 and 1", r.Retries, r.Fallbacks)
	}
	if len(r.Misses) != 1 || r.Misses[0].Example.ExpectedCategory != domain.CategoryAuthFailure {
		t.Errorf("misses = %+v", r.Misses)
	}

	// Per category. database_timeout: called 3 times (ex 1,2,3), correct 2 (ex 1,2), labeled 2.
	// auth_failure: called once (ex 4, correct), labeled 2 (ex 3,4). unknown: 1 called, 1 correct, 1 labeled.
	stats := map[domain.ErrorCategory]CategoryStats{}
	for _, c := range r.Categories {
		stats[c.Category] = c
	}
	checks := []struct {
		cat       domain.ErrorCategory
		precision float64
		recall    float64
		support   int
	}{
		{domain.CategoryDatabaseTimeout, 2.0 / 3, 1.0, 2},
		{domain.CategoryAuthFailure, 1.0, 0.5, 2},
		{domain.CategoryUnknown, 1.0, 1.0, 1},
	}
	for _, c := range checks {
		got := stats[c.cat]
		if !approx(got.Precision, c.precision) || !approx(got.Recall, c.recall) || got.Support != c.support {
			t.Errorf("%s: precision=%v recall=%v support=%d, want %v %v %d", c.cat, got.Precision, got.Recall, got.Support, c.precision, c.recall, c.support)
		}
	}
	if len(r.Categories) != 3 {
		t.Errorf("categories that never appear should be left out, got %d rows", len(r.Categories))
	}
}

func TestReportEdgeCases(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		r := NewReport(nil)
		if r.Total != 0 || r.PriorityAccuracy() != 0 {
			t.Errorf("empty report = %+v", r)
		}
		if !strings.Contains(r.Render("x"), "no examples") {
			t.Error("an empty report should say so")
		}
	})
	t.Run("median of an even count is the middle pair's mean", func(t *testing.T) {
		o := func(l time.Duration) Outcome {
			return outcome(domain.CategoryUnknown, domain.PriorityLow, false, domain.CategoryUnknown, domain.PriorityLow, false, l, 0, false)
		}
		r := NewReport([]Outcome{o(1 * time.Second), o(2 * time.Second), o(4 * time.Second), o(100 * time.Second)})
		if r.MedianLatency != 3*time.Second {
			t.Errorf("median = %v, want 3s", r.MedianLatency)
		}
	})
	t.Run("recall is undefined for a category that was never the answer", func(t *testing.T) {
		// Labeled auth, called db: auth has support 1 but was never predicted -> precision undefined.
		r := NewReport([]Outcome{outcome(domain.CategoryAuthFailure, domain.PriorityLow, false, domain.CategoryDatabaseTimeout, domain.PriorityLow, false, time.Second, 0, false)})
		for _, c := range r.Categories {
			if c.Category == domain.CategoryAuthFailure && (!math.IsNaN(c.Precision) || c.Recall != 0) {
				t.Errorf("auth: precision=%v recall=%v, want NaN and 0", c.Precision, c.Recall)
			}
			if c.Category == domain.CategoryDatabaseTimeout && (c.Precision != 0 || !math.IsNaN(c.Recall)) {
				t.Errorf("db: precision=%v recall=%v, want 0 and NaN", c.Precision, c.Recall)
			}
		}
		if out := r.Render("x"); !strings.Contains(out, "n/a") {
			t.Errorf("undefined values should render as n/a:\n%s", out)
		}
	})
}

func TestRender(t *testing.T) {
	out := NewReport(sampleOutcomes()).Render("single-call")
	for _, want := range []string{
		"=== SINGLE-CALL - 5 examples ===",
		"Category accuracy: 80.0% (4/5)",
		"Priority exact match: 60.0% (3/5); mean distance 0.80",
		"0: 3, 1: 1, 3: 1",
		"needs_human_review accuracy: 80.0%",
		"mean 4s, median 3s, max 10s",
		"Retries: 5, fallbacks: 1",
		"1 example(s) fell back",
		"Category misses (1):",
		"svc/prod: expected auth_failure, got database_timeout",
		"database_timeout", "precision", "recall", "support",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestDisagreementsAndComparison(t *testing.T) {
	a := sampleOutcomes()
	b := sampleOutcomes()
	// Make example 2 differ in priority and example 3 differ in category.
	b[1].Result.Analysis.Priority = domain.PriorityCritical
	b[2].Result.Analysis.Category = domain.CategoryAuthFailure

	diffs := Disagreements(a, b)
	if len(diffs) != 2 {
		t.Fatalf("got %d disagreements, want 2", len(diffs))
	}
	if Disagreements(a, a) != nil {
		t.Error("identical runs should not disagree")
	}

	out := RenderComparison("single", a, "decomposed", b)
	for _, want := range []string{
		"COMPARISON: single vs decomposed",
		"Disagreements (different category or priority): 2/5",
		"category accuracy", "80.0%", "100.0%", // b fixed the wrong category
		"priority exact match",
		"fallbacks",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("comparison is missing %q:\n%s", want, out)
		}
	}
}

func TestRecords(t *testing.T) {
	recs := Records(sampleOutcomes())
	if len(recs) != 5 {
		t.Fatal("one record per outcome")
	}
	b, err := json.Marshal(recs[2])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// The same keys as the Python eval's saved files, plus fell_back.
	for _, k := range []string{
		"service", "environment", "expected_category", "actual_category", "expected_priority", "actual_priority",
		"expected_needs_human_review", "actual_needs_human_review", "confidence", "latency_ms", "retry_count", "fell_back", "notes",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("record is missing %q", k)
		}
	}
	if m["expected_category"] != "auth_failure" || m["actual_category"] != "database_timeout" || m["latency_ms"] != float64(3000) {
		t.Errorf("record = %v", m)
	}
}

// --- Run ---

type scriptedAnalyzer struct {
	results []llm.Result
	errAt   int // 1-based call that fails; 0 = never
	err     error
	calls   int
	texts   []string
}

func (s *scriptedAnalyzer) Analyze(_ context.Context, _ string, _ domain.Environment, text string) (llm.Result, error) {
	s.calls++
	s.texts = append(s.texts, text)
	if s.calls == s.errAt {
		return llm.Result{}, s.err
	}
	return s.results[(s.calls-1)%len(s.results)], nil
}

func twoExamples() []Example {
	return []Example{
		{Service: "a", Environment: domain.EnvProd, RawText: "  boom\n  at x.Y.z(Y.java:1)\n", ExpectedCategory: domain.CategoryUnknown, ExpectedPriority: domain.PriorityLow},
		{Service: "b", Environment: domain.EnvDev, RawText: "bang", ExpectedCategory: domain.CategoryAuthFailure, ExpectedPriority: domain.PriorityHigh},
	}
}

func resultFor(cat domain.ErrorCategory, prio domain.Priority) llm.Result {
	return llm.Result{Analysis: domain.IncidentAnalysis{
		ClassificationResult: domain.ClassificationResult{Category: cat},
		PriorityResult:       domain.PriorityResult{Priority: prio},
	}, Latency: time.Second}
}

func TestRun(t *testing.T) {
	a := &scriptedAnalyzer{results: []llm.Result{
		resultFor(domain.CategoryUnknown, domain.PriorityLow),
		resultFor(domain.CategoryDatabaseTimeout, domain.PriorityHigh),
	}}
	var progress bytes.Buffer

	outcomes, err := Run(context.Background(), "single", a, twoExamples(), &progress)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 || a.calls != 2 {
		t.Fatalf("outcomes=%d calls=%d", len(outcomes), a.calls)
	}
	if want := "boom\nat x.Y.z(Y.java:1)"; a.texts[0] != want {
		t.Errorf("the analyzer must get normalized text: %q, want %q", a.texts[0], want)
	}
	if !outcomes[0].CategoryCorrect() || outcomes[1].CategoryCorrect() {
		t.Error("CategoryCorrect wrong")
	}

	out := progress.String()
	for _, want := range []string{"[single]", " 1/2", " 2/2", "ok", "MISS", "expected auth_failure", "got database_timeout/high"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress output is missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\n") != 2 {
		t.Errorf("want one progress line per example:\n%s", out)
	}
}

func TestRunMarksFallbacksAndAcceptsNilProgress(t *testing.T) {
	fb := resultFor(domain.CategoryUnknown, domain.PriorityMedium)
	fb.FellBack = true
	a := &scriptedAnalyzer{results: []llm.Result{fb}}
	var progress bytes.Buffer

	if _, err := Run(context.Background(), "x", a, twoExamples()[:1], &progress); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), "FALL") {
		t.Errorf("a fallback should be flagged in the progress line: %q", progress.String())
	}
	if _, err := Run(context.Background(), "x", &scriptedAnalyzer{results: []llm.Result{fb}}, twoExamples(), nil); err != nil {
		t.Errorf("a nil progress writer must be fine: %v", err)
	}
}

func TestRunStopsOnErrorKeepingWhatItHad(t *testing.T) {
	boom := errors.New("invalid api key")
	a := &scriptedAnalyzer{results: []llm.Result{resultFor(domain.CategoryUnknown, domain.PriorityLow)}, errAt: 2, err: boom}

	outcomes, err := Run(context.Background(), "x", a, twoExamples(), nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the cause", err)
	}
	if !strings.Contains(err.Error(), "2/2") || !strings.Contains(err.Error(), "(b)") {
		t.Errorf("the error should say which example failed: %v", err)
	}
	if len(outcomes) != 1 {
		t.Errorf("the completed outcome should be returned, got %d", len(outcomes))
	}
}
