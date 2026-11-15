package files

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T, maxBytes int64) (*Store, string) {
	t.Helper()
	root := newRoot(t, "Shared")
	s, err := New(Config{ReadRoots: []string{root}, MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestListReturnsNamesSizesAndKinds(t *testing.T) {
	s, root := newStore(t, 0)
	writeFile(t, filepath.Join(root, "b.txt"), "12345")
	writeFile(t, filepath.Join(root, "a.md"), "x")
	mkdir(t, filepath.Join(root, "sub"))
	got, truncated, err := s.List(root)
	if err != nil || truncated {
		t.Fatalf("List: %v truncated=%v", err, truncated)
	}
	if len(got) != 3 || got[0].Name != "a.md" || got[1].Name != "b.txt" || got[2].Name != "sub" {
		t.Fatalf("entries not sorted by name: %+v", got)
	}
	if got[1].Size != 5 || got[1].Dir || !got[2].Dir || got[1].Modified == "" {
		t.Fatalf("size, kind or time wrong: %+v", got)
	}
}

func TestListCapsAt500AndSaysTruncated(t *testing.T) {
	s, root := newStore(t, 0)
	for i := 0; i < 501; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), "")
	}
	got, truncated, err := s.List(root)
	if err != nil || len(got) != 500 || !truncated {
		t.Fatalf("len=%d truncated=%v err=%v", len(got), truncated, err)
	}
}

func TestReadReturnsUTF8Text(t *testing.T) {
	s, root := newStore(t, 0)
	writeFile(t, filepath.Join(root, "a.txt"), "hola, señor")
	c, err := s.Read(filepath.Join(root, "a.txt"), "", 0)
	if err != nil || c.Data != "hola, señor" || c.Encoding != "utf8" || c.Truncated || c.Size != int64(len("hola, señor")) {
		t.Fatalf("Read = %+v, %v", c, err)
	}
}

func TestReadRefusesBinaryWithoutBase64(t *testing.T) {
	s, root := newStore(t, 0)
	writeFile(t, filepath.Join(root, "b.bin"), "\xff\xfe\x00\x01")
	_, err := s.Read(filepath.Join(root, "b.bin"), "utf8", 0)
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("want an error that mentions base64, got %v", err)
	}
}

func TestReadBase64RoundTrips(t *testing.T) {
	s, root := newStore(t, 0)
	raw := "\xff\xfe\x00\x01 binary"
	writeFile(t, filepath.Join(root, "b.bin"), raw)
	c, err := s.Read(filepath.Join(root, "b.bin"), "base64", 0)
	if err != nil || c.Encoding != "base64" {
		t.Fatalf("Read = %+v, %v", c, err)
	}
	back, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil || string(back) != raw {
		t.Fatalf("round trip failed: %q %v", back, err)
	}
}

func TestReadTruncatesAtMaxBytesAndReportsIt(t *testing.T) {
	s, root := newStore(t, 10)
	writeFile(t, filepath.Join(root, "a.txt"), strings.Repeat("a", 25))
	c, err := s.Read(filepath.Join(root, "a.txt"), "", 0)
	if err != nil || len(c.Data) != 10 || !c.Truncated || c.Size != 25 {
		t.Fatalf("Read = %+v, %v", c, err)
	}
}

func TestReadTruncationNeverSplitsACharacter(t *testing.T) {
	s, root := newStore(t, 5)
	writeFile(t, filepath.Join(root, "a.txt"), "abcdññ") // each n-tilde is 2 bytes: the limit lands inside one
	c, err := s.Read(filepath.Join(root, "a.txt"), "", 0)
	if err != nil || c.Data != "abcd" || !c.Truncated {
		t.Fatalf("Read = %+v, %v", c, err)
	}
}

func TestReadHonoursOffset(t *testing.T) {
	s, root := newStore(t, 0)
	writeFile(t, filepath.Join(root, "a.txt"), "0123456789")
	c, err := s.Read(filepath.Join(root, "a.txt"), "", 4)
	if err != nil || c.Data != "456789" || c.Truncated {
		t.Fatalf("Read = %+v, %v", c, err)
	}
}

func TestReadRefusesADirectoryAndAPathOutsideTheRoot(t *testing.T) {
	s, root := newStore(t, 0)
	mkdir(t, filepath.Join(root, "sub"))
	if _, err := s.Read(filepath.Join(root, "sub"), "", 0); err == nil {
		t.Error("reading a directory must fail")
	}
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	writeFile(t, outside, "secret")
	if _, err := s.Read(outside, "", 0); !errors.Is(err, ErrOutside) {
		t.Errorf("outside path: err = %v, want ErrOutside", err)
	}
	if _, _, err := s.List(filepath.Dir(root)); !errors.Is(err, ErrOutside) {
		t.Errorf("listing outside: err = %v, want ErrOutside", err)
	}
}

