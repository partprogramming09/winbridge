package procs

import "testing"

var sample = []Process{
	{PID: 4, Name: "System"},
	{PID: 100, PPID: 4, Name: "Notepad.exe", Session: 1, Path: `C:\Windows\System32\notepad.exe`},
	{PID: 101, PPID: 4, Name: "notepad++.exe", Session: 1},
	{PID: 200, PPID: 100, Name: "chrome.exe", Session: 1},
}

func TestFilterMatchesNameCaseInsensitivelyAndSortsByPID(t *testing.T) {
	got := Filter(sample, "NOTEPAD", 100)
	if len(got) != 2 || got[0].PID != 100 || got[1].PID != 101 {
		t.Fatalf("got %+v", got)
	}
}

func TestFilterWithoutNameReturnsEverythingUpToTheLimit(t *testing.T) {
	if got := Filter(sample, "", 3); len(got) != 3 || got[2].PID != 101 {
		t.Fatalf("got %+v", got)
	}
	if got := Filter(sample, "", 0); len(got) != len(sample) {
		t.Fatalf("a zero limit means the default, got %d", len(got))
	}
}

func TestFilterDoesNotChangeTheInput(t *testing.T) {
	in := []Process{{PID: 9, Name: "b"}, {PID: 1, Name: "a"}}
	Filter(in, "", 10)
	if in[0].PID != 9 {
		t.Fatal("the input slice was reordered")
	}
}
