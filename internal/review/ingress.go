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
// when this returns nil. Comment anchors stay on their published heads until
// RemapPublished observes the landed patchset.
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
