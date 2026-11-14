package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

const testKey = "test-key-0123456789"

// keyTransport adds the bearer key to every request of the MCP client.
type keyTransport struct{ key string }

func (k keyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+k.key)
	return http.DefaultTransport.RoundTrip(r)
}

// start runs the agent on a test server that accepts loopback clients.
func start(t *testing.T, fake *desktop.Fake) *httptest.Server {
	t.Helper()
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
		Desktop: fake, Tools: profile.Tools, Strict: true,
	}))
	t.Cleanup(srv.Close)
	return srv
}

// session connects an MCP client with the given key.
func session(t *testing.T, srv *httptest.Server, key string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: keyTransport{key}},
	}
	return client.Connect(context.Background(), transport, nil)
}

func get(t *testing.T, url, key string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

func TestHealthIsPublicAndStatusNeedsTheKey(t *testing.T) {
	srv := start(t, &desktop.Fake{})
	code, health := get(t, srv.URL+"/health", "")
	if code != 200 || health["profile"] != "winbridge" || health["status"] != "ok" {
		t.Fatalf("/health: %d %v", code, health)
	}
	if code, _ := get(t, srv.URL+"/status", ""); code != http.StatusUnauthorized {
		t.Fatalf("/status without a key must be 401, got %d", code)
	}
	if code, _ := get(t, srv.URL+"/status", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("/status with a wrong key must be 401, got %d", code)
	}
	code, status := get(t, srv.URL+"/status", testKey)
	if code != 200 || status["strict"] != true || status["tools_count"] != float64(9) || status["expected_tools_count"] != float64(9) {
		t.Fatalf("/status: %d %v", code, status)
	}
	for _, secret := range []string{testKey, "WINREMOTE_AUTH_KEY"} {
		raw, _ := json.Marshal(status)
		if strings.Contains(string(raw), secret) {
			t.Fatalf("/status leaks %q", secret)
		}
	}
}

func TestClientOutsideTheAllowlistGets403OnEveryPathIncludingHealth(t *testing.T) {
	_, other, _ := net.ParseCIDR("100.64.0.9/32")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "t", Upstream: "u", Key: testKey, Allowlist: []*net.IPNet{other}, Desktop: &desktop.Fake{},
		Tools: profile.Tools, Strict: true,
	}))
	defer srv.Close()
	if code, body := get(t, srv.URL+"/status", testKey); code != http.StatusForbidden || !strings.Contains(body["error"].(string), "not in allowlist") {
		t.Fatalf("expected 403, got %d %v", code, body)
	}
	if code, body := get(t, srv.URL+"/health", ""); code != http.StatusForbidden || !strings.Contains(body["error"].(string), "not in allowlist") {
		t.Fatalf("/health must obey the allowlist too, got %d %v", code, body)
	}
}

func TestMCPRejectsAMissingKey(t *testing.T) {
	srv := start(t, &desktop.Fake{})
	if _, err := session(t, srv, "wrong"); err == nil {
		t.Fatal("connecting with a wrong key must fail")
	}
}

func TestListsExactlyTheNineTools(t *testing.T) {
	srv := start(t, &desktop.Fake{})
	cs, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	want := append([]string(nil), profile.Tools...)
	sort.Strings(names)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools %v, want %v", names, want)
	}
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// text returns the text of the last content block.
func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	tc, ok := res.Content[len(res.Content)-1].(*mcp.TextContent)
	if !ok {
		t.Fatalf("last block is %T", res.Content[len(res.Content)-1])
	}
	return tc.Text
}

func TestSnapshotSendsTheImageOnceAndNoStructuredCopy(t *testing.T) {
	fake := &desktop.Fake{
		Image:       []byte("\xff\xd8\xff-jpeg-bytes"),
		WindowList:  []desktop.Window{{Handle: 7, Title: "Notes", Left: 10, Top: 20, Right: 110, Bottom: 220}},
		ElementList: []desktop.Element{{Index: 1, Class: "Button", Text: "OK", Left: 0, Top: 0, Right: 10, Bottom: 10}},
	}
	srv := start(t, fake)
	cs, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res := call(t, cs, "Snapshot", map[string]any{"quality": 50, "max_width": 800})
	if len(res.Content) != 2 {
		t.Fatalf("want an image and a text, got %d blocks", len(res.Content))
	}
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/jpeg" || string(img.Data) != string(fake.Image) {
		t.Fatalf("first block must be the JPEG, got %#v", res.Content[0])
	}
	if res.StructuredContent != nil {
		t.Fatalf("the image must not be repeated as structured content: %v", res.StructuredContent)
	}
	got := text(t, res)
	for _, want := range []string{"[task:", "**System Language:** en_US", "[7] Notes (100x200 at 10,20)", "[1] OK — center (5,5)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("snapshot text lacks %q:\n%s", want, got)
		}
	}
	if log := strings.Join(fake.Log(), "|"); !strings.Contains(log, "screenshot q=50 w=800 m=0") {
		t.Fatalf("options not passed: %s", log)
	}
	noVision := call(t, cs, "Snapshot", map[string]any{"use_vision": "false"})
	if len(noVision.Content) != 1 {
		t.Fatalf("use_vision=false must return only the text, got %d blocks", len(noVision.Content))
	}
}

