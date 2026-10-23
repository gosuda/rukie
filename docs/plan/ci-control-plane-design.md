# ci-control-plane-design.md: rukie built-in CI control plane (server side)

**Status.** This is the complete design. It supersedes all earlier drafts and deltas. An implementer or verifier needs no other document.

**Scope.** Job definition, triggers, durable state machines, queue and scheduling, runner protocol, result cache, logs, artifacts, secrets, attestation output, crash recovery, and the operations surface. The runner sandbox internals are in `research-ci-runner.md` §4 and are not repeated.

**Evidence tags.** **[src]** read in this session (path:line). **[R]** taken from `research-ci-runner.md`; its external sources were not re-fetched. **[I]** design choice or inference. **[U]** unverified; check before implementation.

---

## 0. Decisions

Settled inputs honored: one Go op table; Ruk is the only VCS backend; Ruk attestations are the verdict record; the metadata store sits behind one module.

| ID | Decision | Reason |
|---|---|---|
| D1 | The job file is `.rukie/ci.toml` (TOML 1.0), validated by JSON Schema 2020-12. No GitHub-Actions YAML and no `jobparser` in v1. This overrides research §5, which suggested reusing `jobparser`. | `jobparser` is a Gitea module. GHA expressions and `uses:` make jobs non-hermetic. A one-way importer can live outside the server later. [I] |
| D2 | Untrusted runs (change heads, land candidates, unprotected pushes, forks) take job definitions from the **base commit**. The change's own file is data, not configuration. `spec_from=tree` is an explicit override and is never land-eligible. | A change cannot relax its own required checks. [I] |
| D3 | Attestation kind is `ci/<job>/<job_spec_digest>`. | An old verdict cannot satisfy a new spec. Requires Ruk R2. [I] |
| D4 | Only trusted runs (protected refs) write the cache and receive secrets. Cache v1 stores trusted entries only. | Untrusted code never gets secrets and never writes shared state. [I] |
| D6 | Verdicts are recorded only for final `passed` and `failed`, and for cache hits (which are `passed`). `errored`, `timed_out`, `cancelled`, `skipped`, and `policy` write none. | Infrastructure noise must not supersede a real verdict. A missing verdict blocks landing. [I] |
| D7 | No exactly-once claim. Accepted results commit once. Execution can repeat after a runner is lost. | Ruk: "exactly-once delivery does not exist" [src docs/coordinator.md:47-48]. |
| D8 | Required land checks are `[land] required = [...]` in the base commit's file. Required jobs must have `network = "none"`, `effects = "none"`, and no secrets. | Landing must not depend on external state or on secrets from unreviewed code. Dependencies must be vendored until a module proxy exists (deferred). [I] |
| D9 | Scheduling is hierarchical start-time fair queueing: class, then repo, then principal. Not DRR. | One durable number per entity, cost-aware, no quantum tuning. The DRR and DRF citations were search-only [R §3]. [I] |

**D5 is withdrawn.** Durable heartbeats are a target requirement, not a decision. The normative statement is §4.0, and the endpoint contract is §6.3.

---

## 1. Ruk facts this design depends on

| Fact | Source | Consequence |
|---|---|---|
| `attest.add` args are `{statement, signature}`. The signature is SSHSIG over `canonical_payload_v1`, namespace `ruk-attest`. Private keys never reach the coordinator. | protocol.rs:1787-1790; model `attest.add` | rukie signs. The relay sends signed statements. |
| `AttestStatement` = `subject` (commit Oid), `kind`, `result`, `signed_at`, `supersedes` | protocol.rs:1775-1785 | verdict-only statement; no evidence field (R1) |
| Canonical payload v1 = 7 fields, each prefixed by a 4-byte big-endian length: `"ruk-attest-payload-v1"`, format name (`sha1`/`sha256`), raw subject bytes, kind, result, RFC 3339 timestamp text, superseded digest (32 raw bytes or empty) | protocol.rs:95, 3638-3665; fixtures ~4600-4610 | Go encoder must match byte-for-byte. Ruk fixtures are golden vectors. |
| `StatementDigest` = SHA-256 of canonical payload, 64 lowercase hex | ruk-model.json `$defs.StatementDigest` | chain links computed in rukie before any Ruk call |
| `Timestamp` is RFC 3339 UTC and must end in `Z` | protocol.rs:170-200 | whole-second `...Z` timestamps |
| Subject must be a stored commit, else `E_INVALID`. A supersession target must be in the same (subject, kind, signer) group, else `E_NOT_FOUND`. An identical statement from the same signer dedupes; the first signature is kept. | attestation/prepare.rs module doc and `prepare_batch` errors | per-chain relay in order; replays are no-ops |
| `RequestId` is a 16-byte UUID. Receipts are keyed by (session, request_id) and retained 7 days. | change.rs:436-438, 643-650; protocol.rs:992-1001; ruk/README.md:5 | outbox row id is a UUIDv4 fixed at write time |
| Verification: plain `ssh-ed25519`, namespace `ruk-attest`, sha256 signing hash; signature ≤ 8192 bytes | attestation/verify.rs module doc; `AttestSignature` in model | rukie emits matching SSHSIG |
| The `attest` capability is an exact-match list. `require_attest` uses `contains(kind)`. No pattern grant. | capability.rs:657-658, 730-739, 851-860 | R2 needed for `ci/<job>/<digest>` kinds |
| Land requirements and trusted signers are frozen empty with `policy_generation = 0`. The source says a `[land] requirements` policy section "is follow-up work." | coord/prepare.rs:3430-3434, 3482-3486 | R3: CI cannot gate landing today |
| Land plans expire after 1 h (`LAND_PLAN_TTL`). `land.execute` pushes stored candidate bytes. | coord/land.rs (`LAND_PLAN_TTL`); protocol.rs:2043-2060 | land CI must finish inside the plan TTL |
| The hosted daemon serves only the `op.log`, `claim.list`, and `attest.list` reads. Every other read returns "missing implementation." | daemon/mod.rs:611-615, 623-624; coord/hosted.rs:522-526 | R4: change heads and code reads unavailable |
| Hosted mutation coverage does not name `land.execute`, `push`, or `fetch` | daemon/mod.rs:626-627 | R5 |
| Mutation replies carry `receipt{replayed, retained_until}`, `operation`, and `heads` | protocol.rs:992-1002, 1050-1063 | rukie learns head moves from its own mutations |
| `op.log` entries are `{id, parent, session, method, committed_at, undoes}`. `oldest_retained` marks pruning. `op.prune` applies retention. | protocol.rs:2111-2153 | trigger feed; cursor must stay ≥ `oldest_retained` |

[Showing lines 603-1186 of 2084 (50.0KB limit). Use :1187 to continue]
| `op.log` entries are `{id, parent, session, method, committed_at, undoes}`. `oldest_retained` marks pruning. `op.prune` applies retention. | protocol.rs:2111-2153 | trigger feed; cursor must stay ≥ `oldest_retained` |
| Paging: default 100, max 1000 | ruk-model.json `limits` | same paging defaults |

---

## 2. Job definition file

### 2.1 Location and format
- Path `.rukie/ci.toml` at the repository root. A missing file means no jobs. `.gitea/workflows/` is never read.
- TOML 1.0, decoded strictly: unknown keys rejected, no datetimes, no floats. The decoded value is converted to the JSON data model, then validated.
- Third-party dependencies: one TOML decoder (candidate `github.com/BurntSushi/toml`) and one JSON Schema validator (candidate `github.com/santhosh-tekuri/jsonschema/v6`). The stdlib has neither. [I] The rest of CI is stdlib.

### 2.2 Example
```toml
schema = "rukie.ci/v1"

[triggers]
change = true
push = ["main", "release/*"]
land = ["main"]
schedule = [{ cron = "0 3 * * *", branch = "main" }]

[land]
required = ["lint", "test"]

[[job]]
name = "lint"
runs_on = ["linux-amd64"]
image = "docker.io/library/golang@sha256:<64 hex>"
pure = true
steps = [{ name = "vet", run = "go vet ./..." }]

[[job]]
name = "test"
runs_on = ["linux-amd64"]
image = "docker.io/library/golang@sha256:<64 hex>"
needs = ["lint"]
timeout = "30m"
pure = true
retry = { max = 1, on = ["infra", "lost"] }
env = { CGO_ENABLED = "0" }
artifacts = ["out/coverage.out"]
steps = [
  { name = "build", run = "go build ./..." },
  { name = "test",  run = "go test ./... -coverprofile=out/coverage.out" },
]
```

