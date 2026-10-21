package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

// reviewCaller adapts Deps to the pending-call surface.
type reviewCaller struct {
	deps Deps
}

func (c reviewCaller) CallRuk(ctx context.Context, repo ruk.Repo, method wire.Method, args map[string]any, out any) error {
	return c.deps.callRuk(ctx, repo, method, args, out)
}

// CompareGetIn carries one compare range request.
type CompareGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Range string `path:"range"`
}

// CompareGetOut carries the ahead and behind counts of a range.
type CompareGetOut struct {
	Base   string `json:"base"`
	Head   string `json:"head"`
	Ahead  int64  `json:"ahead"`
	Behind int64  `json:"behind"`
}

func compareGetHandler(d Deps) func(context.Context, op.Principal, CompareGetIn) (CompareGetOut, error) {
	return func(ctx context.Context, p op.Principal, in CompareGetIn) (CompareGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CompareGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CompareGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return CompareGetOut{}, derr
		}
		left, right, rerr := splitDiffRange(CanonicalRange(in.Range))
		if rerr != nil {
			return CompareGetOut{}, rerr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return CompareGetOut{}, oerr
		}
		rc.daemon = daemon
		leftResolved, lerr := d.resolveRev(ctx, rc, left)
		if lerr != nil {
			return CompareGetOut{}, lerr
		}
		rightResolved, rerr := d.resolveRev(ctx, rc, right)
		if rerr != nil {
			return CompareGetOut{}, rerr
		}
		var reply CompareGetOut
		args := map[string]any{
			"base":  headRevisionRef(leftResolved.commit),
			"head":  headRevisionRef(rightResolved.commit),
			"limit": 1000,
		}
		if perr := review.PendingCompare(ctx, reviewCaller{d}, daemon, args, &reply); perr != nil {
			return CompareGetOut{}, mapRukError(perr)
		}
		reply.Base = leftResolved.commit
		reply.Head = rightResolved.commit
		return reply, nil
	}
}

// ChangeJSON is one change in list and get responses.
type ChangeJSON struct {
	Key          string `json:"key"`
	TargetBranch string `json:"target_branch"`
	State        string `json:"state"`
	Head         string `json:"head"`
	Patchsets    int64  `json:"patchsets"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

func changeJSON(row store.ChangeRow) ChangeJSON {
	return ChangeJSON{
		Key:          row.ChangeKey,
		TargetBranch: row.TargetBranch,
		State:        row.State,
		Head:         row.HeadOid,
		Patchsets:    row.PatchsetCount,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// ChangeListIn carries one change list request.
type ChangeListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	State  string `query:"state"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

// ChangeListOut carries one page of changes.
type ChangeListOut struct {
	Next    string       `json:"-"`
	Changes []ChangeJSON `json:"changes"`
}

func changeListHandler(d Deps) func(context.Context, op.Principal, ChangeListIn) (ChangeListOut, error) {
	return func(ctx context.Context, p op.Principal, in ChangeListIn) (ChangeListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ChangeListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ChangeListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ChangeListOut{}, derr
		}
		if in.State != "" {
			switch in.State {
			case "open", "landed", "abandoned", "conflicted":
			default:
				return ChangeListOut{}, op.Errorf(op.CodeValidation, "state is not a change state")
			}
		}
		limit := clampLimit(in.Limit, 30, 100)
		out := ChangeListOut{Changes: []ChangeJSON{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListChanges(rc.row.ID, in.State, limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				out.Changes = append(out.Changes, changeJSON(row))
			}
			return nil
		})
		if rerr != nil {
			return ChangeListOut{}, op.Wrap(op.CodeInternal, rerr, "change list failed")
		}
		return out, nil
	}
}

// ChangeGetIn carries one change read.
type ChangeGetIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Change string `path:"change"`
}

// ChangeGetOut carries one change with its patchset heads.
type ChangeGetOut struct {
	Heads  []string   `json:"heads"`
	Change ChangeJSON `json:"change"`
}

