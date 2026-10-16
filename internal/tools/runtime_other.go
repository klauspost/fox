//go:build !windows

package tools

import "context"

// EnsureRuntime is a no-op: only the bundled Windows tools need the Visual C++ runtime.
func EnsureRuntime(context.Context, func(format string, args ...any)) {}
