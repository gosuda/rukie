package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type OrgJSON struct {
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	Name        string `json:"name"`
	Visibility  string `json:"visibility"`
	ID          int64  `json:"-"`
	Role        string `json:"role,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func orgJSON(row store.OrgRow, role string) OrgJSON {
	return OrgJSON{
		Name: row.Name, DisplayName: row.DisplayName, Description: row.Description,
		Website: row.Website, Visibility: row.Visibility,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Role: role, ID: row.ID,
	}
}

func orgETag(row store.OrgRow) string {
	return fmt.Sprintf("org-%d-%d", row.ID, row.UpdatedAt)
}

func teamETag(row store.TeamRow) string {
	return fmt.Sprintf("team-%d-%d", row.ID, row.UpdatedAt)
}

func matchOrgETag(ifMatch string, row store.OrgRow) error {
	if ifMatch == "" {
		return op.Errorf(op.CodePreconditionReq, "If-Match is required")
	}
	if !etagMatches(ifMatch, orgETag(row)) {
		return op.Errorf(op.CodeStale, "organization changed under this request")
	}
	return nil
}

func matchTeamETag(ifMatch string, row store.TeamRow) error {
	if ifMatch == "" {
		return op.Errorf(op.CodePreconditionReq, "If-Match is required")
	}
	if !etagMatches(ifMatch, teamETag(row)) {
		return op.Errorf(op.CodeStale, "team changed under this request")
	}
	return nil
}

type TeamJSON struct {
	Description  string `json:"description,omitempty"`
	Name         string `json:"name"`
	ID           int64  `json:"-"`
	Permission   string `json:"permission"`
	RepoScope    string `json:"repo_scope"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	CanCreateRep bool   `json:"can_create_repo"`
}

func teamJSON(row store.TeamRow) TeamJSON {
	return TeamJSON{
		Name: row.Name, Description: row.Description, Permission: row.Permission,
		RepoScope: row.RepoScope, CanCreateRep: row.CanCreateRep,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ID: row.ID,
	}
}

func resolveOrgByName(ctx context.Context, d Deps, name string) (store.OrgRow, error) {
	var row store.OrgRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		found, ferr := tx.FindOrgByName(name)
		if ferr == nil {
			row = found
			return nil
		}
		if !errors.Is(ferr, store.ErrNotFound) {
			return ferr
		}
		redirect, rerr := tx.FollowOrgRedirect(name)
		if rerr != nil {
			return ferr
		}
		row = redirect
		return nil
	})
	if serr != nil {
		if errors.Is(serr, store.ErrNotFound) {
			return store.OrgRow{}, op.Errorf(op.CodeNotFound, "organization was not found")
		}
		return store.OrgRow{}, op.Wrap(op.CodeInternal, serr, "organization lookup failed")
	}
	return row, nil
}

func siteAccess(d Deps, rq requester, action string) error {
	st, serr := requireStore(d)
	if serr != nil {
		return serr
	}
	resolver := st.Resolver()
	decision := access.Check(resolver, rq.caller, action, access.Resource{Kind: access.KindSite})
	if !decision.Allow {
		if decision.Reason == "session-required" {
			return op.Errorf(op.CodeUnauthorized, "session authentication is required")
		}
		if decision.Reason == "scope" {
			return op.Errorf(op.CodeForbidden, "token lacks the required scope")
		}
		return op.Errorf(op.CodeForbidden, "organization access was denied")
	}
	return nil
}

func orgAccess(d Deps, rq requester, orgID int64, action string) error {
	st, serr := requireStore(d)
	if serr != nil {
		return serr
	}
	resolver := st.Resolver()
	decision := access.Check(resolver, rq.caller, action, access.Resource{Kind: access.KindOrg, OrgID: orgID})
	if !decision.Allow {
		if decision.NotFound {
			return op.Errorf(op.CodeNotFound, "organization was not found")
		}
		if decision.Reason == "session-required" {
			return op.Errorf(op.CodeUnauthorized, "session authentication is required")
		}
		if decision.Reason == "scope" {
			return op.Errorf(op.CodeForbidden, "token lacks the required scope")
		}
		return op.Errorf(op.CodeForbidden, "organization access was denied")
	}
	return nil
}

func teamAccess(d Deps, rq requester, orgID, teamID int64, action string) error {
	if teamID == 0 {
		return orgAccess(d, rq, orgID, action)
	}
	st, serr := requireStore(d)
	if serr != nil {
		return serr
	}
	resolver := st.Resolver()
	decision := access.Check(resolver, rq.caller, action, access.Resource{Kind: access.KindTeam, OrgID: orgID, TeamID: teamID})
	if !decision.Allow {
		if decision.NotFound {
			return op.Errorf(op.CodeNotFound, "team was not found")
		}
		if decision.Reason == "session-required" {
			return op.Errorf(op.CodeUnauthorized, "session authentication is required")
		}
		if decision.Reason == "scope" {
			return op.Errorf(op.CodeForbidden, "token lacks the required scope")
		}
		return op.Errorf(op.CodeForbidden, "team access was denied")
	}
	return nil
}

func requireSessionUser(ctx context.Context, d Deps, p op.Principal) (auth.User, error) {
	userID, _, err := auth.RequireSession(p)
	if err != nil {
		return auth.User{}, mapAuthError(err)
	}
	u, uerr := d.Service.GetUserByID(ctx, userID)
	if uerr != nil {
		return auth.User{}, mapAuthError(uerr)
	}
	return u, nil
}

type OrgCreateBody struct {
	DisplayName string `json:"display_name,omitempty" maxLength:"100"`
	Description string `json:"description,omitempty" maxLength:"400"`
	Website     string `json:"website,omitempty" maxLength:"2048"`
	Name        string `json:"name" minLength:"1" maxLength:"39"`
	Visibility  string `json:"visibility,omitempty"`
}

type OrgCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           OrgCreateBody
}

type OrgCreateOut struct {
	Org OrgJSON `json:"org"`
}

type OrgNewPageOut struct{}

type OrgGetIn struct {
	Org string `path:"org"`
}

type OrgGetOut struct {
	OrgRepos []RepoJSON `json:"repos,omitempty"`
	Teams    []TeamJSON `json:"teams,omitempty"`
	Org      OrgJSON    `json:"org"`
}

type OrgListIn struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type OrgListOut struct {
	Next string    `json:"-"`
	Orgs []OrgJSON `json:"orgs"`
}

type OrgUpdateBody struct {
	DisplayName *string `json:"display_name,omitempty" maxLength:"100"`
	Description *string `json:"description,omitempty" maxLength:"400"`
	Website     *string `json:"website,omitempty" maxLength:"2048"`
	Visibility  *string `json:"visibility,omitempty"`
}

type OrgUpdateIn struct {
	Body           OrgUpdateBody
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Org            string `path:"org"`
}

type OrgUpdateOut struct {
	Org OrgJSON `json:"org"`
}

type OrgRenameBody struct {
	Name string `json:"name" minLength:"1" maxLength:"39"`
}

type OrgRenameIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Org            string `path:"org"`
	Body           OrgRenameBody
}

type OrgRenameOut struct {
	Org OrgJSON `json:"org"`
}

type OrgDeleteBody struct {
	Confirm string `json:"confirm" maxLength:"140"`
}

type OrgDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Org            string `path:"org"`
	Body           OrgDeleteBody
}

type OrgDeleteOut struct{}

type OrgSettingsPageIn struct {
	Org string `path:"org"`
}

type OrgSettingsPageOut struct {
	ETag string  `json:"etag"`
	Org  OrgJSON `json:"org"`
}

type OrgMemberJSON struct {
	Username string   `json:"username"`
	Role     string   `json:"role"`
	Teams    []string `json:"teams,omitempty"`
}

type OrgMemberListIn struct {
	Role   string `query:"role"`
	Cursor string `query:"cursor"`
	Org    string `path:"org"`
	Limit  int    `query:"limit"`
}

type OrgMemberListOut struct {
	Next    string          `json:"-"`
	Members []OrgMemberJSON `json:"members"`
}

type OrgMemberRemoveIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Org            string `path:"org"`
	User           string `path:"user"`
}

type OrgMemberRemoveOut struct{}

type OrgLeaveIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Org            string `path:"org"`
}

type OrgLeaveOut struct{}

type TeamCreateBody struct {
	Description string `json:"description,omitempty" maxLength:"400"`
	Name        string `json:"name" minLength:"1" maxLength:"64"`
	Permission  string `json:"permission,omitempty"`
	RepoScope   string `json:"repo_scope,omitempty"`
}

type TeamCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Org            string `path:"org"`
	Body           TeamCreateBody
}

type TeamCreateOut struct {
	Team TeamJSON `json:"team"`
}

type TeamGetIn struct {
	Org  string `path:"org"`
	Team string `path:"team"`
}

