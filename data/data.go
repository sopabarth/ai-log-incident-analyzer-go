// Package data holds the labeled synthetic logs the eval scores the pipeline
// against.
package data

import _ "embed"

// SyntheticLogs is synthetic_logs.json: 41 hand-labeled error logs (raw text
// plus the expected category, priority and needs_human_review) covering every
// category across Python, Java, Go and generic log formats.
//
// It is a copy of data/synthetic_logs.json from the Python project, which
// generates it, so both implementations are scored against identical ground
// truth. To refresh it, copy the file over.
//
//go:embed synthetic_logs.json
var SyntheticLogs []byte
