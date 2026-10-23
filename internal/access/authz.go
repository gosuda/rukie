package access

import (
	"github.com/gosuda/rukie/internal/op"
)

type Role int8

const (
	RoleNone Role = iota
	RoleRead
	RoleTriage
	RoleWrite
	RoleMaintain
	RoleAdmin
)

func ParseRole(s string) (Role, bool) {
	switch s {
	case "read":
		return RoleRead, true
	case "triage":
		return RoleTriage, true
	case "write":
		return RoleWrite, true
	case "maintain":
		return RoleMaintain, true
	case "admin":
		return RoleAdmin, true
	default:
		return RoleNone, false
	}
}

func (r Role) String() string {
	switch r {
	case RoleNone:
		return "none"
	case RoleRead:
		return "read"
	case RoleTriage:
		return "triage"
	case RoleWrite:
		return "write"
	case RoleMaintain:
		return "maintain"
	case RoleAdmin:
		return "admin"
	default:
		return "none"
	}
}

type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityInternal Visibility = "internal"
	VisibilityPrivate  Visibility = "private"
)

type ResourceKind int8

const (
	KindSite ResourceKind = iota + 1
	KindOrg
	KindTeam
	KindRepo
	KindInbox
	KindUserAcct
	KindTokenAcct
)

type Resource struct {
	OID          string
	Ref          string
	Attestations []Attestation
	Repo         RepoRecord
	DestOrgID    int64
	OrgID        int64
	TeamID       int64
	DestOwnerID  int64
	OwnerID      int64
	ChangeAuthor int64
	ClaimHolder  int64
	AssigneeID   int64
	AddresseeID  int64
	RepoID       int64
	NewScopes    op.Scopes
	Kind         ResourceKind
	LeaseHeld    bool
}

type Caller struct {
	Scopes     op.Scopes
	NewScopes  op.Scopes
	UserID     int64
	IsAdmin    bool
	Active     bool
	Authed     bool
	PublicOnly bool
	IsToken    bool
}

type Decision struct {
	Reason   string
	Allow    bool
	NotFound bool
}

func AllowDecision() Decision {
	return Decision{Allow: true, Reason: "allow"}
}

func deny(reason string) Decision {
	return Decision{Reason: reason}
}

func notFound() Decision {
	return Decision{Reason: "invisible", NotFound: true}
}

type floorSpec struct {
	floor     Role
	scope     op.Scope
	anonOpen  bool
	needsRule bool
}

