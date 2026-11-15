// Package tools implements the nine reviewed WinBridge tools, and the read-only Diagnose tool, on top of the desktop interface.
package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/partprogramming09/winbridge/agent/internal/desktop"
	"github.com/partprogramming09/winbridge/agent/internal/fuzzy"
	"github.com/partprogramming09/winbridge/agent/internal/procs"
)

const (
	inputLockWait   = 30 * time.Second
	maxWait         = 300 * time.Second
	maxElements     = 50
	minFocusScore   = 50
	defaultQuality  = 75
	maxTypeChars    = 100_000
	typeSettleDelay = 100 * time.Millisecond
)

// TaskRunner runs the pre-approved tasks; a nil runner means the RunTask tool does not exist.
type TaskRunner interface {
	Names() []string
	Run(ctx context.Context, name string) (string, error)
}

// Env is what the tools run against; Sleep is replaced in tests.
type Env struct {
	Desktop desktop.Desktop
	Tasks   TaskRunner
	Gateway GatewayRunner                     // nil leaves the gateway tools out
	Jobs    JobsRunner                        // nil leaves the job tools out
	Files   FilesRunner                       // nil leaves the file tools out
	Procs   procs.Lister                      // nil leaves the Processes tool out
	Diag    func(logLines int) map[string]any // nil leaves the Diagnose tool out
	Sleep   func(context.Context, time.Duration) error
	input   chan struct{}
}

// NewEnv returns an Env for the given desktop with real sleeping.
func NewEnv(d desktop.Desktop) *Env {
	return &Env{Desktop: d, Sleep: sleep, input: make(chan struct{}, 1)}
}

// sleep waits for d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// result is what one handler produces.
type result struct {
	text       string
	images     [][]byte
	structured any
}

type handler func(ctx context.Context, e *Env, a Args) (result, error)

type spec struct {
	name        string
	title       string
	description string
	schema      map[string]any
	readOnly    bool
	serialized  bool
	run         handler
}

// taskID returns a short random identifier shown in front of every result, as the Python task manager did.
func taskID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Register adds the enabled tools to the server in the profile's order.
func Register(s *mcp.Server, e *Env, enabled []string) {
	want := make(map[string]bool, len(enabled))
	for _, name := range enabled {
		want[name] = true
	}
	for _, sp := range specs() {
		if !want[sp.name] {
			continue
		}
		sp := sp
		no := false
		s.AddTool(&mcp.Tool{
			Name:        sp.name,
			Description: sp.description,
			InputSchema: sp.schema,
			Annotations: &mcp.ToolAnnotations{Title: sp.title, ReadOnlyHint: sp.readOnly, OpenWorldHint: &no, DestructiveHint: boolPtr(false)},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return call(ctx, e, sp, req.Params.Arguments), nil
		})
	}
	if e.Diag != nil {
		sp := diagnoseSpec()
		no := false
		s.AddTool(&mcp.Tool{
			Name:        sp.name,
			Description: sp.description,
			InputSchema: sp.schema,
			Annotations: &mcp.ToolAnnotations{Title: sp.title, ReadOnlyHint: true, OpenWorldHint: &no, DestructiveHint: boolPtr(false)},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return call(ctx, e, sp, req.Params.Arguments), nil
		})
	}
	if e.Gateway != nil && len(e.Gateway.Names()) > 0 {
		registerGateway(s, e)
	}
	if e.Jobs != nil {
		registerJobs(s, e)
	}
	if e.Files != nil {
		registerFiles(s, e)
	}
	if e.Procs != nil {
		registerProcesses(s, e)
	}
	if e.Tasks != nil && len(e.Tasks.Names()) > 0 {
		sp := runTaskSpec(e.Tasks.Names())
		no := false
		s.AddTool(&mcp.Tool{
			Name:        sp.name,
			Description: sp.description,
			InputSchema: sp.schema,
			Annotations: &mcp.ToolAnnotations{Title: sp.title, OpenWorldHint: &no, DestructiveHint: boolPtr(true)},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return call(ctx, e, sp, req.Params.Arguments), nil
		})
	}
}

