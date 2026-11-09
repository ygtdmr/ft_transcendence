// Package migrations embeds the Stats Service's versioned SQL files.
//
// Files are named NNNNNN_description.up.sql and NNNNNN_description.down.sql and
// run in numeric order inside the service's own schema.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
