package files

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Adversarial tests for Store.Write. Helpers use the prefix wa so they never clash with the other test files.

func waSkipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses unix path semantics, links or FIFOs")
	}
}

// waNames lists a directory's entry names (sorted); a missing directory is an empty list.
func waNames(t testing.TB, dir string) []string {
	t.Helper()
	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		out = append(out, it.Name())
	}
	sort.Strings(out)
	return out
}

func waSymlink(t testing.TB, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
}

// ---------- extensions ----------

func TestWAExtensionAllowlistIsExact(t *testing.T) {
	waSkipWindows(t)
	rejected := []string{
		"a", "a.", "a..", "a.txt.", "a.txt.exe", "a.exe", "a.ps1", "a.bat", "a.cmd", "a.lnk", "a.url", "a.wsf", "a.tmp",
		".wb-123.tmp", "a.js", "a.svg", "a.htm", "a.jpeg", "a.docx", "authorized_keys", "a.pdf.lnk", "a.tx t",
		"a.txt ", "a.txt\t", "a.txt\n", "a.txt\r", "a.txt:s", "a.txt::$DATA", "a.txt~", "a.txt\u200b", "a.txt\u00a0",
		"a.t\u0445t",           // Cyrillic small ha
		"a.\u0442xt",           // Cyrillic te
		"a.\uff54\uff58\uff54", // fullwidth "txt"
		"a.TXT\u0301",          // combining acute accent after the extension
		"a.t\u0307xt",          // combining dot inside
	}
	s, wroot, _ := newWriteStore(t, 0)
	for _, name := range rejected {
		_, err := s.Write(filepath.Join(wroot, name), "x", "", false)
		if !errors.Is(err, ErrExtension) {
			t.Errorf("%q: err = %v, want ErrExtension", name, err)
		}
		if _, err := s.Write(filepath.Join(wroot, name), "x", "", true); !errors.Is(err, ErrExtension) {
			t.Errorf("%q overwrite: err = %v, want ErrExtension", name, err)
		}
	}
	if got := waNames(t, wroot); len(got) != 0 {
		t.Fatalf("a rejected write left files behind: %q", got)
	}
}

// Case folding must not map a non-allowed extension onto an allowed one: U+0130 (capital I with dot above) folds to
// "i" in Go's simple case mapping, so ".\u0130NI" would pass as ".ini" although no file system sees ".ini" there.
func TestWAExtensionCaseFoldingDoesNotAdmitLookalikes(t *testing.T) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	for i, ext := range []string{".\u0130NI", ".\u0130ni", ".\u212aml", ".l\u00f6g", ".\u017ftl"} {
		name := fmt.Sprintf("a%d%s", i, ext)
		if _, err := s.Write(filepath.Join(wroot, name), "x", "", false); !errors.Is(err, ErrExtension) {
			t.Errorf("%q: err = %v, want ErrExtension", name, err)
		}
	}
}

func TestWAExtensionNulByteAndTrailingSlashForms(t *testing.T) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	if _, err := s.Write(filepath.Join(wroot, "a.txt")+"\x00.exe", "x", "", false); err == nil {
		t.Error(".txt\\x00.exe was accepted")
	}
	if _, err := s.Write(wroot+"/a.exe\x00.txt", "x", "", false); err == nil {
		t.Error(".exe\\x00.txt was accepted")
	}
	if got := waNames(t, wroot); len(got) != 0 {
		t.Fatalf("NUL names left files behind: %q", got)
	}
	// A trailing slash or dot segment may be refused or cleaned, but never leave anything but a.txt, or escape.
	for _, suffix := range []string{"/", "//", "/.", "/./"} {
		s.Write(wroot+"/a.txt"+suffix, "x", "", true)
	}
	for _, n := range waNames(t, wroot) {
		if n != "a.txt" {
			t.Errorf("unexpected entry %q after trailing-slash writes", n)
		}
	}
	if fi, err := os.Lstat(filepath.Join(wroot, "a.txt")); err == nil && !fi.Mode().IsRegular() {
		t.Errorf("a.txt is not a regular file: %v", fi.Mode())
	}
}

