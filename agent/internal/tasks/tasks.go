// Package tasks runs the pre-approved scripts behind the RunTask tool: fixed names, each script pinned by SHA-256,
// no arguments from the caller.
package tasks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxOutput = 16 << 10
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

	// DefaultShell reads the verified script from standard input, so the file cannot change after the hash check.
	DefaultShell = []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "-"}
)

// Spec is one allowed task: the script file and the SHA-256 it must have.
type Spec struct {
	Script string
	SHA256 string
}

// Runner runs the allowed tasks.
type Runner struct {
	specs map[string]Spec
	shell []string
}

// New validates the specs and returns a Runner; shell is the interpreter command that reads the script on stdin.
func New(specs map[string]Spec, shell []string) (*Runner, error) {
	if len(shell) == 0 {
		return nil, errors.New("tasks: no interpreter command")
	}
	for name, s := range specs {
		switch {
		case !namePattern.MatchString(name):
			return nil, fmt.Errorf("tasks: %q is not a valid task name (lowercase letters, digits and dashes, up to 32)", name)
		case !filepath.IsAbs(s.Script):
			return nil, fmt.Errorf("tasks: %s: the script path must be absolute", name)
		case !shaPattern.MatchString(s.SHA256):
			return nil, fmt.Errorf("tasks: %s: sha256 must be 64 hex characters", name)
		}
	}
	return &Runner{specs: specs, shell: shell}, nil
}

// Names returns the allowed task names, sorted.
func (r *Runner) Names() []string {
	names := make([]string, 0, len(r.specs))
	for name := range r.specs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Run executes the named task and returns its combined output. The script is refused unless its SHA-256 matches.
func (r *Runner) Run(ctx context.Context, name string) (string, error) {
	spec, ok := r.specs[name]
	if !ok {
		return "", fmt.Errorf("unknown task %q; allowed: %s", name, strings.Join(r.Names(), ", "))
	}
	body, err := os.ReadFile(spec.Script)
	if err != nil {
		return "", fmt.Errorf("task %s: cannot read the script: %w", name, err)
	}
	sum := sha256.Sum256(body)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(spec.SHA256))) != 1 {
		return "", fmt.Errorf("task %s: the script does not match its pinned SHA-256; refusing to run it", name)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.shell[0], r.shell[1:]...)
	cmd.Cancel = func() error { return KillTree(cmd.Process) }
	cmd.WaitDelay = waitDelay
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = ScrubbedEnv(os.Environ())
	out := &capped{}
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	text := out.String()
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("task %s timed out after %s\n%s", name, timeout, text)
	case err != nil:
		return "", fmt.Errorf("task %s failed: %w\n%s", name, err, text)
	}
	return text, nil
}

// ScrubbedEnv drops the agent's own secrets and settings so a task never sees the auth key.
func ScrubbedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		upper := strings.ToUpper(kv)
		if strings.HasPrefix(upper, "WINREMOTE_") || strings.HasPrefix(upper, "WINBRIDGE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// capped keeps the first maxOutput bytes written to it and notes when it dropped the rest.
type capped struct {
	buf     bytes.Buffer
	dropped bool
}

// Write stores what fits and always reports the whole write as consumed.
func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.dropped = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.dropped = true
	}
	return len(p), nil
}

// String returns the captured output, with a marker when it was cut.
func (c *capped) String() string {
	if c.dropped {
		return c.buf.String() + "\n[output truncated]"
	}
	return c.buf.String()
}

// timeout is how long a task may run.
var timeout = 60 * time.Second

// waitDelay is how long Run waits for the output pipes after the shell was killed, so a grandchild that keeps them
// open cannot hold the call (and the input lock) past the timeout.
var waitDelay = 5 * time.Second

// KillTree ends the process and, on Windows, every process it started.
func KillTree(p *os.Process) error {
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(p.Pid), "/T", "/F").Run()
	}
	return p.Kill()
}
