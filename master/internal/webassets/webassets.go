// Package webassets embeds the built frontend (dist/) into the master binary.
// The frontend build pipeline writes its output to this dist/ directory before
// building the master.
package webassets

import "embed"

//go:embed all:dist
var FS embed.FS
