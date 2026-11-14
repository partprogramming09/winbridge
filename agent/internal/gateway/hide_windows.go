//go:build windows

package gateway

import (
	"os/exec"
	"syscall"
)

// hideWindow starts the child without a console window on the user's desktop.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
