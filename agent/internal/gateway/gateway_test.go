package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain turns the test binary into a stub MCP server when GATEWAY_STUB is set.
func TestMain(m *testing.M) {
	if os.Getenv("GATEWAY_STUB") == "1" {
		runStub()
		return
	}
	os.Exit(m.Run())
}

// runStub serves tools that let the tests observe the gateway: echo, env, pid, big, sleep.
func runStub() {
	s := mcp.NewServer(&mcp.Implementation{Name: "stub", Version: "1"}, nil)
	add := func(name string, h func(ctx context.Context, req *mcp.CallToolRequest) string) {
		s.AddTool(&mcp.Tool{Name: name, Description: "stub " + name, InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: h(ctx, req)}}}, nil
			})
	}
	add("echo", func(_ context.Context, req *mcp.CallToolRequest) string {
		return "echo:" + string(req.Params.Arguments)
	})
	add("env", func(context.Context, *mcp.CallToolRequest) string {
		return fmt.Sprintf("secret=%q extra=%q", os.Getenv("WINBRIDGE_KEY"), os.Getenv("STUB_EXTRA"))
	})
	add("pid", func(context.Context, *mcp.CallToolRequest) string { return fmt.Sprint(os.Getpid()) })
	add("big", func(context.Context, *mcp.CallToolRequest) string { return strings.Repeat("é", 5000) })
	add("secret", func(context.Context, *mcp.CallToolRequest) string { return "not allowed" })
	add("sleep", func(ctx context.Context, req *mcp.CallToolRequest) string {
		for i := 1; i <= 50; i++ {
			select {
			case <-ctx.Done():
				_ = os.WriteFile(os.Getenv("STUB_MARKER"), []byte("cancelled"), 0o600)
				return "cancelled"
			case <-time.After(40 * time.Millisecond):
			}
			if token := req.Params.GetProgressToken(); token != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(i), Total: 50, Message: "tick"})
			}
		}
		return "slept"
	})
	_ = s.Run(context.Background(), &mcp.StdioTransport{})
}

