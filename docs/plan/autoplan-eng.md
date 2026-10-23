# autoplan-eng.md: Engineering review of the rukie ground-up rewrite on Ruk (revision 14)

The plan is buildable, but not in the order the skeleton gives. The ruk daemon can't yet host a forge, so ruk's hosting work has to come before most rukie features. Two things block it today:

1. **Typed forge reads are missing.** Over the daemon, `diff`, `show`, `file`, `blame`, `log` and `query` are refused with `E_HOSTED`.
2. **Large transfers don't stream.** Git-wire traffic is buffered whole in memory, and one long command stalls every other stream on its connection.

So the first job is not "add forge read methods" (R2). It is to fix how the daemon hosts: build the typed reads, stream transfers, keep bulk work from taking all the threads, and accept per-user identity from the forge.

**Fix in this revision (the reviewer was right about the circular dependency):** credential provisioning was implicitly an "admin command" that sounded like rukie's later CLI layer (ENG-14), which comes after the handshake work. It is now an explicit ruk-side task that precedes the handshake change:
- **ENG-06** adds a `ruk host-credential` subcommand to ruk's own CLI (`crates/ruk/src/main.rs`, where `serve` already lives) that generates the random credential and writes the 0640 file owned by the daemon's service account, in a group containing exactly the daemon and rukie uids, with named errors;
- **ENG-07** is the handshake/assertion change that consumes that file.

The task list is now 53 tasks; all cross-references renumbered.

**This file has not been written to disk.** This session is read-only plan mode with no write tool. The orchestrator needs to save this message as `autoplan-eng.md` and create `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` from the JSONL block in §12.

---

## 0. Restated problem

Choose the build order, the process layout and the reliability guarantees for two repositories that change together:
- **ruk:** Rust; workspace gates fmt / clippy / nextest / test.
- **rukie:** Go; today a 393k-LOC Gitea tree that becomes an agent-first forge using only the Ruk daemon as its repository backend.

Constraints:
- every commit builds in both repos;
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
- tests must run against a real daemon.

**Where the labels come from:**
- R1–R6, K1–K4, F, U, V: the draft plan outline in this assignment's context (not stored in any repo file).
- S0–S7: stages in §5.
- G1–G17: findings in §1.
- F1–F7: design forks in §3.

## 0a. Authorization boundary

- **This review authorizes nothing.** autoplan ends at a human approval gate. Every task starts only after that approval.
- **Deleting the Gitea tree (ENG-03)** rests on this assignment's settled decision: "Cut every Gitea surface (API v1, swagger, app.ini, xorm schema, Actions, packages, OAuth2 provider, Gitea webhook formats, builtin SSH code, gitea CLI, Vue/Vite/Tailwind, locales) … Land as commits on top of Gitea history; every commit builds." That text lives only in the assignment context, not in any workspace file; the user confirms it at the gate. It covers rukie only, and it is a reversible commit with history kept.
- **The existing ruk plan:** `ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:119-121` says no source cutover, data migration or source-copy purge is authorized by it, and forbids silent feature removal or storage substitution. So: changing that plan (ENG-01) is a proposal decided item by item; the nested `ruk/rukie/` clone is never deleted, and ENG-02 only proposes an ignore entry; ruk's redb store is untouched. The metadata store (F4) belongs to rukie.
- **No outward acts** (push, publish, name reservation) are proposed.

## 1. What the code shows today

| # | Finding | Evidence | Consequence |
|---|---|---|---|
| G1 | The daemon is wired as `Command::Forgejo { command: ForgejoCommand::Serve }`; the handshake profile is hard-coded `"forgejo"`. | `ruk/crates/ruk/src/main.rs:202-220`; `daemon/ipc.rs:643`; `daemon/tests.rs:322` | R1 is a rename plus a protocol change (ENG-05). |
| G2 | Typed hosted reads serve `op.log`, `claim.list`, `attest.list` with lease-bound cursor continuation (`read()` mints a 30-second cursor when a first page does not exhaust the listing and resumes it after binding checks). Every other read names its missing piece in `read_gap`: `status` needs a workspace autosnapshot and workcopy crawl; `log`/`query` need "a production graph index over store segments" because revset evaluation "builds its graph only in tests"; `diff`/`show`/`file`/`blame` need "tree and object readers over resolved revisions". | `ruk-core/src/coord/hosted.rs:539-560, 612-632, 1347-1362` | A forge doesn't need `status`. `diff`/`show`/`file`/`blame` need tree and object readers (ENG-19); `log`/`query` need the graph index (ENG-32, ENG-33). `daemon/mod.rs:612-614` still says "first pages are served", contradicting the coordinator; ENG-19 fixes that stale doc. The argv `CommandOpen` path returns untyped text and blocks other streams (G5), so browsing waits for ENG-19 instead of shipping on it. |
| G3 | Hosted mutations covered: ref, new, claim, ws.new, pin, attest, describe, split, rebase, squash, abandon, edit, ws.scope, ws.take, ws.drop, resolve, revert, undo, land.prepare. Not covered: `land.execute`, `push`, `push.reconcile`, `fetch`, `bookmark.*`, `tag.*`, `snapshot`, `write`, `op.prune`, `unpin`. | `daemon/mod.rs:627` | Merges, mirrors and releases are blocked on these (ENG-20). |
| G4 | Identity is derived from the peer uid alone (`Principal::from_peer_uid`, `ipc.rs:533-550` accepts every process with an allowed UID), and `InvocationOpen` (`daemon/mod.rs:304-316`) carries no assertion field, nonce or sequence; `session_id` and the mutation `request_id` are optional and reusable. | `daemon/mod.rs:2017,304-316`; `daemon/registry.rs:279`; `hosted.rs:590-596` | rukie's one uid would make every user and agent a single principal, any same-uid process could impersonate all of them, and a captured open frame could be replayed. The daemon needs an authenticated, replay-resistant host assertion under distinct service identities (ENG-06, ENG-07). |
| G5 | A long-running command blocks other streams on the same connection (documented v1 limitation); clone, fetch and archive arrive as argv `CommandOpen` streams. | `daemon/mod.rs` `command_inner` doc; `ruk-git/src/transport/mirror.rs:12` | Connections become the bottleneck (`DEFAULT_MAX_CONNECTIONS = 128`); the argv path has no `Method` to classify by (ENG-11). |
| G6 | Command input is buffered in full (up to 256 MiB) before running; output is captured, then streamed; pushed packs are staged into memory. | `daemon/mod.rs` consts / docs; `ruk-git/src/transport/hooks.rs:1-12` | RSS grows with pack size × concurrency; transfers must stream and spill to disk (ENG-09). |
| G7 | The reconnect outcome ledger is in memory (10,000 entries); durable receipts live in the lane. | `daemon/mod.rs`; `ruk/docs/coordinator.md` | rukie must persist request ids; whether receipts are durable on disk is **inference** from coordinator.md, tested by ENG-21. |
| G8 | Each repo's database (redb) allows one writer process; a second gets `DatabaseAlreadyOpen`. | `AGENT_NATIVE_GIT_PLAN.md:152` | The daemon is the only process that opens hosted stores. |
| G9 | Receive pipeline: quarantine, `pre-receive`/`update`/`post-receive`, fail-closed timeouts, and `proc-receive` diversion using git's exact conversation. | `hooks.rs:1-47`; `proc_receive.rs:1-44` | Git semantics that stay in ruk; rukie adds typed handlers per phase (ENG-28). |
| G10 | The `push --mirror` mapping and refspec pruning already exist. | `ruk-git/src/transport/mirror.rs:1-40` | R4 extends existing code (ENG-45). |
| G11 | Daemon limits: 64 KiB data frame, 1 MiB control frame, 256 KiB starting / 16 MiB maximum window, 16 blocking permits, 60 s per-frame timeout. | `daemon/ipc.rs:20-32`; `daemon/mod.rs:24-37` | Per-stream memory has a known upper bound; subscriptions need keepalives (ENG-10). |
| G12 | The admission classes exist and are private to ruk-core: `pub(crate) enum RequestClass { Control, Interactive, Bulk, Hook, Maintenance }`, documented "The dispatcher selects it; a client never does"; the scheduler has separate limits per class; Bulk leases cancel after 30 s without progress and are capped at 2 h. | `ruk-core/src/coord/admission.rs:33-41` (read directly); `admission/scheduler.rs:30-35,222-226`; `leases/timing.rs:15-22` | Don't add a class. The ruk-core dispatcher maps typed methods to classes; the daemon crate can't name the class, so it needs a public `ExecutorLane` mirroring all five variants plus its own verb rule for argv streams (ENG-11). |
| G13 | The executor has two defects: one shared semaphore for every class, so per-class admission is undone at the thread pool; and `run` acquires the permit outside `spawn_blocking` and drops it only after awaiting the join handle, so a cancelled caller releases the permit while `work` keeps running. | `ruk/crates/ruk/src/daemon/executor.rs` (`run`) | Blocking concurrency and memory can exceed their bound under client cancellations; fixed and tested in ENG-11. |
| G14 | ruk CI: fmt; clippy `--workspace --all-targets --all-features --locked -D warnings` on 3 OSes; nextest 0.9.148 on 3 OSes; `cargo test` on 3 OSes; toolchain 1.99.1. Miri, fuzz, soak and store gates are deferred. Lints deny `allow_attributes`, `unsafe_code`, `todo`, `unimplemented`, `dbg_macro`; `unreachable_pub` warns; exact `=` pins with `--locked`. The existing plan's Miri gate: `cargo +nightly miri test -p ruk-core store::segment`. | `ruk/.github/workflows/ci.yml`; `ruk/Cargo.toml`; `ruk/rust-toolchain.toml`; `AGENT_NATIVE_GIT_PLAN.md` "Verification gates" | Unix-only daemon code must pass Windows clippy without `#[allow]`; a new public ruk-core type needs a real outside user or `unreachable_pub` plus `-D warnings` fails CI; the deferred gates are still owed (ENG-12). |
| G15 | `ruk/rukie/` is a clone of `https://github.com/gosuda/rukie` inside the ruk tree, and ruk's `.gitignore` does not exclude it. | `ruk/rukie/.git/config`; `ruk/.gitignore` | Risk of committing a gitlink; never delete it. |
| G16 | `ruk/tasks/autoplan-ledger.jsonl` holds `ENG-01..06` from the earlier autoplan run; `ruk/.gitignore` ignores `tasks/`; there is no `tasks/` directory at the workspace root. | that file; `ruk/.gitignore`; workspace root listing | This run's ledger is `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`: a new file, no id collision. `ruk/tasks/autoplan-ledger.jsonl` is left alone. |
| G17 | `AGENT_NATIVE_GIT_PLAN.md` targets Forgejo parity, forbids runtime Git, gates release on a capacity profile, and requires explicit revision for any removal. | `AGENT_NATIVE_GIT_PLAN.md:5-14,71-99,115-121` | §2 is a revision proposal, not a supersession. |

## 2. Proposed reconciliation with `AGENT_NATIVE_GIT_PLAN.md` (decided at the gate)

| Existing plan item | Proposed verdict | Carried form |
|---|---|---|
| Slices 1–8 | Carry unchanged | Slice 5's production graph index becomes a scheduled task (ENG-32) because hosted `log`/`query` need it. |
| Slice 9 | Carry; backend half adapted | ENG-19, ENG-20, ENG-22, ENG-33. |
| Slice 10 | Adapt; dropping the Forgejo Go patch needs approval | `ruk serve` plus the rukie client (ENG-05..07, ENG-11, ENG-24). |
| Slice 11 | Carry in ruk; forge side adapted | Hook phases stay (ENG-28); mirrors become ENG-45; dropping wiki/template flows needs approval. |
| Slice 12 | Carry | ENG-51. |
| Slice 13 | Repository half carried; Forgejo-database half pending approval | ENG-50. |
| Slice 14 + release gate | Carry | ENG-53. |
| No runtime Git | Carry | ENG-31. |
| CEO-01 frozen Forgejo inventory | Proposed superseded, approval needed | Replaced by the ENG-26 drift gate. |
| DES-01 one `MergeNotice` record; CEO-02 effective mode + `language_set_version` | Carry | ENG-44. |
| Verification gates: Miri, fuzz, shuttle, crash smoke tests, out-of-process harness | Carry | ENG-12 (fuzz + Miri), ENG-23, ENG-52. |

Every item stays in force until the user approves a change.

## 3. Hypotheses on the major forks

### F1 — Process layout
- **H1a (recommended):** two independent processes over a Unix socket under two dedicated service accounts (F7): the daemon as uid A, rukie as uid B, `allowed_uids = [B]`. rukie terminates HTTP and SSH and forwards byte streams to typed git-wire methods. *Disproved if* deploys must be a single binary *and* rukie restarts never need to keep transfers alive.
- **H1b:** rukie spawns the daemon as a child. Rejected: deploys kill in-flight transfers, and it means two runtime paths.

### F2 — Push policy and refs/for
- **H2a (recommended):** the receive pipeline stays; hosted mode registers typed handlers: ref policy at `pre-receive`/`update`, change-id change creation at `proc-receive`, a wake-up hint at `post-receive`. Script hooks remain the CLI and self-hosted default. *Disproved if* the handlers can't see quarantined objects through the view scripts get.
- **H2b:** delete the hook phases. Rejected: breaks slice 11 parity.

### F3 — How ref updates reach CI, webhooks and mirrors durably
- **H3a (recommended):** a stored op-log position plus outbox rows, advanced in one transaction; `post-receive` is only a wake-up hint. *Disproved if* `op.log` can't be paged in order from a durable position across `op.prune`.
- **H3b:** a push-time callback inserts the jobs. Rejected: events are lost while rukie is down.

### F4 — rukie metadata store (open)
- **H4a (recommended):** SQLite WAL, pure Go, behind a SQL-shaped store module. *Disproved by* the capacity gate (ENG-53).
- **H4b (runner-up):** PostgreSQL, which the existing plan uses for capacity acceptance (`:117`).
- **H4c:** Pebble. Rejected: no SQL.

### F5 — Deleting the Gitea tree
- **H5a (recommended):** one reversible commit.
- **H5b:** staged deletion. Rejected: every intermediate commit is a gutted Gitea that still has to build, with no value.

### F7 — Host principal assertion
- **Deployment:** distinct identities. The daemon runs as uid A; rukie runs as uid B; `allowed_uids = [B]`. Credential provisioning is a **ruk-side CLI subcommand** (`ruk host-credential`, added in ENG-06 before any handshake work), writing the random secret as a 0640 file owned by A in a dedicated group whose only members are A and B. Rukie fails at boot with a named error when it cannot read it; the daemon fails at startup when it cannot. A same-account 0600 file is rejected because any same-uid process reads it.
- **Guarantee, stated honestly:** the uid gate plus the credential prevents impersonation by any process outside {A, B}. It does not separate a process running as rukie's uid (B): that process is inside rukie's trust boundary and can ptrace rukie, read its memory and steal sessions and tokens regardless of this layer. User-attribution separation within one uid is not attainable in userspace; the deployment requirement is that only rukie runs as B. A pid → executable check over `SO_PEERCRED` remains optional defense-in-depth, noted as weak.
- **H7a (recommended):** identity additionally carries a proof with daemon-issued freshness:
  1. the handshake accept reply carries a fresh, cryptographically random, single-use per-connection challenge;
  2. a principal assertion is an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the host credential;
  3. the daemon records consumed challenges for the life of the connection and refuses any reuse; it also refuses any second assertion on a session that is already bound;
  4. the asserted principal is bound to the daemon-issued session, so later frames on that session never re-assert.

  Consequences: the same assertion bytes are never accepted twice; a captured open frame replayed on another connection fails (different challenge); replaying frames on the same connection after binding cannot re-assert identity; invocation-level replay of bound-session frames remains the intended request-id idempotency path. Sessions, leases, receipts and attestations attribute to the bound principal.
- **H7b:** accept bare assertions from allowed uids. Rejected: any allowed-uid process could impersonate every user and forge the audit trail.
- **H7c:** HMAC over a client-chosen nonce only. Rejected: the client can replay its own nonce.
- **H7d:** same service account with a 0600 file. Rejected: no peer separation, and a daemon-created 0600 file is unreadable by a differently-owned rukie.

### F6 — Executor isolation across the crate boundary
- **H6a (recommended):** `RequestClass` stays `pub(crate)`; ruk-core exposes a public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) plus a `Method` → lane mapping derived from the dispatcher's class choice; the daemon classifies argv `CommandOpen` streams by verb on the server side (table below), never emitting Hook or Control; `BoundedExecutor` reserves permits per lane and holds each permit inside the `spawn_blocking` closure. *Disproved if* all blocking work could move into ruk-core's admitted path; the argv runner and git-wire serving live in the daemon crate, so it can't.
- **H6b:** make `RequestClass` public. Rejected: it would let clients choose their own class, against `admission.rs:33`.
- **H6c:** one executor per class. Rejected: idle threads, against the executor's design (`executor.rs` header).
- **H6d:** a three-variant `ExecutorLane`. Rejected: hook work would land on Bulk permits and exceed that class's limit.

Verb rule for argv streams:

| Verbs | Lane |
|---|---|
| `upload-pack`, `receive-pack`, `upload-archive`, `archive`, `fetch`, `push`, `pack-objects`, `index-pack`, `grep`, `blame` | Bulk |
| `gc`, `repack`, `prune`, `maintenance`, `fsck` | Maintenance |
| Any other listed verb | Interactive |
| Unknown verb | Bulk |
| *(never produced by argv)* | Hook, Control |

## 4. Six-principle register (engineering)

| Principle | Decision | Open question |
|---|---|---|
| **Clarity** | One operation table (ENG-15) derives HTML, JSON, MCP and CLI. One wire contract (schema codegen + digest gate). One git-wire method family. One event spine. The daemon is the only process opening stores. One receive pipeline with script or typed handlers. Admission classes chosen server-side only; the executor sees all five lanes. Host identity = distinct service uids + a ruk-CLI-provisioned group-restricted credential + an HMAC'd assertion over a daemon-issued single-use challenge, with the guarantee scoped to outside-{A,B} processes (F7). Plan changes are proposals. | — |
| **Impact** | Fix streaming, per-stream concurrency and executor isolation (G5, G6, G13) first. Tree/object readers unlock file, commit and diff pages. The graph index (ENG-32) is the long pole for history and branch views. | — |
| **Audience** | Typed errors map one-to-one to RFC 9457 / MCP with `Retry-After`. `MergeNotice` and effective mode on every merge result. Zero-JS HTML from the same operations. Agents use scoped tokens on human accounts (ENG-16). | Runner credential class. |
| **Risk** | Pack memory; principal impersonation by outside processes (F7, ENG-07), with same-uid processes named as out of scope; lost events; the single-writer store lock; a cancelled request freeing its permit while its work keeps running, and argv transfers bypassing lanes (G13, ENG-11); leaking `RequestClass` to clients; SSRF; cross-repo drift; runtime Git; unauthorized plan or destructive changes (§0a). | Lane receipt durability (ENG-21). |
| **Sequencing** | Tasks are numbered in stage order (§5); each depends only on earlier tasks. Provisioning (ENG-06) precedes the handshake (ENG-07), both in ruk, so S1 has no circular dependency. Store → operation table → auth → users/orgs before any authenticated feature. Git wire → hooks → frontends → walking skeleton. Graph index → `log`/`query` → history pages → review. Capacity is the final release gate. | — |
| **Reversibility** | `protocol_major` bump plus `RUK_PIN`; forward-only migrations with backup restore; Gitea deletion revertible; PostgreSQL fallback; script hooks can be re-enabled per repo; `ExecutorLane` and the verb table are small and easy to change before release; the host credential can be rotated by regenerating the file. | — |

## 5. Amended dependency order

### 5.1 Which ruk capability blocks which rukie feature

| rukie feature (task) | Blocking ruk capability (task) | Today |
|---|---|---|
| Auth, users, orgs, teams, tokens (ENG-16, ENG-17) | none | — |
| Any repo-touching feature | `ruk serve`, credential provisioning, authenticated host assertion, handshake check (ENG-05, ENG-06, ENG-07, ENG-25) | missing |
| Repo hosting and browsing (ENG-26) | Tree/object readers + tree / refs / compare + lifecycle (ENG-19, ENG-22) | E_HOSTED |
| History, branch view (ENG-34) | Graph index → `log`/`query` (ENG-32, ENG-33) | E_HOSTED; graph built only in tests |
| HTTP / SSH clone & push (ENG-29) | Per-stream tasks, streaming, spill, lanes, git-wire methods, hook handlers (ENG-08, ENG-09, ENG-11, ENG-27, ENG-28) | buffered; permit freed early |
| Webhooks, inbox (ENG-37, ENG-38) | Op-log tail + keepalive (ENG-10, ENG-35) | 60 s frame timeout kills subscriptions |
| Review (ENG-41) | Hook handlers (ENG-28), hosted `land.execute` (ENG-20), R6 extensions (ENG-40) | script-only / `land.execute` uncovered |
| Tasks (ENG-42) | R6 claim extensions (ENG-40); `claim.*` and its cursor paging already exist | lease semantics missing |
| CI (ENG-43) | R6 attestation kinds (ENG-40); `attest.add` and cursor paging already exist | kinds missing |
| Mirrors (ENG-46) | Hosted `push` / `push.reconcile` (ENG-20) + R4 (ENG-45) | uncovered |
| Search (ENG-47) | Bounded `search` in the Bulk lane (ENG-47) | missing |
| Releases (ENG-48) | Hosted `tag.*` (ENG-20) + streaming archive (ENG-27) | uncovered |
| Social and forks (ENG-49) | Registry fork (ENG-22) | missing |
| Repo import (ENG-50) | Offline Git conversion | CLI only |

### 5.2 Stages (each stage = commits that each build; all start after the gate)

| Stage | Tasks | Exit proof |
|---|---|---|
| S0 | ENG-01..04: gate items, K1, Go gates | both CIs green |
| S1 | ENG-05..18: R1 daemon work (rename, provisioning, assertion, per-stream, streaming, keepalive, lanes, ruk gates), topology, K3 store, K2 operation table, auth, users/orgs/teams, UI tokens | soak test + fuzz + Miri; executor and verb-lane tests; provisioning and assertion tests; login and token flows on all four surfaces |
| S2 | ENG-19..26: tree/object reads, mutations, receipts, lifecycle, `testruk`, K4 client, drift gate, repo browsing | real-daemon integration tests; browse a repo end to end |
| S3 | ENG-27..31: git wire → hook handlers → frontends → walking skeleton → no-Git trace | **walking skeleton:** stock git clone / push / `push refs/for/main` through rukie |
| S4 | ENG-32..38: graph index, `log`/`query`, history pages, op-log tail, consumer, webhooks, inbox | kill-restart test: no lost or duplicate deliveries |
| S5 | ENG-39..44: token buckets, R6, review, tasks, CI, MergeNotice | end-to-end agent flow (claim → push → CI attest → land) |
| S6 | ENG-45..51: R4, mirror worker, search, releases, social, import, retention stress | §8 targets |
| S7 | ENG-52..53: fault suite, capacity profile | release gate |

## 6. Concurrency and backpressure
- **Daemon:** one task per stream and one writer per connection, bounded by credit windows (`ipc.rs:29-32`).
- **Admission and executor (ENG-11):** typed methods use the dispatcher's class and the derived five-variant `ExecutorLane`; argv streams use the verb rule with unknown verbs on Bulk and never Hook or Control; permits are reserved per lane and held inside the `spawn_blocking` closure.
- **Identity:** distinct service accounts (A = daemon, B = rukie), `allowed_uids = [B]`, group-restricted credential provisioned by `ruk host-credential` (ENG-06), and the HMAC'd host assertion over a daemon-issued single-use challenge (ENG-07, F7); the guarantee covers processes outside {A, B}.
- **rukie client:** a pool of about 4–8 connections with an invocation semaphore below the daemon's limits; cancelling a request's context cancels its stream; `E_LIMIT` maps to 429/503 with `Retry-After`; only idempotent reads are retried automatically.
- **Per-token limits:** token buckets per token and per account in the operation-table middleware (ENG-39).
- **Ref races:** the lane's compare-and-swap (`E_STALE`) is sent back as a typed conflict; no rukie-side locks.
- **Long waits for agents:** served from an in-process broadcast fed by the event consumer, not by polling the daemon; HTML stays zero-JS.

## 7. Durability of CI, webhook and mirror queues
1. **One spine:** one transaction reads ops from the stored op-log position, inserts outbox rows unique on `(repo, op_id, target)`, and advances the cursor; delivery is at-least-once with handlers safe to run twice.
2. **Job claiming:** rows carry leases and attempt counts; claims are atomic `UPDATE … RETURNING`; retries back off with jitter; failed jobs end in a dead-letter state.
3. **Webhooks:** Standard Webhooks signatures; `webhook-id` = outbox key; circuit breaker per endpoint; resolve-and-pin SSRF guard.
4. **Mirrors:** one run at a time with a dirty-flag rerun; periodic reconcile; credentials only in memory; per-ref fast-forward-only rejections.
5. **CI:** the job DAG is stored in metadata with leases and heartbeats; cache key = hash(inputs + spec + image digest); logs are written in chunks to files; the request id is persisted before `attest.add`; runners fetch over the git wire only.

## 8. Performance budgets

**Release gate:** the reference capacity profile (`AGENT_NATIVE_GIT_PLAN.md:115-121`), run by ENG-53.

| Path | Interim target (until measured) |
|---|---|
| Small daemon read over the socket | p99 ≤ 2 ms |
| Metadata-only API operation | p95 ≤ 20 ms |
| Tree / blob / commit page | p95 ≤ 50 ms |
| HTML page weight | ≤ 50 KB gzip, 0 B JS |
| Clone throughput | ≥ 80% of `git http-backend` |
| Push / clone memory | RSS growth independent of pack size beyond about 16 MiB per stream, including under cancel storms (ENG-11) |
| Ref update → webhook delivered | p95 ≤ 2 s |
| Ref update → mirror started | p95 ≤ 5 s |
| Idle CI queue → job started | ≤ 1 s |
| Search | deadline 2 s, capped results, cancellable |
| Bulk saturation | Interactive p95 unchanged while the Bulk lane is at its limit |

## 9. Testing strategy
- **Real daemon, no mocks of ruk:** `testruk` (ENG-23) starts `ruk serve` from `RUK_BIN` and fails, never skips, when it is missing; `make test` builds ruk at `RUK_PIN`.
- **Drift check (ENG-26):** schema regeneration and digest check plus a handshake version check.
- **Stock git and OpenSSH as clients** outside the boundary (ENG-30, after the ENG-29 frontends), plus a trace proving no `git` runs inside it (ENG-31).
…
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.

[Showing lines 7010-7358 of 8940 (49.6KB limit). Use :7359 to continue]
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
…
- **Stock git and OpenSSH as clients** outside the boundary (ENG-30, after the ENG-29 frontends), plus a trace proving no `git` runs inside it (ENG-31).
- **Provisioning and assertion tests (ENG-06, ENG-07):** the subcommand creates the file with the exact owner, group, and mode; boot fails with a named error when the credential is missing; correct HMAC accepted; missing and wrong HMAC refused; the same assertion bytes replayed on the same and on a fresh connection refused (consumed challenge / challenge mismatch); a second assertion on a bound session refused; a peer with a non-allowed uid refused by the gate; a peer with the allowed uid but no valid credential refused.
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.
- **Operation-table tests (ENG-15):** each registered op is reachable and consistent through HTML form, JSON, MCP and CLI from one definition.
- **Fault and concurrency tests:** kill-9 suite, ruk crash/lock/IO smoke tests, shuttle (ENG-52); 60 s fuzz of frame, pkt-line and git-wire decoders plus Miri (ENG-12).
- **Go gates (ENG-04) and ruk gates (ENG-12):** gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz; fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`.

## 10. Repo migration and module path (after the gate)
- **K1 (ENG-03) is one reversible commit** on top of Gitea history: remove the Gitea tree; add `module github.com/gosuda/rukie` (origin `https://github.com/gosuda/rukie`), Go 1.27 / go1.27.2, a fresh `go.sum`, a minimal `rukie serve`, a new Makefile, new CI and a new AGENTS.md.
- **License:** keep the MIT `LICENSE` with Gitea's notice (whether a rewritten tree still needs it is **inference**).
- **No `gitea.dev` aliases.**

## 11. Open questions for the approval gate
1. **rukie metadata store:** SQLite-first with PostgreSQL fallback (recommended) vs PostgreSQL from the start.
2. **CI runner credentials** under "no agent principals".
3. **Host reads without sessions** vs per-principal session pooling.
4. **User script hooks in hosted repos,** or typed handlers only.
5. **Approve item by item** the §2 rows marked "needs approval": the Forgejo patch, wiki/template flows, the Forgejo-database half of migration, CEO-01.
6. **Approve** the ENG-02 ignore entry for `ruk/rukie/`.
7. **Confirm the settled Gitea-cut decision** quoted in §0a. It exists only in the assignment context.
8. **Accept the F7 boundary:** the assertion scheme protects against processes outside {A, B}; same-uid-as-rukie protection is a deployment requirement (only rukie runs as B), not a protocol guarantee.
9. **Ledger target:** `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` (new file, no id collision). `ruk/tasks/autoplan-ledger.jsonl` is not touched.

## 12. ENG tasks (stage order; one line each: id, description, rationale; all start after the gate)

