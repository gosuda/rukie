package review

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

// IngressTarget splits refs/for/<branch>[/<topic>] into its branch and topic.
// A bare refs/for names no target and is rejected.
func IngressTarget(ref string) (string, string, bool) {
	rest, ok := strings.CutPrefix(ref, "refs/for/")
	if !ok || rest == "" {
		return "", "", false
	}
	branch, topic, _ := strings.Cut(rest, "/")
	if branch == "" || strings.Contains(branch, "..") {
		return "", "", false
	}
	return branch, topic, true
}

// Ingress carries one diverted push into a change create or update.
type Ingress struct {
	Spans    map[string][]Span
	Branch   string
	Topic    string
	HeadOid  string
	RepoID   int64
	AuthorID int64
	// MayEdit reports whether the pusher holds change.edit. Creating a change
	// needs only repo visibility; appending a patchset to somebody else's
	// change does not.
	MayEdit bool
}

// ErrIngressForbidden marks a diverted push that would touch a change the
// pusher may not edit.
var ErrIngressForbidden = errors.New("change.edit is required to update this change")

// IngressChange creates or updates the change for a diverted push inside the
// hook budget. The change commits synchronously: the caller answers ok only
// when this returns nil. Comments anchored to the previous head remap
// through the unchanged spans of each path; paths without spans fall to the
// fallback bucket. Whole-change comments bump heads without remapping.
func IngressChange(tx *store.WriteTx, now time.Time, in Ingress) (store.ChangeRow, string, error) {
	key := in.Branch + "~" + in.Topic
	if in.Topic == "" {
		key = fmt.Sprintf("%s~%d", in.Branch, in.AuthorID)
	}
	if row, gerr := tx.GetChangeByKey(in.RepoID, key); gerr == nil && row.State == "open" {
		if !in.MayEdit {
			return store.ChangeRow{}, "", ErrIngressForbidden
		}
		updated, aerr := tx.AppendPatchset(now, row.ID, in.HeadOid)
		if aerr != nil {
			return store.ChangeRow{}, "", aerr
		}
		if rerr := remapComments(tx, row, in.HeadOid, in.Spans); rerr != nil {
			return store.ChangeRow{}, "", rerr
		}
		return updated, reviewRef(updated), nil
	}
	row, ierr := tx.InsertChange(now, store.ChangeRow{
		RepoID: in.RepoID, ChangeKey: key, TargetBranch: in.Branch,
		AuthorUserID: in.AuthorID, HeadOid: in.HeadOid,
	})
	if ierr != nil {
		return store.ChangeRow{}, "", ierr
	}
	return row, reviewRef(row), nil
}

func reviewRef(row store.ChangeRow) string {
	return fmt.Sprintf("refs/changes/%d/patchset/%d", row.ID, row.PatchsetCount)
}

func remapComments(tx *store.WriteTx, row store.ChangeRow, newHead string, spans map[string][]Span) error {
	comments, lerr := tx.ListCommentsAtHeadIncludingUnmatched(row.ID, row.HeadOid)
	if lerr != nil {
		return lerr
	}
	for _, comment := range comments {
		if comment.Unmatched {
			continue
		}
		if !comment.Path.Valid {
			if rerr := tx.RemapComment(row.ID, comment.ID, row.HeadOid, newHead, comment.Line, false); rerr != nil {
				return rerr
			}
			continue
		}
		line := int64(0)
		if comment.Line.Valid {
			line = comment.Line.Int64
		}
		mapped, ok := RemapLine(uint32(line), spans[comment.Path.String])
		remapped := comment.Line
		if ok {
			remapped.Valid = true
			remapped.Int64 = int64(mapped)
		}
		if rerr := tx.RemapComment(row.ID, comment.ID, row.HeadOid, newHead, remapped, !ok); rerr != nil {
			return rerr
		}
	}
	return nil
}
