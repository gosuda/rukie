package mirror

import (
	"errors"
	"testing"
)

func TestComputePlan(t *testing.T) {
	cases := []struct {
		name          string
		local, remote map[string]string
		m             Mirror
		def           string
		wantErr       bool
		confirm       bool
	}{
		{"exact prune hidden untouched", map[string]string{"refs/heads/main": "new", "refs/tags/v1": "t", "refs/pull/1/head": "x"}, map[string]string{"refs/heads/main": "old", "refs/tags/old": "oldtag", "refs/pull/2/head": "hidden"}, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, "", true, false},
		{"ff only", map[string]string{"refs/heads/main": "b"}, map[string]string{"refs/heads/main": "a", "refs/tags/old": "x"}, Mirror{Mode: ModeFastForward, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, "", false, false},
		{"globs", map[string]string{"refs/heads/main": "1", "refs/tags/v1": "2"}, nil, Mirror{Mode: ModeExact, RefSelection: RefSelectionGlobs, Globs: []string{"refs/heads/*"}, AdoptExistingRefs: true}, "", false, false},
		{"reconcile", map[string]string{"refs/heads/main": "x"}, map[string]string{"refs/heads/main": "x"}, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, "", false, false},
		{"adopt denied", map[string]string{"refs/heads/main": "n"}, map[string]string{"refs/heads/main": "o"}, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll}, "", true, false},
		{"adopt confirmed", map[string]string{"refs/heads/main": "n"}, map[string]string{"refs/heads/main": "o"}, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll}, "", false, true},
		{"D F", map[string]string{"refs/heads/a/b": "new"}, map[string]string{"refs/heads/a": "old"}, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, "", true, false},
		{"opaque tag", map[string]string{"refs/tags/v": "object:tag"}, nil, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, "", false, false},
		{"ff only nonempty target", map[string]string{"refs/heads/main": "n"}, map[string]string{"refs/heads/main": "o"}, Mirror{Mode: ModeFastForward, RefSelection: RefSelectionAll}, "", false, false},
		{"exact adopt delete denied", map[string]string{"refs/heads/main": "n"}, map[string]string{"refs/heads/main": "o", "refs/tags/gone": "t"}, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, "", true, false},
		{"exact adopt delete confirmed", map[string]string{"refs/heads/main": "n"}, map[string]string{"refs/heads/main": "o", "refs/tags/gone": "t"}, Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			digest := ""
			if tc.confirm {
				first, _ := ComputePlan(tc.local, tc.remote, tc.m, tc.def, "")
				digest = first.Digest
			}
			p, err := ComputePlan(tc.local, tc.remote, tc.m, tc.def, digest)
			if errors.Is(err, ErrNeedsConfirmation) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
			if tc.name == "exact prune hidden untouched" {
				if len(p.Deletes) != 1 || p.Deletes[0].Ref != "refs/tags/old" || len(p.Ignored) != 2 {
					t.Fatalf("plan=%+v", p)
				}
				found := false
				for _, r := range p.Updates {
					if r.Ref == "refs/heads/main" && r.Action == ActionForceUpdate {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing force update: %+v", p)
				}
			}
			if tc.name == "ff only" && (len(p.Deletes) != 0 || len(p.Diverged) != 1) {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "globs" && len(p.Updates) != 1 {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "reconcile" && (len(p.Updates) != 1 || p.Updates[0].Action != ActionInSync) {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "D F" {
				if len(p.Updates) != 1 || p.Updates[0].Action != ActionBlockedDF || len(p.Deletes) != 1 {
					t.Fatalf("plan=%+v", p)
				}
			}
			if tc.name == "opaque tag" && (len(p.Updates) != 1 || p.Updates[0].New != "object:tag") {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "exact prune hidden untouched" && (p.Deletes[0].Action != ActionPrune || p.Digest == "") {
				t.Fatalf("plan=%+v", p)
			}
		})
	}
}