func TestReadErrorNeverContainsFileContent(t *testing.T) {
	s, root := newStore(t, 0)
	const secret = "TOP-SECRET-CONTENT"
	writeFile(t, filepath.Join(root, "a.txt"), secret)
	_, err1 := s.Read(filepath.Join(root, "a.txt"), "rot13", 0)
	_, err2 := s.Read(filepath.Join(root, "a.txt"), "", -1)
	for _, err := range []error{err1, err2} {
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("error missing or leaks content: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestNewRefusesDriveRootHomeAndForbiddenOverlap(t *testing.T) {
	base, err := canonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := mkdir(t, filepath.Join(base, "home", "alice"))
	data := mkdir(t, filepath.Join(base, "data"))
	ssh := filepath.Join(data, ".ssh") // does not exist: the check is on the text of the path
	cfg := func(root string) Config {
		return Config{ReadRoots: []string{root}, Home: home, Forbidden: []string{ssh}}
	}
	for name, root := range map[string]string{
		"filesystem root":      string(filepath.Separator),
		"the user profile":     home,
		"contains the profile": filepath.Dir(home),
		"contains forbidden":   data,
		"inside forbidden":     filepath.Join(ssh, "inner"),
	} {
		if root == filepath.Join(ssh, "inner") {
			mkdir(t, root)
			root, _ = canonicalDir(root)
		}
		if _, err := New(cfg(root)); err == nil {
			t.Errorf("%s: New must refuse %q", name, root)
		}
	}
	if _, err := New(cfg(mkdir(t, filepath.Join(home, "Shared")))); err != nil {
		t.Errorf("a folder inside the profile that overlaps nothing must be accepted: %v", err)
	}
}

func TestNewRefusesRelativeRootFileRootAndBadMaxBytes(t *testing.T) {
	root := newRoot(t, "Shared")
	writeFile(t, filepath.Join(root, "f.txt"), "x")
	for name, cfg := range map[string]Config{
		"relative":  {ReadRoots: []string{"Shared"}},
		"a file":    {ReadRoots: []string{filepath.Join(root, "f.txt")}},
		"too big":   {ReadRoots: []string{root}, MaxBytes: 1<<20 + 1},
		"negative":  {ReadRoots: []string{root}, MaxBytes: -1},
		"no folder": {ReadRoots: []string{filepath.Join(root, "missing")}},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New must refuse it", name)
		}
	}
}

func TestDangerousPathsCoverSshStartupAppDataWindowsProgramFilesAndPath(t *testing.T) {
	env := map[string]string{
		"USERPROFILE": `C:\Users\alice`, "ProgramData": `C:\ProgramData`, "APPDATA": `C:\Users\alice\AppData\Roaming`,
		"LOCALAPPDATA": `C:\Users\alice\AppData\Local`, "SystemRoot": `C:\Windows`, "ProgramFiles": `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"PATH":              strings.Join([]string{"/opt/tools/bin", "/opt/other"}, string(os.PathListSeparator)),
	}
	got := DangerousPaths(func(k string) string { return env[k] })
	for _, want := range []string{
		filepath.Join(`C:\Users\alice`, ".ssh"), filepath.Join(`C:\ProgramData`, "ssh"),
		filepath.Join(`C:\ProgramData`, "Microsoft", "Windows", "Start Menu"), `C:\Users\alice\AppData\Roaming`,
		`C:\Users\alice\AppData\Local`, `C:\Windows`, `C:\Program Files`, `C:\Program Files (x86)`, filepath.Join("/opt/tools/bin"), filepath.Join("/opt/other"),
	} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("DangerousPaths misses %q (got %v)", want, got)
		}
	}
	if len(DangerousPaths(func(string) string { return "" })) != 0 {
		t.Error("with no environment there is nothing to forbid")
	}
}

func TestOutsidePathsGiveTheSameErrorWhetherOrNotTheyExist(t *testing.T) {
	s, root := newStore(t, 0)
	outside := filepath.Join(filepath.Dir(root), "elsewhere")
	mkdir(t, outside)
	writeFile(t, filepath.Join(outside, "real.txt"), "x")
	for _, p := range []string{filepath.Join(outside, "real.txt"), filepath.Join(outside, "missing.txt")} {
		if _, err := s.Read(p, "", 0); !errors.Is(err, ErrOutside) {
			t.Errorf("Read(%q) = %v, want ErrOutside whether or not it exists", p, err)
		}
		if _, _, err := s.List(p); !errors.Is(err, ErrOutside) {
			t.Errorf("List(%q) = %v, want ErrOutside whether or not it exists", p, err)
		}
	}
}

func TestCheckEnvironmentOnWindowsNeedsTheVariablesThatNameForbiddenFolders(t *testing.T) {
	full := map[string]string{"USERPROFILE": `C:\Users\alice`, "SystemRoot": `C:\Windows`, "ProgramFiles": `C:\Program Files`}
	get := func(env map[string]string) func(string) string { return func(k string) string { return env[k] } }
	if err := CheckEnvironment(get(full), true); err != nil {
		t.Fatalf("complete environment refused: %v", err)
	}
	for _, missing := range []string{"USERPROFILE", "SystemRoot", "ProgramFiles"} {
		env := map[string]string{}
		for k, v := range full {
			if k != missing {
				env[k] = v
			}
		}
		if err := CheckEnvironment(get(env), true); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s the agent must refuse to start, got %v", missing, err)
		}
	}
	if err := CheckEnvironment(get(nil), false); err != nil {
		t.Errorf("off Windows there is nothing to require: %v", err)
	}
}

func TestDangerousPathsCoverCredentialFoldersAndTheWholeAppData(t *testing.T) {
	got := DangerousPaths(func(k string) string {
		if k == "USERPROFILE" {
			return `C:\Users\alice`
		}
		return ""
	})
	for _, want := range []string{".aws", ".azure", ".kube", ".gnupg", ".docker", ".config", "AppData"} {
		w := filepath.Join(`C:\Users\alice`, want)
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			t.Errorf("DangerousPaths misses %q (got %v)", w, got)
		}
	}
}