type TeamGetOut struct {
	Members []OrgMemberJSON `json:"members"`
	Repos   []string        `json:"repos"`
	Team    TeamJSON        `json:"team"`
}

type TeamListIn struct {
	Cursor string `query:"cursor"`
	Org    string `path:"org"`
	Limit  int    `query:"limit"`
}

type TeamListOut struct {
	Next  string     `json:"-"`
	Teams []TeamJSON `json:"teams"`
}

type TeamUpdateBody struct {
	Description *string `json:"description,omitempty" maxLength:"400"`
	Name        *string `json:"name,omitempty" minLength:"1" maxLength:"64"`
	Permission  *string `json:"permission,omitempty"`
	RepoScope   *string `json:"repo_scope,omitempty"`
}

type TeamUpdateIn struct {
	Body           TeamUpdateBody
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Org            string `path:"org"`
	Team           string `path:"team"`
}

type TeamUpdateOut struct {
	Team TeamJSON `json:"team"`
}

type TeamDeleteIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	IfMatch        string `header:"If-Match"`
	Org            string `path:"org"`
	Team           string `path:"team"`
}

type TeamDeleteOut struct{}

type TeamMemberListIn struct {
	Cursor string `query:"cursor"`
	Org    string `path:"org"`
	Team   string `path:"team"`
	Limit  int    `query:"limit"`
}

type TeamMemberListOut struct {
	Next    string          `json:"-"`
	Members []OrgMemberJSON `json:"members"`
}

type TeamMemberAddBody struct {
	User string `json:"user" minLength:"1" maxLength:"39"`
}

type TeamMemberAddIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Org            string `path:"org"`
	Team           string `path:"team"`
	Body           TeamMemberAddBody
}

type TeamMemberAddOut struct{}

type TeamMemberRemoveIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Org            string `path:"org"`
	Team           string `path:"team"`
	User           string `path:"user"`
}

type TeamMemberRemoveOut struct{}

type TeamRepoListIn struct {
	Cursor string `query:"cursor"`
	Org    string `path:"org"`
	Team   string `path:"team"`
	Limit  int    `query:"limit"`
}

type TeamRepoListOut struct {
	Next  string   `json:"-"`
	Repos []string `json:"repos"`
}

type TeamRepoAddBody struct {
	Repo string `json:"repo" minLength:"1" maxLength:"100"`
}

type TeamRepoAddIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Org            string `path:"org"`
	Team           string `path:"team"`
	Body           TeamRepoAddBody
}

type TeamRepoAddOut struct{}

type TeamRepoRemoveIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Org            string `path:"org"`
	Team           string `path:"team"`
	Repo           string `path:"repo"`
}

type TeamRepoRemoveOut struct{}

func checkOrgName(name string) error {
	if err := auth.CheckUsername(name); err != nil {
		return op.Errorf(op.CodeInvalid, "organization name is not valid")
	}
	return nil
}

func checkTeamName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return op.Errorf(op.CodeValidation, "team name must be 1 to 64 characters")
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return op.Errorf(op.CodeValidation, "team name must be lowercase alphanumeric, dash, or underscore")
	}
	return nil
}

func checkTeamPermission(permission string) error {
	if _, ok := access.ParseRole(permission); !ok || permission == "none" {
		return op.Errorf(op.CodeValidation, "team permission must be read, triage, write, maintain, or admin")
	}
	return nil
}

func checkRepoScope(scope string) error {
	if scope != "all" && scope != "selected" {
		return op.Errorf(op.CodeValidation, "team repo scope must be all or selected")
	}
	return nil
}

func checkVisibilityValue(visibility string) error {
	if visibility != "public" && visibility != "private" {
		return op.Errorf(op.CodeValidation, "visibility must be public or private")
	}
	return nil
}

func orgRoleOf(d Deps, orgID, userID int64) string {
	if d.Store == nil {
		return ""
	}
	resolver := d.Store.Resolver()
	if resolver.IsOrgOwner(orgID, userID) {
		return "owner"
	}
	if resolver.IsOrgMember(orgID, userID) {
		return "member"
	}
	return ""
}

func createOrgHandler(d Deps) func(context.Context, op.Principal, OrgCreateIn) (OrgCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgCreateIn) (OrgCreateOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return OrgCreateOut{}, serr
		}
		user, uerr := requireSessionUser(ctx, d, p)
		if uerr != nil {
			return OrgCreateOut{}, uerr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgCreateOut{}, rerr
		}
		if derr := siteAccess(d, rq, "org.create"); derr != nil {
			return OrgCreateOut{}, derr
		}
		body := in.Body
		name := strings.ToLower(strings.TrimSpace(body.Name))
		if err := checkOrgName(name); err != nil {
			return OrgCreateOut{}, err
		}
		if _, gerr := d.Service.GetUser(ctx, name); gerr == nil {
			return OrgCreateOut{}, op.Errorf(op.CodeConflict, "organization name is taken")
		}
		if body.Website != "" && !isHTTPURL(body.Website) {
			return OrgCreateOut{}, op.Errorf(op.CodeValidation, "website must be an http or https URL")
		}
		visibility := body.Visibility
		if visibility == "" {
			visibility = "public"
		}
		if verr := checkVisibilityValue(visibility); verr != nil {
			return OrgCreateOut{}, verr
		}
		now := d.now()
		var row store.OrgRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			created, cerr := tx.InsertOrg(now, store.OrgInsert{
				Name: name, DisplayName: body.DisplayName, Description: body.Description,
				Website: body.Website, Visibility: visibility,
			})
			if cerr != nil {
				return cerr
			}
			if merr := tx.AddOwnersMember(created.ID, user.ID); merr != nil {
				return merr
			}
			row = created
			return nil
		})
		if werr != nil {
			if errors.Is(werr, store.ErrConflict) {
				return OrgCreateOut{}, op.Errorf(op.CodeConflict, "organization name is taken")
			}
			return OrgCreateOut{}, op.Wrap(op.CodeInternal, werr, "organization record failed")
		}
		return OrgCreateOut{Org: orgJSON(row, "owner")}, nil
	}
}

func getOrgHandler(d Deps) func(context.Context, op.Principal, OrgGetIn) (OrgGetOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgGetIn) (OrgGetOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return OrgGetOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgGetOut{}, rerr
		}
		row, rserr := resolveOrgByName(ctx, d, in.Org)
		if rserr != nil {
			return OrgGetOut{}, rserr
		}
		if derr := orgAccess(d, rq, row.ID, "org.get"); derr != nil {
			return OrgGetOut{}, derr
		}
		role := ""
		if rq.authed {
			role = orgRoleOf(d, row.ID, rq.userID)
		}
		out := OrgGetOut{Org: orgJSON(row, role)}
		var repos []store.RepoRow
		var teams []store.TeamRow
		serr = st.Read(ctx, func(tx *store.ReadTx) error {
			listedRepos, lerr := tx.ListOrgRepos(row.ID, 100)
			if lerr != nil {
				return lerr
			}
			repos = listedRepos
			listedTeams, terr := tx.ListTeams(row.ID, "", 100)
			if terr != nil {
				return terr
			}
			teams = listedTeams
			return nil
		})
		if serr != nil {
			return OrgGetOut{}, op.Wrap(op.CodeInternal, serr, "organization detail failed")
		}
		for _, repo := range repos {
			if d.checkRepoAccess(rq, repo, "repo.view", "") == nil {
				out.OrgRepos = append(out.OrgRepos, repoJSON(row.Name, repo))
			}
		}
		for _, team := range teams {
			if teamAccess(d, rq, row.ID, team.ID, "team.view") == nil {
				out.Teams = append(out.Teams, teamJSON(team))
			}
		}
		return out, nil
	}
}

func listOrgHandler(d Deps) func(context.Context, op.Principal, OrgListIn) (OrgListOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgListIn) (OrgListOut, error) {
		user, uerr := requireSessionUser(ctx, d, p)
		if uerr != nil {
			return OrgListOut{}, uerr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgListOut{}, rerr
		}
		if derr := siteAccess(d, rq, "org.list"); derr != nil {
			return OrgListOut{}, derr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 30
		}
		if limit > 100 {
			limit = 100
		}
		st, serr := requireStore(d)
		if serr != nil {
			return OrgListOut{}, serr
		}
		out := OrgListOut{}
		serr = st.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListOrgsForUser(user.ID, in.Cursor, limit+1)
			if lerr != nil {
				return lerr
			}
			for i, row := range rows {
				if i == limit {
					out.Next = row.LowerName
					break
				}
				role := "member"
				if owner, oerr := tx.IsOrgOwnerAtRead(row.ID, user.ID); oerr == nil && owner {
					role = "owner"
				}
				out.Orgs = append(out.Orgs, orgJSON(row, role))
			}
			return nil
		})
		if serr != nil {
			return OrgListOut{}, op.Wrap(op.CodeInternal, serr, "organization listing failed")
		}
		return out, nil
	}
}

