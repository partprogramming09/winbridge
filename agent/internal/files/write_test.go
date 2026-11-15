package files

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// newWriteStore returns a store with one write folder and one read-only folder.
func newWriteStore(t *testing.T, maxBytes int64) (s *Store, wroot, rroot string) {
	t.Helper()
	wroot, rroot = newRoot(t, "Out"), newRoot(t, "Shared")
	s, err := New(Config{ReadRoots: []string{rroot}, WriteRoots: []string{wroot}, MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	return s, wroot, rroot
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteCreatesAFileInAWriteRoot(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	n, err := s.Write(filepath.Join(wroot, "note.txt"), "hola, señor", "", false)
	if err != nil || n != int64(len("hola, señor")) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := readBack(t, filepath.Join(wroot, "note.txt")); got != "hola, señor" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriteRootIsAlsoReadable(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	if _, err := s.Write(filepath.Join(wroot, "a.txt"), "x", "", false); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Read(filepath.Join(wroot, "a.txt"), "", 0); err != nil || c.Data != "x" {
		t.Fatalf("Read of a file in a write root = %+v, %v", c, err)
	}
}

func TestWriteRefusesAReadOnlyRoot(t *testing.T) {
	s, _, rroot := newWriteStore(t, 0)
	_, err := s.Write(filepath.Join(rroot, "a.txt"), "x", "", false)
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("err = %v, want ErrReadOnly", err)
	}
	if _, statErr := os.Stat(filepath.Join(rroot, "a.txt")); statErr == nil {
		t.Fatal("a file was written in a read-only folder")
	}
}

func TestWriteRefusesToOverwriteUnlessAsked(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	p := filepath.Join(wroot, "a.txt")
	writeFile(t, p, "old")
	if _, err := s.Write(p, "new", "", false); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if got := readBack(t, p); got != "old" {
		t.Fatalf("the file was changed: %q", got)
	}
}

func TestWriteOverwritesWhenAsked(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	p := filepath.Join(wroot, "a.txt")
	writeFile(t, p, "old")
	if n, err := s.Write(p, "new", "", true); err != nil || n != 3 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := readBack(t, p); got != "new" {
		t.Fatalf("content = %q", got)
	}
	if left := tmpLeftovers(t, wroot); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func tmpLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	items, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		if strings.HasSuffix(it.Name(), ".tmp") {
			out = append(out, it.Name())
		}
	}
	return out
}

func TestWriteRefusesAnythingOutsideTheExtensionAllowlist(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	for _, name := range []string{
		"a.exe", "a.ps1", "a.bat", "a.wsf", "a.vbe", "a.cpl", "a.url", "a.psd1", "a.inf",
		"noext", "a.TXT.exe", "A.PS1", "a.txt.", "a.txt ", "a.js", "a.lnk", "a.dll",
		"administrators_authorized_keys", "authorized_keys",
	} {
		if _, err := s.Write(filepath.Join(wroot, name), "x", "", false); !errors.Is(err, ErrExtension) && !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%q: err = %v, want ErrExtension", name, err)
		}
		if _, statErr := os.Stat(filepath.Join(wroot, name)); statErr == nil {
			t.Errorf("%q was written", name)
		}
	}
}

func TestWriteAcceptsAllowedExtensionsInAnyCase(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	for _, name := range []string{"A.TXT", "b.Json", "c.PNG", "d.md"} {
		if _, err := s.Write(filepath.Join(wroot, name), "x", "", false); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

func TestWriteWithoutOverwriteIsExclusiveUnderConcurrency(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	p := filepath.Join(wroot, "race.txt")
	var wins, exists atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch _, err := s.Write(p, "x", "", false); {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrExists):
				exists.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || exists.Load() != 15 {
		t.Fatalf("wins=%d exists=%d, want 1 and 15", wins.Load(), exists.Load())
	}
}

func TestWriteRefusesOutsideAndTraversalPaths(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	outside := newRoot(t, "Elsewhere")
	if _, err := s.Write(filepath.Join(wroot, "..", "x.txt"), "x", "", false); err == nil {
		t.Error("a .. path was accepted")
	}
	if _, err := s.Write(filepath.Join(outside, "x.txt"), "x", "", false); !errors.Is(err, ErrOutside) {
		t.Errorf("outside folder: err = %v, want ErrOutside", err)
	}
	if _, err := s.Write("relative.txt", "x", "", false); err == nil {
		t.Error("a relative path was accepted")
	}
	if _, err := s.Write(filepath.Join(wroot, "missing", "x.txt"), "x", "", false); err == nil {
		t.Error("a missing parent folder was created")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "x.txt")); statErr == nil {
		t.Error("a file was written outside")
	}
}

func TestWriteRefusesASymlinkLeadingOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating links needs privileges on Windows; the contain tests cover junctions there")
	}
	s, wroot, _ := newWriteStore(t, 0)
	outside := newRoot(t, "Elsewhere")
	if err := os.Symlink(outside, filepath.Join(wroot, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(filepath.Join(wroot, "link", "x.txt"), "x", "", false); !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "x.txt")); statErr == nil {
		t.Fatal("a file was written through the link")
	}
}

