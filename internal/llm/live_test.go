package llm

import (
	"cmp"
	"context"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/parser"
)

// TestGroqLive calls the real Groq API, in both modes, on a few clear-cut
// errors. It is opt-in, because having a key in .env (which the app itself
// needs) must not make every `go test ./...` spend API quota:
//
//	GROQ_LIVE_TEST=1 go test -run Live -v ./internal/llm
//
// A run uses about 6k tokens - most of the free tier's 8000 tokens per minute -
// so don't run it twice within a minute; a second run would be rate limited
// (which the retry logic then waits out via Retry-After, but slowly).
//
// It only asserts what a competent model should always get right (the category,
// that no fallback was needed, that the answer is valid); priorities are logged
// for a human to eyeball, since they legitimately vary between runs and modes.
func TestGroqLive(t *testing.T) {
	if os.Getenv("GROQ_LIVE_TEST") != "1" {
		t.Skip("set GROQ_LIVE_TEST=1 to call the real Groq API")
	}
	_ = godotenv.Load("../../.env") // the key lives in .env; the opt-in flag does not
	key := os.Getenv("GROQ_API_KEY")
	if key == "" || key == "your_groq_api_key_here" {
		t.Skip("GROQ_API_KEY not set (environment or .env)")
	}
	g, err := NewGroq(key, cmp.Or(os.Getenv("GROQ_MODEL"), DefaultGroqModel))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		service      string
		env          domain.Environment
		raw          string
		wantCategory domain.ErrorCategory
	}{
		{
			name: "every request rejected", service: "auth-gateway", env: domain.EnvProd,
			raw:          "token validation failed for all incoming requests since 12:15 - signing key rotation mismatch, kid=2026-09-a not found in JWKS cache",
			wantCategory: domain.CategoryAuthFailure,
		},
		{
			name: "single user lockout", service: "user-service", env: domain.EnvProd,
			raw:          "401 Unauthorized: invalid credentials for user_id=88213, attempt=4, account temporarily locked",
			wantCategory: domain.CategoryAuthFailure,
		},
		{
			name: "database timeout", service: "payments-api", env: domain.EnvProd,
			raw: "Traceback (most recent call last):\n" +
				"  File \"app/db/session.py\", line 42, in get_connection\n" +
				"    conn = pool.acquire(timeout=5)\n" +
				"psycopg2.OperationalError: timeout expired",
			wantCategory: domain.CategoryDatabaseTimeout,
		},
	}

	for _, decompose := range []bool{false, true} {
		mode := map[bool]string{false: "single-call", true: "decomposed"}[decompose]
		a, err := NewAnalyzer(g, Options{MaxAttempts: 3, Backoff: 500 * time.Millisecond, Decompose: decompose})
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()

				res, err := a.Analyze(ctx, tc.service, tc.env, parser.NormalizeRawText(tc.raw, parser.DefaultMaxFrames))
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("category=%s priority=%s confidence=%.2f review=%v retries=%d latency=%v\n  %s\n  %s",
					res.Analysis.Category, res.Analysis.Priority, res.Analysis.Confidence, res.Analysis.NeedsHumanReview,
					res.RetryCount, res.Latency.Round(time.Millisecond), res.Analysis.RootCauseSummary, res.Analysis.PriorityReasoning)

				if err := res.Analysis.Validate(); err != nil {
					t.Errorf("the analysis is invalid: %v", err)
				}
				if res.Analysis.ClassificationResult == classificationFallback || res.Analysis == singleCallFallback {
					t.Fatal("fell back instead of getting a real answer (see the warnings above)")
				}
				if res.Analysis.Category != tc.wantCategory {
					t.Errorf("category = %q, want %q", res.Analysis.Category, tc.wantCategory)
				}
			})
		}
	}
}