var actionTable = map[string]floorSpec{
	"site.admin_console":    {RoleNone, op.ScopeAdminWrite, false, false},
	"site.user_create":      {RoleNone, op.ScopeAdminWrite, false, false},
	"site.user_lock":        {RoleNone, op.ScopeAdminWrite, false, false},
	"self.read":             {RoleNone, op.ScopeRepoRead, false, false},
	"user.view":             {RoleNone, op.ScopePublicRead, true, false},
	"self.update":           {RoleNone, op.ScopeIntentsWrite, false, false},
	"token.mint":            {RoleNone, op.ScopeRepoRead, false, false},
	"token.revoke":          {RoleNone, op.ScopeRepoRead, false, false},
	"token.list":            {RoleNone, op.ScopeRepoRead, false, false},
	"org.create":            {RoleNone, op.ScopeAdminWrite, false, false},
	"org.view":              {RoleNone, op.ScopeRepoRead, true, false},
	"org.update":            {RoleNone, op.ScopeAdminWrite, false, false},
	"org.member_add":        {RoleNone, op.ScopeAdminWrite, false, false},
	"org.get":               {RoleNone, op.ScopeRepoRead, true, false},
	"org.list":              {RoleNone, op.ScopeRepoRead, false, false},
	"org.rename":            {RoleNone, op.ScopeRepoWrite, false, false},
	"org.delete":            {RoleNone, op.ScopeRepoWrite, false, false},
	"org.member.list":       {RoleNone, op.ScopeRepoRead, true, false},
	"org.member.remove":     {RoleNone, op.ScopeRepoWrite, false, false},
	"org.leave":             {RoleNone, op.ScopeRepoWrite, false, false},
	"team.create":           {RoleNone, op.ScopeAdminWrite, false, false},
	"team.member_add":       {RoleNone, op.ScopeAdminWrite, false, false},
	"team.view":             {RoleNone, op.ScopeRepoRead, false, false},
	"team.list":             {RoleNone, op.ScopeRepoRead, false, false},
	"team.get":              {RoleNone, op.ScopeRepoRead, false, false},
	"team.update":           {RoleNone, op.ScopeRepoWrite, false, false},
	"team.delete":           {RoleNone, op.ScopeRepoWrite, false, false},
	"team.member.list":      {RoleNone, op.ScopeRepoRead, false, false},
	"team.member.add":       {RoleNone, op.ScopeRepoWrite, false, false},
	"team.member.remove":    {RoleNone, op.ScopeRepoWrite, false, false},
	"team.repo.list":        {RoleNone, op.ScopeRepoRead, false, false},
	"team.repo.add":         {RoleNone, op.ScopeRepoWrite, false, false},
	"team.repo.remove":      {RoleNone, op.ScopeRepoWrite, false, false},
	"repo.create_user":      {RoleNone, op.ScopeRepoCreate, false, false},
	"repo.create_org":       {RoleNone, op.ScopeRepoCreate, false, false},
	"repo.view":             {RoleRead, op.ScopeRepoRead, true, false},
	"repo.clone":            {RoleRead, op.ScopeRepoRead, true, false},
	"repo.push":             {RoleWrite, op.ScopeRepoWrite, false, true},
	"repo.fork":             {RoleRead, op.ScopeRepoWrite, false, false},
	"repo.settings":         {RoleMaintain, op.ScopeRepoWrite, false, false},
	"repo.visibility":       {RoleMaintain, op.ScopeRepoWrite, false, false},
	"repo.visibility_widen": {RoleAdmin, op.ScopeAdminWrite, false, false},
	"repo.transfer":         {RoleAdmin, op.ScopeAdminWrite, false, false},
	"repo.grant":            {RoleAdmin, op.ScopeAdminWrite, false, false},
	"repo.delete":           {RoleAdmin, op.ScopeAdminWrite, false, false},
	"repo.protect":          {RoleMaintain, op.ScopeRepoWrite, false, false},
	"change.create":         {RoleRead, op.ScopeIntentsWrite, false, false},
	"change.view":           {RoleRead, op.ScopeRepoRead, true, false},
	"change.comment":        {RoleRead, op.ScopeIntentsWrite, false, false},
	"change.edit":           {RoleWrite, op.ScopeRepoWrite, false, false},
	"change.review":         {RoleWrite, op.ScopeRepoWrite, false, true},
	"change.abandon":        {RoleRead, op.ScopeIntentsWrite, false, false},
	"change.land":           {RoleWrite, op.ScopeRepoWrite, false, true},
	"task.create":           {RoleTriage, op.ScopeIntentsWrite, false, false},
	"task.view":             {RoleRead, op.ScopeRepoRead, true, false},
	"task.edit":             {RoleTriage, op.ScopeIntentsWrite, false, false},
	"task.claim":            {RoleTriage, op.ScopeIntentsWrite, false, false},
	"task.unclaim":          {RoleTriage, op.ScopeIntentsWrite, false, false},
	"task.delete":           {RoleMaintain, op.ScopeRepoWrite, false, false},
	"ci.view":               {RoleRead, op.ScopeCIRead, true, false},
	"ci.trigger":            {RoleWrite, op.ScopeCITrigger, false, false},
	"ci.cancel":             {RoleWrite, op.ScopeCITrigger, false, false},
	"ci.approve":            {RoleWrite, op.ScopeCITrigger, false, false},
	"ci.config":             {RoleMaintain, op.ScopeCITrigger, false, false},
	"hook.view":             {RoleMaintain, op.ScopeWebhooksWrite, false, false},
	"hook.manage":           {RoleMaintain, op.ScopeWebhooksWrite, false, false},
	"mirror.view":           {RoleMaintain, op.ScopeMirrorsTrigger, false, false},
	"mirror.manage":         {RoleMaintain, op.ScopeMirrorsTrigger, false, false},
	"release.view":          {RoleRead, op.ScopeReleasesRead, true, false},
	"release.publish":       {RoleMaintain, op.ScopeReleasesWrite, false, false},
	"social.star":           {RoleRead, op.ScopeRepoRead, false, false},
	"social.list":           {RoleRead, op.ScopeRepoRead, true, false},
	"search.query":          {RoleRead, op.ScopeRepoRead, true, false},
	"inbox.read":            {RoleNone, op.ScopeRepoRead, false, false},
}