### 2.3 JSON Schema (ships in the binary)
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "rukie://schemas/ci-job-file/v1",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema", "job"],
  "properties": {
    "schema": {"const": "rukie.ci/v1"},
    "triggers": {"$ref": "#/$defs/triggers"},
    "land": {"$ref": "#/$defs/land"},
    "job": {"type": "array", "minItems": 1, "maxItems": 256, "items": {"$ref": "#/$defs/job"}}
  },
  "$defs": {
    "name": {"type": "string", "pattern": "^[a-z][a-z0-9-]{0,62}$"},
    "triggers": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "change": {"type": "boolean"},
        "push": {"type": "array", "maxItems": 16, "uniqueItems": true,
                 "items": {"type": "string", "minLength": 1, "maxLength": 128}},
        "land": {"type": "array", "maxItems": 16, "uniqueItems": true,
                 "items": {"type": "string", "minLength": 1, "maxLength": 128}},
        "schedule": {"type": "array", "maxItems": 8, "items": {
          "type": "object", "additionalProperties": false, "required": ["cron", "branch"],
          "properties": {"cron": {"type": "string", "maxLength": 64},
                         "branch": {"type": "string", "maxLength": 128}}}}
      }
    },
    "land": {
      "type": "object", "additionalProperties": false,
      "properties": {"required": {"type": "array", "maxItems": 64, "uniqueItems": true,
                                  "items": {"$ref": "#/$defs/name"}}}
    },
    "job": {
      "type": "object", "additionalProperties": false,
      "required": ["name", "runs_on", "image", "steps"],
      "properties": {
        "name": {"$ref": "#/$defs/name"},
        "runs_on": {"type": "array", "minItems": 1, "maxItems": 8, "uniqueItems": true,
                    "items": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,30}$"}},
        "image": {"type": "string", "pattern": "^[a-z0-9._/-]+(:[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$"},
        "needs": {"type": "array", "maxItems": 64, "uniqueItems": true, "items": {"$ref": "#/$defs/name"}},
        "timeout": {"type": "string", "pattern": "^[1-9][0-9]{0,2}(m|h)$"},
        "network": {"enum": ["none", "egress"]},
        "secrets": {"type": "array", "maxItems": 32, "uniqueItems": true,
                    "items": {"type": "string", "pattern": "^[A-Z][A-Z0-9_]{0,63}$"}},
        "effects": {"enum": ["none", "idempotent", "exclusive"]},
        "pure": {"type": "boolean"},
        "interruptible": {"type": "boolean"},
        "retry": {"type": "object", "additionalProperties": false, "required": ["max", "on"],
                  "properties": {
                    "max": {"type": "integer", "minimum": 0, "maximum": 3},
                    "on": {"type": "array", "uniqueItems": true,
                           "items": {"enum": ["infra", "lost", "timeout", "script_failure"]}}}},
        "env": {"type": "object", "maxProperties": 64,
                "propertyNames": {"pattern": "^[A-Z_][A-Z0-9_]{0,63}$"},
                "additionalProperties": {"type": "string", "maxLength": 4096}},
        "artifacts": {"type": "array", "maxItems": 16,
                      "items": {"type": "string", "minLength": 1, "maxLength": 512,
                                "pattern": "^(?!/)(?!.*(^|/)\\.\\.(/|$))[^\\u0000]+$"}},
        "steps": {"type": "array", "minItems": 1, "maxItems": 64,
                  "items": {"type": "object", "additionalProperties": false, "required": ["name", "run"],
                            "properties": {"name": {"$ref": "#/$defs/name"},
                                           "run": {"type": "string", "minLength": 1, "maxLength": 16384}}}}
      },
      "if": {"required": ["pure"], "properties": {"pure": {"const": true}}},
      "then": {"properties": {"network": {"const": "none"}, "effects": {"const": "none"},
                              "secrets": {"maxItems": 0}}}
    }
  }
}
```

### 2.4 Semantic rules (checked after the schema)
- Job names are unique. Step names are unique per job. `needs` refer to existing jobs. The graph is acyclic. At most 256 jobs.
- Defaults: `pure=false`, `network="none"`, `effects="none"`, `timeout="30m"` (1 min to 24 h), `interruptible=true` (forced false for `exclusive`), `retry={max=1,on=["infra","lost"]}`, `env={}`, `secrets=[]`, `artifacts=[]`.
- `pure=true` requires `network="none"`, no secrets, `effects="none"`, and `needs` naming only pure jobs.
- `effects="exclusive"` rejects an explicit `interruptible=true` and rejects `lost` in `retry.on`.
- `runs_on` must contain exactly one platform label from a fixed table: `linux-amd64` → `linux/amd64`, `linux-arm64` → `linux/arm64`. The platform feeds the action key.
- `image` must be digest-pinned. The server resolves it to the platform manifest digest at ready time.
- Artifact paths are normalized and relative. A declared artifact is part of the job's pass.
- `[land].required` names must exist and satisfy D8.
- `network="egress"` in an untrusted run: the job becomes `errored` with class `policy` at ready time, with no verdict.
- Steps run as `/bin/sh -eu -c <run>`. Fixed shell; no selection.
- Errors carry a JSON pointer and the TOML line and column.

### 2.5 Canonical bytes and digests
- The effective spec is the validated file with defaults applied. Lists whose order is not significant (`needs`, `runs_on`, `secrets`, `retry.on`, `artifacts`) are sorted and deduplicated. `steps` order is significant.
- Canonical bytes are a restricted RFC 8785: UTF-8; object keys sorted by UTF-16 code unit; no insignificant whitespace; integers only; strings escaped as ECMAScript `JSON.stringify` does (escape `"`, `\`, and control characters only). Go's `encoding/json` is not compliant: it escapes `<`, `>`, `&`, U+2028, and U+2029. Use a ~80-line encoder with golden tests. [I]
- `job_spec_digest = "sha256:" + hex(SHA-256(canonical(effective job object)))`. The job object includes `name`.

### 2.6 Exposure
- `rukie ci schema` prints the schema. `rukie ci validate [--ref R] [--json]` validates.
- MCP tool `ci_validate` and resource `rukie://schemas/ci-job-file/v1`.
- `GET /_/schemas/ci-job-file.json` and `POST /{owner}/{repo}/ci/spec/validate` (§11).

---

## 3. Triggers, trust, and run creation

### 3.1 Sources and keys

| Trigger | Source | Trigger key (unique per repo) | Group (supersede key) | Class | Trust | Spec from |
|---|---|---|---|---|---|---|
| `change` | push to `refs/for/<b>` (edge, Ruk receive path); `op.log` entries that move a change head (edge, needs R4); reconciler (level) | `change:<change_id>:<head_commit>` | `change:<change_id>` | `interactive` | untrusted | base |
| `land` | `land.prepare` candidate | `land:<plan_id>` | `land:<change_id>:<target>` | `land` | untrusted | base |
| `push` | ref update matching `triggers.push` | `push:<ref>:<new_tip>` | `push:<ref>` | `push` | trusted iff ref is protected | tree if trusted, else base |
| `schedule` | cron tick, at branch tip | `sched:<n>:<utc_minute>:<tip>` | `sched:<n>` | `bulk` | trusted iff branch is protected | as `push` |
| `manual` | `ci.run.create` | `manual:<principal>:<request_id>` | none | `interactive` | trusted iff protected ref and requester has maintain rights | base; tree only with `ci.spec.override`, and then untrusted |
| `retry` | `ci.run.retry` | `retry:<run>:<request_id>` | none | class of original run | as original | as original |

- **Protected refs** are a repo setting: globs, default `refs/heads/<default>`. Protected refs move only through `land.execute` or by maintainer push. [I]
- **Base commit** (the source of job definitions): change and land use the target tip at creation. Unprotected push, schedule, and manual use the default branch tip. Protected runs use their own commit.
- **Trust derives from ref protection at trigger time, never from the author.** A fork's change is untrusted even if its author owns the upstream repo.
- A conflicted change head (no exact commit) creates no run.
- An abandoned change (`abandon` in `op.log`) cancels group `change:<id>` with reason `change_abandoned`.

### 3.2 Run creation
1. **Before the transaction:** resolve the spec. Read the spec file at the spec commit from Ruk, validate it, compute digests, and store canonical bytes in `ci_spec` if new.
2. **One transaction:**
   - Insert the run unless `trigger_key` exists. A duplicate trigger returns the existing run.
   - If the spec is invalid, the run is created with `state=errored` and `spec_error` set, with no jobs.
   - Otherwise insert jobs: `queued` if no needs, else `waiting`.
   - Apply supersede (§5.4).
   - Advance the trigger cursor for edge sources.
   - Wake the scheduler.
3. Enabling CI seeds `ci_seen` with current tips, so enabling does not run history.

