# ruk-serve-design.md

**Artifact status:** Plan mode has no write tool, so this document is returned inline for the orchestrator to save at `ruk-serve-design.md`. The planning-dir notes `ruk-forge-residue.md` and `ruk-domain-model.md` were never written to disk, so I re-verified their claims against source.

**Citation confidence:** Plain `path:line` citations were read or grep-confirmed. `≈` marks lines read from image-rendered grep output or inferred from nearby anchors. `[INFERENCE]` marks unverified claims. Repo root is `/home/alpha/rewrite/git-renewed/ruk`, abbreviated `ruk/`.

---

## 0. Decisions for the orchestrator

| # | Decision | Recommendation | Why |
|---|---|---|---|
| D1 | `CommandOpen` | Keep the name as the **git-service stream envelope** (`service` = `receive-pack` / `upload-pack` / `upload-archive`). Remove the argv interpreter fields and path. | The assignment names `CommandOpen receive-pack`. The argv path is Forgejo-only (`.omp/plans/AGENT_NATIVE_GIT_PLAN.md:48` says "never argv", yet `daemon/mod.rs:1205` runs `crate::cli::run_captured`). Both constraints hold if argv goes and the envelope stays. |
| D2 | Receive-pack idempotency | Durable `ServiceReceipts` fact table replaces the in-memory `OutcomeLedger`. | `OutcomeLedger` is process memory capped at 10,000 entries (`daemon/mod.rs:539-585`). Replay must survive a daemon restart. |
| D3 | Principal model | **v1: one OS-peer principal for rukie.** Per-user and per-token authorization is enforced by rukie. Ruk is not extended to accept host-asserted principals. | Ruk derives the principal only from `SO_PEERCRED` (`ruk-core/src/coord/authentication.rs`, `ruk-core` domain model §3.4). Settled decision "agents use scoped tokens on human accounts" therefore cannot be enforced inside Ruk. **Blocking-level choice; confirm.** |
| D4 | Event source | Emit ref events **inside `Store::publish`** for every `FactTable::Refs` mutation, tagged with an explicit `EventSource`. Do not derive events from the op log. | `receive-pack` ref writes bypass the op log and reflogs entirely (no `oplog`/`reflog`/`OperationId` in `ruk-git/src/transport/serve.rs` or `push.rs`; apply path at `ruk-git/src/transport/session.rs:778-833`). |
| D5 | Hooks | CLI keeps script hooks (oracle compatibility). The daemon uses a typed **`HostPolicy`** callback. `refs/pull/` is dropped; `refs/for/` stays as the review divert. | `refs/for/` is a settled requirement. `refs/pull/` is Forgejo PR-specific. Git-compat CLI must keep script behavior. |
| D6 | Schema | `SCHEMA_MINOR` 1→2 in **one** commit. `SCHEMA_MAJOR` stays 1. Add `MethodKind::Admin`. Add **no** new `ErrorCode`; cursor expiry is a reply status. | `ErrorCode` is a closed set (`ruk-core/src/error.rs:31-92`). Minor precedent is `protocol.rs:65-68`. |
| D7 | MCP | `ruk mcp` (local, one repo) is unchanged. Hosted read-only methods are exposed by rukie's Go op table. No credential-bearing or admin method is exposed in any MCP. | `ruk/src/mcp/tools.rs` `TOOL_TABLE` is hand-written, not derived from `Method::ALL`. |
| D8 | `events.tail` | **Long-poll page** (`after`, `limit`, `wait_ms`), not stream frames. | Simpler to replay and resume. Stream frames would need a new invocation shape. |
| D9 | Forge pagination | New **`KeysetPage<T>`** (stateless `next_after`). Do not reuse `PageCursor`. | `PageCursor` binds a 30 s interactive lease (`protocol.rs` ≈497-503; `hosted.rs:566-640`). Forge reads cannot rely on leases. |
| D10 | IPC kinds | Kind 9 `MergeNotices` is **retired, never reused**. New kinds: 12 `HookCall`, 13 `HookReply`. | Kind 9 has no send site (`daemon/mod.rs:850, 1018, 1754, 1830, 1959` are only rejection arms). Kinds end at 11 (`ipc.rs` FrameKind). |

---

## 1. CLI rename

**Current** (verified):
- `ruk/src/main.rs:30-36` `enum ForgejoCommand { Serve { config: PathBuf } }`
- `main.rs:64-72` `Command::Forgejo { command: ForgejoCommand }` with doc "Serve Forgejo as a hosted multi-repository daemon."
- `main.rs:201-215` match arm → `daemon::DaemonConfig::from_toml` → `daemon::serve(cfg)`

**Target:**
- Delete `ForgejoCommand`. Add `Command::Serve { #[arg(long)] config: PathBuf }`, doc "Serve the host daemon until interrupted." Same arm body, no alias and no `forgejo` shim (clean cutover).
- `README.md:67` row becomes ``| `ruk serve --config <path.toml>` | Run the hosted multi-repository Unix-socket daemon. |``
- `README.md:3` rewrite (drops "targeting Forgejo"). `README.md:5` method count 46 → 58 after this change; verify with `ruk model`.
- Verify `main()` argv routing (git-compat dispatch in `ruk/src/cli`) does not intercept `serve`. [INFERENCE: no git verb named `serve` exists.]

**Tests:** clap parse test that `ruk serve --config x` yields `Command::Serve`, and that `ruk forgejo serve` is rejected.

---

## 2. TOML configuration (`ruk serve --config`)

Loader: `daemon/mod.rs:176-220` (`DaemonConfig::from_toml`). Validator: `mod.rs:223-256`. Defaults constants: `mod.rs` ≈36-56. Unknown keys are not currently rejected; add `#[serde(deny_unknown_fields)]`.

| Key | Type | Default | Status | Notes |
|---|---|---|---|---|
| `socket_path` | path | required | keep | Unix socket. |
| `repo_roots` | [path] | required, non-empty | keep | Canonicalized (`registry.rs:83`). |
| `allowed_uids` | [u32] | `[]` (any local peer) | keep | `ipc.rs:~549` `authenticate_peer`. |
| `admin_uids` | [u32] | `[]` (admin methods disabled) | **new** | Gates `MethodKind::Admin`. |
| `max_connections` | usize | 128 | keep | |
| `blocking_pool_size` | usize | 16 | keep | Shared `BoundedExecutor` (`executor.rs`). |
| `transport_permits` | usize | 8 | **new** | Separate bulkhead for long streams (receive, archive). Needed because `BoundedExecutor::run` holds its permit for the whole closure (`executor.rs` doc). |
| `handshake_timeout_ms` | u64 | 5000 | keep | |
| `frame_timeout_ms` | u64 | 60000 | keep | |
| `stream_window_bytes` | u64 | 256 KiB | keep | `ipc.rs` `INITIAL_STREAM_WINDOW_BYTES`, cap `MAX_STREAM_WINDOW_BYTES` (16 MiB). |
| `retention_interval_ms` | u64 | 60000 | keep | 0 disables sweeps. |
| `hook_timeout_ms` | u64 | 10000 | **new** | `HostPolicy` pre-receive deadline; fail-closed. |
| `hook_max_commands` | usize | 1024 | **new** | Max ref commands per push batch (cap 4096). |
| `default_branch` | RefName | `refs/heads/main` | **new** | Used by `repo.init` / `repo.fork`. |
| `event_retention_seconds` | u64 | 604800 | **new** | Matches the 7-day receipt horizon (`ruk-core` domain model, receipts 168 h). |
| `archive_max_bytes` | u64 | 1 GiB | **new** | `E_LIMIT` above this. |
| `compare_max_commits` | usize | 10000 | **new** | Walk budget for `compare`. |
| `search_max_scan_bytes` | u64 | 64 MiB | **new** | Per-request scan budget for `search`. |