// diagnoseSpec describes Diagnose: one optional number, nothing else.
func diagnoseSpec() spec {
	return spec{
		name: "Diagnose", title: "Diagnose", readOnly: true, run: diagnose,
		description: "Read-only health report of this WinBridge agent: version, SHA-256 of its executable, process id, " +
			"uptime, memory, whether the Windows session is locked, the result of the last install, and the last log lines " +
			"of the supervisor and the server (secrets are removed). Use it instead of a screenshot to check the machine.",
		schema: object(nil, map[string]any{"log_lines": intProp("Log lines per file, 0 to 200 (default 20).")}),
	}
}

// diagnose returns the report as JSON text and as structured content.
func diagnose(_ context.Context, e *Env, a Args) (result, error) {
	for key := range a {
		if key != "log_lines" {
			return result{}, fmt.Errorf("unexpected parameter %s", key)
		}
	}
	lines, err := a.Int("log_lines", 20)
	if err != nil {
		return result{}, err
	}
	if lines < 0 || lines > 200 {
		return result{}, fmt.Errorf("log_lines must be between 0 and 200, got %d", lines)
	}
	report := e.Diag(lines)
	text, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return result{}, err
	}
	return result{text: string(text), structured: report}, nil
}

// needUnlocked fails when the lock screen or a secure desktop is in front, so an input tool never reports success
// for keys and clicks that reached nothing.
func needUnlocked(e *Env) error {
	if s := e.Desktop.SessionState(); s.Locked {
		return fmt.Errorf("the Windows session is locked (input desktop: %s); the user must unlock it before input can be sent", s.Desktop)
	}
	return nil
}

// needInput fails when mouse or keyboard input cannot reach the desktop: the session is locked, or an administrator
// window is in front (Windows drops input from a less privileged process, with no error of its own).
func needInput(e *Env) error {
	if err := needUnlocked(e); err != nil {
		return err
	}
	if s := e.Desktop.SessionState(); s.ElevatedForeground != "" {
		return fmt.Errorf("an administrator window (%q) is in front and Windows blocks input to it from this agent, which does not run as administrator; close it or bring a normal window to the front with FocusWindow", s.ElevatedForeground)
	}
	return nil
}

// runTaskSpec describes RunTask: one required name out of a fixed list, nothing else.
func runTaskSpec(names []string) spec {
	return spec{
		name: "RunTask", title: "Run task", serialized: true, run: runTask,
		description: "Run one pre-approved task by name. Allowed tasks: " + strings.Join(names, ", ") + ".",
		schema: object([]string{"name"}, map[string]any{
			"name": map[string]any{"type": "string", "enum": names, "description": "Task name."},
		}),
	}
}

// runTask runs the named task and returns its output.
func runTask(ctx context.Context, e *Env, a Args) (result, error) {
	name, err := a.RequireString("name")
	if err != nil {
		return result{}, err
	}
	for key := range a {
		if key != "name" {
			return result{}, fmt.Errorf("unexpected parameter %s: tasks take no arguments", key)
		}
	}
	out, err := e.Tasks.Run(ctx, name)
	if err != nil {
		return result{}, err
	}
	return result{text: fmt.Sprintf("Task %s finished.\n%s", name, out)}, nil
}

func boolPtr(b bool) *bool { return &b }

// call runs one tool, serializing the ones that move the mouse or press keys, and wraps the outcome.
func call(ctx context.Context, e *Env, sp spec, raw []byte) *mcp.CallToolResult {
	id := taskID()
	fail := func(err error) *mcp.CallToolResult {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("[task:%s] %s", id, err)},
		}}
	}
	args, err := parseArgs(raw)
	if err != nil {
		return fail(fmt.Errorf("%s error: %w", sp.name, err))
	}
	if sp.serialized {
		wait := time.NewTimer(inputLockWait)
		defer wait.Stop()
		select {
		case e.input <- struct{}{}:
			defer func() { <-e.input }()
		case <-wait.C:
			return fail(errors.New("Timeout waiting for input lock (another input task is running)"))
		case <-ctx.Done():
			return fail(ctx.Err())
		}
	}
	res, err := sp.run(ctx, e, args)
	if err != nil {
		return fail(fmt.Errorf("%s error: %w", sp.name, err))
	}
	var content []mcp.Content
	for _, img := range res.images {
		content = append(content, &mcp.ImageContent{Data: img, MIMEType: "image/jpeg"})
	}
	content = append(content, &mcp.TextContent{Text: fmt.Sprintf("[task:%s] %s", id, res.text)})
	return &mcp.CallToolResult{Content: content, StructuredContent: res.structured}
}

