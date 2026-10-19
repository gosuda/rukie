I couldn't write `arch-sketch-eventlog.md`, because none of my tools can write files. The full sketch is below. Please save it as `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/arch-sketch-eventlog.md`.

**Summary**
- **Ruk's `op.log` can't feed the forge today.** It reads newest-first and its cursor fails as soon as a new operation lands. Its entries carry no ref deltas. Git pushes over the wire never record an operation, and attestations and claims add nothing to the log either.
- **Answer: a new long-polling Ruk read method, `op.feed`, not a streaming subscribe.** It reads forward from a stored operation id and returns typed effects. A daemon query, `heads-since`, tells the forge which repos changed, so the forge doesn't poll 10,000 repos.
- **Ruk needs eight changes (R1–R8)** before this works. They include routing `receive-pack` through Ruk's single-writer publication lane (the "lane"), enabling hosted `push` and `land.execute`, a `ref.list` method, and native `refs/for/<branch>` handling.
- **There is one forge log, `flog`.** Changes to forge data append events directly. Ruk-owned data (refs, changes, attestations, claims, land plans) enters only through the ingester, never written twice. Duplicate ingestion is blocked by a unique `(ruk_store, ruk_op, effect_ix)` key.
- **Some read models update in the same transaction as the append**, so a command handler can check them and a page can read its own write. The rest are consumers with durable cursors.
- **Consumers split each batch into `Prepare` (I/O allowed, no writes) and `Commit` (writes only).** Anything leaving the process goes through a work table with a stable key: the webhook id, a Ruk `request_id`, or a mirror push id.
- **Reading your own write:** forge writes are visible as soon as the transaction commits. For Ruk writes the forge waits up to a deadline for ingestion (`ingest.Await`). Agents can send an `after_seq` token.
- **20 Go packages.** All event types live in `flog`, the store backend sits behind `internal/store`, and domain packages never import each other.
- **Three request flows are covered:** a file view that makes no Ruk call to resolve the ref; a land that acts as a merge queue gated on CI attestations for the exact candidate commit; and a smart-HTTP push that wakes the mirror, webhook and CI consumers.
- **The red-flag check found two real costs.** The store interface has to be at domain level (about 120 methods) to keep Pebble possible, and the inbox design conflicts with this one: inbox-design.md fans out in the same transaction, while this sketch makes the inbox an async consumer. That has to be reconciled.
- The p95 numbers are design targets. Ruk daemon latency is unmeasured (ruk-domain-model.md §5).

---

# arch-sketch-eventlog.md — Sketch B: event-sourced core

Labels: **[V]** I read it in a source this session; **[I]** my inference or design choice.

## 0. Ruk facts that shape the design

| # | Fact | Source |
|---|---|---|
| F1 | `op.log` pages **newest-first** by walking `parent` links from `OpHead.last_op`. A continuation fails `E_FORBIDDEN` ("stale view") as soon as any new operation commits. A pruned position fails `E_STALE`. So `op.log` cannot follow new operations in order. [V] | `ruk/crates/ruk-core/src/coord/hosted.rs:851-936`; `/tmp/ruk-model.json` `$defs.OpLogArgs` ("absent starts at the newest operation"), `$defs.PageCursor` ("stops working when its lease expires") |
| F2 | An `OperationEntry` is `{id, parent, session, method, committed_at, undoes}` and has **no deltas**. [V] | `/tmp/ruk-model.json` `$defs.OperationEntry` |
| F3 | Operation rows are keyed big-endian `(id, part)`, and ids are allocated in order from `head.last_op`. So reading forward from a given id is a cheap ordered scan. [V] | `coord/rows.rs:85-88`; `coord/lane/commit.rs:317-333,458` |
| F4 | Only `Refs, RefConflicts, Changes, ChangeHistory, Workspaces, WsIndex, Pins` are tracked. `Attestations, Claims, LandPlans, PushPlans, PublishedHeads, RemoteObservations` are not. When nothing tracked changed, `record` returns `None`, so `attest.add` and `claim.*` get no operation id. [V] | `coord/oplog.rs:41-79, 209-250` |
| F5 | Only lane commits record operations. `receive-pack` applies ref CAS (compare-and-swap) through `Store::publish` directly, and `Store::publish` records nothing. **Git pushes are invisible to the op log.** [V] | `ruk-git/src/transport/serve.rs:754-800`; `transport/session.rs:778-860`; `ruk-core/src/store/mod.rs:880-927` |
| F6 | On the hosted path, `push`, `land.execute` and `op.prune` all fail `E_HOSTED`. `land.prepare` ships empty requirements and `policy_generation = 0`. [V] | `coord/prepare.rs:283-305, 3430-3436`; ruk-domain-model.md §6 |
| F7 | `CommandOpen.session_id` is "accepted and ignored". `CommandExitReport.operation_ids` is empty for CLI verbs. Command stdin is buffered up to `MAX_COMMAND_STDIN_BYTES` = 256 MiB. Outcome lookups are capped at `MAX_OUTCOME_RECORDS` = 10,000. [V] | `ruk/crates/ruk/src/daemon/mod.rs` (`CommandOpen`, `CommandExitReport`, constants) |
| F8 | `refs/for` and `refs/pull` are diverted to a `proc-receive` hook script. A failing `post-receive` hook never fails the push. [V] | `transport/proc_receive.rs:1-45`; `transport/hooks.rs` module doc |
| F9 | A change identity comes from a `change-id` commit header. Without one, Ruk mints a stable id from the commit OID. [V] | `ruk-core/src/records/import.rs` |
| F10 | Land plans expire after 1 h. Claims last at most 3600 s. `PushUpdate` carries `expected_old` and `force`, and push plans can end `uncertain`, resolved with `push.reconcile`. [V] | `/tmp/ruk-model.json` `LandPrepareReply`, `ClaimTtl`, `PushUpdate`, `PushRefStatus` |
| F11 | A Go daemon client already exists: `RukDialConn`, `OpenStream`, `RukLookupOutcome`. [V] | `ruk/forgejo/modules/git/ruk_ipc.go:250,590,618,680` |