Validation additions in `validate` (`mod.rs:223`): `transport_permits ≥ 1`, `hook_timeout_ms > 0`, `hook_max_commands ∈ [1, 4096]`, `default_branch` parses as `RefName`, `event_retention_seconds > 0`.

---

## 3. Residue removal list (file:line)

Action key: **DEL** delete, **REW** rewrite text or shape, **KEEP** generic and retained, **MOVE** relocate.

### 3.1 CLI and daemon entry

| Location | Content | Action |
|---|---|---|
| `ruk/src/main.rs:30-36` | `ForgejoCommand` | DEL |
| `main.rs:64-72` | `Command::Forgejo` variant | REW → `Serve` |
| `main.rs:201-215` | Forgejo match arm | REW |
| `daemon/mod.rs:1-9` | Module doc "behind `ruk forgejo serve`", "Forgejo instance" | REW |
| `daemon/mod.rs:58` | `DaemonConfig` doc "`ruk forgejo serve` process" | REW |
| `daemon/mod.rs:654-655` | Error text "ruk forgejo serve needs a Unix socket" | REW |
| `daemon/mod.rs:665` | thread name `ruk-forgejo-serve` | REW → `ruk-serve` |

### 3.2 Daemon argv command path (Forgejo-only)

| Location | Content | Action |
|---|---|---|
| `mod.rs` ≈372-380 | `MAX_OUTCOME_RECORDS`, `MAX_COMMAND_STDIN_BYTES`, `OUTCOME_COMPLETED`, `OUTCOME_CANCELLED` | DEL |
| `mod.rs:390-408` | `CommandOpen` fields `session_id` (391-393, "accepted and ignored"), `argv` (≈400), `dir` (≈402), `env` (≈404), `command_digest` (≈406), `stderr_credit` | DEL fields; keep struct under D1 with fields from §5.2 |
| `mod.rs:409-416` | `stdout_credit` doc | KEEP |
| `mod.rs:417-427` | `CommandAccepted` | KEEP |
| `mod.rs:429-476` | `CommandExitReport` (`exit_code` ≈432) | KEEP |
| `mod.rs:478-497` | `ControlRequest` / `ControlReply` (`outcome-lookup`, lines 479 and 491) | DEL |
| `mod.rs:505-509` | `StdinGrant` | KEEP if receive-pack keeps stdin credit; else DEL |
| `mod.rs` ≈520-537 | `OutcomeState`, `OutcomeEntry` | DEL |
| `mod.rs:539-585` | `OutcomeLedger` (`struct` 540, `impl` 548) | DEL (replaced by `ServiceReceipts`, §6) |
| `mod.rs:694-695, 721` | `ConnectionRefs.outcomes` field and init | DEL |
| `mod.rs` ≈874-921 | `answer_control_open` | DEL |
| `mod.rs:924-944` | `write_control_error` | DEL |
| `mod.rs:946-950` | `lock_outcomes` | DEL |
| `mod.rs:1092-1104` (in `handle_open` ≈1089) | `decode_control::<CommandOpen>` argv branch | REW → git-service branch |
| `mod.rs:1131-1167` | `RequestSlot`, `claim_request_slot` | DEL (receipt lookup replaces it) |
| `mod.rs:1169-1203` | `CommandServing`, `ReadyCommand` | DEL |
| `mod.rs:1205-1331` | `command_inner` | REW → `serve_git_service` |
| `mod.rs:1333-1416` | `serve_ready_command` (calls `crate::cli::run_captured`) | DEL |
| `mod.rs:1417-1545` | `teardown_command`, `replay_command`, `refuse_command`, `refuse_unclaimed`, `cancel_command`, `replay_pending` | DEL |
| `mod.rs:1547-1620` | `validate_command_fs` | DEL |
| `mod.rs:1589-1644` | `check_env_path_under_roots`, `normalize_lexical` | DEL |
| `mod.rs:1645-1661` | `route_credit` | KEEP |
| `mod.rs:1662-1771` | `CollectFate`, `collect_command_stdin`, `absorb_stdin_chunk` (≈1773) | DEL (replaced by a bounded pump, §6) |
| `mod.rs:1794-1842` | `ControlFate`, `control_during_command` | REW |
| `mod.rs:1843-1925` | `WriteFate`, `write_cmd_data`, `CreditFate`, `await_outbound_credit` | REW → one generic credit-window writer |
| `mod.rs:2346-2440` | `write_data_chunks`, `send_frame`, `send_exit`, `send_error_exit` | KEEP |

### 3.3 IPC (`daemon/ipc.rs`)

| Location | Content | Action |
|---|---|---|
| `ipc.rs:39-43` | `SUPPORTED_FEATURES = ["credits","merge-notices","outcome-lookup"]`; doc names Forgejo Go client | REW → `["credits","hooks.v1","events.v1","archive.v1"]` |
| `ipc.rs:66` | `MergeNotices = 9` (≈ doc 65) | DEL (D10). Arms ≈88 (`from_u8`), ≈110 (`as_u8`), ≈126 (`is_data`) |
| `ipc.rs:410-412` | `protocol_minor` doc "Forgejo Go backend omits this field" | REW |
| `ipc.rs:415-416` | `profile` doc "such as `forgejo`" | REW → `host` |
| `ipc.rs:549-550` | error "not an authorized Forgejo service identity" | REW |
| `ipc.rs:642-646` | test fixture `profile: "forgejo"` | REW |

Registry (`daemon/registry.rs`) and executor (`daemon/executor.rs`) contain **no** Forgejo residue (grep-verified). They gain lifecycle code (§4).

### 3.4 Daemon tests (`daemon/tests.rs`)

| Location | Content | Action |
|---|---|---|
| `tests.rs:3-6` | module doc naming Forgejo backend field names | REW |
| `tests.rs` ≈115-133 | `command_open_payload` (`"session_id":"forgejo-svc"` at 123; `argv` 126) | REW |
| `tests.rs` ≈186-215 | `open_command` helper; `outcome-lookup` query (212) | DEL/REW |
| `tests.rs:321-322` | handshake `profile:"forgejo"`, features with `merge-notices` and `outcome-lookup` | REW |
| `tests.rs:376-377` | handshake fixture `profile:"forgejo"` | REW |
| `tests.rs:409-410` | "argv init creates the store under the daemon root" | REW → `repo.init` test |

Keep helpers `seed_commit`, `temp_root`, `frame_deadline`, `jstr`.

### 3.5 Ruk-git transport and core