// object builds an input schema; required lists the mandatory parameters.
func object(required []string, props map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

var (
	intProp  = func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	numProp  = func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	strProp  = func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	boolProp = func(desc string) map[string]any {
		return map[string]any{"type": []string{"boolean", "string"}, "description": desc}
	}
	allowedBt = map[string]bool{"left": true, "right": true, "middle": true}
)

// specs lists the tools in the profile's order.
func specs() []spec {
	return []spec{
		{
			name: "Snapshot", title: "Snapshot", readOnly: true, run: snapshot,
			description: "Capture desktop screenshot, window list, and interactive UI elements.",
			schema: object(nil, map[string]any{
				"use_vision": boolProp("Include screenshot image (default True)."),
				"quality":    intProp("JPEG quality 1-100 (default 75). Lower = smaller."),
				"max_width":  intProp("Max image width in pixels. 0=native resolution (default). Set to e.g. 1920 to downscale."),
				"monitor":    intProp("Monitor to capture. 0=all monitors (default), 1/2/3=specific monitor."),
				"region":     strProp("Capture only this area, as 'x,y,width,height' in screen pixels. Empty=whole capture area."),
			}),
		},
		{
			name: "Click", title: "Click", serialized: true, run: click,
			description: "Mouse click at screen coordinates. Fails if the Windows session is locked.",
			schema: object([]string{"x", "y"}, map[string]any{
				"x": intProp("X coordinate."), "y": intProp("Y coordinate."),
				"button": strProp("'left', 'right', or 'middle'."),
				"action": strProp("'click', 'double', or 'hover'."),
			}),
		},
		{
			name: "Type", title: "Type", serialized: true, run: typeText,
			description: "Type text, optionally at specific coordinates. Fails if the Windows session is locked. Applications process input a moment after this returns: wait a second before reading the screen.",
			schema: object([]string{"text"}, map[string]any{
				"text": strProp("Text to type."),
				"x":    intProp("X coordinate (0 = current position)."), "y": intProp("Y coordinate (0 = current position)."),
				"clear":       boolProp("Clear existing content first (Ctrl+A, Delete)."),
				"method":      strProp("'paste' (default): paste through the clipboard, exact for any character, the previous clipboard text is restored. 'keys': one paced input event per character, for fields that block pasting."),
				"press_enter": boolProp("Press Enter after typing."),
			}),
		},
		{
			name: "Move", title: "Move", serialized: true, run: move,
			description: "Move mouse or drag to position.",
			schema: object([]string{"x", "y"}, map[string]any{
				"x": intProp("Target X."), "y": intProp("Target Y."),
				"drag":    boolProp("If true, drag from start position to target."),
				"start_x": intProp("Drag start X."), "start_y": intProp("Drag start Y."),
				"duration": numProp("Movement duration in seconds."),
			}),
		},
		{
			name: "Scroll", title: "Scroll", serialized: true, run: scroll,
			description: "Scroll at a position. The amount is the raw wheel value: 120 is one notch, so use hundreds or thousands to move a page.",
			schema: object([]string{"amount"}, map[string]any{
				"amount": intProp("Raw wheel units (120 = one notch); positive=up/right, negative=down/left."),
				"x":      intProp("X coordinate (0 = current)."), "y": intProp("Y coordinate (0 = current)."),
				"horizontal": boolProp("Horizontal scroll instead of vertical."),
			}),
		},
		{
			name: "Shortcut", title: "Shortcut", serialized: true, run: shortcut,
			description: "Execute keyboard shortcut. Fails if the Windows session is locked or the key name is unknown.",
			schema: object([]string{"keys"}, map[string]any{
				"keys": strProp("Shortcut string, e.g. 'ctrl+c', 'alt+tab', 'win+e'."),
			}),
		},
		{
			name: "FocusWindow", title: "FocusWindow", serialized: true, run: focusWindow,
			description: "Bring a window to the foreground.",
			schema: object(nil, map[string]any{
				"title":  strProp("Window title (fuzzy matched)."),
				"handle": intProp("Window handle (exact)."),
			}),
		},
		{
			name: "Wait", title: "Wait", readOnly: true, run: wait,
			description: "Pause execution.",
			schema:      object(nil, map[string]any{"seconds": numProp("Seconds to wait.")}),
		},
		{
			name: "GetSystemInfo", title: "GetSystemInfo", readOnly: true, run: systemInfo,
			description: "Get system information: CPU, memory, disk, network, uptime, and whether the session is locked.",
			schema:      object(nil, map[string]any{}),
		},
	}
}

// parseRegion reads "x,y,width,height"; an empty string means the whole capture area.
func parseRegion(s string) (*desktop.Region, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return nil, fmt.Errorf("region must be x,y,width,height, got %q", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("region must be x,y,width,height, got %q", s)
		}
		v[i] = n
	}
	if v[2] <= 0 || v[3] <= 0 {
		return nil, fmt.Errorf("region width and height must be positive, got %q", s)
	}
	return &desktop.Region{X: v[0], Y: v[1], Width: v[2], Height: v[3]}, nil
}

