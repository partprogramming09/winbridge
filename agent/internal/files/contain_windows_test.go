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
	// checkName refuses the short form before the disk is touched, which is the first line of defence.
	if _, _, err := openInside([]string{root}, filepath.Join(short, "secret.txt")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v, want ErrInvalidPath", err)
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
