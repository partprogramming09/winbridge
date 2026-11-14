//go:build !windows

package gateway

// checkNotWritableByOthers does nothing outside Windows.
func checkNotWritableByOthers(string) error { return nil }