func TestWADotNamesStayInsideAndNeverEscape(t *testing.T) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	for _, name := range []string{".hidden.txt", "..txt", "...md", ".a.JSON"} {
		if _, err := s.Write(filepath.Join(wroot, name), "x", "", false); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	parent := filepath.Dir(wroot)
	for _, p := range []string{wroot + "/..", wroot + "/../a.txt", wroot + "/.", wroot + "/.../x.txt", wroot + "/../" + filepath.Base(wroot) + "/../x.txt"} {
		if _, err := s.Write(p, "x", "", true); err == nil && !strings.HasPrefix(filepath.Clean(p), wroot+"/") {
			t.Errorf("%q was accepted", p)
		}
	}
	for _, n := range waNames(t, parent) {
		if n != filepath.Base(wroot) && n != "Shared" {
			t.Errorf("an entry %q appeared next to the write folder", n)
		}
	}
}

// ---------- failures leave nothing ----------

func TestWAEveryFailureLeavesTheFolderAsItWas(t *testing.T) {
	waSkipWindows(t)
	s, wroot, rroot := newWriteStore(t, 16)
	writeFile(t, filepath.Join(wroot, "keep.txt"), "KEEP")
	mkdir(t, filepath.Join(wroot, "dir.txt"))
	writeFile(t, filepath.Join(wroot, "dir.txt", "child"), "c")
	before := waNames(t, wroot)
	rbefore := waNames(t, rroot)
	cases := []struct {
		name, path, data, enc string
		overwrite             bool
	}{
		{"extension", filepath.Join(wroot, "x.exe"), "x", "", true},
		{"no extension", filepath.Join(wroot, "noext"), "x", "", true},
		{"too big", filepath.Join(wroot, "keep.txt"), strings.Repeat("a", 17), "", true},
		{"too big exclusive", filepath.Join(wroot, "big.txt"), strings.Repeat("a", 17), "", false},
		{"bad base64", filepath.Join(wroot, "keep.txt"), "@@@", "base64", true},
		{"bad encoding", filepath.Join(wroot, "keep.txt"), "x", "latin1", true},
		{"exists", filepath.Join(wroot, "keep.txt"), "NEW", "", false},
		{"missing parent", filepath.Join(wroot, "nope", "x.txt"), "x", "", true},
		{"missing parent exclusive", filepath.Join(wroot, "nope", "x.txt"), "x", "", false},
		{"parent is a file", filepath.Join(wroot, "keep.txt", "x.txt"), "x", "", true},
		{"target is a non-empty dir", filepath.Join(wroot, "dir.txt"), "x", "", true},
		{"target is a dir exclusive", filepath.Join(wroot, "dir.txt"), "x", "", false},
		{"read only", filepath.Join(rroot, "x.txt"), "x", "", true},
		{"outside", filepath.Join(filepath.Dir(wroot), "x.txt"), "x", "", true},
		{"traversal", wroot + "/../x.txt", "x", "", true},
		{"relative", "x.txt", "x", "", true},
		{"empty path", "", "x", "", true},
	}
	for _, c := range cases {
		n, err := s.Write(c.path, c.data, c.enc, c.overwrite)
		if err == nil || n != 0 {
			t.Errorf("%s: Write = %d, %v; want 0 and an error", c.name, n, err)
		}
	}
	if got := waNames(t, wroot); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Errorf("write folder changed: %q -> %q", before, got)
	}
	if got := waNames(t, rroot); strings.Join(got, ",") != strings.Join(rbefore, ",") {
		t.Errorf("read-only folder changed: %q -> %q", rbefore, got)
	}
	if got := readBack(t, filepath.Join(wroot, "keep.txt")); got != "KEEP" {
		t.Errorf("an existing file was touched by a failed write: %q", got)
	}
	if got := waNames(t, filepath.Join(wroot, "dir.txt")); strings.Join(got, ",") != "child" {
		t.Errorf("directory target changed: %q", got)
	}
}

