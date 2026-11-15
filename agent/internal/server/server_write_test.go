package server_test

import (
	"encoding/base64"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/files"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

// startWithWrite runs the agent over one read-only folder and one write folder (either may be empty).
func startWithWrite(t *testing.T, readRoots, writeRoots []string, maxBytes int64) *mcp.ClientSession {
	t.Helper()
	store, err := files.New(files.Config{ReadRoots: readRoots, WriteRoots: writeRoots, MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
		Desktop: &desktop.Fake{}, Tools: profile.Tools, Files: store, Strict: true,
	}))
	t.Cleanup(srv.Close)
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func canonTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWriteFileThroughMCPCreatesAndRefusesOverwrite(t *testing.T) {
	out := canonTemp(t)
	s := startWithWrite(t, nil, []string{out}, 0)
	p := filepath.Join(out, "note.txt")

	text, isErr := callTool(t, s, "WriteFile", map[string]any{"path": p, "content": "hola"})
	if isErr || !strings.Contains(text, "wrote 4 bytes to "+p) {
		t.Fatalf("WriteFile: %v %s", isErr, text)
	}
	if b, _ := os.ReadFile(p); string(b) != "hola" {
		t.Fatalf("content = %q", b)
	}
	text, isErr = callTool(t, s, "WriteFile", map[string]any{"path": p, "content": "otra"})
	if !isErr || !strings.Contains(text, "already exists") {
		t.Fatalf("overwrite without the flag must fail clearly: %v %s", isErr, text)
	}
	if _, isErr = callTool(t, s, "WriteFile", map[string]any{"path": p, "content": "otra", "overwrite": true}); isErr {
		t.Fatal("overwrite=true must work")
	}
	if b, _ := os.ReadFile(p); string(b) != "otra" {
		t.Fatalf("content after overwrite = %q", b)
	}
	if text, isErr = callTool(t, s, "WriteFile", map[string]any{"path": filepath.Join(out, "a.ps1"), "content": "x"}); !isErr || !strings.Contains(text, "extension") {
		t.Fatalf("a script must be refused: %v %s", isErr, text)
	}
	if _, isErr = callTool(t, s, "WriteFile", map[string]any{"path": p, "content": "x", "extra": 1}); !isErr {
		t.Fatal("an unknown parameter must be refused")
	}
}

func TestWriteFileRefusedWhenOnlyReadRootsAreConfigured(t *testing.T) {
	ro := canonTemp(t)
	s := startWithWrite(t, []string{ro}, nil, 0)
	text, isErr := callTool(t, s, "WriteFile", map[string]any{"path": filepath.Join(ro, "a.txt"), "content": "x"})
	if !isErr || !strings.Contains(text, "read-only") {
		t.Fatalf("the tool exists but must say the folder is read-only: %v %s", isErr, text)
	}
	if _, err := os.Stat(filepath.Join(ro, "a.txt")); err == nil {
		t.Fatal("a file was written in a read-only folder")
	}
}

func TestWriteFileOfTheMaximumSizeFitsTheRequestLimit(t *testing.T) {
	out := canonTemp(t)
	s := startWithWrite(t, nil, []string{out}, 1<<20)
	raw := make([]byte, 1<<20)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	p := filepath.Join(out, "big.png")
	text, isErr := callTool(t, s, "WriteFile", map[string]any{
		"path": p, "content": base64.StdEncoding.EncodeToString(raw), "encoding": "base64",
	})
	if isErr || !strings.Contains(text, "wrote 1048576 bytes") {
		t.Fatalf("1 MiB in base64 must fit the request limit: %v %.200s", isErr, text)
	}
	if b, _ := os.ReadFile(p); len(b) != len(raw) {
		t.Fatalf("wrote %d bytes", len(b))
	}
}
