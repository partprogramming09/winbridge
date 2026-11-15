//go:build darwin

package files

import (
	"os"
	"syscall"
	"unsafe"
)

const fGetPath = 50 // F_GETPATH

// fdPath asks the kernel where an open file really is.
func fdPath(f *os.File) (string, error) {
	buf := make([]byte, 1024) // PATH_MAX
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), fGetPath, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return "", errno
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), nil
		}
	}
	return string(buf), nil
}
