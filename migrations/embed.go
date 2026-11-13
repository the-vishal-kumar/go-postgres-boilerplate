// Package migrations embeds the SQL files so the binary can apply them
// without the source tree being present at run time.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
