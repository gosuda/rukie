package review

import (
	"testing"
)

func TestRemapLineInsideSpan(t *testing.T) {
	spans := []Span{{OldStart: 10, NewStart: 20, Lines: 5}}
	for line, want := range map[uint32]uint32{10: 20, 12: 22, 14: 24} {
		got, ok := RemapLine(line, spans)
		if !ok || got != want {
			t.Fatalf("RemapLine(%d) = (%d, %v), want (%d, true)", line, got, ok, want)
		}
	}
}

func TestRemapLineSpanBoundaries(t *testing.T) {
	spans := []Span{{OldStart: 10, NewStart: 20, Lines: 5}}
	if _, ok := RemapLine(9, spans); ok {
		t.Fatal("line before the span maps")
	}
	if _, ok := RemapLine(15, spans); ok {
		t.Fatal("line past the span end maps")
	}
	if _, ok := RemapLine(0, spans); ok {
		t.Fatal("line zero maps")
	}
}

func TestRemapLineGapAndSecondSpan(t *testing.T) {
	spans := []Span{
		{OldStart: 1, NewStart: 1, Lines: 3},
		{OldStart: 10, NewStart: 30, Lines: 2},
	}
	if _, ok := RemapLine(5, spans); ok {
		t.Fatal("line in the edited gap maps")
	}
	got, ok := RemapLine(11, spans)
	if !ok || got != 31 {
		t.Fatalf("RemapLine(11) = (%d, %v), want (31, true)", got, ok)
	}
}

func TestRemapLineEmptySpanSkipped(t *testing.T) {
	spans := []Span{
		{OldStart: 1, NewStart: 1, Lines: 0},
		{OldStart: 4, NewStart: 9, Lines: 2},
	}
	if _, ok := RemapLine(1, spans); ok {
		t.Fatal("empty span maps a line")
	}
	got, ok := RemapLine(5, spans)
	if !ok || got != 10 {
		t.Fatalf("RemapLine(5) = (%d, %v), want (10, true)", got, ok)
	}
}

func TestRemapLineNoSpans(t *testing.T) {
	if _, ok := RemapLine(7, nil); ok {
		t.Fatal("line maps with no spans")
	}
}
