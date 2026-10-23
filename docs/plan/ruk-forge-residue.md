**Status: artifact not written.** This session has no file-write tool (only `read`, `grep`, `web_search`), so I could not create `local://ruk-forge-residue.md` or the absolute path the orchestrator gave. The full manifest is below. Save it to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-forge-residue.md`. Line numbers marked ≈ are read from truncated grep output and may be off by one.

# ruk-forge-residue (ruk repo, nested checkouts excluded)

## 1. Tracked `forgejo` occurrences

**CLI wiring (`crates/ruk/src/main.rs`)**
- :31 `enum ForgejoCommand`; :64 doc; :69 `Forgejo` variant; :71 `command: ForgejoCommand`.
- :201–204 `Command::Forgejo{ForgejoCommand::Serve{config}}` arm; reads the TOML and calls the daemon.

**Daemon (`crates/ruk/src/daemon/`)**
- `mod.rs` :1, :3 module doc ("behind `ruk forgejo serve`", "one process owns every opened repository of a Forgejo instance"); :58 `DaemonConfig` doc; :391–392 `CommandOpen.session_id` doc ("Forgejo service session; accepted and ignored"); :654–655 error text "ruk forgejo serve needs a Unix socket"; :665 thread name `ruk-forgejo-serve`.
- `ipc.rs` :40–43 `SUPPORTED_FEATURES` doc and const; :410–412 `protocol_minor` doc ("The Forgejo Go backend omits"); :415 `profile` doc ("such as `forgejo`"); :550 EForbidden text "not an authorized Forgejo service identity"; :643 test `profile: "forgejo"`.
- `tests.rs` :3, :6 module doc; :123 fixture `session_id: "forgejo-svc"`; :322 and :377 handshake `profile: "forgejo"`.
- No hits in `registry.rs`, `executor.rs`, `cli/`, `mcp/`, `crates/ruk/tests/coordinator.rs`, or any `Cargo.toml`.

**ruk-core**
- `src/error.rs:4` doc on "The Forgejo contract's extra outcomes".
- `src/store/durability.rs:36` ("Forgejo's `MkdirAll`").
- `src/store/mod.rs:288` ("the Forgejo patch" provisions the parent).
- `src/merge/composed/mod.rs:5` ("the Forgejo merge result" consumer).
- No hits in `protocol.rs`, `coord/`, or `capability.rs`.

**ruk-git transport**
- `dumb.rs:3` doc.
- `hooks.rs:22, :24, :32` doc on Forgejo dispatcher scripts; cites `forgejo/modules/git/hook_generate.go`.
- `mirror.rs:1, :3` module doc ("pull/push mirrors").
- `net.rs:265` comment on Forgejo's `forgejo hook post-receive`.
- `proc_receive.rs:4, 7, 8, 11, 20, 24, 33, 42, 56, 58, 99, 355, 730, 884`. Doc comments cite `forgejo/…` paths. 730 is a test name doc and 884 a test comment. `PROC_RECEIVE_PREFIXES` at :59 is `["refs/for","refs/pull"]`, the Forgejo AGit values.
- `serve.rs:153, :605, :724, :726` comments (`MAX_PUSH_OPTIONS`, push options, `update` dispatcher, `Forgejo never sees an update call`).

**Root and docs**
- `README.md:3` ("targeting Forgejo"), `:67` (`ruk forgejo serve --config <path.toml>` row), `:91` (`crates/ruk` "Forgejo multi-repository daemon").
- `.gitignore:6` `forgejo/` ignore rule for the oracle checkout.
- `forgejo-patch-inventory` (repo root, 142.8 KB JSON; `name: "forgejo-patch-inventory"`, `forgejo.revision acd7bc15…`, rows R01–R17 against Forgejo Go code). Forgejo-only. **Tracked status: unverified.** Not in `.gitignore`; the git index is not greppable here.
- `.omp/plans/AGENT_NATIVE_GIT_PLAN.md` (Forgejo integration plan). Matching lines: 7, 13, 14, 25, 26, 28, 29, 34, 44, 48, 49, 55, 57, 64, 65, 67, 80, 83, 84, 87, 94, 95, 102, 108, 117, 125. Output appeared truncated after 125. **Tracked status: unverified.**
- No hits in `CHANGELOG.md`, `docs/coordinator.md`, `.github/workflows/ci.yml`, `Cargo.toml`, `rust-toolchain.toml`, or `.outline/`.
- Excluded: `rukie/`, `forgejo/`, `git/`, `jj/`, `tasks/`.

## 2. Daemon structure (`crates/ruk/src/daemon/`)

Files: `mod.rs` (2444 lines), `registry.rs` (745), `ipc.rs`, `executor.rs` (2.5 KB), `tests.rs` (1958).

**Serve path**
- `main.rs:201–215` → `daemon::serve(config)` at `mod.rs:651`.
- Config: `Doc` at `mod.rs:178`; `from_toml` at :176; `validate` at :223.
- `serve_async` at `mod.rs:707`; builds `Registry` and `BoundedExecutor`; accept loop spawns `serve_connection` (:746–749).
- `serve_connection` at :781: peer auth (`ipc::authenticate_peer`), then handshake via `ipc::negotiate` (`ipc.rs:443`).
- Frame routing: stream 0 → `handle_control_frame` (:824) → `answer_control_open` (:873), which accepts only `type == "outcome-lookup"`. Other streams → `handle_stream_frame` (:957) → `handle_open` (:1089).

**Two open shapes, chosen in `handle_open` (:1089–1127)**
1. **Command stream (Forgejo-only):** if the payload decodes as `CommandOpen` (argv shape, `mod.rs:390`), call `command_inner` (:1205).
2. **Typed invocation:** otherwise decode `InvocationOpen` (`mod.rs:305`) and call `open_inner` (:1972).

**Typed path**
- `open_inner` (:1972) finds the method with `Method::ALL.iter().find(|m| m.as_str()==open.method)`, then `registry.open(repo)` through the executor, then sends `Accepted`.
- It calls `dispatch_method` at :2060.
- `dispatch_method` (:2109–2200):
  - `Schema` → `model_schema_json` (:2119).
  - `SessionOpen` (:2120) and `SessionResume` (:2142) are the bootstrap arms.
  - Default arm: `decode_request_args` (:2262) → `validate` → `refuse_sessionless` (:2214) if no session → Read: `coordinator.read` (:2174) / Mutation: `check_session` (:2180), `prepare_for_dispatch` (:2188), then `submit_prepared` (:2196).
- `decode_request_args` (:2262–~2345) has one arm per method, for example `Method::WsNew => mutation_request(id, decode_args(text)?, Request::WsNew)` (:2289) and `Method::Status` (:2328). `mutation_request` is at :2232.

**Command (argv) path — Forgejo-only**
- `command_inner` (:1205): destructures `CommandOpen` (`session_id: _` at ≈1214–1215), then `claim_request_slot` (:1148, `RequestSlot::{Run,Replay,Busy,DigestMismatch}`), then `executor.run(validate_command_fs)` (:1548; `check_env_path_under_roots` :1589; `normalize_lexical` :1622).
- It then sends `Accepted` and calls `serve_ready_command` (≈:1334–1420).
- `serve_ready_command`: `collect_command_stdin` (:1685) → `full_argv = ["git", argv…]` (≈1360) → `executor.run(cli::run_captured(full_argv, CliEnv{vars,cwd,stdin}))` (≈1369–1375) → `CommandExitReport::success` (:447) → `OutcomeLedger::complete` (:570) → `write_cmd_data` for Stdout/Stderr (:1861) → Exit frame → `replay_pending`.
- Verbs: the daemon has no verb whitelist. Any git-CLI verb is reachable. `cli/mod.rs:209` `run_captured` → `run_inner` (:308) → `cli::lookup` in `cli/dispatch.rs:956–960`.

**Registry (`registry.rs`)**
- `Registry::new(roots)` ≈83–90; `set_runtime` ≈117–120 (`E_HOSTED` until bound); `set_retention_interval` ≈135; `resolve` ≈147–158 (canonicalize; `E_FORBIDDEN` outside roots).
- `open(repo_ref)` ≈188–205: a leading `/` goes to `resolve` + `handle_for_path`, otherwise `open_by_handle`. Then `coordinator_for`.
- `coordinator_for` ≈214–250: `Store::open(canonical, reference_defaults)` (≈237), `repository_owner` (≈270), `HostedCoordinator::open` (≈240), `spawn_on(runtime)`, `spawn_retention`, then cache the entry.
- Also `open_by_path` ≈299, `open_by_handle` ≈309, `handle_for_path` ≈331, `evict_idle` ≈365, `shutdown_all` ≈485, `scratch_root` ≈519.

**Where a new typed method goes**
- Protocol: `ruk-core/src/protocol.rs` `method_table!` (see §3).
- Daemon decode: `daemon/mod.rs` `decode_request_args` (:2262–2345). The generic default arm in `dispatch_method` (:2166+) needs no change.
- Core: reads → `ruk-core/src/coord/hosted.rs` `read` (:566), or `read_gap` (:1349) for names of unimplemented reads. Mutations → `ruk-core/src/coord/prepare.rs` `prepare_mutation_with` (:233, match arms ≈2249–2305). Unimplemented mutations return `uncovered(...)` ("…with no producer on this path").
- Doc: `coord_call_site` (`mod.rs:617–629`) lists covered methods.

**Test harness (`daemon/tests.rs`)**
- Helpers: `seed_commit` (≈44–78; `Store::open` + `prepare`/`publish`); `temp_root` (≈80–84; `ruk-daemon-{pid}-{id}-{case}`); `frame_deadline` (≈91–93, 15 s); `jstr` (≈97–114); `command_open_payload` (≈115–133, fixture `"forgejo-svc"`).
- `struct Driver` (≈134–137: client `UnixStream`); `spawn_pair` (≈335–349: `UnixStream::pair`, `Registry::new(vec![root])`, `set_runtime(Handle::current())`, `ConnectionRefs`, `tokio::spawn(serve_connection)`); `handshake` (≈321–325, raw JSON with `profile:"forgejo"`).
- Forgejo wire tests: `handshake_hello_without_minor_decodes` (≈375), `command_stream_serves_go_wire` (≈385), `command_stream_pipes_stdin_with_credit_grants` (≈482), `command_stream_cancel_reports_cancelled` (≈524).
- Typed tests: `typed_*` at ≈570–1905, for example `typed_dispatch_serves_covered_read_and_mutation` (570), `typed_claim_acquire_and_release_round_trip` (1229), `typed_split_of_unstored_change_refuses_not_found` (1547), `typed_land_execute_refusal_names_plan` (1905).

## 3. Protocol method table and add-a-method steps

**Declaration (`ruk-core/src/protocol.rs`)**
- `SCHEMA_MAJOR = 1` (:63); `SCHEMA_MINOR = 1` (:68). Doc at :66–67: "Minor 1 appends the `git.commit` lane method…".
- Macro doc on append-only order sits just before `macro_rules! method_table` (≈3190–3201). It states discriminants are postcard positional indices, so entries may only be appended.
- `macro_rules! method_table` (≈3133–3280) generates: `enum Method` (:3133–3136), `Method::ALL` (:3140), `as_str`, `kind` (:3152), `is_mutation`, `enum Request` (one `Box<params>` variant per method), `Request::method`, `request_id`, `validate` (`Validate::validate` per variant), and digest helpers (`digest_args`, `request_id_of`, `outcome_of` macros at ≈3060–3110).
- Table invocation (:3285–3333): **47 entries**, lines 3286–3332. Examples: `Schema, "schema", Anonymous` (:3286); `SessionOpen, "session.open", Bootstrap` (:3287); `WsNew, "ws.new", Mutation` (:3289); `Status, "status", Read` (:3325); `GitCommit, "git.commit", Mutation` (:3332).
- `model_schema_json()` at :3684 builds the draft-2020-12 document from `RequestEnvelope`/`ReplyEnvelope`.
- Tests: schema test at :5310–5312 asserts `methods.len() == Method::ALL.len()`. `method_table_is_consistent_and_covers_the_contract` at :5417: checks duplicate names, serde round trip, `is_mutation` vs `kind`, and a hard-coded `required` list of names beginning around :5430.

**Steps to add a method end-to-end**
1. `protocol.rs`: add `XArgs` / `XReply` with `JsonSchema`. Add `Validate` impl if invariants exist. Append one row to the end of `method_table!` (:3285–3333).
2. `protocol.rs:68`: bump `SCHEMA_MINOR` (per the doc at :66–67, additive methods are a minor bump).
3. `protocol.rs` `method_table_is_consistent…` `required` list (≈5430+): add the name.
4. Core, read: `coord/hosted.rs` `read` (:566) add an arm; otherwise `read_gap` (:1349) names the missing machinery. Core, mutation: `coord/prepare.rs` `prepare_mutation_with` (:233) add an arm returning `PreparedMutation`, or leave it refusing with `uncovered`.
5. Daemon: `daemon/mod.rs` `decode_request_args` (:2262–2345) add `Method::X => mutation_request(id, decode_args(text)?, Request::X)` for mutations, or `Request::X(Box::new(decode_args(text)?))` for reads. Update `coord_call_site` (:617–629) doc text.
6. MCP (optional): `mcp/tools.rs` `TOOL_*` consts (:27–61), `TOOL_TABLE` (:77), name decode (:175–190), call dispatch (≈:227). Only 12 tools are exposed.
7. Model dump: `ruk model` → `main.rs:170` → `model_schema_document` (:304) → `model_schema_json` (protocol.rs:3684). Picks up the new method automatically.
8. Tests: a schema assertion (protocol.rs:5310 pattern) and a `typed_*` daemon test in `daemon/tests.rs` (pattern at ≈570–1905).
9. Docs: `README.md:5` states "46 v1 protocol method schemas"; the table has **47**. Fix when touched.

## 4. Gates and toolchain

- **No Makefile or justfile** in ruk root (`read` of `ruk/Makefile` → not found; root listing shows none). `.omp/plans/AGENT_NATIVE_GIT_PLAN.md:108` cites `make test-sqlite` / `make test-e2e-sqlite`. Those are Forgejo's targets, not ruk's.
- **CI** (`.github/workflows/ci.yml`), toolchain `dtolnay/rust-toolchain` pinned to `1.99.1`:
  - `fmt` (ubuntu): `cargo fmt --all -- --check`.
  - `clippy` (ubuntu/macos/windows): `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`.
  - `nextest` (matrix): `taiki-e/install-action` installs `cargo-nextest@0.9.148`; `cargo nextest run --workspace --locked`.
  - `test` (matrix): `cargo test --workspace --locked`.
  - `ci.yml:7–9` states Store/Miri/fuzz/soak gates are not yet included.
- **Toolchain:** `rust-toolchain.toml` → `channel = "1.99.1"`, `profile = "minimal"`, components clippy and rustfmt. Workspace: `edition 2024`, `rust-version 1.99.1` (root `Cargo.toml` `[workspace.package]`). Workspace members: `ruk-core`, `ruk-git`, `ruk`. `[patch.crates-io]` vendors `redb` and `swc_common`.
- **Test counts:** not recorded in README, CHANGELOG, docs, or CI. No matches.
- **Flaky tests:** no `flaky` or `flake` mention in the repo.
- **Ignored tests** (subprocess children, plus one measurement):
  - `ruk-core/src/graph/tests.rs:604–605` (measurement, run on demand).
  - `merge/syntax/pool.rs:1575–1576` (requires built binary).
  - `store/crash_tests.rs:242–243` (crash child).
  - `store/durability.rs:910–911` (subprocess child).
  - `store/metadata_tests.rs:191–192` (lock child).
  - `store/mod.rs:2904–2905` (relative-root child).
- Daemon tests are `#![cfg(unix)]` (`daemon/tests.rs:8`), so Windows CI skips them.

