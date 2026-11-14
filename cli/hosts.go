package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Host describes one Windows PC reachable over Tailscale. It never holds the auth key.
type Host struct {
	Address         string `json:"address"`
	Port            int    `json:"port"`
	User            string `json:"user"`
	SSHKey          string `json:"ssh_key"`
	KeychainService string `json:"keychain_service"`
	ClientIP        string `json:"client_ip"`
}

// Config is the file ~/.config/winbridge/hosts.json.
type Config struct {
	Hosts map[string]Host `json:"hosts"`
}

// configPath returns the location of the hosts file, overridable with WINBRIDGE_CONFIG.
func configPath() string {
	if p := os.Getenv("WINBRIDGE_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "hosts.json"
	}
	return filepath.Join(home, ".config", "winbridge", "hosts.json")
}

// loadConfig reads the hosts file; a missing file is an empty configuration.
func loadConfig(path string) (Config, error) {
	cfg := Config{Hosts: map[string]Host{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if cfg.Hosts == nil {
		cfg.Hosts = map[string]Host{}
	}
	return cfg, nil
}

// saveConfig writes the hosts file with owner-only permissions.
func saveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// resolve picks a host by name, or the only host when no name is given.
func (c Config) resolve(name string) (string, Host, error) {
	if name != "" {
		h, ok := c.Hosts[name]
		if !ok {
			return "", Host{}, fmt.Errorf("no host named %q; `winbridge hosts` lists them", name)
		}
		return name, h, nil
	}
	names := c.names()
	switch len(names) {
	case 0:
		return "", Host{}, errors.New("no hosts configured; run `winbridge hosts add`")
	case 1:
		return names[0], c.Hosts[names[0]], nil
	}
	return "", Host{}, fmt.Errorf("several hosts configured (%s); name one", strings.Join(names, ", "))
}

// names returns the host names in alphabetical order.
func (c Config) names() []string {
	names := make([]string, 0, len(c.Hosts))
	for n := range c.Hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// validate checks the fields every command needs.
func (h Host) validate() error {
	switch {
	case h.Address == "":
		return errors.New("address is required")
	case h.Port < 1024 || h.Port > 65535:
		return errors.New("port must be between 1024 and 65535")
	case h.User == "":
		return errors.New("user is required")
	case h.KeychainService == "":
		return errors.New("keychain_service is required")
	case !safeUser.MatchString(h.User):
		return errors.New("user may only have letters, digits, dot, underscore and hyphen, and must not start with a hyphen")
	case !safeAddress.MatchString(h.Address):
		return errors.New("address may only be a host name or an IP address")
	case h.ClientIP != "" && net.ParseIP(h.ClientIP) == nil:
		return errors.New("client_ip must be an IP address")
	}
	return nil
}

// safeUser and safeAddress keep ssh options and cmd.exe metacharacters out of the values that reach a command line.
var (
	safeUser    = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*$`)
	safeAddress = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)
)

// expandHome replaces a leading ~ with the home directory.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
