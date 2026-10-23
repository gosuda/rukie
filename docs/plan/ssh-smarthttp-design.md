# rukie git transport design: Smart HTTP + SSH served through the Ruk daemon

## 0. Premise and non-goals

- rukie keeps only the `/{owner}/{repo}.git` clone URL shape. Every Gitea surface in the settled decisions is cut, including the builtin SSH code and `forgejo serv`.
- The git pack protocol implementations stay in Ruk (`ruk/crates/ruk-git/src/transport/`). rukie (Go) does **no pkt-line parsing, no pack building, no ref advertisement construction**: it authenticates, authorizes, maps HTTP/SSH bytes to daemon streams, and reacts to ref-update notifications.
- Transport matrix: Smart HTTP (v0 + v2 for fetch; v0 for push) and SSH (v0 + v2 for fetch; v0 for push). Dumb HTTP is out of scope (Ruk names it a gap: `smart_http.rs` module docs — "A response without the smart content type is dumb-http, the remaining HTTP gap").

## 1. What Ruk already implements (rukie reuses verbatim)

### 1.1 Server pack halves

| Capability | Location | Notes |
|---|---|---|
| `upload-pack` v0 serve | `ruk/crates/ruk-git/src/transport/serve.rs` `serve_upload_pack` | Advertises `multi_ack ofs-delta object-format agent` + `symref` for HEAD; always-NAK posture; self-contained packs via `session::local_closure`; thin packs never emitted |
| `upload-pack` v2 serve | `ruk/crates/ruk-git/src/transport/serve_v2.rs` `serve_upload_pack_v2` | Writes the v2 capability advertisement first (`encode_capability_advertisement`), then serves `ls-refs` + `fetch` commands; selected by `GIT_PROTOCOL` containing `version=2` (`git_protocol_requests_v2`); side-band-64k multiplexed pack tail |
| `receive-pack` v0 serve | `serve.rs` `serve_receive_pack` / `serve_receive_pack_with` | Advertises `report-status delete-refs ofs-delta push-options object-format agent`; per-ref `ok`/`ng` report; `atomic` and `push-cert` not advertised (arriving `atomic` rejects every ref); empty command list answers nothing. `post-receive` runs **before** `send_report`, so hook latency is client-visible push latency |
| AGit diversion | `serve.rs` `run_proc_receive_phase` + `proc_receive.rs` `run_proc_receive` | `refs/for/*`, `refs/pull/*` divert to `proc-receive` hook (`PROC_RECEIVE_PREFIXES = ["refs/for", "refs/pull"]`); `option fall-through` rejoins the normal path; an `ok` line reports the review hook accepted the command (no normal ref is written); accepted review refs recorded as `AlternateRef` |
| Hook phases | `hooks.rs` `run_hook`, `apply_hook_env`, `stage_quarantine`, `ReceiveHooks` | `pre-receive` (whole-push veto), `update` (per-ref veto), `post-receive` (never fails push); quarantine via `GIT_QUARANTINE_PATH` + `GIT_OBJECT_DIRECTORY` + `RUK_QUARANTINE_MANIFEST`; `HOOK_TIMEOUT` = 60s; direct exec, no shell |
| Ref application | `transport/session.rs` `apply_refs` | CAS ref updates applied via `Store::publish`; emits no coordinator operation record (see §5.3 binding) |
| pkt-line, fetch/push wire halves | `pktline.rs`, `fetch.rs`, `push.rs`, `refadv.rs`, `proto_v2.rs` | Shared grammars client and server both speak |
| Ingest bounds | `pack.rs` `IngestLimits` (`pack.rs:44-61`) | Defaults: `max_pack_bytes` 2 GiB (`1 << 31`), `max_objects` 5,000,000, `max_object_bytes` 512 MiB, `max_delta_depth` 128; currently fixed at call sites (see R-QUOTA) |
| Push-option bound | `serve.rs` `MAX_PUSH_OPTIONS = 256` | Pre-pack read cap |
| Archive serve half | `archive.rs` `serve_upload_archive`; client `smart_http.rs` `fetch_archive` | Only `tar` spoken; client-side verb consumption is a named gap ("No verb consumes this yet") |
| LFS pointer policy | `lfs.rs` `LfsMode::Deny` / `Pointers`; `verbs_transport.rs` `mode_for_store` | Under `Deny`, pushed pointer blobs are refused per-ref with `ng` lines |
| CLI server entry | `ruk/crates/ruk/src/main.rs:330-333`, `cli/mod.rs` `serve_argv` / `serve_verb_for_dashed` | `git-upload-pack '<dir>'` argv[0] (what SSH spawns) and `ruk upload-pack <dir>` both rewrite to the `upload-pack` verb; same for `receive-pack`. v2 selection reads `GIT_PROTOCOL` from `CliEnv` (`verbs_transport.rs` `upload_pack`) |
| Mirror ref mapping (pure) | `transport/mirror.rs` `prune_for_spec`, `push_mirror_updates` | Forgejo syncs mirrors with plain git commands; LFS content sync is a named gap |

### 1.2 Daemon IPC (what rukie speaks to)

- Socket: Unix socket only. Non-Unix `serve()` returns `Error::Unsupported` (`daemon/mod.rs:651-656`).
- Handshake: stream-zero `Open` carrying `HandshakeHello{protocol_major, profile, object_formats, features}`; daemon negotiates via `ipc::negotiate` (`daemon/ipc.rs`). Current profile string is `"forgejo"` (`daemon/tests.rs:322` handshake fixture; `ipc.rs` docs "such as `forgejo`"). **R1 renames this to `rukie`** (see §7).
- Features: `SUPPORTED_FEATURES = ["credits", "merge-notices", "outcome-lookup"]` (`daemon/ipc.rs`).
- Frame: big-endian `u32` length + `kind: u8` + `stream_id: u64` + payload (`daemon/ipc.rs` module docs). Kinds (`FrameKind`): `Open=1, Accepted=2, Stdin=3, StdinEof=4, Stdout=5, Stderr=6, WindowUpdate=7, Cancel=8, MergeNotices=9, Exit=10, Error=11`.
- Windows: `INITIAL_STREAM_WINDOW_BYTES` 256 KiB, `MAX_STREAM_WINDOW_BYTES` 16 MiB, `MAX_DATA_CHUNK_BYTES` 64 KiB (`daemon/ipc.rs` consts).
- Timeouts: `DEFAULT_HANDSHAKE_TIMEOUT_MS` 5,000, `DEFAULT_FRAME_TIMEOUT_MS` 60,000 (`daemon/mod.rs` consts).
- Auth at daemon layer is **transport identity only, not user authorization**: `authenticate_peer` via `SO_PEERCRED` with an `allowed_uids` allowlist (`daemon/ipc.rs` `authenticate_peer`), plus path confinement — `Registry` resolves caller paths to canonical repos under configured roots (`daemon/registry.rs` `open`/`resolve`), and `validate_command_fs` confines `CommandOpen.repository`/`dir` and `GIT_DIR`/`GIT_WORK_TREE` under roots (`daemon/mod.rs` `validate_command_fs`, `check_env_path_under_roots`). Repository ACLs (owner/repo visibility, per-user read/write) are rukie-side and MUST be evaluated **before** opening the daemon stream (§6); the daemon is additionally locked to rukie's uid via `allowed_uids` at deploy time.
- Two stream kinds:
  - **Typed invocation**: `InvocationOpen{repo, method, request_id?, args_json?, session_id?}` → `InvocationAccepted{stream_id, method, repo_handle}` → `ExitReport{status, code, outcome, completed_at}` / `ErrorReport{code, message}` (`daemon/mod.rs:304-383`).
  - **Command stream** (the git path): `CommandOpen{session_id, repository, request_id, argv, dir, env, command_digest, stdout_credit, stderr_credit}` → `CommandAccepted{stream_id, stdin_credit, invocation_id}` → `CommandExitReport{exit_code, signal, cancelled, outcome, operation_ids, invocation_id}` (`daemon/mod.rs:390-477`). `argv` excludes `argv[0]`; daemon prepends its own program name. Env is hermetic (`KEY=value` only). `command_digest` today covers argv, directory, and environment only (`daemon/mod.rs:405-409`); R-QUOTA extends the digest input to the new `limits` field (see §7). Digest + `request_id` give idempotent reconnect: replays recorded terminal report (`replay_command`); digest mismatch refuses (`claim_request_slot`).