## 5. Dead or Forgejo-only surfaces

1. **Entire argv command-stream path** (`mod.rs:1089–1127` decode branch, `command_inner` :1205, `serve_ready_command`, `OutcomeLedger` :540–585, `claim_request_slot` :1148). It serves only the Forgejo Go client. `.omp/plans/…:48` says "Forgejo uses typed operations, never argv", yet the code still has the argv path. Contradiction; delete.
2. **`FrameKind::MergeNotices` (=9)** (`ipc.rs` enum, ≈:66–67; decode/encode ≈:88–89, :110, :126–127). The daemon **never emits it**. Only receive-side rejection arms exist (`mod.rs` ≈851, 1018–1019, 1755, 1830–1831, 1960). Advertised via feature string `"merge-notices"` (`ipc.rs:43`, negotiated at :466–468). Dead.
3. **`"merge-notices"` and `"outcome-lookup"` feature strings** (`ipc.rs:43`). `outcome-lookup` is served by `answer_control_open` (`mod.rs:873–921`) with `ControlRequest` (:479) and `ControlReply`. Both exist only for Forgejo reconnect.
4. **`HandshakeHello.profile`** (`ipc.rs:415–416`). `negotiate` checks only non-empty (:453–458) and echoes it (:473). The value `"forgejo"` is never compared. Decorative.
5. **`CommandOpen.session_id`** (`mod.rs:391–393`). Destructured and ignored in `command_inner` (≈1214–1215).
6. **`protocol_minor` serde default** (`ipc.rs:410–414`). Compatibility shim for the Go client that omits the field.
7. **`ipc::FrameKind` stream/credit machinery** (`StreamCredits`, `WindowCredit`, `apply_window_update`, `route_credit`, `collect_command_stdin`, `absorb_stdin_chunk` in `mod.rs` ≈1040–1870). Used only by the command path, so dead with item 1.
8. **`ruk-git/src/transport/proc_receive.rs:56–59`**: `PROC_RECEIVE_PREFIXES = ["refs/for","refs/pull"]`, the Forgejo AGit values. Reached through `serve.rs:58–61` and `serve.rs:729`, so not dead, but Forgejo-hardcoded. Make configurable or drop with AGit.
9. **`ruk-git` comment-only Forgejo refs**: `hooks.rs:22–32`, `net.rs:265`, `dumb.rs:3`, `mirror.rs:1–3`, `serve.rs:153/605/724/726`. Behavior is generic git (`mirror.rs` functions are used at `cli/verbs_transport.rs:37`), so keep the code and fix the comments.
10. **Forgejo-named fixtures:** `daemon/tests.rs:123, :322, :377` and `ipc.rs:643, :648–655`. Rewrite or delete with the argv path.
11. **Error and naming text:** `ipc.rs:550` EForbidden "Forgejo service identity"; `mod.rs:654–655` error string; `mod.rs:665` thread name `ruk-forgejo-serve`; `main.rs:31–72` `ForgejoCommand` and `main.rs:201–215`.
12. **Config surface:** `daemon/mod.rs` `Doc` / `from_toml` (:176–226), `DaemonConfig`, `allowed_uids` (`ipc.rs:534–552`, empty = all local peers). Daemon-only. Also `crates/ruk/Cargo.toml` `toml`: probably daemon-only, **unverified** (not grepped).
13. **Root artifacts:** `forgejo-patch-inventory` (142.8 KB), `.omp/plans/AGENT_NATIVE_GIT_PLAN.md`, `README.md:3/67/91`, `.gitignore:6`. Forgejo-only. Tracked status of the first two unverified.
14. **Comments:** `ruk-core/src/error.rs:4–7` (Forgejo contract outcomes), `store/durability.rs:36–37` (Forgejo `MkdirAll`), `store/mod.rs:287–291` (Forgejo patch provisions parent), `merge/composed/mod.rs:4–7` (Forgejo merge result).
15. **Stale count:** `README.md:5` says 46 methods; `protocol.rs:3286–3332` has 47.

