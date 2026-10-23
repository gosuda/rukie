package vm

import "testing"

func TestParseLines(t *testing.T) {
	r, err := ParseLines("12-34")
	if err != nil {
		t.Fatalf("ParseLines = %v", err)
	}
	if r.Start != 12 || r.End != 34 {
		t.Fatalf("range = %+v, want 12-34", r)
	}
	r, err = ParseLines("7")
	if err != nil {
		t.Fatalf("ParseLines = %v", err)
	}
	if r.Start != 7 || r.End != 7 {
		t.Fatalf("range = %+v, want 7-7", r)
	}
	for _, bad := range []string{"", "0", "9-4", "x", "1-2-3", "-5", "L"} {
		if _, badErr := ParseLines(bad); badErr == nil {
			t.Fatalf("ParseLines(%q) = nil, want error", bad)
		}
	}
	r, err = ParseLines("L12-20")
	if err != nil {
		t.Fatalf("ParseLines L-prefix = %v", err)
	}
	if !r.Contains(15) || r.Contains(21) || r.Contains(11) {
		t.Fatalf("Contains wrong for %+v", r)
	}
}
