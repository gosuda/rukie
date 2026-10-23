package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

// This file lands F14: the admin operation surface. Every op here requires
// ScopeAdminWrite plus a site admin, and every mutation writes a domain
// audit row. Reads are safe; none of them is served from a cache.

// ---------------------------------------------------------------------------
// admin.user.delete
// ---------------------------------------------------------------------------

type AdminDeleteBody struct {
	Confirm string `json:"confirm"`
}

type AdminDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	User           string `path:"user"`
	Body           AdminDeleteBody
}

type AdminDeleteOut struct {
	Name       string `json:"name"`
	PurgeAfter int64  `json:"purge_after"`
}

// ---------------------------------------------------------------------------
// admin.repo.list / admin.repo.purge / admin.repo.verify
// ---------------------------------------------------------------------------

type AdminRepoListIn struct {
	State  string `query:"state"`
	Owner  string `query:"owner"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type AdminRepoItem struct {
	Owner     string `json:"owner"`
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	State     string `json:"state"`
	ID        int64  `json:"id"`
	UpdatedAt int64  `json:"updated_at"`
}

type AdminRepoListOut struct {
	Next  string          `json:"next,omitempty"`
	Repos []AdminRepoItem `json:"repos"`
}

type AdminRepoPurgeBody struct {
	Confirm string `json:"confirm"`
}

type AdminRepoPurgeIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	ID             string `path:"id"`
	Body           AdminRepoPurgeBody
}

type AdminRepoPurgeJob struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type AdminRepoPurgeOut struct {
	Repo string            `json:"repo"`
	Job  AdminRepoPurgeJob `json:"job"`
}

type AdminRepoVerifyIn struct {
	ID string `path:"id"`
}

type AdminRepoCheck struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Passed bool   `json:"passed"`
}

type AdminRepoVerifyOut struct {
	Repo   string           `json:"repo"`
	Checks []AdminRepoCheck `json:"checks"`
	OK     bool             `json:"ok"`
}

// ---------------------------------------------------------------------------
// admin.job.list / admin.job.retry
// ---------------------------------------------------------------------------

type AdminJobListIn struct {
	Kind   string `query:"kind"`
	State  string `query:"state"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type AdminJobItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	RepoID    int64  `json:"repo_id"`
	Attempts  int64  `json:"attempts"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type AdminJobListOut struct {
	Next string         `json:"next,omitempty"`
	Jobs []AdminJobItem `json:"jobs"`
}

type AdminJobRetryIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	ID             string `path:"id"`
}

type AdminJobRetryOut struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// ---------------------------------------------------------------------------
// admin.audit.list
// ---------------------------------------------------------------------------

type AdminAuditListIn struct {
	Actor  string `query:"actor"`
	Action string `query:"action"`
	Target string `query:"target"`
	Cursor string `query:"cursor"`
	Since  int64  `query:"since"`
	Limit  int    `query:"limit"`
}

type AdminAuditItem struct {
	Actor  *int64 `json:"actor,omitempty"`
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Result string `json:"result"`
	ID     int64  `json:"id"`
	At     int64  `json:"at"`
}

type AdminAuditListOut struct {
	Next   string           `json:"next,omitempty"`
	Events []AdminAuditItem `json:"events"`
}

// ---------------------------------------------------------------------------
// admin.health.get
// ---------------------------------------------------------------------------

type AdminHealthIn struct{}

type AdminHealthStore struct {
	OK        bool  `json:"ok"`
	LatencyMs int64 `json:"latency_ms"`
}

type AdminHealthRuk struct {
	Reachable bool  `json:"reachable"`
	LatencyMs int64 `json:"latency_ms"`
}

type AdminHealthOut struct {
	Queues map[string]int64 `json:"queues"`
	Store  AdminHealthStore `json:"store"`
	Ruk    AdminHealthRuk   `json:"ruk"`
}

// ---------------------------------------------------------------------------
// admin.token.list / admin.token.create / admin.token.revoke
// ---------------------------------------------------------------------------

type AdminTokenListIn struct {
	User string `path:"user"`
}

type AdminTokenItem struct {
	Prefix     string   `json:"prefix"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  int64    `json:"expires_at"`
	CreatedAt  int64    `json:"created_at"`
	LastUsedAt int64    `json:"last_used_at,omitempty"`
}

type AdminTokenListOut struct {
	User   string           `json:"user"`
	Tokens []AdminTokenItem `json:"tokens"`
}

type AdminTokenCreateBody struct {
	Name   string  `json:"name,omitempty" maxLength:"64"`
	Scopes string  `json:"scopes"`
	TTL    string  `json:"ttl,omitempty"`
	Repos  []int64 `json:"repos,omitempty"`
}

type AdminTokenCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	User           string `path:"user"`
	Body           AdminTokenCreateBody
}

type AdminTokenCreateOut struct {
	Prefix    string   `json:"prefix"`
	Secret    string   `json:"secret,omitempty"`
	Scopes    []string `json:"scopes"`
	ExpiresAt int64    `json:"expires_at"`
}

type AdminTokenRevokeBody struct {
	Prefix string `json:"prefix,omitempty"`
	ID     int64  `json:"id,omitzero"`
	Every  bool   `json:"every,omitzero"`
}

type AdminTokenRevokeIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	User           string `path:"user"`
	Body           AdminTokenRevokeBody
}

type AdminTokenRevokeOut struct {
	User    string `json:"user"`
	Revoked int    `json:"revoked"`
}

// ---------------------------------------------------------------------------
// admin.search.sync
// ---------------------------------------------------------------------------

type AdminSearchSyncIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
}

type AdminSearchSyncOut struct {
	ReposChecked int64 `json:"repos_checked"`
	ReposFixed   int64 `json:"repos_fixed"`
	UsersChecked int64 `json:"users_checked"`
	UsersFixed   int64 `json:"users_fixed"`
}

// ---------------------------------------------------------------------------
// admin.mirror.sync / admin.mirror.retry
// ---------------------------------------------------------------------------

type AdminMirrorSyncIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	ID             string `path:"id"`
}

type AdminMirrorSyncOut struct {
	ID     string `json:"id"`
	Manual bool   `json:"manual"`
}

type AdminMirrorRetryIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Run            string `path:"run"`
}

type AdminMirrorRetryOut struct {
	Run    string `json:"run"`
	Mirror string `json:"mirror"`
	Manual bool   `json:"manual"`
}

// ---------------------------------------------------------------------------
// admin.attest.verify
// ---------------------------------------------------------------------------

type AdminAttestVerifyIn struct {
	Owner   string `query:"owner"`
	Repo    string `query:"repo"`
	Subject string `query:"subject"`
}

