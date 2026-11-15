package files

// Adversarial tests for the files package. Tests whose name starts with TestFixed_ are known reproducers: they FAIL on
// purpose until the production code is fixed. Everything else is expected to pass.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// ---------- fixture ----------

// advFix is one allowed root (Shared) with hostile links, plus secrets in Outside and in Shared2.
type advFix struct{ base, root, outside, sibling string }

func advWrite(tb testing.TB, path, text string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		tb.Fatal(err)
	}
}

func advLink(tb testing.TB, target, link string) {
	tb.Helper()
	if err := os.Symlink(target, link); err != nil {
		tb.Skipf("cannot create a symlink here: %v", err)
	}
}

func newAdvFix(tb testing.TB) advFix {
	tb.Helper()
	base, err := canonicalDir(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	fx := advFix{base: base, root: filepath.Join(base, "Shared"), outside: filepath.Join(base, "Outside"), sibling: filepath.Join(base, "Shared2")}
	advWrite(tb, filepath.Join(fx.root, "in.txt"), "INSIDE-1")
	advWrite(tb, filepath.Join(fx.root, "sub", "in2.txt"), "INSIDE-2")
	advWrite(tb, filepath.Join(fx.outside, "secret.txt"), "OUTSIDE-SECRET")
	advWrite(tb, filepath.Join(fx.sibling, "secret.txt"), "OUTSIDE-SECRET-2")
	for link, target := range map[string]string{
		"lnk_out_dir":  filepath.Join("..", "Outside"),
		"lnk_out_file": filepath.Join("..", "Outside", "secret.txt"),
		"lnk_abs":      filepath.Join(fx.outside, "secret.txt"),
		"lnk_c1":       "lnk_c2",
		"lnk_c2":       filepath.Join("..", "Outside", "secret.txt"),
		"lnk_sib":      filepath.Join("..", "Shared2"),
		"lnk_up":       "..",
		"lnk_in":       filepath.Join("sub", "in2.txt"),
		"lnk_dir_in":   "sub",
		"lnk_loop1":    "lnk_loop2",
		"lnk_loop2":    "lnk_loop1",
		"lnk_dangling": "nowhere",
	} {
		advLink(tb, target, filepath.Join(fx.root, link))
	}
	advLink(tb, filepath.Join("..", "..", "Outside"), filepath.Join(fx.root, "sub", "lnk_deep"))
	return fx
}

func newAdvStore(tb testing.TB, fx advFix, maxBytes int64) *Store {
	tb.Helper()
	s, err := New(Config{ReadRoots: []string{fx.root}, MaxBytes: maxBytes})
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if onWindows {
		t.Skip("uses unix path semantics")
	}
}

// ---------- symlinks that escape ----------

func TestAdvSymlinksThatEscapeAreRefused(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 0)
	j := func(parts ...string) string { return filepath.Join(append([]string{fx.root}, parts...)...) }
	files := []string{
		j("lnk_out_dir", "secret.txt"), j("lnk_out_file"), j("lnk_abs"), j("lnk_c1"), j("lnk_c2"),
		j("lnk_sib", "secret.txt"), j("lnk_up", "Outside", "secret.txt"), j("lnk_up", "Shared2", "secret.txt"),
		j("sub", "lnk_deep", "secret.txt"), j("lnk_dir_in", "lnk_deep", "secret.txt"),
		j("lnk_up", "Shared", "lnk_out_file"),
	}
	for _, p := range files {
		c, err := s.Read(p, "", 0)
		if !errors.Is(err, ErrOutside) || c.Data != "" {
			t.Errorf("Read(%q) = %+v, %v; want ErrOutside and no data", p, c, err)
		}
		c, err = s.Read(p, "base64", 0)
		if !errors.Is(err, ErrOutside) || c.Data != "" {
			t.Errorf("Read base64 (%q) = %+v, %v; want ErrOutside and no data", p, c, err)
		}
	}
	for _, d := range []string{j("lnk_out_dir"), j("lnk_sib"), j("lnk_up"), j("sub", "lnk_deep"), filepath.Dir(fx.root)} {
		if got, _, err := s.List(d); !errors.Is(err, ErrOutside) || len(got) != 0 {
			t.Errorf("List(%q) = %v, %v; want ErrOutside and no entries", d, got, err)
		}
	}
}

func TestAdvSymlinksInsideStillWork(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 0)
	if c, err := s.Read(filepath.Join(fx.root, "lnk_in"), "", 0); err != nil || c.Data != "INSIDE-2" {
		t.Errorf("link to a file inside: %+v, %v", c, err)
	}
	if got, _, err := s.List(filepath.Join(fx.root, "lnk_dir_in")); err != nil || len(got) == 0 {
		t.Errorf("link to a folder inside: %v, %v", got, err)
	}
}