- **ENG-01** — Present the §2 reconciliation as a dated revision proposal for `AGENT_NATIVE_GIT_PLAN.md` and edit that plan only for approved items. *Rationale:* G17 and §0a require an explicit revision rather than a silent supersession.
- **ENG-02** — After approval, add an ignore entry for the nested `ruk/rukie/` clone; never delete it. *Rationale:* avoids committing a gitlink (G15) without purging anything.
- **ENG-03** — K1 under the settled Gitea-cut decision confirmed at the gate: one reversible rukie commit deleting the Gitea tree, module `github.com/gosuda/rukie`, minimal `rukie serve`, new Makefile, CI and AGENTS.md, LICENSE kept. *Rationale:* a staged deletion has no value, and the module path and agent rules must change together (F5).
- **ENG-04** — Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz tests. *Rationale:* every later rukie commit is gated from the start.
- **ENG-05** — R1a: rename to `ruk serve` and profile `host`, and reword Forgejo-specific daemon docs and tests. *Rationale:* the command and profile are Forgejo-named today (G1).
- **ENG-06** — R1b step 1: add a `ruk host-credential` subcommand to ruk's own CLI (`crates/ruk/src/main.rs`, beside `serve`) that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids (paths configurable), with named errors on failure and a test asserting owner, group and mode. *Rationale:* provisioning is ruk-side infrastructure the handshake (ENG-07) consumes; placing it here avoids a circular dependency on rukie's later CLI layer (F7).
- **ENG-07** — R1b step 2: run the daemon as uid A and rukie as uid B with `allowed_uids = [B]`; carry a fresh single-use per-connection challenge in the handshake accept reply; require an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the ENG-06 credential; refuse a reused challenge and any second assertion on a bound session; bind the principal to the daemon session and attribute sessions, leases, receipts and attestations to it; test correct/missing/wrong HMACs, replayed opens on the same and fresh connections, reused challenges, second assertions, refusal of non-allowed uids and of the allowed uid without a valid credential. Document that same-uid-as-rukie processes are out of protocol scope. *Rationale:* a bare uid assertion permits impersonation and a client-chosen nonce permits replay; the guarantee must match what the kernel enforces (G4, F7).
- **ENG-08** — R1c: one task per stream with a per-connection frame writer. *Rationale:* removes the documented stream blocking (G5).
- **ENG-09** — R1d: stream input/output in both directions under credit windows and spill received packs to disk. *Rationale:* buffered packs make RSS scale with pack size (G6).
- **ENG-10** — R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout. *Rationale:* subscriptions would otherwise be timed out (G11).
- **ENG-11** — R1f, in one change: keep `RequestClass` `pub(crate)`; add public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) with a `Method` → lane mapping derived from the dispatcher; classify argv `CommandOpen` streams by verb server-side (transfer/grep/blame → Bulk, gc/repack/prune/maintenance/fsck → Maintenance, unknown → Bulk, never Hook or Control); reserve `BoundedExecutor` permits per lane with each permit held inside the `spawn_blocking` closure; add cancellation, mapping-consistency and verb-table tests. *Rationale:* covers typed methods and argv transfers, keeps hook limits intact, and stops early permit release (G5, G12, G13, F6).
- **ENG-12** — Keep ruk gates green (fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths. *Rationale:* G14 lists owed gates that no other task carries.
- **ENG-13** — Two independent processes with two systemd units and two service accounts, uid-restricted socket, no child-spawn mode. *Rationale:* transfers survive rukie deploys, and the split accounts give the F7 boundary.
- **ENG-14** — K3: SQLite WAL store module, forward-only migrations, leased outbox/job table. *Rationale:* transactional cursor plus outbox needs SQL, and PostgreSQL stays possible (F4).
- **ENG-15** — K2: the `Op[In,Out]` registry on Huma v2 + net/http ServeMux, producing OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), generated `rukie <op> --json` CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots. *Rationale:* every feature, frontend and limit sits on it.
- **ENG-16** — Auth: argon2id passwords + TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts (no agent principals), admin-CLI password reset. *Rationale:* required by every authenticated op and the git frontends.
- **ENG-17** — Users, orgs, teams and repository permissions, enforced in the ENG-15 authz middleware. *Rationale:* authorization for ops and git access.
- **ENG-18** — Design tokens as CSS custom properties and base layouts; each later feature ships its zero-JS pages from its operations. *Rationale:* avoids a big-bang UI phase.
- **ENG-19** — R2a part 1: tree and object readers serving hosted `diff`/`show`/`file`/`blame`; fix the stale `daemon/mod.rs:612-614` doc that says `op.log`/`claim.list`/`attest.list` serve only first pages; exclude `status`. *Rationale:* the real read gap in `read_gap` (hosted.rs:1347-1362); a forge has no workcopy (G2).
- **ENG-20** — R2b: hosted preparation for `bookmark.*`, `tag.*`, `land.execute`, `push`, `push.reconcile`, `fetch`, `unpin`, `op.prune`. *Rationale:* merges, mirrors and releases depend on them (G3).
- **ENG-21** — Crash test proving lane receipts survive a daemon kill at each mutation step and replay is a no-op. *Rationale:* safe retries rest on durable receipts (G7).
- **ENG-22** — R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs, compare/merge-base. *Rationale:* hosting, browsing and forks.
- **ENG-23** — `testruk` harness running the real daemon that fails, never skips, when the binary is missing. *Rationale:* integration tests need a real daemon without false green.
- **ENG-24** — K4: pooled client, semaphore, cancellation, schema codegen, one-to-one RFC 9457 / MCP error map, tested on `testruk`. *Rationale:* backpressure and typed errors for agents.
- **ENG-25** — `RUK_PIN` + schema-digest drift gate + handshake version check. *Rationale:* keeps both repos building together across breaking ruk changes.
- **ENG-26** — Repo hosting and browsing feature: create/rename/delete ops, tree, blob, commit and diff pages and API, on ENG-19/22/24. *Rationale:* first repo-facing feature, proving the operation table end to end.
- **ENG-27** — Typed git-wire methods `git.upload_pack` (v0+v2), `git.receive_pack`, `git.upload_archive`, admitted in the Bulk lane. *Rationale:* one streaming contract for HTTP and SSH (F1).
- **ENG-28** — Keep quarantine and all hook phases (`pre-receive`, `update`, `post-receive`, `proc-receive`, fail-closed timeouts) unchanged; add typed hosted handlers for each phase. *Rationale:* preserves slice 11 parity and git semantics while removing script dispatch for the forge (G9, F2).
- **ENG-29** — rukie Smart HTTP + SSH frontends (`x/crypto/ssh`) that authenticate via ENG-16, authorize via ENG-17, and forward byte streams to the git-wire methods. *Rationale:* authentication stays in rukie, pack logic in ruk (F1).
- **ENG-30** — Walking-skeleton e2e: stock git clone, fetch v2, push and `push refs/for/main` over HTTP and SSH through the ENG-29 frontends to the daemon. *Rationale:* retires the biggest integration risk once its parts exist.
- **ENG-31** — Process-trace test proving no `git` process runs inside the rukie + daemon boundary. *Rationale:* carries the no-runtime-Git rule.
- **ENG-32** — Finish slice 5's production graph index over store segments so revset evaluation runs outside tests. *Rationale:* `read_gap` names it as the `log`/`query` blocker (G2).
- **ENG-33** — R2a part 2: hosted `log`/`query` on the ENG-32 index. *Rationale:* history reads need it (G2).
- **ENG-34** — History and branch-view pages and ops (a branch's changes grouped) on ENG-33. *Rationale:* settled branch-view feature.
- **ENG-35** — R2d: durable ordered op-log tail with a resumable subscription across `op.prune`. *Rationale:* the event spine for CI, webhooks, mirrors and the inbox (F3).
- **ENG-36** — Op-log consumer: one transaction for cursor + outbox, idempotent keys, broadcast for long-wait operations. *Rationale:* no lost events across restarts (F3).
- **ENG-37** — Webhooks: Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker, SSRF guard. *Rationale:* reliable, signed delivery that can't be aimed at internal hosts.
- **ENG-38** — In-app inbox fed by the ENG-36 outbox (no email delivery). *Rationale:* settles the identity/notification decision.
- **ENG-39** — Per-token and per-account token buckets in ENG-15 middleware with `Retry-After`. *Rationale:* many agents must not exhaust the daemon's admission budget.
- **ENG-40** — R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim/attestation cursor paging already exists. *Rationale:* tasks, review and CI depend on these kinds.
- **ENG-41** — Review feature: changes/stacks from `refs/for` with change-ids, branch view, approvals as attestations, merge via `land.execute`. *Rationale:* the settled review unit.
- **ENG-42** — Tasks feature replacing issues, claimable through Ruk leases. *Rationale:* settled feature.
- **ENG-43** — CI core: leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners, results as attestations. *Rationale:* durable, efficient CI that never opens hosted stores (G7, G8).
- **ENG-44** — Reuse `MergeNotice` unchanged and report effective mode + `language_set_version` on every mergeability and land result. *Rationale:* carries DES-01 / CEO-02.
- **ENG-45** — R4: `push` with credentials per call, mirror mode, ref selection, per-ref status on `mirror.rs`. *Rationale:* mirrors need it, and the mapping already exists (G10).
- **ENG-46** — Mirror worker: one run per mirror with dirty-flag rerun, reconcile, credentials only in memory, per-ref ff-only rejections. *Rationale:* collapses ref-update bursts and keeps credentials out of ruk.
- **ENG-47** — R3: bounded `search` method in the Bulk lane with deadline, result cap and cancellation, plus the rukie search op and page. *Rationale:* lightweight search that can't affect interactive traffic.
- **ENG-48** — Releases: tag-backed releases via hosted `tag.*` and streaming archive downloads. *Rationale:* settled feature.
- **ENG-49** — Social: explore, stars, watches and forks (via registry fork). *Rationale:* settled feature.
- **ENG-50** — Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation. *Rationale:* carries the repository half of slice 13.
- **ENG-51** — Retention and maintenance stress under rukie load; maintenance and backups only through daemon methods in the Maintenance lane. *Rationale:* slice 12 plus the single-writer lock (G8).
- **ENG-52** — Kill-9 fault suite (rukie and daemon) mid-push, mid-webhook, mid-CI-attest and mid-mirror; ruk crash/lock/IO smoke tests; shuttle cases. *Rationale:* carried gates that prove §7.
- **ENG-53** — Run the reference capacity profile against rukie + daemon as the release gate; it decides F4. *Rationale:* the carried release gate.

```jsonl
{"id":"ENG-01","description":"Present the reconciliation table as a dated revision proposal for AGENT_NATIVE_GIT_PLAN.md and edit that plan only for user-approved items.","phase":"engineering"}
{"id":"ENG-02","description":"After approval, add an ignore entry for the nested ruk/rukie/ clone and never delete it.","phase":"engineering"}
{"id":"ENG-03","description":"Under the settled Gitea-cut decision confirmed at the gate, make one reversible rukie commit deleting the Gitea tree, setting module github.com/gosuda/rukie, adding a minimal rukie serve, new Makefile, CI and AGENTS.md, and keeping LICENSE.","phase":"engineering"}
{"id":"ENG-04","description":"Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, go test -race and fuzz tests.","phase":"engineering"}
{"id":"ENG-05","description":"R1a: rename the daemon to ruk serve and the handshake profile to host, and reword Forgejo-specific daemon docs and tests.","phase":"engineering"}
{"id":"ENG-06","description":"R1b step 1: add a ruk host-credential subcommand to ruk's own CLI that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids, with named errors and a test asserting owner, group and mode.","phase":"engineering"}
{"id":"ENG-07","description":"R1b step 2: run the daemon as uid A and rukie as uid B with allowed_uids=[B], carry a fresh single-use per-connection challenge in the handshake accept reply, require an HMAC over (challenge, asserted_principal, method) keyed by the ENG-06 credential, refuse a reused challenge and any second assertion on a bound session, bind the principal to the daemon session, attribute sessions, leases, receipts and attestations to it, and test correct, missing and wrong HMACs, replayed opens, reused challenges, second assertions and refusal of non-allowed uids and of the allowed uid without a valid credential, documenting that same-uid-as-rukie processes are out of protocol scope.","phase":"engineering"}
{"id":"ENG-08","description":"R1c: one task per stream with a per-connection frame writer.","phase":"engineering"}
{"id":"ENG-09","description":"R1d: stream input/output in both directions under credit windows and spill received packs to disk.","phase":"engineering"}
{"id":"ENG-10","description":"R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout.","phase":"engineering"}
{"id":"ENG-11","description":"R1f: keep RequestClass pub(crate), add public ExecutorLane mirroring all five RequestClass variants (Control, Interactive, Bulk, Hook, Maintenance) with a Method-to-lane mapping derived from the dispatcher, classify argv CommandOpen streams by verb on the server side with unknown verbs on Bulk and never Hook or Control, reserve BoundedExecutor permits per lane held inside the spawn_blocking closure, and add cancellation, mapping-consistency and verb-table tests.","phase":"engineering"}
{"id":"ENG-12","description":"Keep ruk gates green (fmt, 3-OS clippy -D warnings, nextest, cargo test, Unix cfg gating, no #[allow], unreachable_pub clean, exact pins, --locked) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths.","phase":"engineering"}
{"id":"ENG-13","description":"Two independent processes with two systemd units and two service accounts, a uid-restricted socket and no child-spawn mode.","phase":"engineering"}
{"id":"ENG-14","description":"K3: SQLite WAL store module with forward-only migrations and a leased outbox/job table.","phase":"engineering"}
{"id":"ENG-15","description":"K2: Op[In,Out] registry on Huma v2 and net/http ServeMux deriving OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), the generated rukie CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots.","phase":"engineering"}
{"id":"ENG-16","description":"Auth: argon2id passwords with TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts, and admin-CLI password reset.","phase":"engineering"}
{"id":"ENG-17","description":"Users, orgs, teams and repository permissions enforced in the operation-table authz middleware.","phase":"engineering"}
{"id":"ENG-18","description":"Design tokens as CSS custom properties and base layouts, with each later feature shipping its zero-JS pages from its operations.","phase":"engineering"}
{"id":"ENG-19","description":"R2a part 1: tree and object readers serving hosted diff/show/file/blame, fix the stale daemon doc claiming op.log, claim.list and attest.list serve only first pages, and exclude status.","phase":"engineering"}
{"id":"ENG-20","description":"R2b: hosted preparation for bookmark.*, tag.*, land.execute, push, push.reconcile, fetch, unpin and op.prune.","phase":"engineering"}
{"id":"ENG-21","description":"Crash test proving lane receipts survive a daemon kill at each mutation step and that replay is a no-op.","phase":"engineering"}
{"id":"ENG-22","description":"R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs and compare/merge-base reads.","phase":"engineering"}
{"id":"ENG-23","description":"testruk harness running the real daemon that fails, never skips, when the binary is missing.","phase":"engineering"}
{"id":"ENG-24","description":"K4: pooled daemon client with semaphore, cancellation, schema codegen and a one-to-one RFC 9457/MCP error map, tested on testruk.","phase":"engineering"}
{"id":"ENG-25","description":"RUK_PIN plus a schema-digest drift gate and a handshake version check.","phase":"engineering"}
{"id":"ENG-26","description":"Repo hosting and browsing feature: create/rename/delete ops and tree, blob, commit and diff pages and API.","phase":"engineering"}
{"id":"ENG-27","description":"Typed git-wire methods git.upload_pack (v0+v2), git.receive_pack and git.upload_archive admitted in the Bulk lane.","phase":"engineering"}
{"id":"ENG-28","description":"Keep quarantine and all hook phases (pre-receive, update, post-receive, proc-receive, fail-closed timeouts) unchanged and add typed hosted handlers for each phase.","phase":"engineering"}
{"id":"ENG-29","description":"rukie Smart HTTP and SSH frontends (x/crypto/ssh) that authenticate via ENG-16, authorize via ENG-17 and forward byte streams to the git-wire methods.","phase":"engineering"}
{"id":"ENG-30","description":"Walking-skeleton e2e: stock git clone, fetch v2, push and push refs/for/main over HTTP and SSH through the ENG-29 frontends to the daemon.","phase":"engineering"}
{"id":"ENG-31","description":"Process-trace test proving no git process runs inside the rukie plus daemon boundary.","phase":"engineering"}
{"id":"ENG-32","description":"Finish the slice-5 production graph index over store segments so revset evaluation runs outside tests.","phase":"engineering"}
{"id":"ENG-33","description":"R2a part 2: hosted log/query on the production graph index.","phase":"engineering"}
{"id":"ENG-34","description":"History and branch-view pages and ops grouping a branch's changes.","phase":"engineering"}
{"id":"ENG-35","description":"R2d: durable ordered op-log tail with a resumable subscription across op.prune.","phase":"engineering"}
{"id":"ENG-36","description":"Op-log consumer with one transaction for cursor and outbox, idempotent keys and a broadcast for long-wait operations.","phase":"engineering"}
{"id":"ENG-37","description":"Webhook delivery with Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker and an SSRF guard.","phase":"engineering"}
{"id":"ENG-38","description":"In-app inbox fed by the outbox, with no email delivery.","phase":"engineering"}
{"id":"ENG-39","description":"Per-token and per-account token buckets in the operation-table middleware with Retry-After.","phase":"engineering"}
{"id":"ENG-40","description":"R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim and attestation cursor paging already exists.","phase":"engineering"}
{"id":"ENG-41","description":"Review feature: changes and stacks from refs/for with change-ids, branch view, approvals as attestations and merge via land.execute.","phase":"engineering"}
{"id":"ENG-42","description":"Tasks feature replacing issues, claimable through Ruk leases.","phase":"engineering"}
{"id":"ENG-43","description":"CI core with a leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners and results as attestations.","phase":"engineering"}
{"id":"ENG-44","description":"Reuse MergeNotice unchanged and report effective mode and language_set_version on every mergeability and land result.","phase":"engineering"}
{"id":"ENG-45","description":"R4: push with credentials per call, mirror mode, ref selection and per-ref status on mirror.rs.","phase":"engineering"}
{"id":"ENG-46","description":"Mirror worker with one run per mirror and dirty-flag rerun, reconcile, credentials only in memory and per-ref ff-only rejections.","phase":"engineering"}
{"id":"ENG-47","description":"R3: bounded search method in the Bulk lane with deadline, result cap and cancellation, plus the rukie search op and page.","phase":"engineering"}
{"id":"ENG-48","description":"Releases backed by hosted tag.* with streaming archive downloads.","phase":"engineering"}
{"id":"ENG-49","description":"Social features: explore, stars, watches and forks via registry fork.","phase":"engineering"}
{"id":"ENG-50","description":"Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation.","phase":"engineering"}
{"id":"ENG-51","description":"Retention and maintenance stress under rukie load, with maintenance and backups only through daemon methods in the Maintenance lane.","phase":"engineering"}
{"id":"ENG-52","description":"Kill-9 fault suite for rukie and daemon mid-push, mid-webhook, mid-CI-attest and mid-mirror, plus ruk crash/lock/IO smoke tests and shuttle cases.","phase":"engineering"}
{"id":"ENG-53","description":"Run the reference capacity profile against rukie and the daemon as the release gate, deciding the metadata store.","phase":"engineering"}
```

### Critical Files for Implementation
- `ruk/crates/ruk/src/main.rs` — the CLI that gains the `host-credential` provisioning subcommand (ENG-06) beside `serve`.
- `ruk/crates/ruk/src/daemon/ipc.rs` — the peer-uid check (533-550) and the handshake frames that ENG-07 extends with the single-use challenge.
- `ruk/crates/ruk/src/daemon/executor.rs` — the permit freed on cancel and the shared semaphore (G13, ENG-11).
- `ruk/crates/ruk-core/src/coord/admission.rs` — the private `RequestClass` (lines 33-41) and the dispatcher-only invariant behind the five-lane `ExecutorLane`.
- `ruk/crates/ruk/src/daemon/mod.rs` — the argv `CommandOpen` runner that needs the verb rule, plus buffered input, blocking streams, `InvocationOpen` (304-316), the outcome ledger and the stale read-coverage doc (612-614).

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Remove the nonexistent admin-…

---
## Later corrections from the same agent (apply over the body above)

# autoplan-eng.md: Engineering review of the rukie ground-up rewrite on Ruk (revision 14, reissued)

The plan is buildable, but not in the order the skeleton gives. The ruk daemon can't yet host a forge, so ruk's hosting work has to come before most rukie features. Two things block it today:

1. **Typed forge reads are missing.** Over the daemon, `diff`, `show`, `file`, `blame`, `log` and `query` are refused with `E_HOSTED`.
2. **Large transfers don't stream.** Git-wire traffic is buffered whole in memory, and one long command stalls every other stream on its connection.

So the first job is not "add forge read methods" (R2). It is to fix how the daemon hosts: build the typed reads, stream transfers, keep bulk work from taking all the threads, and accept per-user identity from the forge.

**The admin-command advisory was already fixed in revision 14.** The provisioning command is no longer a phantom rukie-side admin operation: **ENG-06 adds `ruk host-credential` to ruk's own CLI** (`crates/ruk/src/main.rs`, beside `serve`) as R1b step 1, before the handshake change (ENG-07). It depends on nothing built later — not on ENG-14 (rukie's K2 CLI), not on rukie at all — so S1 has no circular dependency. No content changed; this is reissued in full because this final message is what gets saved.

**This file has not been written to disk.** This session is read-only plan mode with no write tool. The orchestrator needs to save this message as `autoplan-eng.md` and create `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` from the JSONL block in §12.

---

## 0. Restated problem

Choose the build order, the process layout and the reliability guarantees for two repositories that change together:
- **ruk:** Rust; workspace gates fmt / clippy / nextest / test.
- **rukie:** Go; today a 393k-LOC Gitea tree that becomes an agent-first forge using only the Ruk daemon as its repository backend.

Constraints:
- every commit builds in both repos;
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
- tests must run against a real daemon.

**Where the labels come from:**
- R1–R6, K1–K4, F, U, V: the draft plan outline in this assignment's context (not stored in any repo file).
- S0–S7: stages in §5.
- G1–G17: findings in §1.
- F1–F7: design forks in §3.

## 0a. Authorization boundary

- **This review authorizes nothing.** autoplan ends at a human approval gate. Every task starts only after that approval.
- **Deleting the Gitea tree (ENG-03)** rests on this assignment's settled decision: "Cut every Gitea surface (API v1, swagger, app.ini, xorm schema, Actions, packages, OAuth2 provider, Gitea webhook formats, builtin SSH code, gitea CLI, Vue/Vite/Tailwind, locales) … Land as commits on top of Gitea history; every commit builds." That text lives only in the assignment context, not in any workspace file; the user confirms it at the gate. It covers rukie only, and it is a reversible commit with history kept.
- **The existing ruk plan:** `ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:119-121` says no source cutover, data migration or source-copy purge is authorized by it, and forbids silent feature removal or storage substitution. So: changing that plan (ENG-01) is a proposal decided item by item; the nested `ruk/rukie/` clone is never deleted, and ENG-02 only proposes an ignore entry; ruk's redb store is untouched. The metadata store (F4) belongs to rukie.
- **No outward acts** (push, publish, name reservation) are proposed.

## 1. What the code shows today

| # | Finding | Evidence | Consequence |
|---|---|---|---|
| G1 | The daemon is wired as `Command::Forgejo { command: ForgejoCommand::Serve }`; the handshake profile is hard-coded `"forgejo"`. | `ruk/crates/ruk/src/main.rs:202-220`; `daemon/ipc.rs:643`; `daemon/tests.rs:322` | R1 is a rename plus a protocol change (ENG-05). |
| G2 | Typed hosted reads serve `op.log`, `claim.list`, `attest.list` with lease-bound cursor continuation (`read()` mints a 30-second cursor when a first page does not exhaust the listing and resumes it after binding checks). Every other read names its missing piece in `read_gap`: `status` needs a workspace autosnapshot and workcopy crawl; `log`/`query` need "a production graph index over store segments" because revset evaluation "builds its graph only in tests"; `diff`/`show`/`file`/`blame` need "tree and object readers over resolved revisions". | `ruk-core/src/coord/hosted.rs:539-560, 612-632, 1347-1362` | A forge doesn't need `status`. `diff`/`show`/`file`/`blame` need tree and object readers (ENG-19); `log`/`query` need the graph index (ENG-32, ENG-33). `daemon/mod.rs:612-614` still says "first pages are served", contradicting the coordinator; ENG-19 fixes that stale doc. The argv `CommandOpen` path returns untyped text and blocks other streams (G5), so browsing waits for ENG-19 instead of shipping on it. |
| G3 | Hosted mutations covered: ref, new, claim, ws.new, pin, attest, describe, split, rebase, squash, abandon, edit, ws.scope, ws.take, ws.drop, resolve, revert, undo, land.prepare. Not covered: `land.execute`, `push`, `push.reconcile`, `fetch`, `bookmark.*`, `tag.*`, `snapshot`, `write`, `op.prune`, `unpin`. | `daemon/mod.rs:627` | Merges, mirrors and releases are blocked on these (ENG-20). |
| G4 | Identity is derived from the peer uid alone (`Principal::from_peer_uid`, `ipc.rs:533-550` accepts every process with an allowed UID), and `InvocationOpen` (`daemon/mod.rs:304-316`) carries no assertion field, nonce or sequence; `session_id` and the mutation `request_id` are optional and reusable. | `daemon/mod.rs:2017,304-316`; `daemon/registry.rs:279`; `hosted.rs:590-596` | rukie's one uid would make every user and agent a single principal, any same-uid process could impersonate all of them, and a captured open frame could be replayed. The daemon needs an authenticated, replay-resistant host assertion under distinct service identities (ENG-06, ENG-07). |
| G5 | A long-running command blocks other streams on the same connection (documented v1 limitation); clone, fetch and archive arrive as argv `CommandOpen` streams. | `daemon/mod.rs` `command_inner` doc; `ruk-git/src/transport/mirror.rs:12` | Connections become the bottleneck (`DEFAULT_MAX_CONNECTIONS = 128`); the argv path has no `Method` to classify by (ENG-11). |
| G6 | Command input is buffered in full (up to 256 MiB) before running; output is captured, then streamed; pushed packs are staged into memory. | `daemon/mod.rs` consts / docs; `ruk-git/src/transport/hooks.rs:1-12` | RSS grows with pack size × concurrency; transfers must stream and spill to disk (ENG-09). |
| G7 | The reconnect outcome ledger is in memory (10,000 entries); durable receipts live in the lane. | `daemon/mod.rs`; `ruk/docs/coordinator.md` | rukie must persist request ids; whether receipts are durable on disk is **inference** from coordinator.md, tested by ENG-21. |
| G8 | Each repo's database (redb) allows one writer process; a second gets `DatabaseAlreadyOpen`. | `AGENT_NATIVE_GIT_PLAN.md:152` | The daemon is the only process that opens hosted stores. |
| G9 | Receive pipeline: quarantine, `pre-receive`/`update`/`post-receive`, fail-closed timeouts, and `proc-receive` diversion using git's exact conversation. | `hooks.rs:1-47`; `proc_receive.rs:1-44` | Git semantics that stay in ruk; rukie adds typed handlers per phase (ENG-28). |
| G10 | The `push --mirror` mapping and refspec pruning already exist. | `ruk-git/src/transport/mirror.rs:1-40` | R4 extends existing code (ENG-45). |
| G11 | Daemon limits: 64 KiB data frame, 1 MiB control frame, 256 KiB starting / 16 MiB maximum window, 16 blocking permits, 60 s per-frame timeout. | `daemon/ipc.rs:20-32`; `daemon/mod.rs:24-37` | Per-stream memory has a known upper bound; subscriptions need keepalives (ENG-10). |
| G12 | The admission classes exist and are private to ruk-core: `pub(crate) enum RequestClass { Control, Interactive, Bulk, Hook, Maintenance }`, documented "The dispatcher selects it; a client never does"; the scheduler has separate limits per class; Bulk leases cancel after 30 s without progress and are capped at 2 h. | `ruk-core/src/coord/admission.rs:33-41` (read directly); `admission/scheduler.rs:30-35,222-226`; `leases/timing.rs:15-22` | Don't add a class. The ruk-core dispatcher maps typed methods to classes; the daemon crate can't name the class, so it needs a public `ExecutorLane` mirroring all five variants plus its own verb rule for argv streams (ENG-11). |
| G13 | The executor has two defects: one shared semaphore for every class, so per-class admission is undone at the thread pool; and `run` acquires the permit outside `spawn_blocking` and drops it only after awaiting the join handle, so a cancelled caller releases the permit while `work` keeps running. | `ruk/crates/ruk/src/daemon/executor.rs` (`run`) | Blocking concurrency and memory can exceed their bound under client cancellations; fixed and tested in ENG-11. |
| G14 | ruk CI: fmt; clippy `--workspace --all-targets --all-features --locked -D warnings` on 3 OSes; nextest 0.9.148 on 3 OSes; `cargo test` on 3 OSes; toolchain 1.99.1. Miri, fuzz, soak and store gates are deferred. Lints deny `allow_attributes`, `unsafe_code`, `todo`, `unimplemented`, `dbg_macro`; `unreachable_pub` warns; exact `=` pins with `--locked`. The existing plan's Miri gate: `cargo +nightly miri test -p ruk-core store::segment`. | `ruk/.github/workflows/ci.yml`; `ruk/Cargo.toml`; `ruk/rust-toolchain.toml`; `AGENT_NATIVE_GIT_PLAN.md` "Verification gates" | Unix-only daemon code must pass Windows clippy without `#[allow]`; a new public ruk-core type needs a real outside user or `unreachable_pub` plus `-D warnings` fails CI; the deferred gates are still owed (ENG-12). |
| G15 | `ruk/rukie/` is a clone of `https://github.com/gosuda/rukie` inside the ruk tree, and ruk's `.gitignore` does not exclude it. | `ruk/rukie/.git/config`; `ruk/.gitignore` | Risk of committing a gitlink; never delete it. |
| G16 | `ruk/tasks/autoplan-ledger.jsonl` holds `ENG-01..06` from the earlier autoplan run; `ruk/.gitignore` ignores `tasks/`; there is no `tasks/` directory at the workspace root. | that file; `ruk/.gitignore`; workspace root listing | This run's ledger is `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`: a new file, no id collision. `ruk/tasks/autoplan-ledger.jsonl` is left alone. |
| G17 | `AGENT_NATIVE_GIT_PLAN.md` targets Forgejo parity, forbids runtime Git, gates release on a capacity profile, and requires explicit revision for any removal. | `AGENT_NATIVE_GIT_PLAN.md:5-14,71-99,115-121` | §2 is a revision proposal, not a supersession. |

## 2. Proposed reconciliation with `AGENT_NATIVE_GIT_PLAN.md` (decided at the gate)

| Existing plan item | Proposed verdict | Carried form |
|---|---|---|
| Slices 1–8 | Carry unchanged | Slice 5's production graph index becomes a scheduled task (ENG-32) because hosted `log`/`query` need it. |
| Slice 9 | Carry; backend half adapted | ENG-19, ENG-20, ENG-22, ENG-33. |
| Slice 10 | Adapt; dropping the Forgejo Go patch needs approval | `ruk serve` plus the rukie client (ENG-05..07, ENG-11, ENG-24). |
| Slice 11 | Carry in ruk; forge side adapted | Hook phases stay (ENG-28); mirrors become ENG-45; dropping wiki/template flows needs approval. |
| Slice 12 | Carry | ENG-51. |
| Slice 13 | Repository half carried; Forgejo-database half pending approval | ENG-50. |
| Slice 14 + release gate | Carry | ENG-53. |
| No runtime Git | Carry | ENG-31. |
| CEO-01 frozen Forgejo inventory | Proposed superseded, approval needed | Replaced by the ENG-25 drift gate. |
| DES-01 one `MergeNotice` record; CEO-02 effective mode + `language_set_version` | Carry | ENG-44. |
| Verification gates: Miri, fuzz, shuttle, crash smoke tests, out-of-process harness | Carry | ENG-12 (fuzz + Miri), ENG-23, ENG-52. |

Every item stays in force until the user approves a change.

## 3. Hypotheses on the major forks

### F1 — Process layout
- **H1a (recommended):** two independent processes over a Unix socket under two dedicated service accounts (F7): the daemon as uid A, rukie as uid B, `allowed_uids = [B]`. rukie terminates HTTP and SSH and forwards byte streams to typed git-wire methods. *Disproved if* deploys must be a single binary *and* rukie restarts never need to keep transfers alive.
- **H1b:** rukie spawns the daemon as a child. Rejected: deploys kill in-flight transfers, and it means two runtime paths.