**Reusable for a new forge:** `protocol.rs` `method_table!` and `Method`/`Request` (generic); `dispatch_method` typed arm (`mod.rs:2109–2200`) with `decode_request_args`; `Registry` (`registry.rs`) and `HostedCoordinator`; `BoundedExecutor`; `ruk-core/src/coord/*`.

## Unverified / caveats
- Tracked status of `forgejo-patch-inventory` and `.omp/`: git index and reflog are not greppable here. `.gitignore` does not exclude either.
- `.omp/plans` match list may be truncated after line 125.
- Some daemon line numbers are ≈ (±1–3) from image-rendered grep output.
- `crates/ruk/Cargo.toml` `toml` usage not verified.
- Daemon `command_inner` / `serve_ready_command` exact line ranges are approximate.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

→ grep(full_argv|MergeNotices|prune_for_spec|push_mirror_updates|GitCommitArgs @ /home/alpha/rewrite/git-renewed/ruk/crates/ru…) ⇒ ok · 129 lines
→ grep(FrameKind::MergeNotices|MergeNotices = |MergeNotices =>|Self::MergeNotices @ /home/alpha/rewrite/git-renewed/ruk/crates…) ⇒ ok · 55 lines

[advisor] <advisory advisor="glm-flash" severity="blocker" guidance="weigh, don't blindly obey"> Re-verify all citations and the …

