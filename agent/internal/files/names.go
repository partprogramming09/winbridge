// Package files lists, reads and writes files inside folders the owner allowed, and nowhere else.
package files

import (
	"errors"
	"regexp"
	"strings"
)

var (
	// ErrInvalidPath means the path is malformed or uses a name Windows would reinterpret.
	ErrInvalidPath = errors.New("invalid path")
	// ErrOutside means the path is not inside any allowed folder.
	ErrOutside = errors.New("path is outside the allowed folders")
)

var (
	driveLetter = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	// shortName matches the 8.3 form of a long name (PROGRA~1, DOCUME~1.TXT).
	shortName = regexp.MustCompile(`^[^.]{1,6}~[0-9]+(\..{0,3})?$`)
	reserved  = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
)

// checkName validates the text of an absolute path without touching the disk. With windows set it also refuses what
// Win32 rewrites into another path: streams, devices, 8.3 names, a trailing dot or space, and the \\?\, \\.\ and UNC prefixes.
func checkName(p string, windows bool) error {
	if p == "" || strings.ContainsRune(p, 0) {
		return ErrInvalidPath
	}
	if !windows {
		if !strings.HasPrefix(p, "/") {
			return ErrInvalidPath
		}
		return nil
	}
	if !driveLetter.MatchString(p) {
		return ErrInvalidPath // relative, \\?\, \\.\, UNC and rooted-without-drive all end here
	}
	rest := p[2:]
	if strings.Contains(rest, ":") {
		return ErrInvalidPath // alternate data stream
	}
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		if part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return ErrInvalidPath
		}
		if isDevice(part) || shortName.MatchString(part) {
			return ErrInvalidPath
		}
	}
	return nil
}

// isDevice reports whether the name, with any extension, is a reserved DOS device (CON, NUL, COM1, LPT9...).
func isDevice(name string) bool {
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	if reserved[base] {
		return true
	}
	if !strings.HasPrefix(base, "COM") && !strings.HasPrefix(base, "LPT") {
		return false
	}
	// COM1-COM9 and LPT1-LPT9, and also the superscript digits Windows 10 reserves.
	switch base[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3":
		return true
	}
	return false
}
