// Package jobs is the agent's side of the job runner: it reads the manifest, drops requests in the inbox, and reads
// back the state and log files the elevated runner writes. It never runs anything itself.
package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultLogChunk = 8 << 10
	maxLogChunk     = 40 << 10
	maxErrLog       = 4 << 10
	maxActive       = 20  // queued plus running jobs
	maxValueLen     = 200 // bytes of one parameter value
	maxArgs         = 20
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// Job is one entry of the manifest, as ListJobs shows it.
type Job struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params"` // parameter name to the regex its value must match
	TimeoutS    int               `json:"timeout_s"`
	Session     int               `json:"session"` // 1 is the desktop session, 0 the service session
}

// Status is the state of one job, with a slice of its log.
type Status struct {
	ID         string `json:"id"`
	Job        string `json:"job"`
	State      string `json:"state"` // queued, running, done, failed, timeout, rejected or cancelled
	Started    string `json:"started,omitempty"`
	Finished   string `json:"finished,omitempty"`
	ExitCode   *int   `json:"exit_code"`
	Error      string `json:"error,omitempty"`
	Log        string `json:"log,omitempty"`
	LogSize    int64  `json:"log_size"`
	NextOffset int64  `json:"next_offset"`
	Stderr     string `json:"stderr,omitempty"`
}

// BusyError says a job is already queued or running and the caller did not ask to queue.
type BusyError struct{ Active []Status }

// Error lists the active ids and jobs.
func (e *BusyError) Error() string {
	parts := make([]string, len(e.Active))
	for i, a := range e.Active {
		parts[i] = a.ID + " (" + a.Job + ", " + a.State + ")"
	}
	return "busy: " + strings.Join(parts, ", ") + "; wait for it, cancel it, or submit with queue=true"
}

// Store reads and writes the job root: manifest.json, inbox\, out\ and cancel\.
type Store struct {
	root string
	poll time.Duration
	mu   sync.Mutex // makes "is a job active" and "write the request" one step
}

// New returns a Store over root, which must hold manifest.json.
func New(root string) (*Store, error) {
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}
	return &Store{root: root, poll: 500 * time.Millisecond}, nil
}

// App is one allowed application as ListJobs shows it; its path and hash stay on the laptop.
type App struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Args        string `json:"args,omitempty"` // regex the arguments of app-run must match; empty means none are accepted
}