## 1. Shape

```
 forge commands ──► [store tx: check sync read models → append flog events → apply sync read models → command receipt] ──► notifier
 Ruk daemon ── heads-since / op.feed ──► ingest ──► [store tx: append ruk-sourced events → sync read models → ruk_cursors] ──► notifier
 notifier ──► consumers (one cursor each): inbox, activity, explore, task, ci, ci-attest, lander, mirror, hook
```

Rules:
1. **One log, two sources.** Forge-owned data (users, orgs, teams, tokens, repos, reviews, tasks, CI runs, mirrors, webhook endpoints, stars and watches, releases) is appended by command handlers. Ruk-owned data (refs, changes, attestations, claims, land and push plans) is appended **only by `ingest`**.
2. **Read models come in two classes.**
   - **Synchronous:** updated in the append transaction. Commands check against them and pages read them right after a write. Tables: `repos, accounts, memberships, repo_access, refs_view, changes_view, reviews, branch_view, tasks, ci_runs, ci_jobs, mirrors, webhook_endpoints, stars, watches, releases`.
   - **Async:** consumer-driven. Tables: `inbox_*, activity, repo_stats, search_facets`.
3. **Volatile state is not an event:**
   - CI heartbeats and lease deadlines (table `ci_attempt_live`)
   - CI log chunks and artifacts (blob store)
   - webhook delivery attempts (`webhook_delivery`)
   - login attempts (`audit_events`, auth-design.md §11)
   - inbox read/done marks (consumer-local)

   This is a deliberate carve-out. Each of these would dominate the log's volume.
4. **Large text never goes in the log.** Comment, task and release bodies are content-addressed blobs (`blobs/sha256/..`). Events carry `body_ref`. Event `data` is capped at 64 KiB.

## 2. Event catalogue

**Envelope.** One row per event. Its field list is the `flog` row schema in §3.

`type` is a dotted `noun.verb`. These names are the webhook vocabulary, as inbox-design.md §8 requires.

**Internal types** (marked *int* below) never reach webhooks.

**Ruk-sourced types** (marked *R* below) are appended only by `ingest`.

### Repos, refs, releases
| type | src | key fields |
|---|---|---|
| `repo.created` | F | repo_id, owner_id, name, visibility, default_branch, ruk_store, fork_of? |
| `repo.renamed` | F | repo_id, old_name, new_name |
| `repo.transferred` | F | repo_id, from_owner, to_owner |
| `repo.settings_changed` | F | repo_id, changed{default_branch?, visibility?, description?} |
| `repo.land_policy_set` | F | repo_id, target_glob, approvals_min, requirements[{kind,result}], trusted_signers[], generation |
| `repo.archived` / `repo.unarchived` | F | repo_id |
| `repo.deleted` | F | repo_id, purge_after |
| `repo.health_changed` *int* | F | repo_id, state: ok, degraded, quarantined, store_changed; detail |
| `ref.updated` | R | repo_id, ref, old?, new?, kind: create, update, delete; ruk_op, ruk_method; resync: bool |
| `ref.conflicted` | R | repo_id, ref, sides[] |
| `release.drafted` | F | repo_id, release_id, tag, target_oid, title, notes_ref |
| `release.published` | F | release_id, tag, target_oid, assets[{name, blob, size}] |
| `release.edited` / `release.deleted` | F | release_id, changed |
| `release.asset_added` | F | release_id, name, blob, size |

### Changes and reviews (the branch view is a read model only, with no events)
| type | src | key fields |
|---|---|---|
| `change.created` | R | repo_id, change_id, head, ruk_op |
| `change.revised` | R | repo_id, change_id, head_before, head_after, revision_n |
| `change.diverged` | R | repo_id, change_id, heads[] |
| `change.abandoned` / `change.restored` | R | repo_id, change_id, ruk_op |
| `change.proposed` | R (`refs/for` push) or F (op) | repo_id, change_id, target, stack_parent?, title, push_options[] |
| `change.retargeted` | F | change_id, from, to |
| `change.review_requested` | F | change_id, reviewer_user? or team?, by |
| `change.voted` | F | change_id, revision (head oid), voter, vote: approve, reject, neutral |
| `change.commented` | F | change_id, comment_id, thread_id, revision, path?, line?, side?, body_ref |
| `change.thread_resolved` | F | thread_id, by |
| `change.land_requested` | F | change_id, target, requested_by, request_id |
| `change.land_prepared` | R | change_id, plan_id, candidate, base, target, requirements[], expires_at |
| `change.landed` | R | change_id, plan_id, candidate, base, target, ruk_op |
| `change.land_failed` | F | change_id, plan_id?, reason: attest_failed, plan_expired, stale_exhausted, policy |
| `change.closed` | F | change_id, reason: landed, abandoned, superseded |
| `attestation.added` | R | repo_id, subject, kind, result, signer, digest, status, supersedes? |

