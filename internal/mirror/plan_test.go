package mirror

import (
	"errors"
	"testing"
)

func TestComputePlan(t *testing.T) {
	cases := []struct {
		local   map[string]string
		remote  map[string]string
		name    string
		srcDef  string
		def     string
		m       Mirror
		wantErr bool
		confirm bool
	}{
		{local: map[string]string{"refs/heads/main": "new", "refs/tags/v1": "t", "refs/pull/1/head": "x"}, remote: map[string]string{"refs/heads/main": "old", "refs/tags/old": "oldtag", "refs/pull/2/head": "hidden"}, name: "exact prune hidden untouched", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, wantErr: true},
		{local: map[string]string{"refs/heads/main": "b"}, remote: map[string]string{"refs/heads/main": "a", "refs/tags/old": "x"}, name: "ff only", m: Mirror{Mode: ModeFastForward, RefSelection: RefSelectionAll, AdoptExistingRefs: true}},
		{local: map[string]string{"refs/heads/main": "1", "refs/tags/v1": "2"}, name: "globs", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionGlobs, Globs: []string{"refs/heads/*"}, AdoptExistingRefs: true}},
		{local: map[string]string{"refs/heads/main": "x"}, remote: map[string]string{"refs/heads/main": "x"}, name: "reconcile", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}},
		{local: map[string]string{"refs/heads/main": "n"}, remote: map[string]string{"refs/heads/main": "o"}, name: "adopt denied", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll}, wantErr: true},
		{local: map[string]string{"refs/heads/main": "n"}, remote: map[string]string{"refs/heads/main": "o"}, name: "adopt confirmed", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll}, confirm: true},
		{local: map[string]string{"refs/heads/a/b": "new"}, remote: map[string]string{"refs/heads/a": "old"}, name: "D F", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, wantErr: true},
		{local: map[string]string{"refs/tags/v": "object:tag"}, name: "opaque tag", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}},
		{local: map[string]string{"refs/heads/main": "n"}, remote: map[string]string{"refs/heads/main": "o"}, name: "ff only nonempty target", m: Mirror{Mode: ModeFastForward, RefSelection: RefSelectionAll}},
		{local: map[string]string{"refs/heads/main": "n"}, remote: map[string]string{"refs/heads/main": "o", "refs/tags/gone": "t"}, name: "exact adopt delete denied", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, wantErr: true},
		{local: map[string]string{"refs/heads/main": "n"}, remote: map[string]string{"refs/heads/main": "o", "refs/tags/gone": "t"}, name: "exact adopt delete confirmed", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, confirm: true},
		{local: map[string]string{}, remote: map[string]string{"refs/heads/main": "m", "refs/tags/gone": "t"}, name: "never prunes default", srcDef: "main", def: "main", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			digest := ""
			if tc.confirm {
				first, _ := ComputePlan(tc.local, tc.remote, tc.m, tc.srcDef, tc.def, "")
				digest = first.Digest
			}
			p, err := ComputePlan(tc.local, tc.remote, tc.m, tc.srcDef, tc.def, digest)
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
			if tc.name == "ff only" && (len(p.Deletes) != 0 || len(p.Diverged) != 0 || len(p.Updates) != 1 || p.Updates[0].Action != ActionUpdate) {
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
			if tc.name == "ff only nonempty target" && (len(p.Updates) != 1 || p.Updates[0].Action != ActionUpdate || p.Updates[0].Old != "o" || p.Updates[0].New != "n") {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "opaque tag" && (len(p.Updates) != 1 || p.Updates[0].New != "object:tag") {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "exact prune hidden untouched" && (p.Deletes[0].Action != ActionPrune || p.Digest == "") {
				t.Fatalf("plan=%+v", p)
			}
			if tc.name == "never prunes default" {
				for _, d := range p.Deletes {
					if d.Ref == "refs/heads/main" {
						t.Fatalf("default branch pruned: %+v", p)
					}
				}
				if len(p.Deletes) != 1 || p.Deletes[0].Ref != "refs/tags/gone" {
					t.Fatalf("tag not pruned: %+v", p)
				}
				kept := false
				for _, r := range p.Ignored {
					if r.Ref == "refs/heads/main" && r.Reason == "default branch is never pruned" {
						kept = true
					}
				}
				if !kept {
					t.Fatalf("default branch not kept as ignored: %+v", p)
				}
			}
		})
	}
}
func planRefs(p Plan) map[string]RefAction {
	out := map[string]RefAction{}
	for _, list := range [][]RefPlan{p.Updates, p.Deletes, p.Diverged, p.Ignored} {
		for _, r := range list {
			out[r.Ref] = r.Action
		}
	}
	return out
}

