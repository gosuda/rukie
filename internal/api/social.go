package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

type StarAddIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
}

type StarAddOut struct{}

type StarRemoveIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
}

type StarRemoveOut struct{}

type StarListIn struct {
	User   string `query:"user"`
	Repo   string `query:"repo"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type ForkCreateBody struct {
	TargetOwner       string `json:"target_owner,omitempty" maxLength:"39"`
	Name              string `json:"name,omitempty" maxLength:"100"`
	Visibility        string `json:"visibility,omitempty"`
	DefaultBranchOnly bool   `json:"default_branch_only,omitzero"`
}

type ForkCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           ForkCreateBody
}

type ForkCreateOut struct {
	Repo RepoJSON `json:"repo"`
}

type ForkListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type ForkPageIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
}

type StarAddPageIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
}

type StarRemovePageIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
}

type StarListPageIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type ForkCreateFormIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	Body           ForkCreateBody
}

type ForkListPageIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type ExploreReposIn struct {
	Sort   string `query:"sort"`
	Topic  string `query:"topic"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type ExploreUsersIn struct {
	Sort   string `query:"sort"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type ExploreOrgsIn struct {
	Sort   string `query:"sort"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

func starViewerID(rq requester) int64 {
	if !rq.authed || rq.caller.PublicOnly {
		return 0
	}
	return rq.userID
}

func (d Deps) requireSocialStore() (*store.Store, error) {
	if d.Store == nil {
		return nil, op.Errorf(op.CodeUnavailable, "metadata store is not configured")
	}
	return d.Store, nil
}

func (d Deps) repoDisplayOwner(ctx context.Context, st *store.Store, row store.RepoRow) (string, error) {
	if row.OrgID != 0 {
		var org store.OrgRow
		rerr := st.Read(ctx, func(tx *store.ReadTx) error {
			var ferr error
			org, ferr = tx.GetOrg(row.OrgID)
			return ferr
		})
		if rerr != nil {
			return "", op.Wrap(op.CodeInternal, rerr, "repository owner lookup failed")
		}
		return org.Name, nil
	}
	name := repoOwnerName(ctx, st, row)
	if name == "" {
		return "", op.Errorf(op.CodeInternal, "repository has no owner")
	}
	return name, nil
}

func (d Deps) starAddCore(ctx context.Context, p op.Principal, owner, repo string) error {
	st, serr := d.requireSocialStore()
	if serr != nil {
		return serr
	}
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return rerr
	}
	if !rq.authed {
		return op.Errorf(op.CodeUnauthorized, "authentication is required")
	}
	if rq.isToken {
		return op.Errorf(op.CodeForbidden, "star changes require a session")
	}
	rc, cerr := d.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		return cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "social.star", ""); derr != nil {
		return derr
	}
	now := d.now()
	werr := st.Write(ctx, func(tx *store.WriteTx) error {
		return tx.AddStar(rc.row.ID, rq.userID, now)
	})
	if werr != nil {
		return op.Wrap(op.CodeInternal, werr, "star could not be saved")
	}
	return nil
}

func (d Deps) starRemoveCore(ctx context.Context, p op.Principal, owner, repo string) error {
	st, serr := d.requireSocialStore()
	if serr != nil {
		return serr
	}
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return rerr
	}
	if !rq.authed {
		return op.Errorf(op.CodeUnauthorized, "authentication is required")
	}
	if rq.isToken {
		return op.Errorf(op.CodeForbidden, "star changes require a session")
	}
	rc, cerr := d.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		return cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "social.star", ""); derr != nil {
		return derr
	}
	werr := st.Write(ctx, func(tx *store.WriteTx) error {
		return tx.RemoveStar(rc.row.ID, rq.userID)
	})
	if werr != nil {
		return op.Wrap(op.CodeInternal, werr, "star could not be removed")
	}
	return nil
}

func starAddHandler(d Deps) func(context.Context, op.Principal, StarAddIn) (StarAddOut, error) {
	return func(ctx context.Context, p op.Principal, in StarAddIn) (StarAddOut, error) {
		if err := d.starAddCore(ctx, p, in.Owner, in.Repo); err != nil {
			return StarAddOut{}, err
		}
		return StarAddOut{}, nil
	}
}

func starRemoveHandler(d Deps) func(context.Context, op.Principal, StarRemoveIn) (StarRemoveOut, error) {
	return func(ctx context.Context, p op.Principal, in StarRemoveIn) (StarRemoveOut, error) {
		if err := d.starRemoveCore(ctx, p, in.Owner, in.Repo); err != nil {
			return StarRemoveOut{}, err
		}
		return StarRemoveOut{}, nil
	}
}