### 3.3 Level-triggered reconciler
Runs every 60 s and at boot. It compares observed branch tips (ref listing through the daemon's git-shaped command path [U]) and change heads (needs R4) with `ci_seen`. It inserts missing triggers using the same keys, so duplicates are no-ops. Edge events only reduce latency. The reconciler guarantees a run exists after a crash between a Ruk commit and the rukie transaction.

---

## 4. State machines

### 4.0 Durability requirement (normative)
Every accepted heartbeat durably writes `last_heartbeat_at`, `lease_expires_at`, and `lost_at` in the same transaction that produces its 200 response. The reply is sent only after that commit. No lease, heartbeat, or fence state is authoritative in memory. Memory holds only a rebuildable timer index. Boot recovery (§10.2 phase A) loads these fields and the fences, and grants nothing until that load commits.

Every transition below runs in one store transaction with its listed effects. Terminal states are absorbing.

### 4.1 Run
States: `pending`, `running`, `cancelling`, `succeeded`, `failed`, `errored`, `cancelled`.

| ID | From | Event [guard] | To | Effects |
|---|---|---|---|---|
| R1 | — | create [new trigger key; spec valid] | `pending` | jobs inserted |
| R2 | — | create [spec invalid] | `errored` | no jobs; `spec_error`; commit status error |
| R3 | `pending` | first attempt accepted (start) | `running` | `started_at` |
| R4 | `pending`, `running` | last job terminal [no cancel requested] | `succeeded` / `failed` / `errored` | aggregation (§4.5) |
| R5 | `pending`, `running` | cancel or supersede | `cancelling` | §5.4 effects; `cancel_reason` set |
| R6 | `cancelling` | last job terminal | `cancelled` | `finished_at` |
| R7 | — | `ci.run.retry` | new run with `retry_of` (R1 with carried jobs) | §4.2 J23 |

### 4.2 Job
States: `waiting`, `queued`, `leased`, `running`, `cancelling`, `passed`, `failed`, `errored`, `timed_out`, `cancelled`, `skipped`, `cached`, `carried`.
For dependents, "success" means `passed`, `cached`, or `carried`.

| ID | From | Event [guard] | To | Effects |
|---|---|---|---|---|
| J1 | — | run created [no needs] | `queued` | `ready_at=now`; `action_digest` computed if pure |
| J2 | — | run created [has needs] | `waiting` | — |
| J3 | `waiting` | all needs success | `queued` | `ready_at=now`; `action_digest` computed if pure |
| J4 | `waiting` | a need ended `failed`, `errored`, `timed_out`, or `skipped` | `skipped` | cascades to waiting dependents |
| J5 | `waiting` | a need ended `cancelled` | `cancelled` | cascades |
| J6 | `waiting`, `queued` | run cancelling | `cancelled` | no attempt |
| J7 | `queued` | pure, trusted cache hit on `action_digest` [checked at ready time (J3/J1) and at lease time] | `cached` | verdict `pass` with cache source; no attempt |
| J8 | `queued` | lease granted [§5.3] | `leased` | attempt `granted`; fence assigned in the grant transaction |
| J9 | `queued` | `queue_deadline_at` passed | `errored` | class `queue_timeout`; no verdict |
| J10 | `leased` | start accepted (A1) | `running` | — |
| J11 | `leased` | attempt `expired_unstarted`, or `released` with `not_started` or `runner_unsuitable` or `shutdown` | `queued` | `ready_at=now`; no attempt consumed |
| J12 | `running` | `complete` `passed` [declared artifacts present] | `passed` | verdict `pass`; cache insert if pure and trusted |
| J13 | `running` | `complete` `passed` [a declared artifact missing] | `failed` | class `script_failure`; verdict `fail` |
| J14 | `running` | `complete` `failed` | `failed` | verdict `fail` |
| J15 | `running` | attempt ended `lost` or `timed_out` or `infra`, class in `retry.on` [§4.6], attempts left | `queued` | `ready_at=now+backoff`; `avoid_runner_id` set; no verdict |
| J16 | `running` | attempt ended with a non-retryable class, or attempts exhausted; job class `infra`, `lost`, or `policy` | `errored` | no verdict |
| J17 | `running` | attempt `timed_out` [class `timeout`], not retried | `timed_out` | no verdict |
| J18 | `running` | attempt `lost`, effects `exclusive` | `errored` (class `lost`) | no auto-retry; manual retry requires `acknowledge_side_effects` |
| J19 | `running` | cancel requested [interruptible] | `cancelling` | attempt `cancel_requested_at` set |
| J20 | `running` | cancel requested [effects `exclusive`] | `running` | not signalled; run stays `cancelling` (no-op transition) |
| J21 | `leased` | cancel requested | `cancelled` | attempt released; nothing consumed |
| J22 | `cancelling` | attempt terminal with class `cancelled`, `lost`, or `timed_out`, or cancel give-up | `cancelled` | no verdict |
| J23 | — | created by `ci.run.retry` [prior verdict still chain head and `passed`] | `carried` | no attempt; no new verdict |
| J24 | `cancelling` | `complete` `passed` or `failed` (finished work) | `passed` or `failed` | verdict recorded (publication rule §9.1); cache insert if pure and passed and trusted |

The `single_flight` wait is an attribute of `queued` (`wait_reason`), not a transition.

### 4.3 Attempt
One attempt per lease. States: `granted`, `started`, `suspect`, `completed`, `released`, `expired_unstarted`, `lost`, `timed_out`, `cancelled`.

Every attempt-scoped write checks that the fence equals `attempt.fence` and `job.current_fence`, and that the attempt is non-terminal. The only exception is a replay (below). Heartbeat writes are conditional: `UPDATE ci_attempt SET … WHERE id=? AND fence=? AND state IN ('started','suspect')`. Zero rows updated means `E_LEASE_LOST`.

| ID | From | Event [guard] | To | Effects (same transaction) |
|---|---|---|---|---|
| A1 | `granted` | `start` [fence matches] | `started` | persist `last_heartbeat_at = now`, `lease_expires_at = now+30 s`, `lost_at = now+150 s`; `deadline_at = now + job timeout` |
| A2 | `granted` | now > `start_deadline_at` (`granted_at` + 60 s) | `expired_unstarted` | job J11 |
| A3 | `granted` | `release` [reason `not_started`, `runner_unsuitable`, `shutdown`, or `cancelled`] | `released` | job J11, or J21 for `cancelled` |
| A4 | `started` | heartbeat [fence matches] | `started` | persist `last_heartbeat_at`, `lease_expires_at`, `lost_at`; reply only after commit |
| A5 | `started` | now > persisted `lease_expires_at` (timer-driven) | `suspect` | persist |
| A6 | `suspect` | heartbeat [fence matches, now ≤ persisted `lost_at`] | `started` | persist revive and the three deadline columns |
| A7 | `suspect` | now > persisted `lost_at` (timer-driven) | `lost` | job J15, J16, or J18; `job.current_fence` cleared |
| A8 | `started`, `suspect` | `complete` [class `script_failure`, `infra`, `lost`, or `timeout`; fence and log checks pass] | `completed` | job transition; finish effects (§9.4) |
| A9 | `started` | now > `deadline_at` + 30 s and no complete | `timed_out` | job J17; cancel flag set for any later heartbeat |
| A10 | `started`, `suspect`, `granted` | cancel requested and no complete within 120 s | `cancelled` | job J22 |
| A11 | `started`, `suspect` | `complete` [class `cancelled`] | `cancelled` | job J22; or J24 if outcome `passed`/`failed` is reported instead |

**Replay.** A `complete` whose attempt is already `completed` returns the stored reply when its `result_digest` matches (§6.3). It is `E_RESULT_CONFLICT` when it differs.

### 4.4 Step (within an attempt)
States: `pending`, `running`, `passed`, `failed`, `skipped`, `cancelled`, `interrupted`. Steps are sequential. A failed step skips the rest. No conditional steps.

| ID | From | Event | To |
|---|---|---|---|
| S1 | `pending` | `step_started` event (next index, offset recorded) | `running` |
| S2 | `running` | `step_finished` with exit 0 | `passed` |
| S3 | `running` | `step_finished` with exit ≠ 0 | `failed` |
| S4 | `pending` | an earlier step failed, or the attempt ended | `skipped` |
| S5 | `running` | attempt cancelled before step end | `cancelled` |
| S6 | `running` | attempt `lost` or `timed_out` before step end | `interrupted` |

Step events arrive inside log appends (§6.3 endpoint 4). The server rejects an out-of-order index.

### 4.5 Invariants (property-test targets)
- **I1** At most one non-terminal attempt per job. A non-terminal attempt's fence equals `job.current_fence`.
- **I2** Terminal states are absorbing. A replayed `complete` with the same `result_digest` is idempotent.
- **I3** A run is terminal iff all its jobs are terminal and no attempt is non-terminal.
- **I4** A verdict exists iff the final job state is `passed`, `failed`, or `cached`. Verdicts are subject to the publication rule (§9.1).
- **I5** Per (subject, kind) there is one publication chain. Each published statement's `supersedes` equals the previous published digest.
- **I6** A cache entry exists iff a pure job passed under a trusted run.
- **I7** Untrusted runs never receive secrets and never write cache. A secret is never delivered to an untrusted-scope runner.
- **I8** Acked log offsets are monotonic. A sealed log digest is immutable.
- **I9** No queued job of a superseded run can be leased.
- **I10** Each attempt's persisted `last_heartbeat_at` is non-decreasing (except the boot rebase, which records the prior value in `pre_restart_heartbeat_at`). A heartbeat reply implies its timestamp is durable.
- **I11** No `lease`, `start`, `heartbeat`, `logs`, `blobs`, `complete`, or `release` is served before the boot recovery transaction (§10.2 phase A) commits. Those endpoints return `E_RECOVERING` until then.
- **I12** Fences are unique across all attempts and never reused. The fence counter is advanced in the grant transaction.

**Aggregation (R4):** if cancel was requested, `cancelled`. Else if any job is `errored` or `timed_out`, `errored`. Else if any is `failed`, `failed`. Else `succeeded`.

### 4.6 Retry policy

| Class | Set when | Auto-retry when | Default |
|---|---|---|---|
| `infra` | sandbox error, image pull or resolve failure, runner self-test failure | `infra` in `retry.on` and attempts left | retry (max 1) |
| `lost` | heartbeat grace expired (A7), or runner self-fence | `lost` in `retry.on`, effects ≠ `exclusive` | retry |
| `timeout` | server deadline (A9) | `timeout` in `retry.on` | no |
| `script_failure` | exit ≠ 0 | `script_failure` in `retry.on` (flake opt-in) | no |
| `policy` | untrusted egress, secret scope violation, required-job rule violation | never | — |
| `cancelled` | user or supersede | never | — |
| `queue_timeout` | scheduler deadline | never | — |

- **Backoff:** 30 s × 2^(n−1), capped at 10 min. A retry avoids the last runner when another matching runner exists.
- Retried attempts record no verdict. Only the final outcome is attested. The statement records `attempts_total`, so a flake-retry pass is visible.
- Manual retry (`ci.run.retry`) may override `retry.on`. It never re-runs an `exclusive` lost job without `acknowledge_side_effects=true`.

---

## 5. Durable queue and scheduling

### 5.1 Store objects (prefix `ci_`)
Transactions must be serializable with conditional writes. Required capabilities are in §14.

- `ci_run`: `id`, `repo_id`, `seq` (per-repo monotonic), `trigger_kind`, `trigger_key` (unique with `repo_id`), `group_key`, `subject`, `tree`, `base_commit`, `spec_from`, `trust`, `principal`, `class`, `state`, `superseded_by`, `cancel_reason`, `retry_of`, `spec_error`, `created_at`, `finished_at`.
- `ci_job`: `id`, `run_id`, `name` (unique with `run_id`), `spec_digest`, `state`, `wait_reason`, `class`, `repo_id`, `principal`, `queued_seq` (global monotonic), `ready_at`, `action_digest`, `attempts_used`, `max_attempts`, `avoid_runner_id`, `current_attempt_id`, `queue_deadline_at`. Index: (`state`, `class`, `repo_id`, `principal`, `queued_seq`).
- `ci_attempt`: `id`, `job_id`, `number`, `fence` (unique), `runner_id`, `state`, `granted_at`, `start_deadline_at`, `started_at`, `last_heartbeat_at`, `lease_expires_at`, `lost_at`, `deadline_at`, `cancel_requested_at`, `cancel_reason`, `completed_at`, `class`, `outcome`, `exit_code`, `result_digest`, `token_hash`, `log_last_seq`, `log_end_offset`, `sealed_log_digest`, `pre_restart_heartbeat_at` (audit; set only by boot rebase), `rebased_at` (set only by boot rebase).
  - Heartbeat columns (`last_heartbeat_at`, `lease_expires_at`, `lost_at`) are persisted per §4.0. Nothing about liveness is memory-only.
  - Indexes: (`state`, `lease_expires_at`) for the expiry reaper; (`state`, `lost_at`) for loss detection.
- `ci_fence`: singleton counter. Advanced in the same transaction as the grant that writes the attempt row.
- `ci_controller`: singleton `epoch`.
- `ci_spec`: `digest` PK, canonical bytes.
- `ci_step`: (`attempt_id`, `idx`), `state`, `start_offset`, `end_offset`, `exit_code`.
- `ci_log_chunk`: (`attempt_id`, `seq`) PK, `offset`, `len`, `sha256`, `bytes`. `ci_log_event`: (`attempt_id`, `seq`, `i`), `kind`, `index`, `offset`, `exit_code`.
- `ci_blob`: `digest` PK, `size`, `state` (`quarantine` | `linked`), `created_at`.
- `ci_artifact`: (`attempt_id`, `name`), `digest`, `size`.
- `ci_runner`: `id`, `labels`, `scope` (`trusted` | `untrusted`), `pubkey` (X25519), `capacity`, `sandbox_profile`, `token_hash`, `status`, `last_seen`.
- `ci_fair`: `scope_key` PK (`class:<c>`, `repo:<c>:<repo>`, `principal:<p>`), `vtime`.
- `ci_cache`: `action_digest` PK, `repo_id`, `producer_trust` (always `T1` in v1), `producer_attempt_id`, `artifact_manifest_digest`, `created_at`.
- `ci_seen`: (`repo_id`, `source`, `key`) PK, `value`. Reconciler state.
- `ci_chain`: `chain_key` PK (subject|kind), `head_digest`, `head_seq`, `head_trust`.
- `ci_outbox`: `id` (UUIDv4) PK, `seq` (global), `chain_key`, `kind` (`attest` | `status` | `webhook`), `payload`, `statement_digest`, `supersedes_digest`, `signed_at`, `state`, `attempts`, `next_at`, `last_error`.
- `ci_statement`: `statement_digest` PK → `blob_digest` (links verdicts to the in-toto blob until R1).
- `ci_idem`: (`principal`, `request_id`) PK, `op`, `args_digest`, `reply`, `created_at`. Retained 30 days.
- `ci_secret`: (`repo_id`, `name`, `version`) PK, `key_id`, `ciphertext`.

IDs are opaque and time-ordered [I].

### 5.2 Classes, weights, deadlines, caps

| Class | Triggers | Weight | Queue deadline |
|---|---|---|---|
| `land` | land | 8 | 30 min (inside the 1 h Ruk plan TTL) |
| `interactive` | change, manual, retry of interactive | 4 | 6 h |
| `push` | push | 2 | 6 h |
| `bulk` | schedule | 1 | 24 h |

Caps (overridable per repo): running per repo 8; per principal 16; per org 64; per runner, its `capacity`.

### 5.3 Lease selection (inside the writer transaction)

```
eligible(job, runner) :=
    job.state = queued ∧ job.ready_at ≤ now ∧ runs_on ⊆ runner.labels
  ∧ scope_ok(job, runner) ∧ runner.free > 0 ∧ caps_not_exceeded(job)

scope_ok:
    trust = untrusted            → runner.scope = untrusted
    trust = trusted ∧ secrets≠∅  → runner.scope = trusted
    otherwise                    → any runner

loop:
    c* = argmin vtime[class:c]     over classes with an eligible job
    r* = argmin vtime[repo:c*:r]   over repos with an eligible job in c*
    p* = argmin vtime[principal:p] over principals with an eligible job in (c*, r*)
    j  = eligible job in (c*, r*, p*) with smallest queued_seq
    if j is pure ∧ trusted cache hit on j.action_digest:  mark j cached (J7); continue loop
    if j is pure ∧ another trusted job with the same digest is leased or running:
          j.wait_reason = single_flight; exclude j; continue loop
    grant(j, runner):  fence = ++ci_fence; insert attempt granted; j.state = leased
    cost = predicted_seconds(repo, job name)
    vtime[class:c*] += cost / weight(c*)
    vtime[repo:c*:r*] += cost
    vtime[principal:p*] += cost
    return attempt
```

- **Idle reactivation:** when an entity goes from empty to non-empty, its vtime becomes `max(own, last served sibling vtime at its level)`. Idle entities cannot bank credit.
- **Predicted seconds:** EWMA over the last 10 completed attempts of (repo, job name). Default 300 s, clamped to [10 s, job timeout].
- **Long-poll:** a lease waits up to 20 s on a notify channel. Enqueue and completion signal it. A 1 s tick is the fallback.
- **Serialization:** one writer goroutine performs all mutations, with group commit (2–10 ms linger, up to 64 operations per transaction). This matches Ruk's publication lane [src ruk/README.md:5]. Every goroutine has a context and a shutdown path.

### 5.4 Supersede and cancel (same transaction as the new run, or the cancel)
For each active run in the same group with a lower `seq`, or the run being cancelled:
- `waiting` and `queued` jobs → `cancelled` (J5/J6). Dependents cascade.
- `leased` jobs → attempt released, job `cancelled` (J21).
- `running` interruptible jobs → attempt `cancel_requested_at` set; job `cancelling` (J19). The heartbeat reply delivers the cancel.
- `running` `exclusive` jobs → not signalled (J20). Their run is marked cancelled and ends when they finish.
- Run → `cancelling` if anything is active, else `cancelled`.

### 5.5 No matching runner
A job whose labels match no registered runner stays `queued` with `wait_reason=no_runner` until its queue deadline (J9). `ci.job.list` shows the reason. Registering a runner wakes the scheduler. A missing runner is a visible wait state, not a silent stall.

---

## 6. Runner protocol

### 6.1 Timers

| Timer | Value | Owner | Meaning |
|---|---|---|---|
| Heartbeat interval | 10 s | runner | cadence; each accepted heartbeat is durable before its reply |
| Lease TTL | 30 s after last acked heartbeat | server | `started` → `suspect` |
| Grace | 120 s | server | `suspect` → `lost` at last heartbeat + 150 s |
| Start window | 60 s after grant | server | `granted` → `expired_unstarted`; no consumption |
| Self-fence | 135 s after last acked heartbeat | runner | kill sandbox; complete with class `lost`. 15 s margin under the server's `lost` time |
| Attempt deadline | job timeout from accepted start | server | deadline; cancel flag on next heartbeat |
| Timeout slack | 30 s after deadline | server | `started` → `timed_out` if no complete |
| Cancel kill | 10 s after SIGTERM | runner | SIGKILL |
| Cancel give-up | 120 s after cancel request | server | `cancelled` without runner report |
| Long-poll | 20 s default, 25 s max | both | lease wait |
| Log chunk | ≤ 64 KiB; flush ≤ 1 s | runner | — |
| Artifact quarantine GC | 24 h | server | unlinked blobs |
| Clock-jump tolerance | 5 s | server | divergence between wall and monotonic elapsed time (§10.2) |

Expiry is driven by persisted deadlines. The in-memory timer index is rebuilt from them at boot. The index is a cache and is never authoritative.

### 6.2 Auth
- **Runner:** `Authorization: Bearer <runner token>` for lease, registration, and release. Stored as a hash.
- **Attempt:** the lease response returns `attempt.token` (256-bit random, stored as a hash). Attempt-scoped calls need the token and the fence. The token dies at terminal state. A fence bump (`lost`) invalidates both.
- The fetch credential for the workspace is the attempt token.
- **Runner key:** X25519, registered at runner registration, used to seal secrets (§8.3).

### 6.3 Endpoints
Base path `/_/ci/runner/v1/`. JSON unless noted. Errors are RFC 9457 `application/problem+json` with `type = urn:rukie:problem:<CODE>` (§6.4).

**1. `POST /lease`**

Request:
```json
{"runner_id": "rnr_01", "labels": ["linux-amd64"], "scope": "trusted",
 "platform": "linux/amd64", "sandbox_profile": "podman-rootless/1",
 "free_slots": 2, "wait_ms": 20000}
```

200:
```json
{"attempt": {"id": "att_01", "number": 1, "fence": 4711, "token": "<b64url 32 bytes>"},
 "run": {"id": "run_01", "trust": "trusted", "spec_source": "tree",
         "subject": "<commit oid>", "tree": "<tree oid>"},
 "job": {"name": "test", "spec_digest": "sha256:<hex>", "action_digest": null,
         "pure": false, "effects": "none", "network": "egress", "interruptible": true,
         "timeout_s": 1800,
         "image": {"ref": "docker.io/library/golang@sha256:<index digest>",
                   "manifest": "sha256:<platform digest>"},
         "steps": [{"name": "build", "run": "go build ./..."}],
         "env": {"CGO_ENABLED": "0", "RUKIE_TREE": "<tree oid>"},
         "artifacts": ["out/coverage.out"]},
 "fetch": {"url": "https://<host>/<owner>/<repo>.git", "want": "<commit oid>",
           "expect_tree": "<tree oid>"},
 "secrets": [{"name": "GOPROXY_TOKEN",
              "sealed": {"epk": "<b64>", "nonce": "<b64>", "ct": "<b64>"}}],
 "timers": {"heartbeat_s": 10, "lease_ttl_s": 30, "self_fence_s": 135}}
```

- 204: no work within `wait_ms`.
- 503 `E_RECOVERING`: boot recovery not committed (I11).
- The server filters, so the runner never receives a job it cannot run.
- Non-pure jobs also receive `RUKIE_COMMIT`, `RUKIE_RUN`, `RUKIE_ATTEMPT`, and `RUKIE_FENCE` (for external systems on `exclusive` jobs). Pure jobs receive none of these.
- `action_digest` is non-null for pure jobs, which never carry secrets.

**2. `POST /attempts/{id}/start`**

Request: `{"fence": 4711}`
200: `{"state": "started", "cancel": null, "heartbeat_persisted_at_ms": 1760000000000}`
409 `E_LEASE_LOST` if the fence or state is wrong.

The runner calls `start` immediately on receiving the lease, before image pull or fetch. Start means "accepted and working," so pulls never consume the 60 s start window. The start transaction is A1 and persists the three deadline columns.

**3. `POST /attempts/{id}/heartbeat`**

Request: `{"fence": 4711}`

Server behavior: one conditional update (§4.3). It persists `last_heartbeat_at`, `lease_expires_at`, and `lost_at`, and sets `state=started` if the attempt was `suspect` (A6). The 200 is sent only after the batch commits. Concurrent heartbeats for one attempt in the same batch collapse to the latest row version; each caller still receives its reply after commit.

200:
```json
{"state": "started",
 "cancel": null,
 "lease_ttl_s": 30,
 "heartbeat_persisted_at_ms": 1760000000000,
 "log_ack_offset": 81920}
```
`cancel`, when set, is `{"reason": "superseded", "kill_after_s": 10}`.

- 409 `E_LEASE_LOST`: fence stale, or attempt terminal, or `lost`.
- 409 `E_NOT_STARTED`: attempt still `granted`.
- 404: unknown attempt (the runner treats it as lost).
- 503 `E_RECOVERING`: boot recovery not yet committed. The runner retries with backoff and keeps the job running.

Write load: at most one coalesced row update per attempt per batch. At 1,000 concurrent attempts that is about 100 updates/s before coalescing (§14).

**4. `POST /attempts/{id}/logs`**

Request:
```json
{"fence": 4711, "seq": 42, "offset": 81920, "sha256": "<hex of raw bytes>",
 "data_b64": "<b64 of at most 64 KiB raw>",
 "events": [{"kind": "step_finished", "index": 0, "offset": 81920, "exit_code": 0},
            {"kind": "step_started", "index": 1, "offset": 83000}]}
```
- Every event offset lies in `[offset, offset + len]`.
- 200: `{"ack_offset": 86016, "next_seq": 43}`. The ack is returned only after commit.
- A replay of the same `seq` with the same `offset` and `sha256`: 200 with the same ack.
- 409 `E_OFFSET_GAP` `{"ack_offset": N, "expected_seq": M}`: `offset` ≠ end, or `seq` skips.
- 409 `E_SEQ_CONFLICT`: the seq exists with different content.
- 422 `E_DIGEST_MISMATCH`: `sha256` wrong.
- 413 `E_LIMIT`: chunk exceeds 64 KiB.
- 409 `E_LEASE_LOST`; 503 `E_RECOVERING`.
- Over the 64 MiB cap, offsets keep advancing and the middle is elided (§8.1). The runner is unaware.

**5. `PUT /attempts/{id}/blobs/{digest}`**

Raw body. Headers: `Content-Length` (required), `X-Rukie-Fence: 4711`.
- 201 new, or 200 already exists. Both return `{"digest": "sha256:<hex>", "size": N}`.
- 422 `E_DIGEST_MISMATCH`; 413 `E_LIMIT` above 1 GiB; 409 `E_LEASE_LOST`.
- Writes to a temp file, verifies the digest while streaming, fsyncs, then renames into CAS as `quarantine`.

**6. `POST /attempts/{id}/complete`**

Request:
```json
{"fence": 4711, "outcome": "passed", "class": null, "exit_code": 0, "failed_step": null,
 "log": {"last_seq": 42, "end_offset": 86016, "sha256": "<hex of the whole raw log>"},
 "artifacts": [{"name": "out/coverage.out", "digest": "sha256:<hex>", "size": 2048}],
 "result_digest": "sha256:<hex of JCS of this object minus result_digest>"}
```
- `outcome` ∈ {`passed`, `failed`, `errored`}.
- `class` ∈ {`script_failure`, `infra`, `lost`, `timeout`, `cancelled`}. Required when `errored`. Must be `script_failure` when `failed`.
- 200: `{"attempt": "completed", "job": "passed", "run": "running", "replayed": false}`.
- A replay (same attempt, same `result_digest`) returns the stored reply with `replayed: true`.
- 409 `E_RESULT_CONFLICT`: attempt terminal with a different digest.
- 409 `E_LEASE_LOST`: fence stale, or attempt `lost` or `timed_out`. A fenced-out result is discarded and never cached or attested.
- 409 `E_LOG_INCOMPLETE` `{"ack_offset": N}`: the runner must replay logs.
- 409 `E_BLOB_MISSING` `{"digest": "sha256:<hex>"}`: the runner must upload.
- 422 `E_DIGEST_MISMATCH`: `result_digest` wrong.
- The finish transaction is §9.4.

**7. `POST /attempts/{id}/release`**

Request: `{"fence": 4711, "reason": "not_started" | "runner_unsuitable" | "shutdown"}`
200 while `granted`. 409 `E_NOT_STARTED`-family error once `started`: the runner must then complete or be lost.

**8. Registration** is the admin op `ci.runner.register` (§11).

### 6.4 Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `E_LEASE_LOST` | 409 | fence stale, or attempt terminal or lost |
| `E_NOT_STARTED` | 409 | heartbeat or complete while still `granted` |
| `E_OFFSET_GAP` | 409 | log offset or seq does not extend the stream |
| `E_SEQ_CONFLICT` | 409 | log seq exists with different content |
| `E_RESULT_CONFLICT` | 409 | completed attempt, different `result_digest` |
| `E_LOG_INCOMPLETE` | 409 | complete before full log ack |
| `E_BLOB_MISSING` | 409 | declared artifact not uploaded |
| `E_DIGEST_MISMATCH` | 422 | sha256 or `result_digest` mismatch |
| `E_RECOVERING` | 503 | boot recovery not committed (I11) |
| `E_LIMIT` | 413 | chunk or blob over limit |
| `E_UNAUTHENTICATED` | 401 | missing or bad token |
| `E_FORBIDDEN` | 403 | scope or permission |
| `E_NOT_FOUND` | 404 | unknown attempt or resource |
| `E_REQUEST_REUSED` | 409 | public op: same `request_id`, different arguments |
| `E_INVALID` | 422 | public op: validation failure (pointer-coded for specs) |

### 6.5 Runner behavior (normative)
- On 5xx or connection refused: retry with jittered backoff. The job keeps running.
- No acked heartbeat for 135 s (monotonic): SIGTERM, 10 s, SIGKILL. Then `complete` with class `lost`, if reachable.
- On a `cancel` flag: SIGTERM, 10 s, SIGKILL, then `complete` with class `cancelled`.
- On startup: remove orphan sandboxes labelled `rukie.attempt=<id>`. `release` any granted attempts it does not own.
- Spool logs to disk. Delete the spool only after `complete` returns 200.

---

## 7. Result cache

### 7.1 Eligibility
Only pure jobs. Only `passed` outcomes. Only trusted producers. Never `failed`, `errored`, `timed_out`, `cancelled`, `lost`, `infra`, or `policy`.

### 7.2 Action digest
`action_digest = "sha256:" + hex(SHA-256(JCS(object)))`, where:

```json
{"schema": "rukie.ci.action/v1",
 "salt": {"epoch": 3, "repo": "<repo id>"},
 "tree": "<tree oid>",
 "job_spec_digest": "sha256:<hex>",
 "image": "sha256:<platform manifest digest>",
 "platform": "linux/amd64",
 "env": {"<declared env, sorted>": "<value>"},
 "deps": [{"need": "lint", "action_digest": "sha256:<hex>"}],
 "runner_abi": "podman-rootless/1"}
```

- **`tree`, not the commit.** Commit, run, attempt, time, runner id, and timeout are excluded. A pass under a 30 min timeout is a pass under 60 min.
- **`job_spec_digest`** already covers steps, labels, needs, artifacts, effects, network, and the pure flag.
- **`image`** is the platform manifest digest, resolved at ready time. A resolution failure is an `infra` error.
- **`env`** is declared env only. `RUKIE_TREE` is derived from the tree and is not repeated.
- **`deps`** are the action digests of `needs`. Pure jobs may need only pure jobs (§2.4), so the digest is Merkle-like.
- **`runner_abi`** is the sandbox profile id. Changing sandbox semantics bumps it.
- **`salt.epoch`** invalidates a repo's entries. **`salt.repo`** isolates repos and forks.
- **Trust is not in the key.** It is a property of the stored entry (`ci_cache` has no trust in the digest) [R correction 2].

### 7.3 Trust levels and reuse
- **T0 (untrusted producer):** runs with `trust=untrusted` (changes, candidates, unprotected pushes, override). **Never stored in v1.**
- **T1 (trusted producer):** runs with `trust=trusted`, i.e., a protected ref whose spec comes from the protected commit.
- **Reuse rule:** an entry is usable iff `entry.producer_trust ≥ requester.floor`. In v1 the floor is T1 for every requester. Untrusted runs read trusted entries. Untrusted runs have no write path. [I]
- **Why:** a hit replaces an execution, so it must be at least as trustworthy as an execution in the requester's scope. A T1 entry meets that bar for every requester.

[295 more lines in resource. Use :1790 to continue]
- **Why:** a hit replaces an execution, so it must be at least as trustworthy as an execution in the requester's scope. A T1 entry meets that bar for every requester.

### 7.4 Writes, single-flight, divergence
- Written only in the finish transaction of the winning attempt (fence valid), in the same transaction as the job's terminal state. `INSERT … ON CONFLICT DO NOTHING`: the first writer wins.
- **Divergence:** a second trusted pass for the same digest with a different artifact manifest records a `ci_cache_divergence` event and alerts. The job is not pure (author error). This is detected only when a pure job re-runs. Shadow re-execution is deferred.
- **Single-flight:** trusted pure jobs with equal digest. Followers wait (`single_flight`). Leader passes → followers `cached`. Leader fails → followers proceed.
- **Invalidation** is automatic through the key: spec change, image change, label change, epoch bump, or `runner_abi` change.

### 7.5 Efficiency notes
- A hit costs one indexed read and no runner time. It still writes an attestation (via outbox).
- Tree-keyed hits cover reverts, rebases without content change, re-runs, and merge candidates identical to a previously tested trusted tree.
- **Limitation:** any file change invalidates a job. Declared input globs are deferred, since soundness requires the declared set to be complete.
- **Cost:** a landed commit runs twice: once as an untrusted candidate, then again post-merge as trusted, because T0 is never stored. Deferred: promote untrusted pure verdicts at `land.execute`, when a protected ref adopts the exact reviewed candidate.

---

## 8. Logs, artifacts, secrets

### 8.1 Logs
- The runner spools in chunks of at most 64 KiB, flushed at least every 1 s. Each chunk carries `seq`, `offset`, and `sha256`.
- The server appends per §6.3 endpoint 4. Each append is one transaction, acked after commit. SQLite WAL with `synchronous=FULL` provides durable commit [R].
- Offsets are raw byte offsets and are never renumbered.
- **Cap:** 64 MiB per attempt. Keep the first 4 MiB and the most recent remainder. Elided ranges are reported by `ci.log.read` as `elided: [{from, to}]`.
- **Seal at finalize:** concatenate raw bytes, gzip them (stdlib `compress/gzip`; zstd would need a third-party module), store as a CAS blob, set `sealed_log_digest`, and delete chunk rows in the same transaction. Sealing is idempotent: CAS write, then conditional update.
- **Redaction:** the runner masks secret values. The server also masks the attempt's resolved secret values across chunk boundaries (window = longest value − 1). This is best-effort. The real control is §8.3.
- **Retention:** logs 90 days (minimum 7, per repo). Attempt rows 1 year. Artifacts 30 days. Cache until epoch bump. Outbox until delivered. `ci_idem` 30 days.
- **Web:** log pages are server-rendered `<pre>` blocks with offset links. No JavaScript.

### 8.2 Artifacts
- Content-addressed. Upload verifies sha256 while streaming. Quarantined until `complete` links them. Quarantine GC after 24 h.
- A declared artifact missing or mismatched at `complete` makes a passed job `failed` (J13).
- Per-blob limit 1 GiB. Per-repo quota default 10 GiB. GC removes unlinked and expired blobs, oldest first.
- Read via `ci.artifact.read`. Auth is in-band; there are no signed URLs.

### 8.3 Secrets
- **Storage:** AES-256-GCM (`crypto/aes`, `crypto/cipher`). A per-secret data key is wrapped by a server key (file or KMS) [I]. Values never appear in logs, attestations, cache keys, run JSON, or API replies. APIs return names and versions only.
- **Declaration:** per job (`secrets`). Pure and required jobs never declare secrets (§2.4, D8).
- **Resolution:** at lease time, only when `run.trust = trusted` and the runner has `scope = trusted` (§5.3). The server decrypts, seals to the runner key, and never persists plaintext.
- **Delivery:** sealed to the runner's X25519 key. Ephemeral X25519 key agreement (`crypto/ecdh`), HKDF-SHA256 (`crypto/hkdf`, Go ≥ 1.24 [I]), then AES-256-GCM. The lease response carries ciphertext only, so plaintext never reaches server logs, proxies, or the database.
- **Scope gates (I7):** untrusted runs never resolve secrets. Trusted secret-bearing jobs lease only to trusted-scope runners. Untrusted-scope runners never run trusted secret-bearing jobs.
- **Fork and agent changes** are never trusted by author identity. Trust comes from ref protection at trigger time.
- Attestation predicates list secret **names** only.

---

## 9. Attestation output and outbox relay

### 9.1 Verdict statement (what Ruk sees)
```
subject    = run.subject                  (commit oid; must be a stored commit)
kind       = "ci/" + job.name + "/" + hex64(job_spec_digest)
result     = "pass" | "fail"
signed_at  = finish time truncated to whole seconds, RFC 3339 UTC "Z"; fixed in the outbox row
supersedes = digest of the previous published verdict on ci_chain(subject|kind), or null
```
- `kind` is at most 3 + 63 + 1 + 64 bytes: a valid `AttestKind`.
- **Publication rule:** an untrusted verdict is published only if the chain head is not trusted (`ci_chain.head_trust`). Otherwise it is recorded in rukie metadata only. This prevents an untrusted run from superseding a trusted verdict on the same commit and kind.
- Chain order is the server's commit order of finish transactions, not `signed_at`.

### 9.2 In-toto statement (rukie-owned blob)
Generated once in the finish transaction, stored in CAS, and never regenerated:
```json
{"_type": "https://in-toto.io/Statement/v1",
 "subject": [{"name": "git:<repo id>@<commit>", "digest": {"gitCommit": "<commit>"}}],
 "predicateType": "https://github.com/gosuda/rukie/ci/job-result/v1",
 "predicate": {
   "job": {"name": "test", "spec_digest": "sha256:<hex>", "spec_source": "base"},
   "run": {"id": "run_01", "trigger": "change", "trust": "untrusted", "base": "<commit>"},
   "tree": "<tree oid>", "action_digest": null, "image": "sha256:<hex>",
   "result": "pass", "exit_code": 0,
   "cache": {"hit": false, "source_action": null},
   "attempt": {"id": "att_01", "number": 1, "runner_id": "rnr_01",
               "started_at": "<RFC 3339 Z>", "finished_at": "<RFC 3339 Z>"},
   "attempts_total": 1,
   "log": "sha256:<hex>",
   "artifacts": [{"name": "out/coverage.out", "digest": "sha256:<hex>", "size": 2048}],
   "secrets": []}}
```
The `gitCommit` digest key follows in-toto [R]. This is an export artifact. Ruk does not read it.

### 9.3 Canonical payload and signature
- **Payload v1 bytes** (must equal Ruk's). For each field in order, write a BE32 length, then the bytes: `"ruk-attest-payload-v1"`, `"sha1"` or `"sha256"`, raw subject bytes (20 or 32), kind, result, timestamp text, superseded digest (32 raw bytes or empty).
- `statement_digest = SHA-256(payload)`.
- **Golden vectors:** copy from ruk-core tests (protocol.rs ~4600-4640, attestation tests, `crypto.rs`) into rukie's tests.
- **Signature:** SSHSIG armored, namespace `ruk-attest`, hash sha256, plain `ssh-ed25519`. The stdlib has `crypto/ed25519` but no SSHSIG framing. Implement the framing (~120 lines) and validate it against a Ruk fixture [I].
- Ed25519 signing is deterministic, so a replayed relay yields identical signature bytes [I].
- **Evidence binding:** Ruk v1 cannot bind the statement blob. Until R1 lands, `ci_statement` maps `statement_digest → blob_digest` in rukie. With R1, `evidence = sha256(blob)` enters canonical payload v2.

### 9.4 Finish transaction (`complete` accepted)
1. **Validate:** attempt non-terminal (or a replay, §4.3); fence matches; log sealed-ack equals `log.end_offset`; every artifact blob exists with matching digest and size; `result_digest` recomputes equal.
2. **Retry decision** (§4.6). If retried: job `queued` (J15), no verdict.
3. **Otherwise** the job is terminal (J12, J13, J14, J16, J17, J18, or J24 when cancelling). Dependents: success → `queued` (J3); failure → `skipped` (J4); cancelled → `cancelled` (J5). Cascade in the same transaction.
4. **Artifacts** linked (`ci_blob` → `linked`). Steps finalized (S4, S6 as applicable).
5. **Cache insert** if pure, trusted, and passed (`INSERT … ON CONFLICT DO NOTHING`).
6. **Verdict** (final `passed` or `failed`, or a cache hit): build the in-toto blob into CAS. Compute the canonical payload and its digest. Read the chain head, set `supersedes`, and advance `ci_chain`. Apply the publication rule (§9.1). Insert a `ci_outbox` row (kind `attest`) with fixed `signed_at`, a UUIDv4 id, and the payload bytes. Insert `ci_statement`.
7. **Run roll-up** (R4, R6). Outbox rows for commit status and signed webhooks (Standard Webhooks headers; `webhook-id` = outbox id [I]).
8. **Commit,** then respond 200.

### 9.5 Relay
- One worker per chain, FIFO by `seq`. Different chains run in parallel under a bounded pool.
- Each relay: sign (deterministic Ed25519) and call `attest.add` with `request_id = outbox.id`, through the CI principal's daemon session.
- **Success or replayed:** mark delivered. Verify `record.payload_digest == statement_digest`; a mismatch is corruption and alerts. Store `record.status` (`live`, `superseded`, or `ambiguous`).
- **Retry** (backoff 1 s to 5 min; the chain blocks in order): `E_NOT_FOUND` (supersedes target or subject not yet visible), `E_LEASE_LOST`, `E_CAPACITY`, transport errors.
- **`E_INVALID`** (subject not a stored commit): retry up to 1 h, then dead-letter and alert.
- **`E_FORBIDDEN`** (kind not granted, R2 missing), `E_POLICY`, `E_OBJECT_FORMAT`: dead-letter and alert. The chain blocks. Operators resolve it. Rows are never silently skipped.
- **Delivery is at-least-once.** Duplicates are harmless:
  - (a) Ruk receipts replay within 7 days [src ruk/README.md:5].
  - (b) An identical statement from the same signer dedupes at row key (subject, kind, signer, digest) and keeps the first signature [src attestation/prepare.rs module doc].
  - (c) Payload bytes are fixed in the outbox row, so a re-send after 7 days is byte-identical.
- The relay does not dead-letter rows for receipt age. Identical statements dedupe at the row key [R correction 3].

---

## 10. Crash recovery

### 10.1 Principles
1. Every transition, and every accepted heartbeat, is durable before its reply.
2. Stored heartbeats and fences are recovered and verified before any grant. Only then are deadlines rebased.
3. Every runner-facing write is idempotent by (attempt, fence, seq), or by (attempt, fence, `result_digest`).
4. Boot never mass-expires leases. Server downtime is not charged to runners.

### 10.2 Boot sequence

**Phase A: recover.** All runner and admin endpoints return `E_RECOVERING` (I11).
1. Open the store. Check schema version and run a quick integrity check. On corruption, stay in recovering state, refuse all grants, and alert.
2. Load every non-terminal attempt, with its job, fence, state, `last_heartbeat_at`, `lease_expires_at`, `lost_at`, cancel fields, and `log_end_offset`. Load `ci_fence` and assert `next > max(fence)`. Load `ci_controller.epoch` and bump it. Every later writer transaction conditions on the new epoch, so a stale controller is rejected if two ever run [I].
3. Verify I1 and fence uniqueness across the loaded set. On violation, stay in recovering state and alert. This is corruption, not a normal case.
4. **Rebase, in one transaction, persisted before any endpoint opens:**
   - `granted`: `start_deadline_at = boot + 60 s`.
   - `started` or `suspect`: copy `last_heartbeat_at` to `pre_restart_heartbeat_at`. Set `last_heartbeat_at = boot`, `lease_expires_at = boot + 30 s`, `lost_at = boot + 150 s`, `rebased_at = boot`, and `state = started`. A `suspect` attempt is revived here as a persisted transition.
   - `deadline_at = boot + job timeout`. Cancel fields are kept. This is lenient by design, since restarts are rare [I].
   - **Rebase uses recovery time, not the stored value,** because stored deadlines would expire every runner that kept retrying through server downtime. The stored heartbeat is recovered and kept for audit. That is why phase A reads it before rebasing.
5. Commit. Open endpoints.
6. Build the in-memory timer index from persisted deadlines. It is a cache and can be rebuilt at any time.

**Phase B: resume (endpoints open).**
7. Rebuild queue views from `ci_job` (`queued`) and `ci_fair`. No heap is needed.
8. Resume the trigger cursor (`ci_seen`) and run one reconcile pass.
9. Resume relays from the lowest undelivered `seq` per chain.
10. Resume sealing for terminal attempts with `sealed_log_digest` null.
11. Quarantined blobs stay until GC.

**Clock-jump guard (ongoing).** Persisted deadlines are wall-clock, so a clock step could mass-expire leases. Each reaper tick compares wall-clock and monotonic elapsed time since its last tick. If they diverge by more than 5 s, the reaper runs the step 4 rebase with reason `clock_jump`, persists it, and only then evaluates expiry.

### 10.3 Crash points

| Crash point | On disk | Recovery |
|---|---|---|
| Before lease transaction | job `queued` | runner retries lease; nothing granted |
| After lease transaction, before response | attempt `granted` | no `start` → `expired_unstarted` after the start window; job back to `queued`; nothing consumed |
| Heartbeat transaction in progress | atomic | runner retries; a repeat heartbeat is an idempotent update |
| Heartbeat committed, reply lost | `last_heartbeat_at` persisted | runner retries; the stored heartbeat is accurate |
| Server down while attempt `started` | persisted heartbeat T0 | boot rebases to boot (persisted before any grant); runner's 5xx retries keep the job alive; revived on heartbeat, else `lost` at boot + 150 s → retry rule |
| Server down while attempt `suspect` | persisted `lost_at` | boot revives to `started` with fresh deadlines; a runner that is really dead becomes `lost` at boot + 150 s |
| During phase A rebase | rebase uncommitted | next boot repeats from stored state; nothing lost |
| During log append transaction | atomic | runner resends same `seq`/`offset`/`sha256` → ack |
| After finish commit, before response | job terminal; outbox rows | runner retries `complete` → `replayed: true` |
| During finish transaction | nothing committed | runner retries; full re-evaluation |
| After finish, before relay | outbox pending | relay resumes; statements byte-identical |
| During `attest.add` | row pending | Ruk receipt replay or row dedupe |
| During blob upload | temp file only | rename after digest verify; `E_BLOB_MISSING` → re-upload |
| During seal | chunks present | seal redone |
| During cancel | `cancel_requested_at` persisted | re-delivered on heartbeat |
| Runner crashes | runner restart kills orphan sandboxes and releases granted attempts; otherwise `lost` after grace | retry rule |
| Store corrupt | — | refuse grants; alert; no partial state served |

---

## 11. Operations surface

Public ops are entries in one `Op[In,Out]` table. Each entry yields an MCP tool (`ci_run_list`, etc.), a CLI command (`rukie ci run list --json`), HTML forms for mutations, OpenAPI 3.1, and RFC 9457 errors. Public URLs negotiate `text/html`, `application/json`, and `text/markdown` on the same path. Runner ops carry `surface: runner`. They appear in OpenAPI only, not in MCP, CLI, or forms.

| Op | HTTP | Idempotent (`request_id`) | Input | Output |
|---|---|---|---|---|
| `ci.run.list` | `GET /{owner}/{repo}/ci/runs` | no | `state?`, `subject?`, `change?`, `ref?`, `trigger?`, `cursor?`, `limit` ≤ 1000 (default 100) | `runs[RunSummary]`, `next_cursor?` |
| `ci.run.get` | `GET /{owner}/{repo}/ci/runs/{run}` | no | `run` | `Run{…, jobs[JobView{…, attempts[AttemptView]}]}` |
| `ci.run.create` | `POST /{owner}/{repo}/ci/runs` | yes | `request_id`, `subject?` (default: ref tip), `ref?` or `change?`, `spec_from` = `base`\|`tree` (default `base`) | `Run` |
| `ci.run.cancel` | `POST /{owner}/{repo}/ci/runs/{run}/cancel` | yes | `request_id`, `reason?` ≤ 200 | `{state, cancelled_jobs[], signalled_attempts[], exclusive_running[]}`; a terminal run returns `noop: true` |
| `ci.run.retry` | `POST /{owner}/{repo}/ci/runs/{run}/retry` | yes | `request_id`, `jobs[]?`, `acknowledge_side_effects?` | `{run (new), carried[], rerun[]}` |
| `ci.job.list` | `GET /{owner}/{repo}/ci/jobs` | no | `state?`, `wait_reason?`, `run?`, `cursor?`, `limit` | `jobs[{id, run, name, state, wait_reason?, runs_on, class, queued_at, ready_at?}]` |
| `ci.log.read` | `GET /{owner}/{repo}/ci/attempts/{attempt}/log` | no | `offset` (default 0), `limit` ≤ 1 MiB (default 64 KiB), `wait_ms` ≤ 25000, `encoding` = `text`\|`base64` | `{data, offset, next_offset, eof, attempt_state, elided[]}` |
| `ci.artifact.list` | `GET /{owner}/{repo}/ci/runs/{run}/artifacts` | no | `job?`, `cursor?` | `artifacts[{job, name, digest, size}]` |
| `ci.artifact.read` | `GET /{owner}/{repo}/ci/blobs/{digest}` | no | `digest` | raw bytes; `Content-Type` from record |
| `ci.spec.validate` | `POST /{owner}/{repo}/ci/spec/validate` | no | `ref` or `tree` | `{valid, digests{job→digest}, errors[{pointer, line, col, message}]}` |
| `ci.spec.get` | `GET /{owner}/{repo}/ci/spec?ref=` | no | `ref` | `{effective, digests, required[]}` |
| `ci.runner.register` (admin) | `POST /_/ci/v1/runners` | yes | `labels[]`, `scope`, `pubkey`, `capacity`, `sandbox_profile` | `{runner_id, token}` (token shown once) |
| `ci.runner.list` (admin) | `GET /_/ci/v1/runners` | no | — | `runners[]` |
| `ci.runner.revoke` (admin) | `POST /_/ci/v1/runners/{id}/revoke` | yes | — | `{status}`; active attempts become `lost` after grace |

**Rules**
- **Permissions:** read requires repo read. Create, cancel, and retry require write. `spec_from=tree` requires `ci.spec.override` (admin). Secret values are never readable.
- **Idempotency:** `request_id` is a UUID. Receipts are keyed by (principal, `request_id`). The same arguments replay the reply. Different arguments return `E_REQUEST_REUSED`, mirroring Ruk [src protocol.rs:992-993].
- **Paging:** opaque cursor; default 100; max 1000 (same as Ruk).
- **Errors:** §6.4 codes plus `E_NOT_FOUND`, `E_FORBIDDEN`, and `E_INVALID` (spec errors pointer-coded).

---

## 12. Why these choices beat the failure modes of GitHub Actions, GitLab CI, and others

"[R]" facts are from `research-ci-runner.md` §1 and its cited sources, which were not re-fetched here. Where a comparison relies on my knowledge, it is marked [I].

| Failure mode | What others do [R unless noted] | This design |
|---|---|---|
| Runner partition makes the job ambiguous | GHA: runner renews its lock every 60 s, retries until `LockedUntil + 5 min`, then stops. GitLab: `no_updates_running` is an opt-in retry class. Buildkite: lost after 3 min without heartbeat, exit `-1`. | Two-phase expiry (`suspect` then `lost`), with both deadlines persisted. Each heartbeat is durable before its reply, so the server's liveness view survives restart. The runner self-fences 15 s before the server can declare `lost`. Every write is fenced. |
| A late success after a reap is dropped or mishandled | Forgejo PR #12509 (closed, unmerged): a slow log POST delays the heartbeat, the reaper fails the task, and the late SUCCESS is dropped by the `IsDone()` guard. | Fence rejects stale writes with `E_LEASE_LOST`. The fenced attempt decides the result once. A lost attempt's result is discarded and never cached or attested. |
| Restart mass-expires running work | Forgejo's reaper keys on `Task.Updated` (`services/actions/clear_tasks.go` ~22-30 [src]). Defaults 10 min / 3 h / 24 h [R]. | Stored state is recovered and verified first. Boot rebases every lease with grace. No reaper keyed on last-update time. |
| Heartbeat blocked behind log uploads | Same Forgejo PR #12509. | Heartbeat is a separate endpoint with its own durable write. Logs use their own endpoint with ack. |
| Logs truncated or lost | GitLab flushes every 3 s (open) or 60 s, or at 128 KB [R]. Buildkite: "Giving up on uploading chunk … partial build log" [R]. | Offset-addressed chunks, durable before ack. The runner spools until the finalize ack. `complete` refuses without a full ack. |
| Required checks satisfied by PR-modified CI | Not covered by the research [I]. | Untrusted runs use the base spec (D2). The kind embeds the spec digest (D3). Landing requires the exact kind (R3). Required jobs have no secrets and no network (D8). |
| Cache poisoned by untrusted code, or base cache readable by PRs | GHA: anyone who can open a PR can read the base-branch cache; "save caches from trusted triggers" [R]. Bazel: "take care in who has the ability to write" [R]. | Only trusted runs write. Reads are trusted only. The key covers all inputs. Salt epoch and repo isolate. |
| Superseded runs burn capacity | GHA concurrency replaces pending runs and can cancel running ones. GitLab `interruptible` and `auto_cancel`, default `conservative` [R]. | Same-transaction supersede: queued jobs cancelled at once; interruptible attempts signalled. Exclusive jobs are never signalled. |
| Noisy neighbor starves others | GHA: self-hosted queue limit 24 h [R]. GitLab: `stuck_pending` failures [R]. | Hierarchical fair queueing across class, repo, and principal. Caps. Class deadlines. A missing runner is a visible wait reason. |
| Silent or unbounded retries | Buildkite: default 2 retries, max 10, creates a new job [R]. GitLab: opt-in retry classes [R]. | Explicit retry classes. Attempts live under one job (history immutable). Script failures do not retry by default. Exclusive jobs never retry automatically. |
| Duplicate execution | Not documented in the sources read; marked unknown [R]. | Admitted: execution can repeat after loss. Prevented: duplicate accepted results and duplicate attestations. Exclusive jobs receive `RUKIE_FENCE` for external dedupe. |
| Queue state held in memory | Woodpecker: `TypeMemory` queue. The store re-pushes only rows, and `Poll` deletes the row on hand-out [R]. | The queue is rows. Nothing else is authoritative. Restart resumes from rows. |
| Green checkmark with no provenance | Not in research. GHA checks carry no signed verdict [I]. | Ruk SSHSIG attestation bound to the commit id, verifiable through Ruk's `verify_statement` [src]. |

---

## 13. Ruk prerequisites

| ID | Change | Why | Where | Blocking? |
|---|---|---|---|---|
| R1 | Canonical payload v2 with `evidence: Option<sha256>` (domain `ruk-attest-payload-v2`). v1 stays verifiable. Breaking change accepted by the settled decisions. | Binds the in-toto blob to the verdict | protocol.rs:1775-1790, 3638-3665 | No (rukie metadata fallback via `ci_statement`) |
| R2 | `Capabilities.attest` accepts a namespace grant (e.g., `ci/`). | Kind carries the spec digest. Exact-match grants would need policy edits on every spec change. | capability.rs:657-658, 730-739, 851-860 | Blocks D3 |
| R3 | Land requirements, trusted signers, and `policy_generation` supplied to `land.prepare` (the `[land] requirements` policy section). | Gates landing on CI verdicts | coord/prepare.rs:3430-3434, 3482-3486; coord/land.rs | Blocks the landing gate |
| R4 | Hosted coordinator serves reads for change and ref observation and for code browsing: `status`, `log`, `show`, `query`, `file`, `diff`. | Change triggers, reconcile, base resolution, and the forge's own code browser | daemon/mod.rs:611-615, 623-624; coord/hosted.rs:522-526 | Blocks change triggers |
| R5 | Hosted coordinator covers `land.execute`, and decides the `push` and `fetch` path | Landing through the daemon | daemon/mod.rs:626-627 | Blocks landing |
| R6 (optional) | `attest.list` filter by kind prefix | Dashboards | protocol.rs `AttestListArgs` | No |

Unknowns that need Ruk confirmation:
- Runner tree fetch by `want <commit>` through Ruk `upload-pack` with unadvertised commits [U]. If unsupported, a tree-export RPC is needed.
- Ref listing through the daemon's git-shaped command path [U].
- Whether receive-pack ref moves appear in `op.log` [U]. The design does not depend on it: the reconciler and the push-path edge trigger cover it.

---

## 14. Risks and deferred items

- **Store.** The metadata store is still open. CI needs: serializable transactions with conditional writes; unique indexes (trigger key, job name, cache digest, fence); ordered index scans for lease selection; durable commit before ack. SQLite (WAL, `synchronous=FULL`) meets these [R]. Pebble and PostgreSQL can too, with more code. The single-writer group-commit design works on any of them.
- **Heartbeat write load.** Coalesced per attempt per batch, roughly N/10 row updates per second for N concurrent attempts, all through the single writer. Benchmark on the target store before v1 [U]. Fallbacks, in order: longer group-commit linger (bounded by the 10 s interval), then an adaptive heartbeat interval (for example 15 s at high concurrency, with lease TTL scaled to keep the same ratio).
- **Purity is declared, not proven.** Divergence detection fires only when a pure job re-runs with different artifacts. Shadow re-execution is deferred.
- **Runner is in the trusted computing base.** An attestation says "the server observed this runner report exit 0." It is not reproducible-build evidence. Runner id is recorded. Cross-runner re-execution is deferred.
- **Sandbox.** Rootless podman is the primary path [R §4]. Research could not probe this host: `apparmor_restrict_unprivileged_userns` is `1` [R]. A runner self-test gates the runner. Landlock is deferred: rulesets are inherited across `fork`/`exec`, so applying Landlock to the runner would confine its podman child [R correction 4].
- **`spec_from=base` trade-off.** CI changes in a change are invisible until merged. The `spec_from=tree` override is untrusted, advisory, and never land-eligible.
- **Cost.** Landing runs twice (candidate, then post-merge trusted). Promotion at `land.execute` is deferred.
- **Required-check constraints** (D8) mean required jobs cannot fetch dependencies. Vendoring is required until a module proxy exists (deferred).
- **Land window.** The candidate's whole CI must finish within the 1 h Ruk plan TTL. The land class deadline is 30 min. A plan that outlives its run is re-prepared [I].
- **Secret sealing** adds X25519 key management for runners.
- **HA.** Multi-controller operation is out of scope. Epoch fencing is in place, so adding it later does not change the protocol.
- **Research corrections applied:** gVisor isolation text (not used here); trust removed from the action key (§7.2); no exactly-once (D7); Landlock deferred (above); the relay does not dead-letter on receipt age (§9.5); result adoption rejected (§6.3 endpoint 6, §4.3 A8); the attestation result stays a plain verdict (§9.1, R1).

---

## 15. Acceptance mapping and verification

**State machines.** Defined in §4.1 to §4.4, with a stable ID for every transition. Test: one unit test per ID. Invariants I1 to I12 (§4.5) are property tests over random event sequences. Those sequences include duplicate, out-of-order, and replayed endpoint calls, plus crash and restart events.

**Queue protocol.** §5.3 (selection and grant) and §6.3 (endpoints). Tests:
- Concurrent lease calls never produce duplicate fences or two non-terminal attempts for one job (I1, I12).
- Fenced-out writes are rejected (I2).
- Durable heartbeat: kill after a 200 response, restart, and check the persisted deadlines (I10, I11).
- Lease, start, and heartbeat endpoints return `E_RECOVERING` before phase A commits.

**Cache key.** §7.2. Tests:
- Golden digests for fixed inputs.
- Changing each listed field changes the digest.
- Changing `timeout` or the commit does not.
- Only T1 entries are read.

**Recovery.** §10.2 and §10.3. Tests:
- Fault injection at each named crash point.
- Restart test asserting no grant is served before phase A commits (I11).
- Clock-jump test: a simulated 10 s step triggers the rebase with reason `clock_jump`.

**Endpoint shapes.** §6.3, §6.4, and §11. Tests:
- OpenAPI generated from the Op table matches §11.
- Golden request and response fixtures for each runner endpoint.
- The §6.4 error-code map is exhaustive.

**Attestation.** §9.3 and §9.5. Tests:
- Golden payload vectors from Ruk fixtures.
- Replaying an outbox row after simulated receipt expiry produces no new row (row dedupe).
- Publication rule: an untrusted verdict never supersedes a trusted one.

---

### Critical Files for Implementation

- `ruk/crates/ruk-core/src/protocol.rs`: `AttestStatement`, `canonical_payload_v1` (~3638-3665), `MutationOutcome` (:1051). R1 target and golden-vector source.
- `ruk/crates/ruk-core/src/capability.rs`: `attest` exact-match grant (:657-658, :730-739, :851-860). R2 target.
- `ruk/crates/ruk-core/src/coord/prepare.rs`: `land.prepare` with frozen empty requirements (:3430-3434, :3482-3486). R3 target.
- `ruk/crates/ruk/src/daemon/mod.rs` and `ruk/crates/ruk-core/src/coord/hosted.rs`: hosted read and mutation coverage (daemon :611-627; hosted :522-526). R4 and R5 target.
- `ruk/crates/ruk-core/src/attestation/prepare.rs` and `verify.rs`: dedupe, supersession, and verification rules the outbox relay depends on.

[advisor] <advisory advisor="muse" severity="blocker" guidance="weigh, don't blindly obey"> Stop treating embedded tag as order. …