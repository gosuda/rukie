**I could not write the artifact.** My tools are `read`, `grep`, `web_search` and `ast_grep`. None of them writes files, so nothing exists at `local://research-ci-runner.md` or in the orchestrator's `local/` directory. The full revised document is below. Please save it as `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-ci-runner.md`.

Changes since my previous message, from the review:
- **Result adoption is dropped.** A fenced-out runner's result is never written to the cache.
- **Cache entries are written only by the winning fence, from trusted scope, for declared-pure jobs.**
- **The design is cut to a v1 core. The rest is listed as deferred.**
- **There is now a coexistence story with rukie's Gitea-Actions YAML.** v1 reuses the existing `jobparser` rather than adding a second parser.
- **Sandbox order is fixed:** podman primary, bubblewrap fallback only if a probe passes, gVisor optional, Firecracker deferred.
- **A host check was done.** `/proc/sys/kernel/apparmor_restrict_unprivileged_userns` reads `1` on this machine, so unprivileged-userns tools are restricted here. I could not run a probe; I have no execution tool.
- **Corrections on citations:** GitHub issues #1546/#2624 are no longer cited for duplicate execution. Duplicate execution is marked unknown.
- **Ruk already fences with lease epochs;** the CI fence mirrors it.
- **The `AttestStatement` result stays a plain verdict.** The evidence digest goes in a new separate field.

---

# research-ci-runner.md — Durable, resilient, efficient built-in CI for rukie

**Confidence labels:** **[V]** a source I read in this session says it; **[I]** my inference or design choice; **[U]** not verified.

## 0. Recommendation (v1)

1. **State.** A server-owned job state machine in SQLite (WAL, `synchronous=FULL`, single writer goroutine).
2. **Runners.** Pull runners hold **leases**. Every grant bumps a monotonic **fence**. The server rejects any attempt-scoped write that carries a stale fence.
3. **Delivery.** At-least-once, with idempotent result commits. Each result commit also writes an **outbox** row for side effects.
4. **Skip rule.** A job may be skipped only if it is declared **pure** and a passing result exists at the same **action digest**. The digest covers the Git tree OID, the canonical job spec, the image digest, declared env and upstream output digests.
5. **Sandbox.** Rootless podman is the primary path.
6. **Job format.** Canonical job-spec JSON is the digest basis. Front-ends: a TOML schema, plus an adapter over rukie's existing GHA-style YAML parser.
7. **Attestation.** Results are recorded as Ruk SSHSIG attestations. The in-toto/SLSA statement is stored as a content-addressed blob next to them.

## 1. Existing CI: failure modes

Cells say "unknown" where nothing in a source I read covers them. I did not infer behaviour for those cells.

