// Package migrations embeds the SQL schema migrations (ADR-0019).
//
// The same files are sqlc's schema source (see sqlc.yaml), so the database
// and the generated Go code can never disagree about the schema.
package migrations

import "embed"

// FS holds every *.sql migration.
//
//go:embed *.sql
var FS embed.FS