func TestWAWriteRootNamedLikeAFileCannotBeReplaced(t *testing.T) {
	waSkipWindows(t)
	base := newRoot(t, "Base")
	wroot := filepath.Join(base, "Out.txt")
	mkdir(t, wroot)
	s, err := New(Config{WriteRoots: []string{wroot}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(wroot, "x", "", true); err == nil {
		t.Fatal("the write folder itself was replaced")
	}
	if fi, err := os.Stat(wroot); err != nil || !fi.IsDir() {
		t.Fatalf("write folder no longer a directory: %v", err)
	}
}

// ---------- sizes ----------

func TestWASizeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		max  int64
		size int
		ok   bool
	}{
		{0, 256 << 10, true}, {0, 256<<10 + 1, false}, {1, 1, true}, {1, 2, false}, {1, 0, true},
		{1 << 20, 1 << 20, true}, {1 << 20, 1<<20 + 1, false},
	} {
		s, wroot, _ := newWriteStore(t, tc.max)
		p := filepath.Join(wroot, "f.txt")
		data := strings.Repeat("a", tc.size)
		n, err := s.Write(p, data, "", false)
		if (err == nil) != tc.ok {
			t.Errorf("max=%d size=%d: err = %v, want ok=%v", tc.max, tc.size, err, tc.ok)
			continue
		}
		if tc.ok {
			if n != int64(tc.size) || readBack(t, p) != data {
				t.Errorf("max=%d size=%d: wrote %d bytes, content len %d", tc.max, tc.size, n, len(readBack(t, p)))
			}
		} else if n != 0 || len(waNames(t, wroot)) != 0 {
			t.Errorf("max=%d size=%d: a refused write left %q (n=%d)", tc.max, tc.size, waNames(t, wroot), n)
		}
	}
}

func TestWASizeLimitCountsBytesNotCharacters(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 4)
	if _, err := s.Write(filepath.Join(wroot, "a.txt"), "\u00e9\u00e9", "", false); err != nil { // 4 bytes
		t.Fatal(err)
	}
	if _, err := s.Write(filepath.Join(wroot, "b.txt"), "\u00e9\u00e9\u00e9", "", false); err == nil { // 6 bytes, 3 runes
		t.Fatal("6 bytes accepted with a 4 byte limit")
	}
	if _, err := s.Write(filepath.Join(wroot, "c.txt"), "\U0001f600\U0001f600", "", false); err == nil { // 8 bytes, 2 runes
		t.Fatal("8 bytes accepted with a 4 byte limit")
	}
}

func TestWABase64SizeIsTheDecodedSize(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 3)
	raw := []byte{0, 255, 128}
	enc := base64.StdEncoding.EncodeToString(raw) // 4 chars for 3 bytes
	if n, err := s.Write(filepath.Join(wroot, "a.png"), enc, "base64", false); err != nil || n != 3 {
		t.Fatalf("exact limit: %d, %v", n, err)
	}
	if got, _ := os.ReadFile(filepath.Join(wroot, "a.png")); !bytes.Equal(got, raw) {
		t.Fatalf("bytes = %v", got)
	}
	enc4 := base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4})
	if n, err := s.Write(filepath.Join(wroot, "b.png"), enc4, "base64", false); err == nil || n != 0 {
		t.Fatalf("4 decoded bytes over a 3 byte limit: %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(wroot, "b.png")); err == nil {
		t.Fatal("b.png was created")
	}
}

func TestWANewRefusesLimitsOutsideTheRange(t *testing.T) {
	r := newRoot(t, "R")
	for _, m := range []int64{-1, 1<<20 + 1, 1 << 40} {
		if _, err := New(Config{WriteRoots: []string{r}, MaxBytes: m}); err == nil {
			t.Errorf("MaxBytes %d accepted", m)
		}
	}
}