func (d Deps) listRepoStarsCore(ctx context.Context, rq requester, owner, name, cursor string, limit int) (StarListOut, error) {
	st, serr := d.requireSocialStore()
	if serr != nil {
		return StarListOut{}, serr
	}
	rc, cerr := d.resolveRepo(ctx, rq, owner, name, false)
	if cerr != nil {
		return StarListOut{}, cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "social.list", ""); derr != nil {
		return StarListOut{}, derr
	}
	var rows []store.StarRow
	var next string
	rderr := st.Read(ctx, func(tx *store.ReadTx) error {
		var lerr error
		rows, next, lerr = tx.ListStars(store.StarFilter{RepoID: rc.row.ID, ViewerUserID: starViewerID(rq), Cursor: cursor, Limit: limit})
		return lerr
	})
	if rderr != nil {
		if errors.Is(rderr, store.ErrInvalid) {
			return StarListOut{}, op.Errorf(op.CodeInvalid, "star cursor is not valid")
		}
		return StarListOut{}, op.Wrap(op.CodeInternal, rderr, "star listing failed")
	}
	out := StarListOut{Next: next, Stars: make([]StarEntry, 0, len(rows))}
	for _, row := range rows {
		out.Stars = append(out.Stars, StarEntry{
			User: row.UserName, Owner: row.OwnerName, Repo: row.RepoName, StarredAt: row.StarredAt,
		})
	}
	out.NextHref = socialHref(ctx, "/api/stars", url.Values{"repo": {owner + "/" + name}}, out.Next)
	return out, nil
}

func starListHandler(d Deps) func(context.Context, op.Principal, StarListIn) (StarListOut, error) {
	return func(ctx context.Context, p op.Principal, in StarListIn) (StarListOut, error) {
		st, serr := d.requireSocialStore()
		if serr != nil {
			return StarListOut{}, serr
		}
		user := strings.TrimSpace(in.User)
		repoFull := strings.TrimSpace(in.Repo)
		if (user == "") == (repoFull == "") {
			return StarListOut{}, op.Errorf(op.CodeValidation, "exactly one of user or repo is required")
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return StarListOut{}, rerr
		}
		viewer := starViewerID(rq)
		filter := store.StarFilter{ViewerUserID: viewer, Cursor: in.Cursor, Limit: in.Limit}
		if repoFull != "" {
			owner, name, ok := strings.Cut(repoFull, "/")
			if !ok || owner == "" || name == "" {
				return StarListOut{}, op.Errorf(op.CodeValidation, "repo must be the full owner/name")
			}
			return d.listRepoStarsCore(ctx, rq, owner, name, in.Cursor, in.Limit)
		}
		if oerr := checkOwnerName(strings.ToLower(user)); oerr != nil {
			return StarListOut{}, oerr
		}
		umeta, uerr := st.LookupUserByName(ctx, strings.ToLower(user))
		if uerr != nil {
			return StarListOut{}, op.Errorf(op.CodeNotFound, "user was not found")
		}
		filter.UserID = umeta.ID
		var rows []store.StarRow
		var next string
		rderr := st.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			rows, next, lerr = tx.ListStars(filter)
			return lerr
		})
		if rderr != nil {
			if errors.Is(rderr, store.ErrInvalid) {
				return StarListOut{}, op.Errorf(op.CodeInvalid, "star cursor is not valid")
			}
			return StarListOut{}, op.Wrap(op.CodeInternal, rderr, "star listing failed")
		}
		out := StarListOut{Next: next, Stars: make([]StarEntry, 0, len(rows))}
		for _, row := range rows {
			out.Stars = append(out.Stars, StarEntry{
				User: row.UserName, Owner: row.OwnerName, Repo: row.RepoName, StarredAt: row.StarredAt,
			})
		}
		out.NextHref = socialHref(ctx, "/api/stars", url.Values{"user": {user}}, out.Next)
		return out, nil
	}
}

type forkDestination struct {
	name    string
	display string
	userID  int64
	orgID   int64
}

func (d Deps) forkDestination(ctx context.Context, rq requester, requested string) (forkDestination, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = rq.user.Name
	}
	lower := strings.ToLower(name)
	if err := checkOwnerName(lower); err != nil {
		return forkDestination{}, err
	}
	umeta, uerr := d.Store.LookupUserByName(ctx, lower)
	if uerr == nil {
		return forkDestination{name: lower, display: umeta.Name, userID: umeta.ID}, nil
	}
	var org store.OrgRow
	rerr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		var ferr error
		org, ferr = tx.FindOrgByName(lower)
		return ferr
	})
	if rerr != nil {
		if errors.Is(rerr, store.ErrNotFound) {
			return forkDestination{}, op.Errorf(op.CodeValidation, "destination owner does not exist")
		}
		return forkDestination{}, op.Wrap(op.CodeInternal, rerr, "destination owner lookup failed")
	}
	return forkDestination{name: lower, display: org.Name, orgID: org.ID}, nil
}

