//go:build linux

package files

import (
	"os"
	"strconv"
)

// fdPath asks the kernel where an open file really is.
func fdPath(f *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
}
