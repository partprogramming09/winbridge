package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// newRoot makes a canonical root folder (macOS temp dirs sit behind a symlink) and returns it.
func newRoot(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	canon, err := canonicalDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canon
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWithinComparesComponentsNotPrefixes(t *testing.T) {
	cases := []struct {
		root, p string
		fold    bool
		want    bool
	}{
		{`C:\Shared`, `C:\Shared\a.txt`, true, true},
		{`C:\Shared`, `C:\Shared`, true, true},
		{`C:\Shared`, `C:\SharedOther\a.txt`, true, false},
		{`C:\Shared`, `c:\shared\A.txt`, true, true},
		{`C:\Shared`, `c:\shared\A.txt`, false, false},
		{"/a/b", "/a/b/c", false, true},
		{"/a/b", "/a/bc", false, false},
		{"/a/b", "/a", false, false},
	}
	for _, c := range cases {
		if got := within(c.root, c.p, c.fold); got != c.want {
			t.Errorf("within(%q, %q, fold=%v) = %v, want %v", c.root, c.p, c.fold, got, c.want)
		}
	}
}

func TestOpenInsideAcceptsAFileInTheRoot(t *testing.T) {
	root := newRoot(t, "Shared")
	writeFile(t, filepath.Join(root, "sub", "a.txt"), "hello")
	f, real, err := openInside([]string{root}, filepath.Join(root, "sub", "a.txt"))
	if err != nil {
		t.Fatalf("openInside: %v", err)
	}
	defer f.Close()
	if real != filepath.Join(root, "sub", "a.txt") {
		t.Errorf("canonical path = %q", real)
	}
}

func TestOpenInsideRejectsDotDotEscape(t *testing.T) {
	root := newRoot(t, "Shared")
	outside := filepath.Join(filepath.Dir(root), "outside", "secret.txt")
	writeFile(t, outside, "secret")
	f, _, err := openInside([]string{root}, root+string(filepath.Separator)+".."+string(filepath.Separator)+"outside"+string(filepath.Separator)+"secret.txt")
	if f != nil {
		f.Close()
	}
	// Windows refuses ".." by name before the disk is touched; elsewhere containment catches it. Either way: refused.
	if !errors.Is(err, ErrOutside) && !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v, want ErrOutside or ErrInvalidPath", err)
	}
}

func TestOpenInsideComparesComponentsNotPrefixesOnDisk(t *testing.T) {
	base := t.TempDir()
	root, err := canonicalDir(mkdir(t, filepath.Join(base, "Shared")))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, "SharedOther", "a.txt"), "x")
	other, err := canonicalDir(filepath.Join(base, "SharedOther"))
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := openInside([]string{root}, filepath.Join(other, "a.txt"))
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
}

func TestOpenInsideRejectsSymlinkEscape(t *testing.T) {
	root := newRoot(t, "Shared")
	outsideDir := filepath.Join(filepath.Dir(root), "outside")
	writeFile(t, filepath.Join(outsideDir, "secret.txt"), "secret")
	if err := os.Symlink(outsideDir, filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	f, _, err := openInside([]string{root}, filepath.Join(root, "link", "secret.txt"))
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
}

func TestOpenInsideReportsAMissingFileAsNotExist(t *testing.T) {
	root := newRoot(t, "Shared")
	_, _, err := openInside([]string{root}, filepath.Join(root, "missing.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestOpenDirInsideReturnsTheCanonicalDirectory(t *testing.T) {
	root := newRoot(t, "Shared")
	if err := os.MkdirAll(filepath.Join(root, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := openDirInside([]string{root}, filepath.Join(root, "inbox"))
	if err != nil || got != filepath.Join(root, "inbox") {
		t.Fatalf("openDirInside = %q, %v", got, err)
	}
	if _, err := openDirInside([]string{root}, filepath.Dir(root)); !errors.Is(err, ErrOutside) {
		t.Fatalf("the parent of the root must be outside, got %v", err)
	}
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
