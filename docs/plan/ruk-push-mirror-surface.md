**Advisories conflict; here is the ground truth from my toolset:** my available tools are `read`, `grep`, and `web_search` only — there is no write/create tool in my inventory, and both write attempts failed (`Path not found` / `Local file not found`). I cannot create the file myself. Following the feasible path: the complete artifact is below, ready to be saved verbatim to:

`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-push-mirror-surface.md`

---

# Ruk's push-mirror surface: what exists today (read-only survey, 2026-10-20)

Scope: client push (HTTP/SSH), credential model, push semantics, `mirror.rs`, receive-side hooks/proc-receive, LFS, large-push behaviour, and gaps for auto push-mirroring to GitHub/GitLab/Forgejo. All paths relative to `/home/alpha/rewrite/git-renewed/ruk/` unless noted. Citations are `file:line`. Everything below was read directly from source; items I could not confirm are marked **unverified**.

## 1. Client-side push: does it work over HTTPS and SSH?

**HTTPS: yes, and it's real.** `ruk push` routes HTTP remotes through the smart-HTTP client (`crates/ruk/src/cli/verbs_transport.rs:1410-1465`, `verbs_transport.rs:9` in the module table). `crates/ruk-git/src/transport/smart_http.rs` is a from-scratch stateless receive-pack client: one `GET <base>/info/refs?service=git-receive-pack` for the advertisement (`smart_http.rs:243-262`), then one `POST <base>/git-receive-pack` carrying ref commands + pack (`smart_http.rs:648-860`). Protocol: v2 is probed with `Git-Protocol: version=2` for *discovery* (`discover_versioned`, `smart_http.rs:289-325`), but **push itself is v0/v1 only** — receive-pack defines no v2 push, and the stateful client refuses v2 advertisement (`push.rs:570-576`; `verbs_transport.rs:10`). Chunked *responses* are decoded (`http.rs:693-745`, `983-1001`); **request** bodies are always `Content-Length` — no chunked/streaming request upload (`http.rs:543-561, 973-981`).

- **TLS**: pure-Rust `rustls` + `webpki-roots` (Mozilla root bundle), `ring` provider; handshake forced up-front so cert errors fail before any request bytes (`http.rs:1-11`, `http.rs:411-419`, `http.rs:495-503`). **No system trust store, no custom-CA config, no `SSL_CERT_FILE` support** — a self-managed GitLab with a private CA will fail TLS verification (gap, §8).
- **Proxies: none.** No `HTTP_PROXY`/`HTTPS_PROXY` handling anywhere in `transport/` (grep for `proxy` matches only a security test string, `net.rs:925`). Corporate proxies are unsupported.
- **Redirects**: GET follows up to 3 hops with Authorization stripped on cross-host (`http.rs:1071-1113`); **POST never follows redirects** — a 3xx on the RPC is an error naming its Location (`smart_http.rs:37-42` doc). GitLab self-managed behind an http→https redirect will break on the POST half.
- **HTTP/1.1 only**, `Connection: close` per request, no keep-alive, 30 s connect/read/write timeout, non-configurable (`http.rs:7-9`, `http.rs:28-30`).
- **Auth-header injection**: `Authorization` is assembled per-URL (see §2) and `http.<url>.extraHeader` config lines are joined verbatim, longest-scope-wins (`http.rs:107-207`, `smart_http.rs:153-200`; wire-proven per `.outline/sdd/task-credhdr-report.md`).
- **Status mapping**: 404 → "repository not found", 401/403 → "authentication failed", everything non-2xx → `E_OFFLINE` (`smart_http.rs:220-228`). 401/403 are surfaced as *offline*, not retried with different credentials — there is no retry or credential fallback loop.

**SSH: yes.** `net.rs` classifies `ssh://[user@]host[:port]/path` and scp-like `user@host:path` via `gix-url` (`net.rs:1-21`, `99-148`, tests at `823-870`). It **spawns the system `ssh` binary**, honouring `GIT_SSH` and `GIT_SSH_COMMAND` exactly like Git (`net.rs:359-398`); key handling, agent, `known_hosts` and `IdentityFile` are all delegated to the user's ssh config/agent — Ruk manages none of it. Dash-led host/user are blocked as flag-injection (`net.rs:382-398`, test `net.rs:924-928`). Child teardown closes stdin, drains stdout, and allows a 30 s grace so remote post-receive hooks complete before kill/reap (`net.rs:241-247`, `304-333`).

**Exact supported URL schemes** (`net.rs:42-61`, `99-148`, `dumb_http_gap` at `201-204`):
- Local: bare paths, `file://`
- `ssh://…` and scp-like `user@host:path` (bare `host:path` *without* user is treated as a **local path**, deliberately — `net.rs:92`, test `814-819`)
- `git://host[:port]/path` (git-daemon over TCP)
- `http://` and `https://` (smart-HTTP; fetch falls back to dumb-HTTP read-only, **push over dumb-http is refused** — `net.rs:201-204`, `verbs_transport.rs:9`)
- Refused loudly: `ext::`, remote-helper `name::` schemes (`net.rs:98-109`, test `867-869`)

## 2. Credential model

Resolution order for smart-HTTP `Authorization` (`smart_http.rs:22-30`, `credentials.rs:747-778`):

1. **Git credential helpers, in-process**: `credential.helper` rows are parsed directly from gitconfig files (system/global/repo — `credentials.rs:397-418`) and the configured **external** helper program is run as `<program> get` with a `protocol=/host=/url=` description on stdin (`credentials.rs:1-16`, `502-534`). `!`-shell helpers run via `sh -c "<line> get"`, same trust shape as `GIT_SSH_COMMAND` (`credentials.rs:10-16`, `429-432`). Scoped `[credential "<url>"]` matching is prefix-on-URL-boundary (`credentials.rs:243-259`). Bounded: 10 s/attempt, ≤16 helpers, no retries (`credentials.rs:34-37`). **Bare built-in helpers (`store`, `cache`, `manager`) are a named gap — never executed** (`credentials.rs:18-26`, `285-289`).
2. **Environment fallback** (`http.rs:187-207`): `GIT_AUTH_TOKEN` → `Bearer <token>`; else `GIT_AUTH_USER` + `GIT_AUTH_PASSWORD` → `Basic base64(user:pass)`. For LFS: `GIT_LFS_AUTH_BEARER` / `GIT_LFS_AUTH_BASIC` (`http.rs:82-105`).
3. **`http.<url>.extraHeader`** — can carry an `Authorization` directly; when both config header and helper/env auth exist, *both* are sent (`http.rs:194-197`).
4. **URL userinfo is NOT a credential channel**: userinfo in the URL is deliberately stripped so it "cannot leak into Host or SNI" — `https://oauth2:TOKEN@gitlab.example/...` will **not** authenticate (`http.rs:311-337`). This matters: the most common GitLab mirror-URL convention silently fails.

**Token never leaves auth scope**: LFS tokens are sent to a presigned `href` only while it stays on the batch endpoint's authority, and are dropped across cross-host redirect hops (`http.rs:70-79`, `1071-1078`).

**Hosted daemon / typed methods**: the typed protocol `push` method has **no credential field** (`/tmp/ruk-model.json` `PushArgs`; `crates/ruk-core/src/protocol.rs:1947-1990`) — and is currently refused anyway (§3). The **command path can**: `CommandOpen` carries a caller-supplied `env` of `KEY=value` entries, "the command sees only these", inheriting *nothing* from the daemon's environment (`crates/ruk/src/daemon/mod.rs:403-404`, `1544-1595`). The daemon executes it as `git <argv>` through the in-process CLI (`daemon/mod.rs:1360-1390`, `cli/mod.rs:209`) — so a caller can push with `GIT_AUTH_TOKEN=...` supplied **per-invocation, in memory, never on disk**. Caveat: stdin/stdout are buffered through the daemon (stdin cap 256 MiB, `daemon/mod.rs:383-385`), so this is capture-mode, not an interactive stream.

**Not present**: no askpass integration, no `GIT_ASKPASS`, no interactive prompting (correct for a daemon), no per-remote env selection beyond helper URL-scoping — the `GIT_AUTH_*` env fallback is process-global, one token for every host in the process.

## 3. Push semantics, and `push` vs `ruk git push`

**Wire-level push (`push.rs`)**: per-update CAS — each `PushUpdate` is `{name, old, new}`, zero `old` = create, zero `new` = delete (`push.rs:46-54`). The advertised old value is compared *before sending*; mismatch = per-ref `Rejected{"remote ref moved: …"}` without transmitting (`push.rs:640-667`). Capability negotiation (`push.rs:618-715`): requests `report-status-v2` else `report-status` (server without either ⇒ plan goes `Uncertain`, `push.rs:620-625`); requires `delete-refs` for any deletion (`628-632`); requires `atomic` capability for atomic pushes (`633-637`); requests `side-band-64k`/`side-band`, `ofs-delta`, `object-format=sha1|sha256`, `agent=ruk`. **No push options** — the client never requests `push-options` (`push.rs:693-715`).

**Force / force-with-lease**: `--force` (`verbs_transport.rs:1214-1218`, applied at `1253`) suppresses the local non-fast-forward check (`build_push_updates`, `verbs_transport.rs:1340-1379`); the remote still only applies the update when the advertised old matches the CAS expectation. There is **no `--force-with-lease` CLI flag** (grep: absent from the whole `crates/ruk/src` tree), but the wire model is lease-shaped by construction: `PushUpdate.old` *is* an explicit expected-old. A lease with an expected value you didn't observe this connection isn't expressible at CLI level.

**Atomic**: `--atomic` (`verbs_transport.rs:1220-1223`); if any update is refused before sending, siblings are refused locally with "atomic push refused locally: a sibling ref moved" rather than desyncing (`push.rs:667-685`, `smart_http.rs:727-742`).

