package profile

import (
	"net"
	"strings"
	"testing"
)

func nets(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// good returns a profile that passes; each case below breaks one thing.
func good(t *testing.T) Strict {
	return Strict{
		Host: "100.64.1.10", Port: 8091, HasKey: true, EnabledTools: append([]string(nil), Tools...),
		Allowlist: nets(t, "100.64.1.10/32", "100.64.1.2/32"),
	}
}

func TestStrictAcceptsTheReviewedProfile(t *testing.T) {
	if err := good(t).Validate(); err != nil {
		t.Fatal(err)
	}
	s := good(t)
	s.Host = "fd7a:115c:a1e0::1234"
	s.Allowlist = nets(t, "fd7a:115c:a1e0::1234/128")
	if err := s.Validate(); err != nil {
		t.Fatalf("an IPv6 Tailscale address must pass: %v", err)
	}
}

func TestStrictRefusesWhatLeavesTheProfile(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Strict)
		want   string
	}{
		{"all interfaces", func(s *Strict) { s.Host = "0.0.0.0" }, "Tailscale IPv4/IPv6 bind address"},
		{"loopback", func(s *Strict) { s.Host = "127.0.0.1" }, "Tailscale IPv4/IPv6 bind address"},
		{"LAN", func(s *Strict) { s.Host = "192.168.1.8" }, "Tailscale IPv4/IPv6 bind address"},
		{"a name", func(s *Strict) { s.Host = "localhost" }, "Tailscale IPv4/IPv6 bind address"},
		{"low port", func(s *Strict) { s.Port = 80 }, "port between 1024 and 65535"},
		{"no key", func(s *Strict) { s.HasKey = false }, "requires an auth key"},
		{"insecure remote", func(s *Strict) { s.AllowInsecureRemote = true }, "allow_insecure_remote"},
		{"tier 3", func(s *Strict) { s.EnableTier3 = true }, "enable_tier3"},
		{"no allowlist", func(s *Strict) { s.Allowlist = nil }, "non-empty ip_allowlist"},
		{"public client", func(s *Strict) { s.Allowlist = nets(t, "8.8.8.8/32") }, "only allows Tailscale clients"},
		{"a range wider than Tailscale", func(s *Strict) { s.Allowlist = nets(t, "100.64.0.0/9") }, "only allows Tailscale clients"},
		{"missing tool", func(s *Strict) { s.EnabledTools = s.EnabledTools[1:] }, "missing: Snapshot"},
		{"extra tool", func(s *Strict) { s.EnabledTools = append(s.EnabledTools, "Shell") }, "extra: Shell"},
	}
	for _, c := range cases {
		s := good(t)
		c.mutate(&s)
		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", c.name, err, c.want)
		}
	}
}

func TestIsTailscaleAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.64.0.1": true, "100.127.255.255": true, "100.128.0.1": false, "100.63.255.255": false,
		"fd7a:115c:a1e0::1": true, "[fd7a:115c:a1e0::1]": true, "fd00::1": false, "": false, "not-an-ip": false,
	} {
		if got := IsTailscaleAddr(addr); got != want {
			t.Errorf("IsTailscaleAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
