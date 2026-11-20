// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package centralweb holds the templates of ctf-central: the boards and admin.
package centralweb

import "embed"

//go:embed *.html *.svg
var FS embed.FS
