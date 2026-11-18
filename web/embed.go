// Package web holds the templates and static files of ctf-status and
// ctf-central, embedded into the binaries so the scratch images need no files
// next to them.
//
//	static/   shared by both: the stylesheet, the leaderboard script
//	status/   ctf-status pages (DESIGN.md §5)
//	central/  ctf-central pages (DESIGN.md §8)
//
// There is no inline script or style anywhere, so the Content-Security-Policy
// in internal/httpx can forbid both. Progress bars are <progress> elements for
// the same reason: a style="width: …" attribute would need 'unsafe-inline'.
package web

import "embed"

//go:embed static status central
var FS embed.FS