| Location | Content | Action |
|---|---|---|
| `ruk-git/src/transport/hooks.rs:1-32` | module doc; Forgejo dispatcher paragraph at 22-32 | REW. **Keep** script machinery (CLI oracle). |
| `transport/hooks.rs:58-63` | `HOOK_TIMEOUT` doc ≈ | KEEP |
| `transport/proc_receive.rs:1-43` | AGit doc naming Forgejo modules and `refs/pull` | REW |
| `transport/proc_receive.rs:59` | `PROC_RECEIVE_PREFIXES = ["refs/for","refs/pull"]` | REW → `["refs/for"]`, configurable |
| `transport/proc_receive.rs:103-115` | `AlternateRef` doc `refs/pull/1/head` | REW → `refs/changes/` |
| `transport/serve.rs:152-155` | `MAX_PUSH_OPTIONS` doc "Forgejo uses at most four AGit keys" | REW |
| `transport/serve.rs:604-607` | push-options doc "Forgejo advertises…" | REW |
| `transport/serve.rs:723-727` | update-hook doc "Forgejo's dispatcher" | REW |
| `transport/net.rs:264-268` | `SSH_EXIT_GRACE` doc "Forgejo's `forgejo hook post-receive`" | REW |
| `transport/dumb.rs:1-6` | doc "HTTP layer lives outside this crate (Forgejo/daemon)" | REW. Callers unverified. Settled decision is smart HTTP + SSH only, so check whether `dumb.rs` has live callers. |
| `transport/mirror.rs:1-6` | doc "for Forgejo pull/push mirrors" | REW. **Keep** `prune_for_spec` (`:71`) and `push_mirror_updates` (`:107`); CLI callers at `cli/verbs_transport.rs:818` and `:1631`. |
| `ruk-core/src/error.rs:3-7` | doc "The Forgejo contract's extra outcomes" | REW. Codes stay. |
| `ruk-core/src/store/mod.rs:287-291` | doc "the Forgejo patch provisions the parent" | REW |
| `ruk-core/src/store/durability.rs:35-39` | doc "Forgejo's `MkdirAll`" | REW |
| `ruk-core/src/merge/composed/mod.rs:4-7` | doc "the Forgejo merge result" and "hosted IPC `MergeNotices` event" | REW. The merge-notice record stays the single definition; its reuse becomes native JSON `merge_notices` plus CLI stderr (D10). |
| `ruk-core/src/coord/*` | no Forgejo residue (grep-verified) | KEEP |

### 3.6 Root and docs

| Location | Content | Action |
|---|---|---|
| `README.md:3, 5, 67, 91` | Forgejo targeting; count 46; `ruk forgejo serve`; "Forgejo multi-repository daemon" | REW |
| `.gitignore:6` | `forgejo/` | Remove only after the owner deletes the ambient `forgejo/` directory. Not Ruk code residue. |
| `forgejo-patch-inventory` (repo root, 142.8 KB, present) | Forgejo patch surface | DEL in the cutover commit. Tracked status unverified. |
| `.omp/plans/AGENT_NATIVE_GIT_PLAN.md:7, 25, 48, 64-65, 95, 108` | `:48` claims "Forgejo uses typed operations, never argv"; `:65` "standalone/Forgejo serve entry points"; `:95` IPC MergeNotices; `:108` `make test-sqlite` | REW to match the design |
| `docs/coordinator.md`, `CHANGELOG.md` | not grep-checked | Grep, then REW; add a changelog entry |

Ambient untracked trees `forgejo/` and `rukie/` sit under the Ruk root. They are not product residue and must not be staged.

---

## 4. Protocol changes (shared)

### 4.1 Method table

- Append rows at the tail of `method_table!` (`ruk-core/src/protocol.rs:3285-3333`; last row `git.commit`). Order must be append-only (`protocol.rs` doc at 3123-3127; postcard discriminants).
- Add each new `Method` to the **`required` array** in `method_table_is_consistent_and_covers_the_contract` (`protocol.rs:5417-5484`, array 5431-5479). Also update the count assertion at `protocol.rs:5483`. Forgetting either fails the test.
- Add `MethodKind::Admin` (`protocol.rs:1131`). Add `Admin` arms to every `macro_rules!` that matches on kind: `request_id_of!`, `digest_args!`, `outcome_of!`, `outcome_of_mut!` (≈3090-3123). The compiler forces this.
- Doc at `protocol.rs:65-68` changes to "Minor 2 appends …" and `SCHEMA_MINOR` becomes 2 (`protocol.rs:68`).

New methods, in this append order:
`repo.stat`, `tree`, `refs.list`, `compare`, `search`, `archive`, `events.tail`, `repo.init`, `repo.delete`, `repo.rename`, `repo.fork`.

`push` already exists (`protocol.rs:1947-1992`). Its refusal stays in `coord/prepare.rs:283-288`, and the hosted path routes it to a transport job (§5.10).

### 4.2 New shared types

```jsonc
// CommitRev: externally tagged, snake_case.
{"ref": "refs/heads/main"}      // full ref name, resolved to a direct commit
{"commit": "<hex oid>"}         // must be a stored, non-conflicted Git commit

// KeysetPage<T>: stateless; cursor = last returned key, hex-encoded bytes.
{"items": [T...], "next_after": "<hex>" | null}

// RefTarget (refdata.rs:193-197, rename_all=lowercase)
{"direct": "<hex oid>"} | {"symbolic": "refs/..."}

// ByteString: UTF-8 string, else {"hex":"..."} (main.rs ByteString contract)
```

`KeysetPage` is new. Existing `Page<T>` and `PageCursor` carry lease bindings (D9).

### 4.3 IPC frames

- Kind 9: retired, never reused (D10).
- Kind 12 `HookCall` (daemon → client, JSON control, stream-scoped).
- Kind 13 `HookReply` (client → daemon, JSON control, stream-scoped).
- Both are control frames, so they fall under `MAX_IPC_CONTROL_BYTES` (1 MiB). `FrameKind::is_data` returns false for both.
- Add arms to `from_u8`, `as_u8`, `is_data`, and the stream-zero rejection arms (`mod.rs:850, 1018, 1754, 1830, 1959` become `HookCall`/`HookReply` rejection, since they must never arrive on stream 0).

### 4.4 InvocationOpen and InvocationAccepted

`InvocationOpen` (`mod.rs:305-321`) gains, with `#[serde(default)]`:
- `transport_auth: Option<TransportAuth>` (§5.10). Custom `Debug` that redacts secrets. Never serialized into args, digests, receipts, or logs.

`InvocationAccepted` (`mod.rs:324-332`) gains:
- `result: Option<serde_json::Value>` (or typed `InvocationResult`) used only by `archive` (§5.6).

### 4.5 Handshake

- `profile`: `"host"` (any non-empty string negotiates; `ipc::negotiate` checks only for empty).
- `features`: `["credits", "hooks.v1", "events.v1", "archive.v1"]`. Clients that send `merge-notices` or `outcome-lookup` get them dropped by negotiation.

### 4.6 Per-stream concurrency (required)

Today `handle_open` awaits `open_inner` → `dispatch_method` inline (`mod.rs:1089`, `2002-2063`). A long read or archive blocks every other stream on that connection. The current code documents this as "the documented v1 limitation" (`mod.rs` doc before `command_inner`, ≈1200).

Target: one tokio task per stream. A single connection writer task owns the socket write half. Streams send frames through a bounded channel, which gives backpressure. This is required before archive and receive-pack can ship.

---

## 5. Typed methods

Notation: **Kind** is the `MethodKind`. **Existing** lists what already runs. **Write** lists what must be written. **Schema** is the bump. **MCP** is the exposure decision. Admin methods are dispatched before any coordinator call and require the peer uid to be in `admin_uids`. They take no session and no receipt.

### 5.1 `repo.init` — Admin (new)

- **Args:** `{"root": "<configured root path>", "path": "<relative path>", "object_format": "sha1"|"sha256" (default "sha1"), "default_branch": "refs/heads/..." (optional)}`
- **Reply:** `{"handle": "<opaque>", "path": "<abs>", "store_id": "<uuid>", "object_format": "sha1"|"sha256", "default_branch": "refs/heads/main"}`
- **Semantics:** `root` must equal a configured root. `path` is normalized and must stay beneath it. Existing paths of any kind are refused (`E_INVALID`, "repository exists"), per the store contract at `store/mod.rs:287-291`. Parents are created with the durable path (`durability.rs`). Default `object_format` is `sha1` (`ids.rs:33`, `HashKind::DEFAULT = Sha1`).
- **Existing:** `Store::create(root, kind, budget)` at `store/mod.rs:308`. Coordinator open at `hosted.rs:169` via `registry.rs:236-239`.
- **Write:** `Registry::create_repo` in `registry.rs` (creates the store and inserts the handle). `default_branch` persisted in a `RepoMeta` fact row (new `FactTable` variant, see §5.2a). Dispatch arm in `daemon/mod.rs:dispatch_method` (`2109`).
- **Files:** `daemon/registry.rs`, `daemon/mod.rs`, `ruk-core/src/protocol.rs` (method + args + reply + `Request`/`Reply` variants), `ruk-core/src/store/types.rs:239-267` (`RepoMeta` variant appended), `ruk-core/src/store/mod.rs` (fact write).
- **Schema:** minor (method) + minor (`MethodKind::Admin`).
- **MCP:** no. Admin.
- **Tests:** `repo_init_creates_store_under_configured_root`, `repo_init_refuses_existing_path`, `repo_init_refuses_path_outside_roots` (traversal `../`), `repo_init_sha256`, `repo_init_non_admin_peer_refused`.

