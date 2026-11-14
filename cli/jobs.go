package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// toolCaller calls one tool of a host; mcpClient is the real one.
type toolCaller interface {
	callTool(timeout time.Duration, name string, args map[string]any) (string, bool, error)
}

// pause is replaced in tests.
var pause = time.Sleep

// jobStatus is the JSON the JobStatus and WaitJob tools answer with.
type jobStatus struct {
	ID         string `json:"id"`
	Job        string `json:"job"`
	State      string `json:"state"`
	ExitCode   *int   `json:"exit_code"`
	Error      string `json:"error"`
	Log        string `json:"log"`
	LogSize    int64  `json:"log_size"`
	NextOffset int64  `json:"next_offset"`
	Stderr     string `json:"stderr"`
}

// ended reports whether a job state is final.
func (s jobStatus) ended() bool {
	switch s.State {
	case "done", "failed", "timeout", "rejected", "cancelled":
		return true
	}
	return false
}

// call runs a tool; a network error is retried twice for the read-only tools, never for the ones that change state.
func call(c toolCaller, timeout time.Duration, name string, args map[string]any, retry bool) (string, error) {
	attempts := 1
	if retry {
		attempts = 3
	}
	var err error
	for i := 0; i < attempts; i++ {
		var text string
		var isErr bool
		text, isErr, err = c.callTool(timeout, name, args)
		if err == nil {
			if isErr {
				return "", errors.New(text)
			}
			return text, nil
		}
		pause(time.Second)
	}
	return "", err
}

// parseInterspersed parses flags that may come before or after the positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return pos, nil
}

// jobsCommand runs `winbridge jobs SUB ...` against an open session.
func jobsCommand(c toolCaller, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	queue := fs.Bool("queue", false, "queue behind a running job instead of failing with busy")
	offset := fs.Int("offset", 0, "log byte offset for status")
	limit := fs.Int("limit", 0, "log bytes for status")
	wait := fs.Int("timeout", 7200, "seconds `run` waits in total")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New("usage: winbridge jobs list|busy|submit JOB [K=V...]|run JOB [K=V...]|status ID|wait ID|cancel ID")
	}
	sub, rest := pos[0], pos[1:]
	idArg := func() (string, error) {
		if len(rest) != 1 {
			return "", fmt.Errorf("%s needs one job id", sub)
		}
		return rest[0], nil
	}
	switch sub {
	case "list":
		text, err := call(c, 30*time.Second, "ListJobs", nil, true)
		fmt.Fprintln(out, text)
		return err
	case "busy":
		text, err := call(c, 30*time.Second, "ListJobs", nil, true)
		if err != nil {
			return err
		}
		var list struct {
			Active []jobStatus `json:"active"`
		}
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			return err
		}
		if len(list.Active) == 0 {
			fmt.Fprintln(out, "no job is queued or running")
		}
		for _, a := range list.Active {
			fmt.Fprintf(out, "%s\t%s\t%s\n", a.ID, a.Job, a.State)
		}
		return nil
	case "submit", "run":
		if len(rest) == 0 {
			return fmt.Errorf("%s needs a job name", sub)
		}
		id, err := submit(c, rest[0], rest[1:], *queue)
		if err != nil {
			return err
		}
		if sub == "submit" {
			fmt.Fprintln(out, id)
			return nil
		}
		return runToEnd(c, id, time.Duration(*wait)*time.Second, out)
	case "status":
		id, err := idArg()
		if err != nil {
			return err
		}
		text, err := call(c, 30*time.Second, "JobStatus", map[string]any{"id": id, "offset": *offset, "limit": *limit}, true)
		fmt.Fprintln(out, text)
		return err
	case "wait":
		id, err := idArg()
		if err != nil {
			return err
		}
		st, err := waitOnce(c, id, 100)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\t%s\n", st.ID, st.State)
		return nil
	case "cancel":
		id, err := idArg()
		if err != nil {
			return err
		}
		text, err := call(c, 30*time.Second, "CancelJob", map[string]any{"id": id}, false)
		fmt.Fprintln(out, text)
		return err
	}
	return fmt.Errorf("unknown jobs command %q", sub)
}

// submit sends SubmitJob with K=V pairs as the job's arguments and returns the job id.
func submit(c toolCaller, job string, pairs []string, queue bool) (string, error) {
	args := map[string]any{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return "", fmt.Errorf("argument %q is not NAME=value", p)
		}
		args[k] = v
	}
	text, err := call(c, 30*time.Second, "SubmitJob", map[string]any{"job": job, "args": args, "queue": queue}, false)
	if err != nil {
		return "", err
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil || res.ID == "" {
		return "", fmt.Errorf("SubmitJob answered without an id: %s", text)
	}
	return res.ID, nil
}

// waitOnce asks WaitJob to wait up to secs seconds and decodes the status.
func waitOnce(c toolCaller, id string, secs int) (jobStatus, error) {
	text, err := call(c, time.Duration(secs+30)*time.Second, "WaitJob", map[string]any{"id": id, "timeout_s": secs}, true)
	if err != nil {
		return jobStatus{}, err
	}
	var st jobStatus
	return st, json.Unmarshal([]byte(text), &st)
}

// runToEnd waits for a job, then prints its state and the whole log; a job that did not end well is an error.
func runToEnd(c toolCaller, id string, total time.Duration, out io.Writer) error {
	fmt.Fprintf(out, "job %s submitted\n", id)
	deadline := time.Now().Add(total)
	var st jobStatus
	for {
		var err error
		if st, err = waitOnce(c, id, 100); err != nil {
			return fmt.Errorf("job %s: %w (it may still be running: winbridge jobs status %s)", id, err, id)
		}
		if st.ended() {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("job %s is still %s after %s; follow it with: winbridge jobs wait %s", id, st.State, total, id)
		}
	}
	var offset int64
	for {
		text, err := call(c, 30*time.Second, "JobStatus", map[string]any{"id": id, "offset": offset, "limit": 40000}, true)
		if err != nil {
			return err
		}
		var part jobStatus
		if err := json.Unmarshal([]byte(text), &part); err != nil {
			return err
		}
		fmt.Fprint(out, part.Log)
		if part.NextOffset >= part.LogSize || part.NextOffset == offset {
			if part.Stderr != "" {
				fmt.Fprintf(out, "\n[stderr]\n%s\n", part.Stderr)
			}
			break
		}
		offset = part.NextOffset
	}
	code := "-"
	if st.ExitCode != nil {
		code = fmt.Sprint(*st.ExitCode)
	}
	fmt.Fprintf(out, "\njob %s: %s (exit code %s) %s\n", id, st.State, code, st.Error)
	if st.State != "done" {
		return fmt.Errorf("job %s ended %s", id, st.State)
	}
	return nil
}
