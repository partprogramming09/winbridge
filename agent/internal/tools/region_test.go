package tools

import "testing"

func TestParseRegion(t *testing.T) {
	r, err := parseRegion(" 10, 20 ,300,400")
	if err != nil || r == nil || r.X != 10 || r.Y != 20 || r.Width != 300 || r.Height != 400 {
		t.Fatalf("got %+v, %v", r, err)
	}
	if r, err := parseRegion(""); r != nil || err != nil {
		t.Fatalf("empty must mean no region, got %+v, %v", r, err)
	}
	for _, bad := range []string{"1,2,3", "a,b,c,d", "0,0,0,5", "0,0,5,-1", "1,2,3,4,5"} {
		if _, err := parseRegion(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