### 5.2 `repo.delete` — Admin (new)

- **Args:** `{"repo": "<handle or abs path>", "confirm_path": "<abs path>"}`
- **Reply:** `{"deleted_path": "<abs path>"}`
- **Semantics:** `confirm_path` must equal the canonical path (accident guard). Refuses if invocations are in flight (`E_CAPACITY`, retryable). Shuts down the coordinator (`HostedCoordinator::shutdown`, `hosted.rs:322-325`), awaits the lane runner, closes the store (`Store::close`, `store/mod.rs:640`), then renames the directory to a tombstone and removes it. Removal must be crash-safe: a leftover tombstone is cleaned at next start.
- **Existing:** `shutdown`, `close`, `evict_idle` (`registry.rs:365`), `shutdown_all` (`registry.rs:458`).
- **Write:** `Registry::remove(handle)`: in-flight check, shutdown, await runner, tombstone rename, delete. Tombstone sweep at `serve` startup.
- **Files:** `daemon/registry.rs`, `daemon/mod.rs`, `protocol.rs`.
- **Schema:** minor.
- **MCP:** no.
- **Tests:** `repo_delete_requires_matching_confirm_path`, `repo_delete_refuses_inflight`, `repo_delete_then_open_is_not_found`, `repo_delete_recovers_tombstone_after_crash` (simulated tombstone at startup).


[383 more lines in resource. Use :604 to continue]

### 5.3 `repo.rename` — Admin (new)

- **Args:** `{"repo": "<handle or abs path>", "root": "<configured root>", "new_path": "<relative>"}`
- **Reply:** `{"handle": "<new opaque handle>", "path": "<new abs>", "previous_path": "<old abs>"}`
- **Semantics:** same root only (`E_FORBIDDEN` otherwise). Old handle becomes invalid; rukie reopens by path. `store_id` is unchanged. Coordinator is shut down before the move and reopened after.
- **Existing:** `std::fs::rename` is not used yet. `Registry::resolve` (`registry.rs:147`), `handle_for_path`.
- **Write:** `Registry::rename_repo` with fsync of old and new parents (durability helpers).
- **Files:** `daemon/registry.rs`, `daemon/mod.rs`, `protocol.rs`.
- **Schema:** minor.
- **MCP:** no.
- **Tests:** `repo_rename_keeps_store_id_and_refs`, `repo_rename_refuses_cross_root`, `repo_rename_old_handle_is_unknown`.

### 5.4 `repo.fork` — Admin (new)

- **Args:** `{"source": "<handle or abs path>", "root": "<configured root>", "path": "<relative>", "refs": "heads_and_tags"|"all" (default "heads_and_tags")}`
- **Reply:** `{"handle": "<opaque>", "path": "<abs>", "store_id": "<new uuid>", "object_format": "...", "refs_copied": <u64>, "source_view": {"store": "...", "view": <op id>}}`
- **Semantics:** new `StoreId`. Objects and refs copied. Default branch inherited.
- **Existing:** `local_fetch(dest, src, remote, &LocalFetchRequest, tracking, ref_mutations, limits, interrupt)` at `ruk-git/src/transport/session.rs:252-265`. It stages the closure, verifies, and publishes with promisor and observation facts plus caller ref mutations. `ref_mutations` must be lane-prepared via `refs::prepare_checked_transaction` (`ruk-core/src/refs.rs`).
- **Write:** orchestration in `daemon/registry.rs` (create via `Store::create`, then `local_fetch`). The `tracking` rows point at the source repo's refs, which is a semantic wart: fork observation rows should be omitted. [INFERENCE: `LocalFetchRequest` can take an empty observation set; confirm.]
- **Files:** `daemon/registry.rs`, `daemon/mod.rs`, `protocol.rs`, `ruk-git/src/transport/session.rs` (accept empty tracking).
- **Schema:** minor.
- **MCP:** no.
- **Tests:** `repo_fork_copies_all_heads_and_tags`, `repo_fork_gets_new_store_id`, `repo_fork_source_unchanged`, `repo_fork_default_branch_inherited`.

### 5.5 `repo.stat` — Read

- **Kind:** Read (session-bound, so rukie uses its service session).
- **Args:** `{}` (repo from the open frame).
- **Reply:**
```jsonc
{
  "store_id": "<uuid>",
  "object_format": "sha1"|"sha256",
  "view": {"store": "<uuid>", "view": <op id>},
  "default_branch": "refs/heads/main",
  "quarantined": false,
  "quarantine_reason": null | "<text>",
  "events": {"oldest": <u64>, "head": <u64>},
  "oldest_op": <op id> | null
}
```
- **Semantics:** no whole-ref counts (plan forbids whole-ref scans). Counts are omitted.
- **Existing:** `Store::store_id` (`store/mod.rs:526`), `hash_kind` (`:519`), `quarantine` and `quarantine_reason` (`:532`, `:538`), `current_view` (`coord/hosted.rs:1371`, private).
- **Write:** `HostedCoordinator::view()` accessor over `current_view`. Read arm in `HostedCoordinator::read` (`hosted.rs:566-640`). `default_branch` from `RepoMeta` (§5.1). Events head and oldest from §6.
- **Files:** `ruk-core/src/coord/hosted.rs` (read arm, accessor, `read_gap` at `:1349` cleared for this method), `protocol.rs`.
- **Schema:** minor.
- **MCP:** yes (read-only, hosted rukie table).
- **Tests:** `stat_reports_view_format_and_default_branch`, `stat_reports_quarantine_latch`, `stat_requires_session`.

### 5.6 `tree` — Read

- **Args:**
```jsonc
{"rev": <CommitRev>, "path": <ByteString> | null, "after": "<hex name>" | null, "limit": 100}
```
  `limit` default 100, max 1000 (over max is rejected, not clamped, per `PageLimit`).
- **Reply:**
```jsonc
{
  "commit": "<hex>",
  "tree": "<hex>",
  "path": <ByteString>,
  "entries": {
    "items": [{"name": <ByteString>, "kind": "tree"|"regular"|"executable"|"symlink"|"gitlink",
               "mode": "040000"|"100644"|"100755"|"120000"|"160000",
               "oid": "<hex>", "size": <u64> | null}],
    "next_after": "<hex name>" | null
  }
}
```
- **Semantics:** entries in Git tree order (`git_order`). `size` is `object_len` for regular, executable, and symlink entries. `null` for tree and gitlink. Path naming a blob returns `E_INVALID` ("not a tree"; use `file`). Conflicted heads return `E_CONFLICTED`.
- **Existing:** `parse_tree(kind, bytes)` (`ruk-core/src/workcopy/tree.rs:99`), `TreeItem` (`:66`), `git_order` (`:74`), `encode_tree` (`:83`), `TreeMode::spelling` (used by `encode_tree`), `Store::read` (`store/mod.rs:735`), `Store::object_len` (`:755`), `CommitRecord::tree()` (`ruk-core/src/commit.rs:647`, returns `&Merge<Oid>`; the resolved Git tree is a single term [INFERENCE: use the existing resolved accessor on `Merge`]).
- **Write:** new public module `ruk-core/src/tree.rs` with `list_directory(store, rev, path, after, limit)`. It uses `pub(crate)` items from `workcopy/tree.rs`, which is possible because the new module lives in the same crate. Wire into `ruk-core/src/lib.rs` module list (`:20-44`). `file` (`protocol.rs:2404`) remains refused (change and workspace model).
- **Files:** `ruk-core/src/tree.rs` (new), `ruk-core/src/lib.rs`, `ruk-core/src/coord/hosted.rs` (read arm), `ruk-core/src/protocol.rs`.
- **Schema:** minor.
- **MCP:** yes.
- **Tests (ruk-core unit):** `tree_lists_git_order_including_tree_suffix_rule`, `tree_pages_with_keyset_no_gaps_or_duplicates`, `tree_size_matches_object_len`, `tree_blob_path_is_invalid`, `tree_conflicted_head_refused`. Daemon: `tree_over_socket_round_trip`.