### Tasks
| type | src | key fields |
|---|---|---|
| `task.created` | F | repo_id, task_no, title, body_ref, labels[], paths[] (claim scope) |
| `task.edited` | F | task_no, changed |
| `task.labeled` / `task.unlabeled` | F | task_no, label |
| `task.assigned` | F | task_no, assignee |
| `task.claimed` | F | task_no, user_id, token_id?, ruk_claim_id, paths[], expires_at |
| `task.claim.renewed` | F | task_no, old_claim_id, ruk_claim_id, expires_at |
| `task.claim.released` | F | task_no, ruk_claim_id, by |
| `task.claim.expired` | F | task_no, ruk_claim_id |
| `task.linked` | F | task_no, change_id, via: trailer or op |
| `task.commented` | F | task_no, comment_id, body_ref |
| `task.closed` / `task.reopened` | F | task_no, reason, by_change? |
| `claim.acquired` / `claim.released` *int* | R | repo_id, claim_id, holder_session, paths[], mode, expires_at |

### CI
Heartbeats and log chunks are not events. CI's design is in research-ci-runner.md §7.1.

| type | src | key fields |
|---|---|---|
| `ci.run.requested` | F | repo_id, run_id, trigger: ref, change, land, manual, schedule; subject_oid, tree_oid, ref?, change_id?, plan_id?, workflow_digest, group |
| `ci.job.queued` | F | run_id, job_id, name, action_digest, pure, deps[] |
| `ci.job.cached` | F | job_id, source_attempt_id |
| `ci.attempt.leased` | F | job_id, attempt_id, runner_id, fence, lease_expires |
| `ci.attempt.started` | F | attempt_id, fence |
| `ci.attempt.lost` | F | attempt_id, fence, reason: lease_expired, runner_gone |
| `ci.attempt.finished` | F | attempt_id, fence, result: passed, failed, cancelled, infra; result_digest, log_blob, artifacts[] |
| `ci.job.finished` | F | job_id, result, attempt_id |
| `ci.run.finished` | F | run_id, result, duration_ms. This is the `ci.finished` of inbox-design.md §8. |
| `ci.run.cancelled` | F | run_id, reason: superseded, manual; by |

### Mirrors
Names follow inbox-design.md §8.

| type | src | key fields |
|---|---|---|
| `mirror.created` | F | repo_id, mirror_id, target_url (no credentials), host: github, gitlab, forgejo, generic; mode: exact, ff_only; refs: all, default_and_tags, globs[]; interval_s |
| `mirror.updated` / `mirror.deleted` | F | mirror_id, changed |
| `mirror.synced` | F | mirror_id, upto_seq, pushed[], deleted[], ruk_plan |
| `mirror.failed` | F | mirror_id, upto_seq, class: auth, not_found, rate_limited, rejected_ref, too_large, push_protection, network, quota, uncertain; retry_at, per_ref[] |
| `mirror.needs_attention` | F | mirror_id, reason (e.g. repeated 401 or 403) |
| `mirror.recovered` | F | mirror_id |

### Webhooks, social, accounts
| type | src | key fields |
|---|---|---|
| `webhook.endpoint.created` *int* | F | endpoint_id, scope: repo_id or org_id; url, types[], secret_version |
| `webhook.endpoint.secret_rotated` *int* | F | endpoint_id, secret_version, previous_valid_until |
| `webhook.endpoint.disabled` | F | endpoint_id, reason: gone_410, exhausted, backlog_cap, manual |
| `webhook.endpoint.updated` / `.deleted` *int* | F | endpoint_id |
| `repo.starred` / `repo.unstarred` | F | repo_id, user_id |
| `repo.watch_set` | F | repo_id, user_id, level: participating, all, ignore |
| `user.created` / `user.disabled` / `user.renamed` | F | user_id, name |
| `org.created`, `org.member_added`, `org.member_removed` | F | org_id, user_id, role |
| `team.created`, `team.member_added`, `team.member_removed`, `team.repo_granted` | F | team_id, user_id?, repo_id?, perm |
| `token.created` / `token.revoked` *int* | F | token_id, user_id, scopes, repos[], expires_at (never the secret) |

Forks are `repo.created` with `fork_of` set.

## 3. Storage

Everything lives behind `internal/store`. The backend (SQLite, Pebble or PostgreSQL) is still open. `store/sqlite` is the first implementation [I].

| table | contents | key / uniqueness |
|---|---|---|
| `flog` | `seq, id, type, schema, stream, stream_ver, repo_id, actor_user, actor_token, via, request_id, causation, at, src, ruk_store, ruk_op, effect_ix, data` | PK `seq`; UNIQUE `id`; UNIQUE `(stream, stream_ver)` for optimistic concurrency; UNIQUE `(ruk_store, ruk_op, effect_ix)` for ingest dedupe |
| `log_head` | `high_water` | single row |
| `command_receipts` | `request_id, actor, op, args_digest, seq_from, seq_to, reply, at` | PK `request_id`. Same id with a different digest → 409. Kept 7 days, matching Ruk's receipt window (ruk-domain-model.md). |
| `cursors` | `consumer, seq, updated_at` | PK `consumer` |
| `ruk_cursors` | `repo_id, ruk_store, last_op, state` (ok, resync, halted) | PK `repo_id` |
| `consumer_dead` | `consumer, seq, error, attempts, at` | PK `(consumer, seq)` |
| `ruk_sessions` | `session_id → user_id, token_id` | maps Ruk ops back to forge actors |
| `blobs/` | content-addressed bodies, logs, artifacts, assets | path by sha256 |