func updateOrgHandler(d Deps) func(context.Context, op.Principal, OrgUpdateIn) (OrgUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgUpdateIn) (OrgUpdateOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return OrgUpdateOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgUpdateOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return OrgUpdateOut{}, serr
		}
		row, rserr := resolveOrgByName(ctx, d, in.Org)
		if rserr != nil {
			return OrgUpdateOut{}, rserr
		}
		if derr := orgAccess(d, rq, row.ID, "org.update"); derr != nil {
			return OrgUpdateOut{}, derr
		}
		body := in.Body
		if body.Website != nil && *body.Website != "" && !isHTTPURL(*body.Website) {
			return OrgUpdateOut{}, op.Errorf(op.CodeValidation, "website must be an http or https URL")
		}
		if body.Visibility != nil {
			if verr := checkVisibilityValue(*body.Visibility); verr != nil {
				return OrgUpdateOut{}, verr
			}
		}
		now := d.now()
		var updated store.OrgRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetOrgForUpdate(row.ID)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "organization was not found")
				}
				return gerr
			}
			if merr := matchOrgETag(in.IfMatch, current); merr != nil {
				return merr
			}
			var uerr error
			updated, uerr = tx.UpdateOrg(row.ID, now, body.DisplayName, body.Description, body.Website, body.Visibility)
			return uerr
		})
		if werr != nil {
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return OrgUpdateOut{}, oerr
			}
			return OrgUpdateOut{}, op.Wrap(op.CodeInternal, werr, "organization update failed")
		}
		return OrgUpdateOut{Org: orgJSON(updated, orgRoleOf(d, updated.ID, rq.userID))}, nil
	}
}

func renameOrgHandler(d Deps) func(context.Context, op.Principal, OrgRenameIn) (OrgRenameOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgRenameIn) (OrgRenameOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return OrgRenameOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgRenameOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return OrgRenameOut{}, serr
		}
		row, rserr := resolveOrgByName(ctx, d, in.Org)
		if rserr != nil {
			return OrgRenameOut{}, rserr
		}
		if derr := orgAccess(d, rq, row.ID, "org.rename"); derr != nil {
			return OrgRenameOut{}, derr
		}
		name := strings.ToLower(strings.TrimSpace(in.Body.Name))
		if err := checkOrgName(name); err != nil {
			return OrgRenameOut{}, err
		}
		if _, gerr := d.Service.GetUser(ctx, name); gerr == nil {
			return OrgRenameOut{}, op.Errorf(op.CodeConflict, "organization name is taken")
		}
		now := d.now()
		var updated store.OrgRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetOrgForUpdate(row.ID)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "organization was not found")
				}
				return gerr
			}
			if merr := matchOrgETag(in.IfMatch, current); merr != nil {
				return merr
			}
			var uerr error
			updated, uerr = tx.RenameOrg(row.ID, now, name)
			return uerr
		})
		if werr != nil {
			if errors.Is(werr, store.ErrConflict) {
				return OrgRenameOut{}, op.Errorf(op.CodeConflict, "organization name is taken")
			}
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return OrgRenameOut{}, oerr
			}
			return OrgRenameOut{}, op.Wrap(op.CodeInternal, werr, "organization rename failed")
		}
		return OrgRenameOut{Org: orgJSON(updated, orgRoleOf(d, updated.ID, rq.userID))}, nil
	}
}

func deleteOrgHandler(d Deps) func(context.Context, op.Principal, OrgDeleteIn) (OrgDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgDeleteIn) (OrgDeleteOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return OrgDeleteOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgDeleteOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return OrgDeleteOut{}, serr
		}
		row, rserr := resolveOrgByName(ctx, d, in.Org)
		if rserr != nil {
			return OrgDeleteOut{}, rserr
		}
		if derr := orgAccess(d, rq, row.ID, "org.delete"); derr != nil {
			return OrgDeleteOut{}, derr
		}
		if in.Body.Confirm != row.Name {
			return OrgDeleteOut{}, op.Errorf(op.CodeValidation, "confirmation must repeat the organization name")
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetOrgForUpdate(row.ID)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "organization was not found")
				}
				return gerr
			}
			if merr := matchOrgETag(in.IfMatch, current); merr != nil {
				return merr
			}
			count, cerr := tx.CountOrgRepos(row.ID)
			if cerr != nil {
				return cerr
			}
			if count > 0 {
				return op.Errorf(op.CodeConflict, "organization still holds repositories")
			}
			return tx.DeleteOrg(row.ID)
		})
		if werr != nil {
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return OrgDeleteOut{}, oerr
			}
			return OrgDeleteOut{}, op.Wrap(op.CodeInternal, werr, "organization delete failed")
		}
		return OrgDeleteOut{}, nil
	}
}

func listOrgMemberHandler(d Deps) func(context.Context, op.Principal, OrgMemberListIn) (OrgMemberListOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgMemberListIn) (OrgMemberListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgMemberListOut{}, rerr
		}
		row, rserr := resolveOrgByName(ctx, d, in.Org)
		if rserr != nil {
			return OrgMemberListOut{}, rserr
		}
		if derr := orgAccess(d, rq, row.ID, "org.member.list"); derr != nil {
			return OrgMemberListOut{}, derr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 30
		}
		if limit > 100 {
			limit = 100
		}
		if in.Role != "" && in.Role != "owner" && in.Role != "member" {
			return OrgMemberListOut{}, op.Errorf(op.CodeValidation, "role must be owner or member")
		}
		out := OrgMemberListOut{}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListOrgMembers(row.ID, in.Cursor, limit+1)
			if lerr != nil {
				return lerr
			}
			for i, member := range rows {
				if i == limit {
					out.Next = strings.ToLower(member.Username)
					break
				}
				if in.Role != "" && member.Role != in.Role {
					continue
				}
				out.Members = append(out.Members, OrgMemberJSON{
					Username: member.Username, Role: member.Role, Teams: member.Teams,
				})
			}
			return nil
		})
		if serr != nil {
			if oerr, ok := errors.AsType[*op.Error](serr); ok {
				return OrgMemberListOut{}, oerr
			}
			return OrgMemberListOut{}, op.Wrap(op.CodeInternal, serr, "member listing failed")
		}
		return out, nil
	}
}

func removeOrgMember(ctx context.Context, d Deps, rq requester, org, targetName, action string) error {
	st, serr := requireStore(d)
	if serr != nil {
		return serr
	}
	row, rserr := resolveOrgByName(ctx, d, org)
	if rserr != nil {
		return rserr
	}
	if derr := orgAccess(d, rq, row.ID, action); derr != nil {
		return derr
	}
	target, terr := d.Service.GetUser(ctx, targetName)
	if terr != nil {
		return op.Errorf(op.CodeNotFound, "unknown user")
	}
	werr := st.Write(ctx, func(tx *store.WriteTx) error {
		teams, lerr := tx.UserTeamsInOrg(row.ID, target.ID)
		if lerr != nil {
			return lerr
		}
		if len(teams) == 0 {
			return op.Errorf(op.CodeNotFound, "user is not an organization member")
		}
		for _, team := range teams {
			if _, rerr := tx.RemoveTeamMember(team.ID, target.ID); rerr != nil {
				if errors.Is(rerr, store.ErrInvalid) {
					return op.Errorf(op.CodeConflict, "cannot remove the last organization owner")
				}
				return rerr
			}
		}
		return nil
	})
	if werr != nil {
		if oerr, ok := errors.AsType[*op.Error](werr); ok {
			return oerr
		}
		return op.Wrap(op.CodeInternal, werr, "member removal failed")
	}
	return nil
}

func removeOrgMemberHandler(d Deps) func(context.Context, op.Principal, OrgMemberRemoveIn) (OrgMemberRemoveOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgMemberRemoveIn) (OrgMemberRemoveOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgMemberRemoveOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return OrgMemberRemoveOut{}, serr
		}
		if derr := removeOrgMember(ctx, d, rq, in.Org, in.User, "org.member.remove"); derr != nil {
			return OrgMemberRemoveOut{}, derr
		}
		return OrgMemberRemoveOut{}, nil
	}
}

func leaveOrgHandler(d Deps) func(context.Context, op.Principal, OrgLeaveIn) (OrgLeaveOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgLeaveIn) (OrgLeaveOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgLeaveOut{}, rerr
		}
		me, merr := requireSessionUser(ctx, d, p)
		if merr != nil {
			return OrgLeaveOut{}, merr
		}
		if derr := removeOrgMember(ctx, d, rq, in.Org, me.Name, "org.leave"); derr != nil {
			return OrgLeaveOut{}, derr
		}
		return OrgLeaveOut{}, nil
	}
}