[advisor] <advisory advisor="muse" severity="blocker" guidance="weigh, don't blindly obey"> Write file or prove no write tool. Yo…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Remove or verify each citation tha…

---
**The artifact is not written.** My toolset is `read`, `grep`, and `web_search`. None of them writes files, so I cannot create `local://ruk-forge-residue.md` or the orchestrator's absolute path. I did not attempt a write because no write tool exists in this session. The corrected manifest follows. Save it to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-forge-residue.md`.

**Verification limits.** I cannot run `git -C ruk grep`. Every search in this session was a filesystem `_grep`, and the first whole-tree search swept ignored trees (`forgejo/`, `rukie/`, `git/`, `jj/`, `tasks/`). Tracked status of every path below is therefore **unverified**. Citations marked ≈ come from image-rendered output and may be off by a few lines.

## 1. Forgejo occurrences in ruk (filesystem grep, tracked status unverified)

Ignored by `.gitignore`: `git/` (:4), `jj/` (:5), `forgejo/` (:6), `tasks/` (:7). `rukie/` is not ignored but is an untracked copy per the harness note. Its hits are excluded here.

- **CLI wiring:** `crates/ruk/src/main.rs:31` (`ForgejoCommand`), :64, :69, :71, :201–202 (`Command::Forgejo` → serve arm).
- **Daemon** (`crates/ruk/src/daemon/`):
  - `mod.rs` :1, :3 (module doc), :58, :391–392 (`CommandOpen.session_id` "accepted and ignored"), :654–655 (error text), :665 (`thread_name("ruk-forgejo-serve")`).
  - `ipc.rs` :40–43 (`SUPPORTED_FEATURES`), :410–412, :415, :550 (EForbidden text), :643 (test profile "forgejo").
  - `tests.rs` :3, :6 (module doc), :123 (`"forgejo-svc"`), :322, :377 (profile "forgejo").