func TestInputToolsKeepTheirMessagesAndDriveTheDesktop(t *testing.T) {
	fake := &desktop.Fake{WindowList: []desktop.Window{
		{Handle: 5, Title: "Calculadora", Right: 10, Bottom: 10}, {Handle: 6, Title: "Notepad - notes.txt", Right: 10, Bottom: 10},
	}}
	srv := start(t, fake)
	cs, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"Click", map[string]any{"x": 10, "y": 20}, "Clicked left at (10,20)"},
		{"Click", map[string]any{"x": 1, "y": 2, "button": "right", "action": "double"}, "Double-clicked right at (1,2)"},
		{"Click", map[string]any{"x": 1, "y": 2, "action": "hover"}, "Hovered at (1,2)"},
		{"Type", map[string]any{"text": "héllo", "clear": "yes", "press_enter": true, "x": 3, "y": 4}, "Typed 5 chars"},
		{"Move", map[string]any{"x": 5, "y": 6}, "Moved to (5,6)"},
		{"Move", map[string]any{"x": 5, "y": 6, "drag": true, "start_x": 1, "start_y": 2}, "Dragged to (5,6)"},
		{"Scroll", map[string]any{"amount": -3}, "Scrolled -3 vertically"},
		{"Scroll", map[string]any{"amount": 2, "horizontal": true, "x": 9, "y": 9}, "Scrolled 2 horizontally"},
		{"Shortcut", map[string]any{"keys": "Ctrl+C"}, "Executed shortcut: Ctrl+C"},
		{"FocusWindow", map[string]any{"title": "notepad"}, "Focused window handle=6 title='Notepad - notes.txt'"},
		{"FocusWindow", map[string]any{"title": "zzzzzzzz"}, "No window matching 'zzzzzzzz'"},
		{"FocusWindow", map[string]any{}, "No window found"},
		{"Wait", map[string]any{"seconds": 0}, "Waited 0s"},
		{"GetSystemInfo", map[string]any{}, "**System:** Fake"},
	}
	for _, c := range cases {
		res := call(t, cs, c.tool, c.args)
		if res.IsError || !strings.Contains(text(t, res), c.want) {
			t.Errorf("%s %v: got error=%v %q, want %q", c.tool, c.args, res.IsError, text(t, res), c.want)
		}
	}
	log := strings.Join(fake.Log(), "|")
	for _, want := range []string{
		"click 3,4 left click", "hotkey [ctrl a]", "press delete", `type "héllo"`, "press enter",
		"drag from 1,2(true) to 5,6", "scroll 2 horizontal=true", "move 9,9", "hotkey [ctrl c]", "focus 6",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("desktop log lacks %q:\n%s", want, log)
		}
	}
}

func TestBadArgumentsAndDesktopFailuresAreToolErrors(t *testing.T) {
	fake := &desktop.Fake{}
	srv := start(t, fake)
	cs, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"Click", map[string]any{"x": 1}, "Click error: missing required parameter y"},
		{"Click", map[string]any{"x": 1, "y": 2, "button": "side"}, "button must be left, right or middle"},
		{"Click", map[string]any{"x": 1.5, "y": 2}, "x must be a whole number"},
		{"Snapshot", map[string]any{"quality": 0}, "quality must be between 1 and 100"},
		{"Wait", map[string]any{"seconds": 9999}, "seconds must be between 0 and 300"},
		{"Shortcut", map[string]any{"keys": " + "}, "keys is empty"},
	} {
		res := call(t, cs, c.tool, c.args)
		if !res.IsError || !strings.Contains(text(t, res), c.want) || !strings.Contains(text(t, res), "[task:") {
			t.Errorf("%s %v: got error=%v %q, want %q", c.tool, c.args, res.IsError, text(t, res), c.want)
		}
	}
	fake.Fail = desktop.ErrUnsupported
	res := call(t, cs, "GetSystemInfo", nil)
	if !res.IsError || !strings.Contains(text(t, res), "GetSystemInfo error: the desktop tools run only on Windows") {
		t.Errorf("a desktop failure must be a tool error, got %q", text(t, res))
	}
}

// TestStatusListsDiagnoseAsBuiltinNotAsConfiguredTool checks that Diagnose is reported apart from the nine tools.
func TestStatusListsDiagnoseAsBuiltinNotAsConfiguredTool(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	for _, withDiag := range []bool{false, true} {
		opts := server.Options{
			Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
			Desktop: &desktop.Fake{}, Tools: profile.Tools, Strict: true,
		}
		if withDiag {
			opts.Diag = func(int) map[string]any { return map[string]any{"version": "x"} }
		}
		srv := httptest.NewServer(server.New(opts))
		_, status := get(t, srv.URL+"/status", testKey)
		srv.Close()
		builtin, _ := status["builtin_tools"].([]any)
		if withDiag != (len(builtin) == 1 && builtin[0] == "Diagnose") {
			t.Errorf("withDiag=%v: builtin_tools=%v", withDiag, status["builtin_tools"])
		}
		if status["tools_count"] != float64(9) {
			t.Errorf("Diagnose must not change tools_count: %v", status["tools_count"])
		}
	}
}
