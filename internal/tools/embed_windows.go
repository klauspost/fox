//go:build windows && !lite

package tools

import (
	"embed"
	"io/fs"
)

//go:embed bundle/core bundle/extra
var embedded embed.FS

var bundleFS, _ = fs.Sub(embedded, "bundle")
