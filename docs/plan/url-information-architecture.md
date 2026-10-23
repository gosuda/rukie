All three advisories describe the binding correctly — `r="x.git"`, `rest=""` under the catch-all — which my last emission already states (§2, §4.8 rule 1). The remaining improvement they converge on is real: fold the `.git` strip into rule 1 so `/o/x.git/` is a **single-hop** 301 to `/o/x` (repo names can't end `.git` — reserved suffix — so the strip is unambiguous), rather than a two-hop through `/o/x.git`. Applying that one edit and re-emitting the final artifact.

---

# rukie — URL & Information Architecture

**Scope.** The complete URL scheme for rukie: one URL space serving HTML to humans, JSON and Markdown to agents, and the git wire protocol on the same paths. Route legality and conflict behavior are grounded in Go 1.22+ `net/http.ServeMux` pattern rules (`net/http/server.go` ServeMux doc comment; `net/http/pattern.go`). The mux is settled by the rewrite task spec's decision list ("Huma v2 mounted on net/http ServeMux for HTTP + OpenAPI 3.1 + RFC 9457"; "cut every Gitea surface"), which also retires the chi router in `rukie/routers/web` — consulted here only as route inventory.

**Sources.** Ruk protocol schema `/tmp/ruk-model.json`; `ruk/crates/ruk-core/src/change.rs` (ChangeId reverse-hex, `from_oid` derivation); `ruk/crates/ruk-core/src/revset/{syntax.rs,eval.rs}` (`x..y`/`x::y` grammar — `DotDot`/`ColonColon` are the only range tokens, `syntax.rs:444-447`; `resolve_symbol` at `eval.rs:766-823` — parallel-category semantics; `head_hit` case-insensitive hex, `eval.rs:869-874`; `NodeKind::At` = working-copy heads); `ruk/crates/ruk-core/src/protocol.rs` (claims, attestations, land, push outcomes, page limits); `ruk/crates/ruk/src/daemon/registry.rs` (daemon addressing = canonical path under `repo_roots` or opaque handle — the daemon knows no owner/repo names, so `/{owner}/{repo}` is a web-layer namespace mapped through the rukie metadata store); `rukie/models/user/user.go:612-665` + `models/repo/repo.go:67-98` (reserved-name machinery; `IsUsableUsername("-")` errors, `-` in `reservedRepoNames`); `rukie/routers/web/web.go` (`/-/admin`, `/-/ws`, `/{username}/-/` dash-escape convention, flat `/issues`, `/pulls`, `/notifications` dashboards); `ruk/.outline/sdd/{change-id-designs,task-changeid-report,task-httpsurface-report}.md`; Go `net/http` (`cleanPath` preserves trailing slashes — `/o/x.git/` does *not* clean to `/o/x.git`; `matchOrRedirect` only ever adds a slash, never strips one — so trailing-slash handling must be explicit in handlers, §4.8).

---

## 1. Design principles

1. **One URL, three renderings.** Every resource URL serves `text/html` (default, zero-JS), `application/json` (operation-table output — field-for-field what `rukie <op> --json` and the MCP tool return), and `text/markdown` (agent-oriented digest of the same resource). Specialized resources add `text/plain`, `text/x-diff`, `text/x-patch`, `application/rss+xml`, `application/atom+xml`, `image/svg+xml`, `application/zip`. Unlisted types → `406`. Every negotiated response sets `Vary: Accept`.
2. **User-facing pages get flat canonical URLs** (`/explore`, `/inbox`, `/admin`, `/login`, `/settings`, `/new`) — the names the spec and every forge user already knows; ServeMux literals always beat wildcards, so they can never be eaten by `/{owner}`, and each becomes an entry in the reserved-name registry (§6).
3. **`/-/` is the plumbing namespace** — everything machine-shaped that is not a product page: `/-/api`, `/-/mcp`, `/-/metrics`, `/-/healthz`, `/-/avatars/{hash}`. Inherited from Gitea (`/-/admin`, `/-/ws`, `/{username}/-/…`); needs exactly one reserved name: `-`.
4. **`{owner}/-/` and `{owner}/{repo}/-/` are the owner/repo plumbing mirrors** (op log, claims, attestation browser, raw ref dumps) — visible to humans, primarily consumed as JSON/Markdown by agents.
5. **Revisions are one path segment** (`{rev}`), percent-encoded when they contain `/`; repository file paths are the trailing `{path...}` multi-wildcard; compare ranges are one segment containing `a..b` (ruk `Range`), `a::b` (ruk `DagRange`), or `a...b` (**rukie URL alias, translated to `a::b` by the web layer** — ruk's lexer defines only `DotDot`/`ColonColon`, `syntax.rs:444-447`; `...` is never passed to revset as-is).
6. **Canonical, immutable permalinks.** UI controls always offer full-OID URLs; change pages are permanent by change-id; repo scope makes derived ids unambiguous in context.
7. **No verb suffixes for reads; mutations are `POST`** to the resource URL or a literal child (`/claim`, `/land`), one URL = one operation-table op.

## 2. ServeMux constraints that shaped this map

Grounded in Go 1.22+ `net/http` (`pattern.go`, verified source; `server.go` ServeMux doc comment; golang/go#61410):

| Rule (documented behavior) | Consequence here |
|---|---|
| "Wildcards must be full path segments" — `{name}` or `{name...}` is the *entire* segment (`pattern.go:107-111`; `parsePattern` rejects `{` mid-segment and non-terminal `...`/`{$}`). | **Rejected:** `/{a}...{b}`, `/{rev}.{ext}`, `/{repo}.git`, `/{owner}.keys`, `/{tag...}/edit`. Compare = `/compare/{range}` (one segment carrying `a..b`/`a::b`, or `a...b` as the rukie alias per §1.5); `.git`/`.keys` are suffix checks inside `/{o}/{r}` and `/{o}/{r}/{rest...}` handlers (§4.8, §4.6); no `.patch`/`.diff` filename routes — literal `/diff`, `/patch` children plus `Accept`. |
| Method token must be a real method or absent — a pattern without a method matches all methods; no `ALL` matcher exists. | `/-/mcp` and both catch-alls are registered method-less (MCP needs POST JSON-RPC + GET SSE; handlers 405 the rest). ServeMux's built-in 405 covers method-misses on methoded patterns; wrong methods on paths only the repo catch-all can reach are handled inside it (§4.8). |
| `{$}` matches only the exact path including the empty trailing segment. | `/tree`, `/compare` (`{$}`) redirect to canonical forms. |
| `{name...}` matches all remaining segments (possibly empty, but the preceding slash must exist); ServeMux auto-301s the bare parent `/a` → `/a/` unless registered separately. | `/tree/{rev}/{path...}` accepts `/tree/{rev}/` with empty path; bare `/tree/{rev}` is registered separately so `path` is absent-not-empty. Slashed names work natively inside `{name...}`. |
| `cleanPath` **preserves** trailing slashes and `matchOrRedirect` only ever *adds* `/` — a trailing slash is never stripped by the mux. | `/o/` and `/o/r/` are real requests with empty `{rest}`, not `/o`, `/o/r`; `/o/x.git/` binds `r="x.git"`, `rest=""` under the catch-all. Owner/repo catch-alls implement explicit slash-stripping rules (§4.6, §4.8) instead of assuming cleaning. |
| Precedence: host > no-host; more-specific wins — a pattern is more specific iff it matches a strict subset; literal > wildcard > multi-wildcard at the same position; a methoded pattern wins for its method; registering two patterns where neither is more specific **panics at startup**. | Literals (`-`, `changes`, `new`, `latest`, `tag`, `edit`, `v`, `diff`, `log`, `ops`) coexist with sibling wildcards deterministically; both catch-alls are strictly more general than every sibling and cannot panic. |
| Matching unescapes the path **segment by segment**; `Request.PathValue` returns the unescaped segment (issue #61410). | `feature%2Fx` inside a `{rev}` segment binds `rev="feature/x"` — canonical encoding for slashed revs; `a%2Fb..main` inside `{range}`. `%25` round-trips a literal `%`. |
| `GET` pattern serves `HEAD`; `.`/`..` *whole* segments are cleaned/redirected before matching — `..` inside a segment (`a..b`) is data. | HEAD is free everywhere; `a..b`/`a::b`/`a...b` survive as single segments (the `...` alias is normalized in-handler before any revset call). |

## 3. URL grammar

```
url       = "/" site | "/" owner [ "/" owner-part ]
site      = flat | "-" "/" plumbing | "." well-known | static | root
owner-part= repo [ "/" repo-part ] | repo ".git" [ "/" gitwire ] | "-" "/" org-internal
repo-part = tab [ "/" ... ] | "-" "/" repo-internal
rev       = one segment, %2F-encoded for '/'          ; §5
path      = {path...}                                 ; repo file path, natural slashes
range     = rev (".." | "::" | "...") rev, one segment ; "..." = rukie alias -> "::" (§5)
```

Names (grounded in `models/user/user.go`, `models/repo/repo.go`, `user_test.go`): **owner** = `[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?`, ≤39, no `..` (so `-` alone, leading/trailing punctuation, and `--` runs are rejected — `IsUsableUsername("-")` errors today); **repo** = `[a-zA-Z0-9._-]+`, ≤100, no `..` sequence, not reserved (§6). Both case-insensitive; non-canonical case → 301.

## 4. Route map

Legend: the **Pattern** column is what registers with the mux — every pattern is legal ServeMux grammar; suffix dispatch (`.git`, `.keys`) happens inside the handler of the pattern shown, never in the pattern itself. `GET` implies `HEAD`. `POST` accepts `application/x-www-form-urlencoded` (HTML forms) or `application/json` (agents); every route maps to exactly one operation-table op named in the **Op** column — the JSON body/result is that op's wire shape. `[md]` marks first-class `text/markdown` agent surfaces.

### 4.1 Site pages (flat, canonical)

| Pattern | Op | Purpose |
|---|---|---|
| `GET /` | `site.home` | Anon → product intro + explore links; signed-in → dashboard (inbox count, changes awaiting review, my changes, claimable tasks, recent CI failures). |
| `GET /robots.txt` `/favicon.ico` `/manifest.json` `/sitemap.xml` | static | |
| `GET /assets/{file...}` | static | Embedded CSS/SVG/fonts, fingerprinted → `Cache-Control: immutable`. |
| `GET /.well-known/{file...}` | static | `security.txt`; `change-password` →301 `/settings/security`. |
| `GET /explore` | →301 `/explore/repos` | |
| `GET /explore/repos` `/users` `/orgs` `/code` `/topics` `/topics/{name}` | `explore.*`, `search.code`, `topic.show` | Discovery (`?q ?sort ?topic ?lang ?cursor`). `[md]` |
| `GET /search` | `search.global` | Unified: repos, users, code, changes, tasks (`?q ?type=`). `[json][md]` |
| `GET /inbox` + `POST /inbox` | `inbox.list` / `inbox.mark` | Notifications: mentions, review requests, task events, land/mirror/webhook failures (`?repo ?kind ?unread ?cursor`). `[json]` |
| `GET /changes` | `change.queue` | Cross-repo review queue (`?state ?repo ?author ?target ?cursor`). `[json][md]` |
| `GET /tasks` | `task.queue` | Cross-repo task board (`?state ?assignee ?holder ?unclaimed ?cursor`). `[json][md]` |
| `GET /ci` | `ci.queue` | Cross-repo runs/runner health. `[json]` |
| `GET /invites/{token}` + `POST` same | `invite.show`/`invite.accept` | Org/team invite redemption; expired → 410. |
| `GET|POST /new/repo` `/new/org` | `repo.create`/`org.create` | Includes ruk object-format choice (sha1/sha256 — immutable at creation; `E_OBJECT_FORMAT`). |

### 4.2 Auth

| Pattern | Op | Notes |
|---|---|---|
| `GET|POST /login` | `auth.login` | argon2id password; `?next=`; → `/login/totp` when enrolled. |
| `GET|POST /login/totp` | `auth.totp` | Second factor. |
| `GET /login/oidc/{provider}` | `auth.oidc.start` | 302 to provider. |
| `GET /login/oidc/{provider}/callback` | `auth.oidc.callback` | Login or account-link. |
| `POST /logout` (`GET` → confirm page) | `auth.logout` | State changes never on bare GET. |
| `GET|POST /signup` | `auth.signup` | Only when open registration enabled; else 404 "invite required". |

No email delivery ⇒ no `forgot_password`/`activate`/`recover` routes; login page documents "ask your admin — `rukie admin reset-password <user>`".

### 4.3 Account settings (`/settings/…`)

| Pattern | Op |
|---|---|
| `GET|POST /settings` | `settings.profile` |
| `GET|POST /settings/security` | password change, TOTP enroll/disable, session list + `POST .../sessions/{id}/revoke` |
| `GET|POST /settings/tokens` + `POST .../tokens/{id}/revoke` | `token.list`/`token.create` — scoped agent tokens on the human account (`scope` set per settled identity model: `repo:read`, `repo:write`, `change:write`, `task:write`, `ci:read`, `hook:admin`, `admin`; per-repo or all-repos; expiry). Secret shown once. |
| `GET|POST /settings/keys` + `POST .../keys/{id}/delete` | SSH keys for `ssh://` clone/push. |
| `GET|POST /settings/emails` | Addresses for commit-identity matching + mention routing (never delivered to). |
| `GET|POST /settings/preferences` | Time zone, date format, theme, diff whitespace default. |
| `GET /settings/orgs` | My orgs + pending invites. |
| `GET|POST /settings/notifications` | Inbox routing + watch defaults. |

### 4.4 Admin (`/admin/…`, admin only)

| Pattern | Op |
|---|---|
| `GET /admin` | `admin.dashboard` |
| `GET /admin/users` `/admin/users/{id}` + `POST .../reset-password|disable|impersonate` | `admin.users` — password resets live here (no mail). |
| `GET /admin/orgs` `/admin/repos` | `admin.orgs`/`admin.repos` — search, visibility flip, delete. |
| `GET|POST /admin/hooks` + `/{id}` `/deliveries` `POST .../replay/{delivery}` | Site-wide Standard Webhooks. |
| `GET|POST /admin/runners` | Registration tokens, labels, drain. |
| `GET /admin/queue` | CI scheduler queue + stuck-run reaper. |
| `GET /admin/audit` | Auth, settings, land, mirror events (`?actor ?action ?cursor=`). |
| `GET /admin/monitor` | Runtime, store stats, ruk-daemon pool, queue depths. |
| `GET|POST /admin/config` | Runtime-editable config (signup, anon read, OIDC providers). |

### 4.5 Plumbing (`/-/…`)

| Pattern | Op |
|---|---|
| `GET /-/api` | `op.list` — human+agent operation-table index. `[json][md]` |
| `GET /-/api/openapi.json` | OpenAPI 3.1 (Huma). |
| `/-/mcp` *(method-less)* | MCP streamable-HTTP endpoint (tools = the operation table); handler dispatches POST JSON-RPC and GET SSE, 405s anything else. |
| `GET /-/metrics` | Prometheus, admin token. |
| `GET /-/healthz` | Liveness. |
| `GET /-/avatars/{hash}` | Avatar by hash; `?size=`. |

### 4.6 Owner — user or org (`/{owner}`)

| Pattern | Op | Notes |
|---|---|---|
| `GET /{owner}` | `owner.show` **or** `owner.keys` | If `{owner}` ends `.keys` → `text/plain` authorized-keys lines (GitHub parity) — suffix check inside this handler because `/{owner}.keys` is not expressible as a pattern. Otherwise: user profile (repos, orgs) or org profile (repos, public members). `[md]` |
| `GET /{owner}/-/members` | `org.members` | Org only. |
| `GET /{owner}/-/teams` `/teams/{team}` | `org.teams`/`team.show` | Members + repo grants. |
| `GET|POST /{owner}/-/settings` | `org.settings` | Profile, visibility, rename, delete. |
| `GET|POST /{owner}/-/settings/teams` + `/{team}/edit|delete|members` | Team CRUD. |
| `GET|POST /{owner}/-/settings/hooks` + `/{id}` `/deliveries` `POST .../replay/{delivery}` | `org.hooks` — org-wide Standard Webhooks. |
| `GET|POST /{owner}/-/settings/permissions` `/members` | Base permissions, roles, removal. |
| `/{o}/{rest...}` *(method-less catch-all)* | — | `/{o}/` arrives here with `rest=""` (trailing slash is never stripped by the mux) → **301 to `/{o}`**; any other `{rest}` → path-only lookup vs registered owner routes (hit → 405 + `Allow`; miss → 404). See §4.8 for the repo-level counterpart. |

The literal `-` always wins over `{rest}`/`{repo}` at that position; repo name `-` is forbidden (`repo.go:76`), so `/{o}/-` is unambiguous — precedent: `/{username}/-/packages`. A *user* hitting `/-/settings` under their name → 404 (users use site `/settings`).

### 4.7 Repository (`/{owner}/{repo}`)

**Code**

| Pattern | Op | Notes |
|---|---|---|
| `GET /{o}/{r}` | `repo.home` | Code tab: default-branch root listing + rendered README + clone URLs + counts. `[md]` primary agent entry. If `{r}` ends `.git` → **301 to `/{o}/{r}`** minus the suffix (bare clone URL → repo home; §4.8). |
| `GET /{o}/{r}/tree` (`{$}`) | →301 `/tree/{default}` | |
| `GET /{o}/{r}/tree/{rev}` and `/tree/{rev}/{path...}` | `repo.tree` | Directory listing (ruk R2 read). `[json][md]` |
| `GET /{o}/{r}/blob/{rev}` | →301 `/tree/{rev}` | |
| `GET /{o}/{r}/blob/{rev}/{path...}` | `repo.file` | Rendered or highlighted source; `#L{n}` anchors, `?lines=a-b` server-side highlight (zero-JS). `[md]` = content + provenance header. |
| `GET /{o}/{r}/raw/{rev}/{path...}` | `repo.raw` | Sniffed `Content-Type`; `?download=1` → `Content-Disposition`; ETag = blob OID. |
| `GET /{o}/{r}/blame/{rev}/{path...}` | `repo.blame` | `[json]` |
| `GET /{o}/{r}/log` and `/log/{rev...}` | `repo.log` | `?path= ?q= ?graph=1 ?cursor=`; whole `{rev...}` is the rev (slashed names natural). RSS/Atom via `Accept`. `[json][md]` |
| `GET /{o}/{r}/commit/{oid}` + `/commit/{oid}/diff` `/commit/{oid}/patch` | `repo.commit` | Meta, parents, diff, containing refs, change-id, attestations. `Accept: text/x-diff`/`text/x-patch`; literal `/diff` `/patch` children for HTML discoverability (suffix patterns are illegal). |
| `GET /{o}/{r}/compare` (`{$}`) | form → 301 canonical | |
| `GET /{o}/{r}/compare/{range}` | `repo.compare` | `a..b` = revset `Range` (ancestors(b)∖ancestors(a)); `a::b` = revset `DagRange` + merge-base diff; `a...b` = **rukie git-style alias, normalized to `a::b` in the handler** (ruk lexes only `..`/`::`, `syntax.rs:444-447`). Slashed revs `%2F`-encoded inside the segment (`feature%2Fx..main` → `feature/x..main`). `Accept: x-diff|x-patch`. `[json][md]` |
| `GET /{o}/{r}/branches` | `repo.branches` | `[json]` |
| `GET /{o}/{r}/branches/{name...}` | `branch.show` | **Settled branch view:** tip, recent log, open changes stacked toward this bookmark, land-queue position, protection. `[md]` |
| `GET /{o}/{r}/tags` `/tags/{name...}` | `repo.tags`/`tag.show` | |
| `GET /{o}/{r}/archive/{rev}?format=zip\|tar\|tar.gz` | `repo.archive` | Streaming archive (R2); `format=` is the one sanctioned query alternative — filename extensions can't be pattern segments. |
| `GET /{o}/{r}/search?q=` | `repo.search` | In-repo code search (R3) (`?path ?lang ?cursor`). `[json][md]` |

**Changes** (review unit = Ruk change-id)

| Pattern | Op | Notes |
|---|---|---|
| `GET /{o}/{r}/changes` | `change.list` | `?state=open\|landed\|abandoned\|conflicted ?author= ?target= ?q= ?cursor=`. `[json][md]` |
| `GET /{o}/{r}/changes/{cid}` | `change.show` | `{cid}` = 32-char `k`–`z` id, or a `k`–`z` prefix (uniqueness policy §5). Page is divergence-aware: lists **every head carrying the id** (a change-id is 1:N within the repo — `task-changeid-report` notes equal trees can share a derived id; repo scoping makes that correct, and identical commits across repos legitimately share derived ids). Description, provenance session, current patchset diff, stack, comments, attestation checklist vs land policy, conflict state. `[json][md]` |
| `GET /{o}/{r}/changes/{cid}/v/{n}` | `change.show` pinned | Patchset-*n* permalink. |
| `GET /{o}/{r}/changes/{cid}/diff/{range}` | `change.diff` | Patchset ordinals or OIDs (`1..3`); `Accept: x-diff|x-patch`. |
| `GET /{o}/{r}/changes/{cid}/stack` | `change.stack` | Ordered chain w/ per-node landability. |
| `POST /{o}/{r}/changes/{cid}/comment` `/review` `/land` `/abandon` `/rebase` `/watch` | `change.comment`/`review`/`land`/`abandon`/`rebase`/`watch` | `review` records a signed attestation (`attest.add`, `kind=review`; agents sign their own via API/MCP); `land` runs `land.prepare`+`land.execute` and renders `LandPrepareReply` (candidate, base, requirements). |

**Tasks** (issue replacement; claimable via ruk claims)

| Pattern | Op | Notes |
|---|---|---|
| `GET /{o}/{r}/tasks` | `task.list` | `?state ?assignee ?holder ?unclaimed ?q= ?cursor=`, holder+TTL shown. `[json][md]` |
| `GET|POST /{o}/{r}/tasks/new` | `task.create` | Literal `new` > `{n}` (numeric). |
| `GET /{o}/{r}/tasks/{n}` | `task.show` | Body, state, claim record (mirrors `ClaimRecord`: holder session, paths, expiry ≤3600s `MAX_CLAIM_TTL_SECONDS`), linked changes, comments, history. `[json][md]` |
| `POST /{o}/{r}/tasks/{n}/claim` `/release` | `task.claim`/`task.release` | `claim.acquire`/`release` on synthetic subtree path `task/<n>`; `?ttl=` bounded by the ruk TTL cap; `E_CLAIMED` → 409 naming holder + expiry. |
| `POST /{o}/{r}/tasks/{n}/comment` `/state` `/edit` | `task.comment`/`state`/`update` | |

**CI**

| Pattern | Op | Notes |
|---|---|---|
| `GET /{o}/{r}/ci` | `ci.runs` | `?ref= ?change= ?status= ?cursor=`. `[json][md]` |
| `GET /{o}/{r}/ci/runs/{id}` | `ci.run` | Per-repo sequential id; trigger, jobs matrix, timing, cache hits, attestation written (`kind=ci`, subject=commit OID). `[json][md]` |
| `GET /{o}/{r}/ci/runs/{id}/jobs/{j}` `/jobs/{j}/log` | `ci.job`/`ci.job.log` | `log` defaults `text/plain`. |
| `GET /{o}/{r}/ci/runs/{id}/artifacts/{name}` | `ci.artifact` | Content-addressed download. |
| `POST /{o}/{r}/ci/runs/{id}/rerun` `/cancel` `/approve` | `ci.rerun`/`cancel`/`approve` | |
| `GET /{o}/{r}/ci/badge.svg` | `ci.badge` | `badge.svg` is a legal literal segment; `?branch=`. |

**Releases**

| Pattern | Op | Notes |
|---|---|---|
| `GET /{o}/{r}/releases` | `release.list` | `[md]`; RSS/Atom via Accept. |
| `GET|POST /{o}/{r}/releases/new` | `release.create` | |
| `GET /{o}/{r}/releases/latest` | →301 tag page | |
| `GET /{o}/{r}/releases/tag/{name...}` | `release.show` | Slashed tags natural. `[md]` |
| `GET|POST /{o}/{r}/releases/edit/{name...}` `POST /releases/delete/{name...}` | `release.update`/`delete` | `edit`/`delete` literal keeps `{name...}` terminal and legal. |
| `GET /{o}/{r}/releases/attachments/{uuid}` | `release.asset` | |

**Social**

| Pattern | Op |
|---|---|
| `GET /{o}/{r}/stars` `/watchers` `/forks` | `repo.stars`/`watchers`/`forks` |
| `POST /{o}/{r}/star` `/unstar` `/watch` | `repo.star`/`repo.watch` (`action=watch|unwatch|ignore`) |
| `GET|POST /{o}/{r}/fork` | `repo.fork` — target owner = self or creatable org. |

**Repo settings (admins)**

| Pattern | Op |
|---|---|
| `GET|POST /{o}/{r}/settings` | `repo.settings` — description, topics, default bookmark, visibility, archive. |
| `GET|POST /{o}/{r}/settings/collaborators` | Users+teams → role grants. |
| `GET|POST /{o}/{r}/settings/protected` | Protected ref patterns + **land policy** — required attestation kinds/results; the web face of ruk `land`/`attest` capabilities. |
| `GET|POST /{o}/{r}/settings/hooks` + `/{id}` `/deliveries` `POST .../replay/{delivery}` | Repo webhooks (Standard Webhooks signing). |
| `GET|POST /{o}/{r}/settings/mirrors` + `/{id}` `POST .../sync` | Push mirrors: mode (`exact` force+prune / `ff-only`), ref selection (`all`/`default+tags`/`globs`), per-ref `PushRefOutcome` history, manual reconcile. |
| `GET|POST /{o}/{r}/settings/ci` | CI enable, runner selection, cache policy. |
| `GET|POST /{o}/{r}/settings/keys` | Read-only deploy keys. |
| `POST /{o}/{r}/settings/rename` `/transfer` `/archive` `/delete` | Destructive ops, typed-confirm forms. |

**Repo internals `/{o}/{r}/-/…`** (plumbing; JSON is the real consumer)

| Pattern | Op |
|---|---|
| `GET /{o}/{r}/-/ops` `/ops/{id}` + `POST .../ops/{id}/undo` (admin) | `repo.oplog`/`repo.op`/`repo.undo` — `op.log` view. |
| `GET /{o}/{r}/-/claims` | `repo.claims` — `claim.list` (holder=any): active claims, holders, paths, expiry. |
| `GET /{o}/{r}/-/attestations` | `repo.attestations` — `attest.list` (`?subject= ?kind= ?signer=`). |
| `GET /{o}/{r}/-/refs` | `repo.refs` — paginated raw refs (R2). `[json]` |

### 4.8 Git wire (`/{o}/{r}.git`)

ServeMux cannot express a `.git` suffix inside a pattern, so routing is:

- `GET /{o}/{r}` — when `{r}` ends `.git` **and nothing follows**: **301 to `/{o}/{r}`** minus the suffix. One decisive behavior — git clients never request the bare `.git` path (they always append `/info/refs?service=…`); browsers and link-followers get the human page.
- `/{o}/{r}/{rest...}` — **one method-less catch-all**, strictly more general than every sibling so it can't panic registration; inside, evaluated in order:
  1. `{rest}` empty → request was `/{o}/{r}/` (the mux never strips trailing slashes; `cleanPath` preserves them and `matchOrRedirect` only ever adds one) → **301 to `/{o}/{r}` minus any trailing `.git` suffix**. So `/o/r/` → `/o/r` and `/o/x.git/` → `/o/x` — a single hop, never a wire 404 (binding: `/{o}/{r}/{rest...}` gives `r="x.git"`, `rest=""`; the wildcard owns only the trailing remainder, never `x.git/`).
  2. `{r}` ends `.git` → `{rest}` selects the wire endpoint: `GET info/refs` (`?service=git-upload-pack|git-receive-pack`), `POST git-upload-pack`, `POST git-receive-pack`, `POST git-upload-archive`, `GET HEAD` (symref, dumb discovery), `GET objects/info/packs`, `GET objects/{...}` (optional dumb-HTTP read — ruk-git has `dumb_http` client-side per `task-httpsurface-report.md`; server side optional). Unknown `.git` sub-paths → 404.
  3. Non-`.git` `{rest}` → **path-only lookup against the registered repo routes**: if `{rest}` matches a known route shape for a different method (e.g. `POST /{o}/{r}/changes`) → **405 + `Allow`** listing its registered methods; otherwise → 404 within repo chrome.

Pushes to `refs/for/{target}` on `git-receive-pack` create/update changes (settled R5); `refs/heads/agent/**` accepted per policy (policy.rs example). Auth: `Authorization: Basic` token-as-password (scoped), anonymous read on public repos; `E_FORBIDDEN`→403, `E_NOT_FOUND`→404 with no existence leak for private repos. SSH is outside HTTP space: `ssh://git@host/{o}/{r}.git`.

### 4.9 Explicit non-resources (settled ruk machinery that gets no URL)

These ruk concepts are deliberately not URL-addressable; observability is covered elsewhere:

- **Workspaces** (`ws.new/scope/take/drop/recover`) — session-scoped coordinator machinery, lease-bound and ephemeral; there is no `ws.list` read today, and a workspace's *output* is visible as changes/commits. Observability: `/-/ops` (op.log records their lifecycle ops) and `/-/claims`. **Reserved shape if Phase R adds a `ws.list` read:** `GET /{o}/{r}/-/workspaces` (+ `/{id}`) under repo plumbing, same JSON-first contract.
- **Sessions** (`session.open/resume`) — transport state, not resources.
- **Land plans** (`PlanId` from `land.prepare`) — expire in one hour; results surface on the change page and `/-/ops`, never a permalink.
- **Pins** (`pin`/`unpin`) — retention bookkeeping; visible only through `/-/ops`.

## 5. Revision addressing (`{rev}`)

A `{rev}` segment resolves by **parallel categories**, exactly `resolve_symbol` (`eval.rs:766-823`). There is no sequential fallback: hit-sets for all categories are computed first, then—

- **0 categories with hits** → `E_INVALID` "matches no bookmark, tag, change, or commit".
- **≥2 categories with hits** → `E_INVALID` "ambiguous across categories" carrying `symbol_candidates` (namespaces + names) → disambiguation page/problem listing typed candidates, each linking its canonical URL. E.g. a bookmark and a tag both named `dup`, or bookmark `deadbeef` vs head prefix `deadbeef` — **never** a prefer-ref pick, never a warning header.
- **Exactly 1 category with hits** → resolve within it: exact match beats prefix inside a ref namespace (`ref_hits`); multiple hits in the category are themselves `E_INVALID` ("ambiguous; spell a longer prefix").

| Category | Rule |
|---|---|
| Bookmark | `refs/heads/{s}`; a full-form `refs/…` symbol (after unescape, e.g. `refs%2Fheads%2Fmain`) matches only full names — never a bare short name. |
| Tag | `refs/tags/{s}` — same rules, independent category. |
| Remote bookmark | Only reachable via full form `refs/remotes/…`; bare symbols never hit this category. |
| Change | `[k-z]+` prefix of a change-id's 32-char reverse hex (`change.rs:104-150`) → that change's head(s). |
| Head | all-hex prefix, case-insensitive (`is_ascii_hexdigit` then `to_ascii_lowercase`, `eval.rs:869-874`) of a commit OID → commit; canonicalized to lowercase in URLs. Full 40/64-hex OID is just the maximal prefix — no special tier. |

The alphabets are **disjoint** — head prefixes `[0-9a-f]` vs change-ids `[k-z]` — so commit-vs-change can never collide; live ambiguity classes are bookmark-vs-tag, bookmark-vs-head-prefix, and intra-namespace prefix.

**Rukie-layer conventions (explicit divergences from raw `resolve_symbol`, stated):**

- **Prefix floor ≥4 for URL/UI display** — a *rukie* DoS/readability policy for `{rev}` segments (a 1–3-char symbol that is not an exact ref is rejected "symbol too short for prefix resolution"), applied on top of ruk resolution, not inside it. Full `resolve_symbol` stays reachable via `?revset=` on `/log`.
- **`@` is not a URL rev** — in ruk revsets `@` is `NodeKind::At`, the working-copy head, and `@-` its parents (`eval.rs:578`, syntax.rs): a hosted repository has no working copy, so `@` means nothing here and is rejected rather than rebound to the default bookmark.
- **`a...b` alias** — inside `{range}` segments only, `...` is **normalized to `::` by the web layer before any revset call** (git's merge-base convention ≈ `DagRange` + merge-base diff); ruk's lexer has no `...` token (`syntax.rs:444-447`), so the alias is a spelling translation, never passed through.
- **Slash encoding** — `/` inside a rev → `%2F` (`/tree/feature%2Fx/src/a.go`, `/compare/feature%2Fx..main`). Graceful fallback for humans typing raw slashes in `{rev}/{path...}` URLs: greedy longest-ref-prefix resolution — `/tree/feature/x/src` parses ref `feature/x` + path `src`; if two interpretations remain plausible, serve the disambiguation page with the canonical `%2F` link instead of guessing. `/log/{rev...}` consumes the whole remainder as one rev — no encoding needed.
- **Defaults** — omitted rev (`/tree`, `/log`, `/compare` form) → default bookmark, 301 to the rev-explicit URL so every shared URL is self-contained. `commit/{oid}` accepts ≥7 hex (display width). No `~n`/`^n` in URLs (revset exposes no parent ops); `?revset=` on `/log` passes arbitrary ruk revset text through `query`.

## 6. Reserved names

Derivation rule (replaces any closed list): **every literal first path segment in the route table is a reserved owner name**, plus explicit suffix patterns — the registry is generated from the route table at build time so a new global route can never silently become a username ban (the Gitea `reservedUsernames`/`reservedUserPatterns` mechanism at `user.go:613-665`, kept as a two-tier design).

**Routing-required (owner names — collide with registered literals):**
`-`, `.`, `..`, `.well-known`, `assets`, `robots.txt`, `favicon.ico`, `manifest.json`, `sitemap.xml`, `explore`, `search`, `inbox`, `changes`, `tasks`, `ci`, `login`, `logout`, `signup`, `settings`, `admin`, `new`, `invites`.

**Anti-spoof / future-proof (no route today or plumbing moved under `-`):**
`api`, `user`, `org`, `repo`, `mcp`, `metrics`, `healthz`, `debug`, `static`, `help`, `about`, `support`, `security`, `terms`, `privacy`, `ghost`, `system`, `root`, `invite`, `avatar`, `avatars`, `openid`, `oauth`, `oidc`, `download`, `attachments`, `notifications`, `issues`, `pulls`, `milestones`, `wiki`, `packages`, `actions` (legacy-forge terms — a `/{issues}` profile would confuse migrants; all free to unreserve later since `/-/` already owns plumbing).

**Suffix patterns (owner AND repo):** `*.git`, `*.wiki`, `*.rss`, `*.atom`, `*.keys`, `*.gpg`. `*.keys` powers `/{owner}.keys` via suffix check; `*.git` keeps clone URLs unambiguous; `*.wiki` reserves a possible future wiki despite being cut.

**Repo names additionally:** `-` (protects `/{o}/-`), `*.zip`, `*.tar`, `*.tar.gz`, `*.tgz`, `*.diff`, `*.patch` (harmless now, keeps archive/diff suffix URLs available).

**Namespace disjointness:** `changes/{cid}` accepts only `[k-z]+`, so literals `new`, `v`, `diff`, `stack` can never collide; `tasks/{n}` and `ci/runs/{id}` are numeric so `new`/`jobs`/`log` are free; `releases/{name...}` never sees `new`/`latest`/`tag`/`edit`/`delete`/`attachments` as tag views because literals win.

## 7. Permalinks & canonicalization

- **Immutable:** `/commit/{full-oid}`, `/blob/{full-oid}/{path}`, `/raw/{full-oid}/{path}`, `/changes/{cid}` (+`/v/{n}` patchset pin), `/tasks/{n}`, `/ci/runs/{id}`, `/releases/tag/{name}`, `/{o}/{r}/-/ops/{id}`. Every rev-addressed page rendered at a non-OID rev shows resolution (`feature/x → 9fceb02…`) and a permalink control reissuing the full-OID URL.
- **Anchors:** `#L{n}`; `?lines={a}-{b}` server-side range highlight; blame rows link per commit; task/change comments `#comment-{id}`.
- **301s (all explicit in handlers — the mux never strips slashes):** case-correct owner/repo; rename/transfer keeps old `{o}/{r}` and `{o}/{r}.git` forwarding via a redirect table [inference: retention policy owned by repo-lifecycle spec — recommend permanent rows]; `/{o}/`→`/{o}` (owner catch-all `rest=""` rule); `/{o}/{r}/`→`/{o}/{r}` and `/{o}/{r}.git/`→`/{o}/{r}` (repo catch-all `rest=""` strip-suffix rule); `/tree`/`/log`/`/compare` bare → rev-explicit; `/releases/latest`; `/.well-known/change-password`; bare `{o}/{r}.git` → `{o}/{r}`.
- **Feeds:** `Accept: application/rss+xml|atom+xml` on `/log`, `/releases`, `/changes`, `/tasks`, `/inbox`.

## 8. Pagination & query conventions

`?limit=` (default 50, max 500 — ruk caps pages at 1000, `limits.page_max`) + `?cursor=<opaque>` on every list. Responses carry `next_cursor` (JSON) and `rel="next"` + `Link:` header (HTML/agents).

**Two cursor classes, never conflated:** URL cursors are *rukie-issued durable keyset cursors* — base64url(JSON `{v:1,k:[sortkeys],id}`), tamper-evident. Ruk's own lease-bound `Page`/`ViewInfo` cursors expire in ~30s (README; `op.log`, `claim.list`, `attest.list`) and are minted/consumed server-side only — a URL cursor is always re-derivable from stored sort keys, never a live lease.

Stable orders: changes `updated_at desc, change_id`; log = ruk order + position tiebreak; refs `name asc`; tasks `updated_at desc, n`; runs `started_at desc, id`; users/repos `name asc`; inbox `created_at desc, id`; ops `seq desc`. Filters compose with cursor; the cursor stores a filter fingerprint — mismatched filter+cursor → `400 E_INVALID`, never silently re-scoped.

## 9. Navigation model

- **Global nav:** logo→`/`; Explore `/explore`; Changes `/changes`; Tasks `/tasks`; CI `/ci`; search box →`/search`; Inbox `/inbox` (server-rendered unread badge); avatar → `/settings`, `/logout`, Admin `/admin` (admins); `+ New` → `/new/repo`, `/new/org`.
- **Owner header:** `/{owner}` root; org adds tabs Members `/-/members` · Teams `/-/teams` · Settings `/-/settings`.
- **Repo tabs:** Code `/{o}/{r}` · Changes `/changes` (open badge) · Tasks `/tasks` (open badge) · CI `/ci` (status dot) · Releases `/releases` · Settings `/settings` (admins). Code secondary row: branch selector (`<details>` list → `/tree/{name}` — zero-JS), Branches `/branches` · Tags `/tags` · Search `/search` · Compare `/compare`.
- **Breadcrumbs:** `{o} / {r} / {rev-chip} / dir / sub / file`; each dir → `/tree/{rev}/{prefix}`; `{rev-chip}` = resolved OID + permalink copy. Change pages: `{o}/{r} / Changes / {cid-short} / v{n}`.
- **Empty repo:** `/{o}/{r}` renders clone/push instructions + first-task/first-change actions instead of the browser.

## 10. Page inventory

Format: **question · action · data · empty/error** — one row per page. `[md]`/`[json]` mark agent-first renderings.

| Page | Question → action → data → empty/error |
|---|---|
| `GET /` | "What needs me?" · open top item · inbox count, review queue, claimable tasks, CI failures (signed-in) or product intro (anon) · empty→explore CTA |
| `GET /explore` | redirect → `/explore/repos` |
| `GET /explore/repos` | "What repos exist?" · open repo · paged list+sort+topic · zero-state copy |
| `GET /explore/users` | "Who's here?" · open profile · paged users |
| `GET /explore/orgs` | "What orgs exist?" · open org · paged orgs |
| `GET /explore/code` | "Where is this code?" · open match · code hits `[md]` · no `?q`→hint |
| `GET /explore/topics` `/topics/{name}` | "What topics exist?" · open topic repos · topic cloud/list |
| `GET /search` | "Find anything?" · open hit · grouped repo/code/change/task/user results `[json][md]` · empty `?q`→help |
| `GET /inbox` | "What happened?" · open/mark read · notifications by repo `[json]` · "all caught up" |
| `GET /changes` | "What review is mine?" · open change · cross-repo queue `[json][md]` · "nothing awaiting" |
| `GET /tasks` | "What can I claim?" · inline claim button · claimable/held cross-repo `[json][md]` |
| `GET /ci` | "Is the fleet green?" · open failing run · running/queued/failed runs `[json]` |
| `GET /invites/{token}` | "Join this team/org?" · accept (POST same URL) · org/team preview · expired→410 |
| `GET|POST /new/repo` | "Create a repo" · submit · form (name, owner, visibility, default bookmark, object format sha1/sha256) · inline field errors |
| `GET|POST /new/org` | "Create an org" · submit · name/visibility form |
| `GET /login` | "Who am I?" · submit password · form + `?next=` · generic 401 (no enumeration) |
| `GET /login/totp` | "Prove second factor" · submit code · TOTP form · invalid→inline error |
| `GET /login/oidc/{provider}` | "Log in via X?" · (302 to provider) · none · disabled provider→404 |
| `GET /login/oidc/{provider}/callback` | "Finish login/link" · redirect home · none · mismatch→error page |
| `GET /logout` (page) / `POST /logout` | "Sign out?" · confirm POST · confirm page · already-out→redirect |
| `GET|POST /signup` | "Create account?" · submit · registration form · disabled→404 "invite required" |
| `GET|POST /settings` | "Edit profile" · save · name/avatar/bio form |
| `GET|POST /settings/security` | "Am I secure?" · change password / enroll TOTP / revoke session · factors+sessions list |
| `GET|POST /settings/tokens` | "Mint an agent token?" · create with scopes · token list w/ scope+expiry; secret shown once · none→"create first token" |
| `GET|POST /settings/keys` | "Add SSH key?" · add/delete · key list w/ fingerprints |
| `GET|POST /settings/emails` | "Bind identities?" · add/remove · addresses for commit matching |
| `GET|POST /settings/preferences` | "Tune display?" · save · timezone/theme/diff-whitespace |
| `GET /settings/orgs` | "Where do I belong?" · open org/invite · orgs+pending invites |
| `GET|POST /settings/notifications` | "Route my inbox?" · save · watch defaults+rules |
| `GET /admin` | "How's the instance?" · drill into monitor/queue · counts+health tiles |
| `GET /admin/users` | "Manage whom?" · open user · paged users · empty→create CTA |
| `GET /admin/users/{id}` | "Inspect/fix this user" · reset-password/disable/impersonate (POST) · user detail+sessions+orgs |
| `GET /admin/orgs` `/admin/repos` | "Manage org/repo" · search/act · paged lists+visibility/delete actions |
| `GET|POST /admin/hooks` + deliveries | "What site hooks fire?" · add/replay · hook list+delivery log+replay result |
| `GET|POST /admin/runners` | "Who runs CI?" · issue token/drain · runner list+labels+status |
| `GET /admin/queue` | "Is CI stuck?" · requeue/cancel · job queue+depths |
| `GET /admin/audit` | "Who did what?" · open event · filtered audit log `[json]` |
| `GET /admin/monitor` | "Is it healthy?" · inspect · runtime/store/daemon-pool/queues |
| `GET|POST /admin/config` | "Change runtime config?" · save · signup/anon-read/OIDC settings |
| `GET /-/api` | "What can I call?" · pick op → schema link · op index `[json][md]` |
| `GET /-/api/openapi.json` | machine spec · fetch · OpenAPI 3.1 doc |
| `/-/mcp` | agent transport · POST JSON-RPC / GET SSE · MCP tools |
| `GET /-/metrics` | "Scrape me" · Prometheus pull · metrics (admin token) |
| `GET /-/healthz` | "Alive?" · probe · liveness body |
| `GET /-/avatars/{hash}` | avatar bytes · `?size=` |
| `GET /{owner}` | "Who/what do they ship?" · open repo · profile+paged repos; org: +members `[md]` · no repos→quiet card; private→404 |
| `GET /{owner}.keys` (via `/{owner}` suffix) | "Grant SSH?" · copy keys · text/plain key lines · none→200 empty |
| `GET /{owner}/-/members` | "Who's in the org?" · manage (owner) · member list+roles |
| `GET /{owner}/-/teams` `/teams/{team}` | "What teams/grants?" · add member/repo (owner) · teams, team members+repo grants |
| `GET|POST /{owner}/-/settings[/*]` | "Configure org" · save · profile/rename/delete, permissions, teams CRUD, hooks+deliveries+replay · rename/delete typed-confirm |
| `/{o}/{rest...}` catch-all | unknown owner paths · `/{o}/`→301 `/{o}`; else 405 if path exists for another method, 404 if not |
| `/{o}/{r}/{rest...}` catch-all | `/{o}/{r}/`→301 (`.git` suffix stripped in one hop: `/o/x.git/`→`/o/x`); `.git` + rest → wire; else 405/404 lookup |
| `GET /{o}/{r}` | "What is this repo?" · browse tree / copy clone URL · root listing+README+clone URLs+chips `[md]` · empty→push instructions; `.git`→301; private→404 |
| `GET /{o}/{r}/tree/{rev}[/{path...}]` | "What's in this dir at rev?" · drill/open file · entries+per-entry last commit `[json][md]` · empty dir; bad rev→§5 error |
| `GET /{o}/{r}/blob/{rev}/{path...}` | "What does this file say?" · permalink/raw/blame · rendered|highlighted+`#L`/`?lines`+blob OID `[md]` · binary→download card; 404 |
| `GET /{o}/{r}/raw/{rev}/{path...}` | "Give bytes" · fetch/save · sniffed bytes, ETag=OID · 404 |
| `GET /{o}/{r}/blame/{rev}/{path...}` | "Who touched line N?" · open commit/change · per-line attribution `[json]` |
| `GET /{o}/{r}/log[/{rev...}]` | "How did we get here?" · open commit/older · log+optional graph+attest chips `[json][md]` · `E_SHALLOW` notice |
| `GET /{o}/{r}/commit/{oid}[/diff|/patch]` | "What did this change?" · read diff · meta+parents+containing refs+change-id+attests · conflicted→conflict terms; bad oid→404 |
| `GET /{o}/{r}/compare` `/{range}` | "A vs B?" · read diff / open change · merge-base+commits+diff `[json][md]` · bad range→help/400 |
| `GET /{o}/{r}/branches` | "What bookmarks?" · open branch view · list+tip age `[json]` |
| `GET /{o}/{r}/branches/{name...}` | "What's queued on this branch?" · open changes/land · tip+log+stacked changes+land queue+protection `[md]` · no changes→empty |
| `GET /{o}/{r}/tags` `/tags/{name...}` | "What was tagged?" · open tag/release · tag→commit/release link |
| `GET /{o}/{r}/archive/{rev}` | "Download snapshot" · pick `?format` · zip/tar stream · bad rev→404 |
| `GET /{o}/{r}/search` | "Where in this repo is X?" · open match · file:line hits+context `[json][md]` · no q→hint; `E_LIMIT`→refine |
| `GET /{o}/{r}/changes` | "What's under review?" · open change · filtered list+CI chips `[json][md]` · empty→push `refs/for/` guidance |
| `GET /{o}/{r}/changes/{cid}` | "Should this land?" · **review/land** · description+provenance+divergent heads+patchset diff+inline comments+attestation checklist vs land policy (`LandPrepareReply.requirements`)+conflict+activity `[json][md]` · conflicted→resolve guide; abandoned→muted+reopen POST; bad cid→404/ambiguity page |
| `GET /{o}/{r}/changes/{cid}/v/{n}` | "What was patchset n?" · compare vs current · pinned diff · bad n→404 |
| `GET /{o}/{r}/changes/{cid}/diff/{range}` | "What changed between patchsets?" · review delta · inter-diff · bad range→400 |
| `GET /{o}/{r}/changes/{cid}/stack` | "What lands first?" · open blocker · ordered chain+landability |
| `GET /{o}/{r}/tasks` | "What's open/claimable?" · inline claim · list+holder+TTL `[json][md]` · empty→create CTA |
| `GET|POST /{o}/{r}/tasks/new` | "File work" · submit · title/body/labels form |
| `GET /{o}/{r}/tasks/{n}` | "What's the work, who holds it?" · claim/release/comment/state · body+`ClaimRecord`+linked changes+history `[json][md]` · `E_CLAIMED`→409 holder page |
| `GET /{o}/{r}/ci` | "Is this repo green?" · open failing run · runs+trigger+ref/change+status `[json][md]` · empty→config help |
| `GET /{o}/{r}/ci/runs/{id}` | "Why pass/fail?" · open job/rerun/cancel/approve (POST) · jobs matrix+timing+artifacts+attestation badge `[json][md]` · queued→position |
| `GET /{o}/{r}/ci/runs/{id}/jobs/{j}` `/log` | "Where did it break?" · read/download log · step log (raw `text/plain`) · pruned→410 |
| `GET /{o}/{r}/ci/runs/{id}/artifacts/{name}` | artifact bytes · download |
| `GET /{o}/{r}/ci/badge.svg` | status badge · embed · svg |
| `GET /{o}/{r}/releases` | "What can I run?" · download · releases+assets+archives `[md]` · empty→tag CTA |
| `GET /{o}/{r}/releases/tag/{name...}` | "What's in this release?" · download · notes+assets+draft flag · bad tag→404 |
| `GET|POST /{o}/{r}/releases/new` `/edit/{name...}` `/delete/{name...}` | publish/edit/remove · save/delete · tag picker+notes+asset upload |
| `GET /{o}/{r}/releases/attachments/{uuid}` | asset bytes · download · 404 |
| `GET /{o}/{r}/stars` `/watchers` `/forks` | "Who cares?" · open profile/repo · paged lists |
| `POST /{o}/{r}/star` `/unstar` `/watch` | toggle · button forms |
| `GET|POST /{o}/{r}/fork` | "Fork where?" · pick owner+submit · owner selector (self/orgs) |
| `GET|POST /{o}/{r}/settings[/*]` | "Configure repo" · save · collaborators, protected refs+**land policy**, hooks+deliveries+replay, mirrors (mode/ref-selection/per-ref `PushRefOutcome`+sync), ci, deploy keys, rename/transfer/archive/delete typed-confirm |
| `GET /{o}/{r}/-/ops` `/ops/{id}` | "What did the repo do?" · inspect op (+admin undo POST) · op.log entries+fact before/after `[json]` |
| `GET /{o}/{r}/-/claims` | "Who holds what?" · inspect/expiry · `claim.list` records `[json]` |
| `GET /{o}/{r}/-/attestations` | "Who attested what?" · filter `?subject ?kind ?signer` · `attest.list` records `[json]` |
| `GET /{o}/{r}/-/refs` | "Raw refs?" · page · paginated ref dump `[json]` |
| `ws.*` workspaces | **deliberately no page** — session-scoped, no `ws.list` read; §4.9 names the reserved shape if Phase R adds one |
| git wire `/{o}/{r}.git/*` | clone/fetch/push · smart-HTTP protocol · pkt-line streams · 403/404 uniform; bare `.git`→301; `/{o}/{r}/`→301 `/{o}/{r}`; `/{o}/{r}.git/`→301 `/{o}/{r}` (one hop) |

## 11. Same URL, agent contract

- **Read:** `Accept: application/json` → op output identical to `rukie <op> --json` / MCP tool result. `text/markdown` → provenance front-matter (`canonical`, `repo`, `resolved_rev`, `oid`) + Markdown rendering (diffs fenced, lists/tables) honoring the same `?cursor`/`?limit`.
- **Write:** `POST` the resource URL, JSON body = op input; `Idempotency-Key` header → `RequestId`, replayed receipts returned via `X-Rukie-Receipt`. Form posts are the same ops + CSRF; `Authorization: Bearer` skips CSRF. Wrong method on a valid resource → `405` + `Allow` — via ServeMux's method-miss handling on methoded patterns, or the catch-all's path-only route lookup for paths it alone can reach (§4.8).
- **Errors:** RFC 9457 `application/problem+json` (`type` stable URN; `code` = mapped ruk `E_*`: `E_NOT_FOUND`→404, `E_CLAIMED`/`E_STALE`/`E_DIVERGENT`/`E_UNDO_CONFLICT`→409, `E_FORBIDDEN`→403, `E_LIMIT`→413/429, `E_SHALLOW`/`E_INVALID`→400). HTML gets styled pages with the same code + remediation; markdown requests on error → problem+json (errors are never markdown).
- **Discovery:** `/-/api` index, `/-/api/openapi.json`, `/-/mcp`; every HTML page emits `<link rel="alternate" type="application/json">` and `…text/markdown` pointing at itself.
- **Auth:** `Bearer <scoped-token>` (from `/settings/tokens`); Basic token-as-password for git HTTP; session cookie for browsers. `401` → `WWW-Authenticate: Bearer realm="rukie", scope="<needed>"`.
- **Caching:** `ETag`/`304` on raw/blob (blob OID), refs (ref generation), log (tip+query hash); `Last-Modified` releases; assets immutable.

## 12. ServeMux legality audit

- Every **Pattern** column value is legal ServeMux grammar: full-segment `{name}`, terminal `{name...}`, `{$}`, method tokens limited to real methods or absent (`/-/mcp`, `/{o}/{rest...}`, `/{o}/{r}/{rest...}` are method-less — there is no `ALL` matcher). Rejected and replaced: `/compare/{a}...{b}` (invalid wildcard name → `/compare/{range}`), `{owner}.keys` and `{repo}.git` (partial-segment suffixes → suffix checks inside `GET /{owner}` and the catch-all), `{rev}.{ext}` (`Accept` + literal `/diff` `/patch` children), `{tag...}/edit` (multi-wildcard must be terminal → `/edit/{name...}`).

[Showing lines 453-904 of 930 (49.4KB limit). Use :905 to continue]
- Every **Pattern** column value is legal ServeMux grammar: full-segment `{name}`, terminal `{name...}`, `{$}`, method tokens limited to real methods or absent (`/-/mcp`, `/{o}/{rest...}`, `/{o}/{r}/{rest...}` are method-less — there is no `ALL` matcher). Rejected and replaced: `/compare/{a}...{b}` (invalid wildcard name → `/compare/{range}`), `{owner}.keys` and `{repo}.git` (partial-segment suffixes → suffix checks inside `GET /{owner}` and the catch-all), `{rev}.{ext}` (`Accept` + literal `/diff` `/patch` children), `{tag...}/edit` (multi-wildcard must be terminal → `/edit/{name...}`).
- Precedence proofs: at each segment position literals (`explore`, `search`, `inbox`, `changes`, `tasks`, `ci`, `login`, `logout`, `signup`, `settings`, `admin`, `new`, `invites`, `assets`, `-`, `.well-known`, `robots.txt`, `favicon.ico`, `manifest.json`, `sitemap.xml`; repo-level `tree`, `blob`, `raw`, `blame`, `log`, `commit`, `compare`, `branches`, `tags`, `archive`, `search`, `changes`, `tasks`, `ci`, `releases`, `stars`, `watchers`, `forks`, `fork`, `star`, `unstar`, `watch`, `settings`, `-`; leaf literals `new`, `latest`, `tag`, `edit`, `delete`, `v`, `diff`, `stack`, `jobs`, `log`, `ops`, `claims`, `refs`, `attestations`, `badge.svg`) outrank sibling wildcards — so `/{o}/-/settings` vs `/{o}/{rest...}` vs `/{o}/{r}/{rest...}`, `/releases/tag/…` vs `/releases/new`, `/tasks/new` vs `/tasks/{n}`, `/-/api` vs `/{o}/{r}` all resolve deterministically with zero registration conflicts.
- Both catch-alls are strictly more general than every sibling — no panic. Trailing slashes are handled explicitly in-handler (`rest=""` → 301 with `.git` strip at repo level; `.git` suffix dispatch on non-empty rest) because `cleanPath` preserves them and `matchOrRedirect` only adds slashes; `/o/`, `/o/r/`, and `/o/x.git/` are never silently cleaned to `/o`, `/o/r`, or `/o/x.git`.
- Wildcard names unique per pattern and valid Go identifiers: `owner`, `repo`, `rev`, `path`, `range`, `cid`, `name`, `id`, `j`, `n`, `uuid`, `token`, `team`, `provider`, `file`, `rest`, `hash`.

## 13. Intentional divergences from Gitea

**Dropped:** `/api/v1`, swagger, `/{o}/{r}/issues|pulls|wiki|projects|packages|activity|actions`, `src|media|render|lastcommit|blob_excerpt|tree-list|tree-view|graph`, all `.rss`/`.atom`/`.diff`/`.patch` filename routes, `/org/{o}` dual hierarchy, web-editor `/_edit` routes (agents write via ruk), `/user/*` sprawl, top-level `/attachments`. **Renamed:** `commits`→`log`, `src`→`tree`/`blob`, `issues`→`tasks`, `pulls`→`changes`, `actions`→`ci`, `notifications`→`inbox`. **Kept:** `{o}/{r}.git` wire shape, `/{owner}.keys` (via `/{owner}` suffix check), `compare`, `releases`, `settings` layout, `/-/` plumbing convention, flat top-level dashboards (`/issues`→`/tasks`, `/pulls`→`/changes`, `/notifications`→`/inbox`). **Not URL resources** (§4.9): workspaces, sessions, land plans, pins.

## 14. Open items for sibling specs

- CI config filename ([inference: `.rukie/ci.yaml`]) — owned by CI spec; URL space is agnostic.
- Human-review attestations assumed server-signed (`change.review`) — owned by review/attestation spec.
- Rename-redirect retention policy (§7 inference) — owned by repo-lifecycle spec.
- Task claim path spelling `task/<n>` must equal whatever the tasks spec feeds `claim.acquire`.
- `{owner}` user vs org differentiation is a store lookup, not URL-visible — owned by identity spec.
- If Phase R adds `ws.list` (or equivalent workspace read), the reserved URL shape is `GET /{o}/{r}/-/workspaces` (+ `/{id}`) — §4.9.

---

### Critical Files for Implementation

- `ruk/crates/ruk-core/src/change.rs` — `ChangeId` reverse-hex `k`–`z` alphabet, exact-32 parse, `from_oid` derivation; defines `{cid}` grammar, prefix rules, and 1:N divergence semantics of change pages.
- `ruk/crates/ruk-core/src/revset/eval.rs` — `resolve_symbol` parallel-category resolution (`:766-823`), `ref_hits`/`head_hit`/`change_hit` prefix mechanics (case-insensitive hex, no length floor), `NodeKind::At` = working-copy head; the exact `{rev}` contract.
- `ruk/crates/ruk-core/src/revset/syntax.rs` — `x..y`/`x::y` (`DotDot`/`ColonColon`) range grammar for `{range}`; `@`/`@-` working-copy atoms excluded from URLs; `a...b` is a rukie alias normalized before revset, not a token.
- `ruk/crates/ruk-core/src/protocol.rs` — `ClaimRecord`/`ClaimTtl`, `AttestStatement`, `LandPrepareReply.requirements`, `PushRefOutcome`, `ws.*` mutation set, page limits; the fields tasks/change/land/mirror pages render and §4.9's non-resource list.
- `ruk/crates/ruk/src/daemon/registry.rs` — daemon addressing is canonical-path-or-handle under `repo_roots`; proves `/{owner}/{repo}` is a web-layer namespace the rukie metadata store must map.