package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/api"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/incident"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/llm"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/store/storetest"
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// modelAnswer is a complete single-call reply.
const modelAnswer = `{"category":"database_timeout","root_cause_summary":"connection pool exhausted",` +
	`"priority":"critical","priority_reasoning":"payments are blocked","confidence":0.95,"needs_human_review":false}`

// countingModel is a stand-in for the language model that counts its calls.
type countingModel struct{ calls atomic.Int32 }

func (m *countingModel) Complete(context.Context, string, string) (string, error) {
	m.calls.Add(1)
	return modelAnswer, nil
}

// TestEndToEnd runs the real stack - HTTP handler, incident service, llm
// analyzer, Postgres - with only the model faked, and checks the property the
// whole project exists for: a repeat of an error never reaches the model.
func TestEndToEnd(t *testing.T) {
	model := &countingModel{}
	analyzer, err := llm.NewAnalyzer(model, llm.Options{MaxAttempts: 1, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	svc := incident.NewService(storetest.New(t), analyzer, time.Minute)
	srv := httptest.NewServer(api.NewHandler(svc, quiet()))
	t.Cleanup(srv.Close)

	post := func(t *testing.T, body string) (int, domain.IncidentRecord) {
		t.Helper()
		resp, err := http.Post(srv.URL+"/analyze-incident", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var rec domain.IncidentRecord
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, rec
	}
	request := func(raw string) string {
		b, err := json.Marshal(map[string]string{
			"service": "payments-api", "environment": "production",
			"timestamp": "2026-09-10T12:31:00", "raw_text": raw,
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	const trace = "Traceback (most recent call last):\n  File \"a.py\", line 1, in f\n    x = 1\nTimeoutError: timeout expired"

	// 1. A new error is analysed and stored.
	status, first := post(t, request(trace))
	if status != http.StatusOK {
		t.Fatalf("first request: status %d", status)
	}
	if first.ID == nil || first.IsDuplicate || first.DuplicateOfID != nil || first.OccurrenceCount != 1 {
		t.Errorf("first response should be a fresh incident: %+v", first)
	}
	if first.Environment != domain.EnvProd {
		t.Errorf("the alias 'production' should be stored as %q, got %q", domain.EnvProd, first.Environment)
	}
	if first.Analysis.Category != domain.CategoryDatabaseTimeout || first.Analysis.Priority != domain.PriorityCritical {
		t.Errorf("analysis = %+v", first.Analysis)
	}
	if !first.Timestamp.Equal(time.Date(2026, 9, 10, 12, 31, 0, 0, time.UTC)) {
		t.Errorf("a timestamp without an offset should be read as UTC, got %v", first.Timestamp)
	}
	if first.LLMLatencyMS == nil || first.LLMRetryCount != 0 {
		t.Errorf("model stats missing: %+v", first)
	}
	if n := model.calls.Load(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}

	// 2. The same error again - and a cosmetically different copy of it
	//    (CRLF line endings, surrounding blank lines) - are duplicates.
	cosmetic := "\r\n" + strings.ReplaceAll(trace, "\n", "\r\n") + "\r\n\r\n"
	for i, raw := range []string{trace, cosmetic} {
		status, dup := post(t, request(raw))
		if status != http.StatusOK {
			t.Fatalf("repeat %d: status %d", i, status)
		}
		if !dup.IsDuplicate || dup.DuplicateOfID == nil || *dup.DuplicateOfID != *first.ID || *dup.ID != *first.ID {
			t.Errorf("repeat %d should point at the first incident: %+v", i, dup)
		}
		if want := i + 2; dup.OccurrenceCount != want {
			t.Errorf("repeat %d: occurrence_count = %d, want %d", i, dup.OccurrenceCount, want)
		}
		if dup.Analysis != first.Analysis {
			t.Errorf("repeat %d should reuse the stored analysis", i)
		}
	}
	if n := model.calls.Load(); n != 1 {
		t.Errorf("model calls = %d after three identical errors, want still 1", n)
	}

	// 3. A different error is a new incident and does call the model.
	status, other := post(t, request("PermissionError: [Errno 13] cannot write /var/log/app.log"))
	if status != http.StatusOK || other.IsDuplicate || *other.ID == *first.ID {
		t.Errorf("a different error must be a new incident: status %d, %+v", status, other)
	}
	if n := model.calls.Load(); n != 2 {
		t.Errorf("model calls = %d, want 2", n)
	}

	// 4. A log with nothing in it is rejected without calling anything.
	if status, _ := post(t, request("  \n\t\n")); status != http.StatusBadRequest {
		t.Errorf("empty log: status %d, want 400", status)
	}
	if n := model.calls.Load(); n != 2 {
		t.Errorf("model calls = %d, want still 2", n)
	}
}

// A burst of identical errors arriving at once must still be counted exactly:
// every request after the first finds the incident and bumps it.
func TestEndToEndConcurrentDuplicates(t *testing.T) {
	model := &countingModel{}
	analyzer, err := llm.NewAnalyzer(model, llm.Options{MaxAttempts: 1, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	svc := incident.NewService(storetest.New(t), analyzer, time.Minute)
	srv := httptest.NewServer(api.NewHandler(svc, quiet()))
	t.Cleanup(srv.Close)

	body := `{"service":"s","environment":"prod","timestamp":"2026-09-10","raw_text":"ValueError: burst"}`
	post := func() domain.IncidentRecord {
		resp, err := http.Post(srv.URL+"/analyze-incident", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Error(err)
			return domain.IncidentRecord{}
		}
		defer func() { _ = resp.Body.Close() }()
		var rec domain.IncidentRecord
		if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
			t.Error(err)
		}
		return rec
	}

	// Seed the incident, so the burst below is all duplicates.
	post()

	const burst = 30
	counts := make(chan int, burst)
	for range burst {
		go func() { counts <- post().OccurrenceCount }()
	}
	var seen []int
	for range burst {
		seen = append(seen, <-counts)
	}
	slices.Sort(seen)
	for i, c := range seen {
		if want := i + 2; c != want { // 2..31: each duplicate got its own count, none lost or repeated
			t.Fatalf("occurrence counts = %v; expected 2..%d each exactly once", seen, burst+1)
		}
	}
	if n := model.calls.Load(); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
}
