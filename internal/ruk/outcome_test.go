package ruk

import (
	"testing"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

func TestOutcomeTypedTable(t *testing.T) {
	cases := []struct {
		name  string
		facts typedFacts
		want  Outcome
	}{
		{"open never written", typedFacts{}, NotCommitted},
		{"terminal success", typedFacts{Attempted: true, Accepted: true, Terminal: true, Success: true}, Committed},
		{"refusal before accept", typedFacts{Attempted: true, Terminal: true, Refusal: wire.CodeForbidden}, NotCommitted},
		{"refusal after accept predispatch", typedFacts{Attempted: true, Accepted: true, Terminal: true, Refusal: wire.CodeNotFound}, NotCommitted},
		{"refusal after accept limit", typedFacts{Attempted: true, Accepted: true, Terminal: true, Refusal: wire.CodeLimit}, NotCommitted},
		{"refusal after accept other", typedFacts{Attempted: true, Accepted: true, Terminal: true, Refusal: wire.CodeStale}, Unknown},
		{"refusal after accept conflict", typedFacts{Attempted: true, Accepted: true, Terminal: true, Refusal: wire.CodeConflicted}, Unknown},
		{"attempted no terminal", typedFacts{Attempted: true, Accepted: true}, Unknown},
		{"attempted never accepted", typedFacts{Attempted: true}, Unknown},
	}
	for _, tc := range cases {
		if got := outcomeTyped(tc.facts); got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}