// snapshot returns the screenshot (once) and the text summary of windows and controls.
func snapshot(ctx context.Context, e *Env, a Args) (result, error) {
	quality, err := a.Int("quality", defaultQuality)
	if err != nil {
		return result{}, err
	}
	maxWidth, err := a.Int("max_width", 0)
	if err != nil {
		return result{}, err
	}
	monitor, err := a.Int("monitor", 0)
	if err != nil {
		return result{}, err
	}
	if quality < 1 || quality > 100 {
		return result{}, fmt.Errorf("quality must be between 1 and 100, got %d", quality)
	}
	region, err := parseRegion(a.String("region", ""))
	if err != nil {
		return result{}, err
	}
	var res result
	if a.Bool("use_vision", true) {
		img, err := e.Desktop.Screenshot(desktop.ScreenshotOptions{Quality: quality, MaxWidth: maxWidth, Monitor: monitor, Region: region})
		if err != nil {
			return result{}, err
		}
		res.images = [][]byte{img}
	}
	windows, err := e.Desktop.Windows()
	if err != nil {
		return result{}, err
	}
	var b strings.Builder
	if s := e.Desktop.SessionState(); s.Locked {
		fmt.Fprintf(&b, "**Session: LOCKED** (input desktop: %s). The image shows the lock screen; the window list below is stale and input tools will fail.\n\n", s.Desktop)
	}
	fmt.Fprintf(&b, "**System Language:** %s\n\n**Windows:**", e.Desktop.SystemLanguage())
	for _, w := range windows {
		fmt.Fprintf(&b, "\n  [%d] %s (%dx%d at %d,%d)", w.Handle, w.Title, w.Width(), w.Height(), w.Left, w.Top)
	}
	elements, err := e.Desktop.Elements()
	if err != nil {
		return result{}, err
	}
	if len(elements) > 0 {
		b.WriteString("\n\n**Interactive Elements (foreground window):**")
		for i, el := range elements {
			if i == maxElements {
				break
			}
			label := el.Text
			if label == "" {
				label = el.Class
			}
			fmt.Fprintf(&b, "\n  [%d] %s — center (%d,%d)", el.Index, label, (el.Left+el.Right)/2, (el.Top+el.Bottom)/2)
		}
	}
	res.text = b.String()
	return res, nil
}

