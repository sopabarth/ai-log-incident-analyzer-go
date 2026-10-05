// Package migrations embeds the SQL migration files so the binary carries its
// own schema and needs nothing on disk at runtime.
package migrations

import "embed"

// FS holds the goose migration files (NNNNN_name.sql).
//
//go:embed *.sql
var FS embed.FS