- **ruk-core:** `error.rs:4` (doc), `store/durability.rs:36` (doc), `store/mod.rs:288` (doc), `merge/composed/mod.rs:5` (doc). All comment-only; not demolition targets.
- **ruk-git transport:**
  - Comment-only: `dumb.rs:3`, `hooks.rs:22, 24, 32`, `mirror.rs:1, 3`, `net.rs:265`.
  - Code with Forgejo-named values: `proc_receive.rs` :4, :7, :8, :11, :20, :24, :33, :42, :56, :58, :99, :355, :730, :884. `:59` `PROC_RECEIVE_PREFIXES = ["refs/for","refs/pull"]`.
  - Comment-only in `serve.rs`: :153, :605, :724, :726.
- **Root and docs:** `README.md` :3, :67, :91. `forgejo-patch-inventory` (repo root JSON; tracking unverified; no line refs).
- **Plan file** `.omp/plans/AGENT_NATIVE_GIT_PLAN.md` (tracking unverified): Forgejo lines 7, 13, 14, 25, 26, 28, 29, 34, 44, 48, 49, 55, 57, 64, 65, 67, 80, 83, 84, 87, 94, 95, 102, 108, 117, 125. All of these appeared in tool output.
- Not Forgejo: `CHANGELOG.md`, `docs/coordinator.md`, `.github/workflows/ci.yml`, `Cargo.toml`, `rust-toolchain.toml`, `crates/ruk/tests/coordinator.rs`, `cli/`, `mcp/`, `daemon/registry.rs`, `daemon/executor.rs`, `protocol.rs`, `coord/`.

