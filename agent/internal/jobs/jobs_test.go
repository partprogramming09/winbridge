package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const manifestJSON = `{"jobs":{
 "game":{"script":"game.ps1","sha256":"aa","timeout_s":600,"session":1,"description":"open the game","params":{"Api":"^(d3d11|d3d12)$","Dim":"^[0-9]{1,3}$"}},
 "state":{"script":"state.ps1","sha256":"bb","timeout_s":30,"params":{}}}}`

// newStore returns a Store over a fresh job root with the manifest above.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"inbox", "out", "cancel"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

// writeStatus stores a status file the way the runner does.
func writeStatus(t *testing.T, root, id, state string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "job": "game", "state": state})
	if err := os.WriteFile(filepath.Join(root, "out", id+".status.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// inboxFiles lists the request files waiting in the inbox.
func inboxFiles(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "inbox", "*.json"))
	return m
}

func TestAppsListsTheRegistryWithoutPathsOrHashes(t *testing.T) {
	s, root := newStore(t)
	m := `{"jobs":{},"apps":{"notepad":{"path":"C:\\Windows\\notepad.exe","sha256":"aa","args":"^[a-z]+$","window_title":"Notepad","description":"Notepad"},"calc":{"path":"C:\\calc.exe"}}}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := s.Apps()
	if err != nil || len(apps) != 2 || apps[0].Name != "calc" || apps[1].Name != "notepad" {
		t.Fatalf("apps = %+v err = %v", apps, err)
	}
	if apps[1].Args != "^[a-z]+$" || apps[1].Description != "Notepad" {
		t.Fatalf("notepad = %+v", apps[1])
	}
	raw, _ := json.Marshal(apps)
	if strings.Contains(string(raw), "Windows") || strings.Contains(string(raw), `"aa"`) {
		t.Fatalf("the path and the hash are not for the caller: %s", raw)
	}
}

func TestFilesWrittenByWindowsPowerShellWithABOMAreRead(t *testing.T) {
	s, root := newStore(t)
	bom := "\xef\xbb\xbf"
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(bom+manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "out", "b1.status.json"), []byte(bom+`{"id":"b1","job":"game","state":"running"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if list, err := s.List(); err != nil || len(list) != 2 {
		t.Fatalf("list = %v err = %v", list, err)
	}
	if st, err := s.Status("b1", 0, 0); err != nil || st.State != "running" {
		t.Fatalf("st = %+v err = %v", st, err)
	}
}

func TestATruncatedStatusFileStillCountsAsBusy(t *testing.T) {
	s, root := newStore(t)
	if err := os.WriteFile(filepath.Join(root, "out", "t1.status.json"), []byte(`{"id":"t1","sta`), 0o644); err != nil {
		t.Fatal(err)
	}
	var busy *BusyError
	if _, err := s.Submit("state", nil, false); !errors.As(err, &busy) {
		t.Fatalf("a status being written must count as busy: %v", err)
	}
}

func TestConcurrentSubmitsLeaveOnlyOneRequest(t *testing.T) {
	s, root := newStore(t)
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := s.Submit("state", nil, false); done <- err }()
	}
	ok := 0
	for i := 0; i < 8; i++ {
		if err := <-done; err == nil {
			ok++
		}
	}
	if ok != 1 || len(inboxFiles(t, root)) != 1 {
		t.Fatalf("%d submits succeeded, %d requests in the inbox", ok, len(inboxFiles(t, root)))
	}
}

func TestListShowsJobsSortedWithParams(t *testing.T) {
	s, _ := newStore(t)
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "game" || list[1].Name != "state" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Params["Api"] != "^(d3d11|d3d12)$" || list[0].TimeoutS != 600 || list[0].Session != 1 {
		t.Fatalf("game = %+v", list[0])
	}
}