// click presses a mouse button at a point, or only hovers.
func click(_ context.Context, e *Env, a Args) (result, error) {
	if err := needInput(e); err != nil {
		return result{}, err
	}
	x, err := a.RequireInt("x")
	if err != nil {
		return result{}, err
	}
	y, err := a.RequireInt("y")
	if err != nil {
		return result{}, err
	}
	button, action := a.String("button", "left"), a.String("action", "click")
	if !allowedBt[button] {
		return result{}, fmt.Errorf("button must be left, right or middle, got %q", button)
	}
	switch action {
	case "click", "double", "hover":
	default:
		return result{}, fmt.Errorf("action must be click, double or hover, got %q", action)
	}
	if err := e.Desktop.Click(x, y, button, action); err != nil {
		return result{}, err
	}
	switch action {
	case "hover":
		return result{text: fmt.Sprintf("Hovered at (%d,%d)", x, y)}, nil
	case "double":
		return result{text: fmt.Sprintf("Double-clicked %s at (%d,%d)", button, x, y)}, nil
	}
	return result{text: fmt.Sprintf("Clicked %s at (%d,%d)", button, x, y)}, nil
}

// typeText types text, optionally after clicking and clearing the field.
func typeText(ctx context.Context, e *Env, a Args) (result, error) {
	if err := needInput(e); err != nil {
		return result{}, err
	}
	text, err := a.RequireString("text")
	if err != nil {
		return result{}, err
	}
	x, err := a.Int("x", 0)
	if err != nil {
		return result{}, err
	}
	y, err := a.Int("y", 0)
	if err != nil {
		return result{}, err
	}
	method := a.String("method", string(desktop.TypePaste))
	if method != string(desktop.TypePaste) && method != string(desktop.TypeKeys) {
		return result{}, fmt.Errorf("method must be paste or keys, got %q", method)
	}
	if n := utf8.RuneCountInString(text); n > maxTypeChars {
		return result{}, fmt.Errorf("text is %d characters; the limit is %d", n, maxTypeChars)
	}
	if x != 0 || y != 0 {
		if err := e.Desktop.Click(x, y, "left", "click"); err != nil {
			return result{}, err
		}
		if err := e.Sleep(ctx, typeSettleDelay); err != nil {
			return result{}, err
		}
	}
	if a.Bool("clear", false) {
		if err := e.Desktop.Hotkey([]string{"ctrl", "a"}); err != nil {
			return result{}, err
		}
		if err := e.Desktop.PressKey("delete"); err != nil {
			return result{}, err
		}
		if err := e.Sleep(ctx, 50*time.Millisecond); err != nil {
			return result{}, err
		}
	}
	if err := e.Desktop.TypeText(text, desktop.TypeMode(method)); err != nil {
		return result{}, err
	}
	if a.Bool("press_enter", false) {
		if err := e.Desktop.PressKey("enter"); err != nil {
			return result{}, err
		}
	}
	return result{text: fmt.Sprintf("Typed %d chars", utf8.RuneCountInString(text))}, nil
}

// move moves the pointer or drags to a point.
func move(_ context.Context, e *Env, a Args) (result, error) {
	if err := needInput(e); err != nil {
		return result{}, err
	}
	x, err := a.RequireInt("x")
	if err != nil {
		return result{}, err
	}
	y, err := a.RequireInt("y")
	if err != nil {
		return result{}, err
	}
	startX, err := a.Int("start_x", 0)
	if err != nil {
		return result{}, err
	}
	startY, err := a.Int("start_y", 0)
	if err != nil {
		return result{}, err
	}
	seconds, err := a.Float("duration", 0.3)
	if err != nil {
		return result{}, err
	}
	if seconds < 0 || seconds > 30 {
		return result{}, fmt.Errorf("duration must be between 0 and 30 seconds, got %v", seconds)
	}
	d := time.Duration(seconds * float64(time.Second))
	if a.Bool("drag", false) {
		if err := e.Desktop.Drag(startX, startY, startX != 0 || startY != 0, x, y, d); err != nil {
			return result{}, err
		}
		return result{text: fmt.Sprintf("Dragged to (%d,%d)", x, y)}, nil
	}
	if err := e.Desktop.MoveTo(x, y, d); err != nil {
		return result{}, err
	}
	return result{text: fmt.Sprintf("Moved to (%d,%d)", x, y)}, nil
}

