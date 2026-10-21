package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

type BranchListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Prefix string `query:"prefix"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type BranchJSON struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

type BranchListOut struct {
	Next     string       `json:"-"`
	Branches []BranchJSON `json:"branches"`
}

type BranchGetIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Branch string `path:"branch"`
}

type BranchGetOut struct {
	Branch BranchJSON `json:"branch"`
}

type BranchCreateBody struct {
	Name string `json:"name" minLength:"1" maxLength:"255"`
	From string `json:"from" minLength:"1" maxLength:"512"`
}

type BranchCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           BranchCreateBody
}

type BranchCreateOut struct {
	Branch BranchJSON `json:"branch"`
}

type BranchUpdateBody struct {
	Target string `json:"target" minLength:"1" maxLength:"512"`
	Force  bool   `json:"force,omitempty"`
}

type BranchUpdateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Branch         string `path:"branch"`
	Body           BranchUpdateBody
}

type BranchUpdateOut struct {
	Branch BranchJSON `json:"branch"`
}

type BranchDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Branch         string `path:"branch"`
}

type BranchDeleteOut struct{}

type TagListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Prefix string `query:"prefix"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type TagJSON struct {
	Commit *string `json:"commit,omitempty"`
	Name   string  `json:"name"`
	Target string  `json:"target"`
}

type TagListOut struct {
	Next string    `json:"-"`
	Tags []TagJSON `json:"tags"`
}

type TagGetIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Tag   string `path:"tag"`
}

type TagGetOut struct {
	Tag TagJSON `json:"tag"`
}

type TagCreateBody struct {
	Name    string `json:"name" minLength:"1" maxLength:"255"`
	Target  string `json:"target" minLength:"1" maxLength:"512"`
	Message string `json:"message,omitempty" maxLength:"65536"`
}

type TagCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           TagCreateBody
}

type TagCreateOut struct {
	Tag TagJSON `json:"tag"`
}

type TagDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Tag            string `path:"tag"`
}

type TagDeleteOut struct{}

const (
	headsPrefix = "refs/heads/"
	tagsPrefix  = "refs/tags/"
)

func branchRefTarget(target ruk.RefTarget) string {
	if target.Direct != nil {
		return *target.Direct
	}
	if target.Symbolic != nil {
		return *target.Symbolic
	}
	return ""
}

func (d Deps) listRefs(ctx context.Context, rc repoContext, prefix, cursor string, limit int) ([]ruk.RefEntry, string, error) {
	args := map[string]any{"prefix": prefix}
	if cursor != "" {
		args["after"] = cursor
	}
	if limit > 0 {
		args["limit"] = limit
	}
	reply, rerr := ruk.RefsList(ctx, d.Ruk, rc.daemon, ruk.RefsListArgs{
		Prefix: prefix,
		After:  optionalString(cursor),
		Limit:  limit,
	})
	if rerr != nil {
		return nil, "", mapRukError(rerr)
	}
	_ = args
	next := ""
	if reply.Refs.NextAfter != nil {
		next = *reply.Refs.NextAfter
	}
	return reply.Refs.Items, next, nil
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func shortRefName(full, prefix string) (string, bool) {
	short, ok := strings.CutPrefix(full, prefix)
	if !ok || short == "" {
		return "", false
	}
	return short, true
}

func pageRefs(entries []ruk.RefEntry, prefix string, limit int, daemonNext string) ([]ruk.RefEntry, string) {
	if limit <= 0 {
		return nil, ""
	}
	shown := make([]ruk.RefEntry, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if _, ok := shortRefName(entry.Name, prefix); !ok {
			continue
		}
		if len(shown) == limit {
			return shown, shown[len(shown)-1].Name
		}
		shown = append(shown, entry)
	}
	if len(shown) == limit {
		return shown, daemonNext
	}
	return shown, ""
}

func branchListHandler(d Deps) func(context.Context, op.Principal, BranchListIn) (BranchListOut, error) {
	return func(ctx context.Context, p op.Principal, in BranchListIn) (BranchListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BranchListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BranchListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return BranchListOut{}, derr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return BranchListOut{}, oerr
		}
		rc.daemon = daemon
		if d.Ruk == nil {
			return BranchListOut{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
		}
		limit := clampLimit(in.Limit, 30, 100)
		entries, next, lerr := d.listRefs(ctx, rc, headsPrefix+in.Prefix, in.Cursor, limit+1)
		if lerr != nil {
			return BranchListOut{}, lerr
		}
		entries, next = pageRefs(entries, headsPrefix, limit, next)
		out := BranchListOut{Branches: []BranchJSON{}}
		for _, entry := range entries {
			short, _ := shortRefName(entry.Name, headsPrefix)
			out.Branches = append(out.Branches, BranchJSON{Name: short, Target: branchRefTarget(entry.Target)})
		}
		out.Next = next
		return out, nil
	}
}

func branchGetHandler(d Deps) func(context.Context, op.Principal, BranchGetIn) (BranchGetOut, error) {
	return func(ctx context.Context, p op.Principal, in BranchGetIn) (BranchGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BranchGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BranchGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return BranchGetOut{}, derr
		}
		if berr := checkBranchName(in.Branch); berr != nil {
			return BranchGetOut{}, berr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return BranchGetOut{}, oerr
		}
		rc.daemon = daemon
		target, ok := d.exactRefTarget(ctx, rc, headsPrefix+in.Branch)
		if !ok {
			return BranchGetOut{}, op.Errorf(op.CodeNotFound, "branch was not found")
		}
		return BranchGetOut{Branch: BranchJSON{Name: in.Branch, Target: target}}, nil
	}
}

