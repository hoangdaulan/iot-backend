// Package migrations embeds the schema migrations, applied in file-name order.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
