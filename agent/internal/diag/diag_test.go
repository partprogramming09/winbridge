package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/partprogramming09/winbridge/agent/internal/desktop"
)

func TestCollectReadsFixedFilesAndHidesSecrets(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "state"), 0o700))
	must(os.MkdirAll(filepath.Join(root, "logs"), 0o700))
	must(os.WriteFile(filepath.Join(root, "state", "last-install.json"), []byte(`{"ok":true,"version":"0.1.0"}`), 0o600))
	must(os.WriteFile(filepath.Join(root, "state", "agent.txt"), []byte("go\n"), 0o600))
	secret := strings.Repeat("A", 48)
	must(os.WriteFile(filepath.Join(root, "logs", "supervisor.log"), []byte("one\nkey "+secret+" two\nthree\n"), 0o600))

	c := New(Options{Version: "9", Upstream: "native-go", Root: root, Desktop: &desktop.Fake{}})
	out := c.Collect(2)
	if out["version"] != "9" || out["agent_selected"] != "go" {
		t.Errorf("basic fields: %v", out)
	}
	if li, _ := out["last_install"].(map[string]any); li["ok"] != true {
		t.Errorf("last_install: %v", out["last_install"])
	}
	logs := out["logs"].(map[string]any)["supervisor"].([]string)
	if len(logs) != 2 || strings.Contains(strings.Join(logs, "\n"), secret) {
		t.Errorf("want 2 scrubbed lines, got %v", logs)
	}
	if s := out["session"].(map[string]any); s["locked"] != false {
		t.Errorf("session: %v", s)
	}
	if h, _ := out["exe_sha256"].(string); len(h) != 64 {
		t.Errorf("exe_sha256: %q", h)
	}
}

func TestCollectWithoutRootAndClampedLines(t *testing.T) {
	c := New(Options{Desktop: &desktop.Fake{}})
	out := c.Collect(9999)
	if _, ok := out["logs"]; ok {
		t.Error("no root, so no logs")
	}
	if out["pid"].(int) != os.Getpid() {
		t.Error("pid")
	}
}

func TestScrubCutsLongLines(t *testing.T) {
	if got := (&Collector{}).scrub(strings.Repeat("a b ", 200)); len(got) > maxLineChars+3 {
		t.Errorf("line not cut: %d", len(got))
	}
}

func TestScrubHidesKeysThePolicyAllows(t *testing.T) {
	c := &Collector{o: Options{Secret: "short-key"}}
	cases := map[string]string{
		"key short-key here":                       "short-key",
		"k " + strings.Repeat("a", 32) + " end":    strings.Repeat("a", 32),
		"k " + strings.Repeat("ab.~", 10) + " end": "ab.~ab.~",
	}
	for line, secret := range cases {
		if got := c.scrub(line); strings.Contains(got, secret) {
			t.Errorf("%q leaked %q: %q", line, secret, got)
		}
	}
	if got := c.scrub("fingerprint 73af789d97d23059 ok"); got != "fingerprint 73af789d97d23059 ok" {
		t.Errorf("a 16-character fingerprint must stay: %q", got)
	}
}

func TestLastInstallErrorIsScrubbed(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "state"), 0o700)
	secret := strings.Repeat("Z", 40)
	_ = os.WriteFile(filepath.Join(root, "state", "last-install.json"), []byte(`{"ok":false,"error":"bad `+secret+`","build_id":"`+strings.Repeat("a", 32)+`"}`), 0o600)
	out := New(Options{Root: root, Desktop: &desktop.Fake{}}).Collect(0)
	li := out["last_install"].(map[string]any)
	if strings.Contains(li["error"].(string), secret) {
		t.Error("error text leaked a key-shaped string")
	}
	if li["build_id"] != strings.Repeat("a", 32) {
		t.Error("build_id must not be scrubbed")
	}
}

func TestTailReadsOnlyTheEndOfALargeFile(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "logs"), 0o700)
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString("line number filler filler filler\n")
	}
	b.WriteString("the last line\n")
	_ = os.WriteFile(filepath.Join(root, "logs", "supervisor.log"), []byte(b.String()), 0o600)
	got := New(Options{Root: root, Desktop: &desktop.Fake{}}).tail(filepath.Join(root, "logs", "supervisor.log"), 2)
	if len(got) != 2 || got[1] != "the last line" {
		t.Errorf("got %v", got)
	}
}

func TestCollectReportsHeldModifiers(t *testing.T) {
	out := New(Options{Desktop: &desktop.Fake{Held: []string{"ctrl"}}}).Collect(0)
	held, _ := out["session"].(map[string]any)["modifiers_held"].([]string)
	if len(held) != 1 || held[0] != "ctrl" {
		t.Errorf("modifiers_held: %v", out["session"])
	}
}
