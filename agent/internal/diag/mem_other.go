//go:build !windows

package diag

import "runtime"

// processMemory reports what the Go runtime knows; there is no working set outside Windows.
func processMemory() map[string]any {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]any{"working_set_bytes": m.Sys, "private_bytes": m.Sys}
}
