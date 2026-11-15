//go:build !windows

package files

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// canonicalDir returns the absolute path of an existing directory with every link resolved.
func canonicalDir(p string) (string, error) {
	if err := checkName(p, false); err != nil {
		return "", err
	}
	return realDir(p)
}

// realDir is canonicalDir without the name check, for paths that are only compared.
func realDir(p string) (string, error) {
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return real, nil
}

// openCanonical resolves every link in p, opens the result and checks that the path did not change meanwhile.
func openCanonical(p string) (*os.File, string, error) {
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return nil, "", err
	}
	// O_NONBLOCK: opening a FIFO must not wait for a writer. Read refuses anything that is not a regular file.
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	// Where the kernel says the open file is: a link swapped in between the checks cannot change this answer.
	opened, err := fdPath(f)
	if err != nil {
		f.Close()
		return nil, "", err
	}
	if again, err := filepath.EvalSymlinks(opened); err != nil || again != opened {
		f.Close()
		return nil, "", ErrOutside
	}
	return f, opened, nil
}