**Visibility contract.** This is part of the store interface and is the subtle part.
- **Rule:** readers only see `seq <= high_water`, and the store guarantees no holes below `high_water`.
- **SQLite:** a single writer connection plus WAL snapshots give this.
- **PostgreSQL:** each append takes `UPDATE log_head SET high_water = high_water + n RETURNING`. That row lock serialises appenders, so commit order equals seq order.
- **Pebble:** one appender goroutine commits batches.

**Retention.**
- **Default:** keep the log forever. Inbox sizing (10k events/day, inbox-design.md §2.1) at ~1 KiB/event [I] is about 3.6 GB/year.
- **Archive:** `rukie admin log archive --before <seq>` exports cold JSONL segments plus a snapshot of the read models.
- **Rebuilds:** `rukie admin rebuild <readmodel>` replays the log for any read model.
- **Schema upgrades:** in-place migrations. Replay is a recovery tool, not the upgrade path.

**Schema evolution.** Each type has a `schema` number and `flog` holds upcasters. Stored events are never rewritten. The webhook payload is the upcast current schema.

## 4. Consumer cursor protocol (at-least-once plus idempotency)

```go
type Consumer interface {
    Name() string
    Types() []flog.Type
    Prepare(ctx context.Context, batch []flog.Event) (any, error) // I/O allowed, no store writes
    Commit(tx store.Tx, plan any) error                            // store writes only, no I/O
}
```

`flog.Runner` runs one goroutine per consumer, owned by `app.Run`'s errgroup (single process in v1):
1. `cur := store.Cursor(name)`. Read `batch := ReadLog(after=cur, limit=256, types)`.
2. If the batch is empty, wait on `notifier.Wait(ctx, cur)` (a channel closed on each high-water advance), or a 5 s timer as a safety net.
3. Call `Prepare` outside any transaction.
4. In one transaction, call `Commit(plan)` and set `cursors.seq = last(batch).seq`.
5. On error, retry the same batch with backoff from 200 ms up to 60 s. After 10 failures on the same seq, insert into `consumer_dead`, advance the cursor past it, and mark health degraded. `rukie admin consumer replay <name> --seq N` re-runs it.
6. Consumers may append events in `Commit`, with `causation` set to the triggering event id. A consumer's output types are never in its own input set, and `hook` drops all `webhook.*` types, so no loops.

Idempotency keys, used when a batch replays after a crash between `Prepare` and commit, or after a manual cursor reset:

| consumer | effect | idempotency |
|---|---|---|
| `inbox` | `inbox_item` | PK `(user_id, event_id)` |
| `activity` | `activity` rows | PK `seq` |
| `explore` | `repo_stats` counters | per-row `applied_seq`: `UPDATE … SET stars = stars + 1, applied_seq = :seq WHERE applied_seq < :seq` |
| `task` | links, `task.closed`, claim-expiry timers | UNIQUE `(causation, type)` on emitted events |
| `ci` | `ci_runs` + `ci.run.requested` | UNIQUE `(trigger_event_id, workflow_path)` |
| `ci-attest` | `attest_outbox` row, then Ruk `attest.add` | Ruk `request_id = uuidv5(ns, outbox_id)`; `signed_at` fixed at finish (research-ci-runner.md, later corrections) |
| `lander` | Ruk `land.execute` | Ruk `request_id = uuidv5(ns, plan_id)` |
| `mirror` | `mirror_dirty`, then Ruk `push` | `mirror_dirty` PK `mirror_id`; Ruk `request_id = uuidv5(ns, mirror_id, upto_seq)`; desired state is recomputed every time |
| `hook` | `webhook_delivery`, then HTTP POST | PK `(endpoint_id, event_id)`; `webhook-id = event.id` stays stable across retries (Standard Webhooks spec lines 137, 210) |

## 5. Reading your own write in a synchronous request

1. **Forge-sourced command (HTML, API, MCP or CLI).** One transaction: check the synchronous read models (optimistic `stream_ver`), append, apply the synchronous read models, write the command receipt. Then:
   - HTML gets `303 See Other` to the resource. Its page reads synchronous read models and so already shows the write.
   - API and MCP replies carry `seq` and the header `Rukie-Seq: <seq>`.
2. **Async read models.** A read with `after_seq=N` (query parameter or `Rukie-After-Seq` header; HTML gets it on the redirect URL) calls `flog.AwaitConsumer(ctx, consumer, N, 250ms)`. On timeout it renders anyway, with a server-side "still updating" notice and `<meta http-equiv="refresh" content="2">`. No JavaScript.
3. **Ruk-sourced writes.**
   - **Typed mutation:** `MutationOutcome.operation` gives `opID`. Call `ingest.Kick(repo)`, then `ingest.Await(ctx, repo, opID, 2s)`. The ingest transaction also updates the synchronous read models, so the following page is current. The handler then returns the flog `seq` of the ingested events.
   - **Git push:** `gitwire` reads `CommandExitReport.operation_ids` (needs R3) and waits for ingestion before ending the HTTP response, with a 1 s cap. A timeout never fails the push.

## 6. Go package layout (20 packages; go 1.27, stdlib first)