### F2 — Push policy and refs/for
- **H2a (recommended):** the receive pipeline stays; hosted mode registers typed handlers: ref policy at `pre-receive`/`update`, change-id change creation at `proc-receive`, a wake-up hint at `post-receive`. Script hooks remain the CLI and self-hosted default. *Disproved if* the handlers can't see quarantined objects through the view scripts get.
- **H2b:** delete the hook phases. Rejected: breaks slice 11 parity.

### F3 — How ref updates reach CI, webhooks and mirrors durably
- **H3a (recommended):** a stored op-log position plus outbox rows, advanced in one transaction; `post-receive` is only a wake-up hint. *Disproved if* `op.log` can't be paged in order from a durable position across `op.prune`.
- **H3b:** a push-time callback inserts the jobs. Rejected: events are lost while rukie is down.

### F4 — rukie metadata store (open)
- **H4a (recommended):** SQLite WAL, pure Go, behind a SQL-shaped store module. *Disproved by* the capacity gate (ENG-53).
- **H4b (runner-up):** PostgreSQL, which the existing plan uses for capacity acceptance (`:117`).
- **H4c:** Pebble. Rejected: no SQL.

### F5 — Deleting the Gitea tree
- **H5a (recommended):** one reversible commit.
- **H5b:** staged deletion. Rejected: every intermediate commit is a gutted Gitea that still has to build, with no value.

### F7 — Host principal assertion
- **Deployment:** distinct identities. The daemon runs as uid A; rukie runs as uid B; `allowed_uids = [B]`. Credential provisioning is a **ruk-side CLI subcommand** (`ruk host-credential`, added in ENG-06 before any handshake work, depending on nothing later), writing the random secret as a 0640 file owned by A in a dedicated group whose only members are A and B. Rukie fails at boot with a named error when it cannot read it; the daemon fails at startup when it cannot. A same-account 0600 file is rejected because any same-uid process reads it.
- **Guarantee, stated honestly:** the uid gate plus the credential prevents impersonation by any process outside {A, B}. It does not separate a process running as rukie's uid (B): that process is inside rukie's trust boundary and can ptrace rukie, read its memory and steal sessions and tokens regardless of this layer. User-attribution separation within one uid is not attainable in userspace; the deployment requirement is that only rukie runs as B. A pid → executable check over `SO_PEERCRED` remains optional defense-in-depth, noted as weak.
- **H7a (recommended):** identity additionally carries a proof with daemon-issued freshness:
  1. the handshake accept reply carries a fresh, cryptographically random, single-use per-connection challenge;
  2. a principal assertion is an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the host credential;
  3. the daemon records consumed challenges for the life of the connection and refuses any reuse; it also refuses any second assertion on a session that is already bound;
  4. the asserted principal is bound to the daemon-issued session, so later frames on that session never re-assert.

  Consequences: the same assertion bytes are never accepted twice; a captured open frame replayed on another connection fails (different challenge); replaying frames on the same connection after binding cannot re-assert identity; invocation-level replay of bound-session frames remains the intended request-id idempotency path. Sessions, leases, receipts and attestations attribute to the bound principal.
- **H7b:** accept bare assertions from allowed uids. Rejected: any allowed-uid process could impersonate every user and forge the audit trail.
- **H7c:** HMAC over a client-chosen nonce only. Rejected: the client can replay its own nonce.
- **H7d:** same service account with a 0600 file. Rejected: no peer separation, and a daemon-created 0600 file is unreadable by a differently-owned rukie.

### F6 — Executor isolation across the crate boundary
- **H6a (recommended):** `RequestClass` stays `pub(crate)`; ruk-core exposes a public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) plus a `Method` → lane mapping derived from the dispatcher's class choice; the daemon classifies argv `CommandOpen` streams by verb on the server side (table below), never emitting Hook or Control; `BoundedExecutor` reserves permits per lane and holds each permit inside the `spawn_blocking` closure. *Disproved if* all blocking work could move into ruk-core's admitted path; the argv runner and git-wire serving live in the daemon crate, so it can't.
- **H6b:** make `RequestClass` public. Rejected: it would let clients choose their own class, against `admission.rs:33`.
- **H6c:** one executor per class. Rejected: idle threads, against the executor's design (`executor.rs` header).
- **H6d:** a three-variant `ExecutorLane`. Rejected: hook work would land on Bulk permits and exceed that class's limit.

Verb rule for argv streams:

| Verbs | Lane |
|---|---|
| `upload-pack`, `receive-pack`, `upload-archive`, `archive`, `fetch`, `push`, `pack-objects`, `index-pack`, `grep`, `blame` | Bulk |
| `gc`, `repack`, `prune`, `maintenance`, `fsck` | Maintenance |
| Any other listed verb | Interactive |
| Unknown verb | Bulk |
| *(never produced by argv)* | Hook, Control |

## 4. Six-principle register (engineering)

| Principle | Decision | Open question |
|---|---|---|
| **Clarity** | One operation table (ENG-15) derives HTML, JSON, MCP and CLI. One wire contract (schema codegen + digest gate). One git-wire method family. One event spine. The daemon is the only process opening stores. One receive pipeline with script or typed handlers. Admission classes chosen server-side only; the executor sees all five lanes. Host identity = distinct service uids + a ruk-CLI-provisioned group-restricted credential + an HMAC'd assertion over a daemon-issued single-use challenge, with the guarantee scoped to outside-{A,B} processes (F7). Plan changes are proposals. | — |
| **Impact** | Fix streaming, per-stream concurrency and executor isolation (G5, G6, G13) first. Tree/object readers unlock file, commit and diff pages. The graph index (ENG-32) is the long pole for history and branch views. | — |
| **Audience** | Typed errors map one-to-one to RFC 9457 / MCP with `Retry-After`. `MergeNotice` and effective mode on every merge result. Zero-JS HTML from the same operations. Agents use scoped tokens on human accounts (ENG-16). | Runner credential class. |
| **Risk** | Pack memory; principal impersonation by outside processes (F7, ENG-07), with same-uid processes named as out of scope; lost events; the single-writer store lock; a cancelled request freeing its permit while its work keeps running, and argv transfers bypassing lanes (G13, ENG-11); leaking `RequestClass` to clients; SSRF; cross-repo drift; runtime Git; unauthorized plan or destructive changes (§0a). | Lane receipt durability (ENG-21). |
| **Sequencing** | Tasks are numbered in stage order (§5); each depends only on earlier tasks. Provisioning (ENG-06) precedes the handshake (ENG-07), both in ruk's CLI, so S1 has no circular dependency on rukie. Store → operation table → auth → users/orgs before any authenticated feature. Git wire → hooks → frontends → walking skeleton. Graph index → `log`/`query` → history pages → review. Capacity is the final release gate. | — |
| **Reversibility** | `protocol_major` bump plus `RUK_PIN`; forward-only migrations with backup restore; Gitea deletion revertible; PostgreSQL fallback; script hooks can be re-enabled per repo; `ExecutorLane` and the verb table are small and easy to change before release; the host credential can be rotated by regenerating the file. | — |

## 5. Amended dependency order

### 5.1 Which ruk capability blocks which rukie feature

| rukie feature (task) | Blocking ruk capability (task) | Today |
|---|---|---|
| Auth, users, orgs, teams, tokens (ENG-16, ENG-17) | none | — |
| Any repo-touching feature | `ruk serve`, credential provisioning, authenticated host assertion, handshake check (ENG-05, ENG-06, ENG-07, ENG-25) | missing |
| Repo hosting and browsing (ENG-26) | Tree/object readers + tree / refs / compare + lifecycle (ENG-19, ENG-22) | E_HOSTED |
| History, branch view (ENG-34) | Graph index → `log`/`query` (ENG-32, ENG-33) | E_HOSTED; graph built only in tests |
| HTTP / SSH clone & push (ENG-29) | Per-stream tasks, streaming, spill, lanes, git-wire methods, hook handlers (ENG-08, ENG-09, ENG-11, ENG-27, ENG-28) | buffered; permit freed early |
| Webhooks, inbox (ENG-37, ENG-38) | Op-log tail + keepalive (ENG-10, ENG-35) | 60 s frame timeout kills subscriptions |
| Review (ENG-41) | Hook handlers (ENG-28), hosted `land.execute` (ENG-20), R6 extensions (ENG-40) | script-only / `land.execute` uncovered |
| Tasks (ENG-42) | R6 claim extensions (ENG-40); `claim.*` and its cursor paging already exist | lease semantics missing |
| CI (ENG-43) | R6 attestation kinds (ENG-40); `attest.add` and cursor paging already exist | kinds missing |
| Mirrors (ENG-46) | Hosted `push` / `push.reconcile` (ENG-20) + R4 (ENG-45) | uncovered |
| Search (ENG-47) | Bounded `search` in the Bulk lane (ENG-47) | missing |
| Releases (ENG-48) | Hosted `tag.*` (ENG-20) + streaming archive (ENG-27) | uncovered |
| Social and forks (ENG-49) | Registry fork (ENG-22) | missing |
| Repo import (ENG-50) | Offline Git conversion | CLI only |

### 5.2 Stages (each stage = commits that each build; all start after the gate)

| Stage | Tasks | Exit proof |
|---|---|---|
| S0 | ENG-01..04: gate items, K1, Go gates | both CIs green |
| S1 | ENG-05..18: R1 daemon work (rename, provisioning, assertion, per-stream, streaming, keepalive, lanes, ruk gates), topology, K3 store, K2 operation table, auth, users/orgs/teams, UI tokens | soak test + fuzz + Miri; executor and verb-lane tests; provisioning and assertion tests; login and token flows on all four surfaces |
| S2 | ENG-19..26: tree/object reads, mutations, receipts, lifecycle, `testruk`, K4 client, drift gate, repo browsing | real-daemon integration tests; browse a repo end to end |
| S3 | ENG-27..31: git wire → hook handlers → frontends → walking skeleton → no-Git trace | **walking skeleton:** stock git clone / push / `push refs/for/main` through rukie |
| S4 | ENG-32..38: graph index, `log`/`query`, history pages, op-log tail, consumer, webhooks, inbox | kill-restart test: no lost or duplicate deliveries |
| S5 | ENG-39..44: token buckets, R6, review, tasks, CI, MergeNotice | end-to-end agent flow (claim → push → CI attest → land) |
| S6 | ENG-45..51: R4, mirror worker, search, releases, social, import, retention stress | §8 targets |
| S7 | ENG-52..53: fault suite, capacity profile | release gate |

## 6. Concurrency and backpressure
- **Daemon:** one task per stream and one writer per connection, bounded by credit windows (`ipc.rs:29-32`).
- **Admission and executor (ENG-11):** typed methods use the dispatcher's class and the derived five-variant `ExecutorLane`; argv streams use the verb rule with unknown verbs on Bulk and never Hook or Control; permits are reserved per lane and held inside the `spawn_blocking` closure.
- **Identity:** distinct service accounts (A = daemon, B = rukie), `allowed_uids = [B]`, group-restricted credential provisioned by `ruk host-credential` (ENG-06), and the HMAC'd host assertion over a daemon-issued single-use challenge (ENG-07, F7); the guarantee covers processes outside {A, B}.
- **rukie client:** a pool of about 4–8 connections with an invocation semaphore below the daemon's limits; cancelling a request's context cancels its stream; `E_LIMIT` maps to 429/503 with `Retry-After`; only idempotent reads are retried automatically.
- **Per-token limits:** token buckets per token and per account in the operation-table middleware (ENG-39).
- **Ref races:** the lane's compare-and-swap (`E_STALE`) is sent back as a typed conflict; no rukie-side locks.
- **Long waits for agents:** served from an in-process broadcast fed by the event consumer, not by polling the daemon; HTML stays zero-JS.

## 7. Durability of CI, webhook and mirror queues
…
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.

[Showing lines 7358-7690 of 8940 (49.9KB limit). Use :7691 to continue]
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
…
## 7. Durability of CI, webhook and mirror queues
1. **One spine:** one transaction reads ops from the stored op-log position, inserts outbox rows unique on `(repo, op_id, target)`, and advances the cursor; delivery is at-least-once with handlers safe to run twice.
2. **Job claiming:** rows carry leases and attempt counts; claims are atomic `UPDATE … RETURNING`; retries back off with jitter; failed jobs end in a dead-letter state.
3. **Webhooks:** Standard Webhooks signatures; `webhook-id` = outbox key; circuit breaker per endpoint; resolve-and-pin SSRF guard.
4. **Mirrors:** one run at a time with a dirty-flag rerun; periodic reconcile; credentials only in memory; per-ref fast-forward-only rejections.
5. **CI:** the job DAG is stored in metadata with leases and heartbeats; cache key = hash(inputs + spec + image digest); logs are written in chunks to files; the request id is persisted before `attest.add`; runners fetch over the git wire only.

## 8. Performance budgets

**Release gate:** the reference capacity profile (`AGENT_NATIVE_GIT_PLAN.md:115-121`), run by ENG-53.

| Path | Interim target (until measured) |
|---|---|
| Small daemon read over the socket | p99 ≤ 2 ms |
| Metadata-only API operation | p95 ≤ 20 ms |
| Tree / blob / commit page | p95 ≤ 50 ms |
| HTML page weight | ≤ 50 KB gzip, 0 B JS |
| Clone throughput | ≥ 80% of `git http-backend` |
| Push / clone memory | RSS growth independent of pack size beyond about 16 MiB per stream, including under cancel storms (ENG-11) |
| Ref update → webhook delivered | p95 ≤ 2 s |
| Ref update → mirror started | p95 ≤ 5 s |
| Idle CI queue → job started | ≤ 1 s |
| Search | deadline 2 s, capped results, cancellable |
| Bulk saturation | Interactive p95 unchanged while the Bulk lane is at its limit |

## 9. Testing strategy
- **Real daemon, no mocks of ruk:** `testruk` (ENG-23) starts `ruk serve` from `RUK_BIN` and fails, never skips, when it is missing; `make test` builds ruk at `RUK_PIN`.
- **Drift check (ENG-25):** schema regeneration and digest check plus a handshake version check.
- **Stock git and OpenSSH as clients** outside the boundary (ENG-30, after the ENG-29 frontends), plus a trace proving no `git` runs inside it (ENG-31).
- **Provisioning and assertion tests (ENG-06, ENG-07):** the subcommand creates the file with the exact owner, group, and mode; boot fails with a named error when the credential is missing; correct HMAC accepted; missing and wrong HMAC refused; the same assertion bytes replayed on the same and on a fresh connection refused (consumed challenge / challenge mismatch); a second assertion on a bound session refused; a peer with a non-allowed uid refused by the gate; a peer with the allowed uid but no valid credential refused.
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.
- **Operation-table tests (ENG-15):** each registered op is reachable and consistent through HTML form, JSON, MCP and CLI from one definition.
- **Fault and concurrency tests:** kill-9 suite, ruk crash/lock/IO smoke tests, shuttle (ENG-52); 60 s fuzz of frame, pkt-line and git-wire decoders plus Miri (ENG-12).
- **Go gates (ENG-04) and ruk gates (ENG-12):** gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz; fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`.

## 10. Repo migration and module path (after the gate)
- **K1 (ENG-03) is one reversible commit** on top of Gitea history: remove the Gitea tree; add `module github.com/gosuda/rukie` (origin `https://github.com/gosuda/rukie`), Go 1.27 / go1.27.2, a fresh `go.sum`, a minimal `rukie serve`, a new Makefile, new CI and a new AGENTS.md.
- **License:** keep the MIT `LICENSE` with Gitea's notice (whether a rewritten tree still needs it is **inference**).
- **No `gitea.dev` aliases.**

## 11. Open questions for the approval gate
1. **rukie metadata store:** SQLite-first with PostgreSQL fallback (recommended) vs PostgreSQL from the start.
2. **CI runner credentials** under "no agent principals".
3. **Host reads without sessions** vs per-principal session pooling.
4. **User script hooks in hosted repos,** or typed handlers only.
5. **Approve item by item** the §2 rows marked "needs approval": the Forgejo patch, wiki/template flows, the Forgejo-database half of migration, CEO-01.
6. **Approve** the ENG-02 ignore entry for `ruk/rukie/`.
7. **Confirm the settled Gitea-cut decision** quoted in §0a. It exists only in the assignment context.
8. **Accept the F7 boundary:** the assertion scheme protects against processes outside {A, B}; same-uid-as-rukie protection is a deployment requirement (only rukie runs as B), not a protocol guarantee.
9. **Ledger target:** `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` (new file, no id collision). `ruk/tasks/autoplan-ledger.jsonl` is not touched.

## 12. ENG tasks (stage order; one line each: id, description, rationale; all start after the gate)

