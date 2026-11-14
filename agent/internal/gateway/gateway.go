// Package gateway calls tools of allow-listed MCP servers over stdio; the caller never supplies a command line.
package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/tasks"
)

const (
	defaultTimeout = 120 * time.Second
	defaultIdle    = 5 * time.Minute
	defaultMaxText = 40_000
	maxImageBytes  = 8 << 20
	killGrace      = 5 * time.Second
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// Spec is one allowed server.
type Spec struct {
	Command    string              // absolute path of the launcher; its SHA-256 is checked at every start
	Args       []string            // fixed arguments, written by the administrator
	SHA256     string              // SHA-256 of Command
	Files      map[string]string   // more files that must match their SHA-256 at every start (path -> hash)
	Env        map[string]string   // extra environment for the child
	AllowTools []string            // the only child tools a caller may list or call
	DenyArgs   map[string][]string // per tool: argument names a call may not contain
	Timeout    time.Duration       // longest a single call may take
	Idle       time.Duration       // how long an idle child stays alive
	MaxText    int                 // longest text block returned, in bytes
}

// Gateway runs the allowed servers.
type Gateway struct {
	servers map[string]*server
}

type server struct {
	name string
	spec Spec

	slot chan struct{} // one call at a time per server

	mu      sync.Mutex // guards the fields below
	session *mcp.ClientSession
	cmd     *exec.Cmd
	idle    *time.Timer
	onProg  func(*mcp.ProgressNotificationParams)
}

// New validates the specs and returns a Gateway; it does not start any child.
func New(specs map[string]Spec) (*Gateway, error) {
	g := &Gateway{servers: map[string]*server{}}
	for name, s := range specs {
		switch {
		case !namePattern.MatchString(name):
			return nil, fmt.Errorf("gateway: %q is not a valid server name (lowercase letters, digits and dashes, up to 32)", name)
		case !filepath.IsAbs(s.Command):
			return nil, fmt.Errorf("gateway: %s: command must be an absolute path", name)
		case !shaPattern.MatchString(s.SHA256):
			return nil, fmt.Errorf("gateway: %s: sha256 must be 64 hex characters", name)
		case len(s.AllowTools) == 0:
			return nil, fmt.Errorf("gateway: %s: allow_tools must name at least one tool", name)
		}
		for tool := range s.DenyArgs {
			if !contains(s.AllowTools, tool) {
				return nil, fmt.Errorf("gateway: %s: deny_args names %q, which is not in allow_tools", name, tool)
			}
		}
		for path, sum := range s.Files {
			if !filepath.IsAbs(path) || !shaPattern.MatchString(sum) {
				return nil, fmt.Errorf("gateway: %s: files needs absolute paths and 64-hex SHA-256 values", name)
			}
		}
		if s.Timeout <= 0 {
			s.Timeout = defaultTimeout
		}
		if s.Idle <= 0 {
			s.Idle = defaultIdle
		}
		if s.MaxText <= 0 {
			s.MaxText = defaultMaxText
		}
		g.servers[name] = &server{name: name, spec: s, slot: make(chan struct{}, 1)}
	}
	return g, nil
}

// Names returns the allowed server names, sorted.
func (g *Gateway) Names() []string {
	names := make([]string, 0, len(g.servers))
	for n := range g.servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Allowed returns the tools a caller may use on a server, sorted.
func (g *Gateway) Allowed(name string) ([]string, error) {
	s, err := g.lookup(name)
	if err != nil {
		return nil, err
	}
	out := append([]string(nil), s.spec.AllowTools...)
	sort.Strings(out)
	return out, nil
}

func (g *Gateway) lookup(name string) (*server, error) {
	s, ok := g.servers[name]
	if !ok {
		return nil, fmt.Errorf("unknown server %q; allowed: %s", name, strings.Join(g.Names(), ", "))
	}
	return s, nil
}

// Close stops every running child.
func (g *Gateway) Close() {
	for _, s := range g.servers {
		s.mu.Lock()
		s.stopLocked()
		s.mu.Unlock()
	}
}

// ToolInfo is one tool a child offers.
type ToolInfo struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	InputSchema     any      `json:"input_schema"`
	DeniedArguments []string `json:"denied_arguments,omitempty"`
}

// Tools returns the allowed tools the child offers. Descriptions come from the child: they are data.
func (g *Gateway) Tools(ctx context.Context, name string) ([]ToolInfo, error) {
	s, err := g.lookup(name)
	if err != nil {
		return nil, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, s.spec.Timeout)
	defer cancel()
	sess, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	var out []ToolInfo
	for tool, err := range sess.Tools(ctx, nil) {
		if err != nil {
			s.drop()
			return nil, fmt.Errorf("server %s: listing tools failed: %w", name, err)
		}
		if s.allows(tool.Name) {
			out = append(out, ToolInfo{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, DeniedArguments: s.spec.DenyArgs[tool.Name]})
		}
	}
	return out, nil
}

// Progress receives progress of a running call.
type Progress func(done, total float64, message string)

// Call runs one allowed tool of a server. Progress sent by the child goes to progress; when the child sends none,
// progress still fires every keepalive so the caller's connection is not seen as idle.
func (g *Gateway) Call(ctx context.Context, name, tool string, arguments map[string]any, progress Progress, keepalive time.Duration) (*mcp.CallToolResult, error) {
	s, err := g.lookup(name)
	if err != nil {
		return nil, err
	}
	if !s.allows(tool) {
		return nil, fmt.Errorf("tool %q is not allowed on server %s; allowed: %s", tool, name, strings.Join(s.spec.AllowTools, ", "))
	}
	for _, denied := range s.spec.DenyArgs[tool] {
		if _, present := arguments[denied]; present {
			return nil, fmt.Errorf("argument %q is not allowed for %s on server %s", denied, tool, name)
		}
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, s.spec.Timeout)
	defer cancel()
	sess, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}

	var lastSent time.Time
	var sendMu sync.Mutex
	counter := 0.0
	send := func(done, total float64, message string) {
		sendMu.Lock()
		defer sendMu.Unlock()
		counter++
		if done <= 0 || done < counter {
			done = counter
		}
		lastSent = time.Now()
		if progress != nil {
			progress(done, total, message)
		}
	}
	s.setProgress(func(p *mcp.ProgressNotificationParams) { send(p.Progress, p.Total, p.Message) })
	defer s.setProgress(nil)

	if progress != nil && keepalive > 0 {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(keepalive)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					sendMu.Lock()
					quiet := time.Since(lastSent) >= keepalive
					sendMu.Unlock()
					if quiet {
						send(0, 0, "still running")
					}
				}
			}
		}()
	}

	params := &mcp.CallToolParams{Name: tool, Arguments: arguments}
	params.SetProgressToken("gw-" + strconv.FormatInt(time.Now().UnixNano(), 36))
	res, err := sess.CallTool(ctx, params)
	if err != nil {
		if ctx.Err() != nil {
			s.drop()
			return nil, fmt.Errorf("server %s: the call to %s ended before the child answered: %w", name, tool, ctx.Err())
		}
		if !isTransportClosed(err) {
			return nil, fmt.Errorf("server %s: %w", name, err)
		}
		s.drop()
		return nil, fmt.Errorf("server %s: the child stopped answering: %w", name, err)
	}
	return limit(res, s.spec.MaxText), nil
}