func TestAdvSymlinkLoopsAndDanglingLinksFailFast(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, n := range []string{"lnk_loop1", "lnk_loop2", "lnk_dangling"} {
			if c, err := s.Read(filepath.Join(fx.root, n), "", 0); err == nil || c.Data != "" {
				t.Errorf("Read(%s) = %+v, %v; want an error", n, c, err)
			}
			if _, _, err := s.List(filepath.Join(fx.root, n)); err == nil {
				t.Errorf("List(%s) must fail", n)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("symlink loop made Read/List hang")
	}
}

// ---------- BUG: a backslash in a unix file name is read as a path separator ----------

// A sibling file named `Shared\secret.txt` must not count as inside the root `Shared` on unix.
func TestFixed_BackslashInUnixNameEscapesTheRoot(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 0)
	evil := filepath.Join(fx.base, `Shared\secret.txt`) // one file, sitting next to the root, not inside it
	advWrite(t, evil, "OUTSIDE-BACKSLASH")
	if c, err := s.Read(evil, "", 0); err == nil {
		t.Errorf("Read(%q) returned %q from OUTSIDE the root; want ErrOutside", evil, c.Data)
	}
	evilDir := filepath.Join(fx.base, `Shared\dir`)
	advWrite(t, filepath.Join(evilDir, "x.txt"), "x")
	if got, _, err := s.List(evilDir); err == nil {
		t.Errorf("List(%q) listed %d entries from OUTSIDE the root; want ErrOutside", evilDir, len(got))
	}
	if within("/r/Shared", `/r/Shared\x`, false) {
		t.Error(`within("/r/Shared", "/r/Shared\\x") = true on a unix path where the backslash is part of a name`)
	}
}

// ---------- random hostile paths: property and fuzzing ----------

// advCheckNeverOutside asserts the central invariant for one path: nothing outside the root is ever returned.
func advCheckNeverOutside(t testing.TB, s *Store, p string) (readOK, listOK bool) {
	if c, err := s.Read(p, "", 0); err == nil {
		readOK = true
		if !strings.HasPrefix(c.Data, "INSIDE") {
			t.Fatalf("Read(%q) returned %q: content from outside the root", p, c.Data)
		}
	}
	if c, err := s.Read(p, "base64", 0); err == nil {
		raw, derr := base64.StdEncoding.DecodeString(c.Data)
		if derr != nil || !strings.HasPrefix(string(raw), "INSIDE") {
			t.Fatalf("Read base64 (%q) returned %q: content from outside the root", p, raw)
		}
	}
	if got, _, err := s.List(p); err == nil {
		listOK = true
		for _, e := range got {
			if strings.HasPrefix(e.Name, "secret") {
				t.Fatalf("List(%q) shows %q: a folder outside the root", p, e.Name)
			}
		}
	}
	return
}

func TestAdvRandomHostilePathsNeverLeakOutsideTheRoot(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 0)
	segs := []string{"..", ".", "", "sub", "in.txt", "in2.txt", "lnk_out_dir", "lnk_out_file", "lnk_up", "lnk_in", "lnk_dir_in",
		"lnk_c1", "lnk_sib", "lnk_loop1", "Shared", "Shared2", "Outside", "secret.txt", "SHARED", "sub/..", "./", "...", "in.txt/",
		"in.txt/.", "in.txt/..", "%2e%2e", " ", "in.txt ", "in.txt.", "~1", "lnk_deep", "\x00", "../Shared2", "../Outside"}
	starts := []string{fx.root, fx.root + "/", fx.base, fx.sibling, fx.outside, "/", "", "Shared", "./Shared", filepath.Dir(fx.base)}
	rng := rand.New(rand.NewSource(20260114))
	reads, lists := 0, 0
	for i := 0; i < 20000; i++ {
		p := starts[rng.Intn(len(starts))]
		for n := 1 + rng.Intn(8); n > 0; n-- {
			p += "/" + segs[rng.Intn(len(segs))]
		}
		r, l := advCheckNeverOutside(t, s, p)
		if r {
			reads++
		}
		if l {
			lists++
		}
	}
	if reads == 0 || lists == 0 {
		t.Fatalf("the generator never produced a valid inside path (reads=%d lists=%d): the property is vacuous", reads, lists)
	}
	t.Logf("20000 paths: %d inside reads, %d inside lists accepted, the rest refused", reads, lists)
}

func FuzzOpenPath(f *testing.F) {
	if onWindows {
		f.Skip("uses unix path semantics")
	}
	fx := newAdvFix(f)
	s := newAdvStore(f, fx, 0)
	bases := []string{fx.root, fx.base, fx.sibling, "", fx.root + "/lnk_out_dir", fx.root + "/lnk_up"}
	for i, seed := range []string{"/in.txt", "/../Outside/secret.txt", "/secret.txt", "/lnk_out_file", "/./sub/../in.txt", "//in.txt",
		"/lnk_sib/secret.txt", "/sub/lnk_deep/secret.txt", "/..", "/../Shared2/secret.txt", "/in.txt/..", "/in.txt/", "\x00",
		"/Shared/lnk_up/Outside/secret.txt", "/lnk_loop1", "/%2e%2e/Outside/secret.txt"} {
		f.Add(uint8(i%len(bases)), seed)
		f.Add(uint8(0), seed)
	}
	f.Fuzz(func(t *testing.T, sel uint8, suffix string) {
		advCheckNeverOutside(t, s, bases[int(sel)%len(bases)]+suffix)
	})
}

// ---------- checkName ----------