type AdminAttestVerifyOut struct {
	Subject      string   `json:"subject"`
	Signers      []string `json:"signers,omitempty"`
	Attestations int      `json:"attestations"`
	Live         int      `json:"live"`
}

// ---------------------------------------------------------------------------
// admin.inbox.test
// ---------------------------------------------------------------------------

type AdminInboxTestBody struct {
	User string `json:"user"`
}

type AdminInboxTestIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           AdminInboxTestBody
}

type AdminInboxTestOut struct {
	User        string   `json:"user"`
	NotifiedVia []string `json:"notified_via"`
	At          int64    `json:"at"`
}

func adminRepoOwnerName(ctx context.Context, deps Deps, row store.RepoRow) (string, error) {
	st, err := requireStore(deps)
	if err != nil {
		return "", err
	}
	if row.OwnerUserID.Valid && row.OwnerUserID.Int64 != 0 {
		meta, lerr := st.LookupUserByID(ctx, row.OwnerUserID.Int64)
		if lerr != nil {
			return "", lerr
		}
		return meta.Name, nil
	}
	var name string
	if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		org, gerr := tx.GetOrg(row.OrgID)
		if gerr != nil {
			return gerr
		}
		name = org.Name
		return nil
	}); rerr != nil {
		return "", rerr
	}
	return name, nil
}

func mapAdminStore(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return op.Errorf(op.CodeNotFound, "admin target was not found")
	}
	if errors.Is(err, store.ErrInvalid) {
		return op.Errorf(op.CodeValidation, "%s", firstLine(err.Error()))
	}
	if errors.Is(err, store.ErrConflict) {
		return op.Errorf(op.CodeConflict, "%s", firstLine(err.Error()))
	}
	if errors.Is(err, store.ErrStale) {
		return op.Errorf(op.CodeStale, "admin target changed under this request")
	}
	return op.Wrap(op.CodeInternal, err, "admin store call failed")
}

func adminListETag(prefix string, n int, next string) string {
	return fmt.Sprintf("\"%s-%d-%s\"", prefix, n, next)
}

// adminDaemonGone reports a daemon reply that already lost the repository.
func adminDaemonGone(err error) bool {
	oerr, ok := errors.AsType[*op.Error](err)
	return ok && oerr.Code == op.CodeNotFound
}

// adminTokenRepos resolves a repo id selector to existing repositories.
func adminTokenRepos(ctx context.Context, deps Deps, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	st, err := requireStore(deps)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []int64
	if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		for _, id := range ids {
			if id <= 0 {
				return op.Errorf(op.CodeValidation, "repository selector holds an invalid id")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, gerr := tx.GetRepo(id); gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "selected repository was not found")
				}
				return gerr
			}
			out = append(out, id)
		}
		return nil
	}); rerr != nil {
		if oerr, ok := errors.AsType[*op.Error](rerr); ok {
			return nil, oerr
		}
		return nil, op.Wrap(op.CodeInternal, rerr, "token repository check failed")
	}
	return out, nil
}

// adminTokenByRow loads one token row and proves it belongs to the target.
func adminTokenByRow(ctx context.Context, deps Deps, userID, rowID int64) (store.TokenRecord, error) {
	st, err := requireStore(deps)
	if err != nil {
		return store.TokenRecord{}, err
	}
	rec, rerr := st.GetTokenByRowID(ctx, rowID)
	if rerr != nil {
		return store.TokenRecord{}, rerr
	}
	if rec.UserID != userID {
		return store.TokenRecord{}, store.ErrNotFound
	}
	return rec, nil
}

// adminJobDisplay maps the stored job state onto the admin vocabulary.
func adminJobDisplay(state string) string {
	switch state {
	case "leased":
		return "running"
	case "done":
		return "succeeded"
	default:
		return state
	}
}

