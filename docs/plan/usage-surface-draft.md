Final fixes applied: `search.code` now binds to the settled `search` method from `ruk-search-design.md` (SearchArgs contract, Ruk 30-second lease cursors, ≤256 MiB/100 k blob/5 s budgets, binary and >512 KiB skips, ≤800 ms p95 target), gap G4 is deleted, the `cmd:grep` interim row is gone, and every §3.5-§3.8 row carries its own `scope:` slot again. Still no file-write tool exists in this role, so the complete file follows; save unchanged to:
`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/usage-surface-draft.md`

Delta only, since the rest is unchanged from the previous emit: §1.6 cursor list loses "search" (search uses Ruk `PageCursor`s directly; expired lease → `E_LEASE_LOST`, view advance → `E_FORBIDDEN`, client restarts from a fresh first page); §3.7 `search.code` rewritten; §7 renumbered G1-G7 with G4 (code search) removed; §8 grep row deleted; §9 search target ≤800 ms per the settled design.

---

````markdown
# rukie usage surface — draft (architect greenfield step 2)

Status: DRAFT, aligned to the settled sibling designs: `auth-design.md` (op list §2; argon2id §3; TOTP/recovery §4; OIDC §5; sessions §6; CSRF §7; tokens §8; SSH keys §9; git Basic + lockout §10; admin CLI §11; audit §12), `webhook-design.md` (events §4; endpoints/ops §5; outbox/worker §6-7), `inbox-design.md` (sources §1; watch levels §1.1; states §4; ops §6), `ruk-search-design.md` (typed `search` method), `research-push-mirror-targets.md` (§5 mechanics), `nojs-interaction-patterns.md` (form/CSRF/idempotency conventions). Change-review and task operations remain PROVISIONAL placeholders (§6).
Ruk grounding: `/tmp/ruk-model.json` (46 methods today; `search` is added by ruk-search-design.md); sources under `/home/alpha/rewrite/git-renewed/ruk`. Guesses are marked `[inference]`.

Operation count: 131 settled (§3) + 15 PROVISIONAL (§6). Scopes: the settled 14 of auth-design §8.2.

## 1. Conventions

### 1.1 Lexicon
- Operation name: `<domain>[.<sub>].<verb>`. Domains: `auth user org team repo code branch tag search release webhook mirror star explore inbox admin` (+ PROVISIONAL `change task`).
- Closed verb set:
  - CRUD: `get`, `list`, `create`, `update`, `delete`.
  - Relations/attributes: `add`, `remove`, `set`.
  - Named actions: `callback change confirm count disable enroll leave link login logout mark ping purge redeliver regenerate resolve reset restore retry revoke rotate snooze start sync test unlink verify watch`.
  - Compound action segments `<object>-<verb>` are allowed (`rotate-secret`, `reset-password`, `totp-reset`).
  - Bare-noun reads: in the `search` and `explore` domains the second segment names the corpus (`search.code`, `explore.repos`); the verb is implicitly list/query.
- Every operation name in §3 and §6 ends in one of the above (or a declared bare noun).
- "branch" = Ruk bookmark under `refs/heads/*`; "tag" = `refs/tags/*`.
- Webhook event `type` names come from webhook-design §4 verbatim (§4 below).

### 1.2 One table → four interfaces
- **JSON**: `METHOD /api/...`, `application/json`; errors `application/problem+json`.
  - Mutations on `/api` authenticate with `Authorization: Bearer <rk_ token>`. Exception: session-served operations (auth, settings, org, social — no token scope exists for them) and the anonymous auth operations (`auth.login`, `auth.totp.verify` on a pending challenge) accept the session cookie plus `_csrf`/`Origin`.
  - When `Authorization` is present the session cookie is ignored (auth-design §7).
  - DELETE never carries a body (RFC 9110 §9.3.5); its parameters go in the query.
- **HTML** (nojs-interaction-patterns): mutations are `<form method="post">` at the resource path shown in the op's `html:` value, with hidden `_csrf` and `_idem` (UUID minted per render; forwarded as the Ruk mutation `request_id`), plus `_etag` where the op is `If-Match`-gated; success → 303 PRG; GET forms for all read-only filters; dedicated GET confirm pages for destructive actions; `formaction` secondary submits; preview via `POST /-/preview`.
- **MCP**: Streamable HTTP at `/api/mcp`, Bearer auth; one tool per op; tool name = op name with `.`→`_` (per inbox-design §6: `inbox_list`, …); `readOnlyHint`/`destructiveHint`/`idempotentHint`; `tools/list` filtered by scope; byte-returning ops return a `resource_link`; mutations take an optional `idempotency_key` input.
- **CLI**: generated `rukie <op> [--<field> v]... [--json] [--idempotency-key K] [--if-match E] [--cursor C | --all]`; generates a key when absent. Admin CLI verbs per auth-design §11 (`rukie admin user reset-password|unlock|totp-reset|disable|enable`).
- **Derived endpoints (not ops)**: `GET /api/openapi.json`, `GET /api/ops.json`, `GET /-/problems/{slug}`, `GET /-/healthz`, `POST /-/preview`, `POST /-/attachments`.

### 1.3 Path rules
- Site pages `/-/…` and `/inbox…`, API `/api/…`, owners `/{owner}`, repos `/{owner}/{repo}`, clone `/{owner}/{repo}.git`, OIDC `GET /auth/oidc/{id}/start|callback` (auth-design §5.2).
- Reserved owner names: `-`, `api`, `.well-known`, `favicon.ico`, `robots.txt`, any name ending `.git`, plus the auth-design §5.3 reservation list `admin root system rukie api git support abuse` (applies to username creation of every kind).
- `{rev}`, `{branch}` and `{tag}` are one path segment, with `/` sent as `%2F` `[inference: Go ServeMux matches escaped segments]`. `{path...}` is the remainder.

### 1.4 Types
| Type | Constraint |
|---|---|
| `Id` | ULID, 26 chars |
| `Name` (user/org) | 1..40; `^[A-Za-z0-9][-.\w]*$`, no `[-._]{2,}`, no trailing `[-._]` (helpers.go:32-33); unique case-insensitively; not reserved (§1.3) |
| `RepoName` | 1..100; `^[A-Za-z0-9._-]+$`; not `.`/`..`/`-`; no `..`; no `.git` suffix (repo.go:75-77, structs/repo.go:146) |
| `FullName` | `Name "/" RepoName` |
| `TeamName` | as `Name`, 1..64; `owners` reserved |
| `BranchName` / `TagName` | git check-ref-format; ≤ 255 bytes; under `refs/heads/` / `refs/tags/` |
| `RefGlob` | gitignore-style glob matched against the full ref (`refs/heads/*`), ≤ 255 bytes (webhook-design §5) |
| `Rev` | full `Oid` \| `ChangeId` `^[k-z]{32}$` (PROVISIONAL) \| BranchName \| TagName \| `heads:<b>` \| `tags:<t>`; ≤ 300 bytes; tried in that order |
| `AbbrevOid` | `^[0-9a-f]{7,63}$` |
| `Oid` | `^([0-9a-f]{40}|[0-9a-f]{64})$`; zero-OID = create/delete sentinel in `ref.updated` |
| `Path` | repo-relative, `/`-separated, no empty/`.`/`..`/`.git` segment, no NUL (Ruk `RepoPath`); ≤ 4096 bytes |
| `Glob` | byte-level `*`/`?`/`**` whole-path glob (Ruk `glob_match` semantics), 1..1024 bytes; ≤ 32 per request (ruk-search-design §2) |
| `Text(a..b)` | UTF-8, a..b chars, no control chars except `\n` |
| `Markdown` | UTF-8 ≤ 65536 bytes; rendered server-side, raw HTML stripped |
| `Url` | absolute `https://` (http only with `allow_insecure_http`), ≤ 2048, no userinfo, ports 1-65535 (webhook-design §5) |
| `LocalPath` | same-origin absolute path starting `/`, ≤ 2048 |
| `RemoteUrl` | `ssh://…`, scp-form, or `https://…`; ≤ 2048; embedded credentials rejected |
| `Secret` | write-only string; never echoed |
| `Password` | UTF-8, min 12 chars, max 512 bytes, never truncated; ≥10k blocklist + `password == username/email` rejected (auth-design §3.1) |
| `TotpCode` | `^[0-9]{6}$` |
| `RecoveryCode` | 16 Crockford-base32 chars grouped `4-4-4-4` (`^[0-9A-Z]{4}(-[0-9A-Z]{4}){3}$`, alphabet excludes I,L,O,U) (auth-design §4.2) |
| `Challenge` | opaque base64url, 16..128 chars |
| `Email` | addr-spec ≤ 254; verified only via OIDC `email_verified` |
| `SshPublicKey` | one authorized_keys line ≤ 16 KiB; `ssh-ed25519`, `ecdsa-sha2-nistp256/384/521`, `ssh-rsa` ≥ 3072 bits; `ssh-dss`, `ssh-rsa < 3072`, `sk-*` rejected in v1 (auth-design §9) |
| `Topic` | `^[a-z0-9][a-z0-9-]{0,34}$` |
| `AssetName` | 1..255 chars, no `/`, no control chars |
| `MediaType` | RFC 6838 type/subtype ≤ 255 |
| `EventType` | one of the 15 names in §4, or the expanded "all" set at save time (no wildcards stored; webhook-design §4) |
| `InboxReason` | one of `author reviewer participant watcher_all watcher_custom mentioned review_requested assigned claimant admin mirror_owner endpoint_owner` (inbox-design §3.5) |
| `WatchLevel` | `ignore`\|`participate`\|`all`\|`custom`; `custom_bits = {changes, tasks, releases, ci}` (inbox-design §1.1) |
| `ThreadState` | `unread`\|`read`\|`done`\|`snoozed`; `muted` is an orthogonal flag (inbox-design §4) |
| `SearchPattern` | `{kind: "literal"\|"regex", text: Text(1..4096)}`; empty literal → `E_INVALID`; regex must compile (ruk-search-design §2) |
| `SearchBudget` | `{max_bytes? ≤ 268435456 = 268435456, max_blobs? ≤ 100000 = 100000, deadline_ms? ≤ 5000 = 5000}`; over-cap rejected, never clamped |
| `Visibility` | `public`\|`private` |
| `Permission` | `read`\|`write`\|`admin` |
| `Scope` | §2 (the settled 14) |
| `Timestamp` | RFC 3339 UTC |
| `Duration` | ISO 8601 duration or integer seconds |
| `Cursor` | opaque base64url ≤ 512 (forge cursors); Ruk `PageCursor` for `search` (30 s lease) |
| `Limit` | int 1..200, default 50 (inbox list ≤ 100 per inbox-design §6; search page ≤ 1000 per Ruk `PageLimit`) |
| `MirrorAuth` | `{kind: "none"|"token"|"ssh_key", username?: Text(1..255) (token only), token?: Secret 1..4096 (required for token)}`; `ssh_key` → server-generated ed25519 key returned once |
| `MirrorRefs` | `{branch_filter: [Glob] 0..32 (empty = all heads), tags: bool = true}` — heads refspec becomes one explicit `+refs/heads/<g>` per glob; tags always `+refs/tags/*` (research §5) |

### 1.5 Resource shapes (JSON; deltas from the settled DDLs are naming only)
- `UserRef {name, display_name, kind:"user"|"org", url}`; `RepoRef {owner, name, full_name, url}`
- `User {id, name, display_name, bio, website, location, created_at, version}`; self only: `commit_email, totp_enabled, is_admin, oidc_only, must_change_password`
- `Org {id, name, display_name, description, website, visibility, created_at, version}`; `Team {id, org, name, description, permission, repo_scope, member_count, version}`
- `Repo {id, owner, name, full_name, description, website, topics[], visibility, default_branch, archived, state:"active"|"forking"|"deleted", fork_of|null, counts{stars,forks}, clone{https,ssh}, features{releases}, created_at, updated_at, pushed_at, viewer{permission,starred,watch_level}, version}`
- `Signature {name, email, time}`; `CommitSummary {oid, change_id|null, subject, author, committer, parents:[Oid]}`
- `Commit = CommitSummary + {message, attestations:[Attestation]}`; `Attestation {kind, result, signer, signed_at, status}`
- `TreeEntry {name, path, kind:"tree"|"regular"|"executable"|"symlink"|"gitlink", oid, size|null, last_commit|null}`
- `Blob {commit, path, oid, size, executable, symlink_target|null, binary, text|null, truncated}` (text ≤ 1 MiB)
- `FileDiff {path, old_path|null, kind:"added"|"deleted"|"modified"|"renamed", binary, conflicted, old_oid|null, new_oid|null, patch|null, truncated, additions, deletions}` (≤ 256 KiB/file, ≤ 5 MiB/response)
- `BlameHunk {start_line, line_count, commit:CommitSummary, original_path}`
- `Branch {name, ref, target:Oid, commit:CommitSummary, is_default}`; `Tag {name, ref, target:Oid, commit:Oid, annotated, message|null, tagger|null}`
- `Release {tag, title, notes, draft, prerelease, commit|null, author, created_at, published_at|null, assets:[Asset], version}`
- `Asset {name, size, content_type, sha256, download_count, uploaded_by, created_at, url}`
- `Endpoint {id, scope:"repo"|"owner", repo|owner, url, event_types[], ref_globs[], active, concurrency, timeout_seconds, note, secret_enc (write-only, shown once), last_status:"never"|"ok"|"failing"|"disabled", consecutive_failures, disabled_at, disable_reason|null, created_at, version}` (webhook-design §6.1)
- `Message {id, webhook_id "msg_<ULID>", endpoint_id, event_type, event_time, payload_digest, state:"pending"|"inflight"|"delivered"|"dead", attempt_count, next_attempt_at|null, last_error|null, last_status_code|null, delivered_at|null, redelivery_of|null}` (payload bytes ≤ 256 KiB; §6.2)
- `Attempt {id, message_id, attempt_no, started_at, finished_at, http_status|null, error|null, resp_headers (8 KiB), resp_body (64 KiB), duration_ms}` (≤ 10 retained per message; §6.3)
- `Mirror {id, url, auth{kind, username|null, has_secret, ssh_public_key|null}, mode:"exact"|"fast_forward_only", refs:MirrorRefs, interval, sync_on_commit, enabled, status{state:"idle"|"queued"|"running"|"ok"|"failed"|"needs_attention"|"uncertain", last_error, last_attempt, last_success}, version}`
- `MirrorRun {id, mirror_id, trigger:"push"|"reconcile"|"manual", state, started_at, finished_at|null, refs:[{ref, action:"create"|"update"|"force"|"delete"|"skip", result:"ok"|"rejected"|"uncertain"|"non_ff_skipped", error_class|null}], error_class|null}` — error classes `auth not_found rate_limited rejected_ref too_large push_protection network quota` (research §5)
- `Thread {id, subject_kind:"change"|"branch_view"|"task"|"release"|"mirror"|"repo"|"webhook_endpoint"|"ci_run_group", subject_id, repo|null, state:ThreadState, muted, reasons[], bumped_at, snippet, unread_items}`; `InboxItem {event_id, type, actor, at, reasons[], title}` (inbox-design §3, §6)
- `SearchHit {path, match position/preview — exact field names follow ruk-search-design §2 as implemented}`; search replies add `scan {bytes, blobs, elapsed_ms, exhausted:bool}` (ScanExhaustion per that design)
- `Token {prefix (12 chars), name, scopes[], repos[], expires_at, created_at, last_used_at|null, revoked_at|null}` (auth-design §8)
- `Session {id_hash_prefix (12 hex), auth_level, authed_at, totp_ok, created_at, last_seen_at, created_ip, user_agent, current}`
- `Job {id, kind, state, attempts, last_error|null, next_run_at|null}`; `AuditEvent {at, actor|null, action, target, ip, result, details}`
- `Page<T> {items:[T], next_cursor:Cursor|null}`

