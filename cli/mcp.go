package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// mcpClient is a minimal MCP client over streamable HTTP: initialize once, then tools/call.
type mcpClient struct {
	http    *http.Client
	url     string
	key     string
	session string
	nextID  int
}

// rpcResponse is the part of a JSON-RPC answer the client reads.
type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// dialMCP opens a session with the host's /mcp endpoint using the key.
func dialMCP(client *http.Client, h Host, key string) (*mcpClient, error) {
	c := &mcpClient{http: client, url: baseURL(h) + "/mcp", key: key}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "winbridge-cli", "version": version},
	}, true)
	if err != nil {
		return nil, err
	}
	if _, err := c.rpc(ctx, "notifications/initialized", map[string]any{}, false); err != nil {
		return nil, err
	}
	return c, nil
}

// rpc sends one JSON-RPC message; with wantReply it returns the matching result.
func (c *mcpClient) rpc(ctx context.Context, method string, params any, wantReply bool) (json.RawMessage, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	id := 0
	if wantReply {
		c.nextID++
		id = c.nextID
		msg["id"] = id
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.session = s
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s answered %d: %s", method, resp.StatusCode, explainHTTPStatus(resp.StatusCode))
	}
	if !wantReply {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil
	}
	return readReply(resp, id)
}

// readReply returns the result of the JSON-RPC answer with the given id, from a JSON body or an event stream.
func readReply(resp *http.Response, id int) (json.RawMessage, error) {
	want := fmt.Sprint(id)
	pick := func(raw []byte) (json.RawMessage, bool, error) {
		var r rpcResponse
		if err := json.Unmarshal(raw, &r); err != nil || string(r.ID) != want {
			return nil, false, nil
		}
		if r.Error != nil {
			return nil, true, errors.New(r.Error.Message)
		}
		return r.Result, true, nil
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, err
		}
		res, ok, err := pick(raw)
		if !ok && err == nil {
			err = errors.New("the answer did not carry the expected id")
		}
		return res, err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if res, ok, err := pick([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))); ok {
			return res, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("the event stream ended without an answer")
}

// callTool runs one tool and returns its text and whether the tool reported an error.
func (c *mcpClient) callTool(timeout time.Duration, name string, args map[string]any) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, true)
	if err != nil {
		return "", false, err
	}
	var res struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, part := range res.Content {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return b.String(), res.IsError, nil
}
