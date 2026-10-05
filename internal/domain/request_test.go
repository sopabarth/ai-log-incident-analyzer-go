package domain

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRawLogEntryUnmarshal(t *testing.T) {
	const body = `{"service":"payments-api","environment":%q,"timestamp":%q,"raw_text":"boom"}`
	decode := func(env, ts string) (RawLogEntry, error) {
		var e RawLogEntry
		err := json.Unmarshal([]byte(fmt.Sprintf(body, env, ts)), &e)
		return e, err
	}

	t.Run("environment alias is normalized", func(t *testing.T) {
		e, err := decode("production", "2026-09-12T20:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		if e.Environment != EnvProd {
			t.Errorf("environment = %q, want %q", e.Environment, EnvProd)
		}
	})

	timestamps := map[string]time.Time{
		"2026-09-12T20:00:00Z":        time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		"2026-09-12T20:00:00.123456Z": time.Date(2026, 9, 12, 20, 0, 0, 123456000, time.UTC),
		"2026-09-12T22:00:00+02:00":   time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		"2026-09-10T12:31:00":         time.Date(2026, 9, 10, 12, 31, 0, 0, time.UTC), // no offset: read as UTC
		"2026-09-10 12:31:00":         time.Date(2026, 9, 10, 12, 31, 0, 0, time.UTC),
		"2026-09-10":                  time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range timestamps {
		t.Run("timestamp "+in, func(t *testing.T) {
			e, err := decode("dev", in)
			if err != nil {
				t.Fatal(err)
			}
			if !e.Timestamp.Equal(want) {
				t.Errorf("timestamp = %v, want %v", e.Timestamp, want)
			}
		})
	}

	rejected := map[string]string{
		"bad environment": `{"service":"s","environment":"banana","timestamp":"2026-09-12T20:00:00Z","raw_text":"x"}`,
		"bad timestamp":   `{"service":"s","environment":"dev","timestamp":"yesterday","raw_text":"x"}`,
		"missing field":   `{"service":"s","environment":"dev","timestamp":"2026-09-12T20:00:00Z"}`,
		"null field":      `{"service":"s","environment":"dev","timestamp":"2026-09-12T20:00:00Z","raw_text":null}`,
		"wrong type":      `{"service":5,"environment":"dev","timestamp":"2026-09-12T20:00:00Z","raw_text":"x"}`,
		"not an object":   `["service"]`,
	}
	for name, body := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			var e RawLogEntry
			if err := json.Unmarshal([]byte(body), &e); err == nil {
				t.Errorf("expected an error, decoded %+v", e)
			}
		})
	}

	t.Run("empty strings are allowed, only missing keys are not", func(t *testing.T) {
		var e RawLogEntry
		body := `{"service":"","environment":"dev","timestamp":"2026-09-12T20:00:00Z","raw_text":""}`
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
