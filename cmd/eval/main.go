// Command eval scores the analysis pipeline against the labeled synthetic logs
// and prints accuracy, precision/recall and latency. It calls the real Groq API,
// so a full run spends quota; use -limit for a quick check.
//
//	go run ./cmd/eval                       # mode from TASK_DECOMPOSITION
//	go run ./cmd/eval -mode both -save      # single call vs decomposed, results saved
//	go run ./cmd/eval -limit 5              # first five examples only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"

	"github.com/sopabarth/ai-log-incident-analyzer-go/data"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/config"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/eval"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/llm"
)

const (
	modeSingle     = "single"
	modeDecomposed = "decomposed"
	modeBoth       = "both"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("eval: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	_ = godotenv.Load() // optional; variables already in the environment win
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	defaultMode := modeSingle
	if cfg.TaskDecomposition {
		defaultMode = modeDecomposed
	}
	var (
		mode     = flag.String("mode", defaultMode, "single, decomposed or both (default follows TASK_DECOMPOSITION)")
		limit    = flag.Int("limit", 0, "only run the first N examples (0 = all)")
		save     = flag.Bool("save", false, "write per-example results as JSON files")
		saveDir  = flag.String("out", filepath.Join("eval", "results"), "directory for -save")
		dataFile = flag.String("data", "", "labeled dataset to use instead of the embedded one")
	)
	flag.Parse()
	if *mode != modeSingle && *mode != modeDecomposed && *mode != modeBoth {
		return fmt.Errorf("-mode must be %s, %s or %s, got %q", modeSingle, modeDecomposed, modeBoth, *mode)
	}

	raw := data.SyntheticLogs
	if *dataFile != "" {
		if raw, err = os.ReadFile(*dataFile); err != nil {
			return err
		}
	}
	examples, err := eval.ParseDataset(raw)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(examples) {
		examples = examples[:*limit]
	}

	groq, err := llm.NewGroq(cfg.GroqAPIKey, cfg.GroqModel)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("model %s, %d examples, mode %s\n\n", cfg.GroqModel, len(examples), *mode)

	var (
		single, decomposed []eval.Outcome
		runErr             error
	)
	if *mode != modeDecomposed {
		single, runErr = runMode(ctx, modeSingle, groq, cfg, false, examples)
	}
	if runErr == nil && *mode != modeSingle {
		decomposed, runErr = runMode(ctx, modeDecomposed, groq, cfg, true, examples)
	}

	// Report whatever finished, even when a run stopped early.
	for _, r := range []struct {
		name     string
		outcomes []eval.Outcome
	}{{modeSingle, single}, {modeDecomposed, decomposed}} {
		if len(r.outcomes) == 0 {
			continue
		}
		fmt.Println(eval.NewReport(r.outcomes).Render(r.name))
		if *save {
			path, err := saveResults(*saveDir, r.name, r.outcomes)
			if err != nil {
				return err
			}
			fmt.Printf("saved %s\n\n", path)
		}
	}
	if *mode == modeBoth && runErr == nil {
		fmt.Println(eval.RenderComparison(modeSingle, single, modeDecomposed, decomposed))
	}
	return runErr
}

// runMode builds an analyzer for one pipeline variant and runs the examples
// through it.
func runMode(ctx context.Context, name string, groq *llm.Groq, cfg config.Config, decompose bool, examples []eval.Example) ([]eval.Outcome, error) {
	a, err := llm.NewAnalyzer(groq, llm.Options{
		MaxAttempts: cfg.LLMMaxAttempts,
		Backoff:     cfg.LLMRetryBackoff,
		Decompose:   decompose,
		// Retries are normal under the free tier's rate limit and the report
		// counts them, so keep the per-retry log lines out of the progress output.
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		return nil, err
	}
	return eval.Run(ctx, name, a, examples, os.Stdout)
}

// saveResults writes the per-example records to dir/<name>-<timestamp>.json.
func saveResults(dir, name string, outcomes []eval.Outcome) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(eval.Records(outcomes), "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.json", name, time.Now().Format("20060102-150405")))
	return path, os.WriteFile(path, body, 0o644)
}