func TestAdvCheckNameHostileWindowsPaths(t *testing.T) {
	for _, p := range []string{
		`C:\Shared\a.txt::$DATA`, `C:\Shared\a.txt:stream`, `C:\Shared:x\a`, `C:\Shared\NUL.`, `C:\Shared\nul.tar.gz`,
		`C:\Shared\Aux`, `C:\Shared\PRN.txt`, `C:\Shared\CON .txt`, `C:\Shared\COM9`, `C:\Shared\LPT1.`, `C:/Shared/NUL`,
		`C:\Shared\CON\a.txt`, `C:\Shared\a\..`, `C:\Shared\...`, `C:\Shared\.. `, `C:\Shared\a. `, `C:\Shared\a \b`,
		`C:\Shared\SECRET~1.TXT`, `C:\Shared\A~1`, `C:\Shared\PROGRA~2\x`,
		`\\?\C:\Shared\a`, `\\?\UNC\srv\share\a`, `\\.\C:\Shared\a`, `//?/C:/Shared/a`, `//./NUL`, `\\srv\share\a`, `//srv/share/a`,
		`\Shared\a`, `\C:\Shared\a`, `C:`, `C:Shared\a`, `C:a`, `:\a`, `1:\a`, `CC:\a`, `Shared\a`, `.\a`, `..\a`, "", "\x00",
		"C:\\Shared\\a\x00.txt", "C:\\\x00", `C:\Shared\a.txt` + "\x00",
	} {
		if err := checkName(p, true); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("checkName(%q, windows) = %v, want ErrInvalidPath", p, err)
		}
	}
}

func TestAdvCheckNameValidWindowsPathsStillPass(t *testing.T) {
	for _, p := range []string{`C:\`, `C:/`, `c:\Shared`, `C:\Shared\.hidden`, `C:\Shared\a.b.c`, `C:\Shared\CONSOLE.txt`,
		`C:\Shared\COM10`, `C:\Shared\COM`, `C:\Shared\LPT`, `C:\Shared\NULL`, `C:\Shared\a ~1b`, `C:\Shared\longname~1`, `C:\Shared\Prog~1x`,
		`C:\\Shared\\\a`, `C:\Shared\a b\c d.txt`, `Z:\x`, `C:\Shared\ñandú.txt`, `C:\Shared\日本語.txt`} {
		if err := checkName(p, true); err != nil {
			t.Errorf("checkName(%q, windows) = %v, want nil", p, err)
		}
	}
}

// TestFixed_CheckNameAcceptsEightDotThreeNamesWithTwoDigitCounter: Windows builds short names as 6 chars + ~N for N<10 but
// 5 chars + ~N for N=10..99, 4 chars + ~N for N=100.. The regexp only knows one digit, so PROGR~10 slips through.
func TestFixed_CheckNameAcceptsEightDotThreeNamesWithTwoDigitCounter(t *testing.T) {
	for _, p := range []string{`C:\Shared\PROGR~10`, `C:\Shared\DOCUM~11.TXT`, `C:\Shared\SECR~100.TXT`, `C:\Shared\A~99`} {
		if err := checkName(p, true); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("checkName(%q, windows) = %v, want ErrInvalidPath (8.3 name with a 2-3 digit counter)", p, err)
		}
	}
}

// TestFixed_CheckNameAcceptsSuperscriptDeviceNames: Microsoft's naming doc lists COM1-COM9, COM and LPT1-LPT9 with the
// superscript digits (U+00B9, U+00B2, U+00B3) as reserved device names.
func TestFixed_CheckNameAcceptsSuperscriptDeviceNames(t *testing.T) {
	for _, d := range []string{"\u00b9", "\u00b2", "\u00b3"} {
		for _, p := range []string{`C:\Shared\COM` + d, `C:\Shared\LPT` + d + `.txt`} {
			if err := checkName(p, true); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("checkName(%q, windows) = %v, want ErrInvalidPath (reserved device name)", p, err)
			}
		}
	}
}

func TestAdvCheckNameUnixOnlyNeedsLeadingSlashAndNoNul(t *testing.T) {
	for _, p := range []string{"/", "//", "/a/../b", `/a\b`, "/a:b", "/nul", "/a b ", "/ñ"} {
		if err := checkName(p, false); err != nil {
			t.Errorf("checkName(%q, unix) = %v, want nil", p, err)
		}
	}
	for _, p := range []string{"", " /a", "a/b", "./a", "../a", `\a`, "C:/a", "/a\x00", "\x00/a", "\x00"} {
		if err := checkName(p, false); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("checkName(%q, unix) = %v, want ErrInvalidPath", p, err)
		}
	}
}

var advShort = shortName // same regexp the production code uses, for the oracle below

// advWindowsOracle says why an accepted Windows path is wrong, or "" when it is fine.
func advWindowsOracle(p string) string {
	if len(p) < 3 || !(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z') || p[1] != ':' || p[2] != '\\' && p[2] != '/' {
		return "no drive-letter prefix"
	}
	if strings.ContainsRune(p, 0) || strings.Contains(p[2:], ":") {
		return "NUL or colon in the path"
	}
	for _, c := range strings.FieldsFunc(p[2:], func(r rune) bool { return r == '\\' || r == '/' }) {
		base := strings.ToUpper(strings.TrimRight(strings.SplitN(c, ".", 2)[0], " "))
		switch {
		case c == "." || c == "..":
			return "dot component"
		case strings.HasSuffix(c, ".") || strings.HasSuffix(c, " "):
			return "trailing dot or space in " + c
		case base == "CON" || base == "PRN" || base == "AUX" || base == "NUL":
			return "device " + c
		case len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9':
			return "device " + c
		case advShort.MatchString(c):
			return "8.3 name " + c
		}
	}
	return ""
}

func FuzzCheckName(f *testing.F) {
	for _, s := range []string{
		`C:\Shared\a.txt`, `C:\Shared\a.txt:s`, `C:\Shared\NUL`, `C:\Shared\con.txt`, `C:\Shared\COM1`, `C:\Shared\lpt9.log`,
		`\\?\C:\Shared\a`, `\\.\NUL`, `\\srv\share\a`, `C:\Shared\a.`, "C:\\Shared\\a ", `C:\Shared\PROGRA~1`, `C:\Shared\..\Users`,
		`C:\Shared\.\a`, `C:/Shared/a.txt:s`, "C:\\\x00", `C:\Shared\a.txt::$DATA`, `C:\Shared\NUL .txt`, `C:\Shared\...`,
		`/tmp/x`, "", "a", "/", "\x00", `//?/C:/x`, `C:\Shared\COM\u00b9`, `C:\Shared\ñ`,
	} {
		f.Add(s, true)
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, p string, windows bool) {
		err := checkName(p, windows)
		if err != nil && !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("checkName(%q, %v) returned an unexpected error type: %v", p, windows, err)
		}
		if (p == "" || strings.ContainsRune(p, 0)) && err == nil {
			t.Fatalf("checkName(%q, %v) accepted an empty path or one with NUL", p, windows)
		}
		if !windows {
			if want := p != "" && p[0] == '/' && !strings.ContainsRune(p, 0); (err == nil) != want {
				t.Fatalf("checkName(%q, unix) = %v, want accepted=%v", p, err, want)
			}
			return
		}
		if err == nil {
			if why := advWindowsOracle(p); why != "" {
				t.Fatalf("checkName(%q, windows) accepted it but: %s", p, why)
			}
		}
	})
}