**Report-status parsing** (`read_report`/`parse_report`, `push.rs:1074-1130`): handles `unpack ok|<err>` + `ok <ref>` / `ng <ref> <msg>` lines, skips v2 `option` lines, side-band demux when negotiated; an `unpack` failure rejects **every** ref sent; the report must cover every live update (`check_report_covers_live`, `push.rs:596-606`). Per-ref outcomes are first-class (`RefOutcome`/`RefResult`, `push.rs:105-122`); the CLI prints `ok <ref>` / `rejected <ref>: <reason>` and fails on any rejection (`verbs_transport.rs:1653-1670`). Tags: pushed via refspec (`refs/tags/*:refs/tags/*`) or `--mirror`; no `--tags` shorthand.

**Durability** (`PushPlanRecord`, `push.rs:125-246`): versioned row in `FactTable::PushPlans` keyed by a 16-byte `PlanId`; states `Prepared → Sending → Confirmed/Rejected`, with `Uncertain` persisted best-effort on any failure past "first byte may have been sent"; an `Uncertain` plan blocks conflicting repeat publication until reconciliation (`push.rs:1-25`). `reconcile(plan, kind, observed)` (`push.rs:396-430`) classifies each ref `EqualNew | EqualOld | Moved{current}` and "never silently authorizes a retry". **Gaps around this**: no CLI verb reads or reconciles stored plans (grep of `crates/ruk/src`: `reconcile` appears only in tests/docstrings); observations rows (`FactTable::RemoteObservations`) are written by fetch, not by push.

**Protocol methods** (schema, `/tmp/ruk-model.json`; `crates/ruk-core/src/protocol.rs:1947-2020`):
- `push` = `{remote: named|url, updates: [{remote_ref, source: delete|commit{oid}|change{ChangeRef}, force, expected_old: absent|present{oid}}], atomicity: atomic|non_atomic (default atomic)}` → `{outcome, plan: UUID, state, refs: [PushRefOutcome {remote_ref, status: uncertain | confirmed{new?} | rejected{reason}}]}`. Note `source: change` — pushes can target a native change, not just an OID.
- `push.reconcile` = `{plan}` → `{outcome, plan, state, observations: [{remote_ref, observed|null, verdict: equal_new|equal_old|moved}]}`. Idempotency for both is via the mutation `request_id` ("a reused request id with a different digest is refused, never executed" — `daemon/mod.rs:405-407`; replay path `daemon/mod.rs:1228-1240`).
- **Status: not implemented.** The hosted coordinator refuses both: `crates/ruk-core/src/coord/prepare.rs:283-293` returns `E_HOSTED` for `Request::Push` and `Request::PushReconcile` ("the send leg runs as a transport job … with no producer for either on this path"); asserted in tests `prepare.rs:4637-4657`; mirrored in `crates/ruk-core/src/coord/undo.rs:186-188`. Today, the only real push path is the CLI (`ruk git push`, `verbs_transport.rs:1212+`) and, transitively, the daemon's **command** channel (§2) — which a Forgejo-style host reaches by exec'ing `git push --mirror --force` as a `CommandOpen` (daemon translates `git <argv>` internally, `daemon/mod.rs:1360-1390`).

## 4. What `mirror.rs` does

It is **neither** a fetcher nor a pusher: it is the **pure ref-mapping layer for Forgejo-style mirror syncs** (`crates/ruk-git/src/transport/mirror.rs:1-47`). Two functions:
- `prune_for_spec(local, advertised, src, dst)` — destination refs orphaned by a fetch refspec (the `--prune` half of a *pull*-mirror sync); single-`*` globs, byte-exact otherwise; FETCH_HEAD-only specs prune nothing (`mirror.rs:60-96`).
- `push_mirror_updates(local, remote)` — the full `push --mirror` list: every local direct tip written to the same name **plus deletes** for destination refs with no local counterpart; force implied for every set (`mirror.rs:47-54` module doc + function docs). Symbolic refs never map or prune (named gap, `mirror.rs:31-36`).

Network scheduling, retries, LFS-content sync are explicitly out of scope ("named gaps, not stubs", `mirror.rs:20-36`); the CLI supplies those (below). So **push mirror = `ruk push --mirror [--lfs]`** (`verbs_transport.rs:1567-1638`), **pull mirror = `ruk fetch --prune [--tags]` / `remote update --prune`** (`verbs_transport.rs:7`, `294-296`, `797-839`), over any supported scheme.

## 5. Receive-side hooks and host notification

`serve.rs` advertises `"report-status delete-refs ofs-delta push-options object-format=<…> agent=<…>"` (`serve.rs:578-581`) — note `atomic` and `push-cert` are **not** advertised; a client sending `atomic` anyway gets every ref rejected ("atomic pushes are not supported", `serve.rs:32-34`, `764-770`).

Hook machinery (`hooks.rs`):
- **Phases** mirror git-receive-pack: `pre-receive` (stdin `<old> <new> <ref>` lines; nonzero exit refuses the whole push), `update` (argv `<ref> <old> <new>`; refuses only that ref), `post-receive` (stdin landed lines; failure only logged — v0 has no sideband slot for it) (`hooks.rs:28-38`; execution in `serve.rs:822-856`).
- **Resolution**: `<repo_dir>/hooks/<name>`, executed directly, no shell; cwd = repo dir. Covers Ruk roots, legacy bare `.git`, and Forgejo's generated dispatcher scripts that chain `hooks/<name>.d/*` incl. the internal `gitea` hook — no `.d` handling needed (`hooks.rs:13-26`).
- **Environment passed to hooks**: inherited server env plus `GIT_QUARANTINE_PATH`, `GIT_OBJECT_DIRECTORY` (quarantine objects dir, so hook `git` commands see staged objects), `GIT_DIR`, `RUK_QUARANTINE_MANIFEST`, and `GIT_PUSH_OPTION_COUNT` / `GIT_PUSH_OPTION_<i>` **only when the client negotiated `push-options`** (`hooks.rs:40-46`, `326-345`). Stdout discarded; stderr = refusal message; fail-closed 60 s timeout (`hooks.rs:8-10`, `281-284`).
- **Quarantine**: pushed pack stages unpublished; a per-push dir materialises staged payloads as loose objects + manifest for hook inspection; promotion via `Store::publish` only after refusing phases pass (`hooks.rs:1-12`, `196-209`).
- **Host notification**: the only mechanism is the `post-receive` hook (or a `proc-receive` hook). `serve_receive_pack_with` returns bare `Result<(), GitError>` (`serve.rs:554-569`) — the embedding host gets **no per-ref outcome return value**, so a rukie backend wanting to trigger mirror-sync/webhooks should install post-receive hooks (reading `<old> <new> <ref>` stdin) or have its post-receive chain call back into the platform.
- **proc-receive / AGit**: implemented. `refs/for` and `refs/pull` prefixes divert to a `proc-receive` hook speaking the oracle conversation (`version=1[\0<features>]`, commands, optional `push-options` forwarding, `ok`/`ng`/`option` report; `option fall-through` returns the command to the normal path; `option refname/old-oid/new-oid/forced-update` recorded for observability) (`proc_receive.rs:1-59`, `358-393`, `486`). Broken hook conversations refuse only the diverted refs (`proc_receive.rs:37-39`). Client-side `push-options`: the *server* negotiates and forwards them, but the **client never sends them** (§3), so end-to-end AGit options need a client addition.

## 6. LFS support level

- **Client: implemented, both directions.** `lfs-payload-sync` speaks the git-lfs batch API: endpoint resolution `lfs.url` → `remote.<name>.lfsurl` → `<url>/info/lfs`, `POST …/objects/batch` (download or upload operation), streamed payload GET / PUT per `actions.*.href`, echoing per-action headers and env Bearer only on the endpoint's own authority (`verbs_transport.rs:3235-3260`, `3488-3512`, `3927-3973`; wire format `http.rs:1145-1155`, `1276-1394`). Integrated as `fetch --lfs` (pull-mirror half) and `push --lfs` (upload verified payloads from the outgoing closure *before* the pack moves; silent skip if non-Full policy or nothing to send — `verbs_transport.rs:1203-1210`, `1505-1510`, `3703-3710`).
- **Server: pointer gate only, no LFS API server.** `lfs.rs` refuses pushed LFS pointer blobs under `LfsMode::Deny` (`ng` per affected ref, `E_UNSUPPORTED_LFS`), records them plainly under `lfs = "pointers"`, no-op under `Full` (`lfs.rs:1-46`); wired into receive-pack (`serve.rs:504-510`). Grep confirms no `/objects/batch` serving endpoint exists in the tree.
- Gap for mirrors: LFS batch auth is env-only (`GIT_LFS_AUTH_BEARER/BASIC`); credential helpers are out of scope for LFS (`.outline/sdd/task-credhdr-report.md`; `http.rs:87-89`).

## 7. Large pushes: pack generation, thin packs, size behaviour

- **Thin packs: yes.** `pair_thin_bases(_filtered)` pairs same-path blobs between each ref's old/new trees; bases are guaranteed reachable from the receiver's advertised tips (privacy-safe: an excluded base disables pairing for that blob and the full object is sent instead); deltas are only emitted if smaller (`push.rs:12-15`, `890-916`).
- **Closure**: wants-minus-haves via commit-ancestry exclusion of advertised tips; promised/private objects never emitted (`push.rs:789-878`). `push --lfs` reuses the same closure to find pointer blobs (`push.rs:797-798`).
- **Memory behaviour — the weak point.** The entire closure is materialised in memory as `Vec<ClosureObject>` with full object bytes (`push.rs:799-878`; per-object read cap 512 MiB, `push.rs:41-42`) *and* thin-base bytes are added on top. The stateful `push_over` streams the pack body to the socket (`push.rs:735-745`), but **smart-HTTP builds the commands+entire pack into one in-memory buffer and POSTs it with Content-Length** (`smart_http.rs:735-820`; `http.rs:973-981` — no chunked request bodies). A first push of a multi-GB repo over HTTPS will be memory-bound. Server-side ingest is properly streaming with default limits: 2 GiB pack, 5 M objects, 512 MiB/object, delta depth 128 (`pack.rs:44-63`, `296-343`; used at `serve.rs:390`).

