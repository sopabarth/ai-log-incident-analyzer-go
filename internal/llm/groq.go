package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultGroqBaseURL is Groq's OpenAI-compatible API root.
	DefaultGroqBaseURL = "https://api.groq.com/openai/v1"
	// DefaultGroqModel is the model the Python service uses too.
	DefaultGroqModel = "openai/gpt-oss-120b"

	// A low temperature keeps classification stable from run to run.
	temperature = 0.1

	defaultTimeout   = 60 * time.Second
	maxResponseBytes = 1 << 20
)

// APIError is an HTTP-level failure from the provider.
type APIError struct {
	StatusCode int
	Message    string
	// RetryAfter is how long the provider asked us to wait before trying
	// again (its Retry-After header, sent on rate limiting); zero if it didn't.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Message)
}

// Transient reports whether the failure is worth retrying: the provider is
// rate limiting us (429) or having a server-side problem (5xx). Anything else
// (401 bad key, 400 bad request, 403, ...) will fail the same way every time.
func (e *APIError) Transient() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// Groq is a Completer backed by Groq's chat completions API, called over plain
// HTTP. Groq speaks the OpenAI wire format, so the request is the familiar
// {model, messages, temperature, response_format} and the reply is read from
// choices[0].message.content.
type Groq struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// GroqOption customizes a Groq client.
type GroqOption func(*Groq)

// WithBaseURL points the client at a different API root (used by tests).
func WithBaseURL(url string) GroqOption {
	return func(g *Groq) { g.baseURL = strings.TrimRight(url, "/") }
}

// WithHTTPClient replaces the default HTTP client (60 second timeout).
func WithHTTPClient(c *http.Client) GroqOption {
	return func(g *Groq) { g.client = c }
}

// NewGroq returns a client that authenticates with apiKey and asks model.
func NewGroq(apiKey, model string, opts ...GroqOption) (*Groq, error) {
	if apiKey == "" {
		return nil, errors.New("groq: GROQ_API_KEY is not set")
	}
	if model == "" {
		return nil, errors.New("groq: model is empty")
	}
	g := &Groq{
		apiKey:  apiKey,
		model:   model,
		baseURL: DefaultGroqBaseURL,
		client:  &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(g)
	}
	return g, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Complete implements Completer.
func (g *Groq) Complete(ctx context.Context, system, user string) (string, error) {
	reqBody := chatRequest{
		Model: g.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: temperature,
	}
	reqBody.ResponseFormat.Type = "json_object"

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("groq: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("groq: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("groq: send request: %w", err) // a *url.Error: a net.Error, so retryable
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("groq: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := newAPIError(resp.StatusCode, body)
		apiErr.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		return "", fmt.Errorf("groq: %w", apiErr)
	}

	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("%w: groq response is not valid JSON: %v", ErrMalformedOutput, err)
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("%w: groq response has no message content", ErrMalformedOutput)
	}
	return out.Choices[0].Message.Content, nil
}

// parseRetryAfter reads a Retry-After header given as a number of seconds,
// which is how Groq sends it (fractions are fine). The HTTP-date form is not
// handled. Zero means absent or unusable.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// newAPIError builds an APIError from a non-2xx response, preferring the
// provider's own message ({"error": {"message": ...}}) over the raw body.
func newAPIError(status int, body []byte) *APIError {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var msg string
	if json.Unmarshal(body, &parsed) == nil {
		// A JSON body without a message ({}) is no more informative than
		// the status line, so don't echo it back.
		msg = parsed.Error.Message
	} else {
		// Not the provider's JSON (an HTML error page from a proxy, say):
		// the start of the raw body is the best clue there is.
		msg = strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = strings.ToValidUTF8(msg[:200], "") + "..."
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{StatusCode: status, Message: msg}
}
