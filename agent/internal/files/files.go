package files

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxBytes = 256 << 10
	maxListEntries  = 500
)

// Config holds what the owner put in the [files] table, plus what the agent knows about itself.
type Config struct {
	ReadRoots []string
	MaxBytes  int64    // largest read, in bytes; 0 means 256 KiB, at most 1 MiB
	Home      string   // the user's profile folder: a root may not be it or contain it
	Forbidden []string // folders no root may contain or lie inside (WinBridge's own, the jobs and tasks folders, DangerousPaths)
}

// Store is the only way to touch files: every path goes through the allowed folders.
type Store struct {
	roots    []string // canonical
	spelled  []string // canonical and as configured: a path outside all of them is refused without touching the disk
	maxBytes int64
}

const maxAllowedBytes = 1 << 20

// New canonicalizes the folders (so paths compare against what Windows really sees), refuses the dangerous ones
// and applies the defaults.
func New(cfg Config) (*Store, error) {
	if cfg.MaxBytes < 0 || cfg.MaxBytes > maxAllowedBytes {
		return nil, fmt.Errorf("files: max_bytes must be between 1 and %d", maxAllowedBytes)
	}
	s := &Store{maxBytes: cfg.MaxBytes}
	if s.maxBytes == 0 {
		s.maxBytes = defaultMaxBytes
	}
	forbidden := forbiddenForms(cfg.Forbidden)
	for _, root := range cfg.ReadRoots {
		canon, err := canonicalDir(root)
		if err != nil {
			return nil, fmt.Errorf("files: folder %q: %w", root, err)
		}
		if n := len(components(canon, onWindows)); n == 0 || onWindows && n == 1 {
			return nil, fmt.Errorf("files: folder %q is a drive or filesystem root", root)
		}
		if cfg.Home != "" {
			for _, home := range forbiddenForms([]string{cfg.Home}) {
				if within(canon, home, onWindows) {
					return nil, fmt.Errorf("files: folder %q is or contains the user profile", root)
				}
			}
		}
		for _, f := range forbidden {
			if within(canon, f, onWindows) || within(f, canon, onWindows) {
				return nil, fmt.Errorf("files: folder %q overlaps %q, which must stay out of reach", root, f)
			}
		}
		s.roots = append(s.roots, canon)
		s.spelled = append(s.spelled, canon, filepath.Clean(root))
	}
	return s, nil
}

// spelledInside is the cheap first check: a path that does not even start inside an allowed folder is refused before
// anything touches the disk, so the answer does not reveal whether it exists.
func (s *Store) spelledInside(path string) bool {
	clean := filepath.Clean(path)
	for _, root := range s.spelled {
		if within(root, clean, onWindows) {
			return true
		}
	}
	return false
}

// forbiddenForms returns each path as given and, when it exists, in canonical form, so a link cannot hide it.
func forbiddenForms(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		out = append(out, filepath.Clean(p))
		if canon := canonicalLoose(p); canon != filepath.Clean(p) {
			out = append(out, canon)
		}
	}
	return out
}

// Entry is one line of a directory listing.
type Entry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"` // RFC 3339, UTC
}

// List returns the entries of a directory sorted by name, at most 500; truncated says more existed.
func (s *Store) List(path string) (entries []Entry, truncated bool, err error) {
	if !s.spelledInside(path) {
		return nil, false, ErrOutside
	}
	f, _, err := openInside(s.roots, path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	items, err := f.ReadDir(-1) // shortcut: reads the whole directory before capping; fine for folders a person set up
	if err != nil {
		return nil, false, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	if len(items) > maxListEntries {
		items, truncated = items[:maxListEntries], true
	}
	for _, it := range items {
		e := Entry{Name: it.Name(), Dir: it.IsDir()}
		if info, err := it.Info(); err == nil {
			e.Size, e.Modified = info.Size(), info.ModTime().UTC().Format(time.RFC3339)
		}
		entries = append(entries, e)
	}
	return entries, truncated, nil
}

// Content is what Read returns.
type Content struct {
	Data      string `json:"data"`
	Encoding  string `json:"encoding"` // utf8 or base64
	Size      int64  `json:"size"`     // size of the whole file
	Truncated bool   `json:"truncated"`
}

// Read returns up to MaxBytes of the file from offset, as UTF-8 text or base64. A file that is not valid UTF-8
// is refused unless base64 is asked for; the error never carries file content.
func (s *Store) Read(path, encoding string, offset int64) (Content, error) {
	switch encoding {
	case "", "utf8":
		encoding = "utf8"
	case "base64":
	default:
		return Content{}, errors.New(`encoding must be "utf8" or "base64"`)
	}
	if offset < 0 {
		return Content{}, errors.New("offset must not be negative")
	}
	if !s.spelledInside(path) {
		return Content{}, ErrOutside
	}
	f, _, err := openInside(s.roots, path)
	if err != nil {
		return Content{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Content{}, err
	}
	if info.IsDir() {
		return Content{}, errors.New("path is a directory; use ListFiles")
	}
	if !info.Mode().IsRegular() {
		return Content{}, errors.New("path is not a regular file")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return Content{}, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, s.maxBytes))
	if err != nil {
		return Content{}, err
	}
	c := Content{Encoding: encoding, Size: info.Size(), Truncated: offset+int64(len(buf)) < info.Size()}
	if encoding == "base64" {
		c.Data = base64.StdEncoding.EncodeToString(buf)
		return c, nil
	}
	cut := 0
	if c.Truncated { // the limit may land inside a character: drop only that incomplete tail
		for k := 1; k < utf8.UTFMax && k <= len(buf); k++ {
			if tail := buf[len(buf)-k:]; utf8.RuneStart(tail[0]) && !utf8.FullRune(tail) {
				cut = k
				break
			}
		}
	}
	if utf8.Valid(buf[:len(buf)-cut]) {
		c.Data = string(buf[:len(buf)-cut])
		return c, nil
	}
	return Content{}, errors.New(`file is not valid UTF-8 text; request encoding "base64"`)
}
