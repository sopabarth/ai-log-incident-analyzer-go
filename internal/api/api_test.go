package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/incident"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeService returns canned results and remembers what it was asked.
type fakeService struct {
	record domain.IncidentRecord
	err    error
	panics bool

	got []domain.RawLogEntry
}

func (f *fakeService) Analyze(_ context.Context, e domain.RawLogEntry) (domain.IncidentRecord, error) {
	f.got = append(f.got, e)
	if f.panics {
		panic("kaboom")
	}
	return f.record, f.err
}

const validBody = `{"service":"payments-api","environment":"production","timestamp":"2026-09-10T12:31:00","raw_text":"boom"}`

func do(t *testing.T, svc Service, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewHandler(svc, discardLogger()).ServeHTTP(rec, req)
	return rec.Result()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func detail(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body map[string]string
	raw := readBody(t, resp)
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("error body %q is not a JSON object of strings: %v", raw, err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	return body["detail"]
}

func TestHealth(t *testing.T) {
	resp := do(t, &fakeService{}, http.MethodGet, "/health", "")
	if resp.StatusCode != 200 || strings.TrimSpace(readBody(t, resp)) != `{"status":"ok"}` {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestAnalyzeIncidentSuccess(t *testing.T) {
	id := uuid.New()
	want := domain.IncidentRecord{
		ID: &id, Service: "payments-api", Environment: domain.EnvProd,
		Timestamp:       time.Date(2026, 9, 10, 12, 31, 0, 0, time.UTC),
		RawTextHash:     "abc",
		OccurrenceCount: 1,
		Analysis: domain.IncidentAnalysis{
			ClassificationResult: domain.ClassificationResult{Category: domain.CategoryAuthFailure, RootCauseSummary: "x", Confidence: 0.9},
			PriorityResult:       domain.PriorityResult{Priority: domain.PriorityLow, PriorityReasoning: "y"},
		},
	}
	svc := &fakeService{record: want}

	resp := do(t, svc, http.MethodPost, "/analyze-incident", validBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, readBody(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	// The request was parsed leniently: alias environment, timestamp without an offset.
	if len(svc.got) != 1 {
		t.Fatalf("service called %d times", len(svc.got))
	}
	if got := svc.got[0]; got.Service != "payments-api" || got.Environment != domain.EnvProd ||
		!got.Timestamp.Equal(time.Date(2026, 9, 10, 12, 31, 0, 0, time.UTC)) || got.RawText != "boom" {
		t.Errorf("service received %+v", got)
	}

	// The response is the flat API shape.
	var m map[string]any
	if err := json.Unmarshal([]byte(readBody(t, resp)), &m); err != nil {
		t.Fatal(err)
	}
	analysis, _ := m["analysis"].(map[string]any)
	if m["id"] != id.String() || m["environment"] != "prod" || analysis["category"] != "auth_failure" ||
		analysis["priority"] != "low" || m["is_duplicate"] != false || m["duplicate_of_id"] != nil {
		t.Errorf("unexpected response: %v", m)
	}
}

func TestAnalyzeIncidentBadRequests(t *testing.T) {
	big := `{"service":"s","environment":"dev","timestamp":"2026-09-10","raw_text":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantDetail string // substring
	}{
		{"empty body", ``, 400, "not valid JSON"},
		{"not JSON", `hello`, 400, "not valid JSON"},
		{"truncated JSON", `{"service":"s"`, 400, "not valid JSON"},
		{"two objects", validBody + validBody, 400, "single JSON object"},
		{"trailing garbage", validBody + ` nope`, 400, ""},
		{"not an object", `["service"]`, 422, ""},
		{"missing field", `{"service":"s","environment":"dev","timestamp":"2026-09-10"}`, 422, "raw_text"},
		{"null field", `{"service":"s","environment":"dev","timestamp":"2026-09-10","raw_text":null}`, 422, "raw_text"},
		{"unknown environment", `{"service":"s","environment":"banana","timestamp":"2026-09-10","raw_text":"x"}`, 422, "banana"},
		{"bad timestamp", `{"service":"s","environment":"dev","timestamp":"yesterday","raw_text":"x"}`, 422, "yesterday"},
		{"wrong type", `{"service":5,"environment":"dev","timestamp":"2026-09-10","raw_text":"x"}`, 422, ""},
		{"too large", big, 413, "too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeService{}
			resp := do(t, svc, http.MethodPost, "/analyze-incident", tt.body)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if d := detail(t, resp); !strings.Contains(d, tt.wantDetail) || d == "" {
				t.Errorf("detail = %q, want it to contain %q", d, tt.wantDetail)
			}
			if len(svc.got) != 0 {
				t.Error("a rejected request must not reach the service")
			}
		})
	}
}

func TestAnalyzeIncidentServiceErrors(t *testing.T) {
	secret := "db password is hunter2"
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantDetail string
	}{
		{"empty text", incident.ErrEmptyText, 400, "no usable content"},
		{"analysis failed", fmt.Errorf("%w: %w", incident.ErrAnalysis, errors.New(secret)), 502, "language model"},
		{"timeout", fmt.Errorf("check: %w", context.DeadlineExceeded), 504, "timed out"},
		{"anything else", errors.New(secret), 500, "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := do(t, &fakeService{err: tt.err}, http.MethodPost, "/analyze-incident", validBody)
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			d := detail(t, resp)
			if !strings.Contains(d, tt.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", d, tt.wantDetail)
			}
			if strings.Contains(d, "hunter2") {
				t.Errorf("internal error text leaked to the client: %q", d)
			}
		})
	}

	t.Run("client went away", func(t *testing.T) {
		resp := do(t, &fakeService{err: context.Canceled}, http.MethodPost, "/analyze-incident", validBody)
		if resp.StatusCode != statusClientClosedRequest {
			t.Errorf("status = %d, want %d", resp.StatusCode, statusClientClosedRequest)
		}
	})
}

func TestRoutingAndMethods(t *testing.T) {
	svc := &fakeService{}
	if resp := do(t, svc, http.MethodGet, "/analyze-incident", ""); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /analyze-incident = %d, want 405", resp.StatusCode)
	} else if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "POST") {
		t.Errorf("Allow = %q", allow)
	}
	if resp := do(t, svc, http.MethodPost, "/health", ""); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /health = %d, want 405", resp.StatusCode)
	}
	if resp := do(t, svc, http.MethodGet, "/nope", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", resp.StatusCode)
	}
	if len(svc.got) != 0 {
		t.Error("none of these should have reached the service")
	}
}

func TestPanicBecomesA500(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	req := httptest.NewRequest(http.MethodPost, "/analyze-incident", strings.NewReader(validBody))
	rec := httptest.NewRecorder()
	NewHandler(&fakeService{panics: true}, logger).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "kaboom") {
		t.Errorf("panic value leaked to the client: %s", rec.Body)
	}
	out := logs.String()
	if !strings.Contains(out, "kaboom") || !strings.Contains(out, "panic in handler") {
		t.Errorf("the panic should be logged:\n%s", out)
	}
	if !strings.Contains(out, "status=500") {
		t.Errorf("the request log should show the 500 the panic produced:\n%s", out)
	}
}

func TestRequestsAreLogged(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	NewHandler(&fakeService{}, logger).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	out := logs.String()
	for _, want := range []string{"method=GET", "path=/health", "status=200", "duration="} {
		if !strings.Contains(out, want) {
			t.Errorf("request log %q is missing %q", out, want)
		}
	}
}
