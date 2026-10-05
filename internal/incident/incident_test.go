package incident

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/llm"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/parser"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/store"
)

type fakeStore struct {
	duplicate    *domain.IncidentRecord // returned by RecordDuplicate when set; otherwise ErrNotFound
	duplicateErr error                  // returned by RecordDuplicate instead, when set
	createErr    error

	lookups []lookup
	created []store.NewIncident
}

type lookup struct {
	hash   string
	window time.Duration
}

func (f *fakeStore) RecordDuplicate(_ context.Context, hash string, window time.Duration) (domain.IncidentRecord, error) {
	f.lookups = append(f.lookups, lookup{hash, window})
	switch {
	case f.duplicateErr != nil:
		return domain.IncidentRecord{}, f.duplicateErr
	case f.duplicate != nil:
		return *f.duplicate, nil
	}
	return domain.IncidentRecord{}, store.ErrNotFound
}

func (f *fakeStore) Create(_ context.Context, in store.NewIncident) (domain.IncidentRecord, error) {
	f.created = append(f.created, in)
	if f.createErr != nil {
		return domain.IncidentRecord{}, f.createErr
	}
	id := uuid.New()
	return domain.IncidentRecord{
		ID: &id, Service: in.Service, Environment: in.Environment, Timestamp: in.Timestamp,
		RawTextHash: in.RawTextHash, Analysis: in.Analysis, OccurrenceCount: 1,
		LLMRetryCount: in.LLMRetryCount, LLMLatencyMS: &in.LLMLatencyMS,
	}, nil
}

type fakeAnalyzer struct {
	result llm.Result
	err    error

	calls []analyzeCall
}

type analyzeCall struct {
	service string
	env     domain.Environment
	text    string
}

func (f *fakeAnalyzer) Analyze(_ context.Context, service string, env domain.Environment, text string) (llm.Result, error) {
	f.calls = append(f.calls, analyzeCall{service, env, text})
	return f.result, f.err
}

var sampleAnalysis = domain.IncidentAnalysis{
	ClassificationResult: domain.ClassificationResult{
		Category: domain.CategoryDatabaseTimeout, RootCauseSummary: "pool exhausted", Confidence: 0.95,
	},
	PriorityResult: domain.PriorityResult{Priority: domain.PriorityCritical, PriorityReasoning: "payments blocked"},
}

func entry(raw string) domain.RawLogEntry {
	return domain.RawLogEntry{
		Service:     "payments-api",
		Environment: domain.EnvProd,
		Timestamp:   time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		RawText:     raw,
	}
}

const trace = "Traceback (most recent call last):\n  File \"a.py\", line 1, in f\n    x = 1\nTimeoutError: timeout expired"

func TestNewIncidentIsAnalysedAndSaved(t *testing.T) {
	st := &fakeStore{}
	an := &fakeAnalyzer{result: llm.Result{Analysis: sampleAnalysis, Latency: 1234 * time.Millisecond, RetryCount: 2}}
	svc := NewService(st, an, 10*time.Minute)

	rec, err := svc.Analyze(context.Background(), entry(trace))
	if err != nil {
		t.Fatal(err)
	}

	normalized := parser.NormalizeRawText(trace, parser.DefaultMaxFrames)
	wantHash := parser.ComputeErrorHash(normalized)

	if len(st.lookups) != 1 || st.lookups[0] != (lookup{wantHash, 10 * time.Minute}) {
		t.Errorf("dedup lookup = %+v, want one lookup of the normalized-text hash within the window", st.lookups)
	}
	if len(an.calls) != 1 || an.calls[0] != (analyzeCall{"payments-api", domain.EnvProd, normalized}) {
		t.Errorf("analyzer calls = %+v; it must get the normalized text, not the raw log", an.calls)
	}
	if len(st.created) != 1 {
		t.Fatalf("created %d incidents, want 1", len(st.created))
	}
	got := st.created[0]
	want := store.NewIncident{
		Service: "payments-api", Environment: domain.EnvProd, Timestamp: time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		RawTextHash: wantHash, Analysis: sampleAnalysis, LLMRetryCount: 2, LLMLatencyMS: 1234,
	}
	if got != want {
		t.Errorf("saved %+v\nwant  %+v", got, want)
	}
	if rec.ID == nil || rec.IsDuplicate || rec.Analysis != sampleAnalysis {
		t.Errorf("returned record = %+v", rec)
	}
}

