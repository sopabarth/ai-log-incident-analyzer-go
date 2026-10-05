package config

import (
	"strings"
	"testing"
	"time"
)

// allVars is every variable Load reads; each test starts with them all cleared.
var allVars = []string{
	"DATABASE_URL", "DEDUP_WINDOW_MINUTES", "GROQ_API_KEY", "GROQ_MODEL",
	"TASK_DECOMPOSITION", "LLM_MAX_ATTEMPTS", "LLM_RETRY_BACKOFF_SECONDS",
}

func TestLoad(t *testing.T) {
	defaults := Config{
		DatabaseURL:       "postgres://x",
		DedupWindow:       10 * time.Minute,
		GroqModel:         "openai/gpt-oss-120b",
		TaskDecomposition: false,
		LLMMaxAttempts:    3,
		LLMRetryBackoff:   500 * time.Millisecond,
	}
	with := func(mutate func(*Config)) Config {
		c := defaults
		mutate(&c)
		return c
	}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr []string // substrings that must all appear in the error
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": "postgres://x"},
			want: defaults,
		},
		{
			name: "everything set",
			env: map[string]string{
				"DATABASE_URL": "postgres://x", "DEDUP_WINDOW_MINUTES": "3", "GROQ_API_KEY": "gsk_test",
				"GROQ_MODEL": "some/model", "TASK_DECOMPOSITION": "true", "LLM_MAX_ATTEMPTS": "5",
				"LLM_RETRY_BACKOFF_SECONDS": "1.5",
			},
			want: Config{
				DatabaseURL: "postgres://x", DedupWindow: 3 * time.Minute, GroqAPIKey: "gsk_test",
				GroqModel: "some/model", TaskDecomposition: true, LLMMaxAttempts: 5, LLMRetryBackoff: 1500 * time.Millisecond,
			},
		},
		{
			name: "zero backoff is allowed",
			env:  map[string]string{"DATABASE_URL": "postgres://x", "LLM_RETRY_BACKOFF_SECONDS": "0"},
			want: with(func(c *Config) { c.LLMRetryBackoff = 0 }),
		},
		{
			name: "bool spellings",
			env:  map[string]string{"DATABASE_URL": "postgres://x", "TASK_DECOMPOSITION": "1"},
			want: with(func(c *Config) { c.TaskDecomposition = true }),
		},
		{
			name:    "database url is required",
			env:     map[string]string{},
			wantErr: []string{"DATABASE_URL is required"},
		},
		{
			name:    "window must be a number",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "DEDUP_WINDOW_MINUTES": "ten"},
			wantErr: []string{"DEDUP_WINDOW_MINUTES must be an integer"},
		},
		{
			name:    "window must be positive",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "DEDUP_WINDOW_MINUTES": "0"},
			wantErr: []string{"DEDUP_WINDOW_MINUTES must be positive"},
		},
		{
			name:    "a typo in the decomposition flag is an error, not false",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "TASK_DECOMPOSITION": "ture"},
			wantErr: []string{"TASK_DECOMPOSITION must be true or false"},
		},
		{
			name:    "attempts must be at least one",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "LLM_MAX_ATTEMPTS": "0"},
			wantErr: []string{"LLM_MAX_ATTEMPTS must be at least 1"},
		},
		{
			name:    "attempts must be a number",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "LLM_MAX_ATTEMPTS": "many"},
			wantErr: []string{"LLM_MAX_ATTEMPTS must be an integer"},
		},
		{
			name:    "backoff must not be negative",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "LLM_RETRY_BACKOFF_SECONDS": "-1"},
			wantErr: []string{"LLM_RETRY_BACKOFF_SECONDS must be a non-negative number"},
		},
		{
			name:    "backoff must be a number",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "LLM_RETRY_BACKOFF_SECONDS": "soon"},
			wantErr: []string{"LLM_RETRY_BACKOFF_SECONDS must be a number"},
		},
		{
			name:    "backoff must be finite",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "LLM_RETRY_BACKOFF_SECONDS": "Inf"},
			wantErr: []string{"LLM_RETRY_BACKOFF_SECONDS must be a non-negative number"},
		},
		{
			name: "all problems are reported together",
			env:  map[string]string{"DEDUP_WINDOW_MINUTES": "-1", "LLM_MAX_ATTEMPTS": "0", "TASK_DECOMPOSITION": "maybe"},
			wantErr: []string{
				"DATABASE_URL is required", "DEDUP_WINDOW_MINUTES must be positive",
				"LLM_MAX_ATTEMPTS must be at least 1", "TASK_DECOMPOSITION must be true or false",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv restores each variable when the test ends.
			for _, name := range allVars {
				t.Setenv(name, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := Load()
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected an error, got config %+v", got)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q should contain %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
