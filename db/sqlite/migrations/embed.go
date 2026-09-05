package migrations

import "embed"

// Files contains the immutable, forward-only SQLite migrations.
//
//go:embed *.up.sql
var Files embed.FS