- **ENG-01** — Present the §2 reconciliation as a dated revision proposal for `AGENT_NATIVE_GIT_PLAN.md` and edit that plan only for approved items. *Rationale:* G17 and §0a require an explicit revision rather than a silent supersession.
- **ENG-02** — After approval, add an ignore entry for the nested `ruk/rukie/` clone; never delete it. *Rationale:* avoids committing a gitlink (G15) without purging anything.
- **ENG-03** — K1 under the settled Gitea-cut decision confirmed at the gate: one reversible rukie commit deleting the Gitea tree, module `github.com/gosuda/rukie`, minimal `rukie serve`, new Makefile, CI and AGENTS.md, LICENSE kept. *Rationale:* a staged deletion has no value, and the module path and agent rules must change together (F5).
- **ENG-04** — Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz tests. *Rationale:* every later rukie commit is gated from the start.
- **ENG-05** — R1a: rename to `ruk serve` and profile `host`, and reword Forgejo-specific daemon docs and tests. *Rationale:* the command and profile are Forgejo-named today (G1).
- **ENG-06** — R1b step 1: add a `ruk host-credential` subcommand to ruk's own CLI (`crates/ruk/src/main.rs`, beside `serve`) that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids (paths configurable), with named errors on failure and a test asserting owner, group and mode. *Rationale:* provisioning is ruk-side infrastructure the handshake (ENG-07) consumes; placing it here avoids a circular dependency on rukie's later CLI layer (F7).
- **ENG-07** — R1b step 2: run the daemon as uid A and rukie as uid B with `allowed_uids = [B]`; carry a fresh single-use per-connection challenge in the handshake accept reply; require an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the ENG-06 credential; refuse a reused challenge and any second assertion on a bound session; bind the principal to the daemon session and attribute sessions, leases, receipts and attestations to it; test correct/missing/wrong HMACs, replayed opens on the same and fresh connections, reused challenges, second assertions, refusal of non-allowed uids and of the allowed uid without a valid credential. Document that same-uid-as-rukie processes are out of protocol scope. *Rationale:* a bare uid assertion permits impersonation and a client-chosen nonce permits replay; the guarantee must match what the kernel enforces (G4, F7).
- **ENG-08** — R1c: one task per stream with a per-connection frame writer. *Rationale:* removes the documented stream blocking (G5).
- **ENG-09** — R1d: stream input/output in both directions under credit windows and spill received packs to disk. *Rationale:* buffered packs make RSS scale with pack size (G6).
- **ENG-10** — R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout. *Rationale:* subscriptions would otherwise be timed out (G11).
- **ENG-11** — R1f, in one change: keep `RequestClass` `pub(crate)`; add public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) with a `Method` → lane mapping derived from the dispatcher; classify argv `CommandOpen` streams by verb server-side (transfer/grep/blame → Bulk, gc/repack/prune/maintenance/fsck → Maintenance, unknown → Bulk, never Hook or Control); reserve `BoundedExecutor` permits per lane with each permit held inside the `spawn_blocking` closure; add cancellation, mapping-consistency and verb-table tests. *Rationale:* covers typed methods and argv transfers, keeps hook limits intact, and stops early permit release (G5, G12, G13, F6).
- **ENG-12** — Keep ruk gates green (fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths. *Rationale:* G14 lists owed gates that no other task carries.
- **ENG-13** — Two independent processes with two systemd units and two service accounts, uid-restricted socket, no child-spawn mode. *Rationale:* transfers survive rukie deploys, and the split accounts give the F7 boundary.
- **ENG-14** — K3: SQLite WAL store module, forward-only migrations, leased outbox/job table. *Rationale:* transactional cursor plus outbox needs SQL, and PostgreSQL stays possible (F4).
- **ENG-15** — K2: the `Op[In,Out]` registry on Huma v2 + net/http ServeMux, producing OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), generated `rukie <op> --json` CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots. *Rationale:* every feature, frontend and limit sits on it.
- **ENG-16** — Auth: argon2id passwords + TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts (no agent principals), admin-CLI password reset. *Rationale:* required by every authenticated op and the git frontends.
- **ENG-17** — Users, orgs, teams and repository permissions, enforced in the ENG-15 authz middleware. *Rationale:* authorization for ops and git access.
- **ENG-18** — Design tokens as CSS custom properties and base layouts; each later feature ships its zero-JS pages from its operations. *Rationale:* avoids a big-bang UI phase.
- **ENG-19** — R2a part 1: tree and object readers serving hosted `diff`/`show`/`file`/`blame`; fix the stale `daemon/mod.rs:612-614` doc that says `op.log`/`claim.list`/`attest.list` serve only first pages; exclude `status`. *Rationale:* the real read gap in `read_gap` (hosted.rs:1347-1362); a forge has no workcopy (G2).
- **ENG-20** — R2b: hosted preparation for `bookmark.*`, `tag.*`, `land.execute`, `push`, `push.reconcile`, `fetch`, `unpin`, `op.prune`. *Rationale:* merges, mirrors and releases depend on them (G3).
- **ENG-21** — Crash test proving lane receipts survive a daemon kill at each mutation step and replay is a no-op. *Rationale:* safe retries rest on durable receipts (G7).
- **ENG-22** — R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs, compare/merge-base. *Rationale:* hosting, browsing and forks.
- **ENG-23** — `testruk` harness running the real daemon that fails, never skips, when the binary is missing. *Rationale:* integration tests need a real daemon without false green.
- **ENG-24** — K4: pooled client, semaphore, cancellation, schema codegen, one-to-one RFC 9457 / MCP error map, tested on `testruk`. *Rationale:* backpressure and typed errors for agents.
- **ENG-25** — `RUK_PIN` + schema-digest drift gate + handshake version check. *Rationale:* keeps both repos building together across breaking ruk changes.
- **ENG-26** — Repo hosting and browsing feature: create/rename/delete ops, tree, blob, commit and diff pages and API, on ENG-19/22/24. *Rationale:* first repo-facing feature, proving the operation table end to end.
- **ENG-27** — Typed git-wire methods `git.upload_pack` (v0+v2), `git.receive_pack`, `git.upload_archive`, admitted in the Bulk lane. *Rationale:* one streaming contract for HTTP and SSH (F1).
- **ENG-28** — Keep quarantine and all hook phases (`pre-receive`, `update`, `post-receive`, `proc-receive`, fail-closed timeouts) unchanged; add typed hosted handlers for each phase. *Rationale:* preserves slice 11 parity and git semantics while removing script dispatch for the forge (G9, F2).
- **ENG-29** — rukie Smart HTTP + SSH frontends (`x/crypto/ssh`) that authenticate via ENG-16, authorize via ENG-17, and forward byte streams to the git-wire methods. *Rationale:* authentication stays in rukie, pack logic in ruk (F1).
- **ENG-30** — Walking-skeleton e2e: stock git clone, fetch v2, push and `push refs/for/main` over HTTP and SSH through the ENG-29 frontends to the daemon. *Rationale:* retires the biggest integration risk once its parts exist.
- **ENG-31** — Process-trace test proving no `git` process runs inside the rukie + daemon boundary. *Rationale:* carries the no-runtime-Git rule.
- **ENG-32** — Finish slice 5's production graph index over store segments so revset evaluation runs outside tests. *Rationale:* `read_gap` names it as the `log`/`query` blocker (G2).
- **ENG-33** — R2a part 2: hosted `log`/`query` on the ENG-32 index. *Rationale:* history reads need it (G2).
- **ENG-34** — History and branch-view pages and ops (a branch's changes grouped) on ENG-33. *Rationale:* settled branch-view feature.
- **ENG-35** — R2d: durable ordered op-log tail with a resumable subscription across `op.prune`. *Rationale:* the event spine for CI, webhooks, mirrors and the inbox (F3).
- **ENG-36** — Op-log consumer: one transaction for cursor + outbox, idempotent keys, broadcast for long-wait operations. *Rationale:* no lost events across restarts (F3).
- **ENG-37** — Webhooks: Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker, SSRF guard. *Rationale:* reliable, signed delivery that can't be aimed at internal hosts.
- **ENG-38** — In-app inbox fed by the ENG-36 outbox (no email delivery). *Rationale:* settles the identity/notification decision.
- **ENG-39** — Per-token and per-account token buckets in ENG-15 middleware with `Retry-After`. *Rationale:* many agents must not exhaust the daemon's admission budget.
- **ENG-40** — R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim/attestation cursor paging already exists. *Rationale:* tasks, review and CI depend on these kinds.
- **ENG-41** — Review feature: changes/stacks from `refs/for` with change-ids, branch view, approvals as attestations, merge via `land.execute`. *Rationale:* the settled review unit.
- **ENG-42** — Tasks feature replacing issues, claimable through Ruk leases. *Rationale:* settled feature.
- **ENG-43** — CI core: leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners, results as attestations. *Rationale:* durable, efficient CI that never opens hosted stores (G7, G8).
- **ENG-44** — Reuse `MergeNotice` unchanged and report effective mode + `language_set_version` on every mergeability and land result. *Rationale:* carries DES-01 / CEO-02.
- **ENG-45** — R4: `push` with credentials per call, mirror mode, ref selection, per-ref status on `mirror.rs`. *Rationale:* mirrors need it, and the mapping already exists (G10).
- **ENG-46** — Mirror worker: one run per mirror with dirty-flag rerun, reconcile, credentials only in memory, per-ref ff-only rejections. *Rationale:* collapses ref-update bursts and keeps credentials out of ruk.
- **ENG-47** — R3: bounded `search` method in the Bulk lane with deadline, result cap and cancellation, plus the rukie search op and page. *Rationale:* lightweight search that can't affect interactive traffic.
- **ENG-48** — Releases: tag-backed releases via hosted `tag.*` and streaming archive downloads. *Rationale:* settled feature.
- **ENG-49** — Social: explore, stars, watches and forks (via registry fork). *Rationale:* settled feature.
- **ENG-50** — Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation. *Rationale:* carries the repository half of slice 13.
- **ENG-51** — Retention and maintenance stress under rukie load; maintenance and backups only through daemon methods in the Maintenance lane. *Rationale:* slice 12 plus the single-writer lock (G8).
- **ENG-52** — Kill-9 fault suite (rukie and daemon) mid-push, mid-webhook, mid-CI-attest and mid-mirror; ruk crash/lock/IO smoke tests; shuttle cases. *Rationale:* carried gates that prove §7.
- **ENG-53** — Run the reference capacity profile against rukie + daemon as the release gate; it decides F4. *Rationale:* the carried release gate.

```jsonl
{"id":"ENG-01","description":"Present the reconciliation table as a dated revision proposal for AGENT_NATIVE_GIT_PLAN.md and edit that plan only for user-approved items.","phase":"engineering"}
{"id":"ENG-02","description":"After approval, add an ignore entry for the nested ruk/rukie/ clone and never delete it.","phase":"engineering"}
{"id":"ENG-03","description":"Under the settled Gitea-cut decision confirmed at the gate, make one reversible rukie commit deleting the Gitea tree, setting module github.com/gosuda/rukie, adding a minimal rukie serve, new Makefile, CI and AGENTS.md, and keeping LICENSE.","phase":"engineering"}
{"id":"ENG-04","description":"Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, go test -race and fuzz tests.","phase":"engineering"}
{"id":"ENG-05","description":"R1a: rename the daemon to ruk serve and the handshake profile to host, and reword Forgejo-specific daemon docs and tests.","phase":"engineering"}
{"id":"ENG-06","description":"R1b step 1: add a ruk host-credential subcommand to ruk's own CLI that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids, with named errors and a test asserting owner, group and mode.","phase":"engineering"}
{"id":"ENG-07","description":"R1b step 2: run the daemon as uid A and rukie as uid B with allowed_uids=[B], carry a fresh single-use per-connection challenge in the handshake accept reply, require an HMAC over (challenge, asserted_principal, method) keyed by the ENG-06 credential, refuse a reused challenge and any second assertion on a bound session, bind the principal to the daemon session, attribute sessions, leases, receipts and attestations to it, and test correct, missing and wrong HMACs, replayed opens, reused challenges, second assertions and refusal of non-allowed uids and of the allowed uid without a valid credential, documenting that same-uid-as-rukie processes are out of protocol scope.","phase":"engineering"}
{"id":"ENG-08","description":"R1c: one task per stream with a per-connection frame writer.","phase":"engineering"}
{"id":"ENG-09","description":"R1d: stream input/output in both directions under credit windows and spill received packs to disk.","phase":"engineering"}
{"id":"ENG-10","description":"R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout.","phase":"engineering"}
{"id":"ENG-11","description":"R1f: keep RequestClass pub(crate), add public ExecutorLane mirroring all five RequestClass variants (Control, Interactive, Bulk, Hook, Maintenance) with a Method-to-lane mapping derived from the dispatcher, classify argv CommandOpen streams by verb on the server side with unknown verbs on Bulk and never Hook or Control, reserve BoundedExecutor permits per lane held inside the spawn_blocking closure, and add cancellation, mapping-consistency and verb-table tests.","phase":"engineering"}
{"id":"ENG-12","description":"Keep ruk gates green (fmt, 3-OS clippy -D warnings, nextest, cargo test, Unix cfg gating, no #[allow], unreachable_pub clean, exact pins, --locked) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths.","phase":"engineering"}
{"id":"ENG-13","description":"Two independent processes with two systemd units and two service accounts, a uid-restricted socket and no child-spawn mode.","phase":"engineering"}
{"id":"ENG-14","description":"K3: SQLite WAL store module with forward-only migrations and a leased outbox/job table.","phase":"engineering"}
{"id":"ENG-15","description":"K2: Op[In,Out] registry on Huma v2 and net/http ServeMux deriving OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), the generated rukie CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots.","phase":"engineering"}
{"id":"ENG-16","description":"Auth: argon2id passwords with TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts, and admin-CLI password reset.","phase":"engineering"}
{"id":"ENG-17","description":"Users, orgs, teams and repository permissions enforced in the operation-table authz middleware.","phase":"engineering"}
{"id":"ENG-18","description":"Design tokens as CSS custom properties and base layouts, with each later feature shipping its zero-JS pages from its operations.","phase":"engineering"}
{"id":"ENG-19","description":"R2a part 1: tree and object readers serving hosted diff/show/file/blame, fix the stale daemon doc claiming op.log, claim.list and attest.list serve only first pages, and exclude status.","phase":"engineering"}
{"id":"ENG-20","description":"R2b: hosted preparation for bookmark.*, tag.*, land.execute, push, push.reconcile, fetch, unpin and op.prune.","phase":"engineering"}
{"id":"ENG-21","description":"Crash test proving lane receipts survive a daemon kill at each mutation step and that replay is a no-op.","phase":"engineering"}
{"id":"ENG-22","description":"R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs and compare/merge-base reads.","phase":"engineering"}
{"id":"ENG-23","description":"testruk harness running the real daemon that fails, never skips, when the binary is missing.","phase":"engineering"}
{"id":"ENG-24","description":"K4: pooled daemon client with semaphore, cancellation, schema codegen and a one-to-one RFC 9457/MCP error map, tested on testruk.","phase":"engineering"}
{"id":"ENG-25","description":"RUK_PIN plus a schema-digest drift gate and a handshake version check.","phase":"engineering"}
{"id":"ENG-26","description":"Repo hosting and browsing feature: create/rename/delete ops and tree, blob, commit and diff pages and API.","phase":"engineering"}
{"id":"ENG-27","description":"Typed git-wire methods git.upload_pack (v0+v2), git.receive_pack and git.upload_archive admitted in the Bulk lane.","phase":"engineering"}
{"id":"ENG-28","description":"Keep quarantine and all hook phases (pre-receive, update, post-receive, proc-receive, fail-closed timeouts) unchanged and add typed hosted handlers for each phase.","phase":"engineering"}
{"id":"ENG-29","description":"rukie Smart HTTP and SSH frontends (x/crypto/ssh) that authenticate via ENG-16, authorize via ENG-17 and forward byte streams to the git-wire methods.","phase":"engineering"}
{"id":"ENG-30","description":"Walking-skeleton e2e: stock git clone, fetch v2, push and push refs/for/main over HTTP and SSH through the ENG-29 frontends to the daemon.","phase":"engineering"}
{"id":"ENG-31","description":"Process-trace test proving no git process runs inside the rukie plus daemon boundary.","phase":"engineering"}
{"id":"ENG-32","description":"Finish the slice-5 production graph index over store segments so revset evaluation runs outside tests.","phase":"engineering"}
{"id":"ENG-33","description":"R2a part 2: hosted log/query on the production graph index.","phase":"engineering"}
{"id":"ENG-34","description":"History and branch-view pages and ops grouping a branch's changes.","phase":"engineering"}
{"id":"ENG-35","description":"R2d: durable ordered op-log tail with a resumable subscription across op.prune.","phase":"engineering"}
{"id":"ENG-36","description":"Op-log consumer with one transaction for cursor and outbox, idempotent keys and a broadcast for long-wait operations.","phase":"engineering"}
{"id":"ENG-37","description":"Webhook delivery with Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker and an SSRF guard.","phase":"engineering"}
{"id":"ENG-38","description":"In-app inbox fed by the outbox, with no email delivery.","phase":"engineering"}
{"id":"ENG-39","description":"Per-token and per-account token buckets in the operation-table middleware with Retry-After.","phase":"engineering"}
{"id":"ENG-40","description":"R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim and attestation cursor paging already exists.","phase":"engineering"}
{"id":"ENG-41","description":"Review feature: changes and stacks from refs/for with change-ids, branch view, approvals as attestations and merge via land.execute.","phase":"engineering"}
{"id":"ENG-42","description":"Tasks feature replacing issues, claimable through Ruk leases.","phase":"engineering"}
{"id":"ENG-43","description":"CI core with a leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners and results as attestations.","phase":"engineering"}
{"id":"ENG-44","description":"Reuse MergeNotice unchanged and report effective mode and language_set_version on every mergeability and land result.","phase":"engineering"}
{"id":"ENG-45","description":"R4: push with credentials per call, mirror mode, ref selection and per-ref status on mirror.rs.","phase":"engineering"}
{"id":"ENG-46","description":"Mirror worker with one run per mirror and dirty-flag rerun, reconcile, credentials only in memory and per-ref ff-only rejections.","phase":"engineering"}
{"id":"ENG-47","description":"R3: bounded search method in the Bulk lane with deadline, result cap and cancellation, plus the rukie search op and page.","phase":"engineering"}
{"id":"ENG-48","description":"Releases backed by hosted tag.* with streaming archive downloads.","phase":"engineering"}
{"id":"ENG-49","description":"Social features: explore, stars, watches and forks via registry fork.","phase":"engineering"}
{"id":"ENG-50","description":"Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation.","phase":"engineering"}
{"id":"ENG-51","description":"Retention and maintenance stress under rukie load, with maintenance and backups only through daemon methods in the Maintenance lane.","phase":"engineering"}
{"id":"ENG-52","description":"Kill-9 fault suite for rukie and daemon mid-push, mid-webhook, mid-CI-attest and mid-mirror, plus ruk crash/lock/IO smoke tests and shuttle cases.","phase":"engineering"}
{"id":"ENG-53","description":"Run the reference capacity profile against rukie and the daemon as the release gate, deciding the metadata store.","phase":"engineering"}
```

### Critical Files for Implementation
- `ruk/crates/ruk/src/main.rs` — the CLI that gains the `host-credential` provisioning subcommand (ENG-06) beside `serve`.
- `ruk/crates/ruk/src/daemon/ipc.rs` — the peer-uid check (533-550) and the handshake frames that ENG-07 extends with the single-use challenge.
- `ruk/crates/ruk/src/daemon/executor.rs` — the permit freed on cancel and the shared semaphore (G13, ENG-11).
- `ruk/crates/ruk-core/src/coord/admission.rs` — the private `RequestClass` (lines 33-41) and the dispatcher-only invariant behind the five-lane `ExecutorLane`.
- `ruk/crates/ruk/src/daemon/mod.rs` — the argv `CommandOpen` runner that needs the verb rule, plus buffered input, blocking streams, `InvocationOpen` (304-316), the outcome ledger and the stale read-coverage doc (612-614).

[advisor] <advisory advisor="muse" severity="concern" guidance="weigh, don't blindly obey"> Fix two wrong ENG links to drift gate…

---
# autoplan-eng.md: Engineering review of the rukie ground-up rewrite on Ruk (revision 15)

The plan is buildable, but not in the order the skeleton gives. The ruk daemon can't yet host a forge, so ruk's hosting work has to come before most rukie features. Two things block it today:

1. **Typed forge reads are missing.** Over the daemon, `diff`, `show`, `file`, `blame`, `log` and `query` are refused with `E_HOSTED`.
2. **Large transfers don't stream.** Git-wire traffic is buffered whole in memory, and one long command stalls every other stream on its connection.

So the first job is not "add forge read methods" (R2). It is to fix how the daemon hosts: build the typed reads, stream transfers, keep bulk work from taking all the threads, and accept per-user identity from the forge.

**Fix in this revision (the reviewer was right):** two cross-references still pointed at the old numbering and named the wrong task as the drift gate. The drift gate (schema-digest + handshake version check) is **ENG-25**. Corrected: the §2 CEO-01 row and the §9 drift-check bullet now say ENG-25. A sweep of every other ENG reference found no further stale ids. Everything else is unchanged from revision 14 and reissued in full because this final message is what gets saved.

**This file has not been written to disk.** This session is read-only plan mode with no write tool. The orchestrator needs to save this message as `autoplan-eng.md` and create `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` from the JSONL block in §12.

---

## 0. Restated problem

Choose the build order, the process layout and the reliability guarantees for two repositories that change together:
- **ruk:** Rust; workspace gates fmt / clippy / nextest / test.
- **rukie:** Go; today a 393k-LOC Gitea tree that becomes an agent-first forge using only the Ruk daemon as its repository backend.

Constraints:
- every commit builds in both repos;
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
- tests must run against a real daemon.

**Where the labels come from:**
- R1–R6, K1–K4, F, U, V: the draft plan outline in this assignment's context (not stored in any repo file).
- S0–S7: stages in §5.
- G1–G17: findings in §1.
- F1–F7: design forks in §3.

## 0a. Authorization boundary

- **This review authorizes nothing.** autoplan ends at a human approval gate. Every task starts only after that approval.
- **Deleting the Gitea tree (ENG-03)** rests on this assignment's settled decision: "Cut every Gitea surface (API v1, swagger, app.ini, xorm schema, Actions, packages, OAuth2 provider, Gitea webhook formats, builtin SSH code, gitea CLI, Vue/Vite/Tailwind, locales) … Land as commits on top of Gitea history; every commit builds." That text lives only in the assignment context, not in any workspace file; the user confirms it at the gate. It covers rukie only, and it is a reversible commit with history kept.
- **The existing ruk plan:** `ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:119-121` says no source cutover, data migration or source-copy purge is authorized by it, and forbids silent feature removal or storage substitution. So: changing that plan (ENG-01) is a proposal decided item by item; the nested `ruk/rukie/` clone is never deleted, and ENG-02 only proposes an ignore entry; ruk's redb store is untouched. The metadata store (F4) belongs to rukie.
- **No outward acts** (push, publish, name reservation) are proposed.

## 1. What the code shows today

| # | Finding | Evidence | Consequence |
|---|---|---|---|
| G1 | The daemon is wired as `Command::Forgejo { command: ForgejoCommand::Serve }`; the handshake profile is hard-coded `"forgejo"`. | `ruk/crates/ruk/src/main.rs:202-220`; `daemon/ipc.rs:643`; `daemon/tests.rs:322` | R1 is a rename plus a protocol change (ENG-05). |
| G2 | Typed hosted reads serve `op.log`, `claim.list`, `attest.list` with lease-bound cursor continuation (`read()` mints a 30-second cursor when a first page does not exhaust the listing and resumes it after binding checks). Every other read names its missing piece in `read_gap`: `status` needs a workspace autosnapshot and workcopy crawl; `log`/`query` need "a production graph index over store segments" because revset evaluation "builds its graph only in tests"; `diff`/`show`/`file`/`blame` need "tree and object readers over resolved revisions". | `ruk-core/src/coord/hosted.rs:539-560, 612-632, 1347-1362` | A forge doesn't need `status`. `diff`/`show`/`file`/`blame` need tree and object readers (ENG-19); `log`/`query` need the graph index (ENG-32, ENG-33). `daemon/mod.rs:612-614` still says "first pages are served", contradicting the coordinator; ENG-19 fixes that stale doc. The argv `CommandOpen` path returns untyped text and blocks other streams (G5), so browsing waits for ENG-19 instead of shipping on it. |
| G3 | Hosted mutations covered: ref, new, claim, ws.new, pin, attest, describe, split, rebase, squash, abandon, edit, ws.scope, ws.take, ws.drop, resolve, revert, undo, land.prepare. Not covered: `land.execute`, `push`, `push.reconcile`, `fetch`, `bookmark.*`, `tag.*`, `snapshot`, `write`, `op.prune`, `unpin`. | `daemon/mod.rs:627` | Merges, mirrors and releases are blocked on these (ENG-20). |
| G4 | Identity is derived from the peer uid alone (`Principal::from_peer_uid`, `ipc.rs:533-550` accepts every process with an allowed UID), and `InvocationOpen` (`daemon/mod.rs:304-316`) carries no assertion field, nonce or sequence; `session_id` and the mutation `request_id` are optional and reusable. | `daemon/mod.rs:2017,304-316`; `daemon/registry.rs:279`; `hosted.rs:590-596` | rukie's one uid would make every user and agent a single principal, any same-uid process could impersonate all of them, and a captured open frame could be replayed. The daemon needs an authenticated, replay-resistant host assertion under distinct service identities (ENG-06, ENG-07). |
| G5 | A long-running command blocks other streams on the same connection (documented v1 limitation); clone, fetch and archive arrive as argv `CommandOpen` streams. | `daemon/mod.rs` `command_inner` doc; `ruk-git/src/transport/mirror.rs:12` | Connections become the bottleneck (`DEFAULT_MAX_CONNECTIONS = 128`); the argv path has no `Method` to classify by (ENG-11). |
| G6 | Command input is buffered in full (up to 256 MiB) before running; output is captured, then streamed; pushed packs are staged into memory. | `daemon/mod.rs` consts / docs; `ruk-git/src/transport/hooks.rs:1-12` | RSS grows with pack size × concurrency; transfers must stream and spill to disk (ENG-09). |
| G7 | The reconnect outcome ledger is in memory (10,000 entries); durable receipts live in the lane. | `daemon/mod.rs`; `ruk/docs/coordinator.md` | rukie must persist request ids; whether receipts are durable on disk is **inference** from coordinator.md, tested by ENG-21. |
| G8 | Each repo's database (redb) allows one writer process; a second gets `DatabaseAlreadyOpen`. | `AGENT_NATIVE_GIT_PLAN.md:152` | The daemon is the only process that opens hosted stores. |
| G9 | Receive pipeline: quarantine, `pre-receive`/`update`/`post-receive`, fail-closed timeouts, and `proc-receive` diversion using git's exact conversation. | `hooks.rs:1-47`; `proc_receive.rs:1-44` | Git semantics that stay in ruk; rukie adds typed handlers per phase (ENG-28). |
| G10 | The `push --mirror` mapping and refspec pruning already exist. | `ruk-git/src/transport/mirror.rs:1-40` | R4 extends existing code (ENG-45). |
| G11 | Daemon limits: 64 KiB data frame, 1 MiB control frame, 256 KiB starting / 16 MiB maximum window, 16 blocking permits, 60 s per-frame timeout. | `daemon/ipc.rs:20-32`; `daemon/mod.rs:24-37` | Per-stream memory has a known upper bound; subscriptions need keepalives (ENG-10). |
| G12 | The admission classes exist and are private to ruk-core: `pub(crate) enum RequestClass { Control, Interactive, Bulk, Hook, Maintenance }`, documented "The dispatcher selects it; a client never does"; the scheduler has separate limits per class; Bulk leases cancel after 30 s without progress and are capped at 2 h. | `ruk-core/src/coord/admission.rs:33-41` (read directly); `admission/scheduler.rs:30-35,222-226`; `leases/timing.rs:15-22` | Don't add a class. The ruk-core dispatcher maps typed methods to classes; the daemon crate can't name the class, so it needs a public `ExecutorLane` mirroring all five variants plus its own verb rule for argv streams (ENG-11). |
| G13 | The executor has two defects: one shared semaphore for every class, so per-class admission is undone at the thread pool; and `run` acquires the permit outside `spawn_blocking` and drops it only after awaiting the join handle, so a cancelled caller releases the permit while `work` keeps running. | `ruk/crates/ruk/src/daemon/executor.rs` (`run`) | Blocking concurrency and memory can exceed their bound under client cancellations; fixed and tested in ENG-11. |
| G14 | ruk CI: fmt; clippy `--workspace --all-targets --all-features --locked -D warnings` on 3 OSes; nextest 0.9.148 on 3 OSes; `cargo test` on 3 OSes; toolchain 1.99.1. Miri, fuzz, soak and store gates are deferred. Lints deny `allow_attributes`, `unsafe_code`, `todo`, `unimplemented`, `dbg_macro`; `unreachable_pub` warns; exact `=` pins with `--locked`. The existing plan's Miri gate: `cargo +nightly miri test -p ruk-core store::segment`. | `ruk/.github/workflows/ci.yml`; `ruk/Cargo.toml`; `ruk/rust-toolchain.toml`; `AGENT_NATIVE_GIT_PLAN.md` "Verification gates" | Unix-only daemon code must pass Windows clippy without `#[allow]`; a new public ruk-core type needs a real outside user or `unreachable_pub` plus `-D warnings` fails CI; the deferred gates are still owed (ENG-12). |
| G15 | `ruk/rukie/` is a clone of `https://github.com/gosuda/rukie` inside the ruk tree, and ruk's `.gitignore` does not exclude it. | `ruk/rukie/.git/config`; `ruk/.gitignore` | Risk of committing a gitlink; never delete it. |
| G16 | `ruk/tasks/autoplan-ledger.jsonl` holds `ENG-01..06` from the earlier autoplan run; `ruk/.gitignore` ignores `tasks/`; there is no `tasks/` directory at the workspace root. | that file; `ruk/.gitignore`; workspace root listing | This run's ledger is `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`: a new file, no id collision. `ruk/tasks/autoplan-ledger.jsonl` is left alone. |
| G17 | `AGENT_NATIVE_GIT_PLAN.md` targets Forgejo parity, forbids runtime Git, gates release on a capacity profile, and requires explicit revision for any removal. | `AGENT_NATIVE_GIT_PLAN.md:5-14,71-99,115-121` | §2 is a revision proposal, not a supersession. |

## 2. Proposed reconciliation with `AGENT_NATIVE_GIT_PLAN.md` (decided at the gate)

| Existing plan item | Proposed verdict | Carried form |
|---|---|---|
| Slices 1–8 | Carry unchanged | Slice 5's production graph index becomes a scheduled task (ENG-32) because hosted `log`/`query` need it. |
| Slice 9 | Carry; backend half adapted | ENG-19, ENG-20, ENG-22, ENG-33. |
| Slice 10 | Adapt; dropping the Forgejo Go patch needs approval | `ruk serve` plus the rukie client (ENG-05..07, ENG-11, ENG-24). |
| Slice 11 | Carry in ruk; forge side adapted | Hook phases stay (ENG-28); mirrors become ENG-45; dropping wiki/template flows needs approval. |
| Slice 12 | Carry | ENG-51. |
| Slice 13 | Repository half carried; Forgejo-database half pending approval | ENG-50. |
| Slice 14 + release gate | Carry | ENG-53. |
| No runtime Git | Carry | ENG-31. |
| CEO-01 frozen Forgejo inventory | Proposed superseded, approval needed | Replaced by the ENG-25 drift gate. |
| DES-01 one `MergeNotice` record; CEO-02 effective mode + `language_set_version` | Carry | ENG-44. |
| Verification gates: Miri, fuzz, shuttle, crash smoke tests, out-of-process harness | Carry | ENG-12 (fuzz + Miri), ENG-23, ENG-52. |

Every item stays in force until the user approves a change.

## 3. Hypotheses on the major forks

### F1 — Process layout
- **H1a (recommended):** two independent processes over a Unix socket under two dedicated service accounts (F7): the daemon as uid A, rukie as uid B, `allowed_uids = [B]`. rukie terminates HTTP and SSH and forwards byte streams to typed git-wire methods. *Disproved if* deploys must be a single binary *and* rukie restarts never need to keep transfers alive.
- **H1b:** rukie spawns the daemon as a child. Rejected: deploys kill in-flight transfers, and it means two runtime paths.

### F2 — Push policy and refs/for
- **H2a (recommended):** the receive pipeline stays; hosted mode registers typed handlers: ref policy at `pre-receive`/`update`, change-id change creation at `proc-receive`, a wake-up hint at `post-receive`. Script hooks remain the CLI and self-hosted default. *Disproved if* the handlers can't see quarantined objects through the view scripts get.
- **H2b:** delete the hook phases. Rejected: breaks slice 11 parity.

### F3 — How ref updates reach CI, webhooks and mirrors durably
- **H3a (recommended):** a stored op-log position plus outbox rows, advanced in one transaction; `post-receive` is only a wake-up hint. *Disproved if* `op.log` can't be paged in order from a durable position across `op.prune`.
- **H3b:** a push-time callback inserts the jobs. Rejected: events are lost while rukie is down.

### F4 — rukie metadata store (open)
- **H4a (recommended):** SQLite WAL, pure Go, behind a SQL-shaped store module. *Disproved by* the capacity gate (ENG-53).
- **H4b (runner-up):** PostgreSQL, which the existing plan uses for capacity acceptance (`:117`).
- **H4c:** Pebble. Rejected: no SQL.

### F5 — Deleting the Gitea tree
- **H5a (recommended):** one reversible commit.
- **H5b:** staged deletion. Rejected: every intermediate commit is a gutted Gitea that still has to build, with no value.

### F7 — Host principal assertion
- **Deployment:** distinct identities. The daemon runs as uid A; rukie runs as uid B; `allowed_uids = [B]`. Credential provisioning is a **ruk-side CLI subcommand** (`ruk host-credential`, added in ENG-06 before any handshake work, depending on nothing later), writing the random secret as a 0640 file owned by A in a dedicated group whose only members are A and B. Rukie fails at boot with a named error when it cannot read it; the daemon fails at startup when it cannot. A same-account 0600 file is rejected because any same-uid process reads it.
- **Guarantee, stated honestly:** the uid gate plus the credential prevents impersonation by any process outside {A, B}. It does not separate a process running as rukie's uid (B): that process is inside rukie's trust boundary and can ptrace rukie, read its memory and steal sessions and tokens regardless of this layer. User-attribution separation within one uid is not attainable in userspace; the deployment requirement is that only rukie runs as B. A pid → executable check over `SO_PEERCRED` remains optional defense-in-depth, noted as weak.
- **H7a (recommended):** identity additionally carries a proof with daemon-issued freshness:
  1. the handshake accept reply carries a fresh, cryptographically random, single-use per-connection challenge;
  2. a principal assertion is an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the host credential;
  3. the daemon records consumed challenges for the life of the connection and refuses any reuse; it also refuses any second assertion on a session that is already bound;
  4. the asserted principal is bound to the daemon-issued session, so later frames on that session never re-assert.

  Consequences: the same assertion bytes are never accepted twice; a captured open frame replayed on another connection fails (different challenge); replaying frames on the same connection after binding cannot re-assert identity; invocation-level replay of bound-session frames remains the intended request-id idempotency path. Sessions, leases, receipts and attestations attribute to the bound principal.
- **H7b:** accept bare assertions from allowed uids. Rejected: any allowed-uid process could impersonate every user and forge the audit trail.
- **H7c:** HMAC over a client-chosen nonce only. Rejected: the client can replay its own nonce.
- **H7d:** same service account with a 0600 file. Rejected: no peer separation, and a daemon-created 0600 file is unreadable by a differently-owned rukie.

### F6 — Executor isolation across the crate boundary
- **H6a (recommended):** `RequestClass` stays `pub(crate)`; ruk-core exposes a public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) plus a `Method` → lane mapping derived from the dispatcher's class choice; the daemon classifies argv `CommandOpen` streams by verb on the server side (table below), never emitting Hook or Control; `BoundedExecutor` reserves permits per lane and holds each permit inside the `spawn_blocking` closure. *Disproved if* all blocking work could move into ruk-core's admitted path; the argv runner and git-wire serving live in the daemon crate, so it can't.
- **H6b:** make `RequestClass` public. Rejected: it would let clients choose their own class, against `admission.rs:33`.
- **H6c:** one executor per class. Rejected: idle threads, against the executor's design (`executor.rs` header).
- **H6d:** a three-variant `ExecutorLane`. Rejected: hook work would land on Bulk permits and exceed that class's limit.

Verb rule for argv streams:

| Verbs | Lane |
|---|---|
| `upload-pack`, `receive-pack`, `upload-archive`, `archive`, `fetch`, `push`, `pack-objects`, `index-pack`, `grep`, `blame` | Bulk |
| `gc`, `repack`, `prune`, `maintenance`, `fsck` | Maintenance |
| Any other listed verb | Interactive |
| Unknown verb | Bulk |
| *(never produced by argv)* | Hook, Control |

## 4. Six-principle register (engineering)

| Principle | Decision | Open question |
|---|---|---|
| **Clarity** | One operation table (ENG-15) derives HTML, JSON, MCP and CLI. One wire contract (schema codegen + digest gate). One git-wire method family. One event spine. The daemon is the only process opening stores. One receive pipeline with script or typed handlers. Admission classes chosen server-side only; the executor sees all five lanes. Host identity = distinct service uids + a ruk-CLI-provisioned group-restricted credential + an HMAC'd assertion over a daemon-issued single-use challenge, with the guarantee scoped to outside-{A,B} processes (F7). Plan changes are proposals. | — |
| **Impact** | Fix streaming, per-stream concurrency and executor isolation (G5, G6, G13) first. Tree/object readers unlock file, commit and diff pages. The graph index (ENG-32) is the long pole for history and branch views. | — |
| **Audience** | Typed errors map one-to-one to RFC 9457 / MCP with `Retry-After`. `MergeNotice` and effective mode on every merge result. Zero-JS HTML from the same operations. Agents use scoped tokens on human accounts (ENG-16). | Runner credential class. |
| **Risk** | Pack memory; principal impersonation by outside processes (F7, ENG-07), with same-uid processes named as out of scope; lost events; the single-writer store lock; a cancelled request freeing its permit while its work keeps running, and argv transfers bypassing lanes (G13, ENG-11); leaking `RequestClass` to clients; SSRF; cross-repo drift; runtime Git; unauthorized plan or destructive changes (§0a). | Lane receipt durability (ENG-21). |
| **Sequencing** | Tasks are numbered in stage order (§5); each depends only on earlier tasks. Provisioning (ENG-06) precedes the handshake (ENG-07), both in ruk's CLI, so S1 has no circular dependency on rukie. Store → operation table → auth → users/orgs before any authenticated feature. Git wire → hooks → frontends → walking skeleton. Graph index → `log`/`query` → history pages → review. Capacity is the final release gate. | — |
| **Reversibility** | `protocol_major` bump plus `RUK_PIN`; forward-only migrations with backup restore; Gitea deletion revertible; PostgreSQL fallback; script hooks can be re-enabled per repo; `ExecutorLane` and the verb table are small and easy to change before release; the host credential can be rotated by regenerating the file. | — |

## 5. Amended dependency order

### 5.1 Which ruk capability blocks which rukie feature

| rukie feature (task) | Blocking ruk capability (task) | Today |
|---|---|---|
| Auth, users, orgs, teams, tokens (ENG-16, ENG-17) | none | — |
| Any repo-touching feature | `ruk serve`, credential provisioning, authenticated host assertion, handshake check (ENG-05, ENG-06, ENG-07, ENG-25) | missing |
| Repo hosting and browsing (ENG-26) | Tree/object readers + tree / refs / compare + lifecycle (ENG-19, ENG-22) | E_HOSTED |
| History, branch view (ENG-34) | Graph index → `log`/`query` (ENG-32, ENG-33) | E_HOSTED; graph built only in tests |
| HTTP / SSH clone & push (ENG-29) | Per-stream tasks, streaming, spill, lanes, git-wire methods, hook handlers (ENG-08, ENG-09, ENG-11, ENG-27, ENG-28) | buffered; permit freed early |
| Webhooks, inbox (ENG-37, ENG-38) | Op-log tail + keepalive (ENG-10, ENG-35) | 60 s frame timeout kills subscriptions |
| Review (ENG-41) | Hook handlers (ENG-28), hosted `land.execute` (ENG-20), R6 extensions (ENG-40) | script-only / `land.execute` uncovered |
| Tasks (ENG-42) | R6 claim extensions (ENG-40); `claim.*` and its cursor paging already exist | lease semantics missing |
| CI (ENG-43) | R6 attestation kinds (ENG-40); `attest.add` and cursor paging already exist | kinds missing |
| Mirrors (ENG-46) | Hosted `push` / `push.reconcile` (ENG-20) + R4 (ENG-45) | uncovered |
| Search (ENG-47) | Bounded `search` in the Bulk lane (ENG-47) | missing |
| Releases (ENG-48) | Hosted `tag.*` (ENG-20) + streaming archive (ENG-27) | uncovered |
| Social and forks (ENG-49) | Registry fork (ENG-22) | missing |
| Repo import (ENG-50) | Offline Git conversion | CLI only |

### 5.2 Stages (each stage = commits that each build; all start after the gate)

| Stage | Tasks | Exit proof |
|---|---|---|
| S0 | ENG-01..04: gate items, K1, Go gates | both CIs green |
| S1 | ENG-05..18: R1 daemon work (rename, provisioning, assertion, per-stream, streaming, keepalive, lanes, ruk gates), topology, K3 store, K2 operation table, auth, users/orgs/teams, UI tokens | soak test + fuzz + Miri; executor and verb-lane tests; provisioning and assertion tests; login and token flows on all four surfaces |
| S2 | ENG-19..26: tree/object reads, mutations, receipts, lifecycle, `testruk`, K4 client, drift gate, repo browsing | real-daemon integration tests; browse a repo end to end |
…
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.

[Showing lines 7690-8034 of 8940 (49.9KB limit). Use :8035 to continue]
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
…
| S2 | ENG-19..26: tree/object reads, mutations, receipts, lifecycle, `testruk`, K4 client, drift gate, repo browsing | real-daemon integration tests; browse a repo end to end |
| S3 | ENG-27..31: git wire → hook handlers → frontends → walking skeleton → no-Git trace | **walking skeleton:** stock git clone / push / `push refs/for/main` through rukie |
| S4 | ENG-32..38: graph index, `log`/`query`, history pages, op-log tail, consumer, webhooks, inbox | kill-restart test: no lost or duplicate deliveries |
| S5 | ENG-39..44: token buckets, R6, review, tasks, CI, MergeNotice | end-to-end agent flow (claim → push → CI attest → land) |
| S6 | ENG-45..51: R4, mirror worker, search, releases, social, import, retention stress | §8 targets |
| S7 | ENG-52..53: fault suite, capacity profile | release gate |

## 6. Concurrency and backpressure
- **Daemon:** one task per stream and one writer per connection, bounded by credit windows (`ipc.rs:29-32`).
- **Admission and executor (ENG-11):** typed methods use the dispatcher's class and the derived five-variant `ExecutorLane`; argv streams use the verb rule with unknown verbs on Bulk and never Hook or Control; permits are reserved per lane and held inside the `spawn_blocking` closure.
- **Identity:** distinct service accounts (A = daemon, B = rukie), `allowed_uids = [B]`, group-restricted credential provisioned by `ruk host-credential` (ENG-06), and the HMAC'd host assertion over a daemon-issued single-use challenge (ENG-07, F7); the guarantee covers processes outside {A, B}.
- **rukie client:** a pool of about 4–8 connections with an invocation semaphore below the daemon's limits; cancelling a request's context cancels its stream; `E_LIMIT` maps to 429/503 with `Retry-After`; only idempotent reads are retried automatically.
- **Per-token limits:** token buckets per token and per account in the operation-table middleware (ENG-39).
- **Ref races:** the lane's compare-and-swap (`E_STALE`) is sent back as a typed conflict; no rukie-side locks.
- **Long waits for agents:** served from an in-process broadcast fed by the event consumer, not by polling the daemon; HTML stays zero-JS.

## 7. Durability of CI, webhook and mirror queues
1. **One spine:** one transaction reads ops from the stored op-log position, inserts outbox rows unique on `(repo, op_id, target)`, and advances the cursor; delivery is at-least-once with handlers safe to run twice.
2. **Job claiming:** rows carry leases and attempt counts; claims are atomic `UPDATE … RETURNING`; retries back off with jitter; failed jobs end in a dead-letter state.
3. **Webhooks:** Standard Webhooks signatures; `webhook-id` = outbox key; circuit breaker per endpoint; resolve-and-pin SSRF guard.
4. **Mirrors:** one run at a time with a dirty-flag rerun; periodic reconcile; credentials only in memory; per-ref fast-forward-only rejections.
5. **CI:** the job DAG is stored in metadata with leases and heartbeats; cache key = hash(inputs + spec + image digest); logs are written in chunks to files; the request id is persisted before `attest.add`; runners fetch over the git wire only.

## 8. Performance budgets

**Release gate:** the reference capacity profile (`AGENT_NATIVE_GIT_PLAN.md:115-121`), run by ENG-53.

