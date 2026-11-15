package server_test

import (
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"context"

	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/files"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

// startWithFiles runs the agent over one allowed folder holding hello.txt and returns the server and the folder.
func startWithFiles(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := files.New(files.Config{ReadRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := filepath.EvalSymlinks(root)
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
		Desktop: &desktop.Fake{}, Tools: profile.Tools, Files: store, Strict: true,
	}))
	t.Cleanup(srv.Close)
	return srv, canon
}

func TestFileToolsExistOnlyWithFilesAndKeepTheNineToolCount(t *testing.T) {
	plain := start(t, &desktop.Fake{})
	s, err := session(t, plain, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains([]string{"ListFiles", "ReadFile", "WriteFile"}, tool.Name) {
			t.Fatalf("%s must not exist without [files]", tool.Name)
		}
	}
	srv, _ := startWithFiles(t)
	_, status := get(t, srv.URL+"/status", testKey)
	if status["tools_count"] != float64(9) || !strings.Contains(fmt.Sprint(status["builtin_tools"]), "ReadFile") || !strings.Contains(fmt.Sprint(status["builtin_tools"]), "WriteFile") {
		t.Fatalf("tools_count must stay 9 and the file tools be built-in: %v", status)
	}
}

func TestReadFileThroughMCPAndRefusesAnOutsidePath(t *testing.T) {
	srv, root := startWithFiles(t)
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	out, isErr := callTool(t, s, "ReadFile", map[string]any{"path": filepath.Join(root, "hello.txt")})
	if isErr || !strings.Contains(out, `"hello"`) {
		t.Fatalf("ReadFile: %v %s", isErr, out)
	}
	out, isErr = callTool(t, s, "ListFiles", map[string]any{"path": root})
	if isErr || !strings.Contains(out, "hello.txt") {
		t.Fatalf("ListFiles: %v %s", isErr, out)
	}
	out, isErr = callTool(t, s, "ReadFile", map[string]any{"path": filepath.Dir(root)})
	if !isErr || !strings.Contains(out, "outside the allowed folders") {
		t.Fatalf("an outside path must be refused with a clear error: %v %s", isErr, out)
	}
	if _, isErr = callTool(t, s, "ReadFile", map[string]any{"path": filepath.Join(root, "hello.txt"), "extra": 1}); !isErr {
		t.Fatal("an unknown parameter must be refused")
	}
}