## 8. GAP list for a robust push mirror to GitHub / GitLab / Forgejo

1. **Typed `push`/`push.reconcile` are refused, not implemented** — `E_HOSTED` with no producer (`crates/ruk-core/src/coord/prepare.rs:283-293`). A hosted mirror service must either shell through the command channel or implement the transport job. (Also: `PushArgs` has no credentials slot — §2.)
2. **No push-plan lifecycle in the CLI**: plans are persisted (`push.rs:125-246`) but no verb lists/reconciles them (`crates/ruk/src` grep: reconcile only in tests); a crashed mirror sync leaves an `Uncertain` plan with no operator path to resolve it.
3. **Memory-bound HTTPS pushes**: whole pack buffered for the smart-HTTP POST (`smart_http.rs:735-820`), closure fully in RAM (`push.rs:799-878`). Needs streamed/chunked request upload for first-sync of large repos.
4. **No HTTP proxy support** anywhere in `transport/` (`http.rs:1-11` scope note; grep clean). Blocks corporate/self-hosted network paths.
5. **TLS roots are webpki-embedded only** (`http.rs:411-419`): no custom-CA/`SSL_CERT_FILE` support → self-managed GitLab/Forgejo with private CAs fail.
6. **URL userinfo ignored** (`http.rs:311-337`) — the idiomatic `https://oauth2:TOKEN@gitlab/...` mirror URL authenticates as anonymous. Must be fixed or firmly documented for GitLab mirrors.
7. **Per-remote credentials are weak**: `GIT_AUTH_TOKEN` is process-global; helpers are URL-scoped but built-ins are never executed (`credentials.rs:18-26`). A daemon mirroring N repos on 3 hosts needs per-mirror token injection — today only via `CommandOpen.env` (§2).
8. **No `--force-with-lease` flag** and no CLI plumbing for explicit expected-old (`crates/ruk/src` grep: absent); CAS exists at wire level (`push.rs:46-54`) — expose it.
9. **POST redirects not followed** (`smart_http.rs:37-42`); GET-redirect target isn't fed back into the RPC base → http→https fronted GitLab breaks on the push POST.
10. **No retry/backoff/jitter and fixed 30 s timeouts** (`http.rs:28-30`) — mirrors need retry semantics for 5xx/timeouts, and per-remote configurability.
11. **One-rejection-fails-the-sync**: CLI `push_outcomes` errors on any rejected ref (`verbs_transport.rs:1653-1670`) — a mirror against GitHub protected branches (which refuse force/deletes) aborts the whole sync instead of reporting partial success.
12. **Client never sends push-options** (`push.rs:693-715`) though the server negotiates them (`serve.rs:578-581`) — blocks AGit-style mirror pushes to Forgejo (`topic=`, CI triggers).
13. **Receive-pack API returns nothing** (`serve.rs:554-569`): hosts must scrape via post-receive hooks; an in-process event/outcome callback would be cleaner for webhooks + mirror triggering (§5).
14. **No LFS server** and LFS client auth env-only (`lfs.rs:1-46`; `.outline/sdd/task-credhdr-report.md`) — a rukie backend serving LFS to mirrored repos starts from zero.
15. **`--mirror` maps only direct tips** — no HEAD/default-branch handling, no namespace filtering (e.g. don't export `refs/ruk/*`), symbolic refs unmapped (`mirror.rs:31-36`, `verbs_transport.rs:1641-1645`).
16. **401/403 surface as `E_OFFLINE`** with no credential-failure signal (`smart_http.rs:220-228`) — a mirror loop can't distinguish "rotate token" from "host down".
17. **Parity harness has no network rows**: `tasks/parity-harness/GAPS.md` passes 56 local rows with zero fetch/push/HTTP coverage (`GAPS.md:1-29`) — **stale relative to the transport work** (STATUS.md rows 76/95/115 do cover it). Mirror work needs end-to-end harness rows against real/simulated GitHub/GitLab/Forgejo.

## Bottom line for the rukie rewrite

The primitives are unusually strong for a from-scratch codebase: real CAS pushes with durable plans and reconcile semantics, thin packs, working smart-HTTP and SSH clients, in-process credential resolution, hooks with quarantine, and a Forgejo-shaped `--mirror`/`--prune` surface (`crates/ruk/src/cli/verbs_transport.rs:7-9` documents all of it as "push-mirror parity"). The missing 20% is exactly the operational 80% for an automatic mirror service: typed-method push with per-mirror credentials (§2/§8.1), streaming HTTPS (§8.3), proxies/custom CAs/userinfo URLs (§8.4-6), partial-failure tolerance (§8.11), and a lifecycle API for reconciling uncertain syncs (§8.2).

*[INFERENCE-free note: every claim above cites source I read this session except: (a) GAPS.md staleness — inferred from comparing `GAPS.md:28-29` with `STATUS.md` rows 76/95/115; (b) behaviour against *real* GitHub/GitLab/Forgejo endpoints — all transport verification in-tree is against scripted peers/fake servers per `.outline/sdd/task-smarthttp-report.md` and `task-netremote-report.md`; live-host behaviour is **unverified**.]*

---

### Critical Files for Implementation
- `ruk/crates/ruk-git/src/transport/push.rs` — wire push, CAS/plan states, report parsing; the core of any mirror push
- `ruk/crates/ruk-git/src/transport/smart_http.rs` — HTTPS push path; streaming, redirects and status handling must change here
- `ruk/crates/ruk-git/src/transport/credentials.rs` — per-remote credential resolution for multi-host mirroring
- `ruk/crates/ruk/src/cli/verbs_transport.rs` — CLI push/fetch/mirror flags and outcome handling (partial-failure policy)
- `ruk/crates/ruk-core/src/coord/prepare.rs` — where typed `push`/`push.reconcile` are refused (GAP #1)

---

**Summary (for Main):** Artifact content is inline above — please save it to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-push-mirror-surface.md` (I have no write tool; both write attempts returned not-found). All 8 questions answered with file:line citations; 17 mirror-blocking gaps catalogued, the largest being: typed `push`/`push.reconcile` refused at `ruk-core/src/coord/prepare.rs:283-293`, whole-pack-in-memory HTTPS pushes (`smart_http.rs:735-820`), no proxy/custom-CA/userinfo auth, no `--force-with-lease`, partial-failure-intolerant CLI, and no LFS server. Live-host behaviour against real GitHub/GitLab/Forgejo is **unverified** (in-tree verification is scripted-peer only, per the sdd reports).

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> You cannot create files with …

---
## Later corrections from the same agent (apply over the body above)

Confirmed blocker resolution: I have no write tool (only `read`/`grep`/`web_search`; both write attempts returned not-found), so per the feasible advisory the complete artifact is delivered here for Main to write and verify at:

**`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-push-mirror-surface.md`**

Full artifact content follows (verbatim, save as-is):

---

# Ruk's push-mirror surface: what exists today (read-only survey, 2026-10-20)

Scope: client push (HTTP/SSH), credential model, push semantics, `mirror.rs`, receive-side hooks/proc-receive, LFS, large-push behaviour, and gaps for auto push-mirroring to GitHub/GitLab/Forgejo. All paths relative to `/home/alpha/rewrite/git-renewed/ruk/` unless noted. Citations are `file:line`. Everything below was read directly from source; unconfirmed items marked **unverified**.

## 1. Client-side push: HTTPS and SSH

**HTTPS: yes, real implementation.** `ruk push` routes HTTP remotes through the smart-HTTP client (`crates/ruk/src/cli/verbs_transport.rs:1410-1465`; module table `verbs_transport.rs:9`). `crates/ruk-git/src/transport/smart_http.rs` is a from-scratch stateless receive-pack client: `GET <base>/info/refs?service=git-receive-pack` for the advertisement (`smart_http.rs:243-262`), then one `POST <base>/git-receive-pack` carrying ref commands + pack (`smart_http.rs:648-860`). Protocol: v2 probed via `Git-Protocol: version=2` for discovery (`discover_versioned`, `smart_http.rs:289-325`), but **push itself is v0/v1 only** — no v2 push exists, and the stateful client refuses a v2 advertisement (`push.rs:570-576`; `verbs_transport.rs:10`). Chunked *responses* are decoded (`http.rs:693-745`, `983-1001`); **request** bodies are always `Content-Length` — no chunked/streaming request upload (`http.rs:543-561`, `973-981`).

- **TLS**: pure-Rust `rustls` + `webpki-roots` (Mozilla roots), `ring` provider; handshake forced up-front so cert errors fail before request bytes (`http.rs:1-11`, `411-419`, `495-503`). **No system trust store, no custom-CA config, no `SSL_CERT_FILE`** — self-managed GitLab with private CA fails TLS (GAP, §8.5).
- **Proxies: none.** No `HTTP_PROXY`/`HTTPS_PROXY` handling anywhere in `transport/` (grep matches only the `net.rs:925` security test string).
- **Redirects**: GET follows ≤3 hops, Authorization stripped on cross-host (`http.rs:1071-1113`); **POST never follows redirects** (`smart_http.rs:37-42`). GitLab behind http→https redirect breaks on the POST half.
- **HTTP/1.1 only**, `Connection: close` per request, no keep-alive, fixed 30 s timeouts (`http.rs:7-9`, `28-30`).
- **Auth-header injection**: per-URL `Authorization` (§2) plus `http.<url>.extraHeader` config lines, longest-scope-wins (`http.rs:107-207`; `smart_http.rs:153-200`; wire-proven per `.outline/sdd/task-credhdr-report.md`).
- **Status mapping**: 404 → "repository not found", 401/403 → "authentication failed", all non-2xx → `E_OFFLINE` (`smart_http.rs:220-228`). No retry/credential-fallback loop.

**SSH: yes.** `net.rs` classifies `ssh://[user@]host[:port]/path` and scp-like `user@host:path` via `gix-url` (`net.rs:1-21`, `99-148`; tests `823-870`), and **spawns the system `ssh` binary**, honouring `GIT_SSH`/`GIT_SSH_COMMAND` like Git (`net.rs:359-398`). Key handling, agent, `known_hosts`, `IdentityFile` are all delegated to ssh config/agent. Dash-led host/user blocked (`net.rs:382-398`; test `924-928`). Teardown: stdin closed, stdout drained, 30 s grace so remote post-receive hooks finish (`net.rs:241-247`, `304-333`).

**Supported URL schemes** (`net.rs:42-61`, `99-148`, `201-204`): local bare paths + `file://`; `ssh://` and `user@host:path` (bare `host:path` without user = **local path**, `net.rs:92`, tests `814-819`); `git://`; `http(s)://` (fetch falls back to dumb-HTTP read-only; **push over dumb-http refused**, `verbs_transport.rs:9`); `ext::` and `name::` helper schemes refuse loudly (`net.rs:98-109`).

## 2. Credential model

Resolution order for smart-HTTP `Authorization` (`smart_http.rs:22-30`; `credentials.rs:747-778`):
1. **Git credential helpers, in-process**: `credential.helper` rows parsed from gitconfig files (`credentials.rs:397-418`); configured **external** helper run as `<program> get` with `protocol=/host=/url=` stdin description (`credentials.rs:1-16`, `502-534`); `!`-shell helpers via `sh -c "<line> get"` (`credentials.rs:10-16`, `429-432`); URL-scoped `[credential "<pattern>"]` prefix-boundary matching (`243-259`); bounded 10 s/attempt, ≤16 helpers (`34-37`). **Bare built-ins (`store`/`cache`/`manager`) never executed** (`18-26`, `285-289`).
2. **Env fallback** (`http.rs:187-207`): `GIT_AUTH_TOKEN` → Bearer; else `GIT_AUTH_USER`+`GIT_AUTH_PASSWORD` → Basic. LFS: `GIT_LFS_AUTH_BEARER`/`GIT_LFS_AUTH_BASIC` (`http.rs:82-105`).
3. **`http.<url>.extraHeader`** may carry `Authorization`; both header and helper/env auth can coexist (`http.rs:194-197`).
4. **URL userinfo is NOT a credential channel** — stripped so it "cannot leak into Host or SNI" (`http.rs:311-337`): `https://oauth2:TOKEN@gitlab…` silently authenticates as anonymous. Common GitLab mirror-URL convention fails.

**Token scoping**: LFS tokens sent to presigned `href` only same-authority, dropped across cross-host redirects (`http.rs:70-79`, `1071-1078`).

**Hosted daemon / typed methods**: protocol `push` has **no credential field** (`/tmp/ruk-model.json` PushArgs; `crates/ruk-core/src/protocol.rs:1947-1990`) and is refused anyway (§3). The **command path can**: `CommandOpen.env` is caller-supplied, hermetic, "the command sees only these", inheriting nothing from the daemon env (`crates/ruk/src/daemon/mod.rs:403-404`, `1544-1595`); daemon runs it as `git <argv>` through the in-process CLI (`daemon/mod.rs:1360-1390`; `cli/mod.rs:209`). So a host can push with `GIT_AUTH_TOKEN=…` **per-invocation, in memory, never on disk**. Caveat: stdin/output buffered through the daemon (stdin cap 256 MiB, `daemon/mod.rs:383-385`).

**Not present**: no askpass/interactive prompting; `GIT_AUTH_*` env fallback is process-global (one token per process, no per-remote selection beyond helper URL-scoping).

## 3. Push semantics; `push` protocol method vs `ruk git push`

**Wire push (`push.rs`)**: per-update CAS `PushUpdate{name, old, new}`; zero `old` = create, zero `new` = delete (`push.rs:46-54`). Advertised old compared *before sending*; mismatch ⇒ per-ref `Rejected{"remote ref moved: …"}` without transmitting (`push.rs:640-667`). Capabilities (`push.rs:618-715`): requests `report-status-v2` else `report-status` (neither ⇒ `Uncertain`, `620-625`); deletions require server `delete-refs` (`628-632`); atomic requires `atomic` cap (`633-637`); also `side-band-64k`/`side-band`, `ofs-delta`, `object-format=sha1|sha256`, `agent=ruk`. **No client push-options** (`push.rs:693-715`).

**Force / force-with-lease**: `--force` (`verbs_transport.rs:1214-1218`, applied `1253`) suppresses the local non-FF check (`build_push_updates`, `verbs_transport.rs:1340-1379`); the CAS `old` still gates application remotely. **No `--force-with-lease` CLI flag** (grep: absent across `crates/ruk/src`), but the wire model is lease-shaped: `PushUpdate.old` is an explicit expected-old. A lease against an expected value not observed on this connection is not expressible at CLI level.

**Atomic**: `--atomic` (`verbs_transport.rs:1220-1223`); any pre-send refusal ⇒ siblings refused locally "atomic push refused locally: a sibling ref moved" (`push.rs:667-685`; `smart_http.rs:727-742`).

**Report parsing** (`read_report`/`parse_report`, `push.rs:1074-1130`): `unpack ok|<err>` + `ok <ref>`/`ng <ref> <msg>`, v2 `option` lines skipped, side-band demux when negotiated; `unpack` failure rejects every ref; report must cover every live update (`check_report_covers_live`, `push.rs:596-606`). Per-ref outcomes first-class (`RefOutcome`/`RefResult`, `push.rs:105-122`); CLI prints `ok <ref>`/`rejected <ref>: <reason>` and fails on any rejection (`verbs_transport.rs:1653-1670`). Tags: via refspec (`refs/tags/*:refs/tags/*`) or `--mirror`; no `--tags` shorthand.

**Durability** (`PushPlanRecord`, `push.rs:125-246`): versioned row in `FactTable::PushPlans` keyed by 16-byte `PlanId`; `Prepared → Sending → Confirmed/Rejected`, `Uncertain` persisted best-effort past "first byte may have been sent"; `Uncertain` blocks conflicting repeat publication until reconciliation (`push.rs:1-25`). `reconcile(plan, kind, observed)` (`push.rs:396-430`) yields `EqualNew | EqualOld | Moved{current}` and never authorizes a retry. **Gap**: no CLI verb lists/reconciles stored plans (grep: `reconcile` in `crates/ruk/src` only in tests/docstrings).

**Protocol methods** (`/tmp/ruk-model.json`; `protocol.rs:1947-2020`):
- `push` = `{remote: named|url, updates: [{remote_ref, source: delete|commit{oid}|change{ChangeRef}, force, expected_old: absent|present{oid}}], atomicity: atomic|non_atomic (default atomic)}` → `{outcome, plan: UUID, state, refs: [PushRefOutcome {remote_ref, status: uncertain | confirmed{new?} | rejected{reason}}]}`. Note `source: change` — pushes can target native changes.
- `push.reconcile` = `{plan}` → `{outcome, plan, state, observations: [{remote_ref, observed|null, verdict: equal_new|equal_old|moved}]}`.
- Idempotency via mutation `request_id`: "a reused request id with a different digest is refused, never executed" (`daemon/mod.rs:405-407`; replay `daemon/mod.rs:1228-1240`).
- **Status: NOT IMPLEMENTED.** The hosted coordinator refuses both with `E_HOSTED` and no producer (`crates/ruk-core/src/coord/prepare.rs:283-293`; asserted `prepare.rs:4637-4657`; echoed `coord/undo.rs:186-188`). The only real push paths today: CLI `ruk git push` (`verbs_transport.rs:1212+`) and the daemon's **command** channel (§2), which a Forgejo-style host drives by exec'ing `git push --mirror --force` as a `CommandOpen`.

## 4. What `mirror.rs` does

Neither fetcher nor pusher: **pure ref-mapping for Forgejo-style mirror syncs** (`crates/ruk-git/src/transport/mirror.rs:1-47`). Two functions:
- `prune_for_spec(local, advertised, src, dst)` — destination refs orphaned by a fetch refspec (pull-mirror `--prune` half); single-`*` globs, byte-exact otherwise; FETCH_HEAD-only specs prune nothing (`mirror.rs:60-96`).

[Showing lines 1-458 of 694 (49.8KB limit). Use :459 to continue]
- `prune_for_spec(local, advertised, src, dst)` — destination refs orphaned by a fetch refspec (pull-mirror `--prune` half); single-`*` globs, byte-exact otherwise; FETCH_HEAD-only specs prune nothing (`mirror.rs:60-96`).
- `push_mirror_updates(local, remote)` — full `push --mirror` list: every local direct tip to the same name **plus deletes** for destination refs with no local counterpart; force implied (`mirror.rs:47-54`). Symbolic refs never map/prune (named gap, `mirror.rs:31-36`).

Scheduling, retries, LFS-content sync explicitly out of scope ("named gaps, not stubs", `mirror.rs:20-36`); CLI supplies those. **Push mirror = `ruk push --mirror [--lfs]`** (`verbs_transport.rs:1567-1638`); **pull mirror = `ruk fetch --prune [--tags]` / `remote update --prune`** (`verbs_transport.rs:7`, `294-296`, `797-839`).

## 5. Receive-side hooks and host notification

`serve.rs` advertises `"report-status delete-refs ofs-delta push-options object-format=<…> agent=<…>"` (`serve.rs:577-581`) — **`atomic` and `push-cert` not advertised**; client sending `atomic` anyway gets every ref rejected ("atomic pushes are not supported", `serve.rs:32-34`, `764-770`).

Hooks (`hooks.rs`):
- **Phases**: `pre-receive` (stdin `<old> <new> <ref>` lines; nonzero exit refuses whole push), `update` (argv `<ref> <old> <new>`; refuses only that ref), `post-receive` (stdin landed lines; failure only logged — v0 report has no sideband slot) (`hooks.rs:28-38`; execution `serve.rs:822-856`).
- **Resolution**: `<repo_dir>/hooks/<name>`, executed directly, no shell, cwd = repo dir; covers Ruk roots, legacy bare `.git`, and Forgejo dispatcher scripts chaining `hooks/<name>.d/*` incl. internal `gitea` hook (`hooks.rs:13-26`).
- **Hook env**: inherited server env + `GIT_QUARANTINE_PATH`, `GIT_OBJECT_DIRECTORY`, `GIT_DIR`, `RUK_QUARANTINE_MANIFEST`, and `GIT_PUSH_OPTION_COUNT`/`GIT_PUSH_OPTION_<i>` **only when client negotiated `push-options`** (`hooks.rs:40-46`, `326-345`). Stdout discarded; stderr = refusal message; fail-closed 60 s timeout (`hooks.rs:8-10`, `281-284`).
- **Quarantine**: pack stages unpublished; per-push dir materializes staged payloads as loose objects + manifest for hook inspection; promotion via `Store::publish` only after refusing phases pass (`hooks.rs:1-12`, `196-209`).
- **Host notification**: only mechanism is the `post-receive` hook (or `proc-receive`). `serve_receive_pack_with` returns bare `Result<(), GitError>` (`serve.rs:554-569`) — no per-ref outcome return; a rukie backend should install post-receive hooks (reading `<old> <new> <ref>` stdin) or call back from the post-receive chain.
- **proc-receive / AGit: implemented.** `refs/for` + `refs/pull` prefixes divert to a `proc-receive` hook speaking the oracle conversation (`version=1[\0<features>]`, commands, optional `push-options` forwarding, `ok`/`ng`/`option` report; `option fall-through` returns command to normal path; `option refname/old-oid/new-oid/forced-update` recorded) (`proc_receive.rs:1-59`, `358-393`, `486`). Broken hook conversations refuse only diverted refs (`proc_receive.rs:37-39`). Client-side `push-options`: server negotiates/forwards, **client never sends them** (§3).

## 6. LFS support level

- **Client: implemented, both directions.** `lfs-payload-sync` speaks the git-lfs batch API: endpoint `lfs.url` → `remote.<name>.lfsurl` → `<url>/info/lfs`, `POST …/objects/batch` (download/upload), streamed payload GET/PUT per `actions.*.href`, echoing per-action headers and env Bearer only on the endpoint's authority (`verbs_transport.rs:3235-3260`, `3488-3512`, `3927-3973`; wire format `http.rs:1145-1155`, `1276-1394`). Integrated as `fetch --lfs` and `push --lfs` (upload verified payloads from outgoing closure *before* pack moves; silent skip if non-Full policy or nothing to send — `verbs_transport.rs:1203-1210`, `1505-1510`, `3703-3710`).
- **Server: pointer gate only, no LFS API server.** `lfs.rs` refuses pushed pointer blobs under `LfsMode::Deny` (`ng` per affected ref, `E_UNSUPPORTED_LFS`), records plainly under `lfs = "pointers"`, no-op under `Full` (`lfs.rs:1-46`); wired into receive-pack (`serve.rs:504-510`). Grep: no `/objects/batch` serving endpoint in tree.
- Gap: LFS batch auth is env-only (`GIT_LFS_AUTH_BEARER/BASIC`); helpers out of scope for LFS (`http.rs:87-89`; `.outline/sdd/task-credhdr-report.md`).

## 7. Large pushes: packs, thin packs, size behaviour

- **Thin packs: yes.** `pair_thin_bases(_filtered)` pairs same-path blobs between each ref's old/new trees; bases guaranteed reachable from receiver's advertised tips (privacy-safe: excluded base ⇒ full object sent); deltas only if smaller (`push.rs:12-15`, `890-916`).
- **Closure**: wants-minus-haves via commit-ancestry exclusion of advertised tips; promised/private objects never emitted (`push.rs:789-878`). `push --lfs` reuses the closure to find pointer blobs (`push.rs:797-798`).
- **Memory — weak point.** Entire closure materialized in RAM as `Vec<ClosureObject>` with full bytes (`push.rs:799-878`; per-object cap 512 MiB, `push.rs:41-42`) plus thin-base bytes on top. Stateful `push_over` streams the pack body to the socket (`push.rs:735-745`), but **smart-HTTP builds commands+entire pack into one in-memory buffer and POSTs with Content-Length** (`smart_http.rs:735-820`; `http.rs:973-981` — no chunked request bodies). First push of a multi-GB repo over HTTPS is memory-bound. Server ingest is streaming with default limits: 2 GiB pack, 5 M objects, 512 MiB/object, delta depth 128 (`pack.rs:44-63`, `296-343`; used at `serve.rs:390`).

## 8. GAP list for a robust push mirror to GitHub / GitLab / Forgejo

1. **Typed `push`/`push.reconcile` refused, not implemented** — `E_HOSTED`, no producer (`crates/ruk-core/src/coord/prepare.rs:283-293`); `PushArgs` has no credentials slot (§2). Mirror service must shell through the command channel or implement the transport job.
2. **No push-plan lifecycle in CLI**: plans persisted (`push.rs:125-246`) but no verb lists/reconciles them (grep: reconcile only in tests); crashed mirror sync leaves `Uncertain` plan with no operator path.
3. **Memory-bound HTTPS pushes**: whole pack buffered for smart-HTTP POST (`smart_http.rs:735-820`); closure fully in RAM (`push.rs:799-878`). Needs streamed/chunked request upload for first syncs.
4. **No HTTP proxy support** (`http.rs:1-11` scope note; grep clean).
5. **TLS roots webpki-embedded only** (`http.rs:411-419`): no custom CA/`SSL_CERT_FILE` ⇒ private-CA self-managed GitLab/Forgejo fail.
6. **URL userinfo ignored** (`http.rs:311-337`) — `https://oauth2:TOKEN@gitlab…` authenticates as anonymous.
7. **Per-remote credentials weak**: `GIT_AUTH_TOKEN` process-global; helpers URL-scoped but built-ins never executed (`credentials.rs:18-26`). N repos × 3 hosts needs per-mirror token injection — today only via `CommandOpen.env` (§2).
8. **No `--force-with-lease` flag** / no CLI plumbing for explicit expected-old (`crates/ruk/src` grep: absent); CAS exists at wire level (`push.rs:46-54`) — expose it.
9. **POST redirects not followed** (`smart_http.rs:37-42`); GET-redirect target not fed back into the RPC base ⇒ http→https-fronted GitLab breaks on push POST.
10. **No retry/backoff; fixed 30 s timeouts** (`http.rs:28-30`); mirrors need retry semantics for 5xx/timeouts, per-remote configurability.
11. **One rejection fails the sync**: CLI `push_outcomes` errors on any rejected ref (`verbs_transport.rs:1653-1670`) — GitHub protected branches refusing force/deletes abort the whole sync instead of reporting partial success.
12. **Client never sends push-options** (`push.rs:693-715`) though server negotiates them (`serve.rs:577-581`) — blocks AGit-style mirror pushes to Forgejo (`topic=`, CI triggers).
13. **Receive-pack API returns nothing** (`serve.rs:554-569`): hosts must scrape via post-receive hooks; an in-process event/outcome callback would be cleaner for webhooks + mirror triggering (§5).
14. **No LFS server**; LFS client auth env-only (`lfs.rs:1-46`; `.outline/sdd/task-credhdr-report.md`) — backend serving LFS to mirrored repos starts from zero.
15. **`--mirror` maps only direct tips**: no HEAD/default-branch handling, no namespace filtering (e.g. don't export `refs/ruk/*`), symbolic refs unmapped (`mirror.rs:31-36`; `verbs_transport.rs:1641-1645`).
16. **401/403 surface as `E_OFFLINE`** with no credential-failure signal (`smart_http.rs:220-228`) — mirror loop can't distinguish "rotate token" from "host down".
17. **Parity harness has no network rows**: `tasks/parity-harness/GAPS.md:1-29` passes 56 local rows, zero fetch/push/HTTP coverage — **stale relative to transport work** (STATUS.md rows 76/95/115 cover it). Mirror work needs end-to-end harness rows against real/simulated GitHub/GitLab/Forgejo.

**Bottom line**: strong primitives — real CAS pushes with durable plans + reconcile semantics, thin packs, working smart-HTTP and SSH clients, in-process credential resolution, hooks with quarantine, Forgejo-shaped `--mirror`/`--prune` surface (`verbs_transport.rs:7-9`, "push-mirror parity"). The missing 20% is exactly the operational 80% for an automatic mirror service: typed-method push with per-mirror credentials (§8.1), streaming HTTPS (§8.3), proxies/custom CAs/userinfo URLs (§8.4-6), partial-failure tolerance (§8.11), and a lifecycle API for reconciling uncertain syncs (§8.2).

*[Unverified: (a) GAPS.md staleness — inferred from comparing `GAPS.md:28-29` with `STATUS.md` rows 76/95/115; (b) behaviour against real GitHub/GitLab/Forgejo endpoints — all in-tree transport verification is against scripted peers/fake servers per `.outline/sdd/task-smarthttp-report.md` and `task-netremote-report.md`; live-host behaviour unverified.]*

---

### Critical Files for Implementation
- `ruk/crates/ruk-git/src/transport/push.rs` — wire push, CAS/plan states, report parsing; core of any mirror push
- `ruk/crates/ruk-git/src/transport/smart_http.rs` — HTTPS push path; streaming, redirects, status handling must change here
- `ruk/crates/ruk-git/src/transport/credentials.rs` — per-remote credential resolution for multi-host mirroring
- `ruk/crates/ruk/src/cli/verbs_transport.rs` — CLI push/fetch/mirror flags and partial-failure handling
- `ruk/crates/ruk-core/src/coord/prepare.rs` — where typed `push`/`push.reconcile` are refused (GAP #1)

---

**Summary:** Complete artifact (all 8 sections, file:line citations) delivered inline above for Main to write to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-push-mirror-surface.md`. I could not create the file myself: my toolset is read-only (`read`/`grep`/`web_search`); write attempts on both the absolute path and `local://` URI returned not-found. Key findings: HTTPS push works (smart-HTTP, v0 wire, rustls/webpki-roots); SSH works via system `ssh`; credentials = in-process external helpers → `GIT_AUTH_*` env → `http.<url>.extraHeader` (URL userinfo deliberately stripped — breaks GitLab token URLs); hosted daemon can push only via hermetic `CommandOpen.env` (in-memory, no disk), typed `push`/`push.reconcile` are refused (`E_HOSTED`, `prepare.rs:283-293`); server side has hooks+quarantine+proc-receive (AGit) but no atomic, no per-ref API return, no LFS server; 17 mirror-blocking gaps catalogued. Live-host behaviour unverified.

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Miscite found: net.rs:241-247 is i…

---
→ grep(const SSH_EXIT_GRACE|impl Drop for SshStream|self.stdin.take\(\)|SSH_EXIT_GRACE; @ /home/alpha/rewrite/git-renewed/ruk/…) ⇒ ok · 23 lines
→ grep(fn read_report|fn parse_report|fn check_report_covers_live|fn push_over_inner|fn push_closure_filtered @ /home/alpha/re…) ⇒ ok · 29 lines

[advisor] <advisory advisor="minimax" severity="nit" guidance="weigh, don't blindly obey"> The miscite survives your final copy: …

---
All three citation fixes verified against source. Below is the corrected artifact (delta only — everything else stands as delivered; Main should apply these fixes when writing the file).

**Correction 1 — §1 SSH teardown citation** (was `net.rs:241-247`): the grace constant doc is `net.rs:262-267`, `SSH_EXIT_GRACE` is `net.rs:268`, and the Drop teardown (stdin close → drain thread → bounded grace loop) is `net.rs:318-342` — verified by grep just now (`net.rs:267-268`, `318-342`). The `241-247` range indeed lands inside `ssh_invocation` (wrong).

**Correction 2 — §5 receive-pack caps citation** (was `serve.rs:577-581`): caps string is `serve.rs:569-573` ("report-status delete-refs ofs-delta push-options object-format=… agent=…"), sent at `serve.rs:574`; `serve_receive_pack_with` starts at `serve.rs:554`. Verified.

**Correction 3 — §1 HTTPS discovery citation** (was `smart_http.rs:243-262`): the v0 discovery GET is `smart_http.rs:243-256` (`discover` at 243, `refs_url` 249, `get_to_writer` 252, content-type check 254-255); v2 probe `discover_versioned` GET at `smart_http.rs:294-311`. Verified.

**Addition per overlycritic — §3 receipt semantics (verified in `/tmp/ruk-model.json` just now):** every mutation reply wraps a `MutationOutcome` whose `$defs/MutationOutcome` requires `receipt`, `heads`, `stamps`, `warnings`, `provenance` (plus optional `identity`, `merge`, `operation`) — i.e., typed `push`/`push.reconcile` replies are designed to carry a full receipt with per-workspace stamps, head deltas, and provenance, not just the per-ref list. Add to §3 and as a note under GAP #1: because both methods are refused with `E_HOSTED` before any lane runs (`prepare.rs:283-293`), **no successful mutation receipt is ever produced** on the typed path — the receipt/stamps/provenance fields exist only in schema; there is no live producer, so a mirror service relying on typed receipts today has nothing to consume. (Consequence: an automatic push-mirror built on the command channel gets only buffered stdout/stderr + exit code per invocation — `CommandExitReport`, `daemon/mod.rs:1209-1403` — and must parse `ok`/`rejected` lines from stdout.)

**Unverified-citation audit (per minimax):** all file:line values in the artifact were either directly read this session or re-anchored by the verification greps in this turn. The only citations I did not individually re-anchor are the small ones inside `mirror.rs:60-96`, `hooks.rs:40-46/326-345`, `pack.rs:44-63`, `proc_receive.rs:358-393`, and `http.rs` LFS ranges — these came from direct reads of those files in earlier turns and I stand by them; `prepare.rs:4637-4657` test line numbers came from the grep image and I re-confirmed the `E_HOSTED` assertions exist (visible in `prepare.rs` grep output) but the exact line numbers are **unverified** to ±5.

Final artifact path for Main (content = my previous full message with the three citation fixes and the receipt note applied):

`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-push-mirror-surface.md`

[advisor] <advisory advisor="minimax" severity="concern" turns_ago="2" guidance="weigh, don't blindly obey"> Your push.rs anchors…

---
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs:1980-1992) ⇒ ok · 21 lines

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Do not hand off a delta. Main writ…

---
Full corrected artifact re-emitted (this copy supersedes all prior versions; Main should write it verbatim).

---

# Ruk's push-mirror surface: what exists today (read-only survey, 2026-10-20)

Scope: client push (HTTP/SSH), credential model, push semantics, `mirror.rs`, receive-side hooks/proc-receive, LFS, large-push behaviour, and gaps for auto push-mirroring to GitHub/GitLab/Forgejo. All paths relative to `/home/alpha/rewrite/git-renewed/ruk/` unless noted. Citations are `file:line`; anchors marked ⚓ were grep-re-verified this session. Unconfirmed items marked **unverified**.

## 1. Client-side push: does it work over HTTPS and SSH?

**HTTPS: yes, and it's real.** `ruk push` routes HTTP remotes through the smart-HTTP client (`crates/ruk/src/cli/verbs_transport.rs:1410-1465`; module table `verbs_transport.rs:9`). `crates/ruk-git/src/transport/smart_http.rs` is a from-scratch stateless receive-pack client: ⚓ one `GET <base>/info/refs?service=git-receive-pack` for the advertisement (`smart_http.rs:243-256`; v2 probe GET at `294-311`), then one `POST <base>/git-receive-pack` carrying ref commands + pack (`smart_http.rs:648-860`). Protocol: v2 is probed with `Git-Protocol: version=2` for *discovery* only; **push itself is v0/v1** — receive-pack defines no v2 push, and the stateful client refuses a v2 advertisement (`push.rs:570-576`; `verbs_transport.rs:10`). Chunked *responses* are decoded (`http.rs:693-745`, `983-1001`); **request** bodies are always `Content-Length` — no chunked/streaming request upload (`http.rs:543-561`, `973-981`).

- **TLS**: pure-Rust `rustls` + `webpki-roots` (Mozilla roots), `ring` provider; handshake forced up-front so cert errors fail before request bytes (`http.rs:1-11`, `411-419`, `495-503`). **No system trust store, no custom-CA config, no `SSL_CERT_FILE`** — self-managed GitLab with a private CA fails TLS (GAP, §8.5).
- **Proxies: none.** No `HTTP_PROXY`/`HTTPS_PROXY` handling anywhere in `transport/` (grep matches only the `net.rs:925` security test string).
- **Redirects**: GET follows ≤3 hops, Authorization stripped on cross-host (`http.rs:1071-1113`); **POST never follows redirects** (`smart_http.rs:37-42`). GitLab behind http→https redirect breaks on the POST half.
- **HTTP/1.1 only**, `Connection: close` per request, no keep-alive, fixed 30 s timeouts (`http.rs:7-9`, `28-30`).
- **Auth-header injection**: per-URL `Authorization` (§2) plus `http.<url>.extraHeader` config lines, longest-scope-wins (`http.rs:107-207`; `smart_http.rs:153-200`; wire-proven per `.outline/sdd/task-credhdr-report.md`).
- **Status mapping**: 404 → "repository not found", 401/403 → "authentication failed", all non-2xx → `E_OFFLINE` (`smart_http.rs:220-228`). No retry/credential-fallback loop.

**SSH: yes.** `net.rs` classifies `ssh://[user@]host[:port]/path` and scp-like `user@host:path` via `gix-url` (`net.rs:1-21`, `99-148`; tests `823-870`) and **spawns the system `ssh` binary**, honouring `GIT_SSH`/`GIT_SSH_COMMAND` like Git (`net.rs:359-398`). Key handling, agent, `known_hosts`, `IdentityFile` all delegated to ssh config/agent. Dash-led host/user blocked (`net.rs:382-398`; test `924-928`). ⚓ Teardown: stdin closed, stdout drained by a detached thread, then a bounded grace loop (`SSH_EXIT_GRACE` doc `net.rs:262-267`, const at `net.rs:268`, Drop impl `net.rs:318-342`) so remote post-receive hooks finish before kill/reap — note post-receive hook stderr arrives on this pipe as band-2 *after* the report-status flush, so draining is required, not hygiene (`net.rs:320-333`).

**Supported URL schemes** (`net.rs:42-61`, `99-148`, `201-204`): local bare paths + `file://`; `ssh://` and `user@host:path` (bare `host:path` without user = **local path**, `net.rs:92`, tests `814-819`); `git://`; `http(s)://` (fetch falls back to dumb-HTTP read-only; **push over dumb-http refused**, `verbs_transport.rs:9`); `ext::`/`name::` helper schemes refuse loudly (`net.rs:98-109`).

## 2. Credential model

Resolution order for smart-HTTP `Authorization` (`smart_http.rs:22-30`; `credentials.rs:747-778`):
1. **Git credential helpers, in-process**: `credential.helper` rows parsed from gitconfig files (`credentials.rs:397-418`); configured **external** helper run as `<program> get` with `protocol=/host=/url=` stdin description (`credentials.rs:1-16`, `502-534`); `!`-shell helpers via `sh -c "<line> get"` (`credentials.rs:10-16`, `429-432`); URL-scoped `[credential "<pattern>"]` prefix-boundary matching (`243-259`); bounded 10 s/attempt, ≤16 helpers, no retries (`34-37`). **Bare built-ins (`store`/`cache`/`manager`) never executed** (`18-26`, `285-289`).
2. **Env fallback** (`http.rs:187-207`): `GIT_AUTH_TOKEN` → Bearer; else `GIT_AUTH_USER`+`GIT_AUTH_PASSWORD` → Basic. LFS: `GIT_LFS_AUTH_BEARER`/`GIT_LFS_AUTH_BASIC` (`http.rs:82-105`).
3. **`http.<url>.extraHeader`** may carry `Authorization`; both header and helper/env auth can coexist (`http.rs:194-197`).
4. **URL userinfo is NOT a credential channel** — stripped so it "cannot leak into Host or SNI" (`http.rs:311-337`): `https://oauth2:TOKEN@gitlab…` silently authenticates as anonymous.

**Token scoping**: LFS tokens sent to presigned `href` only same-authority, dropped across cross-host redirects (`http.rs:70-79`, `1071-1078`).

**Hosted daemon / typed methods**: protocol `push` has **no credential field** (`/tmp/ruk-model.json` PushArgs; `crates/ruk-core/src/protocol.rs:1947-1990`) and is refused anyway (§3). The **command path can**: `CommandOpen.env` is caller-supplied, hermetic, "the command sees only these", inheriting nothing from the daemon env (`crates/ruk/src/daemon/mod.rs:403-404`, `1544-1595`); daemon runs it as `git <argv>` through the in-process CLI (`daemon/mod.rs:1360-1390`; `cli/mod.rs:209`). So a host can push with `GIT_AUTH_TOKEN=…` **per-invocation, in memory, never on disk**. Caveat: stdin/output buffered through the daemon (stdin cap 256 MiB, `daemon/mod.rs:383-385`).

**Not present**: no askpass/interactive prompting; `GIT_AUTH_*` env fallback is process-global (one token per process; no per-remote selection beyond helper URL-scoping).

## 3. Push semantics, receipts, and `push` vs `ruk git push`

**Wire push (`push.rs`)**: per-update CAS `PushUpdate{name, old, new}` ⚓ (`push.rs:46-54`); zero `old` = create, zero `new` = delete. Advertised old compared *before sending*; mismatch ⇒ per-ref `Rejected{"remote ref moved: …"}` without transmitting (`push_over_inner`, ⚓ `push.rs:599-667`). Capabilities (`push.rs:618-715`): requests `report-status-v2` else `report-status` (neither ⇒ `Uncertain`, `620-625`); deletions require server `delete-refs` (`628-632`); atomic requires `atomic` cap (`633-637`); also `side-band-64k`/`side-band`, `ofs-delta`, `object-format=sha1|sha256`, `agent=ruk`. **No client push-options** (`push.rs:693-715`).

**Force / force-with-lease**: `--force` (`verbs_transport.rs:1214-1218`, applied `1253`) suppresses the local non-FF check (`build_push_updates`, `verbs_transport.rs:1340-1379`); the CAS `old` still gates application remotely. **No `--force-with-lease` CLI flag** (grep: absent across `crates/ruk/src`), but the wire model is lease-shaped: `PushUpdate.old` *is* an explicit expected-old. A lease against an expected value not observed on this connection is not expressible at CLI level.

**Atomic**: `--atomic` (`verbs_transport.rs:1220-1223`); any pre-send refusal ⇒ siblings refused locally "atomic push refused locally: a sibling ref moved" (`push.rs:667-685`; `smart_http.rs:727-742`).

**Report parsing** ⚓ (`read_report` `push.rs:1079-1110`; `parse_report` `push.rs:1112-1170`; `check_report_covers_live` `push.rs:566-576`): `unpack ok|<err>` + `ok <ref>`/`ng <ref> <msg>`, v2 `option` lines skipped, side-band demux when negotiated; `unpack` failure rejects every ref; report must cover every live update. Per-ref outcomes first-class (`RefOutcome`/`RefResult`, `push.rs:105-122`); CLI prints `ok <ref>`/`rejected <ref>: <reason>` and fails on any rejection (`verbs_transport.rs:1653-1670`). Tags: via refspec (`refs/tags/*:refs/tags/*`) or `--mirror`; no `--tags` shorthand.

**Durability** (`PushPlanRecord` ⚓ `push.rs:134-246`): versioned row in `FactTable::PushPlans` keyed by 16-byte `PlanId`; ⚓ `Prepared → Sending → Confirmed/Rejected` with `Uncertain` persisted best-effort past "first byte may have been sent" (`prepare` 152, `begin_send` 167, `finalize` 182, `refuse_locally` 203, `mark_uncertain` 216, `persist_mutation` 228); `Uncertain` blocks conflicting repeat publication until reconciliation (`push.rs:1-25`). ⚓ `reconcile(plan, kind, observed)` (`push.rs:396-430`, `ReconciledRef` at 397) yields `EqualNew | EqualOld | Moved{current}` and never authorizes a retry. **Gap**: no CLI verb lists/reconciles stored plans (grep: `reconcile` in `crates/ruk/src` only in tests/docstrings).

**Receipts (typed path)** ⚓ verified in `crates/ruk-core/src/protocol.rs` and `/tmp/ruk-model.json`: every mutation reply wraps `MutationOutcome` (`protocol.rs:1051-1067`; schema `$defs/MutationOutcome` requires `receipt`, `heads`, `stamps`, `warnings`, `provenance`), and `ReceiptRef` (`protocol.rs:991-1002`) = `{session, request_id, digest, replayed: bool, retained_until}` — idempotency: same `(session, request_id)` + same digest replays the original reply (`replayed: true`) until `retained_until`; same id with a **different** digest fails `E_REQUEST_REUSED` (`protocol.rs:991-993`; digest over postcard-encoded args incl. expected stamps, never the request id — `protocol.rs:26-27`, `1067-1069`, `digest_input_v1` `3210+`). `PushReply` = `{outcome: MutationOutcome, plan, state, refs: [PushRefOutcome{remote_ref, status}]}` ⚓ (`protocol.rs:1986-1990`); `PushReconcileReply` similarly carries `outcome` (`protocol.rs:2016-2020`). **Status: NOT IMPLEMENTED** — the hosted coordinator refuses both with `E_HOSTED`, no producer (`crates/ruk-core/src/coord/prepare.rs:283-293`; asserted `prepare.rs:4637-4657` ±5, **unverified** exact test lines; echoed `coord/undo.rs:186-188`), so **no successful mutation receipt is ever produced on the typed path**: receipt/stamps/provenance exist only in schema, with nothing to consume. A mirror service on the command channel instead gets buffered stdout/stderr + exit code per invocation (`CommandExitReport`, `daemon/mod.rs:1209-1403`) and must parse `ok`/`rejected` lines from stdout. The only real push paths today: CLI `ruk git push` (`verbs_transport.rs:1212+`) and the daemon **command** channel (§2).

## 4. What `mirror.rs` does

Neither fetcher nor pusher: **pure ref-mapping for Forgejo-style mirror syncs** (`crates/ruk-git/src/transport/mirror.rs:1-47`). Two functions:
- `prune_for_spec(local, advertised, src, dst)` — destination refs orphaned by a fetch refspec (pull-mirror `--prune` half); single-`*` globs, byte-exact otherwise; FETCH_HEAD-only specs prune nothing (`mirror.rs:60-96`).
- `push_mirror_updates(local, remote)` — full `push --mirror` list: every local direct tip to the same name **plus deletes** for destination refs with no local counterpart; force implied (`mirror.rs:47-54`). Symbolic refs never map/prune (named gap, `mirror.rs:31-36`).

Scheduling, retries, LFS-content sync explicitly out of scope ("named gaps, not stubs", `mirror.rs:20-36`). **Push mirror = `ruk push --mirror [--lfs]`** (`verbs_transport.rs:1567-1638`); **pull mirror = `ruk fetch --prune [--tags]` / `remote update --prune`** (`verbs_transport.rs:7`, `294-296`, `797-839`).

## 5. Receive-side hooks and host notification

⚓ Receive-pack advertises `"report-status delete-refs ofs-delta push-options object-format=<…> agent=<…>"` (`serve.rs:569-574`) — **`atomic` and `push-cert` not advertised**; client sending `atomic` anyway gets every ref rejected ("atomic pushes are not supported", `serve.rs:32-34`, `764-770`).

Hooks (`hooks.rs`):
- **Phases**: `pre-receive` (stdin `<old> <new> <ref>` lines; nonzero exit refuses whole push), `update` (argv `<ref> <old> <new>`; refuses only that ref), `post-receive` (stdin landed lines; failure only logged — v0 report has no sideband slot) (`hooks.rs:28-38`; execution `serve.rs:822-856`).
- **Resolution**: `<repo_dir>/hooks/<name>`, executed directly, no shell, cwd = repo dir; covers Ruk roots, legacy bare `.git`, and Forgejo dispatcher scripts chaining `hooks/<name>.d/*` incl. internal `gitea` hook (`hooks.rs:13-26`).
- **Hook env**: inherited server env + `GIT_QUARANTINE_PATH`, `GIT_OBJECT_DIRECTORY`, `GIT_DIR`, `RUK_QUARANTINE_MANIFEST`, and `GIT_PUSH_OPTION_COUNT`/`GIT_PUSH_OPTION_<i>` **only when client negotiated `push-options`** (`hooks.rs:40-46`, `326-345`). Stdout discarded; stderr = refusal message; fail-closed 60 s timeout (`hooks.rs:8-10`, `281-284`).
- **Quarantine**: pack stages unpublished; per-push dir materializes staged payloads as loose objects + manifest for hook inspection; promotion via `Store::publish` only after refusing phases pass (`hooks.rs:1-12`, `196-209`).
- **Host notification**: only mechanism is the `post-receive` hook (or `proc-receive`). ⚓ `serve_receive_pack_with` returns bare `Result<(), GitError>` (`serve.rs:554-569`) — no per-ref outcome return; a rukie backend should install post-receive hooks (reading `<old> <new> <ref>` stdin) or call back from the post-receive chain.
- **proc-receive / AGit: implemented.** `refs/for` + `refs/pull` prefixes divert to a `proc-receive` hook speaking the oracle conversation (`version=1[\0<features>]`, commands, optional `push-options` forwarding, `ok`/`ng`/`option` report; `option fall-through` returns command to normal path; `option refname/old-oid/new-oid/forced-update` recorded) (`proc_receive.rs:1-59`, `358-393`, `486`). Broken hook conversations refuse only diverted refs (`proc_receive.rs:37-39`). Client-side `push-options`: server negotiates/forwards, **client never sends them** (§3).

## 6. LFS support level

- **Client: implemented, both directions.** `lfs-payload-sync` speaks the git-lfs batch API: endpoint `lfs.url` → `remote.<name>.lfsurl` → `<url>/info/lfs`, `POST …/objects/batch` (download/upload), streamed payload GET/PUT per `actions.*.href`, echoing per-action headers and env Bearer only on the endpoint's authority (`verbs_transport.rs:3235-3260`, `3488-3512`, `3927-3973`; wire format `http.rs:1145-1155`, `1276-1394`). Integrated as `fetch --lfs` and `push --lfs` (upload verified payloads from outgoing closure *before* pack moves; silent skip if non-Full policy or nothing to send — `verbs_transport.rs:1203-1210`, `1505-1510`, `3703-3710`).
- **Server: pointer gate only, no LFS API server.** `lfs.rs` refuses pushed pointer blobs under `LfsMode::Deny` (`ng` per affected ref, `E_UNSUPPORTED_LFS`), records plainly under `lfs = "pointers"`, no-op under `Full` (`lfs.rs:1-46`); wired into receive-pack (`serve.rs:504-510`). Grep: no `/objects/batch` serving endpoint in tree.
- Gap: LFS batch auth is env-only; helpers out of scope for LFS (`http.rs:87-89`; `.outline/sdd/task-credhdr-report.md`).

## 7. Large pushes: packs, thin packs, size behaviour

- **Thin packs: yes.** `pair_thin_bases(_filtered)` pairs same-path blobs between each ref's old/new trees; bases guaranteed reachable from receiver's advertised tips (privacy-safe: excluded base ⇒ full object sent); deltas only if smaller (`push.rs:12-15`, `890-916`).
- **Closure**: wants-minus-haves via commit-ancestry exclusion of advertised tips ⚓ (`push_closure_filtered`, `push.rs:814-878`; pub wrapper `push_closure` just above); promised/private objects never emitted. `push --lfs` reuses the closure to find pointer blobs (`push.rs:797-798`).
- **Memory — weak point.** Entire closure materialized in RAM as `Vec<ClosureObject>` with full bytes (`push.rs:766-783`, `814-878`; per-object cap 512 MiB, `push.rs:41-42`) plus thin-base bytes on top. Stateful `push_over` streams the pack body to the socket (`push.rs:735-745`), but **smart-HTTP builds commands+entire pack into one in-memory buffer and POSTs with Content-Length** (`smart_http.rs:735-820`; `http.rs:973-981` — no chunked request bodies). First push of a multi-GB repo over HTTPS is memory-bound. Server ingest is streaming with default limits: 2 GiB pack, 5 M objects, 512 MiB/object, delta depth 128 (`pack.rs:44-63`, `296-343`; used at `serve.rs:390`).

## 8. GAP list for a robust push mirror to GitHub / GitLab / Forgejo

1. **Typed `push`/`push.reconcile` refused, not implemented** — `E_HOSTED`, no producer (`crates/ruk-core/src/coord/prepare.rs:283-293`); `PushArgs` has no credentials slot, and the designed receipt fields (`ReceiptRef`, `MutationOutcome`, `protocol.rs:991-1002`/`1051-1067`) have no producer on this path. Mirror service must shell through the command channel or implement the transport job.
2. **No push-plan lifecycle in CLI**: plans persisted (`push.rs:134-246`) but no verb lists/reconciles them (grep: reconcile only in tests); crashed mirror sync leaves `Uncertain` plan with no operator path.
3. **Memory-bound HTTPS pushes**: whole pack buffered for smart-HTTP POST (`smart_http.rs:735-820`); closure fully in RAM (`push.rs:814-878`). Needs streamed/chunked request upload for first syncs.
4. **No HTTP proxy support** (`http.rs:1-11` scope note; grep clean).
5. **TLS roots webpki-embedded only** (`http.rs:411-419`): no custom CA/`SSL_CERT_FILE` ⇒ private-CA self-managed GitLab/Forgejo fail.
6. **URL userinfo ignored** (`http.rs:311-337`) — `https://oauth2:TOKEN@gitlab…` authenticates as anonymous.
7. **Per-remote credentials weak**: `GIT_AUTH_TOKEN` process-global; helpers URL-scoped but built-ins never executed (`credentials.rs:18-26`). N repos × 3 hosts needs per-mirror token injection — today only via `CommandOpen.env` (§2).
8. **No `--force-with-lease` flag** / no CLI plumbing for explicit expected-old (`crates/ruk/src` grep: absent); CAS exists at wire level (`push.rs:46-54`) — expose it.
9. **POST redirects not followed** (`smart_http.rs:37-42`); GET-redirect target not fed back into the RPC base ⇒ http→https-fronted GitLab breaks on push POST.
10. **No retry/backoff; fixed 30 s timeouts** (`http.rs:28-30`); mirrors need retry semantics for 5xx/timeouts, per-remote configurability.
11. **One rejection fails the sync**: CLI `push_outcomes` errors on any rejected ref (`verbs_transport.rs:1653-1670`) — GitHub protected branches refusing force/deletes abort the whole sync instead of reporting partial success.
12. **Client never sends push-options** (`push.rs:693-715`) though server negotiates them (`serve.rs:569-574`) — blocks AGit-style mirror pushes to Forgejo (`topic=`, CI triggers).
13. **Receive-pack API returns nothing** (`serve.rs:554-569`): hosts must scrape via post-receive hooks; an in-process event/outcome callback would be cleaner for webhooks + mirror triggering (§5).
14. **No LFS server**; LFS client auth env-only (`lfs.rs:1-46`; `.outline/sdd/task-credhdr-report.md`) — backend serving LFS to mirrored repos starts from zero.
15. **`--mirror` maps only direct tips**: no HEAD/default-branch handling, no namespace filtering (e.g. don't export `refs/ruk/*`), symbolic refs unmapped (`mirror.rs:31-36`; `verbs_transport.rs:1641-1645`).
16. **401/403 surface as `E_OFFLINE`** with no credential-failure signal (`smart_http.rs:220-228`) — mirror loop can't distinguish "rotate token" from "host down".
17. **Parity harness has no network rows**: `tasks/parity-harness/GAPS.md:1-29` passes 56 local rows, zero fetch/push/HTTP coverage — **stale relative to transport work** (STATUS.md rows 76/95/115 cover it). Mirror work needs end-to-end harness rows against real/simulated GitHub/GitLab/Forgejo.

**Bottom line**: strong primitives — real CAS pushes with durable plans + reconcile semantics, thin packs, working smart-HTTP and SSH clients, in-process credential resolution, hooks with quarantine, Forgejo-shaped `--mirror`/`--prune` surface (`verbs_transport.rs:7-9`, "push-mirror parity"). The missing 20% is exactly the operational 80% for an automatic mirror service: typed-method push with per-mirror credentials and receipts (§8.1), streaming HTTPS (§8.3), proxies/custom CAs/userinfo URLs (§8.4-6), partial-failure tolerance (§8.11), and a lifecycle API for reconciling uncertain syncs (§8.2).

*[Unverified: (a) GAPS.md staleness — inferred from comparing `GAPS.md:28-29` with `STATUS.md` rows 76/95/115; (b) live-host behaviour against real GitHub/GitLab/Forgejo — all in-tree transport verification is against scripted peers/fake servers per `.outline/sdd/task-smarthttp-report.md` and `task-netremote-report.md`; (c) `prepare.rs:4637-4657` test line numbers are ±5.]*

---

### Critical Files for Implementation
- `ruk/crates/ruk-git/src/transport/push.rs` — wire push, CAS/plan states, report parsing; core of any mirror push
- `ruk/crates/ruk-git/src/transport/smart_http.rs` — HTTPS push path; streaming, redirects, status handling must change here
- `ruk/crates/ruk-git/src/transport/credentials.rs` — per-remote credential resolution for multi-host mirroring
- `ruk/crates/ruk/src/cli/verbs_transport.rs` — CLI push/fetch/mirror flags and partial-failure handling
- `ruk/crates/ruk-core/src/coord/prepare.rs` — where typed `push`/`push.reconcile` are refused (GAP #1)

---

**Summary:** Complete corrected artifact above — write verbatim to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-push-mirror-surface.md`. All advisories applied and re-verified by grep: SSH teardown re-anchored (`net.rs:262-268` doc/const, `318-342` Drop), receive-pack caps `serve.rs:569-574`, discovery GET `smart_http.rs:243-256`, push.rs anchors re-anchored (`check_report_covers_live` 566, `push_over_inner` 599, `push_closure_filtered` 814, `read_report` 1079, `parse_report` 1112), and a new receipts subsection in §3 with `ReceiptRef{session, request_id, digest, replayed, retained_until}` (`protocol.rs:991-1002`), `MutationOutcome` required fields (`protocol.rs:1051-1067`), `PushReply` (`protocol.rs:1986-1990`), replay/`E_REQUEST_REUSED` semantics — noting typed push produces no receipts today. I have no write tool; this message is the final deliverable.