// RegisterAdminF14 registers the F14 admin operations. All of them require
// ScopeAdminWrite plus a site admin; the framework enforces the scope and
// each handler enforces the admin check before touching the store.
func RegisterAdminF14(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[AdminDeleteIn, AdminDeleteOut]{
		Name: "admin.user.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users/{user}/delete",
		Scope: op.ScopeAdminWrite, Summary: "Delete a user account",
		Description: "Site admins only. Tombstones the account until its purge horizon, revokes sessions and tokens, and soft-deletes user-namespace repos. Requires a current If-Match entity tag and a confirmation naming the account. Refuses the last active admin.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Destructive: true,
		Status:      202,
		ETag: func(out AdminDeleteOut) string {
			return "admin-deleted-" + out.Name
		},
		Location: func(AdminDeleteOut) string { return "/admin/users" },
		Handler: func(ctx context.Context, p op.Principal, in AdminDeleteIn) (AdminDeleteOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminDeleteOut{}, err
			}
			purgeAfter, derr := deps.Service.DeleteUser(ctx, admin.ID, auth.DeleteUserInput{
				Target: in.User, Confirm: in.Body.Confirm, IfMatch: in.IfMatch,
			}, op.ClientIP(ctx))
			if derr != nil {
				return AdminDeleteOut{}, mapAuthError(derr)
			}
			return AdminDeleteOut{Name: in.User, PurgeAfter: purgeAfter}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminRepoListIn, AdminRepoListOut]{
		Name: "admin.repo.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/repos",
		Scope: op.ScopeAdminWrite, Summary: "List repositories for administration",
		Description: "Site admins only. Pages by update time across every namespace including soft-deleted rows on request.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		ETag: func(out AdminRepoListOut) string {
			return adminListETag("admin-repos", len(out.Repos), out.Next)
		},
		Pagination: &op.Pagination[AdminRepoListOut]{Default: 25, Max: 100, Next: func(out AdminRepoListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in AdminRepoListIn) (AdminRepoListOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminRepoListOut{}, err
			}
			state := cmp.Or(in.State, "all")
			if state != "active" && state != "deleted" && state != "forking" && state != "all" {
				return AdminRepoListOut{}, op.Errorf(op.CodeValidation, "state must be active, deleted, forking, or all")
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminRepoListOut{}, err
			}
			filter := store.RepoListFilter{State: state, Sort: "updated", IncludeDeleted: true, Limit: clampLimit(in.Limit, 25, 100), Cursor: in.Cursor}
			if in.Owner != "" {
				meta, lerr := st.LookupUserByName(ctx, strings.ToLower(in.Owner))
				if lerr != nil {
					return AdminRepoListOut{}, op.Errorf(op.CodeValidation, "unknown owner")
				}
				filter.OwnerUserID = meta.ID
			}
			var rows []store.RepoRow
			var next string
			if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
				var lerr error
				rows, next, lerr = tx.ListRepos(filter)
				return lerr
			}); rerr != nil {
				return AdminRepoListOut{}, mapAdminStore(rerr)
			}
			out := AdminRepoListOut{Next: next}
			for _, row := range rows {
				owner, oerr := adminRepoOwnerName(ctx, deps, row)
				if oerr != nil {
					return AdminRepoListOut{}, mapAdminStore(oerr)
				}
				out.Repos = append(out.Repos, AdminRepoItem{
					ID: row.ID, Owner: owner, Name: row.Name, FullName: owner + "/" + row.Name,
					State: row.State, UpdatedAt: row.UpdatedAt,
				})
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminRepoPurgeIn, AdminRepoPurgeOut]{
		Name: "admin.repo.purge", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/repos/{id}/purge",
		Scope: op.ScopeAdminWrite, Summary: "Purge a repository immediately",
		Description: "Site admins only. Removes the daemon repository and hard-deletes the row at once. Live rows are tombstoned first, so a current If-Match is always required. The confirmation must name the full owner/name. A daemon miss on a live row fails instead of purging.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq, op.CodeUnavailable},
		Concurrency: op.ConcurrencyIfMatchRequired,
		Destructive: true,
		Status:      202,
		ETag: func(out AdminRepoPurgeOut) string {
			return "admin-purged-" + out.Repo
		},
		Location: func(AdminRepoPurgeOut) string { return "/admin/repos" },
		Handler: func(ctx context.Context, p op.Principal, in AdminRepoPurgeIn) (AdminRepoPurgeOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminRepoPurgeOut{}, err
			}
			id, perr := strconv.ParseInt(in.ID, 10, 64)
			if perr != nil || id <= 0 {
				return AdminRepoPurgeOut{}, op.Errorf(op.CodeValidation, "repository id is not valid")
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminRepoPurgeOut{}, err
			}
			var row store.RepoRow
			if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
				var gerr error
				row, gerr = tx.GetRepo(id)
				return gerr
			}); rerr != nil {
				return AdminRepoPurgeOut{}, mapAdminStore(rerr)
			}
			owner, oerr := adminRepoOwnerName(ctx, deps, row)
			if oerr != nil {
				return AdminRepoPurgeOut{}, mapAdminStore(oerr)
			}
			full := owner + "/" + row.Name
			if in.Body.Confirm == "" || in.Body.Confirm != full {
				return AdminRepoPurgeOut{}, op.Errorf(op.CodeValidation, "confirmation must name the repository")
			}
			if err := matchRepoETag(in.IfMatch, owner, row); err != nil {
				return AdminRepoPurgeOut{}, err
			}
			wasDeleted := row.DeletedAt.Valid
			if !wasDeleted {
				now := deps.now()
				if werr := st.Write(ctx, func(tx *store.WriteTx) error {
					if serr := tx.SoftDeleteRepo(row.ID, now, now); serr != nil {
						return serr
					}
					tombstone, _ := marshalJSON(map[string]any{
						"owner": owner, "name": row.Name, "repo_id": row.ID,
						"purge_after": now.UnixMilli(),
					})
					if _, rerr := tx.Outbox().Emit(store.Event{
						Kind:       "repo.deleted",
						Body:       tombstone,
						OccurredAt: now,
					}); rerr != nil {
						return rerr
					}
					return nil
				}); werr != nil {
					if _, ok := errors.AsType[*op.Error](werr); ok {
						return AdminRepoPurgeOut{}, werr
					}
					return AdminRepoPurgeOut{}, mapAdminStore(werr)
				}
			}
			if row.RukPath != "" {
				daemon, oerr := deps.openDaemonRepo(ctx, row)
				if oerr != nil {
					return AdminRepoPurgeOut{}, oerr
				}
				var reply map[string]any
				if cerr := deps.callRuk(ctx, daemon, wire.MethodRepoDelete,
					map[string]any{"repo": row.RukPath, "confirm_path": row.RukPath}, &reply); cerr != nil {
					// A daemon miss only counts as already-purged when the
					// row was already a tombstone. On a live row it means a
					// bad daemon path, and failing preserves the data.
					if !wasDeleted || !adminDaemonGone(cerr) {
						return AdminRepoPurgeOut{}, mapRukError(cerr)
					}
				}
			}
			jobID := fmt.Sprintf("repo-purge:%d", row.ID)
			if werr := st.Write(ctx, func(tx *store.WriteTx) error {
				if herr := tx.HardDeleteRepo(row.ID); herr != nil {
					return herr
				}
				_ = tx.DeleteJob(jobID)
				return nil
			}); werr != nil {
				return AdminRepoPurgeOut{}, mapAdminStore(werr)
			}
			deps.Service.Audit(ctx, admin.ID, "admin.repo_purge", "repo:"+full, op.ClientIP(ctx))
			return AdminRepoPurgeOut{Repo: full, Job: AdminRepoPurgeJob{ID: jobID, State: "done"}}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminRepoVerifyIn, AdminRepoVerifyOut]{
		Name: "admin.repo.verify", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/repos/{id}/verify",
		Scope: op.ScopeAdminWrite, Summary: "Verify a repository against its daemon",
		Description: "Site admins only. Checks the store row and the daemon registration and reports every check.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeUnavailable},
		ETag: func(out AdminRepoVerifyOut) string {
			return fmt.Sprintf("\"admin-verify-%s-%v\"", out.Repo, out.OK)
		},
		Handler: func(ctx context.Context, p op.Principal, in AdminRepoVerifyIn) (AdminRepoVerifyOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminRepoVerifyOut{}, err
			}
			id, perr := strconv.ParseInt(in.ID, 10, 64)
			if perr != nil || id <= 0 {
				return AdminRepoVerifyOut{}, op.Errorf(op.CodeValidation, "repository id is not valid")
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminRepoVerifyOut{}, err
			}
			var row store.RepoRow
			if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
				var gerr error
				row, gerr = tx.GetRepo(id)
				return gerr
			}); rerr != nil {
				return AdminRepoVerifyOut{}, mapAdminStore(rerr)
			}
			owner, oerr := adminRepoOwnerName(ctx, deps, row)
			if oerr != nil {
				return AdminRepoVerifyOut{}, mapAdminStore(oerr)
			}
			full := owner + "/" + row.Name
			out := AdminRepoVerifyOut{Repo: full, OK: true}
			check := func(name string, passed bool, detail string) {
				out.Checks = append(out.Checks, AdminRepoCheck{Name: name, Passed: passed, Detail: detail})
				if !passed {
					out.OK = false
				}
			}
			check("row-present", true, "")
			check("not-deleted", !row.DeletedAt.Valid, "")
			check("daemon-path", row.RukPath != "", row.RukPath)
			if row.RukPath != "" {
				if _, oerr := deps.openDaemonRepo(ctx, row); oerr != nil {
					check("daemon-registered", false, firstLine(oerr.Error()))
				} else {
					check("daemon-registered", true, "")
				}
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminJobListIn, AdminJobListOut]{
		Name: "admin.job.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/jobs",
		Scope: op.ScopeAdminWrite, Summary: "List background jobs for administration",
		Description: "Site admins only. Pages newest first with kind and state filters. The running, succeeded, failed, and dead states map onto the stored leased, done, and dead states.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation},
		ETag: func(out AdminJobListOut) string {
			return adminListETag("admin-jobs", len(out.Jobs), out.Next)
		},
		Pagination: &op.Pagination[AdminJobListOut]{Default: 50, Max: 200, Next: func(out AdminJobListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in AdminJobListIn) (AdminJobListOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminJobListOut{}, err
			}
			if in.Kind != "" && !queue.ValidKind(queue.Kind(in.Kind)) {
				return AdminJobListOut{}, op.Errorf(op.CodeValidation, "unknown job kind")
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminJobListOut{}, err
			}
			rows, next, lerr := st.ListJobsAdmin(ctx, store.AdminJobFilter{
				Kind: in.Kind, State: cmp.Or(in.State, "all"),
				Limit: clampLimit(in.Limit, 50, 200), Cursor: in.Cursor,
			})
			if lerr != nil {
				return AdminJobListOut{}, mapAdminStore(lerr)
			}
			out := AdminJobListOut{Next: next}
			for _, job := range rows {
				out.Jobs = append(out.Jobs, AdminJobItem{
					ID: job.ID, Kind: job.Kind, RepoID: job.RepoID,
					State: adminJobDisplay(job.State), Attempts: job.Attempts,
					CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
				})
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminJobRetryIn, AdminJobRetryOut]{
		Name: "admin.job.retry", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/jobs/{id}/retry",
		Scope: op.ScopeAdminWrite, Summary: "Retry a dead background job",
		Description: "Site admins only. Re-queues a dead job with a fresh payload envelope. Refuses jobs that did not fail.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict},
		Destructive: true,
		Status:      202,
		Location:    func(AdminJobRetryOut) string { return "/admin/jobs" },
		Handler: func(ctx context.Context, p op.Principal, in AdminJobRetryIn) (AdminJobRetryOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminJobRetryOut{}, err
			}
			if in.ID == "" || len(in.ID) > 128 {
				return AdminJobRetryOut{}, op.Errorf(op.CodeValidation, "job id is not valid")
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminJobRetryOut{}, err
			}
			var job store.Job
			if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
				var gerr error
				job, gerr = tx.GetJob(in.ID)
				return gerr
			}); rerr != nil {
				return AdminJobRetryOut{}, mapAdminStore(rerr)
			}
			if job.State != "dead" {
				return AdminJobRetryOut{}, op.Errorf(op.CodeConflict, "only dead jobs can be retried")
			}
			now := deps.now()
			if werr := st.Write(ctx, func(tx *store.WriteTx) error {
				return tx.ResetJob(store.JobSpec{
					ID: job.ID, Kind: job.Kind, RepoID: job.RepoID,
					Payload: job.Payload, At: now,
				}, now)
			}); werr != nil {
				return AdminJobRetryOut{}, mapAdminStore(werr)
			}
			deps.Service.Audit(ctx, admin.ID, "admin.job_retry", "job:"+job.ID, op.ClientIP(ctx))
			return AdminJobRetryOut{ID: job.ID, State: "queued"}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminAuditListIn, AdminAuditListOut]{
		Name: "admin.audit.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/audit",
		Scope: op.ScopeAdminWrite, Summary: "List domain audit events",
		Description: "Site admins only. Pages newest first with actor, action, target, and since filters over the 365-day retention window.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation},
		ETag: func(out AdminAuditListOut) string {
			return adminListETag("admin-audit", len(out.Events), out.Next)
		},
		Pagination: &op.Pagination[AdminAuditListOut]{Default: 50, Max: 200, Next: func(out AdminAuditListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: func(ctx context.Context, p op.Principal, in AdminAuditListIn) (AdminAuditListOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminAuditListOut{}, err
			}
			filter := store.AuditFilter{
				Action: in.Action, Target: in.Target, Since: in.Since,
				Limit: clampLimit(in.Limit, 50, 200), Cursor: in.Cursor,
			}
			if in.Actor != "" {
				u, uerr := deps.Service.GetUser(ctx, in.Actor)
				if uerr != nil {
					return AdminAuditListOut{}, op.Errorf(op.CodeValidation, "unknown actor")
				}
				filter.Actor = u.ID
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminAuditListOut{}, err
			}
			rows, next, lerr := st.ListAuditEvents(ctx, filter)
			if lerr != nil {
				return AdminAuditListOut{}, mapAdminStore(lerr)
			}
			out := AdminAuditListOut{Next: next}
			for _, row := range rows {
				item := AdminAuditItem{
					ID: row.ID, At: row.At, Action: row.Action,
					Target: row.Target, Result: row.Result,
				}
				if row.ActorSet {
					id := row.Actor
					item.Actor = &id
				}
				out.Events = append(out.Events, item)
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminHealthIn, AdminHealthOut]{
		Name: "admin.health.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/health",
		Scope: op.ScopeAdminWrite, Summary: "Read instance health for administration",
		Description: "Site admins only. Never cached. Reports store latency, daemon reachability, and queue depths.",
		Problems:    []op.Code{op.CodeForbidden},
		Handler: func(ctx context.Context, p op.Principal, _ AdminHealthIn) (AdminHealthOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminHealthOut{}, err
			}
			out := AdminHealthOut{Queues: map[string]int64{}}
			// Every hop below is bounded: a slow store degrades this
			// reading, never the request.
			pctx, cancel := context.WithTimeoutCause(ctx, 5*time.Second, fmt.Errorf("admin health store ping timed out"))
			defer cancel()
			start := time.Now()
			pingErr := error(nil)
			st, serr := requireStore(deps)
			if serr != nil {
				return AdminHealthOut{}, serr
			}
			if rerr := st.Read(pctx, func(_ *store.ReadTx) error { return nil }); rerr != nil {
				pingErr = rerr
			}
			out.Store = AdminHealthStore{OK: pingErr == nil, LatencyMs: time.Since(start).Milliseconds()}
			rstart := time.Now()
			out.Ruk = AdminHealthRuk{Reachable: deps.Ruk != nil, LatencyMs: time.Since(rstart).Milliseconds()}
			if counts, cerr := st.CountJobsByState(pctx); cerr == nil {
				for state, n := range counts {
					out.Queues[adminJobDisplay(state)] = n
				}
			}
			for _, want := range []string{"queued", "running", "succeeded", "dead"} {
				if _, ok := out.Queues[want]; !ok {
					out.Queues[want] = 0
				}
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminTokenListIn, AdminTokenListOut]{
		Name: "admin.token.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/users/{user}/tokens",
		Scope: op.ScopeAdminWrite, Summary: "List another user's agent tokens",
		Description: "Site admins only. Shows token prefixes, scopes, and expiry. Secrets never leave the minting response.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeNotFound},
		ETag: func(out AdminTokenListOut) string {
			return adminListETag("admin-tokens", len(out.Tokens), out.User)
		},
		Handler: func(ctx context.Context, p op.Principal, in AdminTokenListIn) (AdminTokenListOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminTokenListOut{}, err
			}
			target, terr := deps.Service.GetUser(ctx, in.User)
			if terr != nil {
				return AdminTokenListOut{}, mapAuthError(terr)
			}
			tokens, terr := deps.Service.ListTokens(ctx, target.ID)
			if terr != nil {
				return AdminTokenListOut{}, mapAdminStore(terr)
			}
			out := AdminTokenListOut{User: target.Name}
			for _, tok := range tokens {
				out.Tokens = append(out.Tokens, AdminTokenItem{
					Prefix: tok.PublicPrefix, Name: tok.Name, Scopes: auth.ScopeNames(tok.Scopes),
					ExpiresAt: tok.ExpiresAt, CreatedAt: tok.CreatedAt, LastUsedAt: tok.LastUsedAt,
				})
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminTokenCreateIn, AdminTokenCreateOut]{
		Name: "admin.token.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users/{user}/tokens",
		Scope: op.ScopeAdminWrite, Summary: "Mint an agent token for another user",
		Description: "Site admins only. The secret is shown once. A repo selector narrows the token to listed repositories.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		Destructive: true,
		Status:      201,
		Location:    func(AdminTokenCreateOut) string { return "/admin/audit" },
		Handler: func(ctx context.Context, p op.Principal, in AdminTokenCreateIn) (AdminTokenCreateOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminTokenCreateOut{}, err
			}
			target, terr := deps.Service.GetUser(ctx, in.User)
			if terr != nil {
				return AdminTokenCreateOut{}, mapAuthError(terr)
			}
			scopes, serr := auth.ParseScopeSet(strings.Fields(in.Body.Scopes))
			if serr != nil {
				return AdminTokenCreateOut{}, mapAuthError(serr)
			}
			if !target.IsAdmin && (scopes.Has(op.ScopeAdminWrite) || scopes.Has(op.ScopeAdminRead)) {
				return AdminTokenCreateOut{}, op.Errorf(op.CodeForbidden, "admin scope requires an admin account")
			}
			var ttl time.Duration
			if in.Body.TTL != "" {
				ttl, serr = time.ParseDuration(in.Body.TTL)
				if serr != nil {
					return AdminTokenCreateOut{}, op.Errorf(op.CodeValidation, "unparseable ttl")
				}
			}
			// Site admins mint for the target, not themselves: only the
			// repository's existence is checked, never the caller's access.
			// Visibility is enforced when the token is used.
			repos, verr := adminTokenRepos(ctx, deps, in.Body.Repos)
			if verr != nil {
				return AdminTokenCreateOut{}, verr
			}
			minted, merr := deps.Service.CreateToken(ctx, auth.CreateTokenInput{
				UserID: target.ID, Name: in.Body.Name, Scopes: scopes, TTL: ttl, Repos: repos,
			})
			if merr != nil {
				return AdminTokenCreateOut{}, mapAuthError(merr)
			}
			deps.Service.Audit(ctx, admin.ID, "admin.token_create", "token:"+minted.Token.PublicPrefix, op.ClientIP(ctx))
			return AdminTokenCreateOut{
				Prefix: minted.Token.PublicPrefix, Secret: minted.Secret,
				Scopes: auth.ScopeNames(minted.Token.Scopes), ExpiresAt: minted.Token.ExpiresAt,
			}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminTokenRevokeIn, AdminTokenRevokeOut]{
		Name: "admin.token.revoke", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/users/{user}/tokens/revoke",
		Scope: op.ScopeAdminWrite, Summary: "Revoke another user's agent tokens",
		Description: "Site admins only. Revokes by prefix, by row id, or every token of the account. At least one selector is required. The every flag is spelled every because all is a global CLI flag.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		Destructive: true,
		Location:    func(AdminTokenRevokeOut) string { return "/admin/audit" },
		Handler: func(ctx context.Context, p op.Principal, in AdminTokenRevokeIn) (AdminTokenRevokeOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminTokenRevokeOut{}, err
			}
			target, terr := deps.Service.GetUser(ctx, in.User)
			if terr != nil {
				return AdminTokenRevokeOut{}, mapAuthError(terr)
			}
			set := 0
			if in.Body.Prefix != "" {
				set++
			}
			if in.Body.ID != 0 {
				set++
			}
			if in.Body.Every {
				set++
			}
			if set != 1 {
				return AdminTokenRevokeOut{}, op.Errorf(op.CodeValidation, "give exactly one of prefix, id, or every")
			}
			var revoked int
			switch {
			case in.Body.Every:
				n, rerr := deps.Service.RevokeUserTokens(ctx, target.ID, "admin.token_revoke")
				if rerr != nil {
					return AdminTokenRevokeOut{}, mapAuthError(rerr)
				}
				revoked = int(n)
			case in.Body.Prefix != "":
				ok, rerr := deps.Service.RevokeTokenByPrefix(ctx, target.ID, in.Body.Prefix, op.ClientIP(ctx))
				if rerr != nil {
					return AdminTokenRevokeOut{}, mapAuthError(rerr)
				}
				if !ok {
					return AdminTokenRevokeOut{}, op.Errorf(op.CodeNotFound, "unknown token")
				}
				revoked = 1
			default:
				rec, gerr := adminTokenByRow(ctx, deps, target.ID, in.Body.ID)
				if gerr != nil {
					return AdminTokenRevokeOut{}, mapAdminStore(gerr)
				}
				ok, rerr := deps.Service.RevokeTokenByPrefix(ctx, target.ID, rec.PublicPrefix, op.ClientIP(ctx))
				if rerr != nil {
					return AdminTokenRevokeOut{}, mapAuthError(rerr)
				}
				if !ok {
					return AdminTokenRevokeOut{}, op.Errorf(op.CodeNotFound, "unknown token")
				}
				revoked = 1
			}
			deps.Service.Audit(ctx, admin.ID, "admin.token_revoke", "user:"+target.Name, op.ClientIP(ctx))
			return AdminTokenRevokeOut{User: target.Name, Revoked: revoked}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminSearchSyncIn, AdminSearchSyncOut]{
		Name: "admin.search.sync", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/search/sync",
		Scope: op.ScopeAdminWrite, Summary: "Reconcile the search index with the store",
		Description: "Site admins only. Heals FTS rows missing for live repos and users, then reports what it checked and fixed.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation},
		Location:    func(AdminSearchSyncOut) string { return "/admin/monitor" },
		Handler: func(ctx context.Context, p op.Principal, _ AdminSearchSyncIn) (AdminSearchSyncOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminSearchSyncOut{}, err
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminSearchSyncOut{}, err
			}
			rep, serr := st.SyncSearchIndex(ctx)
			if serr != nil {
				return AdminSearchSyncOut{}, mapAdminStore(serr)
			}
			deps.Service.Audit(ctx, admin.ID, "admin.search_sync", "search:fts", op.ClientIP(ctx))
			return AdminSearchSyncOut{
				ReposChecked: rep.ReposChecked, ReposFixed: rep.ReposFixed,
				UsersChecked: rep.UsersChecked, UsersFixed: rep.UsersFixed,
			}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminMirrorSyncIn, AdminMirrorSyncOut]{
		Name: "admin.mirror.sync", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/mirrors/{id}/sync",
		Scope: op.ScopeAdminWrite, Summary: "Flag a push mirror for a manual sync",
		Description: "Site admins only. Marks the mirror manual-due so the scheduler picks it up on the next pass. Refuses disabled mirrors and throttled repeats.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict},
		Destructive: true,
		Status:      202,
		Location:    func(AdminMirrorSyncOut) string { return "/admin/monitor" },
		Handler: func(ctx context.Context, p op.Principal, in AdminMirrorSyncIn) (AdminMirrorSyncOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminMirrorSyncOut{}, err
			}
			id, perr := strconv.ParseInt(in.ID, 10, 64)
			if perr != nil || id <= 0 {
				return AdminMirrorSyncOut{}, op.Errorf(op.CodeValidation, "mirror id is not valid")
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminMirrorSyncOut{}, err
			}
			var row store.MirrorRow
			if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
				var gerr error
				row, gerr = tx.GetMirror(id)
				return gerr
			}); rerr != nil {
				return AdminMirrorSyncOut{}, mapAdminStore(rerr)
			}
			if !row.Enabled {
				return AdminMirrorSyncOut{}, op.Errorf(op.CodeConflict, "mirror is disabled")
			}
			nowMs := deps.now().UnixMilli()
			if row.LastManualAt != 0 && nowMs-row.LastManualAt < manualSyncThrottleMs {
				return AdminMirrorSyncOut{}, op.Errorf(op.CodeConflict, "manual sync was throttled")
			}
			now := deps.now()
			if werr := st.Write(ctx, func(tx *store.WriteTx) error {
				return tx.SetMirrorManual(now, row.ID)
			}); werr != nil {
				return AdminMirrorSyncOut{}, mapAdminStore(werr)
			}
			deps.Service.Audit(ctx, admin.ID, "admin.mirror_sync", "mirror:"+row.PublicID, op.ClientIP(ctx))
			return AdminMirrorSyncOut{ID: row.PublicID, Manual: true}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminMirrorRetryIn, AdminMirrorRetryOut]{
		Name: "admin.mirror.retry", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/mirror/runs/{run}/retry",
		Scope: op.ScopeAdminWrite, Summary: "Retry a failed mirror run",
		Description: "Site admins only. Flags the mirror manual-due again so the scheduler re-runs it. Refuses runs that did not fail.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict},
		Destructive: true,
		Status:      202,
		Location:    func(AdminMirrorRetryOut) string { return "/admin/monitor" },
		Handler: func(ctx context.Context, p op.Principal, in AdminMirrorRetryIn) (AdminMirrorRetryOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminMirrorRetryOut{}, err
			}
			if in.Run == "" {
				return AdminMirrorRetryOut{}, op.Errorf(op.CodeValidation, "mirror run is not valid")
			}
			st, err := requireStore(deps)
			if err != nil {
				return AdminMirrorRetryOut{}, err
			}
			var run store.MirrorRunRow
			if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
				var gerr error
				run, gerr = tx.GetRunByPublicID(in.Run)
				return gerr
			}); rerr != nil {
				return AdminMirrorRetryOut{}, mapAdminStore(rerr)
			}
			if run.State != "failed" {
				return AdminMirrorRetryOut{}, op.Errorf(op.CodeConflict, "only failed mirror runs can be retried")
			}
			var row store.MirrorRow
			if rerr := st.Read(ctx, func(tx *store.ReadTx) error {
				var gerr error
				row, gerr = tx.GetMirror(run.MirrorID)
				return gerr
			}); rerr != nil {
				return AdminMirrorRetryOut{}, mapAdminStore(rerr)
			}
			if !row.Enabled {
				return AdminMirrorRetryOut{}, op.Errorf(op.CodeConflict, "mirror is disabled")
			}
			now := deps.now()
			if werr := st.Write(ctx, func(tx *store.WriteTx) error {
				return tx.SetMirrorManual(now, row.ID)
			}); werr != nil {
				return AdminMirrorRetryOut{}, mapAdminStore(werr)
			}
			deps.Service.Audit(ctx, admin.ID, "admin.mirror_retry", "mirror:"+row.PublicID, op.ClientIP(ctx))
			return AdminMirrorRetryOut{Run: run.PublicID, Mirror: row.PublicID, Manual: true}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminAttestVerifyIn, AdminAttestVerifyOut]{
		Name: "admin.attest.verify", Kind: op.KindRead, Method: http.MethodGet, Path: "/admin/attestations/verify",
		Scope: op.ScopeAdminWrite, Summary: "Verify attestations for one subject",
		Description: "Site admins only. Lists the daemon attestations for a commit subject and reports how many are live.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeInvalid, op.CodeNotFound, op.CodeUnavailable},
		ETag: func(out AdminAttestVerifyOut) string {
			return fmt.Sprintf("\"admin-attest-%s-%d-%d\"", out.Subject, out.Attestations, out.Live)
		},
		Handler: func(ctx context.Context, p op.Principal, in AdminAttestVerifyIn) (AdminAttestVerifyOut, error) {
			if _, err := requireAdmin(ctx, deps.Service, p); err != nil {
				return AdminAttestVerifyOut{}, err
			}
			if in.Owner == "" || in.Repo == "" {
				return AdminAttestVerifyOut{}, op.Errorf(op.CodeValidation, "owner and repo are required")
			}
			if in.Subject == "" {
				return AdminAttestVerifyOut{}, op.Errorf(op.CodeValidation, "subject is required")
			}
			rq, rerr := deps.requester(ctx, p)
			if rerr != nil {
				return AdminAttestVerifyOut{}, rerr
			}
			rc, cerr := deps.resolveRepo(ctx, rq, in.Owner, in.Repo, false)
			if cerr != nil {
				return AdminAttestVerifyOut{}, cerr
			}
			daemon, oerr := deps.openDaemonRepo(ctx, rc.row)
			if oerr != nil {
				return AdminAttestVerifyOut{}, oerr
			}
			var reply attestListReply
			if rerr := deps.callRuk(ctx, daemon, wire.MethodAttestList,
				map[string]any{"subject": in.Subject, "page": map[string]any{"limit": 100}}, &reply); rerr != nil {
				return AdminAttestVerifyOut{}, mapRukError(rerr)
			}
			out := AdminAttestVerifyOut{Subject: in.Subject}
			seen := map[string]bool{}
			for _, record := range reply.Records.Items {
				out.Attestations++
				if record.Status == "live" {
					out.Live++
				}
				if record.Signer != "" && !seen[record.Signer] {
					seen[record.Signer] = true
					out.Signers = append(out.Signers, record.Signer)
				}
			}
			return out, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[AdminInboxTestIn, AdminInboxTestOut]{
		Name: "admin.inbox.test", Kind: op.KindMutation, Method: http.MethodPost, Path: "/admin/inbox/test",
		Scope: op.ScopeAdminWrite, Summary: "Send a test admin notice to a user",
		Description: "Site admins only. No inbox store ships in v1, so the test notice lands in the domain audit log, where admin.audit.list shows it immediately.",
		Problems:    []op.Code{op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		Destructive: true,
		Status:      202,
		Location:    func(AdminInboxTestOut) string { return "/admin/audit" },
		Handler: func(ctx context.Context, p op.Principal, in AdminInboxTestIn) (AdminInboxTestOut, error) {
			admin, err := requireAdmin(ctx, deps.Service, p)
			if err != nil {
				return AdminInboxTestOut{}, err
			}
			if in.Body.User == "" {
				return AdminInboxTestOut{}, op.Errorf(op.CodeValidation, "user is required")
			}
			target, terr := deps.Service.GetUser(ctx, in.Body.User)
			if terr != nil {
				return AdminInboxTestOut{}, mapAuthError(terr)
			}
			at := deps.now().UnixMilli()
			deps.Service.Audit(ctx, admin.ID, "admin.inbox_test", "user:"+target.Name, op.ClientIP(ctx))
			return AdminInboxTestOut{User: target.Name, NotifiedVia: []string{"audit"}, At: at}, nil
		},
	}); err != nil {
		return err
	}
	return nil
}

// RegisterAdminF14Views binds the F14 operations to the Views contract:
// page views for the reads, forms for the mutations.
func RegisterAdminF14Views(v *op.Views) {
	op.PageView(v, "admin.repo.list", func(w io.Writer, c op.ViewCtx, out AdminRepoListOut) error {
		var b strings.Builder
		b.WriteString(heading("Repositories"))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<table>\n<thead><tr><th>ID</th><th>Repository</th><th>State</th><th>Updated</th></tr></thead>\n<tbody>\n")
		for _, r := range out.Repos {
			b.WriteString("<tr><td>" + esc(strconv.FormatInt(r.ID, 10)) + "</td><td>" +
				"<a href=\"/admin/repos/" + esc(strconv.FormatInt(r.ID, 10)) + "/verify\">" + esc(r.FullName) + "</a></td><td>" +
				esc(r.State) + "</td><td>" + esc(strconv.FormatInt(r.UpdatedAt, 10)) + "</td></tr>\n")
		}
		b.WriteString("</tbody>\n</table>\n")
		_, err := io.WriteString(w, pageShell("Repositories", b.String()))
		return err
	})
	op.PageView(v, "admin.repo.verify", func(w io.Writer, c op.ViewCtx, out AdminRepoVerifyOut) error {
		var b strings.Builder
		b.WriteString(heading("Verify " + out.Repo))
		b.WriteString(errorList(c.Errors))
		status := "pass"
		if !out.OK {
			status = "fail"
		}
		b.WriteString("<p>Result: " + esc(status) + "</p>\n<ul>\n")
		for _, check := range out.Checks {
			mark := "ok"
			if !check.Passed {
				mark = "FAILED"
			}
			b.WriteString("<li>" + esc(check.Name) + ": " + esc(mark))
			if check.Detail != "" {
				b.WriteString(" (" + esc(check.Detail) + ")")
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
		_, err := io.WriteString(w, pageShell("Verify "+out.Repo, b.String()))
		return err
	})
	op.PageView(v, "admin.job.list", func(w io.Writer, c op.ViewCtx, out AdminJobListOut) error {
		var b strings.Builder
		b.WriteString(heading("Background jobs"))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<table>\n<thead><tr><th>ID</th><th>Kind</th><th>State</th><th>Attempts</th></tr></thead>\n<tbody>\n")
		for _, job := range out.Jobs {
			b.WriteString("<tr><td>" + esc(job.ID) + "</td><td>" + esc(job.Kind) + "</td><td>" +
				esc(job.State) + "</td><td>" + esc(strconv.FormatInt(job.Attempts, 10)) + "</td></tr>\n")
		}
		b.WriteString("</tbody>\n</table>\n")
		_, err := io.WriteString(w, pageShell("Background jobs", b.String()))
		return err
	})
	op.PageView(v, "admin.audit.list", func(w io.Writer, c op.ViewCtx, out AdminAuditListOut) error {
		var b strings.Builder
		b.WriteString(heading("Audit log"))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<table>\n<thead><tr><th>ID</th><th>At</th><th>Action</th><th>Target</th><th>Result</th></tr></thead>\n<tbody>\n")
		for _, e := range out.Events {
			b.WriteString("<tr><td>" + esc(strconv.FormatInt(e.ID, 10)) + "</td><td>" +
				esc(strconv.FormatInt(e.At, 10)) + "</td><td>" + esc(e.Action) + "</td><td>" +
				esc(e.Target) + "</td><td>" + esc(e.Result) + "</td></tr>\n")
		}
		b.WriteString("</tbody>\n</table>\n")
		_, err := io.WriteString(w, pageShell("Audit log", b.String()))
		return err
	})
	op.PageView(v, "admin.health.get", func(w io.Writer, c op.ViewCtx, out AdminHealthOut) error {
		var b strings.Builder
		b.WriteString(heading("Instance health"))
		b.WriteString(errorList(c.Errors))
		storeState := "down"
		if out.Store.OK {
			storeState = "ok"
		}
		b.WriteString("<p>Store: " + esc(storeState) + " (" + esc(strconv.FormatInt(out.Store.LatencyMs, 10)) + " ms)</p>\n")
		rukState := "unreachable"
		if out.Ruk.Reachable {
			rukState = "reachable"
		}
		b.WriteString("<p>Daemon: " + esc(rukState) + "</p>\n")
		b.WriteString("<table>\n<thead><tr><th>Queue state</th><th>Depth</th></tr></thead>\n<tbody>\n")
		for _, state := range []string{"queued", "running", "succeeded", "dead"} {
			b.WriteString("<tr><td>" + esc(state) + "</td><td>" +
				esc(strconv.FormatInt(out.Queues[state], 10)) + "</td></tr>\n")
		}
		b.WriteString("</tbody>\n</table>\n")
		_, err := io.WriteString(w, pageShell("Instance health", b.String()))
		return err
	})
	op.PageView(v, "admin.token.list", func(w io.Writer, c op.ViewCtx, out AdminTokenListOut) error {
		var b strings.Builder
		b.WriteString(heading("Tokens for " + out.User))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<table>\n<thead><tr><th>Prefix</th><th>Name</th><th>Scopes</th><th>Expires</th></tr></thead>\n<tbody>\n")
		for _, tok := range out.Tokens {
			b.WriteString("<tr><td><code>" + esc(tok.Prefix) + "</code></td><td>" + esc(tok.Name) +
				"</td><td>" + esc(strings.Join(tok.Scopes, " ")) + "</td><td>" +
				esc(strconv.FormatInt(tok.ExpiresAt, 10)) + "</td></tr>\n")
		}
		b.WriteString("</tbody>\n</table>\n")
		_, err := io.WriteString(w, pageShell("Tokens for "+out.User, b.String()))
		return err
	})
	op.PageView(v, "admin.attest.verify", func(w io.Writer, c op.ViewCtx, out AdminAttestVerifyOut) error {
		var b strings.Builder
		b.WriteString(heading("Attestations for " + out.Subject))
		b.WriteString(errorList(c.Errors))
		b.WriteString("<p>" + esc(strconv.Itoa(out.Attestations)) + " attestations, " +
			esc(strconv.Itoa(out.Live)) + " live.</p>\n")
		if len(out.Signers) > 0 {
			b.WriteString("<ul>\n")
			for _, s := range out.Signers {
				b.WriteString("<li>" + esc(s) + "</li>\n")
			}
			b.WriteString("</ul>\n")
		}
		_, err := io.WriteString(w, pageShell("Attestations", b.String()))
		return err
	})
	v.Form("admin.user.delete", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Delete user") + errorList(c.Errors) +
			para("The account is tombstoned until its purge horizon. Sessions and tokens are revoked at once.") +
			formOpen("/admin/users/"+c.Input.Get("user")+"/delete", c.CSRF) +
			textField("confirm", "Confirm username", c.Input.Get("confirm"), false) +
			submitField("Delete user")
		_, err := io.WriteString(w, pageShell("Delete user", body))
		return err
	})
	v.Form("admin.repo.purge", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Purge repository") + errorList(c.Errors) +
			para("The daemon repository and the tombstoned row are removed at once. This cannot be undone.") +
			formOpen("/admin/repos/"+c.Input.Get("id")+"/purge", c.CSRF) +
			hiddenField("_etag", c.Input.Get("_etag")) +
			textField("confirm", "Confirm owner/name", c.Input.Get("confirm"), false) +
			submitField("Purge repository")
		_, err := io.WriteString(w, pageShell("Purge repository", body))
		return err
	})
	v.Form("admin.job.retry", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Retry job") + errorList(c.Errors) +
			para("Only dead jobs can be retried. The job returns to queued with a fresh attempt.") +
			formOpen("/admin/jobs/"+c.Input.Get("id")+"/retry", c.CSRF) +
			submitField("Retry job")
		_, err := io.WriteString(w, pageShell("Retry job", body))
		return err
	})
	v.Form("admin.token.create", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Mint token") + errorList(c.Errors) +
			formOpen("/admin/users/"+c.Input.Get("user")+"/tokens", c.CSRF) +
			textField("name", "Name", c.Input.Get("name"), false) +
			textField("scopes", "Scopes", c.Input.Get("scopes"), false) +
			textField("ttl", "TTL", c.Input.Get("ttl"), false) +
			submitField("Mint token")
		_, err := io.WriteString(w, pageShell("Mint token", body))
		return err
	})
	v.Form("admin.token.revoke", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Revoke tokens") + errorList(c.Errors) +
			para("Give exactly one of prefix, row id, or all.") +
			formOpen("/admin/users/"+c.Input.Get("user")+"/tokens/revoke", c.CSRF) +
			textField("prefix", "Prefix", c.Input.Get("prefix"), false) +
			textField("id", "Row id", c.Input.Get("id"), false) +
			checkField("every", "Revoke every token", false) +
			submitField("Revoke tokens")
		_, err := io.WriteString(w, pageShell("Revoke tokens", body))
		return err
	})
	v.Form("admin.search.sync", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Sync search index") + errorList(c.Errors) +
			para("Heal FTS rows missing for live repos and users.") +
			formOpen("/admin/search/sync", c.CSRF) +
			submitField("Sync search index")
		_, err := io.WriteString(w, pageShell("Sync search index", body))
		return err
	})
	v.Form("admin.mirror.sync", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Sync mirror") + errorList(c.Errors) +
			para("Flag the mirror manual-due so the scheduler picks it up.") +
			formOpen("/admin/mirrors/"+c.Input.Get("id")+"/sync", c.CSRF) +
			submitField("Sync mirror")
		_, err := io.WriteString(w, pageShell("Sync mirror", body))
		return err
	})
	v.Form("admin.mirror.retry", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Retry mirror run") + errorList(c.Errors) +
			para("Only failed runs can be retried.") +
			formOpen("/admin/mirror/runs/"+c.Input.Get("run")+"/retry", c.CSRF) +
			submitField("Retry mirror run")
		_, err := io.WriteString(w, pageShell("Retry mirror run", body))
		return err
	})
	v.Form("admin.inbox.test", func(w io.Writer, c op.ViewCtx) error {
		body := heading("Send test notice") + errorList(c.Errors) +
			para("The test notice lands in the domain audit log.") +
			formOpen("/admin/inbox/test", c.CSRF) +
			textField("user", "Username", c.Input.Get("user"), false) +
			submitField("Send test notice")
		_, err := io.WriteString(w, pageShell("Send test notice", body))
		return err
	})
}