| Path | Interim target (until measured) |
|---|---|
| Small daemon read over the socket | p99 ≤ 2 ms |
| Metadata-only API operation | p95 ≤ 20 ms |
| Tree / blob / commit page | p95 ≤ 50 ms |
| HTML page weight | ≤ 50 KB gzip, 0 B JS |
| Clone throughput | ≥ 80% of `git http-backend` |
| Push / clone memory | RSS growth independent of pack size beyond about 16 MiB per stream, including under cancel storms (ENG-11) |
| Ref update → webhook delivered | p95 ≤ 2 s |
| Ref update → mirror started | p95 ≤ 5 s |
| Idle CI queue → job started | ≤ 1 s |
| Search | deadline 2 s, capped results, cancellable |
| Bulk saturation | Interactive p95 unchanged while the Bulk lane is at its limit |

## 9. Testing strategy
- **Real daemon, no mocks of ruk:** `testruk` (ENG-23) starts `ruk serve` from `RUK_BIN` and fails, never skips, when it is missing; `make test` builds ruk at `RUK_PIN`.
- **Drift check (ENG-25):** schema regeneration and digest check plus a handshake version check.
- **Stock git and OpenSSH as clients** outside the boundary (ENG-30, after the ENG-29 frontends), plus a trace proving no `git` runs inside it (ENG-31).
- **Provisioning and assertion tests (ENG-06, ENG-07):** the subcommand creates the file with the exact owner, group, and mode; boot fails with a named error when the credential is missing; correct HMAC accepted; missing and wrong HMAC refused; the same assertion bytes replayed on the same and on a fresh connection refused (consumed challenge / challenge mismatch); a second assertion on a bound session refused; a peer with a non-allowed uid refused by the gate; a peer with the allowed uid but no valid credential refused.
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.
- **Operation-table tests (ENG-15):** each registered op is reachable and consistent through HTML form, JSON, MCP and CLI from one definition.
- **Fault and concurrency tests:** kill-9 suite, ruk crash/lock/IO smoke tests, shuttle (ENG-52); 60 s fuzz of frame, pkt-line and git-wire decoders plus Miri (ENG-12).
- **Go gates (ENG-04) and ruk gates (ENG-12):** gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz; fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`.

## 10. Repo migration and module path (after the gate)
- **K1 (ENG-03) is one reversible commit** on top of Gitea history: remove the Gitea tree; add `module github.com/gosuda/rukie` (origin `https://github.com/gosuda/rukie`), Go 1.27 / go1.27.2, a fresh `go.sum`, a minimal `rukie serve`, a new Makefile, new CI and a new AGENTS.md.
- **License:** keep the MIT `LICENSE` with Gitea's notice (whether a rewritten tree still needs it is **inference**).
- **No `gitea.dev` aliases.**

## 11. Open questions for the approval gate
1. **rukie metadata store:** SQLite-first with PostgreSQL fallback (recommended) vs PostgreSQL from the start.
2. **CI runner credentials** under "no agent principals".
3. **Host reads without sessions** vs per-principal session pooling.
4. **User script hooks in hosted repos,** or typed handlers only.
5. **Approve item by item** the §2 rows marked "needs approval": the Forgejo patch, wiki/template flows, the Forgejo-database half of migration, CEO-01.
6. **Approve** the ENG-02 ignore entry for `ruk/rukie/`.
7. **Confirm the settled Gitea-cut decision** quoted in §0a. It exists only in the assignment context.
8. **Accept the F7 boundary:** the assertion scheme protects against processes outside {A, B}; same-uid-as-rukie protection is a deployment requirement (only rukie runs as B), not a protocol guarantee.
9. **Ledger target:** `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` (new file, no id collision). `ruk/tasks/autoplan-ledger.jsonl` is not touched.

## 12. ENG tasks (stage order; one line each: id, description, rationale; all start after the gate)

- **ENG-01** — Present the §2 reconciliation as a dated revision proposal for `AGENT_NATIVE_GIT_PLAN.md` and edit that plan only for approved items. *Rationale:* G17 and §0a require an explicit revision rather than a silent supersession.
- **ENG-02** — After approval, add an ignore entry for the nested `ruk/rukie/` clone; never delete it. *Rationale:* avoids committing a gitlink (G15) without purging anything.
- **ENG-03** — K1 under the settled Gitea-cut decision confirmed at the gate: one reversible rukie commit deleting the Gitea tree, module `github.com/gosuda/rukie`, minimal `rukie serve`, new Makefile, CI and AGENTS.md, LICENSE kept. *Rationale:* a staged deletion has no value, and the module path and agent rules must change together (F5).
- **ENG-04** — Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz tests. *Rationale:* every later rukie commit is gated from the start.
- **ENG-05** — R1a: rename to `ruk serve` and profile `host`, and reword Forgejo-specific daemon docs and tests. *Rationale:* the command and profile are Forgejo-named today (G1).
- **ENG-06** — R1b step 1: add a `ruk host-credential` subcommand to ruk's own CLI (`crates/ruk/src/main.rs`, beside `serve`) that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids (paths configurable), with named errors on failure and a test asserting owner, group and mode. *Rationale:* provisioning is ruk-side infrastructure the handshake (ENG-07) consumes; placing it here avoids a circular dependency on rukie's later CLI layer (F7).
- **ENG-07** — R1b step 2: run the daemon as uid A and rukie as uid B with `allowed_uids = [B]`; carry a fresh single-use per-connection challenge in the handshake accept reply; require an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the ENG-06 credential; refuse a reused challenge and any second assertion on a bound session; bind the principal to the daemon session and attribute sessions, leases, receipts and attestations to it; test correct/missing/wrong HMACs, replayed opens on the same and fresh connections, reused challenges, second assertions, refusal of non-allowed uids and of the allowed uid without a valid credential. Document that same-uid-as-rukie processes are out of protocol scope. *Rationale:* a bare uid assertion permits impersonation and a client-chosen nonce permits replay; the guarantee must match what the kernel enforces (G4, F7).
- **ENG-08** — R1c: one task per stream with a per-connection frame writer. *Rationale:* removes the documented stream blocking (G5).
- **ENG-09** — R1d: stream input/output in both directions under credit windows and spill received packs to disk. *Rationale:* buffered packs make RSS scale with pack size (G6).
- **ENG-10** — R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout. *Rationale:* subscriptions would otherwise be timed out (G11).
- **ENG-11** — R1f, in one change: keep `RequestClass` `pub(crate)`; add public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) with a `Method` → lane mapping derived from the dispatcher; classify argv `CommandOpen` streams by verb server-side (transfer/grep/blame → Bulk, gc/repack/prune/maintenance/fsck → Maintenance, unknown → Bulk, never Hook or Control); reserve `BoundedExecutor` permits per lane with each permit held inside the `spawn_blocking` closure; add cancellation, mapping-consistency and verb-table tests. *Rationale:* covers typed methods and argv transfers, keeps hook limits intact, and stops early permit release (G5, G12, G13, F6).
- **ENG-12** — Keep ruk gates green (fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths. *Rationale:* G14 lists owed gates that no other task carries.
- **ENG-13** — Two independent processes with two systemd units and two service accounts, uid-restricted socket, no child-spawn mode. *Rationale:* transfers survive rukie deploys, and the split accounts give the F7 boundary.
- **ENG-14** — K3: SQLite WAL store module, forward-only migrations, leased outbox/job table. *Rationale:* transactional cursor plus outbox needs SQL, and PostgreSQL stays possible (F4).
- **ENG-15** — K2: the `Op[In,Out]` registry on Huma v2 + net/http ServeMux, producing OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), generated `rukie <op> --json` CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots. *Rationale:* every feature, frontend and limit sits on it.
- **ENG-16** — Auth: argon2id passwords + TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts (no agent principals), admin-CLI password reset. *Rationale:* required by every authenticated op and the git frontends.
- **ENG-17** — Users, orgs, teams and repository permissions, enforced in the ENG-15 authz middleware. *Rationale:* authorization for ops and git access.
- **ENG-18** — Design tokens as CSS custom properties and base layouts; each later feature ships its zero-JS pages from its operations. *Rationale:* avoids a big-bang UI phase.
- **ENG-19** — R2a part 1: tree and object readers serving hosted `diff`/`show`/`file`/`blame`; fix the stale `daemon/mod.rs:612-614` doc that says `op.log`/`claim.list`/`attest.list` serve only first pages; exclude `status`. *Rationale:* the real read gap in `read_gap` (hosted.rs:1347-1362); a forge has no workcopy (G2).
- **ENG-20** — R2b: hosted preparation for `bookmark.*`, `tag.*`, `land.execute`, `push`, `push.reconcile`, `fetch`, `unpin`, `op.prune`. *Rationale:* merges, mirrors and releases depend on them (G3).
- **ENG-21** — Crash test proving lane receipts survive a daemon kill at each mutation step and replay is a no-op. *Rationale:* safe retries rest on durable receipts (G7).
- **ENG-22** — R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs, compare/merge-base. *Rationale:* hosting, browsing and forks.
- **ENG-23** — `testruk` harness running the real daemon that fails, never skips, when the binary is missing. *Rationale:* integration tests need a real daemon without false green.
- **ENG-24** — K4: pooled client, semaphore, cancellation, schema codegen, one-to-one RFC 9457 / MCP error map, tested on `testruk`. *Rationale:* backpressure and typed errors for agents.
- **ENG-25** — `RUK_PIN` + schema-digest drift gate + handshake version check. *Rationale:* keeps both repos building together across breaking ruk changes; replaces the frozen Forgejo inventory (CEO-01) as the cross-repo contract.
- **ENG-26** — Repo hosting and browsing feature: create/rename/delete ops, tree, blob, commit and diff pages and API, on ENG-19/22/24. *Rationale:* first repo-facing feature, proving the operation table end to end.
- **ENG-27** — Typed git-wire methods `git.upload_pack` (v0+v2), `git.receive_pack`, `git.upload_archive`, admitted in the Bulk lane. *Rationale:* one streaming contract for HTTP and SSH (F1).
- **ENG-28** — Keep quarantine and all hook phases (`pre-receive`, `update`, `post-receive`, `proc-receive`, fail-closed timeouts) unchanged; add typed hosted handlers for each phase. *Rationale:* preserves slice 11 parity and git semantics while removing script dispatch for the forge (G9, F2).
- **ENG-29** — rukie Smart HTTP + SSH frontends (`x/crypto/ssh`) that authenticate via ENG-16, authorize via ENG-17, and forward byte streams to the git-wire methods. *Rationale:* authentication stays in rukie, pack logic in ruk (F1).
- **ENG-30** — Walking-skeleton e2e: stock git clone, fetch v2, push and `push refs/for/main` over HTTP and SSH through the ENG-29 frontends to the daemon. *Rationale:* retires the biggest integration risk once its parts exist.
- **ENG-31** — Process-trace test proving no `git` process runs inside the rukie + daemon boundary. *Rationale:* carries the no-runtime-Git rule.
- **ENG-32** — Finish slice 5's production graph index over store segments so revset evaluation runs outside tests. *Rationale:* `read_gap` names it as the `log`/`query` blocker (G2).
- **ENG-33** — R2a part 2: hosted `log`/`query` on the ENG-32 index. *Rationale:* history reads need it (G2).
- **ENG-34** — History and branch-view pages and ops (a branch's changes grouped) on ENG-33. *Rationale:* settled branch-view feature.
- **ENG-35** — R2d: durable ordered op-log tail with a resumable subscription across `op.prune`. *Rationale:* the event spine for CI, webhooks, mirrors and the inbox (F3).
- **ENG-36** — Op-log consumer: one transaction for cursor + outbox, idempotent keys, broadcast for long-wait operations. *Rationale:* no lost events across restarts (F3).
- **ENG-37** — Webhooks: Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker, SSRF guard. *Rationale:* reliable, signed delivery that can't be aimed at internal hosts.
- **ENG-38** — In-app inbox fed by the ENG-36 outbox (no email delivery). *Rationale:* settles the identity/notification decision.
- **ENG-39** — Per-token and per-account token buckets in ENG-15 middleware with `Retry-After`. *Rationale:* many agents must not exhaust the daemon's admission budget.
- **ENG-40** — R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim/attestation cursor paging already exists. *Rationale:* tasks, review and CI depend on these kinds.
- **ENG-41** — Review feature: changes/stacks from `refs/for` with change-ids, branch view, approvals as attestations, merge via `land.execute`. *Rationale:* the settled review unit.
- **ENG-42** — Tasks feature replacing issues, claimable through Ruk leases. *Rationale:* settled feature.
- **ENG-43** — CI core: leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners, results as attestations. *Rationale:* durable, efficient CI that never opens hosted stores (G7, G8).
- **ENG-44** — Reuse `MergeNotice` unchanged and report effective mode + `language_set_version` on every mergeability and land result. *Rationale:* carries DES-01 / CEO-02.
- **ENG-45** — R4: `push` with credentials per call, mirror mode, ref selection, per-ref status on `mirror.rs`. *Rationale:* mirrors need it, and the mapping already exists (G10).
- **ENG-46** — Mirror worker: one run per mirror with dirty-flag rerun, reconcile, credentials only in memory, per-ref ff-only rejections. *Rationale:* collapses ref-update bursts and keeps credentials out of ruk.
- **ENG-47** — R3: bounded `search` method in the Bulk lane with deadline, result cap and cancellation, plus the rukie search op and page. *Rationale:* lightweight search that can't affect interactive traffic.
- **ENG-48** — Releases: tag-backed releases via hosted `tag.*` and streaming archive downloads. *Rationale:* settled feature.
- **ENG-49** — Social: explore, stars, watches and forks (via registry fork). *Rationale:* settled feature.
- **ENG-50** — Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation. *Rationale:* carries the repository half of slice 13.
- **ENG-51** — Retention and maintenance stress under rukie load; maintenance and backups only through daemon methods in the Maintenance lane. *Rationale:* slice 12 plus the single-writer lock (G8).
- **ENG-52** — Kill-9 fault suite (rukie and daemon) mid-push, mid-webhook, mid-CI-attest and mid-mirror; ruk crash/lock/IO smoke tests; shuttle cases. *Rationale:* carried gates that prove §7.
- **ENG-53** — Run the reference capacity profile against rukie + daemon as the release gate; it decides F4. *Rationale:* the carried release gate.

```jsonl
{"id":"ENG-01","description":"Present the reconciliation table as a dated revision proposal for AGENT_NATIVE_GIT_PLAN.md and edit that plan only for user-approved items.","phase":"engineering"}
{"id":"ENG-02","description":"After approval, add an ignore entry for the nested ruk/rukie/ clone and never delete it.","phase":"engineering"}
{"id":"ENG-03","description":"Under the settled Gitea-cut decision confirmed at the gate, make one reversible rukie commit deleting the Gitea tree, setting module github.com/gosuda/rukie, adding a minimal rukie serve, new Makefile, CI and AGENTS.md, and keeping LICENSE.","phase":"engineering"}
{"id":"ENG-04","description":"Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, go test -race and fuzz tests.","phase":"engineering"}
{"id":"ENG-05","description":"R1a: rename the daemon to ruk serve and the handshake profile to host, and reword Forgejo-specific daemon docs and tests.","phase":"engineering"}
{"id":"ENG-06","description":"R1b step 1: add a ruk host-credential subcommand to ruk's own CLI that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids, with named errors and a test asserting owner, group and mode.","phase":"engineering"}
{"id":"ENG-07","description":"R1b step 2: run the daemon as uid A and rukie as uid B with allowed_uids=[B], carry a fresh single-use per-connection challenge in the handshake accept reply, require an HMAC over (challenge, asserted_principal, method) keyed by the ENG-06 credential, refuse a reused challenge and any second assertion on a bound session, bind the principal to the daemon session, attribute sessions, leases, receipts and attestations to it, and test correct, missing and wrong HMACs, replayed opens, reused challenges, second assertions and refusal of non-allowed uids and of the allowed uid without a valid credential, documenting that same-uid-as-rukie processes are out of protocol scope.","phase":"engineering"}
{"id":"ENG-08","description":"R1c: one task per stream with a per-connection frame writer.","phase":"engineering"}
{"id":"ENG-09","description":"R1d: stream input/output in both directions under credit windows and spill received packs to disk.","phase":"engineering"}
{"id":"ENG-10","description":"R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout.","phase":"engineering"}
{"id":"ENG-11","description":"R1f: keep RequestClass pub(crate), add public ExecutorLane mirroring all five RequestClass variants (Control, Interactive, Bulk, Hook, Maintenance) with a Method-to-lane mapping derived from the dispatcher, classify argv CommandOpen streams by verb on the server side with unknown verbs on Bulk and never Hook or Control, reserve BoundedExecutor permits per lane held inside the spawn_blocking closure, and add cancellation, mapping-consistency and verb-table tests.","phase":"engineering"}
{"id":"ENG-12","description":"Keep ruk gates green (fmt, 3-OS clippy -D warnings, nextest, cargo test, Unix cfg gating, no #[allow], unreachable_pub clean, exact pins, --locked) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths.","phase":"engineering"}
{"id":"ENG-13","description":"Two independent processes with two systemd units and two service accounts, a uid-restricted socket and no child-spawn mode.","phase":"engineering"}
{"id":"ENG-14","description":"K3: SQLite WAL store module with forward-only migrations and a leased outbox/job table.","phase":"engineering"}
{"id":"ENG-15","description":"K2: Op[In,Out] registry on Huma v2 and net/http ServeMux deriving OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), the generated rukie CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots.","phase":"engineering"}
{"id":"ENG-16","description":"Auth: argon2id passwords with TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts, and admin-CLI password reset.","phase":"engineering"}
{"id":"ENG-17","description":"Users, orgs, teams and repository permissions enforced in the operation-table authz middleware.","phase":"engineering"}
{"id":"ENG-18","description":"Design tokens as CSS custom properties and base layouts, with each later feature shipping its zero-JS pages from its operations.","phase":"engineering"}
{"id":"ENG-19","description":"R2a part 1: tree and object readers serving hosted diff/show/file/blame, fix the stale daemon doc claiming op.log, claim.list and attest.list serve only first pages, and exclude status.","phase":"engineering"}
{"id":"ENG-20","description":"R2b: hosted preparation for bookmark.*, tag.*, land.execute, push, push.reconcile, fetch, unpin and op.prune.","phase":"engineering"}
{"id":"ENG-21","description":"Crash test proving lane receipts survive a daemon kill at each mutation step and that replay is a no-op.","phase":"engineering"}
{"id":"ENG-22","description":"R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs and compare/merge-base reads.","phase":"engineering"}
{"id":"ENG-23","description":"testruk harness running the real daemon that fails, never skips, when the binary is missing.","phase":"engineering"}
{"id":"ENG-24","description":"K4: pooled daemon client with semaphore, cancellation, schema codegen and a one-to-one RFC 9457/MCP error map, tested on testruk.","phase":"engineering"}
{"id":"ENG-25","description":"RUK_PIN plus a schema-digest drift gate and a handshake version check.","phase":"engineering"}
{"id":"ENG-26","description":"Repo hosting and browsing feature: create/rename/delete ops and tree, blob, commit and diff pages and API.","phase":"engineering"}
{"id":"ENG-27","description":"Typed git-wire methods git.upload_pack (v0+v2), git.receive_pack and git.upload_archive admitted in the Bulk lane.","phase":"engineering"}
{"id":"ENG-28","description":"Keep quarantine and all hook phases (pre-receive, update, post-receive, proc-receive, fail-closed timeouts) unchanged and add typed hosted handlers for each phase.","phase":"engineering"}
{"id":"ENG-29","description":"rukie Smart HTTP and SSH frontends (x/crypto/ssh) that authenticate via ENG-16, authorize via ENG-17 and forward byte streams to the git-wire methods.","phase":"engineering"}
{"id":"ENG-30","description":"Walking-skeleton e2e: stock git clone, fetch v2, push and push refs/for/main over HTTP and SSH through the ENG-29 frontends to the daemon.","phase":"engineering"}
{"id":"ENG-31","description":"Process-trace test proving no git process runs inside the rukie plus daemon boundary.","phase":"engineering"}
{"id":"ENG-32","description":"Finish the slice-5 production graph index over store segments so revset evaluation runs outside tests.","phase":"engineering"}
{"id":"ENG-33","description":"R2a part 2: hosted log/query on the production graph index.","phase":"engineering"}
{"id":"ENG-34","description":"History and branch-view pages and ops grouping a branch's changes.","phase":"engineering"}
{"id":"ENG-35","description":"R2d: durable ordered op-log tail with a resumable subscription across op.prune.","phase":"engineering"}
{"id":"ENG-36","description":"Op-log consumer with one transaction for cursor and outbox, idempotent keys and a broadcast for long-wait operations.","phase":"engineering"}
{"id":"ENG-37","description":"Webhook delivery with Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker and an SSRF guard.","phase":"engineering"}
{"id":"ENG-38","description":"In-app inbox fed by the outbox, with no email delivery.","phase":"engineering"}
{"id":"ENG-39","description":"Per-token and per-account token buckets in the operation-table middleware with Retry-After.","phase":"engineering"}
{"id":"ENG-40","description":"R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim and attestation cursor paging already exists.","phase":"engineering"}
{"id":"ENG-41","description":"Review feature: changes and stacks from refs/for with change-ids, branch view, approvals as attestations and merge via land.execute.","phase":"engineering"}
{"id":"ENG-42","description":"Tasks feature replacing issues, claimable through Ruk leases.","phase":"engineering"}
{"id":"ENG-43","description":"CI core with a leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners and results as attestations.","phase":"engineering"}
{"id":"ENG-44","description":"Reuse MergeNotice unchanged and report effective mode and language_set_version on every mergeability and land result.","phase":"engineering"}
{"id":"ENG-45","description":"R4: push with credentials per call, mirror mode, ref selection and per-ref status on mirror.rs.","phase":"engineering"}
{"id":"ENG-46","description":"Mirror worker with one run per mirror and dirty-flag rerun, reconcile, credentials only in memory and per-ref ff-only rejections.","phase":"engineering"}
{"id":"ENG-47","description":"R3: bounded search method in the Bulk lane with deadline, result cap and cancellation, plus the rukie search op and page.","phase":"engineering"}
{"id":"ENG-48","description":"Releases backed by hosted tag.* with streaming archive downloads.","phase":"engineering"}
{"id":"ENG-49","description":"Social features: explore, stars, watches and forks via registry fork.","phase":"engineering"}
{"id":"ENG-50","description":"Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation.","phase":"engineering"}
{"id":"ENG-51","description":"Retention and maintenance stress under rukie load, with maintenance and backups only through daemon methods in the Maintenance lane.","phase":"engineering"}
{"id":"ENG-52","description":"Kill-9 fault suite for rukie and daemon mid-push, mid-webhook, mid-CI-attest and mid-mirror, plus ruk crash/lock/IO smoke tests and shuttle cases.","phase":"engineering"}
{"id":"ENG-53","description":"Run the reference capacity profile against rukie and the daemon as the release gate, deciding the metadata store.","phase":"engineering"}
```

### Critical Files for Implementation
- `ruk/crates/ruk/src/main.rs` — the CLI that gains the `host-credential` provisioning subcommand (ENG-06) beside `serve`.
- `ruk/crates/ruk/src/daemon/ipc.rs` — the peer-uid check (533-550) and the handshake frames that ENG-07 extends with the single-use challenge.
- `ruk/crates/ruk/src/daemon/executor.rs` — the permit freed on cancel and the shared semaphore (G13, ENG-11).
- `ruk/crates/ruk-core/src/coord/admission.rs` — the private `RequestClass` (lines 33-41) and the dispatcher-only invariant behind the five-lane `ExecutorLane`.
- `ruk/crates/ruk/src/daemon/mod.rs` — the argv `CommandOpen` runner that needs the verb rule, plus buffered input, blocking streams, `InvocationOpen` (304-316), the outcome ledger and the stale read-coverage doc (612-614).

[advisor] <advisory advisor="minimax" severity="nit" guidance="weigh, don't blindly obey"> Renumber drift-gate reference: drift g…

---
# autoplan-eng.md: Engineering review of the rukie ground-up rewrite on Ruk (revision 16)

The plan is buildable, but not in the order the skeleton gives. The ruk daemon can't yet host a forge, so ruk's hosting work has to come before most rukie features. Two things block it today:

1. **Typed forge reads are missing.** Over the daemon, `diff`, `show`, `file`, `blame`, `log` and `query` are refused with `E_HOSTED`.
2. **Large transfers don't stream.** Git-wire traffic is buffered whole in memory, and one long command stalls every other stream on its connection.

So the first job is not "add forge read methods" (R2). It is to fix how the daemon hosts: build the typed reads, stream transfers, keep bulk work from taking all the threads, and accept per-user identity from the forge.

**Notes on this revision:**
- **Drift-gate references were already fixed in revision 15.** §2's CEO-01 row and §9's drift-check bullet both read ENG-25; §5.1's handshake row uses ENG-25. The advisory citing ENG-26 is stale.
- **Fixed (the reviewer was right):** §5.1's search row was self-referential (`Search (ENG-47) | … (ENG-47)`). It now names the Ruk capability (the R3 bounded `search` method, the ruk half of ENG-47, ordered before its rukie half) and the Bulk lane it rides on (ENG-11), with the rukie search op/page as the consumer.

**This file has not been written to disk.** This session is read-only plan mode with no write tool. The orchestrator needs to save this message as `autoplan-eng.md` and create `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` from the JSONL block in §12.

---

## 0. Restated problem

Choose the build order, the process layout and the reliability guarantees for two repositories that change together:
- **ruk:** Rust; workspace gates fmt / clippy / nextest / test.
- **rukie:** Go; today a 393k-LOC Gitea tree that becomes an agent-first forge using only the Ruk daemon as its repository backend.

Constraints:
- every commit builds in both repos;
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
- tests must run against a real daemon.

**Where the labels come from:**
- R1–R6, K1–K4, F, U, V: the draft plan outline in this assignment's context (not stored in any repo file).
- S0–S7: stages in §5.
- G1–G17: findings in §1.
- F1–F7: design forks in §3.

## 0a. Authorization boundary

- **This review authorizes nothing.** autoplan ends at a human approval gate. Every task starts only after that approval.
- **Deleting the Gitea tree (ENG-03)** rests on this assignment's settled decision: "Cut every Gitea surface (API v1, swagger, app.ini, xorm schema, Actions, packages, OAuth2 provider, Gitea webhook formats, builtin SSH code, gitea CLI, Vue/Vite/Tailwind, locales) … Land as commits on top of Gitea history; every commit builds." That text lives only in the assignment context, not in any workspace file; the user confirms it at the gate. It covers rukie only, and it is a reversible commit with history kept.
- **The existing ruk plan:** `ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:119-121` says no source cutover, data migration or source-copy purge is authorized by it, and forbids silent feature removal or storage substitution. So: changing that plan (ENG-01) is a proposal decided item by item; the nested `ruk/rukie/` clone is never deleted, and ENG-02 only proposes an ignore entry; ruk's redb store is untouched. The metadata store (F4) belongs to rukie.
- **No outward acts** (push, publish, name reservation) are proposed.

## 1. What the code shows today

| # | Finding | Evidence | Consequence |
|---|---|---|---|
| G1 | The daemon is wired as `Command::Forgejo { command: ForgejoCommand::Serve }`; the handshake profile is hard-coded `"forgejo"`. | `ruk/crates/ruk/src/main.rs:202-220`; `daemon/ipc.rs:643`; `daemon/tests.rs:322` | R1 is a rename plus a protocol change (ENG-05). |
| G2 | Typed hosted reads serve `op.log`, `claim.list`, `attest.list` with lease-bound cursor continuation (`read()` mints a 30-second cursor when a first page does not exhaust the listing and resumes it after binding checks). Every other read names its missing piece in `read_gap`: `status` needs a workspace autosnapshot and workcopy crawl; `log`/`query` need "a production graph index over store segments" because revset evaluation "builds its graph only in tests"; `diff`/`show`/`file`/`blame` need "tree and object readers over resolved revisions". | `ruk-core/src/coord/hosted.rs:539-560, 612-632, 1347-1362` | A forge doesn't need `status`. `diff`/`show`/`file`/`blame` need tree and object readers (ENG-19); `log`/`query` need the graph index (ENG-32, ENG-33). `daemon/mod.rs:612-614` still says "first pages are served", contradicting the coordinator; ENG-19 fixes that stale doc. The argv `CommandOpen` path returns untyped text and blocks other streams (G5), so browsing waits for ENG-19 instead of shipping on it. |
| G3 | Hosted mutations covered: ref, new, claim, ws.new, pin, attest, describe, split, rebase, squash, abandon, edit, ws.scope, ws.take, ws.drop, resolve, revert, undo, land.prepare. Not covered: `land.execute`, `push`, `push.reconcile`, `fetch`, `bookmark.*`, `tag.*`, `snapshot`, `write`, `op.prune`, `unpin`. | `daemon/mod.rs:627` | Merges, mirrors and releases are blocked on these (ENG-20). |
| G4 | Identity is derived from the peer uid alone (`Principal::from_peer_uid`, `ipc.rs:533-550` accepts every process with an allowed UID), and `InvocationOpen` (`daemon/mod.rs:304-316`) carries no assertion field, nonce or sequence; `session_id` and the mutation `request_id` are optional and reusable. | `daemon/mod.rs:2017,304-316`; `daemon/registry.rs:279`; `hosted.rs:590-596` | rukie's one uid would make every user and agent a single principal, any same-uid process could impersonate all of them, and a captured open frame could be replayed. The daemon needs an authenticated, replay-resistant host assertion under distinct service identities (ENG-06, ENG-07). |
| G5 | A long-running command blocks other streams on the same connection (documented v1 limitation); clone, fetch and archive arrive as argv `CommandOpen` streams. | `daemon/mod.rs` `command_inner` doc; `ruk-git/src/transport/mirror.rs:12` | Connections become the bottleneck (`DEFAULT_MAX_CONNECTIONS = 128`); the argv path has no `Method` to classify by (ENG-11). |
| G6 | Command input is buffered in full (up to 256 MiB) before running; output is captured, then streamed; pushed packs are staged into memory. | `daemon/mod.rs` consts / docs; `ruk-git/src/transport/hooks.rs:1-12` | RSS grows with pack size × concurrency; transfers must stream and spill to disk (ENG-09). |
| G7 | The reconnect outcome ledger is in memory (10,000 entries); durable receipts live in the lane. | `daemon/mod.rs`; `ruk/docs/coordinator.md` | rukie must persist request ids; whether receipts are durable on disk is **inference** from coordinator.md, tested by ENG-21. |
| G8 | Each repo's database (redb) allows one writer process; a second gets `DatabaseAlreadyOpen`. | `AGENT_NATIVE_GIT_PLAN.md:152` | The daemon is the only process that opens hosted stores. |
| G9 | Receive pipeline: quarantine, `pre-receive`/`update`/`post-receive`, fail-closed timeouts, and `proc-receive` diversion using git's exact conversation. | `hooks.rs:1-47`; `proc_receive.rs:1-44` | Git semantics that stay in ruk; rukie adds typed handlers per phase (ENG-28). |
| G10 | The `push --mirror` mapping and refspec pruning already exist. | `ruk-git/src/transport/mirror.rs:1-40` | R4 extends existing code (ENG-45). |
| G11 | Daemon limits: 64 KiB data frame, 1 MiB control frame, 256 KiB starting / 16 MiB maximum window, 16 blocking permits, 60 s per-frame timeout. | `daemon/ipc.rs:20-32`; `daemon/mod.rs:24-37` | Per-stream memory has a known upper bound; subscriptions need keepalives (ENG-10). |
| G12 | The admission classes exist and are private to ruk-core: `pub(crate) enum RequestClass { Control, Interactive, Bulk, Hook, Maintenance }`, documented "The dispatcher selects it; a client never does"; the scheduler has separate limits per class; Bulk leases cancel after 30 s without progress and are capped at 2 h. | `ruk-core/src/coord/admission.rs:33-41` (read directly); `admission/scheduler.rs:30-35,222-226`; `leases/timing.rs:15-22` | Don't add a class. The ruk-core dispatcher maps typed methods to classes; the daemon crate can't name the class, so it needs a public `ExecutorLane` mirroring all five variants plus its own verb rule for argv streams (ENG-11). |
| G13 | The executor has two defects: one shared semaphore for every class, so per-class admission is undone at the thread pool; and `run` acquires the permit outside `spawn_blocking` and drops it only after awaiting the join handle, so a cancelled caller releases the permit while `work` keeps running. | `ruk/crates/ruk/src/daemon/executor.rs` (`run`) | Blocking concurrency and memory can exceed their bound under client cancellations; fixed and tested in ENG-11. |
| G14 | ruk CI: fmt; clippy `--workspace --all-targets --all-features --locked -D warnings` on 3 OSes; nextest 0.9.148 on 3 OSes; `cargo test` on 3 OSes; toolchain 1.99.1. Miri, fuzz, soak and store gates are deferred. Lints deny `allow_attributes`, `unsafe_code`, `todo`, `unimplemented`, `dbg_macro`; `unreachable_pub` warns; exact `=` pins with `--locked`. The existing plan's Miri gate: `cargo +nightly miri test -p ruk-core store::segment`. | `ruk/.github/workflows/ci.yml`; `ruk/Cargo.toml`; `ruk/rust-toolchain.toml`; `AGENT_NATIVE_GIT_PLAN.md` "Verification gates" | Unix-only daemon code must pass Windows clippy without `#[allow]`; a new public ruk-core type needs a real outside user or `unreachable_pub` plus `-D warnings` fails CI; the deferred gates are still owed (ENG-12). |
| G15 | `ruk/rukie/` is a clone of `https://github.com/gosuda/rukie` inside the ruk tree, and ruk's `.gitignore` does not exclude it. | `ruk/rukie/.git/config`; `ruk/.gitignore` | Risk of committing a gitlink; never delete it. |
| G16 | `ruk/tasks/autoplan-ledger.jsonl` holds `ENG-01..06` from the earlier autoplan run; `ruk/.gitignore` ignores `tasks/`; there is no `tasks/` directory at the workspace root. | that file; `ruk/.gitignore`; workspace root listing | This run's ledger is `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`: a new file, no id collision. `ruk/tasks/autoplan-ledger.jsonl` is left alone. |
| G17 | `AGENT_NATIVE_GIT_PLAN.md` targets Forgejo parity, forbids runtime Git, gates release on a capacity profile, and requires explicit revision for any removal. | `AGENT_NATIVE_GIT_PLAN.md:5-14,71-99,115-121` | §2 is a revision proposal, not a supersession. |

## 2. Proposed reconciliation with `AGENT_NATIVE_GIT_PLAN.md` (decided at the gate)

| Existing plan item | Proposed verdict | Carried form |
|---|---|---|
| Slices 1–8 | Carry unchanged | Slice 5's production graph index becomes a scheduled task (ENG-32) because hosted `log`/`query` need it. |
| Slice 9 | Carry; backend half adapted | ENG-19, ENG-20, ENG-22, ENG-33. |
| Slice 10 | Adapt; dropping the Forgejo Go patch needs approval | `ruk serve` plus the rukie client (ENG-05..07, ENG-11, ENG-24). |
| Slice 11 | Carry in ruk; forge side adapted | Hook phases stay (ENG-28); mirrors become ENG-45; dropping wiki/template flows needs approval. |
| Slice 12 | Carry | ENG-51. |
| Slice 13 | Repository half carried; Forgejo-database half pending approval | ENG-50. |
| Slice 14 + release gate | Carry | ENG-53. |
| No runtime Git | Carry | ENG-31. |
| CEO-01 frozen Forgejo inventory | Proposed superseded, approval needed | Replaced by the ENG-25 drift gate. |
| DES-01 one `MergeNotice` record; CEO-02 effective mode + `language_set_version` | Carry | ENG-44. |
| Verification gates: Miri, fuzz, shuttle, crash smoke tests, out-of-process harness | Carry | ENG-12 (fuzz + Miri), ENG-23, ENG-52. |

Every item stays in force until the user approves a change.

## 3. Hypotheses on the major forks

### F1 — Process layout
- **H1a (recommended):** two independent processes over a Unix socket under two dedicated service accounts (F7): the daemon as uid A, rukie as uid B, `allowed_uids = [B]`. rukie terminates HTTP and SSH and forwards byte streams to typed git-wire methods. *Disproved if* deploys must be a single binary *and* rukie restarts never need to keep transfers alive.
- **H1b:** rukie spawns the daemon as a child. Rejected: deploys kill in-flight transfers, and it means two runtime paths.

### F2 — Push policy and refs/for
- **H2a (recommended):** the receive pipeline stays; hosted mode registers typed handlers: ref policy at `pre-receive`/`update`, change-id change creation at `proc-receive`, a wake-up hint at `post-receive`. Script hooks remain the CLI and self-hosted default. *Disproved if* the handlers can't see quarantined objects through the view scripts get.
- **H2b:** delete the hook phases. Rejected: breaks slice 11 parity.

### F3 — How ref updates reach CI, webhooks and mirrors durably
- **H3a (recommended):** a stored op-log position plus outbox rows, advanced in one transaction; `post-receive` is only a wake-up hint. *Disproved if* `op.log` can't be paged in order from a durable position across `op.prune`.
- **H3b:** a push-time callback inserts the jobs. Rejected: events are lost while rukie is down.

### F4 — rukie metadata store (open)
- **H4a (recommended):** SQLite WAL, pure Go, behind a SQL-shaped store module. *Disproved by* the capacity gate (ENG-53).
- **H4b (runner-up):** PostgreSQL, which the existing plan uses for capacity acceptance (`:117`).
- **H4c:** Pebble. Rejected: no SQL.

### F5 — Deleting the Gitea tree
- **H5a (recommended):** one reversible commit.
- **H5b:** staged deletion. Rejected: every intermediate commit is a gutted Gitea that still has to build, with no value.

### F7 — Host principal assertion
- **Deployment:** distinct identities. The daemon runs as uid A; rukie runs as uid B; `allowed_uids = [B]`. Credential provisioning is a **ruk-side CLI subcommand** (`ruk host-credential`, added in ENG-06 before any handshake work, depending on nothing later), writing the random secret as a 0640 file owned by A in a dedicated group whose only members are A and B. Rukie fails at boot with a named error when it cannot read it; the daemon fails at startup when it cannot. A same-account 0600 file is rejected because any same-uid process reads it.
- **Guarantee, stated honestly:** the uid gate plus the credential prevents impersonation by any process outside {A, B}. It does not separate a process running as rukie's uid (B): that process is inside rukie's trust boundary and can ptrace rukie, read its memory and steal sessions and tokens regardless of this layer. User-attribution separation within one uid is not attainable in userspace; the deployment requirement is that only rukie runs as B. A pid → executable check over `SO_PEERCRED` remains optional defense-in-depth, noted as weak.
- **H7a (recommended):** identity additionally carries a proof with daemon-issued freshness:
  1. the handshake accept reply carries a fresh, cryptographically random, single-use per-connection challenge;
  2. a principal assertion is an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the host credential;
  3. the daemon records consumed challenges for the life of the connection and refuses any reuse; it also refuses any second assertion on a session that is already bound;
  4. the asserted principal is bound to the daemon-issued session, so later frames on that session never re-assert.

  Consequences: the same assertion bytes are never accepted twice; a captured open frame replayed on another connection fails (different challenge); replaying frames on the same connection after binding cannot re-assert identity; invocation-level replay of bound-session frames remains the intended request-id idempotency path. Sessions, leases, receipts and attestations attribute to the bound principal.
- **H7b:** accept bare assertions from allowed uids. Rejected: any allowed-uid process could impersonate every user and forge the audit trail.
- **H7c:** HMAC over a client-chosen nonce only. Rejected: the client can replay its own nonce.
- **H7d:** same service account with a 0600 file. Rejected: no peer separation, and a daemon-created 0600 file is unreadable by a differently-owned rukie.

### F6 — Executor isolation across the crate boundary
- **H6a (recommended):** `RequestClass` stays `pub(crate)`; ruk-core exposes a public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) plus a `Method` → lane mapping derived from the dispatcher's class choice; the daemon classifies argv `CommandOpen` streams by verb on the server side (table below), never emitting Hook or Control; `BoundedExecutor` reserves permits per lane and holds each permit inside the `spawn_blocking` closure. *Disproved if* all blocking work could move into ruk-core's admitted path; the argv runner and git-wire serving live in the daemon crate, so it can't.
- **H6b:** make `RequestClass` public. Rejected: it would let clients choose their own class, against `admission.rs:33`.
- **H6c:** one executor per class. Rejected: idle threads, against the executor's design (`executor.rs` header).
- **H6d:** a three-variant `ExecutorLane`. Rejected: hook work would land on Bulk permits and exceed that class's limit.

