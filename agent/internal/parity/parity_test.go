package parity_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/diag"
	"github.com/partprogramming09/winbridge/agent/internal/jobs"
	"github.com/partprogramming09/winbridge/agent/internal/parity"
	"github.com/partprogramming09/winbridge/agent/internal/procs"
	"github.com/partprogramming09/winbridge/agent/internal/profile"
	"github.com/partprogramming09/winbridge/agent/internal/server"
)

const key = "parity-key-0123456789"

// TestGoAgentConformsToTheContract runs the black-box check against the Go server with a fake desktop.
func TestGoAgentConformsToTheContract(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	fake := &desktop.Fake{}
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: key, Allowlist: []*net.IPNet{loopback},
		Desktop: fake, Tools: profile.Tools, Strict: true,
	}))
	defer srv.Close()
	if fails := parity.Run(context.Background(), srv.URL, key); len(fails) != 0 {
		t.Fatalf("deviations from the contract:\n%s", strings.Join(fails, "\n"))
	}
}

// TestGoAgentWithDiagnoseStillConforms adds the Go-only Diagnose tool and checks that the contract still holds.
func TestGoAgentWithDiagnoseStillConforms(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	fake := &desktop.Fake{}
	c := diag.New(diag.Options{Version: "0.0.0-test", Desktop: fake})
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: key, Allowlist: []*net.IPNet{loopback},
		Desktop: fake, Tools: profile.Tools, Strict: true, Diag: c.Collect,
	}))
	defer srv.Close()
	if fails := parity.Run(context.Background(), srv.URL, key); len(fails) != 0 {
		t.Fatalf("deviations from the contract:\n%s", strings.Join(fails, "\n"))
	}
}

// TestRunReportsDeviations proves the check fails on a server that is not WinBridge.
func TestRunReportsDeviations(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if fails := parity.Run(context.Background(), srv.URL, key); len(fails) < 3 {
		t.Fatalf("want several deviations from a foreign server, got %v", fails)
	}
}

// TestGoAgentWithProcessesStillConforms adds the read-only Processes tool, which the real agent always exposes.
func TestGoAgentWithProcessesStillConforms(t *testing.T) {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	fake := &desktop.Fake{}
	list := []procs.Process{{PID: 4, Name: "System"}}
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: key, Allowlist: []*net.IPNet{loopback},
		Desktop: fake, Tools: profile.Tools, Strict: true,
		Procs: func() ([]procs.Process, error) { return list, nil },
	}))
	defer srv.Close()
	if fails := parity.Run(context.Background(), srv.URL, key); len(fails) != 0 {
		t.Fatalf("deviations from the contract:\n%s", strings.Join(fails, "\n"))
	}
}

// TestGoAgentWithJobsStillConforms enables the job tools, which exist only when the owner configures [jobs].
func TestGoAgentWithJobsStillConforms(t *testing.T) {
	root := t.TempDir()
	for _, sub := range []string{"inbox", "out", "cancel"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"jobs":{"state":{"script":"s.ps1","sha256":"aa","timeout_s":30}}}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := jobs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	srv := httptest.NewServer(server.New(server.Options{
		Version: "0.0.0-test", Upstream: "native-go", Key: key, Allowlist: []*net.IPNet{loopback},
		Desktop: &desktop.Fake{}, Tools: profile.Tools, Jobs: store, Strict: true,
	}))
	defer srv.Close()
	if fails := parity.Run(context.Background(), srv.URL, key); len(fails) != 0 {
		t.Fatalf("deviations from the contract:\n%s", strings.Join(fails, "\n"))
	}
}