func branchCreateHandler(d Deps) func(context.Context, op.Principal, BranchCreateIn) (BranchCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in BranchCreateIn) (BranchCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BranchCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BranchCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", headsPrefix+in.Body.Name); derr != nil {
			return BranchCreateOut{}, derr
		}
		if rc.row.Archived {
			return BranchCreateOut{}, op.Errorf(op.CodeArchived, "repository is archived")
		}
		if berr := checkBranchName(in.Body.Name); berr != nil {
			return BranchCreateOut{}, berr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return BranchCreateOut{}, oerr
		}
		rc.daemon = daemon
		from, ferr := d.resolveRev(ctx, rc, in.Body.From)
		if ferr != nil {
			return BranchCreateOut{}, ferr
		}
		var reply bookmarkSetReply
		serr := d.callRuk(ctx, daemon, wire.MethodBookmarkSet, map[string]any{
			"name":     headsPrefix + in.Body.Name,
			"target":   map[string]any{"commit": map[string]any{"oid": from.commit}},
			"expected": "absent",
		}, &reply)
		if serr != nil {
			if oerr, ok := errors.AsType[*op.Error](serr); ok && oerr.Code == op.CodeConflict {
				return BranchCreateOut{}, op.Errorf(op.CodeConflict, "branch already exists")
			}
			return BranchCreateOut{}, serr
		}
		return BranchCreateOut{Branch: BranchJSON{Name: in.Body.Name, Target: reply.Target}}, nil
	}
}

type bookmarkSetReply struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

func branchUpdateHandler(d Deps) func(context.Context, op.Principal, BranchUpdateIn) (BranchUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in BranchUpdateIn) (BranchUpdateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BranchUpdateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BranchUpdateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", headsPrefix+in.Branch); derr != nil {
			return BranchUpdateOut{}, derr
		}
		if rc.row.Archived {
			return BranchUpdateOut{}, op.Errorf(op.CodeArchived, "repository is archived")
		}
		if berr := checkBranchName(in.Branch); berr != nil {
			return BranchUpdateOut{}, berr
		}
		if in.IfMatch == "" {
			return BranchUpdateOut{}, op.Errorf(op.CodePreconditionReq, "If-Match is required")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return BranchUpdateOut{}, oerr
		}
		rc.daemon = daemon
		current, ok := d.exactRefTarget(ctx, rc, headsPrefix+in.Branch)
		if !ok {
			return BranchUpdateOut{}, op.Errorf(op.CodeNotFound, "branch was not found")
		}
		if !etagMatches(in.IfMatch, "\""+current+"\"") && !etagMatches(in.IfMatch, "\"branch-"+current+"\"") {
			return BranchUpdateOut{}, op.Errorf(op.CodeStale, "branch moved under this request")
		}
		to, terr := d.resolveRev(ctx, rc, in.Body.Target)
		if terr != nil {
			return BranchUpdateOut{}, terr
		}
		var reply bookmarkSetReply
		serr := d.callRuk(ctx, daemon, wire.MethodBookmarkSet, map[string]any{
			"name":     headsPrefix + in.Branch,
			"target":   map[string]any{"commit": map[string]any{"oid": to.commit}},
			"expected": map[string]any{"present": map[string]any{"direct": current}},
		}, &reply)
		if serr != nil {
			return BranchUpdateOut{}, serr
		}
		return BranchUpdateOut{Branch: BranchJSON{Name: in.Branch, Target: reply.Target}}, nil
	}
}

