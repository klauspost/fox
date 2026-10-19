//go:build windows && lite

package tools

import (
	"embed"
	"io/fs"
)

// The extra tools are left out; fox-lite takes them from the host if installed.
//
//go:embed bundle/core
var embedded embed.FS

var bundleFS, _ = fs.Sub(embedded, "bundle")
