//go:build !windows

package gateway

import "os/exec"

// hideWindow does nothing outside Windows.
func hideWindow(*exec.Cmd) {}