Verb rule for argv streams:

| Verbs | Lane |
|---|---|
| `upload-pack`, `receive-pack`, `upload-archive`, `archive`, `fetch`, `push`, `pack-objects`, `index-pack`, `grep`, `blame` | Bulk |
| `gc`, `repack`, `prune`, `maintenance`, `fsck` | Maintenance |
| Any other listed verb | Interactive |
| Unknown verb | Bulk |
| *(never produced by argv)* | Hook, Control |

## 4. Six-principle register (engineering)

| Principle | Decision | Open question |
|---|---|---|
| **Clarity** | One operation table (ENG-15) derives HTML, JSON, MCP and CLI. One wire contract (schema codegen + digest gate). One git-wire method family. One event spine. The daemon is the only process opening stores. One receive pipeline with script or typed handlers. Admission classes chosen server-side only; the executor sees all five lanes. Host identity = distinct service uids + a ruk-CLI-provisioned group-restricted credential + an HMAC'd assertion over a daemon-issued single-use challenge, with the guarantee scoped to outside-{A,B} processes (F7). Plan changes are proposals. | — |
| **Impact** | Fix streaming, per-stream concurrency and executor isolation (G5, G6, G13) first. Tree/object readers unlock file, commit and diff pages. The graph index (ENG-32) is the long pole for history and branch views. | — |
| **Audience** | Typed errors map one-to-one to RFC 9457 / MCP with `Retry-After`. `MergeNotice` and effective mode on every merge result. Zero-JS HTML from the same operations. Agents use scoped tokens on human accounts (ENG-16). | Runner credential class. |
| **Risk** | Pack memory; principal impersonation by outside processes (F7, ENG-07), with same-uid processes named as out of scope; lost events; the single-writer store lock; a cancelled request freeing its permit while its work keeps running, and argv transfers bypassing lanes (G13, ENG-11); leaking `RequestClass` to clients; SSRF; cross-repo drift; runtime Git; unauthorized plan or destructive changes (§0a). | Lane receipt durability (ENG-21). |
| **Sequencing** | Tasks are numbered in stage order (§5); each depends only on earlier tasks. Provisioning (ENG-06) precedes the handshake (ENG-07), both in ruk's CLI, so S1 has no circular dependency on rukie. Store → operation table → auth → users/orgs before any authenticated feature. Git wire → hooks → frontends → walking skeleton. Graph index → `log`/`query` → history pages → review. Capacity is the final release gate. | — |
| **Reversibility** | `protocol_major` bump plus `RUK_PIN`; forward-only migrations with backup restore; Gitea deletion revertible; PostgreSQL fallback; script hooks can be re-enabled per repo; `ExecutorLane` and the verb table are small and easy to change before release; the host credential can be rotated by regenerating the file. | — |

## 5. Amended dependency order

### 5.1 Which ruk capability blocks which rukie feature


[Showing lines 8034-8372 of 8940 (49.9KB limit). Use :8373 to continue]

