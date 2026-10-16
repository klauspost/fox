package tools

import (
	"embed"
	"io/fs"
)

//go:embed all:bundle
var embedded embed.FS

var bundleFS, _ = fs.Sub(embedded, "bundle")