// scroll turns the wheel, first moving to a point when one is given.
func scroll(_ context.Context, e *Env, a Args) (result, error) {
	if err := needInput(e); err != nil {
		return result{}, err
	}
	amount, err := a.RequireInt("amount")
	if err != nil {
		return result{}, err
	}
	x, err := a.Int("x", 0)
	if err != nil {
		return result{}, err
	}
	y, err := a.Int("y", 0)
	if err != nil {
		return result{}, err
	}
	if x != 0 || y != 0 {
		if err := e.Desktop.MoveTo(x, y, 0); err != nil {
			return result{}, err
		}
	}
	horizontal := a.Bool("horizontal", false)
	if err := e.Desktop.Scroll(amount, horizontal); err != nil {
		return result{}, err
	}
	direction := "vertically"
	if horizontal {
		direction = "horizontally"
	}
	return result{text: fmt.Sprintf("Scrolled %d %s", amount, direction)}, nil
}

// shortcut presses a key combination such as ctrl+c.
func shortcut(_ context.Context, e *Env, a Args) (result, error) {
	if err := needInput(e); err != nil {
		return result{}, err
	}
	keys, err := a.RequireString("keys")
	if err != nil {
		return result{}, err
	}
	var parts []string
	for _, k := range strings.Split(strings.ToLower(keys), "+") {
		if k = strings.TrimSpace(k); k != "" {
			parts = append(parts, k)
		}
	}
	if len(parts) == 0 {
		return result{}, errors.New("keys is empty")
	}
	if err := e.Desktop.Hotkey(parts); err != nil {
		return result{}, err
	}
	return result{text: "Executed shortcut: " + keys}, nil
}

// focusWindow brings a window to the front by handle, or by the best fuzzy title match.
func focusWindow(_ context.Context, e *Env, a Args) (result, error) {
	if err := needUnlocked(e); err != nil {
		return result{}, err
	}
	title := a.String("title", "")
	handle, err := a.Int("handle", 0)
	if err != nil {
		return result{}, err
	}
	target := int64(handle)
	if target == 0 && title != "" {
		windows, err := e.Desktop.Windows()
		if err != nil {
			return result{}, err
		}
		best := 0
		for _, w := range windows {
			if score := fuzzy.PartialRatio(title, w.Title); score > best {
				best, target = score, w.Handle
			}
		}
		if best < minFocusScore {
			return result{text: fmt.Sprintf("No window matching '%s' (best score %d)", title, best)}, nil
		}
	}
	if target == 0 {
		return result{text: "No window found"}, nil
	}
	got, err := e.Desktop.Focus(target)
	if err != nil {
		return result{text: fmt.Sprintf("Failed to focus: %v", err)}, nil
	}
	return result{text: fmt.Sprintf("Focused window handle=%d title='%s'", target, got)}, nil
}

// wait pauses for the given seconds, up to a cap.
func wait(ctx context.Context, e *Env, a Args) (result, error) {
	seconds, err := a.Float("seconds", 1.0)
	if err != nil {
		return result{}, err
	}
	if seconds < 0 || time.Duration(seconds*float64(time.Second)) > maxWait {
		return result{}, fmt.Errorf("seconds must be between 0 and %d, got %v", int(maxWait.Seconds()), seconds)
	}
	if err := e.Sleep(ctx, time.Duration(seconds*float64(time.Second))); err != nil {
		return result{}, err
	}
	return result{text: fmt.Sprintf("Waited %gs", seconds)}, nil
}

// systemInfo reports CPU, memory, disk, network and uptime.
func systemInfo(_ context.Context, e *Env, _ Args) (result, error) {
	text, err := e.Desktop.SystemInfo()
	if err != nil {
		return result{}, err
	}
	s := e.Desktop.SessionState()
	state := "unlocked"
	if s.Locked {
		state = "LOCKED"
	}
	return result{text: fmt.Sprintf("%s\n**Session:** %s (input desktop: %s)", text, state, s.Desktop)}, nil
}
