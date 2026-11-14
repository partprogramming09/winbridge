package server_test

import (
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/procs"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

// startWithProcs runs the agent with a fixed process list.
func startWithProcs(t *testing.T, d desktop.Desktop, list []procs.Process, err error) *httptest.Server {
	t.Helper()
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: testKey, Allowlist: []*net.IPNet{loopback},
		Desktop: d, Tools: profile.Tools, Strict: true,
		Procs: func() ([]procs.Process, error) { return list, err },
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProcessesFiltersByNameAndWorksWhileLocked(t *testing.T) {
	list := []procs.Process{{PID: 7, Name: "notepad.exe", Session: 1}, {PID: 8, Name: "chrome.exe", Session: 1}}
	srv := startWithProcs(t, &desktop.Fake{Session: desktop.SessionState{Locked: true, Desktop: "Winlogon"}}, list, nil)
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out, isErr := callTool(t, s, "Processes", map[string]any{"name": "note"})
	if isErr || !strings.Contains(out, "notepad.exe") || strings.Contains(out, "chrome.exe") {
		t.Fatalf("Processes: %v %s", isErr, out)
	}
	_, status := get(t, srv.URL+"/status", testKey)
	if status["tools_count"] != float64(9) || !strings.Contains(fmt.Sprint(status["builtin_tools"]), "Processes") {
		t.Fatalf("Processes must be built-in and keep tools_count 9: %v", status)
	}
}

func TestProcessesRejectsBadInputAndReportsListErrors(t *testing.T) {
	srv := startWithProcs(t, &desktop.Fake{}, nil, fmt.Errorf("boom"))
	s, err := session(t, srv, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, args := range []map[string]any{{"name": "x", "path": "C:\\"}, {"limit": 100000}, {"limit": -1}} {
		if out, isErr := callTool(t, s, "Processes", args); !isErr {
			t.Errorf("%v must fail, got %s", args, out)
		}
	}
	if out, isErr := callTool(t, s, "Processes", nil); !isErr || !strings.Contains(out, "boom") {
		t.Fatalf("a listing error must reach the caller: %v %s", isErr, out)
	}
}
