package parser

import (
	"encoding/json"
	"os"
	"testing"
)

type goldenCase struct {
	Name       string `json:"name"`
	Raw        string `json:"raw"`
	MaxFrames  int    `json:"max_frames"`
	Normalized string `json:"normalized"`
	Hash       string `json:"hash"`
}

// TestMatchesPythonReference checks the port against output produced by the
// Python implementation: the 41 synthetic logs plus ordinary edge cases (CRLF,
// truncation, empty input, ...). Same normalized text and SHA-256 is what lets
// the Go and Python services dedup against each other on a shared database.
func TestMatchesPythonReference(t *testing.T) {
	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse golden file: %v", err)
	}
	if len(cases) < 41 {
		t.Fatalf("golden file looks truncated: only %d cases", len(cases))
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got := NormalizeRawText(c.Raw, c.MaxFrames)
			if got != c.Normalized {
				t.Errorf("normalized text differs from Python\n got: %q\nwant: %q", got, c.Normalized)
			}
			if h := ComputeErrorHash(got); h != c.Hash {
				t.Errorf("hash differs from Python\n got: %s\nwant: %s", h, c.Hash)
			}
		})
	}
}

func TestNormalizeRawText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		max  int
		want string
	}{
		{
			name: "empty input",
			raw:  "",
			want: "",
		},
		{
			name: "only whitespace",
			raw:  "  \n\t\n",
			want: "",
		},
		{
			name: "messages first, then frames",
			raw:  "  at a.B.c(B.java:1)\nboom happened\n",
			max:  5,
			want: "boom happened\nat a.B.c(B.java:1)",
		},
		{
			name: "windows line endings",
			raw:  "boom\r\n  at a.B.c(B.java:1)\r\n",
			max:  5,
			want: "boom\nat a.B.c(B.java:1)",
		},
		{
			name: "python source echo under a File frame is dropped",
			raw:  "Traceback (most recent call last):\n  File \"a.py\", line 1, in f\n    x = 1\nKeyError: 'k'",
			max:  5,
			want: "Traceback (most recent call last):\nKeyError: 'k'\nFile \"a.py\", line 1, in f",
		},
		{
			name: "an unindented line after a File frame is a message",
			raw:  "File \"a.py\", line 1, in f\nKeyError: 'k'",
			max:  5,
			want: "KeyError: 'k'\nFile \"a.py\", line 1, in f",
		},
		{
			name: "frames beyond the limit are replaced by a note",
			raw:  "err\nat a.B.c(B.java:1)\nat a.B.d(B.java:2)\nat a.B.e(B.java:3)",
			max:  2,
			want: "err\nat a.B.c(B.java:1)\nat a.B.d(B.java:2)\n... (1 more frames truncated)",
		},
		{
			name: "zero frames keeps only messages",
			raw:  "err\nat a.B.c(B.java:1)",
			max:  0,
			want: "err\n... (1 more frames truncated)",
		},
		{
			name: "negative limit behaves like zero",
			raw:  "err\nat a.B.c(B.java:1)",
			max:  -3,
			want: "err\n... (1 more frames truncated)",
		},
		{
			name: "limit larger than the frame count",
			raw:  "err\nat a.B.c(B.java:1)",
			max:  50,
			want: "err\nat a.B.c(B.java:1)",
		},
		{
			name: "go file:line is a frame without a leading 'at'",
			raw:  "oops\n\t/app/search/index.go:203 +0x1b",
			max:  5,
			want: "oops\n/app/search/index.go:203 +0x1b",
		},
		{
			name: "'at' only counts as a frame when followed by whitespace",
			raw:  "attempt failed\nat",
			max:  5,
			want: "attempt failed\nat",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeRawText(tt.raw, tt.max); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputeErrorHash(t *testing.T) {
	// echo -n "hello" | sha256sum
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got := ComputeErrorHash("hello"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if ComputeErrorHash("x") == ComputeErrorHash("y") {
		t.Error("different inputs must not share a hash")
	}
}

func BenchmarkNormalizeRawText(b *testing.B) {
	raw := "Traceback (most recent call last):\n" +
		"  File \"app/db/session.py\", line 42, in get_connection\n" +
		"    conn = pool.acquire(timeout=5)\n" +
		"  File \"app/services/payment.py\", line 88, in charge_card\n" +
		"    with db.session() as session:\n" +
		"psycopg2.OperationalError: timeout expired\n"
	for b.Loop() {
		NormalizeRawText(raw, DefaultMaxFrames)
	}
}
