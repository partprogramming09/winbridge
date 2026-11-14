// Package procs lists the processes of the machine, read-only.
package procs

import (
	"sort"
	"strings"
)

// DefaultLimit is how many processes Filter returns when the caller gives no limit.
const DefaultLimit = 200

// Process is one running process.
type Process struct {
	PID     int    `json:"pid"`
	PPID    int    `json:"ppid"`
	Name    string `json:"name"`
	Session int    `json:"session"`
	Path    string `json:"path,omitempty"` // empty when Windows does not let the agent read it
}

// Lister returns every process; List is the real one.
type Lister func() ([]Process, error)

// Filter returns, sorted by pid, up to limit processes whose name contains name (case-insensitive); an empty name matches all.
func Filter(list []Process, name string, limit int) []Process {
	if limit <= 0 {
		limit = DefaultLimit
	}
	name = strings.ToLower(name)
	out := make([]Process, 0, len(list))
	for _, p := range list {
		if name == "" || strings.Contains(strings.ToLower(p.Name), name) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
