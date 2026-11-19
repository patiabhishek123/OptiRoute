// Package migrations embeds the goose SQL migrations so the API binary can apply them itself
// (`api migrate`, or MIGRATE_ON_START=true). See docs/ARCHITECTURE.md §8a.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
