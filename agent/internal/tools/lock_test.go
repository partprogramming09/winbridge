package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
)

// textOf joins the text parts of a result.
func textOf(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// specNamed returns the spec of a tool.
func specNamed(t *testing.T, name string) spec {
	t.Helper()
	for _, sp := range specs() {
		if sp.name == name {
			return sp
		}
	}
	t.Fatalf("no tool %s", name)
	return spec{}
}

func TestInputToolsRefuseWhileLocked(t *testing.T) {
	cases := map[string]string{
		"Click": `{"x":1,"y":2}`, "Type": `{"text":"a"}`, "Move": `{"x":1,"y":2}`,
		"Scroll": `{"amount":120}`, "Shortcut": `{"keys":"ctrl+c"}`, "FocusWindow": `{"title":"x"}`,
	}
	for name, args := range cases {
		fake := &desktop.Fake{Session: desktop.SessionState{Locked: true, Desktop: "Winlogon"}}
		e := NewEnv(fake)
		res := call(context.Background(), e, specNamed(t, name), []byte(args))
		if !res.IsError || !strings.Contains(textOf(res), "locked") {
			t.Errorf("%s while locked: want a locked error, got %q", name, textOf(res))
		}
		for _, c := range fake.Log() {
			if !strings.HasPrefix(c, "windows") && !strings.HasPrefix(c, "elements") {
				t.Errorf("%s while locked still touched the desktop: %s", name, c)
			}
		}
	}
}

func TestInputToolsSayWhenAnAdministratorWindowIsInFront(t *testing.T) {
	cases := map[string]string{
		"Click": `{"x":1,"y":2}`, "Type": `{"text":"a"}`, "Move": `{"x":1,"y":2}`,
		"Scroll": `{"amount":120}`, "Shortcut": `{"keys":"ctrl+c"}`,
	}
	for name, args := range cases {
		fake := &desktop.Fake{Session: desktop.SessionState{Desktop: "Default", ElevatedForeground: "Administrador: PowerShell"}}
		res := call(context.Background(), NewEnv(fake), specNamed(t, name), []byte(args))
		got := textOf(res)
		if !res.IsError || !strings.Contains(got, "administrator window") || !strings.Contains(got, "Administrador: PowerShell") {
			t.Errorf("%s with an elevated window in front: want a clear error naming it, got %q", name, got)
		}
		for _, c := range fake.Log() {
			if !strings.HasPrefix(c, "windows") && !strings.HasPrefix(c, "elements") {
				t.Errorf("%s still touched the desktop: %s", name, c)
			}
		}
	}
	// FocusWindow is how a normal window is brought to the front, so it must not be refused for this reason.
	fake := &desktop.Fake{Session: desktop.SessionState{Desktop: "Default", ElevatedForeground: "Administrador: PowerShell"}}
	res := call(context.Background(), NewEnv(fake), specNamed(t, "FocusWindow"), []byte(`{"title":"x"}`))
	if strings.Contains(textOf(res), "administrator window") {
		t.Errorf("FocusWindow must not be refused because of an elevated window: %s", textOf(res))
	}
}

func TestInputToolsWorkWhenUnlocked(t *testing.T) {
	e := NewEnv(&desktop.Fake{})
	res := call(context.Background(), e, specNamed(t, "Click"), []byte(`{"x":1,"y":2}`))
	if res.IsError {
		t.Fatalf("unlocked click failed: %s", textOf(res))
	}
}

func TestSnapshotAndSystemInfoSayWhenLocked(t *testing.T) {
	locked := NewEnv(&desktop.Fake{Session: desktop.SessionState{Locked: true, Desktop: "Winlogon"}})
	snap := call(context.Background(), locked, specNamed(t, "Snapshot"), []byte(`{"use_vision":false}`))
	if snap.IsError || !strings.Contains(textOf(snap), "LOCKED") || !strings.Contains(textOf(snap), "Winlogon") {
		t.Errorf("Snapshot while locked should say so: %q", textOf(snap))
	}
	info := call(context.Background(), locked, specNamed(t, "GetSystemInfo"), []byte(`{}`))
	if !strings.Contains(textOf(info), "**Session:** LOCKED") {
		t.Errorf("GetSystemInfo while locked should say so: %q", textOf(info))
	}
	open := NewEnv(&desktop.Fake{})
	info = call(context.Background(), open, specNamed(t, "GetSystemInfo"), []byte(`{}`))
	if !strings.Contains(textOf(info), "**Session:** unlocked") {
		t.Errorf("GetSystemInfo unlocked: %q", textOf(info))
	}
}

func TestDiagnoseReturnsStructuredReportAndChecksRange(t *testing.T) {
	e := NewEnv(&desktop.Fake{})
	var asked int
	e.Diag = func(n int) map[string]any { asked = n; return map[string]any{"version": "x", "pid": 7} }
	res := call(context.Background(), e, diagnoseSpec(), []byte(`{"log_lines":5}`))
	if res.IsError || asked != 5 {
		t.Fatalf("Diagnose failed or ignored log_lines: asked=%d %s", asked, textOf(res))
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok || m["version"] != "x" {
		t.Errorf("structured content missing: %#v", res.StructuredContent)
	}
	if !strings.Contains(textOf(res), `"pid": 7`) {
		t.Errorf("text should carry the same JSON: %q", textOf(res))
	}
	for _, bad := range []string{`{"log_lines":-1}`, `{"log_lines":201}`, `{"other":1}`} {
		if r := call(context.Background(), e, diagnoseSpec(), []byte(bad)); !r.IsError {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

func TestTypeUsesPasteByDefaultAndKeysOnRequest(t *testing.T) {
	for args, want := range map[string]string{
		`{"text":"Áé|~"}`:                 `type "Áé|~" paste`,
		`{"text":"abc","method":"keys"}`:  `type "abc" keys`,
		`{"text":"abc","method":"paste"}`: `type "abc" paste`,
	} {
		fake := &desktop.Fake{}
		res := call(context.Background(), NewEnv(fake), specNamed(t, "Type"), []byte(args))
		if res.IsError || !strings.Contains(strings.Join(fake.Log(), "|"), want) {
			t.Errorf("%s: want %q in %v (error=%v %s)", args, want, fake.Log(), res.IsError, textOf(res))
		}
	}
	res := call(context.Background(), NewEnv(&desktop.Fake{}), specNamed(t, "Type"), []byte(`{"text":"a","method":"magic"}`))
	if !res.IsError {
		t.Error("an unknown method must be rejected")
	}
}
