package review

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/gosuda/rukie/internal/store"
)

// PublishedChange identifies one immutable review ref confirmed by receive-pack.
type PublishedChange struct {
	HeadOid  string
	RepoID   int64
	ChangeID int64
	Patchset int64
}

// PublishedTarget parses a landed review ref without accepting partial names.
func PublishedTarget(repoID int64, name, head string) (PublishedChange, bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 5 || parts[0] != "refs" || parts[1] != "changes" || parts[3] != "patchset" || head == "" {
		return PublishedChange{}, false
	}
	id, ierr := strconv.ParseInt(parts[2], 10, 64)
	ordinal, oerr := strconv.ParseInt(parts[4], 10, 64)
	if ierr != nil || oerr != nil || id <= 0 || ordinal <= 0 || repoID <= 0 {
		return PublishedChange{}, false
	}
	return PublishedChange{HeadOid: head, RepoID: repoID, ChangeID: id, Patchset: ordinal}, true
}

func (target PublishedChange) matches(row store.ChangeRow) bool {
	return row.RepoID == target.RepoID && row.HeadOid == target.HeadOid && row.PatchsetCount == target.Patchset
}

// RemapPublished catches up matched comments to the current published patchset.
// Diff returns every changed path; absent paths are unchanged. Failed diffs
// leave anchors pending. Replayed or superseded targets cannot move comments.
func RemapPublished(ctx context.Context, st *store.Store, target PublishedChange, diff func(context.Context, string, string) (map[string][]Span, error)) error {
	var heads []string
	if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		row, err := tx.ReadChange(target.ChangeID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !target.matches(row) {
			return nil
		}
		heads, err = tx.PendingCommentHeads(target.ChangeID, target.HeadOid)
		return err
	}); rerr != nil {
		return fmt.Errorf("RemapPublished: %w", rerr)
	}
	if len(heads) == 0 {
		return nil
	}
	diffs := make(map[string]map[string][]Span, len(heads))
	for _, head := range heads {
		spans, err := diff(ctx, head, target.HeadOid)
		if err != nil {
			return fmt.Errorf("RemapPublished: %w", err)
		}
		diffs[head] = spans
	}
	if werr := st.Write(ctx, func(tx *store.WriteTx) error {
		row, err := tx.GetChangeForUpdate(target.ChangeID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !target.matches(row) {
			return nil
		}
		for _, head := range heads {
			if err := remapComments(tx, row.ID, head, target.HeadOid, diffs[head]); err != nil {
				return err
			}
		}
		return nil
	}); werr != nil {
		return fmt.Errorf("RemapPublished: %w", werr)
	}
	return nil
}

func remapComments(tx *store.WriteTx, changeID int64, oldHead, newHead string, spans map[string][]Span) error {
	comments, err := tx.ListCommentsAtHeadIncludingUnmatched(changeID, oldHead)
	if err != nil {
		return err
	}
	for _, comment := range comments {
		if comment.Unmatched {
			continue
		}
		mapped := comment.Line
		matched := true
		if comment.Path.Valid {
			if !comment.Line.Valid || comment.Line.Int64 <= 0 || comment.Line.Int64 > math.MaxUint32 {
				matched = false
			} else if fileSpans, changed := spans[comment.Path.String]; changed {
				line, ok := RemapLine(uint32(comment.Line.Int64), fileSpans)
				matched = ok
				if matched {
					mapped.Int64 = int64(line)
				}
			}
		}
		if err := tx.RemapComment(changeID, comment.ID, oldHead, newHead, mapped, !matched); err != nil {
			return err
		}
	}
	return nil
}
