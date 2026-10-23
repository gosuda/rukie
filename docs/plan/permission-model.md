# Rukie Permission Model

## 0. Status and scope

This document is the normative authorization spec for the rukie rewrite
(agent-first Go forge, Ruk-only backend). It replaces Gitea's permission
system wholesale. Gitea references below are departure notes with file
evidence, not retained behavior.

Settled inputs (not reopened):

- Principals are humans only. Agents authenticate as scoped tokens on human
  accounts. No agent principals, no deploy keys, no Actions users, no OAuth2
  provider, no package-registry tokens.
- Identity is argon2id+TOTP or OIDC; no email delivery. Admin CLI resets
  passwords out of band.
- One operation table (`Op[In,Out]`, Huma v2) derives HTTP/JSON, MCP tools,
  generated `rukie <op>` CLI, and zero-JS HTML forms. Every enforceable
  operation below MUST be one `Op` with an `Action` id in §9.
- Metadata store backend is still open (SQLite/Pebble/PostgreSQL); this spec
  assumes one store module and never names a backend.
- Ruk records review/CI facts as signed attestations and task claims as
  leases; rukie evaluates them at land/claim time but never mints Ruk
  authority itself.

### What Gitea does today (departure baseline)

- Five `AccessMode` levels `none < read < write < admin < owner`
  (`rukie/models/perm/access_mode.go:16-23`), with `owner` reachable for repo
  owners, site admins, and owner-team members.
- Per-unit overrides: `Permission{AccessMode, unitsMode map[unit.Type]AccessMode}`
  (`rukie/models/perm/access/repo_permission.go:29-39`), team base mode plus
  per-unit `team_unit.access_mode`
  (`rukie/models/organization/team.go:81-83`, `team_unit.go:19-20`) is cut.
  One team grants one role per repo.
- Resolution order in `GetIndividualUserRepoPermission`
  (`rukie/models/perm/access/repo_permission.go:474-518`): token-public-only
  gate → private-anon deny → owner-visibility gate
  (`HasOrgOrUserVisible`, `rukie/models/organization/org.go:457-461`) →
  site-admin/owner fast path → `modeByOwnerAndAccess`
  (`rukie/models/perm/access/access.go:38-72`) → public-read floor →
  team-unit max. Owner-team membership short-circuits to owner.
- Token scopes are per-category `read:<cat>/write:<cat>` plus `public-only`
  and `all` (`rukie/models/auth/access_token_scope.go`), carried on the doer
  (`rukie/models/user/user_extdata.go:57-108`); site-admin-via-token requires
  full scope (`DoerIsSiteAdminFullAccess`, same file:114-120).
- Owner visibility `public|limited|private`
  (`rukie/modules/structs/visible_type.go:9-18`) gates repo reachability
  independently of `repository.is_private`; listing queries encode this in SQL
  (`AccessibleRepositoryCondition`, `rukie/models/repo/repo_list.go:661-700`).
- Branch protection is a separate per-branch rule object with push/merge
  allowlists, required approvals, status checks, signed-commit flags
  (`rukie/models/git/protected_branch.go:29-80,134-243`).
- Actions tokens have their own vocabulary (`contents`/`read-all`/`write-all`,
  per-scope `read`/`write`/`none`, clamping by `MaxTokenPermissions`) in
  `rukie/services/actions/token_permission_design.md`. That vocabulary is
  **cut with Actions** and is NOT reused: it is unit-scoped (`code`, `issues`,
  `packages`, …) and workflow-centric, while rukie scopes (§6) are
  role-lattice ceilings over the single `Op` table. Reusing it would preserve
  the per-unit matrix this rewrite deletes.

Rukie deliberately keeps **none** of the per-unit matrix, none of the owner
`VisibleType` axis, and none of the branch-protection object shape. It keeps
three ideas: max-over-grants resolution, public-only token restriction, and
404-instead-of-403 for invisible resources.

ID namespaces (no collisions): §9 decision-table rows use `S/O/M/R/C/K/I/H/N/L/F/E/B`
plus a number (e.g. table row `R5` = `repo.push`). §8 bookmark-guard rules use
`G0–G11` (e.g. guard `G5` = no self-approval). A bare `R5` always means the
table row; a bare `G5` always means the guard rule. Phase references never use
bare `R` IDs (see §10).

---

## 1. Principals

```go
type Principal struct {
    Kind        PrincipalKind // Anonymous | User | System
    UserID      int64         // 0 for Anonymous/System
    IsSiteAdmin bool          // only when Kind == User
    IsActive    bool          // false = deactivated / locked / prohibit-login
    Token       *TokenScope   // nil = session auth (password/TOTP/OIDC cookie/SSH);
                             // non-nil = scoped token acting as this user
}

type TokenScope struct {
    Scopes          []Scope   // ceiling; §6
    PublicOnly      bool      // restrict to public resources even if Scopes allow more
    ExpiresAt       time.Time // enforced before authz.Check; expiry => unauthenticated
    CredentialID    int64     // audit only
}

type SystemReason int
const (
    SystemCI SystemReason = iota // CI scheduler/runner internals
    SystemMirror                  // mirror sync worker
    SystemWebhook                 // webhook dispatcher
    SystemGC                      // store maintenance
)
```

Rules:

1. **P1 — Human-only agency.** `Kind == User` covers interactive sessions and
   scoped tokens. There are no bot users, no deploy-key principals, no CI-user
   principals. CI runners, mirror workers, and webhook dispatchers use
   `Kind == System` with a `SystemReason`; they never impersonate a user and
   never pass `authz.Check` as that user.
2. **P2 — Token is a restriction.** A token authenticates *as* its user and
   can only narrow that user's power (§6, ASCII formula F4). A token can never
   add grants, roles, team memberships, or site-admin power the user lacks.
3. **P3 — Session is full power.** `Token == nil` (first-factor + TOTP/OIDC
   session, or SSH session key bound at login) exercises the user's full
   grants. Scope intersection is skipped; all other gates still apply
   (visibility, role, protected bookmarks).
4. **P4 — Anonymous is read-only on public repos.** `Kind == Anonymous` may
   exercise exactly the `public only` cells of the §9 `anon` column (browse,
   clone, view, list). Anonymous fork, star, watch, comment, and every mutate
   action deny at §7.3 step 4 with `Forbidden/anonymous-mutate` before any
   role is consulted (§11 D2). There is no anonymous fork/star/watch path.
5. **P5 — Inactive denies all except self-read.** `IsActive == false` denies
   every action except `self.read` on the caller's own profile (view locked
   state) and admin-CLI recovery (out of band, not an `Op`). The exception
   still enforces token expiry and scope (§7.3 step 2); there is no other
   inactive path.
6. **P6 — Site admin is a grant, not a bypass.** `IsSiteAdmin` grants `admin`
   on a repo only through the admin-console path defined in §7.1 (F1b): a
   site admin with no repo grant is invisible to private repos for ordinary
   actions and must go through `site.admin_console`. When `Token != nil`, the
   admin is still intersected with scopes — an admin holding a `read` token
   has exactly `read` (Gitea precedent: `DoerIsSiteAdminFullAccess` in
   `rukie/models/user/user_extdata.go:114-120`). Site admin never bypasses
   protected-bookmark attestation gates (§8, guard G9).

---

## 2. Namespaces and resources

Two namespaces. No nested orgs. No owner-visibility axis.

| Namespace | Owner | Member semantics |
|---|---|---|
| `user/<name>` | exactly one user | Collaborator grants per repo (§5); no teams |
| `org/<name>` | the org entity | Org roles (§5) + teams (§5); teams hold all repo grants |

