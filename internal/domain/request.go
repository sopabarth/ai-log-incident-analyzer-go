package domain

import (
	"fmt"
	"time"
)

// RawLogEntry is the POST /analyze-incident request body.
type RawLogEntry struct {
	Service     string      `json:"service"`
	Environment Environment `json:"environment"`
	Timestamp   time.Time   `json:"timestamp"`
	RawText     string      `json:"raw_text"`
}

// UnmarshalJSON requires all four fields (an empty string is fine, a missing
// key is not) and parses environment and timestamp leniently: environment
// accepts aliases, and timestamp accepts datetimes without a UTC offset
// (read as UTC) and plain dates.
func (e *RawLogEntry) UnmarshalJSON(b []byte) error {
	var wire struct {
		Service     string `json:"service"`
		Environment string `json:"environment"`
		Timestamp   string `json:"timestamp"`
		RawText     string `json:"raw_text"`
	}
	if err := decodeRequired(b, &wire, "service", "environment", "timestamp", "raw_text"); err != nil {
		return err
	}
	env, ok := ParseEnvironment(wire.Environment)
	if !ok {
		return fmt.Errorf("invalid environment %q, expected one of %v", wire.Environment, environments)
	}
	ts, err := parseTimestamp(wire.Timestamp)
	if err != nil {
		return err
	}
	*e = RawLogEntry{Service: wire.Service, Environment: env, Timestamp: ts, RawText: wire.RawText}
	return nil
}

var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	time.DateOnly,
}

func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q, expected an ISO 8601 datetime", s)
}
