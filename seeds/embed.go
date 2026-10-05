// Package seeds embeds the idempotent development seed scripts.
package seeds

import "embed"

//go:embed *.sql
var Files embed.FS
