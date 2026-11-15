//go:build windows

package files

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// shortPath returns the 8.3 form of an existing path, or "" if the volume has short names turned off.
func shortPath(t *testing.T, p string) string {
	t.Helper()
	from, _ := syscall.UTF16PtrFromString(p)
	buf := make([]uint16, 512)
	k32 := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW")
	n, _, _ := k32.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) > len(buf) {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func TestOpenInsideRejectsJunctionEscape(t *testing.T) {
	root := newRoot(t, "Shared")
	outsideDir := filepath.Join(filepath.Dir(root), "outside")
	writeFile(t, filepath.Join(outsideDir, "secret.txt"), "secret")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "junction"), outsideDir).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction here: %v %s", err, out)
	}
	f, _, err := openInside([]string{root}, filepath.Join(root, "junction", "secret.txt"))
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
}

func TestOpenInsideRejectsShortNameEscape(t *testing.T) {
	root := newRoot(t, "Shared")
	outsideDir := filepath.Join(filepath.Dir(root), "a long outside folder")
	writeFile(t, filepath.Join(outsideDir, "secret.txt"), "secret")
	short := shortPath(t, outsideDir)
	if short == "" || !strings.Contains(short, "~") {
		t.Skip("8.3 short names are off on this volume")
	}
	// The short form is not refused by its text; the opened file's real path is outside the root, so it is refused there.
	if _, _, err := openInside([]string{root}, filepath.Join(short, "secret.txt")); !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
}

func TestOpenInsideAcceptsTheShortNameOfAFileInsideTheRoot(t *testing.T) {
	root := newRoot(t, "Shared")
	inside := filepath.Join(root, "a long inside folder")
	writeFile(t, filepath.Join(inside, "note.txt"), "x")
	short := shortPath(t, inside)
	if short == "" || !strings.Contains(short, "~") {
		t.Skip("8.3 short names are off on this volume")
	}
	f, real, err := openInside([]string{root}, filepath.Join(short, "note.txt"))
	if err != nil {
		t.Fatalf("a short name that leads inside must be accepted: %v", err)
	}
	f.Close()
	if strings.Contains(real, "~") {
		t.Errorf("the canonical path must be the long one, got %q", real)
	}
}

func TestOpenInsideFoldsCase(t *testing.T) {
	root := newRoot(t, "Shared")
	writeFile(t, filepath.Join(root, "A.txt"), "x")
	f, real, err := openInside([]string{root}, strings.ToLower(filepath.Join(root, "A.txt")))
	if err != nil {
		t.Fatalf("a different case of an allowed path must be accepted: %v", err)
	}
	f.Close()
	if !strings.EqualFold(real, filepath.Join(root, "A.txt")) {
		t.Errorf("canonical path = %q", real)
	}
}

func TestOpenInsideReadsFromTheVerifiedHandle(t *testing.T) {
	root := newRoot(t, "Shared")
	writeFile(t, filepath.Join(root, "a.txt"), "hello")
	f, _, err := openInside([]string{root}, filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 5)
	if n, err := f.Read(buf); err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("read %q, %v", buf, err)
	}
	if _, err := os.Stat(f.Name()); err != nil {
		t.Fatalf("the canonical name must exist: %v", err)
	}
}
