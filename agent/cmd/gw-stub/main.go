// Command gw-stub is a tiny MCP server over stdio used to test the agent's gateway: its sleep tool reports progress.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "gw-stub", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{
		Name: "sleep", Description: "Sleep for `seconds` (default 5), reporting progress every `every` seconds (default 5).",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"seconds": map[string]any{"type": "number"}, "every": map[string]any{"type": "number"},
		}},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var a struct{ Seconds, Every float64 }
		a.Seconds, a.Every = 5, 5
		_ = json.Unmarshal(req.Params.Arguments, &a)
		start := time.Now()
		token := req.Params.GetProgressToken()
		for time.Since(start) < time.Duration(a.Seconds*float64(time.Second)) {
			select {
			case <-ctx.Done():
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "cancelled after " + time.Since(start).Round(time.Second).String()}}}, nil
			case <-time.After(time.Duration(a.Every * float64(time.Second))):
			}
			if token != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token, Progress: time.Since(start).Seconds(), Total: a.Seconds, Message: "sleeping",
				})
			}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("slept %s, progress token %v", time.Since(start).Round(time.Second), token != nil)}}}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