func resolveTeam(ctx context.Context, d Deps, orgName, teamName string) (store.OrgRow, store.TeamRow, error) {
	org, oerr := resolveOrgByName(ctx, d, orgName)
	if oerr != nil {
		return store.OrgRow{}, store.TeamRow{}, oerr
	}
	var team store.TeamRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		found, ferr := tx.FindTeam(org.ID, teamName)
		if ferr != nil {
			return ferr
		}
		team = found
		return nil
	})
	if serr != nil {
		if errors.Is(serr, store.ErrNotFound) {
			return store.OrgRow{}, store.TeamRow{}, op.Errorf(op.CodeNotFound, "team was not found")
		}
		return store.OrgRow{}, store.TeamRow{}, op.Wrap(op.CodeInternal, serr, "team lookup failed")
	}
	return org, team, nil
}

func createTeamHandler(d Deps) func(context.Context, op.Principal, TeamCreateIn) (TeamCreateOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamCreateIn) (TeamCreateOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return TeamCreateOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamCreateOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamCreateOut{}, serr
		}
		org, oerr := resolveOrgByName(ctx, d, in.Org)
		if oerr != nil {
			return TeamCreateOut{}, oerr
		}
		if derr := teamAccess(d, rq, org.ID, 0, "team.create"); derr != nil {
			return TeamCreateOut{}, derr
		}
		body := in.Body
		name := strings.ToLower(strings.TrimSpace(body.Name))
		if err := checkTeamName(name); err != nil {
			return TeamCreateOut{}, err
		}
		permission := body.Permission
		if permission == "" {
			permission = "read"
		}
		if err := checkTeamPermission(permission); err != nil {
			return TeamCreateOut{}, err
		}
		scope := body.RepoScope
		if scope == "" {
			scope = "selected"
		}
		if err := checkRepoScope(scope); err != nil {
			return TeamCreateOut{}, err
		}
		now := d.now()
		var team store.TeamRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			created, cerr := tx.InsertTeam(now, store.TeamInsert{
				OrgID: org.ID, Name: name, Description: body.Description,
				Permission: permission, RepoScope: scope,
			})
			if cerr != nil {
				return cerr
			}
			team = created
			return nil
		})
		if werr != nil {
			if errors.Is(werr, store.ErrConflict) {
				return TeamCreateOut{}, op.Errorf(op.CodeConflict, "team name is taken")
			}
			if errors.Is(werr, store.ErrInvalid) {
				return TeamCreateOut{}, op.Errorf(op.CodeConflict, "owners team is reserved")
			}
			return TeamCreateOut{}, op.Wrap(op.CodeInternal, werr, "team record failed")
		}
		return TeamCreateOut{Team: teamJSON(team)}, nil
	}
}

func getTeamHandler(d Deps) func(context.Context, op.Principal, TeamGetIn) (TeamGetOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamGetIn) (TeamGetOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamGetOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamGetOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamGetOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.get"); derr != nil {
			return TeamGetOut{}, derr
		}
		out := TeamGetOut{Team: teamJSON(team)}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			members, merr := tx.ListTeamMembers(team.ID, "", 100)
			if merr != nil {
				return merr
			}
			for _, member := range members {
				out.Members = append(out.Members, OrgMemberJSON{Username: member.Username})
			}
			repos, rerr := tx.ListTeamRepos(team.ID, "", 100)
			if rerr != nil {
				return rerr
			}
			for _, repo := range repos {
				out.Repos = append(out.Repos, repo.Name)
			}
			return nil
		})
		if serr != nil {
			return TeamGetOut{}, op.Wrap(op.CodeInternal, serr, "team detail failed")
		}
		return out, nil
	}
}

func listTeamHandler(d Deps) func(context.Context, op.Principal, TeamListIn) (TeamListOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamListIn) (TeamListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamListOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamListOut{}, serr
		}
		org, oerr := resolveOrgByName(ctx, d, in.Org)
		if oerr != nil {
			return TeamListOut{}, oerr
		}
		if derr := teamAccess(d, rq, org.ID, 0, "team.list"); derr != nil {
			return TeamListOut{}, derr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 30
		}
		if limit > 100 {
			limit = 100
		}
		out := TeamListOut{}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListTeams(org.ID, in.Cursor, limit+1)
			if lerr != nil {
				return lerr
			}
			for i, row := range rows {
				if i == limit {
					out.Next = row.LowerName
					break
				}
				out.Teams = append(out.Teams, teamJSON(row))
			}
			return nil
		})
		if serr != nil {
			return TeamListOut{}, op.Wrap(op.CodeInternal, serr, "team listing failed")
		}
		return out, nil
	}
}

func updateTeamHandler(d Deps) func(context.Context, op.Principal, TeamUpdateIn) (TeamUpdateOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamUpdateIn) (TeamUpdateOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return TeamUpdateOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamUpdateOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamUpdateOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamUpdateOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.update"); derr != nil {
			return TeamUpdateOut{}, derr
		}
		body := in.Body
		var name *string
		if body.Name != nil {
			trimmed := strings.ToLower(strings.TrimSpace(*body.Name))
			if err := checkTeamName(trimmed); err != nil {
				return TeamUpdateOut{}, err
			}
			name = &trimmed
		}
		if body.Permission != nil {
			if err := checkTeamPermission(*body.Permission); err != nil {
				return TeamUpdateOut{}, err
			}
		}
		if body.RepoScope != nil {
			if err := checkRepoScope(*body.RepoScope); err != nil {
				return TeamUpdateOut{}, err
			}
		}
		now := d.now()
		var updated store.TeamRow
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetTeamForUpdate(team.ID)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "team was not found")
				}
				return gerr
			}
			if merr := matchTeamETag(in.IfMatch, current); merr != nil {
				return merr
			}
			var uerr error
			updated, uerr = tx.UpdateTeam(team.ID, now, name, body.Description, body.Permission, body.RepoScope)
			return uerr
		})
		if werr != nil {
			if errors.Is(werr, store.ErrConflict) {
				return TeamUpdateOut{}, op.Errorf(op.CodeConflict, "team name is taken")
			}
			if errors.Is(werr, store.ErrInvalid) {
				return TeamUpdateOut{}, op.Errorf(op.CodeConflict, "owners team is immutable")
			}
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return TeamUpdateOut{}, oerr
			}
			return TeamUpdateOut{}, op.Wrap(op.CodeInternal, werr, "team update failed")
		}
		return TeamUpdateOut{Team: teamJSON(updated)}, nil
	}
}

func deleteTeamHandler(d Deps) func(context.Context, op.Principal, TeamDeleteIn) (TeamDeleteOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamDeleteIn) (TeamDeleteOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return TeamDeleteOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamDeleteOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamDeleteOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamDeleteOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.delete"); derr != nil {
			return TeamDeleteOut{}, derr
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			current, gerr := tx.GetTeamForUpdate(team.ID)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "team was not found")
				}
				return gerr
			}
			if merr := matchTeamETag(in.IfMatch, current); merr != nil {
				return merr
			}
			return tx.DeleteTeam(team.ID)
		})
		if werr != nil {
			if errors.Is(werr, store.ErrInvalid) {
				return TeamDeleteOut{}, op.Errorf(op.CodeConflict, "owners team cannot be deleted")
			}
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return TeamDeleteOut{}, oerr
			}
			return TeamDeleteOut{}, op.Wrap(op.CodeInternal, werr, "team delete failed")
		}
		return TeamDeleteOut{}, nil
	}
}

func listTeamMemberHandler(d Deps) func(context.Context, op.Principal, TeamMemberListIn) (TeamMemberListOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamMemberListIn) (TeamMemberListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamMemberListOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamMemberListOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamMemberListOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.member.list"); derr != nil {
			return TeamMemberListOut{}, derr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 30
		}
		if limit > 100 {
			limit = 100
		}
		out := TeamMemberListOut{}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListTeamMembers(team.ID, in.Cursor, limit+1)
			if lerr != nil {
				return lerr
			}
			for i, row := range rows {
				if i == limit {
					out.Next = strings.ToLower(row.Username)
					break
				}
				out.Members = append(out.Members, OrgMemberJSON{Username: row.Username})
			}
			return nil
		})
		if serr != nil {
			return TeamMemberListOut{}, op.Wrap(op.CodeInternal, serr, "team member listing failed")
		}
		return out, nil
	}
}

