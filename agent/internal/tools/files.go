package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/files"
)

// FilesRunner lists, reads and writes files inside the folders the owner allowed; a nil runner means the file tools do
// not exist. Writing is refused unless the path is in a write folder.
type FilesRunner interface {
	List(path string) ([]files.Entry, bool, error)
	Read(path, encoding string, offset int64) (files.Content, error)
	Write(path, data, encoding string, overwrite bool) (int64, error)
}

// FileToolNames are the tools registered when the owner configures [files].
var FileToolNames = []string{"ListFiles", "ReadFile", "WriteFile"}

// registerFiles adds the file tools. They never touch the desktop, so a locked session does not stop them.
func registerFiles(s *mcp.Server, e *Env) {
	no := false
	add := func(name, title, desc string, schema map[string]any, readOnly bool, run func(a Args) (any, error)) {
		s.AddTool(&mcp.Tool{
			Name: name, Description: desc, InputSchema: schema,
			Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: readOnly, OpenWorldHint: &no, DestructiveHint: boolPtr(!readOnly)},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a, err := parseArgs(req.Params.Arguments)
			if err == nil {
				var out any
				if out, err = run(a); err == nil {
					if msg, ok := out.(string); ok { // a plain sentence, not JSON: Windows paths would show doubled backslashes
						return text(msg, false), nil
					}
					raw, _ := json.MarshalIndent(out, "", " ")
					return text(string(raw), false), nil
				}
			}
			return text(name+" error: "+err.Error(), true), nil
		})
	}
	pathProp := map[string]any{"type": "string", "description": "Absolute path inside a folder the owner allowed."}
	add("ListFiles", "List files",
		"List a folder (name, kind, size, modified time; at most 500 entries, sorted by name). Only folders the owner allowed in [files] are reachable; any other path is refused.",
		object([]string{"path"}, map[string]any{"path": pathProp}),
		true, func(a Args) (any, error) {
			if err := only(a, "path"); err != nil {
				return nil, err
			}
			path, err := a.RequireString("path")
			if err != nil {
				return nil, err
			}
			entries, truncated, err := e.Files.List(path)
			if err != nil {
				return nil, err
			}
			return map[string]any{"entries": entries, "truncated": truncated}, nil
		})
	add("ReadFile", "Read file",
		"Read a file as UTF-8 text, or as base64 with encoding=base64 for binary files. Returns at most the owner's limit (256 KiB by default) from offset; truncated says there is more. Only folders the owner allowed in [files] are reachable.",
		object([]string{"path"}, map[string]any{
			"path":     pathProp,
			"encoding": map[string]any{"type": "string", "enum": []string{"utf8", "base64"}, "description": "utf8 (default) or base64."},
			"offset":   map[string]any{"type": "integer", "description": "Byte offset to start from (default 0)."},
		}),
		true, func(a Args) (any, error) {
			if err := only(a, "path", "encoding", "offset"); err != nil {
				return nil, err
			}
			path, err := a.RequireString("path")
			if err != nil {
				return nil, err
			}
			offset, err := a.Int("offset", 0)
			if err != nil {
				return nil, err
			}
			return e.Files.Read(path, a.String("encoding", ""), int64(offset))
		})
	add("WriteFile", "Write file",
		"Write a data file (UTF-8 text, or base64 with encoding=base64) inside a write folder the owner allowed in [files]. Never replaces an existing file unless overwrite=true. Only data extensions are accepted (txt, md, json, csv, log, xml, yaml, yml, toml, ini, html, png, jpg, pdf): no scripts or executables. The parent folder must already exist. Folders that are read-only, and any other path, are refused.",
		object([]string{"path", "content"}, map[string]any{
			"path":      pathProp,
			"content":   map[string]any{"type": "string", "description": "The file content: UTF-8 text, or base64 when encoding=base64."},
			"encoding":  map[string]any{"type": "string", "enum": []string{"utf8", "base64"}, "description": "utf8 (default) or base64."},
			"overwrite": map[string]any{"type": "boolean", "description": "Replace the file if it exists (default false)."},
		}),
		false, func(a Args) (any, error) {
			if err := only(a, "path", "content", "encoding", "overwrite"); err != nil {
				return nil, err
			}
			path, err := a.RequireString("path")
			if err != nil {
				return nil, err
			}
			content, err := a.RequireString("content")
			if err != nil {
				return nil, err
			}
			n, err := e.Files.Write(path, content, a.String("encoding", ""), a.Bool("overwrite", false))
			if err != nil {
				return nil, err
			}
			return fmt.Sprintf("wrote %d bytes to %s", n, path), nil
		})
}