func TestWAEmptyContentWritesAnEmptyFile(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	for _, enc := range []string{"", "utf8", "base64"} {
		p := filepath.Join(wroot, "e-"+enc+".txt")
		if n, err := s.Write(p, "", enc, false); err != nil || n != 0 {
			t.Errorf("enc %q: %d, %v", enc, n, err)
		}
		if fi, err := os.Stat(p); err != nil || fi.Size() != 0 {
			t.Errorf("enc %q: %v %v", enc, fi, err)
		}
	}
}

// ---------- base64 ----------

func TestWABase64MalformedIsRefusedWithoutWriting(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	for _, bad := range []string{"YQ=", "YQ", "Y", " YQ==", "YQ== ", "\tYQ==", "-_-_", "YQ==YQ==", "YQ=a", "Y===", "=", "====", "YQ\x00=", "YQ==\x00", "\u0059\u0051==", "YW\u200bJj"} {
		if bad == "\u0059\u0051==" { // plain "YQ==" is valid: make sure the table is sane
			continue
		}
		if n, err := s.Write(filepath.Join(wroot, "b.txt"), bad, "base64", true); err == nil {
			t.Errorf("%q accepted (%d bytes)", bad, n)
		}
	}
	if got := waNames(t, wroot); len(got) != 0 {
		t.Fatalf("malformed base64 left files behind: %q", got)
	}
}

// Whatever the policy on line breaks inside base64, the result must be all-or-nothing and equal to the decoded bytes.
func TestWABase64LineBreaksAreAllOrNothing(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	raw := bytes.Repeat([]byte("0123456789abcdef"), 20)
	enc := base64.StdEncoding.EncodeToString(raw)
	var wrapped strings.Builder
	for i := 0; i < len(enc); i += 76 {
		end := min(i+76, len(enc))
		wrapped.WriteString(enc[i:end] + "\r\n")
	}
	for i, data := range []string{wrapped.String(), enc[:10] + "\n" + enc[10:], enc + "\n"} {
		p := filepath.Join(wroot, fmt.Sprintf("w%d.pdf", i))
		if _, err := s.Write(p, data, "base64", false); err != nil {
			if _, statErr := os.Stat(p); statErr == nil {
				t.Errorf("case %d: refused (%v) but a file exists", i, err)
			}
			continue
		}
		if got, _ := os.ReadFile(p); !bytes.Equal(got, raw) {
			t.Errorf("case %d: accepted but content differs (%d vs %d bytes)", i, len(got), len(raw))
		}
	}
	if n, err := s.Write(filepath.Join(wroot, "ok.pdf"), "YQ==", "base64", false); err != nil || n != 1 {
		t.Fatalf("plain padded base64: %d, %v", n, err)
	}
}

// ---------- links ----------

func TestWALinksAtTheTargetNeverReachBehindThem(t *testing.T) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	outside := newRoot(t, "Outside")
	victim := filepath.Join(outside, "victim.txt")
	writeFile(t, victim, "KEEP")
	hard := filepath.Join(wroot, "hard.txt")
	if err := os.Link(victim, hard); err != nil {
		t.Skipf("no hard links: %v", err)
	}
	waSymlink(t, victim, filepath.Join(wroot, "file.txt"))
	waSymlink(t, filepath.Join(outside, "new.txt"), filepath.Join(wroot, "dangling.txt")) // does not exist yet
	waSymlink(t, outside, filepath.Join(wroot, "dir.txt"))
	waSymlink(t, "chain2.txt", filepath.Join(wroot, "chain1.txt"))
	waSymlink(t, victim, filepath.Join(wroot, "chain2.txt"))
	waSymlink(t, "loop2.txt", filepath.Join(wroot, "loop1.txt"))
	waSymlink(t, "loop1.txt", filepath.Join(wroot, "loop2.txt"))
	for _, name := range []string{"hard.txt", "file.txt", "dangling.txt", "dir.txt", "chain1.txt", "loop1.txt"} {
		for _, ow := range []bool{false, true} {
			s.Write(filepath.Join(wroot, name), "PWNED", "", ow)
		}
	}
	if got := readBack(t, victim); got != "KEEP" {
		t.Errorf("file outside was changed: %q", got)
	}
	if got := waNames(t, outside); strings.Join(got, ",") != "victim.txt" {
		t.Errorf("outside folder gained entries: %q", got)
	}
}

