package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func respond(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func newTestGroq(t *testing.T, handler http.HandlerFunc, opts ...GroqOption) *Groq {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	g, err := NewGroq("test-key", "test-model", append([]GroqOption{WithBaseURL(srv.URL)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGroqCompleteSendsOpenAIStyleRequest(t *testing.T) {
	var gotPath, gotAuth, gotContentType, gotMethod string
	var gotBody map[string]any

	g := newTestGroq(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotContentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		respond(w, 200, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	})

	got, err := g.Complete(context.Background(), "SYSTEM", "USER")
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"ok":true}` {
		t.Errorf("content = %q", got)
	}

	if gotMethod != http.MethodPost || gotPath != "/chat/completions" {
		t.Errorf("request = %s %s, want POST /chat/completions", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" || gotContentType != "application/json" {
		t.Errorf("headers: Authorization=%q Content-Type=%q", gotAuth, gotContentType)
	}
	if gotBody["model"] != "test-model" || gotBody["temperature"] != 0.1 {
		t.Errorf("model/temperature wrong: %v", gotBody)
	}
	if rf, _ := gotBody["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format = %v, want json_object", gotBody["response_format"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %v", gotBody["messages"])
	}
	for i, want := range []map[string]string{{"role": "system", "content": "SYSTEM"}, {"role": "user", "content": "USER"}} {
		m, _ := msgs[i].(map[string]any)
		if m["role"] != want["role"] || m["content"] != want["content"] {
			t.Errorf("message %d = %v, want %v", i, m, want)
		}
	}
}

func TestGroqCompleteHTTPErrors(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantMessage   string
		wantTransient bool
	}{
		{"rate limited", 429, `{"error":{"message":"Rate limit reached","type":"tokens"}}`, "Rate limit reached", true},
		{"server error", 500, `{"error":{"message":"boom"}}`, "boom", true},
		{"unavailable", 503, `upstream down`, "upstream down", true},
		{"bad key", 401, `{"error":{"message":"Invalid API Key"}}`, "Invalid API Key", false},
		{"bad request", 400, `{"error":{"message":"bad model"}}`, "bad model", false},
		{"forbidden", 403, `{}`, "Forbidden", false},
		{"not found", 404, ``, "Not Found", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newTestGroq(t, func(w http.ResponseWriter, r *http.Request) { respond(w, tt.status, tt.body) })

			_, err := g.Complete(context.Background(), "s", "u")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *APIError, got %T: %v", err, err)
			}
			if apiErr.StatusCode != tt.status || apiErr.Message != tt.wantMessage {
				t.Errorf("got %d %q, want %d %q", apiErr.StatusCode, apiErr.Message, tt.status, tt.wantMessage)
			}
			if apiErr.Transient() != tt.wantTransient || isTransient(err) != tt.wantTransient {
				t.Errorf("transient = %v, want %v", isTransient(err), tt.wantTransient)
			}
		})
	}
}

func TestGroqCompleteUnusableSuccessResponses(t *testing.T) {
	bodies := map[string]string{
		"not json":      `<html>gateway</html>`,
		"no choices":    `{"choices":[]}`,
		"empty content": `{"choices":[{"message":{"content":""}}]}`,
		"null content":  `{"choices":[{"message":{"content":null}}]}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			g := newTestGroq(t, func(w http.ResponseWriter, r *http.Request) { respond(w, 200, body) })
			_, err := g.Complete(context.Background(), "s", "u")
			if !errors.Is(err, ErrMalformedOutput) {
				t.Errorf("err = %v, want ErrMalformedOutput", err)
			}
		})
	}
}

func TestGroqCompleteNetworkFailuresAreTransient(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		g, err := NewGroq("k", "m", WithBaseURL(srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		srv.Close() // nothing is listening any more

		_, err = g.Complete(context.Background(), "s", "u")
		var netErr net.Error
		if !errors.As(err, &netErr) || !isTransient(err) {
			t.Errorf("err = %v (%T); want a transient net.Error", err, err)
		}
	})

	t.Run("client timeout", func(t *testing.T) {
		g := newTestGroq(t, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			respond(w, 200, `{}`)
		}, WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))

		_, err := g.Complete(context.Background(), "s", "u")
		if err == nil || !isTransient(err) {
			t.Errorf("a timeout should be transient, got %v", err)
		}
	})
}

func TestGroqCompleteHonorsContext(t *testing.T) {
	g := newTestGroq(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		respond(w, 200, `{}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := g.Complete(ctx, "s", "u")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestNewGroqValidation(t *testing.T) {
	if _, err := NewGroq("", "m"); err == nil {
		t.Error("an empty API key must be rejected")
	}
	if _, err := NewGroq("k", ""); err == nil {
		t.Error("an empty model must be rejected")
	}

	trimmed, err := NewGroq("k", "m", WithBaseURL("http://example.test/v1///"))
	if err != nil {
		t.Fatal(err)
	}
	if trimmed.baseURL != "http://example.test/v1" {
		t.Errorf("trailing slashes should be trimmed, got %q", trimmed.baseURL)
	}

	defaulted, err := NewGroq("k", "m")
	if err != nil {
		t.Fatal(err)
	}
	if defaulted.baseURL != DefaultGroqBaseURL {
		t.Errorf("default base URL = %q, want %q", defaulted.baseURL, DefaultGroqBaseURL)
	}
}

func TestAPIErrorLongBodyIsTruncated(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	err := newAPIError(502, long)
	if len(err.Message) > 210 {
		t.Errorf("message of %d bytes should have been truncated", len(err.Message))
	}
}

func TestGroqCompleteReadsRetryAfter(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"2", 2 * time.Second},
		{"1.5", 1500 * time.Millisecond},
		{" 30 ", 30 * time.Second},
		{"", 0},
		{"soon", 0},
		{"-5", 0},
		{"0", 0},
		{"NaN", 0},
		{"Inf", 0},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0}, // the HTTP-date form is not supported
	}
	for _, tt := range tests {
		t.Run("Retry-After "+tt.header, func(t *testing.T) {
			g := newTestGroq(t, func(w http.ResponseWriter, r *http.Request) {
				if tt.header != "" {
					w.Header().Set("Retry-After", tt.header)
				}
				respond(w, 429, `{"error":{"message":"Rate limit reached"}}`)
			})
			_, err := g.Complete(context.Background(), "s", "u")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *APIError, got %v", err)
			}
			if apiErr.RetryAfter != tt.want {
				t.Errorf("RetryAfter = %v, want %v", apiErr.RetryAfter, tt.want)
			}
		})
	}
}
