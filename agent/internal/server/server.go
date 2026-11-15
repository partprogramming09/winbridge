// Package server assembles the HTTP surface: /health, /status and the MCP endpoint behind the guards.
package server

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/httpx"
	"github.com/partprogramming09/winbridge/agent/internal/procs"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/tools"
)

// Options is everything New needs.
type Options struct {
	Version   string
	Upstream  string
	Key       string
	Allowlist []*net.IPNet
	Desktop   desktop.Desktop
	Tools     []string
	Tasks     tools.TaskRunner                  // nil keeps the nine-tool profile
	Gateway   tools.GatewayRunner               // nil leaves the gateway tools out
	Jobs      tools.JobsRunner                  // nil leaves the job tools out
	Files     tools.FilesRunner                 // nil leaves the file tools out
	Procs     procs.Lister                      // nil leaves the Processes tool out
	Diag      func(logLines int) map[string]any // nil leaves the Diagnose tool out
	Strict    bool
}

// maxRequestBytes bounds a request body: the largest legitimate one is a Type call of 100000 characters.
const maxRequestBytes = 4 << 20

// instructions is what the MCP initialize answer tells the client.
const instructions = "WinBridge MCP Server. Provides a reviewed, nine-tool Windows desktop support profile over " +
	"Tailscale: Snapshot, Click, Type, Move, Scroll, Shortcut, FocusWindow, Wait and GetSystemInfo, plus the " +
	"read-only Diagnose tool. Input tools fail with an error while the Windows session is locked; Snapshot then " +
	"shows the lock screen and says so. Call Diagnose to check the agent instead of reading screenshots."

// New returns the HTTP handler of the agent.
func New(o Options) http.Handler {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "winbridge-mcp", Version: o.Version},
		&mcp.ServerOptions{Instructions: instructions})
	env := tools.NewEnv(o.Desktop)
	env.Tasks = o.Tasks
	env.Gateway = o.Gateway
	env.Jobs = o.Jobs
	env.Files = o.Files
	env.Procs = o.Procs
	env.Diag = o.Diag
	tools.Register(mcpServer, env, o.Tools)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "version": o.Version, "upstream": o.Upstream, "profile": o.profileName()})
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		names := append([]string(nil), o.Tools...)
		if o.Tasks != nil && len(o.Tasks.Names()) > 0 {
			names = append(names, "RunTask")
		}
		sort.Strings(names)
		builtin := []string{}
		if o.Diag != nil {
			builtin = append(builtin, "Diagnose")
		}
		if o.Gateway != nil && len(o.Gateway.Names()) > 0 {
			builtin = append(builtin, tools.GatewayToolNames...)
		}
		if o.Jobs != nil {
			builtin = append(builtin, tools.JobToolNames...)
		}
		if o.Files != nil {
			builtin = append(builtin, tools.FileToolNames...)
		}
		if o.Procs != nil {
			builtin = append(builtin, "Processes")
		}
		writeJSON(w, map[string]any{
			"status": "ok", "version": o.Version, "upstream": o.Upstream, "profile": o.profileName(),
			"strict": o.Strict, "tools": names, "tools_count": len(names), "expected_tools_count": len(profile.Tools),
			"builtin_tools": builtin,
		})
	})
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute}))

	// /health needs no key, but it obeys the IP allowlist like every other path: a tailnet device that is not allowed
	// learns nothing, not even the version. The PC's own Tailscale address is in the allowlist, so local checks work.
	public := map[string]bool{"/health": true}
	return http.MaxBytesHandler(httpx.Allowlist(o.Allowlist, nil, httpx.Auth(o.Key, public, mux)), maxRequestBytes)
}

// profileName is what /health and /status report as the profile.
func (o Options) profileName() string {
	if o.Strict {
		return "winbridge"
	}
	return "development"
}

// writeJSON sends a JSON body with status 200.
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