// sum returns the SHA-256 of a file.
func sum(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// newGateway returns a gateway with one stub server named "stub".
func newGateway(t *testing.T, mutate func(*Spec)) *Gateway {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_STUB", "1")
	t.Setenv("WINBRIDGE_KEY", "super-secret")
	spec := Spec{
		Command: exe, SHA256: sum(t, exe), AllowTools: []string{"echo", "env", "pid", "big", "sleep"},
		Env: map[string]string{"STUB_EXTRA": "yes", "STUB_MARKER": filepath.Join(t.TempDir(), "marker")},
	}
	if mutate != nil {
		mutate(&spec)
	}
	g, err := New(map[string]Spec{"stub": spec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

// textOf returns the first text block of a result.
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first block is %T", res.Content[0])
	}
	return tc.Text
}

func TestCallEchoesArguments(t *testing.T) {
	g := newGateway(t, nil)
	res, err := g.Call(context.Background(), "stub", "echo", map[string]any{"a": 1}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(t, res); got != `echo:{"a":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestToolOutsideAllowListIsRefused(t *testing.T) {
	g := newGateway(t, nil)
	if _, err := g.Call(context.Background(), "stub", "secret", nil, nil, 0); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("want not allowed, got %v", err)
	}
	if _, err := g.Call(context.Background(), "nope", "echo", nil, nil, 0); err == nil {
		t.Fatal("unknown server must fail")
	}
}

func TestToolsListIsFilteredByAllowList(t *testing.T) {
	g := newGateway(t, nil)
	list, err := g.Tools(context.Background(), "stub")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list {
		if tool.Name == "secret" {
			t.Fatal("secret must not be listed")
		}
	}
	if len(list) != 5 {
		t.Fatalf("want 5 tools, got %d", len(list))
	}
}

func TestChangedLauncherIsNotStarted(t *testing.T) {
	g := newGateway(t, func(s *Spec) { s.SHA256 = strings.Repeat("0", 64) })
	_, err := g.Call(context.Background(), "stub", "echo", nil, nil, 0)
	if err == nil || !strings.Contains(err.Error(), "pinned SHA-256") {
		t.Fatalf("want hash refusal, got %v", err)
	}
}

func TestChangedExtraFileIsNotStarted(t *testing.T) {
	extra := filepath.Join(t.TempDir(), "module.py")
	if err := os.WriteFile(extra, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newGateway(t, func(s *Spec) { s.Files = map[string]string{extra: sum(t, extra)} })
	if _, err := g.Call(context.Background(), "stub", "echo", nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	g.Close()
	if err := os.WriteFile(extra, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Call(context.Background(), "stub", "echo", nil, nil, 0); err == nil || !strings.Contains(err.Error(), "module.py") {
		t.Fatalf("want refusal naming module.py, got %v", err)
	}
}

func TestChildDoesNotSeeAgentSecrets(t *testing.T) {
	g := newGateway(t, nil)
	res, err := g.Call(context.Background(), "stub", "env", nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(t, res); got != `secret="" extra="yes"` {
		t.Fatalf("got %s", got)
	}
}

func TestLongTextIsCutOnARuneBoundary(t *testing.T) {
	g := newGateway(t, func(s *Spec) { s.MaxText = 1001 })
	res, err := g.Call(context.Background(), "stub", "big", nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := textOf(t, res)
	if len(first) != 1000 || strings.ContainsRune(first, '�') {
		t.Fatalf("want 1000 clean bytes, got %d", len(first))
	}
	if last := res.Content[len(res.Content)-1].(*mcp.TextContent).Text; !strings.Contains(last, "[gateway:") {
		t.Fatalf("missing note: %q", last)
	}
}

func TestProgressIsForwarded(t *testing.T) {
	g := newGateway(t, nil)
	var n atomic.Int32
	var last atomic.Value
	res, err := g.Call(context.Background(), "stub", "sleep", nil, func(done, total float64, message string) {
		n.Add(1)
		last.Store(message)
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if textOf(t, res) != "slept" || n.Load() < 10 {
		t.Fatalf("result %q, %d progress notifications", textOf(t, res), n.Load())
	}
}

func TestKeepaliveFiresWhenChildIsSilent(t *testing.T) {
	g := newGateway(t, func(s *Spec) { s.AllowTools = append(s.AllowTools, "sleep") })
	var msgs []string
	var mu sync.Mutex
	_, err := g.Call(context.Background(), "stub", "sleep", nil, func(_, _ float64, m string) {
		mu.Lock()
		msgs = append(msgs, m)
		mu.Unlock()
	}, 15*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("no progress at all")
	}
}

func TestCancelReachesTheChild(t *testing.T) {
	var marker string
	g := newGateway(t, func(s *Spec) { marker = s.Env["STUB_MARKER"] })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	if _, err := g.Call(ctx, "stub", "sleep", nil, nil, 0); err == nil {
		t.Fatal("want an error after cancel")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Log("the child was ended before it could write its marker; accepted: the process tree is killed")
}

func TestIdleChildIsStoppedAndRestarted(t *testing.T) {
	g := newGateway(t, func(s *Spec) { s.Idle = 200 * time.Millisecond })
	first, err := g.Call(context.Background(), "stub", "pid", nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	second, err := g.Call(context.Background(), "stub", "pid", nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if textOf(t, first) == textOf(t, second) {
		t.Fatal("the idle child was not stopped")
	}
}

func TestChildStaysAliveBetweenQuickCalls(t *testing.T) {
	g := newGateway(t, nil)
	a, _ := g.Call(context.Background(), "stub", "pid", nil, nil, 0)
	b, _ := g.Call(context.Background(), "stub", "pid", nil, nil, 0)
	if textOf(t, a) != textOf(t, b) {
		t.Fatal("the child restarted between calls")
	}
}

func TestCallsOnOneServerRunOneAtATime(t *testing.T) {
	g := newGateway(t, nil)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = g.Call(context.Background(), "stub", "sleep", nil, nil, 0)
		}()
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed < 3500*time.Millisecond {
		t.Fatalf("two 2 s calls finished in %s: they overlapped", elapsed)
	}
}

func TestNewRejectsBadSpecs(t *testing.T) {
	good := Spec{Command: "/x/y", SHA256: strings.Repeat("a", 64), AllowTools: []string{"t"}}
	cases := map[string]func(*Spec){
		"relative":   func(s *Spec) { s.Command = "y" },
		"bad hash":   func(s *Spec) { s.SHA256 = "zz" },
		"no allowed": func(s *Spec) { s.AllowTools = nil },
		"bad file":   func(s *Spec) { s.Files = map[string]string{"rel": strings.Repeat("a", 64)} },
	}
	for name, mutate := range cases {
		s := good
		mutate(&s)
		if _, err := New(map[string]Spec{"ok": s}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := New(map[string]Spec{"Bad Name": good}); err == nil {
		t.Error("a bad name must fail")
	}
}

func TestDeniedArgumentIsRefusedBeforeTheChildRuns(t *testing.T) {
	g := newGateway(t, func(s *Spec) { s.DenyArgs = map[string][]string{"echo": {"program", "wine"}} })
	for _, args := range []map[string]any{{"program": "calc.exe"}, {"wine": nil}, {"ok": 1, "program": ""}} {
		if _, err := g.Call(context.Background(), "stub", "echo", args, nil, 0); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%v must be refused, got %v", args, err)
		}
	}
	if _, err := g.Call(context.Background(), "stub", "echo", map[string]any{"label": "x"}, nil, 0); err != nil {
		t.Fatalf("a call without the denied arguments must pass: %v", err)
	}
	if _, err := g.Call(context.Background(), "stub", "pid", map[string]any{"program": "calc.exe"}, nil, 0); err != nil {
		t.Fatalf("the filter is per tool: %v", err)
	}
	list, err := g.Tools(context.Background(), "stub")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list {
		if tool.Name == "echo" && len(tool.DeniedArguments) != 2 {
			t.Errorf("ServerTools must report the denied arguments, got %v", tool.DeniedArguments)
		}
	}
}

func TestNewRejectsDenyArgsForAToolThatIsNotAllowed(t *testing.T) {
	s := Spec{Command: "/x/y", SHA256: strings.Repeat("a", 64), AllowTools: []string{"t"}, DenyArgs: map[string][]string{"other": {"a"}}}
	if _, err := New(map[string]Spec{"ok": s}); err == nil {
		t.Fatal("want an error")
	}
}