func addTeamMemberHandler(d Deps) func(context.Context, op.Principal, TeamMemberAddIn) (TeamMemberAddOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamMemberAddIn) (TeamMemberAddOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return TeamMemberAddOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamMemberAddOut{}, rerr
		}
		me, merr := requireSessionUser(ctx, d, p)
		if merr != nil {
			return TeamMemberAddOut{}, merr
		}
		_ = me
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamMemberAddOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.member.add"); derr != nil {
			return TeamMemberAddOut{}, derr
		}
		username := strings.ToLower(strings.TrimSpace(in.Body.User))
		target, terr := d.Service.GetUser(ctx, username)
		if terr != nil {
			return TeamMemberAddOut{}, op.Errorf(op.CodeNotFound, "unknown user")
		}
		if !target.IsActive {
			return TeamMemberAddOut{}, op.Errorf(op.CodeValidation, "user is not active")
		}
		now := d.now()
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			if _, gerr := tx.GetTeamForUpdate(team.ID); gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "team was not found")
				}
				return gerr
			}
			added, aerr := tx.AddTeamMember(team.ID, target.ID)
			if aerr != nil {
				return aerr
			}
			if !added {
				return nil
			}
			body, merr := marshalJSON(map[string]any{
				"org": org.Name, "team": team.Name, "user": target.Name,
			})
			if merr != nil {
				return merr
			}
			_, eerr := tx.Outbox().Emit(store.Event{
				Kind: "membership", Body: body, OccurredAt: now,
			})
			return eerr
		})
		if werr != nil {
			if errors.Is(werr, store.ErrReference) {
				return TeamMemberAddOut{}, op.Errorf(op.CodeNotFound, "unknown user")
			}
			return TeamMemberAddOut{}, op.Wrap(op.CodeInternal, werr, "team member add failed")
		}
		return TeamMemberAddOut{}, nil
	}
}

func removeTeamMemberHandler(d Deps) func(context.Context, op.Principal, TeamMemberRemoveIn) (TeamMemberRemoveOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamMemberRemoveIn) (TeamMemberRemoveOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return TeamMemberRemoveOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamMemberRemoveOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamMemberRemoveOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamMemberRemoveOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.member.remove"); derr != nil {
			return TeamMemberRemoveOut{}, derr
		}
		target, terr := d.Service.GetUser(ctx, strings.ToLower(strings.TrimSpace(in.User)))
		if terr != nil {
			return TeamMemberRemoveOut{}, op.Errorf(op.CodeNotFound, "unknown user")
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			removed, rerr := tx.RemoveTeamMember(team.ID, target.ID)
			if rerr != nil {
				if errors.Is(rerr, store.ErrInvalid) {
					return op.Errorf(op.CodeConflict, "cannot remove the last organization owner")
				}
				return rerr
			}
			if !removed {
				return op.Errorf(op.CodeNotFound, "user is not a team member")
			}
			return nil
		})
		if werr != nil {
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return TeamMemberRemoveOut{}, oerr
			}
			return TeamMemberRemoveOut{}, op.Wrap(op.CodeInternal, werr, "team member removal failed")
		}
		return TeamMemberRemoveOut{}, nil
	}
}

func listTeamRepoHandler(d Deps) func(context.Context, op.Principal, TeamRepoListIn) (TeamRepoListOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamRepoListIn) (TeamRepoListOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamRepoListOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamRepoListOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamRepoListOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.repo.list"); derr != nil {
			return TeamRepoListOut{}, derr
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 30
		}
		if limit > 100 {
			limit = 100
		}
		out := TeamRepoListOut{}
		serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
			rows, lerr := tx.ListTeamRepos(team.ID, in.Cursor, limit+1)
			if lerr != nil {
				return lerr
			}
			for i, row := range rows {
				if i == limit {
					out.Next = row.Name
					break
				}
				out.Repos = append(out.Repos, row.Name)
			}
			return nil
		})
		if serr != nil {
			return TeamRepoListOut{}, op.Wrap(op.CodeInternal, serr, "team repo listing failed")
		}
		return out, nil
	}
}

func addTeamRepoHandler(d Deps) func(context.Context, op.Principal, TeamRepoAddIn) (TeamRepoAddOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamRepoAddIn) (TeamRepoAddOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return TeamRepoAddOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamRepoAddOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamRepoAddOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamRepoAddOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.repo.add"); derr != nil {
			return TeamRepoAddOut{}, derr
		}
		if team.RepoScope == "all" {
			return TeamRepoAddOut{}, op.Errorf(op.CodeConflict, "team already grants every repository")
		}
		repoName := strings.TrimSpace(in.Body.Repo)
		if err := checkRepoName(repoName); err != nil {
			return TeamRepoAddOut{}, err
		}
		var repoID int64
		verr := st.Read(ctx, func(tx *store.ReadTx) error {
			repo, ferr := tx.FindRepo(0, org.ID, repoName)
			if ferr != nil {
				if errors.Is(ferr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "repository is not in this organization")
				}
				return ferr
			}
			repoID = repo.ID
			return nil
		})
		if verr != nil {
			if oerr, ok := errors.AsType[*op.Error](verr); ok {
				return TeamRepoAddOut{}, oerr
			}
			return TeamRepoAddOut{}, op.Wrap(op.CodeInternal, verr, "team repo grant failed")
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			return tx.AddTeamRepo(team.ID, repoID)
		})
		if werr != nil {
			if errors.Is(werr, store.ErrNotFound) {
				return TeamRepoAddOut{}, op.Errorf(op.CodeNotFound, "repository is not in this organization")
			}
			if errors.Is(werr, store.ErrInvalid) {
				return TeamRepoAddOut{}, op.Errorf(op.CodeNotFound, "repository is not in this organization")
			}
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return TeamRepoAddOut{}, oerr
			}
			return TeamRepoAddOut{}, op.Wrap(op.CodeInternal, werr, "team repo grant failed")
		}
		return TeamRepoAddOut{}, nil
	}
}

func removeTeamRepoHandler(d Deps) func(context.Context, op.Principal, TeamRepoRemoveIn) (TeamRepoRemoveOut, error) {
	return func(ctx context.Context, p op.Principal, in TeamRepoRemoveIn) (TeamRepoRemoveOut, error) {
		st, serr := requireStore(d)
		if serr != nil {
			return TeamRepoRemoveOut{}, serr
		}
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return TeamRepoRemoveOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return TeamRepoRemoveOut{}, serr
		}
		org, team, rerr := resolveTeam(ctx, d, in.Org, in.Team)
		if rerr != nil {
			return TeamRepoRemoveOut{}, rerr
		}
		if derr := teamAccess(d, rq, org.ID, team.ID, "team.repo.remove"); derr != nil {
			return TeamRepoRemoveOut{}, derr
		}
		if team.RepoScope == "all" {
			return TeamRepoRemoveOut{}, op.Errorf(op.CodeConflict, "team already grants every repository")
		}
		var repoID int64
		verr := st.Read(ctx, func(tx *store.ReadTx) error {
			repo, ferr := tx.FindRepo(0, org.ID, in.Repo)
			if ferr != nil {
				if errors.Is(ferr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "repository is not in this organization")
				}
				return ferr
			}
			repoID = repo.ID
			return nil
		})
		if verr != nil {
			if oerr, ok := errors.AsType[*op.Error](verr); ok {
				return TeamRepoRemoveOut{}, oerr
			}
			return TeamRepoRemoveOut{}, op.Wrap(op.CodeInternal, verr, "team repo revoke failed")
		}
		werr := st.Write(ctx, func(tx *store.WriteTx) error {
			removed, rerr := tx.RemoveTeamRepo(team.ID, repoID)
			if rerr != nil {
				return rerr
			}
			if !removed {
				return op.Errorf(op.CodeNotFound, "repository is not granted to this team")
			}
			return nil
		})
		if werr != nil {
			if oerr, ok := errors.AsType[*op.Error](werr); ok {
				return TeamRepoRemoveOut{}, oerr
			}
			return TeamRepoRemoveOut{}, op.Wrap(op.CodeInternal, werr, "team repo revoke failed")
		}
		return TeamRepoRemoveOut{}, nil
	}
}