| # | package | owns | may import |
|---|---|---|---|
| 1 | `cmd/rukie` | server `serve`, admin CLI, generated client CLI (`rukie <op> --json`) | app, ops |
| 2 | `internal/app` | config (`rukie.toml`), wiring, errgroup lifecycle, shutdown order | all below |
| 3 | `internal/ops` | **the operation table**: `Op{Name, In, Out, Scope, Perm, Emits []flog.Type, Form, Handler}`; derives routes, OpenAPI 3.1, MCP tool schemas, CLI | account, flog |
| 4 | `internal/web` | templ pages, form binding, PRG, ETag caching, zero JavaScript | ops |
| 5 | `internal/api` | JSON HTTP, RFC 9457 problems, OpenAPI document | ops |
| 6 | `internal/mcp` | MCP server over `ops` (go-sdk) | ops |
| 7 | `internal/account` | users, orgs, teams, tokens, sessions, argon2id, TOTP, OIDC, permission checks | flog, store |
| 8 | `internal/flog` | envelope, **all typed payload structs** (one file per domain), upcasters, `Append`, `Notifier`, `Runner`, `AwaitConsumer` | store |
| 9 | `internal/store` | `Store`, `Tx`, `LogRow`, `ReadLog`, `HighWater`, cursors, per-domain table interfaces | — |
| 10 | `internal/store/sqlite` | first implementation (modernc.org/sqlite, goose migrations per research-go-stack.md) | store |
| 11 | `internal/ruk` | daemon client: framing and credits (port of `ruk_ipc.go`), typed invocations generated from the Ruk schema, session pool, request-id helpers | — |
| 12 | `internal/ingest` | `heads-since` watcher, `op.feed` workers, effect→event translation table, resync, `Kick`/`Await` | ruk, flog, store |
| 13 | `internal/gitwire` | smart HTTP and SSH (`x/crypto/ssh`) → `CommandOpen` streams; push size and auth gates | ruk, account, ingest |
| 14 | `internal/repo` | repos, browse, file and blame views, code search (via Ruk `grep`, R8), releases, stars, watches, forks, `explore` consumer | flog, store, ruk |
| 15 | `internal/review` | changes, reviews, branch view, land orchestration, `lander` consumer | flog, store, ruk |
| 16 | `internal/task` | tasks, claims via Ruk, `task` consumer, claim-expiry timer | flog, store, ruk |
| 17 | `internal/ci` | scheduler, runner lease RPCs, fences, cache, `ci` and `ci-attest` consumers | flog, store, ruk |
| 18 | `internal/mirror` | `mirror` consumer, push worker, reconcile timer | flog, store, ruk |
| 19 | `internal/hook` | endpoints, Standard Webhooks signer and sender, `hook` consumer | flog, store |
| 20 | `internal/inbox` | `inbox` and `activity` consumers, inbox pages | flog, store |

Domain packages (14–20) never import each other. They talk only through events. `account` is imported only by `ops`, `gitwire` and `app`; domains receive an already-authorised `flog.Actor`.

## 7. Ruk integration

**Options weighed:**
- **(A) Poll `op.log`:** impossible (F1, F2, F4, F5).
- **(B) Streaming `op.subscribe`:** needs per-subscriber state in the daemon and backpressure, and outcome lookup doesn't cover reads.
- **(C) Chosen: `op.feed`, a read with a durable cursor, plus a cross-repo wake-up signal.** No subscriber state lives in Ruk. A restart resumes from `ruk_cursors.last_op`.

**Required Ruk changes:**
- **R1 `op.feed` (kind `read`).**
  - **Args:** `{after: OperationId|null, limit: PageLimit≤1000, wait_ms: 0..25000}`.
  - **Reply:** `{view: ViewInfo, head: OperationId, oldest_retained, items: [FeedEntry{entry: OperationEntry, effects: [Effect], effects_truncated}], next_after}`.
  - **`Effect` is one of:**
    - `ref{name, old?, new?}`
    - `change_heads{change, before[], after[], state}`
    - `review_push{change, target, push_options[]}`
    - `attestation{record}`
    - `claim{claim, holder, paths, mode, expires_at, state}`
    - `land_plan{plan, change, candidate, base, target, requirements, expires_at, state: prepared or executed}`
    - `push_plan{plan, url_redacted, refs[PushRefOutcome], state}`
  - **Implementation:** an ordered scan from `op_key(after+1, 0)`. Deltas are decoded inside Ruk with `OpDeltaReader`; Go never parses postcard. `wait_ms` blocks on a `watch` channel that the lane commit updates.
  - **Pruned position:** `after < oldest_retained − 1` fails `E_STALE`.
