package fuzzy

import "testing"

func TestPartialRatio(t *testing.T) {
	for _, c := range []struct {
		a, b    string
		atLeast int
		atMost  int
	}{
		{"notepad", "Notepad - notes.txt", 100, 100},
		{"NOTEPAD", "notepad", 100, 100},
		{"calc", "Calculadora", 100, 100},
		{"zzzzzzzz", "Calculadora", 0, 30},
		{"", "anything", 0, 0},
		{"", "", 100, 100},
		{"notepd", "Notepad", 70, 99},
	} {
		got := PartialRatio(c.a, c.b)
		if got < c.atLeast || got > c.atMost {
			t.Errorf("PartialRatio(%q, %q) = %d, want %d..%d", c.a, c.b, got, c.atLeast, c.atMost)
		}
	}
	if PartialRatio("abc", "xabcx") != PartialRatio("xabcx", "abc") {
		t.Error("the score must not depend on argument order")
	}
}
