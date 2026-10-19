I have no write tool, so I can't save `mirror-design.md` myself. The orchestrator saves from this final message, so the complete artifact follows. I made no further tool calls.

---

# mirror-design.md: rukie push-mirror subsystem

**Evidence labels:** **[V]** verified in code or schema this session, with path:line. **[R]** taken from `research-push-mirror-targets.md`, which cites its own primary sources. **[M]** inference or recalled external fact, not verified here. **[U]** unverified; needs a live probe or source read before GA.

Paths are relative to `/home/alpha/rewrite/git-renewed/` unless absolute.

---

## 0. Decisions and blockers

**Decisions**

1. Rukie (Go) owns mirror state, scheduling, leases, credentials, policy, and API. Ruk only performs the wire leg (`ls-remote`, push) and the local ref snapshot.
2. `--mirror` is never sent. Every update is an explicit refspec computed by rukie: `+src:dst` in exact mode, `src:dst` (non-forced) in ff-only mode. Deletes go in a separate invocation.
3. Credentials are AES-256-GCM under a key derived from rukie's `SECRET_KEY` file. Do not reuse `modules/secret.EncryptSecret` (§5).
4. Every host receives `Authorization: Basic base64(user:token)`. Bearer is not used.
5. Codeberg mirrors are off by default and require an explicit policy acknowledgement (§3).
6. The daemon environment must be hermetic for Ruk: no `GIT_AUTH_*`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=` (empty). Otherwise ambient auth answers for every host (§2, RC-1). An empty `GIT_CONFIG_GLOBAL` skips home config [V credentials.rs:134-151, test 1053-1054]. An unscoped `credential.helper` row runs for every URL [V credentials.rs:314-333].

**Blockers in the current tree**

- **B1.** Typed `push` and `push.reconcile` are refused with `E_HOSTED`. `Request::Push` and `Request::PushReconcile` return `uncovered(...)` [V ruk-core/src/coord/prepare.rs:283-294; helper 338-341]. Hosted dispatch calls `prepare_mutation_with` [V ruk-core/src/coord/hosted.rs:262-281, call at 280], reached from `Method::Push` [V ruk/crates/ruk/src/daemon/mod.rs:2317-2318]. The schema still advertises both methods [V /tmp/ruk-model.json `methods`].
- **B2.** No credential channel reaches the wire. `PushArgs` and `RemoteSpec` carry no auth field [V ruk-model.json `$defs.PushArgs`, `RemoteSpec`, `RemoteUrl`]. The HTTP header builder reads the process environment directly [V ruk-git/src/transport/smart_http.rs:201-207]. Daemon `CommandOpen.env` is not consumed by that path [V ruk/crates/ruk/src/daemon/mod.rs:404 `env`, versus smart_http.rs:201-207]. The only per-call header path is `-c http.<url>.extraHeader`, which puts the secret in argv [V ruk/crates/ruk/src/cli/mod.rs:385-391, 123-124; ruk/crates/ruk/src/cli/verbs_transport.rs:254-289].
- **B3.** `--mirror` includes every local direct ref, Ruk-internal refs included, and deletes every remote ref it lacks [V verbs_transport.rs:1248-1257 (force implied), 1621-1652 (`mirror_push_updates`, `direct_tips`, no namespace filter)].
- **B4.** The fast-forward check fails the whole batch before any network I/O [V verbs_transport.rs:1365-1387].
- **B5.** Any error after `begin_send` marks the plan `Uncertain`, including a 401, 403, 429, or 3xx on the POST [V smart_http.rs:759-803 (`begin_send` before POST), 814-849 (`check_status` on POST), 648-686 (`mark_uncertain` on any error)].
- **B6.** `PushRejected`, `PushUncertain`, and `RemoteMoved` all map to `E_STALE` [V ruk-git/src/lib.rs:160-162]. The protocol defines `E_PUSH_REJECTED`, `E_PUSH_UNCERTAIN`, and `E_REMOTE_MOVED` [V ruk-core/src/error.rs:47-49, 385-387].

---

## 1. Ruk capability map (what exists today)

| Capability | State | Evidence |
|---|---|---|
| Smart-HTTP push: GET `info/refs?service=git-receive-pack`, POST `git-receive-pack`, report-status, side-band, thin packs | Implemented. Reached only from CLI `push_net`. | smart_http.rs:243-257 (discover), 648-849; verbs_transport.rs:1420-1484, 1523 |
| Local CAS against the advertised old value before POST | Implemented | smart_http.rs:715-765 |
| Per-ref outcome with verbatim server reason | Implemented. `Rejected { reason: String }`. | push.rs:114-120; verbs_transport.rs:1653-1673 |
| Durable plan row: Prepared → Sending → Confirmed / Rejected / Uncertain | Implemented. Row key is the plan ID. | push.rs:65-77, 151-220, 233-237, 466, 526-531; types.rs:257, 322 (`push_plans`) |
| Pure reconcile function | Exists; no method exposes it | push.rs:418-423 |
| Typed `push`, `push.reconcile` | Refused (`E_HOSTED`) | prepare.rs:283-294 |
| Credentials on push | None in protocol. Only ambient env, credential helper, repo-config extraheader, and `-c`. | smart_http.rs:159-172, 201-207; credentials.rs:753-779; verbs_transport.rs:254-289 |
| Push ref ceiling (`push_refs`) | `require_push` is defined; no production caller | capability.rs:789-792; tests only at 1387-1390 |
| Fast-forward check | CLI only. Skipped when `force` or `force_all` is set. Batch-fatal. | verbs_transport.rs:1365-1387 |
| Mirror update list | All local direct refs; deletes remote refs absent locally | verbs_transport.rs:1621-1652; mirror.rs `push_mirror_updates` |
| Local ref listing | `for-each-ref` (`%(refname)`, `%(objectname)`, `%(objecttype)`), `show-ref` | dispatch.rs:304, 766; ruk/crates/ruk/src/cli/STATUS.md for-each-ref row [V, read from rendered output] |
| Remote ref listing | `ls-remote`, which uses `http_extra_headers` and `discover_versioned` | dispatch.rs:416; verbs_transport.rs:4204-4250 (headers at 4247) |
| Ancestry | `merge-base` verb; `is_ancestor` | dispatch.rs:451; cli/state.rs:1462 |
| Idempotent command replay | `CommandOpen.request_id`: reconnects replay the recorded terminal report | daemon/mod.rs:390-411 |
| Stdin for verbs | `CliEnv.stdin`; daemon passes stdin bytes | cli/mod.rs:75-94; daemon/mod.rs:1375 |
| Error mapping | `Offline` → `E_OFFLINE`; push errors → `E_STALE` | lib.rs:158, 160-162 |
| HTTP status handling | Every non-2xx → `Offline`, with URL; body dropped | smart_http.rs:221-229 |
| `Retry-After` / 429 | Not handled. No match under `ruk/crates` [V grep]. | — |
| Redirects | GET follows up to 3 hops, drops `authorization` cross-origin, returns only status and headers. POST is never followed. | http.rs:42, 1081-1107; http.rs:973 (POST doc) |
| TLS roots | `webpki_roots::TLS_SERVER_ROOTS` only. No extra CA, no proxy [V grep of ruk-git/src]. | http.rs:411-417 |
| Userinfo in URLs | Kept in `normalize_base`. Echoed in errors. | smart_http.rs:124-130, 139-151, 221-229 |
| Cancellation | CLI push passes `Interrupt::none()` | verbs_transport.rs:1523-1545 |
| "Uncertain blocks repeat publication" | Documented only. No reader of `PushPlans` beyond per-ID read and write. | push.rs:19-25 (doc); 527-531, 762 |
| Undo | Push is not undoable | undo.rs:186-187 |
| Ruk-internal refs | `refs/ruk/<alias>/` (remote helper); `refs/stash`; default notes `refs/notes/commits`; `refs/notes/ruk-attest` as a capability example | verbs_remote_helper.rs:276, 283, 312; verbs_branch.rs:5924; verbs_notes.rs:50; capability.rs:1254 |
| Attestations | Store rows, not refs. `ruk-attest` is an SSHSIG namespace. | types.rs:256; ruk-core/src/attestation/verify.rs:20 |
| Secret precedent | `ReconnectSecret`: "Never logged" | ruk-model.json `$defs.ReconnectSecret` |
| Typed op-log cursor | `op.log` with `OpLogArgs` (page, session) and `OpLogReply` (view, entries, oldest_retained). Entry fields [U]. | ruk-model.json `$defs.OpLogArgs`, `OpLogReply` |

---

## 2. Ruk gaps and required changes

Phases: **P0** is required before any mirror runs correctly. **P1** is required for the typed target and production hardening. **P2** is later.

**RC-1 (P0). Credential channel that reaches the wire, with ambient auth disabled per call.**
- Evidence: B2. Ambient headers are appended unconditionally in `request_headers`, so a repo `extraHeader` Authorization and the env header can both go out (smart_http.rs:159-172; http.rs:195-197 documents that both are sent). `live_headers` reads `std::env::vars()` (smart_http.rs:201-207) and is called for discover (250), ls-refs (296, 346), fetch (428, 575), and push POST (829).
- Change: add `--auth-stdin` to `push` and `ls-remote`. It reads one JSON record `{"username","password"}` from `CliEnv.stdin` (cli/mod.rs:82) and builds one explicit `authorization` header. Add an `AuthPolicy::{Explicit, Ambient}` parameter, threaded into `discover`, `discover_versioned`, `ls_refs_v2`, `fetch_pack_v2`, and `push_pack`. The explicit path replaces `live_headers`.
- Acceptance: with `GIT_AUTH_TOKEN` set and an explicit credential, the fake host sees exactly one `Authorization` header and no request goes to any other origin.

**RC-2 (P0). Fail-closed namespace policy on the network push path.**
- Evidence: B3. `require_push` has no caller (capability.rs:789).
- Change: filter `direct_tips` and `mirror_push_updates` output through a denylist: `refs/ruk/**`, `refs/stash`, `refs/pull/**`, `refs/merge-requests/**`, `refs/for/**`, `refs/keep-around/**`, `refs/pipelines/**`, `refs/environments/**`. Call `require_push` for every explicit update in `push_net`. Refuse deletes of hidden refs.
- Acceptance: `push --delete <url> refs/pull/1/head` is refused before any POST.

**RC-3 (P0 for ff-only). Per-ref fast-forward isolation.**
- Evidence: B4 (verbs_transport.rs:1365-1387).
- Change: a non-fast-forward spec becomes a per-ref `rejected: non-fast-forward` outcome, and the other specs proceed. Alternative: rukie pre-classifies with `merge-base --is-ancestor` and never sends the diverged spec.
- Acceptance: a batch with one diverged and one fast-forward ref pushes the fast-forward ref and reports the diverged one.

**RC-4 (P0). Honest error taxonomy.**
- Evidence: B6. `check_status` maps every non-2xx to `Offline` and drops the body (smart_http.rs:221-229). `ErrorDetail` has only a `push` variant (ruk-model.json `$defs.ErrorDetail`).
- Change:
  - `PushRejected` → `E_PUSH_REJECTED`, `PushUncertain` → `E_PUSH_UNCERTAIN`, `RemoteMoved` → `E_REMOTE_MOVED` (lib.rs:160-162).
  - 401/403 → `E_FORBIDDEN`; 404 → `E_NOT_FOUND`; 3xx → `E_REMOTE_MOVED` with `location`; 5xx and transport → `E_OFFLINE`.
  - Add `E_REMOTE_RATE_LIMITED` for 429 [M: a protocol addition; Ruk may change with breaking changes].
  - Add `ErrorDetail::Http { status, retry_after_seconds?, location?, body_preview? }`. `body_preview` is at most 512 bytes after URL and token scrubbing, so host messages such as GitHub's `GH0xx` text survive.
- Acceptance: unit tests per status code over a scripted listener, following the pattern at http.rs:1490-1540.

**RC-5 (P0). `Uncertain` only when bytes may have reached the server.**
- Evidence: B5.
- Change: a POST response with 4xx before any report body is `Rejected`, because receive-pack did not run. 5xx and transport loss after the body was written stay `Uncertain`. Apply the same rule in stateful `push_over` (push.rs:503-507).
- Acceptance: a 401 on the POST yields `PushState::Rejected`, not `Uncertain`.

**RC-6 (P0). Redaction and URL hygiene.**
- Evidence: `normalize_base` keeps userinfo (smart_http.rs:124-130). URLs are embedded in errors (smart_http.rs:139-151, 221-229). `push_net` stores the raw target bytes in the plan row (`PushPlanRecord::prepare(..., target.as_bytes().to_vec(), ...)`, verbs_transport.rs ~1470-1475; push.rs:152, 234).
- Change: the explicit-auth path rejects URLs with userinfo. Error and plan text is scrubbed of `scheme://user:pass@` and of the token value. Plan rows store the credential-free URL.
- Acceptance: a userinfo URL is refused; a canary token never appears in `push_plans`, errors, or stderr.

**RC-7 (P1). Redirects and trust roots.**
- Evidence: `get_to_writer` follows redirects silently and returns no final URL (http.rs:1091-1107). POST never follows (http.rs:973 doc). No extra CA (http.rs:411-417).
- Change: return the final URL from `get_to_writer`. Surface 3xx on POST as `E_REMOTE_MOVED` with `location`. Refuse cross-origin redirects on auth-bearing calls. Add optional per-call PEM roots for private-CA self-managed GitLab and Forgejo. Proxy support is P2.
- Acceptance: a 301 on the receive-pack POST returns `E_REMOTE_MOVED` with the new location.

**RC-8 (P1). Interrupt wired to the job lease.**
- Evidence: `Interrupt::none()` in CLI push (verbs_transport.rs:1523-1545). `PushPlanRecord` has no `Drop` guard (no `impl Drop` in push.rs), so a killed process leaves `Sending`.
- Change: the typed job passes a cancel handle. Lease loss cancels the in-flight push. Rukie treats any `sending` row found at startup as `Uncertain` (§11).
- Acceptance: cancelling mid-POST returns `E_CANCELLED`, and the plan is `Uncertain`.

**RC-9 (P1, target). Typed `push` and `push.reconcile` as transport jobs.**
- Evidence: B1. The pure `reconcile()` exists (push.rs:418-423). `PushReply` and `PushRefStatus` are in the schema (ruk-model.json).
- Change: `prepare` returns a transport-job spec for `Request::Push` instead of `E_HOSTED` (prepare.rs:283-288). The job runs `push_pack` or `push_over` with RC-1 auth and returns `PushReply` with per-ref `confirmed` or `rejected`. `push.reconcile` observes via ls-refs and feeds `reconcile()`. A new push to a ref is blocked while that ref's prior plan is `Uncertain`. This is the transport-job producer the advisory asks for; no lane preparation is needed.
- Acceptance: `push` over the daemon returns a populated `refs` list. `push.reconcile` returns verdicts `equal_new`, `equal_old`, or `moved`.

**RC-10 (P1). Remote-address guard.** Ruk dials every resolved address (http.rs `connect` doc) and follows GET redirects to any host. No private-range or loopback check is documented [U: confirm by reading `connect`]. Add a per-call allowlist, default-deny for private ranges, applied to resolution and redirects.

**RC-11 (P2). Atomic fallback.** Requesting `atomic` without server support is a hard error (smart_http.rs:712-714; push.rs:633-637). Rukie uses non-atomic for mirrors, so this is off the mirror critical path. Add `atomicity: best_effort` later.

**RC-12 (P2). Preflight and batching.** Closure size and largest blob are not reported before the POST. GitHub's 2 GiB pack limit and 100 MiB file limit therefore surface only as server rejections [R §1.3]. Add a closure-stats preflight and commit-batch splitting.

**Phase plan.** P0 is RC-1 through RC-6 on the CLI path, with credentials on stdin. P1 is RC-7 through RC-10, plus typed push (RC-9). P2 is RC-11, RC-12, SSH mirrors, LFS payloads, and proxy support.

---

## 3. Host matrix

Source: [R] §1–§3 and §5–§6, using the research note's citations. Limits are as published; I have not re-verified them.

| Host kind | Username for Basic | Write token | Refs refused or hidden | Limits and hints | [U] to verify |
|---|---|---|---|---|---|
| `github` (github.com, GHES) | `x-access-token` for App installation tokens [R §1.1]. Any non-empty name for PATs per a community report [M]. | App installation token with Contents:write (plus Workflows:write if workflow files change), or a fine-grained PAT scoped to one repo | `refs/pull/*` is read-only: "deny updating a hidden ref" [R §1.2] | 2 GiB per push. 100 MiB per file, warning at 50 MiB. ≤6 pushes/min/repo recommended. Secondary limit: 80 content-creating requests/min [R §1.3]. Renamed repos return 301 on GET [R §1.4]. | Whether `atomic`, `delete-refs`, and `report-status` are advertised. Exact text for protected branch, push protection (GH013), and size (GH001). |
| `gitlab` (gitlab.com, self-managed) | `oauth2` for PATs. Any non-blank name for project and group tokens [R §2.1]. | PAT with `write_repository`, or a project token with Developer or higher | `refs/merge-requests/*`, `refs/keep-around/*`, `refs/pipelines/*`, `refs/environments/*` [R §2.2] | Default branch is "fully protected": no force-push unless toggled [R §2.2]. 5 GiB per push on gitlab.com [R §2.3]. Auth ban: 403 for 15 min after 300 failed auth requests per minute from one IP [R §2.3]. `Retry-After` and `RateLimit-ResetTime` [R §2.3]. | `RateLimit-ResetTime` format. Force-push to the default branch with the toggle on. |
| `forgejo` (self-hosted) and `codeberg` | Token is the Basic password. Username is the token owner's login [M]. | PAT with `write:repository` [R §3.1] | `refs/pull/N/head` unless AGit; `refs/for/*` creates PRs [R §3.2] | Quota: HTTP 413 `quotaExceeded` [R §3.2]. Push-mirror minimum interval 10 min on Forgejo [R §3.3]. | Codeberg numeric limits are undisclosed [R §3.3]. |
| `generic` | Required, user-supplied | Required | Ruk denylist only | Conservative defaults | — |

**Codeberg policy gate.** Codeberg's Terms §2(1)7 prohibit projects "that mostly consist of code written by 'generative AI'-tools (including ... *Claude*, *OpenAI Codex*)" [R §6]. The Codeberg blog calls autonomous LLM-agent projects unwelcome [R §6]. Therefore `host_kind=forgejo` on a codeberg.org host is refused unless `policy_ack=true` is set, and a warning is returned. It is never enabled in bulk.

**Auth header.** Always `Authorization: Basic base64(username:token)`. Basic is the form the research shows working on all three hosts [R §1.1, §2.1, §3.1].

---

## 4. Mirror configuration

| Field | Type, default | Rule |
|---|---|---|
| `id` | UUID | Server-generated |
| `repo_id` | ref | Owner repo |
| `name` | string | 1–64 chars, `[a-z0-9-]`, unique per repo |
| `target_url` | string | `https://` only. No userinfo, query, or fragment. Trailing `/` removed. Blocked if the host resolves to a private, loopback, or link-local address, unless the operator allowlist permits it. Re-checked every run, as Forgejo does [R §5.1]. |
| `host_kind` | enum, required | `github` \| `gitlab` \| `forgejo` \| `generic`. Never inferred silently. |
| `username` | string | Default by host: `github`→`x-access-token`; `gitlab`→`oauth2`; `forgejo`→required; `generic`→required |
| `credential` | write-only | Stored per §5. The API returns `last4` and timestamps only. |
| `mode` | `ff_only` \| `exact`, default `ff_only` | `exact` requires a confirmed dry-run when the plan contains deletes or the target is non-empty (§8, S3). |
| `ref_selection` | `all` \| `default_branch_tags` \| `globs`, default `default_branch_tags` | Globs use Ruk's `RefPattern` grammar: whole-segment `*`, trailing `**` [V ruk-model.json `$defs.RefPattern`] |
| `include_notes` | bool, false | Adds `refs/notes/**` to the selection, subject to §6 |
| `protect_default_branch` | bool, true | Exact mode never deletes the source default branch or the remote HEAD target |
| `enabled` | bool, true | Disabled mirrors get no triggers and no reconcile |
| `sync_on_ref_update` | bool, true | Enables the event trigger (§7) |
| `debounce_seconds` | int, 15 (5–300) | §7 |
| `max_delay_seconds` | int, 300 (30–3600) | §7 |
| `reconcile_interval_seconds` | int, 3600 (300–604800), ±10% jitter | §7 |
| `create_if_missing` | bool, false | Requires a credential with create scope. Creates an empty repo once: GitHub `POST /user/repos` or `/orgs/{org}/repos` with `auto_init:false`; GitLab `POST /projects`; Forgejo `POST /user/repos` [R §1.4, §2.4, §3.4]. Whether a GitHub App token can create repos under a user account is [U]. |
| `policy_ack` | bool | Required for codeberg.org |
| `adopt_existing_refs` | bool, false | Required to create an `exact` mirror whose target already has refs |

**Ref selection semantics**
- `all`: `refs/heads/**` and `refs/tags/**`.
- `default_branch_tags`: `refs/heads/<source default>` and `refs/tags/**`.
- `globs`: the listed patterns, intersected with the denylist.

The denylist in §6 always applies after selection.

---

## 5. Credentials

**Storage.** Table `mirror_credentials`: `mirror_id` (PK), `key_version`, `nonce` (12 bytes), `ciphertext` (includes the GCM tag), `last4` (plaintext, for display), `created_at`, `rotated_at`, `last_used_at`.

**Crypto.**
- AES-256-GCM via `crypto/aes` and `crypto/cipher` (stdlib). Each record uses a fresh 12-byte nonce from `crypto/rand`.
- Key: HKDF-SHA256 over the `SECRET_KEY` bytes, info `rukie/mirror-cred/v1`, empty salt, via `crypto/hkdf` (stdlib since Go 1.24) [M].
- AAD: `"rukie/mirror-cred/v1|" + mirror_id + "|" + host_kind + "|" + key_version`. A row moved to another mirror fails authentication.
- Decryption failure → `secret` class, `needs_attention`. Never fall back to another key.

**Why not the existing helper.** `rukie/modules/secret/secret.go:18-34` uses AES-CFB with a random IV and base64-wrapped plaintext, with no MAC. Tampering is undetected and the mode is malleable. `EncryptSecret` (secret.go:58-61) keys with plain SHA-256 of the secret. Do not reuse either.

**Key source.** `SECRET_KEY_URI=file://…` through the existing `loadSecret` [V rukie/modules/setting/security.go:85-114, 147]. Refuse to store or decrypt credentials when the key is empty, or when the file is missing or group- or world-readable. The fallback comment at security.go:149-150 describes a default value; do not accept it for credential encryption. Rotation: increment `key_version`, keep old keys until a background job re-encrypts every row.

**Lifetime.** Decrypt inside a single attempt. Hold the plaintext in a `[]byte` and zero it after use (best effort in Go). Pass it to Ruk only out-of-band:
- Phase 0: stdin frame (RC-1).
- Phase 1: a session-scoped auth handle, never a field in `args`. The mutation digest covers `args` [V ruk-model.json `$defs.Mutation26` description], so a secret in `args` would enter the idempotency digest.

**Never** stored in mirror, job, plan, or attempt rows. Never logged. Never in errors, events, or webhook bodies. Never in API responses. Never in argv or environment of Ruk commands.

**Redaction.**
1. The `slog` handler replaces attributes whose key matches `token|secret|password|authorization|cred` with `[REDACTED]`.
2. The error sanitizer runs on every error string and Ruk stderr line before persistence. It removes `scheme://user[:pass]@`, `Authorization:` header values, and any byte sequence equal to the in-memory token.
3. Event payloads include the host only, never the full URL.

**Rotation.** Explicit only, via `mirror.update` with a new token. Never rotate automatically. Never retry with a different username. An `auth_failed` mirror stops automatic retries (§10).

**Deletion.** `mirror.delete` removes the ciphertext row and the mirror. It does not touch remote refs in v1.

**Probe.** `mirror.probe` runs `ls-remote --auth-stdin` against the receive-pack discovery endpoint and reports `auth_ok` and advertised capabilities. It stores nothing. Write scope is confirmed by the first sync [U].

---

## 6. Ref selection and namespace policy

| Namespace | Default | Source |
|---|---|---|
| `refs/heads/**` | Included by `all`, `default_branch_tags`, and `globs` | — |
| `refs/tags/**` | Included by `all` and `default_branch_tags`. Exact mode prunes tags absent locally. | — |
| `refs/notes/**` | Excluded unless `include_notes` is set and the pattern is listed in globs | verbs_notes.rs:50 (default notes ref) |
| `refs/stash` | Always denied | verbs_branch.rs:5924 |
| `refs/ruk/**` | Always denied | verbs_remote_helper.rs:276-312 |
| `refs/notes/ruk-attest` | Always denied | capability.rs:1254 |
| `refs/pull/**`, `refs/merge-requests/**`, `refs/for/**`, `refs/keep-around/**`, `refs/pipelines/**`, `refs/environments/**` | Always denied | [R §1.2, §2.2, §3.2] |
| Symbolic refs (HEAD) | Never mirrored | mirror.rs doc: "symbolic refs never enter a mirror update" |
| Ruk change refs | Not mirrored. `refs/heads/agent/**` only through explicit globs. | — |

**Order of application.** Selection, then denylist. Names that fail `check-ref-format` are rejected at config time. For the remote side, a remote ref outside the selection or inside the denylist is never updated or deleted, and is counted as `ignored_hidden`.

Attestations are store rows, not refs [V types.rs:256], so mirrors do not carry CI results.

---

## 7. Triggers, debounce, reconcile

**Event source.** Ruk's `op.log` cursor [V ruk-model.json `$defs.OpLogArgs`, `OpLogReply`] is the change feed. Poll every 2 s, starting from the stored cursor. Each operation that moves a ref touches every enabled mirror of that repo. Entry field names are [U]. Fallback: rukie's own write paths also touch mirrors, and periodic reconcile covers writes that bypass the feed.

**Touch.** `touch(mirror, now)`:
- Set `dirty_since` if null.
- Set `last_event_at = now`.
- `run_after = min(last_event_at + debounce, dirty_since + max_delay)`.
- Clamp `run_after >= last_attempt_at + host_spacing` (§10).
- One queued job per mirror. A unique partial index enforces this.

**Periodic reconcile.** `next_reconcile_at = last_success_at + interval`, jittered ±10%. Reconcile runs the same algorithm and pushes only differences.

**Manual.** `mirror.sync` sets `run_after = now` with high priority. It still respects host spacing and concurrency. Manual syncs are limited to one per 60 s per mirror.

**Startup.** Scan for mirrors with `dirty_since` set or `next_reconcile_at <= now`.

---

## 8. Sync algorithm

Inputs: mirror config, decrypted credential, lease.

**S0. Claim and validate.** Claim the job (§11). Re-validate the URL and SSRF rules. Refuse Codeberg without `policy_ack`. Decrypt. A failure yields the `policy` or `secret` class (§9).

**S1. Remote snapshot.** Run `ls-remote --heads --tags --auth-stdin <url>` (Phase 0), or read the typed reply (Phase 1). Build `R = {ref → oid}` from direct refs only. Symbolic entries are used only for the remote HEAD target. Drop refs outside the selection and denylist into `ignored_hidden`, for counting only. Peeled `^{}` duplicates are already dropped [V ruk-git/src/transport/net.rs `to_local_refs` doc].

**S2. Local snapshot.** Run `for-each-ref --format='%(refname) %(objectname) %(objecttype)'` (dispatch.rs:304; the format fields are listed in STATUS.md). Build `L = {ref → oid}` from direct refs, filtered by selection and denylist. For annotated tags, `objectname` should be the tag object, matching what the advertisement lists [U: confirm for-each-ref does not peel].

**S3. Plan.**
- `create`: `L \ R` → `(old = zero, new = L[ref])`.
- `update`: `L ∩ R` where `L[ref] ≠ R[ref]` → `(old = R[ref], new = L[ref])`.
  - In `ff_only`, first check `merge-base --is-ancestor R[ref] L[ref]` (dispatch.rs:451; state.rs:1462). A false result, or `R[ref]` missing locally, gives status `diverged`, and nothing is sent.
  - In `exact`, no check; the update is forced.
- `delete` (exact only): `R \ L`, restricted to the selection, minus the denylist, minus the source default branch and remote HEAD target when `protect_default_branch` is set.
- `skip`: equal.
- Exact mode with a non-empty target and no `adopt_existing_refs` is refused. The first exact sync with any deletes requires a dry-run digest (`confirm_digest`).

**S4. Ordering and D/F conflicts.** Git cannot hold `refs/heads/a` and `refs/heads/a/b` together. Deletes run before creates. If a delete fails, creates that depend on it become `blocked_df`.

**S5. Execute.** Two non-atomic invocations, deletes first.
- Invocation A (deletes, exact only): `push --no-verify --delete --auth-stdin <url> <ref>...`. `--no-verify` skips the local pre-push hook [V verbs_transport.rs:1243-1261 `no-verify` flag; hook call in push_net near 1488-1497].
- Invocation B (creates and updates): `push --no-verify --auth-stdin <url> +<ref>:<ref>...` in exact mode, and without `+` in ff-only mode. A per-spec `+` forces only that spec [V verbs_transport.rs:1357-1359].
- Batch cap: 200 refs per invocation.
- Never pass `--mirror`, global `--force`, or `--atomic`.
- Each invocation gets a fresh Ruk `request_id` per attempt (§11).

**S6. Results.** For each ref:
- `ok`: confirmed; record the new remote oid.
- `rejected(reason)`: classify (§9).
- Whole-invocation error: every ref in it becomes `pending` with the error class. For `uncertain`, go to S7.

CLI output lines are `ok <ref>` and `rejected <ref>: <reason>` [V verbs_transport.rs:1653-1673]. On rejection, parse the error detail. Phase 1 reads `PushReply.refs` directly.

**S7. Uncertain.** Re-run S1 to observe. For each uncertain ref:
- observed equals new → `ok`, no resend;
- observed equals old → resend in a new attempt;
- observed is another value → `exact`: re-plan using the fresh value as CAS. `ff_only`: `diverged`, needs attention.

**S8. Finalize.**
- Job state: `succeeded` (all desired refs in sync or ok), `partial` (some refs rejected, others ok), or `failed` (transport, auth, or other whole-job error).
- `last_attempt_at` always updates.
- `last_success_at` updates only on `succeeded`.
- `last_error` is set or cleared.
- Write `mirror_refstate` rows.
- Emit `mirror.sync.completed` if the state or any per-ref status changed.

**S9. Schedule.** Apply §10.

---

## 9. Result capture and status model

**Mirror state:** `idle`, `queued`, `running`, `ok`, `partial`, `failing` (transient, will retry), `auth_failed`, `needs_attention`, `disabled`.

**Per-ref status:** `in_sync`, `pending`, `ok`, `rejected`, `diverged`, `blocked_df`, `ignored_hidden`, `deleted`, `delete_rejected`. Fields: local oid, remote oid, reason, reason class, first failed at, last attempt at, last success at, consecutive failures.

**Last error:** class, sanitized message (at most 512 bytes), HTTP status if any, Ruk code if any, observed at, retry at.

**Classification.** Inputs are the Ruk error code, the HTTP status (via the RC-4 detail), `Retry-After` and `x-ratelimit-*` headers, and per-ref reason text matched against a per-host table. Unknown text becomes `ref_other`, with the verbatim message kept.

| Signal | Class | Retry | Mirror state |
|---|---|---|---|
| `E_FORBIDDEN` (401/403), no rate-limit headers | `auth` | No | `auth_failed` |
| 429, or 403 with `Retry-After` or `x-ratelimit-remaining: 0` | `rate_limited` | Yes, honoring the delay | `failing` |
| `E_NOT_FOUND` (404) | `repo_missing` | Once, only if `create_if_missing` | `needs_attention` |
| `E_REMOTE_MOVED` (3xx) | `repo_moved` | No | `needs_attention` |
| 5xx | `server` | Yes | `failing` |
| `E_OFFLINE` without status, or timeout | `network` | Yes | `failing` |
| `E_PUSH_UNCERTAIN`, or a `sending` row found at startup | `uncertain` | After observation | `failing` |
| Per-ref reason: hidden-ref text (e.g., "deny updating a hidden ref") | `ref_hidden` | No; indicates a selection bug, so alert | `partial` |
| Per-ref reason: protected branch or ruleset (GitLab "Allowed to force push", Forgejo "Not allowed to push to protected branch", GitHub protected-branch text [U]) | `ref_protected` | No, until the ref changes or reconcile runs. Three identical reasons → `needs_attention`. | `partial` |
| Per-ref reason: push protection or secret scanning (GH013-like [U]) | `ref_policy` | No | `partial` |
| Per-ref reason or HTTP 413: "exceeds", "file size limit", "pack exceeds" | `too_large` | No | `needs_attention` |
| Per-ref reason: hook declined | `ref_hook` | No | `partial` |
| Per-ref other | `ref_other` | No | `partial` |
| `E_CORRUPT`, grammar errors | `protocol` | 3 attempts, then `needs_attention` | `failing` → `needs_attention` |
| Local ff check false (ff-only) | `diverged` | No | `partial` |
| URL, scheme, SSRF, or Codeberg policy failure | `policy` | No | `needs_attention` |
| `E_CAPACITY`, or lease lost (`E_CANCELLED`) | `busy` | Yes, short delay | `failing` |
| Secret decryption failure | `secret` | No | `needs_attention` |

---

## 10. Retry, backoff, host limits, stop rules

**Backoff** (for `network`, `server`, `protocol`, `busy`):
- `delay_n = min(cap, base * 2^(n-1))`, base 30 s, cap 60 min.
- Sleep uses full jitter: `U[0, delay_n]`.
- `consecutive_failures` resets on success.

**`rate_limited`:** sleep is `max(Retry-After, 60 s if absent) + U[0, 10%]`. `Retry-After` is either seconds or an HTTP date. Cap at 6 h. GitLab's `RateLimit-ResetTime` is parsed the same way [U: format].

**Circuit breaker** per target: 5 consecutive `network` or `server` failures open the circuit for 15 min, doubling up to 6 h. Half-open allows one `ls-remote` probe.

**Failure cap:** 8 consecutive failures cap the interval at 6 h, until success or manual sync.

**Stop rules (no automatic retry):** `auth`, `repo_missing` (after one create attempt), `repo_moved`, `too_large`, `policy`, `secret`, `ref_protected` repeated three times with the same reason, and `protocol` after three attempts. Automatic retries also never rotate credentials, never switch usernames, and never follow redirects on auth-bearing requests.

**Host spacing and concurrency**

| Host | Min spacing per target | Concurrent per host | Notes |
|---|---|---|---|
| github | 10 s | 2 | ≤6 invocations/min per target, as recommended [R §1.3] |
| gitlab | 5 s | 2 | |
| forgejo | 5 s | 2 | |
| codeberg | 10 s | 1 | Limits undisclosed [R §3.3] |
| generic | 10 s | 1 | |

Global: 4 mirror workers and 2 concurrent Ruk invocations. Per target: one job at a time.

---

## 11. Durability, leases, idempotency, recovery

Store-neutral. Requirements: conditional update (compare-and-set) and a unique partial index. Works with the open store candidates.

**Tables**
- `mirrors`: config (§4), `last_success_at`, `last_attempt_at`, `next_run_at`, `state`, `consecutive_failures`, `last_error`, `event_cursor`.
- `mirror_credentials`: §5.
- `mirror_jobs`: `id`, `mirror_id`, `kind` (`sync` | `reconcile` | `manual` | `probe`), `state`, `run_after`, `dirty_since`, `last_event_at`, `attempt_no`, `lease_owner`, `lease_gen`, `lease_expires_at`, `ruk_request_ids[]`, `started_at`, `finished_at`, `outcome_class`, `outcome_summary` (no secrets).
- `mirror_refstate`: `mirror_id`, `ref`, status fields (§9).
- `mirror_attempts`: audit row per attempt; Ruk plan IDs in Phase 1.
- `mirror_events`: outbox for `mirror.sync.completed`, keyed by `(mirror_id, job_id)`.

**Lease**
- Claim: `UPDATE mirror_jobs SET state='running', lease_owner=?, lease_gen=lease_gen+1, lease_expires_at=now+60s WHERE id=? AND state='queued' AND run_after<=now`. Rows affected = 1 means claimed.
- Renew every 20 s with `WHERE lease_owner=? AND lease_gen=?`. Renewal failure cancels the in-flight Ruk call (RC-8).
- Every finishing write is fenced by `lease_gen`. A stale worker's write matches zero rows, and that worker stops.
- The lease TTL (60 s) must be shorter than the worst-case push. Cancel-on-lease-loss in RC-8 enforces this on the Ruk side.

**Idempotency**
- One Ruk `request_id` per invocation per attempt. Retries of the same attempt reuse it verbatim. `CommandOpen` replays the recorded terminal report for a reused id rather than running the command twice [V daemon/mod.rs:390-411]. The digest covers argv, directory, and env [V same].
- Retries must keep argv identical. The credential is on stdin, which is outside the digest, so a rotation during a replay window returns the old recorded report. This is acceptable and documented.

**Recovery at startup**
- Any job `running` with an expired lease → treat as `uncertain`. Run S7 before any new push.
- Any attempt with an invocation started but no terminal record → `uncertain`.
- Any Ruk plan in `sending` (Phase 1) → `uncertain`.

---

## 12. Operations

Each row is an `Op[In,Out]` in the single operation table. The same table generates HTTP (Huma, OpenAPI 3.1, RFC 9457 problems), MCP tools, the CLI, and zero-JS HTML forms.

| Op | In | Out | Permission | Notes |
|---|---|---|---|---|
| `mirror.create` | repo, name, target_url, host_kind, username?, token (write-only), mode, ref_selection, include_notes, protect_default_branch, enabled, sync_on_ref_update, debounce_seconds, max_delay_seconds, reconcile_interval_seconds, create_if_missing, policy_ack, adopt_existing_refs | mirror (no token; `last4`), probe result | `repo:admin` | Runs probe. Refuses exact mode over a non-empty target without `adopt_existing_refs`. |
| `mirror.update` | id, partial fields, token? (rotates) | mirror | `repo:admin` | Rotation is explicit. |
| `mirror.delete` | id, `prune_remote` (must be false in v1) | deleted | `repo:admin` | Remote refs untouched. |
| `mirror.sync` | id, `dry_run`, `confirm_digest`?, `mode` (now or reconcile) | job_id, run_after; dry run: planned create, update, delete, diverged, ignored (paged), plan digest | `repo:write` | Manual limit 1/min. |
| `mirror.status` | id, include_refs, page | state, last_success_at, last_attempt_at, next_run_at, consecutive_failures, last_error, credential {last4, created_at, last_used_at}, refs (paged) | `repo:read`; credential fields need `repo:admin` | |
| `mirror.list` | repo, page | summaries | `repo:read` | |
| `mirror.probe` | target_url, host_kind, username, token | auth_ok, capabilities, default_branch? | `repo:admin` | Stores nothing. |

**Events.** `mirror.sync.completed` carries `{mirror_id, job_id, state, counts{ok,rejected,diverged,deleted}, errors[{ref,class,message}]}` and is signed with Standard Webhooks. It never contains a token or a URL with credentials.

**Surfaces.** HTML forms POST to the same routes. Status renders as HTML and Markdown. CLI: `rukie mirror sync <id> --json`.

---

## 13. Ruk integration contracts

**Phase 0 (CLI, after RC-1 through RC-6).** Each call goes through `CommandOpen`, with `argv`, `dir`, a hermetic `env`, and stdin carrying the credential record.
- `ls-remote --heads --tags --auth-stdin <url>` [V dispatch.rs:416; verbs_transport.rs:4204-4250].
- `push --no-verify --auth-stdin --delete <url> <ref>...`.
- `push --no-verify --auth-stdin <url> [+]<src>:<dst>...`.
- `for-each-ref --format=...` [V dispatch.rs:304].
- `merge-base --is-ancestor` [V dispatch.rs:451].
- Output: `ok <ref>` and `rejected <ref>: <reason>` lines, parsed from the error detail. Exit status maps through RC-4.

**Phase 1 (typed, after RC-9).**
- `push` with `PushArgs {remote: {url:{url}}, updates: [{remote_ref, source: {commit:{oid}} | "delete", expected_old: {present:{oid}} | "absent", force}], atomicity: "non_atomic"}` returns `PushReply {plan, state, refs: [{remote_ref, status: {confirmed:{new}} | {rejected:{reason}} | "uncertain"}]}` [V ruk-model.json `$defs.PushArgs`, `PushUpdate`, `PushReply`, `PushRefOutcome`, `PushRefStatus`]. Auth goes out-of-band (§5), never in `args`.
- `push.reconcile` with `{plan}` returns `observations: [{remote_ref, verdict: equal_new|equal_old|moved, observed}]` [V ruk-model.json `$defs.PushReconcileArgs`, `ReconcileObservation`, `ReconcileVerdict`].

---

## 14. Tests

**A. Rukie unit tests**
- Config validation table: userinfo, `http://`, private address, Codeberg without `policy_ack`, duplicate name, invalid glob grammar.
- Ref selection and denylist: property tests over random ref names.
- Plan computation, table-driven over fake `R` and `L`: create, update, ff versus diverged, delete with default-branch protection, D/F ordering, hidden remote refs untouched, adopt gate, dry-run digest.
- Classifier: every row of the §9 table, plus unknown text.
- Backoff math: bounds, `Retry-After` precedence, cap, circuit transitions, failure cap.
- Lease CAS: concurrent claimers (exactly one wins); a stale finish is rejected.
- AEAD: round trip; AAD swap fails; key-version mismatch fails; ciphertext tamper fails; missing key fails closed.
- Redaction: canary token absent from `slog` output, error strings, and JSON.

**B. Ruk unit tests (ruk-git)**, using the scripted-listener pattern at http.rs:1490-1540 and the smart-HTTP ls-remote test at cli/tests.rs:2157:
- 401 on POST → `Rejected`, not `Uncertain` (RC-5).
- 429 with `Retry-After` → `E_REMOTE_RATE_LIMITED` with the delay (RC-4).

[Showing lines 558-1145 of 1233 (49.9KB limit). Use :1146 to continue]
- 429 with `Retry-After` → `E_REMOTE_RATE_LIMITED` with the delay (RC-4).
- 301 on the receive-pack POST → `E_REMOTE_MOVED` with location (RC-7).
- With explicit auth, `GIT_AUTH_TOKEN` is not sent, and only one `Authorization` header goes out (RC-1).
- Error text contains no userinfo (RC-6).
- `E_PUSH_*` code mapping in `lib.rs` (RC-4).
- `push --delete refs/pull/1/head` refused before any POST (RC-2).
- Non-ff spec in a batch: other specs proceed (RC-3).
- The existing test at push_persistence.rs:136-139 (`Uncertain` on a truncated report) must still pass.

**C. Integration against a local fake host**

The fake host is a Go `net/http` server:
- Basic-auth middleware: 401 when the credential is missing, 403 on a bad token.
- Git backend via `net/http/cgi` running `git http-backend`, with `GIT_PROJECT_ROOT`, `GIT_HTTP_EXPORT_ALL=1`, and `REMOTE_USER` set, which enables receive-pack [M: confirm against git-http-backend docs]. Repos set `http.receivepack=true`.
- Hidden refs: `receive.hideRefs=refs/pull`, so pushes to `refs/pull/*` fail with "deny updating a hidden ref" [M].
- Protected branch: a pre-receive hook rejects non-ff updates and deletions on `refs/heads/main` with GitHub-like text.
- Size: a pre-receive hook rejects blobs over N bytes.
- Rate limit: middleware returns 429 with `Retry-After` on the first N receive-pack POSTs.
- Redirects: middleware returns 301 for `info/refs` and for the receive-pack POST.
- Slow body: stall after half of the request body, to exercise timeouts.

Scenarios:
- Exact: a remote-only tag is deleted; a hidden ref is untouched; the default branch is protected; an annotated tag keeps its tag-object oid.
- ff-only: a diverged ref is not sent; fast-forward refs are pushed; creates work.
- D/F: the source deletes `refs/heads/a` and creates `refs/heads/a/b` in one sync.
- Crash: kill Ruk mid-POST. Assert the job becomes `uncertain`. On reconcile, `equal_old` → resend; `equal_new` → no resend. Count receive-pack POSTs on the fake host; expect no double-apply.
- Idempotent retry: drop the daemon connection after the terminal frame, then retry with the same `request_id`. The fake host receives exactly one POST.
- Auth stop: 403 → `auth_failed`, with zero further POSTs until `mirror.update` or a manual sync.
- Rate budget: 200 mirrors with 10 coalesced events per minute. Count POSTs per target per minute and assert ≤6 invocations.

**D. Model and invariant tests.** Random sequences of local commits, deletes, and tags, plus third-party remote changes. After each successful exact sync, `R == L` on the selection. After an ff-only sync, every remote ref in the selection equals local or is flagged diverged. Invariant: no ref outside the selection is ever written or deleted on the fake host (snapshot diff).

**E. Secret leakage.** Run a canary token through every path, including failure injection. Grep the database dump, logs, API JSON, webhook bodies, and captured Ruk stderr. Expect zero matches.

**F. Live smoke (manual, env-gated).** Real test repos on GitHub, GitLab, and Forgejo. Record advertised capabilities (`report-status`, `delete-refs`, `atomic`, `side-band-64k`) as fixtures. Record exact messages for hidden ref, protected branch, push protection, and size. Codeberg is excluded unless policy-acknowledged.

---

## 15. Open items before GA

1. Per-host advertised capabilities: `report-status`, `delete-refs`, `atomic`, `side-band-64k`, `ofs-delta` (probe F).
2. Exact rejection text per host for hidden refs, protected branches, push protection, file size, and pack size (probe F). GitHub GH0xx codes are from memory of GitHub docs [M]; not verified here.
3. GitLab `RateLimit-ResetTime` format, and whether force-push to the default branch is accepted when the toggle is set.
4. Whether GitHub App installation tokens can create repos under a user account [R §1.4 marks this unverified].
5. Whether `for-each-ref` objectname for annotated tags matches the advertised tag object (S2).
6. Whether `resolve_rev` in `build_push_updates` peels annotated tags (affects the tag oid sent in S5).
7. Whether the RC-10 private-address guard already exists in `connect` (read the function body).
8. `op.log` entry shape for the change feed.
9. Whether `ls-remote` returns a `symref=HEAD:` capability for remote default-branch detection.
10. Codeberg policy and ToU wording, as of the research date [R §6].
11. Whether Ruk's stored `CommandOpen` digest or request-id receipts persist argv or env in plaintext. Expected not [U].
12. Behavior of `receive.hideRefs` on the fake host, and of GitHub's hidden refs [M].

---

### Critical Files for Implementation

- `ruk/crates/ruk-git/src/transport/smart_http.rs`: explicit-auth path, `live_headers` replacement, `check_status` body capture, 4xx-before-body classification (RC-1, RC-4, RC-5, RC-6).
- `ruk/crates/ruk-git/src/transport/push.rs`: plan state machine and `Uncertain` semantics; exposing `reconcile` (RC-5, RC-8, RC-9).
- `ruk/crates/ruk/src/cli/verbs_transport.rs`: `push`, `push_net`, fast-forward isolation in `build_push_updates`, namespace filter, `--auth-stdin`, `ls-remote` headers (RC-1, RC-2, RC-3, RC-6).
- `ruk/crates/ruk-core/src/coord/prepare.rs` and `hosted.rs`: typed `push` refusal, to be replaced by a transport-job producer (RC-9).
- `ruk/crates/ruk-git/src/lib.rs`: error-code mapping for push and HTTP errors (RC-4).

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Fix §11 lease rule: TTL must excee…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Remove citations you did not read;…

[advisor] <advisory advisor="default" severity="nit" guidance="weigh, don't blindly obey"> Cut §12 surfaces the ask does not name…

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Fix ref semantics before fina…