//go:build windows

package files

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// openHandle opens a file or directory for reading and returns its handle.
func openHandle(p string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, ErrInvalidPath
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	return windows.CreateFile(name, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

// finalPath asks Windows where the open handle really is: junctions, symlinks, 8.3 names and case are already resolved.
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if int(n) > len(buf) {
			buf = make([]uint16, n)
			continue
		}
		out := windows.UTF16ToString(buf[:n])
		if rest, ok := strings.CutPrefix(out, `\\?\UNC\`); ok {
			return `\\` + rest, nil
		}
		return strings.TrimPrefix(out, `\\?\`), nil
	}
}

// canonicalDir returns the real path of an existing directory.
func canonicalDir(p string) (string, error) {
	if err := checkName(p, true); err != nil {
		return "", err
	}
	return realDir(p)
}

// realDir is canonicalDir without the name check, for paths that are only compared.
func realDir(p string) (string, error) {
	h, err := openHandle(p)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", errors.New("not a directory")
	}
	return finalPath(h)
}

// openCanonical opens p once and reads its real path from that same handle, so nothing can be swapped in between.
func openCanonical(p string) (*os.File, string, error) {
	h, err := openHandle(p)
	if err != nil {
		return nil, "", err
	}
	real, err := finalPath(h)
	if err != nil {
		windows.CloseHandle(h)
		return nil, "", err
	}
	return os.NewFile(uintptr(h), real), real, nil
}

// openedPath is where Windows says the open file really is.
func openedPath(f *os.File) (string, error) { return finalPath(windows.Handle(f.Fd())) }
