package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr []string // substrings that must all appear in the error
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": "postgres://x"},
			want: Config{DatabaseURL: "postgres://x", DedupWindow: 10 * time.Minute},
		},
		{
			name: "custom window",
			env:  map[string]string{"DATABASE_URL": "postgres://x", "DEDUP_WINDOW_MINUTES": "3"},
			want: Config{DatabaseURL: "postgres://x", DedupWindow: 3 * time.Minute},
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
			name:    "all problems are reported together",
			env:     map[string]string{"DEDUP_WINDOW_MINUTES": "-1"},
			wantErr: []string{"DATABASE_URL is required", "DEDUP_WINDOW_MINUTES must be positive"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Start from a clean slate: t.Setenv restores values afterwards.
			t.Setenv("DATABASE_URL", "")
			t.Setenv("DEDUP_WINDOW_MINUTES", "")
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
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
