// Package parity is a black-box check of a WinBridge server against docs/AGENT-CONTRACT.md. It runs the same way
// against the Python agent and the Go agent, so both implementations are held to one contract.
package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolSpec is the contract of one tool: its parameter names and the ones without a default.
type toolSpec struct {
	params   []string
	required []string
}

var contract = map[string]toolSpec{
	"Snapshot":      {params: []string{"use_vision", "quality", "max_width", "monitor"}},
	"Click":         {params: []string{"x", "y", "button", "action"}, required: []string{"x", "y"}},
	"Type":          {params: []string{"text", "x", "y", "clear", "press_enter"}, required: []string{"text"}},
	"Move":          {params: []string{"x", "y", "drag", "start_x", "start_y", "duration"}, required: []string{"x", "y"}},
	"Scroll":        {params: []string{"amount", "x", "y", "horizontal"}, required: []string{"amount"}},
	"Shortcut":      {params: []string{"keys"}, required: []string{"keys"}},
	"FocusWindow":   {params: []string{"title", "handle"}},
	"Wait":          {params: []string{"seconds"}},
	"GetSystemInfo": {},
}

// extraParams are parameters an implementation may add on top of the contract.
var extraParams = map[string][]string{"Snapshot": {"region"}, "Type": {"method"}}

// bearer adds the Authorization header to every request.
type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// Run checks the server at baseURL (for example http://100.x.y.z:8091) with the given key and returns every
// deviation from the contract; an empty result means it conforms.
func Run(ctx context.Context, baseURL, key string) []string {
	baseURL = strings.TrimRight(baseURL, "/")
	var fails []string
	fail := func(format string, a ...any) { fails = append(fails, fmt.Sprintf(format, a...)) }

	code, health := getJSON(ctx, baseURL+"/health", "")
	if code != 200 || health["status"] != "ok" || health["profile"] != "winbridge" {
		fail("/health: want 200 status=ok profile=winbridge, got %d %v", code, health)
	}
	for _, field := range []string{"version", "upstream"} {
		if s, _ := health[field].(string); s == "" {
			fail("/health: missing %q", field)
		}
	}

	if code, _ := getJSON(ctx, baseURL+"/status", ""); code != http.StatusUnauthorized {
		fail("/status without a key: want 401, got %d", code)
	}
	if code, _ := getJSON(ctx, baseURL+"/status", "wrong-key"); code != http.StatusUnauthorized {
		fail("/status with a wrong key: want 401, got %d", code)
	}
	code, status := getJSON(ctx, baseURL+"/status", key)
	if code != 200 {
		fail("/status with the key: want 200, got %d", code)
	}
	if status["strict"] != true || status["profile"] != "winbridge" {
		fail("/status: want strict=true profile=winbridge, got %v %v", status["strict"], status["profile"])
	}
	want := sortedNames()
	if got := stringList(status["tools"]); !equal(got, want) {
		fail("/status tools: want %v, got %v", want, got)
	}
	if status["tools_count"] != float64(len(want)) || status["expected_tools_count"] != float64(len(want)) {
		fail("/status counts: want %d/%d, got %v/%v", len(want), len(want), status["tools_count"], status["expected_tools_count"])
	}

	fails = append(fails, checkTools(ctx, baseURL, key)...)
	return fails
}

// checkTools lists the MCP tools and compares names, parameter names and required parameters with the contract.
func checkTools(ctx context.Context, baseURL, key string) []string {
	var fails []string
	client := mcp.NewClient(&mcp.Implementation{Name: "parity", Version: "0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   baseURL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{key}},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return []string{"MCP connect: " + err.Error()}
	}
	defer session.Close()

	seen := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return append(fails, "MCP list tools: "+err.Error())
		}
		seen[tool.Name] = true
		if tool.Name == "ListServers" || tool.Name == "ServerTools" || tool.Name == "CallServerTool" {
			continue
		}
		if tool.Name == goOnlyTool {
			fails = append(fails, checkDiagnose(ctx, session)...)
			continue
		}
		spec, ok := contract[tool.Name]
		if !ok {
			fails = append(fails, "MCP: tool outside the contract: "+tool.Name)
			continue
		}
		props, required := schemaShape(tool.InputSchema)
		params := append(append([]string{}, spec.params...), extraParams[tool.Name]...)
		for _, p := range spec.params {
			if !contains(props, p) {
				fails = append(fails, fmt.Sprintf("MCP %s: missing parameter %q", tool.Name, p))
			}
		}
		for _, p := range props {
			if !contains(params, p) {
				fails = append(fails, fmt.Sprintf("MCP %s: parameter outside the contract %q", tool.Name, p))
			}
		}
		if !equal(required, spec.required) {
			fails = append(fails, fmt.Sprintf("MCP %s: required parameters want %v, got %v", tool.Name, spec.required, required))
		}
	}
	for name := range contract {
		if !seen[name] {
			fails = append(fails, "MCP: missing tool "+name)
		}
	}
	return fails
}

// schemaShape returns the sorted property names and required names of a tool input schema.
func schemaShape(schema any) (props, required []string) {
	raw, _ := json.Marshal(schema)
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	_ = json.Unmarshal(raw, &s)
	for name := range s.Properties {
		props = append(props, name)
	}
	sort.Strings(props)
	required = append(required, s.Required...)
	sort.Strings(required)
	return props, required
}

// getJSON fetches a URL, with a bearer key when given, and decodes a JSON object body (nil when it is not one).
func getJSON(ctx context.Context, url, key string) (int, map[string]any) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

func sortedNames() []string {
	names := make([]string, 0, len(contract))
	for name := range contract {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func stringList(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func equal(a, b []string) bool {
	a, b = append([]string{}, a...), append([]string{}, b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// goOnlyTool is the one tool the Go agent adds to the nine of the contract; the Python agent does not have it.
const goOnlyTool = "Diagnose"

// checkDiagnose calls Diagnose and checks that the structured report has the fields clients rely on.
func checkDiagnose(ctx context.Context, session *mcp.ClientSession) []string {
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: goOnlyTool, Arguments: map[string]any{"log_lines": 0}})
	if err != nil {
		return []string{"Diagnose call: " + err.Error()}
	}
	if res.IsError {
		return []string{"Diagnose returned an error"}
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		return []string{"Diagnose: no structured content"}
	}
	var fails []string
	for _, k := range []string{"version", "exe_sha256", "pid", "uptime_seconds", "memory", "session", "tools"} {
		if _, ok := m[k]; !ok {
			fails = append(fails, "Diagnose: missing field "+k)
		}
	}
	if sess, _ := m["session"].(map[string]any); sess == nil || sess["locked"] == nil {
		fails = append(fails, "Diagnose: session.locked is missing")
	}
	return fails
}
