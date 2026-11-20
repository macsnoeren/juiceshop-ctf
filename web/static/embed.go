// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package static is the stylesheet and scripts both programs serve under
// <base>/static/. No inline script or style anywhere, so the
// Content-Security-Policy in internal/httpx can forbid both.
package static

import "embed"

//go:embed *.css *.js
var FS embed.FS
