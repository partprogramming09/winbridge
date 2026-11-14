package server_test

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

// fakeRunner is a task runner with one task named "ping".
type fakeRunner struct{}

func (fakeRunner) Names() []string { return []string{"ping"} }

func (fakeRunner) Run(_ context.Context, name string) (string, error) {
	if name != "ping" {
		return "", fmt.Errorf("unknown task %q", name)
	}
	return "pong", nil
}

// startWithTasks runs the agent with a task runner.
func startWithTasks(t *testing.T) *httptest.Server {
	t.Helper()
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
		Desktop: &desktop.Fake{}, Tools: profile.Tools, Tasks: fakeRunner{}, Strict: true,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunTaskExistsOnlyWithTasks(t *testing.T) {
	plain := start(t, &desktop.Fake{})
	_, status := get(t, plain.URL+"/status", testKey)
	if status["tools_count"] != float64(9) {
		t.Fatalf("without tasks the agent must keep 9 tools, got %v", status["tools"])
	}

	srv := startWithTasks(t)
	_, status = get(t, srv.URL+"/status", testKey)
	if status["tools_count"] != float64(10) || !strings.Contains(fmt.Sprint(status["tools"]), "RunTask") {
		t.Fatalf("with tasks /status must list RunTask, got %v", status["tools"])
	}
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "RunTask", Arguments: map[string]any{"name": "ping"}})
	if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "pong") {
		t.Fatalf("RunTask ping: %v %+v", err, res)
	}
	for _, args := range []map[string]any{{"name": "other"}, {"name": "ping", "extra": "x"}, {}} {
		res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "RunTask", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("RunTask %v must be refused", args)
		}
	}
}
