package files

import (
	"errors"
	"testing"
)

func TestCheckNameRejectsWindowsTricks(t *testing.T) {
	cases := []string{
		`C:\Shared\a.txt:s`,
		`C:\Shared\NUL`,
		`C:\Shared\con.txt`,
		`C:\Shared\COM1`,
		`C:\Shared\lpt9.log`,
		`\\?\C:\Shared\a`,
		`\\.\NUL`,
		`\\srv\share\a`,
		`C:\Shared\a.`,
		"C:\\Shared\\a ",
		`C:\Shared\..\Users`,
		`C:\Shared\.\a`,
		`C:/Shared/a.txt:s`,
	}
	for _, p := range cases {
		if err := checkName(p, true); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("checkName(%q, windows) = %v, want ErrInvalidPath", p, err)
		}
	}
}

func TestCheckNameAcceptsOrdinaryPaths(t *testing.T) {
	for _, p := range []string{`C:\Shared\a b.txt`, `C:\Shared\sub\notes.md`, `C:/Shared/sub/x.json`, `d:\data\report-2026.csv`} {
		if err := checkName(p, true); err != nil {
			t.Errorf("checkName(%q, windows) = %v, want nil", p, err)
		}
	}
	if err := checkName("/tmp/shared/a.txt", false); err != nil {
		t.Errorf("checkName on a unix path = %v, want nil", err)
	}
}

func TestCheckNameRejectsEmptyNulAndRelative(t *testing.T) {
	for _, windows := range []bool{true, false} {
		for _, p := range []string{"", "a.txt", "a\x00b", "sub/a.txt"} {
			if err := checkName(p, windows); !errors.Is(err, ErrInvalidPath) {
				t.Errorf("checkName(%q, windows=%v) = %v, want ErrInvalidPath", p, windows, err)
			}
		}
	}
	if err := checkName(`Shared\a.txt`, true); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("a Windows path without a drive must be refused, got %v", err)
	}
}

func TestCheckNameIsPermissiveOffWindowsForColonAndDevices(t *testing.T) {
	for _, p := range []string{"/tmp/a:b", "/tmp/nul", "/tmp/a."} {
		if err := checkName(p, false); err != nil {
			t.Errorf("checkName(%q, unix) = %v, want nil", p, err)
		}
	}
}

// A short (8.3) name is not refused by its text: a TEMP folder spelled with a short user folder is ordinary. Where it really
// points is decided on the opened file, so a short name that leads outside the folder is still refused there.
func TestCheckNameAcceptsShortNames(t *testing.T) {
	for _, p := range []string{`C:\Temp\USERNA~1\Local\a.txt`, `C:\Shared\PROGRA~1`, `C:\Shared\PROGR~10`, `C:\Shared\DOCUM~11.TXT`} {
		if err := checkName(p, true); err != nil {
			t.Errorf("checkName(%q, windows) = %v, want nil", p, err)
		}
	}
}
