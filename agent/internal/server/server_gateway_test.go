package server_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/gateway"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

// fakeGateway has one server "box" with the tool "work", which reports three progress steps.
type fakeGateway struct{ calls atomic.Int32 }

func (*fakeGateway) Names() []string { return []string{"box"} }

func (*fakeGateway) Allowed(name string) ([]string, error) {
	if name != "box" {
		return nil, fmt.Errorf("unknown server %q", name)
	}
	return []string{"work"}, nil
}

func (*fakeGateway) Tools(context.Context, string) ([]gateway.ToolInfo, error) {
	return []gateway.ToolInfo{{Name: "work", Description: "does work"}}, nil
}

func (f *fakeGateway) Call(_ context.Context, name, tool string, args map[string]any, progress gateway.Progress, _ time.Duration) (*mcp.CallToolResult, error) {
	f.calls.Add(1)
	if name != "box" || tool != "work" {
		return nil, fmt.Errorf("not allowed")
	}
	for i := 1; i <= 3 && progress != nil; i++ {
		progress(float64(i), 3, "step")
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("worked %v", args["n"])}}}, nil
}

// startWithGateway runs the agent with the fake gateway on the given desktop.
func startWithGateway(t *testing.T, d desktop.Desktop) (*httptest.Server, *fakeGateway) {
	t.Helper()
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	gw := &fakeGateway{}
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
		Desktop: d, Tools: profile.Tools, Gateway: gw, Strict: true,
	}))
	t.Cleanup(srv.Close)
	return srv, gw
}

func TestGatewayToolsExistOnlyWithServers(t *testing.T) {
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
		if strings.Contains("ListServers ServerTools CallServerTool", tool.Name) {
			t.Fatalf("%s must not exist without servers", tool.Name)
		}
	}

	srv, _ := startWithGateway(t, &desktop.Fake{})
	_, status := get(t, srv.URL+"/status", testKey)
	if status["tools_count"] != float64(9) || !strings.Contains(fmt.Sprint(status["builtin_tools"]), "CallServerTool") {
		t.Fatalf("the nine-tool count must stay and /status must list the gateway tools as built-in: %v", status)
	}
}

func TestCallServerToolForwardsResultAndProgress(t *testing.T) {
	srv, _ := startWithGateway(t, &desktop.Fake{})
	var steps atomic.Int32
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(context.Context, *mcp.ProgressNotificationClientRequest) { steps.Add(1) },
	})
	s, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: keyTransport{testKey}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	params := &mcp.CallToolParams{Name: "CallServerTool", Arguments: map[string]any{"server": "box", "tool": "work", "arguments": map[string]any{"n": 7}}}
	params.SetProgressToken("tok")
	res, err := s.CallTool(context.Background(), params)
	if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "worked 7") {
		t.Fatalf("call: %v %+v", err, res)
	}
	time.Sleep(200 * time.Millisecond)
	if steps.Load() != 3 {
		t.Fatalf("want 3 progress notifications at the client, got %d", steps.Load())
	}
}

func TestGatewayToolsRefuseWhileLocked(t *testing.T) {
	srv, gw := startWithGateway(t, &desktop.Fake{Session: desktop.SessionState{Locked: true, Desktop: "Winlogon"}})
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, p := range []*mcp.CallToolParams{
		{Name: "CallServerTool", Arguments: map[string]any{"server": "box", "tool": "work"}},
		{Name: "ServerTools", Arguments: map[string]any{"server": "box"}},
	} {
		res, err := s.CallTool(context.Background(), p)
		if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "locked") {
			t.Errorf("%s must refuse while locked: %v %+v", p.Name, err, res)
		}
	}
	if gw.calls.Load() != 0 {
		t.Fatal("the gateway ran while the session was locked")
	}
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "ListServers"})
	if err != nil || res.IsError {
		t.Fatalf("ListServers reads no child and must work while locked: %v %+v", err, res)
	}
}

func TestCallServerToolRejectsBadArguments(t *testing.T) {
	srv, gw := startWithGateway(t, &desktop.Fake{})
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, args := range []map[string]any{
		{"server": "box", "tool": "work", "command": "calc.exe"},
		{"server": "box", "tool": "work", "arguments": "calc.exe"},
		{"server": "other", "tool": "work"},
		{"tool": "work"},
	} {
		res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "CallServerTool", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("%v must be refused", args)
		}
	}
	if gw.calls.Load() > 1 {
		t.Fatalf("a refused call reached the gateway %d times", gw.calls.Load())
	}
}
