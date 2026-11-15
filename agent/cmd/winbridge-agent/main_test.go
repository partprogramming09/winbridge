package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/partprogramming09/winbridge/agent/internal/config"
)

// TestPrintConfig checks that --print-config emits the summary the installer doctor reads, without needing a key.
func TestPrintConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "winbridge.toml")
	toml := "[server]\nhost = \"100.64.0.1\"\nport = 8090\n[security]\nip_allowlist = [\"100.64.0.2/32\"]\n[tools]\nenable = [\"Snapshot\"]\n"
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := run([]string{"--config", path, "--print-config"}, func(string) string { return "" })
	os.Stdout = stdout
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(r)
	var got struct {
		Host        string   `json:"host"`
		Port        int      `json:"port"`
		Allowlist   []string `json:"ip_allowlist"`
		EnableTier3 bool     `json:"enable_tier3"`
		Tools       []string `json:"tools"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not JSON: %q", raw)
	}
	if got.Host != "100.64.0.1" || got.Port != 8090 || len(got.Allowlist) != 1 || got.EnableTier3 || len(got.Tools) != 1 {
		t.Fatalf("unexpected summary: %+v", got)
	}
}

func TestBuildFilesIsNilWithoutTheTable(t *testing.T) {
	store, err := buildFiles(config.Config{}, func(string) string { return "" })
	if err != nil || store != nil {
		t.Fatalf("no [files] table must give no store: %v %v", store, err)
	}
}

func TestBuildFilesIsNilWithEmptyRoots(t *testing.T) {
	cfg := config.Config{Files: config.Files{MaxBytes: 1024}}
	store, err := buildFiles(cfg, func(string) string { return "" })
	if err != nil || store != nil {
		t.Fatalf("empty read_roots must give no store: %v %v", store, err)
	}
}

func TestBuildFilesBuildsAStoreForAnAllowedFolder(t *testing.T) {
	cfg := config.Config{Files: config.Files{ReadRoots: []string{t.TempDir()}}}
	store, err := buildFiles(cfg, func(string) string { return "" })
	if err != nil || store == nil {
		t.Fatalf("an ordinary folder must be accepted: %v %v", store, err)
	}
}

func TestBuildFilesRefusesJobsTasksAndWinBridgeOverlap(t *testing.T) {
	base := t.TempDir()
	jobsRoot := filepath.Join(base, "jobs")
	scripts := filepath.Join(base, "scripts")
	local := filepath.Join(base, "local")
	for _, d := range []string{jobsRoot, scripts, filepath.Join(local, "winbridge")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	getenv := func(k string) string {
		if k == "LOCALAPPDATA" {
			return local
		}
		return ""
	}
	for name, root := range map[string]string{"jobs": jobsRoot, "tasks": scripts, "winbridge": filepath.Join(local, "winbridge")} {
		cfg := config.Config{
			Jobs:  config.Jobs{Root: jobsRoot},
			Tasks: map[string]config.Task{"t": {Script: filepath.Join(scripts, "t.ps1")}},
			Files: config.Files{ReadRoots: []string{root}},
		}
		if _, err := buildFiles(cfg, getenv); err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("%s folder must be refused as overlap, got %v", name, err)
		}
	}
}