func specOf(action string) (floorSpec, bool) {
	spec, ok := actionTable[action]
	return spec, ok
}

func grantRole(res Resolver, c Caller, repo RepoRecord) Role {
	var direct, team Role
	if d, ok := res.DirectRole(repo.ID, c.UserID); ok {
		if repo.OrgID == 0 {
			direct = d
		}
	}
	if t, ok := res.TeamRole(repo.ID, c.UserID); ok {
		if t > team {
			team = t
		}
	}
	grant := direct
	if team > grant {
		grant = team
	}
	if repo.OrgID == 0 && repo.OwnerUserID != 0 && repo.OwnerUserID == c.UserID {
		grant = RoleAdmin
	}
	if repo.OrgID != 0 && res.IsOrgOwner(repo.OrgID, c.UserID) {
		grant = RoleAdmin
	}
	if grant < RoleRead {
		switch repo.Visibility {
		case VisibilityPublic:
			grant = RoleRead
		case VisibilityInternal:
			if c.Authed && !c.PublicOnly {
				grant = RoleRead
			}
		case VisibilityPrivate:
			grant = RoleNone
		}
	}
	return grant
}

func visibleRepo(res Resolver, c Caller, action string, repo RepoRecord) bool {
	switch repo.Visibility {
	case VisibilityPublic:
		return true
	case VisibilityInternal:
		return c.Authed && !c.PublicOnly
	case VisibilityPrivate:
		if !c.PublicOnly && grantRole(res, c, repo) >= RoleRead {
			return true
		}
		return action == "site.admin_console"
	default:
		return false
	}
}

func isAdmin(res Resolver, c Caller) bool {
	if c.IsAdmin {
		return true
	}
	return c.UserID != 0 && res.IsSiteAdmin(c.UserID)
}

func isActive(res Resolver, c Caller) bool {
	if !c.Active {
		return false
	}
	return c.UserID == 0 || res.IsActive(c.UserID)
}

func Check(res Resolver, c Caller, action string, r Resource) Decision {
	spec, ok := specOf(action)
	if !ok {
		return deny("deny:D1")
	}
	if !isActive(res, c) {
		if action == "self.read" && r.Kind == KindUserAcct && r.OwnerID == c.UserID && c.Authed {
			if !creationScopeCovers(c, action, spec) {
				return deny("scope")
			}
			return AllowDecision()
		}
		return deny("inactive")
	}
	if r.Kind == KindRepo {
		repo, found := res.Repo(r.RepoID)
		if !found {
			return notFound()
		}
		r.Repo = repo
		if !visibleRepo(res, c, action, repo) {
			return notFound()
		}
		if !c.Authed && !spec.anonOpen {
			return deny("anonymous-mutate")
		}
		if action == "site.admin_console" {
			if !isAdmin(res, c) {
				return deny("precondition")
			}
			if !creationScopeCovers(c, action, spec) {
				return deny("scope")
			}
			return AllowDecision()
		}
		grant := grantRole(res, c, repo)
		if grant < spec.floor {
			return deny("role-floor")
		}
		if pre := repoPrecondition(res, c, action, r, repo); pre != "" {
			return deny(pre)
		}
		if !creationScopeCovers(c, action, spec) {
			return deny("scope")
		}
		if spec.needsRule {
			if reason := protectionPass(res, c, grant, action, r, repo); reason != "" {
				return deny(reason)
			}
		}
		return AllowDecision()
	}
	if !visibleNonRepo(res, c, action, r) {
		return notFound()
	}
	if !c.Authed && !spec.anonOpen {
		return deny("anonymous-mutate")
	}
	if action == "site.admin_console" {
		if !isAdmin(res, c) {
			return deny("precondition")
		}
		if !creationScopeCovers(c, action, spec) {
			return deny("scope")
		}
		return AllowDecision()
	}
	if pre := nonRepoPrecondition(res, c, action, r); pre != "" {
		return deny(pre)
	}
	if !creationScopeCovers(c, action, spec) {
		return deny("scope")
	}
	return AllowDecision()
}

