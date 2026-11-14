// Package profile holds the strict WinBridge profile: the exact tool set and where the agent may listen.
package profile

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Tools is the reviewed tool set, in the order the installer writes it.
var Tools = []string{"Snapshot", "Click", "Type", "Move", "Scroll", "Shortcut", "FocusWindow", "Wait", "GetSystemInfo"}

var tailscaleNetworks = mustNetworks("100.64.0.0/10", "fd7a:115c:a1e0::/48")

// mustNetworks parses CIDRs known at compile time.
func mustNetworks(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		nets = append(nets, n)
	}
	return nets
}

// IsTailscaleAddr reports whether host is an address inside Tailscale's IPv4 or IPv6 range.
func IsTailscaleAddr(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	return InTailscale(ip)
}

// InTailscale reports whether ip is inside Tailscale's ranges.
func InTailscale(ip net.IP) bool {
	for _, n := range tailscaleNetworks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Strict is everything the strict profile checks at start-up.
type Strict struct {
	Host                string
	Port                int
	HasKey              bool
	AllowInsecureRemote bool
	EnableTier3         bool
	EnabledTools        []string
	Allowlist           []*net.IPNet
}

// Validate returns an error unless the agent runs exactly the reviewed profile; the agent refuses to start otherwise.
func (s Strict) Validate() error {
	if s.Port < 1024 || s.Port > 65535 {
		return errors.New("WinBridge strict profile requires a port between 1024 and 65535")
	}
	if !IsTailscaleAddr(s.Host) {
		return errors.New("WinBridge strict profile requires a Tailscale IPv4/IPv6 bind address; " +
			"0.0.0.0, loopback, LAN and public addresses are refused")
	}
	if !s.HasKey {
		return errors.New("WinBridge strict profile requires an auth key")
	}
	if s.AllowInsecureRemote {
		return errors.New("WinBridge strict profile refuses allow_insecure_remote")
	}
	if s.EnableTier3 {
		return errors.New("WinBridge strict profile refuses enable_tier3")
	}
	if len(s.Allowlist) == 0 {
		return errors.New("WinBridge strict profile requires a non-empty ip_allowlist")
	}
	for _, n := range s.Allowlist {
		if !InTailscale(n.IP) || !contained(n) {
			return fmt.Errorf("WinBridge strict profile only allows Tailscale clients; %s is outside 100.64.0.0/10 and fd7a:115c:a1e0::/48", n)
		}
	}
	missing, extra := diff(Tools, s.EnabledTools)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("WinBridge strict profile requires exactly the 9 reviewed tools (missing: %s; extra: %s)",
			orNone(missing), orNone(extra))
	}
	return nil
}

// contained reports whether the whole network n lies inside a Tailscale range, not just its first address.
func contained(n *net.IPNet) bool {
	ones, bits := n.Mask.Size()
	for _, t := range tailscaleNetworks {
		tOnes, tBits := t.Mask.Size()
		if tBits == bits && ones >= tOnes && t.Contains(n.IP) {
			return true
		}
	}
	return false
}

// diff returns the names in want that are not in have, and those in have that are not in want, both sorted.
func diff(want, have []string) (missing, extra []string) {
	wantSet := toSet(want)
	haveSet := toSet(have)
	for name := range wantSet {
		if !haveSet[name] {
			missing = append(missing, name)
		}
	}
	for name := range haveSet {
		if !wantSet[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func toSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