func RegisterOrgs(t *op.Table, deps Deps) error {
	if err := op.Register(t, op.Op[op.NoBody, OrgNewPageOut]{
		Name: "org.create.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/new/org",
		Scope: op.ScopePublicRead, Summary: "Show the new organization form",
		Description: "Page-only. Renders the zero-JS organization creation form.",
		ETag:        func(OrgNewPageOut) string { return "org-new" },
		Hidden:      true,
		Handler: func(context.Context, op.Principal, op.NoBody) (OrgNewPageOut, error) {
			return OrgNewPageOut{}, nil
		},
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgCreateIn, OrgCreateOut]{
		Name: "org.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/new/org",
		Scope: op.ScopeRepoWrite, Summary: "Create an organization",
		Description: "Session-only. The creator joins the owners team, which holds admin on every org repository.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeValidation, op.CodeConflict},
		Location:    func(out OrgCreateOut) string { return "/" + out.Org.Name },
		ETag:        func(out OrgCreateOut) string { return fmt.Sprintf("org-%d-%d", out.Org.ID, out.Org.UpdatedAt) },
		Handler:     createOrgHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgGetIn, OrgGetOut]{
		Name: "org.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/orgs/{org}",
		Scope: op.ScopePublicRead, Summary: "Show an organization",
		Description: "Public organizations render for anyone. Private organizations render for members and 404 otherwise.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out OrgGetOut) string {
			return fmt.Sprintf("org-%d-%d", out.Org.ID, out.Org.UpdatedAt)
		},
		Handler: getOrgHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgListIn, OrgListOut]{
		Name: "org.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/settings/orgs",
		Scope: op.ScopeRepoRead, Summary: "List my organizations",
		Description: "Session-only. Keyset pages over the organizations the caller joined through any team.",
		Problems:    []op.Code{op.CodeUnauthorized},
		ETag: func(out OrgListOut) string {
			if len(out.Orgs) == 0 {
				return "orgs-empty"
			}
			return fmt.Sprintf("orgs-%d-%s-%s", len(out.Orgs), out.Orgs[0].Name, out.Next)
		},
		Pagination: &op.Pagination[OrgListOut]{Default: 30, Max: 100, Next: func(out OrgListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: listOrgHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgSettingsPageIn, OrgSettingsPageOut]{
		Name: "org.settings.page", Kind: op.KindRead, Method: http.MethodGet, Path: "/{org}/-/settings",
		Scope: op.ScopePublicRead, Summary: "Show organization settings",
		Description: "Page-only. Renders the zero-JS settings forms with a current entity tag.",
		ETag:        func(out OrgSettingsPageOut) string { return out.ETag },
		Hidden:      true,
		Handler:     settingsOrgPageHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgUpdateIn, OrgUpdateOut]{
		Name: "org.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/settings/general",
		Scope: op.ScopeRepoWrite, Summary: "Update organization profile",
		Description: "Session-only, organization owners. Requires a current If-Match entity tag.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		ETag:        func(out OrgUpdateOut) string { return fmt.Sprintf("org-%d-%d", out.Org.ID, out.Org.UpdatedAt) },
		Location:    func(out OrgUpdateOut) string { return "/" + out.Org.Name + "/-/settings" },
		Handler:     updateOrgHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgRenameIn, OrgRenameOut]{
		Name: "org.rename", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/settings/rename",
		Scope: op.ScopeRepoWrite, Summary: "Rename an organization",
		Description: "Session-only, organization owners. The old name keeps resolving until another organization takes it.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		ETag:        func(out OrgRenameOut) string { return fmt.Sprintf("org-%d-%d", out.Org.ID, out.Org.UpdatedAt) },
		Location:    func(out OrgRenameOut) string { return "/" + out.Org.Name + "/-/settings" },
		Handler:     renameOrgHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgDeleteIn, OrgDeleteOut]{
		Name: "org.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/settings/delete",
		Scope: op.ScopeRepoWrite, Summary: "Delete an organization",
		Description: "Session-only, organization owners. Refuses while the organization still holds repositories.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		ETag:        func(OrgDeleteOut) string { return "" },
		Status:      http.StatusNoContent,
		Location:    func(OrgDeleteOut) string { return "/settings/orgs" },
		Handler:     deleteOrgHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgMemberListIn, OrgMemberListOut]{
		Name: "org.member.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{org}/-/members",
		Scope: op.ScopePublicRead, Summary: "List organization members",
		Description: "Public organizations list for anyone. Private organizations list for members and 404 otherwise.",
		Problems:    []op.Code{op.CodeNotFound, op.CodeForbidden},
		ETag: func(out OrgMemberListOut) string {
			if len(out.Members) == 0 {
				return "org-members-empty"
			}
			return fmt.Sprintf("org-members-%d-%s-%s", len(out.Members), out.Members[0].Username, out.Next)
		},
		Pagination: &op.Pagination[OrgMemberListOut]{Default: 30, Max: 100, Next: func(out OrgMemberListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: listOrgMemberHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgMemberRemoveIn, OrgMemberRemoveOut]{
		Name: "org.member.remove", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/members/{user}/remove",
		Scope: op.ScopeRepoWrite, Summary: "Remove an organization member",
		Description: "Session-only, organization owners. Removes the user from every team. Refuses the last owner.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeConflict},
		Status:      http.StatusNoContent,
		Location:    func(OrgMemberRemoveOut) string { return "" },
		Handler:     removeOrgMemberHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[OrgLeaveIn, OrgLeaveOut]{
		Name: "org.leave", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/members/leave",
		Scope: op.ScopeRepoWrite, Summary: "Leave an organization",
		Description: "Session-only. Removes the caller from every team. Refuses the last owner.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeConflict},
		Status:      http.StatusNoContent,
		Location:    func(OrgLeaveOut) string { return "/settings/orgs" },
		Handler:     leaveOrgHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamListIn, TeamListOut]{
		Name: "team.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{org}/-/teams",
		Scope: op.ScopeRepoRead, Summary: "List organization teams",
		Description: "Session-only, organization members. Keyset pages over team names.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound},
		ETag: func(out TeamListOut) string {
			if len(out.Teams) == 0 {
				return "teams-empty"
			}
			return fmt.Sprintf("teams-%d-%s-%s", len(out.Teams), out.Teams[0].Name, out.Next)
		},
		Pagination: &op.Pagination[TeamListOut]{Default: 30, Max: 100, Next: func(out TeamListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: listTeamHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamCreateIn, TeamCreateOut]{
		Name: "team.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/teams/create",
		Scope: op.ScopeRepoWrite, Summary: "Create a team",
		Description: "Session-only, organization owners. One permission covers every selected repository.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict},
		Location:    func(TeamCreateOut) string { return "" },
		ETag: func(out TeamCreateOut) string {
			return fmt.Sprintf("team-%s-%d", out.Team.Name, out.Team.UpdatedAt)
		},
		Handler: createTeamHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamGetIn, TeamGetOut]{
		Name: "team.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/{org}/-/teams/{team}",
		Scope: op.ScopeRepoRead, Summary: "Show a team",
		Description: "Session-only, organization members. Renders members and repository grants.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound},
		ETag: func(out TeamGetOut) string {
			return fmt.Sprintf("team-%s-%d-%d", out.Team.Name, len(out.Members), len(out.Repos))
		},
		Handler: getTeamHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamUpdateIn, TeamUpdateOut]{
		Name: "team.update", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/teams/{team}/edit",
		Scope: op.ScopeRepoWrite, Summary: "Update a team",
		Description: "Session-only, organization owners. The owners team is immutable. Requires If-Match.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		ETag: func(out TeamUpdateOut) string {
			return fmt.Sprintf("team-%s-%d", out.Team.Name, out.Team.UpdatedAt)
		},
		Location: func(TeamUpdateOut) string { return "" },
		Handler:  updateTeamHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamDeleteIn, TeamDeleteOut]{
		Name: "team.delete", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/teams/{team}/delete",
		Scope: op.ScopeRepoWrite, Summary: "Delete a team",
		Description: "Session-only, organization owners. The owners team cannot be deleted. Requires If-Match.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeConflict, op.CodeStale, op.CodePreconditionReq},
		Concurrency: op.ConcurrencyIfMatchRequired,
		ETag:        func(TeamDeleteOut) string { return "" },
		Status:      http.StatusNoContent,
		Location:    func(TeamDeleteOut) string { return "" },
		Handler:     deleteTeamHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamMemberListIn, TeamMemberListOut]{
		Name: "team.member.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{org}/-/teams/{team}/members",
		Scope: op.ScopeRepoRead, Summary: "List team members",
		Description: "Session-only, organization members. Keyset pages over member names.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound},
		ETag: func(out TeamMemberListOut) string {
			if len(out.Members) == 0 {
				return "team-members-empty"
			}
			return fmt.Sprintf("team-members-%d-%s-%s", len(out.Members), out.Members[0].Username, out.Next)
		},
		Pagination: &op.Pagination[TeamMemberListOut]{Default: 30, Max: 100, Next: func(out TeamMemberListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: listTeamMemberHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamMemberAddIn, TeamMemberAddOut]{
		Name: "team.member.add", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/teams/{team}/members",
		Scope: op.ScopeRepoWrite, Summary: "Add a team member",
		Description: "Session-only, organization owners. Membership implies organization membership and sends a notice.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound},
		Status:      http.StatusNoContent,
		Location:    func(TeamMemberAddOut) string { return "" },
		Handler:     addTeamMemberHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamMemberRemoveIn, TeamMemberRemoveOut]{
		Name: "team.member.remove", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/teams/{team}/members/{user}/remove",
		Scope: op.ScopeRepoWrite, Summary: "Remove a team member",
		Description: "Session-only, organization owners. Refuses the last organization owner.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeConflict},
		Status:      http.StatusNoContent,
		Location:    func(TeamMemberRemoveOut) string { return "" },
		Handler:     removeTeamMemberHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamRepoListIn, TeamRepoListOut]{
		Name: "team.repo.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/{org}/-/teams/{team}/repos",
		Scope: op.ScopeRepoRead, Summary: "List team repositories",
		Description: "Session-only, organization members. Keyset pages over repository names.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound},
		ETag: func(out TeamRepoListOut) string {
			if len(out.Repos) == 0 {
				return "team-repos-empty"
			}
			return fmt.Sprintf("team-repos-%d-%s-%s", len(out.Repos), out.Repos[0], out.Next)
		},
		Pagination: &op.Pagination[TeamRepoListOut]{Default: 30, Max: 100, Next: func(out TeamRepoListOut) (string, bool) {
			if out.Next == "" {
				return "", false
			}
			return out.Next, true
		}},
		Handler: listTeamRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamRepoAddIn, TeamRepoAddOut]{
		Name: "team.repo.add", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/teams/{team}/repos",
		Scope: op.ScopeRepoWrite, Summary: "Grant a repository to a team",
		Description: "Session-only, organization owners. Rejected for all-scope teams and cross-org repositories.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeValidation, op.CodeNotFound, op.CodeConflict},
		Status:      http.StatusNoContent,
		Location:    func(TeamRepoAddOut) string { return "" },
		Handler:     addTeamRepoHandler(deps),
	}); err != nil {
		return err
	}
	if err := op.Register(t, op.Op[TeamRepoRemoveIn, TeamRepoRemoveOut]{
		Name: "team.repo.remove", Kind: op.KindMutation, Method: http.MethodPost, Path: "/{org}/-/teams/{team}/repos/{repo}/remove",
		Scope: op.ScopeRepoWrite, Summary: "Revoke a team repository grant",
		Description: "Session-only, organization owners. Rejected for all-scope teams.",
		Problems:    []op.Code{op.CodeUnauthorized, op.CodeForbidden, op.CodeNotFound, op.CodeConflict},
		Status:      http.StatusNoContent,
		Location:    func(TeamRepoRemoveOut) string { return "" },
		Handler:     removeTeamRepoHandler(deps),
	}); err != nil {
		return err
	}
	return nil
}

func settingsOrgPageHandler(d Deps) func(context.Context, op.Principal, OrgSettingsPageIn) (OrgSettingsPageOut, error) {
	return func(ctx context.Context, p op.Principal, in OrgSettingsPageIn) (OrgSettingsPageOut, error) {
		rq, rerr := d.requester(ctx, p)
		if rerr != nil {
			return OrgSettingsPageOut{}, rerr
		}
		if _, serr := requireSessionUser(ctx, d, p); serr != nil {
			return OrgSettingsPageOut{}, serr
		}
		row, rserr := resolveOrgByName(ctx, d, in.Org)
		if rserr != nil {
			return OrgSettingsPageOut{}, rserr
		}
		if derr := orgAccess(d, rq, row.ID, "org.update"); derr != nil {
			return OrgSettingsPageOut{}, derr
		}
		return OrgSettingsPageOut{Org: orgJSON(row, "owner"), ETag: orgETag(row)}, nil
	}
}

func writeOrgNewPage(w io.Writer, c op.ViewCtx, _ OrgNewPageOut) error {
	body := heading("New organization") + errorList(c.Errors) +
		formOpen("/new/org", c.CSRF) +
		textField("name", "Name", c.Input.Get("name"), false) +
		textField("display_name", "Display name", c.Input.Get("display_name"), false) +
		textField("description", "Description", c.Input.Get("description"), false) +
		textField("website", "Website", c.Input.Get("website"), false) +
		textField("visibility", "Visibility (public, private)", c.Input.Get("visibility"), false) +
		submitField("Create organization")
	_, err := io.WriteString(w, pageShell("New organization", body))
	return err
}

func writeOrgProfile(w io.Writer, c op.ViewCtx, out OrgGetOut) error {
	var b strings.Builder
	b.WriteString(heading(out.Org.Name))
	b.WriteString(errorList(c.Errors))
	if out.Org.DisplayName != "" {
		b.WriteString(para(out.Org.DisplayName))
	}
	if out.Org.Description != "" {
		b.WriteString(para(out.Org.Description))
	}
	if out.Org.Role != "" {
		b.WriteString(para("Your role: " + out.Org.Role))
	}
	b.WriteString("<p><a href=\"/" + esc(out.Org.Name) + "/-/members\">Members</a> · " +
		"<a href=\"/" + esc(out.Org.Name) + "/-/teams\">Teams</a> · " +
		"<a href=\"/" + esc(out.Org.Name) + "/-/settings\">Settings</a></p>\n")
	if len(out.OrgRepos) > 0 {
		b.WriteString("<h2>Repositories</h2>\n<ul>\n")
		for _, repo := range out.OrgRepos {
			b.WriteString("<li><a href=\"/" + esc(repo.Owner+"/"+repo.Name) + "\">" + esc(repo.Name) + "</a></li>\n")
		}
		b.WriteString("</ul>\n")
	}
	if len(out.Teams) > 0 {
		b.WriteString("<h2>Teams</h2>\n<ul>\n")
		for _, team := range out.Teams {
			b.WriteString("<li><a href=\"/" + esc(out.Org.Name) + "/-/teams/" + esc(team.Name) + "\">" + esc(team.Name) + "</a> — " + esc(team.Permission) + "</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	_, err := io.WriteString(w, pageShell(out.Org.Name, b.String()))
	return err
}

func writeOrgList(w io.Writer, c op.ViewCtx, out OrgListOut) error {
	var b strings.Builder
	b.WriteString(heading("Organizations"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, org := range out.Orgs {
		b.WriteString("<li><a href=\"/" + esc(org.Name) + "\">" + esc(org.Name) + "</a> — " + esc(org.Role) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<p><a href=\"/new/org\">New organization</a></p>\n")
	_, err := io.WriteString(w, pageShell("Organizations", b.String()))
	return err
}

func writeOrgSettings(w io.Writer, c op.ViewCtx, out OrgSettingsPageOut) error {
	org := out.Org.Name
	if org == "" {
		org = "{org}"
	}
	tag := out.ETag
	var b strings.Builder
	b.WriteString(heading("Organization settings"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<h2>Profile</h2>\n" +
		formOpen("/"+esc(org)+"/-/settings/general", c.CSRF) +
		hiddenField("_etag", tag) +
		textField("display_name", "Display name", c.Input.Get("display_name"), false) +
		textField("description", "Description", c.Input.Get("description"), false) +
		textField("website", "Website", c.Input.Get("website"), false) +
		textField("visibility", "Visibility (public, private)", c.Input.Get("visibility"), false) +
		submitField("Save profile"))
	b.WriteString("<h2>Rename</h2>\n" +
		formOpen("/"+esc(org)+"/-/settings/rename", c.CSRF) +
		hiddenField("_etag", tag) +
		textField("name", "New name", "", false) +
		submitField("Rename organization"))
	b.WriteString("<h2>Delete</h2>\n" +
		formOpen("/"+esc(org)+"/-/settings/delete", c.CSRF) +
		hiddenField("_etag", tag) +
		textField("confirm", "Type the organization name to confirm", "", false) +
		submitField("Delete organization"))
	_, err := io.WriteString(w, pageShell("Organization settings", b.String()))
	return err
}

func writeOrgMembers(w io.Writer, c op.ViewCtx, out OrgMemberListOut) error {
	base := strings.TrimSuffix(c.Path, "/members")
	org := strings.TrimPrefix(base, "/")
	var b strings.Builder
	b.WriteString(heading("Members"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, member := range out.Members {
		b.WriteString("<li>" + esc(member.Username) + " — " + esc(member.Role) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<h2>Remove a member</h2>\n" +
		formOpen("/"+esc(org)+"/-/members/_user_/remove", c.CSRF) +
		"<p>Post to /" + esc(org) + "/-/members/{user}/remove with the member name.</p>\n" +
		textField("user", "User", "", false) +
		submitField("Remove member"))
	b.WriteString("<h2>Leave</h2>\n" +
		formOpen("/"+esc(org)+"/-/members/leave", c.CSRF) +
		submitField("Leave this organization"))
	_, err := io.WriteString(w, pageShell("Members", b.String()))
	return err
}

func writeTeamList(w io.Writer, c op.ViewCtx, out TeamListOut) error {
	base := strings.TrimSuffix(c.Path, "/teams")
	org := strings.TrimPrefix(base, "/")
	var b strings.Builder
	b.WriteString(heading("Teams"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, team := range out.Teams {
		b.WriteString("<li><a href=\"/" + esc(org) + "/-/teams/" + esc(team.Name) + "\">" + esc(team.Name) + "</a> — " + esc(team.Permission) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<h2>New team</h2>\n" +
		formOpen("/"+esc(org)+"/-/teams/create", c.CSRF) +
		textField("name", "Name", "", false) +
		textField("description", "Description", "", false) +
		textField("permission", "Permission (read, triage, write, maintain, admin)", "", false) +
		textField("repo_scope", "Repository scope (all, selected)", "", false) +
		submitField("Create team"))
	_, err := io.WriteString(w, pageShell("Teams", b.String()))
	return err
}

func writeTeamDetail(w io.Writer, c op.ViewCtx, out TeamGetOut) error {
	teamPath := c.Path
	var b strings.Builder
	b.WriteString(heading(out.Team.Name))
	b.WriteString(errorList(c.Errors))
	b.WriteString(para("Permission: " + out.Team.Permission + " · Scope: " + out.Team.RepoScope))
	b.WriteString("<h2>Members</h2>\n<ul>\n")
	for _, member := range out.Members {
		b.WriteString("<li>" + esc(member.Username) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<h2>Repositories</h2>\n<ul>\n")
	for _, repo := range out.Repos {
		b.WriteString("<li>" + esc(repo) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	b.WriteString("<h2>Add a member</h2>\n" +
		formOpen(teamPath+"/members", c.CSRF) +
		textField("user", "User", "", false) +
		submitField("Add member"))
	b.WriteString("<h2>Grant a repository</h2>\n" +
		formOpen(teamPath+"/repos", c.CSRF) +
		textField("repo", "Repository", "", false) +
		submitField("Grant repository"))
	_, err := io.WriteString(w, pageShell(out.Team.Name, b.String()))
	return err
}

func writeTeamMembers(w io.Writer, c op.ViewCtx, out TeamMemberListOut) error {
	var b strings.Builder
	b.WriteString(heading("Team members"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, member := range out.Members {
		b.WriteString("<li>" + esc(member.Username) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Team members", b.String()))
	return err
}

func writeTeamRepos(w io.Writer, c op.ViewCtx, out TeamRepoListOut) error {
	var b strings.Builder
	b.WriteString(heading("Team repositories"))
	b.WriteString(errorList(c.Errors))
	b.WriteString("<ul>\n")
	for _, repo := range out.Repos {
		b.WriteString("<li>" + esc(repo) + "</li>\n")
	}
	b.WriteString("</ul>\n")
	_, err := io.WriteString(w, pageShell("Team repositories", b.String()))
	return err
}

func RegisterOrgsViews(v *op.Views) {
	op.PageView(v, "org.create.page", writeOrgNewPage)
	v.Form("org.create", func(w io.Writer, c op.ViewCtx) error {
		return writeOrgNewPage(w, c, OrgNewPageOut{})
	})
	op.PageView(v, "org.get", writeOrgProfile)
	op.PageView(v, "org.list", writeOrgList)
	op.PageView(v, "org.settings.page", writeOrgSettings)
	v.Form("org.update", func(w io.Writer, c op.ViewCtx) error {
		return writeOrgSettings(w, c, OrgSettingsPageOut{})
	})
	v.Form("org.rename", func(w io.Writer, c op.ViewCtx) error {
		return writeOrgSettings(w, c, OrgSettingsPageOut{})
	})
	v.Form("org.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeOrgSettings(w, c, OrgSettingsPageOut{})
	})
	op.PageView(v, "org.member.list", writeOrgMembers)
	v.Form("org.member.remove", func(w io.Writer, c op.ViewCtx) error {
		return writeOrgMembers(w, c, OrgMemberListOut{})
	})
	v.Form("org.leave", func(w io.Writer, c op.ViewCtx) error {
		return writeOrgMembers(w, c, OrgMemberListOut{})
	})
	op.PageView(v, "team.list", writeTeamList)
	v.Form("team.create", func(w io.Writer, c op.ViewCtx) error {
		return writeTeamList(w, c, TeamListOut{})
	})
	op.PageView(v, "team.get", writeTeamDetail)
	v.Form("team.update", func(w io.Writer, c op.ViewCtx) error {
		return writeTeamDetail(w, c, TeamGetOut{})
	})
	v.Form("team.delete", func(w io.Writer, c op.ViewCtx) error {
		return writeTeamList(w, c, TeamListOut{})
	})
	op.PageView(v, "team.member.list", writeTeamMembers)
	v.Form("team.member.add", func(w io.Writer, c op.ViewCtx) error {
		return writeTeamMembers(w, c, TeamMemberListOut{})
	})
	v.Form("team.member.remove", func(w io.Writer, c op.ViewCtx) error {
		return writeTeamMembers(w, c, TeamMemberListOut{})
	})
	op.PageView(v, "team.repo.list", writeTeamRepos)
	v.Form("team.repo.add", func(w io.Writer, c op.ViewCtx) error {
		return writeTeamRepos(w, c, TeamRepoListOut{})
	})
	v.Form("team.repo.remove", func(w io.Writer, c op.ViewCtx) error {
		return writeTeamRepos(w, c, TeamRepoListOut{})
	})
}

func orgProfileForOwner(ctx context.Context, deps Deps, p op.Principal, owner string) (UserGetOut, error) {
	rq, rerr := deps.requester(ctx, p)
	if rerr != nil {
		return UserGetOut{}, rerr
	}
	row, rserr := resolveOrgByName(ctx, deps, owner)
	if rserr != nil {
		return UserGetOut{}, rserr
	}
	if derr := orgAccess(deps, rq, row.ID, "org.get"); derr != nil {
		return UserGetOut{}, derr
	}
	role := ""
	if rq.authed {
		role = orgRoleOf(deps, row.ID, rq.userID)
	}
	return UserGetOut{
		Kind: "org", Role: role, Name: row.Name, DisplayName: row.DisplayName,
		Description: row.Description, Website: row.Website, Visibility: row.Visibility,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

func (d Deps) teamGrantsForRepo(ctx context.Context, row store.RepoRow) ([]TeamGrantJSON, error) {
	var teams []store.TeamRow
	serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
		listed, lerr := tx.ListTeams(row.OrgID, "", 1000)
		if lerr != nil {
			return lerr
		}
		teams = listed
		return nil
	})
	if serr != nil {
		return nil, op.Wrap(op.CodeInternal, serr, "team grant listing failed")
	}
	var out []TeamGrantJSON
	for _, team := range teams {
		granted := team.RepoScope == "all"
		if !granted {
			serr := d.Store.Read(ctx, func(tx *store.ReadTx) error {
				repos, lerr := tx.ListTeamRepos(team.ID, "", 1000)
				if lerr != nil {
					return lerr
				}
				for _, repo := range repos {
					if repo.ID == row.ID {
						granted = true
						break
					}
				}
				return nil
			})
			if serr != nil {
				return nil, op.Wrap(op.CodeInternal, serr, "team grant listing failed")
			}
		}
		if granted {
			out = append(out, TeamGrantJSON{Team: team.Name, Permission: team.Permission})
		}
	}
	return out, nil
}

func tokenRepoSelector(ctx context.Context, d Deps, p op.Principal, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	st, serr := requireStore(d)
	if serr != nil {
		return nil, serr
	}
	rq, rerr := d.requester(ctx, p)
	if rerr != nil {
		return nil, rerr
	}
	seen := map[int64]bool{}
	var out []int64
	serr = st.Read(ctx, func(tx *store.ReadTx) error {
		for _, id := range ids {
			if id <= 0 {
				return op.Errorf(op.CodeValidation, "repository selector holds an invalid id")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			row, gerr := tx.GetRepo(id)
			if gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					return op.Errorf(op.CodeNotFound, "selected repository was not found")
				}
				return gerr
			}
			if derr := d.checkRepoAccess(rq, row, "repo.view", ""); derr != nil {
				return op.Errorf(op.CodeValidation, "token repository is not visible to the caller")
			}
			out = append(out, id)
		}
		return nil
	})
	if serr != nil {
		if oerr, ok := errors.AsType[*op.Error](serr); ok {
			return nil, oerr
		}
		return nil, op.Wrap(op.CodeInternal, serr, "token repository check failed")
	}
	return out, nil
}

func rejectTakenOrgName(ctx context.Context, d Deps, name string) error {
	st, serr := requireStore(d)
	if serr != nil {
		return serr
	}
	var taken bool
	rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		_, ferr := tx.FindOrgByName(strings.ToLower(strings.TrimSpace(name)))
		if ferr == nil {
			taken = true
			return nil
		}
		if errors.Is(ferr, store.ErrNotFound) {
			return nil
		}
		return ferr
	})
	if rerr != nil {
		return op.Wrap(op.CodeInternal, rerr, "organization lookup failed")
	}
	if taken {
		return op.Errorf(op.CodeConflict, "organization name is taken")
	}
	return nil
}
