package files

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// onWindows is true where path names follow Win32 rules; it picks the name checks and the case folding.
var onWindows = runtime.GOOS == "windows"

// within reports whether p is root or lies below it. It compares path components, so C:\Shared does not contain
// C:\SharedOther; with fold set it ignores case (Windows). Both paths must already be canonical.
func within(root, p string, fold bool) bool {
	rs, ps := components(root, fold), components(p, fold)
	if len(ps) < len(rs) {
		return false
	}
	for i := range rs {
		if fold && !strings.EqualFold(rs[i], ps[i]) || !fold && rs[i] != ps[i] {
			return false
		}
	}
	return true
}

// components splits a path into its names. A backslash separates names only on Windows; on other systems it is part
// of a name.
func components(p string, windows bool) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' || windows && r == '\\' })
}

func withinAny(roots []string, p string) bool {
	for _, root := range roots {
		if within(root, p, onWindows) {
			return true
		}
	}
	return false
}

// canonicalLoose resolves links in the longest existing part of p and keeps the rest as written, so a folder that does
// not exist yet is still compared through the links in front of it.
func canonicalLoose(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if real, err := realDir(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// openInside opens p and checks that what was opened (not just the text of the path) lies inside an allowed root.
// It returns the open file and its canonical path; the caller closes the file.
func openInside(roots []string, p string) (*os.File, string, error) {
	if err := checkName(p, onWindows); err != nil {
		return nil, "", err
	}
	f, real, err := openCanonical(p)
	if err != nil {
		return nil, "", err
	}
	if !withinAny(roots, real) {
		f.Close()
		return nil, "", ErrOutside
	}
	return f, real, nil
}

// openDirInside returns the canonical path of the existing directory dir if it lies inside an allowed root.
func openDirInside(roots []string, dir string) (string, error) {
	if err := checkName(dir, onWindows); err != nil {
		return "", err
	}
	real, err := canonicalDir(dir)
	if err != nil {
		return "", err
	}
	if !withinAny(roots, real) {
		return "", ErrOutside
	}
	return real, nil
}