func changeGetHandler(d Deps) func(context.Context, op.Principal, ChangeGetIn) (ChangeGetOut, error) {
	return func(ctx context.Context, p op.Principal, in ChangeGetIn) (ChangeGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ChangeGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ChangeGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ChangeGetOut{}, derr
		}
		if in.Change == "" {
			return ChangeGetOut{}, op.Errorf(op.CodeValidation, "change id must not be empty")
		}
		out := ChangeGetOut{Heads: []string{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			row, gerr := tx.ReadChangeByKey(rc.row.ID, in.Change)
			if gerr != nil {
				return gerr
			}
			out.Change = changeJSON(row)
			sets, lerr := tx.ListPatchsets(row.ID)
			if lerr != nil {
				return lerr
			}
			for _, set := range sets {
				out.Heads = append(out.Heads, set.HeadOid)
			}
			return nil
		})
		if rerr != nil {
			return ChangeGetOut{}, storeToOp(rerr, "change was not found")
		}
		return out, nil
	}
}

// ChangeDiffIn carries one change diff request.
type ChangeDiffIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Change string `path:"change"`
	Range  string `path:"range"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

// ChangeDiffOut carries the files changed between two patchsets.
type ChangeDiffOut struct {
	Left  string         `json:"left"`
	Right string         `json:"right"`
	Next  string         `json:"-"`
	Files []DiffFileJSON `json:"files"`
}

func changeDiffGetHandler(d Deps) func(context.Context, op.Principal, ChangeDiffIn) (ChangeDiffOut, error) {
	return func(ctx context.Context, p op.Principal, in ChangeDiffIn) (ChangeDiffOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ChangeDiffOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ChangeDiffOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ChangeDiffOut{}, derr
		}
		leftRev, rightRev, rerr := splitDiffRange(in.Range)
		if rerr != nil {
			return ChangeDiffOut{}, rerr
		}
		left, right, verr := resolveChangeRevs(ctx, d, rc, in.Change, leftRev, rightRev)
		if verr != nil {
			return ChangeDiffOut{}, storeToOp(verr, "change was not found")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return ChangeDiffOut{}, oerr
		}
		limit := clampLimit(in.Limit, 100, 1000)
		args := map[string]any{
			"left":  headRevisionRef(left),
			"right": headRevisionRef(right),
			"paths": "all",
		}
		if in.Cursor != "" {
			cursor, cerr := parseRukCursor(in.Cursor)
			if cerr != nil {
				return ChangeDiffOut{}, cerr
			}
			args["page"] = map[string]any{"cursor": cursor, "limit": limit}
		} else if limit > 0 {
			args["page"] = map[string]any{"limit": limit}
		}
		files, next, derr := d.readDiffPage(ctx, daemon, args)
		if derr != nil {
			return ChangeDiffOut{}, derr
		}
		return ChangeDiffOut{Left: left, Right: right, Next: next, Files: files}, nil
	}
}

func resolveChangeRevs(ctx context.Context, d Deps, rc repoContext, key, leftRev, rightRev string) (string, string, error) {
	var left, right string
	verr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		row, gerr := tx.ReadChangeByKey(rc.row.ID, key)
		if gerr != nil {
			return gerr
		}
		sets, lerr := tx.ListPatchsets(row.ID)
		if lerr != nil {
			return lerr
		}
		byOrdinal := map[string]string{"head": row.HeadOid}
		for _, set := range sets {
			byOrdinal[fmt.Sprint(set.Ordinal)] = set.HeadOid
			byOrdinal[set.HeadOid] = set.HeadOid
		}
		var ok bool
		if left, ok = byOrdinal[leftRev]; !ok {
			return store.ErrNotFound
		}
		if right, ok = byOrdinal[rightRev]; !ok {
			return store.ErrNotFound
		}
		return nil
	})
	if verr != nil {
		return "", "", verr
	}
	return left, right, nil
}

// ChangeBlameIn carries one change blame request.
type ChangeBlameIn struct {
	Owner     string `path:"owner"`
	Repo      string `path:"repo"`
	Change    string `path:"change"`
	Path      string `query:"path"`
	Cursor    string `query:"cursor"`
	StartLine int    `query:"start_line"`
	EndLine   int    `query:"end_line"`
	Limit     int    `query:"limit"`
}

// ChangeBlameOut carries blame hunks at a change head.
type ChangeBlameOut struct {
	Head  string          `json:"head"`
	Path  string          `json:"path"`
	Next  string          `json:"-"`
	Hunks []BlameHunkJSON `json:"hunks"`
}

func changeBlameGetHandler(d Deps) func(context.Context, op.Principal, ChangeBlameIn) (ChangeBlameOut, error) {
	return func(ctx context.Context, p op.Principal, in ChangeBlameIn) (ChangeBlameOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ChangeBlameOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ChangeBlameOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ChangeBlameOut{}, derr
		}
		if in.Path == "" {
			return ChangeBlameOut{}, op.Errorf(op.CodeValidation, "path must not be empty")
		}
		if perr := checkTreePath(in.Path); perr != nil {
			return ChangeBlameOut{}, perr
		}
		head, ok := changeHead(ctx, d, rc, in.Change)
		if !ok {
			return ChangeBlameOut{}, op.Errorf(op.CodeNotFound, "change was not found")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return ChangeBlameOut{}, oerr
		}
		limit := clampLimit(in.Limit, 100, 1000)
		args := map[string]any{
			"at":   headRevisionRef(head),
			"path": in.Path,
		}
		if in.StartLine != 0 || in.EndLine != 0 {
			start := in.StartLine
			if start == 0 {
				start = 1
			}
			end := in.EndLine
			if end == 0 {
				end = 1 << 30
			}
			if end < start {
				return ChangeBlameOut{}, op.Errorf(op.CodeValidation, "end line must not precede start line")
			}
			args["lines"] = map[string]any{"start": start, "end": end}
		}
		page := map[string]any{}
		if in.Cursor != "" {
			cursor, cerr := parseRukCursor(in.Cursor)
			if cerr != nil {
				return ChangeBlameOut{}, cerr
			}
			page["cursor"] = cursor
		}
		if limit > 0 {
			page["limit"] = limit
		}
		if len(page) > 0 {
			args["page"] = page
		}
		var reply blameReply
		if rerr := d.callRuk(ctx, daemon, wire.MethodBlame, args, &reply); rerr != nil {
			return ChangeBlameOut{}, rerr
		}
		out := ChangeBlameOut{Head: head, Path: in.Path, Hunks: []BlameHunkJSON{}}
		for _, hunk := range reply.Hunks.Items {
			headOid, herr := gitHeadOid(hunk.Head)
			if herr != nil {
				headOid = ""
			}
			out.Hunks = append(out.Hunks, BlameHunkJSON{
				Head:         headOid,
				Author:       hunk.Author.Name.String(),
				Email:        hunk.Author.Email.String(),
				OriginalPath: hunk.OriginalPath.String(),
				Change:       hunk.Change,
				StartLine:    hunk.StartLine,
				LineCount:    hunk.LineCount,
			})
		}
		return out, nil
	}
}

func changeHead(ctx context.Context, d Deps, rc repoContext, key string) (string, bool) {
	var head string
	found := false
	_ = d.Store.Read(ctx, func(tx *store.ReadTx) error {
		row, gerr := tx.ReadChangeByKey(rc.row.ID, key)
		if gerr != nil {
			return gerr
		}
		head = row.HeadOid
		found = head != ""
		return nil
	})
	return head, found
}

// ChangeStackOut carries the open changes of one target branch.
type ChangeStackIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Change string `path:"change"`
}

// ChangeStackOut carries the stack bottom to top.
type ChangeStackOut struct {
	Target  string       `json:"target"`
	Changes []ChangeJSON `json:"changes"`
}

func changeStackHandler(d Deps) func(context.Context, op.Principal, ChangeStackIn) (ChangeStackOut, error) {
	return func(ctx context.Context, p op.Principal, in ChangeStackIn) (ChangeStackOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ChangeStackOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ChangeStackOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ChangeStackOut{}, derr
		}
		out := ChangeStackOut{Changes: []ChangeJSON{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			row, gerr := tx.ReadChangeByKey(rc.row.ID, in.Change)
			if gerr != nil {
				return gerr
			}
			out.Target = row.TargetBranch
			rows, lerr := tx.ListChanges(rc.row.ID, "open", 100)
			if lerr != nil {
				return lerr
			}
			for _, other := range rows {
				if other.TargetBranch == row.TargetBranch {
					out.Changes = append(out.Changes, changeJSON(other))
				}
			}
			return nil
		})
		if rerr != nil {
			return ChangeStackOut{}, storeToOp(rerr, "change was not found")
		}
		return out, nil
	}
}

// BranchChangeListIn carries one branch view request.
type BranchChangeListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Branch string `path:"branch"`
	State  string `query:"state"`
	Limit  int    `query:"limit"`
}

// BranchChangeListOut carries the open changes of one branch.
type BranchChangeListOut struct {
	Target  string       `json:"target"`
	Changes []ChangeJSON `json:"changes"`
}

func branchChangeListHandler(d Deps) func(context.Context, op.Principal, BranchChangeListIn) (BranchChangeListOut, error) {
	return func(ctx context.Context, p op.Principal, in BranchChangeListIn) (BranchChangeListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BranchChangeListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BranchChangeListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return BranchChangeListOut{}, derr
		}
		if in.Branch == "" {
			return BranchChangeListOut{}, op.Errorf(op.CodeValidation, "branch must not be empty")
		}
		state := cmp.Or(in.State, "open")
		switch state {
		case "open", "landed", "abandoned", "conflicted":
		default:
			return BranchChangeListOut{}, op.Errorf(op.CodeValidation, "state is not a change state")
		}
		out := BranchChangeListOut{Target: in.Branch, Changes: []ChangeJSON{}}
		limit := clampLimit(in.Limit, 30, 100)
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListChanges(rc.row.ID, state, limit)
			if lerr != nil {
				return lerr
			}
			for _, row := range rows {
				if row.TargetBranch == in.Branch {
					out.Changes = append(out.Changes, changeJSON(row))
				}
			}
			return nil
		})
		if rerr != nil {
			return BranchChangeListOut{}, op.Wrap(op.CodeInternal, rerr, "branch change list failed")
		}
		return out, nil
	}
}

// CommentJSON is one review comment.
type CommentJSON struct {
	Path         *string `json:"path,omitempty"`
	Line         *int64  `json:"line,omitempty"`
	Key          string  `json:"id"`
	Body         string  `json:"body"`
	RemappedFrom string  `json:"remapped_from,omitempty"`
	CreatedAt    int64   `json:"created_at,omitzero"`
	Unmatched    bool    `json:"unmatched,omitzero"`
}

func commentJSON(row store.CommentRow) CommentJSON {
	out := CommentJSON{
		Key:          row.PublicID,
		Body:         row.Body,
		Unmatched:    row.Unmatched,
		CreatedAt:    row.CreatedAt,
		RemappedFrom: row.RemappedFrom,
	}
	if row.Path.Valid {
		path := row.Path.String
		out.Path = &path
	}
	if row.Line.Valid {
		line := row.Line.Int64
		out.Line = &line
	}
	return out
}

// CommentListIn carries one comment list request.
type CommentListIn struct {
	Owner            string `path:"owner"`
	Repo             string `path:"repo"`
	Change           string `path:"change"`
	IncludeUnmatched string `query:"unmatched"`
}

// CommentListOut carries the comments of one change head.
type CommentListOut struct {
	Head     string        `json:"head"`
	Comments []CommentJSON `json:"comments"`
}

func commentListHandler(d Deps) func(context.Context, op.Principal, CommentListIn) (CommentListOut, error) {
	return func(ctx context.Context, p op.Principal, in CommentListIn) (CommentListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CommentListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CommentListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return CommentListOut{}, derr
		}
		out := CommentListOut{Comments: []CommentJSON{}}
		rerr = d.Store.Read(ctx, func(tx *store.ReadTx) error {
			row, gerr := tx.ReadChangeByKey(rc.row.ID, in.Change)
			if gerr != nil {
				return gerr
			}
			out.Head = row.HeadOid
			var rows []store.CommentRow
			var lerr error
			if in.IncludeUnmatched == "1" || strings.EqualFold(in.IncludeUnmatched, "true") {
				rows, lerr = tx.ListCommentsAtHeadIncludingUnmatched(row.ID, row.HeadOid)
			} else {
				rows, lerr = tx.ListCommentsAtHead(row.ID, row.HeadOid)
			}
			if lerr != nil {
				return lerr
			}
			for _, crow := range rows {
				out.Comments = append(out.Comments, commentJSON(crow))
			}
			return nil
		})
		if rerr != nil {
			return CommentListOut{}, storeToOp(rerr, "change was not found")
		}
		return out, nil
	}
}

// CommentCreateBody carries one new comment.
type CommentCreateBody struct {
	Path string `json:"path,omitempty" maxLength:"4096"`
	Body string `json:"body" minLength:"1" maxLength:"8192"`
	Line int64  `json:"line,omitzero"`
}

// CommentCreateIn carries one comment create request.
type CommentCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Change         string `path:"change"`
	Body           CommentCreateBody
}

// CommentCreateOut carries the created comment.
type CommentCreateOut struct {
	Comment CommentJSON `json:"comment"`
}

func commentCreateHandler(d Deps) func(context.Context, op.Principal, CommentCreateIn) (CommentCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in CommentCreateIn) (CommentCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return CommentCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return CommentCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", ""); derr != nil {
			return CommentCreateOut{}, derr
		}
		if strings.TrimSpace(in.Body.Body) == "" {
			return CommentCreateOut{}, op.Errorf(op.CodeValidation, "comment body must not be empty")
		}
		if len(in.Body.Body) > 8192 {
			return CommentCreateOut{}, op.Errorf(op.CodeValidation, "comment body is too long")
		}
		if in.Body.Path != "" {
			if perr := checkTreePath(in.Body.Path); perr != nil {
				return CommentCreateOut{}, perr
			}
		}
		if in.Body.Line < 0 {
			return CommentCreateOut{}, op.Errorf(op.CodeValidation, "line must not be negative")
		}
		if in.Body.Line > 0 && in.Body.Path == "" {
			return CommentCreateOut{}, op.Errorf(op.CodeValidation, "line comments need a path")
		}
		if !rq.authed || rq.user.ID == 0 {
			return CommentCreateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		out := CommentCreateOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, gerr := tx.GetChangeByKey(rc.row.ID, in.Change)
			if gerr != nil {
				return gerr
			}
			if row.State != "open" {
				return op.Errorf(op.CodeConflict, "change %s is %s", in.Change, row.State)
			}
			crow := store.CommentRow{ChangeID: row.ID, HeadOid: row.HeadOid, Body: in.Body.Body, AuthorUserID: rq.user.ID}
			if in.Body.Path != "" {
				crow.Path = sql.NullString{String: in.Body.Path, Valid: true}
			}
			if in.Body.Line > 0 {
				crow.Line = sql.NullInt64{Int64: in.Body.Line, Valid: true}
			}
			saved, ierr := tx.InsertComment(d.now(), crow)
			if ierr != nil {
				return ierr
			}
			out.Comment = commentJSON(saved)
			return nil
		})
		if werr != nil {
			return CommentCreateOut{}, storeToOp(werr, "change was not found")
		}
		return out, nil
	}
}

// ReviewCreateBody carries one review verdict.
type ReviewCreateBody struct {
	Verdict string `json:"verdict" minLength:"1" maxLength:"16"`
	Body    string `json:"body,omitempty" maxLength:"8192"`
	Model   bool   `json:"model,omitempty"`
}

// ReviewCreateIn carries one review submit request.
type ReviewCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Change         string `path:"change"`
	Body           ReviewCreateBody
}

// ReviewCreateOut carries the recorded review.
type ReviewCreateOut struct {
	Comment CommentJSON `json:"comment"`
	Pending bool        `json:"pending_attestation"`
}

func reviewCreateHandler(d Deps) func(context.Context, op.Principal, ReviewCreateIn) (ReviewCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in ReviewCreateIn) (ReviewCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ReviewCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ReviewCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.review", ""); derr != nil {
			return ReviewCreateOut{}, derr
		}
		var kind string
		switch in.Body.Verdict {
		case "approve":
			kind = "review/approve"
		case "block":
			kind = "review/block"
		case "comment":
			kind = "review/comment"
		default:
			return ReviewCreateOut{}, op.Errorf(op.CodeValidation, "verdict must be approve, block, or comment")
		}
		if !rq.authed || rq.user.ID == 0 {
			return ReviewCreateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
		}
		out := ReviewCreateOut{Pending: true}
		var headOid string
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, gerr := tx.GetChangeByKey(rc.row.ID, in.Change)
			if gerr != nil {
				return gerr
			}
			if row.State != "open" {
				return op.Errorf(op.CodeConflict, "change %s is %s", in.Change, row.State)
			}
			if kind == "review/approve" && rq.user.ID == row.AuthorUserID {
				return op.Errorf(op.CodeForbidden, "self-approval by the change author is refused")
			}
			body := in.Body.Verdict
			if in.Body.Model {
				body = "model " + body
			}
			if in.Body.Body != "" {
				body = body + ": " + in.Body.Body
			}
			saved, ierr := tx.InsertComment(d.now(), store.CommentRow{
				ChangeID: row.ID, HeadOid: row.HeadOid, Body: body, AuthorUserID: rq.user.ID,
			})
			if ierr != nil {
				return ierr
			}
			out.Comment = commentJSON(saved)
			headOid = row.HeadOid
			return nil
		})
		if werr != nil {
			return ReviewCreateOut{}, storeToOp(werr, "change was not found")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return ReviewCreateOut{}, oerr
		}
		args := map[string]any{
			"subject": headOid,
			"kind":    kind,
		}
		var reply map[string]any
		if aerr := review.PendingAttestAdd(ctx, reviewCaller{d}, daemon, args, &reply); aerr != nil {
			return ReviewCreateOut{}, mapRukError(aerr)
		}
		out.Pending = false
		return out, nil
	}
}

// ChangeStateBody carries one state transition.
type ChangeStateBody struct {
	State string `json:"state" minLength:"1" maxLength:"16"`
}

// ChangeStateIn carries one change state request.
type ChangeStateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Change         string `path:"change"`
	Body           ChangeStateBody
}

// ChangeStateOut carries the moved change.
type ChangeStateOut struct {
	Change ChangeJSON `json:"change"`
}

func changeStateHandler(d Deps) func(context.Context, op.Principal, ChangeStateIn) (ChangeStateOut, error) {
	return func(ctx context.Context, p op.Principal, in ChangeStateIn) (ChangeStateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ChangeStateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ChangeStateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", ""); derr != nil {
			return ChangeStateOut{}, derr
		}
		switch in.Body.State {
		case "open", "abandoned":
		default:
			return ChangeStateOut{}, op.Errorf(op.CodeValidation, "state must be open or abandoned")
		}
		out := ChangeStateOut{}
		werr := d.Store.Write(ctx, func(tx *store.WriteTx) error {
			row, gerr := tx.GetChangeByKey(rc.row.ID, in.Change)
			if gerr != nil {
				return gerr
			}
			if row.State == "landed" {
				return op.Errorf(op.CodeConflict, "landed change %s never reopens", in.Change)
			}
			if serr := tx.SetChangeState(d.now(), row.ID, in.Body.State); serr != nil {
				return serr
			}
			updated, gerr := tx.GetChangeForUpdate(row.ID)
			if gerr != nil {
				return gerr
			}
			out.Change = changeJSON(updated)
			return nil
		})
		if werr != nil {
			return ChangeStateOut{}, storeToOp(werr, "change was not found")
		}
		return out, nil
	}
}

// ProofListIn carries one proof list request.
type ProofListIn struct {
	Owner             string `path:"owner"`
	Repo              string `path:"repo"`
	Change            string `path:"change"`
	Kind              string `query:"kind"`
	IncludeSuperseded string `query:"include_superseded"`
	Limit             int    `query:"limit"`
}

// AttestationJSON is one live attestation.
type AttestationJSON struct {
	Kind   string `json:"kind"`
	Result string `json:"result"`
	Head   string `json:"head"`
	Live   bool   `json:"live"`
}

// ProofListOut carries live attestations of a change head.
type ProofListOut struct {
	Head   string            `json:"head"`
	Proofs []AttestationJSON `json:"proofs"`
}

func proofListHandler(d Deps) func(context.Context, op.Principal, ProofListIn) (ProofListOut, error) {
	return func(ctx context.Context, p op.Principal, in ProofListIn) (ProofListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ProofListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ProofListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "change.view", ""); derr != nil {
			return ProofListOut{}, derr
		}
		head, ok := changeHead(ctx, d, rc, in.Change)
		if !ok {
			return ProofListOut{}, op.Errorf(op.CodeNotFound, "change was not found")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return ProofListOut{}, oerr
		}
		limit := clampLimit(in.Limit, 100, 1000)
		args := map[string]any{"subject": head, "page": map[string]any{"limit": limit}}
		if in.Kind != "" {
			args["kind"] = in.Kind
		}
		var reply attestListReply
		if rerr := d.callRuk(ctx, daemon, wire.MethodAttestList, args, &reply); rerr != nil {
			return ProofListOut{}, rerr
		}
		out := ProofListOut{Head: head, Proofs: []AttestationJSON{}}
		for _, record := range reply.Records.Items {
			out.Proofs = append(out.Proofs, AttestationJSON{
				Kind: record.Kind, Result: record.Result, Head: head, Live: record.Status == "live",
			})
		}
		return out, nil
	}
}

func storeToOp(err error, notFound string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return op.Errorf(op.CodeNotFound, "%s", notFound)
	}
	if rerr, ok := errors.AsType[*ruk.Error](err); ok {
		return mapRukError(rerr)
	}
	if oerr, ok := errors.AsType[*op.Error](err); ok {
		return oerr
	}
	return op.Wrap(op.CodeInternal, err, "review store failed")
}
