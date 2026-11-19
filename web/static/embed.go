// Package static is the stylesheet and scripts both programs serve under
// <base>/static/. No inline script or style anywhere, so the
// Content-Security-Policy in internal/shared/httpx can forbid both.
package static

import "embed"

//go:embed *.css *.js
var FS embed.FS
