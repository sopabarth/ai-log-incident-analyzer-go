// Package config reads the service's settings from environment variables.
package config

import (
	"errors"
	"fmt"
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
}

const defaultDedupWindowMinutes = 10

// Load reads the configuration from the environment. Every problem found is
// reported at once rather than one per run.
func Load() (Config, error) {
	var errs []error

	cfg := Config{DatabaseURL: os.Getenv("DATABASE_URL")}
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}

	minutes, err := intFromEnv("DEDUP_WINDOW_MINUTES", defaultDedupWindowMinutes)
	if err != nil {
		errs = append(errs, err)
	} else if minutes <= 0 {
		errs = append(errs, fmt.Errorf("DEDUP_WINDOW_MINUTES must be positive, got %d", minutes))
	}
	cfg.DedupWindow = time.Duration(minutes) * time.Minute

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
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
