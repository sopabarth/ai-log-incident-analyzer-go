package eval

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/llm"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/parser"
)

// Analyzer analyses one normalized error. *llm.Analyzer implements it.
type Analyzer interface {
	Analyze(ctx context.Context, service string, env domain.Environment, normalizedText string) (llm.Result, error)
}

// Outcome is what the pipeline made of one example.
type Outcome struct {
	Example Example
	Result  llm.Result
}

// CategoryCorrect reports whether the category matches the label.
func (o Outcome) CategoryCorrect() bool {
	return o.Result.Analysis.Category == o.Example.ExpectedCategory
}

// perExampleTimeout bounds one example, retries and rate-limit waits included.
const perExampleTimeout = 2 * time.Minute

// Run sends each example through the analyzer, one at a time, and returns the
// outcomes in order. Examples run sequentially on purpose: the free tier's
// tokens-per-minute limit would throttle a parallel run anyway, and the
// analyzer already waits out rate limits (Retry-After) rather than failing.
//
// If progress is not nil, one line per example is written to it as it
// finishes, labelled with name. When Run stops early - ctx ended, or a failure
// retrying cannot fix such as a rejected API key - it returns the outcomes
// completed so far together with the error.
func Run(ctx context.Context, name string, a Analyzer, examples []Example, progress io.Writer) ([]Outcome, error) {
	outcomes := make([]Outcome, 0, len(examples))
	for i, ex := range examples {
		exCtx, cancel := context.WithTimeout(ctx, perExampleTimeout)
		res, err := a.Analyze(exCtx, ex.Service, ex.Environment, parser.NormalizeRawText(ex.RawText, parser.DefaultMaxFrames))
		cancel()
		if err != nil {
			return outcomes, fmt.Errorf("example %d/%d (%s): %w", i+1, len(examples), ex.Service, err)
		}

		out := Outcome{Example: ex, Result: res}
		outcomes = append(outcomes, out)
		if progress != nil {
			verdict := "ok  "
			switch {
			case res.FellBack:
				verdict = "FALL"
			case !out.CategoryCorrect():
				verdict = "MISS"
			}
			// Progress output is best effort; a closed terminal is not worth failing a run for.
			_, _ = fmt.Fprintf(progress, "  [%s] %2d/%d %s  %-24s expected %-23s got %s/%s (%v)\n",
				name, i+1, len(examples), verdict, ex.Service, ex.ExpectedCategory,
				res.Analysis.Category, res.Analysis.Priority, res.Latency.Round(10*time.Millisecond))
		}
	}
	return outcomes, nil
}