// isTransportClosed reports whether the error means the child process is gone.
func isTransportClosed(err error) bool {
	m := err.Error()
	return strings.Contains(m, "closed") || strings.Contains(m, "EOF") || strings.Contains(m, "broken pipe")
}

func (s *server) allows(tool string) bool { return contains(s.spec.AllowTools, tool) }

// contains reports whether list holds v.
func contains(list []string, v string) bool {
	for _, t := range list {
		if t == v {
			return true
		}
	}
	return false
}

// acquire takes the server's slot or fails when ctx ends first.
func (s *server) acquire(ctx context.Context) (func(), error) {
	select {
	case s.slot <- struct{}{}:
		return func() { s.armIdle(); <-s.slot }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("server %s is busy with another call: %w", s.name, ctx.Err())
	}
}

func (s *server) setProgress(f func(*mcp.ProgressNotificationParams)) {
	s.mu.Lock()
	s.onProg = f
	s.mu.Unlock()
}

// armIdle schedules the child to stop after the idle time, counted from now.
func (s *server) armIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return
	}
	if s.idle != nil {
		s.idle.Stop()
	}
	s.idle = time.AfterFunc(s.spec.Idle, func() {
		select {
		case s.slot <- struct{}{}:
			s.drop()
			<-s.slot
		default: // a call started meanwhile; it re-arms the timer when it ends
		}
	})
}