func (d Deps) deleteForkStore(ctx context.Context, path string) {
	if d.Ruk == nil || path == "" {
		return
	}
	_, _ = ruk.Call[map[string]any, map[string]any](ctx, d.Ruk, ruk.Repo{}, wire.MethodRepoDelete,
		map[string]any{"repo": path, "confirm_path": path})
}

func (d Deps) forkCreateCore(ctx context.Context, p op.Principal, owner, repo string, body ForkCreateBody) (ForkCreateOut, error) {
	st, serr := d.requireSocialStore()
	if serr != nil {
		return ForkCreateOut{}, serr
	}
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return ForkCreateOut{}, rerr
	}
	if !rq.authed {
		return ForkCreateOut{}, op.Errorf(op.CodeUnauthorized, "authentication is required")
	}
	rc, cerr := d.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		return ForkCreateOut{}, cerr
	}
	dest, derr := d.forkDestination(ctx, rq, body.TargetOwner)
	if derr != nil {
		return ForkCreateOut{}, derr
	}
	if rq.isToken {
		if !rq.caller.Scopes.Has(op.ScopeRepoCreate) {
			return ForkCreateOut{}, op.Errorf(op.CodeForbidden, "token needs the repo:create scope")
		}
		if dest.orgID != 0 || dest.userID != rq.userID {
			return ForkCreateOut{}, op.Errorf(op.CodeForbidden, "tokens fork only into the token owner's namespace")
		}
	}
	if aerr := d.checkRepoAccessWithDest(rq, rc.row, "repo.fork", "", dest.userID, dest.orgID); aerr != nil {
		return ForkCreateOut{}, aerr
	}
	visibility := cmp.Or(body.Visibility, rc.row.Visibility)
	if verr := checkVisibility(visibility); verr != nil {
		return ForkCreateOut{}, verr
	}
	if !access.ForkVisibilityOK(access.Visibility(rc.row.Visibility), access.Visibility(visibility)) {
		return ForkCreateOut{}, op.Errorf(op.CodeForbidden, "a fork cannot be more visible than its source")
	}
	name := cmp.Or(strings.TrimSpace(body.Name), rc.row.Name)
	if nerr := checkRepoName(name); nerr != nil {
		return ForkCreateOut{}, nerr
	}
	if d.RepoRoot == "" {
		return ForkCreateOut{}, op.Errorf(op.CodeUnavailable, "repository root is not configured")
	}
	if d.Ruk == nil {
		return ForkCreateOut{}, op.Errorf(op.CodeUnavailable, "ruk backend is not configured")
	}
	destPath := dest.name + "/" + name + ".git"
	refs := "all"
	if body.DefaultBranchOnly {
		refs = "default-and-tags"
	}
	sourceAbs := rc.row.RukPath
	if d.RepoRoot != "" && !isAbsPath(sourceAbs) {
		sourceAbs = joinPath(d.RepoRoot, sourceAbs)
	}
	destAbs := destPath
	if d.RepoRoot != "" && !isAbsPath(destAbs) {
		destAbs = joinPath(d.RepoRoot, destAbs)
	}
	var reply map[string]any
	ferr := d.callRuk(ctx, ruk.Repo{}, wire.MethodRepoFork, map[string]any{
		"source": sourceAbs,
		"path":   destAbs,
		"refs":   refs,
	}, &reply)
	if ferr != nil {
		if oerr, ok := errors.AsType[*op.Error](ferr); ok && oerr.Code == op.CodeConflict {
			return ForkCreateOut{}, op.Errorf(op.CodeConflict, "repository name is already taken")
		}
		return ForkCreateOut{}, ferr
	}
	now := d.now()
	jobPayload, _ := marshalJSON(map[string]any{"path": destPath})
	insert := store.RepoInsert{
		Name:          name,
		Visibility:    visibility,
		DefaultBranch: cmp.Or(rc.row.DefaultBranch, "main"),
		RukPath:       destPath,
		Description:   rc.row.Description,
		Website:       rc.row.Website,
		Topics:        rc.row.Topics,
	}
	if dest.orgID != 0 {
		insert.OrgID = dest.orgID
	} else {
		insert.OwnerUserID = sql.NullInt64{Int64: dest.userID, Valid: true}
	}
	var row store.RepoRow
	jobID := "repo-fork:" + newIdem()
	werr := st.Write(ctx, func(tx *store.WriteTx) error {
		id, ierr := tx.CreateFork(store.ForkSpec{
			SourceRepoID: rc.row.ID,
			Repo:         insert,
			Job: store.JobSpec{
				ID:      jobID,
				Kind:    string(queue.KindRepoFork),
				RepoID:  rc.row.ID,
				Payload: string(jobPayload),
				At:      now,
			},
		}, now)
		if ierr != nil {
			return ierr
		}
		body, _ := marshalJSON(map[string]any{
			"owner": dest.display, "name": name, "repo_id": id,
			"visibility": visibility, "fork_of": rc.owner + "/" + rc.row.Name,
		})
		if _, eerr := tx.Outbox().Emit(store.Event{
			Kind:       "repo.created",
			Body:       body,
			OccurredAt: now,
		}); eerr != nil {
			return eerr
		}
		var gerr error
		row, gerr = tx.GetRepoForUpdate(id, false)
		return gerr
	})
	if werr != nil {
		d.deleteForkStore(ctx, destAbs)
		if isUniqueViolation(werr) {
			return ForkCreateOut{}, op.Errorf(op.CodeConflict, "repository name is already taken")
		}
		return ForkCreateOut{}, op.Wrap(op.CodeInternal, werr, "fork record failed")
	}
	if d.OnRepoCreated != nil {
		d.OnRepoCreated(row.ID)
	}
	out := repoJSON(dest.display, row)
	out.State = "forking"
	return ForkCreateOut{Repo: out}, nil
}