func branchDeleteHandler(d Deps) func(context.Context, op.Principal, BranchDeleteIn) (BranchDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in BranchDeleteIn) (BranchDeleteOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return BranchDeleteOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return BranchDeleteOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", headsPrefix+in.Branch); derr != nil {
			return BranchDeleteOut{}, derr
		}
		if rc.row.Archived {
			return BranchDeleteOut{}, op.Errorf(op.CodeArchived, "repository is archived")
		}
		if berr := checkBranchName(in.Branch); berr != nil {
			return BranchDeleteOut{}, berr
		}
		if in.Branch == rc.row.DefaultBranch {
			return BranchDeleteOut{}, op.Errorf(op.CodeConflict, "the default branch cannot be deleted")
		}
		if in.IfMatch == "" {
			return BranchDeleteOut{}, op.Errorf(op.CodePreconditionReq, "If-Match is required")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return BranchDeleteOut{}, oerr
		}
		rc.daemon = daemon
		current, ok := d.exactRefTarget(ctx, rc, headsPrefix+in.Branch)
		if !ok {
			return BranchDeleteOut{}, op.Errorf(op.CodeNotFound, "branch was not found")
		}
		if !etagMatches(in.IfMatch, "\""+current+"\"") && !etagMatches(in.IfMatch, "\"branch-"+current+"\"") {
			return BranchDeleteOut{}, op.Errorf(op.CodeStale, "branch moved under this request")
		}
		var reply map[string]any
		derr := d.callRuk(ctx, daemon, wire.MethodBookmarkDelete, map[string]any{
			"name":     headsPrefix + in.Branch,
			"expected": map[string]any{"present": map[string]any{"direct": current}},
		}, &reply)
		if derr != nil {
			return BranchDeleteOut{}, derr
		}
		return BranchDeleteOut{}, nil
	}
}

func tagListHandler(d Deps) func(context.Context, op.Principal, TagListIn) (TagListOut, error) {
	return func(ctx context.Context, p op.Principal, in TagListIn) (TagListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TagListOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return TagListOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return TagListOut{}, derr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return TagListOut{}, oerr
		}
		rc.daemon = daemon
		if d.Ruk == nil {
			return TagListOut{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
		}
		limit := clampLimit(in.Limit, 30, 100)
		entries, next, lerr := d.listRefs(ctx, rc, tagsPrefix+in.Prefix, in.Cursor, limit+1)
		if lerr != nil {
			return TagListOut{}, lerr
		}
		entries, next = pageRefs(entries, tagsPrefix, limit, next)
		out := TagListOut{Tags: []TagJSON{}}
		for _, entry := range entries {
			short, _ := shortRefName(entry.Name, tagsPrefix)
			out.Tags = append(out.Tags, TagJSON{Name: short, Target: branchRefTarget(entry.Target)})
		}
		out.Next = next
		return out, nil
	}
}

func tagGetHandler(d Deps) func(context.Context, op.Principal, TagGetIn) (TagGetOut, error) {
	return func(ctx context.Context, p op.Principal, in TagGetIn) (TagGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TagGetOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return TagGetOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return TagGetOut{}, derr
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return TagGetOut{}, terr
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return TagGetOut{}, oerr
		}
		rc.daemon = daemon
		target, ok := d.exactRefTarget(ctx, rc, tagsPrefix+in.Tag)
		if !ok {
			return TagGetOut{}, op.Errorf(op.CodeNotFound, "tag was not found")
		}
		out := TagGetOut{Tag: TagJSON{Name: in.Tag, Target: target}}
		if commit, _, perr := d.peelTag(ctx, rc, target); perr == nil {
			out.Tag.Commit = &commit
		}
		return out, nil
	}
}

