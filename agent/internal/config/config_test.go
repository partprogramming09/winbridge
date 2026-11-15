package config

import (
	"strings"
	"testing"
)

// installed is what bootstrap.ps1 renders from winbridge.toml.template.
const installed = `
[server]
host = "100.64.1.10"
port = 8091
allow_insecure_remote = false

[security]
ip_allowlist = ["100.64.1.10", "100.64.1.2/32"]
enable_tier3 = false
disable_tier2 = false

[tools]
enable = ["Snapshot", "Click", "Type", "Move", "Scroll", "Shortcut", "FocusWindow", "Wait", "GetSystemInfo"]
exclude = []
`

func TestParsesTheInstalledFile(t *testing.T) {
	cfg, err := Parse(installed)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Host != "100.64.1.10" || cfg.Server.Port != 8091 || cfg.Server.AllowInsecureRemote {
		t.Fatalf("server table: %+v", cfg.Server)
	}
	if len(cfg.EnabledTools()) != 9 {
		t.Fatalf("tools: %v", cfg.EnabledTools())
	}
	nets, err := cfg.AllowedNetworks()
	if err != nil || len(nets) != 2 || nets[0].String() != "100.64.1.10/32" || nets[1].String() != "100.64.1.2/32" {
		t.Fatalf("allowlist: %v %v", nets, err)
	}
}

func TestUnknownKeysAreRefused(t *testing.T) {
	_, err := Parse(installed + "\n[server2]\nx = 1\n")
	if err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("an unknown table must fail, got %v", err)
	}
	if _, err := Parse(strings.Replace(installed, "allow_insecure_remote", "allow_insecure", 1)); err == nil {
		t.Fatal("a misspelled key must fail instead of silently keeping the default")
	}
}

func TestExcludeRemovesToolsCaseInsensitively(t *testing.T) {
	cfg, _ := Parse(installed + "")
	cfg.Tools.Exclude = []string{"wait"}
	for _, name := range cfg.EnabledTools() {
		if name == "Wait" {
			t.Fatal("Wait should be excluded")
		}
	}
}

func TestBadAllowlistEntriesAreReported(t *testing.T) {
	cfg := Config{Security: Security{IPAllowlist: []string{"100.64.0.1", "nope", "10.0.0.0/99"}}}
	_, err := cfg.AllowedNetworks()
	if err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "10.0.0.0/99") {
		t.Fatalf("both bad entries must be named, got %v", err)
	}
}

func TestParseFilesTable(t *testing.T) {
	cfg, err := Parse(installed + "\n[files]\nread_roots = ['C:\\Users\\alice\\Shared']\nmax_bytes = 4096\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Files.ReadRoots) != 1 || cfg.Files.ReadRoots[0] != `C:\Users\alice\Shared` || cfg.Files.MaxBytes != 4096 {
		t.Fatalf("files table = %+v", cfg.Files)
	}
	empty, err := Parse(installed)
	if err != nil || len(empty.Files.ReadRoots) != 0 {
		t.Fatalf("no [files] table must mean no roots: %+v %v", empty.Files, err)
	}
}

func TestParseWriteRoots(t *testing.T) {
	cfg, err := Parse(installed + "\n[files]\nwrite_roots = ['C:\\Users\\alice\\Out']\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Files.WriteRoots) != 1 || cfg.Files.WriteRoots[0] != `C:\Users\alice\Out` || len(cfg.Files.ReadRoots) != 0 {
		t.Fatalf("files table = %+v", cfg.Files)
	}
}

func TestParseRejectsUnknownFilesKey(t *testing.T) {
	if _, err := Parse(installed + "\n[files]\nread_root = ['C:\\x']\n"); err == nil {
		t.Fatal("a typo in [files] must be refused so it cannot silently open nothing, or something else")
	}
}