func TestWALinkedFoldersInTheMiddleOfThePath(t *testing.T) {
	waSkipWindows(t)
	s, wroot, rroot := newWriteStore(t, 0)
	outside := newRoot(t, "Outside")
	mkdir(t, filepath.Join(wroot, "real"))
	waSymlink(t, outside, filepath.Join(wroot, "out"))
	waSymlink(t, rroot, filepath.Join(wroot, "ro"))
	waSymlink(t, filepath.Join(wroot, "real"), filepath.Join(wroot, "in"))
	waSymlink(t, "../Outside", filepath.Join(wroot, "real", "up"))
	for _, p := range []string{"out/x.txt", "ro/x.txt", "real/up/x.txt", "in/../out/x.txt", "out/../x.txt"} {
		for _, ow := range []bool{false, true} {
			s.Write(filepath.Join(wroot, p), "x", "", ow)
		}
	}
	if got := waNames(t, outside); len(got) != 0 {
		t.Errorf("outside folder gained %q", got)
	}
	if got := waNames(t, rroot); len(got) != 0 {
		t.Errorf("read-only folder gained %q", got)
	}
	if _, err := s.Write(filepath.Join(wroot, "ro", "x.txt"), "x", "", false); !errors.Is(err, ErrReadOnly) {
		t.Errorf("write to a read-only folder through a link: %v, want ErrReadOnly", err)
	}
	if _, err := s.Write(filepath.Join(wroot, "in", "ok.txt"), "x", "", false); err != nil {
		t.Errorf("a link that stays inside was refused: %v", err)
	}
}

func TestWAFifoAtTheTargetDoesNotHang(t *testing.T) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	for _, ow := range []bool{false, true} {
		p := filepath.Join(wroot, fmt.Sprintf("fifo%v.txt", ow))
		if err := mkfifo(p); err != nil {
			t.Skipf("no mkfifo: %v", err)
		}
		done := make(chan error, 1)
		go func() { _, err := s.Write(p, "x", "", ow); done <- err }()
		select {
		case err := <-done:
			if !ow && !errors.Is(err, ErrExists) {
				t.Errorf("exclusive over a FIFO: %v, want ErrExists", err)
			}
			if ow && err == nil {
				if fi, _ := os.Lstat(p); !fi.Mode().IsRegular() || readBack(t, p) != "x" {
					t.Errorf("overwrite over a FIFO left %v", fi.Mode())
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("Write hung on a FIFO target (overwrite=%v)", ow)
		}
	}
	if got := tmpLeftovers(t, wroot); len(got) != 0 {
		t.Errorf("temporary files left: %q", got)
	}
}

func TestWAParentIsAFifoOrMissingCreatesNothing(t *testing.T) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	if _, err := s.Write(filepath.Join(wroot, "a", "b", "c.txt"), "x", "", true); err == nil {
		t.Fatal("a missing parent was accepted")
	}
	if got := waNames(t, wroot); len(got) != 0 {
		t.Fatalf("directories were created: %q", got)
	}
}

// ---------- long names and paths ----------

