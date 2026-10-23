package api

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func TestVerbatimCursorRoundTrip(t *testing.T) {
	raw := `{"view":"9f2c","store":"aa00","lease":"550e8400-e29b-41d4-a716-446655440000","position":"e30=","query":"ab12"}`
	var c verbatimCursor
	if err := unmarshalJSON([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, merr := marshalJSON(c)
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	if string(out) != raw {
		t.Fatalf("round trip changed bytes:\n got %s\nwant %s", out, raw)
	}
}

func TestVerbatimCursorNull(t *testing.T) {
	var c verbatimCursor
	if err := unmarshalJSON([]byte(`null`), &c); err != nil {
		t.Fatalf("null: %v", err)
	}
	if c != "" {
		t.Fatalf("null cursor = %q, want empty", c)
	}
	out, merr := marshalJSON(c)
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	if string(out) != "null" {
		t.Fatalf("empty cursor marshals to %s, want null", out)
	}
}

func TestVerbatimCursorRejects(t *testing.T) {
	for _, raw := range []string{`[1,2]`, `"str"`, `42`, `{oops`, ``} {
		var c verbatimCursor
		if err := unmarshalJSON([]byte(raw), &c); err == nil {
			t.Fatalf("cursor %q accepted, want rejection", raw)
		}
	}
}

func TestCompositeCursorRoundTrip(t *testing.T) {
	in := compositeCursor{
		Version: 1, Print: "fp",
		Cursors:   map[string]verbatimCursor{"7": verbatimCursor(`{"a":1}`)},
		AfterName: "zeta", AfterID: 9,
	}
	encoded, merr := encodeComposite(in)
	if merr != nil {
		t.Fatalf("encode: %v", merr)
	}
	if strings.HasPrefix(encoded, "{") {
		t.Fatal("composite must not look like a raw Ruk cursor")
	}
	got, derr := decodeComposite(encoded)
	if derr != nil {
		t.Fatalf("decode: %v", derr)
	}
	if got.Version != 1 || got.Print != "fp" || got.AfterName != "zeta" || got.AfterID != 9 {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if string(got.Cursors["7"]) != `{"a":1}` {
		t.Fatalf("round trip mangled inner cursor: %q", got.Cursors["7"])
	}
}

func TestCompositeCursorRejects(t *testing.T) {
	if _, err := decodeComposite("!!!not-base64!!!"); err == nil {
		t.Fatal("bad base64 accepted")
	}
	if _, err := decodeComposite(`{"v":1}`); err == nil {
		t.Fatal("raw-object composite accepted")
	}
	raw, _ := marshalJSON(compositeCursor{Version: 2, Print: "x"})
	bad := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := decodeComposite(bad); err == nil {
		t.Fatal("version 2 composite accepted")
	}
	raw, _ = marshalJSON(compositeCursor{Version: 1})
	if _, err := decodeComposite(base64.RawURLEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("empty-print composite accepted")
	}
}

func TestSearchFingerprint(t *testing.T) {
	q := searchCodeQuery{q: "needle", globs: []string{"*.go", "*.md"}}
	repos := []searchRepoRef{{name: "c", row: store.RepoRow{ID: 3}}, {name: "a", row: store.RepoRow{ID: 1}}, {name: "b", row: store.RepoRow{ID: 2}}}
	a := searchFingerprint(q, repos)
	b := searchFingerprint(q, []searchRepoRef{repos[1], repos[2], repos[0]})
	if a != b {
		t.Fatal("fingerprint depends on repo order")
	}
	other := q
	other.q = "needle2"
	if searchFingerprint(other, repos) == a {
		t.Fatal("fingerprint ignores the query")
	}
	renamed := []searchRepoRef{{name: "c", row: store.RepoRow{ID: 3}}, {name: "a2", row: store.RepoRow{ID: 1}}, {name: "b", row: store.RepoRow{ID: 2}}}
	if searchFingerprint(q, renamed) == a {
		t.Fatal("fingerprint ignores renames")
	}
	revved := q
	revved.rev = "side"
	if searchFingerprint(revved, repos) == a {
		t.Fatal("fingerprint ignores rev")
	}
	if searchFingerprint(q, repos[:2]) == a {
		t.Fatal("fingerprint ignores the repo set")
	}
}

func fakeSearchRepos(n int) []searchRepoRef {
	out := make([]searchRepoRef, 0, n)
	for i := range n {
		out = append(out, searchRepoRef{
			name: string(rune('a'+i%26)) + strings.Repeat("x", i/26),
			row:  store.RepoRow{ID: int64(i + 1)},
		})
	}
	return out
}

func TestPlanFreshCoversFirstPage(t *testing.T) {
	repos := fakeSearchRepos(3)
	q := searchCodeQuery{q: "x", perPage: 10}
	fp := searchFingerprint(q, repos)
	plan, err := planMultiPage(repos, compositeCursor{}, fp)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.slots) != 3 {
		t.Fatalf("slots = %d, want 3", len(plan.slots))
	}
	if plan.after != repos[2].name || plan.afterID != 3 {
		t.Fatalf("frontier = %q/%d", plan.after, plan.afterID)
	}
}

func TestPlanCapsAndCarries(t *testing.T) {
	repos := fakeSearchRepos(searchMaxRepos + 5)
	q := searchCodeQuery{q: "x", perPage: 10}
	fp := searchFingerprint(q, repos)
	plan, err := planMultiPage(repos, compositeCursor{}, fp)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.slots) != searchMaxRepos {
		t.Fatalf("slots = %d, want cap %d", len(plan.slots), searchMaxRepos)
	}
}

func TestMergeTerminalTruncation(t *testing.T) {
	repos := []searchRepoRef{{name: "a", row: store.RepoRow{ID: 1}}}
	plan := multiPlan{slots: []multiSlot{{ref: repos[0], fresh: true}}, carried: map[string]verbatimCursor{}, after: "a", afterID: 1}
	out, err := mergeMultiPage(plan, map[int64]multiResult{1: {
		hits:      []searchHitReply{{Path: "f", Line: 1}},
		truncated: true, exhausted: "none",
	}}, "", repos)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !out.Truncated {
		t.Fatal("terminal truncated page lost its flag")
	}
	if out.Next != nil {
		t.Fatalf("terminal page carries a continuation: %q", *out.Next)
	}
	if out.Exhausted != "none" {
		t.Fatalf("terminal exhausted = %q, want none", out.Exhausted)
	}
}

func TestMergeDrainsEarlierRepos(t *testing.T) {
	repos := []searchRepoRef{{name: "a", row: store.RepoRow{ID: 1}}, {name: "b", row: store.RepoRow{ID: 2}}}
	plan := multiPlan{slots: []multiSlot{{ref: repos[0], fresh: true}, {ref: repos[1], fresh: true}}, carried: map[string]verbatimCursor{}, after: "b", afterID: 2, base: "", baseID: 0, prior: map[string]verbatimCursor{}}
	out, err := mergeMultiPage(plan, map[int64]multiResult{
		1: {hits: []searchHitReply{{Path: "a.txt", Line: 1}}, next: verbatimCursor(`{"p":"a"}`), exhausted: "none"},
		2: {hits: []searchHitReply{{Path: "b.txt", Line: 1}}, next: verbatimCursor(`{"p":"b"}`), exhausted: "none"},
	}, "fp", repos)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(out.Hits) != 1 || out.Hits[0].Path != "a.txt" {
		t.Fatalf("drain emitted %+v, want only the earlier repo", out.Hits)
	}
	if out.Next == nil {
		t.Fatal("drain page has no continuation")
	}
	decoded, derr := decodeComposite(*out.Next)
	if derr != nil {
		t.Fatalf("continuation: %v", derr)
	}
	if _, ok := decoded.Cursors["2"]; ok {
		t.Fatalf("withheld repo leaked a new cursor: %+v", decoded.Cursors)
	}
	if string(decoded.Cursors["1"]) != `{"p":"a"}` {
		t.Fatalf("emitted repo lost its cursor: %+v", decoded.Cursors)
	}
}

func TestPlanResumeAndMismatch(t *testing.T) {
	repos := fakeSearchRepos(3)
	q := searchCodeQuery{q: "x", perPage: 10}
	fp := searchFingerprint(q, repos)
	resume, err := encodeComposite(compositeCursor{
		Version: 1, Print: fp,
		Cursors:   map[string]verbatimCursor{"2": verbatimCursor(`{"a":1}`)},
		AfterName: repos[0].name, AfterID: 1,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, derr := decodeComposite(resume)
	if derr != nil {
		t.Fatalf("decode: %v", derr)
	}
	plan, perr := planMultiPage(repos, decoded, fp)
	if perr != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(plan.slots) != 2 {
		t.Fatalf("resume slots = %d, want 2 (repo 2 resumed, repo 3 fresh)", len(plan.slots))
	}
	if plan.slots[0].ref.row.ID != 2 || plan.slots[0].fresh {
		t.Fatal("first slot must resume repo 2")
	}
	bad, _ := encodeComposite(compositeCursor{Version: 1, Print: "deadbeef"})
	decodedBad, _ := decodeComposite(bad)
	if _, err := planMultiPage(repos, decodedBad, fp); err == nil {
		t.Fatal("fingerprint mismatch accepted")
	}
	gone, _ := encodeComposite(compositeCursor{
		Version: 1, Print: fp, Cursors: map[string]verbatimCursor{"99": "{}"},
	})
	decodedGone, _ := decodeComposite(gone)
	if _, err := planMultiPage(repos, decodedGone, fp); err == nil {
		t.Fatal("cursor for a vanished repo accepted")
	}
}

func TestMergePrefersLeaseConflictOverCancel(t *testing.T) {
	leaseErr := op.Errorf(op.CodeConflict, "search slot lease was lost")
	cancelErr := mapRukError(context.Canceled)
	if !isCancelErr(cancelErr) {
		t.Fatalf("cancel harness is not a cancellation: %v", cancelErr)
	}
	if isCancelErr(leaseErr) {
		t.Fatalf("lease harness looks like a cancellation: %v", leaseErr)
	}
	for _, cancelFirst := range []bool{false, true} {
		repos := fakeSearchRepos(2)
		plan := multiPlan{
			slots:   []multiSlot{{ref: repos[0], fresh: true}, {ref: repos[1], fresh: true}},
			carried: map[string]verbatimCursor{},
			after:   "b", afterID: 2,
		}
		first, second := error(leaseErr), cancelErr
		if cancelFirst {
			first, second = second, first
		}
		results := map[int64]multiResult{1: {err: first}, 2: {err: second}}
		_, err := mergeMultiPage(plan, results, "fp", repos)
		oerr, ok := errors.AsType[*op.Error](err)
		if !ok || oerr.Code != op.CodeConflict {
			t.Fatalf("cancelFirst=%v: merge returned %v, want the lease conflict", cancelFirst, err)
		}
		if !errors.Is(err, leaseErr) {
			t.Fatalf("cancelFirst=%v: merge returned %v, want the lease error itself", cancelFirst, err)
		}
	}
}

func TestSinglePageEmptyExhaustedMeansNone(t *testing.T) {
	if got := normalizeSearchExhausted(""); got != "none" {
		t.Fatalf("empty exhausted = %q, want none", got)
	}
	for _, mark := range []string{"none", "budget", "deadline"} {
		if got := normalizeSearchExhausted(mark); got != mark {
			t.Fatalf("exhausted %q normalized to %q, want it unchanged", mark, got)
		}
	}
}
