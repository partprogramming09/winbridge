// Package config reads the TOML file the WinBridge installer writes.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Server is the [server] table.
type Server struct {
	Host                string `toml:"host"`
	Port                int    `toml:"port"`
	AllowInsecureRemote bool   `toml:"allow_insecure_remote"`
}

// Security is the [security] table.
type Security struct {
	IPAllowlist  []string `toml:"ip_allowlist"`
	EnableTier3  bool     `toml:"enable_tier3"`
	DisableTier2 bool     `toml:"disable_tier2"`
}

// Tools is the [tools] table.
type Tools struct {
	Enable  []string `toml:"enable"`
	Exclude []string `toml:"exclude"`
}

// Task is one [tasks.<name>] table: a script and the SHA-256 it must have.
type Task struct {
	Script string `toml:"script"`
	SHA256 string `toml:"sha256"`
}

// MCPServer is one [servers.<name>] table: a launcher pinned by SHA-256 and the child tools a caller may use.
type MCPServer struct {
	Command    string              `toml:"command"`
	Args       []string            `toml:"args"`
	SHA256     string              `toml:"sha256"`
	Files      map[string]string   `toml:"files"`
	Env        map[string]string   `toml:"env"`
	AllowTools []string            `toml:"allow_tools"`
	DenyArgs   map[string][]string `toml:"deny_args"`
	Timeout    int                 `toml:"timeout"`
	Idle       int                 `toml:"idle"`
	MaxOutput  int                 `toml:"max_output"`
}

// Jobs is the [jobs] table: the folder the elevated job runner serves.
type Jobs struct {
	Root string `toml:"root"`
}

// Files is the [files] table: the folders the file tools may reach. Without it the tools do not exist.
type Files struct {
	ReadRoots  []string `toml:"read_roots"`
	WriteRoots []string `toml:"write_roots"` // also readable; only data extensions are written
	MaxBytes   int64    `toml:"max_bytes"`
}

// Config is the whole file.
type Config struct {
	Server   Server               `toml:"server"`
	Security Security             `toml:"security"`
	Tools    Tools                `toml:"tools"`
	Tasks    map[string]Task      `toml:"tasks"`
	Servers  map[string]MCPServer `toml:"servers"`
	Jobs     Jobs                 `toml:"jobs"`
	Files    Files                `toml:"files"`
}

// Load reads and validates the file at path; unknown keys are an error so a typo cannot widen the profile silently.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(string(data))
}

// Parse decodes a TOML document.
func Parse(text string) (Config, error) {
	var cfg Config
	meta, err := toml.Decode(text, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("config is not valid TOML: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("config has unknown keys: %s", strings.Join(keys, ", "))
	}
	return cfg, nil
}

// EnabledTools returns the tools named in enable (in order) without those in exclude.
func (c Config) EnabledTools() []string {
	excluded := make(map[string]bool, len(c.Tools.Exclude))
	for _, name := range c.Tools.Exclude {
		excluded[strings.ToLower(name)] = true
	}
	var out []string
	for _, name := range c.Tools.Enable {
		if !excluded[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	return out
}

// AllowedNetworks parses ip_allowlist; a bare address becomes a /32 or /128.
func (c Config) AllowedNetworks() ([]*net.IPNet, error) {
	var nets []*net.IPNet
	var problems []string
	for _, raw := range c.Security.IPAllowlist {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				problems = append(problems, entry+": not an IP address")
				continue
			}
			if ip.To4() != nil {
				entry += "/32"
			} else {
				entry += "/128"
			}
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			problems = append(problems, raw+": "+err.Error())
			continue
		}
		nets = append(nets, network)
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid ip_allowlist entries: " + strings.Join(problems, "; "))
	}
	return nets, nil
}