func TestWALongNames(t *testing.T) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	ok := strings.Repeat("a", 251) + ".txt" // 255 bytes, the usual NAME_MAX
	for _, ow := range []bool{false, true} {
		if _, err := s.Write(filepath.Join(wroot, ok), "x", "", ow); err != nil {
			t.Errorf("255 byte name, overwrite=%v: %v", ow, err)
		}
	}
	tooLong := strings.Repeat("a", 252) + ".txt"
	deep := wroot
	for i := 0; i < 40; i++ {
		deep = filepath.Join(deep, strings.Repeat("d", 200))
	}
	for _, ow := range []bool{false, true} {
		if n, err := s.Write(filepath.Join(wroot, tooLong), "x", "", ow); err == nil || n != 0 {
			t.Errorf("256 byte name accepted (overwrite=%v): %d, %v", ow, n, err)
		}
		if n, err := s.Write(filepath.Join(deep, "x.txt"), "x", "", ow); err == nil || n != 0 {
			t.Errorf("8 KB path accepted (overwrite=%v): %d, %v", ow, n, err)
		}
	}
	if got := tmpLeftovers(t, wroot); len(got) != 0 {
		t.Errorf("temporary files left: %q", got)
	}
	if got := waNames(t, wroot); len(got) != 1 || got[0] != ok {
		t.Errorf("unexpected entries: %q", got)
	}
}

// ---------- overwrite semantics ----------

func TestWAOverwriteCreatesWhenMissingAndReplacesWholeContent(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	p := filepath.Join(wroot, "f.txt")
	if _, err := s.Write(p, "first-long-content", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(p, "b", "", true); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, p); got != "b" {
		t.Fatalf("content = %q, want a clean replacement", got)
	}
	if n, err := s.Write(p, "", "", true); err != nil || n != 0 || readBack(t, p) != "" {
		t.Fatalf("empty replacement: %d, %v, %q", n, err, readBack(t, p))
	}
	if got := tmpLeftovers(t, wroot); len(got) != 0 {
		t.Fatalf("temporary files left: %q", got)
	}
}

func TestWAWritesNeverLeaveTemporaryFilesAndTmpNamesAreNotWritable(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	for i := 0; i < 50; i++ {
		s.Write(filepath.Join(wroot, "f.txt"), strings.Repeat("x", i), "", true)
		s.Write(filepath.Join(wroot, "bad.exe"), "x", "", true)
		s.Write(filepath.Join(wroot, "f.txt"), "x", "", false)
	}
	if got := waNames(t, wroot); strings.Join(got, ",") != "f.txt" {
		t.Fatalf("entries = %q", got)
	}
	for _, name := range []string{".wb-1.tmp", ".wb-.tmp", "x.TMP"} {
		if _, err := s.Write(filepath.Join(wroot, name), "x", "", true); !errors.Is(err, ErrExtension) {
			t.Errorf("%q: %v, want ErrExtension", name, err)
		}
	}
}

// ---------- concurrency ----------

