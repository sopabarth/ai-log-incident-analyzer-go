// Package parser normalizes raw log/stack-trace text before it is sent to the
// LLM and hashed for dedup.
//
// A trace is cut down to the part that carries signal - the error message
// plus the top few frames - so tokens aren't spent on 300-line traces whose
// last 280 lines are framework internals. The normalized text is also what
// gets hashed, so two occurrences of the same error hash the same even if
// timestamps or spacing differ.
//
// This is a port of app/parser.py from the Python service, and for ordinary
// logs the two produce the same text and the same hash (see the golden test).
// It uses Go's standard notion of whitespace and line breaks, though, so input
// with exotic characters (form feeds, NEL, a lone '\r') can normalize slightly
// differently than in Python.
package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultMaxFrames is how many stack-frame lines survive truncation.
const DefaultMaxFrames = 5

// pyFilePrefix starts a Python traceback frame: File "x.py", line 12, in f
const pyFilePrefix = `File "`

// frameLine matches lines that look like a stack frame, across the languages
// we expect: Python (File "x.py", line 12), Java/Kotlin/JS (at pkg.Class.m),
// and Go (goroutine 1 [running]: / /path/file.go:20 +0x1b).
var frameLine = regexp.MustCompile(
	`^(?:at\s|` + pyFilePrefix + `|goroutine\s|\S+\.(?:java|go|py|js|kt):\d+)`,
)

// NormalizeRawText reduces a raw log blob to its non-frame lines (the error
// message and context) followed by at most maxFrames stack-frame lines, with a
// note when frames were dropped. Messages always survive; only frames are
// truncated, since the frames closest to the failure carry the diagnosis.
//
// In a Python traceback the source line echoed under a `File "...", line N`
// frame (conn = pool.acquire(...)) just repeats what the frame already points
// at, so it is dropped rather than kept as if it were a message.
func NormalizeRawText(rawText string, maxFrames int) string {
	var messages, frames []string
	afterPyFrame := false // the previous kept line was a Python `File "..."` frame

	for line := range strings.Lines(strings.Trim(rawText, "\n")) {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		switch {
		case frameLine.MatchString(text):
			frames = append(frames, text)
			afterPyFrame = strings.HasPrefix(text, pyFilePrefix)
			continue
		case afterPyFrame && isIndented(line):
			afterPyFrame = false
			continue
		}
		afterPyFrame = false
		messages = append(messages, text)
	}

	kept := frames[:min(max(maxFrames, 0), len(frames))]
	lines := slices.Concat(messages, kept)
	if dropped := len(frames) - len(kept); dropped > 0 {
		lines = append(lines, fmt.Sprintf("... (%d more frames truncated)", dropped))
	}
	return strings.Join(lines, "\n")
}

// isIndented reports whether the line starts with whitespace.
func isIndented(line string) bool {
	r, _ := utf8.DecodeRuneInString(line)
	return unicode.IsSpace(r)
}

// ComputeErrorHash returns the hex SHA-256 of the normalized text (not of the
// raw input). The dedup layer keys on it.
func ComputeErrorHash(normalizedText string) string {
	sum := sha256.Sum256([]byte(normalizedText))
	return hex.EncodeToString(sum[:])
}