// ---------- within ----------

func FuzzWithin(f *testing.F) {
	for _, s := range [][2]string{
		{`C:\Shared`, `C:\Shared\a.txt`}, {`C:\Shared`, `C:\SharedOther\a.txt`}, {`C:\Shared`, `c:\shared\A.txt`}, {"/a/b", "/a/b/c"},
		{"/a/b", "/a/bc"}, {"/a/b", "/a"}, {"/", "/a"}, {"", ""}, {"", "/a"}, {"/a/b", "/a//b"}, {"/a/b", "/a/b/"}, {"/a/b", "/a/./b"},
		{"/a/K", "/a/\u212a/x"}, {"/a/s", "/a/\u017f"}, {"/a/b", "/A/B"}, {`/a\b`, "/a/b"}, {"/a/b", `/a\b\c`}, {"//", "//"},
	} {
		f.Add(s[0], s[1], true)
		f.Add(s[0], s[1], false)
	}
	f.Fuzz(func(t *testing.T, r, p string, fold bool) {
		if !within(r, r, fold) {
			t.Fatalf("within(%q, itself, %v) = false", r, fold)
		}
		// Folding case must only widen the match. A backslash separates names only in the fold (Windows) mode, so the
		// comparison is meaningful only for names without one.
		if !strings.Contains(r+p, `\`) && within(r, p, false) && !within(r, p, true) {
			t.Fatalf("within(%q, %q): true without folding but false with folding", r, p)
		}
		if !within(r, r+"/child", fold) {
			t.Fatalf("within(%q, root/child, %v) = false", r, fold)
		}
		if len(components(r, fold)) > 0 && !strings.HasSuffix(r, "/") && !strings.HasSuffix(r, `\`) {
			if within(r, r+"zz", fold) {
				t.Fatalf("within(%q, %q, %v) = true: a longer sibling name is not inside", r, r+"zz", fold)
			}
			if within(r+"/child", r, fold) {
				t.Fatalf("within(child, parent) = true for %q", r)
			}
		}
		if within(r, p, fold) && within(p, r, fold) && len(components(r, fold)) != len(components(p, fold)) {
			t.Fatalf("within(%q, %q) both ways with a different depth", r, p)
		}
	})
}

func TestAdvWithinRootAndSiblingTables(t *testing.T) {
	cases := []struct {
		root, p string
		fold    bool
		want    bool
	}{
		{`C:\Shared`, `C:\Shared\\a`, true, true},
		{`C:\Shared`, `C:/Shared/a`, true, true},
		{`C:\Shared\`, `C:\Shared`, true, true},
		{`C:\Shared`, `C:\Share`, true, false},
		{`C:\Shared`, `C:\Shared.bak\a`, true, false},
		{`C:\Shared`, `C:\Shared `, true, false},
		{`C:\Shared`, `D:\Shared\a`, true, false},
		{`C:\Shared`, `C:`, true, false},
		{`C:\Shared`, ``, true, false},
		{`C:\Shared\sub`, `C:\Shared`, true, false},
		{`C:\Ñandú`, `c:\ñandú\x`, true, true},
		{"/a/b", "/a/B", false, false},
		{"/a/b", "/a/b%2f..", false, false},
	}
	for _, c := range cases {
		if got := within(c.root, c.p, c.fold); got != c.want {
			t.Errorf("within(%q, %q, fold=%v) = %v, want %v", c.root, c.p, c.fold, got, c.want)
		}
	}
}

// ---------- Read: offsets, limits, multibyte boundaries ----------

func TestAdvReadOffsetsAtAndBeyondTheEnd(t *testing.T) {
	s, root := newStore(t, 0)
	p := filepath.Join(root, "a.txt")
	writeFile(t, p, "0123456789")
	for _, off := range []int64{10, 11, 1 << 40, math.MaxInt64 - 1, math.MaxInt64} {
		c, err := s.Read(p, "", off)
		if err == nil && (c.Data != "" || c.Truncated || c.Size != 10) {
			t.Errorf("offset %d: %+v, want empty data, not truncated, size 10 (or an error)", off, c)
		}
	}
	for _, off := range []int64{-1, -10, math.MinInt64} {
		for _, enc := range []string{"", "base64"} {
			if c, err := s.Read(p, enc, off); err == nil {
				t.Errorf("offset %d enc %q: want an error, got %+v", off, enc, c)
			}
		}
	}
	if c, err := s.Read(p, "", 9); err != nil || c.Data != "9" || c.Truncated {
		t.Errorf("last byte: %+v, %v", c, err)
	}
	if c, err := s.Read(p, "base64", 0); err != nil || c.Data != base64.StdEncoding.EncodeToString([]byte("0123456789")) {
		t.Errorf("base64: %+v, %v", c, err)
	}
}

func TestAdvReadEmptyFile(t *testing.T) {
	s, root := newStore(t, 0)
	p := filepath.Join(root, "empty")
	writeFile(t, p, "")
	for _, enc := range []string{"", "utf8", "base64"} {
		if c, err := s.Read(p, enc, 0); err != nil || c.Data != "" || c.Truncated || c.Size != 0 {
			t.Errorf("empty file enc %q: %+v, %v", enc, c, err)
		}
	}
}

func TestAdvReadSizesAroundMaxBytes(t *testing.T) {
	const m = 10
	s, root := newStore(t, m)
	for _, n := range []int{m - 1, m, m + 1, 2 * m} {
		p := filepath.Join(root, fmt.Sprintf("f%d.txt", n))
		writeFile(t, p, strings.Repeat("x", n))
		c, err := s.Read(p, "", 0)
		wantLen := min(n, m)
		if err != nil || len(c.Data) != wantLen || c.Truncated != (n > m) || c.Size != int64(n) {
			t.Errorf("size %d, max %d: %+v, %v", n, m, c, err)
		}
		c, err = s.Read(p, "base64", 0)
		raw, _ := base64.StdEncoding.DecodeString(c.Data)
		if err != nil || len(raw) != wantLen || c.Truncated != (n > m) {
			t.Errorf("base64 size %d, max %d: len=%d trunc=%v err=%v", n, m, len(raw), c.Truncated, err)
		}
	}
}

func TestAdvReadDefaultAndHardLimits(t *testing.T) {
	for _, tc := range []struct{ cfg, limit int64 }{{0, 256 << 10}, {1 << 20, 1 << 20}} {
		s, root := newStore(t, tc.cfg)
		for _, n := range []int64{tc.limit - 1, tc.limit, tc.limit + 1} {
			p := filepath.Join(root, fmt.Sprintf("big%d_%d", tc.cfg, n))
			writeFile(t, p, strings.Repeat("y", int(n)))
			c, err := s.Read(p, "", 0)
			if err != nil || int64(len(c.Data)) != min(n, tc.limit) || c.Truncated != (n > tc.limit) {
				t.Errorf("limit %d, file %d: len=%d truncated=%v err=%v", tc.limit, n, len(c.Data), c.Truncated, err)
			}
		}
	}
}

func TestAdvReadMultibyteCharacterAtTheLimit(t *testing.T) {
	const m = 10
	for _, ch := range []string{"ñ", "€", "😀"} {
		w := len(ch)
		s, root := newStore(t, m)
		for k := 1; k < w; k++ { // k bytes of the character fit under the limit
			p := filepath.Join(root, fmt.Sprintf("cut_%d_%d.txt", w, k))
			writeFile(t, p, strings.Repeat("a", m-k)+ch+"bbb")
			c, err := s.Read(p, "", 0)
			if err != nil || c.Data != strings.Repeat("a", m-k) || !c.Truncated || !utf8.ValidString(c.Data) {
				t.Errorf("%q cut after %d of %d bytes: %+v, %v", ch, k, w, c, err)
			}
		}
		p := filepath.Join(root, fmt.Sprintf("fit_%d.txt", w)) // the character ends exactly on the limit, more text follows
		writeFile(t, p, strings.Repeat("a", m-w)+ch+"zzz")
		if c, err := s.Read(p, "", 0); err != nil || c.Data != strings.Repeat("a", m-w)+ch || !c.Truncated || len(c.Data) != m {
			t.Errorf("%q ends on the limit: %+v, %v", ch, c, err)
		}
		p = filepath.Join(root, fmt.Sprintf("end_%d.txt", w)) // the file ends exactly with the character on the limit
		writeFile(t, p, strings.Repeat("a", m-w)+ch)
		if c, err := s.Read(p, "", 0); err != nil || c.Data != strings.Repeat("a", m-w)+ch || c.Truncated {
			t.Errorf("%q is the last thing in the file: %+v, %v", ch, c, err)
		}
	}
}

func TestAdvReadFileEndingInHalfACharacterIsNotText(t *testing.T) {
	s, root := newStore(t, 0)
	p := filepath.Join(root, "half.txt")
	writeFile(t, p, "ok\xe2\x82") // 2 of the 3 bytes of the euro sign, and the file really ends there
	if c, err := s.Read(p, "", 0); err == nil {
		t.Errorf("a file that ends mid-character is not valid UTF-8, got %+v", c)
	}
	if c, err := s.Read(p, "base64", 0); err != nil || c.Truncated {
		t.Errorf("base64 must accept it: %+v, %v", c, err)
	}
}

func TestAdvReadInvalidUTF8InTheMiddleIsRefusedButBase64Works(t *testing.T) {
	s, root := newStore(t, 0)
	p := filepath.Join(root, "bad.txt")
	raw := "a\xc3(b\xf0\x28\x8c\x28c" + "\xed\xa0\x80" + "\xc0\xaf" // overlong, surrogate and truncated forms
	writeFile(t, p, raw)
	if c, err := s.Read(p, "utf8", 0); err == nil {
		t.Errorf("invalid UTF-8 must be refused, got %+v", c)
	}
	c, err := s.Read(p, "base64", 0)
	back, _ := base64.StdEncoding.DecodeString(c.Data)
	if err != nil || string(back) != raw {
		t.Errorf("base64 round trip failed: %q %v", back, err)
	}
}

func TestAdvReadEncodingNamesAreStrict(t *testing.T) {
	s, root := newStore(t, 0)
	p := filepath.Join(root, "a.txt")
	writeFile(t, p, "x")
	for _, enc := range []string{"UTF8", "utf-8", "Base64", " base64", "base64 ", "\x00", "latin1", "utf8\x00"} {
		if c, err := s.Read(p, enc, 0); err == nil {
			t.Errorf("encoding %q accepted: %+v", enc, c)
		}
	}
}

func TestAdvReadPagingReproducesTheFile(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []string{"a", "ñ", "€", "😀", "\n", "Z"}
	_, root := newStore(t, 0)
	for i := 0; i < 150; i++ {
		var sb strings.Builder
		for n := rng.Intn(40); n > 0; n-- {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		want := sb.String()
		m := int64(4 + rng.Intn(13)) // 4 is the longest UTF-8 character: below that a window can hold no whole character
		s, _ := New(Config{ReadRoots: []string{root}, MaxBytes: m})
		p := filepath.Join(root, fmt.Sprintf("p%d.txt", i))
		writeFile(t, p, want)
		var got string
		for off, guard := int64(0), 0; ; guard++ {
			c, err := s.Read(p, "", off)
			if err != nil || guard > 200 || int64(len(c.Data)) > m || !utf8.ValidString(c.Data) || c.Truncated != (off+int64(len(c.Data)) < c.Size) {
				t.Fatalf("file %q max %d offset %d: %+v, %v", want, m, off, c, err)
			}
			got += c.Data
			off += int64(len(c.Data))
			if !c.Truncated {
				break
			}
			if len(c.Data) == 0 {
				t.Fatalf("no progress on %q with max %d at offset %d", want, m, off)
			}
		}
		if got != want {
			t.Fatalf("paging gave %q, want %q (max %d)", got, want, m)
		}
	}
}

func TestAdvReadBase64PagingReproducesRandomBytes(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	_, root := newStore(t, 0)
	for i := 0; i < 100; i++ {
		want := make([]byte, rng.Intn(60))
		rng.Read(want)
		m := int64(1 + rng.Intn(16))
		s, _ := New(Config{ReadRoots: []string{root}, MaxBytes: m})
		p := filepath.Join(root, fmt.Sprintf("r%d.bin", i))
		writeFile(t, p, string(want))
		var got []byte
		for off, guard := int64(0), 0; ; guard++ {
			c, err := s.Read(p, "base64", off)
			raw, derr := base64.StdEncoding.DecodeString(c.Data)
			if err != nil || derr != nil || guard > 200 || int64(len(raw)) > m || c.Truncated != (off+int64(len(raw)) < c.Size) {
				t.Fatalf("max %d offset %d: %+v %v %v", m, off, c, err, derr)
			}
			got = append(got, raw...)
			off += int64(len(raw))
			if !c.Truncated {
				break
			}
		}
		if string(got) != string(want) {
			t.Fatalf("base64 paging lost bytes (max %d)", m)
		}
	}
}

// ---------- directories, lists, special files ----------

func TestAdvListAFileAndReadADirectoryFailWithoutContent(t *testing.T) {
	s, root := newStore(t, 0)
	writeFile(t, filepath.Join(root, "f.txt"), "TOP-SECRET")
	mkdir(t, filepath.Join(root, "d"))
	if got, _, err := s.List(filepath.Join(root, "f.txt")); err == nil || len(got) != 0 {
		t.Errorf("List(file) = %v, %v; want an error", got, err)
	}
	for _, p := range []string{filepath.Join(root, "d"), root, filepath.Join(root, "d") + "/."} {
		for _, enc := range []string{"", "base64"} {
			if c, err := s.Read(p, enc, 0); err == nil || c.Data != "" {
				t.Errorf("Read(dir %q, %q) = %+v, %v; want an error", p, enc, c, err)
			}
		}
	}
}

func TestAdvListExactly500EntriesIsNotTruncated(t *testing.T) {
	s, root := newStore(t, 0)
	for i := 0; i < 500; i++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%03d", i)), "")
	}
	got, truncated, err := s.List(root)
	if err != nil || len(got) != 500 || truncated {
		t.Fatalf("len=%d truncated=%v err=%v", len(got), truncated, err)
	}
	if got[0].Name != "f000" || got[499].Name != "f499" {
		t.Errorf("not sorted: %s .. %s", got[0].Name, got[499].Name)
	}
}

func TestAdvListEmptyDirectoryAndOddNames(t *testing.T) {
	s, root := newStore(t, 0)
	if got, tr, err := s.List(root); err != nil || len(got) != 0 || tr {
		t.Fatalf("empty root: %v %v %v", got, tr, err)
	}
	names := []string{"a b", "ñ", "-rf", "日本"}
	if runtime.GOOS != "windows" { // Windows forbids tab, newline, backslash, * and ? in a name
		names = append(names, "tab\there", "new\nline", `back\slash`, "*", "?")
	}
	for _, n := range names {
		writeFile(t, filepath.Join(root, n), "x")
	}
	got, _, err := s.List(root)
	if err != nil || len(got) != len(names) {
		t.Fatalf("List = %d entries, %v; want %d", len(got), err, len(names))
	}
}

func TestFixed_ReadAFifoInsideTheRootHangs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs")
	}
	s, root := newStore(t, 0)
	fifo := filepath.Join(root, "pipe")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Skipf("mkfifo: %v %s", err, out)
	}
	t.Cleanup(func() { // release a reader stuck in open(2) so the goroutine below ends
		if w, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
			time.Sleep(100 * time.Millisecond)
			w.Close()
		}
	})
	done := make(chan error, 1)
	go func() { _, err := s.Read(fifo, "", 0); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Read of a FIFO returned without an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read of a FIFO inside the allowed folder blocks forever in open(2): a stuck request per FIFO (DoS)")
	}
}

// ---------- the root changes after New ----------

func TestAdvRootDeletedAfterNew(t *testing.T) {
	s, root := newStore(t, 0)
	p := filepath.Join(root, "a.txt")
	writeFile(t, p, "TOP-SECRET")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Read(p, "", 0); err == nil || c.Data != "" {
		t.Errorf("Read after the root vanished: %+v, %v", c, err)
	}
	if got, _, err := s.List(root); err == nil || len(got) != 0 {
		t.Errorf("List after the root vanished: %v, %v", got, err)
	}
}

func TestAdvRootReplacedByALinkToOutsideAfterNew(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 0)
	if err := os.RemoveAll(fx.root); err != nil {
		t.Fatal(err)
	}
	advLink(t, fx.outside, fx.root)
	for _, p := range []string{filepath.Join(fx.root, "secret.txt")} {
		if c, err := s.Read(p, "", 0); !errors.Is(err, ErrOutside) || c.Data != "" {
			t.Errorf("Read(%q) = %+v, %v; want ErrOutside", p, c, err)
		}
	}
	if got, _, err := s.List(fx.root); !errors.Is(err, ErrOutside) || len(got) != 0 {
		t.Errorf("List(root link) = %v, %v; want ErrOutside", got, err)
	}
}

func TestAdvRootReplacedByAFileAfterNew(t *testing.T) {
	s, root := newStore(t, 0)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "TOP-SECRET")
	if c, err := s.Read(root, "", 0); err == nil && c.Data != "" {
		t.Logf("the old root is now a plain file and Read returns it: %+v (the file is still at the root path itself)", c)
	}
	if got, _, err := s.List(root); err == nil || len(got) != 0 {
		t.Errorf("List(file at the old root) = %v, %v; want an error", got, err)
	}
}

// ---------- New: dangerous roots ----------

func TestAdvNewWithNoRootsAllowsNothing(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fx.root, filepath.Join(fx.root, "in.txt"), "/", "/etc/passwd", os.TempDir()} {
		if c, err := s.Read(p, "", 0); !errors.Is(err, ErrOutside) || c.Data != "" {
			t.Errorf("Read(%q) with no roots = %+v, %v", p, c, err)
		}
		if got, _, err := s.List(p); !errors.Is(err, ErrOutside) || len(got) != 0 {
			t.Errorf("List(%q) with no roots = %v, %v", p, got, err)
		}
	}
}

func TestAdvNewRefusesRootsBehindLinks(t *testing.T) {
	skipOnWindows(t)
	base, _ := canonicalDir(t.TempDir())
	home := mkdir(t, filepath.Join(base, "home", "alice"))
	forb := mkdir(t, filepath.Join(base, "data", "keys"))
	advLink(t, "/", filepath.Join(base, "to_slash"))
	advLink(t, filepath.Dir(home), filepath.Join(base, "to_home_parent"))
	advLink(t, home, filepath.Join(base, "to_home"))
	advLink(t, filepath.Join(base, "data"), filepath.Join(base, "to_data"))
	advLink(t, forb, filepath.Join(base, "to_keys"))
	cfg := func(root string) Config {
		return Config{ReadRoots: []string{root}, Home: home, Forbidden: []string{forb}}
	}
	for name, root := range map[string]string{
		"link to /": filepath.Join(base, "to_slash"), "link to the parent of home": filepath.Join(base, "to_home_parent"),
		"link to home": filepath.Join(base, "to_home"), "link to a folder that holds a forbidden one": filepath.Join(base, "to_data"),
		"link to the forbidden folder": filepath.Join(base, "to_keys"), "dot-dot to home": home + "/../alice", "slash": "/", "double slash": "//",
	} {
		if _, err := New(cfg(root)); err == nil {
			t.Errorf("%s: New must refuse %q", name, root)
		}
	}
	// the same Home and Forbidden given through a link must also be seen
	cfg2 := Config{ReadRoots: []string{filepath.Dir(home)}, Home: filepath.Join(base, "to_home")}
	if _, err := New(cfg2); err == nil {
		t.Error("Home given through a link must still be protected")
	}
	cfg3 := Config{ReadRoots: []string{filepath.Join(base, "data")}, Forbidden: []string{filepath.Join(base, "to_keys")}}
	if _, err := New(cfg3); err == nil {
		t.Error("Forbidden given through a link must still be protected")
	}
}

// A forbidden folder or Home that does not exist yet, spelled through a link, must still protect the root that will hold it.
func TestFixed_NewMissesAForbiddenFolderThatDoesNotExistYetAndIsSpelledThroughALink(t *testing.T) {
	skipOnWindows(t)
	base, _ := canonicalDir(t.TempDir())
	realDir := mkdir(t, filepath.Join(base, "real"))
	data := mkdir(t, filepath.Join(realDir, "data"))
	advLink(t, realDir, filepath.Join(base, "via"))
	missing := filepath.Join(base, "via", "data", ".ssh") // does not exist yet; lives inside root `data`
	if _, err := New(Config{ReadRoots: []string{data}, Forbidden: []string{missing}}); err == nil {
		t.Errorf("New accepted %q, which will contain the forbidden folder %q (same place through the link)", data, missing)
	}
	missingHome := filepath.Join(base, "via", "data", "home") // Home that does not exist yet, same story
	if _, err := New(Config{ReadRoots: []string{realDir}, Home: missingHome}); err == nil {
		t.Errorf("New accepted %q, which contains the user profile %q", realDir, missingHome)
	}
}

func TestAdvNewForbiddenEdgeValues(t *testing.T) {
	skipOnWindows(t)
	root := newRoot(t, "Shared")
	for name, cfg := range map[string]Config{
		"empty forbidden":    {ReadRoots: []string{root}, Forbidden: []string{"", ""}},
		"empty home":         {ReadRoots: []string{root}, Home: ""},
		"unrelated nonexist": {ReadRoots: []string{root}, Forbidden: []string{"/nonexistent/zzz"}, Home: "/nonexistent/home"},
		"relative forbidden": {ReadRoots: []string{root}, Forbidden: []string{"relative/path"}},
	} {
		if _, err := New(cfg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New(Config{ReadRoots: []string{root}, Forbidden: []string{"/"}}); err == nil {
		t.Error("a forbidden / must refuse every root")
	}
	if _, err := New(Config{ReadRoots: []string{root, ""}}); err == nil {
		t.Error("an empty root must be refused")
	}
	if _, err := New(Config{ReadRoots: []string{root, "relative"}}); err == nil {
		t.Error("a relative root must be refused")
	}
	if _, err := New(Config{ReadRoots: []string{root + "\x00"}}); err == nil {
		t.Error("a root with NUL must be refused")
	}
}

func TestAdvDangerousPathsOddEnvironment(t *testing.T) {
	sep := string(os.PathListSeparator)
	env := map[string]string{"PATH": sep + "/opt/a" + sep + sep + "/opt/b" + sep}
	got := DangerousPaths(func(k string) string { return env[k] })
	if len(got) != 2 || got[0] != filepath.Clean("/opt/a") || got[1] != filepath.Clean("/opt/b") {
		t.Errorf("empty PATH entries must add nothing, got %q", got)
	}
	for _, g := range DangerousPaths(func(k string) string { return "X" }) {
		if g == "" {
			t.Error("DangerousPaths returned an empty path (it would match everything)")
		}
	}
	if got := DangerousPaths(func(k string) string {
		if k == "USERPROFILE" {
			return "/home/alice"
		}
		return ""
	}); len(got) != 8 || got[0] != filepath.Join("/home/alice", ".ssh") {
		t.Errorf("only USERPROFILE set must give the eight folders under it, got %q", got)
	}
}

// ---------- concurrency and races ----------

func TestAdvConcurrentReadsAndLists(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 16)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if c, err := s.Read(filepath.Join(fx.root, "in.txt"), "", 0); err != nil || c.Data != "INSIDE-1" {
					t.Errorf("Read: %+v, %v", c, err)
					return
				}
				if _, err := s.Read(filepath.Join(fx.root, "lnk_out_file"), "", 0); !errors.Is(err, ErrOutside) {
					t.Errorf("escape: %v", err)
					return
				}
				if _, _, err := s.List(fx.root); err != nil {
					t.Errorf("List: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// Bounded race probe: root/sw flips between a real folder and a link to the outside while Read runs.
func TestFixed_SwapRaceReturnsOutsideContent(t *testing.T) {
	skipOnWindows(t)
	fx := newAdvFix(t)
	s := newAdvStore(t, fx, 0)
	advWrite(t, filepath.Join(fx.root, "real_sw", "f"), "INSIDE-SWAP")
	advWrite(t, filepath.Join(fx.outside, "swd", "f"), "OUTSIDE-SWAP")
	advLink(t, filepath.Join(fx.outside, "swd"), filepath.Join(fx.root, "link_sw"))
	sw, real, lnk := filepath.Join(fx.root, "sw"), filepath.Join(fx.root, "real_sw"), filepath.Join(fx.root, "link_sw")
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
			os.Rename(real, sw)
			os.Rename(sw, real)
			os.Rename(lnk, sw)
			os.Rename(sw, lnk)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	var leaked atomic.Value
	var reads atomic.Int64
	var readers sync.WaitGroup
	for g := 0; g < 4; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for time.Now().Before(deadline) && leaked.Load() == nil {
				reads.Add(1)
				if c, err := s.Read(filepath.Join(sw, "f"), "", 0); err == nil && c.Data != "INSIDE-SWAP" {
					leaked.Store(c.Data)
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	wg.Wait()
	if v := leaked.Load(); v != nil {
		t.Fatalf("Read returned %q from outside the root after %d reads", v, reads.Load())
	}
	t.Logf("%d reads, none leaked", reads.Load())
}