func TestWAConcurrentOverwriteLeavesOneCompleteContent(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	p := filepath.Join(wroot, "shared.txt")
	const writers = 8
	payload := func(i int) string { return strings.Repeat(string(rune('A'+i)), 1000*(i+1)) }
	valid := map[string]bool{}
	for i := 0; i < writers; i++ {
		valid[payload(i)] = true
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var bad atomic.Value
	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() { // a reader must never see a mixed or half written content
		defer readerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(p); err == nil && !valid[string(b)] && len(b) > 0 {
				bad.Store(fmt.Sprintf("reader saw %d bytes, first %q", len(b), b[:1]))
				return
			} else if err == nil && len(b) == 0 {
				bad.Store("reader saw an empty file")
				return
			}
		}
	}()
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 100; k++ {
				if _, err := s.Write(p, payload(i), "", true); err != nil {
					bad.Store("write failed: " + err.Error())
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	readerWG.Wait()
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
	if !valid[readBack(t, p)] {
		t.Fatal("final content is mixed or incomplete")
	}
	if got := waNames(t, wroot); strings.Join(got, ",") != "shared.txt" {
		t.Fatalf("entries = %q (leftover temporaries?)", got)
	}
}

func TestWAConcurrentExclusiveHasOneWinnerWithItsOwnCompleteContent(t *testing.T) {
	s, wroot, _ := newWriteStore(t, 0)
	for round := 0; round < 40; round++ {
		p := filepath.Join(wroot, fmt.Sprintf("r%d.txt", round))
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				switch _, err := s.Write(p, strings.Repeat(string(rune('A'+i)), 4096), "", false); {
				case err == nil:
					wins.Add(1)
				case !errors.Is(err, ErrExists):
					t.Errorf("round %d: err = %v", round, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		got := readBack(t, p)
		if wins.Load() != 1 || len(got) != 4096 || strings.Count(got, got[:1]) != 4096 {
			t.Fatalf("round %d: %d winners, content len %d", round, wins.Load(), len(got))
		}
	}
	if got := tmpLeftovers(t, wroot); len(got) != 0 {
		t.Fatalf("temporary files left: %q", got)
	}
}

// A link appears and disappears at the target name while Write runs: nothing behind the link may change, either way.
func TestWATargetSwappedForALinkDuringWrites(t *testing.T) {
	waSkipWindows(t)
	for _, ow := range []bool{false, true} {
		s, wroot, _ := newWriteStore(t, 0)
		outside := newRoot(t, "Outside")
		victim := filepath.Join(outside, "victim.txt")
		writeFile(t, victim, "KEEP")
		target := filepath.Join(wroot, "t.txt")
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				os.Remove(target)
				if os.Symlink(victim, target) == nil {
					os.Remove(target)
				}
			}
		}()
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			s.Write(target, "PWNED", "", ow)
		}
		close(stop)
		wg.Wait()
		if got := readBack(t, victim); got != "KEEP" {
			t.Errorf("overwrite=%v: file behind the swapped link changed: %q", ow, got)
		}
		if got := waNames(t, outside); strings.Join(got, ",") != "victim.txt" {
			t.Errorf("overwrite=%v: outside gained %q", ow, got)
		}
	}
}

// waSwapRace flips root/d between a real folder and a link to a folder outside while writers run.
// With seeded the real folder already holds every name the writers use; dataOnly counts only non-empty files outside;
// finalOnly looks only at what is left outside once the writers stop.
func waSwapRace(t *testing.T, overwrite, seeded, dataOnly, finalOnly bool) {
	waSkipWindows(t)
	s, wroot, _ := newWriteStore(t, 0)
	outside := newRoot(t, "Outside")
	real, link, d := filepath.Join(wroot, "real"), filepath.Join(wroot, "lnk"), filepath.Join(wroot, "d")
	mkdir(t, real)
	waSymlink(t, outside, link)
	const names = 4
	if seeded {
		for i := 0; i < names; i++ {
			writeFile(t, filepath.Join(real, fmt.Sprintf("n%d.txt", i)), "INSIDE")
		}
	}
	stop := make(chan struct{})
	var swapper sync.WaitGroup
	swapper.Add(1)
	go func() {
		defer swapper.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Rename(real, d)
			os.Rename(d, real)
			os.Rename(link, d)
			os.Rename(d, link)
		}
	}()
	var violation atomic.Value
	var writes atomic.Int64
	deadline := time.Now().Add(3 * time.Second)
	var writers sync.WaitGroup
	for g := 0; g < 4; g++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for time.Now().Before(deadline) {
				writes.Add(1)
				s.Write(filepath.Join(d, fmt.Sprintf("n%d.txt", int(writes.Load())%names)), "PWNED", "", overwrite)
				for _, n := range waNames(t, outside) {
					if finalOnly {
						break
					}
					fi, err := os.Stat(filepath.Join(outside, n))
					if err != nil || (dataOnly && fi.Size() == 0) {
						continue
					}
					violation.Store(fmt.Sprintf("%s (%d bytes)", n, fi.Size()))
					os.Remove(filepath.Join(outside, n))
				}
			}
		}()
	}
	writers.Wait()
	close(stop)
	swapper.Wait()
	if left := waNames(t, outside); finalOnly && len(left) != 0 {
		t.Fatalf("overwrite=%v: %d entries left outside after the writers stopped, e.g. %q (%d writes)", overwrite, len(left), left[0], writes.Load())
	}
	if v := violation.Load(); v != nil {
		t.Fatalf("overwrite=%v seeded=%v dataOnly=%v: last file seen outside the write folder: %v (%d writes)", overwrite, seeded, dataOnly, v, writes.Load())
	}
	t.Logf("%d writes, nothing outside", writes.Load())
}