### 5.7 `refs.list` — Read

- **Args:** `{"prefix": "refs/heads/" | "refs/tags/" | "refs/" (default "refs/"), "after": "<name>" | null, "limit": 100}`
- **Reply:**
```jsonc
{
  "as_of": <u64 events head>,
  "refs": {"items": [{"name": "refs/heads/main", "target": {"direct": "<hex>"}}],
           "next_after": "<name>" | null}
}
```
- **Semantics:** read `as_of` first, then the page. Events after `as_of` are replayed idempotently by `events.tail`. Keys are exact bytes (`RefName` grammar). Bookmarks are `refs/heads/*` and tags are `refs/tags/*` (`prepare.rs:587-589`).
- **Existing:** `page_refs(store, after, limit)` at `ruk-core/src/refs.rs:97`; returns `RefPage{entries, more}` (`:92`). It has **no prefix**. Iteration uses `Store::fact_page(FactTable::Refs, after, budget)` (`store/mod.rs:847`), which is exclusive-after.
- **Write:** prefix seek. Add an inclusive start mode to `fact_page` (or a `prefix` argument) so a prefix page does not scan the whole namespace. `page_refs` gains a `prefix` argument. Coordinator read arm.
- **Files:** `ruk-core/src/store/mod.rs` (fact_page start mode), `ruk-core/src/refs.rs`, `ruk-core/src/coord/hosted.rs`, `protocol.rs`.
- **Schema:** minor.
- **MCP:** yes.
- **Tests:** `page_refs_prefix_seek_does_not_scan_other_namespaces`, `refs_list_pages_stable_across_writes`, `refs_list_as_of_not_ahead_of_events`, `refs_list_limit_over_max_rejected`.

### 5.8 `compare` — Read

- **Args:** `{"base": <CommitRev>, "head": <CommitRev>, "limit": 100, "after": "<time>:<hex oid>" | null}`
- **Reply:**
```jsonc
{
  "base": "<hex>", "head": "<hex>",
  "merge_bases": ["<hex>"],
  "ahead": <u64>, "behind": <u64>,
  "exact": true,
  "changes": {
    "items": [{"oid": "<hex>", "parents": ["<hex>"], "subject": <ByteString>,
               "committer_time": <i64 seconds>}],
    "next_after": "<time>:<hex>" | null
  }
}
```
- **Semantics:** `ahead` is commits reachable from head but not base; `behind` is the reverse. `changes` is the ahead set in order `(committer_time desc, oid asc)`. Walk budget `compare_max_commits`. On budget exhaustion, `exact: false` and counts are lower bounds. `merge_bases` returns all bases, so criss-cross histories are correct.
- **Existing:** `merge_base` at `ruk/src/cli/state.rs:1491`; `merge_bases_all` at `ruk/src/cli/verbs_history.rs:1698`; `commit_parents` at `cli/state.rs` ≈1639; `commit_record` at `cli/state.rs:1288`; `is_ancestor` at `ruk-core/src/graph/traversal.rs:175` (uses a `GraphAccess` trait, which has no production store impl [INFERENCE from `read_gap` text at `hosted.rs:1349-1366`: "revset evaluation builds its graph only in tests"]).
- **Write:** move paint-down merge-base (`cli/state.rs:1491`, `verbs_history.rs:1698`) into ruk-core, generic over a parents closure, with a budget. Implement the bounded ahead and behind walk. Keyset cursor on `(time, oid)`. `subject` from `CommitRecord` payload.
- **Files:** `ruk-core/src/ops/compare.rs` (new) or `ruk-core/src/tree.rs`-style module, `ruk-core/src/coord/hosted.rs`, `ruk/src/cli/state.rs` and `verbs_history.rs` (switch callers to the core function; keep CLI output identical), `protocol.rs`.
- **Schema:** minor.
- **MCP:** yes.
- **Tests:** `compare_ahead_behind_matches_git_rev_list_counts` (oracle: external `git rev-list --left-right --count`, test-only), `compare_criss_cross_returns_all_merge_bases`, `compare_budget_exhaustion_sets_exact_false`, `compare_pagination_keyset_stable`, `compare_identical_refs_zero_zero`.

### 5.9 `search` — Read (Bulk admission)

