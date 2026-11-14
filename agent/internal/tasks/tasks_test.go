//go:build !windows

package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var sh = []string{"sh", "-s"}

// script writes a script file and returns its path and SHA-256.
func script(t *testing.T, body string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task.sh")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return path, hex.EncodeToString(sum[:])
}

func runner(t *testing.T, name, body string) (*Runner, string) {
	t.Helper()
	path, sum := script(t, body)
	r, err := New(map[string]Spec{name: {Script: path, SHA256: sum}}, sh)
	if err != nil {
		t.Fatal(err)
	}
	return r, path
}

func TestRunsAPinnedScript(t *testing.T) {
	r, _ := runner(t, "hello", "echo hello\n")
	out, err := r.Run(context.Background(), "hello")
	if err != nil || strings.TrimSpace(out) != "hello" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestRefusesAScriptThatChanged(t *testing.T) {
	r, path := runner(t, "hello", "echo hello\n")
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(path, []byte("touch "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("want a SHA-256 refusal, got %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the changed script ran")
	}
}

func TestRefusesUnknownNames(t *testing.T) {
	r, _ := runner(t, "hello", "echo hello\n")
	for _, name := range []string{"other", "hello; rm -rf /", "../hello", ""} {
		if _, err := r.Run(context.Background(), name); err == nil {
			t.Errorf("%q must be refused", name)
		}
	}
}

func TestTheAuthKeyDoesNotReachTheScript(t *testing.T) {
	t.Setenv("WINREMOTE_AUTH_KEY", "secret-value")
	t.Setenv("WINBRIDGE_STRICT", "1")
	r, _ := runner(t, "env", "echo \"[$WINREMOTE_AUTH_KEY][$WINBRIDGE_STRICT]\"\n")
	out, err := r.Run(context.Background(), "env")
	if err != nil || strings.TrimSpace(out) != "[][]" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestFailureAndTruncation(t *testing.T) {
	r, _ := runner(t, "bad", "echo oops\nexit 3\n")
	if _, err := r.Run(context.Background(), "bad"); err == nil || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("want the failure with its output, got %v", err)
	}
	big, _ := runner(t, "big", "yes x | head -c 40000\n")
	out, err := big.Run(context.Background(), "big")
	if err != nil || !strings.HasSuffix(out, "[output truncated]") || len(out) > maxOutput+40 {
		t.Fatalf("output not capped: %d bytes, %v", len(out), err)
	}
}

func TestNewValidatesTheSpecs(t *testing.T) {
	sum := strings.Repeat("a", 64)
	bad := map[string]Spec{
		"Bad Name": {Script: "/x.ps1", SHA256: sum},
		"relative": {Script: "x.ps1", SHA256: sum},
		"short":    {Script: "/x.ps1", SHA256: "abc"},
	}
	for name, spec := range bad {
		if _, err := New(map[string]Spec{name: spec}, sh); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// TestATimedOutTaskDoesNotWaitForItsGrandchildren checks that a child that keeps the output pipes open cannot hold the call.
func TestATimedOutTaskDoesNotWaitForItsGrandchildren(t *testing.T) {
	oldTimeout, oldDelay := timeout, waitDelay
	timeout, waitDelay = 300*time.Millisecond, 300*time.Millisecond
	defer func() { timeout, waitDelay = oldTimeout, oldDelay }()
	r, _ := runner(t, "slow", "sleep 20 &\nwait\n")
	start := time.Now()
	_, err := r.Run(context.Background(), "slow")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout error, got %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the call was held %s by a grandchild", took)
	}
}