func forkCreateHandler(d Deps) func(context.Context, op.Principal, ForkCreateIn) (ForkCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in ForkCreateIn) (ForkCreateOut, error) {
		return d.forkCreateCore(ctx, p, in.Owner, in.Repo, in.Body)
	}
}

func (d Deps) listRepoForksCore(ctx context.Context, p op.Principal, owner, repo, cursor string, limit int) (ForkListOut, error) {
	st, serr := d.requireSocialStore()
	if serr != nil {
		return ForkListOut{}, serr
	}
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return ForkListOut{}, rerr
	}
	rc, cerr := d.resolveRepo(ctx, rq, owner, repo, false)
	if cerr != nil {
		return ForkListOut{}, cerr
	}
	if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
		return ForkListOut{}, derr
	}
	viewer := starViewerID(rq)
	var rows []store.RepoRow
	var next string
	rderr := st.Read(ctx, func(tx *store.ReadTx) error {
		var lerr error
		rows, next, lerr = tx.ListForks(rc.row.ID, viewer, cursor, limit)
		return lerr
	})
	if rderr != nil {
		if errors.Is(rderr, store.ErrInvalid) {
			return ForkListOut{}, op.Errorf(op.CodeInvalid, "fork cursor is not valid")
		}
		return ForkListOut{}, op.Wrap(op.CodeInternal, rderr, "fork listing failed")
	}
	out := ForkListOut{Next: next, Forks: make([]RepoJSON, 0, len(rows))}
	for _, row := range rows {
		display, oerr := d.repoDisplayOwner(ctx, st, row)
		if oerr != nil {
			continue
		}
		if derr := d.checkRepoAccess(rq, row, "repo.view", ""); derr != nil {
			continue
		}
		out.Forks = append(out.Forks, repoJSON(display, row))
	}
	out.NextHref = socialHref(ctx, "/api/repos/"+owner+"/"+repo+"/forks", nil, out.Next)
	return out, nil
}

func forkListHandler(d Deps) func(context.Context, op.Principal, ForkListIn) (ForkListOut, error) {
	return func(ctx context.Context, p op.Principal, in ForkListIn) (ForkListOut, error) {
		return d.listRepoForksCore(ctx, p, in.Owner, in.Repo, in.Cursor, in.Limit)
	}
}

func forkPageHandler(d Deps) func(context.Context, op.Principal, ForkPageIn) (ForkPageOut, error) {
	return func(ctx context.Context, p op.Principal, in ForkPageIn) (ForkPageOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ForkPageOut{}, rerr
		}
		rc, cerr := d.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
		if cerr != nil {
			return ForkPageOut{}, cerr
		}
		if derr := d.checkRepoAccess(rq, rc.row, "repo.view", ""); derr != nil {
			return ForkPageOut{}, derr
		}
		return ForkPageOut{Owner: rc.owner, Repo: rc.row.Name}, nil
	}
}

