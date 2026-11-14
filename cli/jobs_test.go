package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scriptedCaller answers tool calls from a function and records them.
type scriptedCaller struct {
	calls []string
	fn    func(name string, args map[string]any) (string, bool, error)
}

// callTool records the call and delegates to fn.
func (s *scriptedCaller) callTool(_ time.Duration, name string, args map[string]any) (string, bool, error) {
	s.calls = append(s.calls, name)
	return s.fn(name, args)
}

func (s *scriptedCaller) count(name string) int {
	n := 0
	for _, c := range s.calls {
		if c == name {
			n++
		}
	}
	return n
}

func init() { pause = func(time.Duration) {} }

func TestMCPClientSendsKeyAndSessionAndReadsJSONAndSSE(t *testing.T) {
	var sawKey, sawSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "S1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, *msg.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			sawKey, sawSession = r.Header.Get("Authorization"), r.Header.Get("Mcp-Session-Id")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}],\"isError\":true}}\n\n", *msg.ID)
		}
	}))
	defer srv.Close()
	var host Host
	host.Address, host.Port = splitHostPort(t, srv.URL)
	c, err := dialMCP(srv.Client(), host, "K")
	if err != nil {
		t.Fatal(err)
	}
	text, isErr, err := c.callTool(5*time.Second, "Wait", nil)
	if err != nil || text != "hi" || !isErr {
		t.Fatalf("text=%q isErr=%v err=%v", text, isErr, err)
	}
	if sawKey != "Bearer K" || sawSession != "S1" {
		t.Fatalf("key=%q session=%q", sawKey, sawSession)
	}
}

// splitHostPort returns the address and port of a test server URL.
func splitHostPort(t *testing.T, url string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func TestJobsRunWaitsThenPrintsWholeLogAndState(t *testing.T) {
	waits := 0
	c := &scriptedCaller{fn: func(name string, a map[string]any) (string, bool, error) {
		switch name {
		case "SubmitJob":
			return `{"id":"j1"}`, false, nil
		case "WaitJob":
			waits++
			if waits < 2 {
				return `{"id":"j1","state":"running"}`, false, nil
			}
			return `{"id":"j1","state":"done","exit_code":0}`, false, nil
		case "JobStatus":
			if a["offset"].(int64) == 0 {
				return `{"id":"j1","state":"done","exit_code":0,"log":"abc","log_size":6,"next_offset":3}`, false, nil
			}
			return `{"id":"j1","state":"done","exit_code":0,"log":"def","log_size":6,"next_offset":6}`, false, nil
		}
		return "", true, errors.New("unexpected " + name)
	}}
	var out bytes.Buffer
	if err := jobsCommand(c, []string{"run", "game", "Api=d3d11"}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "abcdef") || !strings.Contains(out.String(), "done (exit code 0)") || waits != 2 {
		t.Fatalf("waits=%d out=%q", waits, out.String())
	}
}

func TestJobsRunFailsWhenTheJobDoesNotEndDone(t *testing.T) {
	c := &scriptedCaller{fn: func(name string, _ map[string]any) (string, bool, error) {
		switch name {
		case "SubmitJob":
			return `{"id":"j2"}`, false, nil
		case "WaitJob":
			return `{"id":"j2","state":"timeout"}`, false, nil
		}
		return `{"id":"j2","state":"timeout","log":"","log_size":0,"next_offset":0,"error":"exceeded 2 s"}`, false, nil
	}}
	var out bytes.Buffer
	if err := jobsCommand(c, []string{"run", "x"}, &out); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err=%v", err)
	}
}

func TestJobsSubmitShowsBusyAndNeverRetriesAndParsesPairsAndFlags(t *testing.T) {
	var got map[string]any
	c := &scriptedCaller{fn: func(name string, a map[string]any) (string, bool, error) {
		got = a
		return "busy: j9 (game, running); wait for it", true, nil
	}}
	var out bytes.Buffer
	err := jobsCommand(c, []string{"submit", "game", "Api=d3d11", "-queue"}, &out)
	if err == nil || !strings.Contains(err.Error(), "busy: j9") {
		t.Fatalf("err=%v", err)
	}
	if got["queue"] != true || got["args"].(map[string]any)["Api"] != "d3d11" {
		t.Fatalf("args=%v", got)
	}
	if err := jobsCommand(c, []string{"submit", "game", "oops"}, &out); err == nil {
		t.Fatal("NAME=value is required")
	}
}

func TestReadOnlyCallsRetryNetworkErrorsButSubmitDoesNot(t *testing.T) {
	c := &scriptedCaller{fn: func(string, map[string]any) (string, bool, error) { return "", false, errors.New("timeout") }}
	var out bytes.Buffer
	_ = jobsCommand(c, []string{"status", "j1"}, &out)
	if c.count("JobStatus") != 3 {
		t.Fatalf("JobStatus tried %d times", c.count("JobStatus"))
	}
	_ = jobsCommand(c, []string{"submit", "x"}, &out)
	if c.count("SubmitJob") != 1 {
		t.Fatalf("SubmitJob tried %d times", c.count("SubmitJob"))
	}
}

func TestTakeHostPullsTheFlagFromAnywhere(t *testing.T) {
	host, rest := takeHost([]string{"run", "x", "--host", "laptop", "A=1"})
	if host != "laptop" || strings.Join(rest, " ") != "run x A=1" {
		t.Fatalf("host=%q rest=%v", host, rest)
	}
}