- **R1b daemon control query `heads-since`.** A stream-zero long-poll beside `outcome-lookup`: `{since_token} → {changed: [{repo_handle, store, last_op}], token}`. One forge goroutine watches all repos.
- **R2 Record more tables.** Split `tracked()` into `undoable()` (today's 7 tables) and `journaled()` (those 7 plus Attestations, Claims, LandPlans, PushPlans). `undo` inverts only undoable deltas. Then `attest.add`, `claim.*`, `land.*` and `push` get operation ids.
- **R3 `receive-pack` through the lane.**
  - New method `git.receive`: ref CAS plus commit facts in one lane batch, with an operation recorded.
  - `CommandExitReport.operation_ids` is filled in.
  - This replaces the direct `Store::publish` in `apply_refs`.
- **R4 Hosted `push`, `push.reconcile` and `land.execute`.** `land.execute` lands onto a local target, so `remote` becomes optional (meaning "this store"). A new `policy.land.set{target_glob, requirements, trusted_signers}` bumps `policy_generation`. The forge calls it from `repo.land_policy_set`.
- **R5 Honour `CommandOpen.session_id`.** Pushes are then attributed to the user's Ruk session and claim enforcement applies (`E_CLAIMED`).
- **R6 `ref.list{prefix, page}`.** For bootstrap and resync; there is no typed ref listing in the 47 methods today.
- **R7 Native `refs/for/<branch>`.** Handled in-process with no hook script.
  - The pushed commit becomes a head of the change named by its `change-id` header (F9). Accepting a Gerrit `Change-Id:` trailer is [I], not supported today.
  - Ruk emits a `review_push` effect.
  - It replies `ok` with `option refname refs/changes/<id>`.
- **R8 Retention and search.**
  - `op.prune` gets a retention policy of at least 7 days (F6).
  - A `grep{rev, pattern, paths, page}` read serves code search.

**Ingester.**
1. **Watch:** one goroutine long-polls `heads-since`. Changed repos go to a worker pool of 8.
2. **Read:** each worker calls `op.feed{after: last_op, limit: 1000}` and loops while `next_after < head`.
3. **Translate:** a lookup table keeps the cyclomatic complexity under 10.

   | Ruk effect | forge event(s) |
   |---|---|
   | `ref` | `ref.updated` |
   | `change_heads`, before empty → one head | `change.created` |
   | `change_heads`, one head → another | `change.revised` |
   | `change_heads`, more than one head | `change.diverged` |
   | `change_heads`, state change | `change.abandoned` / `change.restored` |
   | `review_push` | `change.proposed`, or `change.revised` if a review is already open |
   | `attestation` | `attestation.added` |
   | `claim` | `claim.*` |
   | `land_plan`, prepared | `change.land_prepared` |
   | `land_plan`, executed | `change.landed` |

4. **Attribute:** the actor comes from `ruk_sessions[entry.session]`. Unknown sessions are recorded as system actor `ruk:<session>`.
5. **Commit:** one transaction per page appends the events (`INSERT … ON CONFLICT (ruk_store, ruk_op, effect_ix) DO NOTHING`), applies the synchronous read models, and sets `ruk_cursors.last_op`.
6. **Resync** after `E_STALE`, or when `ViewInfo.store` differs from `ruk_cursors.ruk_store` (the latter halts the repo until an admin confirms):
   - Call `ref.list` and diff it against `refs_view`, emitting `ref.updated{resync: true}`.
   - Call `attest.list` to fill `attestation.added` gaps.
   - CI ignores `resync: true`. Mirrors don't need it, because they act on current state rather than on individual events.

## 8. Request flows

### (a) Render a file at a revision: `GET /{owner}/{repo}/src/{rev}/{path…}` (op `repo.file.get`)
1. `ops` authorises `read` against `repo_access`, a synchronous read model with no Ruk call.
2. Resolve `rev`:

[169 more lines in resource. Use :604 to continue]
2. Resolve `rev`:
   - hex OID → used as-is
   - branch or tag → `refs_view(repo_id, refs/heads/rev | refs/tags/rev)`
   - change id → `changes_view` single head (divergent → 409 problem listing the heads)
3. Call `ruk.File{at:{head:{type:"git", oid}}, path}` on a pooled read session. Inline bytes are used directly. Streamed bytes go through credit windows. Blobs over 1 MiB render a "raw" link. The `raw/` endpoint streams.
4. Render with templ. READMEs go through goldmark. Highlighting is capped at 512 KiB [I].
5. Caching:
   - OID URLs: `ETag = blob oid`, `Cache-Control: immutable`.
   - Branch URLs: `no-cache` plus ETag.
6. **Target:** p95 ≤ 80 ms for a blob up to 256 KiB on a warm daemon [I; unmeasured].
7. **Failures:**
   - unknown ref or path → 404 problem
   - daemon down → 503 with `Retry-After: 5`
   - `FileBody.conflict` → a conflict-terms view
   - `E_CORRUPT` → 500, the repo is marked degraded, and `repo.health_changed` is appended

### (b) Land a change with required attestations (op `change.land`)
1. Authorise `land` on the target, using `repo_access` plus branch rules.
2. In the forge transaction:
   - Check the `reviews` read model: review open, current head `H`, `approvals_min` met on `H`, no `reject` on `H`. Otherwise return 409 `land-blocked` listing what is missing.
   - Append `change.land_requested`.
3. Call Ruk `land.prepare{change:{change, head:H}, target}` with `request_id = command id`. This returns plan `P`, candidate `C`, base `B`, requirements `Rq` (from R4 policy) and `expires_at`. Ingest appends `change.land_prepared`.
4. If every `Rq` is satisfied by live `attestation.added` rows for subject `C`, go to step 6. This is the case when `C == H` and CI already passed on `H`.
5. Otherwise return `202 Accepted` with `Location` pointing at the change page, which shows the plan as waiting on `Rq`.
   - The `ci` consumer reacts to `change.land_prepared` by scheduling a run with `trigger: land` on `C`.
   - `ci-attest` signs the `ci/<job>` verdicts.
   - Ingest appends `attestation.added`.
   - The `lander` consumer sees the requirements met for `P` and continues.
6. Call Ruk `land.execute{plan: P, candidate: C}` with `request_id = uuidv5(P)`. Then `ingest.Await(op)`. That yields `ref.updated(target, B→C)` and `change.landed`. The synchronous read models now show `reviews.state = merged` and an updated `branch_view`.
7. Async effects:
   - `change.closed{landed}` (lander)
   - `task.closed` for linked tasks
   - mirror marked dirty
   - webhooks
   - inbox
8. **Failures:**
   - **`E_STALE` (target moved):** re-prepare up to 3 times; a new `C` needs a new CI run. After that, `change.land_failed{stale_exhausted}`.
   - **Plan expired (1 h):** re-prepare once, otherwise `land_failed{plan_expired}`.
   - **Any `fail` verdict:** `land_failed{attest_failed}`.
   - **`LandLocalState = conflicting_local_change`:** still recorded as landed, because the remote is confirmed. Append `repo.health_changed`.
   - **Lost connection:** retry with the same `request_id`; the Ruk receipt replays.

### (c) Push over smart HTTP, triggering mirror, webhook and CI
1. `POST /{o}/{r}.git/git-receive-pack`. `gitwire` checks Basic token auth and the `repo:write` scope (auth-design.md §10.1).
   - Over the 256 MiB stdin cap (F7) → `413` before any bytes are sent to Ruk. With no `Content-Length`, the stream is counted and cut at the cap.
   - The actor's Ruk session is resolved.
2. `CommandOpen{repository, request_id, argv: receive-pack stateless, session_id}` (R5). The body is streamed to stdin under credit windows, and stdout is streamed back as the report.
3. Ruk:
   - ingests and verifies the pack
   - handles `refs/for/*` natively (R7)
   - applies ref CAS via lane `git.receive` (R3)
   - exits with `operation_ids = [O]`
4. `gitwire` calls `ingest.Kick(repo)` and `ingest.Await(repo, O, 1s)`, then returns. One transaction appends `ref.updated` (and `change.*` for `refs/for`) and updates `refs_view` and `branch_view`. The notifier fires.
5. Consumers, each on its own cursor:
   - **`ci`**
     - `Prepare`: reads the workflow at `new` via Ruk `file` (I/O outside the transaction).
     - `Commit`: inserts the `ci_runs` row and appends `ci.run.requested` and `ci.job.queued`, cancelling superseded queued runs in the same `group`.
     - Runners long-poll for leases (research-ci-runner.md §7.1).
   - **`mirror`**
     - `Commit`: upserts `mirror_dirty(mirror_id, upto_seq, not_before = last_push + 10 s)` for each mirror whose ref selection matches.
     - The worker computes the desired refs from `refs_view` and the selection.
     - `exact` mode: `force: true, expected_old: null` (overwrite whatever the remote advertises). It deletes only refs that rukie itself pushed earlier and that are no longer desired, tracked in `mirror_ref_state` (research-push-mirror-targets.md §5.4).
     - `ff_only` mode: `force: false`. Non-fast-forward refs report `rejected_ref`.
     - Ruk `push{remote:{url}, atomicity: non_atomic}`. Per-ref outcomes go into `mirror_ref_state` and `mirror.synced` or `mirror.failed`. `uncertain` triggers `push.reconcile` on the next tick.
     - The reconcile timer re-marks every mirror dirty each `interval_s` (jittered), capped at 6 pushes/min per target. `Retry-After` is honoured.
     - Credentials go through Ruk's existing credential-helper hop (`transport/credentials.rs`): a URL-scoped `credential.helper=!rukie credential` reads the mirror's sealed token. No secret enters Ruk request args, receipts or plans [I].
   - **`hook`**
     - `Commit`: inserts `webhook_delivery` rows for each endpoint subscribed to the type with access to the repo.
     - The sender POSTs `{type, timestamp, data:{seq, id, repo, actor, …}}` with `webhook-id = event.id`, `webhook-timestamp`, and `webhook-signature: v1,<HMAC-SHA256(id.ts.body)>`. During a secret rotation it sends signatures from both the current and the previous secret (spec lines 141-143, 210-216).
     - 15 s timeout. Retry schedule: 5 s, 5 m, 30 m, 2 h, 5 h, 10 h, 14 h, 20 h, 24 h (spec lines 257-275).
     - `410` disables the endpoint (`webhook.endpoint.disabled`). `Retry-After` is honoured.
     - One delivery in flight per endpoint. Retries never block newer messages. Payloads carry `seq` so receivers can reorder.
   - **`inbox`**, **`activity`**, **`task`**: as in §4.

## 9. Failure paths

| failure | behaviour | recovery |
|---|---|---|
| Crash after a Ruk mutation, before ingest | Ruk is durable; ingest catches up via `heads-since` | Client retries with the same `request_id` → Ruk receipt replay → ingest dedupe → reply rebuilt from `command_receipts` |
| Crash in a consumer between `Prepare` and commit | Cursor not advanced | Batch replays; natural keys (§4) |
| Webhook sent, crash before it is marked | Resent with the same `webhook-id` | Receiver dedupes (spec line 233) |
| Mirror pushed, crash | Same desired state re-pushed; no-op | `push.reconcile` if `uncertain` |
| `attest.add` sent, crash | Same `request_id` | Receipt replay (7 days), then a deterministic statement after that |
| Ruk daemon down | Reads 503 with `Retry-After`; pushes fail; ingest backs off 1–30 s | Consumers keep working on events already ingested |
| Ruk store quarantined (`E_CORRUPT`) | Ingest halts that repo; `repo.health_changed{quarantined}`; repo read-only | Admin repair, then resync |
| Ops pruned before ingest (`E_STALE`) | Repo goes to `resync` | `ref.list` + `attest.list` diff, `resync: true` events |
| Ruk `StoreId` changed | Repo `halted` | Admin `rukie admin repo resync` |
| Metadata store I/O error or disk full | Appends fail → commands 503; consumers stall; health red | Operator |
| Poison event | Dead-lettered after 10 tries; health degraded | `admin consumer replay` |
| Webhook backlog over 10k pending per endpoint | Endpoint disabled | Inbox item for the owner |
| Plan expired or target moved during CI | Re-prepare (bounded) | `change.land_failed` |
| Claim TTL (≤ 3600 s) lapses | `task` timer confirms via `claim.list`, then `task.claim.expired` | User re-claims |
| Outcome lookup evicted (over 10,000) on a push stream | Git client sees an error; truth is still ingested from Ruk | — |
| Optimistic concurrency conflict on `(stream, stream_ver)` | Command retried once | 409 problem |
| Webhook URL resolves to a private address | Rejected at send time (resolve and pin) | Admin allow-list |

**Goroutine shutdown order** (all under the `app.Run` errgroup context):
1. Stop the HTTP and SSH listeners.
2. Drain in-flight requests (30 s).
3. Cancel the ingest watcher and workers.
4. Cancel the consumer runners; each finishes its current transaction.
5. Stop the hook, mirror and CI worker pools and the timers.
6. Close Ruk connections.
7. Close the store.

## 10. Red-flag self-check

- **Hidden complexity.**
  - Two logs plus a translation layer. Kept bounded: data flows one way (Ruk → forge), Ruk decodes its own rows, and there is a single dedupe key.
  - Seq visibility in commit order is a store contract that every backend must prove with a test (concurrent appenders, reader never sees a hole).
  - Read-your-write waits add latency. They have hard deadlines and degrade to a "still updating" notice.
- **Leaky abstraction.**
  - Keeping Pebble possible forces a domain-level store interface (about 120 methods [I]) rather than SQL in the domains. That is the price of the open store decision.
  - Ruk ids (`ChangeId`, `Oid`, `PlanId`) appear in events on purpose: they are the domain vocabulary.
- **Unbounded scope.** The "every mutation is an event" rule has explicit exclusions: heartbeats, log chunks, delivery attempts, login attempts, inbox marks. Event bodies are blobs and the payload cap is 64 KiB.
- **Premature generality.** Avoided: no multi-process consumer leases, no external broker, no snapshots, no generic CQRS framework. Single process in v1.
- **Missing error handling.**
  - R1–R8 are hard prerequisites. Without R2, R3 and R5, pushes, attestations and claims are invisible or unattributed.
  - The push size cap is 256 MiB (F7).
  - **Cross-doc conflict:** inbox-design.md §2 puts inbox fan-out in the same transaction; this sketch makes it an async consumer (target lag p99 < 1 s [I]). The two must be reconciled.
- **Bar.** Translation tables and the split `Prepare`/`Commit` keep functions under cyclomatic 10. Every goroutine is listed above with its shutdown path.
- **p95 design targets [I]:** file page 80 ms, change page 120 ms, list pages 60 ms, forge-only command 50 ms. Read-your-write wait is at most 250 ms (async) and 2 s (Ruk).

## 11. Build order

1. `store` + `store/sqlite` + `flog`, with the visibility contract test.
2. `ruk` client (port of `ruk_ipc.go`) and Ruk R1, R1b, R2, R3, R5, R6.
3. `ingest` + `gitwire` + `repo` (browse).
4. `account`, then `ops`, `web`, `api`, `mcp`.
5. `review` + Ruk R4 and R7.
6. `ci`.
7. `hook`, `mirror` (needs R4 push), `task`.
8. `inbox`, `explore`, R8 search.

Each step is a commit on top of Gitea history that builds. Gitea packages are deleted in the same commit that replaces their surface.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/oplog.rs` — the tracked/journaled split (R2) and the delta reader that `op.feed` decodes with.
- `ruk/crates/ruk-core/src/coord/hosted.rs` — `read_oplog_page` (why `op.log` can't be followed); home of `op.feed` and the `wait_ms` notify.
- `ruk/crates/ruk-git/src/transport/serve.rs` and `transport/session.rs` — `receive-pack` direct `Store::publish` and `apply_refs`, to move onto the lane (R3) and handle `refs/for` natively (R7).
- `ruk/crates/ruk-core/src/coord/prepare.rs` — hosted `E_HOSTED` refusals for push, `land.execute` and `op.prune`, and the empty land requirements (R4, R8).
- `ruk/forgejo/modules/git/ruk_ipc.go` and `ruk/crates/ruk/src/daemon/mod.rs` — the Go framing client to port into `internal/ruk`, and the daemon `CommandOpen`/`ExitReport`/control-query types (R1b, R5).

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Set expected_old from mirror_ref_…

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Fix flow (b): a crash between…

[advisor] <advisory advisor="overlycritic" severity="nit" guidance="weigh, don't blindly obey"> Remove or check the Standard Webh…

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Map your Ruk changes to the plan…

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Name the mechanism that runs synch…

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Remove the `push_plan` effect from…

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Make `lander` abort when the revie…

[advisor] <advisory advisor="minimax" severity="nit" guidance="weigh, don't blindly obey"> Add the `land.prepare` failure row to …

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> State in R2 the two reader checks …

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> R3 fixes only receive-pack. Other …

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Add a sync-projector registry to `…

[advisor] <advisory advisor="qwen" severity="nit" guidance="weigh, don't blindly obey"> Fix the §6 import table. As written, no p…

[advisor] <advisory advisor="qwen" severity="nit" guidance="weigh, don't blindly obey"> Give resync events a dedupe key. As desig…