func tagCreateHandler(d Deps) func(context.Context, op.Principal, TagCreateIn) (TagCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in TagCreateIn) (TagCreateOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TagCreateOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return TagCreateOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", tagsPrefix+in.Body.Name); derr != nil {
			return TagCreateOut{}, derr
		}
		if rc.row.Archived {
			return TagCreateOut{}, op.Errorf(op.CodeArchived, "repository is archived")
		}
		if terr := checkTagName(in.Body.Name); terr != nil {
			return TagCreateOut{}, terr
		}
		if len(in.Body.Message) > 65536 {
			return TagCreateOut{}, op.Errorf(op.CodeValidation, "tag message is too long")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return TagCreateOut{}, oerr
		}
		rc.daemon = daemon
		to, terr := d.resolveRev(ctx, rc, in.Body.Target)
		if terr != nil {
			return TagCreateOut{}, terr
		}
		args := map[string]any{
			"name":     tagsPrefix + in.Body.Name,
			"target":   to.commit,
			"expected": "absent",
		}
		if in.Body.Message != "" {
			args["annotation"] = map[string]any{"message": in.Body.Message}
		}
		var reply tagSetReply
		serr := d.callRuk(ctx, daemon, wire.MethodTagSet, args, &reply)
		if serr != nil {
			if oerr, ok := errors.AsType[*op.Error](serr); ok && oerr.Code == op.CodeConflict {
				return TagCreateOut{}, op.Errorf(op.CodeConflict, "tag already exists")
			}
			return TagCreateOut{}, serr
		}
		tag := TagJSON{Name: in.Body.Name, Target: refTargetString(reply.Target)}
		if commit, _, perr := d.peelTag(ctx, rc, tag.Target); perr == nil {
			tag.Commit = &commit
		}
		return TagCreateOut{Tag: tag}, nil
	}
}

type tagSetReply struct {
	Target map[string]any `json:"target"`
	Name   string         `json:"name"`
}

func refTargetString(target map[string]any) string {
	if direct, ok := target["direct"].(string); ok {
		return direct
	}
	if symbolic, ok := target["symbolic"].(string); ok {
		return symbolic
	}
	return ""
}

func tagDeleteHandler(d Deps) func(context.Context, op.Principal, TagDeleteIn) (TagDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in TagDeleteIn) (TagDeleteOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TagDeleteOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return TagDeleteOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.push", tagsPrefix+in.Tag); derr != nil {
			return TagDeleteOut{}, derr
		}
		if rc.row.Archived {
			return TagDeleteOut{}, op.Errorf(op.CodeArchived, "repository is archived")
		}
		if terr := checkTagName(in.Tag); terr != nil {
			return TagDeleteOut{}, terr
		}
		if in.IfMatch == "" {
			return TagDeleteOut{}, op.Errorf(op.CodePreconditionReq, "If-Match is required")
		}
		daemon, oerr := d.openDaemonRepo(ctx, rc.row)
		if oerr != nil {
			return TagDeleteOut{}, oerr
		}
		rc.daemon = daemon
		current, ok := d.exactRefTarget(ctx, rc, tagsPrefix+in.Tag)
		if !ok {
			return TagDeleteOut{}, op.Errorf(op.CodeNotFound, "tag was not found")
		}
		if !etagMatches(in.IfMatch, "\""+current+"\"") && !etagMatches(in.IfMatch, "\"tag-"+current+"\"") {
			return TagDeleteOut{}, op.Errorf(op.CodeStale, "tag moved under this request")
		}
		var reply map[string]any
		derr := d.callRuk(ctx, daemon, wire.MethodTagDelete, map[string]any{
			"name":     tagsPrefix + in.Tag,
			"expected": map[string]any{"present": map[string]any{"direct": current}},
		}, &reply)
		if derr != nil {
			return TagDeleteOut{}, derr
		}
		return TagDeleteOut{}, nil
	}
}

