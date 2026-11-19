// Package centralweb holds the templates of ctf-central: the boards and admin.
package centralweb

import "embed"

//go:embed *.html
var FS embed.FS