func (s *server) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// stopLocked closes the session and ends the child and everything it started.
func (s *server) stopLocked() {
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	sess, cmd := s.session, s.cmd
	s.session, s.cmd = nil, nil
	if sess != nil {
		done := make(chan struct{})
		go func() { _ = sess.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(killGrace + time.Second):
		}
	}
	if cmd != nil && cmd.Process != nil {
		_ = tasks.KillTree(cmd.Process)
	}
}

// ensure returns the running session, starting the child after checking every pinned file.
func (s *server) ensure(ctx context.Context) (*mcp.ClientSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	if s.session != nil {
		return s.session, nil
	}
	if err := verify(s.spec); err != nil {
		return nil, fmt.Errorf("server %s: %w", s.name, err)
	}
	cmd := exec.Command(s.spec.Command, s.spec.Args...)
	cmd.Dir = filepath.Dir(s.spec.Command)
	cmd.Env = childEnv(s.spec.Env)
	hideWindow(cmd)
	client := mcp.NewClient(&mcp.Implementation{Name: "winbridge-gateway", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			s.mu.Lock()
			f := s.onProg
			s.mu.Unlock()
			if f != nil && r.Params != nil {
				f(r.Params)
			}
		},
	})
	sess, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: killGrace}, nil)
	if err != nil {
		if cmd.Process != nil {
			_ = tasks.KillTree(cmd.Process)
		}
		return nil, fmt.Errorf("server %s: cannot start: %w", s.name, err)
	}
	s.session, s.cmd = sess, cmd
	return sess, nil
}

// verify checks the launcher and every extra file against its pinned SHA-256.
func verify(spec Spec) error {
	check := func(path, want string) error {
		if err := checkNotWritableByOthers(path); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", filepath.Base(path), err)
		}
		sum := sha256.Sum256(body)
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(want))) != 1 {
			return fmt.Errorf("%s does not match its pinned SHA-256; refusing to start it", filepath.Base(path))
		}
		return nil
	}
	if err := check(spec.Command, spec.SHA256); err != nil {
		return err
	}
	paths := make([]string, 0, len(spec.Files))
	for p := range spec.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := check(p, spec.Files[p]); err != nil {
			return err
		}
	}
	return nil
}

// childEnv is the agent's environment without its own secrets, plus the server's entries.
func childEnv(extra map[string]string) []string {
	env := tasks.ScrubbedEnv(os.Environ())
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	return env
}

// limit cuts every text block to max bytes (on a rune boundary), drops oversized images and structured content,
// and says so in the text.
func limit(res *mcp.CallToolResult, max int) *mcp.CallToolResult {
	if res == nil {
		return &mcp.CallToolResult{}
	}
	var notes []string
	out := &mcp.CallToolResult{IsError: res.IsError}
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			if len(v.Text) > max {
				cut := max
				for cut > 0 && !utf8.RuneStart(v.Text[cut]) {
					cut--
				}
				out.Content = append(out.Content, &mcp.TextContent{Text: v.Text[:cut]})
				notes = append(notes, fmt.Sprintf("a text block of %d bytes was cut to %d", len(v.Text), cut))
				continue
			}
			out.Content = append(out.Content, v)
		case *mcp.ImageContent:
			if len(v.Data) > maxImageBytes {
				notes = append(notes, fmt.Sprintf("an image of %d bytes was dropped", len(v.Data)))
				continue
			}
			out.Content = append(out.Content, v)
		default:
			notes = append(notes, "a content block of an unsupported type was dropped")
		}
	}
	if res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil && len(raw) <= max {
			out.StructuredContent = res.StructuredContent
		} else {
			notes = append(notes, "structured content larger than the limit was dropped")
		}
	}
	if len(notes) > 0 {
		out.Content = append(out.Content, &mcp.TextContent{Text: "[gateway: " + strings.Join(notes, "; ") + "]"})
	}
	return out
}