func TestWriteDoesNotFollowASymlinkAtTheTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating links needs privileges on Windows")
	}
	s, wroot, _ := newWriteStore(t, 0)
	outside := newRoot(t, "Elsewhere")
	victim := filepath.Join(outside, "victim.txt")
	writeFile(t, victim, "keep")
	if err := os.Symlink(victim, filepath.Join(wroot, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(filepath.Join(wroot, "a.txt"), "pwned", "", false); !errors.Is(err, ErrExists) {
		t.Fatalf("exclusive: err = %v, want ErrExists", err)
	}
	if _, err := s.Write(filepath.Join(wroot, "a.txt"), "pwned", "", true); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got := readBack(t, victim); got != "keep" {
		t.Fatalf("the file behind the link was changed: %q", got)
	}
}

func TestWriteRefusesWindowsNames(t *testing.T) {
	// checkName runs with the host rules in Write; this pins the stream case through the same function.
	if err := checkName(`C:\Out\a.txt:s`, true); err == nil {
		t.Fatal("a stream name passed checkName")
	}
}

func TestWriteFailureLeavesTargetAndNoTmp(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder the process cannot write to")
	}
	s, wroot, _ := newWriteStore(t, 0)
	p := filepath.Join(wroot, "a.txt")
	writeFile(t, p, "old")
	if err := os.Chmod(wroot, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(wroot, 0o755)
	if _, err := s.Write(p, "new", "", true); err == nil {
		t.Fatal("expected an error")
	}
	os.Chmod(wroot, 0o755)
	if got := readBack(t, p); got != "old" {
		t.Fatalf("the previous file changed: %q", got)
	}
	if left := tmpLeftovers(t, wroot); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestWriteRefusesOverMaxBytes(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 10)
	if _, err := s.Write(filepath.Join(wroot, "a.txt"), strings.Repeat("x", 11), "", false); err == nil {
		t.Fatal("11 bytes over a limit of 10 were accepted")
	}
	if _, err := os.Stat(filepath.Join(wroot, "a.txt")); err == nil {
		t.Fatal("something was written")
	}
	if n, err := s.Write(filepath.Join(wroot, "b.txt"), strings.Repeat("x", 10), "", false); err != nil || n != 10 {
		t.Fatalf("exactly the limit: %d, %v", n, err)
	}
}

func TestWriteDecodesBase64(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	raw := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 0xff}
	n, err := s.Write(filepath.Join(wroot, "a.png"), base64.StdEncoding.EncodeToString(raw), "base64", false)
	if err != nil || n != int64(len(raw)) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := readBack(t, filepath.Join(wroot, "a.png")); got != string(raw) {
		t.Fatalf("content = %v", []byte(got))
	}
}

func TestWriteRefusesInvalidBase64AndUnknownEncoding(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	if _, err := s.Write(filepath.Join(wroot, "a.png"), "***not base64***", "base64", false); err == nil {
		t.Error("invalid base64 accepted")
	}
	if _, err := s.Write(filepath.Join(wroot, "b.txt"), "x", "latin1", false); err == nil {
		t.Error("unknown encoding accepted")
	}
	for _, n := range []string{"a.png", "b.txt"} {
		if _, err := os.Stat(filepath.Join(wroot, n)); err == nil {
			t.Errorf("%s was written", n)
		}
	}
}

func TestWriteRefusesADirectoryTarget(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	mkdir(t, filepath.Join(wroot, "d.txt"))
	if _, err := s.Write(filepath.Join(wroot, "d.txt"), "x", "", true); err == nil {
		t.Fatal("overwrote a directory")
	}
}

func TestNewValidatesWriteRootsLikeReadRoots(t *testing.T) {
	forbidden := newRoot(t, "Secret")
	home := newRoot(t, "Home")
	if _, err := New(Config{WriteRoots: []string{forbidden}, Forbidden: []string{forbidden}}); err == nil {
		t.Error("a forbidden write folder was accepted")
	}
	if _, err := New(Config{WriteRoots: []string{home}, Home: home}); err == nil {
		t.Error("the profile folder was accepted as a write folder")
	}
	if _, err := New(Config{WriteRoots: []string{string(filepath.Separator)}}); err == nil {
		t.Error("a filesystem root was accepted as a write folder")
	}
}

func TestWriteRefusesInvalidUTF8UnlessBase64(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	if _, err := s.Write(filepath.Join(wroot, "a.txt"), "\xff\xfe", "", false); err == nil {
		t.Fatal("bytes that are not UTF-8 were written as text")
	}
	if _, err := os.Stat(filepath.Join(wroot, "a.txt")); err == nil {
		t.Fatal("something was written")
	}
}

func TestNewRefusesAReadFolderInsideAWriteFolder(t *testing.T) {
	w := newRoot(t, "Out")
	ro := mkdir(t, filepath.Join(w, "ro"))
	if _, err := New(Config{ReadRoots: []string{ro}, WriteRoots: []string{w}}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("a read-only folder inside a write folder would be writable; err = %v", err)
	}
	// the other way round (a write folder inside a read folder) is the normal case and stays allowed
	r := newRoot(t, "Shared")
	sub := mkdir(t, filepath.Join(r, "out"))
	if _, err := New(Config{ReadRoots: []string{r}, WriteRoots: []string{sub}}); err != nil {
		t.Fatalf("write folder inside a read folder: %v", err)
	}
}