func TestWASwapRaceExclusiveSeededNeverWritesDataOutside(t *testing.T) {
	waSwapRace(t, false, true, true, false)
}
func TestWASwapRaceOverwriteNeverCreatesOutside(t *testing.T) {
	waSwapRace(t, true, false, false, false)
}
func TestWASwapRaceOverwriteLeavesNothingOutsideAtTheEnd(t *testing.T) {
	waSwapRace(t, true, false, false, true)
}
func TestWASwapRaceExclusiveLeavesNothingOutsideAtTheEnd(t *testing.T) {
	waSwapRace(t, false, false, false, true)
}

// ---------- Windows names, checked from any system ----------

func TestWAWindowsNamesForWrite(t *testing.T) {
	bad := []string{
		`C:\Out\a.txt:s`, `C:\Out\a.txt::$DATA`, `C:\Out\a.txt:`, `C:\Out:s\a.txt`, `C:\Out\a.txt:s:$DATA`,
		`C:\Out\NUL`, `C:\Out\NUL.txt`, `C:\Out\nul.TXT`, `C:\Out\CON.txt`, `C:\Out\con`, `C:\Out\aux.md`, `C:\Out\PRN.log`,
		`C:\Out\COM1.txt`, `C:\Out\com9.json`, `C:\Out\LPT3.csv`, "C:\\Out\\COM\u00b9.txt", "C:\\Out\\LPT\u00b2.txt", "C:\\Out\\COM\u00b3",
		`C:\Out\NUL .txt`, `C:\Out\NUL.x.txt`, `C:\Out\NUL\a.txt`, `C:\Out\a.txt\NUL`, `C:\Out\sub\CON.d\a.txt`,
		`C:\Out\a.txt.`, `C:\Out\a.txt `, `C:\Out\a.txt . .`, `C:\Out\a.txt...`, `C:\Out\dir.\a.txt`, `C:\Out\dir \a.txt`, `C:\Out\ . \a.txt`,
		`C:Out\a.txt`, `C:a.txt`, `\Out\a.txt`, `a.txt`, `Out\a.txt`, `\\?\C:\Out\a.txt`, `\\.\NUL`, `\\.\C:\Out\a.txt`,
		`\\server\share\a.txt`, `//?/C:/a.txt`, `//./NUL`, `\\?\UNC\server\share\a.txt`,
		`C:\Out\..\a.txt`, `C:\Out\.\a.txt`, `C:\Out\..\..\a.txt`, `C:/Out/../a.txt`, `C:/Out/a.txt:s`, `C:/Out/NUL.txt`,
		"C:\\Out\\a.txt\x00.exe", "C:\\Out\\a.txt\x00", "", `C:`, `C:\Out\a.txt:s.exe`, `C:\Out\a.TXT:evil.ps1`,
	}
	for _, p := range bad {
		if err := checkName(p, true); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("checkName(%q) = %v, want ErrInvalidPath", p, err)
		}
	}
	good := []string{
		`C:\Out\a.txt`, `C:/Out/a.txt`, `c:\out\A.TXT`, `C:\Out\sub dir\a b.txt`, `C:\Out\CONSOLE.txt`, `C:\Out\NULL.txt`, `C:\Out\COM10.txt`,
		`C:\Out\LPT.txt`, `C:\Out\.hidden.txt`, "C:\\Out\\\u00fc\u00f1.txt", `C:\Out\a.b.c.txt`, `C:\Out\COM.txt`, `C:\Out\a nul.txt`,
	}
	for _, p := range good {
		if err := checkName(p, true); err != nil {
			t.Errorf("checkName(%q) = %v, want nil", p, err)
		}
	}
}
