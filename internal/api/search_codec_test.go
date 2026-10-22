package api

import (
	"encoding/base64"
	"strings"
	"testing"

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
	a := searchFingerprint(q, []int64{3, 1, 2})
	b := searchFingerprint(q, []int64{2, 3, 1})
	if a != b {
		t.Fatal("fingerprint depends on repo order")
	}
	other := q
	other.q = "needle2"
	if searchFingerprint(other, []int64{1, 2, 3}) == a {
		t.Fatal("fingerprint ignores the query")
	}
	if searchFingerprint(q, []int64{1, 2, 4}) == a {
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
	ids := []int64{1, 2, 3}
	fp := searchFingerprint(q, ids)
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
	var ids []int64
	for _, r := range repos {
		ids = append(ids, r.row.ID)
	}
	fp := searchFingerprint(q, ids)
	plan, err := planMultiPage(repos, compositeCursor{}, fp)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.slots) != searchMaxRepos {
		t.Fatalf("slots = %d, want cap %d", len(plan.slots), searchMaxRepos)
	}
}

func TestPlanResumeAndMismatch(t *testing.T) {
	repos := fakeSearchRepos(3)
	q := searchCodeQuery{q: "x", perPage: 10}
	ids := []int64{1, 2, 3}
	fp := searchFingerprint(q, ids)
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