func scopeCovers(c Caller, need op.Scope) bool {
	if !c.IsToken {
		return true
	}
	return c.Scopes.Has(need)
}

func creationScopeCovers(c Caller, action string, spec floorSpec) bool {
	if scopeCovers(c, spec.scope) {
		return true
	}
	if !c.IsToken {
		return false
	}
	switch action {
	case "repo.create_user", "repo.create_org", "repo.fork":
		return c.Scopes.Has(op.ScopeRepoCreate)
	default:
		return false
	}
}

func visibleNonRepo(res Resolver, c Caller, action string, r Resource) bool {
	switch r.Kind {
	case KindRepo:
		return false
	case KindSite:
		return true
	case KindUserAcct, KindTokenAcct:
		return true
	case KindOrg:
		if res.OrgPublic(r.OrgID) {
			return true
		}
		if res.IsOrgMember(r.OrgID, c.UserID) {
			return true
		}
		return action == "site.admin_console" && isAdmin(res, c)
	case KindTeam:
		org, ok := res.TeamOrg(r.TeamID)
		if !ok {
			return false
		}
		if res.OrgPublic(org) {
			return true
		}
		if res.IsOrgMember(org, c.UserID) {
			return true
		}
		return action == "site.admin_console" && isAdmin(res, c)
	case KindInbox:
		return r.AddresseeID == c.UserID || (action == "site.admin_console" && isAdmin(res, c))
	default:
		return false
	}
}

