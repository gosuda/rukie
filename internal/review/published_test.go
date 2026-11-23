package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func publishedFixture(t *testing.T) (*store.Store, store.ChangeRow) {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: t.TempDir() + "/published.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	author, err := st.InsertAuthUser(t.Context(), store.AuthUserCreate{
		Name: "ada", Lower: "ada", Email: "ada@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var row store.ChangeRow
	if err := st.Write(t.Context(), func(tx *store.WriteTx) error {
		repoID, err := tx.InsertRepo(time.Unix(1, 0), store.RepoInsert{
			Name: "hello", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: author.ID, Valid: true},
		})
		if err != nil {
			return err
		}
		row, _, err = IngressChange(tx, time.Unix(1, 0), Ingress{
			RepoID: repoID, AuthorID: author.ID, Branch: "main", HeadOid: strings.Repeat("a", 40),
		})
		if err != nil {
			return err
		}
		for _, comment := range []store.CommentRow{
			{Body: "mapped", Path: sql.NullString{String: "changed.txt", Valid: true}, Line: sql.NullInt64{Int64: 1, Valid: true}},
			{Body: "unchanged", Path: sql.NullString{String: "stable.txt", Valid: true}, Line: sql.NullInt64{Int64: 1, Valid: true}},
			{Body: "edited", Path: sql.NullString{String: "edited.txt", Valid: true}, Line: sql.NullInt64{Int64: 1, Valid: true}},
			{Body: "whole"},
			{Body: "unmatched", Path: sql.NullString{String: "changed.txt", Valid: true}, Line: sql.NullInt64{Int64: 1, Valid: true}, Unmatched: true},
		} {
			comment.ChangeID = row.ID
			comment.AuthorUserID = author.ID
			comment.HeadOid = row.HeadOid
			if _, err := tx.InsertComment(time.Unix(1, 0), comment); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return st, row
}

func advancePublished(t *testing.T, st *store.Store, row store.ChangeRow, head string) PublishedChange {
	t.Helper()
	var target PublishedChange
	if err := st.Write(t.Context(), func(tx *store.WriteTx) error {
		updated, ref, err := IngressChange(tx, time.Unix(2, 0), Ingress{
			RepoID: row.RepoID, AuthorID: row.AuthorUserID, Branch: row.TargetBranch,
			HeadOid: head, MayEdit: true,
		})
		if err != nil {
			return err
		}
		var ok bool
		target, ok = PublishedTarget(row.RepoID, ref, head)
		if !ok || target.Patchset != updated.PatchsetCount {
			return fmt.Errorf("invalid published target %q", ref)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return target
}

func publishedComments(t *testing.T, st *store.Store, id int64, head string) map[string]store.CommentRow {
	t.Helper()
	found := make(map[string]store.CommentRow)
	if err := st.Read(t.Context(), func(tx *store.ReadTx) error {
		comments, err := tx.ListCommentsAtHeadIncludingUnmatched(id, head)
		if err != nil {
			return err
		}
		for _, comment := range comments {
			found[comment.Body] = comment
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

func remapTestSpans(line uint32) map[string][]Span {
	return map[string][]Span{
		"changed.txt": {{OldStart: 1, NewStart: line, Lines: 1}},
		"edited.txt":  nil,
	}
}

func TestRemapPublishedRetriesWithoutLosingAnchors(t *testing.T) {
	st, row := publishedFixture(t)
	target := advancePublished(t, st, row, strings.Repeat("b", 40))
	if got := publishedComments(t, st, row.ID, row.HeadOid); len(got) != 5 {
		t.Fatalf("ingress changed comment anchors: %+v", got)
	}
	unavailable := errors.New("diff unavailable")
	err := RemapPublished(t.Context(), st, target, func(context.Context, string, string) (map[string][]Span, error) {
		return nil, unavailable
	})
	if !errors.Is(err, unavailable) {
		t.Fatalf("remap error = %v, want the diff error", err)
	}
	if got := publishedComments(t, st, row.ID, row.HeadOid); len(got) != 5 || got["mapped"].Unmatched {
		t.Fatalf("failed diff consumed pending anchors: %+v", got)
	}
	if err := RemapPublished(t.Context(), st, target, func(_ context.Context, oldHead, newHead string) (map[string][]Span, error) {
		if oldHead != row.HeadOid || newHead != target.HeadOid {
			return nil, fmt.Errorf("diff = %s..%s", oldHead, newHead)
		}
		return remapTestSpans(2), nil
	}); err != nil {
		t.Fatal(err)
	}
	got := publishedComments(t, st, row.ID, target.HeadOid)
	if len(got) != 4 || got["mapped"].Line.Int64 != 2 || got["mapped"].Unmatched {
		t.Fatalf("mapped comments = %+v", got)
	}
	if got["unchanged"].Line.Int64 != 1 || got["unchanged"].Unmatched || got["whole"].Unmatched || !got["edited"].Unmatched {
		t.Fatalf("unchanged, whole, or edited comment = %+v", got)
	}
	if old := publishedComments(t, st, row.ID, row.HeadOid); len(old) != 1 || !old["unmatched"].Unmatched {
		t.Fatalf("existing unmatched comment moved: %+v", old)
	}
	if err := RemapPublished(t.Context(), st, target, func(context.Context, string, string) (map[string][]Span, error) {
		t.Fatal("duplicate callback requested another diff")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if again := publishedComments(t, st, row.ID, target.HeadOid); len(again) != len(got) || again["mapped"] != got["mapped"] {
		t.Fatalf("duplicate callback changed comments: %+v", again)
	}
}

func TestRemapPublishedSupersededCallbackCatchesUp(t *testing.T) {
	st, row := publishedFixture(t)
	stale := advancePublished(t, st, row, strings.Repeat("b", 40))
	current := advancePublished(t, st, row, strings.Repeat("c", 40))
	if err := RemapPublished(t.Context(), st, stale, func(context.Context, string, string) (map[string][]Span, error) {
		t.Fatal("superseded callback requested a diff")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := RemapPublished(t.Context(), st, current, func(_ context.Context, oldHead, newHead string) (map[string][]Span, error) {
		if oldHead != row.HeadOid || newHead != current.HeadOid {
			return nil, fmt.Errorf("catch-up diff = %s..%s", oldHead, newHead)
		}
		return remapTestSpans(3), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := RemapPublished(t.Context(), st, stale, func(context.Context, string, string) (map[string][]Span, error) {
		t.Fatal("late callback requested a diff")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	got := publishedComments(t, st, row.ID, current.HeadOid)
	if len(got) != 4 || got["mapped"].Line.Int64 != 3 {
		t.Fatalf("current comments = %+v", got)
	}
}

func TestRemapPublishedChecksTargetAfterDiff(t *testing.T) {
	st, row := publishedFixture(t)
	stale := advancePublished(t, st, row, strings.Repeat("b", 40))
	var current PublishedChange
	if err := RemapPublished(t.Context(), st, stale, func(context.Context, string, string) (map[string][]Span, error) {
		current = advancePublished(t, st, row, strings.Repeat("c", 40))
		return remapTestSpans(2), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := publishedComments(t, st, row.ID, row.HeadOid); len(got) != 5 {
		t.Fatalf("stale diff changed comment anchors: %+v", got)
	}
	if err := RemapPublished(t.Context(), st, current, func(context.Context, string, string) (map[string][]Span, error) {
		return remapTestSpans(3), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := publishedComments(t, st, row.ID, current.HeadOid); len(got) != 4 || got["mapped"].Line.Int64 != 3 {
		t.Fatalf("new target lost pending comments: %+v", got)
	}
}

func TestRemapPublishedScopesRepository(t *testing.T) {
	st, row := publishedFixture(t)
	target := advancePublished(t, st, row, strings.Repeat("b", 40))
	target.RepoID++
	if err := RemapPublished(t.Context(), st, target, func(context.Context, string, string) (map[string][]Span, error) {
		t.Fatal("foreign repository requested a diff")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := publishedComments(t, st, row.ID, row.HeadOid); len(got) != 5 {
		t.Fatalf("foreign callback changed anchors: %+v", got)
	}
}

func TestPublishedTargetRejectsMalformedRefs(t *testing.T) {
	for _, name := range []string{
		"refs/heads/main", "refs/changes/1", "refs/changes/1/patchset/2/extra",
		"refs/changes/zero/patchset/2", "refs/changes/0/patchset/2",
		"refs/changes/1/patchset/-1", "refs/changes/1/other/2",
	} {
		if _, ok := PublishedTarget(1, name, strings.Repeat("a", 40)); ok {
			t.Fatalf("accepted malformed review ref %q", name)
		}
	}
}
