package eval

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

// CategoryStats are the precision and recall for one category: of the logs the
// pipeline called X, how many really were X (precision), and of the logs that
// really were X, how many it called X (recall). Either is NaN when undefined.
type CategoryStats struct {
	Category  domain.ErrorCategory
	Precision float64
	Recall    float64
	Support   int // how many logs are labeled with this category
}

// Report scores a set of outcomes.
type Report struct {
	Total int

	CategoryCorrect  int
	CategoryAccuracy float64
	Categories       []CategoryStats

	PriorityExact        int
	PriorityMeanDistance float64
	// PriorityDistances counts examples by how many levels the priority was off
	// (0 = exact). Priority is graded, so near misses are told apart from far ones.
	PriorityDistances map[int]int

	ReviewAccuracy float64

	MeanLatency   time.Duration
	MedianLatency time.Duration
	MaxLatency    time.Duration
	Retries       int
	// Fallbacks counts examples where a step ran out of attempts. They are
	// scored like any other (as misses), but this shows how many results are
	// the pipeline's failure rather than the model's judgment.
	Fallbacks int

	// Misses are the examples with the wrong category.
	Misses []Outcome
}

// NewReport scores outcomes.
func NewReport(outcomes []Outcome) Report {
	r := Report{Total: len(outcomes), PriorityDistances: map[int]int{}}
	if r.Total == 0 {
		return r
	}

	priorityRank := map[domain.Priority]int{}
	for i, p := range domain.Priorities() {
		priorityRank[p] = i
	}

	var (
		reviewCorrect int
		distanceSum   int
		latencies     = make([]time.Duration, 0, r.Total)
		latencySum    time.Duration
	)
	for _, o := range outcomes {
		a := o.Result.Analysis
		if o.CategoryCorrect() {
			r.CategoryCorrect++
		} else {
			r.Misses = append(r.Misses, o)
		}
		d := priorityRank[a.Priority] - priorityRank[o.Example.ExpectedPriority]
		d = max(d, -d)
		distanceSum += d
		r.PriorityDistances[d]++
		if d == 0 {
			r.PriorityExact++
		}
		if a.NeedsHumanReview == o.Example.ExpectedNeedsHumanReview {
			reviewCorrect++
		}
		latencies = append(latencies, o.Result.Latency)
		latencySum += o.Result.Latency
		r.Retries += o.Result.RetryCount
		if o.Result.FellBack {
			r.Fallbacks++
		}
	}

	n := float64(r.Total)
	r.CategoryAccuracy = float64(r.CategoryCorrect) / n
	r.PriorityMeanDistance = float64(distanceSum) / n
	r.ReviewAccuracy = float64(reviewCorrect) / n
	r.Categories = categoryStats(outcomes)

	slices.Sort(latencies)
	r.MeanLatency = latencySum / time.Duration(r.Total)
	r.MaxLatency = latencies[len(latencies)-1]
	mid := len(latencies) / 2
	if len(latencies)%2 == 1 {
		r.MedianLatency = latencies[mid]
	} else {
		r.MedianLatency = (latencies[mid-1] + latencies[mid]) / 2
	}
	return r
}

// PriorityAccuracy is the share of examples whose priority matched exactly.
func (r Report) PriorityAccuracy() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.PriorityExact) / float64(r.Total)
}

func categoryStats(outcomes []Outcome) []CategoryStats {
	var stats []CategoryStats
	for _, cat := range domain.ErrorCategories() {
		var truePositive, predicted, actual int
		for _, o := range outcomes {
			got, want := o.Result.Analysis.Category, o.Example.ExpectedCategory
			if got == cat {
				predicted++
			}
			if want == cat {
				actual++
			}
			if got == cat && want == cat {
				truePositive++
			}
		}
		if predicted == 0 && actual == 0 {
			continue
		}
		stats = append(stats, CategoryStats{
			Category:  cat,
			Precision: ratio(truePositive, predicted),
			Recall:    ratio(truePositive, actual),
			Support:   actual,
		})
	}
	return stats
}

func ratio(num, den int) float64 {
	if den == 0 {
		return math.NaN()
	}
	return float64(num) / float64(den)
}