func TestComputePlanNotesSelection(t *testing.T) {
	notesMirror := func(sel RefSelection, globs ...string) Mirror {
		return Mirror{Mode: ModeExact, RefSelection: sel, Globs: globs, IncludeNotes: true, AdoptExistingRefs: true}
	}
	cases := []struct {
		name string
		ref  string
		m    Mirror
		want bool
	}{
		{name: "all includes notes", m: notesMirror(RefSelectionAll), ref: "refs/notes/foo", want: true},
		{name: "notes off excludes", m: Mirror{Mode: ModeExact, RefSelection: RefSelectionAll, AdoptExistingRefs: true}, ref: "refs/notes/foo"},
		{name: "attest excluded", m: notesMirror(RefSelectionAll), ref: "refs/notes/ruk-attest"},
		{name: "attest subtree excluded", m: notesMirror(RefSelectionAll), ref: "refs/notes/ruk-attest/x"},
		{name: "glob match includes", m: notesMirror(RefSelectionGlobs, "refs/notes/*"), ref: "refs/notes/foo", want: true},
		{name: "glob miss excludes", m: notesMirror(RefSelectionGlobs, "refs/heads/*"), ref: "refs/notes/foo"},
		{name: "default tags skip notes", m: func() Mirror { m := notesMirror(RefSelectionDefaultBranchTags); return m }(), ref: "refs/notes/foo"},
		{name: "heads unaffected", m: notesMirror(RefSelectionAll), ref: "refs/heads/main", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ComputePlan(map[string]string{tc.ref: "new"}, map[string]string{}, tc.m, "main", "main", "")
			if err != nil {
				t.Fatalf("ComputePlan: %v", err)
			}
			_, found := planRefs(p)[tc.ref]
			if found != tc.want {
				t.Fatalf("ref %q selected = %v, want %v: %+v", tc.ref, found, tc.want, p)
			}
		})
	}
}

func TestComputePlanDefaultBranchTagsSource(t *testing.T) {
	mk := Mirror{Mode: ModeExact, RefSelection: RefSelectionDefaultBranchTags}
	t.Run("source develop selected over target main", func(t *testing.T) {
		local := map[string]string{"refs/heads/develop": "new", "refs/heads/main": "same"}
		remote := map[string]string{"refs/heads/main": "same"}
		p, err := ComputePlan(local, remote, mk, "develop", "main", "")
		if !errors.Is(err, ErrNeedsConfirmation) {
			t.Fatalf("err = %v, want confirmation", err)
		}
		p, err = ComputePlan(local, remote, mk, "develop", "main", p.Digest)
		if err != nil {
			t.Fatalf("confirmed: %v", err)
		}
		refs := planRefs(p)
		if refs["refs/heads/develop"] != ActionCreate {
			t.Fatalf("develop not created: %+v", p)
		}
		if _, found := refs["refs/heads/main"]; found {
			t.Fatalf("target main leaked into selection: %+v", p)
		}
	})
	t.Run("empty target does not fall back to main", func(t *testing.T) {
		p, err := ComputePlan(map[string]string{"refs/heads/develop": "new"}, map[string]string{}, mk, "develop", "", "")
		if err != nil {
			t.Fatalf("ComputePlan: %v", err)
		}
		refs := planRefs(p)
		if refs["refs/heads/develop"] != ActionCreate || len(p.Updates) != 1 {
			t.Fatalf("develop not the only update: %+v", p)
		}
	})
	t.Run("empty source selects no branch", func(t *testing.T) {
		p, err := ComputePlan(
			map[string]string{"refs/heads/develop": "new", "refs/tags/v1": "t"},
			map[string]string{}, mk, "", "", "")
		if err != nil {
			t.Fatalf("ComputePlan: %v", err)
		}
		refs := planRefs(p)
		if _, found := refs["refs/heads/develop"]; found {
			t.Fatalf("develop selected without a source default: %+v", p)
		}
		if refs["refs/tags/v1"] != ActionCreate {
			t.Fatalf("tag not selected: %+v", p)
		}
	})
}
