package files

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	// ErrExists means the target exists and overwrite was not asked for.
	ErrExists = errors.New("file already exists; pass overwrite=true to replace it")
	// ErrReadOnly means the path is inside a folder the owner allowed for reading only.
	ErrReadOnly = errors.New("path is in a read-only folder; writing is allowed only in the write folders")
	// ErrExtension means the file name does not end in an extension from the data allowlist.
	ErrExtension = errors.New("file extension is not allowed for writing")
)

// allowedExtensions is a list of what may be written, not of what is banned: a banned list always forgets one
// (.wsf, .vbe, .cpl, .url, .psd1, .inf, and administrators_authorized_keys has no extension). The owner cannot change it.
var allowedExtensions = []string{".txt", ".md", ".json", ".csv", ".log", ".xml", ".yaml", ".yml", ".toml", ".ini", ".html", ".png", ".jpg", ".pdf"}

func extensionAllowed(name string) bool {
	ext := filepath.Ext(name)
	for i := 0; i < len(ext); i++ {
		if ext[i] >= utf8.RuneSelf {
			return false // ToLower would fold some letters into ASCII (U+0130 into "i"), so only plain ASCII qualifies
		}
	}
	ext = strings.ToLower(ext)
	for _, ok := range allowedExtensions {
		if ext == ok {
			return true
		}
	}
	return false
}

// Write saves data (UTF-8 text, or base64 with encoding "base64") to path and returns the bytes written. The parent
// folder must exist inside a write folder and the name must end in an allowed extension. Without overwrite the file
// is created exclusively and never replaced; with overwrite a temporary file in the same folder is renamed over the
// target, so the target is never left half written.
func (s *Store) Write(path, data, encoding string, overwrite bool) (int64, error) {
	var raw []byte
	switch encoding {
	case "", "utf8":
		if !utf8.ValidString(data) {
			return 0, errors.New("content is not valid UTF-8; use encoding base64 for binary data")
		}
		raw = []byte(data)
	case "base64":
		var err error
		if raw, err = base64.StdEncoding.DecodeString(data); err != nil {
			return 0, errors.New("content is not valid base64")
		}
	default:
		return 0, errors.New(`encoding must be "utf8" or "base64"`)
	}
	if int64(len(raw)) > s.maxBytes {
		return 0, fmt.Errorf("content is %d bytes; the limit is %d", len(raw), s.maxBytes)
	}
	if !s.spelledInside(path) {
		return 0, ErrOutside
	}
	if err := checkName(path, onWindows); err != nil {
		return 0, err
	}
	name := filepath.Base(path)
	if !extensionAllowed(name) {
		return 0, ErrExtension
	}
	dir, err := openDirInside(s.roots, filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	if !withinAny(s.writeRoots, dir) {
		return 0, ErrReadOnly
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if err := s.rootInside(root); err != nil {
		return 0, err
	}
	if overwrite {
		err = replace(root, name, raw)
	} else {
		err = create(root, name, raw)
	}
	if err != nil {
		return 0, err
	}
	return int64(len(raw)), nil
}

// create writes a new file; O_EXCL makes the check and the creation one step. Everything goes through root, a handle
// on the folder that was already checked, so a link swapped into the path afterwards cannot redirect it.
func create(root *os.Root, name string, raw []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	_, werr := f.Write(raw)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		root.Remove(name) // never leave a half-written file that this call created
		return werr
	}
	return nil
}

// replace writes the data to a temporary file next to the target and renames it over the target.
func replace(root *os.Root, name string, raw []byte) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := ".wb-" + hex.EncodeToString(rnd[:]) + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(raw)
	if werr == nil {
		werr = f.Sync() // so a power cut after the rename cannot leave an empty file under the target's name
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, name)
	}
	if werr != nil {
		root.Remove(tmp)
		return werr
	}
	return nil
}

// rootInside checks, through the open folder itself, that it lies in a write folder: a link swapped in between the
// path check and the open is caught here, before anything is created.
func (s *Store) rootInside(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	real, err := openedPath(d)
	if err != nil {
		return err
	}
	if !withinAny(s.writeRoots, real) {
		return ErrOutside
	}
	return nil
}