// Render formats the report for a terminal, under a heading made from name.
func (r Report) Render(name string) string {
	var b builder
	b.printf("=== %s - %d examples ===\n\n", strings.ToUpper(name), r.Total)
	if r.Total == 0 {
		b.printf("(no examples)\n")
		return b.String()
	}

	b.printf("Category accuracy: %s (%d/%d)\n", pct(r.CategoryAccuracy), r.CategoryCorrect, r.Total)
	tw := tabwriter.NewWriter(&b.Builder, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(tw, "category\tprecision\trecall\tsupport\t")
	for _, c := range r.Categories {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t\n", c.Category, pctOrNA(c.Precision), pctOrNA(c.Recall), c.Support)
	}
	_ = tw.Flush()

	b.printf("\nPriority exact match: %s (%d/%d); mean distance %.2f (0 = exact)\n",
		pct(r.PriorityAccuracy()), r.PriorityExact, r.Total, r.PriorityMeanDistance)
	b.printf("  levels off -> count: %s\n", formatDistances(r.PriorityDistances))
	b.printf("needs_human_review accuracy: %s\n", pct(r.ReviewAccuracy))

	b.printf("\nLatency: mean %v, median %v, max %v (retry waits included)\n",
		r.MeanLatency.Round(time.Millisecond), r.MedianLatency.Round(time.Millisecond), r.MaxLatency.Round(time.Millisecond))
	b.printf("Retries: %d, fallbacks: %d\n", r.Retries, r.Fallbacks)
	if r.Fallbacks > 0 {
		b.printf("  %d example(s) fell back because the model kept failing; they count as misses above.\n", r.Fallbacks)
	}

	if len(r.Misses) > 0 {
		b.printf("\nCategory misses (%d):\n", len(r.Misses))
		for _, o := range r.Misses {
			b.printf("  %s/%s: expected %s, got %s (confidence %.2f)", o.Example.Service, o.Example.Environment,
				o.Example.ExpectedCategory, o.Result.Analysis.Category, o.Result.Analysis.Confidence)
			if o.Result.FellBack {
				b.printf(" [fallback]")
			}
			if o.Example.Notes != "" {
				b.printf(" - %s", o.Example.Notes)
			}
			b.printf("\n")
		}
	}
	return b.String()
}

// Disagreement is an example the two compared pipelines answered differently.
type Disagreement struct {
	A, B Outcome
}

// Disagreements lists the examples where a and b differ in category or priority.
// a and b must come from the same examples, in the same order.
func Disagreements(a, b []Outcome) []Disagreement {
	var out []Disagreement
	for i := range min(len(a), len(b)) {
		x, y := a[i].Result.Analysis, b[i].Result.Analysis
		if x.Category != y.Category || x.Priority != y.Priority {
			out = append(out, Disagreement{A: a[i], B: b[i]})
		}
	}
	return out
}

// RenderComparison formats a head-to-head of two runs over the same examples.
func RenderComparison(nameA string, a []Outcome, nameB string, b []Outcome) string {
	ra, rb := NewReport(a), NewReport(b)
	diffs := Disagreements(a, b)

	var out builder
	out.printf("=== COMPARISON: %s vs %s ===\n", nameA, nameB)
	out.printf("Disagreements (different category or priority): %d/%d\n", len(diffs), min(len(a), len(b)))
	for _, d := range diffs {
		ex := d.A.Example
		out.printf("  %s/%s (expected %s/%s):\n", ex.Service, ex.Environment, ex.ExpectedCategory, ex.ExpectedPriority)
		out.printf("    %-12s %s/%s\n", nameA+":", d.A.Result.Analysis.Category, d.A.Result.Analysis.Priority)
		out.printf("    %-12s %s/%s\n", nameB+":", d.B.Result.Analysis.Category, d.B.Result.Analysis.Priority)
	}

	out.printf("\n")
	tw := tabwriter.NewWriter(&out.Builder, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintf(tw, "metric\t%s\t%s\t\n", nameA, nameB)
	_, _ = fmt.Fprintf(tw, "category accuracy\t%s\t%s\t\n", pct(ra.CategoryAccuracy), pct(rb.CategoryAccuracy))
	_, _ = fmt.Fprintf(tw, "priority exact match\t%s\t%s\t\n", pct(ra.PriorityAccuracy()), pct(rb.PriorityAccuracy()))
	_, _ = fmt.Fprintf(tw, "priority mean distance\t%.2f\t%.2f\t\n", ra.PriorityMeanDistance, rb.PriorityMeanDistance)
	_, _ = fmt.Fprintf(tw, "needs_human_review accuracy\t%s\t%s\t\n", pct(ra.ReviewAccuracy), pct(rb.ReviewAccuracy))
	_, _ = fmt.Fprintf(tw, "median latency\t%v\t%v\t\n", ra.MedianLatency.Round(time.Millisecond), rb.MedianLatency.Round(time.Millisecond))
	_, _ = fmt.Fprintf(tw, "fallbacks\t%d\t%d\t\n", ra.Fallbacks, rb.Fallbacks)
	_ = tw.Flush()
	return out.String()
}

// Record is one example's result in the JSON files written by `eval -save`.
type Record struct {
	Service                  string  `json:"service"`
	Environment              string  `json:"environment"`
	ExpectedCategory         string  `json:"expected_category"`
	ActualCategory           string  `json:"actual_category"`
	ExpectedPriority         string  `json:"expected_priority"`
	ActualPriority           string  `json:"actual_priority"`
	ExpectedNeedsHumanReview bool    `json:"expected_needs_human_review"`
	ActualNeedsHumanReview   bool    `json:"actual_needs_human_review"`
	Confidence               float64 `json:"confidence"`
	LatencyMS                int64   `json:"latency_ms"`
	RetryCount               int     `json:"retry_count"`
	FellBack                 bool    `json:"fell_back"`
	Notes                    string  `json:"notes"`
}

// Records converts outcomes to their saved form.
func Records(outcomes []Outcome) []Record {
	records := make([]Record, len(outcomes))
	for i, o := range outcomes {
		a := o.Result.Analysis
		records[i] = Record{
			Service: o.Example.Service, Environment: string(o.Example.Environment),
			ExpectedCategory: string(o.Example.ExpectedCategory), ActualCategory: string(a.Category),
			ExpectedPriority: string(o.Example.ExpectedPriority), ActualPriority: string(a.Priority),
			ExpectedNeedsHumanReview: o.Example.ExpectedNeedsHumanReview, ActualNeedsHumanReview: a.NeedsHumanReview,
			Confidence: a.Confidence, LatencyMS: o.Result.Latency.Milliseconds(),
			RetryCount: o.Result.RetryCount, FellBack: o.Result.FellBack, Notes: o.Example.Notes,
		}
	}
	return records
}

// builder is a strings.Builder with a printf. Writing to a strings.Builder
// cannot fail, so the error from Fprintf is deliberately dropped here, once.
type builder struct{ strings.Builder }

func (b *builder) printf(format string, args ...any) { _, _ = fmt.Fprintf(&b.Builder, format, args...) }

func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

func pctOrNA(f float64) string {
	if math.IsNaN(f) {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

func formatDistances(d map[int]int) string {
	keys := make([]int, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d: %d", k, d[k])
	}
	return strings.Join(parts, ", ")
}