| rukie feature (task) | Blocking ruk capability (task) | Today |
|---|---|---|
| Auth, users, orgs, teams, tokens (ENG-16, ENG-17) | none | — |
| Any repo-touching feature | `ruk serve`, credential provisioning, authenticated host assertion, handshake check (ENG-05, ENG-06, ENG-07, ENG-25) | missing |
| Repo hosting and browsing (ENG-26) | Tree/object readers + tree / refs / compare + lifecycle (ENG-19, ENG-22) | E_HOSTED |
| History, branch view (ENG-34) | Graph index → `log`/`query` (ENG-32, ENG-33) | E_HOSTED; graph built only in tests |
| HTTP / SSH clone & push (ENG-29) | Per-stream tasks, streaming, spill, lanes, git-wire methods, hook handlers (ENG-08, ENG-09, ENG-11, ENG-27, ENG-28) | buffered; permit freed early |
| Webhooks, inbox (ENG-37, ENG-38) | Op-log tail + keepalive (ENG-10, ENG-35) | 60 s frame timeout kills subscriptions |
| Review (ENG-41) | Hook handlers (ENG-28), hosted `land.execute` (ENG-20), R6 extensions (ENG-40) | script-only / `land.execute` uncovered |
| Tasks (ENG-42) | R6 claim extensions (ENG-40); `claim.*` and its cursor paging already exist | lease semantics missing |
| CI (ENG-43) | R6 attestation kinds (ENG-40); `attest.add` and cursor paging already exist | kinds missing |
| Mirrors (ENG-46) | Hosted `push` / `push.reconcile` (ENG-20) + R4 (ENG-45) | uncovered |
| Search op and page (ENG-47, rukie half) | R3 bounded `search` method (ENG-47, ruk half, built first) riding the Bulk lane (ENG-11) | missing |
| Releases (ENG-48) | Hosted `tag.*` (ENG-20) + streaming archive (ENG-27) | uncovered |
| Social and forks (ENG-49) | Registry fork (ENG-22) | missing |
| Repo import (ENG-50) | Offline Git conversion (existing ruk CLI; the admin wrapper is ENG-50's own half) | CLI only |

### 5.2 Stages (each stage = commits that each build; all start after the gate)

| Stage | Tasks | Exit proof |
|---|---|---|
| S0 | ENG-01..04: gate items, K1, Go gates | both CIs green |
| S1 | ENG-05..18: R1 daemon work (rename, provisioning, assertion, per-stream, streaming, keepalive, lanes, ruk gates), topology, K3 store, K2 operation table, auth, users/orgs/teams, UI tokens | soak test + fuzz + Miri; executor and verb-lane tests; provisioning and assertion tests; login and token flows on all four surfaces |
| S2 | ENG-19..26: tree/object reads, mutations, receipts, lifecycle, `testruk`, K4 client, drift gate, repo browsing | real-daemon integration tests; browse a repo end to end |
| S3 | ENG-27..31: git wire → hook handlers → frontends → walking skeleton → no-Git trace | **walking skeleton:** stock git clone / push / `push refs/for/main` through rukie |
| S4 | ENG-32..38: graph index, `log`/`query`, history pages, op-log tail, consumer, webhooks, inbox | kill-restart test: no lost or duplicate deliveries |
| S5 | ENG-39..44: token buckets, R6, review, tasks, CI, MergeNotice | end-to-end agent flow (claim → push → CI attest → land) |
| S6 | ENG-45..51: R4, mirror worker, search (ruk method then rukie op/page), releases, social, import, retention stress | §8 targets |
| S7 | ENG-52..53: fault suite, capacity profile | release gate |

## 6. Concurrency and backpressure
- **Daemon:** one task per stream and one writer per connection, bounded by credit windows (`ipc.rs:29-32`).
- **Admission and executor (ENG-11):** typed methods use the dispatcher's class and the derived five-variant `ExecutorLane`; argv streams use the verb rule with unknown verbs on Bulk and never Hook or Control; permits are reserved per lane and held inside the `spawn_blocking` closure.
- **Identity:** distinct service accounts (A = daemon, B = rukie), `allowed_uids = [B]`, group-restricted credential provisioned by `ruk host-credential` (ENG-06), and the HMAC'd host assertion over a daemon-issued single-use challenge (ENG-07, F7); the guarantee covers processes outside {A, B}.
- **rukie client:** a pool of about 4–8 connections with an invocation semaphore below the daemon's limits; cancelling a request's context cancels its stream; `E_LIMIT` maps to 429/503 with `Retry-After`; only idempotent reads are retried automatically.
- **Per-token limits:** token buckets per token and per account in the operation-table middleware (ENG-39).
- **Ref races:** the lane's compare-and-swap (`E_STALE`) is sent back as a typed conflict; no rukie-side locks.
- **Long waits for agents:** served from an in-process broadcast fed by the event consumer, not by polling the daemon; HTML stays zero-JS.

## 7. Durability of CI, webhook and mirror queues
1. **One spine:** one transaction reads ops from the stored op-log position, inserts outbox rows unique on `(repo, op_id, target)`, and advances the cursor; delivery is at-least-once with handlers safe to run twice.
2. **Job claiming:** rows carry leases and attempt counts; claims are atomic `UPDATE … RETURNING`; retries back off with jitter; failed jobs end in a dead-letter state.
3. **Webhooks:** Standard Webhooks signatures; `webhook-id` = outbox key; circuit breaker per endpoint; resolve-and-pin SSRF guard.
4. **Mirrors:** one run at a time with a dirty-flag rerun; periodic reconcile; credentials only in memory; per-ref fast-forward-only rejections.
5. **CI:** the job DAG is stored in metadata with leases and heartbeats; cache key = hash(inputs + spec + image digest); logs are written in chunks to files; the request id is persisted before `attest.add`; runners fetch over the git wire only.

## 8. Performance budgets

**Release gate:** the reference capacity profile (`AGENT_NATIVE_GIT_PLAN.md:115-121`), run by ENG-53.

| Path | Interim target (until measured) |
|---|---|
| Small daemon read over the socket | p99 ≤ 2 ms |
| Metadata-only API operation | p95 ≤ 20 ms |
| Tree / blob / commit page | p95 ≤ 50 ms |
| HTML page weight | ≤ 50 KB gzip, 0 B JS |
| Clone throughput | ≥ 80% of `git http-backend` |
| Push / clone memory | RSS growth independent of pack size beyond about 16 MiB per stream, including under cancel storms (ENG-11) |
| Ref update → webhook delivered | p95 ≤ 2 s |
| Ref update → mirror started | p95 ≤ 5 s |
| Idle CI queue → job started | ≤ 1 s |
| Search | deadline 2 s, capped results, cancellable |
| Bulk saturation | Interactive p95 unchanged while the Bulk lane is at its limit |

## 9. Testing strategy
- **Real daemon, no mocks of ruk:** `testruk` (ENG-23) starts `ruk serve` from `RUK_BIN` and fails, never skips, when it is missing; `make test` builds ruk at `RUK_PIN`.
- **Drift check (ENG-25):** schema regeneration and digest check plus a handshake version check.
- **Stock git and OpenSSH as clients** outside the boundary (ENG-30, after the ENG-29 frontends), plus a trace proving no `git` runs inside it (ENG-31).
- **Provisioning and assertion tests (ENG-06, ENG-07):** the subcommand creates the file with the exact owner, group, and mode; boot fails with a named error when the credential is missing; correct HMAC accepted; missing and wrong HMAC refused; the same assertion bytes replayed on the same and on a fresh connection refused (consumed challenge / challenge mismatch); a second assertion on a bound session refused; a peer with a non-allowed uid refused by the gate; a peer with the allowed uid but no valid credential refused.
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.
- **Operation-table tests (ENG-15):** each registered op is reachable and consistent through HTML form, JSON, MCP and CLI from one definition.
- **Fault and concurrency tests:** kill-9 suite, ruk crash/lock/IO smoke tests, shuttle (ENG-52); 60 s fuzz of frame, pkt-line and git-wire decoders plus Miri (ENG-12).
- **Go gates (ENG-04) and ruk gates (ENG-12):** gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz; fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`.

## 10. Repo migration and module path (after the gate)
- **K1 (ENG-03) is one reversible commit** on top of Gitea history: remove the Gitea tree; add `module github.com/gosuda/rukie` (origin `https://github.com/gosuda/rukie`), Go 1.27 / go1.27.2, a fresh `go.sum`, a minimal `rukie serve`, a new Makefile, new CI and a new AGENTS.md.
- **License:** keep the MIT `LICENSE` with Gitea's notice (whether a rewritten tree still needs it is **inference**).
- **No `gitea.dev` aliases.**

## 11. Open questions for the approval gate
1. **rukie metadata store:** SQLite-first with PostgreSQL fallback (recommended) vs PostgreSQL from the start.
2. **CI runner credentials** under "no agent principals".
3. **Host reads without sessions** vs per-principal session pooling.
4. **User script hooks in hosted repos,** or typed handlers only.
5. **Approve item by item** the §2 rows marked "needs approval": the Forgejo patch, wiki/template flows, the Forgejo-database half of migration, CEO-01.
6. **Approve** the ENG-02 ignore entry for `ruk/rukie/`.
7. **Confirm the settled Gitea-cut decision** quoted in §0a. It exists only in the assignment context.
8. **Accept the F7 boundary:** the assertion scheme protects against processes outside {A, B}; same-uid-as-rukie protection is a deployment requirement (only rukie runs as B), not a protocol guarantee.
9. **Ledger target:** `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` (new file, no id collision). `ruk/tasks/autoplan-ledger.jsonl` is not touched.

## 12. ENG tasks (stage order; one line each: id, description, rationale; all start after the gate)

- **ENG-01** — Present the §2 reconciliation as a dated revision proposal for `AGENT_NATIVE_GIT_PLAN.md` and edit that plan only for approved items. *Rationale:* G17 and §0a require an explicit revision rather than a silent supersession.
- **ENG-02** — After approval, add an ignore entry for the nested `ruk/rukie/` clone; never delete it. *Rationale:* avoids committing a gitlink (G15) without purging anything.
- **ENG-03** — K1 under the settled Gitea-cut decision confirmed at the gate: one reversible rukie commit deleting the Gitea tree, module `github.com/gosuda/rukie`, minimal `rukie serve`, new Makefile, CI and AGENTS.md, LICENSE kept. *Rationale:* a staged deletion has no value, and the module path and agent rules must change together (F5).
- **ENG-04** — Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz tests. *Rationale:* every later rukie commit is gated from the start.
- **ENG-05** — R1a: rename to `ruk serve` and profile `host`, and reword Forgejo-specific daemon docs and tests. *Rationale:* the command and profile are Forgejo-named today (G1).
- **ENG-06** — R1b step 1: add a `ruk host-credential` subcommand to ruk's own CLI (`crates/ruk/src/main.rs`, beside `serve`) that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids (paths configurable), with named errors on failure and a test asserting owner, group and mode. *Rationale:* provisioning is ruk-side infrastructure the handshake (ENG-07) consumes; placing it here avoids a circular dependency on rukie's later CLI layer (F7).
- **ENG-07** — R1b step 2: run the daemon as uid A and rukie as uid B with `allowed_uids = [B]`; carry a fresh single-use per-connection challenge in the handshake accept reply; require an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the ENG-06 credential; refuse a reused challenge and any second assertion on a bound session; bind the principal to the daemon session and attribute sessions, leases, receipts and attestations to it; test correct/missing/wrong HMACs, replayed opens on the same and fresh connections, reused challenges, second assertions, refusal of non-allowed uids and of the allowed uid without a valid credential. Document that same-uid-as-rukie processes are out of protocol scope. *Rationale:* a bare uid assertion permits impersonation and a client-chosen nonce permits replay; the guarantee must match what the kernel enforces (G4, F7).
- **ENG-08** — R1c: one task per stream with a per-connection frame writer. *Rationale:* removes the documented stream blocking (G5).
- **ENG-09** — R1d: stream input/output in both directions under credit windows and spill received packs to disk. *Rationale:* buffered packs make RSS scale with pack size (G6).
- **ENG-10** — R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout. *Rationale:* subscriptions would otherwise be timed out (G11).
- **ENG-11** — R1f, in one change: keep `RequestClass` `pub(crate)`; add public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) with a `Method` → lane mapping derived from the dispatcher; classify argv `CommandOpen` streams by verb server-side (transfer/grep/blame → Bulk, gc/repack/prune/maintenance/fsck → Maintenance, unknown → Bulk, never Hook or Control); reserve `BoundedExecutor` permits per lane with each permit held inside the `spawn_blocking` closure; add cancellation, mapping-consistency and verb-table tests. *Rationale:* covers typed methods and argv transfers, keeps hook limits intact, and stops early permit release (G5, G12, G13, F6).
- **ENG-12** — Keep ruk gates green (fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths. *Rationale:* G14 lists owed gates that no other task carries.
- **ENG-13** — Two independent processes with two systemd units and two service accounts, uid-restricted socket, no child-spawn mode. *Rationale:* transfers survive rukie deploys, and the split accounts give the F7 boundary.
- **ENG-14** — K3: SQLite WAL store module, forward-only migrations, leased outbox/job table. *Rationale:* transactional cursor plus outbox needs SQL, and PostgreSQL stays possible (F4).
- **ENG-15** — K2: the `Op[In,Out]` registry on Huma v2 + net/http ServeMux, producing OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), generated `rukie <op> --json` CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots. *Rationale:* every feature, frontend and limit sits on it.
- **ENG-16** — Auth: argon2id passwords + TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts (no agent principals), admin-CLI password reset. *Rationale:* required by every authenticated op and the git frontends.
- **ENG-17** — Users, orgs, teams and repository permissions, enforced in the ENG-15 authz middleware. *Rationale:* authorization for ops and git access.
- **ENG-18** — Design tokens as CSS custom properties and base layouts; each later feature ships its zero-JS pages from its operations. *Rationale:* avoids a big-bang UI phase.
- **ENG-19** — R2a part 1: tree and object readers serving hosted `diff`/`show`/`file`/`blame`; fix the stale `daemon/mod.rs:612-614` doc that says `op.log`/`claim.list`/`attest.list` serve only first pages; exclude `status`. *Rationale:* the real read gap in `read_gap` (hosted.rs:1347-1362); a forge has no workcopy (G2).
- **ENG-20** — R2b: hosted preparation for `bookmark.*`, `tag.*`, `land.execute`, `push`, `push.reconcile`, `fetch`, `unpin`, `op.prune`. *Rationale:* merges, mirrors and releases depend on them (G3).
- **ENG-21** — Crash test proving lane receipts survive a daemon kill at each mutation step and replay is a no-op. *Rationale:* safe retries rest on durable receipts (G7).
- **ENG-22** — R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs, compare/merge-base. *Rationale:* hosting, browsing and forks.
- **ENG-23** — `testruk` harness running the real daemon that fails, never skips, when the binary is missing. *Rationale:* integration tests need a real daemon without false green.
- **ENG-24** — K4: pooled client, semaphore, cancellation, schema codegen, one-to-one RFC 9457 / MCP error map, tested on `testruk`. *Rationale:* backpressure and typed errors for agents.
- **ENG-25** — `RUK_PIN` + schema-digest drift gate + handshake version check. *Rationale:* keeps both repos building together across breaking ruk changes; replaces the frozen Forgejo inventory (CEO-01) as the cross-repo contract.
- **ENG-26** — Repo hosting and browsing feature: create/rename/delete ops, tree, blob, commit and diff pages and API, on ENG-19/22/24. *Rationale:* first repo-facing feature, proving the operation table end to end.
- **ENG-27** — Typed git-wire methods `git.upload_pack` (v0+v2), `git.receive_pack`, `git.upload_archive`, admitted in the Bulk lane. *Rationale:* one streaming contract for HTTP and SSH (F1).
- **ENG-28** — Keep quarantine and all hook phases (`pre-receive`, `update`, `post-receive`, `proc-receive`, fail-closed timeouts) unchanged; add typed hosted handlers for each phase. *Rationale:* preserves slice 11 parity and git semantics while removing script dispatch for the forge (G9, F2).
- **ENG-29** — rukie Smart HTTP + SSH frontends (`x/crypto/ssh`) that authenticate via ENG-16, authorize via ENG-17, and forward byte streams to the git-wire methods. *Rationale:* authentication stays in rukie, pack logic in ruk (F1).
- **ENG-30** — Walking-skeleton e2e: stock git clone, fetch v2, push and `push refs/for/main` over HTTP and SSH through the ENG-29 frontends to the daemon. *Rationale:* retires the biggest integration risk once its parts exist.
- **ENG-31** — Process-trace test proving no `git` process runs inside the rukie + daemon boundary. *Rationale:* carries the no-runtime-Git rule.
- **ENG-32** — Finish slice 5's production graph index over store segments so revset evaluation runs outside tests. *Rationale:* `read_gap` names it as the `log`/`query` blocker (G2).
- **ENG-33** — R2a part 2: hosted `log`/`query` on the ENG-32 index. *Rationale:* history reads need it (G2).
- **ENG-34** — History and branch-view pages and ops (a branch's changes grouped) on ENG-33. *Rationale:* settled branch-view feature.
- **ENG-35** — R2d: durable ordered op-log tail with a resumable subscription across `op.prune`. *Rationale:* the event spine for CI, webhooks, mirrors and the inbox (F3).
- **ENG-36** — Op-log consumer: one transaction for cursor + outbox, idempotent keys, broadcast for long-wait operations. *Rationale:* no lost events across restarts (F3).
- **ENG-37** — Webhooks: Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker, SSRF guard. *Rationale:* reliable, signed delivery that can't be aimed at internal hosts.
- **ENG-38** — In-app inbox fed by the ENG-36 outbox (no email delivery). *Rationale:* settles the identity/notification decision.
- **ENG-39** — Per-token and per-account token buckets in ENG-15 middleware with `Retry-After`. *Rationale:* many agents must not exhaust the daemon's admission budget.
- **ENG-40** — R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim/attestation cursor paging already exists. *Rationale:* tasks, review and CI depend on these kinds.
- **ENG-41** — Review feature: changes/stacks from `refs/for` with change-ids, branch view, approvals as attestations, merge via `land.execute`. *Rationale:* the settled review unit.
- **ENG-42** — Tasks feature replacing issues, claimable through Ruk leases. *Rationale:* settled feature.
- **ENG-43** — CI core: leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners, results as attestations. *Rationale:* durable, efficient CI that never opens hosted stores (G7, G8).
- **ENG-44** — Reuse `MergeNotice` unchanged and report effective mode + `language_set_version` on every mergeability and land result. *Rationale:* carries DES-01 / CEO-02.
- **ENG-45** — R4: `push` with credentials per call, mirror mode, ref selection, per-ref status on `mirror.rs`. *Rationale:* mirrors need it, and the mapping already exists (G10).
- **ENG-46** — Mirror worker: one run per mirror with dirty-flag rerun, reconcile, credentials only in memory, per-ref ff-only rejections. *Rationale:* collapses ref-update bursts and keeps credentials out of ruk.
- **ENG-47** — R3, in order: first the bounded `search` method in ruk (Bulk lane, deadline, result cap, cancellation), then the rukie search op and page on top of it. *Rationale:* lightweight search that can't affect interactive traffic; the ruk half is the capability the rukie half consumes.
- **ENG-48** — Releases: tag-backed releases via hosted `tag.*` and streaming archive downloads. *Rationale:* settled feature.
- **ENG-49** — Social: explore, stars, watches and forks (via registry fork). *Rationale:* settled feature.
- **ENG-50** — Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation. *Rationale:* carries the repository half of slice 13.
- **ENG-51** — Retention and maintenance stress under rukie load; maintenance and backups only through daemon methods in the Maintenance lane. *Rationale:* slice 12 plus the single-writer lock (G8).
- **ENG-52** — Kill-9 fault suite (rukie and daemon) mid-push, mid-webhook, mid-CI-attest and mid-mirror; ruk crash/lock/IO smoke tests; shuttle cases. *Rationale:* carried gates that prove §7.
- **ENG-53** — Run the reference capacity profile against rukie + daemon as the release gate; it decides F4. *Rationale:* the carried release gate.

```jsonl
{"id":"ENG-01","description":"Present the reconciliation table as a dated revision proposal for AGENT_NATIVE_GIT_PLAN.md and edit that plan only for user-approved items.","phase":"engineering"}
{"id":"ENG-02","description":"After approval, add an ignore entry for the nested ruk/rukie/ clone and never delete it.","phase":"engineering"}
{"id":"ENG-03","description":"Under the settled Gitea-cut decision confirmed at the gate, make one reversible rukie commit deleting the Gitea tree, setting module github.com/gosuda/rukie, adding a minimal rukie serve, new Makefile, CI and AGENTS.md, and keeping LICENSE.","phase":"engineering"}
{"id":"ENG-04","description":"Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, go test -race and fuzz tests.","phase":"engineering"}
{"id":"ENG-05","description":"R1a: rename the daemon to ruk serve and the handshake profile to host, and reword Forgejo-specific daemon docs and tests.","phase":"engineering"}
{"id":"ENG-06","description":"R1b step 1: add a ruk host-credential subcommand to ruk's own CLI that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids, with named errors and a test asserting owner, group and mode.","phase":"engineering"}
{"id":"ENG-07","description":"R1b step 2: run the daemon as uid A and rukie as uid B with allowed_uids=[B], carry a fresh single-use per-connection challenge in the handshake accept reply, require an HMAC over (challenge, asserted_principal, method) keyed by the ENG-06 credential, refuse a reused challenge and any second assertion on a bound session, bind the principal to the daemon session, attribute sessions, leases, receipts and attestations to it, and test correct, missing and wrong HMACs, replayed opens, reused challenges, second assertions and refusal of non-allowed uids and of the allowed uid without a valid credential, documenting that same-uid-as-rukie processes are out of protocol scope.","phase":"engineering"}
{"id":"ENG-08","description":"R1c: one task per stream with a per-connection frame writer.","phase":"engineering"}
{"id":"ENG-09","description":"R1d: stream input/output in both directions under credit windows and spill received packs to disk.","phase":"engineering"}
{"id":"ENG-10","description":"R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout.","phase":"engineering"}
{"id":"ENG-11","description":"R1f: keep RequestClass pub(crate), add public ExecutorLane mirroring all five RequestClass variants (Control, Interactive, Bulk, Hook, Maintenance) with a Method-to-lane mapping derived from the dispatcher, classify argv CommandOpen streams by verb on the server side with unknown verbs on Bulk and never Hook or Control, reserve BoundedExecutor permits per lane held inside the spawn_blocking closure, and add cancellation, mapping-consistency and verb-table tests.","phase":"engineering"}
{"id":"ENG-12","description":"Keep ruk gates green (fmt, 3-OS clippy -D warnings, nextest, cargo test, Unix cfg gating, no #[allow], unreachable_pub clean, exact pins, --locked) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths.","phase":"engineering"}
{"id":"ENG-13","description":"Two independent processes with two systemd units and two service accounts, a uid-restricted socket and no child-spawn mode.","phase":"engineering"}
{"id":"ENG-14","description":"K3: SQLite WAL store module with forward-only migrations and a leased outbox/job table.","phase":"engineering"}
{"id":"ENG-15","description":"K2: Op[In,Out] registry on Huma v2 and net/http ServeMux deriving OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), the generated rukie CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots.","phase":"engineering"}
{"id":"ENG-16","description":"Auth: argon2id passwords with TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts, and admin-CLI password reset.","phase":"engineering"}
{"id":"ENG-17","description":"Users, orgs, teams and repository permissions enforced in the operation-table authz middleware.","phase":"engineering"}
{"id":"ENG-18","description":"Design tokens as CSS custom properties and base layouts, with each later feature shipping its zero-JS pages from its operations.","phase":"engineering"}
{"id":"ENG-19","description":"R2a part 1: tree and object readers serving hosted diff/show/file/blame, fix the stale daemon doc claiming op.log, claim.list and attest.list serve only first pages, and exclude status.","phase":"engineering"}
{"id":"ENG-20","description":"R2b: hosted preparation for bookmark.*, tag.*, land.execute, push, push.reconcile, fetch, unpin and op.prune.","phase":"engineering"}
{"id":"ENG-21","description":"Crash test proving lane receipts survive a daemon kill at each mutation step and that replay is a no-op.","phase":"engineering"}
{"id":"ENG-22","description":"R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs and compare/merge-base reads.","phase":"engineering"}
{"id":"ENG-23","description":"testruk harness running the real daemon that fails, never skips, when the binary is missing.","phase":"engineering"}
{"id":"ENG-24","description":"K4: pooled daemon client with semaphore, cancellation, schema codegen and a one-to-one RFC 9457/MCP error map, tested on testruk.","phase":"engineering"}
{"id":"ENG-25","description":"RUK_PIN plus a schema-digest drift gate and a handshake version check.","phase":"engineering"}
{"id":"ENG-26","description":"Repo hosting and browsing feature: create/rename/delete ops and tree, blob, commit and diff pages and API.","phase":"engineering"}
{"id":"ENG-27","description":"Typed git-wire methods git.upload_pack (v0+v2), git.receive_pack and git.upload_archive admitted in the Bulk lane.","phase":"engineering"}
{"id":"ENG-28","description":"Keep quarantine and all hook phases (pre-receive, update, post-receive, proc-receive, fail-closed timeouts) unchanged and add typed hosted handlers for each phase.","phase":"engineering"}
{"id":"ENG-29","description":"rukie Smart HTTP and SSH frontends (x/crypto/ssh) that authenticate via ENG-16, authorize via ENG-17 and forward byte streams to the git-wire methods.","phase":"engineering"}
{"id":"ENG-30","description":"Walking-skeleton e2e: stock git clone, fetch v2, push and push refs/for/main over HTTP and SSH through the ENG-29 frontends to the daemon.","phase":"engineering"}
{"id":"ENG-31","description":"Process-trace test proving no git process runs inside the rukie plus daemon boundary.","phase":"engineering"}
{"id":"ENG-32","description":"Finish the slice-5 production graph index over store segments so revset evaluation runs outside tests.","phase":"engineering"}
{"id":"ENG-33","description":"R2a part 2: hosted log/query on the production graph index.","phase":"engineering"}
{"id":"ENG-34","description":"History and branch-view pages and ops grouping a branch's changes.","phase":"engineering"}
{"id":"ENG-35","description":"R2d: durable ordered op-log tail with a resumable subscription across op.prune.","phase":"engineering"}
{"id":"ENG-36","description":"Op-log consumer with one transaction for cursor and outbox, idempotent keys and a broadcast for long-wait operations.","phase":"engineering"}
{"id":"ENG-37","description":"Webhook delivery with Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker and an SSRF guard.","phase":"engineering"}
{"id":"ENG-38","description":"In-app inbox fed by the outbox, with no email delivery.","phase":"engineering"}
{"id":"ENG-39","description":"Per-token and per-account token buckets in the operation-table middleware with Retry-After.","phase":"engineering"}
{"id":"ENG-40","description":"R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim and attestation cursor paging already exists.","phase":"engineering"}
{"id":"ENG-41","description":"Review feature: changes and stacks from refs/for with change-ids, branch view, approvals as attestations and merge via land.execute.","phase":"engineering"}
{"id":"ENG-42","description":"Tasks feature replacing issues, claimable through Ruk leases.","phase":"engineering"}
{"id":"ENG-43","description":"CI core with a leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners and results as attestations.","phase":"engineering"}
{"id":"ENG-44","description":"Reuse MergeNotice unchanged and report effective mode and language_set_version on every mergeability and land result.","phase":"engineering"}
{"id":"ENG-45","description":"R4: push with credentials per call, mirror mode, ref selection and per-ref status on mirror.rs.","phase":"engineering"}
{"id":"ENG-46","description":"Mirror worker with one run per mirror and dirty-flag rerun, reconcile, credentials only in memory and per-ref ff-only rejections.","phase":"engineering"}
{"id":"ENG-47","description":"R3 in order: first the bounded search method in ruk riding the Bulk lane with deadline, result cap and cancellation, then the rukie search op and page on top of it.","phase":"engineering"}
{"id":"ENG-48","description":"Releases backed by hosted tag.* with streaming archive downloads.","phase":"engineering"}
{"id":"ENG-49","description":"Social features: explore, stars, watches and forks via registry fork.","phase":"engineering"}
{"id":"ENG-50","description":"Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation.","phase":"engineering"}
{"id":"ENG-51","description":"Retention and maintenance stress under rukie load, with maintenance and backups only through daemon methods in the Maintenance lane.","phase":"engineering"}
{"id":"ENG-52","description":"Kill-9 fault suite for rukie and daemon mid-push, mid-webhook, mid-CI-attest and mid-mirror, plus ruk crash/lock/IO smoke tests and shuttle cases.","phase":"engineering"}
{"id":"ENG-53","description":"Run the reference capacity profile against rukie and the daemon as the release gate, deciding the metadata store.","phase":"engineering"}
```

### Critical Files for Implementation
- `ruk/crates/ruk/src/main.rs` — the CLI that gains the `host-credential` provisioning subcommand (ENG-06) beside `serve`.
- `ruk/crates/ruk/src/daemon/ipc.rs` — the peer-uid check (533-550) and the handshake frames that ENG-07 extends with the single-use challenge.
- `ruk/crates/ruk/src/daemon/executor.rs` — the permit freed on cancel and the shared semaphore (G13, ENG-11).
- `ruk/crates/ruk-core/src/coord/admission.rs` — the private `RequestClass` (lines 33-41) and the dispatcher-only invariant behind the five-lane `ExecutorLane`.
- `ruk/crates/ruk/src/daemon/mod.rs` — the argv `CommandOpen` runner that needs the verb rule, plus buffered input, blocking streams, `InvocationOpen` (304-316), the outcome ledger and the stale read-coverage doc (612-614).

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Fix the search row before fin…

---
# autoplan-eng.md: Engineering review of the rukie ground-up rewrite on Ruk (revision 16, reissued)

The plan is buildable, but not in the order the skeleton gives. The ruk daemon can't yet host a forge, so ruk's hosting work has to come before most rukie features. Two things block it today:

1. **Typed forge reads are missing.** Over the daemon, `diff`, `show`, `file`, `blame`, `log` and `query` are refused with `E_HOSTED`.
2. **Large transfers don't stream.** Git-wire traffic is buffered whole in memory, and one long command stalls every other stream on its connection.

So the first job is not "add forge read methods" (R2). It is to fix how the daemon hosts: build the typed reads, stream transfers, keep bulk work from taking all the threads, and accept per-user identity from the forge.

**The search-row advisory was already fixed in revision 16.** §5.1's row no longer gives ENG-47 as both sides: it reads "Search op and page (ENG-47, rukie half) | R3 bounded `search` method (ENG-47, ruk half, built first) riding the Bulk lane (ENG-11)" — the Bulk lane is cited via ENG-11 and the Ruk search method is named separately. The drift-gate references (ENG-25) were also already corrected in revision 15. No content changed; this is reissued in full because this final message is what gets saved.

**This file has not been written to disk.** This session is read-only plan mode with no write tool. The orchestrator needs to save this message as `autoplan-eng.md` and create `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` from the JSONL block in §12.

---

## 0. Restated problem

Choose the build order, the process layout and the reliability guarantees for two repositories that change together:
- **ruk:** Rust; workspace gates fmt / clippy / nextest / test.
- **rukie:** Go; today a 393k-LOC Gitea tree that becomes an agent-first forge using only the Ruk daemon as its repository backend.

Constraints:
- every commit builds in both repos;
- many concurrent agents must be handled safely (backpressure, retries that don't double-apply, durable job queues);
- tests must run against a real daemon.

**Where the labels come from:**
- R1–R6, K1–K4, F, U, V: the draft plan outline in this assignment's context (not stored in any repo file).
- S0–S7: stages in §5.
- G1–G17: findings in §1.
- F1–F7: design forks in §3.

## 0a. Authorization boundary

- **This review authorizes nothing.** autoplan ends at a human approval gate. Every task starts only after that approval.
- **Deleting the Gitea tree (ENG-03)** rests on this assignment's settled decision: "Cut every Gitea surface (API v1, swagger, app.ini, xorm schema, Actions, packages, OAuth2 provider, Gitea webhook formats, builtin SSH code, gitea CLI, Vue/Vite/Tailwind, locales) … Land as commits on top of Gitea history; every commit builds." That text lives only in the assignment context, not in any workspace file; the user confirms it at the gate. It covers rukie only, and it is a reversible commit with history kept.
- **The existing ruk plan:** `ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:119-121` says no source cutover, data migration or source-copy purge is authorized by it, and forbids silent feature removal or storage substitution. So: changing that plan (ENG-01) is a proposal decided item by item; the nested `ruk/rukie/` clone is never deleted, and ENG-02 only proposes an ignore entry; ruk's redb store is untouched. The metadata store (F4) belongs to rukie.
- **No outward acts** (push, publish, name reservation) are proposed.

## 1. What the code shows today

| # | Finding | Evidence | Consequence |
|---|---|---|---|
| G1 | The daemon is wired as `Command::Forgejo { command: ForgejoCommand::Serve }`; the handshake profile is hard-coded `"forgejo"`. | `ruk/crates/ruk/src/main.rs:202-220`; `daemon/ipc.rs:643`; `daemon/tests.rs:322` | R1 is a rename plus a protocol change (ENG-05). |
| G2 | Typed hosted reads serve `op.log`, `claim.list`, `attest.list` with lease-bound cursor continuation (`read()` mints a 30-second cursor when a first page does not exhaust the listing and resumes it after binding checks). Every other read names its missing piece in `read_gap`: `status` needs a workspace autosnapshot and workcopy crawl; `log`/`query` need "a production graph index over store segments" because revset evaluation "builds its graph only in tests"; `diff`/`show`/`file`/`blame` need "tree and object readers over resolved revisions". | `ruk-core/src/coord/hosted.rs:539-560, 612-632, 1347-1362` | A forge doesn't need `status`. `diff`/`show`/`file`/`blame` need tree and object readers (ENG-19); `log`/`query` need the graph index (ENG-32, ENG-33). `daemon/mod.rs:612-614` still says "first pages are served", contradicting the coordinator; ENG-19 fixes that stale doc. The argv `CommandOpen` path returns untyped text and blocks other streams (G5), so browsing waits for ENG-19 instead of shipping on it. |
| G3 | Hosted mutations covered: ref, new, claim, ws.new, pin, attest, describe, split, rebase, squash, abandon, edit, ws.scope, ws.take, ws.drop, resolve, revert, undo, land.prepare. Not covered: `land.execute`, `push`, `push.reconcile`, `fetch`, `bookmark.*`, `tag.*`, `snapshot`, `write`, `op.prune`, `unpin`. | `daemon/mod.rs:627` | Merges, mirrors and releases are blocked on these (ENG-20). |
| G4 | Identity is derived from the peer uid alone (`Principal::from_peer_uid`, `ipc.rs:533-550` accepts every process with an allowed UID), and `InvocationOpen` (`daemon/mod.rs:304-316`) carries no assertion field, nonce or sequence; `session_id` and the mutation `request_id` are optional and reusable. | `daemon/mod.rs:2017,304-316`; `daemon/registry.rs:279`; `hosted.rs:590-596` | rukie's one uid would make every user and agent a single principal, any same-uid process could impersonate all of them, and a captured open frame could be replayed. The daemon needs an authenticated, replay-resistant host assertion under distinct service identities (ENG-06, ENG-07). |
| G5 | A long-running command blocks other streams on the same connection (documented v1 limitation); clone, fetch and archive arrive as argv `CommandOpen` streams. | `daemon/mod.rs` `command_inner` doc; `ruk-git/src/transport/mirror.rs:12` | Connections become the bottleneck (`DEFAULT_MAX_CONNECTIONS = 128`); the argv path has no `Method` to classify by (ENG-11). |
| G6 | Command input is buffered in full (up to 256 MiB) before running; output is captured, then streamed; pushed packs are staged into memory. | `daemon/mod.rs` consts / docs; `ruk-git/src/transport/hooks.rs:1-12` | RSS grows with pack size × concurrency; transfers must stream and spill to disk (ENG-09). |
| G7 | The reconnect outcome ledger is in memory (10,000 entries); durable receipts live in the lane. | `daemon/mod.rs`; `ruk/docs/coordinator.md` | rukie must persist request ids; whether receipts are durable on disk is **inference** from coordinator.md, tested by ENG-21. |
| G8 | Each repo's database (redb) allows one writer process; a second gets `DatabaseAlreadyOpen`. | `AGENT_NATIVE_GIT_PLAN.md:152` | The daemon is the only process that opens hosted stores. |
| G9 | Receive pipeline: quarantine, `pre-receive`/`update`/`post-receive`, fail-closed timeouts, and `proc-receive` diversion using git's exact conversation. | `hooks.rs:1-47`; `proc_receive.rs:1-44` | Git semantics that stay in ruk; rukie adds typed handlers per phase (ENG-28). |
| G10 | The `push --mirror` mapping and refspec pruning already exist. | `ruk-git/src/transport/mirror.rs:1-40` | R4 extends existing code (ENG-45). |
| G11 | Daemon limits: 64 KiB data frame, 1 MiB control frame, 256 KiB starting / 16 MiB maximum window, 16 blocking permits, 60 s per-frame timeout. | `daemon/ipc.rs:20-32`; `daemon/mod.rs:24-37` | Per-stream memory has a known upper bound; subscriptions need keepalives (ENG-10). |
| G12 | The admission classes exist and are private to ruk-core: `pub(crate) enum RequestClass { Control, Interactive, Bulk, Hook, Maintenance }`, documented "The dispatcher selects it; a client never does"; the scheduler has separate limits per class; Bulk leases cancel after 30 s without progress and are capped at 2 h. | `ruk-core/src/coord/admission.rs:33-41` (read directly); `admission/scheduler.rs:30-35,222-226`; `leases/timing.rs:15-22` | Don't add a class. The ruk-core dispatcher maps typed methods to classes; the daemon crate can't name the class, so it needs a public `ExecutorLane` mirroring all five variants plus its own verb rule for argv streams (ENG-11). |
| G13 | The executor has two defects: one shared semaphore for every class, so per-class admission is undone at the thread pool; and `run` acquires the permit outside `spawn_blocking` and drops it only after awaiting the join handle, so a cancelled caller releases the permit while `work` keeps running. | `ruk/crates/ruk/src/daemon/executor.rs` (`run`) | Blocking concurrency and memory can exceed their bound under client cancellations; fixed and tested in ENG-11. |
| G14 | ruk CI: fmt; clippy `--workspace --all-targets --all-features --locked -D warnings` on 3 OSes; nextest 0.9.148 on 3 OSes; `cargo test` on 3 OSes; toolchain 1.99.1. Miri, fuzz, soak and store gates are deferred. Lints deny `allow_attributes`, `unsafe_code`, `todo`, `unimplemented`, `dbg_macro`; `unreachable_pub` warns; exact `=` pins with `--locked`. The existing plan's Miri gate: `cargo +nightly miri test -p ruk-core store::segment`. | `ruk/.github/workflows/ci.yml`; `ruk/Cargo.toml`; `ruk/rust-toolchain.toml`; `AGENT_NATIVE_GIT_PLAN.md` "Verification gates" | Unix-only daemon code must pass Windows clippy without `#[allow]`; a new public ruk-core type needs a real outside user or `unreachable_pub` plus `-D warnings` fails CI; the deferred gates are still owed (ENG-12). |
| G15 | `ruk/rukie/` is a clone of `https://github.com/gosuda/rukie` inside the ruk tree, and ruk's `.gitignore` does not exclude it. | `ruk/rukie/.git/config`; `ruk/.gitignore` | Risk of committing a gitlink; never delete it. |
| G16 | `ruk/tasks/autoplan-ledger.jsonl` holds `ENG-01..06` from the earlier autoplan run; `ruk/.gitignore` ignores `tasks/`; there is no `tasks/` directory at the workspace root. | that file; `ruk/.gitignore`; workspace root listing | This run's ledger is `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`: a new file, no id collision. `ruk/tasks/autoplan-ledger.jsonl` is left alone. |
| G17 | `AGENT_NATIVE_GIT_PLAN.md` targets Forgejo parity, forbids runtime Git, gates release on a capacity profile, and requires explicit revision for any removal. | `AGENT_NATIVE_GIT_PLAN.md:5-14,71-99,115-121` | §2 is a revision proposal, not a supersession. |

## 2. Proposed reconciliation with `AGENT_NATIVE_GIT_PLAN.md` (decided at the gate)

| Existing plan item | Proposed verdict | Carried form |
|---|---|---|
| Slices 1–8 | Carry unchanged | Slice 5's production graph index becomes a scheduled task (ENG-32) because hosted `log`/`query` need it. |
| Slice 9 | Carry; backend half adapted | ENG-19, ENG-20, ENG-22, ENG-33. |
| Slice 10 | Adapt; dropping the Forgejo Go patch needs approval | `ruk serve` plus the rukie client (ENG-05..07, ENG-11, ENG-24). |
| Slice 11 | Carry in ruk; forge side adapted | Hook phases stay (ENG-28); mirrors become ENG-45; dropping wiki/template flows needs approval. |
| Slice 12 | Carry | ENG-51. |
| Slice 13 | Repository half carried; Forgejo-database half pending approval | ENG-50. |
| Slice 14 + release gate | Carry | ENG-53. |
| No runtime Git | Carry | ENG-31. |
| CEO-01 frozen Forgejo inventory | Proposed superseded, approval needed | Replaced by the ENG-25 drift gate. |
| DES-01 one `MergeNotice` record; CEO-02 effective mode + `language_set_version` | Carry | ENG-44. |
| Verification gates: Miri, fuzz, shuttle, crash smoke tests, out-of-process harness | Carry | ENG-12 (fuzz + Miri), ENG-23, ENG-52. |

Every item stays in force until the user approves a change.

## 3. Hypotheses on the major forks

### F1 — Process layout
- **H1a (recommended):** two independent processes over a Unix socket under two dedicated service accounts (F7): the daemon as uid A, rukie as uid B, `allowed_uids = [B]`. rukie terminates HTTP and SSH and forwards byte streams to typed git-wire methods. *Disproved if* deploys must be a single binary *and* rukie restarts never need to keep transfers alive.
- **H1b:** rukie spawns the daemon as a child. Rejected: deploys kill in-flight transfers, and it means two runtime paths.

### F2 — Push policy and refs/for
- **H2a (recommended):** the receive pipeline stays; hosted mode registers typed handlers: ref policy at `pre-receive`/`update`, change-id change creation at `proc-receive`, a wake-up hint at `post-receive`. Script hooks remain the CLI and self-hosted default. *Disproved if* the handlers can't see quarantined objects through the view scripts get.
- **H2b:** delete the hook phases. Rejected: breaks slice 11 parity.

### F3 — How ref updates reach CI, webhooks and mirrors durably
- **H3a (recommended):** a stored op-log position plus outbox rows, advanced in one transaction; `post-receive` is only a wake-up hint. *Disproved if* `op.log` can't be paged in order from a durable position across `op.prune`.
- **H3b:** a push-time callback inserts the jobs. Rejected: events are lost while rukie is down.

### F4 — rukie metadata store (open)
- **H4a (recommended):** SQLite WAL, pure Go, behind a SQL-shaped store module. *Disproved by* the capacity gate (ENG-53).
- **H4b (runner-up):** PostgreSQL, which the existing plan uses for capacity acceptance (`:117`).
- **H4c:** Pebble. Rejected: no SQL.

### F5 — Deleting the Gitea tree
- **H5a (recommended):** one reversible commit.
- **H5b:** staged deletion. Rejected: every intermediate commit is a gutted Gitea that still has to build, with no value.

### F7 — Host principal assertion
- **Deployment:** distinct identities. The daemon runs as uid A; rukie runs as uid B; `allowed_uids = [B]`. Credential provisioning is a **ruk-side CLI subcommand** (`ruk host-credential`, added in ENG-06 before any handshake work, depending on nothing later), writing the random secret as a 0640 file owned by A in a dedicated group whose only members are A and B. Rukie fails at boot with a named error when it cannot read it; the daemon fails at startup when it cannot. A same-account 0600 file is rejected because any same-uid process reads it.
- **Guarantee, stated honestly:** the uid gate plus the credential prevents impersonation by any process outside {A, B}. It does not separate a process running as rukie's uid (B): that process is inside rukie's trust boundary and can ptrace rukie, read its memory and steal sessions and tokens regardless of this layer. User-attribution separation within one uid is not attainable in userspace; the deployment requirement is that only rukie runs as B. A pid → executable check over `SO_PEERCRED` remains optional defense-in-depth, noted as weak.
- **H7a (recommended):** identity additionally carries a proof with daemon-issued freshness:
  1. the handshake accept reply carries a fresh, cryptographically random, single-use per-connection challenge;
  2. a principal assertion is an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the host credential;
  3. the daemon records consumed challenges for the life of the connection and refuses any reuse; it also refuses any second assertion on a session that is already bound;
  4. the asserted principal is bound to the daemon-issued session, so later frames on that session never re-assert.

  Consequences: the same assertion bytes are never accepted twice; a captured open frame replayed on another connection fails (different challenge); replaying frames on the same connection after binding cannot re-assert identity; invocation-level replay of bound-session frames remains the intended request-id idempotency path. Sessions, leases, receipts and attestations attribute to the bound principal.
- **H7b:** accept bare assertions from allowed uids. Rejected: any allowed-uid process could impersonate every user and forge the audit trail.
- **H7c:** HMAC over a client-chosen nonce only. Rejected: the client can replay its own nonce.
- **H7d:** same service account with a 0600 file. Rejected: no peer separation, and a daemon-created 0600 file is unreadable by a differently-owned rukie.

### F6 — Executor isolation across the crate boundary
- **H6a (recommended):** `RequestClass` stays `pub(crate)`; ruk-core exposes a public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) plus a `Method` → lane mapping derived from the dispatcher's class choice; the daemon classifies argv `CommandOpen` streams by verb on the server side (table below), never emitting Hook or Control; `BoundedExecutor` reserves permits per lane and holds each permit inside the `spawn_blocking` closure. *Disproved if* all blocking work could move into ruk-core's admitted path; the argv runner and git-wire serving live in the daemon crate, so it can't.
- **H6b:** make `RequestClass` public. Rejected: it would let clients choose their own class, against `admission.rs:33`.
- **H6c:** one executor per class. Rejected: idle threads, against the executor's design (`executor.rs` header).
- **H6d:** a three-variant `ExecutorLane`. Rejected: hook work would land on Bulk permits and exceed that class's limit.

Verb rule for argv streams:

| Verbs | Lane |
|---|---|
| `upload-pack`, `receive-pack`, `upload-archive`, `archive`, `fetch`, `push`, `pack-objects`, `index-pack`, `grep`, `blame` | Bulk |
| `gc`, `repack`, `prune`, `maintenance`, `fsck` | Maintenance |
| Any other listed verb | Interactive |
| Unknown verb | Bulk |
| *(never produced by argv)* | Hook, Control |

## 4. Six-principle register (engineering)

| Principle | Decision | Open question |
|---|---|---|

[Showing lines 8372-8721 of 8940 (49.9KB limit). Use :8722 to continue]
|---|---|---|
| **Clarity** | One operation table (ENG-15) derives HTML, JSON, MCP and CLI. One wire contract (schema codegen + digest gate). One git-wire method family. One event spine. The daemon is the only process opening stores. One receive pipeline with script or typed handlers. Admission classes chosen server-side only; the executor sees all five lanes. Host identity = distinct service uids + a ruk-CLI-provisioned group-restricted credential + an HMAC'd assertion over a daemon-issued single-use challenge, with the guarantee scoped to outside-{A,B} processes (F7). Plan changes are proposals. | — |
| **Impact** | Fix streaming, per-stream concurrency and executor isolation (G5, G6, G13) first. Tree/object readers unlock file, commit and diff pages. The graph index (ENG-32) is the long pole for history and branch views. | — |
| **Audience** | Typed errors map one-to-one to RFC 9457 / MCP with `Retry-After`. `MergeNotice` and effective mode on every merge result. Zero-JS HTML from the same operations. Agents use scoped tokens on human accounts (ENG-16). | Runner credential class. |
| **Risk** | Pack memory; principal impersonation by outside processes (F7, ENG-07), with same-uid processes named as out of scope; lost events; the single-writer store lock; a cancelled request freeing its permit while its work keeps running, and argv transfers bypassing lanes (G13, ENG-11); leaking `RequestClass` to clients; SSRF; cross-repo drift; runtime Git; unauthorized plan or destructive changes (§0a). | Lane receipt durability (ENG-21). |
| **Sequencing** | Tasks are numbered in stage order (§5); each depends only on earlier tasks. Provisioning (ENG-06) precedes the handshake (ENG-07), both in ruk's CLI, so S1 has no circular dependency on rukie. Store → operation table → auth → users/orgs before any authenticated feature. Git wire → hooks → frontends → walking skeleton. Graph index → `log`/`query` → history pages → review. Capacity is the final release gate. | — |
| **Reversibility** | `protocol_major` bump plus `RUK_PIN`; forward-only migrations with backup restore; Gitea deletion revertible; PostgreSQL fallback; script hooks can be re-enabled per repo; `ExecutorLane` and the verb table are small and easy to change before release; the host credential can be rotated by regenerating the file. | — |

## 5. Amended dependency order

### 5.1 Which ruk capability blocks which rukie feature

| rukie feature (task) | Blocking ruk capability (task) | Today |
|---|---|---|
| Auth, users, orgs, teams, tokens (ENG-16, ENG-17) | none | — |
| Any repo-touching feature | `ruk serve`, credential provisioning, authenticated host assertion, handshake check (ENG-05, ENG-06, ENG-07, ENG-25) | missing |
| Repo hosting and browsing (ENG-26) | Tree/object readers + tree / refs / compare + lifecycle (ENG-19, ENG-22) | E_HOSTED |
| History, branch view (ENG-34) | Graph index → `log`/`query` (ENG-32, ENG-33) | E_HOSTED; graph built only in tests |
| HTTP / SSH clone & push (ENG-29) | Per-stream tasks, streaming, spill, lanes, git-wire methods, hook handlers (ENG-08, ENG-09, ENG-11, ENG-27, ENG-28) | buffered; permit freed early |
| Webhooks, inbox (ENG-37, ENG-38) | Op-log tail + keepalive (ENG-10, ENG-35) | 60 s frame timeout kills subscriptions |
| Review (ENG-41) | Hook handlers (ENG-28), hosted `land.execute` (ENG-20), R6 extensions (ENG-40) | script-only / `land.execute` uncovered |
| Tasks (ENG-42) | R6 claim extensions (ENG-40); `claim.*` and its cursor paging already exist | lease semantics missing |
| CI (ENG-43) | R6 attestation kinds (ENG-40); `attest.add` and cursor paging already exist | kinds missing |
| Mirrors (ENG-46) | Hosted `push` / `push.reconcile` (ENG-20) + R4 (ENG-45) | uncovered |
| Search op and page (ENG-47, rukie half) | R3 bounded `search` method (ENG-47, ruk half, built first) riding the Bulk lane (ENG-11) | missing |
| Releases (ENG-48) | Hosted `tag.*` (ENG-20) + streaming archive (ENG-27) | uncovered |
| Social and forks (ENG-49) | Registry fork (ENG-22) | missing |
| Repo import (ENG-50) | Offline Git conversion (existing ruk CLI; the admin wrapper is ENG-50's own half) | CLI only |

### 5.2 Stages (each stage = commits that each build; all start after the gate)

| Stage | Tasks | Exit proof |
|---|---|---|
| S0 | ENG-01..04: gate items, K1, Go gates | both CIs green |
| S1 | ENG-05..18: R1 daemon work (rename, provisioning, assertion, per-stream, streaming, keepalive, lanes, ruk gates), topology, K3 store, K2 operation table, auth, users/orgs/teams, UI tokens | soak test + fuzz + Miri; executor and verb-lane tests; provisioning and assertion tests; login and token flows on all four surfaces |
| S2 | ENG-19..26: tree/object reads, mutations, receipts, lifecycle, `testruk`, K4 client, drift gate, repo browsing | real-daemon integration tests; browse a repo end to end |
| S3 | ENG-27..31: git wire → hook handlers → frontends → walking skeleton → no-Git trace | **walking skeleton:** stock git clone / push / `push refs/for/main` through rukie |
| S4 | ENG-32..38: graph index, `log`/`query`, history pages, op-log tail, consumer, webhooks, inbox | kill-restart test: no lost or duplicate deliveries |
| S5 | ENG-39..44: token buckets, R6, review, tasks, CI, MergeNotice | end-to-end agent flow (claim → push → CI attest → land) |
| S6 | ENG-45..51: R4, mirror worker, search (ruk method then rukie op/page), releases, social, import, retention stress | §8 targets |
| S7 | ENG-52..53: fault suite, capacity profile | release gate |

## 6. Concurrency and backpressure
- **Daemon:** one task per stream and one writer per connection, bounded by credit windows (`ipc.rs:29-32`).
- **Admission and executor (ENG-11):** typed methods use the dispatcher's class and the derived five-variant `ExecutorLane`; argv streams use the verb rule with unknown verbs on Bulk and never Hook or Control; permits are reserved per lane and held inside the `spawn_blocking` closure.
- **Identity:** distinct service accounts (A = daemon, B = rukie), `allowed_uids = [B]`, group-restricted credential provisioned by `ruk host-credential` (ENG-06), and the HMAC'd host assertion over a daemon-issued single-use challenge (ENG-07, F7); the guarantee covers processes outside {A, B}.
- **rukie client:** a pool of about 4–8 connections with an invocation semaphore below the daemon's limits; cancelling a request's context cancels its stream; `E_LIMIT` maps to 429/503 with `Retry-After`; only idempotent reads are retried automatically.
- **Per-token limits:** token buckets per token and per account in the operation-table middleware (ENG-39).
- **Ref races:** the lane's compare-and-swap (`E_STALE`) is sent back as a typed conflict; no rukie-side locks.
- **Long waits for agents:** served from an in-process broadcast fed by the event consumer, not by polling the daemon; HTML stays zero-JS.

## 7. Durability of CI, webhook and mirror queues
1. **One spine:** one transaction reads ops from the stored op-log position, inserts outbox rows unique on `(repo, op_id, target)`, and advances the cursor; delivery is at-least-once with handlers safe to run twice.
2. **Job claiming:** rows carry leases and attempt counts; claims are atomic `UPDATE … RETURNING`; retries back off with jitter; failed jobs end in a dead-letter state.
3. **Webhooks:** Standard Webhooks signatures; `webhook-id` = outbox key; circuit breaker per endpoint; resolve-and-pin SSRF guard.
4. **Mirrors:** one run at a time with a dirty-flag rerun; periodic reconcile; credentials only in memory; per-ref fast-forward-only rejections.
5. **CI:** the job DAG is stored in metadata with leases and heartbeats; cache key = hash(inputs + spec + image digest); logs are written in chunks to files; the request id is persisted before `attest.add`; runners fetch over the git wire only.

## 8. Performance budgets

**Release gate:** the reference capacity profile (`AGENT_NATIVE_GIT_PLAN.md:115-121`), run by ENG-53.

| Path | Interim target (until measured) |
|---|---|
| Small daemon read over the socket | p99 ≤ 2 ms |
| Metadata-only API operation | p95 ≤ 20 ms |
| Tree / blob / commit page | p95 ≤ 50 ms |
| HTML page weight | ≤ 50 KB gzip, 0 B JS |
| Clone throughput | ≥ 80% of `git http-backend` |
| Push / clone memory | RSS growth independent of pack size beyond about 16 MiB per stream, including under cancel storms (ENG-11) |
| Ref update → webhook delivered | p95 ≤ 2 s |
| Ref update → mirror started | p95 ≤ 5 s |
| Idle CI queue → job started | ≤ 1 s |
| Search | deadline 2 s, capped results, cancellable |
| Bulk saturation | Interactive p95 unchanged while the Bulk lane is at its limit |

## 9. Testing strategy
- **Real daemon, no mocks of ruk:** `testruk` (ENG-23) starts `ruk serve` from `RUK_BIN` and fails, never skips, when it is missing; `make test` builds ruk at `RUK_PIN`.
- **Drift check (ENG-25):** schema regeneration and digest check plus a handshake version check.
- **Stock git and OpenSSH as clients** outside the boundary (ENG-30, after the ENG-29 frontends), plus a trace proving no `git` runs inside it (ENG-31).
- **Provisioning and assertion tests (ENG-06, ENG-07):** the subcommand creates the file with the exact owner, group, and mode; boot fails with a named error when the credential is missing; correct HMAC accepted; missing and wrong HMAC refused; the same assertion bytes replayed on the same and on a fresh connection refused (consumed challenge / challenge mismatch); a second assertion on a bound session refused; a peer with a non-allowed uid refused by the gate; a peer with the allowed uid but no valid credential refused.
- **Executor and lane tests (ENG-11):** closures block on a barrier in each of the five lanes, callers are cancelled, and the test asserts live closures per lane never exceed that lane's permits, new work queues until closures finish, and Interactive is still admitted while Bulk is saturated (must fail on today's executor); a ruk-core test checks the `Method` → lane mapping matches each method's class; a daemon test checks the argv verb table including the unknown-verb default.
- **Operation-table tests (ENG-15):** each registered op is reachable and consistent through HTML form, JSON, MCP and CLI from one definition.
- **Fault and concurrency tests:** kill-9 suite, ruk crash/lock/IO smoke tests, shuttle (ENG-52); 60 s fuzz of frame, pkt-line and git-wire decoders plus Miri (ENG-12).
- **Go gates (ENG-04) and ruk gates (ENG-12):** gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz; fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`.

## 10. Repo migration and module path (after the gate)
- **K1 (ENG-03) is one reversible commit** on top of Gitea history: remove the Gitea tree; add `module github.com/gosuda/rukie` (origin `https://github.com/gosuda/rukie`), Go 1.27 / go1.27.2, a fresh `go.sum`, a minimal `rukie serve`, a new Makefile, new CI and a new AGENTS.md.
- **License:** keep the MIT `LICENSE` with Gitea's notice (whether a rewritten tree still needs it is **inference**).
- **No `gitea.dev` aliases.**

## 11. Open questions for the approval gate
1. **rukie metadata store:** SQLite-first with PostgreSQL fallback (recommended) vs PostgreSQL from the start.
2. **CI runner credentials** under "no agent principals".
3. **Host reads without sessions** vs per-principal session pooling.
4. **User script hooks in hosted repos,** or typed handlers only.
5. **Approve item by item** the §2 rows marked "needs approval": the Forgejo patch, wiki/template flows, the Forgejo-database half of migration, CEO-01.
6. **Approve** the ENG-02 ignore entry for `ruk/rukie/`.
7. **Confirm the settled Gitea-cut decision** quoted in §0a. It exists only in the assignment context.
8. **Accept the F7 boundary:** the assertion scheme protects against processes outside {A, B}; same-uid-as-rukie protection is a deployment requirement (only rukie runs as B), not a protocol guarantee.
9. **Ledger target:** `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` (new file, no id collision). `ruk/tasks/autoplan-ledger.jsonl` is not touched.

## 12. ENG tasks (stage order; one line each: id, description, rationale; all start after the gate)

- **ENG-01** — Present the §2 reconciliation as a dated revision proposal for `AGENT_NATIVE_GIT_PLAN.md` and edit that plan only for approved items. *Rationale:* G17 and §0a require an explicit revision rather than a silent supersession.
- **ENG-02** — After approval, add an ignore entry for the nested `ruk/rukie/` clone; never delete it. *Rationale:* avoids committing a gitlink (G15) without purging anything.
- **ENG-03** — K1 under the settled Gitea-cut decision confirmed at the gate: one reversible rukie commit deleting the Gitea tree, module `github.com/gosuda/rukie`, minimal `rukie serve`, new Makefile, CI and AGENTS.md, LICENSE kept. *Rationale:* a staged deletion has no value, and the module path and agent rules must change together (F5).
- **ENG-04** — Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, `go test -race`, fuzz tests. *Rationale:* every later rukie commit is gated from the start.
- **ENG-05** — R1a: rename to `ruk serve` and profile `host`, and reword Forgejo-specific daemon docs and tests. *Rationale:* the command and profile are Forgejo-named today (G1).
- **ENG-06** — R1b step 1: add a `ruk host-credential` subcommand to ruk's own CLI (`crates/ruk/src/main.rs`, beside `serve`) that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids (paths configurable), with named errors on failure and a test asserting owner, group and mode. *Rationale:* provisioning is ruk-side infrastructure the handshake (ENG-07) consumes; placing it here avoids a circular dependency on rukie's later CLI layer (F7).
- **ENG-07** — R1b step 2: run the daemon as uid A and rukie as uid B with `allowed_uids = [B]`; carry a fresh single-use per-connection challenge in the handshake accept reply; require an HMAC-SHA256 over `(challenge, asserted_principal, method)` keyed by the ENG-06 credential; refuse a reused challenge and any second assertion on a bound session; bind the principal to the daemon session and attribute sessions, leases, receipts and attestations to it; test correct/missing/wrong HMACs, replayed opens on the same and fresh connections, reused challenges, second assertions, refusal of non-allowed uids and of the allowed uid without a valid credential. Document that same-uid-as-rukie processes are out of protocol scope. *Rationale:* a bare uid assertion permits impersonation and a client-chosen nonce permits replay; the guarantee must match what the kernel enforces (G4, F7).
- **ENG-08** — R1c: one task per stream with a per-connection frame writer. *Rationale:* removes the documented stream blocking (G5).
- **ENG-09** — R1d: stream input/output in both directions under credit windows and spill received packs to disk. *Rationale:* buffered packs make RSS scale with pack size (G6).
- **ENG-10** — R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout. *Rationale:* subscriptions would otherwise be timed out (G11).
- **ENG-11** — R1f, in one change: keep `RequestClass` `pub(crate)`; add public `ExecutorLane` mirroring all five variants (`Control, Interactive, Bulk, Hook, Maintenance`) with a `Method` → lane mapping derived from the dispatcher; classify argv `CommandOpen` streams by verb server-side (transfer/grep/blame → Bulk, gc/repack/prune/maintenance/fsck → Maintenance, unknown → Bulk, never Hook or Control); reserve `BoundedExecutor` permits per lane with each permit held inside the `spawn_blocking` closure; add cancellation, mapping-consistency and verb-table tests. *Rationale:* covers typed methods and argv transfers, keeps hook limits intact, and stops early permit release (G5, G12, G13, F6).
- **ENG-12** — Keep ruk gates green (fmt, 3-OS clippy `-D warnings`, nextest, `cargo test`, Unix cfg gating, no `#[allow]`, `unreachable_pub` clean, exact pins, `--locked`) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths. *Rationale:* G14 lists owed gates that no other task carries.
- **ENG-13** — Two independent processes with two systemd units and two service accounts, uid-restricted socket, no child-spawn mode. *Rationale:* transfers survive rukie deploys, and the split accounts give the F7 boundary.
- **ENG-14** — K3: SQLite WAL store module, forward-only migrations, leased outbox/job table. *Rationale:* transactional cursor plus outbox needs SQL, and PostgreSQL stays possible (F4).
- **ENG-15** — K2: the `Op[In,Out]` registry on Huma v2 + net/http ServeMux, producing OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), generated `rukie <op> --json` CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots. *Rationale:* every feature, frontend and limit sits on it.
- **ENG-16** — Auth: argon2id passwords + TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts (no agent principals), admin-CLI password reset. *Rationale:* required by every authenticated op and the git frontends.
- **ENG-17** — Users, orgs, teams and repository permissions, enforced in the ENG-15 authz middleware. *Rationale:* authorization for ops and git access.
- **ENG-18** — Design tokens as CSS custom properties and base layouts; each later feature ships its zero-JS pages from its operations. *Rationale:* avoids a big-bang UI phase.
- **ENG-19** — R2a part 1: tree and object readers serving hosted `diff`/`show`/`file`/`blame`; fix the stale `daemon/mod.rs:612-614` doc that says `op.log`/`claim.list`/`attest.list` serve only first pages; exclude `status`. *Rationale:* the real read gap in `read_gap` (hosted.rs:1347-1362); a forge has no workcopy (G2).
- **ENG-20** — R2b: hosted preparation for `bookmark.*`, `tag.*`, `land.execute`, `push`, `push.reconcile`, `fetch`, `unpin`, `op.prune`. *Rationale:* merges, mirrors and releases depend on them (G3).
- **ENG-21** — Crash test proving lane receipts survive a daemon kill at each mutation step and replay is a no-op. *Rationale:* safe retries rest on durable receipts (G7).
- **ENG-22** — R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs, compare/merge-base. *Rationale:* hosting, browsing and forks.
- **ENG-23** — `testruk` harness running the real daemon that fails, never skips, when the binary is missing. *Rationale:* integration tests need a real daemon without false green.
- **ENG-24** — K4: pooled client, semaphore, cancellation, schema codegen, one-to-one RFC 9457 / MCP error map, tested on `testruk`. *Rationale:* backpressure and typed errors for agents.
- **ENG-25** — `RUK_PIN` + schema-digest drift gate + handshake version check. *Rationale:* keeps both repos building together across breaking ruk changes; replaces the frozen Forgejo inventory (CEO-01) as the cross-repo contract.
- **ENG-26** — Repo hosting and browsing feature: create/rename/delete ops, tree, blob, commit and diff pages and API, on ENG-19/22/24. *Rationale:* first repo-facing feature, proving the operation table end to end.
- **ENG-27** — Typed git-wire methods `git.upload_pack` (v0+v2), `git.receive_pack`, `git.upload_archive`, admitted in the Bulk lane. *Rationale:* one streaming contract for HTTP and SSH (F1).
- **ENG-28** — Keep quarantine and all hook phases (`pre-receive`, `update`, `post-receive`, `proc-receive`, fail-closed timeouts) unchanged; add typed hosted handlers for each phase. *Rationale:* preserves slice 11 parity and git semantics while removing script dispatch for the forge (G9, F2).
- **ENG-29** — rukie Smart HTTP + SSH frontends (`x/crypto/ssh`) that authenticate via ENG-16, authorize via ENG-17, and forward byte streams to the git-wire methods. *Rationale:* authentication stays in rukie, pack logic in ruk (F1).
- **ENG-30** — Walking-skeleton e2e: stock git clone, fetch v2, push and `push refs/for/main` over HTTP and SSH through the ENG-29 frontends to the daemon. *Rationale:* retires the biggest integration risk once its parts exist.
- **ENG-31** — Process-trace test proving no `git` process runs inside the rukie + daemon boundary. *Rationale:* carries the no-runtime-Git rule.
- **ENG-32** — Finish slice 5's production graph index over store segments so revset evaluation runs outside tests. *Rationale:* `read_gap` names it as the `log`/`query` blocker (G2).
- **ENG-33** — R2a part 2: hosted `log`/`query` on the ENG-32 index. *Rationale:* history reads need it (G2).
- **ENG-34** — History and branch-view pages and ops (a branch's changes grouped) on ENG-33. *Rationale:* settled branch-view feature.
- **ENG-35** — R2d: durable ordered op-log tail with a resumable subscription across `op.prune`. *Rationale:* the event spine for CI, webhooks, mirrors and the inbox (F3).
- **ENG-36** — Op-log consumer: one transaction for cursor + outbox, idempotent keys, broadcast for long-wait operations. *Rationale:* no lost events across restarts (F3).
- **ENG-37** — Webhooks: Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker, SSRF guard. *Rationale:* reliable, signed delivery that can't be aimed at internal hosts.
- **ENG-38** — In-app inbox fed by the ENG-36 outbox (no email delivery). *Rationale:* settles the identity/notification decision.
- **ENG-39** — Per-token and per-account token buckets in ENG-15 middleware with `Retry-After`. *Rationale:* many agents must not exhaust the daemon's admission budget.
- **ENG-40** — R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim/attestation cursor paging already exists. *Rationale:* tasks, review and CI depend on these kinds.
- **ENG-41** — Review feature: changes/stacks from `refs/for` with change-ids, branch view, approvals as attestations, merge via `land.execute`. *Rationale:* the settled review unit.
- **ENG-42** — Tasks feature replacing issues, claimable through Ruk leases. *Rationale:* settled feature.
- **ENG-43** — CI core: leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners, results as attestations. *Rationale:* durable, efficient CI that never opens hosted stores (G7, G8).
- **ENG-44** — Reuse `MergeNotice` unchanged and report effective mode + `language_set_version` on every mergeability and land result. *Rationale:* carries DES-01 / CEO-02.
- **ENG-45** — R4: `push` with credentials per call, mirror mode, ref selection, per-ref status on `mirror.rs`. *Rationale:* mirrors need it, and the mapping already exists (G10).
- **ENG-46** — Mirror worker: one run per mirror with dirty-flag rerun, reconcile, credentials only in memory, per-ref ff-only rejections. *Rationale:* collapses ref-update bursts and keeps credentials out of ruk.
- **ENG-47** — R3, in order: first the bounded `search` method in ruk (Bulk lane, deadline, result cap, cancellation), then the rukie search op and page on top of it. *Rationale:* lightweight search that can't affect interactive traffic; the ruk half is the capability the rukie half consumes.
- **ENG-48** — Releases: tag-backed releases via hosted `tag.*` and streaming archive downloads. *Rationale:* settled feature.
- **ENG-49** — Social: explore, stars, watches and forks (via registry fork). *Rationale:* settled feature.
- **ENG-50** — Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation. *Rationale:* carries the repository half of slice 13.
- **ENG-51** — Retention and maintenance stress under rukie load; maintenance and backups only through daemon methods in the Maintenance lane. *Rationale:* slice 12 plus the single-writer lock (G8).
- **ENG-52** — Kill-9 fault suite (rukie and daemon) mid-push, mid-webhook, mid-CI-attest and mid-mirror; ruk crash/lock/IO smoke tests; shuttle cases. *Rationale:* carried gates that prove §7.
- **ENG-53** — Run the reference capacity profile against rukie + daemon as the release gate; it decides F4. *Rationale:* the carried release gate.

```jsonl
{"id":"ENG-01","description":"Present the reconciliation table as a dated revision proposal for AGENT_NATIVE_GIT_PLAN.md and edit that plan only for user-approved items.","phase":"engineering"}
{"id":"ENG-02","description":"After approval, add an ignore entry for the nested ruk/rukie/ clone and never delete it.","phase":"engineering"}
{"id":"ENG-03","description":"Under the settled Gitea-cut decision confirmed at the gate, make one reversible rukie commit deleting the Gitea tree, setting module github.com/gosuda/rukie, adding a minimal rukie serve, new Makefile, CI and AGENTS.md, and keeping LICENSE.","phase":"engineering"}
{"id":"ENG-04","description":"Go gates in the new rukie CI: gofmt, go vet, golangci-lint v2, govulncheck, go test -race and fuzz tests.","phase":"engineering"}
{"id":"ENG-05","description":"R1a: rename the daemon to ruk serve and the handshake profile to host, and reword Forgejo-specific daemon docs and tests.","phase":"engineering"}
{"id":"ENG-06","description":"R1b step 1: add a ruk host-credential subcommand to ruk's own CLI that generates the random credential and writes it as a 0640 file owned by the daemon's service account in a group whose only members are the daemon and rukie uids, with named errors and a test asserting owner, group and mode.","phase":"engineering"}
{"id":"ENG-07","description":"R1b step 2: run the daemon as uid A and rukie as uid B with allowed_uids=[B], carry a fresh single-use per-connection challenge in the handshake accept reply, require an HMAC over (challenge, asserted_principal, method) keyed by the ENG-06 credential, refuse a reused challenge and any second assertion on a bound session, bind the principal to the daemon session, attribute sessions, leases, receipts and attestations to it, and test correct, missing and wrong HMACs, replayed opens, reused challenges, second assertions and refusal of non-allowed uids and of the allowed uid without a valid credential, documenting that same-uid-as-rukie processes are out of protocol scope.","phase":"engineering"}
{"id":"ENG-08","description":"R1c: one task per stream with a per-connection frame writer.","phase":"engineering"}
{"id":"ENG-09","description":"R1d: stream input/output in both directions under credit windows and spill received packs to disk.","phase":"engineering"}
{"id":"ENG-10","description":"R1e: stream keepalives and a subscription class exempt from the 60 s frame timeout.","phase":"engineering"}
{"id":"ENG-11","description":"R1f: keep RequestClass pub(crate), add public ExecutorLane mirroring all five RequestClass variants (Control, Interactive, Bulk, Hook, Maintenance) with a Method-to-lane mapping derived from the dispatcher, classify argv CommandOpen streams by verb on the server side with unknown verbs on Bulk and never Hook or Control, reserve BoundedExecutor permits per lane held inside the spawn_blocking closure, and add cancellation, mapping-consistency and verb-table tests.","phase":"engineering"}
{"id":"ENG-12","description":"Keep ruk gates green (fmt, 3-OS clippy -D warnings, nextest, cargo test, Unix cfg gating, no #[allow], unreachable_pub clean, exact pins, --locked) and add 60 s fuzz of frame, pkt-line and git-wire decoders plus the Miri gate on the existing plan's supported unsafe paths.","phase":"engineering"}
{"id":"ENG-13","description":"Two independent processes with two systemd units and two service accounts, a uid-restricted socket and no child-spawn mode.","phase":"engineering"}
{"id":"ENG-14","description":"K3: SQLite WAL store module with forward-only migrations and a leased outbox/job table.","phase":"engineering"}
{"id":"ENG-15","description":"K2: Op[In,Out] registry on Huma v2 and net/http ServeMux deriving OpenAPI 3.1, RFC 9457 errors, MCP tools (go-sdk v1.8.0), the generated rukie CLI and zero-JS HTML forms, with authn, authz and rate-limit middleware slots.","phase":"engineering"}
{"id":"ENG-16","description":"Auth: argon2id passwords with TOTP, OIDC redirect flow, sessions, scoped tokens on human accounts, and admin-CLI password reset.","phase":"engineering"}
{"id":"ENG-17","description":"Users, orgs, teams and repository permissions enforced in the operation-table authz middleware.","phase":"engineering"}
{"id":"ENG-18","description":"Design tokens as CSS custom properties and base layouts, with each later feature shipping its zero-JS pages from its operations.","phase":"engineering"}
{"id":"ENG-19","description":"R2a part 1: tree and object readers serving hosted diff/show/file/blame, fix the stale daemon doc claiming op.log, claim.list and attest.list serve only first pages, and exclude status.","phase":"engineering"}
{"id":"ENG-20","description":"R2b: hosted preparation for bookmark.*, tag.*, land.execute, push, push.reconcile, fetch, unpin and op.prune.","phase":"engineering"}
{"id":"ENG-21","description":"Crash test proving lane receipts survive a daemon kill at each mutation step and that replay is a no-op.","phase":"engineering"}
{"id":"ENG-22","description":"R2c: registry lifecycle (create/delete/rename/fork), tree listing, paginated refs and compare/merge-base reads.","phase":"engineering"}
{"id":"ENG-23","description":"testruk harness running the real daemon that fails, never skips, when the binary is missing.","phase":"engineering"}
{"id":"ENG-24","description":"K4: pooled daemon client with semaphore, cancellation, schema codegen and a one-to-one RFC 9457/MCP error map, tested on testruk.","phase":"engineering"}
{"id":"ENG-25","description":"RUK_PIN plus a schema-digest drift gate and a handshake version check.","phase":"engineering"}
{"id":"ENG-26","description":"Repo hosting and browsing feature: create/rename/delete ops and tree, blob, commit and diff pages and API.","phase":"engineering"}
{"id":"ENG-27","description":"Typed git-wire methods git.upload_pack (v0+v2), git.receive_pack and git.upload_archive admitted in the Bulk lane.","phase":"engineering"}
{"id":"ENG-28","description":"Keep quarantine and all hook phases (pre-receive, update, post-receive, proc-receive, fail-closed timeouts) unchanged and add typed hosted handlers for each phase.","phase":"engineering"}
{"id":"ENG-29","description":"rukie Smart HTTP and SSH frontends (x/crypto/ssh) that authenticate via ENG-16, authorize via ENG-17 and forward byte streams to the git-wire methods.","phase":"engineering"}
{"id":"ENG-30","description":"Walking-skeleton e2e: stock git clone, fetch v2, push and push refs/for/main over HTTP and SSH through the ENG-29 frontends to the daemon.","phase":"engineering"}
{"id":"ENG-31","description":"Process-trace test proving no git process runs inside the rukie plus daemon boundary.","phase":"engineering"}
{"id":"ENG-32","description":"Finish the slice-5 production graph index over store segments so revset evaluation runs outside tests.","phase":"engineering"}
{"id":"ENG-33","description":"R2a part 2: hosted log/query on the production graph index.","phase":"engineering"}
{"id":"ENG-34","description":"History and branch-view pages and ops grouping a branch's changes.","phase":"engineering"}
{"id":"ENG-35","description":"R2d: durable ordered op-log tail with a resumable subscription across op.prune.","phase":"engineering"}
{"id":"ENG-36","description":"Op-log consumer with one transaction for cursor and outbox, idempotent keys and a broadcast for long-wait operations.","phase":"engineering"}
{"id":"ENG-37","description":"Webhook delivery with Standard Webhooks signatures, idempotent ids, backoff, dead-letter, circuit breaker and an SSRF guard.","phase":"engineering"}
{"id":"ENG-38","description":"In-app inbox fed by the outbox, with no email delivery.","phase":"engineering"}
{"id":"ENG-39","description":"Per-token and per-account token buckets in the operation-table middleware with Retry-After.","phase":"engineering"}
{"id":"ENG-40","description":"R6: claim extensions for claimable tasks and attestation kinds for CI results and review approvals; claim and attestation cursor paging already exists.","phase":"engineering"}
{"id":"ENG-41","description":"Review feature: changes and stacks from refs/for with change-ids, branch view, approvals as attestations and merge via land.execute.","phase":"engineering"}
{"id":"ENG-42","description":"Tasks feature replacing issues, claimable through Ruk leases.","phase":"engineering"}
{"id":"ENG-43","description":"CI core with a leased DAG, content-addressed cache, chunked logs, persisted request ids, git-wire-only runners and results as attestations.","phase":"engineering"}
{"id":"ENG-44","description":"Reuse MergeNotice unchanged and report effective mode and language_set_version on every mergeability and land result.","phase":"engineering"}
{"id":"ENG-45","description":"R4: push with credentials per call, mirror mode, ref selection and per-ref status on mirror.rs.","phase":"engineering"}
{"id":"ENG-46","description":"Mirror worker with one run per mirror and dirty-flag rerun, reconcile, credentials only in memory and per-ref ff-only rejections.","phase":"engineering"}
{"id":"ENG-47","description":"R3 in order: first the bounded search method in ruk riding the Bulk lane with deadline, result cap and cancellation, then the rukie search op and page on top of it.","phase":"engineering"}
{"id":"ENG-48","description":"Releases backed by hosted tag.* with streaming archive downloads.","phase":"engineering"}
{"id":"ENG-49","description":"Social features: explore, stars, watches and forks via registry fork.","phase":"engineering"}
{"id":"ENG-50","description":"Repo import through ruk's offline Git conversion with verify and rollback, exposed as an admin CLI operation.","phase":"engineering"}
{"id":"ENG-51","description":"Retention and maintenance stress under rukie load, with maintenance and backups only through daemon methods in the Maintenance lane.","phase":"engineering"}
{"id":"ENG-52","description":"Kill-9 fault suite for rukie and daemon mid-push, mid-webhook, mid-CI-attest and mid-mirror, plus ruk crash/lock/IO smoke tests and shuttle cases.","phase":"engineering"}
{"id":"ENG-53","description":"Run the reference capacity profile against rukie and the daemon as the release gate, deciding the metadata store.","phase":"engineering"}
```

### Critical Files for Implementation
- `ruk/crates/ruk/src/main.rs` — the CLI that gains the `host-credential` provisioning subcommand (ENG-06) beside `serve`.
- `ruk/crates/ruk/src/daemon/ipc.rs` — the peer-uid check (533-550) and the handshake frames that ENG-07 extends with the single-use challenge.
- `ruk/crates/ruk/src/daemon/executor.rs` — the permit freed on cancel and the shared semaphore (G13, ENG-11).
- `ruk/crates/ruk-core/src/coord/admission.rs` — the private `RequestClass` (lines 33-41) and the dispatcher-only invariant behind the five-lane `ExecutorLane`.
- `ruk/crates/ruk/src/daemon/mod.rs` — the argv `CommandOpen` runner that needs the verb rule, plus buffered input, blocking streams, `InvocationOpen` (304-316), the outcome ledger and the stale read-coverage doc (612-614).