// Apps returns the applications the app-run job may start, sorted by name.
func (s *Store) Apps() ([]App, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m struct {
		Apps map[string]App `json:"apps"`
	}
	if err := json.Unmarshal(trimBOM(raw), &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	out := make([]App, 0, len(m.Apps))
	for name, a := range m.Apps {
		a.Name = name
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type manifestEntry struct {
	Description string            `json:"description"`
	TimeoutS    int               `json:"timeout_s"`
	Session     int               `json:"session"`
	Params      map[string]string `json:"params"`
}

// manifest reads the jobs the runner allows.
func (s *Store) manifest() (map[string]manifestEntry, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m struct {
		Jobs map[string]manifestEntry `json:"jobs"`
	}
	if err := json.Unmarshal(trimBOM(raw), &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	return m.Jobs, nil
}

// trimBOM drops the UTF-8 byte order mark that Windows PowerShell 5.1 writes in front of JSON files.
func trimBOM(raw []byte) []byte { return bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")) }

// List returns the allowed jobs sorted by name.
func (s *Store) List() ([]Job, error) {
	m, err := s.manifest()
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(m))
	for name, e := range m {
		if e.Params == nil {
			e.Params = map[string]string{}
		}
		out = append(out, Job{Name: name, Description: e.Description, Params: e.Params, TimeoutS: e.TimeoutS, Session: e.Session})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Submit checks the request against the manifest, refuses it when a job is active and queue is false, and writes it
// to the inbox. The runner validates everything again; this check only gives the caller a clear error early.
func (s *Store) Submit(job string, args map[string]any, queue bool) (string, error) {
	m, err := s.manifest()
	if err != nil {
		return "", err
	}
	entry, ok := m[job]
	if !ok {
		return "", fmt.Errorf("job %q is not allowed", job)
	}
	if len(args) > maxArgs {
		return "", fmt.Errorf("at most %d arguments", maxArgs)
	}
	clean := make(map[string]string, len(args))
	for name, v := range args {
		rx, ok := entry.Params[name]
		if !ok {
			return "", fmt.Errorf("parameter %q is not allowed for job %s", name, job)
		}
		val, err := scalar(v)
		if err != nil {
			return "", fmt.Errorf("parameter %s: %w", name, err)
		}
		if len(val) > maxValueLen || strings.HasPrefix(val, "-") {
			return "", fmt.Errorf("value of %s is longer than %d bytes or starts with '-'", name, maxValueLen)
		}
		if re, err := regexp.Compile(`\A(?:` + rx + `)\z`); err == nil && !re.MatchString(val) {
			return "", fmt.Errorf("value of %s does not match %s", name, rx)
		}
		clean[name] = val
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active, err := s.active()
	if err != nil {
		return "", err
	}
	if !queue && len(active) > 0 {
		return "", &BusyError{Active: active}
	}
	if len(active) >= maxActive {
		return "", fmt.Errorf("the queue is full (%d jobs queued or running)", maxActive)
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"job": job, "args": clean, "queue": queue})
	tmp := filepath.Join(s.root, "inbox", id+".tmp")
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(s.root, "inbox", id+".json")); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return id, nil
}

// scalar turns a JSON string, number or boolean into the text the runner passes as a parameter.
func scalar(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	return "", errors.New("must be a string, number or boolean")
}

// newID returns a time-ordered identifier that is safe as a file name.
func newID() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "j" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b), nil
}

func checkID(id string) error {
	if !idPattern.MatchString(id) {
		return errors.New("invalid job id")
	}
	return nil
}

// Status returns the state of job id and up to limit bytes of its log from offset.
func (s *Store) Status(id string, offset int64, limit int) (Status, error) {
	if err := checkID(id); err != nil {
		return Status{}, err
	}
	st, err := s.state(id)
	if err != nil {
		return Status{}, err
	}
	if limit <= 0 {
		limit = defaultLogChunk
	}
	if limit > maxLogChunk {
		limit = maxLogChunk
	}
	st.Log, st.LogSize, st.NextOffset = readRange(filepath.Join(s.root, "out", id+".log"), offset, limit)
	st.Stderr, _, _ = readRange(filepath.Join(s.root, "out", id+".err.log"), 0, maxErrLog)
	return st, nil
}

// state reads out\<id>.status.json, or reports queued when only the request exists.
func (s *Store) state(id string) (Status, error) {
	path := filepath.Join(s.root, "out", id+".status.json")
	for try := 0; try < 3; try++ {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			break
		}
		if err == nil {
			var st Status
			if err = json.Unmarshal(trimBOM(raw), &st); err == nil {
				st.ID = id
				return st, nil
			}
		}
		// the runner may be rewriting the file (or antivirus holds it): read it again
		time.Sleep(20 * time.Millisecond)
		if try == 2 {
			// still unreadable: the job exists, so report it as running rather than as missing or ended
			return Status{ID: id, State: "running", Error: "status file is being written"}, nil
		}
	}
	if req, err := os.ReadFile(filepath.Join(s.root, "inbox", id+".json")); err == nil {
		var r struct {
			Job string `json:"job"`
		}
		_ = json.Unmarshal(req, &r)
		return Status{ID: id, Job: r.Job, State: "queued"}, nil
	}
	return Status{}, fmt.Errorf("no job with id %s", id)
}

// readRange returns up to limit bytes of a file from offset, the file size, and the offset to continue from.
func readRange(path string, offset int64, limit int) (string, int64, int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, 0
	}
	size := info.Size()
	if offset < 0 {
		offset = 0
	}
	if offset >= size {
		return "", size, size
	}
	buf := make([]byte, limit)
	n, _ := f.ReadAt(buf, offset)
	return string(buf[:n]), size, offset + int64(n)
}

func terminal(state string) bool {
	switch state {
	case "done", "failed", "timeout", "rejected", "cancelled":
		return true
	}
	return false
}

// Wait polls job id until it ends or timeout passes, and returns the last state seen.
func (s *Store) Wait(ctx context.Context, id string, timeout time.Duration) (Status, error) {
	if err := checkID(id); err != nil {
		return Status{}, err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	for {
		st, err := s.state(id)
		if err != nil {
			return Status{}, err
		}
		if terminal(st.State) {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-deadline.C:
			return st, nil
		case <-tick.C:
		}
	}
}

// Cancel asks the runner to stop job id by writing a marker it checks; a finished or unknown job is an error.
func (s *Store) Cancel(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	st, err := s.state(id)
	if err != nil {
		return err
	}
	if terminal(st.State) {
		return fmt.Errorf("job %s already ended: %s", id, st.State)
	}
	return os.WriteFile(filepath.Join(s.root, "cancel", id), nil, 0o644)
}

// active returns the jobs that are queued or running.
func (s *Store) active() ([]Status, error) {
	out := []Status{}
	seen := map[string]bool{}
	statuses, _ := filepath.Glob(filepath.Join(s.root, "out", "*.status.json"))
	requests, _ := filepath.Glob(filepath.Join(s.root, "inbox", "*.json"))
	for _, p := range append(statuses, requests...) {
		id := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".status.json"), ".json")
		if seen[id] || checkID(id) != nil {
			continue
		}
		seen[id] = true
		st, err := s.state(id)
		if err == nil && !terminal(st.State) {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Busy returns the jobs that are queued or running.
func (s *Store) Busy() ([]Status, error) { return s.active() }