```go
type ResourceKind int
const (
    RSite ResourceKind = iota
    ROrg
    RTeam
    RRepo
    RChange   // a Ruk change / stack under review (change-id rooted)
    RBookmark // a ref-namespace rule target: e.g. `main`, `release/*`
    RTask
    RCIRun    // run + its jobs/logs/artifacts as one object
    RCIConfig // pipeline config + secrets (separate from runs)
    RWebhook
    RMirror
    RRelease
    RInboxItem // addressed to one user; parent is the user, not a repo
)

type Resource struct {
    Kind       ResourceKind
    SiteID     string // constant "rukie" (keeps Op table uniform)
    OrgID      int64  // for org-owned objects; org itself carries its ID here
    OwnerID    int64  // user-namespace owner, or org id as user id
    RepoID     int64  // for repo-scoped objects; 0 when not repo-scoped
    ChangeID   string // Ruk change id (RChange)
    TaskID     int64
    RunID      int64
    Visibility Visibility // denormalized effective visibility at check time

    // Subject fields for context-dependent floors (§9.1 C6, K3, K5).
    // Loaded by the Op handler before Check; zero means "not applicable".
    ChangeAuthorID int64 // author of RChange (C6 own-vs-others)
    AssigneeID     int64 // proposed assignee (K3 assignability floor)
    ClaimHolderID  int64 // current Ruk lease holder of the task (K5)
    AddresseeID    int64 // owner of RInboxItem (B1)

    // Destination namespace for copy/move actions (§9.1 R6 fork, R10 transfer).
    // Loaded by the Op handler before Check; zero means "missing" (rejected).
    DestOwnerID int64 // target user-namespace owner (R6, R10 into user ns)
    DestOrgID   int64 // target org (R6, R10 into org ns)
}
```

Hierarchy for grant inheritance (only downward, never upward):

```
Site
├── user/<name> ── Repo (user-ns)
└── org/<name> ── Team, Repo (org-ns) ── Change, Bookmark, Task, CIRun,
                                          CIConfig, Webhook, Mirror, Release
```

A grant on an org never implies a grant on its repos (except org-owner, §5).
A grant on a repo implies the same level on its subordinate objects except
where §9 explicitly raises the floor (e.g. mirror config needs `maintain`
even though the change list needs `read`).

`repo_of(r)` returns `(repoID, true)` for `RRepo, RChange, RBookmark, RTask,
RCIRun, RCIConfig, RWebhook, RMirror, RRelease` **with `RepoID != 0`**; it
returns `(0, false)` for `RSite, ROrg, RTeam, RInboxItem` and for any resource
with `RepoID == 0`. A not-yet-existing repo (creation intent) therefore never
counts as repo-scoped. `site.admin_console` (S1) is exempt from both branches
per §7.3 step 6a: it checks `IsSiteAdmin` only and never consults `floor()`
or `grant_role()`.

Creation classification (normative, before `Check`): repository creation MUST
be checked as a non-repo target, never as `RRepo`, because the repo does not
exist yet and `floor()` is undefined for `—` rows:

- R1 `repo.create_user` → `Resource{Kind: RSite, OwnerID: <dest user>}`.
  The destination user namespace travels in `OwnerID`, not `RepoID`.
- R2 `repo.create_org` → `Resource{Kind: ROrg, OrgID: <dest org>}`.
  The destination org travels in `OrgID`.

Handlers that construct `RRepo` with `RepoID == 0` for R1/R2 are in violation;
`repo_of` returns false for them by the `RepoID != 0` clause, so they still
land in the precondition branch, but new code MUST use the classified shapes
above. Fork (R6) and transfer (R10) are different: their sources exist
(`RepoID != 0`, repo-scoped source half) and their destinations travel in
`DestOwnerID` / `DestOrgID`, verified via `dest_create_right` (§7.1 F1d).

---

## 3. Visibility

Three repo visibility levels. No per-owner visibility. No per-unit anonymous/
everyone overrides (Gitea's `RepoUnit.AnonymousAccessMode/EveryoneAccessMode`,
`rukie/models/repo/repo_unit.go:47-50`, are cut). The table below is
normative for what anonymous callers can do; §9 R6/F1/C3 and §7.3 step 4
agree with it (all anonymous mutations deny).

| Level | Anonymous | Any authenticated user | Explicit grantee | Site admin (ordinary action) |
|---|---|---|---|---|
| `public` | browse/clone/view/list only; appears in explore/search. Fork, star, watch, comment, and every mutate action deny (`Forbidden/anonymous-mutate`). | browse + clone + fork + star/watch; appears in explore/search | same | same as authenticated user |
| `internal` | invisible (`NotFound`, §10); absent from explore/search | browse + clone + fork + star/watch; appears in explore/search | same | same as authenticated user |
| `private` | invisible; absent | invisible unless grantee; absent from explore/search/forks/stars for non-grantees | per grant | invisible unless grantee; must use `site.admin_console` (§7.1 F1b) |

Rules:

3. **V1 — Single axis.** Effective visibility of a repo is one enum stored on
   the repo row. The org's visibility (org profile `public|private`) is a
   ceiling: a repo in a private org MUST NOT be `public` or `internal`
   (create/update rejects; org-made-private cascades repos to `private`,
   mirroring Gitea's fork-visibility cascade precedent in
   `rukie/services/repository/repository.go:184-198`).
4. **V2 — Internal means signed-in.** `internal` requires an active
   authenticated principal (`User`, any site). Public-only tokens (§6) and
   anonymous callers see `internal` as nonexistent.
5. **V3 — Private means grant-or-console.** `private` is visible iff
   `grant_role(p, repo) ≥ read` (grant role defined in §7.1 F1a, which
   excludes site admin) or the action is `site.admin_console` executed by a
   site admin (F1b). A site admin calling any ordinary action (§9 R3–L2, C2,
   K2, I1, F1–F2, E1) on a private repo without a grant gets `NotFound`,
   exactly like a stranger. Console-mediated reads are logged and never leak
   into another user's listings.
6. **V4 — Subordinate objects inherit.** Changes, tasks, CI runs/logs,
   releases, webhooks/mirrors configs, bookmarks: effective visibility equals
   the parent repo's. No object is more visible than its repo. Inbox items are
   visible only to their addressee and site admins (via admin console).
   Creation targets (§2) have no repo visibility yet; R1 checks no repo
   visibility (RSite is always visible, §7.3.1) and R2 checks destination-org
   visibility (private destination orgs are `NotFound` to outsiders).
7. **V5 — Listings are filtered reads.** Explore, search, fork networks, star/
   watch lists, org repo lists, and user repo lists MUST apply the same
   visibility gate as a single-object read *before* any other predicate. A
   listing MUST NEVER contain an object the caller could not `repo.view`
   (INV-5). Site-admin listings use the same gate: private repos without
   grants are absent from ordinary explore/search even for admins.
8. **V6 — Forks never widen.** Fork visibility ≤ source visibility at fork
   time and after every source visibility tightening (source made private ⇒
   forks made private in the same transaction). Widening a fork beyond its
   source is rejected.

---

## 4. Org roles, teams, repo roles

### 4.1 Org roles

Exactly two: `owner`, `member`.

| Capability | member (alone) | owner |
|---|---|---|
| View org profile (public org) | yes (anyone) | yes |
| View private org profile | only if member | yes |
| List org teams / team membership | only own memberships unless repo grant implies | yes (all) |
| Create/update/delete teams, add/remove members, set team repo grants | no | yes |
| Create repos in org | only via team `can_create_repo` flag (§4.2) | yes |
| Manage org settings (rename, visibility, delete org) | no | yes |
| Repo access by membership alone | **none** — membership grants zero repo power | `admin` on every org repo (owner team equivalence) |
| Last-owner guard | — | removing the last owner is rejected |

Membership alone granting zero repo access is intentional and differs from
Gitea's public-read floor (which in rukie comes from repo visibility §3, not
from membership).

### 4.2 Teams

```go
type Team struct {
    ID           int64
    OrgID        int64
    Name         string
    Members      []int64            // user ids; all are org members
    RepoGrants   map[int64]RepoRole // repoID -> role; repo MUST be in OrgID
    CanCreateRepo bool              // may create repos in this org
}
```

- Teams belong to exactly one org. A team's `RepoGrants` keys MUST all be repos
  owned by that org (enforced on write; cross-org grant rows are rejected).
- No per-unit modes. Gitea's `Team{AccessMode, Units[]}` + `team_unit`
  (`rukie/models/organization/team.go:81-83`, `team_unit.go:19-20`) is cut.
  One team grants one role per repo.
- No team-admin delegation in v1: only org owners manage teams. (Non-goal;
  do not add `team.manager` until a follow-up spec.)
- Special immutable `Owners` team per org: members == org owners,
  `RepoGrants` == `admin` on all current and future org repos,
  `CanCreateRepo == true`. Deleting it or removing its last member is rejected.

### 4.3 Repo roles

Strict total order. `none` is the absence of a grant, not a storable role.

```
none < read < triage < write < maintain < admin
```

`triage` and `maintain` are new: Gitea's `AccessMode` has no equivalent
(`access_mode.go:16-23` stops at `read/write/admin/owner`), and Gitea
expresses their substance through per-unit matrices and service-level
exceptions instead. The import mapping is fixed in §14 (Gitea `admin` →
rukie `admin`; `maintain`/`triage` start empty; admins demote explicitly).

Role meanings (detailed per-operation mapping is §9; this is the prose
contract):

- **read** — view and copy for authenticated grantees: browse tree, view
  files/history/blame/diff, clone/fetch, download archives, view
  changes/tasks/CI runs/releases. Authenticated `read` holders may additionally
  fork (needs `write` scope, R6), star/watch (F1), create changes via
  `refs/for/*` (§8 G1), and comment (needs `triage` scope, C3/K-comments). No
  direct push to any bookmark. No approve. No land. Anonymous callers get the
  public-view subset only (§3/P4); the fork/star/comment extensions in this
  paragraph are authenticated-only.
- **triage** — read plus task stewardship: create/edit/label/assign/close/
  reopen tasks, acquire/release task claims (task stewardship plus the
  claim-hold rule in §8 G11). Triage cannot push, cannot approve, cannot land,
  cannot trigger CI reruns, cannot touch settings or integrations.
- **write** — triage plus code ingress: direct push to *unprotected*
  bookmarks, all change operations except land (create, edit, rebase, split,
  squash on any change in the repo, review and approve), CI trigger, rerun,
  cancel, and gated-run approval. Write has no release-publish power and no
  webhook, mirror, or CI-config power. Write satisfies attestation signer
  floors that ask for `write` or above.
- **maintain** — write plus repo stewardship short of destruction: manage
  protected-bookmark rules, webhooks, mirrors, CI config and secrets, publish
  and edit releases, edit repo settings including visibility narrowing, run
  task bulk operations. Cannot delete the repo, cannot transfer it, cannot
  widen visibility, cannot manage org teams.
- **admin** — full repo control: all of the above plus visibility widening,
  transfer (with destination-namespace create right), collaborator grants on
  user-namespace repos via table row R11, and repo deletion. Org-namespace
  repo membership changes go through M1 team management, never through R11.
  Org owners always resolve to `admin`.

Direct (collaborator) grants exist ONLY on user-namespace repos, mapping
user→role. Org-namespace repos grant exclusively via teams (plus the Owners
team). Storing a direct collaborator row on an org repo is rejected.

---

## 5. What each role permits per operation family

§9 is normative. This section states the shape so the table is unsurprising:

- **browse** (tree, file, blame, history, diff, compare, archive): `read`
  (anonymous: public repos only).
- **clone** (smart HTTP + SSH fetch, `fetch`/`push.reconcile` reads): `read`
  (anonymous: public repos only).
- **push** (direct ref update incl. force/delete where the transport allows):
  `write`, minus protected bookmarks (§8). `triage` and `read` direct-push
  always deny, even with force. Anonymous denies at step 4.
- **change review/approve**: comment needs `read` role with `triage` scope
  (authenticated only); approve (attestation-grade `review:approved`) needs
  `write` from a non-author (§8 G5). Content edits (rebase, split, squash,
  patch-set) need `write`. An author without `write` may only comment on and
  abandon their own change (see §9 rows `change.edit`, `change.abandon`).
- **land**: `write` floor plus protected-bookmark rule passage (allowlists
  plus required attestations, §8). Land is never available below `write`,
  including on unprotected bookmarks.
- **task edit/claim**: edit, label, assign, close need `triage`; claim acquire
  and release need `triage` plus Ruk lease hold (holder-only release, §8 G11).
  All authenticated-only.
- **CI trigger/cancel**: trigger, rerun, cancel, and gated-run approval need
  `write`; CI config and secrets management needs `maintain`; viewing runs
  and logs needs `read` (anonymous: public runs only).
- **mirror config**: `maintain` (configure, edit, trigger-now, delete).
- **webhook config**: `maintain` (create, edit, delete, redeliver, test).
- **release publish**: view and download need `read` (anonymous: public only);
  publish, edit, and delete need `maintain`. Asset upload to a draft release
  needs `write`; attaching assets to a published release needs `maintain`.
  Deleting a tag release that matches a protected tag pattern additionally
  needs the bookmark rule's allowlist (§8 G8).
- **settings** (description, default bookmark, options, visibility narrowing):
  `maintain`. Visibility widening and transfer need `admin`.
- **delete** (repo, org, team; webhook, mirror, and release destructive ops
  live under their own families): repo delete needs `admin` plus the
  user-namespace-owner or org-owner precondition; org delete needs org `owner`;
  team delete needs org `owner`.
- **fork / star / watch**: authenticated-only mutations (§9 R6/F1, anon `no`).
  They are NOT anonymous read actions even on public repos; §7.3 step 4 denies
  anonymous callers before any floor is consulted.
- **create (R1/R2)**: bound to the destination namespace (§7.1 F1c/F1d).
  Creating in another user's namespace, or in an org without owner or
  `can_create_repo` standing, denies even for otherwise active callers.

---

## 6. Token scopes

Scopes are a ceiling over operation families, orthogonal to roles. A token
carries a set; the effective allow for an action requires BOTH the role floor
(or non-repo precondition in §9) AND a scope covering the action (formula F4).

```go
type Scope string
const (
    ScopeRead     Scope = "read"      // all read-only ops: view/browse/clone/star/watch-list/views,
                                      // view changes/tasks/runs/releases, search/explore, inbox read.
                                      // Fork is NOT read: forking needs ScopeWrite (R6).
                                      // Starring/watching need ScopeRead but also sign-in (F1).
    ScopeTriage   Scope = "triage"    // read + task create/edit/claim + change/task comments + change.create
    ScopeWrite    Scope = "write"     // triage + fork, direct push (unprotected), change edit/review/approve/land,
                                      // CI trigger/rerun/cancel/approve, draft-release asset upload
    ScopeMaintain Scope = "maintain"  // write + webhooks, mirrors, CI config, releases, repo settings
    ScopeAdmin    Scope = "admin"      // maintain + visibility-widen, transfer, grants, delete,
                                      // org/team management, site admin console (if site admin)
    ScopePublicOnly Scope = "public-only" // RESTRICTION flag, not a grant (see below)
)
```

Scope lattice (grant order):

```
read < triage < write < maintain < admin
```

A token holding scope S covers every action whose §9 `scope` column requires
≤ S. `admin` covers all. `change.create` (C1) requires `triage` scope even
though it creates reviewable content; every other change-mutation action
requires `write`; `repo.fork` (R6) requires `write` scope even though it is
initiated from a `read` grant. Creation scopes bind with the namespace: R1/R2
require `write` scope **plus** the F1c namespace precondition; a `write`
token does not create into foreign namespaces. The `PublicOnly` flag is not a
level: when set, the token additionally fails every action on a non-`public`

[809 more lines in resource. Use :16885 to continue]
level: when set, the token additionally fails every action on a non-`public`
resource regardless of Scopes (Gitea precedent: `public-only` scope +
`DoerTokenAllowsRepo` / `PublicRepoUnderPublicOwnerCond` in
`rukie/models/repo/repo_list.go:744-783` and
`rukie/models/user/user_extdata.go:105-108`).

Minting rule (tokens create tokens only via an explicit `token.mint` op):
child scopes ≤ parent scopes, and `PublicOnly(parent) ⇒ PublicOnly(child)`.
(Gitea precedent: `CanCreateChildScope` / `EnforcePublicOnlyFrom` in
`rukie/models/auth/access_token_scope.go`.) A session (`Token == nil`) may
mint any scope set for its own user; a token may only mint at-or-below itself.

SSH transport authentication selects the same `Principal`: the SSH key is
bound to a user at login (session-equivalent) — it does not carry independent
scope. Agent automation over SSH MUST use a token-bound credential, not a raw
key, when scope restriction is wanted.

---

## 7. Effective permission and decision function

### 7.1 Grant resolution

```
// F1a  grant_role(p, repo) = max over applicable grants, or none.
//      Site admin is EXCLUDED here by construction (see F1b).
//      Consulted ONLY for repo-scoped resources with RepoID != 0 (existing repos).
grant_role(p, repo) = max(
    owner_role(p, repo),    // admin iff user-ns owner, or org owner of org-ns repo
    direct_role(p, repo),   // collaborator grant; user-ns repos only
    max_{t in teams(p, org)} team_grant(t, repo),
    public_floor(repo),     // read iff repo public and caller passes V-gates
    internal_floor(p, repo) // read iff repo internal and p is active authenticated
)
// F1b  admin-console path (the ONLY grant-independent site-admin power).
//      Ordinary actions never take this branch.
admin_console(p, a) = (p.Kind == User && p.IsSiteAdmin && a == "site.admin_console")
// F1c  non-repo preconditions (for rows whose §9 role cell is "—", incl. R1/R2 creation).
//      Evaluated INSTEAD of a role floor; scope (F4) still applies.
//      Creation rows are namespace-bound (see F1d); bare "active user" never suffices.
precondition(p, a, r) = match a:
    site.admin_console | site.user_create | site.user_lock : IsSiteAdmin
    self.read   : p.UserID == r.OwnerID
    self.update : p.UserID == r.OwnerID            // scope triage required (§9 S4)
    token.*     : p.UserID == r.OwnerID            // own tokens; S1 for others'
    org.create  : active authenticated user        // no namespace; any active user
    repo.create_user : active authenticated user AND p.UserID == r.OwnerID
                      // R1 target is RSite{OwnerID: dest user} (§2); foreign OwnerID denies.
    repo.create_org  : (is_org_owner(p, r.OrgID) OR has_can_create_repo(p, r.OrgID))
                      // R2 target is ROrg{OrgID: dest org} (§2); bound to that org only.
    org.view    : org public OR org member OR admin_console
    org.update/delete | org.member_* | team.* : org owner of r.OrgID (last-owner guard, INV-19)
    team.view   : org owner OR team member (private org ⇒ NotFound for outsiders)
    repo.transfer : admin on source (repo branch) AND dest_create_right on dest
    repo.delete   : admin on repo (repo branch) AND namespace-owner precondition
    repo.grant    : admin on repo (repo branch); user-ns rows only
    social/inbox/search : per §9 Notes (read-equivalent or addressee)
// F1d  destination create-right (defines the F8 predicate used by R6/R10).
//      Exactly one of (DestOwnerID, DestOrgID) must be set; both-set or both-zero denies (D11).
dest_create_right(p, destOwnerID, destOrgID) =
    (destOrgID != 0 AND destOwnerID == 0) → (is_org_owner_user(p, destOrgID) OR has_can_create_repo(p, destOrgID))
    (destOwnerID != 0 AND destOrgID == 0) → (p.Kind == User AND p.UserID == destOwnerID)
    otherwise → false
//      Foreign destinations therefore deny: a caller cannot fork/transfer into another
//      user's namespace (UserID mismatch) nor into an org where they lack owner /
//      can_create_repo standing. Site-admin console does not confer dest rights;
//      admins fork/transfer through their own grants like everyone else (G9 spirit).
// F2  owner team equivalence
p ∈ owners(org)  ==>  grant_role(p, r) = admin  for every r in org
// F3  membership alone grants nothing
p ∈ members(org) \ owners(org), no team grant  ==>  contributes none
```

Notes: `max` uses the §4.3 order. `public_floor`/`internal_floor` apply only
to read-equivalent actions and never satisfy a floor above `read` (a public
repo does not make strangers `triage`). Site admin contributes nothing to
`grant_role`; admin power over private repos without grants flows exclusively
through `admin_console(p, a)` plus F5b visibility.

### 7.2 Scope intersection (token side)

```
// F4  scope gate (only when p.Token != nil)
scope_covers(p, action) =
    level(required_scope(action)) ≤ level(max(p.Token.Scopes))
    ∧ ¬(p.Token.PublicOnly ∧ visibility(resource) ≠ public)
// F5a  final allow, existing repo-scoped resources (repo_of(r) = true, non-console action)
allow_repo(p, a, r) =
    active(p) ∧ system_pass(p, a)
    ∧ visible(p, r, a)
    ∧ grant_role(p, r) ≥ floor(a, r)
    ∧ dest_allows(p, a, r)
    ∧ scope_covers(p, a)
    ∧ protection_pass(p, a, r)   // §8: allowlists + attestations + lease hold
    ∧ ¬denied(p, a, r)           // §11 deny rules D1..D11
// F5b  final allow, non-repo resources incl. creation targets (repo_of(r) = false)
allow_nonrepo(p, a, r) =
    active(p) ∧ system_pass(p, a)
    ∧ visible_nonrepo(p, r, a)   // defined in §7.3.1; R1→RSite-true, R2→dest-org gate
    ∧ precondition(p, a, r)      // F1c instead of a role floor; R1/R2 namespace-bound
    ∧ scope_covers(p, a)
    ∧ ¬denied(p, a, r)
// F5c  admin-console allow (action == site.admin_console, any resource kind)
allow_console(p, a, r) =
    active(p) ∧ admin_console(p, a) ∧ scope_covers(p, a) ∧ ¬denied(p, a, r)
//      Visibility for console calls passes via the admin_console branch of
//      visible_repo / visible_nonrepo; grant_role is never consulted.
```

`floor(a, r)` is the §9 minimum-role cell for existing-repo non-console
actions (a bookmark-protected push/land raises the effective bar via
`protection_pass`, never lowers it). `floor()` is undefined for `—` rows
(including R1/R2) and for `site.admin_console` by design; those paths use
F1c/F5b/F5c instead. Context-dependent floors read the §2 subject fields: C6
compares `p.UserID` against `r.ChangeAuthorID`; K3 checks the assignee floor
against `r.AssigneeID` (assignee must resolve to ≥ `read` on the repo); K5
compares `p.UserID` against `r.ClaimHolderID` (holder-only, `maintain`
override); R6 and R10 additionally verify `r.DestOwnerID`/`r.DestOrgID` via
`dest_allows` (F1d/F8). All conjuncts are mandatory; failure of any one
denies. Evaluation order is fixed (§7.3) so that 404-vs-403 (§10) is
deterministic.

### 7.3 `authz.Check` signature (normative)

```go
package authz

type Action string // e.g. "repo.view", "repo.push", "change.land", ... (§9.1)

type Decision struct {
    Allow  bool
    // Exactly one of these is set when Allow == false:
    NotFound  bool   // caller must render 404 (invisible resource, §10)
    Forbidden bool   // caller must render 403 (visible, insufficient grant)
    Reason    string // machine-readable code: "inactive", "anonymous-mutate",
                     // "invisible", "role-floor", "scope", "protected", ... (§10)
}

// Check is the SOLE authorization entry point for every Op.
// Callers MUST NOT replicate grant/visibility/scope logic around it.
// Handlers load the §2 subject fields (author/assignee/holder/addressee/dest)
// into r BEFORE calling Check; zero means "missing" (rejected for R6/R10).
// R1/R2 handlers MUST use the §2 creation-classified shapes (RSite/ROrg);
// R6/R10 handlers MUST populate DestOwnerID/DestOrgID; Check rejects violations.
func Check(ctx context.Context, p Principal, a Action, r Resource) Decision
```

Evaluation order (normative pseudocode):

```
1. if p.Kind == System: return system_gate(p, a, r)   // §11 D10; only infra ops
2. if p.Kind == User and !p.IsActive:
      if a == "self.read" and r.OwnerID == p.UserID:
         if p.Token != nil and (expired(p.Token) or !scope_covers(p, a, r)):
            return Deny(Forbidden, "scope")           // inactive exception keeps expiry+scope teeth (INV-1)
         return Allow
      return Deny(Forbidden, "inactive")              // P5 exception is scoped self.read only
3. if p.Token != nil and expired(p.Token): return Deny(Forbidden, "token-expired")
4. if action_is_mutation(a) and p.Kind == Anonymous: return Deny(Forbidden, "anonymous-mutate")
      // Mutation = every action whose §9 anon cell is not "public only".
      // Includes create (R1/R2), fork (R6), star/watch (F1), comments (C3), task create (K1).
5. resolve visibility(r) from store (repo row + org ceiling, §3 V1/V4; creation §3 V4-p2).
   // R1 (RSite target): visible_nonrepo RSite branch → always true; step 5 never blocks creation.
   // R2 (ROrg target): visible_nonrepo ROrg branch → dest-org gate; private-org outsiders get NotFound here.
   if repo_of(r):
      if !visible_repo(p, r, a): return Deny(NotFound, "invisible")  // §10: no oracle
   else:
      if !visible_nonrepo(p, r, a): return Deny(NotFound, "invisible")
6a. if admin_console(p, a):
      // Console path bypasses grant_role and floor() entirely (F5c).
      // grant_role(p, r) is admin/none-independent here by design.
      goto step 7.
6b. // R1/R2 creation ALWAYS lands here (never in the repo_of branch): by §2 they are
    // classified as RSite/ROrg with RepoID == 0, so repo_of(r) == false and floor() is
    // never consulted for them. Any RRepo-shaped creation call is a handler bug and
    // still lands here via the RepoID == 0 clause — it can never reach floor().
    if repo_of(r):
      if (a == "repo.fork" or a == "repo.transfer") and r.DestOwnerID == 0 and r.DestOrgID == 0:
         return Deny(Forbidden, "precondition")       // dest fields are mandatory (§2, D11)
      role = grant_role(p, repo_of(r))                // F1a; site admin EXCLUDED
      if role < floor(a, r): return Deny(Forbidden, "role-floor")    // C6/K3/K5 read subject fields here
      if a == "repo.fork" or a == "repo.transfer":
         if !dest_allows(p, a, r): return Deny(Forbidden, "precondition")  // F1d/F8 dest half
    else:
      if (a == "repo.create_user" or a == "repo.create_org") and (r.OwnerID == 0 and r.OrgID == 0):
         return Deny(Forbidden, "precondition")       // creation namespace is mandatory (F1c)
      if !precondition(p, a, r):                      // F1c; floor() is undefined here BY DESIGN
         return Deny(Forbidden, "precondition")       // step 5 already screened hidden orgs/teams
7. if p.Token != nil and !scope_covers(p, a, r): return Deny(Forbidden, "scope")
8. if repo_of(r) and a != "site.admin_console" and !protection_pass(p, a, r):  // §8; non-repo + console skip
      return Deny(Forbidden, "protected")  // reason detail: "allowlist"|"attestation"|"lease-hold"|"author-approve"
9.  if denied(p, a, r): return Deny(Forbidden, "deny")               // §11
10. return Allow
```

Step 6a is load-bearing: `site.admin_console` never consults `grant_role()`
(which excludes site admin by F1a) or `floor()` (which is undefined for `—`
rows). Without 6a the console action would deny on every repo-scoped resource
because `grant_role` returns `none` for grantless admins. Step 6b reaches only
visible resources (step 5 screened hidden ones), so its precondition failure
is always `Forbidden`, never `NotFound`. Creation (R1/R2) never consults
`floor()`: the §2 classification plus the `RepoID != 0` clause in `repo_of`
force the precondition branch even for misshaped callers.

Visibility helpers (break the §3/§7 circularity by construction):

```
visible_repo(p, r, a) =
    (vis(r) == public  && org_ceiling_ok(r))
 || (vis(r) == internal && authed(p) && !public_only(p) && org_ceiling_ok(r))
 || (vis(r) == private  && (grant_role(p, r) >= read || admin_console(p, a)))
```

`grant_role` excludes site admin, so step 5 cannot pass on admin status
alone; a site admin without a grant reaches a private repo only via
`admin_console(p, a)`. Steps 5→6→7 order is load-bearing: invisibility always
reports `NotFound` even if the role or scope would also fail (prevents
scope/role oracles on private objects). Scope is checked after role so audit
logs distinguish "user lacks grant" from "token lacks scope".

Caching: decisions MUST NOT be cached across grant/visibility/scope/protection
mutations. If a cache is introduced later it MUST key on
(user, token-id, repo, action, resource-version, protection-version) with
explicit invalidation — no such cache in v1.

### 7.3.1 `visible_nonrepo` (normative)

`visible_nonrepo(p, r, a)` is the complete visibility predicate for
`repo_of(r) == false` resources, including creation targets. Exactly one
branch applies per `r.Kind`:

```
visible_nonrepo(p, r, a) =
  (r.Kind == RSite) → true
      // The site object itself is never secret; F1c + scope decide.
      // R1 creation targets (RSite{OwnerID}) always pass step 5 here; the
      // namespace binding happens at step 6b via F1c, not via visibility.
  (r.Kind == ROrg) →
      (org_profile(r) == public)
   || (p.Kind == User && is_org_member(p.UserID, r.OrgID))
   || admin_console(p, a)
      // Anonymous sees public orgs only (O2 anon column); private orgs are
      // NotFound to outsiders. R2 creation into a private org is therefore
      // NotFound for outsiders at step 5, Forbidden/precondition for members
      // without standing at step 6b. Restricted/public-only tokens follow §6.
  (r.Kind == RTeam) →
      visible_nonrepo(p, org_resource(r.OrgID), a)
      // Team visibility inherits parent-org visibility ONLY; membership is a
      // step-6b precondition (M3), not a step-5 visibility gate. Hence a
      // public-org outsider passes step 5 and denies Forbidden/precondition
      // at step 6b, while a private-org outsider fails step 5 (NotFound).
  (r.Kind == RInboxItem) →
      (p.Kind == User && p.UserID == r.AddresseeID)
   || admin_console(p, a)
      // Inbox items are never repo-scoped and never public (B1).
```

`is_org_member / is_org_owner / is_team_member` read the store; anonymous
principals fail all three. `admin_console(p, a)` is F1b. No other
non-repo visibility exists; adding a kind requires extending this function.

---

## 8. Protected bookmarks (and the change-creation rule)

Gitea's `ProtectedBranch{CanPush, whitelists, RequiredApprovals,
StatusCheckContexts, RequireSignedCommits, BlockAdminMergeOverride, ...}`
(`rukie/models/git/protected_branch.go:29-80`) is replaced by one rule type
per repo. Ruk attestations (`review`, `ci`, custom kinds) evaluated by Ruk's
`evaluate_requirements` are the only approval/CI facts; rukie stores the rule,
Ruk evaluates the facts. This section defines `BookmarkRule` once; G0–G11 are
its complete semantics and override any conflicting comment elsewhere.

```go
type BookmarkRule struct {
    RepoID  int64
    Pattern string   // exact ("main") or glob ("release/*"); match order in G0
    Mode    RefMode  // Open | LandOnly
    // PushAllow: who may DIRECT-push when direct push is permitted at all.
    // Direct push is permitted iff Mode == Open, OR (Mode == LandOnly AND
    // BypassAdmin == true AND caller matches PushAllow) — the G4 emergency
    // path. In all other LandOnly cases direct push denies for everyone.
    PushAllow Allowlist
    LandAllow Allowlist // who may LAND (both modes); always required
    Required  []AttestationReq // all must be satisfied at land time; never bypassed
    // BypassAdmin: emergency direct-push valve ONLY. When true, callers
    // matching PushAllow may direct-push despite Mode == LandOnly. It NEVER
    // exempts land from LandAllow or Required, and NEVER lowers a §9 role
    // floor. Default false. Setting true requires admin and is audit-logged.
    BypassAdmin bool
}
type Allowlist struct {
    Roles []RepoRole // e.g. [maintain, admin]; empty = nobody via role
    Users []int64
    Teams []int64    // org-ns repos only; team must belong to the repo's org
}
type AttestationReq struct {
    Kind       string   // e.g. "review", "ci"
    Result     string   // e.g. "approved", "pass"
    MinSigners int      // ≥1; distinct trusted signers on the exact subject OID
    Trusted    Allowlist // whose signatures count (usually Roles:[write,...] or a team)
}
```

Rules:

- **G0 — Match order (deterministic).** Candidate rules for a ref are ordered
  by: (1) exact patterns before globs; (2) longer pattern strings before
  shorter ones; (3) lower rule ID (earlier created) before higher ones. The
  first rule in this order whose pattern matches governs. "First match wins"
  means first in THIS order, not creation order alone. No ref ever evaluates
  two rules.
- **G1 — Change creation is cheap.** Pushing `refs/for/<bookmark>` (or the
  Ruk `new`/`describe` equivalent) requires only `read` on the repo. This is
  the Gerrit/AGit path for outside contributors (Gitea AGit precedent:
  "anyone with read permission can push `refs/for/...`" in
  `rukie/services/actions/notifier_helper.go:696-699`). The created change
  records its author in `Resource.ChangeAuthorID`; authorship alone grants no
  further power (G5, G6).
- **G2 — Unprotected bookmarks.** No matching rule ⇒ direct push needs
  `write`; land of a change targeting it needs `write` (no attestations).
- **G3 — `Open` + rule.** Direct push needs `write` AND `PushAllow` match.
  Land needs `write` AND `LandAllow` match AND all `Required` satisfied.
- **G4 — `LandOnly` + rule.** Direct push denies for everyone, with exactly
  one exception: `BypassAdmin == true` AND the caller matches `PushAllow`
  (emergency path, audit-logged). Land needs `write` AND `LandAllow` AND
  `Required`. `BypassAdmin` never exempts land from anything.
- **G5 — No self-approval.** An `AttestationReq{Kind:"review"}` is satisfied
  only by signers who are not the change author, each with role ≥ `write`
  (or matching `Trusted`), on the exact landed OID. Author signatures never
  count toward `MinSigners`.
- **G6 — Exact-subject attestations.** CI/review facts are evaluated against
  the exact commit OID being landed (Ruk `attested`/`evaluate_requirements`
  semantics). Rebase/new-patch-set invalidates prior attestations; there is no
  "stale approval carries over" except via an explicit new attestation.
- **G7 — Attestation floor.** Recording a `review` attestation requires
  `change.review` (§9 row C5: `write`); recording `ci` results requires
  `System/CI` or `write` via rerun-triggered runs — never `read`/`triage`.
- **G8 — Tag protection folded in.** Tag patterns are `BookmarkRule`s on
  `tags/<pattern>` with the same shape (replaces Gitea's separate
  `ProtectedTag`, `rukie/models/git/protected_tag.go:126-130`). Release
  publish/delete on a matching tag needs rule passage.
- **G9 — No admin bypass by default.** `BypassAdmin == false` (default and
  REQUIRED default for new rules): site admins, org owners, and repo admins
  must match `LandAllow`/`PushAllow` and satisfy `Required` like everyone
  else. Setting `BypassAdmin == true` requires `admin` AND is audit-logged;
  reviewers treat it as a smell. Even with `BypassAdmin == true`, land still
  requires `LandAllow` plus all `Required`.
- **G10 — Rule management.** Creating/editing/deleting `BookmarkRule`s needs
  `maintain` (§9 row R13 `repo.protect`). Rules are versioned;
  `protection_pass` evaluates the version current at land time.
- **G11 — Task-claim lease hold.** Acquiring a task claim (`task.claim`)
  needs `triage` on the repo AND an uncontested Ruk `claim.acquire` on the
  task's claim path. Releasing needs `triage` AND holder identity read from
  `Resource.ClaimHolderID` (only the holder — or `maintain` or above — may
  release another holder's claim; forced releases are audit-logged). Claim
  state is read from Ruk at check time, not cached.

---

## 9. Decision table

### 9.1 Action inventory (every `Op` maps to exactly one)

Families: `site.`, `org.`, `team.`, `repo.`, `change.`, `task.`, `ci.`,
`hook.` (webhooks), `mirror.`, `release.`, `social.` (stars/watches/forks),
`search.` (explore/search), `inbox.`, `token.`.

Minimum-role floor per action (`floor(a,r)`) applies ONLY to existing-repo
non-console resources (`repo_of(r) == true`, i.e. `RepoID != 0`). Rows with
`—` are non-repo actions governed by the F1c precondition in Notes, INCLUDING
creation rows R1/R2 which are classified as non-repo targets per §2; for those
rows `floor()` is undefined by design and §7.3 step 6b takes the precondition
branch. `site.admin_console` never uses `floor()` on any resource kind (step
6a). Plus minimum scope (`required_scope`), plus anonymous allowance
(`public only` = anonymous may call it on public repos; `no` = anonymous
denies at step 4 even on public repos).

| # | Action | Min role / precondition (target) | Min scope | Anon | Notes |
|---|---|---|---|---|---|
| Site / identity | | | | | |
| S1 | `site.admin_console` | — : `IsSiteAdmin` | `admin` | no | Sole grant-independent admin path (F1b, F5c). Delegated sub-checks re-enter `Check`. Skips floor and protection (steps 6a/8). |
| S2 | `site.user_create` | — : `IsSiteAdmin` | `admin` | no | First-user bootstrap is installer, not an Op. Afterwards site-admin only. |
| S3 | `site.user_lock` / `unlock` | — : `IsSiteAdmin` | `admin` | no | Site-admin only. |
| S4 | `self.read` / `self.update` | — : owner (`r.OwnerID == p.UserID`) | `read`/`triage` | no | Update needs `triage` scope. Inactive callers allowed `self.read` only (P5 + step 2 scope/expiry). |
| T1 | `token.mint` / `revoke` / `list` | — : owner (own tokens) | ≤ parent (§6 minting) | no | Admin revoking another user's token needs S1. |
| Orgs & teams | | | | | |
| O1 | `org.create` | — : active authenticated user (no namespace) | `maintain` | no | Server MAY gate behind allow-list; default allow. |
| O2 | `org.view` | — : org public OR member OR console | `read` | public orgs only | Private org: member or site-admin-console, else `NotFound` (§7.3.1). |
| O3 | `org.update` / `org.delete` | — : org `owner` of `r.OrgID` | `admin` | no | Delete rejected if repos remain unless `force` + admin console. |
| O4 | `org.member_add` / `remove` | — : org `owner` of `r.OrgID` | `admin` | no | Last-owner removal rejected (guard). |
| M1 | `team.create` / `update` / `delete` | — : org `owner` of `r.OrgID` | `admin` | no | Includes grant edits and `can_create_repo` flag. Sole path for org-repo grant changes. |
| M2 | `team.member_add` / `remove` | — : org `owner` of `r.OrgID` | `admin` | no | Target must be org member (add implies member-add). |
| M3 | `team.view` | — : org owner sees all; member sees own teams | `read` | no | Step 5 passes on parent-org visibility (§7.3.1); outsiders on public orgs deny `Forbidden/precondition` here, on private orgs they never reach here (`NotFound` at step 5). |
| Repos (creation rows are non-repo targets per §2) | | | | | |
| R1 | `repo.create_user` | — : active user AND `p.UserID == r.OwnerID` (target `RSite{OwnerID}`) | `write` | no | Own user namespace only (F1c). Foreign `OwnerID` denies `Forbidden/precondition`. Never `RRepo`; never `floor()`. Server MAY quota. |
| R2 | `repo.create_org` | — : org `owner` of `r.OrgID` OR `can_create_repo` in `r.OrgID` (target `ROrg{OrgID}`) | `write` | no | Bound to the destination org in `r.OrgID` (F1c). Other orgs' standing is irrelevant. Never `RRepo`; never `floor()`. |
| R3 | `repo.view` (browse/file/history/blame/diff/compare) | `read` | `read` | public only | Existing repo (`RepoID != 0`). Visibility gate first (§3). `internal` needs authenticated; `private` needs grant or console. |
| R4 | `repo.clone` (fetch/archive/pack) | `read` | `read` | public only | Same gate as R3. SSH/HTTP transports call `Check` per repo per session. |
| R5 | `repo.push` (direct ref update) | `write` | `write` | no | Plus `protection_pass` (§8 G2–G4). `read`/`triage` always deny at role floor; anonymous denies at step 4. |
| R6 | `repo.fork` | `read` on source + `dest_create_right` on target (`DestOwnerID`/`DestOrgID`, F1d) | `write` | no | Authenticated-only mutation. V6 monotonicity enforced. Forker gets `admin` on the fork. `Check` verifies source grant, source visibility, source scope, then destination create-right; all must pass. |
| R7 | `repo.settings` | `maintain` | `maintain` | no | Description, default bookmark, options. |
| R8 | `repo.visibility` (narrow only) | `maintain` | `maintain` | no | Narrowing (`public→internal→private`) at `maintain`. |
| R9 | `repo.visibility_widen` | `admin` | `admin` | no | Any widening step needs `admin`. Tightening cascades to forks (§3 V6). |
| R10 | `repo.transfer` | `admin` on source + `dest_create_right` on dest (`DestOwnerID`/`DestOrgID`, F1d) | `admin` | no | `Check` verifies both halves; zero/foreign dest rejected at step 6b (`Forbidden/precondition`, D11/INV-25). |
| R11 | `repo.grant` (collaborator add/edit/remove, user-ns) | `admin` | `admin` | no | User-ns repos only. Org repos: use M1; direct rows rejected (§11 D6). |
| R12 | `repo.delete` | `admin` + namespace-owner precondition | `admin` | no | User-ns owner, or org `owner` of `r.OrgID`. |
| R13 | `repo.protect` (bookmark rule manage) | `maintain` | `maintain` | no | §8 G10. Rule version bumped per write. |
| Changes / review (Ruk changes + branch view) | | | | | |
| C1 | `change.create` (push `refs/for/*`, Ruk `new`) | `read` | `triage` | no | Cheap-ingress guard G1 for role; scope stays `triage`. Authenticated-only. |
| C2 | `change.view` (incl. branch-view grouping) | `read` | `read` | public only | Same visibility as parent repo. |
| C3 | `change.comment` | `read` | `triage` | no | Authenticated-only. Author may always comment on own change. Abandoned/landed changes need `triage` role to comment. |
| C4 | `change.edit` (desc/rebase/split/squash/patch-set) | `write` | `write` | no | Author with below `write` may NOT edit content (may C3 + C6-own-abandon only). |
| C5 | `change.review` (approve / request-changes attestation) | `write` | `write` | no | Plus G5 (no self-approval), G6 (exact OID), G7. |
| C6 | `change.abandon` / `reopen` | own change (`p.UserID == r.ChangeAuthorID`): `read`; others: `write` | `triage` (own abandon) / `write` (others + all reopens) | no | Author may abandon own change with `read` role + `triage` scope. Any reopen needs `write` role + `write` scope. Handler must load `ChangeAuthorID`. |
| C7 | `change.land` | `write` | `write` | no | Plus full `protection_pass`: `LandAllow` + all `Required` (G3/G4/G9). Branch-view "land stack" checks C7 per change, bottom-up. |
| Tasks (issues replacement; claims via Ruk leases) | | | | | |
| K1 | `task.create` | `triage` | `triage` | no | Anonymous task creation is NOT supported (spam surface; reporters use the inbox-to-maintainer flow). |
| K2 | `task.view` | `read` | `read` | public only | Same visibility as repo. |
| K3 | `task.edit` (title/body/labels/assign/close) | `triage` | `triage` | no | Assignee (`r.AssigneeID`) must resolve to ≥ `read` on the repo (assignability floor). |
| K4 | `task.claim` (acquire) | `triage` | `triage` | no | Plus G11 lease-acquire (uncontested path). |
| K5 | `task.unclaim` (release) | holder (`p.UserID == r.ClaimHolderID`): `triage`; forced release of another holder's claim: `maintain` | `triage` (own) / `maintain` (forced) | no | Holder-only release; `maintain` or above may force-release another holder's claim (audit-logged). Handler must load `ClaimHolderID` from Ruk. |
| K6 | `task.delete` | `maintain` | `maintain` | no | Destructive; bulk-close stays K3. |
| CI (built-in; results as Ruk attestations) | | | | | |
| I1 | `ci.view` (runs/jobs/logs/artifacts) | `read` | `read` | public only | Same visibility as repo. Secrets never in scope. |
| I2 | `ci.trigger` / `rerun` | `write` | `write` | no | Fork-PR runs from below-`write` authors execute with restricted secrets (runner policy, not authz). |
| I3 | `ci.cancel` | `write` | `write` | no | Requester, or `write` or above on repo. |
| I4 | `ci.approve` (approve gated run) | `write` | `write` | no | Same population as C5 (counts as human approval, not `ci:pass` fact). |
| I5 | `ci.config` (pipeline + secrets + cache policy) | `maintain` | `maintain` | no | Secret values write-only; reads return redacted descriptors. |
| Webhooks / mirrors / releases | | | | | |
| H1 | `hook.view` | `maintain` | `maintain` | no | Config contains secrets — NOT `read`. Delivery history likewise. |
| H2 | `hook.manage` (create/edit/delete/redeliver) | `maintain` | `maintain` | no | Test-delivery included. |
| N1 | `mirror.view` | `maintain` | `maintain` | no | Same secrecy argument as H1 (credentials in config). |
| N2 | `mirror.manage` (create/edit/delete/trigger-now) | `maintain` | `maintain` | no | Per-mirror mode + ref selection stored here. |
| L1 | `release.view` / download | `read` | `read` | public only | Same visibility as repo. |
| L2 | `release.publish` / edit / delete | `maintain` | `maintain` | no | Tag-matching protection rules additionally apply (G8). Draft-asset upload: `write`; attach to published: `maintain`. |
| Social / discovery | | | | | |
| F1 | `social.star` / `unstar`, `watch` / `unwatch` | `read` | `read` | no | Authenticated-only mutation: requires `repo.view` plus sign-in (public: any signed-in; internal: signed-in; private: grantee). Anonymous denies at step 4 even on public repos. |
| F2 | `social.list` (stars/watchers/forks of a repo) | `read` on that repo | `read` | public only | Entries pointing at repos the caller cannot view are omitted (V5). |
| E1 | `search.explore` / `search.query` / code search | per-hit `read` (no single floor) | `read` | public only | Hit set = `{o : allow(view)}` pre-filtered (V5). Code search executes in Ruk with the caller's repo allow-set pushed down. |
| Inbox (replaces email delivery) | | | | | |
| B1 | `inbox.read` / `ack` / `clear` | — : addressee (`p.UserID == r.AddresseeID`) | `read` | no | Never repo-scoped. Site-admin access only via S1 console. Handler must load `AddresseeID`. |

Min-scope rationale in one line: the scope column is the *lowest* token that
can ever exercise the action; the role column (or precondition) is the lowest
grant. Both must hold (F5a/F5b/F5c). Raising either independently denies.
Fork (R6) is the intentional outlier proving the rule: its role floor starts
at `read` on the source, but its scope floor is `write`, so a `read`-scoped
token with source `read` still denies on scope.

Protected-bookmark overrides (the only row-level floor raisers): table row R5
`repo.push` on a matching rule additionally requires `PushAllow` (G3) or
denies entirely (G4 LandOnly); C7 `change.land` additionally requires
`LandAllow` + all `Required` attestations on the exact OID (G3/G4/G9); L2 on
a tag matching `tags/<pattern>` additionally requires that tag rule's passage
(G8). No rule lowers any floor (D8).

Grant-path summary (no guessing): user-namespace collaborator changes use
table row R11; org-namespace grant changes use table rows M1/M2 (team
management) exclusively — R11 on an org repo is rejected per D6. Org
membership changes use O4; team membership changes use M2. Creation is
namespace-bound non-repo checks: R1 checks `RSite{OwnerID}` with caller-owned
`OwnerID`, R2 checks `ROrg{OrgID}` with standing in that org (F1c); neither
ever consults `floor()`. Fork/transfer destinations require
`dest_create_right` (F1d) — foreign namespaces always deny.

Anonymous determinism (normative tie-break): wherever prose says a `read`
role "includes" fork/star/comment, the §9 `anon` column and §7.3 step 4
control anonymous callers. Anonymous fork (R6), star/watch (F1), and comment
(C3) are `no` and deny `Forbidden/anonymous-mutate` even on public repos,
even though the same actions are `read`-floor for signed-in callers. There is
no anonymous fork/star path.

### 9.2 Worked resolutions (normative examples)

1. Site admin with no grant calls `repo.view` on a private repo: step 5
   evaluates `visible_repo` with `grant_role` (admin excluded) → false, and
   the action is not `site.admin_console` → deny `NotFound/invisible`. The
   same admin through `site.admin_console` passes F1b/step 6a and the
   delegated sub-check is audit-logged.
2. Admin with a `read`-scoped public-only token pushing to their own public
   repo: `grant_role` is `admin` ≥ `write`, but step 7 (scope) fails (`write`
   above `read`) → deny `Forbidden/scope`. Scope never widens (INV-1).
3. Org member, no team, reads org `internal` repo: step 5 passes (authenticated
   + internal) → `grant_role`: membership contributes none (F3), no team
   grant, internal floor `read` → `read` ≥ `read` → allow for `repo.view`;
   `repo.push` (`write` floor) denies at step 6b with `role-floor`.
4. Outside contributor (`read` via `internal` visibility) pushes
   `refs/for/main`: the `change.create` (C1) call carries role `read` and
   `triage` scope → allowed. The same contributor calling `repo.push` (table
   row R5) direct to `main` with role `read` denies at step 6b (`role-floor`);
   a `write`-role caller outside `PushAllow` on a guarded bookmark passes step
   6b and denies at step 8 (`protected:allowlist`).
5. Author with `read` tries `change.review` approving own change: step 6b
   already denies (`write` floor) with `role-floor`; even with `write`, step 8
   denies with `protected:author-approve` (guard G5).
6. `maintain` holder edits webhook on private repo: H2 floor `maintain`,
   scope `maintain` → allow; a `write`-scoped token with the same role denies
   at step 7 with `scope`.
7. Public-org outsider calls `team.view`: step 5 passes (parent org public,
   §7.3.1 imposes no membership gate); step 6b precondition (M3: owner or
   member) fails → deny `Forbidden/precondition`. The same call on a
   private-org team fails step 5 → `NotFound`.
8. Anonymous caller forks a public repo: step 4 denies
   `Forbidden/anonymous-mutate` (R6 anon `no`), even though §3 lists the repo
   as publicly visible. Visibility grants reachability, not mutation rights.
9. Caller with source `read` forks into another user's namespace
   (`DestOwnerID` ≠ caller): source half passes, `dest_create_right` fails
   (F1d user-namespace branch) → deny `Forbidden/precondition` at step 6b
   (INV-25). Same result for a transfer into an org without owner or
   `can_create_repo` standing, or for R1 with a foreign `OwnerID`, or R2 with
   standing only in another org.
10. Caller creates `repo.create_user` with own `OwnerID`: target is
   `RSite{OwnerID == caller}` (§2 classification), `repo_of` false, step 5
   passes (RSite always visible), step 6b checks F1c
   (`p.UserID == r.OwnerID`) → pass to scope. A foreign `OwnerID` fails the
   same check → `Forbidden/precondition` (INV-25); it never reaches `floor()`.

---

## 10. Error mapping (404 vs 403)

`Decision` → transport mapping is part of the contract (prevents private-object
oracles):

| Failure step | `Decision` | HTTP | Git wire / SSH | MCP / CLI |
|---|---|---|---|---|
| Step 5 visibility fail | `NotFound/invisible` | 404 (identical body to random-id 404) | `repository not found` (same as nonexistent ref/repo) | `NOT_FOUND` |
| Steps 2–4, 6b–9 | `Forbidden/<reason>` | 403 with `reason` code + RFC 9457 problem body | `permission denied (<reason>)` on the failed ref/op; other refs unaffected (per-ref status described in the Ruk forge plan) | `PERMISSION_DENIED` + `reason` |
| Unauthenticated where auth required | `Forbidden/anonymous-mutate` | 401 if no credentials, else 403 | `authentication required` | `UNAUTHENTICATED`/`PERMISSION_DENIED` |

Reasons: `inactive`, `token-expired`, `anonymous-mutate`, `invisible`,
`role-floor`, `precondition`, `scope`, `protected:allowlist`,
`protected:attestation`, `protected:author-approve`, `lease-hold`,
`deny:<D-id>`. Reason strings are stable API (agents key automation off them).
Step 6b precondition failures always report `Forbidden/precondition`: hidden
orgs/teams never reach step 6b (they deny `NotFound` at step 5).

Ruk-protocol correspondence (for the daemon boundary only, Phase R): rukie
`NotFound/invisible` surfaces as Ruk `E_NOT_FOUND`; `Forbidden/role-floor`,
`Forbidden/precondition`, `Forbidden/scope`, and `deny:*` surface as
`E_FORBIDDEN`; `protected:*` (including attestation gaps) surface as
`E_POLICY`; malformed actions surface as `E_INVALID`. The Ruk
`kind: anonymous/bootstrap/mutation` taxonomy is a transport framing concern
and does not change these mappings: an anonymous caller denied at step 4 is
`E_FORBIDDEN`, not a framing error.

---

## 11. Deny-by-default rules

These are conjunct `¬denied(p,a,r)` inputs (step 9). All default-deny; each
lists its override, if any.

- **D1 — Unknown action denies.** Any `Action` not in §9.1 denies. No
  wildcard grants. (Catches Op-table drift: adding an Op without a table row
  is closed-by-default until the row lands.)
- **D2 — Anonymous mutation denies.** `Kind == Anonymous` + any action whose
  `anon` cell is not `public only` denies. This includes create (R1/R2), fork
  (R6), star/watch (F1), comments (C3), and task create (K1) even on public
  repos.
- **D3 — Inactive denies.** Covered in step 2; repeated here for audit
  completeness. Sole override: scoped, unexpired `self.read` on one's own
  profile (P5).
- **D4 — Invisible denies before all else.** Step 5 precedes role/scope so
  failures report `NotFound`. No override; site-admin cross-visibility reads
  go through S1-gated console paths that re-establish visibility explicitly.
- **D5 — Team grants are org-confined.** A team grant referencing a repo
  outside its org is unwritable and, if ever read from a corrupt store, is
  ignored (fail-closed; quarantine + audit event).
- **D6 — Collaborator grants are user-ns-only.** Direct user→repo rows on
  org-ns repos are rejected on write and ignored on read (same fail-closed
  handling as D5). Org grant changes use M1/M2, never R11.
- **D7 — Token scope never widens (override: none).** Even `IsSiteAdmin` +
  token ⇒ intersection. Even `BypassAdmin == true` rules do not lift scope
  failure. (Gitea analog: admin-console-behind-full-scope in
  `rukie/models/user/user_extdata.go:114-120`.)
- **D8 — Protection raises, never lowers.** `BookmarkRule`s can only add
  conjuncts (allowlist match, attestations). A rule CANNOT grant push/land to
  a principal below the §9 role floor, `BypassAdmin` cannot exempt land from
  `LandAllow`/`Required`, and `BypassAdmin` defaults false (G9).
- **D9 — Least visibility wins on composites.** Fork networks, stars/watchers,
  search hits, org repo lists, branch-view stacks spanning forks: each element
  is individually gated; the composite never reveals an element the caller
  could not open directly (V5).
- **D10 — System principals are least-privilege.** `Kind == System` may
  exercise ONLY its reason's infra ops (CI worker: record attestations/logs
  for its run; mirror worker: push via ephemeral per-mirror credentials;
  webhook dispatcher: read event payloads; GC: store maintenance). Any
  user-facing `Op` with a System principal denies. System actions are
  audit-logged with the causal user op id where one exists.
- **D11 — Destination is mandatory and bound for copy/move.** R6/R10 with
  zero, both-set, or unauthorized `DestOwnerID`/`DestOrgID` deny
  `Forbidden/precondition` at step 6b via F1d before any grant check. Creation
  rows R1/R2 likewise deny foreign namespaces via F1c.

---

## 12. ASCII logic formulas (normative summary)

```
-- F1a grant role (site admin EXCLUDED; existing repos only; breaks the visibility cycle)
grant_role(p,r) = max(owner_role(p,r),
                      direct_role(p,r), max_t(team_grant(t,r)),

[206 more lines in resource. Use :17488 to continue]
                      direct_role(p,r), max_t(team_grant(t,r)),
                      public_floor(r), internal_floor(p,r))

-- F1b admin console (sole grant-independent admin power)
admin_console(p,a) = is_site_admin(p) /\ (a == "site.admin_console")

-- F1c non-repo preconditions replace floors (floor() undefined there; namespace-bound)
precondition(p,a,r) = per-row predicate in §7.1 F1c / §9 Notes
  -- R1 binds p.UserID == r.OwnerID on RSite{OwnerID}; R2 binds standing in r.OrgID on ROrg{OrgID}.

-- F1d destination create-right (R6/R10 second half; defined once here)
dest_create_right(p, dOwner, dOrg) =
    (dOrg != 0 /\ dOwner == 0) → (is_org_owner(p, dOrg) \/ has_can_create_repo(p, dOrg))
    (dOwner != 0 /\ dOrg == 0) → (is_user(p, dOwner))
    otherwise → false

-- F2/F3 ownership & membership
owns(p,r)    => grant_role(p,r) = admin
member_only(p,org) => contributes none to grant_role(p,r in org)

-- F4 scope gate (token present; also enforced on the P5 self.read path)
scope_covers(p,a,r) = (lvl(scope(a)) <= lvl(max_scopes(p)))
                    /\ ~(public_only(p) /\ vis(r) != public)

-- F5a allow, existing-repo non-console (step 6b repo_of path)
allow_repo(p,a,r) = active(p) /\ visible_repo(p,r,a) /\ (grant_role(p,r) >= floor(a,r))
                  /\ dest_allows(p,a,r) /\ scope_covers(p,a,r)
                  /\ protection_pass(p,a,r) /\ ~denied(p,a,r)

-- F5b allow, non-repo incl. creation targets (step 6b precondition path)
allow_nonrepo(p,a,r) = active(p) /\ visible_nonrepo(p,r,a) /\ precondition(p,a,r)
                     /\ scope_covers(p,a,r) /\ ~denied(p,a,r)

-- F5c allow, console (step 6a path; any resource kind)
allow_console(p,a,r) = active(p) /\ admin_console(p,a)
                     /\ scope_covers(p,a,r) /\ ~denied(p,a,r)

-- F6 visibility (grant_role only; admin via console branch)
visible_repo(p,r,a) = (vis(r)=public  /\ org_ceiling_ok(r))
             \/ (vis(r)=internal /\ authed(p) /\ ~public_only(p) /\ org_ceiling_ok(r))
             \/ (vis(r)=private  /\ (grant_role(p,r) >= read \/ admin_console(p,a)))
visible_nonrepo(p,r,a) = per-kind predicate in §7.3.1

-- F7 land = floor + rule + facts (G3/G4/G5/G6, G0 selects the rule)
rule(r,ref) = first_match(order(G0), rules(r), ref)
land_ok(p,change) = (grant_role >= write) /\ land_allow_match(p,rule)
                  /\ all(req in rule.required: attested_exact(change.oid,req)
                         /\ signers(req) cap trusted /\ author not in signers /\ n >= min)

-- F8 copy/move destination (R6/R10 second half; calls F1d)
dest_allows(p,a,r) = (a not in {repo.fork, repo.transfer})
                   \/ dest_create_right(p, r.DestOwnerID, r.DestOrgID)
```

`floor`, `scope(a)`, `vis`, `attested_exact` are exactly the §9 table, §6
lattice, §3 enum, and Ruk `evaluate_requirements` over the landed OID.
`floor()` is defined only for existing-repo non-console actions; creation and
other non-repo actions use `precondition()` (F1c) and console actions use F5c.

---

## 13. Property-test invariants (acceptance: all listed, all tested)

Each INV is a executable property over (store fixture × principal × action).
`// test:` sketches the generator; the suite lives next to `authz.Check`
(port: Go property tests, e.g. rapid/gopter-style; exact framework is an
implementation choice, the properties are not).

- **INV-1 Scope never widens, including the inactive path.** For all (user,
  token-scopes S, action, repo): `Check(token-user) = allow ⇒
  Check(session-user) = allow` with same grants; the P5 `self.read` exception
  still requires unexpired token and `scope_covers`. `// test: random S ⊆
  scopes incl. empty, random action incl. self.read on inactive users.`
- **INV-2 Admin+token still clamped.** `IsSiteAdmin` with token scopes S may
  allow only actions with `scope(a) ≤ max(S)`. `// test: admin × all S × all a.`
- **INV-3 Private invisible to non-grantees, admins included.** For private
  repo R and principal with `grant_role < read` invoking an ordinary action:
  `Check = NotFound`, even when `IsSiteAdmin`, unless the action is
  `site.admin_console`. R ∉ ordinary explore/search/forks/stars listings.
  `// test: matrix over anon/signed/outsider/admin-without-grant.`
- **INV-4 Internal invisible to anonymous and public-only tokens.**
  `Check(view)=NotFound`; absent from listings. `// test: anon + public-only
  token × internal repos.`
- **INV-5 Listing ⊆ viewable.** For every listing op, every returned element
  satisfies `Check(view)=allow` for the caller. `// test: random caller ×
  explore/search/fork-network/star-list; re-check each hit.`
- **INV-6 Fork monotonicity.** `vis(fork) ≤ vis(source)` at fork time and
  after every source narrowing; widening fork beyond source rejected.
  `// test: random vis transitions on source; assert fork follows.`
- **INV-7 Membership alone is nothing.** Org member with no team grant has
  `grant_role = none` (+visibility floors only): can `repo.view`
  internal/public, cannot `push/review/land/trigger`. `// test:
  member-minus-teams × all a.`
- **INV-8 Owner team is total.** Org owner resolves `admin` on every org repo
  including repos created after the grant. `// test: create repo after owner
  assignment; assert admin.`
- **INV-9 Team confinement.** No team grant ever authorizes a repo outside its
  org (D5); corrupt cross-org rows are ignored. `// test: inject cross-org
  row; assert deny.`
- **INV-10 Collaborator confinement.** Direct grants authorize only user-ns
  repos (D6). `// test: inject direct row on org repo; assert deny.`
- **INV-11 Land requires facts.** `change.land` on a rule-guarded bookmark
  allows only with all `Required` satisfied on the exact OID; stale (pre-
  rebase) attestations do not satisfy. `// test: mutate OID after approval;
  assert deny.`
- **INV-12 No self-approval.** Author's `review:approved` never counts toward
  `MinSigners` (guard G5). `// test: single approval by author; assert gap
  remains.`
- **INV-13 Direct push to LandOnly denies.** Including site/repo admins with
  `BypassAdmin=false`; with `BypassAdmin=true` only `PushAllow`-matched
  callers pass (still role-floored, guard G4). `// test: all roles × LandOnly
  push.`
- **INV-14 Claim hold.** `task.unclaim` by non-holder with below `maintain`
  denies; `task.claim` on contested path denies without lease win (guard G11).
  `// test: two holders race; exactly one acquires.`
- **INV-15 Child scope ≤ parent.** Minted token scopes ⊆ minter scopes;
  `PublicOnly` monotonic. `// test: random mint chains; assert containment.`
- **INV-16 404-before-403.** Any invisible resource reports `NotFound`
  regardless of role/scope inputs (no oracle via reason strings or timing
  beyond unavoidable store latency — timing out of scope for v1).
  `// test: private repo × wrong-role vs wrong-scope callers; both NotFound.`
- **INV-17 Star/watch/fork imply view, fork needs write scope, all need
  sign-in.** `social.star/watch` allows only if `repo.view` allows (read
  scope, authenticated); `repo.fork` allows only if source `repo.view` allows
  AND scope is `write` or above AND caller is authenticated. Anonymous fork/
  star/watch always deny at step 4. `// test: anon × public fork/star (deny);
  read-scoped fork (deny on scope); write-scoped fork (pass).`
- **INV-18 Config secrecy.** Webhook/mirror/CI-config reads require
  `maintain`; `read`-scoped callers deny even with `admin` grant role (scope
  gate). `// test: admin role × read token × H1/N1/I5.`
- **INV-19 Last-owner guard.** Removing the final org owner, deleting the
  Owners team, or deactivating the last site admin is rejected.
- **INV-20 Delete needs admin+namespace.** `repo.delete` allows only with
  grant role `admin` AND (user-ns owner ∨ org owner); `write`/`maintain`
  always deny.
- **INV-21 Non-repo rows decide, including creation.** Every `—` row in §9.1
  (including R1/R2 as classified non-repo targets) returns exactly one
  decision via F1c/F1d/F5b/F5c + scope: org/team/site/inbox/creation actions
  never fall through to a role floor and never fail open. `// test: all
  non-repo actions × anon/outsider/member/owner/admin matrix, incl. R1 with
  own vs foreign OwnerID and R2 with standing vs foreign OrgID.`
- **INV-22 Rule order deterministic.** Overlapping `BookmarkRule` patterns
  always select the G0 winner independent of insertion order.
  `// test: permute rule creation order; assert same governing rule.`
- **INV-23 Console bypasses floor, nothing else.** `site.admin_console`
  skips `floor()`/`grant_role()` on every resource kind (step 6a); no
  ordinary action skips them. `// test: console × repo/org/team/inbox with
  grantless admin allows visibility gate; ordinary actions deny NotFound.`
- **INV-24 Team visibility splits 404/403.** Private-org outsiders get
  `NotFound` at step 5 for `team.view`; public-org outsiders pass step 5 and
  get `Forbidden/precondition` at step 6b. `// test: team.view ×
  public/private org × outsider.`
- **INV-25 Destination verified and bound.** R6/R10 with zero, both-set, or
  unauthorized destination deny `Forbidden/precondition` even when the source
  half passes; R1 denies foreign `OwnerID`; R2 denies standing in another
  org; foreign fork/transfer/creation destinations always deny. `// test:
  valid source grant × missing/foreign/both-set dest; cross-user R1;
  cross-org R2.`

---

## 14. Implementation notes (non-normative but binding-adjacent)

- Single enforcement point: every `Op` handler calls `authz.Check` first;
  transport layers (HTTP/SSH/MCP/CLI) map `Decision` per §10 and MUST NOT
  pre-filter with their own grant logic (pre-filtering causes 403-vs-404
  divergence — the exact bug class Gitea's split
  `AccessibleRepositoryCondition` vs `GetIndividualUserRepoPermission` paths
  invite). Creation handlers classify targets per §2 before calling `Check`;
  fork/transfer handlers populate `DestOwnerID`/`DestOrgID` before calling it.
- Audit every deny at `maintain`-and-above floors and every allow on
  destructive ops (`repo.delete/transfer/visibility_widen`, `team.*`,
  `org.*`, `hook.*`, `mirror.*`, `ci.config`, force-release of claims,
  `BypassAdmin=true` rule writes) with (principal, token-id, action,
  resource, decision, reason, rule-version, attestation OIDs). Log the
  classified target kind for R1/R2 and both source and destination for R6/R10.
- Ruk callout shape: `protection_pass` needs `evaluate_requirements(repo,
  oid, required, trusted_signers)` from the Ruk daemon (Phase R6
  attestation/claim/land extensions) and `claim.acquire/list` for tasks.
  Treat Ruk answers as facts; treat rukie rules as policy. Never cache Ruk
  claim/attestation answers across ops.
- Migration from Gitea data (one-time importer, not runtime): collapse each
  user's per-unit modes to `max` → nearest role at-or-below per the fixed
  table below; drop unit-level distinctions; map `is_private=false→public`,
  `true→private` (no auto-`internal`; admins may reclassify post-import);
  owner-team → Owners team; collaborator rows on org repos → team grants on
  an `imported` team (human review). Importer MUST emit a per-repo grant diff
  for sign-off.

| Gitea `AccessMode` (+ units) | Rukie role | Note |
|---|---|---|
| `none` | `none` | |
| `read` (any unit max `read`) | `read` | Unit distinctions dropped; max wins |
| `write` | `write` | |
| `admin` | `admin` | Gitea `admin` already means full repo control; maps straight across |
| `owner` (repo owner / owner-team / site admin) | `admin` (+ namespace ownership) | No separate `owner` repo role in rukie; ownership lives in the namespace |
| — (no source) | `triage`, `maintain` | New roles with no Gitea equivalent; start unassigned, admins delegate explicitly post-import |

---

### Critical Files for Implementation

- `rukie/models/perm/access/repo_permission.go` — Grant-resolution order being replaced (token gate → visibility → admin/owner fast path → team-unit max); normative model for F1 ordering.
- `rukie/models/perm/access/access.go` — `modeByOwnerAndAccess` public-read floor and owner fast path; source of the visibility-vs-grant layering §7 preserves.
- `rukie/models/repo/repo_list.go` — `AccessibleRepositoryCondition` / `DoerTokenAllowsRepo` / `PublicRepoUnderPublicOwnerCond`; listing-as-filtered-read precedent for §3 V5 and INV-5.
- `rukie/models/user/user_extdata.go` — Token scope carriage on the doer and full-scope admin gate; precedent for §6 minting rule and INV-2.
- `rukie/models/git/protected_branch.go` — Branch-protection allowlists/approvals/status-checks being folded into the single `BookmarkRule` + Ruk attestation evaluation of §8.