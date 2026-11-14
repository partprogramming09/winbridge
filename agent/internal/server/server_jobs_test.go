package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/jobs"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

// startWithJobs runs the agent over a real job folder with one job "state" and returns the server and the folder.
func startWithJobs(t *testing.T, d desktop.Desktop) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{"inbox", "out", "cancel"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"jobs":{"state":{"script":"s.ps1","sha256":"aa","timeout_s":30,"params":{"Mode":"^(fast|full)$"}}}}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := jobs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
		Desktop: d, Tools: profile.Tools, Jobs: store, Strict: true,
	}))
	t.Cleanup(srv.Close)
	return srv, root
}

// callTool calls one tool and returns its text and whether it was an error.
func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestJobToolsExistOnlyWithJobsAndKeepTheNineToolCount(t *testing.T) {
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
		if slices.Contains([]string{"ListJobs", "SubmitJob", "JobStatus", "WaitJob", "CancelJob"}, tool.Name) {
			t.Fatalf("%s must not exist without [jobs]", tool.Name)
		}
	}
	srv, _ := startWithJobs(t, &desktop.Fake{})
	_, status := get(t, srv.URL+"/status", testKey)
	if status["tools_count"] != float64(9) || !strings.Contains(fmt.Sprint(status["builtin_tools"]), "SubmitJob") {
		t.Fatalf("tools_count must stay 9 and the job tools be built-in: %v", status)
	}
}

func TestJobLifecycleThroughMCPWorksWhileLocked(t *testing.T) {
	srv, root := startWithJobs(t, &desktop.Fake{Session: desktop.SessionState{Locked: true, Desktop: "Winlogon"}})
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	out, isErr := callTool(t, s, "ListJobs", nil)
	if isErr || !strings.Contains(out, `"state"`) || !strings.Contains(out, "^(fast|full)$") {
		t.Fatalf("ListJobs: %v %s", isErr, out)
	}
	out, isErr = callTool(t, s, "SubmitJob", map[string]any{"job": "state", "args": map[string]any{"Mode": "fast"}})
	if isErr {
		t.Fatalf("SubmitJob: %s", out)
	}
	var sub struct{ ID string }
	if err := json.Unmarshal([]byte(strings.ReplaceAll(out, `"id"`, `"ID"`)), &sub); err != nil || sub.ID == "" {
		t.Fatalf("no id in %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "inbox", sub.ID+".json")); err != nil {
		t.Fatal("request not in the inbox")
	}
	if out, isErr = callTool(t, s, "SubmitJob", map[string]any{"job": "state"}); !isErr || !strings.Contains(out, "busy") || !strings.Contains(out, sub.ID) {
		t.Fatalf("a second submit must be busy and name %s: %v %s", sub.ID, isErr, out)
	}
	if out, isErr = callTool(t, s, "JobStatus", map[string]any{"id": sub.ID}); isErr || !strings.Contains(out, "queued") {
		t.Fatalf("JobStatus: %v %s", isErr, out)
	}
	if out, isErr = callTool(t, s, "CancelJob", map[string]any{"id": sub.ID}); isErr {
		t.Fatalf("CancelJob: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "cancel", sub.ID)); err != nil {
		t.Fatal("cancel marker missing")
	}
}

func TestJobToolsRejectBadInput(t *testing.T) {
	srv, root := startWithJobs(t, &desktop.Fake{})
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bad := []struct {
		tool string
		args map[string]any
	}{
		{"SubmitJob", map[string]any{"job": "state", "args": map[string]any{"Mode": "x; calc.exe"}}},
		{"SubmitJob", map[string]any{"job": "state", "args": map[string]any{"Cmd": "calc.exe"}}},
		{"SubmitJob", map[string]any{"job": "calc"}},
		{"SubmitJob", map[string]any{"job": "state", "command": "calc.exe"}},
		{"SubmitJob", map[string]any{"job": "state", "args": "calc.exe"}},
		{"JobStatus", map[string]any{"id": `..\manifest`}},
		{"WaitJob", map[string]any{"id": "x", "timeout_s": 9999}},
		{"CancelJob", map[string]any{"id": "ghost"}},
	}
	for _, c := range bad {
		if out, isErr := callTool(t, s, c.tool, c.args); !isErr {
			t.Errorf("%s %v must fail, got %s", c.tool, c.args, out)
		}
	}
	if files, _ := filepath.Glob(filepath.Join(root, "inbox", "*")); len(files) != 0 {
		t.Fatalf("rejected requests left files: %v", files)
	}
}