func exploreReposHandler(d Deps) func(context.Context, op.Principal, ExploreReposIn) (ExploreReposOut, error) {
	return func(ctx context.Context, p op.Principal, in ExploreReposIn) (ExploreReposOut, error) {
		st, serr := d.requireSocialStore()
		if serr != nil {
			return ExploreReposOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return ExploreReposOut{}, rerr
		}
		switch in.Sort {
		case "", "stars", "updated", "created", "name":
		default:
			return ExploreReposOut{}, op.Errorf(op.CodeValidation, "sort must be stars, updated, created, or name")
		}
		var rows []store.RepoRow
		var next string
		rderr := st.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			rows, next, lerr = tx.ExploreRepos(store.ExploreRepoFilter{
				ViewerUserID: starViewerID(rq),
				Sort:         in.Sort,
				Topic:        in.Topic,
				Cursor:       in.Cursor,
				Limit:        in.Limit,
			})
			return lerr
		})
		if rderr != nil {
			if errors.Is(rderr, store.ErrInvalid) {
				return ExploreReposOut{}, op.Errorf(op.CodeInvalid, "explore cursor is not valid")
			}
			return ExploreReposOut{}, op.Wrap(op.CodeInternal, rderr, "explore listing failed")
		}
		out := ExploreReposOut{Next: next, Repos: make([]RepoJSON, 0, len(rows))}
		for _, row := range rows {
			owner, oerr := d.repoDisplayOwner(ctx, st, row)
			if oerr != nil {
				continue
			}
			out.Repos = append(out.Repos, repoJSON(owner, row))
		}
		filters := url.Values{}
		if in.Sort != "" {
			filters.Set("sort", in.Sort)
		}
		if in.Topic != "" {
			filters.Set("topic", in.Topic)
		}
		out.NextHref = socialHref(ctx, "/api/explore/repos", filters, out.Next)
		return out, nil
	}
}

func exploreUsersHandler(d Deps) func(context.Context, op.Principal, ExploreUsersIn) (ExploreUsersOut, error) {
	return func(ctx context.Context, p op.Principal, in ExploreUsersIn) (ExploreUsersOut, error) {
		st, serr := d.requireSocialStore()
		if serr != nil {
			return ExploreUsersOut{}, serr
		}
		if _, rerr := d.requester(ctx, p); rerr != nil {
			return ExploreUsersOut{}, rerr
		}
		switch in.Sort {
		case "", "name", "created":
		default:
			return ExploreUsersOut{}, op.Errorf(op.CodeValidation, "sort must be name or created")
		}
		var rows []store.ExploreUserRow
		var next string
		rderr := st.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			rows, next, lerr = tx.ExploreUsers(in.Cursor, in.Limit, in.Sort)
			return lerr
		})
		if rderr != nil {
			if errors.Is(rderr, store.ErrInvalid) {
				return ExploreUsersOut{}, op.Errorf(op.CodeInvalid, "explore cursor is not valid")
			}
			return ExploreUsersOut{}, op.Wrap(op.CodeInternal, rderr, "explore user listing failed")
		}
		out := ExploreUsersOut{Next: next, Users: make([]ExploreUser, 0, len(rows))}
		for _, row := range rows {
			out.Users = append(out.Users, ExploreUser{Name: row.Name, FullName: row.FullName})
		}
		filters := url.Values{}
		if in.Sort != "" {
			filters.Set("sort", in.Sort)
		}
		out.NextHref = socialHref(ctx, "/api/explore/users", filters, out.Next)
		return out, nil
	}
}

func exploreOrgsHandler(d Deps) func(context.Context, op.Principal, ExploreOrgsIn) (ExploreOrgsOut, error) {
	return func(ctx context.Context, p op.Principal, in ExploreOrgsIn) (ExploreOrgsOut, error) {
		st, serr := d.requireSocialStore()
		if serr != nil {
			return ExploreOrgsOut{}, serr
		}
		if _, rerr := d.requester(ctx, p); rerr != nil {
			return ExploreOrgsOut{}, rerr
		}
		switch in.Sort {
		case "", "name", "created":
		default:
			return ExploreOrgsOut{}, op.Errorf(op.CodeValidation, "sort must be name or created")
		}
		var rows []store.OrgRow
		var next string
		rderr := st.Read(ctx, func(tx *store.ReadTx) error {
			var lerr error
			rows, next, lerr = tx.ExploreOrgs(in.Cursor, in.Limit, in.Sort)
			return lerr
		})
		if rderr != nil {
			if errors.Is(rderr, store.ErrInvalid) {
				return ExploreOrgsOut{}, op.Errorf(op.CodeInvalid, "explore cursor is not valid")
			}
			return ExploreOrgsOut{}, op.Wrap(op.CodeInternal, rderr, "explore org listing failed")
		}
		out := ExploreOrgsOut{Next: next, Orgs: make([]OrgJSON, 0, len(rows))}
		for _, row := range rows {
			out.Orgs = append(out.Orgs, orgJSON(row, ""))
		}
		filters := url.Values{}
		if in.Sort != "" {
			filters.Set("sort", in.Sort)
		}
		out.NextHref = socialHref(ctx, "/api/explore/orgs", filters, out.Next)
		return out, nil
	}
}