func nonRepoPrecondition(res Resolver, c Caller, action string, r Resource) string {
	switch action {
	case "site.user_create", "site.user_lock":
		if !isAdmin(res, c) {
			return "precondition"
		}
		return ""
	case "self.read", "self.update":
		if r.OwnerID != c.UserID {
			return "precondition"
		}
		return ""
	case "token.mint", "token.revoke", "token.list":
		if r.OwnerID != c.UserID {
			if isAdmin(res, c) && action == "token.revoke" {
				return ""
			}
			return "precondition"
		}
		if action == "token.mint" && c.IsToken {
			for _, s := range op.AllScopes() {
				if r.NewScopes.Has(s) && !c.Scopes.Has(s) {
					return "scope"
				}
			}
			if r.NewScopes.Has(op.ScopePublicRead) {
				return "scope"
			}
		}
		return ""
	case "repo.create_user":
		if r.OwnerID != c.UserID {
			return "precondition"
		}
		return ""
	case "repo.create_org":
		if !res.IsOrgOwner(r.OrgID, c.UserID) && !res.CanCreateRepo(r.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "org.view":
		if res.OrgPublic(r.OrgID) {
			return ""
		}
		if res.IsOrgMember(r.OrgID, c.UserID) {
			return ""
		}
		return "precondition"
	case "org.update", "org.member_add":
		if c.IsToken {
			return "session-required"
		}
		if !res.IsOrgOwner(r.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "org.get", "org.member.list":
		if res.OrgPublic(r.OrgID) {
			return ""
		}
		if res.IsOrgMember(r.OrgID, c.UserID) {
			return ""
		}
		return "precondition"
	case "org.list":
		if c.IsToken {
			return "session-required"
		}
		if !c.Authed {
			return "precondition"
		}
		return ""
	case "org.rename", "org.delete", "org.member.remove":
		if c.IsToken {
			return "session-required"
		}
		if !res.IsOrgOwner(r.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "org.leave":
		if c.IsToken {
			return "session-required"
		}
		if !c.Authed || !res.IsOrgMember(r.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "org.create":
		if c.IsToken {
			return "session-required"
		}
		if !c.Authed {
			return "precondition"
		}
		return ""
	case "team.create", "team.member_add":
		if c.IsToken {
			return "session-required"
		}
		if !res.IsOrgOwner(r.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "team.update", "team.delete", "team.member.add", "team.member.remove", "team.repo.add", "team.repo.remove":
		if c.IsToken {
			return "session-required"
		}
		if !res.IsOrgOwner(r.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "team.list", "team.get", "team.member.list", "team.repo.list":
		if c.IsToken {
			return "session-required"
		}
		if !c.Authed {
			return "precondition"
		}
		if !res.IsOrgOwner(r.OrgID, c.UserID) && !res.IsOrgMember(r.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "team.view":
		if c.IsToken {
			return "session-required"
		}
		if res.IsOrgOwner(r.OrgID, c.UserID) {
			return ""
		}
		if res.IsOrgMember(r.OrgID, c.UserID) && res.IsTeamMember(r.TeamID, c.UserID) {
			return ""
		}
		return "precondition"
	case "inbox.read":
		if r.AddresseeID != c.UserID {
			return "precondition"
		}
		return ""
	default:
		return ""
	}
}

func repoPrecondition(res Resolver, c Caller, action string, r Resource, repo RepoRecord) string {
	switch action {
	case "repo.fork":
		if !destCreateRight(res, c, r.DestOwnerID, r.DestOrgID) {
			return "precondition"
		}
		return ""
	case "repo.transfer":
		if !destCreateRight(res, c, r.DestOwnerID, r.DestOrgID) {
			return "precondition"
		}
		return ""
	case "repo.delete":
		if repo.OrgID == 0 {
			if repo.OwnerUserID != c.UserID {
				return "precondition"
			}
			return ""
		}
		if !res.IsOrgOwner(repo.OrgID, c.UserID) {
			return "precondition"
		}
		return ""
	case "repo.grant":
		if repo.OrgID != 0 {
			return "deny:D6"
		}
		return ""
	case "task.unclaim":
		if r.ClaimHolder != 0 && r.ClaimHolder != c.UserID {
			grant := grantRole(res, c, repo)
			if grant < RoleMaintain {
				return "lease-hold"
			}
		}
		return ""
	case "task.claim":
		if r.LeaseHeld {
			return "lease-hold"
		}
		return ""
	case "change.abandon":
		if r.ChangeAuthor == c.UserID {
			return ""
		}
		grant := grantRole(res, c, repo)
		if grant < RoleWrite {
			return "role-floor"
		}
		return ""
	default:
		return ""
	}
}

func destCreateRight(res Resolver, c Caller, destOwner, destOrg int64) bool {
	if destOrg != 0 && destOwner == 0 {
		return res.IsOrgOwner(destOrg, c.UserID) || res.CanCreateRepo(destOrg, c.UserID)
	}
	if destOwner != 0 && destOrg == 0 {
		return destOwner == c.UserID
	}
	return false
}

func GuardLastOwner(members []int64, target int64) error {
	remaining := 0
	for _, m := range members {
		if m != target {
			remaining++
		}
	}
	if remaining == 0 {
		return errorString("cannot remove the last org owner")
	}
	return nil
}

type errorString string

func (e errorString) Error() string {
	return string(e)
}

func GuardLastAdmin(admins []int64, target int64) error {
	remaining := 0
	for _, m := range admins {
		if m != target {
			remaining++
		}
	}
	if remaining == 0 {
		return errorString("cannot deactivate the last site admin")
	}
	return nil
}

func ForkVisibilityOK(source, fork Visibility) bool {
	rank := map[Visibility]int{VisibilityPublic: 2, VisibilityInternal: 1, VisibilityPrivate: 0}
	return rank[fork] <= rank[source]
}

func FilterVisible(res Resolver, c Caller, repoIDs []int64) []int64 {
	var out []int64
	for _, id := range repoIDs {
		if d := Check(res, c, "repo.view", Resource{Kind: KindRepo, RepoID: id}); d.Allow {
			out = append(out, id)
		}
	}
	return out
}
