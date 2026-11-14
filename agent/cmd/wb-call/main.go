// Command wb-call calls one tool of a WinBridge MCP server and prints the result. The key comes from WINBRIDGE_KEY or,
// on a Mac, from the Keychain entry named by --keychain-service; it is never printed. Images can be saved to files.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearer adds the Authorization header to every request.
type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

func main() {
	url := flag.String("url", "", "MCP endpoint, for example http://100.x.y.z:8099/mcp")
	tool := flag.String("tool", "", "tool name; empty lists the tools")
	args := flag.String("args", "{}", "tool arguments as JSON")
	argsFile := flag.String("args-file", "", "read the tool arguments from this JSON file (avoids shell quoting)")
	saveDir := flag.String("save-dir", "", "save returned images into this directory and print their paths")
	keychain := flag.String("keychain-service", "", "macOS Keychain service holding the key, used when WINBRIDGE_KEY is empty")
	timeout := flag.Int("timeout", 60, "seconds the whole call may take")
	progress := flag.Bool("progress", false, "ask for progress notifications and print each one with the time since the call began")
	flag.Parse()
	if *argsFile != "" {
		data, err := os.ReadFile(*argsFile)
		if err != nil {
			fmt.Println("args-file:", err)
			os.Exit(2)
		}
		*args = string(data)
	}
	key := os.Getenv("WINBRIDGE_KEY")
	if key == "" && *keychain != "" {
		out, err := exec.Command("security", "find-generic-password", "-a", "winbridge", "-s", *keychain, "-w").Output()
		if err != nil {
			fmt.Println("keychain: no key found for", *keychain)
			os.Exit(2)
		}
		key = strings.TrimSpace(string(out))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()
	began := time.Now()
	opts := &mcp.ClientOptions{}
	if *progress {
		opts.ProgressNotificationHandler = func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			fmt.Printf("progress +%s: %.0f/%.0f %s\n", time.Since(began).Round(time.Second), r.Params.Progress, r.Params.Total, r.Params.Message)
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "wb-call", Version: "0"}, opts)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   *url,
		HTTPClient: &http.Client{Transport: bearer{key}},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	defer session.Close()

	if *tool == "" {
		for t, err := range session.Tools(ctx, nil) {
			if err != nil {
				fmt.Println("list:", err)
				os.Exit(1)
			}
			fmt.Println(t.Name)
		}
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(*args), &parsed); err != nil {
		fmt.Println("args:", err)
		os.Exit(2)
	}
	params := &mcp.CallToolParams{Name: *tool, Arguments: parsed}
	if *progress {
		params.SetProgressToken("wb-call")
	}
	began = time.Now()
	res, err := session.CallTool(ctx, params)
	if err != nil {
		fmt.Println("call:", err)
		os.Exit(1)
	}
	fmt.Println("isError:", res.IsError, "structured:", res.StructuredContent != nil)
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			fmt.Println("text:", v.Text)
		case *mcp.ImageContent:
			fmt.Printf("image: %s %d bytes\n", v.MIMEType, len(v.Data))
			if *saveDir != "" {
				if err := os.MkdirAll(*saveDir, 0o700); err != nil {
					fmt.Println("save-dir:", err)
					os.Exit(1)
				}
				path := filepath.Join(*saveDir, fmt.Sprintf("image-%d.jpg", time.Now().UnixNano()))
				if err := os.WriteFile(path, v.Data, 0o600); err != nil {
					fmt.Println("save:", err)
					os.Exit(1)
				}
				fmt.Println("saved:", path)
			}
		}
	}
}