func starListETag(out StarListOut) string {
	return fmt.Sprintf("stars-%d-%s", len(out.Stars), out.Next)
}

func forkListETag(out ForkListOut) string {
	return fmt.Sprintf("forks-%d-%s", len(out.Forks), out.Next)
}

func exploreReposETag(out ExploreReposOut) string {
	return fmt.Sprintf("explore-repos-%d-%s", len(out.Repos), out.Next)
}

func exploreUsersETag(out ExploreUsersOut) string {
	return fmt.Sprintf("explore-users-%d-%s", len(out.Users), out.Next)
}

func exploreOrgsETag(out ExploreOrgsOut) string {
	return fmt.Sprintf("explore-orgs-%d-%s", len(out.Orgs), out.Next)
}

// RegisterSocial registers the star, fork, and explore operations. JSON
// callers use the /api paths; the zero-JS HTML routes are hidden aliases
// that delegate to the same handlers.
func RegisterSocial(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[StarAddIn, StarAddOut]{
		Name: "star.add", Kind: op.KindMutation, Method: http.MethodPut,
		Path:  "/api/user/stars/{owner}/{repo}",
		Scope: op.ScopeRepoRead, Summary: "Star a repository",
		Description: "Session only. Idempotent: re-starring is a no-op. The star edge and the " +
			"repository star count commit together.",
		Problems:   []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeInvalid, op.CodeValidation},
		Status:     http.StatusNoContent,
		Idempotent: true,
		Location:   func(StarAddOut) string { return "" },
		Handler:    starAddHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[StarRemoveIn, StarRemoveOut]{
		Name: "star.remove", Kind: op.KindMutation, Method: http.MethodDelete,
		Path:  "/api/user/stars/{owner}/{repo}",
		Scope: op.ScopeRepoRead, Summary: "Unstar a repository",
		Description: "Session only. Idempotent: unstarring a repository you did not star is a no-op.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeInvalid, op.CodeValidation},
		Status:      http.StatusNoContent,
		Idempotent:  true,
		Location:    func(StarRemoveOut) string { return "" },
		Handler:     starRemoveHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[StarListIn, StarListOut]{
		Name: "star.list", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/api/stars",
		Scope: op.ScopePublicRead, Summary: "List stars for a user or a repository",
		Description: "Exactly one of user or repo. Keyset pages newest first. Stars on repositories " +
			"the viewer cannot see are omitted.",
		Problems: []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation},
		ETag:     starListETag,
		Pagination: &op.Pagination[StarListOut]{Default: 30, Max: 100, Next: func(out StarListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: starListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ForkPageIn, ForkPageOut]{
		Name: "repo.fork.page", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/{owner}/{repo}/fork",
		Scope: op.ScopePublicRead, Summary: "Show the fork form",
		Description: "Page-only. Renders the zero-JS fork form.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag:        func(out ForkPageOut) string { return "fork-" + out.Owner + "-" + out.Repo },
		Hidden:      true,
		Handler:     forkPageHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ForkCreateIn, ForkCreateOut]{
		Name: "repo.fork.create", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/api/repos/{owner}/{repo}/forks",
		Scope: op.ScopeRepoCreate, Summary: "Fork a repository",
		Description: "Ruk repo.fork first, then the fork row and the repo_fork job in one metadata " +
			"transaction. The fork reads state forking until the job completes. default_branch_only " +
			"copies the default branch and tags; otherwise every ref copies. A fork cannot be more " +
			"visible than its source. Tokens fork only into the token owner's namespace.",
		Problems: []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeUnavailable},
		Status:   http.StatusAccepted,
		Location: func(out ForkCreateOut) string { return "/" + out.Repo.Owner + "/" + out.Repo.Name },
		ETag:     func(out ForkCreateOut) string { return repoOutETag(out.Repo) },
		Handler:  forkCreateHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ForkListIn, ForkListOut]{
		Name: "repo.fork.list", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/api/repos/{owner}/{repo}/forks",
		Scope: op.ScopePublicRead, Summary: "List the forks of a repository",
		Description: "Keyset pages newest first. Forks the viewer cannot see are omitted.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag:        forkListETag,
		Pagination: &op.Pagination[ForkListOut]{Default: 30, Max: 100, Next: func(out ForkListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: forkListHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ExploreReposIn, ExploreReposOut]{
		Name: "explore.repos", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/api/explore/repos",
		Scope: op.ScopePublicRead, Summary: "Explore repositories",
		Description: "Visibility-filtered keyset pages. Anonymous callers see public repositories; " +
			"signed-in callers also see internal repositories and their own grants.",
		Problems: []op.Code{op.CodeInvalid, op.CodeValidation},
		ETag:     exploreReposETag,
		Pagination: &op.Pagination[ExploreReposOut]{Default: 25, Max: 100, Next: func(out ExploreReposOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: exploreReposHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ExploreUsersIn, ExploreUsersOut]{
		Name: "explore.users", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/api/explore/users",
		Scope: op.ScopePublicRead, Summary: "Explore users",
		Description: "Active accounts in keyset pages.",
		Problems:    []op.Code{op.CodeInvalid, op.CodeValidation},
		ETag:        exploreUsersETag,
		Pagination: &op.Pagination[ExploreUsersOut]{Default: 25, Max: 100, Next: func(out ExploreUsersOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: exploreUsersHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ExploreOrgsIn, ExploreOrgsOut]{
		Name: "explore.orgs", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/api/explore/orgs",
		Scope: op.ScopePublicRead, Summary: "Explore organizations",
		Description: "Public organizations in keyset pages.",
		Problems:    []op.Code{op.CodeInvalid, op.CodeValidation},
		ETag:        exploreOrgsETag,
		Pagination: &op.Pagination[ExploreOrgsOut]{Default: 25, Max: 100, Next: func(out ExploreOrgsOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: exploreOrgsHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[StarListPageIn, StarListOut]{
		Name: "star.list.page", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/{owner}/{repo}/stargazers",
		Scope: op.ScopePublicRead, Summary: "Show the stargazers page",
		Description: "Page-only. Renders the zero-JS stargazer list.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag:        starListETag,
		Hidden:      true,
		Pagination: &op.Pagination[StarListOut]{Default: 30, Max: 100, Next: func(out StarListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in StarListPageIn) (StarListOut, error) {
			rq, rerr := deps.requester(ctx, p)
			if rerr != nil {
				return StarListOut{}, rerr
			}
			out, err := deps.listRepoStarsCore(ctx, rq, in.Owner, in.Repo, in.Cursor, in.Limit)
			if err != nil {
				return StarListOut{}, err
			}
			out.NextHref = socialHref(ctx, "/"+in.Owner+"/"+in.Repo+"/stargazers", nil, out.Next)
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[StarAddPageIn, StarAddOut]{
		Name: "star.add.page", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/star",
		Scope: op.ScopeRepoRead, Summary: "Star a repository from its page",
		Description: "Page-only. The zero-JS star form posts here.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeInvalid, op.CodeValidation},
		Status:      http.StatusNoContent,
		Idempotent:  true,
		Location:    func(StarAddOut) string { return "" },
		Hidden:      true,
		Handler: func(ctx context.Context, p op.Principal, in StarAddPageIn) (StarAddOut, error) {
			if err := deps.starAddCore(ctx, p, in.Owner, in.Repo); err != nil {
				return StarAddOut{}, err
			}
			return StarAddOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[StarRemovePageIn, StarRemoveOut]{
		Name: "star.remove.page", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/unstar",
		Scope: op.ScopeRepoRead, Summary: "Unstar a repository from its page",
		Description: "Page-only. The zero-JS unstar form posts here.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeInvalid, op.CodeValidation},
		Status:      http.StatusNoContent,
		Idempotent:  true,
		Location:    func(StarRemoveOut) string { return "" },
		Hidden:      true,
		Handler: func(ctx context.Context, p op.Principal, in StarRemovePageIn) (StarRemoveOut, error) {
			if err := deps.starRemoveCore(ctx, p, in.Owner, in.Repo); err != nil {
				return StarRemoveOut{}, err
			}
			return StarRemoveOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ForkCreateFormIn, ForkCreateOut]{
		Name: "repo.fork.form", Kind: op.KindMutation, Method: http.MethodPost,
		Path:  "/{owner}/{repo}/fork",
		Scope: op.ScopeRepoCreate, Summary: "Fork a repository from its page",
		Description: "Page-only. The zero-JS fork form posts here.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeUnavailable},
		Status:      http.StatusAccepted,
		Location:    func(out ForkCreateOut) string { return "/" + out.Repo.Owner + "/" + out.Repo.Name },
		ETag:        func(out ForkCreateOut) string { return repoOutETag(out.Repo) },
		Hidden:      true,
		Handler: func(ctx context.Context, p op.Principal, in ForkCreateFormIn) (ForkCreateOut, error) {
			return deps.forkCreateCore(ctx, p, in.Owner, in.Repo, in.Body)
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ForkListPageIn, ForkListOut]{
		Name: "repo.forks.page", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/{owner}/{repo}/forks",
		Scope: op.ScopePublicRead, Summary: "Show the forks page",
		Description: "Page-only. Renders the zero-JS fork list.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeInvalid, op.CodeValidation, op.CodeForbidden},
		ETag:        forkListETag,
		Hidden:      true,
		Pagination: &op.Pagination[ForkListOut]{Default: 30, Max: 100, Next: func(out ForkListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in ForkListPageIn) (ForkListOut, error) {
			out, err := deps.listRepoForksCore(ctx, p, in.Owner, in.Repo, in.Cursor, in.Limit)
			if err != nil {
				return ForkListOut{}, err
			}
			out.NextHref = socialHref(ctx, "/"+in.Owner+"/"+in.Repo+"/forks", nil, out.Next)
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ExploreReposIn, ExploreReposOut]{
		Name: "explore.repos.page", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/-/explore/repos",
		Scope: op.ScopePublicRead, Summary: "Show the repository explore page",
		Description: "Page-only. Renders the zero-JS repository listing.",
		Problems:    []op.Code{op.CodeInvalid, op.CodeValidation},
		ETag:        exploreReposETag,
		Hidden:      true,
		Pagination: &op.Pagination[ExploreReposOut]{Default: 25, Max: 100, Next: func(out ExploreReposOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in ExploreReposIn) (ExploreReposOut, error) {
			out, err := exploreReposHandler(deps)(ctx, p, in)
			if err != nil {
				return ExploreReposOut{}, err
			}
			filters := url.Values{}
			if in.Sort != "" {
				filters.Set("sort", in.Sort)
			}
			if in.Topic != "" {
				filters.Set("topic", in.Topic)
			}
			out.NextHref = socialHref(ctx, "/-/explore/repos", filters, out.Next)
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[ExploreUsersIn, ExploreUsersOut]{
		Name: "explore.users.page", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/-/explore/users",
		Scope: op.ScopePublicRead, Summary: "Show the user explore page",
		Description: "Page-only. Renders the zero-JS user listing.",
		Problems:    []op.Code{op.CodeInvalid, op.CodeValidation},
		ETag:        exploreUsersETag,
		Hidden:      true,
		Pagination: &op.Pagination[ExploreUsersOut]{Default: 25, Max: 100, Next: func(out ExploreUsersOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in ExploreUsersIn) (ExploreUsersOut, error) {
			out, err := exploreUsersHandler(deps)(ctx, p, in)
			if err != nil {
				return ExploreUsersOut{}, err
			}
			filters := url.Values{}
			if in.Sort != "" {
				filters.Set("sort", in.Sort)
			}
			out.NextHref = socialHref(ctx, "/-/explore/users", filters, out.Next)
			return out, nil
		},
	}); err != nil {
		return err
	}
	return op.Register(t, op.Op[ExploreOrgsIn, ExploreOrgsOut]{
		Name: "explore.orgs.page", Kind: op.KindRead, Method: http.MethodGet,
		Path:  "/-/explore/orgs",
		Scope: op.ScopePublicRead, Summary: "Show the organization explore page",
		Description: "Page-only. Renders the zero-JS organization listing.",
		Problems:    []op.Code{op.CodeInvalid, op.CodeValidation},
		ETag:        exploreOrgsETag,
		Hidden:      true,
		Pagination: &op.Pagination[ExploreOrgsOut]{Default: 25, Max: 100, Next: func(out ExploreOrgsOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in ExploreOrgsIn) (ExploreOrgsOut, error) {
			out, err := exploreOrgsHandler(deps)(ctx, p, in)
			if err != nil {
				return ExploreOrgsOut{}, err
			}
			filters := url.Values{}
			if in.Sort != "" {
				filters.Set("sort", in.Sort)
			}
			out.NextHref = socialHref(ctx, "/-/explore/orgs", filters, out.Next)
			return out, nil
		},
	})
}