## 2. Daemon structure and dispatch

- **Serve:** `main.rs:201–215` → `daemon/mod.rs:651 serve`. `serve_async` ≈707, `serve_connection` ≈781. Config: `Doc` :178, `from_toml` :176, `validate` :223.
- **Frame routing:** stream 0 → `handle_control_frame` ≈824 → `answer_control_open` ≈873, which accepts only `type == "outcome-lookup"`. Other streams → `handle_stream_frame` ≈957 → `handle_open` :1089.
- **`handle_open` (:1089, verified):** it first tries `ipc::decode_control::<CommandOpen>(&frame.payload)`, which is the argv shape. On a match it calls `command_inner`. Otherwise it decodes `InvocationOpen` and calls `open_inner` ≈1125. The malformed-command case falls through to `InvocationOpen` and reports that error. Exact branch line numbers are ≈.
- **Typed path (shared, not Forgejo-only):**
  - `open_inner` :1972 finds the method with `Method::ALL.iter().find(|m| m.as_str()==open.method)`. It then calls `registry.open(repo)` through the executor and sends `Accepted`.
  - `dispatch_method` :2109 (`dispatch_method(&coordinator,…)` call at :2060):
    - `Schema` → `model_schema_json` (:2119).
    - `SessionOpen` (:2120) and `SessionResume` (≈2142) are the bootstrap arms.
    - Default arm: `decode_request_args` :2262 → `validate` → `refuse_sessionless` ≈2214 → Read: `coordinator.read` (:2174–2175) / Mutation: `check_session` (:2180), `prepare_for_dispatch` (:2188), `submit_prepared` (:2195–2196).
  - `decode_request_args` :2262 onward has one arm per method. `mutation_request` at :2232. Example arms: `Method::WsNew` ≈2289, `Method::Status` :2328.
  - `coord_call_site` :617–629 documents the covered methods.
- **Argv path (Forgejo-only, contradicts plan):**
  - `command_inner` ≈1205. Its destructure includes `session_id: _` at ≈1214–1215, so the session id is ignored.
  - Stages: `claim_request_slot` (:1148) → `executor.run(validate_command_fs)` (:1548; `check_env_path_under_roots` :1589; `normalize_lexical` :1622) → `Accepted` → `serve_ready_command` ≈1334.
  - **Verified:** `mod.rs:1363–1365` pushes `"git"` onto `full_argv` and then extends it with `argv`. `mod.rs:1370–1371` calls `crate::cli::run_captured(full_argv, CliEnv{…})`.
  - This is generic CLI dispatch with no verb allowlist. The daemon validates paths, prepends `git`, and runs the whole git-CLI through `cli/mod.rs:209 run_captured`. It contradicts `.omp/plans/AGENT_NATIVE_GIT_PLAN.md:48`, which says "Forgejo uses typed operations, never argv." Both citations are verified.
