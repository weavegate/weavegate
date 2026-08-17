// Package rules embeds the shipped diagnostic rule table.
package rules

import (
	"embed"
	"io/fs"
)

//go:embed *.json
var embedded embed.FS

// FS returns the diagnostic rule files independently of the repository
// checkout and process working directory.
func FS() fs.FS { return embedded }
