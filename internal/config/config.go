// Package config reads the service's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

// Config holds the settings the service runs with.
type Config struct {
	// DatabaseURL is a Postgres connection string (required).
	DatabaseURL string
	// DedupWindow is how long after an incident was last seen a repeat of the
	// same error counts as a duplicate rather than a new incident.
	DedupWindow time.Duration

	// GroqAPIKey authenticates against Groq. It is not checked here, because
	// commands that never call the model (migrate) don't need it; llm.NewGroq
	// rejects an empty key.
	GroqAPIKey string
	// GroqModel is the Groq model to ask.
	GroqModel string
	// TaskDecomposition makes the analysis two focused model calls (classify,
	// then prioritize) instead of one.
	TaskDecomposition bool
	// LLMMaxAttempts is how many times each model step may be tried before
	// falling back.
	LLMMaxAttempts int
	// LLMRetryBackoff is the base wait between attempts after a transient
	// provider error; the wait before retry n is n times this.
	LLMRetryBackoff time.Duration
}

const (
	defaultDedupWindowMinutes = 10
	defaultGroqModel          = "openai/gpt-oss-120b"
	defaultLLMMaxAttempts     = 3
	defaultLLMBackoffSeconds  = 0.5
)

// Load reads the configuration from the environment. Every problem found is
// reported at once rather than one per run.
func Load() (Config, error) {
	var errs []error
	collect := func(err error) { errs = append(errs, err) }

	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		GroqAPIKey:  os.Getenv("GROQ_API_KEY"),
		GroqModel:   stringFromEnv("GROQ_MODEL", defaultGroqModel),
	}
	if cfg.DatabaseURL == "" {
		collect(errors.New("DATABASE_URL is required"))
	}

	minutes, err := intFromEnv("DEDUP_WINDOW_MINUTES", defaultDedupWindowMinutes)
	switch {
	case err != nil:
		collect(err)
	case minutes <= 0:
		collect(fmt.Errorf("DEDUP_WINDOW_MINUTES must be positive, got %d", minutes))
	}
	cfg.DedupWindow = time.Duration(minutes) * time.Minute

	if cfg.TaskDecomposition, err = boolFromEnv("TASK_DECOMPOSITION", false); err != nil {
		collect(err)
	}

	cfg.LLMMaxAttempts, err = intFromEnv("LLM_MAX_ATTEMPTS", defaultLLMMaxAttempts)
	switch {
	case err != nil:
		collect(err)
	case cfg.LLMMaxAttempts < 1:
		collect(fmt.Errorf("LLM_MAX_ATTEMPTS must be at least 1, got %d", cfg.LLMMaxAttempts))
	}

	seconds, err := floatFromEnv("LLM_RETRY_BACKOFF_SECONDS", defaultLLMBackoffSeconds)
	switch {
	case err != nil:
		collect(err)
	case seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0):
		collect(fmt.Errorf("LLM_RETRY_BACKOFF_SECONDS must be a non-negative number, got %v", seconds))
	}
	cfg.LLMRetryBackoff = time.Duration(seconds * float64(time.Second))

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// stringFromEnv returns the named variable, or def if it is unset or empty.
func stringFromEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// intFromEnv returns the integer in the named variable, or def if it is unset
// or empty.
func intFromEnv(name string, def int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", name, raw)
	}
	return n, nil
}

// floatFromEnv returns the number in the named variable, or def if it is unset
// or empty.
func floatFromEnv(name string, def float64) (float64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", name, raw)
	}
	return f, nil
}

// boolFromEnv returns the boolean in the named variable (true/false, 1/0, t/f,
// in any case), or def if it is unset or empty. Anything else is an error
// rather than silently meaning false, so a typo in TASK_DECOMPOSITION is noticed.
func boolFromEnv(name string, def bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", name, raw)
	}
	return b, nil
}
