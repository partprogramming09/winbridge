//go:build !windows

package files

import "syscall"

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