func TestDuplicateSkipsTheModel(t *testing.T) {
	id := uuid.New()
	existing := domain.IncidentRecord{ID: &id, IsDuplicate: true, DuplicateOfID: &id, OccurrenceCount: 7, Analysis: sampleAnalysis}
	st := &fakeStore{duplicate: &existing}
	an := &fakeAnalyzer{}
	svc := NewService(st, an, time.Minute)

	rec, err := svc.Analyze(context.Background(), entry(trace))
	if err != nil {
		t.Fatal(err)
	}
	if len(an.calls) != 0 || len(st.created) != 0 {
		t.Errorf("a duplicate must not call the model (%d calls) or create anything (%d)", len(an.calls), len(st.created))
	}
	if !rec.IsDuplicate || rec.OccurrenceCount != 7 || *rec.ID != id {
		t.Errorf("returned record = %+v", rec)
	}
}

// Cosmetic differences in the raw log must not defeat dedup: it is the
// normalized text that is hashed.
func TestCosmeticDifferencesHashTheSame(t *testing.T) {
	st := &fakeStore{}
	svc := NewService(st, &fakeAnalyzer{result: llm.Result{Analysis: sampleAnalysis}}, time.Minute)

	variants := []string{
		trace,
		"\n\n" + trace + "\n\n",
		"Traceback (most recent call last):\r\n  File \"a.py\", line 1, in f\r\n    x = 1\r\nTimeoutError: timeout expired\r\n",
	}
	for _, raw := range variants {
		if _, err := svc.Analyze(context.Background(), entry(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(st.lookups); i++ {
		if st.lookups[i].hash != st.lookups[0].hash {
			t.Errorf("variant %d hashed differently from the first", i)
		}
	}
}

func TestEmptyTextIsRejectedBeforeAnyWork(t *testing.T) {
	for _, raw := range []string{"", "   \n\t\n"} {
		st, an := &fakeStore{}, &fakeAnalyzer{}
		_, err := NewService(st, an, time.Minute).Analyze(context.Background(), entry(raw))
		if !errors.Is(err, ErrEmptyText) {
			t.Errorf("%q: err = %v, want ErrEmptyText", raw, err)
		}
		if len(st.lookups) != 0 || len(an.calls) != 0 {
			t.Errorf("%q: nothing should have been looked up or analysed", raw)
		}
	}
}

func TestErrors(t *testing.T) {
	boom := errors.New("boom")

	t.Run("a failing duplicate lookup is not mistaken for 'not found'", func(t *testing.T) {
		st := &fakeStore{duplicateErr: boom}
		an := &fakeAnalyzer{}
		_, err := NewService(st, an, time.Minute).Analyze(context.Background(), entry(trace))
		if !errors.Is(err, boom) || errors.Is(err, ErrAnalysis) {
			t.Errorf("err = %v, want the store error", err)
		}
		if len(an.calls) != 0 {
			t.Error("the model must not be called when the dedup lookup itself failed")
		}
	})

	t.Run("analysis failures are marked as such and nothing is saved", func(t *testing.T) {
		st := &fakeStore{}
		an := &fakeAnalyzer{err: boom}
		_, err := NewService(st, an, time.Minute).Analyze(context.Background(), entry(trace))
		if !errors.Is(err, ErrAnalysis) || !errors.Is(err, boom) {
			t.Errorf("err = %v, want ErrAnalysis wrapping the cause", err)
		}
		if len(st.created) != 0 {
			t.Error("a failed analysis must not be stored")
		}
	})

	t.Run("a context ending during analysis is not an analysis failure", func(t *testing.T) {
		for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
			an := &fakeAnalyzer{err: ctxErr}
			_, err := NewService(&fakeStore{}, an, time.Minute).Analyze(context.Background(), entry(trace))
			if !errors.Is(err, ctxErr) || errors.Is(err, ErrAnalysis) {
				t.Errorf("err = %v, want plain %v", err, ctxErr)
			}
		}
	})

	t.Run("save failures are reported", func(t *testing.T) {
		st := &fakeStore{createErr: boom}
		an := &fakeAnalyzer{result: llm.Result{Analysis: sampleAnalysis}}
		_, err := NewService(st, an, time.Minute).Analyze(context.Background(), entry(trace))
		if !errors.Is(err, boom) || errors.Is(err, ErrAnalysis) {
			t.Errorf("err = %v, want the store error", err)
		}
	})
}