func RegisterRefs(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[BranchListIn, BranchListOut]{
		Name: "branch.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/branches",
		Scope: op.ScopePublicRead, Summary: "List branches",
		Description: "Keyset pages over the hosted branch namespace.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out BranchListOut) string {
			return fmt.Sprintf("branches-%d-%x", len(out.Branches), etagHash(out.Next))
		},
		Pagination: &op.Pagination[BranchListOut]{Default: 30, Max: 100, Next: func(out BranchListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: branchListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[BranchGetIn, BranchGetOut]{
		Name: "branch.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/branches/{branch}",
		Scope: op.ScopePublicRead, Summary: "Show a branch",
		Description: "The branch target as stored. Slashed names arrive percent-encoded.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeInvalid, op.CodeForbidden},
		ETag:        func(out BranchGetOut) string { return "branch-" + out.Branch.Target },
		Handler:     branchGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[BranchCreateIn, BranchCreateOut]{
		Name: "branch.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/branches/create",
		Scope: op.ScopeRepoWrite, Summary: "Create a branch",
		Description: "Compare-and-swap on absence through the hosted bookmark. Needs repo:write.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeArchived},
		ETag:        func(out BranchCreateOut) string { return "branch-" + out.Branch.Target },
		Location:    func(BranchCreateOut) string { return "" },
		Handler:     branchCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[BranchUpdateIn, BranchUpdateOut]{
		Name: "branch.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/branches/{branch}/update",
		Scope: op.ScopeRepoWrite, Summary: "Move a branch",
		Description: "Compare-and-swap on the If-Match target. Needs repo:write.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq, op.CodeArchived},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusOK,
		ETag:        func(out BranchUpdateOut) string { return "branch-" + out.Branch.Target },
		Location:    func(BranchUpdateOut) string { return "" },
		Handler:     branchUpdateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[BranchDeleteIn, BranchDeleteOut]{
		Name: "branch.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/branches/{branch}/delete",
		Scope: op.ScopeRepoWrite, Summary: "Delete a branch",
		Description: "Compare-and-swap on the If-Match target. The default branch stays. Needs repo:write.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq, op.CodeArchived},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusNoContent,
		Location:    func(BranchDeleteOut) string { return "" },
		ETag:        func(BranchDeleteOut) string { return "branch-deleted" },
		Handler:     branchDeleteHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TagListIn, TagListOut]{
		Name: "tag.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/tags",
		Scope: op.ScopePublicRead, Summary: "List tags",
		Description: "Keyset pages over the hosted tag namespace.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeForbidden},
		ETag: func(out TagListOut) string {
			return fmt.Sprintf("tags-%d-%x", len(out.Tags), etagHash(out.Next))
		},
		Pagination: &op.Pagination[TagListOut]{Default: 30, Max: 100, Next: func(out TagListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: tagListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TagGetIn, TagGetOut]{
		Name: "tag.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/tags/{tag}",
		Scope: op.ScopePublicRead, Summary: "Show a tag",
		Description: "The tag target with the peeled commit when the object is annotated.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeValidation, op.CodeInvalid, op.CodeForbidden},
		ETag:        func(out TagGetOut) string { return "tag-" + out.Tag.Target },
		Handler:     tagGetHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TagCreateIn, TagCreateOut]{
		Name: "tag.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tags/create",
		Scope: op.ScopeRepoWrite, Summary: "Create a tag",
		Description: "Lightweight without a message, annotated with one. Needs repo:write.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeArchived},
		ETag:        func(out TagCreateOut) string { return "tag-" + out.Tag.Target },
		Location:    func(TagCreateOut) string { return "" },
		Handler:     tagCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TagDeleteIn, TagDeleteOut]{
		Name: "tag.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{owner}/{repo}/tags/{tag}/delete",
		Scope: op.ScopeRepoWrite, Summary: "Delete a tag",
		Description: "Compare-and-swap on the If-Match target. Needs repo:write.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq, op.CodeArchived},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Status:      http.StatusNoContent,
		Location:    func(TagDeleteOut) string { return "" },
		ETag:        func(TagDeleteOut) string { return "tag-deleted" },
		Handler:     tagDeleteHandler(deps),
	}); err != nil {
		return err
	}
	return nil
}

func RegisterRefsViews(v *op.Views) {
	op.PageView(v, "branch.list", writeBranches)
	op.PageView(v, "branch.get", writeBranch)
	v.Form("branch.create", func(w io.Writer, c op.ViewCtx) error {
		return writeBranches(w, c, BranchListOut{})
	})
	v.Form("branch.update", func(w io.Writer, c op.ViewCtx) error {
		return writeBranch(w, c, BranchGetOut{})
	})
	v.Form("branch.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeBranch(w, c, BranchGetOut{})
	})
	op.PageView(v, "tag.list", writeTags)
	op.PageView(v, "tag.get", writeTag)
	v.Form("tag.create", func(w io.Writer, c op.ViewCtx) error {
		return writeTags(w, c, TagListOut{})
	})
	v.Form("tag.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeTag(w, c, TagGetOut{})
	})
}