| System | Server restart | Runner crash | Network partition | Duplicate execution |
|---|---|---|---|---|
| **GitHub Actions** | Unknown. GitHub does not publish server internals. | The runner renews its job lock every 60 s. After a failed renewal it retries until `LockedUntil + 5 min`, then stops renewing ([JobDispatcher.cs](https://raw.githubusercontent.com/actions/runner/main/src/Runner.Listener/JobDispatcher.cs)) [V]. | Same lock-retry window [V]. [Issue #1546](https://github.com/actions/runner/issues/1546) shows a job that finished on the host while CI reported "lost communication" [V as a symptom report]. | **Unknown.** #1546 shows no duplicate run. #2624 lists causes (killed runner, starved CPU or memory, blocked network), not duplication. A job not picked up within 60 s is re-queued ([docs](https://docs.github.com/en/actions/reference/runners/self-hosted-runners)) [V]. |

GitHub Actions limits, from the [limits page](https://docs.github.com/en/actions/reference/limits.md) [V]:

| Limit | Value |
|---|---|
| Workflow run | 35 days |
| Self-hosted job execution | 5 days |
| GitHub-hosted job execution | 6 hours |
| Self-hosted job queue | 24 hours |
| Re-runs per run | 50 |
| Workflow trigger events | 1500 per 10 s per repo |

Other GitHub facts:
- Re-runs are possible up to 30 days after the run, and reuse the same `GITHUB_SHA` and `GITHUB_REF` ([re-run docs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs.md)) [V].
- GitHub recommends ephemeral runners because it "cannot guarantee" persistent ones aren't assigned jobs while shut down ([docs](https://docs.github.com/en/actions/reference/runners/self-hosted-runners)) [V].

| System | Server restart | Runner crash | Network partition | Duplicate execution |
|---|---|---|---|---|
| **GitLab CI** ([retry keywords](https://docs.gitlab.com/ci/yaml/#retry)) | Unknown. | `runner_system_failure`, `runner_interrupted`, `no_updates_running`, `stuck_pending_*`, `scheduler_failure`, `data_integrity_failure`; each is an opt-in retry class [V]. | `no_updates_running` covers "runner loses connectivity" [V]. | Unknown. |

GitLab facts:
- The runner polls every `check_interval` (default 3 s). Workhorse long-polls for 50 s ([runner config](https://docs.gitlab.com/runner/configuration/advanced-configuration/)) [V].
- The job token is valid only while the job runs and is revoked on finish ([job token](https://docs.gitlab.com/ci/jobs/ci_job_token/)) [V].
- Logs go to the server in chunks: every 3 s if the log is open, every 60 s otherwise. They flush to a persistent store at 128 KB, with a 100 MB size cap ([job logs](https://docs.gitlab.com/administration/cicd/job_logs/)) [V].
- `interruptible` and `workflow:auto_cancel:on_new_commit` (default `conservative`) are documented in the [retry/YAML reference](https://docs.gitlab.com/ci/yaml/#retry) [V].

| System | Server restart | Runner crash | Network partition | Duplicate execution |
|---|---|---|---|---|
| **Buildkite** ([lifecycle](https://buildkite.com/docs/agent/lifecycle.md), [retry](https://buildkite.com/docs/pipelines/configure/retry.md)) | SaaS; unknown. | No heartbeat for 3 minutes marks the agent lost within the next 60 s; the job gets exit `-1`. A graceful stop gives `signal_reason: agent_stop` [V]. | Heartbeat runs in its own loop. Pings are jittered ([agent_worker.go v3.100.0](https://github.com/buildkite/agent/blob/v3.100.0/agent/agent_worker.go)) [V]. | Unknown. |

Buildkite facts:
- A retry creates a new job; history is immutable. The default retry limit is 2 and the maximum is 10. Canceled jobs are never auto-retried [V].
- Cancel sends `cancel-signal` (SIGTERM by default), then SIGKILL after 10 s [V].
- On a failed log chunk upload the agent logs "Giving up on uploading chunk … partial build log" ([log_streamer.go](https://github.com/buildkite/agent/blob/v3.100.0/agent/log_streamer.go)) [V].

| System | Server restart | Runner crash | Network partition | Duplicate execution |
|---|---|---|---|---|
| **Woodpecker v3.18** | See below. | See below. | See below. | Unknown. |

Woodpecker, [`queue.go`](https://raw.githubusercontent.com/woodpecker-ci/woodpecker/v3.18.0/server/queue/queue.go), [`persistent.go`](https://raw.githubusercontent.com/woodpecker-ci/woodpecker/v3.18.0/server/queue/persistent.go), [`fifo.go`](https://raw.githubusercontent.com/woodpecker-ci/woodpecker/v3.18.0/server/queue/fifo.go):
- The only queue backend is `TypeMemory`. It is wrapped by `WithTaskStore` when a store is configured [V].
- At startup `WithTaskStore` re-pushes only the store's task rows. `Poll` deletes the task's row once it is handed out [V]. Recovery of in-flight tasks after a restart is therefore unclear [U].
- Each running task has a `deadline`, extended by `Extend`. `resubmitExpiredPipelines` re-queues expired tasks with `ErrTaskExpired` [V].
- `Extend` rejects only a mismatched agent ID (`ErrAgentMissMatch`); no monotonic token is visible [V].

| System | Server restart | Runner crash | Network partition | Duplicate execution |
|---|---|---|---|---|
| **Gitea/Forgejo Actions** (rukie is Gitea-derived) | Default reapers: zombie 10 min, endless 3 h, abandoned 24 h (`rukie/modules/setting/actions.go:133-135`) [V]. | `StopZombieTasks` keys only on `Task.Updated` (`rukie/services/actions/clear_tasks.go:25-30`) [V]. | See the Forgejo PR below. | Unknown. |

Forgejo/Gitea details:
- [Forgejo PR #12509](https://codeberg.org/forgejo/forgejo/pulls/12509) is **closed and unmerged**, so it is evidence of a reported flaw, not of shipped behaviour [V].
  - Its report: the runner reporter serializes log and heartbeat RPCs, with no per-call timeout. A slow log POST therefore delays the heartbeat, the reaper marks the task failed, and the runner's later SUCCESS is dropped by the `IsDone()` guard.
  - The reaper pattern is the same in rukie's `clear_tasks.go`.
- `UpdateLog` returns an `AckIndex` and trims already-acked rows. `FetchTask` uses a `tasks_version` counter to make empty polls cheap (`rukie/routers/api/actions/runner/runner.go:166-343`) [V].
- rukie added `taskReportTimeout = 1m` at `rukie/models/actions/task.go:62-65` [V].

**Design lessons [I]:**
1. Keep the heartbeat tiny and on its own connection.
2. Don't mass-expire leases at boot.
3. A reaped task needs a fence, not just a status guard.
4. Log chunks need a sequence number, an offset and an ack.
5. Make retry per failure class and opt-in.
6. Cancel superseded work immediately.

## 2. Durable execution patterns


[Showing lines 1-542 of 769 (49.8KB limit). Use :543 to continue]

- **Leases and fencing.** A timeout lock is unsafe after a process pause or delayed packet unless the protected resource checks a monotonically increasing token and rejects older ones. Timeouts are only failure guesses ([Kleppmann](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html)) [V].
- **Ruk already does this.** Workspace lease epochs advance on `ws.take`, are checked with `E_LEASE_LOST`, and "never move backward"; `undo` refuses to restore a row with an older epoch (`ruk/crates/ruk-core/src/protocol.rs` ~911-915, 1212; `undo.rs` ~18-27) [V]. The CI fence reuses this model and error name. I did not locate a separate `leases` module [U].
- **At-least-once and idempotent commit.**
  - REAPI: the server "does not need to guarantee … at most one execution" ([remote_execution.proto](https://raw.githubusercontent.com/bazelbuild/remote-apis/main/build/bazel/remote/execution/v2/remote_execution.proto)) [V].
  - Temporal guarantees a task "either runs or timeouts" and detects loss via timeouts, with retries under policy ([docs](https://docs.temporal.io/activity-execution.md)) [V].
  - Ruk's coordinator is at-least-once with stable request ids and 7-day receipts (`ruk/docs/coordinator.md`, README) [V].
- **Outbox.** Write the message in the same transaction as the state change. A relay may publish twice, so consumers must be idempotent ([microservices.io](https://microservices.io/patterns/data/transactional-outbox.html)) [V].
- **SQLite.**
  - WAL gives one writer at a time and atomic commits ([wal](https://sqlite.org/wal.html)) [V].
  - `synchronous=FULL` is durable in WAL mode; `NORMAL` can lose a committed transaction on power loss ([pragma](https://sqlite.org/pragma.html)) [V].
  - `BEGIN IMMEDIATE` can return `SQLITE_BUSY` ([transactions](https://sqlite.org/lang_transaction.html)) [V].
- **Why SQLite and not Ruk's redb [I].**
  - Ruk's redb store is repo-scoped and owned by the Rust daemon, and a Go forge cannot open it.
  - CI scheduling is forge-level and relational: tenants, queues, runners, tokens.
  - Only the *verdicts* go into Ruk, as attestations.
- **Replay-based durable workflows are unnecessary.** DAGs are static, so the DB row is the log [I].

## 3. Efficiency

- **Action key** ([REAPI](https://raw.githubusercontent.com/bazelbuild/remote-apis/main/build/bazel/remote/execution/v2/remote_execution.proto) `Action`) [V]:
  - Fields: command digest, input-root digest, timeout, `do_not_cache`, `salt`, platform.
  - Timeout is part of the key.
  - `salt` separates a cache namespace so a poisoned set can be disowned.
  - Clients "must take care to understand the caching behaviour"; caching is ideal only for reproducible actions.
- **Nix** derivations list all inputs explicitly, plus `system`, builder, args and env ([manual](https://releases.nixos.org/nix/nix-2.35.1/manual/store/derivation/index.html)) [V].
- **Turborepo** hashes task definition, lockfile, input files, declared env values and passthrough args. It "assumes that your tasks are deterministic" ([caching](https://turborepo.dev/docs/crafting-your-repository/caching)) [V].
- **Nx** hashes sources, dependencies, runtime values and CLI args ([docs](https://nx.dev/docs/concepts/how-caching-works.md)) [V].
- **Bazel**: a remote cache is safe only if the build is reproducible, and "take care in who has the ability to write" ([docs](https://bazel.build/remote/caching)) [V].
- **GHA cache scoping:**
  - Restores come from the current, default and PR-base branches, never from children or siblings.
  - Anyone who can open a PR can read the base-branch cache.
  - Entries unused for 7 days are evicted; the default limit is 10 GB per repo.
  - The page says to "save caches from trusted triggers" ([dependency caching](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching.md)) [V].
- **Fair scheduling** ([Kubernetes APF](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/)) [V]:
  - Priority levels have nominal shares and bounded lend/borrow.
  - Within a level, flows are fair-queued, and shuffle sharding insulates low-intensity flows.
  - It gives no preemption semantics for running work. Preemption of running jobs is unsourced [U].
  - DRF (Ghodsi et al., NSDI'11) and DRR (Shreedhar & Varghese, SIGCOMM'95) were identified by search only [U].
- **Supersede and cancel:**
  - GHA concurrency: a new pending run replaces an older pending one; `cancel-in-progress` also kills a running one ([docs](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)) [V].
  - GitLab `interruptible` and `auto_cancel` [V].
  - Buildkite cancel signal then SIGKILL [V].
- **Log streaming:** chunked, ack-based. Evidence: Gitea `AckIndex`, GitLab 128 KB flush, Buildkite `Sequence`/`Offset` [V].

## 4. Sandboxing

| Option | Isolation | Dependencies for a static Go runner | Source |
|---|---|---|---|
| **Rootless podman** | Namespaces + cgroups v2 limits + seccomp | External `podman`, subuid/subgid, a network helper. Resource limits need cgroup v2. Native overlay needs kernel ≥5.12. Containers get 65536 UIDs | [rootless.md](https://raw.githubusercontent.com/containers/podman/main/rootless.md) [V] |
| **bubblewrap** | Mount, user, pid, net, ipc and uts namespaces plus seccomp. **Protection is "entirely determined by the arguments"; not a security boundary by default** | One small binary; no image store, no cgroups | [README](https://raw.githubusercontent.com/containers/bubblewrap/main/README.md) [V] |
| **gVisor `runsc`** | User-space kernel. `systrap` is the default platform; no CPU overhead; syscall, VFS and network overhead | `runsc` plus an OCI bundle | [perf](https://gvisor.dev/docs/architecture_guide/performance/), [platforms](https://gvisor.dev/docs/user_guide/platforms/), [OCI](https://raw.githubusercontent.com/google/gvisor/master/g3doc/user_guide/quick_start/oci.md) [V] |
| **Firecracker** | KVM microVM + jailer + seccomp. The operator must filter guest egress | `/dev/kvm`, jailer, kernel, rootfs, TAP setup, guest agent. ≤125 ms from `InstanceStart` to init in the spec | [design](https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/design.md), [SPECIFICATION](https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/SPECIFICATION.md), [prod-host-setup](https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/prod-host-setup.md) [V] |

- **Firecracker snapshot-restore figure.** `prod-host-setup.md` mentions snapshot restore going from 3 ms to 8.5 ms on aarch64 because of host console logging. It is an incident note, not a latency guarantee.
- **Host fact checked:** on this machine `/proc/sys/kernel/apparmor_restrict_unprivileged_userns` is `1` [V, read]. Ubuntu confines unprivileged user namespaces with AppArmor (23.10+/24.04) ([Ubuntu discourse](https://discourse.ubuntu.com/t/understanding-apparmor-user-namespace-restriction/58007)) [V]. `bwrap` fails to set `uid_map` without a profile (Launchpad #2144531, search result) [V].
- **I could not probe `podman` or `bwrap` here** (no execution tool). Whether a podman AppArmor profile ships with the distro package is [U].

**Decision [I], one primary path with ordered fallbacks:**
1. **v1: rootless podman** behind a small `Sandbox` interface:
   `podman run --rm --pull=never <img>@sha256:… --network=none --read-only --cap-drop=ALL --security-opt no-new-privileges --pids-limit N --memory M --cpus C --label rukie.attempt=ID`
2. The runner's **first startup step is a self-test** for userns/podman. If it fails, the runner refuses jobs and prints the AppArmor remedy. Shipping a profile in the install docs is part of v1.
3. **bubblewrap fallback** only if the probe passes. Trade-off: lowest dependencies, weakest isolation, no cgroups, no image support. It is limited to trusted-scope, pure, tool-only jobs.
4. **gVisor** is an optional hardened label (`--runtime=runsc`), enabled after a host probe.
5. **Firecracker** is deferred.
6. A raw-syscall Go sandbox is **not recommended**: it would mean writing a container runtime.
7. Also apply Landlock to the runner process itself ([go-landlock](https://pkg.go.dev/github.com/landlock-lsm/go-landlock/landlock)) [V]; the kernel doc says TCP bind/connect restrictions exist from ABI 4 [V via search snippet].

## 5. Job format

**Coexistence with what rukie ships [V]:** rukie already parses Gitea-Actions YAML. `rukie/modules/actions/jobparser` provides `ReadWorkflow`, `ParseRawSingleWorkflow` and a YAML-alias resolver, and `modules/actions/workflows.go:141` provides `ValidateWorkflowContent`.

**Decision [I]:**
1. The digest basis is a **canonical job-spec JSON** (RFC 8785 JCS, [RFC](https://www.rfc-editor.org/rfc/rfc8785)). The format choice then only affects front-ends.
2. **v1 front-end A (reuse, no new parser):** run the existing `jobparser` over `.gitea/workflows/*.yaml`, then lower each expanded job to canonical JSON.
   - Support `run:` steps only.
   - Reject `uses:` and any dynamic feature that can't be lowered deterministically, with a clear error.
   - Existing workflows run unmodified where they fit.
3. **v1 front-end B (native):** `.rukie/ci.toml` with a published JSON Schema and no expression language, lowered to the same JSON.
4. Starlark generator and CUE are **deferred**.

Evidence for the format comparison, one source per claim:
- **TOML:** "designed to map unambiguously to a hash table"; duplicate keys are invalid ([toml.io v1.0.0](https://toml.io/en/v1.0.0)) [V].
- **YAML:** 1.2 changed implicit typing relative to 1.1, so parsers can differ ([yaml.org 1.2 spec intro](https://yaml.org/spec/1.2.2/)) [V]. Community "Norway problem" reports exist but I did not fetch a primary one [U].
- **Starlark:** "deterministic and hermetic", finite, no recursion ([spec](https://raw.githubusercontent.com/bazelbuild/starlark/master/spec.md)) [V].
- **CUE:** order-independent lattice; schema, data and policy in one language ([cuelang.org](https://cuelang.org/docs/concept/the-logic-of-cue/)) [V].
- The GHA workflow JSON schema was not fetched [U].

## 6. Attestation, bridged to Ruk

- **in-toto Statement v1:** `_type`, `subject[]` with `digest`, `predicateType`, `predicate` ([statement.md](https://raw.githubusercontent.com/in-toto/attestation/main/spec/v1/statement.md)) [V].
- **SLSA provenance** (`https://slsa.dev/provenance/v1`): `buildDefinition{buildType, externalParameters, internalParameters, resolvedDependencies}` and `runDetails{builder.id, metadata, byproducts}`. I read the v1.1 page, whose banner says **v1.2 is current** ([v1.1](https://slsa.dev/spec/v1.1/provenance), [v1.2](https://slsa.dev/spec/v1.2/provenance); v1.2 body not read [U]).
- **Test-result predicate** `https://in-toto.io/attestation/test-result/v0.1`: `result` is PASSED/WARNED/FAILED, with `configuration`, `url`, and `passedTests`/`failedTests` ([spec](https://raw.githubusercontent.com/in-toto/attestation/main/spec/predicates/test-result.md)) [V].
- **Ruk today:**
  - Struct: `AttestStatement{subject: Oid, kind, result, signed_at, supersedes}` (`ruk/crates/ruk-core/src/protocol.rs:1775-1785`) [V].
  - `kind` and `result` are non-empty UTF-8 text with no control characters, up to the control-frame limit. The doc comments give "review"/"ci" and "approved"/"pass" as examples [V].
  - Signature: SSHSIG, ed25519, namespace `ruk-attest`, over a canonical 7-field payload [V].
  - Subject: must be a commit object [V].
  - Gating: requirement matching compares result text exactly (reported at `attestation/query.rs:266` and `protocol.rs:2037`; I did not re-read those lines myself [U]).
  - Delivery: mutations through the hosted daemon are idempotent via request-id receipts [V].

**Mapping [I]:**

| in-toto / CI | Ruk |
|---|---|
| Statement `subject[].digest.gitCommit` | `AttestStatement.subject` (commit Oid) |
| Overall verdict (PASSED/FAILED) | `result` = `"pass"` or `"fail"`. **A plain verdict only**, so exact-match land gates still work |
| Job identity | `kind` = `"ci/<job-name>"` |
| Re-run or cache re-attest | `supersedes` = prior statement digest |
| Full SLSA/test-result Statement | Content-addressed blob in rukie's CAS, linked from the attestation |

- **Link to the blob.** `AttestStatement` has no free field. Add an optional `evidence: Digest` to a **canonical payload v2** (breaking changes are allowed). `result` stays a verdict and is not overloaded.
- **Which signature wins.** The **SSHSIG attestation is the authoritative gate signature**; land requirements trust only Ruk-verified signers. The in-toto Statement may additionally be wrapped in a DSSE envelope signed with the same ed25519 key, as an exported artifact for external SLSA verifiers. That DSSE envelope is not read by Ruk.
- **Signing.** The runner never holds the key. The server signs after the commit transaction, through the outbox, using the outbox id as the Ruk request id.
- **Cache hits.** A hit writes a new attestation with `supersedes`, and its Statement records the original attempt and the action key's input set.

## 7. Architecture

### 7.1 v1 core (first testable milestone)

1. **Tables:** `run`, `job`, `attempt`, `fence_seq`, `runner`, `blob`, `result_cache`, `outbox`, `tenant_vtime`.
2. **Lease RPCs:**
   - `lease` (long-poll ≤25 s, jittered backoff) grants a job in a `BEGIN IMMEDIATE` transaction: `fence = ++fence_seq`, `lease_expires = now + 30 s`.
   - `heartbeat{fence}` is tiny, runs on its own connection with its own timeout, and its reply carries `cancel`, `deadline` and `ack_offset`.
   - A lease that is not `started` within 60 s returns to the queue without consuming an attempt.
3. **Two-phase expiry.**
   - After `lease_expires` the attempt becomes `suspect`. A heartbeat with the same fence before `grace_until` (default 2 min) revives it.
   - After `grace_until` the fence is bumped, the attempt is marked `lost`, and the job is requeued per retry policy.
   - The `grace_until` idea mirrors GHA's `LockedUntil + 5 min` retry window [V].
4. **Boot rule.** At startup every active attempt gets `lease_expires = now + TTL` and `grace_until = now + grace`.
5. **Fence check on every attempt-scoped write** (log append, link artifact, finish, job-token use). A stale fence returns `E_LEASE_LOST`.
6. **Finish.** `finish{attempt, fence, result_digest}` is idempotent. The same digest replays as 200; a different one is 409. One transaction does all of: terminal attempt and job; run roll-up; cache insert (rules below); downstream enqueue; outbox rows for Ruk attestation, commit status, signed webhook and notification. The relay delivers at-least-once and consumers dedupe by outbox id.
7. **Runner behaviour:**
   - 5xx and refused connections are retryable, and the job keeps running.
   - It self-fences by monotonic time since the last ack: SIGTERM, grace, SIGKILL. This only saves resources; safety is the server fence.
   - On startup it kills orphan sandboxes (`podman rm -f --filter label=rukie.attempt`) and posts `abandon{attempt, fence}` for immediate requeue.
8. **Logs.** The runner appends to a local on-disk spool in chunks ≤64 KiB or ≤1 s. Each chunk carries `(attempt, seq, offset)`. The server appends idempotently and returns a contiguous `ack_offset`. The runner replays from the ack after an outage and deletes the spool only after the finalize ack. The server seals the log as a zstd blob. The cap is 64 MiB, keeping the tail. Secrets are redacted on the runner.
9. **Retry classes.** Classes are `runner_lost`, `infra`, `timeout` and `script_failure`. The default auto-retry is `runner_lost` and `infra`, max 2. Jobs with `effects = "exclusive"` never auto-retry on `runner_lost`. They receive `RUKIE_FENCE` for external systems to check.
10. **Queue limits.** `queue_deadline` defaults to 6 h. Enqueue is rejected when no registered runner matches the labels (cf. GitLab `stuck_pending_no_matching_runners`).
11. **Scheduling v1.** Priority classes `interactive > agent > bulk`, each with a nominal share. Inside a class, one fair queue across `tenant = (org, principal)` using deficit round robin over estimated runner-seconds. The heap is rebuilt from SQLite at boot.
12. **Supersede.** On a new run in the same `(repo, workflow, ref)` group, queued jobs are cancelled in the same transaction. Running `interruptible` jobs are cancelled via the heartbeat reply (SIGTERM, 10 s, SIGKILL). `effects = "exclusive"` jobs are never cancelled.
13. **Auth.** A runner token is scoped and stored as a hash. A per-attempt job token is valid only while its fence is current, with default scope: read the tree, write its own logs and artifacts, no push. A fence bump revokes it.

### 7.2 Cache rules (v1, restrictive)

- **Eligibility.** A job is cached or skipped only if it declares `pure = true`, which requires:
  - `network = "none"`;
  - no secrets;
  - no `effects` other than `none`;
  - `cache = "pass"` (the default for pure jobs).
- **Never skippable:** deploys, publishes, anything with `effects = "idempotent"` or `"exclusive"`, anything using the network or secrets. These always run. Equivalent to REAPI `do_not_cache` [V].
- **Digest.** `action_digest = SHA-256(JCS({schema, salt{epoch, repo}, tree, job_spec_digest, image, platform, env, deps, runner_abi, trust}))`:
  - `tree` is the Git tree OID. Ruk exposes exact Git object interop [V from the README], so the tree OID is available [I].
  - `image` is the platform manifest digest, resolved at enqueue.
  - `env` holds declared names and values only.
  - `deps` are digests of consumed upstream outputs.
  - Bumping the salt epoch invalidates everything.
- **Cache writes** happen only in the transaction of the **winning attempt's `finish`** (valid fence), from a **trusted-scope** run, only for `passed` results, and never for `cancelled`, `lost` or `infra`.
- **Trust scoping.** Untrusted-scope runs (agent branches, forks) may read results written by trusted runs. They **never write** pass entries that trusted runs can read. This follows GHA's scoping and Bazel's "who can write" warning [V].
- **Fenced-out results are discarded.** No adoption; see open questions.
- **On a hit.** The job becomes `cached`, logs replay from the stored blob, and a new attestation is written with `supersedes` and the key's input set recorded.
- **Single-flight** for identical in-flight digests. Followers wait; if the leader fails, a follower is promoted.

### 7.3 Deferred (not in v1)

- Shadow re-execution of cache hits.
- Layered content-addressed step and directory caches. Plain blob CAS is enough for logs and artifacts in v1.
- `explain-miss` CLI. v1 just stores the action-key JSON per job.
- Starlark generator and CUE.
- Firecracker, gVisor and bubblewrap tiers beyond the interface.
- Preemption of running jobs.
- DRF multi-resource weights and shuffle sharding.
- Trust-aware cache sharing across repos.

## 8. Open questions

1. Probe `podman` and `bwrap` on this host (AppArmor `userns` profile, rootless podman cold start). I could not run anything. Result decides the primary sandbox.
2. How does the runner obtain the tree: Ruk `upload-archive` over the pkt-line wire (present in `ruk-git`) or a new tree-export RPC? `ruk git archive` is listed unsupported [V]; the `upload-archive` wire path needs checking [U].
3. Add an `evidence: Digest` field (canonical payload v2) to Ruk `AttestStatement`: breaking change, confirm acceptable.
4. Does adopting a fenced-out result into the cache ever make sense? **Low confidence; rejected for v1.** Any such design needs the same trust and fence checks as a normal write.
5. Embedded DB: confirm SQLite throughput under heartbeat load (batch heartbeats into one `UPDATE` per tick [I]) and the Go driver choice (pure-Go vs CGO) [U].
6. Woodpecker restart recovery of in-flight tasks: not traced [U].
7. DRF and DRR claims rest on search abstracts [U]. Preemption semantics need a primary source (Nomad or Kubernetes scheduler code) [U].
8. Duplicate execution in GitHub, GitLab, Buildkite, Woodpecker and Forgejo: undocumented in the sources I read; marked unknown.
9. `uses:` support in the YAML adapter: which actions can be pinned and made hermetic?

### Critical Files for Implementation
- `rukie/modules/actions/jobparser/jobparser.go` — existing GHA-style parser to reuse as the YAML front-end.
- `rukie/modules/actions/workflows.go` — `ValidateWorkflowContent`; static validation entry point.
- `rukie/services/actions/clear_tasks.go` — current `Updated`-only zombie reaper to replace with fenced two-phase expiry.
- `rukie/routers/api/actions/runner/runner.go` — existing Connect RPC (FetchTask/UpdateTask/UpdateLog with ack index) as the protocol reference.
- `ruk/crates/ruk-core/src/protocol.rs` — `AttestStatement`, canonical payload, and the lease-epoch fencing the CI fence should mirror.

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Correct the gVisor row; the perfo…

---
## Later corrections from the same agent (apply over the body above)

These four findings are valid. The artifact still could not be written (no write tool), so below are exact replacements for the four affected spots. Apply them to the document from my last message.

**1. §4 gVisor row, replace the isolation cell with:**
> User-space kernel (Sentry/Gofer). `systrap` is the default platform; `ptrace` is no longer supported; KVM is best only on bare metal. The performance guide says gVisor "imposes runtime costs over native containers" in cycles and memory. Raw CPU instructions run natively with no emulation cost, but syscalls, VFS and networking cost more. [V: https://gvisor.dev/docs/architecture_guide/performance/, https://gvisor.dev/docs/user_guide/platforms/]

The statement "No CPU overhead" is removed. It applied only to raw instruction execution, not to the whole workload.

**2. §7.2 action digest.**
- **Remove** `trust:<class>` from the digest fields. The digest is now over `{schema, salt{epoch, repo}, tree, job_spec_digest, image, platform, env, deps, runner_abi}`.
- **Store** `producer_trust` as a column on `result_cache`. The key is `(action_digest)` with `producer_trust` as metadata.
- **Lookup rule:** a requester at trust level T may use an entry only if `producer_trust` is T or more trusted. A trusted requester reads only trusted entries. An untrusted requester reads trusted entries and its own scope's entries.
- This is one lookup by digest plus a filter. No second lookup path is needed. [I]

**3. §6 signing and §7.1 item 6: no exactly-once claim.**
- Ruk receipts last 7 days (`ruk/README.md`, line 5) [V]. `ruk/docs/coordinator.md` says "exactly-once delivery does not exist" [V]. The outbox relay is at-least-once.
- Replace the sentence with:
  > The relay passes the outbox id as the Ruk request id. Beyond the 7-day receipt window, duplicates are prevented by determinism: `signed_at` is fixed in the outbox row when the result commits (the attempt's finish time), never taken at relay time. A replay then produces a byte-identical statement. Ruk keys attestation rows by `(subject, kind, signer, digest)`, so the replay hits the same row [I, from `ruk-core/src/attestation.rs` module docs seen in the first grep image]. The relay also marks rows dead after 7 days and alerts instead of retrying.

**4. §4 Landlock: remove "Apply Landlock to the runner process itself" from v1.**
- Landlock rulesets are inherited across `fork`/`exec`. A confined runner would confine its `podman` child too [U: kernel doc page returned no body here; this is standard Landlock behaviour].
- That could block podman's storage, runtime dir, `/etc/subuid` and `newuidmap` access on every launch [I].
- Move Landlock to **Deferred**, with this note:
  > Landlock may be applied only to a separate control-plane worker that never launches podman, or to the podman child with a ruleset that explicitly grants its storage and runtime paths. Test with the podman self-test before enabling.

No other part of the previous document changes.

[advisor] <advisory advisor="glm-flash" severity="nit" guidance="weigh, don't blindly obey"> Nothing new to flag. </advisory>

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Drop "its own scope's entries" fr…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Relabel unseen [V] figures as [U]…