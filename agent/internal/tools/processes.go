package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/procs"
)

const maxProcessLimit = 500

// registerProcesses adds the read-only Processes tool. It does not touch the desktop, so a locked session does not stop it.
func registerProcesses(s *mcp.Server, e *Env) {
	no := false
	s.AddTool(&mcp.Tool{
		Name: "Processes",
		Description: "List the running processes (pid, parent pid, name, Windows session, executable path when readable), " +
			"sorted by pid. Pass name to keep only processes whose name contains it (case-insensitive). Read-only.",
		InputSchema: object(nil, map[string]any{
			"name":  map[string]any{"type": "string", "description": "Part of the process name, for example notepad."},
			"limit": map[string]any{"type": "integer", "description": "Most processes to return, 1 to 500 (default 200)."},
		}),
		Annotations: &mcp.ToolAnnotations{Title: "Processes", ReadOnlyHint: true, OpenWorldHint: &no, DestructiveHint: boolPtr(false)},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fail := func(err error) (*mcp.CallToolResult, error) { return text("Processes error: "+err.Error(), true), nil }
		a, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return fail(err)
		}
		for key := range a {
			if key != "name" && key != "limit" {
				return fail(errors.New("unexpected parameter " + key))
			}
		}
		limit, err := a.Int("limit", 0)
		if err != nil {
			return fail(err)
		}
		if limit < 0 || limit > maxProcessLimit {
			return fail(errors.New("limit must be between 1 and 500"))
		}
		list, err := e.Procs()
		if err != nil {
			return fail(err)
		}
		raw, _ := json.MarshalIndent(procs.Filter(list, a.String("name", ""), limit), "", " ")
		return text(string(raw), false), nil
	})
}