- Coordinator dispatch for typed streams: `dispatch_method` requires a session for reads/mutations; reads run through `HostedCoordinator::read` (only `op.log`, `claim.list`, `attest.list` **first pages** served; all other reads name their missing implementation); mutations through `prepare_for_dispatch` + `submit_prepared` with a fixed covered list (`ref, new, claim, ws.new, pin, attest, describe, split, rebase, squash, abandon, edit, ws.scope, ws.take, ws.drop, resolve, revert, undo, land.prepare`) — see `coord_call_site` (`daemon/mod.rs:611-630`) and `dispatch_method` docs. Git-wire ref updates do **not** flow through this path (see §5.3 binding).
- Outcome ledger: `MAX_OUTCOME_RECORDS` 10,000 per process, evict-oldest.

## 2. Smart HTTP endpoints rukie exposes

Base: `https://<host>/<owner>/<repo>.git` (also accept without `.git`; canonicalize with a redirect on GET discovery only, never on POST — see below).

| # | Method + path | Git semantics | rukie behavior |
|---|---|---|---|
| H1 | `GET /{owner}/{repo}.git/info/refs?service=git-upload-pack` | Fetch discovery; v0 or v2 depending on the `Git-Protocol` request header | Auth (anonymous allowed iff repo public, §3) → authorize read → daemon command stream `["upload-pack", <repo-path>]` with `GIT_PROTOCOL` forwarded verbatim → reply `200`, `Content-Type: application/x-git-upload-pack-advertisement` (`SmartService::advertisement_type`, `smart_http.rs`), `Cache-Control: no-cache`, body forwarded verbatim from the daemon: (a) no `Git-Protocol: version=2` on the request → v0 form `# service=git-upload-pack\n` + flush + v0 advertisement; (b) `Git-Protocol: version=2` → **v2 capability advertisement bytes as written by `serve_upload_pack_v2` (`encode_capability_advertisement` first, `serve_v2.rs`), with NO `# service=` preamble and NO v0 flush**. rukie branches on the request header and never synthesizes either preamble itself |
| H2 | `GET /{owner}/{repo}.git/info/refs?service=git-receive-pack` | v0 push discovery (v2 defines no push) | Same as H1 case (a) for `receive-pack`; **requires authenticated identity with push permission** (never anonymous) |
| H3 | `POST /{owner}/{repo}.git/git-upload-pack` | v0 fetch RPC or v2 `ls-refs`/`fetch` RPCs (distinguished inside the body, not by URL) | Auth/authorize read → daemon `upload-pack` stream with `GIT_PROTOCOL` forwarded → pipe request body in as stdin frames, stream stdout frames out with `Content-Type: application/x-git-upload-pack-result` (`SmartService::result_type`). One daemon stream per POST (stateless) |
| H4 | `POST /{owner}/{repo}.git/git-receive-pack` | Push RPC (commands + pack) | Auth/authorize write → daemon `receive-pack` stream (hooks enabled per §5, limits per R-QUOTA) → pipe body in, stream report out with `Content-Type: application/x-git-receive-pack-result` |
| — | `GET ...?service=git-upload-archive` | Archive discovery | **Refused** `403 Service not enabled` (mirrors `git/http-backend.c select_service`, cf. `smart_http.rs SmartService::UploadArchive` docs) |

Header and body rules:

- `Git-Protocol: version=2` on H1/H3 is forwarded verbatim into the command env (`GIT_PROTOCOL=version=2`); the daemon verb selects the v2 serve half (`verbs_transport.rs upload_pack`; `serve_v2.rs git_protocol_requests_v2`). Ruk's `discover_versioned` proves the contract: one GET with the `Git-Protocol` probe parses either the v2 capability advertisement or, from a server that ignored the header, the v0 document (`smart_http.rs discover_versioned`). rukie never parses v2 bodies.
- Request `Content-Type` must be `application/x-<svc>-request` (`SmartService::request_type`); mismatch → `400` before touching the daemon.
- Response content types use `advertisement_type` / `result_type` exactly — stock git and Ruk's `discover`/`discover_versioned` match them exactly, anything else is treated as dumb-http.
- `POST` redirects: never emit. Ruk clients treat any 3xx on RPC endpoints as an error naming `Location` (`smart_http.rs` module docs). Canonicalization redirects apply to H1/H2 only.
- Status mapping mirrors Ruk client expectations (`smart_http.rs check_status`): `404` = missing repository, `401/403` = auth failure.
- `POST .../git-upload-archive` is not exposed.

## 3. Auth (HTTP)

