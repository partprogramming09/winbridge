package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/jobs"
)

const (
	defaultWaitJob = 60
	maxWaitJob     = 120
)

// JobsRunner reaches the elevated job runner through its folder; a nil runner means the job tools do not exist.
type JobsRunner interface {
	List() ([]jobs.Job, error)
	Submit(job string, args map[string]any, queue bool) (string, error)
	Status(id string, offset int64, limit int) (jobs.Status, error)
	Wait(ctx context.Context, id string, timeout time.Duration) (jobs.Status, error)
	Cancel(id string) error
	Busy() ([]jobs.Status, error)
	Apps() ([]jobs.App, error)
}

// JobToolNames are the tools registered when the jobs folder is configured.
var JobToolNames = []string{"ListJobs", "SubmitJob", "JobStatus", "WaitJob", "CancelJob"}

// registerJobs adds the five job tools. They never touch the desktop, so a locked session does not stop them.
func registerJobs(s *mcp.Server, e *Env) {
	no := false
	add := func(name, title, desc string, readOnly bool, schema map[string]any, run func(ctx context.Context, a Args) (any, error)) {
		s.AddTool(&mcp.Tool{
			Name: name, Description: desc, InputSchema: schema,
			Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: readOnly, OpenWorldHint: &no, DestructiveHint: boolPtr(false)},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a, err := parseArgs(req.Params.Arguments)
			if err == nil {
				var out any
				if out, err = run(ctx, a); err == nil {
					raw, _ := json.MarshalIndent(out, "", " ")
					return text(string(raw), false), nil
				}
			}
			return text(name+" error: "+err.Error(), true), nil
		})
	}
	idSchema := func(extra map[string]any) map[string]any {
		props := map[string]any{"id": map[string]any{"type": "string", "description": "Job id from SubmitJob."}}
		for k, v := range extra {
			props[k] = v
		}
		return object([]string{"id"}, props)
	}
	only := func(a Args, allowed ...string) error {
	next:
		for key := range a {
			for _, k := range allowed {
				if key == k {
					continue next
				}
			}
			return errors.New("unexpected parameter " + key)
		}
		return nil
	}

	add("ListJobs", "List jobs",
		"List the jobs the runner allows, with each parameter's regex, the time limit in seconds and the session (1 desktop, 0 service), the jobs now queued or running, and the applications the app-run job may start (use SubmitJob job=app-run with App and optional AppArgs).",
		true, object(nil, map[string]any{}),
		func(ctx context.Context, a Args) (any, error) {
			if err := only(a); err != nil {
				return nil, err
			}
			list, err := e.Jobs.List()
			if err != nil {
				return nil, err
			}
			busy, err := e.Jobs.Busy()
			if err != nil {
				return nil, err
			}
			apps, err := e.Jobs.Apps()
			return map[string]any{"jobs": list, "active": busy, "apps": apps}, err
		})
	add("SubmitJob", "Submit job",
		"Start an allowed job on the laptop and return its id. Parameters are values for the job's named parameters (see ListJobs), never a command line. If another job is queued or running the call fails with busy and the ids, unless queue is true. Use WaitJob or JobStatus afterwards.",
		false, object([]string{"job"}, map[string]any{
			"job":   map[string]any{"type": "string", "description": "Job name from ListJobs."},
			"args":  map[string]any{"type": "object", "description": "Values for the job's parameters, by name."},
			"queue": map[string]any{"type": "boolean", "description": "Queue behind the active job instead of failing with busy (default false)."},
		}),
		func(ctx context.Context, a Args) (any, error) {
			if err := only(a, "job", "args", "queue"); err != nil {
				return nil, err
			}
			job, err := a.RequireString("job")
			if err != nil {
				return nil, err
			}
			var args map[string]any
			if raw, ok := a["args"]; ok && raw != nil {
				if args, ok = raw.(map[string]any); !ok {
					return nil, errors.New("args must be an object")
				}
			}
			id, err := e.Jobs.Submit(job, args, a.Bool("queue", false))
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "job": job, "state": "queued"}, nil
		})
	add("JobStatus", "Job status",
		"State of one job (queued, running, done, failed, timeout, rejected, cancelled), exit code, error and a slice of its log. Read a long log in parts: pass next_offset from the previous answer as offset.",
		true, idSchema(map[string]any{
			"offset": map[string]any{"type": "integer", "description": "Byte offset in the log (default 0)."},
			"limit":  map[string]any{"type": "integer", "description": "Bytes to return, up to 40000 (default 8000)."},
		}),
		func(ctx context.Context, a Args) (any, error) {
			if err := only(a, "id", "offset", "limit"); err != nil {
				return nil, err
			}
			id, err := a.RequireString("id")
			if err != nil {
				return nil, err
			}
			offset, err := a.Int("offset", 0)
			if err != nil {
				return nil, err
			}
			limit, err := a.Int("limit", 0)
			if err != nil {
				return nil, err
			}
			return e.Jobs.Status(id, int64(offset), limit)
		})
	add("WaitJob", "Wait for job",
		"Wait up to timeout_s seconds (default 60, at most 120) for a job to end. Returns its final state, or running with the id if it is still going: call again. Same fields as JobStatus.",
		true, idSchema(map[string]any{
			"timeout_s": map[string]any{"type": "integer", "description": "Seconds to wait, 1 to 120 (default 60)."},
		}),
		func(ctx context.Context, a Args) (any, error) {
			if err := only(a, "id", "timeout_s"); err != nil {
				return nil, err
			}
			id, err := a.RequireString("id")
			if err != nil {
				return nil, err
			}
			secs, err := a.Int("timeout_s", defaultWaitJob)
			if err != nil {
				return nil, err
			}
			if secs < 1 || secs > maxWaitJob {
				return nil, errors.New("timeout_s must be between 1 and 120")
			}
			if _, err := e.Jobs.Wait(ctx, id, time.Duration(secs)*time.Second); err != nil {
				return nil, err
			}
			return e.Jobs.Status(id, 0, 0)
		})
	add("CancelJob", "Cancel job",
		"Ask the runner to stop a queued or running job and everything it started. The state becomes cancelled; check it with JobStatus.",
		false, idSchema(nil),
		func(ctx context.Context, a Args) (any, error) {
			if err := only(a, "id"); err != nil {
				return nil, err
			}
			id, err := a.RequireString("id")
			if err != nil {
				return nil, err
			}
			if err := e.Jobs.Cancel(id); err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "requested": "cancel"}, nil
		})
}