- **Args:**
```jsonc
{"rev": <CommitRev>, "path": <ByteString> | null,
 "pattern": "<text, <=1024 bytes>", "mode": "literal"|"regex",
 "case_sensitive": true, "whole_word": false,
 "max_matches": 100,                       // max 1000
 "after": {"path": <ByteString>, "line": <u32>} | null}
```
- **Reply:**
```jsonc
{"commit": "<hex>",
 "matches": [{"path": <ByteString>, "line": <u32>, "column": <u32>,
              "preview": <ByteString>, "preview_truncated": false}],
 "truncated": false,
 "next_after": {"path": <ByteString>, "line": <u32>} | null,
 "scanned_bytes": <u64>,
 "skipped": {"binary": <u64>, "large": <u64>}}
```
- **Semantics:** file search over a tree at a commit. Binary means a NUL in the first 8000 bytes (matches Git's heuristic). Files over 1 MiB are skipped and counted. Preview capped at 512 bytes. Stops at `max_matches` or `search_max_scan_bytes`, setting `truncated` and `next_after`. Work runs on the Rayon CPU pool, not the async runtime.
- **Existing:** CLI oracle `grep` at `ruk/src/cli/verbs_grep.rs:41` (flags `-F`/`-E`, `-i`, `-w`; last engine flag wins). Tree-scope test `grep_tree_scope_searches_commit`. Tree walk from §5.6. Blob read `Store::read`.
- **Write:** core search module. Regex: ruk-core has no `regex` dependency. Add the workspace `regex` dependency to `ruk-core/Cargo.toml` (the workspace already has `regex`; `ruk/Cargo.toml:21` uses it). Use `regex::bytes::RegexBuilder` with `size_limit` and `dfa_size_limit` set. Literal mode escapes the pattern.
- **Files:** `ruk-core/src/search.rs` (new), `ruk-core/Cargo.toml`, `ruk-core/src/coord/hosted.rs` (bulk read arm), `protocol.rs`.
- **Schema:** minor.
- **MCP:** yes, with a note that it is bounded.
- **Tests:** `search_literal_and_regex_match_git_grep_lines` (oracle), `search_skips_binary_and_large_and_counts_them`, `search_budget_sets_truncated_and_resumable_cursor`, `search_pattern_over_1024_bytes_refused`, `search_whole_word_matches_oracle`.

### 5.10 `archive` — Read, streamed

- **Args:** `{"rev": <CommitRev>, "path": <ByteString> | null, "format": "tar"|"zip", "prefix": "<dir>/" | null}`
- **Accepted:** `InvocationAccepted` with `result: {"commit": "<hex>", "format": "tar", "content_type": "application/x-tar"}` (or `application/zip`).
- **Body:** archive bytes as `Stdout` data frames (credit-controlled, 64 KiB chunks). Terminal `Exit{status:0, code:"OK", outcome:"Completed"}`. A truncated stream without `Exit` is a client error.
- **Dispatch:** `archive` cannot return `Vec<u8>` from `dispatch_method`, so `open_inner` special-cases the method, the same way it special-cases the git-service envelope.
- **Semantics:** `archive_max_bytes` cap (`E_LIMIT`). Bulk admission. Read-only, so no receipt.
- **Existing:** tar member walk `walk_tree` at `ruk-git/src/transport/archive.rs:473`. Sideband-only tar writer `BandWriter::write_tar` at `archive.rs:841-842`; a plain `Write` tar writer must be factored out of it. Zip transcoder `tar_to_zip` at `ruk/src/cli/verbs_transport.rs:2710`, with `zip_mtime` (`:2678`), `zip_failed` (`:2664`), and the CLI `archive` verb (`:1957`).
- **Write:** factor a `TarWriter<W: Write>` out of `archive.rs`. Move the zip transcoder to `ruk/src/archive_zip.rs` so both the CLI verb and the daemon share it (both live in the `ruk` crate, which already depends on `zip`, `ruk/Cargo.toml:25`). Backpressure: bounded channel from the blocking worker to the connection writer, sized to the credit window.
- **Files:** `ruk-git/src/transport/archive.rs`, `ruk/src/archive_zip.rs` (new), `ruk/src/cli/verbs_transport.rs` (switch to shared zip), `ruk/src/daemon/mod.rs` (stream arm), `ruk/src/daemon/ipc.rs` (`archive.v1` feature), `protocol.rs`.
- **Schema:** minor.
- **MCP:** no. Binary and large. Exposed only over the HTTP API by rukie.
- **Tests:** `archive_tar_file_set_matches_git_archive` (oracle), `archive_zip_round_trip_keeps_modes_and_mtimes`, `archive_streams_under_credit_window` (client grants per chunk; never exceeds window), `archive_cancel_midstream_closes_without_exit`, `archive_over_limit_refused_before_first_byte`.

### 5.11 `events.tail` — Read (long-poll)

- **Args:** `{"after": <u64> | null, "limit": 100, "wait_ms": 0, "prefix": "refs/..." | null}`. `limit` max 1000. `wait_ms` max 30000. `after` absent means "from head".
- **Reply:**
```jsonc
{
  "status": "ok" | "expired",
  "head": <u64>, "oldest": <u64>,
  "events": [{"seq": <u64>, "name": "refs/heads/main",
              "old": <RefTarget> | null, "new": <RefTarget> | null,
              "at": "<RFC3339>",
              "source": "receive_pack"|"lane"|"local_push"|"fork"|"import"|"land",
              "operation": <op id> | null,
              "request_id": "<id>" | null, "session": "<id>" | null}],
  "next_after": <u64>
}
```
- **Semantics:** events have `seq > after`. `expired` iff `after + 1 < oldest`, with empty `events`. The client then takes a `refs.list` snapshot (with `as_of`) and tails from `as_of`. Long-poll: wait up to `wait_ms` for `head > after`, polling the events head fact every 100 ms. Events are idempotent by `(name, new)`. Webhooks (Standard Webhooks) are driven from this stream, not from hook frames.
- **Existing:** none. Op log is insufficient: it has no per-ref values, and receive-pack bypasses it (D4).
- **Write:**
  1. `FactTable::Events` (append to `store/types.rs:239-267`). Rows keyed by big-endian `seq`. `FactTable::EventHead` row holds the last seq.
  2. Emission in `Store::publish` (`store/mod.rs:911`). Under the writer lock (`lock_writer`, `store/mod.rs:640-641`), every `Refs` mutation in the batch appends one event row and bumps the head. `publish` gains an `EventSource` argument; call sites are updated in the same commit. [INFERENCE: call-site count is unmeasured; grep before implementing.]
  3. `oplog::tracked` (`ruk-core/src/coord/oplog.rs:48`) returns false for `Events`, so events never enter the op log.
  4. Retention sweep in `coord/retention.rs` (48.7 KB): prunes events older than `event_retention_seconds`, keeping `oldest` current.
  5. Read arm in `coord/hosted.rs` using `Store::fact_page(Events, after)` (`store/mod.rs:847`).
- **Files:** `ruk-core/src/store/types.rs`, `ruk-core/src/store/mod.rs`, `ruk-core/src/coord/oplog.rs`, `ruk-core/src/coord/retention.rs`, `ruk-core/src/coord/hosted.rs`, `ruk-git/src/transport/session.rs` (`apply_refs` passes `EventSource::ReceivePack`), `ruk-core/src/protocol.rs`.
- **Schema:** minor. The new fact tables are additive to store format. [INFERENCE: confirm an existing store opens with an unknown-to-it but known-to-code table; check whether `FactTable` names are persisted (`oplog.rs:22-24` treats unknown table names as corruption).]
- **MCP:** yes, one bounded page (`wait_ms` capped to 0 in MCP).
- **Tests:** `events_one_per_ref_mutation`, `events_strictly_increasing_across_lane_and_receive`, `events_resume_no_gap_no_duplicate`, `events_expired_reports_status_not_error`, `events_long_poll_returns_on_publish`, `events_not_emitted_for_non_ref_tables`, `events_survive_restart`, `events_retention_prunes_and_moves_oldest`.

### 5.12 `push` — Mutation, transport job

- **Args:** existing `PushArgs` (`protocol.rs:1947-1952`): `{"remote": {"url": {"url": "https://..."}}, "updates": [{"remote_ref": "refs/...", "source": {"commit": {"oid": "<hex>"}} | {"change": ...} | "delete", "expected_old": null | "absent" | {"present": "<hex>"}, "force": false}], "atomicity": "atomic"|"non_atomic"}`. Credentials are **not** in args (see below).
- **Reply:** existing `PushReply` (`protocol.rs:1986-1992`): `{"outcome": ..., "plan": <PlanId>, "state": "prepared"|"sending"|"confirmed"|"rejected"|"uncertain", "refs": {"items": [{"remote_ref": "...", "status": {"confirmed": {"new": "<hex>" | null}} | {"rejected": {"reason": "..."}} | "uncertain"}], "truncated": false, "total": N}}`.
- **Semantics:** hosted push requires an explicit URL (`RemoteSpec::Url`). Named remotes are refused in hosted mode so credentials are never stored in repo config. Per-ref status is already typed.
- **Injected credentials:** `InvocationOpen.transport_auth`:
```jsonc
{"kind": "basic", "username": "...", "password": "..."}  // or {"kind":"bearer","token":"..."}
// {"kind":"none"} for anonymous
```
  Priority: injected, then the existing helper chain (`credentials.rs` `auth_header_for_url_with`, `:753`), then environment, then anonymous. Injected credentials apply only to the exact URL origin given. They are never sent after redirects to another origin. SSH key injection is deferred [INFERENCE: v2, via a 0600 tmpfs file unlinked after spawn].
- **Digest:** credentials live outside `PushArgs`, so they never enter `digest_input_v1` (`protocol.rs` ≈3200). Add a golden test that the digest is identical with and without credentials.
- **Existing:** `local_push` (`session.rs:621`), `push_over` (`ruk-git/src/transport/push.rs:466`), `PushPlanRecord` persist (`session.rs` local_push body), `net.rs` transport, `credential_fill_with` (`credentials.rs:502`).
- **Write:** `ruk/src/daemon/mod.rs` `dispatch_method` routes `Push` to a transport-job function before the coordinator. The job persists the plan, runs the network push outside the lane (holding no store writer lock during I/O), then runs a lane batch to publish the terminal plan state and `ServiceReceipts`-style receipt. `credentials.rs` gets an `Injected` source variant. `coord/prepare.rs:283-288` refusal text is updated to point at the transport job.
- **Files:** `ruk/src/daemon/mod.rs`, `ruk/src/daemon/ipc.rs` (`TransportAuth`, redacting `Debug`), `ruk-git/src/transport/credentials.rs`, `ruk-git/src/transport/net.rs`, `ruk-git/src/transport/push.rs`, `ruk-core/src/coord/prepare.rs`.
- **Schema:** no change to `PushArgs` (so no digest change). Adding the method-level route is not a schema change. Receipt semantics unchanged. Bump minor only if `PushRefStatus` or `PushState` change.
- **MCP:** no (credentials must not flow through MCP args).
- **Tests:** `push_injected_basic_auth_used_over_config_helpers`, `push_credentials_absent_from_digest_and_receipt` (golden), `push_redacts_secrets_in_debug_and_error_text`, `push_named_remote_refused_in_hosted_mode`, `push_per_ref_confirmed_rejected_uncertain`, `push_redirect_to_other_origin_drops_credentials`.

### 5.13 Receive-pack (git-service stream, not a Method)

Covered in §6.

---

## 6. Receive path: `CommandOpen` git service and `HostPolicy`

### 6.1 Current behavior (verified)

- `serve_receive_pack(reader, writer, store, kind, head, interrupt, lfs)` at `ruk-git/src/transport/serve.rs:514` wraps `serve_receive_pack_with(..., &ReceiveHooks::disabled(), lfs)` (`:554`).
- `serve_command` (`serve.rs:2097-2116`): receive-pack arm passes `LfsMode::Deny` and therefore disabled hooks. Comment: "no repository directory travels with a bare command dispatch".
- Phases in `serve_receive_pack_with`: quarantine staging (`hooks.rs:197` `stage_quarantine`), `pre-receive` all-or-nothing script (`hooks.rs:285` `run_hook`), per-ref `update` script (`serve.rs` ≈723-754), objects and commit facts published (`serve.rs:755`), AGit proc-receive scripts (`proc_receive.rs:152` `run_proc_receive`, invoked around `serve.rs` ≈785), ref CAS via `apply_refs` (`session.rs:778`), `post-receive` script.
- Two publications today: objects and commit facts (`serve.rs:755`), then refs per ref (`session.rs` apply_refs). Neither writes a reflog, an op, or an event.

### 6.2 Target: the envelope (D1)

```jsonc
// CommandOpen (open frame payload, stream-scoped)
{
  "service": "receive-pack" | "upload-pack" | "upload-archive",
  "repository": "<handle or abs path>",
  "session_id": "<SessionId>" | null,   // REQUIRED for receive-pack
  "request_id": "<RequestId>" | null,   // REQUIRED for receive-pack
  "git_protocol": "version=2" | null,   // replaces GIT_PROTOCOL env
  "stdout_credit": 262144
}
// CommandAccepted: {"stream_id", "stdin_credit", "invocation"}
// CommandExitReport: {"exit_code", "signal": ""}
```
Removed fields: `argv`, `dir`, `env`, `command_digest` (now computed daemon-side from parsed commands), `stderr_credit` (sideband carries messages), and `session_id` as ignored Forgejo text.

### 6.3 Policy trait (ruk-git)

```rust
// ruk-git/src/transport/hooks.rs (new trait; names illustrative)
pub trait ReceivePolicy {
    fn needs_quarantine(&self) -> bool;
    fn pre_receive(&self, batch: &[RefCommand]) -> Vec<RefVerdict>; // one verdict per command
    fn post_receive(&self, landed: &[LandedRef]);                   // best-effort, never fails push
}
```
- `ScriptPolicy(ReceiveHooks)`: existing behavior for the CLI. Its three script phases are preserved.
- `HostPolicy` (ruk daemon): see §6.4.
- `NoPolicy`: accept all. Used only where `ReceiveHooks::disabled()` is used today.
- `serve_receive_pack_with` (`serve.rs:554`) takes `&dyn ReceivePolicy` in place of `&ReceiveHooks`. `serve_command` (`:2097`) gains a policy argument. The CLI passes `ScriptPolicy`.

### 6.4 HostPolicy frames

`HookCall` (kind 12, daemon → client, stream-scoped):
```jsonc
{"id": 1, "phase": "pre-receive",
 "commands": [{"ref": "refs/heads/main", "old": "<hex|zero>", "new": "<hex|zero>",
               "action": "create"|"update"|"delete", "authorized": true}],
 "push_options": ["..."], "new_commits": 3, "pack_bytes": 12345,
 "deadline_ms": 10000}
{"id": 2, "phase": "post-receive",
 "landed": [{"ref": "...", "old": "...", "new": "..."}], "events": [101, 102]}
```
`HookReply` (kind 13, client → daemon):
```jsonc
{"id": 1,
 "verdicts": [{"ref": "refs/heads/main", "verdict": "accept"},
              {"ref": "refs/for/main", "verdict": "divert", "review_ref": "refs/changes/42/head"},
              {"ref": "refs/heads/x", "verdict": "reject", "reason": "protected branch"}],
 "reject_all": null}
```
Rules:
- Every command ref gets exactly one verdict. Missing, duplicate, or unknown refs are treated as `reject_all`.
- `accept` on a command with `"authorized": false` is ignored. Ruk's `push_refs` capability (`ruk-core/src/capability.rs`) decides before the callback runs.
- `divert` is permitted only for refs under the configured review prefix (`refs/for/`). `review_ref` must start with `refs/changes/` and must differ from every command ref. Ruk writes the review ref through the same CAS path.
- `reason` capped at 512 bytes.
- Deadline `hook_timeout_ms`. On expiry, or on stream close, every ref is rejected with `policy timeout`. A late reply is ignored by id.
- `post-receive` expects no reply. Durable notification is the event stream. The `post-receive` frame is advisory.
- The callback runs **before** any store publish and holds no store writer lock. The host may issue read methods during the callback.

### 6.5 Ordering on the daemon path

1. Parse command list (pkt-lines). Compute digest: SHA-256 over sorted `(ref, old, new)` plus service name. [INFERENCE: sorted is safe because pack ingest does not reorder commands.]
2. Receipt lookup in `ServiceReceipts` keyed by `(session, request_id)`. Same digest → replay stored report, no publish. Different digest → `E_REQUEST_REUSED` (existing code).
3. Ingest pack into memory (existing `StagedPack`). Run existing refusals (`refusals_for_live`, LFS mode from repo policy rather than the hardcoded `LfsMode::Deny` at `serve.rs:2108`).
4. Capability check (`push_refs`) per ref. Mark `authorized`.
5. `HookCall pre-receive`, await `HookReply` (timeout as above).
6. Publish objects and commit facts (existing `serve.rs:755`).
7. Per-ref CAS via `apply_refs` (`session.rs:778`), each publication carrying its `Refs` mutation and emitted event (`EventSource::ReceivePack`).
8. Write `ServiceReceipts` row with the report bytes, in the final publication.
9. Send report. Send `HookCall post-receive` (advisory).

Replay tolerance: if the connection dies after step 7 but before step 8, a retry recomputes CAS. A ref already equal to `new` counts as ok. This closes the gap without a combined batch.

### 6.6 Sync-async bridge

Receive-pack's reader and writer are synchronous `Read`/`Write` (`serve.rs:514-516`). The daemon runs it on the blocking pool under `transport_permits`. A `Read` adapter pulls from a channel fed by the async stdin pump. A `Write` adapter sends to the connection writer task. `HostPolicy` sends `HookCall` through the writer channel and waits on a `std::sync::mpsc` fed by the reader when a `HookReply` arrives.

### 6.7 Tests

- Core: `host_pre_receive_per_ref_accept_reject`, `host_divert_writes_review_ref_only`, `host_reply_missing_ref_means_reject_all`, `host_timeout_rejects_all_fail_closed`, `host_unauthorized_ref_refused_before_callback`, `post_receive_failure_never_fails_push`, `receive_replay_same_digest_no_second_publish`, `receive_digest_mismatch_refused`, `receive_ref_already_at_new_on_replay_is_ok`, `script_policy_matches_existing_hook_outputs` (regression for CLI), `refs_pull_no_longer_diverted`.
- Daemon: `git_service_receive_pack_round_trip_over_socket`, `git_service_requires_session_and_request_id`, `frame_kind_9_rejected_on_any_stream`.

---

## 7. Schema and digest bump rules

| Change | Bump | Notes |
|---|---|---|
| Append `Method` row + `required` entry + count | `SCHEMA_MINOR` | `protocol.rs:65-68`; `:5417-5484`. Never reorder or remove (postcard). |
| Add `MethodKind::Admin` | minor | Readers see unknown kind. |
| New optional (`serde(default)`) field on a **read** or **new** method | minor | No digest impact (reads have no receipts). |
| New optional field on an **existing mutation** arg | minor **plus** `REQUEST_DIGEST_VERSION` bump (`protocol.rs:92`) if that method already has receipts | Adding a field changes `digest_input_v1` postcard encoding. Avoid by moving secrets to the open frame (§5.12). |
| New `ErrorCode` | **avoid** | Closed set (`error.rs:31-92`). Cursor expiry is a reply status. |
| Change required field, rename, or change semantics of an existing method | major | |
| Durable row struct change (Receipts, Session, Capabilities, PushPlans, OpHeader, OpDelta) | row version bump, not schema minor | Postcard is positional (`protocol.rs:3123-3127`). |
| New `FactTable` variant (`Events`, `EventHead`, `ServiceReceipts`, `RepoMeta`) | store format review | Verify persisted-name behavior (§5.11). Not tracked by op log. |
| IPC kind 9 retired; kinds 12, 13 added; features changed; profile changed | IPC is tied to `SCHEMA_MAJOR` (`ipc.rs` imports it) | Ship with minor 2. Only rukie and ruk talk IPC, and they ship together. |

Single schema bump commit: `SCHEMA_MINOR` 1→2 with all additive methods, `MethodKind::Admin`, and the updated consistency test.

---

## 8. Implementation slices (each commit builds)

1. **CLI rename and docs.** `main.rs` `Serve`, README row, `daemon/mod.rs` doc and error text. Clap test.
2. **Remove argv path.** Delete §3.2 items. Add git-service envelope (`command_inner` rewritten). Keep `route_credit`, `write_data_chunks`, `send_*`. Tests in `tests.rs` rewritten to the envelope. Handshake features and profile updated. `MergeNotices` kind 9 retired.
3. **Per-stream tasks and writer task** (§4.6). No protocol change.
4. **`ReceivePolicy` trait.** `ScriptPolicy` wraps `ReceiveHooks`. CLI behavior unchanged. Regression tests.
5. **Events foundation.** `FactTable::Events`, `EventHead`, emission in `Store::publish` with `EventSource` argument (call sites updated in this commit), retention sweep, `oplog::tracked` exclusion. Store-level tests only (no method yet).
6. **Schema bump to minor 2** with `events.tail`, `refs.list` (+ prefix seek), `tree` (+ `ruk-core/src/tree.rs`), `repo.stat` (+ `RepoMeta`). Required array and count updated in the same commit.
7. **`compare` and `search`.** Move merge-base into core. Add `regex` to `ruk-core`. CLI callers switched.
8. **`archive`.** `TarWriter` factored out, zip transcoder moved to `ruk/src/archive_zip.rs`, streamed dispatch in `open_inner`.
9. **`HostPolicy` and `ServiceReceipts`.** Frames 12 and 13. Bridge. Git-service receive-pack over socket.
10. **Admin methods.** `MethodKind::Admin` (may be folded into slice 6 if bump is batched). `repo.init`, `repo.delete`, `repo.rename`, `repo.fork`. `admin_uids`.
11. **`push` transport job** and `TransportAuth`.
12. **Cutover.** Delete `forgejo-patch-inventory`, rewrite `.omp` plan, README count (58), CHANGELOG entry, `.gitignore:6` once ambient dir is removed by owner.

Gates per slice: `cargo fmt --all -- --check`, `cargo clippy`, `cargo test -p ruk-core`, `cargo test -p ruk-git`, `cargo test -p ruk`. Oracle-backed tests use external `git` as a test-only dependency.

---

## 9. Gaps not in the requested list (flagged)

- `remote.refs` (advertised remote refs). Needed for exact-mode push mirror prune. Not requested; `push` alone cannot compute prune sets.
- `fetch` for pull mirrors. Still refused at `coord/prepare.rs:277-282`. Same transport-job pattern as push.
- `land.execute` is refused (`prepare.rs:296-301`), and `land.prepare` ships empty requirements (`prepare.rs:3431-3486`). Lands cannot produce `land` events until this exists.
- `file` method for directories (`protocol.rs:2404`) remains refused; `tree` is the forge path.

---

## 10. Decision points and risks

1. **Principal model (D3).** Single OS-peer principal means Ruk enforces only service-level capabilities. Per-user and per-token scopes are rukie's job. Confirm.
2. **Store-level event emission (D4).** Touches every `Store::publish` call site. Measure before slice 5.
3. **Bulkhead starvation.** `BoundedExecutor::run` holds its permit for the whole closure (`executor.rs` doc). Long streams must use `transport_permits`, not the shared 16-permit pool.
4. **Digest drift.** Any new field on an existing mutation's args changes `digest_input_v1`. Keep credentials and new mutation metadata out of args.
5. **Replay after partial publish (§6.5).** Relies on "already at new means ok". Covered by tests, but it is the subtlest invariant in this design.
6. **`ruk/src/cli` shares archive and merge-base code with the daemon.** Moving them requires CLI output golden tests to stay green.
7. **Unverified:** `dumb.rs` callers; `docs/coordinator.md` content; exact call-site count for `Store::publish`; whether `FactTable` names are persisted; `Merge<Oid>` resolved accessor name; `zeroize` availability in the workspace.

---

### Critical Files for Implementation

- `ruk/crates/ruk/src/daemon/mod.rs` — argv path removal (`:390-585`, `:1089-1661`), git-service envelope, `HostPolicy` frames, `dispatch_method` arms (`:2109`), archive stream arm (`open_inner` `:1973`).
- `ruk/crates/ruk-core/src/protocol.rs` — `method_table!` tail (`:3285-3333`), `required` array and count (`:5431-5483`), `SCHEMA_MINOR` (`:68`), `MethodKind` (`:1131`), `KeysetPage`, arg and reply types.
- `ruk/crates/ruk-core/src/coord/hosted.rs` — read arms (`:566-640`), `read_gap` cleanup (`:1349`), `view()` accessor, bulk read admission.
- `ruk/crates/ruk-git/src/transport/serve.rs` — `serve_receive_pack_with` policy trait swap (`:554`), phase rewiring (`:690-830`), `serve_command` (`:2097`).
- `ruk/crates/ruk-core/src/store/mod.rs` — `Store::publish` event emission (`:911`), `fact_page` prefix seek (`:847`), `lock_writer` (`:640`).

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Do not claim `refs.list.as_of…