func TestSubmitWritesRequestAndReturnsValidID(t *testing.T) {
	s, root := newStore(t)
	id, err := s.Submit("game", map[string]any{"Api": "d3d11", "Dim": float64(30)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`).MatchString(id) {
		t.Fatalf("id %q is not valid", id)
	}
	raw, err := os.ReadFile(filepath.Join(root, "inbox", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Job   string            `json:"job"`
		Args  map[string]string `json:"args"`
		Queue bool              `json:"queue"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.Job != "game" || req.Args["Api"] != "d3d11" || req.Args["Dim"] != "30" || req.Queue {
		t.Fatalf("request = %s", raw)
	}
}

func TestSubmitRefusesWhatTheManifestDoesNotAllowAndWritesNothing(t *testing.T) {
	s, root := newStore(t)
	cases := []struct {
		name string
		job  string
		args map[string]any
	}{
		{"unknown job", "calc", nil},
		{"unknown param", "game", map[string]any{"Evil": "1"}},
		{"value breaks the regex", "game", map[string]any{"Api": "x; calc.exe"}},
		{"non scalar value", "game", map[string]any{"Dim": []any{"1"}}},
		{"param on a job without params", "state", map[string]any{"A": "1"}},
	}
	for _, c := range cases {
		if _, err := s.Submit(c.job, c.args, false); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
	if got := inboxFiles(t, root); len(got) != 0 {
		t.Fatalf("inbox has %v", got)
	}
}

func TestSubmitWhileBusyReturnsBusyErrorUnlessQueued(t *testing.T) {
	s, root := newStore(t)
	writeStatus(t, root, "run1", "running")
	_, err := s.Submit("state", nil, false)
	var busy *BusyError
	if !errors.As(err, &busy) || len(busy.Active) != 1 || busy.Active[0].ID != "run1" {
		t.Fatalf("err = %v", err)
	}
	id, err := s.Submit("state", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// a second plain submit sees the queued one as busy too
	writeStatus(t, root, "run1", "done")
	if _, err := s.Submit("state", nil, false); !errors.As(err, &busy) || busy.Active[0].ID != id {
		t.Fatalf("queued request must count as busy: %v", err)
	}
}

func TestSubmitCapsQueueDepthAndValueSize(t *testing.T) {
	s, _ := newStore(t)
	for i := 0; i < maxActive; i++ {
		if _, err := s.Submit("state", nil, true); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if _, err := s.Submit("state", nil, true); err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("the queue must have a depth cap: %v", err)
	}
	s2, _ := newStore(t)
	if _, err := s2.Submit("game", map[string]any{"Dim": strings.Repeat("1", maxValueLen+1)}, false); err == nil {
		t.Fatal("an oversized value must be refused")
	}
}

func TestStatusReadsStatusFileQueuedAndUnknown(t *testing.T) {
	s, root := newStore(t)
	if _, err := s.Status("nope", 0, 0); err == nil {
		t.Fatal("unknown id must fail")
	}
	id, _ := s.Submit("state", nil, false)
	st, err := s.Status(id, 0, 0)
	if err != nil || st.State != "queued" {
		t.Fatalf("st = %+v err = %v", st, err)
	}
	writeStatus(t, root, id, "running")
	if st, _ = s.Status(id, 0, 0); st.State != "running" {
		t.Fatalf("st = %+v", st)
	}
}

func TestStatusRejectsIDsThatEscapeTheRoot(t *testing.T) {
	s, _ := newStore(t)
	for _, id := range []string{"..\\manifest", "../manifest", "a/b", "", strings.Repeat("a", 41), "a b"} {
		if _, err := s.Status(id, 0, 0); err == nil {
			t.Errorf("id %q accepted", id)
		}
		if err := s.Cancel(id); err == nil {
			t.Errorf("cancel accepted id %q", id)
		}
	}
}

func TestStatusReturnsLogRangeAndNextOffset(t *testing.T) {
	s, root := newStore(t)
	writeStatus(t, root, "r1", "done")
	if err := os.WriteFile(filepath.Join(root, "out", "r1.log"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status("r1", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if st.Log != "2345" || st.NextOffset != 6 || st.LogSize != 10 {
		t.Fatalf("st = %+v", st)
	}
	st, _ = s.Status("r1", 6, 0)
	if st.Log != "6789" || st.NextOffset != 10 {
		t.Fatalf("st = %+v", st)
	}
	st, _ = s.Status("r1", 99, 0)
	if st.Log != "" || st.NextOffset != 10 {
		t.Fatalf("offset past the end: %+v", st)
	}
}

func TestWaitReturnsWhenTheJobEnds(t *testing.T) {
	s, root := newStore(t)
	s.poll = 5 * time.Millisecond
	writeStatus(t, root, "w1", "running")
	go func() {
		time.Sleep(30 * time.Millisecond)
		writeStatus(t, root, "w1", "done")
	}()
	st, err := s.Wait(context.Background(), "w1", 2*time.Second)
	if err != nil || st.State != "done" {
		t.Fatalf("st = %+v err = %v", st, err)
	}
}

func TestWaitTimesOutWithRunningState(t *testing.T) {
	s, root := newStore(t)
	s.poll = 5 * time.Millisecond
	writeStatus(t, root, "w2", "running")
	st, err := s.Wait(context.Background(), "w2", 40*time.Millisecond)
	if err != nil || st.State != "running" {
		t.Fatalf("st = %+v err = %v", st, err)
	}
}

func TestCancelWritesMarkerOnlyForActiveJobs(t *testing.T) {
	s, root := newStore(t)
	writeStatus(t, root, "c1", "running")
	if err := s.Cancel("c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "cancel", "c1")); err != nil {
		t.Fatal("marker missing")
	}
	writeStatus(t, root, "c2", "done")
	if err := s.Cancel("c2"); err == nil {
		t.Fatal("a finished job cannot be cancelled")
	}
	if err := s.Cancel("ghost"); err == nil {
		t.Fatal("an unknown job cannot be cancelled")
	}
}