- Scheme: HTTP Basic; **token as password, username ignored-but-logged**. Also accept `Authorization: Bearer <token>` (Ruk's own client sends Bearer from `GIT_AUTH_TOKEN`, else Basic from `GIT_AUTH_USER`/`GIT_AUTH_PASSWORD` — `smart_http.rs` module docs). Accepting both keeps Ruk-as-client (mirrors, R4) working against rukie.
- Token types (rukie metadata, Phase F auth): per-token scope (`read` / `write` / `ci` / `admin`), bound to a human account, optional expiry, hash stored. Agent tokens are scoped tokens on human accounts (settled decision — no agent principals).
- Anonymous: allowed on H1/H3 **iff repo is public**. H2/H4 always require authentication. No code path may reach the daemon on H2/H4 without an identity.
- Only `GIT_PROTOCOL` (and narrowly scoped `RUK_*` hook-callback vars, §5.4) pass from the request into the daemon env. There is no header allowlist to implement.
- Rate limiting: per-token + per-IP token-bucket on H3/H4 (count bytes, not requests); stricter bucket for unauthenticated H1 on public repos. rukie middleware, not Ruk.

## 4. Streaming request/response bodies through daemon stdin/stdout frames with credit flow

One HTTP request = one daemon command stream on a shared Unix-socket connection (connection pool in the rukie `rukdaemon` client module, Phase K4). Mapping:

```
HTTP request body chunks  → IPC Stdin frames (≤64 KiB each) … StdinEof
IPC Stdout frames         → HTTP response body chunks
IPC Stderr frames         → rukie per-request log (verb stderr only — NOT hook
                            post-receive diagnostics; see §5.3/R-5.5)
IPC Accepted              → headers may now be sent
IPC Exit{status 0}        → finish response normally
IPC Exit{nonzero}/Error   → H1/H2: 500; H3/H4 mid-body: truncate + log
                            (git treats a truncated body as a failed RPC)
IPC Cancel (stream)       → on client disconnect
```

Credit flow (per `daemon/ipc.rs` windows):

- Outbound (daemon→rukie stdout): daemon sends up to the window (`INITIAL` 256 KiB + `stdout_credit`/`stderr_credit` from `CommandOpen`), then waits. rukie grants more via `WindowUpdate{direction: "stdout"/"stderr", bytes}` as it flushes to the HTTP socket. **rukie MUST auto-grant (grant-on-flush)**: stopping grants while still reading deadlocks the pack mid-stream. Cap outstanding at `MAX_STREAM_WINDOW_BYTES` 16 MiB.
- Inbound (rukie→daemon stdin): `CommandAccepted.stdin_credit` is always positive; rukie sends request chunks as they arrive (no whole-body buffering), ending with `StdinEof`. Per-stream effective limits travel in `CommandOpen.limits` (R-QUOTA) and are covered by `command_digest` (§7).
- **Current inbound grants are NOT backpressure**: `absorb_stdin_chunk` (`daemon/mod.rs`) extends the in-memory buffer and then grants credit back, so the grant reflects bytes copied into daemon RAM, not bytes consumed by any child — with `serve_ready_command` collecting via `collect_command_stdin` to a `Vec<u8>` and running `run_captured` (which returns one whole `CliOutcome{stdout, stderr}`, `cli/mod.rs:209-213`) only after stdin EOF, a large push is fully buffered (cap `MAX_COMMAND_STDIN_BYTES` 256 MiB) before execution starts, and outputs are fully buffered before the first stdout frame. The R-7.1 streaming replacement MUST define credit against actual pipe progress: grant inbound credit only after the child stdin pipe accepts the bytes, and emit outbound frames while the child runs (generalizing `write_cmd_data`/`await_outbound_credit` to a live child) — otherwise memory scales with pack size and "streaming" is fake.
- Sequential-stream constraint: `command_inner` owns the whole connection while collecting stdin + running (`daemon/mod.rs command_inner` docs: "A long-running command blocks sibling streams on the same connection … frames for other streams arriving mid-command are buffered and replayed"). **rukie therefore opens N pooled connections, not N streams on one connection** — one git RPC per connection — until R-7.2 lands.

### Gzip request bodies

Stock git sends `Content-Encoding: gzip` on large POSTs. rukie MUST:

1. If `Content-Encoding: gzip` present, wrap the request reader in a streaming gzip decoder **before** framing into `Stdin` chunks (the daemon speaks raw pkt-lines and knows nothing of HTTP framing).
2. Enforce `Content-Length` / decoded-bytes caps during decode (see §6, R-QUOTA) — decompression bombs are bounded by the same max-pack budget.
3. Never forward `Content-Encoding` to the daemon env.

(Ruk has no server-side HTTP code — `http.rs` server hits are test-only `fake_server` helpers — so there is no Ruk gzip behavior to reuse; this is pure rukie work.)

## 5. Receive-pack hooks: how rukie learns ref updates

### 5.1 Enabling hooks on the daemon path (Ruk change required)

Today the daemon's `receive-pack` verb calls `serve_receive_pack` (no-hooks historical entry: `serve.rs:500-532` — "Hook phases are disabled … Pass `ReceiveHooks` to `serve_receive_pack_with`"), and `verbs_transport.rs receive_pack` likewise calls the no-hooks half. So **no hook ever runs for daemon-served pushes today**. Ruk change R-5.1 (§7): daemon `receive-pack` command streams enable hooks via `serve_receive_pack_with` with a rukie-owned hook directory.

Path-precision constraint (binding): hook resolution is `<repo_dir>/hooks/<name>` and a missing script is a pass (`hooks.rs` module docs; `ReceiveHooks::for_repo` docs: "Run hooks from `<repo_dir>/hooks/`"). Therefore `ReceiveHooks::for_repo(<repo>/.ruk-hooks)` resolves scripts at `<repo>/.ruk-hooks/hooks/<name>` — **not** at `<repo>/.ruk-hooks/<name>`. The rukie installer MUST write shims to the exact resolved paths (including `proc-receive`), and rukie MUST verify at startup (and on repo create) that each hosted repo's resolved shim paths exist and are executable. A missing script passes silently, so installer/resolver disagreement means policy hooks silently never run — treat "resolved hook path missing" as a serving error for hosted repos, not a pass. R-5.4 adds an explicit hook-dir constructor (e.g. `ReceiveHooks::for_hook_dir`) resolving `<hook_dir>/<name>` directly, eliminating the nested `hooks/hooks` ambiguity; until it lands, the double-nested path is the contract — installer and resolver must agree on it verbatim.

Hook scripts are **rukie-owned, installed by rukie** (not user executables): `pre-receive` = policy shim, `post-receive` = notification emitter, `proc-receive` = change-creation shim. They call back to rukie over a second Unix socket / internal HTTP endpoint carrying the invocation id plus a per-invocation credential (§5.4).

### 5.2 Synchronous pre-receive policy check (protected bookmarks)

1. Daemon stages pack in quarantine (unpublished), runs `pre-receive` with `<old> <new> <ref>` lines on stdin.
2. rukie's `pre-receive` shim calls rukie-internal (`POST /internal/hooks/pre-receive {invocation_id, lines[], push_options}` with per-invocation Bearer credential, §5.4), which evaluates protected-bookmark rules (e.g. `main` requires change + CI green; tags immutable; deletions forbidden on protected refs), then answers pass/refuse-with-message.
3. Refusal text = hook stderr first-non-blank-line → in-band `ng` line (`hooks.rs decline_message`, `sanitize` in `serve.rs`). Nonzero `pre-receive` exit refuses the whole push (every live ref `ng`); `update`-phase refusal marks only its ref. Keep both: `pre-receive` for push-wide policy (one round trip), `update` for per-ref messages.

Timeout: hook budget 60s (`HOOK_TIMEOUT`); policy callback inside it gets 10s, fail-closed ("policy check timed out").

### 5.3 Post-receive notification → CI, webhooks, mirrors (with durable fallback)

After refs land via the CAS path (`session::apply_refs`), the daemon runs `post-receive` with landed `<old> <new> <ref>` lines, and only then sends the per-ref report — so `post-receive` latency is client-visible push latency. Hook semantics are fixed and binding: a `post-receive` failure never fails the push, and its diagnostics reach nowhere observable today — `serve_receive_pack_with` has no stderr writer for hook output, the failure path calls `tracing::warn` (`serve.rs` post-receive phase) with no `tracing` subscriber installed anywhere in `ruk/crates`, so the warn emits nothing: not a per-stream `Stderr` frame, not even a daemon-global log line. The push therefore succeeds even when notifications vanish. The design treats this as the normal case and makes notification loss recoverable without slowing every push:

1. The `post-receive` shim MUST deliver landed state to rukie through its authenticated callback (`POST /internal/hooks/post-receive {invocation_id, landed[], alternates[]}` with the §5.4 Bearer credential) — that callback, not IPC frames, not daemon logs, and not `op.log`, is the CI/webhook/mirror trigger. A shim that only prints diagnostics and exits 0 notifies rukie of nothing. Post-receive failure text observable by rukie is only what the callback carries (or what the shim echoes on its own stderr channel if R-5.5 adds one). Daemon-log scraping is never the notification or recovery path: there is nothing to scrape.
2. Spool **before** callback, with a bounded hook budget: on entry the shim fsyncs a spool record `{invocation_id, repo, landed[], alternates[], actor, attempted_at}` to a local durable spool directory sibling to the repo (one file per invocation) **first**, then attempts the callback with a short timeout (single attempt ~2–3s plus at most one retry inside the remaining hook budget). On success it removes the spool record and exits 0; on failure it exits 0 leaving the spool for the drain worker. Ordering is load-bearing: spool-first closes the crash window between callback attempts (a kill during or after the callback attempt still leaves the spool), and a single short attempt keeps rukie outages from adding up to 60s to every push — the async drain, not the hook, absorbs the retry budget. A rukie spool-drain worker (every ~30s) replays spool records into the callback path with the same idempotency key.
3. rukie resolves the invocation → (repo, actor, push options) from its pending-push table, then enqueues **atomically** in one metadata-store transaction (outbox pattern, Phase K3/F): either all of {CI scheduler scan, Standard Webhooks signing queue, mirror queue (exact vs ff-only per mirror, ref globs), inbox entries} are durably enqueued, or a retry record persists and the callback reports failure (which keeps the spool record alive). Partial enqueue (e.g. CI queued but webhook lost) is forbidden — one transaction or one retry record. The outbox row is therefore persisted **before** hook success is acknowledged: the callback returns success to the shim only after the transaction commits; any failure before commit leaves the spool record plus the pending-push row for drain/reconcile retry.
4. Callback handling is idempotent on `invocation_id`: retried callbacks and spool replays deduplicate against the pending-push/outbox row, so at-least-once delivery never double-enqueues CI builds or double-signs webhooks.
5. Independent reconciler closes the remaining gap (shim host crash between ref-land and spool write). Binding: git-wire ref updates apply through `session::apply_refs` → `Store::publish` and emit **no** coordinator operation record, so `op.log` contains no git-push entries and **no `op.log` tail, however complete, can find this crash window** — there is no journaled ref-update source today. The reconciler therefore works in two layers: (a) v1, no Ruk change — **lossy** tip-comparison sweep: periodically list current ref tips (via the R-2.2 paginated-refs read) and compare against rukie's `last_notified_tip` table per repo/ref; any `current != last_notified` without a committed outbox row is enqueued with `old = last_notified`, `new = current`, provenance `reconciled:true`, watermark advancing only on outbox commit. This layer is explicitly best-effort and degraded: concurrent pushes between sweeps collapse (A→B→C appears as A→C, intermediate B lost; `old` may be stale under races). It guarantees eventual convergence of mirrors/CI-scan state but **not** exact per-push webhook fidelity. (b) Ruk change R-REFJOURNAL — durable ref-update journal: `serve_receive_pack_with`, after the CAS apply, appends `{invocation_id, ref, old, new, received_at}` records to a per-repo durable journal, so the reconciler can attribute recovered updates exactly instead of inferring old/new from watermarks. Exact per-push webhooks and exact mirror old/new provenance require (b); until it lands, v1 notifications are convergent but lossy under concurrency. No recovery path in this design depends on `op.log` containing git pushes.
6. R-5.5 adds explicit diagnostic plumbing: a per-stream hook-diagnostic writer (or `MergeNotices` payload / landed-report return value) so post-receive failures and informational lines reach rukie keyed by invocation id instead of vanishing (current state: not even `tracing` output exists without a subscriber).

Ruk change: `serve_receive_pack_with` returns `()` — the landed set and `AlternateRef`s reach observers only as `post-receive` stdin text. The shim re-parses stdin lines (sufficient for v1); the structured-report change (R-5.3), the diagnostic-plumbing change (R-5.5, including installing a real `tracing` subscriber or removing the misleading `warn`), and the ref-journal change (R-REFJOURNAL) are follow-ups, but the authenticated spool-first callback with atomic outbox and lossy tip-comparison reconcile above is not a follow-up — it is the v1 notification path.

### 5.4 Invocation identity and hook-callback credential wiring

`CommandOpen.request_id` is chosen by **rukie** (e.g. `push-<ulid>`), so reconnects, hook callbacks, spool records, reconciler findings, and the outbox join on one id. The daemon echoes it as `CommandExitReport.invocation_id` (`daemon/mod.rs`) and the outcome ledger replays it on retry (`replay_command`). rukie's pending-push table maps `invocation_id → {repo, actor, token id, push options, started_at}`, deleted only after the outbox transaction commits (not on hook exit) so retries and spool replays still resolve; terminal `Exit`/`Error`/Cancel without a committed outbox row leaves a reconciler-visible gap instead of deleting state.

Two properties that must not be confused:

- `invocation_id` is a **correlation id, not a secret**: it is echoed in `CommandExitReport`, appears in logs and spool filenames, and is replayable by design (outcome-lookup). It MUST NOT authorize hook callbacks on its own — any caller that can reach the internal endpoint and guess a ULID could otherwise approve pushes or enqueue CI/webhooks.
- Hook-callback authorization is a **per-invocation random credential**: rukie generates a fresh 256-bit `RUK_HOOK_TOKEN` per command stream, binds it to the invocation id server-side (pending-push row), and delivers it to hook children only via the new hook-env plumbing (R-5.2). Shims present it as `Authorization: Bearer <token>` on every internal callback (`pre-receive`, `proc-receive`, `post-receive`) and on spool-replay. rukie verifies token + invocation binding, single-invocation scope, and expiry on outbox commit (not on hook exit, so spool replay stays authorized until drained). The callback endpoint itself binds to a Unix socket (preferred; `SO_PEERCRED`-gated to the daemon uid) or localhost-only TCP — never a routable interface.

Env-plumbing constraint (binding): daemon `CliEnv.vars` is a hermetic map for the verb (`daemon/mod.rs` `ReadyCommand{vars}`, `validate_command_fs`), while `hooks::execute` spawns hook children via `std::process::Command` inheriting the daemon process environment plus `apply_hook_env` additions (`hooks.rs` `execute`, `apply_hook_env`); `ReceiveHooks` stores only paths and timeout, and no `RUK_*` map reaches `Command` today. Setting `RUK_*` in `CommandOpen.env` therefore does **not** reach hook children without a code change. R-5.2 must add an explicit extra-env channel (e.g. `ReceiveHooks` field `hook_env: Vec<(String, String)>` or new constructor argument) threaded through `run_hook` → `execute` → `apply_hook_env` → `Command::env`, carrying at minimum `RUK_INVOCATION_ID`, `RUK_HOOK_CALLBACK`, `RUK_HOOK_TOKEN`, plus spool-directory path. Audit under R-7.3: selective `env()` additions only, never full-process-env inheritance widening, and the same confinement review as `GIT_DIR`/`GIT_WORK_TREE`.

### 5.5 `refs/for/<branch>` pushes creating or updating changes (synchronous — no spool-and-`ok`)

- Client pushes to `refs/for/main` (also accept `refs/for/<branch>/<topic>`, topic as title hint; legacy `refs/pull/*` falls through as normal refs).
- Daemon diverts matching commands to `proc-receive` when the hook exists (`PROC_RECEIVE_PREFIXES`, `proc_receive.rs`; match rule: equal to prefix or extends past `/`, `is_proc_receive_ref`). The hook path is the same resolved path as §5.1 (`<repo>/.ruk-hooks/hooks/proc-receive` until R-5.4 lands, then the explicit hook dir) — installer must write it there.
- rukie's `proc-receive` shim speaks the hook protocol verbatim (`converse`: `version=1` + flush both ways, commands + flush, push options on request, `ok`/`ng`/`option` report — `proc_receive.rs` module docs): per command it calls rukie-internal (authenticated per §5.4) to create-or-update the change (new change-id or topic match on target branch; new patchset row), then answers `ok <ref>` + `option refname refs/changes/<nn>/patchset/<k>` (+ `option old-oid/new-oid`). `option fall-through` returns unowned refs to the normal CAS path.
- Binding verdict rule: an `ok` line reports the review hook accepted the command (`proc_receive.rs`; no normal ref is written for diverted commands). The §5.3 spool-and-exit-0 fallback applies to `post-receive` notifications **only** and MUST NOT be reused here: the shim answers `ok` **if and only if** the change/patchset transaction commits synchronously inside the hook budget. Any callback failure — timeout, error, rukie down, or an uncommitted transaction — answers `ng <ref> <reason>` (with a short reason line), never `ok`-plus-spool. A spooled-but-uncreated change may be kept as an operator-recovery record, but it is not a success: reporting `ok` while no change or patchset exists lies to the client (the pusher believes the review exists; later fetches prove otherwise) and breaks idempotent retry (the client will not retry a push the server called `ok`). Fail-closed `ng` preserves exactly-once client semantics: the pusher retries and either creates the change once or observes the existing one.
- Client-visible result on success: original ref reports `ok` (review backend owns the alternate write — Forgejo `UpdateRef` semantics in `proc_receive.rs` module docs); `post-receive` observes the hook-supplied review ref (`alternate_line` in `serve.rs`).
- Diverted commands **skip the `update` hook and the normal ref update entirely**, so `refs/for/*` push-time policy lives in the shim (always allowed for writers; target-branch mergeability is review-time, not push-time). The synchronous creation call fits the same 60s hook budget with a short internal timeout (e.g. ≤10s); on timeout the verdict is `ng`, not deferred success.
- Caveat carried over from Ruk: with no `proc-receive` script, commands fall through to the normal path (Ruk deliberately differs from the oracle, which would refuse — `proc_receive.rs` module docs). rukie always installs the shim plus the §5.1 existence check; a `refs/for/*` ref appearing as a real ref is the installation-failure alarm as defense in depth.

## 6. SSH server

Ruk has no SSH server (no ssh-server dependency in `crates/*/Cargo.toml`). rukie builds its own with `golang.org/x/crypto/ssh`:

- Host keys: ed25519 from config at startup; rotation = config change + restart (no hot-reload in v1).
- Auth: `PublicKeyCallback` mapping presented key → user via fingerprint lookup in the metadata store (`sha256:` canonical form). No password/kbd-interactive/agent-forwarding/shell/pty/subsystem — `exec` only. Unknown key → reject before repo resolution.
- Exec requests: accept exactly `git-upload-pack '/owner/repo.git'` and `git-receive-pack '/owner/repo.git'` (single-quote form stock git sends; also the bare-argument spelling). Strict grammar: command in {upload-pack, receive-pack}, one argument matching the owner/repo shape (optional `.git`). Anything else → exit 127.
- `GIT_PROTOCOL`: honored from `SendEnv` acceptlist (`env GIT_PROTOCOL`) → forwarded into the daemon command env. Absence = v0.
- Authorization **before** opening the daemon stream: resolve (owner, repo) → visibility + permission check (read for upload-pack, write for receive-pack) → only then `CommandOpen`, with a fresh per-stream `RUK_HOOK_TOKEN` and R-QUOTA limits for receive-pack (§5.4, §7). Failed authz never touches the daemon.
- I/O mapping: SSH channel stdin/stdout/stderr ↔ daemon `Stdin`/`Stdout`/`Stderr` frames exactly as §4 (same pooled-connection client, same grant-on-flush). SSH and daemon-credit windows are independent — the bridge pumps both.
- Session end: daemon terminal `Exit` → channel exit-status. Hook-refusal pushes exit 0 at the pack layer (refusals are in-band `ng` lines, per `serve.rs` docs) — SSH status reflects transport health, not per-ref outcomes. `proc-receive` `ng` verdicts (§5.5) are likewise in-band per-ref outcomes, not transport failures.

## 7. Timeouts and limits

| Knob | Value | Enforced where | Ruk source |
|---|---|---|---|
| Max pack / push bytes | 100 MiB default, per-repo override up to 2 GiB | HTTP `Content-Length` gate + decoded-bytes counter during gzip decode (rukie) using the R-QUOTA limit set; daemon stdin cap + ingest bound per stream (R-QUOTA) | `MAX_COMMAND_STDIN_BYTES` 256 MiB (`daemon/mod.rs`); `IngestLimits::default` 2 GiB (`pack.rs:44-61`); effective per-stream values via R-QUOTA |
| Max push options | 256 | Daemon serve half | `MAX_PUSH_OPTIONS` (`serve.rs`) |
| Idle stream timeout | 60s per frame | Daemon `frame_timeout` | `DEFAULT_FRAME_TIMEOUT_MS` 60,000 |
| Handshake deadline | 5s | Daemon | `DEFAULT_HANDSHAKE_TIMEOUT_MS` 5,000 |
| Hook budget | 60s per hook; `pre-receive` policy callback 10s inside it; `post-receive` spool-first + one short callback attempt (~2–3s) inside the same budget with the remainder via async drain; `proc-receive` creation call ≤10s synchronous, `ng` on timeout (never deferred `ok`) | Daemon `ReceiveHooks`, rukie shims + spool drain | `HOOK_TIMEOUT` (`hooks.rs`); `serve.rs` post-receive-before-`send_report` ordering makes this client-visible |
| HTTP handler budget | upload-pack 10 min (large clones), receive-pack 5 min | rukie HTTP timeouts + context cancel → IPC `Cancel` | New (rukie) |
| SSH idle | 10 min, keepalive 60s | rukie SSH server | New (rukie) |
| Control frame bound | 1 MiB JSON | Daemon | `MAX_IPC_CONTROL_BYTES` |
| Data chunk | 64 KiB | Daemon framing | `MAX_DATA_CHUNK_BYTES` |
| Hook token lifetime | single invocation; expires on outbox commit / change commit (not hook exit, so spool replay stays authorized) | rukie pending-push table + callback verifier | New (rukie + R-5.2 plumbing) |
| Spool / reconciler lag | spool drain every ~30s; lossy tip-comparison sweep every ~5 min | rukie workers (reconciler reads via R-2.2) | New (rukie) |

Cancellation: client disconnect (HTTP) / channel close (SSH) → IPC `Cancel` → daemon cancels before commit (`CancelledBeforeCommit` path, `cancel_command`); pending-push row and hook token are revoked only after the outbox/reconciler decision, not at hook exit, so a cancelled push cannot orphan an un-notified landing.

## 8. Exact Ruk-side changes needed (files/functions)

Breaking changes allowed (neither project released). Gateway work (HTTP listener, Basic/Bearer auth, gzip decode, SSH server, ACLs, outbox, spool drain, tip-comparison reconciler, CI/webhook/mirror workers) is rukie-side and NOT listed here — only Rust streaming, hook, limit, environment, journal, logging, and protocol-schema changes are Ruk work.

**R-1 forge-neutral `ruk serve` (Phase R1)**

- R-1.1 `crates/ruk/src/daemon/mod.rs`: rename `ruk forgejo serve` → `ruk serve` (module docs, `serve()`, thread name `"ruk-forgejo-serve"`, `DaemonConfig::from_toml` docs). All `forgejo` strings in `daemon/`, `daemon/ipc.rs` (`HandshakeHello.profile` docs), `daemon/tests.rs` handshake fixtures.
- R-1.2 `crates/ruk/src/daemon/ipc.rs`: handshake profile `"forgejo"` → `"rukie"`; keep accepting `"forgejo"` for one dev cycle behind a compat flag, then remove. `SUPPORTED_FEATURES` unchanged.
- R-1.3 `crates/ruk/src/daemon/registry.rs`: no functional change expected (path/handle authorization is already forge-neutral); remove Forgejo-layout comments; verify `resolve` + `handle_for_path` cover the `<root>/<owner>/<repo>.git` layout rukie chooses (integration test).

**R-2 forge read methods (Phase R2)**

- R-2.1 Tree listing: new typed read (paginated `{path, kind, oid, size}` rows). Touches: `ruk-core` protocol schema source (Method + args/result types; schema snapshot regenerates), `HostedCoordinator::read` new arm, `dispatch_method` coverage. No current read serves tree data.
- R-2.2 Paginated refs + merge-base/compare reads. Same files. The paginated-refs read doubles as the reconciler's tip source (§5.3 layer (a)). (Wire `ls-refs` in `serve_v2.rs` is unauthenticated protocol, not a forge read — not reusable for visibility-filtered browsing.)
- R-2.3 Streaming archive: expose `archive.rs serve_upload_archive` as a daemon command-stream verb with true streaming (see R-7.1); `verbs_transport.rs` already serves `upload-archive` over live pipes — the daemon path needs the same verb.
- R-2.4 Repo lifecycle: typed mutations `repo.create/delete/rename/fork` (new methods; coordinator arms; registry create/delete of on-disk repos). `Registry::open` is lazy-open only (`registry.rs`); creation/deletion have no entry point.
- R-2.5 `op.log` tail subscription: today first-pages-only single-shot (`coord_call_site`). Either long-poll `op.log.tail{after_seq}` or `MergeNotices`-frame push of ref-update events. `FrameKind::MergeNotices` exists but no producer for ref updates was found. Decide long-poll in R2 (simpler, matches the window model). Binding limit: even a complete `op.log` tail covers coordinator mutations only — the §5.3 crash-gap reconciler cannot rely on it until R-REFJOURNAL lands, and uses tip-comparison (R-2.2) meanwhile. No recovery path in this design tails `op.log` for git pushes.

**R-3 search (Phase R3)**: new typed read `search` (filename + bounded-revision content grep, no persistent index). New `crates/ruk-core/src/search.rs` (pure) + coordinator read arm + `dispatch_method` coverage. No existing search code.

**R-4 push with ephemeral credentials + per-ref status**

- R-4.1 Ephemeral mirror credentials: `push.rs push_over` and `smart_http.rs push_pack`/`fetch_pack` take credentials only from process env (`GIT_AUTH_TOKEN/USER/PASSWORD`), credential helper, and resolved `extra_headers` (`smart_http.rs request_headers_with`). Thread per-call credentials (`PushCredentials{bearer?, basic?}`) through `push_pack_inner`, `fetch_pack`, `discover_versioned`, `ls_refs_v2`, `fetch_pack_v2`. No protocol change. Mirror secrets must never touch process env or disk.
- R-4.2 Per-ref status already exists (`push.rs RefOutcome{name, result: Ok/Rejected{reason}/Uncertain}`, `parse_report`, `PushPlanRecord::{finalize, refuse_locally}`) — rukie consumes as-is.
- R-4.3 `reconcile` exists as pure observation (`push.rs reconcile`: "Observation is not causal proof") — rukie drives it on a timer; no Ruk change.

**R-5 receive-pack host notification + refs/for (Phase R5)**

- R-5.1 `crates/ruk/src/cli/verbs_transport.rs receive_pack`: route to `serve_receive_pack_with` with a rukie-owned hook directory. Name the exact resolved paths in code and installer contract: until R-5.4 lands, `ReceiveHooks::for_repo(<repo>/.ruk-hooks)` resolves to `<repo>/.ruk-hooks/hooks/{pre-receive,update,post-receive,proc-receive}`; installer writes exactly there; serving treats a missing resolved shim as an error for hosted repos (overriding the library's missing-is-pass default). Both the buffered-`stdin` and live-pipe arms. Hook failure semantics stay oracle-compatible (`serve.rs` post-receive phase: warn, never reject) — durability comes from the §5.3 spool-first callback/drain/reconcile layer, not from turning hook failures into push failures. `proc-receive` verdict semantics are the exception only in the fail-closed direction (§5.5: `ng` unless committed).
- R-5.2 `daemon/mod.rs` (`validate_command_fs` / `ReadyCommand`) + `crates/ruk-git/src/transport/hooks.rs` (`ReceiveHooks`, `run_hook`, `execute`, `apply_hook_env`): add an explicit hook-env channel for `RUK_INVOCATION_ID`, `RUK_HOOK_CALLBACK`, `RUK_HOOK_TOKEN`, plus spool-directory path (new `ReceiveHooks` fields or constructor arguments, threaded into `Command::env` via `apply_hook_env`). `CommandOpen.env`/`CliEnv.vars` alone is insufficient — hook children are spawned by `execute` from the daemon process environment plus `apply_hook_env` additions and never see the verb's hermetic `vars` map. Selective `env()` additions only.
- R-5.3 `transport/serve.rs serve_receive_pack_with`: return the landed report (`Vec<(refname, old, new)>` + `Vec<AlternateRef>`) instead of `()`. Non-blocking for rukie v1 (shims parse `post-receive` stdin) but do it while R-5.1 touches the signature; the returned report also feeds R-REFJOURNAL's journal write in the same commit window.
- R-5.4 `transport/proc_receive.rs` + `hooks.rs`: explicit hook-dir constructor (e.g. `ReceiveHooks::for_hook_dir`) resolving `<hook_dir>/<name>` directly, so the steady-state layout is `<repo>/.ruk-hooks/{pre-receive,update,post-receive,proc-receive}` with no nested `hooks/` level; keep `for_repo` default (`<repo_dir>/hooks/<name>`) for compatibility. Migrate installer and R-5.1 to the new constructor when it lands.
- R-5.5 Hook diagnostics and logging: `serve_receive_pack_with` has no stderr writer for hook output; the post-receive failure path calls `tracing::warn` but no `tracing` subscriber is installed anywhere in `ruk/crates`, so the warn emits nothing — not a per-stream `Stderr` frame, not even a daemon-global log line. Either install a real subscriber (daemon-global log sink) or remove the misleading `warn` and document silent-by-design; in both cases add an explicit per-stream diagnostic channel for hook phases (e.g. a `&mut (dyn Write)` diagnostic writer threaded into `serve_receive_pack_with` → hook phases, or hook diagnostics folded into the R-5.3 landed report / a `MergeNotices` payload), keyed by invocation id. Until it lands, the binding rule is: rukie receives post-receive failure text only through the shim's authenticated callback payload — never through IPC `Stderr` frames, never via daemon logs (which do not exist for this path), and never via `op.log`.

**R-REFJOURNAL durable ref-update journal (Phase R5)**

- Problem: `session::apply_refs` applies git-wire ref updates via `Store::publish` and emits no coordinator operation record, so `op.log` contains no git-push entries even after R-2.5 is complete. A crash between ref publication and spool creation is therefore invisible to any `op.log` tail — the reconciler cannot attribute it.
- Change: after the CAS apply in `serve_receive_pack_with` (same commit window as R-5.3), append durable per-ref journal records `{invocation_id, ref, old, new, received_at}` to a per-repo journal (either coordinator `op.log` entries for git applies or a new lightweight ref-journal file alongside the store; decide in R2 design — journal file is preferred to avoid overloading op semantics). Journal write precedes `post-receive` execution so a crash after publication still leaves an attributable record. The §5.3 reconciler layer (b) consumes this journal; layer (a) lossy tip-comparison covers the window until it lands. Nothing in §5.3 depends on `op.log` containing git pushes.

**R-6 attestation/claim/land extensions (Phase R6)**

- R-6.1 `land.execute` has **no preparation path** (`coord_call_site` covers `land.prepare` but not `land.execute`) — implement the `prepare` arm (the CI-gated submit path review needs).
- R-6.2 `claim.*` exists (`claim.acquire/release/list`) — verify `acquire` supports task semantics (expiry, heartbeat, agent-token holder id as plain string); extend args if the holder model assumes interactive sessions. Confirm in R6 against `ruk-core` claim types.
- R-6.3 `attest.add/list` exist — confirm kinds cover CI results (pass/fail + content-addressed result pointer + runner id); extend the kind enum, not the method.
- R-6.4 Reads beyond first pages (`op.log` history, `attest.list` pagination, `status, log, diff, show, file, blame, query, bookmark/tag resolve…`) need real `HostedCoordinator::read` arms — same work as R-2; review branch-view grouping depends on `log`/`compare` reads.

**R-7 streaming + concurrency (spans R1/R5)**

- R-7.1 True streaming command I/O: replace buffer-then-run (`collect_command_stdin` → `run_captured` → stream captured outputs) with a spawned verb on piped stdio proxied concurrently: stdin frames → child pipe (grant inbound credit **only after the pipe accepts bytes** — current `absorb_stdin_chunk` grants on memcpy, which is not backpressure), child stdout/stderr → frames while running (generalize `write_cmd_data`/`await_outbound_credit` to a live child). Until this lands, the 256 MiB stdin cap contradicts the 2 GiB ingest bound: large pushes cannot arrive via daemon.
- R-7.2 Head-of-line blocking: fix the frame pump so one long command does not own the connection (`command_inner` "documented v1 limitation"), or contractually bless one-RPC-per-connection with a pool-sizing rule (§4 already assumes this). Fixing is better; blessing is acceptable for v1.

[Showing lines 4121-4387 of 4431 (49.9KB limit). Use :4388 to continue]
- R-7.2 Head-of-line blocking: fix the frame pump so one long command does not own the connection (`command_inner` "documented v1 limitation"), or contractually bless one-RPC-per-connection with a pool-sizing rule (§4 already assumes this). Fixing is better; blessing is acceptable for v1.
- R-7.3 Fail-closed audit of the R-5.2 `RUK_*` env hole under `validate_command_fs` / `check_env_path_under_roots`: new hook-env additions get the same confinement review as `GIT_DIR`/`GIT_WORK_TREE`; verify no daemon-process secrets leak into hook children via inherited environment.

**R-QUOTA per-repository pack/limit quotas (Phase R/K)**

- Problem: limits are fixed constants today — `IngestLimits::default` in `crates/ruk-git/src/transport/pack.rs:44-61`, `MAX_COMMAND_STDIN_BYTES` 256 MiB in `crates/ruk/src/daemon/mod.rs`, `MAX_PUSH_OPTIONS` in `serve.rs` — so the §7 table's "100 MiB default, per-repo override up to 2 GiB" is not implementable without a config path and propagation.
- Change: introduce one per-repo limit set `{max_pack_bytes, max_objects, max_object_bytes, max_delta_depth, max_stdin_bytes}` (optionally `max_push_options`). Carry it per command stream — extend `CommandOpen` with an optional `limits` override (preferred: daemon stays stateless across repos; rukie sends the repo's effective limits on each open) — or via daemon config keyed by repo handle with reload; per-stream override is preferred. `verbs_transport.rs receive_pack`/`upload_pack` resolve effective limits and pass them into `serve_receive_pack_with` → `ingest_and_verify` → `ingest_pack` (and closure ingest), and the daemon applies `max_stdin_bytes` as the per-stream stdin-collection cap instead of the fixed constant. Include the effective limits in `command_digest` (`daemon/mod.rs:405-409` covers argv/directory/environment only today): a reconnect replaying one `request_id` with different quota fields must digest-mismatch and refuse rather than replay-or-run under a different bound, preserving idempotency and preventing quota bypass. rukie enforces the same `max_pack_bytes` at the HTTP `Content-Length` gate and the gzip decoded-bytes counter before framing. Default `max_pack_bytes` 100 MiB for hosted repos, per-repo override up to the 2 GiB ingest ceiling; overrides stored in rukie metadata and sent per stream.

## 9. Gaps found in Ruk code (with citations)

1. **Daemon is Forgejo-bound**: `ruk forgejo serve` naming, `thread_name("ruk-forgejo-serve")` (`daemon/mod.rs`), handshake profile `"forgejo"` (`daemon/tests.rs:322`, `daemon/ipc.rs`). Rename for R1; mechanism itself is forge-neutral.
2. **No hooks run on the daemon path**: `receive_pack` verb → `serve_receive_pack` → `serve_receive_pack_with(…, ReceiveHooks::disabled(), …)` (`transport/serve.rs`); `verbs_transport.rs receive_pack` calls the same no-hooks half. `pre-receive`/`update`/`post-receive`/`proc-receive` are dead code for hosted pushes until R-5.1.
3. **Buffer-then-run, not streaming, with fake inbound backpressure**: `serve_ready_command` collects stdin to `Vec<u8>` (`collect_command_stdin`), `absorb_stdin_chunk` grants credit on buffer-extend (not child consumption), `run_captured` returns one whole `CliOutcome{stdout, stderr}` (`cli/mod.rs`), outputs stream only after completion. Memory scales with pack size (R-7.1). `post-receive` additionally runs before `send_report`, so any in-hook retry budget is client-visible push latency — hence spool-first plus one short callback attempt (§5.3), with the retry budget in the async drain.
4. **Connection-level head-of-line blocking**: documented in `command_inner` ("documented v1 limitation").
5. **No server-side HTTP or SSH**: `smart_http.rs` is client-only (`discover*`/`fetch_pack*`/`push_pack`); `http.rs` servers are test fakes; no SSH-server crate in `crates/*/Cargo.toml`. rukie builds both servers from scratch.
6. **No per-call credentials**: push/fetch auth reads process env + helper + `extra_headers` (`smart_http.rs request_headers_with`). Ephemeral per-mirror secrets have no parameter (R-4.1).
7. **`proc-receive` fall-through differs from oracle**: missing script → normal-path fall-through, oracle would refuse when config names prefixes (`proc_receive.rs` module docs). Safe under rukie (shim always installed + §5.1 existence check) but needs the §5.5 installation-failure alarm as defense in depth. Separately, `proc-receive` success semantics forbid the `post-receive` spool-and-`ok` pattern: `ok` means the review exists now, so creation must commit synchronously or the verdict is `ng` (§5.5).
8. **v2 fetch-only, filter/shallow/deepen unhonored**: `serve_v2.rs` module docs ("`receive-pack` stays v0"; "`filter` refused, `shallow`/`deepen` consumed but not honored"); same for v0 in `serve.rs`. Partial clone fetches full history — document, don't fix in v1.
9. **`atomic`/`push-cert` unsupported**: arriving `atomic` rejects every ref (`serve.rs` docs). Multi-ref all-or-nothing stays a review/land-time property.
10. **Reads mostly unimplemented**: only `op.log`/`claim.list`/`attest.list` first pages; everything else is `E_HOSTED` naming its missing path (`coord_call_site`). `land.execute` uncovered. Bulk of R-2/R-6.
11. **No op-log subscription, and `op.log` would not cover git pushes anyway**: `MergeNotices` kind + `merge-notices` feature exist (`daemon/ipc.rs`), no ref-update producer; `op.log` is single-shot first-page reads (R-2.5); and git-wire applies bypass the coordinator (`session::apply_refs` → `Store::publish`, no operation record), so even a complete `op.log` tail is the wrong reconciler source until R-REFJOURNAL lands — the v1 reconciler uses explicitly lossy tip-comparison (R-2.2 + watermark table) and never tails `op.log` for git pushes.
12. **Daemon auth is uid-allowlist + roots confinement only** (`authenticate_peer`, `Registry::open`/`resolve`, `validate_command_fs`); user/token/permission logic is rukie-side, evaluated before open; lock `allowed_uids` to rukie's uid at deploy (configuration).
13. **LFS gaps inherited**: `Deny` refuses pointer pushes per-ref (v1 default); content sync is a named gap (`mirror.rs` docs). LFS HTTP API out of scope for v1.
14. **Hook children do not inherit the verb's hermetic env**: `hooks::execute`/`apply_hook_env` build hook `Command`s from the daemon process environment plus quarantine/push-option additions; `ReceiveHooks` carries only paths and timeout and `Command` receives no `RUK_*` map, while daemon `ReadyCommand.vars` never becomes the hook process environment. Callback identity, credential, and spool-path delivery require the R-5.2 env channel — setting `CommandOpen.env` alone leaves shims unable to identify, authorize, or spool.
15. **Hook-callback endpoint has no credential in the naive design**: `invocation_id` (`push-<ulid>`, echoed in `CommandExitReport`) is a correlation id, not a secret. Unauthenticated `{invocation_id, lines[]}` callbacks would let any network neighbor approve pushes or enqueue CI/webhooks. Requires the §5.4 per-invocation `RUK_HOOK_TOKEN` plus non-routable binding.
16. **Per-repo quotas have no config path, and quota fields are outside the digest**: `IngestLimits` is fixed at call sites and the daemon stdin cap is a process constant, so per-repo 100 MiB / 2 GiB limits need R-QUOTA's per-stream override and propagation through `receive_pack` → `ingest_and_verify` → `ingest_pack`, plus the rukie-side HTTP/gzip gates; and `command_digest` covers argv/directory/environment only, so the new `limits` must join the digest input or reconnects can replay one identity under a different bound.
17. **Post-receive diagnostics go nowhere, and a failed callback loses notifications**: `serve_receive_pack_with` has no stderr writer for hook output and its failure path calls `tracing::warn` with no `tracing` subscriber installed anywhere in `ruk/crates` — the warn emits nothing: no per-stream `Stderr` frame, not even a daemon-global log line. And because post-receive failure never rejects the push, a failed `post-receive` callback (rukie down, timeout, crash between land and spool) succeeds the push while CI/webhooks/mirrors vanish. Requires the §5.3 spool-first retry + local spool + atomic outbox + idempotent replay + explicitly lossy tip-comparison reconciler + R-REFJOURNAL chain, plus R-5.5's explicit per-stream diagnostic channel and a real logging/subscriber decision; daemon-log scraping and `op.log` tailing for git pushes are never the recovery path. `proc-receive` is excluded from the spool-and-`ok` half of this chain by construction (§5.5/§9.7: `ng` unless committed).

## 10. Request lifecycle examples

**Fetch (SSH, v2):** `git-upload-pack '/alice/blog.git'` + `SendEnv GIT_PROTOCOL=version=2` → rukie: key→alice, resolve `alice/blog`, read-allowed? → pool connection → `CommandOpen{repository: /data/repos/alice/blog.git, argv: ["upload-pack", path], env: [GIT_PROTOCOL=version=2, RUK_INVOCATION_ID=…], request_id: fetch-<ulid>}` → pump channel↔frames with grant-on-flush → `Exit` → channel exit-status.

**Push (HTTPS):** `POST /bob/db.git/git-receive-pack` (Basic token, body commands+pack) → rukie: token→bob, write-allowed? → pending-push{invocation: push-<ulid>, hook_token + R-QUOTA limits} → `CommandOpen{argv: ["receive-pack", path], request_id: push-<ulid>, limits: {...}}` (hook env carries `RUK_INVOCATION_ID` + `RUK_HOOK_TOKEN` + spool path via R-5.2 plumbing; limits covered by the digest) → stream body in / report out (hook: spool-first, one short callback attempt, exit 0) → `Exit` → commit outbox transaction (or keep spool + lossy tip-comparison/reconciler gap on failure) → drop pending-push and revoke hook token only after the outbox decision.

**Push (`refs/for/main`):** as above through the daemon stream, except the `proc-receive` shim calls change-creation **synchronously** and answers `ok` only on commit, `ng` otherwise — never spool-and-`ok`; `post-receive` observes the hook-supplied review ref on success.

---

### Critical Files for Implementation

- `ruk/crates/ruk/src/daemon/mod.rs` — CommandOpen/Accepted/ExitReport structs, collect-then-run pipeline (`collect_command_stdin`, `absorb_stdin_chunk`, `serve_ready_command`, `command_inner`), dispatch_method coverage, stdin cap and digest input; target of R-1, R-5.2, R-7, R-QUOTA (also covers `daemon/ipc.rs` handshake profile and credit windows)
- `ruk/crates/ruk-git/src/transport/serve.rs` — receive-pack halves, hook-phase callsites (incl. warn-and-continue post-receive semantics with no installed subscriber, pre-`send_report` ordering), per-ref reports; target of R-5.1/R-5.3/R-5.5/R-REFJOURNAL
- `ruk/crates/ruk-git/src/transport/proc_receive.rs` — refs/for diversion protocol (`PROC_RECEIVE_PREFIXES`, `is_proc_receive_ref`, `converse`), `ok`-means-accepted verdict semantics; target of R-5.4 and §5.5
- `ruk/crates/ruk-git/src/transport/hooks.rs` — Hook resolution (`<repo_dir>/hooks/<name>`, missing-is-pass), `ReceiveHooks`, `execute`/`apply_hook_env` env construction, 60s budget; target of R-5.2/R-5.4
- `ruk/crates/ruk-git/src/transport/smart_http.rs` — Endpoint/content-type/auth contracts the rukie server must satisfy (advertisement_type, request_type, result_type, check_status, Git-Protocol v2 probing via `discover_versioned`)