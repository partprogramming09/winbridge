// Package diag collects the read-only facts the Diagnose tool returns: what is running, how it is doing, and what the
// installer and the supervisor last logged. It reads only fixed files under the install root.
package diag

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/partprogramming09/winbridge/agent/internal/desktop"
)

const (
	maxLogLines  = 200
	maxLineChars = 300
	tailBytes    = 64 << 10
)

// secretShape matches anything with the shape of an auth key: 32 or more characters from the set the installer accepts
// for keys (the minimum key length is 32). Log lines and error texts are scrubbed with it.
var secretShape = regexp.MustCompile(`[A-Za-z0-9._~\-]{32,}`)

// Options is what the Collector needs from the agent.
type Options struct {
	Version  string
	Upstream string
	Strict   bool
	Host     string
	Port     int
	Tools    []string
	Root     string // install root, for example %LOCALAPPDATA%\winbridge; empty skips files
	Desktop  desktop.Desktop
	Secret   string // the auth key; every occurrence in a returned text is replaced
}

// Collector holds what does not change while the agent runs.
type Collector struct {
	o        Options
	started  time.Time
	exePath  string
	hashOnce sync.Once
	exeHash  string
}

// New returns a Collector that notes the start time and the running executable.
func New(o Options) *Collector {
	c := &Collector{o: o, started: time.Now()}
	if p, err := os.Executable(); err == nil {
		c.exePath = p
	}
	return c
}

// lastInstall returns the recorded install result with its error text scrubbed.
func (c *Collector) lastInstall(path string) any {
	v := readJSON(path)
	if m, ok := v.(map[string]any); ok {
		if msg, ok := m["error"].(string); ok {
			m["error"] = c.scrub(msg)
		}
	}
	return v
}

// Collect returns the diagnostics; logLines is clamped to 0..200.
func (c *Collector) Collect(logLines int) map[string]any {
	if logLines < 0 {
		logLines = 0
	}
	if logLines > maxLogLines {
		logLines = maxLogLines
	}
	c.hashOnce.Do(func() {
		if c.exePath != "" {
			c.exeHash = fileSHA256(c.exePath)
		}
	})
	session := c.o.Desktop.SessionState()
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	mem := processMemory()
	mem["go_heap_bytes"] = heap.HeapAlloc
	out := map[string]any{
		"version":        c.o.Version,
		"upstream":       c.o.Upstream,
		"strict":         c.o.Strict,
		"host":           c.o.Host,
		"port":           c.o.Port,
		"tools":          c.o.Tools,
		"pid":            os.Getpid(),
		"exe_path":       c.exePath,
		"exe_sha256":     c.exeHash,
		"started_at":     c.started.Format(time.RFC3339),
		"uptime_seconds": int(time.Since(c.started).Seconds()),
		"memory":         mem,
		"goroutines":     runtime.NumGoroutine(),
		"session":        map[string]any{"locked": session.Locked, "input_desktop": session.Desktop, "elevated_foreground": session.ElevatedForeground, "modifiers_held": c.o.Desktop.HeldModifiers()},
	}
	if c.o.Root != "" {
		out["last_install"] = c.lastInstall(filepath.Join(c.o.Root, "state", "last-install.json"))
		out["agent_selected"] = readTrim(filepath.Join(c.o.Root, "state", "agent.txt"))
		if logLines > 0 {
			out["logs"] = map[string]any{
				"supervisor": c.tail(filepath.Join(c.o.Root, "logs", "supervisor.log"), logLines),
				"server_err": c.tail(filepath.Join(c.o.Root, "logs", "server.err.log"), logLines),
			}
		}
	}
	return out
}

// fileSHA256 returns the lower-case SHA-256 of a file, or "" when it cannot be read.
func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readJSON returns the decoded file, or a map with an "error" key when it is missing or invalid.
func readJSON(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{"error": "not available"}
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return map[string]any{"error": "not valid JSON"}
	}
	return v
}

// readTrim returns the trimmed text of a small file, or "" when it cannot be read.
func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64 {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// tail returns the last n lines of the last 64 KB of a file, scrubbed and cut to a safe length.
func (c *Collector) tail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > tailBytes {
		if _, err := f.Seek(info.Size()-tailBytes, io.SeekStart); err == nil {
			r := bufio.NewReader(f)
			_, _ = r.ReadString('\n') // drop the partial first line
			return c.lastLines(r, n)
		}
	}
	return c.lastLines(f, n)
}

// lastLines returns the last n scrubbed lines read from r.
func (c *Collector) lastLines(r io.Reader, n int) []string {
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := c.scrub(sc.Text())
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, line)
	}
	return ring
}

// scrub hides the exact key and anything key-shaped, and cuts the text to a safe length.
func (c *Collector) scrub(line string) string {
	if c.o.Secret != "" {
		line = strings.ReplaceAll(line, c.o.Secret, "[redacted]")
	}
	line = secretShape.ReplaceAllString(line, "[redacted]")
	if len(line) > maxLineChars {
		line = line[:maxLineChars] + "..."
	}
	return line
}
