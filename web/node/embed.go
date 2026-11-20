// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package nodeweb holds the templates of the node's student page.
package nodeweb

import "embed"

//go:embed *.html
var FS embed.FS
