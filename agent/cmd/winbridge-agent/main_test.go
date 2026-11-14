package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
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
