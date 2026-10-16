//go:build !windows

package tools

import (
	"embed"
	"io/fs"
)

// The bundled tools are Windows binaries; elsewhere tools come from PATH or overrides.
var bundleFS fs.FS = embed.FS{}