- **Registry** (`registry.rs`, ≈ lines): `Registry::new` ≈83–90, `set_runtime` ≈117–120 (`E_HOSTED` until bound), `resolve` ≈147–158, `open(repo_ref)` ≈188–205 (path → `resolve`+`handle_for_path`; handle → `open_by_handle`), `coordinator_for` ≈214–250 (`Store::open`, `repository_owner`, `HostedCoordinator::open`, `spawn_on`, `spawn_retention`).
- **Test harness** (`daemon/tests.rs`, ≈ lines):
  - Helpers: `seed_commit` ≈44–78; `temp_root` ≈80–84; `frame_deadline` ≈91–93; `command_open_payload` ≈115–133 (`"forgejo-svc"`).
  - `struct Driver` ≈134–137; `spawn_pair` ≈335–349 (`UnixStream::pair`, `Registry::new`, `set_runtime`, `tokio::spawn(serve_connection)`); `handshake` ≈321–325 (raw JSON with `profile:"forgejo"`).
  - Forgejo-wire tests: `handshake_hello_without_minor_decodes` ≈375, `command_stream_serves_go_wire` ≈385, `command_stream_pipes_stdin_with_credit_grants` ≈482, `command_stream_cancel_reports_cancelled` ≈524.
  - Typed tests: ≈570–1905 (`typed_*`).

## 3. Protocol method table and adding a method

- **Versions:** `SCHEMA_MAJOR = 1` (`protocol.rs:63`), `SCHEMA_MINOR = 1` (:68). Doc at :66–67 says minor 1 appended `git.commit`.
- **Table:** `method_table!` invocation at :3285–3333, **47 entries** (lines 3286–3332). `GitCommit, "git.commit", Mutation, Mutation<GitCommitArgs>, NewReply;` is last at :3332. `GitCommitArgs` is defined at :1368.
- **Generated items:** `enum Method`, `Method::ALL` (:3140), `as_str`, `kind` (:3152), `is_mutation`, `enum Request`, `request_id`, `validate`, digest helpers.
- **Append-only rule:** doc above the macro (≈3190–3201). Postcard encodes discriminants by position, so reordering corrupts durable v1 data.
- **Tests:** schema assertion :5310–5312; `method_table_is_consistent_and_covers_the_contract` :5417 (duplicates, serde round trip, `is_mutation`, a hard-coded `required` list ≈5430+).
- **Steps to add a method (example: `git.commit`):**
  1. `protocol.rs`: define `XArgs` and `XReply` with `JsonSchema`; append one row to `method_table!` (:3285–3333).
  2. Bump `SCHEMA_MINOR` (:68).
  3. Add the name to the `required` list at :5417+.
  4. Core: reads → `coord/hosted.rs` `read` (:566) plus `read_gap` (:1349). Mutations → `coord/prepare.rs` `prepare_mutation_with` (:233), add an arm, or leave it refusing with "no producer".
  5. Daemon: `daemon/mod.rs` `decode_request_args` (:2262) add an arm; update `coord_call_site` (:617–629).
  6. Model dump: `ruk model` → `main.rs:170` → `model_schema_document` :304 → `protocol.rs:3684 model_schema_json`. Generated, not a checked-in file.
  7. MCP (optional, manual): `mcp/tools.rs` has `TOOL_*` constants (:27–61), `TOOL_TABLE` (:77), name decode (:175–190), and `call_on_store` (≈227). Only 12 tools are exposed. A new method needs a new tool only if it should appear over MCP.
  8. Tests: `protocol.rs` schema assertion (:5310) and a `daemon/tests.rs` `typed_*` test.
- **Drift:** `README.md:5` says "46 v1 protocol method schemas". The table has 47. Fix when touched.

## 4. Gates

**CI-run (`.github/workflows/ci.yml`, toolchain pinned to 1.99.1):**
- `fmt`: `cargo fmt --all -- --check`.
- `clippy` (ubuntu/macos/windows): `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`.
- `nextest` (matrix): `cargo-nextest@0.9.148`; `cargo nextest run --workspace --locked`.
- `test` (matrix): `cargo test --workspace --locked`.
- `ci.yml:7–9` says Store, Miri, fuzz, and soak gates are not claimed yet.
- `rust-toolchain.toml:1–2`: `channel = "1.99.1"`, `profile = "minimal"`, components clippy and rustfmt.

**Plan-only (not run by CI, listed in `.omp/plans/AGENT_NATIVE_GIT_PLAN.md:104–108`):** `cargo +nightly miri test`, `cargo nextest run -p ruk-core --features shuttle`, and `make test-sqlite` / `make test-e2e-sqlite`, which are Forgejo's Makefile targets.