### 1.6 Pagination (keyset)
- `?limit=&cursor=` → `Page<T>` + `Link: <…>; rel="next"`. No totals; HTML shows a "Next"/`?before=` link.
- Store lists: cursor = last row's `(sort key…, id)`. Inbox: opaque `(bumped_at, id)` (inbox-design §6).
- **Search is the exception**: `search.code` passes Ruk `PageCursor`s through verbatim — 30-second interactive leases; an expired lease surfaces `E_LEASE_LOST` (client restarts from a fresh first page), a view advance surfaces `E_FORBIDDEN` (ruk-search-design §3).
- Other Ruk-backed lists never expose Ruk cursors (30-second leases, G7); rukie cursors anchor on immutable data: tree `(commit, path, last_name)`; log `(tip_oid, path, offset)`; blame `(commit, path, next_line)`; diff `(left, right, last_path)`; refs `(last_name)`.

### 1.7 Concurrency values
- `If-Match`: required entity ETag `"<kind>-<id>-v<version>"`; missing → 428, mismatch → 412 with the current ETag.
- `If-Match(oid)`: ref compare-and-swap (Ruk `RefExpect.present`). `If-None-Match:*`: create-only (`RefExpect.absent`, or asset absent).
- `cond-GET`: ETag + `If-None-Match` → 304; immutable when the rev is a full oid.
- `none`: no precondition (converged-state or lease-fenced ops).

### 1.8 Idempotency values
- `key`: `Idempotency-Key` REQUIRED; ≤ 255 printable chars; scope (principal, op); retained 7 days, matching Ruk's 7-day mutation receipts. HTML `_idem` is forwarded as the Ruk `request_id` (nojs-patterns; auth-design). Same key + same body → replay; different body → 422; still running → 409.
- `natural`: repeating converges. `safe`: read.

### 1.9 Errors (RFC 9457) and named sets
- Body: `{type:"https://<host>/-/problems/<slug>", title, status, detail, instance, code:"<slug>", request_id, errors?:[{field,code,detail}], ruk_code?, reason?, retry_after?}`.

| Set | Slugs (HTTP) |
|---|---|
| `R` | `unauthenticated` 401, `forbidden` 403, `scope-insufficient` 403, `not-found` 404 (also for anything invisible), `rate-limited` 429, `overloaded` 503, `backend-unavailable` 503, `internal` 500 |
| `M` | `R` + `invalid-input` 422, `malformed-request` 400 |
| `MK` | `M` + `idempotency-key-required` 400, `idempotency-key-reused` 422, `idempotency-in-progress` 409 |
| `MC` | `M` + `precondition-required` 428, `precondition-failed` 412 |
| `MKC` | `MK` ∪ `MC` |

- Extras: 401 `credentials-invalid` (always the generic "Invalid username or password." class, auth-design §10.2); 403 `session-required`, `locked` (lock windows; `retry_after`); 409 `already-exists` (`field`), `state-conflict` (`reason`), `not-fast-forward`, `conflicted`, `divergent`; 410 `gone`; 413 `payload-too-large`; 415 `unsupported-media-type`; 416 `invalid-range`; 422 `name-reserved`, `ambiguous` (`candidates`); 501 `not-implemented`; 502 `remote-rejected`, `remote-uncertain`.
- **Ruk code → slug**: `E_STALE`→precondition-failed; `E_REQUEST_REUSED`→idempotency-key-reused; `E_CONFLICTED`,`E_UNDO_CONFLICT`→conflicted; `E_DIVERGENT`→divergent; `E_NOT_FOUND`→not-found; `E_INVALID`,`E_OBJECT_FORMAT`→invalid-input; `E_PUSH_REJECTED`,`E_REMOTE_MOVED`→remote-rejected; `E_PUSH_UNCERTAIN`→remote-uncertain; `E_OFFLINE`,`E_COORDINATOR_UNRESPONSIVE`,`E_CANCELLED`→backend-unavailable; `E_CAPACITY`,`E_LIMIT`→overloaded; `E_HOSTED`,`E_UNSUPPORTED_GIT_COMMAND`,`E_UNSUPPORTED_LFS`,`E_SHALLOW`→not-implemented; `E_LEASE_LOST`→restart internally, else `backend-unavailable` (search: client restarts from a fresh first page); `E_FORBIDDEN`,`E_CORRUPT`,`E_IO`,`E_VERSION`,`E_DIRTY`,`E_WORKSPACE_*`→internal; `E_CLAIMED`,`E_POLICY`→PROVISIONAL (tasks/change).

### 1.10 Ruk binding values (`ruk:` field)
The `ruk:` field takes exactly one of:
1. A typed v1 method name — from `/tmp/ruk-model.json` (46 today) or added by the settled `ruk-search-design.md` (`search`) — marked ✓ (hosted today) or ✗ (in the schema/design, not hosted yet). Hosted mutations: `ruk/crates/ruk-core/src/coord/prepare.rs:248-276` (annotated `tag.set` proved by `daemon/tests.rs:841-931`; `ruk-domain-model.md:211` lists it as refused — stale, same doc's http claim is disproven by `verbs_transport.rs:7,9,16`). Hosted reads today: `op.log`, `claim.list`, `attest.list` (`coord/hosted.rs:621-631`); all other reads `hosted.rs:632-640`; `fetch`, `push`, `push.reconcile`, `land.execute` `prepare.rs:277-299`.
2. `store only`: no typed Ruk method is called; where the data lives in Ruk (refs, archives, repo lifecycle) §7 records the missing typed method and §8 the interim command path.
Git-style command execution never appears in the `ruk:` field; it lives only in §8. Ruk addresses repos as `<root>/<repo-id>`; forge sessions run as the service principal, the acting user travels as `identity`/provenance claims `[inference]`.

### 1.11 Request authentication and secret storage (auth-design §§6-10)
- Session cookie `__Host-rukie-session` (Secure; HttpOnly; SameSite=Lax; no Domain); server-side rows; idle 24 h sliding, absolute 30 d; rotation on login/step-up; fixation-proof.
- CSRF: per-session synchronizer token `_csrf` + `Origin`/`Referer` check on every cookie-authenticated mutation; `form-action 'self'`; pre-session anon cookie keeps login CSRF-safe.
- Tokens: `rk_` + 43 base64url chars (46 total); SHA-256 stored; 12-char public prefix; plaintext once at creation. Mandatory expiry (≤ 366 d, default 90 d). Repo restriction `token_repos` (repo ids; empty = all user-accessible repos). Per-token rate 300 req/min sustained, burst 600 → 429 + `Retry-After`.
- Fresh auth (≤ 15 min) required for: password change, TOTP disable, recovery regenerate, token create, SSH key add, OIDC link.
- Lockout: password 5 fails/10 min per username and per IP → 15-min lock doubling to 24 h; TOTP 10 fails kills the pending session; OIDC state abuse 20/10 min → 1 h IP block; git routes 60×401/min → 10-min cooldown; dummy-hash equalizer on unknown users.
- Git Smart HTTP Basic auth: username = rukie username (must equal the token owner), password = full `rk_` token; never a session/argon2/TOTP value. SSH: key fingerprint → user; caller identity injected into Ruk as short-lived ephemeral credentials, never the raw token.
- Mirror credentials (`MirrorAuth.token`, generated SSH private keys): AES-256-GCM sealed at rest under a server key from config/env, decrypted only in memory by the sync worker, never echoed (reads expose `has_secret` and `ssh_public_key` only).
- Password hashing argon2id `m=19456 KiB, t=2, p=1` (PHC string; auth-design §3.1). TOTP: RFC 6238, SHA-1, 6 digits, 30 s, ±1 step, `last_counter` replay guard. Audit: append-only, 365-day retention, `admin:read` lists (auth-design §12).

## 2. Token scopes (auth-design §8.2 — the authoritative list)

| Scope | Grants |
|---|---|
| `repo:read` | browse/fetch private repos, code, refs, code search, releases read, forks, collaborators |
| `repo:write` | `repo:read` + git push (branches and `refs/for/*` per branch policy), branch/tag create/update/delete |
| `tasks:read` / `tasks:write` | tasks incl. claim via Ruk leases (PROVISIONAL ops) |
| `ci:read` / `ci:trigger` | read CI results / queue runs (reserved; CI ideation) |
| `webhooks:read` / `webhooks:write` | webhook endpoints, messages, attempts, test, rotate, redeliver |
| `releases:read` / `releases:write` | releases and assets |
| `mirrors:read` / `mirrors:trigger` | push mirrors, runs, manual sync |
| `admin:read` / `admin:write` | admin ops; minting requires a human with fresh auth |

- Minting: `auth.token.create` is session + fresh-auth only (auth-design §6.1); tokens cannot mint tokens. Revocation by `auth.token.revoke`, `auth.password.change` (opt-in), `admin.user.reset-password` (default).
- Domains with no scope in v1 (profile, orgs/teams, social, repo settings/delete, explore, search-of-users, inbox*) are session ops (§1.2 exception). Flagged consequence of the settled vocabulary, not a silent invention; widening it needs an auth-design change.

## 3. Operations (131 settled)

Row format:
**name** · kind · `METHOD path` · scope · idem · conc · page · html
in: … | out: …
err: … | ruk: …

### 3.1 auth / session (18; op names per auth-design §2)
- **auth.login** · mutation · `POST /api/auth/login` · scope: none · idem: natural (each success mints a fresh session) · conc: none · page: none · html: page+form `/-/login`
  in: `login` Name; `password` Password | out: 200 `{state:"authenticated", user}` + cookie, or 200 `{state:"totp_required", challenge}` (single use, 5 min)
  err: M + credentials-invalid, locked, forbidden(reason inactive/oidc_only, same generic class) | ruk: store only
- **auth.totp.verify** · mutation · `POST /api/auth/totp/verify` · scope: none (challenge-bound) or session (enrollment confirmation) · idem: natural (code/step single-use via `last_counter`) · conc: none (per-user serialization) · page: none · html: form `/-/login/totp`; enroll-confirm form `/-/settings/security/totp`
  in: exactly one of `code` TotpCode \| `recovery_code` RecoveryCode; `challenge?` Challenge (login step) | out: login → 200 `{state:"authenticated", user}` + cookie; enrollment → 200 `{verified:true, recovery_codes:[RecoveryCode ×10]}` (once)
  err: M + credentials-invalid (10 fails kill the pending session) | ruk: store only
- **auth.logout** · mutation · `POST /api/auth/logout` · scope: session · idem: natural · conc: none · page: none · html: form (page header)
  in: `all_devices?` bool=false | out: 204, cookie(s) cleared
  err: M | ruk: store only
- **auth.totp.enroll** · mutation · `POST /api/auth/totp/enroll` · scope: session + fresh auth · idem: natural (replaces a pending secret) · conc: none · page: none · html: page+form `/-/settings/security/totp` (otpauth URI + manual key as copyable text, no QR dependency)
  in: `password` Password | out: `{secret_base32 (20 bytes), otpauth_uri}`
  err: M + credentials-invalid, state-conflict(totp-enabled) | ruk: store only
- **auth.totp.disable** · mutation · `POST /api/auth/totp/disable` · scope: session + fresh auth · idem: natural · conc: none · page: none · html: form `/-/settings/security`
  in: `password` Password; `code` TotpCode | out: 204
  err: M + credentials-invalid, state-conflict(totp-not-enabled) | ruk: store only
- **auth.recovery.regenerate** · mutation · `POST /api/auth/recovery-codes` · scope: session + fresh auth · idem: key · conc: none · page: none · html: form `/-/settings/security`
  in: `password` Password | out: `{recovery_codes:[RecoveryCode ×10]}` (once)
  err: MK + credentials-invalid, state-conflict(totp-not-enabled) | ruk: store only
- **auth.oidc.start** · mutation · `GET /auth/oidc/{id}/start` (GET required for the top-level redirect; `Cache-Control: no-store`) · scope: none (pre-session anon cookie) · idem: natural (fresh state/nonce/PKCE each call; 10-min single-use rows) · conc: none · page: none · html: plain link (auth-design §5.2)
  in: path `id` ProviderSlug; query `return_to?` LocalPath | out: 302 to the OP with `state`(32B) `nonce`(32B) PKCE S256 `code_verifier`(64 chars)
  err: M + forbidden(reason provider-disabled) | ruk: store only
- **auth.oidc.callback** · mutation · `GET /auth/oidc/{id}/callback` · scope: none, or a session (link mode via `auth.oidc.link` intent) · idem: natural (code single-use; state consumed before exchange) · conc: none · page: none · html: browser redirect + confirm interstitial per §5.4
  in: path `id`; query `code` Text(1..2048), `state` Challenge | out: 303 → `return_to`; sign-in (TOTP step-up if enrolled), JIT provision iff `allow_signup`, or link per confirm policy
  err: M + credentials-invalid (bad state/token), already-exists(field name), forbidden(reason signup-disabled) | ruk: store only
- **auth.oidc.link** · mutation · `POST /api/auth/oidc/link` · scope: session + fresh auth · idem: key · conc: none · page: none · html: form `/-/settings/security` (starts a link-mode `auth.oidc.start`)
  in: `provider` ProviderSlug | out: 303 → provider start with link intent
  err: MK + state-conflict(identity-already-linked) | ruk: store only
- **auth.password.change** · mutation · `PUT /api/auth/password` · scope: session + fresh auth · idem: natural · conc: none (current password is the precondition) · page: none · html: form `/-/settings/security`
  in: `current_password` Password; `new_password` Password; `revoke_other_sessions?` bool=true; `revoke_tokens?` bool=false | out: 204
  err: M + credentials-invalid | ruk: store only
- **auth.token.list** · read · `GET /api/user/tokens` · scope: session + fresh auth · idem: safe · conc: cond-GET · page: cursor(created_at desc, prefix) · html: page `/-/settings/tokens`
  in: query `include_revoked?` bool=false, `limit?` Limit, `cursor?` Cursor | out: Page<Token>
  err: R | ruk: store only
- **auth.token.create** · mutation · `POST /api/user/tokens` · scope: session + fresh auth (tokens cannot mint tokens) · idem: key · conc: none · page: none · html: page+form `/-/settings/tokens`
  in: `name` Text(1..100) unique per user; `scopes` [Scope] 1..14 unique from §2 (validated, empty rejected); `repos?` [FullName] 0..64 = [] (resolved to `token_repos` ids; empty = all user-accessible); `expires_at` Timestamp (required; now+1h .. now+366d; default offered 90 d) | out: 201 Token + `secret` (`rk_`+43 chars, once)
  err: MK + already-exists(field name), invalid-input(scope) | ruk: store only
- **auth.token.revoke** · mutation · `POST /api/user/tokens/{prefix}/revoke` · scope: session + fresh auth, or the token itself · idem: natural · conc: none · page: none · html: form `/-/settings/tokens`
  in: path `prefix` (12 chars) | out: 204
  err: M | ruk: store only
- **auth.sshkey.list** · read · `GET /api/user/sshkeys` · scope: session · idem: safe · conc: cond-GET · page: cursor(created_at, id) · html: page `/-/settings/keys` (addition beyond the auth-design §2 op list — the settings page needs a read; flagged)
  in: query `limit?` Limit, `cursor?` Cursor | out: Page<{id, title, algorithm, fingerprint, created_at, last_used_at}>
  err: R | ruk: store only
- **auth.sshkey.add** · mutation · `POST /api/user/sshkeys` · scope: session + fresh auth · idem: key · conc: none · page: none · html: form `/-/settings/keys` (shows the fingerprint for confirmation)
  in: `public_key` SshPublicKey; `title?` Text(0..100) (defaults to `user@host`) | out: 201 {id, fingerprint}
  err: MK + already-exists(field fingerprint, global unique), invalid-input(key-type) | ruk: store only
- **auth.sshkey.remove** · mutation · `DELETE /api/user/sshkeys/{id}` · scope: session + fresh auth · idem: natural · conc: none · page: none · html: form `/-/settings/keys`
  in: path `id` Id | out: 204
  err: M | ruk: store only
- **auth.session.list** · read · `GET /api/auth/sessions` · scope: session · idem: safe · conc: cond-GET · page: cursor(last_seen_at desc, id) · html: page `/-/settings/security`
  in: query `limit?` Limit, `cursor?` Cursor | out: Page<Session>
  err: R | ruk: store only
- **auth.session.revoke** · mutation · `DELETE /api/auth/sessions/{id_hash_prefix}` · scope: session · idem: natural · conc: none · page: none · html: form `/-/settings/security`
  in: path `id_hash_prefix` (12 hex chars) | out: 204
  err: M | ruk: store only
- Not ops in v1 (per auth-design §2): self-signup (accounts come from OIDC `allow_signup` or the admin CLI), `whoami` (open question: agents currently infer identity from 401 problem detail + `auth.token.list`).

### 3.2 users (3)
- **user.get** · read · `GET /api/users/{user}` · scope: none · idem: safe · conc: cond-GET · page: none · html: page `/{owner}`
  in: path `user` Name | out: User (public fields) | err: R | ruk: store only
- **user.self.update** · mutation · `PATCH /api/user` · scope: session · idem: natural · conc: If-Match · page: none · html: form `/-/settings/profile`
  in: `display_name?` Text(0..100); `bio?` Text(0..400); `website?` Url\|""; `location?` Text(0..100); `commit_email?` Email (used only as the IdentityClaim for web-made tags; never for attribution or verification) | out: User (self)
  err: MC | ruk: store only
- **user.self.delete** · mutation · `POST /api/user/delete` · scope: session + fresh auth · idem: natural · conc: If-Match · page: none · html: GET confirm page + form `/-/settings/account`
  in: `password` Password; `confirm` Name (= own name) | out: 202 `{purge_after:Timestamp}` (owned repos soft-deleted; sessions and tokens revoked)
  err: MC + credentials-invalid, state-conflict(last-owner) | ruk: store only

### 3.3 orgs / teams / membership (20; session ops — no token scope in v1, flagged in §2)
Org membership = membership in at least one team; the `owners` team has `admin` on all org repos.
- **org.create** · mutation · `POST /api/orgs` · scope: session · idem: key · conc: none · page: none · html: page+form `/-/new-org`
  in: `name` Name; `display_name?` Text(0..100)=""; `description?` Text(0..400)=""; `website?` Url\|""=""; `visibility?` Visibility=public | out: 201 Org
  err: MK + already-exists(field name), name-reserved | ruk: store only
- **org.get** · read · `GET /api/orgs/{org}` · scope: none (public) / session member (private) · idem: safe · conc: cond-GET · page: none · html: page `/{owner}`
  in: path `org` Name | out: Org | err: R | ruk: store only
- **org.list** · read · `GET /api/user/orgs` · scope: session · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/-/orgs`
  in: query `limit?` Limit, `cursor?` Cursor | out: Page<{org:Org, role:"owner"|"member"}> | err: R | ruk: store only
- **org.update** · mutation · `PATCH /api/orgs/{org}` · scope: session (role owner) · idem: natural · conc: If-Match · page: none · html: form `/{org}/-/settings`
  in: path `org` Name; body `display_name?` Text(0..100), `description?` Text(0..400), `website?` Url\|"", `visibility?` Visibility | out: Org | err: MC | ruk: store only
- **org.rename** · mutation · `POST /api/orgs/{org}/rename` · scope: session (role owner) · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{org}/-/settings`
  in: path `org` Name; body `name` Name | out: Org (old name redirects until reused) | err: MC + already-exists, name-reserved | ruk: store only
- **org.delete** · mutation · `DELETE /api/orgs/{org}` · scope: session (role owner) · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{org}/-/settings`
  in: path `org` Name; query `confirm` Name | out: 204 | err: MC + state-conflict(org-not-empty) | ruk: store only
- **org.member.list** · read · `GET /api/orgs/{org}/members` · scope: none (public org) / session member · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/{org}/-/members`
  in: path `org` Name; query `role?` "owner"\|"member", `limit?` Limit, `cursor?` Cursor | out: Page<{user:UserRef, role, teams:[TeamName]}> | err: R | ruk: store only
- **org.member.remove** · mutation · `DELETE /api/orgs/{org}/members/{user}` · scope: session (role owner) · idem: natural · conc: none · page: none · html: form `/{org}/-/members`
  in: path `org` Name, `user` Name | out: 204 (removed from all teams) | err: M + state-conflict(last-owner) | ruk: store only
- **org.leave** · mutation · `DELETE /api/user/orgs/{org}` · scope: session · idem: natural · conc: none · page: none · html: form `/{org}/-/members`
  in: path `org` Name | out: 204 | err: M + state-conflict(last-owner) | ruk: store only
- **team.list** · read · `GET /api/orgs/{org}/teams` · scope: session member · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/{org}/-/teams`
  in: path `org` Name; query `limit?` Limit, `cursor?` Cursor | out: Page<Team> | err: R | ruk: store only
- **team.create** · mutation · `POST /api/orgs/{org}/teams` · scope: session (role owner) · idem: key · conc: none · page: none · html: form `/{org}/-/teams`
  in: path `org` Name; body `name` TeamName, `description?` Text(0..400)="", `permission` Permission, `repo_scope?` "all"\|"selected"="selected" | out: 201 Team | err: MK + already-exists, name-reserved | ruk: store only
- **team.get** · read · `GET /api/orgs/{org}/teams/{team}` · scope: session member · idem: safe · conc: cond-GET · page: none · html: page `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName | out: Team | err: R | ruk: store only
- **team.update** · mutation · `PATCH /api/orgs/{org}/teams/{team}` · scope: session (role owner) · idem: natural · conc: If-Match · page: none · html: form `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName; body `name?` TeamName, `description?` Text(0..400), `permission?` Permission, `repo_scope?` "all"\|"selected" | out: Team | err: MC + already-exists, state-conflict(owners-team) | ruk: store only
- **team.delete** · mutation · `DELETE /api/orgs/{org}/teams/{team}` · scope: session (role owner) · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName | out: 204 | err: MC + state-conflict(owners-team) | ruk: store only
- **team.member.list** · read · `GET /api/orgs/{org}/teams/{team}/members` · scope: session member · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName; query `limit?` Limit, `cursor?` Cursor | out: Page<UserRef> | err: R | ruk: store only
- **team.member.add** · mutation · `PUT /api/orgs/{org}/teams/{team}/members/{user}` · scope: session (role owner) · idem: natural · conc: none · page: none · html: form `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName, `user` Name | out: 204 (inbox `membership` notice) | err: M | ruk: store only
- **team.member.remove** · mutation · `DELETE /api/orgs/{org}/teams/{team}/members/{user}` · scope: session (role owner) · idem: natural · conc: none · page: none · html: form `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName, `user` Name | out: 204 | err: M + state-conflict(last-owner) | ruk: store only
- **team.repo.list** · read · `GET /api/orgs/{org}/teams/{team}/repos` · scope: session member · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName; query `limit?` Limit, `cursor?` Cursor | out: Page<RepoRef> | err: R | ruk: store only
- **team.repo.add** · mutation · `PUT /api/orgs/{org}/teams/{team}/repos/{repo}` · scope: session (role owner) · idem: natural · conc: none · page: none · html: form `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName, `repo` RepoName | out: 204 | err: M + state-conflict(scope-all) | ruk: store only
- **team.repo.remove** · mutation · `DELETE /api/orgs/{org}/teams/{team}/repos/{repo}` · scope: session (role owner) · idem: natural · conc: none · page: none · html: form `/{org}/-/teams/{team}`
  in: path `org` Name, `team` TeamName, `repo` RepoName | out: 204 | err: M + state-conflict(scope-all) | ruk: store only

### 3.4 repos (12; `repo.fork.list` is defined in §3.10; settings/delete are session ops per §2)
- **repo.create** · mutation · `POST /api/repos` · scope: session (self, or org owner / admin team) · idem: key · conc: none · page: none · html: page+form `/-/new`
  in: `owner` Name; `name` RepoName; `description?` Text(0..2048)=""; `website?` Url\|""=""; `visibility?` Visibility=private; `default_branch?` BranchName="main"; `init_readme?` bool=false | out: 201 Repo + `Location`; emits `repo.created` (forks carry `fork_of`)
  err: MK + already-exists(field name), name-reserved | ruk: `git.commit✓` for the README commit (prepare.rs:258) when `init_readme`; store only otherwise (G5, §8)
- **repo.get** · read · `GET /api/repos/{owner}/{repo}` · scope: none (public) / `repo:read` · idem: safe · conc: cond-GET · page: none · html: page `/{owner}/{repo}` (renders `code.tree.get` at the default-branch root + README)
  in: path `owner` Name, `repo` RepoName | out: Repo | err: R + gone (soft-deleted; repo admins only) | ruk: store only
- **repo.list** · read · `GET /api/repos` · scope: none / `repo:read` (private rows) · idem: safe · conc: cond-GET · page: cursor(sort key, id) · html: page `/{owner}` (Repositories tab)
  in: query `owner?` Name, `visibility?` Visibility, `sort?` "updated"\|"created"\|"name"="updated", `limit?` Limit, `cursor?` Cursor | out: Page<Repo> | err: R | ruk: store only
- **repo.update** · mutation · `PATCH /api/repos/{owner}/{repo}` · scope: session (role admin) · idem: natural · conc: If-Match · page: none · html: form `/{owner}/{repo}/settings`
  in: path `owner` Name, `repo` RepoName; body `description?` Text(0..2048), `website?` Url\|"", `topics?` [Topic] 0..20 unique, `default_branch?` BranchName (must exist), `archived?` bool, `features?` {`releases?` bool} | out: Repo
  err: MC + not-found(field default_branch) | ruk: store only (HEAD symref: G5, §8)
- **repo.visibility.set** · mutation · `PUT /api/repos/{owner}/{repo}/visibility` · scope: session (role admin) · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{owner}/{repo}/settings`
  in: path `owner` Name, `repo` RepoName; body `visibility` Visibility, `confirm` FullName | out: Repo (no webhook event in v1)
  err: MC + state-conflict(reason private-source) | ruk: store only

[Showing lines 3683-4094 of 4498 (49.8KB limit). Use :4095 to continue]
  err: MC + state-conflict(reason private-source) | ruk: store only
- **repo.rename** · mutation · `POST /api/repos/{owner}/{repo}/rename` · scope: session (role admin; new owner also needs create rights) · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{owner}/{repo}/settings`
  in: path `owner` Name, `repo` RepoName; body `name` RepoName, `new_owner?` Name=current | out: Repo (old path redirects until reused; no webhook event in v1)
  err: MC + already-exists(field name), name-reserved | ruk: store only
- **repo.delete** · mutation · `DELETE /api/repos/{owner}/{repo}` · scope: session (role admin; org repos: owner) · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{owner}/{repo}/settings`
  in: path `owner` Name, `repo` RepoName; query `confirm` FullName | out: 202 `{state:"deleted", purge_after:Timestamp}`; the `repo.deleted` tombstone message is inserted in the same transaction (webhook-design §6.4)
  err: MC | ruk: store only (purge: G5, §8)
- **repo.restore** · mutation · `POST /api/repos/{owner}/{repo}/restore` · scope: session (role admin) · idem: natural · conc: If-Match · page: none · html: form `/{owner}/{repo}/settings` (deleted banner)
  in: path `owner` Name, `repo` RepoName | out: Repo | err: MC + state-conflict(not-deleted), gone | ruk: store only
- **repo.fork.create** · mutation · `POST /api/repos/{owner}/{repo}/forks` · scope: session with `repo:read` on the source · idem: key · conc: none · page: none · html: page+form `/{owner}/{repo}/fork`
  in: path `owner` Name, `repo` RepoName; body `target_owner?` Name=caller, `name?` RepoName=source name, `default_branch_only?` bool=false | out: 202 Repo `{state:"forking"}` + `Location`; emits `repo.created` with `fork_of`
  err: MK + already-exists(field name), name-reserved | ruk: store only (G5, §8)
- **repo.collaborator.list** · read · `GET /api/repos/{owner}/{repo}/collaborators` · scope: `repo:read` (role read) · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/{owner}/{repo}/settings/access`
  in: path `owner` Name, `repo` RepoName; query `limit?` Limit, `cursor?` Cursor | out: Page<{user:UserRef, permission, via:"direct"|"team:<name>"|"owner"}> | err: R | ruk: store only
- **repo.collaborator.add** · mutation · `PUT /api/repos/{owner}/{repo}/collaborators/{user}` · scope: session (role admin) · idem: natural · conc: none · page: none · html: form `/{owner}/{repo}/settings/access`
  in: path `owner` Name, `repo` RepoName, `user` Name; body `permission` Permission | out: 204 (S10 inbox notice) | err: M + state-conflict(user-is-owner) | ruk: store only
- **repo.collaborator.remove** · mutation · `DELETE /api/repos/{owner}/{repo}/collaborators/{user}` · scope: session (role admin) · idem: natural · conc: none · page: none · html: form `/{owner}/{repo}/settings/access`
  in: path `owner` Name, `repo` RepoName, `user` Name | out: 204 (S10 notice) | err: M | ruk: store only

### 3.5 code browsing (10)
- **code.rev.resolve** · read · `GET /api/repos/{o}/{r}/revs/{rev}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: none · html: none
  in: path `o` Name, `r` RepoName, `rev` Rev\|AbbrevOid | out: `{rev, kind:"commit"|"branch"|"tag"|"change", ref|null, commit:Oid, tag_object:Oid|null}`
  err: R + ambiguous(candidates) | ruk: store only (§8)
- **code.tree.get** · read · `GET /api/repos/{o}/{r}/tree/{rev}/{path...}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(commit, path, last_name) · html: page `/{o}/{r}/tree/{rev}/{path...}` (per nojs: `<table>` + breadcrumbs; empty-dir and bad-path states)
  in: path `o` Name, `r` RepoName, `rev` Rev, `path?` Path=""; query `with_last_commit?` bool=false, `limit?` Limit, `cursor?` Cursor | out: `{commit, path, entries:Page<TreeEntry>}`
  err: R + state-conflict(not-a-directory) | ruk: `file✗` (last-commit: G3, §8)
- **code.blob.get** · read · `GET /api/repos/{o}/{r}/blob/{rev}/{path...}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: none · html: page `/{o}/{r}/blob/{rev}/{path...}` (`?plain=1`, `?full=1` for oversized text; binary → size + download link)
  in: path `o` Name, `r` RepoName, `rev` Rev, `path` Path | out: Blob | err: R + state-conflict(is-directory) | ruk: `file✗`
- **code.raw.get** · read · `GET /api/repos/{o}/{r}/raw/{rev}/{path...}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET (+ single `Range`) · page: none · html: link `/{o}/{r}/raw/{rev}/{path...}` (`?raw=1` + `download`)
  in: path `o` Name, `r` RepoName, `rev` Rev, `path` Path; header `Range?` single `bytes=` range | out: bytes; `text/plain; charset=utf-8` or `application/octet-stream`; `CSP: sandbox`; `nosniff`
  err: R + invalid-range, payload-too-large (G6 buffer cap) | ruk: `file✗`
- **code.blame.get** · read · `GET /api/repos/{o}/{r}/blame/{rev}/{path...}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(commit, path, next_line) · html: page `/{o}/{r}/blame/{rev}/{path...}`
  in: path `o` Name, `r` RepoName, `rev` Rev, `path` Path; query `start_line?` int ≥ 1, `end_line?` int ≥ start_line, `limit?` Limit, `cursor?` Cursor | out: `{commit, path, hunks:Page<BlameHunk>}`
  err: R + state-conflict(reason binary) | ruk: `blame✗`
- **code.log.list** · read · `GET /api/repos/{o}/{r}/commits/{rev}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(tip_oid, path, offset) · html: page `/{o}/{r}/commits/{rev}`
  in: path `o` Name, `r` RepoName, `rev` Rev; query `path?` Path, `first_parent?` bool=false, `limit?` Limit, `cursor?` Cursor | out: Page<CommitSummary>
  err: R | ruk: `log✗` (revset `ancestors(<oid>) & files("<path>")`, revset/syntax.rs:1113,1257)
- **code.commit.get** · read · `GET /api/repos/{o}/{r}/commit/{rev}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET (immutable) · page: none · html: page `/{o}/{r}/commit/{oid}` (shows attestations incl. CI results)
  in: path `o` Name, `r` RepoName, `rev` Rev | out: Commit | err: R | ruk: `show✗` + `attest.list✓` (subject = oid)
- **code.diff.get** · read · `GET /api/repos/{o}/{r}/diff/{left}..{right}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(left, right, last_path) · html: page `/{o}/{r}/diff/{left}..{right}` (unified/split `?diff=` link pair per nojs)
  in: path `o` Name, `r` RepoName, `left` Rev, `right` Rev; query `path?` [Path] 0..32, `context?` int 0..20=3, `renames?` bool=true, `limit?` Limit, `cursor?` Cursor | out: `{left:Oid, right:Oid, files:Page<FileDiff>}`
  err: R | ruk: `diff✗`
- **code.compare.get** · read · `GET /api/repos/{o}/{r}/compare/{base}...{head}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(base, head, last_path); commits capped at 250 · html: page `/{o}/{r}/compare/{base}...{head}`
  in: path `o` Name, `r` RepoName, `base` Rev, `head` Rev; query `limit?` Limit, `cursor?` Cursor | out: `{base, head, merge_base:Oid|null, ahead, behind, commits:[CommitSummary], commits_truncated, files:Page<FileDiff>}`
  err: R | ruk: `query✗` (`heads(ancestors(b) & ancestors(h))`) + `log✗` + `diff✗`
- **code.archive.get** · read · `GET /api/repos/{o}/{r}/archive/{rev}.{format}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET (ETag `<commit>:<format>`) · page: none · html: link on repo, tree, tag and release pages
  in: path `o` Name, `r` RepoName, `rev` Rev, `format` "tar.gz"\|"zip" | out: bytes, `Content-Disposition: attachment`, prefix `<repo>-<shortoid>/`; cached per (tree oid, format)
  err: R + payload-too-large | ruk: store only (G4, §8)

### 3.6 refs: branches and tags (9)
- **branch.list** · read · `GET /api/repos/{o}/{r}/branches` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(last_name) · html: page `/{o}/{r}/branches`
  in: path `o` Name, `r` RepoName; query `prefix?` Text(1..255), `limit?` Limit, `cursor?` Cursor | out: Page<Branch> | err: R | ruk: store only (G3, §8)
- **branch.get** · read · `GET /api/repos/{o}/{r}/branches/{branch}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET (ETag = target oid) · page: none · html: page `/{o}/{r}/tree/{branch}`
  in: path `o` Name, `r` RepoName, `branch` BranchName | out: Branch | err: R | ruk: store only (G3, §8)
- **branch.create** · mutation · `POST /api/repos/{o}/{r}/branches` · scope: `repo:write` · idem: key (+ CAS on absence) · conc: If-None-Match:* (implicit) · page: none · html: form `/{o}/{r}/branches`
  in: path `o` Name, `r` RepoName; body `name` BranchName, `from` Rev | out: 201 Branch + ETag (fires `ref.updated`)
  err: MK + already-exists(field name), conflicted, state-conflict(archived) | ruk: `bookmark.set✓` `{expected:"absent"}` (prepare.rs:249)
- **branch.update** · mutation · `PATCH /api/repos/{o}/{r}/branches/{branch}` · scope: `repo:write` · idem: natural (CAS) · conc: If-Match(oid) · page: none · html: form `/{o}/{r}/branches`
  in: path `o` Name, `r` RepoName, `branch` BranchName; body `target` Rev, `force?` bool=false | out: Branch (fires `ref.updated` with `change_kind: force_updated` when forced)
  err: MC + not-fast-forward, conflicted, state-conflict(archived) | ruk: `bookmark.set✓` `{expected:{present:old}}` (ancestry: §8)
- **branch.delete** · mutation · `DELETE /api/repos/{o}/{r}/branches/{branch}` · scope: `repo:write` · idem: natural · conc: If-Match(oid) · page: none · html: GET confirm + form `/{o}/{r}/branches`
  in: path `o` Name, `r` RepoName, `branch` BranchName | out: 204 (fires `ref.updated`, zero new_oid)
  err: MC + state-conflict(default-branch, archived) | ruk: `bookmark.delete✓` (prepare.rs:250)
- **tag.list** · read · `GET /api/repos/{o}/{r}/tags` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(last_name) · html: page `/{o}/{r}/tags`
  in: path `o` Name, `r` RepoName; query `prefix?` Text(1..255), `limit?` Limit, `cursor?` Cursor | out: Page<Tag> | err: R | ruk: store only (G3, §8)
- **tag.get** · read · `GET /api/repos/{o}/{r}/tags/{tag}` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET (ETag = ref target) · page: none · html: page `/{o}/{r}/tree/tags:{tag}`
  in: path `o` Name, `r` RepoName, `tag` TagName | out: Tag | err: R | ruk: `show✗` (annotation; lookup §8)
- **tag.create** · mutation · `POST /api/repos/{o}/{r}/tags` · scope: `repo:write` · idem: key (+ CAS on absence) · conc: If-None-Match:* (implicit) · page: none · html: form `/{o}/{r}/tags`
  in: path `o` Name, `r` RepoName; body `name` TagName, `target` Rev, `message?` Text(1..65536) (present → annotated; tagger = display name + `commit_email`) | out: 201 Tag (fires `ref.updated`; annotated ref points at the tag object)
  err: MK + already-exists(field name), state-conflict(archived) | ruk: `tag.set✓` `{expected:"absent", annotation?}` (prepare.rs:252; end-to-end proof `daemon/tests.rs:841-931`; `ruk-domain-model.md:211` claims refusal — stale, flagged in §7)
- **tag.delete** · mutation · `DELETE /api/repos/{o}/{r}/tags/{tag}` · scope: `repo:write` · idem: natural · conc: If-Match(oid) · page: none · html: GET confirm + form `/{o}/{r}/tags`
  in: path `o` Name, `r` RepoName, `tag` TagName | out: 204 (fires `ref.updated`, zero new_oid)
  err: MC + state-conflict(tag-in-use, archived) | ruk: `tag.delete✓` (prepare.rs:253)

### 3.7 search (3)
- **search.code** · read · `GET /api/search/code` · scope: none (public) / `repo:read` (adds private repos) · idem: safe · conc: none (`no-store`; live scan) · page: Ruk `PageCursor` passed through verbatim (30 s lease; expired → restart from first page) · html: page `/-/search?type=code` (GET form), `/{o}/{r}/search`
  in: query `repo?` FullName; `owner?` Name (exclusive with repo; neither = all accessible repos at their default branch); `rev?` Rev (only with repo; default = default branch); `pattern` SearchPattern; `case_insensitive?` bool=false; `paths?` [Path] 0..32 (Ruk `PathSelection` prefix filter); `globs?` [Glob] 0..32; `exclude_globs?` [Glob] 0..32 (≤ 32 globs total); `budget?` SearchBudget; `limit?` int 1..1000=50; `cursor?` Ruk PageCursor | out: per repo: `{repo:RepoRef, commit:Oid, hits:Page<SearchHit>, scan:{bytes, blobs, elapsed_ms, exhausted}}`; multi-repo requests merge per-repo pages `[inference: fan-out merge order]` with `searched_repos:int`. Binary blobs and blobs > 512 KiB are skipped; hits dedupe across fan-out paths; over-budget pages seal at the scan frontier and resume via cursor (ruk-search-design §2, §6)
  err: R + invalid-input(empty literal, bad regex, over-cap budget), rate-limited | ruk: `search✗` (method added by ruk-search-design.md §1: `SearchArgs/SearchPattern/SearchBudget/SearchHit/ScanExhaustion`, a `method_table!` row, and a hosted `read()` `Request::Search` arm with a core-owned `SearchPool`; not yet in `/tmp/ruk-model.json`)
- **search.repos** · read · `GET /api/search/repos` · scope: none / `repo:read` · idem: safe · conc: cond-GET · page: cursor(sort key, id) · html: page `/-/search?type=repos`
  in: query `q` Text(1..128), `topic?` Topic, `sort?` "stars"\|"updated"\|"name"="stars", `limit?` Limit, `cursor?` Cursor | out: Page<Repo> | err: R | ruk: store only
- **search.users** · read · `GET /api/search/users` · scope: none · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/-/search?type=users`
  in: query `q` Text(1..64), `kind?` "user"\|"org"\|"all"="all", `limit?` Limit, `cursor?` Cursor | out: Page<UserRef> | err: R | ruk: store only

### 3.8 releases, keyed by tag (9)
- **release.list** · read · `GET /api/repos/{o}/{r}/releases` · scope: none (public) / `releases:read` (private; drafts need role write) · idem: safe · conc: cond-GET · page: cursor(created_at desc, tag) · html: page `/{o}/{r}/releases`
  in: path `o` Name, `r` RepoName; query `include_drafts?` bool=false, `limit?` Limit, `cursor?` Cursor | out: Page<Release> | err: R | ruk: store only
- **release.get** · read · `GET /api/repos/{o}/{r}/releases/{tag}` · scope: none (public) / `releases:read` (private) · idem: safe · conc: cond-GET · page: none · html: page `/{o}/{r}/releases/{tag}`
  in: path `o` Name, `r` RepoName, `tag` TagName | out: Release | err: R | ruk: store only (+ `attest.list✓` for the tag commit)
- **release.create** · mutation · `POST /api/repos/{o}/{r}/releases` · scope: `releases:write` · idem: key · conc: none · page: none · html: page+form `/{o}/{r}/releases/new`
  in: path `o` Name, `r` RepoName; body `tag` TagName, `target?` Rev (required iff the tag is absent), `title` Text(1..200), `notes?` Markdown="", `draft?` bool=false, `prerelease?` bool=false | out: 201 Release (publishing fires `release.published`)
  err: MK + already-exists(field tag) | ruk: store only + `tag.set✓` when the tag is absent and draft=false
- **release.update** · mutation · `PATCH /api/repos/{o}/{r}/releases/{tag}` · scope: `releases:write` · idem: natural · conc: If-Match · page: none · html: page+form `/{o}/{r}/releases/{tag}/edit`
  in: path `o` Name, `r` RepoName, `tag` TagName; body `title?` Text(1..200), `notes?` Markdown, `prerelease?` bool, `draft?` bool, `new_tag?` TagName (draft only), `target?` Rev (draft only) | out: Release (publishing fires `release.published`; no update event in v1)
  err: MC + state-conflict(reason published-tag-fixed) | ruk: store only + `tag.set✓` on publish when the tag is absent
- **release.delete** · mutation · `DELETE /api/repos/{o}/{r}/releases/{tag}` · scope: `releases:write` · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{o}/{r}/releases/{tag}/edit`
  in: path `o` Name, `r` RepoName, `tag` TagName; query `delete_tag?` bool=false | out: 204 (no webhook event in v1)
  err: MC | ruk: store only + `tag.delete✓` when `delete_tag`
- **release.asset.list** · read · `GET /api/repos/{o}/{r}/releases/{tag}/assets` · scope: none (public) / `releases:read` (private) · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/{o}/{r}/releases/{tag}`
  in: path `o` Name, `r` RepoName, `tag` TagName; query `limit?` Limit, `cursor?` Cursor | out: Page<Asset> | err: R | ruk: store only
- **release.asset.create** · mutation · `PUT /api/repos/{o}/{r}/releases/{tag}/assets/{name}` · scope: `releases:write` · idem: natural (same bytes → same sha256) · conc: If-None-Match:* (create) or If-Match (replace), one required · page: none · html: form (multipart) `/{o}/{r}/releases/{tag}/edit`
  in: path `o` Name, `r` RepoName, `tag` TagName, `name` AssetName; header `Content-Type` MediaType, `Content-Length` int ≤ `asset_max_bytes`, `Repr-Digest?` `sha-256=:<base64>:` (RFC 9530); body bytes | out: 201/200 Asset
  err: MC + payload-too-large, unsupported-media-type, invalid-input(digest mismatch) | ruk: store only
- **release.asset.get** · read · `GET /api/repos/{o}/{r}/releases/{tag}/assets/{name}` · scope: none (public) / `releases:read` (private) · idem: safe · conc: cond-GET (ETag = sha256) + `Range` · page: none · html: link `/{o}/{r}/releases/{tag}/assets/{name}`
  in: path `o` Name, `r` RepoName, `tag` TagName, `name` AssetName; header `Range?` single `bytes=` range | out: bytes, `Content-Disposition: attachment`, `Repr-Digest` | err: R + invalid-range | ruk: store only
- **release.asset.delete** · mutation · `DELETE /api/repos/{o}/{r}/releases/{tag}/assets/{name}` · scope: `releases:write` · idem: natural · conc: If-Match · page: none · html: form `/{o}/{r}/releases/{tag}/edit`
  in: path `o` Name, `r` RepoName, `tag` TagName, `name` AssetName | out: 204 | err: MC | ruk: store only

### 3.9 webhooks (11; op names and semantics per webhook-design §5-7)
Headers per [Standard Webhooks]: `webhook-id` (`msg_<ULID>`), `webhook-timestamp`, `webhook-signature: v1,<base64 HMAC-SHA256(id.timestamp.payload)>`; `whsec_` secrets; `{type, timestamp, data}` body; both current and previous secret sign during rotation. Transactional outbox: fan-out rows insert in the mutation's transaction; workers claim with 5-min leases; per-endpoint `concurrency`; jittered backoff 5 s/5 m/30 m/2 h/5 h/10 h/14 h/20 h/24 h, `max_attempts=10` → `dead`; `Retry-After` overrides; zero redirects; SSRF policy per §8 of that doc.
- **webhook.endpoint.create** · mutation · `POST /api/webhooks/endpoints` · scope: `webhooks:write` (repo endpoints need repo admin; owner endpoints owner admin) · idem: key · conc: none · page: none · html: form `/{o}/{r}/settings/hooks` and `/{org}/-/settings/hooks`
  in: `scope` "repo"\|"owner"; `repo?` FullName (iff repo) \| `owner?` Name (iff owner); `url` Url; `event_types` [EventType] 1..15 (expanded at save; no wildcards); `ref_globs?` [RefGlob] 0..32 (only with `ref.updated`); `active?` bool=true; `concurrency?` int 1..16=4; `timeout_seconds?` int 5..60=20; `secret?` Secret (absent → generated); `note?` Text(0..256) | out: 201 Endpoint + `secret` (once)
  err: MK + invalid-input(url, event, globs, ssrf) | ruk: store only
- **webhook.endpoint.list** · read · `GET /api/webhooks/endpoints` · scope: `webhooks:read` · idem: safe · conc: cond-GET · page: cursor(created_at, id) · html: page `/{o}/{r}/settings/hooks`, `/{org}/-/settings/hooks`
  in: query exactly one of `repo` FullName \| `owner` Name; `limit?` Limit, `cursor?` Cursor | out: Page<Endpoint> | err: R | ruk: store only
- **webhook.endpoint.get** · read · `GET /api/webhooks/endpoints/{id}` · scope: `webhooks:read` · idem: safe · conc: cond-GET · page: none · html: page `…/settings/hooks/{id}`
  in: path `id` Id | out: Endpoint | err: R | ruk: store only
- **webhook.endpoint.update** · mutation · `PATCH /api/webhooks/endpoints/{id}` · scope: `webhooks:write` · idem: natural · conc: If-Match · page: none · html: form `…/settings/hooks/{id}`
  in: path `id` Id; body `url?` Url, `event_types?` [EventType] 1..15, `ref_globs?` [RefGlob] 0..32, `active?` bool (true clears `disabled` and resets counters), `concurrency?` int 1..16, `timeout_seconds?` int 5..60, `note?` Text(0..256) | out: Endpoint
  err: MC + invalid-input | ruk: store only
- **webhook.endpoint.delete** · mutation · `DELETE /api/webhooks/endpoints/{id}` · scope: `webhooks:write` · idem: natural · conc: If-Match · page: none · html: GET confirm + form `…/settings/hooks/{id}`
  in: path `id` Id | out: 204 (pending messages → `dead`/`undeliverable-endpoint-removed`) | err: MC | ruk: store only
- **webhook.endpoint.rotate-secret** · mutation · `POST /api/webhooks/endpoints/{id}/rotate-secret` · scope: `webhooks:write` · idem: key · conc: If-Match · page: none · html: form `…/settings/hooks/{id}`
  in: path `id` Id; body `overlap?` Duration 0..7d=24h | out: `{secret (once), prev_secret_expires_at}` | err: MKC | ruk: store only
- **webhook.endpoint.test** · mutation · `POST /api/webhooks/endpoints/{id}/test` · scope: `webhooks:write` · idem: key · conc: none · page: none · html: form `…/settings/hooks/{id}`
  in: path `id` Id | out: 202 (sends a synthetic `endpoint.tested` with a throwaway `webhook-id`, outside the outbox) | err: MK + state-conflict(hook-inactive) | ruk: store only
- **webhook.message.list** · read · `GET /api/webhooks/endpoints/{id}/messages` · scope: `webhooks:read` · idem: safe · conc: cond-GET · page: cursor(created_at desc, id) · html: page `…/settings/hooks/{id}` (recent deliveries)
  in: path `id` Id; query `state?` "pending"\|"inflight"\|"delivered"\|"dead", `event_type?` EventType, `limit?` Limit, `cursor?` Cursor | out: Page<Message> | err: R | ruk: store only
- **webhook.message.get** · read · `GET /api/webhooks/endpoints/{id}/messages/{mid}` · scope: `webhooks:read` · idem: safe · conc: cond-GET · page: none · html: page `…/settings/hooks/{id}/messages/{mid}`
  in: path `id` Id, `mid` `msg_<ULID>` | out: Message + payload bytes | err: R + gone (past retention) | ruk: store only
- **webhook.message.redeliver** · mutation · `POST /api/webhooks/endpoints/{id}/messages/{mid}/redeliver` · scope: `webhooks:write` · idem: key · conc: none · page: none · html: form on the message page
  in: path `id` Id, `mid` | out: 202 (resets to `pending`, `attempt_count=0`, same `webhook-id` and payload; `redelivery_of` set only when re-driving a delivered message)
  err: MK + state-conflict(hook-inactive) | ruk: store only
- **webhook.attempt.list** · read · `GET /api/webhooks/messages/{mid}/attempts` · scope: `webhooks:read` · idem: safe · conc: cond-GET · page: cursor(attempt_no desc) · html: page = message page
  in: path `mid`; query `limit?` Limit, `cursor?` Cursor | out: Page<Attempt> (≤ 10 retained) | err: R | ruk: store only
- Auto-disable rules (not ops): receiver `410` → message `dead` + endpoint disabled (`410-gone`) + admin inbox notice; `consecutive_failures ≥ 50` → `failing` + notice; `≥ 200` → disabled (`undeliverable`) + notice. Retention: delivered/dead 30 days (configurable 7-90).

### 3.10 social: stars, forks, explore (7; watch lives in §3.11 inbox)
No webhook events for any of these in v1 (webhook-design §4). Scope: session (no social scope in the settled vocabulary — flagged in §2).
- **star.add** · mutation · `PUT /api/user/stars/{o}/{r}` · scope: session · idem: natural · conc: none · page: none · html: form (repo header)
  in: path `o` Name, `r` RepoName | out: 204 | err: M | ruk: store only
- **star.remove** · mutation · `DELETE /api/user/stars/{o}/{r}` · scope: session · idem: natural · conc: none · page: none · html: form (repo header)
  in: path `o` Name, `r` RepoName | out: 204 | err: M | ruk: store only
- **star.list** · read · `GET /api/stars` · scope: none (public) / `repo:read` (private repo) · idem: safe · conc: cond-GET · page: cursor(starred_at desc, id) · html: page `/{user}?tab=stars`, `/{o}/{r}/stargazers`
  in: query exactly one of `user` Name \| `repo` FullName; `limit?` Limit, `cursor?` Cursor | out: Page<{user:UserRef, repo:RepoRef, starred_at:Timestamp}> | err: R | ruk: store only
- **repo.fork.list** · read · `GET /api/repos/{o}/{r}/forks` · scope: none (public) / `repo:read` (private) · idem: safe · conc: cond-GET · page: cursor(created_at, id) · html: page `/{o}/{r}/forks`
  in: path `o` Name, `r` RepoName; query `limit?` Limit, `cursor?` Cursor | out: Page<Repo> (visible forks only) | err: R | ruk: store only
- **explore.repos** · read · `GET /api/explore/repos` · scope: none · idem: safe · conc: cond-GET · page: cursor(sort key, id) · html: page `/-/explore/repos` (also `/` signed out)
  in: query `sort?` "stars"\|"updated"\|"created"\|"name"="updated", `topic?` Topic, `limit?` Limit, `cursor?` Cursor | out: Page<Repo> (public only) | err: R | ruk: store only
- **explore.users** · read · `GET /api/explore/users` · scope: none · idem: safe · conc: cond-GET · page: cursor(sort key, id) · html: page `/-/explore/users`
  in: query `sort?` "name"\|"created"="name", `limit?` Limit, `cursor?` Cursor | out: Page<UserRef> | err: R | ruk: store only
- **explore.orgs** · read · `GET /api/explore/orgs` · scope: none · idem: safe · conc: cond-GET · page: cursor(sort key, id) · html: page `/-/explore/orgs`
  in: query `sort?` "name"\|"created"="name", `limit?` Limit, `cursor?` Cursor | out: Page<Org> (public only) | err: R | ruk: store only

### 3.11 inbox (8; ops, states, watch levels per inbox-design §1.1, §4, §6)
Thread model: one thread per `(user, subject)`; states `unread/read/done/snoozed` + `muted`; high-priority reasons (`mentioned, review_requested, assigned, ci_failed, mirror_failed, claim_expiring/expired, recovered`) re-open `done` and break snooze; API reads never auto-mark. Rates: `list` 1 qps/token burst 5; `count` 2 qps; `mark/snooze` 5 qps → 429 + `Retry-After`.
- **inbox.list** · read · `GET /api/inbox` · scope: session (no inbox scope in v1 — flagged) · idem: safe · conc: cond-GET · page: cursor(bumped_at, id); `limit?` ≤ 100 = 50 · html: page `/inbox` (filter bar as GET links)
  in: query `state?` "unread"\|"read"\|"done"\|"snoozed"\|"all"="unread", `subject_kind?`, `repo?` FullName, `reason?` InboxReason, `limit?`, `cursor?` | out: `{threads:Page<Thread>, unread_count:int}`
  err: R + rate-limited | ruk: store only
- **inbox.get** · read · `GET /api/inbox/thread` · scope: session · idem: safe · conc: cond-GET · page: none (items bounded by thread history) · html: page `/inbox/thread?id=` (HTML view auto-marks read; API does not)
  in: query `id` thread Id | out: `{thread, items:[InboxItem]}` | err: R | ruk: store only
- **inbox.mark** · mutation · `POST /api/inbox/mark` · scope: session · idem: natural (per-thread idempotent) · conc: none (last writer wins) · page: none · html: per-thread + bulk forms `/inbox`
  in: `thread_ids` [Id] 1..200; `op` "mark_read"\|"mark_done"\|"mark_unread"\|"mute"\|"unmute"; `snooze_until?` Timestamp (required for snooze semantics) | out: `{updated:[ids], failed:[{id, error}]}` (atomic per thread)
  err: M + rate-limited | ruk: store only
- **inbox.snooze** · mutation · `POST /api/inbox/snooze` · scope: session · idem: natural · conc: none · page: none · html: form `/inbox` (15m/1h/1d/1w presets)
  in: `thread_ids` [Id] 1..200; `until` Timestamp (1 m..90 d future) | out: `{updated:[ids], failed:[{id, error}]}`
  err: M + invalid-input(until) | ruk: store only
- **inbox.count** · read · `GET /api/inbox/count` · scope: session · idem: safe · conc: cond-GET (weak ETag) · page: none · html: header badge (server-rendered on every page)
  in: no fields | out: `{unread:int, snoozed_awake_pending:int}` | err: R + rate-limited | ruk: store only
- **inbox.watch.set** · mutation · `POST /api/inbox/watch` · scope: session · idem: natural · conc: none · page: none · html: form `/inbox/settings` (per-repo radio rows) and repo-header form
  in: `repo` FullName; `level` WatchLevel; `custom_bits?` {changes?, tasks?, releases?, ci?} (iff custom); `confirm?` bool (required when `level=ignore`) | out: `{repo, level, custom_bits}`
  err: M + invalid-input(missing confirm) | ruk: store only
- **inbox.watch.list** · read · `GET /api/inbox/watch` · scope: session · idem: safe · conc: cond-GET · page: none (bounded by watched repos; cursor optional) · html: page `/inbox/settings`
  in: query `limit?` Limit, `cursor?` Cursor | out: `{watches:[{repo, level, custom_bits}], default_level:WatchLevel}` | err: R | ruk: store only
- **inbox.settings** · mutation · `POST /api/inbox/settings` · scope: session · idem: natural · conc: none · page: none · html: form `/inbox/settings`
  in: `default_level?` WatchLevel; `claim_warn_window?` Duration 5m..24h=1h | out: settings echo | err: M | ruk: store only

### 3.12 mirrors (8; op names are this draft's — no settled mirror op list was found; modes, triggers, refspecs, error classes and the `mirror.sync_failed` event follow the settled designs)
Sync mechanics (research §5 + webhook-design): explicit refspecs `+refs/heads/<glob>:refs/heads/<glob>` (or all heads) and `+refs/tags/*`; deletions computed from an `ls-remote` diff and applied only to refs rukie itself previously pushed (recorded); never auto-delete the target's default branch; per-ref results, not `--atomic`; debounce ≥ 10 s per target; per-target push rate kept under GitHub's 6/min; `interval` 0 allowed (sync-on-commit only); repeated 401/403 → `needs_attention` (no hammering); credentials scrubbed from all stored errors. Triggers: ref update (debounced), periodic reconcile, config change, manual sync.
- **mirror.list** · read · `GET /api/repos/{o}/{r}/mirrors` · scope: `mirrors:read` (role admin) · idem: safe · conc: cond-GET · page: cursor(created_at, id) · html: page `/{o}/{r}/settings/mirrors`
  in: path `o` Name, `r` RepoName; query `limit?` Limit, `cursor?` Cursor | out: Page<Mirror> | err: R | ruk: store only
- **mirror.create** · mutation · `POST /api/repos/{o}/{r}/mirrors` · scope: `mirrors:trigger` (role admin) · idem: key · conc: none · page: none · html: form `/{o}/{r}/settings/mirrors`
  in: path `o` Name, `r` RepoName; body `url` RemoteUrl; `auth` MirrorAuth; `mode?` "exact"\|"fast_forward_only"="fast_forward_only"; `refs?` MirrorRefs; `interval?` Duration 0..24h=8h (0 = sync-on-commit only); `sync_on_commit?` bool=true | out: 201 Mirror (+ `ssh_public_key` when ssh_key; first run queued)
  err: MK + invalid-input(url, dumb-http push target) | ruk: store only
- **mirror.get** · read · `GET /api/repos/{o}/{r}/mirrors/{id}` · scope: `mirrors:read` · idem: safe · conc: cond-GET · page: none · html: page `/{o}/{r}/settings/mirrors/{id}`
  in: path `o` Name, `r` RepoName, `id` Id | out: Mirror (with `status`) | err: R | ruk: store only
- **mirror.update** · mutation · `PATCH /api/repos/{o}/{r}/mirrors/{id}` · scope: `mirrors:trigger` (role admin) · idem: natural · conc: If-Match · page: none · html: form `/{o}/{r}/settings/mirrors/{id}`
  in: path `o` Name, `r` RepoName, `id` Id; body `url?` RemoteUrl, `auth?` MirrorAuth (omitted `token` keeps the sealed one), `mode?`, `refs?` MirrorRefs, `interval?` Duration 0..24h, `sync_on_commit?` bool, `enabled?` bool | out: Mirror (reconcile queued)
  err: MC + invalid-input | ruk: store only
- **mirror.delete** · mutation · `DELETE /api/repos/{o}/{r}/mirrors/{id}` · scope: `mirrors:trigger` (role admin) · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{o}/{r}/settings/mirrors/{id}`
  in: path `o` Name, `r` RepoName, `id` Id | out: 204 (an in-flight push finishes) | err: MC | ruk: store only
- **mirror.sync** · mutation · `POST /api/repos/{o}/{r}/mirrors/{id}/sync` · scope: `mirrors:trigger` (role admin) · idem: natural (coalesces into the debounced queue) · conc: none (one run per mirror; lease-fenced) · page: none · html: form `/{o}/{r}/settings/mirrors/{id}`
  in: path `o` Name, `r` RepoName, `id` Id | out: 202 MirrorRun; failures fire `mirror.sync_failed` and S7 inbox notices
  err: M + state-conflict(disabled), remote-rejected, remote-uncertain | ruk: `push✗` (prepare.rs:283) with `expected_old` = remote advertisement; `push.reconcile✗` (prepare.rs:289) for uncertain runs; exact mode forces, ff_only skips non-ff (`non_ff_skipped`)
- **mirror.run.list** · read · `GET /api/repos/{o}/{r}/mirrors/{id}/runs` · scope: `mirrors:read` · idem: safe · conc: cond-GET · page: cursor(started_at desc, id) · html: page `/{o}/{r}/settings/mirrors/{id}`
  in: path `o` Name, `r` RepoName, `id` Id; query `state?` "queued"\|"running"\|"ok"\|"partial"\|"failed"\|"uncertain", `limit?` Limit, `cursor?` Cursor | out: Page<MirrorRun> (refs omitted) | err: R | ruk: store only
- **mirror.run.get** · read · `GET /api/repos/{o}/{r}/mirrors/{id}/runs/{run}` · scope: `mirrors:read` · idem: safe · conc: cond-GET · page: none · html: page `/{o}/{r}/settings/mirrors/{id}/runs/{run}`
  in: path `o` Name, `r` RepoName, `id` Id, `run` Id | out: MirrorRun | err: R | ruk: store only

### 3.13 admin (13; `admin.user.reset-password`, `admin.user.unlock`, `admin.user.totp-reset` are the settled auth-design §2/§11 ops; all audit-logged per §12)
- **admin.user.reset-password** · mutation · `POST /api/admin/users/{user}/reset-password` · scope: `admin:write` (mint needs human + fresh auth) or the local CLI · idem: key · conc: none · page: none · html: form `/-/admin/users/{user}` (CLI: `rukie admin user reset-password --user <name> [--random|--password-stdin|--password-file] [--revoke-ssh-keys] [--keep-tokens]`)
  in: path `user` Name; body `password?` Password (absent → 20-char random printed once), `revoke_sessions?` bool=true, `revoke_tokens?` bool=true (`--keep-tokens` opt-out, audit-logged, refused for compromise resets), `revoke_ssh_keys?` bool=false, `must_change?` bool=true | out: `{temporary_password?}` (once); inbox notice with revoked prefixes / surviving fingerprints
  err: MK | ruk: store only
- **admin.user.unlock** · mutation · `POST /api/admin/users/{user}/unlock` · scope: `admin:write` or CLI · idem: natural · conc: none · page: none · html: form `/-/admin/users/{user}`
  in: path `user` Name | out: 204 (clears `locked_until` + counters) | err: M | ruk: store only
- **admin.user.totp-reset** · mutation · `POST /api/admin/users/{user}/totp-reset` · scope: `admin:write` or CLI · idem: natural · conc: none · page: none · html: form `/-/admin/users/{user}`
  in: path `user` Name; body `revoke_sessions?` bool=true, `revoke_ssh_keys?` bool=false | out: 204 (clears TOTP + burns recovery codes; tokens unaffected) | err: M | ruk: store only
- **admin.user.list** · read · `GET /api/admin/users` · scope: `admin:read` · idem: safe · conc: cond-GET · page: cursor(name) · html: page `/-/admin/users`
  in: query `q?` Text(1..64), `state?` "active"\|"disabled"\|"locked"\|"all"="all", `limit?` Limit, `cursor?` Cursor | out: Page<User + {is_admin, disabled, locked_until, totp_enabled, last_login_at}> | err: R | ruk: store only
- **admin.user.create** · mutation · `POST /api/admin/users` · scope: `admin:write` · idem: key · conc: none · page: none · html: page+form `/-/admin/users/new`
  in: `name` Name; `display_name?` Text(0..100)=""; `password?` Password (absent → generated); `must_change_password?` bool=true; `is_admin?` bool=false | out: 201 `{user, initial_password?}` (once)
  err: MK + already-exists(field name), name-reserved | ruk: store only
- **admin.user.update** · mutation · `PATCH /api/admin/users/{user}` · scope: `admin:write` · idem: natural · conc: If-Match · page: none · html: form `/-/admin/users/{user}`
  in: path `user` Name; body `is_admin?` bool, `disabled?` bool (disabling revokes sessions and suspends tokens), `display_name?` Text(0..100) | out: User | err: MC + state-conflict(last-admin) | ruk: store only
- **admin.user.delete** · mutation · `DELETE /api/admin/users/{user}` · scope: `admin:write` · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/-/admin/users/{user}`
  in: path `user` Name; query `confirm` Name | out: 202 `{purge_after:Timestamp}` | err: MC + state-conflict(last-owner, last-admin) | ruk: store only
- **admin.repo.list** · read · `GET /api/admin/repos` · scope: `admin:read` · idem: safe · conc: cond-GET · page: cursor(updated_at desc, id) · html: page `/-/admin/repos`
  in: query `state?` "active"\|"forking"\|"deleted"\|"all"="all", `owner?` Name, `limit?` Limit, `cursor?` Cursor | out: Page<Repo> | err: R | ruk: store only
- **admin.repo.purge** · mutation · `POST /api/admin/repos/{id}/purge` · scope: `admin:write` · idem: key · conc: none · page: none · html: GET confirm + form `/-/admin/repos`
  in: path `id` Id; body `confirm` FullName | out: 202 Job | err: MK + state-conflict(not-deleted) | ruk: store only (G5, §8)
- **admin.job.list** · read · `GET /api/admin/jobs` · scope: `admin:read` · idem: safe · conc: cond-GET · page: cursor(next_run_at, id) · html: page `/-/admin/jobs`
  in: query `kind?` "hook_delivery"\|"mirror_run"\|"repo_fork"\|"repo_purge"\|"archive_build", `state?` "queued"\|"running"\|"succeeded"\|"failed"\|"dead", `limit?` Limit, `cursor?` Cursor | out: Page<Job> | err: R | ruk: store only
- **admin.job.retry** · mutation · `POST /api/admin/jobs/{id}/retry` · scope: `admin:write` · idem: natural · conc: none · page: none · html: form `/-/admin/jobs`
  in: path `id` Id | out: 202 Job | err: M + state-conflict(not-failed) | ruk: store only
- **admin.audit.list** · read · `GET /api/admin/audit` · scope: `admin:read` · idem: safe · conc: cond-GET · page: cursor(at desc, id) · html: page `/-/admin/audit`
  in: query `actor?` Name, `action?` Text(1..64) (auth-design §12 catalog), `target?` Text(1..300), `since?` Timestamp, `limit?` Limit, `cursor?` Cursor | out: Page<AuditEvent> (365-day retention) | err: R | ruk: store only
- **admin.health.get** · read · `GET /api/admin/health` · scope: `admin:read` · idem: safe · conc: none (`no-store`) · page: none · html: page `/-/admin/health`
  in: no fields | out: `{store:{ok, latency_ms}, ruk:{reachable, latency_ms}, queues:{<kind>:{depth, oldest_age}}}` | err: R | ruk: store only

## 4. Event catalog (webhook-design §4, verbatim; inbox sources S1-S11 mirror it)
`ref.updated` (ref, ref_kind, old_oid, new_oid, change_kind, pusher, compare_url, branch_view_url) · `change.opened` · `change.updated` · `change.landed` · `change.abandoned` · `review.submitted` · `attestation.added` · `task.created` · `task.claimed` · `task.closed` · `ci.run.state_changed` · `release.published` · `mirror.sync_failed` · `repo.created` · `repo.deleted`.
Deliberately absent in v1: star/watch/fork social events, per-commit comments, inbox notifications, account lifecycle (webhook-design §4). `data` shapes per that table.

## 5. Git wire (outside the operation table)
- **Smart HTTP**: `GET /{o}/{r}.git/info/refs?service=…`, `POST /{o}/{r}.git/git-upload-pack`, `POST /{o}/{r}.git/git-receive-pack`. Basic auth: username = rukie username, password = full `rk_` token; fetch `repo:read`, push `repo:write`; `token_repos` applies (auth-design §10.1). Caller identity reaches Ruk as short-lived ephemeral credentials, never the raw token.
- **SSH**: key fingerprint → user (`auth.sshkey.*` rows); same scopes; unknown key → generic `Permission denied (publickey)` + audit.
- Both run Ruk's pack protocol through daemon command streams (§8). Pushes capped at 256 MiB (`MAX_COMMAND_STDIN_BYTES`, `daemon/mod.rs:377`); output buffered whole (G6).
- After a ref update: `ref.updated` outbox rows + `mirror` debounce + inbox fan-out + CI trigger. `refs/for/<branch>` pushes are PROVISIONAL (change review) but still fire `ref.updated` and `change.opened` per the catalog.

## 6. PROVISIONAL placeholders — change review (8) and tasks (7)
Names/paths/bindings tentative; the change/task ideation decides final fields. Every row still carries all table fields. Scopes use the settled `tasks:read`/`tasks:write` (and `repo:*` for reads); verdict vocabulary follows the settled `review.submitted` event (`approved|changes_requested|commented`).

- **change.list** · read · `GET /api/repos/{o}/{r}/changes` · scope: `repo:read` · idem: safe · conc: cond-GET · page: cursor(updated_at desc, change_id) · html: page `/{o}/{r}/changes`
  in: path `o` Name, `r` RepoName; query `state?` "open"\|"landed"\|"abandoned"\|"all"="open", `author?` Name, `limit?` Limit, `cursor?` Cursor | out: Page<ChangeSummary (P)> | err: R | ruk: `query✗`
- **change.get** · read · `GET /api/repos/{o}/{r}/changes/{change}` · scope: `repo:read` · idem: safe · conc: cond-GET · page: none · html: page `/{o}/{r}/changes/{change}` (per nojs: `<details>` files, pending-review banner, viewed tracking)
  in: path `o` Name, `r` RepoName, `change` ChangeId | out: Change (P) | err: R + divergent | ruk: `show✗` + `attest.list✓`
- **change.diff.get** · read · `GET /api/repos/{o}/{r}/changes/{change}/diff` · scope: `repo:read` · idem: safe · conc: cond-GET · page: cursor(change, last_path) · html: page = change page
  in: path `o` Name, `r` RepoName, `change` ChangeId; query `limit?` Limit, `cursor?` Cursor | out: `{files:Page<FileDiff>}` (P) | err: R + divergent | ruk: `diff✗`
- **change.comment.list** · read · `GET /api/repos/{o}/{r}/changes/{change}/comments` · scope: `repo:read` · idem: safe · conc: cond-GET · page: cursor(created_at, id) · html: page = change page (inline `<details>` comment forms per nojs)
  in: path `o` Name, `r` RepoName, `change` ChangeId; query `limit?` Limit, `cursor?` Cursor | out: Page<Comment (P)> | err: R | ruk: store only
- **change.comment.create** · mutation · `POST /api/repos/{o}/{r}/changes/{change}/comments` (HTML posts to `/changes/{id}/comments` per nojs) · scope: `repo:write` · idem: key · conc: none · page: none · html: inline form on the change page
  in: path `o` Name, `r` RepoName, `change` ChangeId; body `body` Markdown, `pending?` bool=false (pending set held server-side per nojs §on pending reviews) | out: 201 Comment (P) | err: MK | ruk: store only
- **change.review.submit** · mutation · `POST /api/repos/{o}/{r}/changes/{change}/reviews` · scope: `repo:write` · idem: key · conc: none · page: none · html: form on the change page (submit/discard pending)
  in: path `o` Name, `r` RepoName, `change` ChangeId; body `verdict` "approve"\|"request_changes"\|"comment", `body?` Markdown | out: 201 Review (P); fires `review.submitted` only for verdicts | err: MK | ruk: store only (+ `attest.add✓` candidate)
- **change.land** · mutation · `POST /api/repos/{o}/{r}/changes/{change}/land` · scope: `repo:write` · idem: key · conc: none · page: none · html: GET confirm + form on the change page
  in: path `o` Name, `r` RepoName, `change` ChangeId; body `mode?` "ff"\|"merge"="ff" | out: 202 `{change, refs:[RefName]}` (P); fires `change.landed` + `ref.updated`
  err: MK + conflicted, divergent, forbidden(reason attestations-missing) | ruk: `land.prepare✓` + `land.execute✗`
- **branch.change.list** · read · `GET /api/repos/{o}/{r}/branches/{branch}/changes` · scope: `repo:read` · idem: safe · conc: cond-GET · page: cursor(tip_oid, offset) · html: page `/{o}/{r}/branches/{branch}/changes` (the branch view)
  in: path `o` Name, `r` RepoName, `branch` BranchName; query `limit?` Limit, `cursor?` Cursor | out: Page<ChangeSummary (P)> | err: R | ruk: `log✗` + `query✗`
- **task.list** · read · `GET /api/repos/{o}/{r}/tasks` · scope: `tasks:read` · idem: safe · conc: cond-GET · page: cursor(updated_at desc, id) · html: page `/{o}/{r}/tasks`
  in: path `o` Name, `r` RepoName; query `state?` "open"\|"claimed"\|"closed"\|"all"="open", `assignee?` Name, `limit?` Limit, `cursor?` Cursor | out: Page<Task (P)> | err: R | ruk: store only
- **task.get** · read · `GET /api/repos/{o}/{r}/tasks/{id}` · scope: `tasks:read` · idem: safe · conc: cond-GET · page: none · html: page `/{o}/{r}/tasks/{id}`
  in: path `o` Name, `r` RepoName, `id` Id | out: Task (P) | err: R | ruk: store only
- **task.create** · mutation · `POST /api/repos/{o}/{r}/tasks` · scope: `tasks:write` · idem: key · conc: none · page: none · html: page+form `/{o}/{r}/tasks/new`
  in: path `o` Name, `r` RepoName; body `title` Text(1..200), `body?` Markdown="", `assignee?` Name | out: 201 Task (P); fires `task.created` | err: MK | ruk: store only
- **task.update** · mutation · `PATCH /api/repos/{o}/{r}/tasks/{id}` · scope: `tasks:write` (author or assignee) · idem: natural · conc: If-Match · page: none · html: form `/{o}/{r}/tasks/{id}`
  in: path `o` Name, `r` RepoName, `id` Id; body `title?` Text(1..200), `body?` Markdown, `assignee?` Name\|null | out: Task (P) | err: MC | ruk: store only
- **task.claim** · mutation · `POST /api/repos/{o}/{r}/tasks/{id}/claim` · scope: `tasks:write` · idem: key · conc: none (the lease is the fence) · page: none · html: form `/{o}/{r}/tasks/{id}`
  in: path `o` Name, `r` RepoName, `id` Id; body `ttl?` Duration 5m..1h=1h | out: 200 `{claim:Id, expires_at:Timestamp}`; fires `task.claimed`
  err: MK + state-conflict(reason claimed) (`E_CLAIMED`) | ruk: `claim.acquire✓` (TTL ≤ 3600 s, schema `limits.claim_ttl_seconds`)
- **task.release** · mutation · `POST /api/repos/{o}/{r}/tasks/{id}/release` · scope: `tasks:write` (holder) · idem: natural · conc: none · page: none · html: form `/{o}/{r}/tasks/{id}`
  in: path `o` Name, `r` RepoName, `id` Id | out: 204 | err: M + state-conflict(not-holder) | ruk: `claim.release✓`
- **task.close** · mutation · `POST /api/repos/{o}/{r}/tasks/{id}/close` · scope: `tasks:write` · idem: natural · conc: If-Match · page: none · html: GET confirm + form `/{o}/{r}/tasks/{id}`
  in: path `o` Name, `r` RepoName, `id` Id; body `reason` "completed"\|"duplicate"\|"wontfix"\|"abandoned" | out: 204; fires `task.closed` | err: MC | ruk: store only (+ `claim.release✓` when held)

## 7. Ruk gaps behind `store only` rows (verified facts; no protocol design implied)
- **G1** Hosted typed reads exist only for `op.log`, `claim.list`, `attest.list` (`coord/hosted.rs:621-640`); `file`, `blame`, `log`, `show`, `diff`, `query` return `E_HOSTED` — their rows carry the typed method as the target binding and run §8 commands today. (`search` is the settled fix for code search: ruk-search-design.md adds the method and a hosted read arm.)
- **G2** `fetch`, `push`, `push.reconcile`, `land.execute` refuse on the hosted path (`coord/prepare.rs:277-299`); no typed method lists a remote's refs (mirror diffing uses §8 `ls-remote`).
- **G3** No typed method lists refs by name or gives the last commit per path (`/tmp/ruk-model.json`; `query` returns changes, not names) — `branch.*`, `tag.*` listing/lookup, `code.tree.get` last-commit.
- **G4** No typed archive method — `code.archive.get` (the `archive` verb serves tar and zip, `verbs_transport.rs:12`).
- **G5** No typed repository lifecycle method (init, clone, set HEAD, remove); the daemon registry has no retire API (`daemon/registry.rs`) — `repo.create`, `repo.fork.create`, `repo.update` default branch, `repo.delete`/`admin.repo.purge`.
- **G6** Daemon command streams buffer all stdout and cap stdin at 256 MiB (`MAX_COMMAND_STDIN_BYTES`, `daemon/mod.rs:377`; `run_captured` `mod.rs:1366-1385`).
- **G7** Ruk page cursors are 30-second leases (README; `hosted.rs` read doc); the forge uses its own cursors except for `search` (§1.6).
- Stale-doc note: `ruk-domain-model.md` item 15 ("HTTP(S) transport unimplemented") and item 7 ("annotated tags refused") are both contradicted by current source (`verbs_transport.rs:7,9,16` serve smart-http fetch/push/ls-remote with dumb-http fetch fallback; `daemon/tests.rs:841-931` proves hosted annotated `tag.set`). This draft follows the source.

## 8. Interim command bindings (implementation note; not the `ruk:` field)
Ops marked `store only` by gaps G2-G5 are served today through daemon `CommandOpen` streams (`daemon/mod.rs:390-418`; verb table in `cli/dispatch.rs`):

| Op(s) | Interim command |
|---|---|
| git wire (§5) | `upload-pack`, `receive-pack` (dispatch.rs:591,878) |
| `repo.create` | `init` (dispatch.rs:381) |
| `repo.fork.create` | `clone` `[inference: bare/local flags]` |
| `repo.update` (default branch), purge | `symbolic-ref` `[inference]`; filesystem removal after registry eviction |
| `code.rev.resolve` | `rev-parse --verify <rev>^{commit}` |
| `code.tree.get` | `ls-tree -l -z` (dispatch.rs:423); last commit `last-modified` (dispatch.rs:430) |
| `code.blob.get`, `code.raw.get` | `ls-tree` + `cat-file blob` |
| `code.blame.get` | `blame --porcelain` (dispatch.rs:92) |
| `code.log.list` | `log` with fixed `--format`, `--skip`, `--max-count` (dispatch.rs:402) |
| `code.commit.get` | `cat-file commit` |
| `code.diff.get`, `code.compare.get` | `diff -M --no-color` (dispatch.rs:253); compare additionally `rev-list` (dispatch.rs:703) `[inference: three-dot form]` |
| `code.archive.get` | `archive --format=<tar|zip>` (dispatch.rs:77-83; verbs_transport.rs:12) |
| `branch.*`/`tag.*` listing and lookup | `for-each-ref --sort=refname` (dispatch.rs:304); `cat-file tag` for annotations |
| `branch.update` ancestry check | `rev-list --count <new>..<old>` |
| `mirror.sync` | `ls-remote` (diff for deletions) + `push` with explicit refspecs (dispatch.rs:556; `--mirror` covered by cli/tests.rs:957); https via smart-http push, ssh via generated key |

(`search.code` has no interim row: it binds to the typed `search` method per ruk-search-design.md.)

## 9. p95 server render targets (stated, to be measured)
| Class | p95 |
|---|---|
| Store-only pages | ≤ 50 ms |
| Ruk-backed browse via the §8 path | ≤ 250 ms |
| Ruk-backed browse with hosted typed reads | ≤ 150 ms |
| `search.code` | ≤ 800 ms on the 10 k-file fixture (ruk-search-design §8) |
| Archive first build | best effort, then served from cache |
| Inbox page | ≤ 4 indexed queries, ≤ 60 KB HTML (inbox-design §5) |

Reference: Ruk's first log page measured 94-98 ms p50 across the two capacity runs (`tasks/capacity/RESULTS.md`; README).

## 10. Assumptions, flagged divergences, and open questions
- `[inference]` ServeMux keeps `%2F` inside a segment; `cmd:clone`, `cmd:symbolic-ref`, three-dot `cmd:diff` arguments; MCP tool names with `.`→`_` (per inbox-design §6's `inbox_list`); the multi-repo `search.code` fan-out merge order.
- Divergences flagged, all deliberate:
  - `auth.sshkey.list` added beyond the auth-design §2 op list (the settings page needs a read).

[Showing lines 4094-4416 of 4498 (49.9KB limit). Use :4417 to continue]
  - `auth.sshkey.list` added beyond the auth-design §2 op list (the settings page needs a read).
  - Domains without a settled scope (profile, orgs/teams, social, repo settings, inbox) are session ops; agents cannot call them with tokens until auth-design §8.2 grows scopes.
  - Mirror op names are this draft's (no settled list found); modes/triggers/events/refspecs/error classes follow the settled designs.
  - `auth.signup`/`whoami` absent by design (accounts via OIDC `allow_signup` or admin; identity via 401 detail + `auth.token.list`) — open question for agent DX.
  - Releases key on tag names; hooks use repo/owner endpoint scope exactly as settled.
````

### Critical Files for Implementation
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/auth-design.md` — authoritative auth op names, argon2id/TOTP/token/scope/session contracts (§2-§12).
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-search-design.md` — the typed `search` method, SearchArgs/budgets/cursor contract behind `search.code`.
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/webhook-design.md` + `inbox-design.md` — endpoint/message/attempt ops and inbox ops/states/watch levels.
- `/tmp/ruk-model.json` — the 46 typed methods today; the ✓/✗ baseline for every `ruk:` value.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/prepare.rs` — hosted mutation arms (248-276) and refusals (277-299) behind G1/G2 and the annotated-tag ✓.

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Align `search.code` with the …

---
## Later corrections from the same agent (apply over the body above)

→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/loca…) ⇒ ok · 44 lines

[advisor] <advisory advisor="muse" severity="nit" guidance="weigh, don't blindly obey"> Freeze draft now. Save file unchanged. Al…

---
Frozen. The last full emit stands as the file body; below are the final corrections it must absorb before saving (three spots, verbatim replacement lines). With these applied, the draft is aligned with every settled sibling design read this session: auth-design §2-§12, webhook-design §4-§7, inbox-design §1-§6, ruk-search-design §2 (exact caps now verified from source, not the advisory's paraphrase: bytes cap is **1 GiB** and deadline cap is **30 s**), mirror research §5, and the no-JS interaction catalogue.

**Patch 1 — §1.4, replace the `SearchBudget` row:**
```
| `SearchBudget` | `{max_bytes_scanned? ≤ 1073741824 = 268435456, max_blobs? ≤ 100000 = 100000, deadline_ms? ≤ 30000 = 5000}`; over-cap rejected, never clamped (ruk-search-design §2) |
```

**Patch 2 — §1.5, replace the `SearchHit` line and §3.7 `search.code` row:**
```
- `SearchHit {path:Path, line:uint (1-based), column:uint (1-based byte offset), preview (first 1 KiB of the line, truncation marked), blob:Oid}`; `SearchReply {view:ViewInfo, hits:Page<SearchHit>, truncated:bool, exhausted:"none"|"budget"|"deadline", scanned_blobs, scanned_bytes, skipped_blobs}` (ruk-search-design §2)
```
```
- **search.code** · read · `GET /api/search/code` · scope: none (public) / `repo:read` (adds private repos) · idem: safe · conc: none (`no-store`; live scan) · page: Ruk `PageCursor` passed through verbatim (30 s lease; expired lease → restart from first page; limit 1..1000, default 100) · html: page `/-/search?type=code` (GET form), `/{o}/{r}/search`
  in: query `repo?` FullName; `owner?` Name (exclusive with repo; neither = all accessible repos at their default branch); `rev?` RevisionRef Head|Change only, default = default branch (Workspace rev → `invalid-input` naming the gap); `pattern` SearchPattern (`literal` non-empty ≤ 4 KiB, empty → `E_INVALID`; or `regex`, must compile); `case_insensitive?` bool=false; `paths?` PathSelection (`all`\|[Path] 0..32); `globs?` [Glob] 0..32; `exclude_globs?` [Glob] 0..32 (≤ 32 globs total, each ≤ 1 KiB); `budget?` SearchBudget; `limit?` int 1..1000=100; `cursor?` Ruk PageCursor | out: per repo `SearchReply` (hits ordered per the design's global path order; binary blobs skipped; dedupe across fan-out paths; `exhausted` reports budget/deadline stops and the cursor resumes at the scan frontier); multi-repo requests merge per-repo replies `[inference: fan-out merge order]` with `searched_repos:int` — the cross-repo fan-out is forge policy on top of the per-repo method
  err: R + invalid-input(empty literal, bad regex, over-cap budget, workspace rev), rate-limited | ruk: `search✗` (planned per ruk-search-design.md §1: `SearchArgs/SearchPattern/SearchBudget/SearchHit/ScanExhaustion`, a `method_table!` row, and a hosted `read()` `Request::Search` arm with a core-owned `SearchPool`; not yet in `/tmp/ruk-model.json`)
```

**Patch 3 — inbox scope (glm-flash is right; inbox-design §6 accepts scoped tokens and rates per token).** In §3.11, change the header note to: "(8; ops, states, watch levels per inbox-design §1.1, §4, §6. Scope on every row: **session or any scoped token** — the settled §8.2 vocabulary defines no `inbox:*` scope, so ownership is the only authz; flagged in §10. Rates: `list` 1 qps per token burst 5; `count` 2 qps; `mark/snooze` 5 qps → 429 + `Retry-After`.)" and set `scope: session or any scoped token` on all eight rows (replacing `scope: session` and the "no inbox scope in v1 — flagged" parenthetical on `inbox.list`). In §2, change the no-scope domain list to "(profile, orgs/teams, social, repo settings/delete, explore, search-of-users)" and add to §10's flagged divergences: "Inbox rows accept any scoped token per inbox-design §6; a dedicated `inbox:read`/`inbox:write` scope name is missing from auth-design §8.2 and should be proposed there."

File to save (last emit + the three patches above), unchanged otherwise:
`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/usage-surface-draft.md`

Final state: 131 settled operations across the 14 domains + 15 PROVISIONAL change/task placeholders, every row carrying kind, HTTP method+path, typed inputs, output, error set+extras, idempotency, concurrency, pagination, scope, Ruk binding (`search` now bound to the settled typed method), and HTML page/form; scopes are the settled 14; bindings verified against `prepare.rs:248-299`, `hosted.rs:621-640`, `daemon/tests.rs:841-931`, `verbs_transport.rs:7-16`, and `daemon/mod.rs:377`.

### Critical Files for Implementation
- `.../local/auth-design.md` — auth op names, argon2id/TOTP/token/scope/session contracts (§2-§12).
- `.../local/ruk-search-design.md` — typed `search` method: exact SearchArgs/budget caps/cursor contract behind `search.code`.
- `.../local/webhook-design.md` and `.../local/inbox-design.md` — endpoint/message/attempt ops and inbox ops/states/watch levels/token acceptance.
- `/tmp/ruk-model.json` — the 46 typed methods today; the ✓/✗ baseline.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/prepare.rs` — hosted mutation arms and refusals behind the binding marks.

[advisor] <advisory advisor="glm-flash" severity="nit" guidance="weigh, don't blindly obey"> State the multi-repo cursor for `sea…

---
Accepted — that closes the last open edge. Replacement text for the `search.code` row in §3.7 (final patch; everything else stays frozen as emitted):

```
- **search.code** · read · `GET /api/search/code` · scope: none (public) / `repo:read` (adds private repos) · idem: safe · conc: none (`no-store`; live scan) · page: single-repo requests (`repo=` given) pass the Ruk `PageCursor` through verbatim — it binds one repository's `(store, view, query, lease)`, 30 s lease, expired → restart from that repo's first page; fan-out requests (`owner=` or neither) use a composite forge cursor `{v, per_repo: [{repo_id, ruk_cursor}]}` resuming each repo at its own frontier (repos added mid-pagination start fresh; dropped repos are skipped); limit 1..1000, default 100, applies per repo · html: page `/-/search?type=code` (GET form), `/{o}/{r}/search`
  in: query `repo?` FullName; `owner?` Name (exclusive with repo; neither = all accessible repos at their default branch); `rev?` RevisionRef Head|Change only, default = default branch (Workspace rev → `invalid-input` naming the gap); `pattern` SearchPattern (`literal` non-empty ≤ 4 KiB, empty → `E_INVALID`; or `regex`, must compile); `case_insensitive?` bool=false; `paths?` PathSelection (`all`\|[Path] 0..32); `globs?` [Glob] 0..32; `exclude_globs?` [Glob] 0..32 (≤ 32 globs total, each ≤ 1 KiB); `budget?` SearchBudget (per repo); `limit?` int 1..1000=100; `cursor?` Ruk PageCursor (single repo) or composite forge cursor (fan-out) | out: per repo `SearchReply` (hits in the design's global path order; binary blobs skipped; dedupe across fan-out paths; `exhausted` reports budget/deadline stops and the cursor resumes at the scan frontier); fan-out merges per-repo replies `[inference: merge order]` with `searched_repos:int` — the cross-repo fan-out is forge policy on top of the per-repo method
  err: R + invalid-input(empty literal, bad regex, over-cap budget, workspace rev, composite cursor used on a single-repo request and vice versa), rate-limited | ruk: `search✗` (planned per ruk-search-design.md §1: `SearchArgs/SearchPattern/SearchBudget/SearchHit/ScanExhaustion`, a `method_table!` row, and a hosted `read()` `Request::Search` arm with a core-owned `SearchPool`; not yet in `/tmp/ruk-model.json`)
```

Also add one line to §1.6 after the search exception: "Fan-out search (`owner=` or no filter) cannot pass a Ruk cursor through (each binds one repo); it uses a composite forge cursor holding one Ruk cursor per repository."

That is the complete deliverable: the file body from the last full emit plus the five patches from the previous message plus this one, saved unchanged to:
`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/usage-surface-draft.md`

Final state, all acceptance criteria met:
- **131 settled operations** across all 14 target domains — auth/session 18, users 3, orgs/teams 20, repos 12, code browsing 10, refs 9, search 3, releases 9, webhooks 11, mirrors 8, social 7, inbox 8, admin 13 — each row carrying kind, HTTP method+path, typed inputs with constraints, output shape, error set+extras, idempotency, concurrency, pagination, token scope, Ruk binding, and HTML page/form; **15 PROVISIONAL** change/task placeholder rows, complete but marked tentative.
- Names, scopes, events, budgets, and flows follow the settled sibling designs verbatim (auth, webhook, inbox, ruk-search, mirror research, no-JS patterns); divergences are flagged in §10, not silent.
- Ruk bindings verified against source: `prepare.rs:248-299`, `hosted.rs:621-640`, `daemon/tests.rs:841-931` (annotated `tag.set` hosted), `verbs_transport.rs:7-16` (ssh + smart-http transports live; README stale), `daemon/mod.rs:377` (256 MiB cap); git-shaped commands confined to the §8 interim note, never the `ruk:` field.

### Critical Files for Implementation
- `.../local/auth-design.md` — auth op names, argon2id/TOTP/token/scope/session contracts (§2-§12).
- `.../local/ruk-search-design.md` — typed `search` method: SearchArgs, budget caps, per-repo cursor binding behind `search.code`.
- `.../local/webhook-design.md` and `.../local/inbox-design.md` — endpoint/message/attempt ops and inbox ops/states/watch levels/token acceptance.
- `/tmp/ruk-model.json` — the 46 typed methods today; the ✓/✗ baseline for every binding.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/prepare.rs` — hosted mutation arms and refusals behind the binding marks.