**Other facts:** no Makefile or justfile in the ruk root (read of `ruk/Makefile` returned not found). No test counts recorded. No flaky-test notes found. Ignored tests exist: `graph/tests.rs:604–605`, `merge/syntax/pool.rs:1575–1576`, `store/crash_tests.rs:242–243`, `store/durability.rs:910–911`, `store/metadata_tests.rs:191–192`, `store/mod.rs:2904–2905`.

## 5. Dead and dormant surfaces

**Dormant, proved by a complete grep of `crates/ruk/src/daemon/`:**
- `FrameKind::MergeNotices` (`ipc.rs:66`, decode `:88`, encode `:110`, `:126`). There is no send site in `daemon/`. It appears only in receive-side rejection arms at `mod.rs:850, 1018, 1754, 1830, 1959`. Feature string `"merge-notices"` is advertised at `ipc.rs:43`. Label: dormant, not implemented. The grep covered `daemon/` only. Other crates were not searched for `MergeNotices`.

**Forgejo-only (proved by the reads above):**
- Argv command path: `handle_open` branch → `command_inner` → `serve_ready_command` (`mod.rs:1363–1371`). Contradicts plan `:48`.
- `CommandOpen.session_id` is destructured as `session_id: _` (`mod.rs` ≈1214–1215) and documented as "accepted and ignored" (`mod.rs:391–392`).
- `HandshakeHello.profile` is only checked non-empty and echoed. `ipc.rs:415` documents it; the echo is `profile: hello.profile.clone()` at `ipc.rs:473`. Nothing compares it to `"forgejo"`.
- Thread name `ruk-forgejo-serve` at `mod.rs:665`.
- `ControlRequest`/`outcome-lookup` (`mod.rs:479`, `answer_control_open` ≈873).
- `OutcomeLedger` and command replay: used only by the argv path.
- Test fixtures: `daemon/tests.rs:123, 322, 377`; `ipc.rs:643`.

**Live, not dead (do not demolish):**
- `ruk-git/src/transport/mirror.rs`: `prune_for_spec` (:71) and `push_mirror_updates` (:107). Callers exist at `cli/verbs_transport.rs:37` (import), `:818` (`prune_for_spec`), and `:1631` (`push_mirror_updates`).
- `proc_receive.rs`: `PROC_RECEIVE_PREFIXES` (:59) and `is_proc_receive_ref`. Used by `serve.rs:58–61` and `serve.rs:729`. Forgejo-named values, but generic git behavior.

**Comment-only (rewrite, do not delete):** `error.rs:4`, `store/durability.rs:36`, `store/mod.rs:288`, `merge/composed/mod.rs:5`, `hooks.rs:22–32`, `net.rs:265`, `dumb.rs:3`, `mirror.rs:1–3`, `serve.rs:153/605/724/726`.

**Root artifacts (Forgejo-only, tracking unverified):** `forgejo-patch-inventory`, `.omp/plans/AGENT_NATIVE_GIT_PLAN.md`, `README.md:3/67/91`, `.gitignore:6`.

**Stale:** `README.md:5` count (46 vs 47).

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Remove StreamCredits from section…

---
Neither advisory changes the manifest, but both need a clear statement.

**StreamCredits (advisory 1):** Not a defect in my current manifest. It appeared in an earlier draft and is not in the last one. Section 5 of the last manifest lists only the argv path, `session_id`, `profile`, the thread name, `ControlRequest`/`outcome-lookup`, `OutcomeLedger`, the test fixtures, and the dormant `MergeNotices`. The advisory is right that `StreamCredits` is live on the typed path. The `handle_open` read shows the typed branch calling `credits.open_stream(stream_id)` and then `open_inner(stream, stream_id, &open, credits, refs, peer)`. It must not be demolished. Ignore any earlier draft that lists it.

**Section 1 provenance (advisory 2):** Correct. I have no shell and cannot run `git -C .../ruk grep -n -i forgejo`. Every search in this session was a filesystem `_grep`. The first whole-tree search returned hits from ignored and untracked trees, including `rukie/options/fileicon/material-icon-rules.json` around line 667. The section 1 list in my last manifest omits `rukie/` and the other ignored trees, but it was still built from filesystem grep. Its tracked status is unverified. The contract's required command has not been run. Run `git -C /home/alpha/rewrite/git-renewed/ruk grep -n -i forgejo` and compare it with section 1 before relying on any citation.

**Artifact:** Still not written. This session has no file-write tool (`read`, `grep`, `web_search` only). The manifest is in my previous reply. Save it to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-forge-residue.md`, then run the `git -C` grep to verify section 1.

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Delete the plan:48 quote and the s…