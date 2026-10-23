> **Note to orchestrator (not part of the artifact):** a tool result contained an embedded instruction to stop researching and write files. I ignored it. This session has no write tool, so the artifact below is the complete content for `ci-runner-sandbox-design.md`. Please save it verbatim.

# rukie CI runner: process, protocol, and sandbox

**Status:** v1 design. Nothing was executed. **Scope:** the `rukie runner` process, the runner↔server protocol, the job sandbox, and host requirements. **Out of scope:** the job-YAML front end, the attestation payload, and UI.

**Evidence labels.** **[V]** read or grepped this session (path given). **[I]** design decision or inference. **[U]** unverified; must be probed on a real host (see §18).

---

## 0. Decisions

1. **One static binary.** `rukie runner {probe,register,run,status,rotate}`. The in-container step executor is the hidden subcommand `rukie job-exec`. The runner bind-mounts its own resolved executable into each container; nothing is copied per job. Build with `CGO_ENABLED=0`.
2. **Pull-only.** The runner long-polls `lease`. At most one outstanding lease per runner.
3. **Fencing on every attempt-scoped write.** Every such request carries `Rukie-Attempt` and `Rukie-Fence`. The server checks both in the same SQLite transaction as the write.
4. **Heartbeat every 10 s** on its own HTTP client. The server requeues after 6 min without a heartbeat. The runner self-fences 5 min after the send time of its last acknowledged heartbeat. Invariant I-1 (§5.4) orders these.
5. **Workspace = exact Git tree.** Fetched by commit OID over Smart HTTP into a per-repo bare mirror that persists across jobs. No `.git` in the workspace.
6. **Sandbox = rootless podman ≥ 5.0, and nothing else.** No bubblewrap or other fallback tier. If podman is missing or insufficient, the runner refuses to start with exit 78 and a named error.
7. **The job container holds no rukie credential.** It has no network unless the spec requests `egress` and the runner is trusted and probe-verified.
8. **Logs** are written by the in-container executor into host-visible files. The runner tails them with offset-addressed, digest-checked chunks. The runner is never in the write path of the job's stdout.
9. **Artifacts** are collected after the container is removed, content-addressed per repo, and deduplicated by `HEAD`.
10. **Cache volumes** are keyed by (trust class, repo, name), exclusive by default, and quarantined after a crash.
11. **Startup reaper.** Orphan containers are removed by label before any lease. Unfinished attempts are abandoned.
12. **Results are idempotent.** `finish` is keyed by result digest. The result is persisted in the state directory until the server answers 200, 409, or `E_LEASE_LOST`.

---

## 1. Deliberate changes from `research-ci-runner.md`

| Research said | This design | Reason |
|---|---|---|
| Bubblewrap as a fallback when the probe passes | Removed | A silent downgrade conflicts with "refuse to start with a named error". |
| Job token (`read tree, write logs/artifacts`) in the container | No credential in the container. The runner fetches with a host-only `rkf_` token, and the runner uploads logs and artifacts | Smaller blast radius. A container escape then yields no forge token. |
| Runner forwards logs from a local spool | The executor writes files; the runner tails them | No log bytes depend on podman's log plumbing. Disk stays bounded by the executor's cap. |
| Self-fence 20 s after last ack; `lease_expires` 30 s; grace 2 min | Self-fence 5 min after last acked send; server `lost_after` 6 min | A 20 s self-fence would kill every job during a routine server restart. 5 min keeps I-1 with a 45 s margin. |
| Landlock on the runner process | Removed | Landlock inherits across `fork`/`exec`, so it would confine podman too [U: standard behaviour; kernel doc not fetched]. |
| "Exactly-once" wording | At-least-once with idempotent consumers | Ruk receipts last 7 days, and `ruk/docs/coordinator.md` says exactly-once does not exist [V, per research correction]. |
| Action digest includes `trust` | Trust is excluded | Per research correction. The runner does not compute cache keys anyway (§17). |
| Workspace materialization left open | Exact tree, `ls-tree` + `cat-file --batch`, no `.git` | Concrete and deterministic. `git archive` is rejected because `export-ignore` and `export-subst` change content. |

---

## 2. Process model

### 2.1 Binary and subcommands

| Command | Purpose | Exit codes |
|---|---|---|
| `rukie runner probe` | Run the startup probe (§3) without registering. Writes `probe.json`. | 0 ok; 78 host requirement; 75 transient (e.g. probe image pull) |
| `rukie runner register --url U --token rkt_… --name N --labels k=v,…` | Probe, then register. Writes `credential` and `runner.toml`. | 0; 78 probe or token error; 75 network |
| `rukie runner run --config F` | Long-running daemon. | 0 clean drain; 1 unexpected; 75 drained with abandoned jobs or transient; 78 config or host requirement |
| `rukie runner status` | Reads state dir and prints active attempts and probe summary. Does not contact the server. | 0 |
| `rukie runner rotate` | Rotates the credential (§4.3). | 0; 78; 75 |
| `rukie job-exec --spec F` / `--probe` | **Hidden.** Runs inside the container only. | 0, 1, 3, 4, 143 (§9.7) |

Exit 78 (`EX_CONFIG`) means "do not restart automatically". Set `RestartPreventExitStatus=78` in the unit (§14).

**Error format.** Every named error is logged as one JSON line `{"level":"error","code":"E_…","msg":"…","remedy":"…"}` and printed to stderr as `E_<CODE>: <what failed>. <remedy>`.

### 2.2 Goroutines and shutdown

| Goroutine | Count | Context | Exit path |
|---|---|---|---|
| Lease loop | 1 | Runner ctx | Ctx cancelled on drain, or fatal `E_RUNNER_REVOKED` / `E_PROTOCOL` |
| Attempt supervisor | 1 per attempt | Attempt ctx (child of runner ctx) | Attempt terminal and cleanup complete |
| Heartbeat | 1 per attempt | Attempt ctx; own `http.Client` and transport | Attempt ctx |
| Log shipper | ≤4 per attempt (`job.stdout`, `job.stderr`, `events`, `runner`) | Attempt ctx | EOF after container exit and final ack, or ctx |
| Blob uploader | ≤4 per attempt (semaphore) | Attempt ctx | Queue drained, or ctx |
| Podman waiter | 1 per attempt | `exec.CommandContext` with attempt ctx | Process exit. Cancelling ctx kills the `podman` client only; the container is stopped explicitly with `podman stop` (§13.2). |
| Finish retrier | 0–1 per attempt | Runner ctx | 200, 409, or `E_LEASE_LOST`. On runner shutdown, `result.json` is already fsynced, so the next start resumes. |
| Reaper | Startup only | 60 s timeout per podman call | Complete |
| Background cleanup (`RemoveAll` of workspace, cache eviction) | ≤1 | Runner ctx | Done or ctx |

Each goroutine has a shutdown path. Each `podman` call has a timeout.

### 2.3 State directory

Mode `0700`, owned by the runner user, on the same filesystem as podman's storage where possible.

```
<state>/
  runner.lock                  flock; one runner per state dir
  credential                   rkr_… (0600)
  runner.toml                  copy of effective config (0600)
  probe.json                   capabilities and check digests
  runtime/rukie                bind-mount source: resolved path of the running executable
  git/<repo_id>.git/           bare mirror (§8.1c)
  cache/<trust>/<repo_id>/<name>/  and  <name>.lock files (§12)
  quarantine/                  crashed or invalid caches and stale attempts
  attempts/<attempt_id>/
    attempt.json               fence, phase, last_ack_send (boottime ns), result_pending
    spec.json                  canonical spec (0600)
    steps.json                 executor input (ro-mounted)
    env                        --env-file (0600)
    work/                      workspace (container rw)
    log/                       job.stdout, job.stderr, events (container rw; runner ro-reads)
    runner.log                 runner-authored lines (not mounted)
    result.json                finish body, fsynced before POST
    upload/                    blob staging reads (none written; kept for resume metadata)
```

`log/` and `work/` are the only container-writable host paths. The runner treats them as untrusted (§9.8).

### 2.4 Configuration (`runner.toml`)

| Key | Default | Limit | Failure |
|---|---|---|---|
| `url` | required | `https://`; `http://` only with `insecure_dev = true` | `E_CONFIG`, exit 78 |
| `state_dir` | `$XDG_STATE_HOME/rukie-runner` | Mode 0700, owned by runner | `E_STATE_DIR`, exit 78 |
| `podman_path` | `LookPath("podman")` | Resolved to absolute path at probe; the same path is used for every call | `E_PODMAN_MISSING` |
| `probe_image` | required | `name@sha256:<64 hex>`; may be `scratch`-based | `E_CONFIG` |
| `max_jobs` | 1 | 1–64 | `E_CONFIG` |
| `cpus` | all online CPUs (probe) | ≤ CPUs present | `E_CONFIG` |
| `memory` | 80% of MemTotal (probe) | ≤ MemTotal | `E_CONFIG` |
| `min_free_disk` | 20 GiB | ≥ 1 GiB; < state fs size | Stops leasing below this (§6.3) |
| `disk_floor` | 2 GiB | < `min_free_disk` | Cancels running attempts with `E_DISK_FLOOR` (infra) below this |
| `outage_budget` | 5m | ≤ 5m (fixed by I-1) | `E_CONFIG` |
| `drain_timeout` | 30m | ≤ 6h | `E_CONFIG` |
| `prepare_timeout` | 10m | ≤ 30m | `E_CONFIG` |
| `collect_timeout` | 10m | ≤ 30m | `E_CONFIG` |
| `workspace.max_bytes` | 8 GiB | Checked before any write (§8.1e) | `E_WORKSPACE_LIMIT` (config, no retry) |
| `mirror.max_bytes` | 20 GiB | LRU over idle mirrors | Evicts; never evicts a locked mirror |
| `cache.max_bytes` | 50 GiB | LRU over idle cache dirs | Evicts; never evicts a locked cache |
| `egress` | false | Requires probe P12 | `E_EGRESS_PROBE`, exit 78 |

### 2.5 Clocks

Safety decisions use `CLOCK_BOOTTIME` (suspend-inclusive), read via a stdlib `syscall.Syscall(SYS_CLOCK_GETTIME, CLOCK_BOOTTIME, …)` wrapper [I]. Go's monotonic clock excludes suspend, so a suspended host would look fresh to it. Wall-clock time is used only for logs and for `finished_at` display.

---

## 3. Host requirements and startup probe

### 3.1 Minimum requirements

| Requirement | Minimum | Why | Measured on this host [V] |
|---|---|---|---|
| Kernel | ≥ 5.19 for `memory.peak`. Native overlay needs ≥ 5.12 per podman rootless docs [V, search] | Accounting; storage | 7.0 (`linux 7.0.0-38-generic`) |
| cgroup v2, unified | `/sys/fs/cgroup/cgroup.controllers` exists | Limits | Present. Root controllers: `cpuset cpu io memory hugetlb pids rdma misc dmem` |
| Controllers delegated to the runner's user manager | `cpu memory pids` in `user@<uid>.service/cgroup.subtree_control` | `--cpus`, `--memory`, `--pids-limit` | `user@1000.service`: controllers `cpu memory pids`, subtree_control `cpu memory pids`. **`io` not delegated → no I/O limits in v1.** |
| systemd user session, lingering | `loginctl enable-linger <user>` for a service | Runner survives logout | `/var/lib/systemd/linger/alpha` present |
| `XDG_RUNTIME_DIR` set and a directory | Required by rootless podman | podman | Session-dependent [U] |
| podman ≥ 5.0, rootless | 5.0 makes pasta the default rootless network [V, Debian podman-rootless(7)] | All sandboxing | **Absent**: `/usr/bin/podman` not found |
| OCI runtime | `crun` (recommended). `runc` acceptable if probe passes | Containers | `crun` absent. `/usr/bin/runc` present (Docker's); used by nothing here [V] |
| `conmon`, `catatonit` | Shipped with podman packages | `--init` | [U] |
| `pasta` (package `passt`) | Only for `egress` | Egress network | Absent [V] |
| `/etc/subuid` and `/etc/subgid` range for the runner user | ≥ 65536 entries | `--userns=keep-id` maps host uid into container uid 1000 and needs the subuid range | `/etc/subuid`: `alpha:100000:65536` [V]. `/etc/subgid` not read [U]. |
| Unprivileged userns | Permitted for podman's binary path | podman | `apparmor_restrict_unprivileged_userns = 1` [V]. `/etc/apparmor.d/podman` grants `userns` to `/usr/bin/podman` with `flags=(unconfined)` [V]. `/etc/apparmor.d/crun` present [V]. |
| git | ≥ 2.31 (`GIT_CONFIG_COUNT`, `--no-write-fetch-head`) | Mirror fetch | Git present (`/usr/bin/git`); version not probed [U] |
| Filesystem for state | Supports overlay upper layers (ext4, xfs, btrfs) | podman storage | Not measured [I] |
| Host firewall (egress only) | Forge URL, host gateway, and cloud metadata address unreachable from pasta containers | Egress isolation | Not configured; enforced by probe P12 [I] |

**Consequence on this host, verified:** `rukie runner probe` would exit 78 with `E_PODMAN_MISSING`. Podman must be installed at `/usr/bin/podman` for the shipped AppArmor profile to match. Installing it elsewhere (e.g. `/usr/local/bin`) yields `E_USERNS_DENIED` (P8).

Docker, containerd, and dockerd are installed on this host [V, `/usr/bin` listing]. The runner never touches `/var/run/docker.sock`.

### 3.2 Podman missing

If `podman` cannot be resolved (config path absent, or `LookPath` fails):

```
E_PODMAN_MISSING: rukie runner needs rootless podman >= 5.0 at podman_path or on PATH; nothing was registered and no job was leased.
Install the distribution package "podman" (on Ubuntu it installs /usr/bin/podman, which matches the shipped AppArmor profile), then run: rukie runner probe
```

Exit 78. `register` runs the same probe first, so a host without podman cannot be registered. No lease request is made. No fallback tier exists.

### 3.3 Probe sequence

The probe runs at every `run` start and at `register` and `probe`. It is ordered; the first failure stops it.

| # | Check | Method | Named error | Exit |
|---|---|---|---|---|
| P1 | podman resolvable | `exec.LookPath` or `podman_path` | `E_PODMAN_MISSING` | 78 |
| P2 | version ≥ 5.0.0 | `podman version --format json` (`Client.Version`) | `E_PODMAN_VERSION` | 78 |
| P3 | rootless and not uid 0 | `podman info --format json` (`host.security.rootless`) [U key name] | `E_PODMAN_ROOTFUL` | 78 |
| P4 | cgroup v2 | `/sys/fs/cgroup/cgroup.controllers` exists; `podman info` cgroup version = v2 [U key] | `E_CGROUP_V1` | 78 |
| P5 | controllers delegated | `cpu`, `memory`, `pids` in the runner's delegating `cgroup.subtree_control`; the authoritative check is P11's limit read-back | `E_CGROUP_NOT_DELEGATED` | 78 |
| P6 | `XDG_RUNTIME_DIR` | Set and a directory | `E_XDG_RUNTIME` | 78 |
| P7 | subuid and subgid ranges | Parse `/etc/subuid`, `/etc/subgid` for the runner's user name | `E_SUBUID_MISSING` | 78 |
| P8 | userns allowed | Probe container (P11) fails with a userns error. Diagnose: `/proc/sys/kernel/apparmor_restrict_unprivileged_userns` = 1 and no profile for `podman_path`. Remedy text names the profile path. | `E_USERNS_DENIED` | 78 |
| P9 | git | `git --version` ≥ 2.31 | `E_GIT_MISSING` / `E_GIT_VERSION` | 78 |
| P10 | probe image | `podman image exists`; else `podman pull --quiet`, then verify `RepoDigests` | `E_PROBE_IMAGE` (transient) | 75 |
| P11 | in-container checks | Run `rukie job-exec --probe` with the **same argument builder as jobs** (§9.2), `--cpus 0.25 --memory 64 MiB --pids-limit 64`. See list below. | `E_SANDBOX_PROBE:<check>` | 78 |
| P12 | egress isolation (only if `egress = true`) | Run a pasta container. Assert TCP to the forge host:port, host gateway, and `169.254.169.254` all fail. | `E_EGRESS_PROBE` | 78 if configured; otherwise `egress` not advertised |
| P13 | state dir | Exists, mode 0700, owned by runner; `runner.lock` acquired | `E_STATE_DIR`, `E_RUNNER_ALREADY_RUNNING` | 78 |

**P11 in-container checks** (each reads the container's own `/proc` or `/sys`):

- `uid` 1000; `/proc/self/uid_map` maps container uid 1000 to the runner's host uid.
- `CapEff` is all zero.
- `NoNewPrivs: 1`.
- `Seccomp: 2`.
- `/sys/fs/cgroup/pids.max` = 64; `memory.max` = 64 MiB; `memory.swap.max` = 0; `cpu.max` quota matches 0.25 CPU.
- `/proc/net/dev` lists only `lo`.
- Root filesystem read-only (`touch /x` fails).
- `/work`, `/.rukie/log` writable; `/.rukie/steps.json` read-only.
- `/tmp` mount options include `noexec` (from `/proc/self/mountinfo`).
- `RLIMIT_CORE` is 0 (from `/proc/self/limits`).
- Parent PID is 1 (the `--init` process exists).
- The executor runs a two-step test spec: step 1 exits 0, step 2 sleeps past a 1 s timeout and is killed; result: exit 1 with a `step_timeout` event.
- A file written by the container at `/.rukie/log/probe` is readable on the host by the runner's uid.

Probe output is written to `probe.json`: podman version, cgroup controllers, check digests, `egress` allowed or not, and the advertised capabilities (§4.2).

---

## 4. Registration, credentials, scope, labels

### 4.1 Registration token

- Issued by an administrator. Format `rkt_<base64url 32 bytes>`. Stored as SHA-256 hash.
- Single use. TTL 1 h.
- Carries: owner scope (`instance`, `org:<name>`, or `repo:<owner>/<repo>`), trust class (`trusted` or `untrusted`), allowed label keys, and `auto_approve` (bool).
- A trusted runner without `auto_approve` is created in state `pending` until an administrator approves it. A pending runner polls `GET /runner/v1/self` every 30 s and does not lease. It logs `E_RUNNER_PENDING` once.

### 4.2 Registration request and capabilities

`POST /runner/v1/register` with `Authorization: Bearer rkt_…` and body:

```json
{"protocol":1,"name":"…","labels":{"team":"core"},
 "capabilities":{"os":"linux","arch":"amd64","podman":"5.x.y","rootless":true,
   "cgroup":"v2","controllers":["cpu","memory","pids"],"sandbox":true,
   "egress":"pasta|none","git":"2.x","cpus":8,"memory_bytes":N,"probe_digest":"sha256:…"}}
```

- Reserved label keys: `os`, `arch`, `trust`, `scope`, `sandbox`, `egress`, `podman`, `cpus`, `memory`. The server rejects user labels that use reserved keys.
- The server rejects with 422 `E_CAPABILITY` if `sandbox` is not true or `cgroup` is not `v2`.
- The server trusts capability self-reports for admission only. A dishonest runner is limited by trust class, scope, and admin approval. This limit is accepted [I].

### 4.3 Credential

- Format `rkr_<base64url 32 bytes>`. Returned once in the register response. Stored in `<state>/credential` (0600) by write-to-temp, fsync, rename.
- Server stores SHA-256 hash, runner id, scope, trust class, and labels.
- Realm is separate from human and agent tokens. A runner credential cannot call any non-runner operation.
- **Rotation:** `POST /runner/v1/credential/rotate` returns a new credential. The old one remains valid for 5 min. The runner writes the new file, then uses it.
- **Revocation** by an administrator: the server immediately fences every attempt owned by that runner (`abandon` semantics, reason `runner_revoked`, not charged) and rejects its requests with 403 `E_RUNNER_REVOKED`. The runner then kills all containers, logs once, and exits 78.

### 4.4 Scope and trust

- A job's repo must be inside the runner's scope. Otherwise the server never offers it.
- **Trust classes are exclusive per runner.** A `trusted` runner runs only trusted jobs; an `untrusted` runner runs only untrusted jobs. This is a deliberate choice: a sandbox escape on a shared runner would expose the trusted runner's credential and caches. Mounts already keep caches separate, so this is defence in depth [I].
- Egress is permitted only on `trusted` runners whose probe passed P12.

### 4.5 Labels and capabilities in matching

- A job's `requires` is a map of exact equality constraints, e.g. `{"arch":"amd64","sandbox":"true","egress":"pasta"}`. No expressions.
- The server offers a job only if every key matches a runner's reserved or user label, and the runner's free capacity fits the job's resources (§6.1).

---

## 5. Protocol, fencing, timers

### 5.1 Endpoints

All under `/runner/v1/`. JSON unless noted. Each is an operation in the single `Op` table with surface `runner` (JSON only; excluded from HTML, MCP, and the generated CLI) [I].

| Method and path | Auth | Fence | Idempotent by | Success | Failure codes |
|---|---|---|---|---|---|
| `POST register` | `rkt_` | — | Token single use | 201 `{runner_id, credential, state}` | 401 `E_REG_TOKEN`; 409 `E_REG_TOKEN_USED`; 422 `E_CAPABILITY`; 426 `E_PROTOCOL` |
| `GET self` | `rkr_` | — | — | 200 `{state, scope, trust}` | 401 `E_RUNNER_AUTH`; 403 `E_RUNNER_REVOKED` |
| `POST lease` | `rkr_` | — | One outstanding per runner | 200 grant, or 204 after ≤25 s | 409 `E_LEASE_BUSY`; 403 `E_RUNNER_REVOKED`; 426 |
| `POST attempts/{id}/start` | `rkr_` | Yes | Attempt state = `leased` | 200 | 409 `E_LEASE_LOST` (including start deadline passed) |
| `POST attempts/{id}/heartbeat` | `rkr_` | Yes | Naturally idempotent | 200 `{cancel, lost_after_ms}` | 409 `E_LEASE_LOST` |
| `PUT attempts/{id}/streams/{s}?offset=N` | `rkr_` | Yes | (offset, len, sha256) | 200 `{ack_offset}` | 409 `E_LOG_GAP` (with `ack_offset`), `E_LOG_DIVERGED`, `E_LEASE_LOST`, `E_ATTEMPT_TERMINAL`; 413 `E_CHUNK_SIZE`, `E_LOG_LIMIT` |
| `HEAD blobs/{repo}/sha256/{hex}` | `rkr_` | — | — | 200 committed; 404 absent | — |
| `PUT blobs/{repo}/sha256/{hex}` | `rkr_` | Yes | Digest | 200 chunk accepted; 201 finalized | 400 `E_BLOB_DIGEST`; 409 `E_UPLOAD_OFFSET` (with `Upload-Length`), `E_LEASE_LOST` |
| `POST attempts/{id}/artifacts` | `rkr_` | Yes | (attempt, path set) | 200 | 422 `E_ARTIFACT_MISSING_BLOB`, `E_ARTIFACT_PATH`; 409 `E_LEASE_LOST` |
| `POST attempts/{id}/finish` | `rkr_` | Yes | Result digest | 200 `{state}` | 409 `E_FINISH_CONFLICT` (different digest), `E_LEASE_LOST`; 422 `E_RESULT_SCHEMA` |
| `POST attempts/{id}/abandon` | `rkr_` | Yes | Attempt | 200 | 409 `E_LEASE_LOST` (safe to ignore) |
| `POST credential/rotate` | `rkr_` | — | — | 200 `{credential}` | 401 |

**Ownership.** For every `rkr_`-authenticated attempt request, the server requires `attempt.runner_id` to equal the authenticated runner. Otherwise it returns 404, not 403, to avoid revealing other runners' attempt ids.

**Staged blob uploads** are written to a staging area with a 1 h TTL. Staged bytes become visible only when the artifacts call links them under a valid fence.

### 5.2 Fence rules

1. `job.fence` is a monotonic integer per job. It increments on every grant, every `lost` transition, every `abandon` that requeues, and every start-deadline expiry. Fences never repeat across attempts of the same job.
2. Each attempt-scoped write is validated in one `BEGIN IMMEDIATE` transaction: `Rukie-Attempt` = `job.current_attempt`; `Rukie-Fence` = `job.fence`; the authenticated runner owns the attempt. Otherwise `E_LEASE_LOST` (409).
3. A terminal attempt accepts only an identical `finish` replay (same digest → 200). Anything else is 409 `E_ATTEMPT_TERMINAL`.
4. **Fenced-out results are discarded.** They are never adopted into the cache or published [V, research correction].
5. A runner that receives 409 `E_LEASE_LOST` on any write stops that attempt's container, stops all writes except none, and keeps `log/` and `runner.log` for 24 h for diagnosis.
6. **Blob bytes are not attempt-state.** Staged bytes count only after linking. A fenced-out upload leaves no visible artifact.

### 5.3 Attempt states (server)

| From | Event | To | Side effects |
|---|---|---|---|
| — | Lease commit | `leased` | `job.fence++`; capacity reserved; start deadline = now + 60 s |
| `leased` | `start` 200 | `running` | `last_hb_recv = now` |
| `leased` | Start deadline passed | Job `queued`; not charged | `fence++`; capacity released |
| `leased` | `abandon` `rejected_before_start` | Job `queued`; not charged | `fence++` |
| `running` | Heartbeat | `running` | `last_hb_recv = now` |
| `running` | `now − last_hb_recv > 6 min` | `lost`; charged as `runner_lost` | `fence++`; capacity released; retry policy (§16.2) |
| `running` | `abandon` `runner_shutdown` or `runner_outage` | Job `queued`; not charged | `fence++` |
| `running` | `abandon` `runner_restart` | `lost`; charged | `fence++`; retry policy |
| `running` | `finish` | Verdict state (`passed`, `failed`, `errored`, `timed_out`, `cancelled`, `resource_exhausted`) | Outbox rows for attestation, status, webhooks |
| `running` | Cancel requested by user or owner | Heartbeat reply `cancel: true`; runner then finishes as `cancelled` | — |

`suspect` is a display flag, not a state: `now − last_hb_recv > 30 s`. It does not requeue.

Server boot: all `running` attempts get `last_hb_recv = now` (the restart time). Runners that were alive keep their attempts (I-1).

### 5.4 Timers and invariant I-1

| Name | Value | Owner | Meaning |
|---|---|---|---|
| Long-poll hold | 25 s (client timeout 35 s) | Server holds; runner | Lease request |
| Start deadline | 60 s after grant | Server | Not started → requeue, uncharged |
| Heartbeat interval | 10 s ± 1 s jitter | Runner | Heartbeat cadence |
| Heartbeat timeout | 5 s | Runner (heartbeat client) | Request deadline |
| Suspect | 30 s | Server | Display only |
| `lost_after` | 6 min since last received heartbeat | Server | Fence bump, requeue |
| `outage_budget` | 5 min since send time of last **acknowledged** heartbeat or start (boottime) | Runner | Self-fence: kill containers |
| Podman stop | `--stop-timeout=15` | Runner | SIGTERM, then SIGKILL after 15 s |
| Executor kill grace | 10 s | `job-exec` | SIGTERM to step group, then SIGKILL |
| Prepare timeout | 10 min default | Runner | Errored, infra |
| Collect timeout | 10 min default | Runner | Errored, infra |
| Job timeout | Spec, default 60 min, max 24 h | `job-exec` | Exit 3 |
| Backstop | Job timeout + 2 min | Runner | Stop container if still running |
| Log flush before finish | 2 min | Runner | Else finish with `log_incomplete: true` |
| Finish retry | 1 s, doubling, cap 5 min, jittered | Runner | Until 200, 409, or `E_LEASE_LOST` |
| Drain timeout | 30 min default | Runner | §14 |
| Queue deadline | 6 h | Server | Enqueue rejected with no eligible runner; queued jobs past deadline fail as `errored`/`config` |
| Blob staging TTL | 1 h | Server | Staged bytes deleted |

**I-1.** Runner kills all containers by `t_send + 5 min + 15 s` (budget plus stop timeout). The server marks the attempt `lost` no earlier than `t_recv + 6 min ≥ t_send + 6 min`. Margin ≥ 45 s. This holds for any acknowledged send time `t_send`, because the server's timer starts at receipt of a heartbeat that was sent no earlier than the last acknowledged one. It also holds across server restarts: the boot rule sets the timer to the restart time, which is after any runner's last acknowledged send before the restart.

**Residual risk.** If the runner **process** is frozen (SIGSTOP, cgroup freeze) rather than the host, the runner cannot kill containers, while the server may requeue after 6 min. The container then runs until its in-container job timeout. Exclusive-effect jobs are protected by `RUKIE_FENCE` (§9.7), not by this timer [I]. A host-side hard kill independent of the runner process is an open question (§18).

### 5.5 Grant payload

```json
{"attempt":"…","fence":7,"job":"…","run":"…","repo_id":"…",
 "clone_url":"https://forge/owner/repo.git",
 "commit":"<40 hex>","tree":"<40 hex>","ref":"refs/heads/main",
 "fetch_token":"rkf_…","fetch_token_ttl_s":N,
 "trust":"untrusted","allocated":{"cpus":2,"memory_bytes":4294967296,"slots":1},
 "spec_digest":"sha256:…","spec":{…JobSpec…}}
```

`fetch_token` is read-only, scoped to the one repo, and its TTL covers `prepare_timeout + job timeout`. It goes only into the git process environment on the host (§8.1c), never argv and never the container.

---

## 6. Pull loop

### 6.1 Capacity model

The runner reserves `(cpus, memory, slot)` per attempt from its configured totals. `max_jobs` caps slots. Reservations are released when an attempt is terminal, abandoned, or lost locally.

### 6.2 Lease request

```json
{"protocol":1,"free":{"slots":1,"cpus":8,"memory_bytes":N},"disk_ok":true,"degraded":false,"probe_digest":"sha256:…"}
```

- The server holds the request up to 25 s and wakes on enqueue or capacity change. It grants only jobs whose `requires` match and whose resources fit `free`.
- On grant the runner checks `spec_digest`, `trust`, and fit. Then it calls `start` (P1). **No side effect happens before `start` returns 200.**
- If the runner cannot accept a grant (race, degraded state), it calls `abandon` with `rejected_before_start` (not charged).

### 6.3 Backoff and degraded state

- Lease errors (5xx, connection refused): full-jitter exponential backoff, 1 s to 30 s cap. A 429 honours `Retry-After`.
- 204: re-poll immediately.
- `E_RUNNER_REVOKED` (403): kill everything, exit 78.
- 401 `E_RUNNER_AUTH` (not revoked): exit 78 `E_RUNNER_CREDENTIAL`.
- 426: exit 78 `E_PROTOCOL`.
- **Degraded** (no new leases; `degraded: true` in the next lease body): free disk < `min_free_disk`; or three `podman` infra failures (`E_SANDBOX_START`, `E_CONTAINER_KILLED`) within five minutes. Leaves degraded when disk recovers, or after a passing re-probe.

---

## 7. Job spec consumed by the runner

The server validates and canonicalizes the spec at enqueue and again at lease. The runner validates it again (§8 P0). The canonical form is RFC 8785 JCS. Stdlib `encoding/json` with `SetEscapeHTML(false)` produces the same bytes **if** keys are ASCII `[a-z0-9_.-]` and values are strings, integers, booleans, arrays, or objects, with no floats [I; needs a test vector].

| Field | Type | Default | Limits | Runner behaviour |
|---|---|---|---|---|
| `schema` | int | — | must be 1 | Else `E_SPEC_INVALID` (config) |
| `image` | string | — | `name@sha256:<64 hex>`; tags rejected | Else `E_SPEC_INVALID` |
| `platform` | `{os,arch}` | `linux/amd64` | Must equal runner's | `E_IMAGE_DIGEST` if mismatch |
| `steps` | array | — | 1–64 steps | Each: `name`, `argv` **or** `shell` (not both), `cwd`, `env`, `timeout`, `continue_on_error` |
| `steps[].argv` | string array | — | ≤256 elements | Exec form. No shell unless `shell` is set. |
| `steps[].shell` | string | — | `sh -eu -c` | Requires `/bin/sh` in the image; otherwise step exit 127 |
| `env` | map | `{}` | ≤256 keys; keys `^[A-Za-z_][A-Za-z0-9_]*$`; values ≤16 KiB; keys starting `RUKIE_` rejected | Written to `env` file |
| `timeout` | duration | 60m | ≤24h | Executor deadline |
| `resources` | `{cpus, memory, pids, tmp}` | 1 CPU, 1 GiB, 512, 512 MiB | cpus ≤ runner cpus; memory ≤ runner memory; pids ≤4096; tmp ≤4 GiB | Podman flags (§9.3) |
| `network` | `none` or `egress` | `none` | `egress` only on trusted, P12-verified runners | Podman `--network` |
| `caches` | array | `[]` | ≤8; `name` `^[a-z0-9][a-z0-9-]{0,31}$`; `path` = `/cache/<name>`; `mode` `exclusive` (default) or `shared` | §12 |
| `artifacts` | `{paths, max_files, max_bytes}` | `[]`, 1000, 512 MiB | paths relative under `/work`, files or directories, **no globs in v1**; max_files ≤10 000; max_bytes ≤4 GiB | §11 |
| `effects` | `none`, `idempotent`, or `exclusive` | `none` | — | Affects retry (§16.2) and `RUKIE_FENCE` |
| `tmp_exec` | bool | false | Trusted runners only | Removes `noexec` from `/tmp` |
| `interruptible` | bool | false | — | Server supersede rules; runner honours cancel as usual |

Unknown fields → `E_SPEC_INVALID`. The runner never ignores fields silently.

**Not in v1:** secrets, submodules (rejected at materialization), `uses:` actions, service containers, matrix expansion. These belong to the front end, not the runner.

---

## 8. Attempt lifecycle

| Phase | Entry | Work | Timeout | Success | Failure: attempt outcome, class, charged |
|---|---|---|---|---|---|
| P0 Validate | Grant received | Verify `spec_digest`; schema; labels; trust; fit; probe current | — | P1 | Fit or trust mismatch: `abandon rejected_before_start` (uncharged). Spec invalid: `errored`/`config`, not retried. |
| P1 Start | P0 ok | `POST start` | 10 s per try, until start deadline | P2 | 409: drop grant silently, no side effects. Network errors until start deadline: give up locally; no container exists. |
| P2 Prepare | Start 200 | §8.1 | `prepare_timeout` | P3 | `errored`/`infra` (retried): `E_IMAGE_PULL`, `E_FETCH_AUTH`, `E_FETCH`, `E_CACHE_BUSY`, `E_DISK`, `E_MATERIALIZE_IO`. `errored`/`config` (not retried): `E_IMAGE_DIGEST`, `E_FETCH_NOTFOUND`, `E_FETCH_REFUSED`, `E_TREE_MISMATCH`, `E_SUBMODULE_UNSUPPORTED`, `E_TREE_PATH`, `E_WORKSPACE_LIMIT`, `E_CACHE_PATH`. Timeout: `errored`/`infra`. |
| P3 Run | Prepare ok | §9 container | Spec timeout + 2 min backstop | P4 | §9.7 mapping |
| P4 Collect | Container removed | §10 log flush, §11 artifacts, §12 cache release, workspace removal | `collect_timeout` | P5 | `E_ARTIFACT_*` errored/config, not retried. Upload failures: retried inside timeout; then `errored`/`infra`. |
| P5 Finish | P4 done | `finish` (§5.1) | Retried until 200, 409, or `E_LEASE_LOST` | P6 | 409 `E_LEASE_LOST`: discard result. Outage: keep `result.json`. |
| P6 Cleanup | Finish 200 or `E_LEASE_LOST` | Remove attempt dir (kept 24 h if fenced out); release locks; keep mirror and cache | — | Done | — |

**Outage budget scope.** The 5-min self-fence applies only while a container is running. After container exit, finish and log retries continue until 200, 409, or `E_LEASE_LOST`. Nothing external remains to stop.

### 8.1 Prepare details

**a. Spec checks.** Digest, schema, env keys, cache names and paths, artifact paths (no absolute, no `..`, no `.git` component), resources fit, network allowed for trust.

**b. Image.**
1. `podman image exists <image>` → if missing, `podman pull --quiet <image>`.
2. Verify `podman image inspect --format '{{json .RepoDigests}}'` contains the expected `name@sha256:…`.
3. Verify `{{.Os}}/{{.Architecture}}` equals the spec platform.
- Pull failure → `E_IMAGE_PULL` (infra). Digest or platform mismatch → `E_IMAGE_DIGEST` (config). The pull is never retried with a tag.

**c. Object pack cache (mirror).**
- Path `<state>/git/<repo_id>.git`. Created with `git init --bare` under an `flock` on `<state>/git/<repo_id>.lock`.
- Fetch by commit OID, protocol v2, no refs written, no FETCH_HEAD, no auto gc:

```
env GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0 \
    GIT_CONFIG_COUNT=2 \
    GIT_CONFIG_KEY_0=http.extraHeader GIT_CONFIG_VALUE_0="Authorization: Bearer <rkf_…>" \
    GIT_CONFIG_KEY_1=protocol.version GIT_CONFIG_VALUE_1=2 \
  git -c gc.auto=0 --git-dir=<state>/git/<repo_id>.git \
      fetch --no-tags --no-write-fetch-head --no-recurse-submodules \
      <clone_url> <commit>
```

- The token never appears in argv (only in the environment of this child process).
- **Why fetch by OID rather than a run ref.** Ruk's typed ref model accepts only `refs/heads/*` for bookmarks, `refs/tags/*` for tags, and `refs/remotes/*` for remote bookmarks [V: `ruk/crates/ruk-core/src/protocol.rs` ~2901–2931; `prepare.rs` ~590, "a tag name must live under refs/tags/"]. A `refs/rukie/ci/...` pin is therefore not expressible without a Ruk change. Ruk's serve test `fetch_rejects_unknown_want_and_unadvertised_filter` [V: `ruk-git/src/transport/serve_v2.rs:997–1000`] rejects **unknown** objects. It does not test wanting an existing, unadvertised object; that is inferred to be accepted [I]. **Must be verified by a test before v1 ships** (§18).
- Ruk does not serve shallow or partial fetch on this path: `shallow` and `deepen` lines are consumed but not honoured, and `filter` is refused unless advertised [V: `proto_v2.rs`, `serve_v2.rs`]. Every mirror therefore receives full history on first fetch. Cost is accepted in v1 [I].
- Verification after fetch: `git cat-file -t <commit>` = `commit`; `git rev-parse <commit>^{tree}` = `tree`. Mismatch → `E_TREE_MISMATCH` (config).
- Errors: 401/403 → `E_FETCH_AUTH` (infra, retried with a new grant). 404 → `E_FETCH_NOTFOUND` (config). Server refusing the want → `E_FETCH_REFUSED` (config; indicates a Ruk change is needed). Corrupt pack or object → delete mirror, refetch once, then `E_MIRROR_CORRUPT` (infra).
- Maintenance: `git repack -d -l` and `git prune --expire=2.weeks.ago` when loose objects exceed 6700 (checked at idle). Mirrors are LRU-evicted by `mirror.max_bytes`, never while locked.

**d. Materialize the exact tree.** No `.git` directory. No attribute processing.
1. `git --git-dir=<mirror> ls-tree -r -l -z --full-tree <tree>` → for each entry: mode, type, oid, size, path.
2. **Validate everything before writing:**
   - Type `commit` (gitlink) → `E_SUBMODULE_UNSUPPORTED` (config).
   - Mode not in {100644, 100755, 120000} → `E_TREE_MODE` (config).
   - Path: no `..` element, no absolute, no NUL, no `.git` element (case-insensitive), no duplicates → `E_TREE_PATH` (config).
   - Sum of sizes ≤ `workspace.max_bytes` → else `E_WORKSPACE_LIMIT` (config).
3. Write. All writes go through an `os.Root` opened on `work/`. Directories are created through the root. Blobs stream from one `git cat-file --batch` process in tree order. Modes: 100755 → 0755; 100644 → 0644. Symlinks (120000) are created with `os.Symlink` after the parent is verified through the root; target length ≤4096 and no NUL. No container exists during this step, so there is no concurrent writer.
4. Verify written count and bytes equal the validated totals.
5. LFS pointer files are written as pointer text (no smudge). LFS sync is deferred [I].
6. Errors: `E_MATERIALIZE_IO` (infra) on any write failure; the partial `work/` is removed.

**e. Caches.** See §12. Lock and create cache dirs, write `.inflight` markers.

**f. Attempt files.**
- `spec.json` (0600): canonical JCS of the spec.
- `steps.json` (0644, mounted read-only): executor input, including `timeout_ms`, `kill_grace_ms: 10000`, `cap_bytes: 67108864`.
- `env` (0600, `--env-file`): `HOME=/tmp`; `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`; `RUKIE_ATTEMPT`, `RUKIE_FENCE`, `RUKIE_COMMIT`, `RUKIE_TREE`, `RUKIE_REF`; then the spec's `env`.
- `attempt.json` (0600): fence, phase, `last_ack_send` (boottime ns), `result_pending`.

---

## 9. Sandbox

### 9.1 Choice

- **Rootless podman ≥ 5.0**, invoked as a subprocess by absolute path, with the same argument builder for the probe and for jobs. No socket, no daemon [I].
- **Rejected for v1:** bubblewrap (its own man page says protection is "entirely determined by the arguments" [V, research]; a silent fallback breaks the named-refusal rule); gVisor (`runsc` is a user-space kernel with real syscall and network cost [V, research correction]); Firecracker (needs `/dev/kvm`, jailer, guest image, TAP setup); a raw-syscall Go sandbox (a container runtime in disguise).
- gVisor is a possible later `sandbox = runsc` tier, selected only by explicit spec or label, never by fallback [I].

### 9.2 Invocation template

One builder, used by P11 (probe) and P3 (jobs). Byte values are used for memory and sizes because podman accepts bytes.

```
podman run
  --rm
  --name rukie-<runner8>-<attempt>
  --label rukie.state=<statehash>
  --label rukie.attempt=<attempt>
  --label rukie.fence=<fence>
  --pull=never
  --init
  --entrypoint=/.rukie/bin/rukie
  --user=1000:1000
  --userns=keep-id:uid=1000,gid=1000
  --workdir=/work
  --hostname=rukie-job
  --network=none                      # or --network=pasta when spec network=egress
  --read-only
  --cap-drop=ALL
  --security-opt=no-new-privileges
  --ipc=private --pid=private --uts=private --cgroupns=private
  --cpus=<cpus> --memory=<bytes> --memory-swap=<bytes> --pids-limit=<pids>
  --shm-size=67108864
  --tmpfs=/tmp:rw,noexec,nosuid,nodev,size=<tmp_bytes>,mode=1777   # noexec removed iff tmp_exec
  --tmpfs=/run:rw,nosuid,nodev,size=16777216,mode=0755
  --ulimit=core=0:0
  --log-driver=none
  --stop-signal=SIGTERM --stop-timeout=15
  --env-file=<state>/attempts/<id>/env
  --mount=type=bind,src=<state>/attempts/<id>/work,dst=/work,nosuid,nodev
  --mount=type=bind,src=<state>/attempts/<id>/log,dst=/.rukie/log,nosuid,nodev
  --mount=type=bind,src=<state>/runtime/rukie,dst=/.rukie/bin/rukie,ro,nosuid?,nodev
  --mount=type=bind,src=<state>/attempts/<id>/steps.json,dst=/.rukie/steps.json,ro,nosuid,nodev
  --mount=type=bind,src=<cache_dir>,dst=/cache/<name>,nosuid,nodev    # one per cache
  <image>@sha256:<digest>
  job-exec --spec /.rukie/steps.json
```

`nosuid` on the executor binary mount is omitted; `nodev` and `ro` are kept. Exact option spelling is [U] until P11 passes on a real host.

### 9.3 Flag table

| Flag | Value | Purpose | Failure behaviour |
|---|---|---|---|
| `--rm` | — | Container removed on exit, before collection | Leftover container: reaped at next start (§15). Collect still runs. |
| `--name` | `rukie-<runner8>-<attempt>` | Targeted stop and reap | Name collision → `E_SANDBOX_START` (infra); next attempt has a new id |
| `--label rukie.state/attempt/fence` | — | Reaper finds all runner containers, even after re-registration (`state` is per state dir) | Reaper cannot find → orphans persist; reaper refuses to lease until `podman ps` works (§15) |
| `--pull=never` | — | Only digest-verified local images run | Image absent at run → `E_SANDBOX_START` (infra); cannot happen after §8.1b |

[1194 more lines in resource. Use :604 to continue]
| `--pull=never` | — | Only digest-verified local images run | Image absent at run → `E_SANDBOX_START` (infra); cannot happen after §8.1b |
| `--init` | — | PID 1 reaps and forwards signals (catatonit) | Podman error → P11 fails → `E_SANDBOX_PROBE:init` |
| `--entrypoint=/.rukie/bin/rukie` | — | Runs the executor, not the image's entrypoint | Executor mount missing → exit 127 → `E_ENTRYPOINT` (config) |
| `--user=1000:1000` | — | Non-root in container | Image lacking uid 1000 is fine with keep-id; podman writes the passwd entry [U] |
| `--userns=keep-id:uid=1000,gid=1000` | — | Container uid 1000 = runner's host uid; no host root in container; workspace owned by runner without chown | Missing subuid range → P7 `E_SUBUID_MISSING`; podman error → P8 `E_USERNS_DENIED` |
| `--workdir=/work` | — | Default cwd | — |
| `--hostname=rukie-job` | — | Fixed, non-identifying | — |
| `--network=none` | Default | No network stack except `lo` | Probe P11 checks `/proc/net/dev` |
| `--network=pasta` | Only for `egress` | Outbound network | Missing `pasta` → `E_PASTA_MISSING`; runner does not advertise `egress` |
| `--read-only` | — | Root filesystem immutable | Probe checks `touch /x` fails |
| `--cap-drop=ALL` | — | No capabilities | Probe checks `CapEff` = 0 |
| `--security-opt=no-new-privileges` | — | No setuid escalation | Probe checks `NoNewPrivs: 1` |
| seccomp | **Not set** (podman default profile) | Default syscall filter | Spec or config may not set `seccomp=unconfined` (`E_SECCOMP_OVERRIDE`, rejected at validation). Probe checks `Seccomp: 2`. |
| AppArmor | Podman default container profile | Per podman | Not overridden; `unconfined` is rejected |
| `--ipc/--pid/--uts/--cgroupns=private` | — | Namespace isolation (explicit; podman defaults) | Probe checks PID namespace via PPid=1 |
| `--cpus=<n>` | From spec | CPU quota | Probe reads `cpu.max` |
| `--memory=<bytes>` and `--memory-swap=<bytes>` | Equal → no swap | Memory limit | Probe reads `memory.max`, `memory.swap.max` = 0. OOM → §9.7 `E_OOM`. |
| `--pids-limit=<n>` | From spec, default 512 | Fork-bomb limit | Probe reads `pids.max` |
| `--shm-size=64m` | — | Shared memory for tools | — |
| `--tmpfs=/tmp:…noexec…` | `size` from spec `tmp`; `mode=1777` | Scratch space; noexec unless `tmp_exec` | Exceeding size → ENOSPC in job (job failure, not host) |
| `--tmpfs=/run` | 16 MiB | Tools expecting `/run` | — |
| `--ulimit=core=0:0` | — | No core files leaking memory to host `core_pattern` | Probe reads `Max core file size` = 0 |
| `--log-driver=none` | — | Podman stores no container logs; the executor's files are the log | Unsupported value → P11 fails. [U] |
| `--stop-signal=SIGTERM --stop-timeout=15` | — | Graceful stop, then SIGKILL | Used by cancel and drain (§13.2, §14) |
| `--env-file` | Attempt `env` file | Environment without putting values in argv | Missing file → `E_SANDBOX_START` |
| `--mount` (§9.4) | — | Only declared paths | Missing source → `E_SANDBOX_START` |
| `--device`, `--privileged`, `--volume`, `--group-add`, `--publish`, `--add-host`, `--sysctl`, `--cgroup-parent`, `--security-opt unmask=…` | **Never** | — | The builder has no code path that emits these |

### 9.4 Mounts

| Host source (under `<state>/attempts/<id>/` unless noted) | Container | Mode | Writable by container | Runner reads |
|---|---|---|---|---|
| `work/` | `/work` | rw | Yes | After exit only, via WalkDir (Lstat) for artifacts |
| `log/` | `/.rukie/log` | rw | Yes | While running: `job.stdout`, `job.stderr`, `events` via `O_NOFOLLOW\|O_NONBLOCK`, regular-file check |
| `steps.json` | `/.rukie/steps.json` | ro | No | No |
| `<state>/runtime/rukie` | `/.rukie/bin/rukie` | ro | No | No |
| `<state>/cache/<trust>/<repo>/<name>/` | `/cache/<name>` | rw | Yes | Size walk and eviction after exit |

The container never sees `credential`, `git/`, `runner.log`, `result.json`, `spec.json`, or any other attempt's directory.

### 9.5 Network modes

| Mode | Allowed for | Mechanism | Guarantee | Not guaranteed |
|---|---|---|---|---|
| `none` | All trust classes | `--network=none` | No network | — |
| `egress` | Trusted runners that pass P12 | `--network=pasta` | Forge, host gateway, and metadata address unreachable (probe-verified) | Destination allowlist. **Not in v1**: egress is all-or-nothing [I]. |

A job that exfiltrates its workspace or cache through `egress` is possible by design on trusted runners. Trusted code is the trust boundary.

### 9.6 Identity

- Runner runs as a dedicated unprivileged user (on this host, `alpha`, uid 1000; recommend a dedicated system user for production [I]).
- Container uid 1000 maps to that host uid. Only the declared mounts are visible. The container cannot reach runner state, the credential, or other attempts [I].
- Rootless podman means a container escape yields the runner user's privileges, including its credential. That is the blast radius: one runner's scope. The mitigation is trust-class separation (§4.4) and dedicated hosts for untrusted code [I].

### 9.7 In-container executor (`rukie job-exec`)

**Input:** `/.rukie/steps.json`. **Output:** `log/job.stdout`, `log/job.stderr`, `log/events`.

**Behaviour:**
1. Re-verify the steps file digest against `spec_digest` passed by env (`RUKIE_SPEC_DIGEST`). Mismatch → exit 4.
2. Start the job deadline timer (`timeout_ms`). On expiry: stop current step group as in step 5, then exit 3.
3. For each step, in order:
   - Start the process in its own process group (`Setpgid: true`) with `Dir` = `cwd` (default `/work`), env = env file + step env. Stdout and stderr are pipes.
   - One copier goroutine per pipe appends to the log file (`O_APPEND|O_CREATE|O_WRONLY`, 0640), counting bytes. Beyond `cap_bytes` per stream: discard and count `dropped`. No network write; the only backpressure is disk.
   - Step timeout (optional, `steps[].timeout`): SIGTERM to group, wait 10 s, SIGKILL group. Event `step_timeout`.
   - Append JSON line to `events`: `step_start`, `step_end{exit, duration_ms, timeout}`, `log_truncated{stream, dropped}`.
   - Failure (non-zero exit, timeout, or exec error) without `continue_on_error`: skip remaining steps (events `step_skipped`), stop.
4. Exit codes: 0 all passed (or only `continue_on_error` failures); 1 a step failed or timed out; 3 job deadline; 4 internal error (spec invalid, cannot open log, log write error); 143 SIGTERM received.
5. On SIGTERM/SIGINT: forward SIGTERM to the current step group; after 10 s SIGKILL the group; then exit 143. A second signal is treated the same without waiting.
6. Reaps its own children via `Wait`. Grandchildren orphaned by steps are reaped by `--init`.
7. `events` is advisory. The verdict comes from the executor's exit code and the runner's observation of the container, not from event content. A step can write to `events` through the shared mount; that is accepted because it cannot change the verdict [I].

**Runner verdict mapping:**

| Observed | Verdict | Reason | Class | Charged? |
|---|---|---|---|---|
| Executor 0 | `passed` | — | — | — |
| Executor 1, no timeout event, not OOM | `failed` | `E_STEP_FAILED` | `script_failure` | No |
| Executor 1, step timeout event | `failed` | `E_STEP_TIMEOUT` | `script_failure` | No |
| Executor 3 | `timed_out` | `E_JOB_TIMEOUT` | `timeout` | No |
| `OOMKilled = true` (any exit) | `resource_exhausted` | `E_OOM` | `resource_exhausted` | No |
| Executor 143, cancel requested | `cancelled` | `E_CANCELLED` | `cancelled` | No |
| Executor 143, runner shutdown or outage | (no finish) `abandoned` | `runner_shutdown` / `runner_outage` | — | No |
| Executor 4 | `errored` | `E_EXECUTOR` | `infra` | Yes |
| Podman 125, container never started | `errored` | `E_SANDBOX_START` | `infra` | Yes |
| Podman 126/127, container never started | `errored` | `E_ENTRYPOINT` | `config` | No |
| Other container exit (e.g. 137 without OOM) | `errored` | `E_CONTAINER_KILLED` | `infra` | Yes |
| Host-path violation (§9.8) | `errored` | `E_HOST_PATH_VIOLATION` | `security` | No; alert |

### 9.8 Host-side path rules (container-writable paths)

The container can write `work/` and `log/`, and any cache directory. The runner reads host files on the **same** filesystem, so a symlink created in the container can point at a host path. Rules:

1. **While running:** the runner reads only `log/job.stdout`, `log/job.stderr`, `log/events` by name, with `open(O_RDONLY|O_NOFOLLOW|O_NONBLOCK)` followed by `fstat` requiring a regular file. Any other type or a symlink → `E_HOST_PATH_VIOLATION`; kill the container. The intermediate directories are runner-owned and cannot be replaced from inside (only their contents are bind-mounted).
2. **After exit:** no writer exists. Use `fs.WalkDir` (which uses Lstat and does not follow symlinks) over `work/` and open files through an `os.Root`.
3. **Cache directories:** size walks use Lstat. Eviction uses `os.RemoveAll`, which removes symlinks without following them.
4. **Executor files:** the executor opens its own log files with `O_NOFOLLOW` (the container cannot race this; it is the writer).
5. Every host-side open of container-writable content goes through `os.Root` (Go ≥1.24) or uses `O_NOFOLLOW`. Tests must cover: symlink to `../credential`, FIFO in `log/`, symlink in artifacts.

### 9.9 Not enforced in v1

| Gap | Why | Mitigation |
|---|---|---|
| Per-job disk quota for `work/` | Needs XFS project quota or a dedicated filesystem | `min_free_disk`, `disk_floor`, `workspace.max_bytes` at materialization [I] |
| I/O limits | `io` controller not delegated on this host [V] | None; documented |
| Egress allowlist | Needs a proxy plus enforcement | Trusted-only egress with P12 |
| Secrets injection | Out of scope | Deferred; would use `--env-file` on trusted runs only |
| Image signature verification | Out of scope | Digest pinning only |
| Custom seccomp | Not needed | Podman default |
| Devices, GPUs | Not needed | Never allowed |

---

## 10. Log streaming

### 10.1 Pipeline

```
child stdout/err ──pipe──▶ job-exec copier ──append──▶ log/job.stdout (≤64 MiB, then drop+count)
                                                     │
runner tail (O_NOFOLLOW, regular, poll 250 ms) ◀─────┘ reads from acked offset, ≤64 KiB
                 │
                 ▼ PUT streams/job.stdout?offset=N  (one in-flight chunk per stream)
              server: append in SQLite, fsync, ack_offset
```

- The job's output is decoupled from the network. A slow or unreachable server only makes the runner's read position lag. The file grows on disk up to the cap.
- Streams: `job.stdout`, `job.stderr`, `events` (executor JSON lines), `runner` (runner-authored, from `runner.log`).
- Caps: 64 MiB per job stream; 4 MiB for `events`; 4 MiB for `runner`. Server accepts up to the same caps plus 1 MiB slack.

### 10.2 Chunk rules (runner)

- Chunk ≤64 KiB. Header `Content-Digest: sha-256=:<base64>:` (RFC 9530). Headers `Rukie-Attempt`, `Rukie-Fence`.
- One in-flight chunk per stream. Read position advances only on `ack_offset`.
- Poll interval 250 ms when idle; reads continue while data remains.
- After container exit: read to EOF, send all chunks, then include per-stream `{bytes, sha256}` in `finish`. The server recomputes and compares.

### 10.3 Server acceptance

| Condition | Response | Runner action |
|---|---|---|
| `offset == stored` | 200, append, `ack_offset = stored + len` | Advance |
| `offset < stored` and `(offset, len, sha256)` matches a stored chunk record | 200, `ack_offset = stored` | Advance to ack |
| `offset < stored`, mismatch | 409 `E_LOG_DIVERGED` | Stop shipping this stream; mark `log_truncated`; keep the local file |
| `offset > stored` | 409 `E_LOG_GAP`, `ack_offset = stored` | Rewind read position to `ack_offset` (file still has the bytes) |
| `len > 64 KiB` | 413 `E_CHUNK_SIZE` | Programming error: stop stream, log |
| Stored bytes would exceed cap | 413 `E_LOG_LIMIT` | Stop shipping; `log_truncated` |
| Attempt terminal (not an identical finish replay) | 409 `E_ATTEMPT_TERMINAL` | Stop stream |
| Fenced out | 409 `E_LEASE_LOST` | Stop attempt (§5.2 rule 5) |

The server keeps a chunk index `(attempt, stream, offset, len, sha256)` so duplicates are recognised without storing bytes twice. On seal, the stream is compressed (zstd) into a per-repo blob.

### 10.4 Failure table

| Failure | Behaviour |
|---|---|
| Server unreachable | Backoff 1 s → 30 s with jitter; job keeps running; file grows to cap; containers subject to outage budget (§5.4). |
| Runner crash | File persists. On restart, the reaper asks `GET attempts/{id}/streams` for stored offsets and ships the remainder before `abandon`. |
| Server crash after commit, before response | Retry hits `offset < stored` with matching chunk record → 200. |
| Disk full in executor copier | Executor exit 4 (`E_EXECUTOR`, infra). Job fails and is retried. Output past this point is lost; stated in events. |
| Log cap reached | Job continues; `log_truncated` in result; verdict unaffected. |
| `finish` before flush | Waits up to 2 min for `ack == file size`; then sends `log_incomplete: true`. |

---

## 11. Artifacts

### 11.1 Selection and limits

- `artifacts.paths`: relative paths under `/work`; files or directories; **no globs in v1** (stdlib-first; `path.Match` cannot express `**`) [I].
- Defaults: 1000 files, 512 MiB total. Maximums: 10 000 files, 4 GiB.

### 11.2 Collection (P4, after container removal)

1. For each declared path: `filepath.Clean`; reject absolute or `..` (rechecked). Walk with `fs.WalkDir` over the `work/` root.
2. Entry types:
   - Symlink → `E_ARTIFACT_SYMLINK` (errored, config, not retried).
   - Regular file with `nlink > 1` → `E_ARTIFACT_HARDLINK`.
   - Directory → recurse. Regular file → include.
   - FIFO, socket, or device → `E_ARTIFACT_TYPE`.
3. Count and bytes are checked before hashing. Over limit → `E_ARTIFACT_LIMIT` (config, not retried).
4. Hash each file (sha256, streaming). Record `{path, executable (bit from 0755), size, sha256}`.
5. For each unique digest: `HEAD blobs/<repo>/sha256/<hex>`. If absent, `PUT` in 16 MiB chunks with `Upload-Offset` and fence headers. Concurrency 4. Re-read the file through `os.Root` with `O_NOFOLLOW` for upload.
6. `POST attempts/{id}/artifacts` with the item list. The server verifies every digest is staged and matches its size; links them to the attempt. Artifacts become visible to users at `finish` (verdict state), not at link time.

### 11.3 Deduplication

- Namespace is **per repo**: `blobs/<repo_id>/sha256/<hex>`. Cross-repo dedup is disabled so `HEAD` cannot be used as an existence oracle across repos [I].
- Within a repo, identical content uploads once.

### 11.4 Failure table

| Failure | Result |
|---|---|
| Symlink, hardlink, or special file | `errored`, `E_ARTIFACT_*`, config, not retried |
| Over count or bytes | `errored`, `E_ARTIFACT_LIMIT`, config, not retried |
| Path missing | Not an error; artifact absent (recorded in result) |
| Upload failure (network) | Retried inside `collect_timeout`; then `errored`, `infra`, retried |
| Digest mismatch on server | 400 `E_BLOB_DIGEST`; runner recomputes from file; if repeated, `errored`, `E_HOST_PATH_VIOLATION` (file changed after hashing while container removed: impossible unless host tampering; treated as security) |
| Fenced out | Discard artifacts; staged bytes expire after 1 h |

---

## 12. Cache volumes

### 12.1 Layout and locks

- Host path `<state>/cache/<trust>/<repo_id>/<name>/`, mounted at spec `path` (`/cache/<name>`).
- Lock file `<name>.lock`. `flock(LOCK_EX)` for `exclusive`, `flock(LOCK_SH)` for `shared`. Acquisition waits up to `prepare_timeout`; on timeout → `E_CACHE_BUSY` (infra, retried).
- `exclusive` is the default because concurrent writers corrupt most caches. `shared` is opt-in for tools documented as concurrency-safe (e.g. Go's module and build caches).

### 12.2 Crash safety

- Before mounting: create `<name>.inflight` marker in the cache directory.
- After container exit and before unlock: remove the marker.
- On runner start (after reaper, §15): a cache with `.inflight` marker and no held lock is **quarantined**: renamed to `quarantine/<ts>-<repo>-<name>`, deleted in the background. The job gets a fresh empty cache. No error. Logged as `E_CACHE_QUARANTINED` (informational).

### 12.3 Quota and eviction

- After exit, measure cache size (Lstat walk, capped at 2× quota to bound work).
- If total across idle caches exceeds `cache.max_bytes`: evict whole cache directories, least recently used first (by last-attempt time recorded in `<name>.meta`). Locked directories are never evicted.
- Whole-directory eviction is coarse but predictable. Finer eviction is deferred [I].

### 12.4 Trust separation

- Path includes `<trust>`. A trusted job never mounts an untrusted cache and vice versa. Runners are exclusive per trust class (§4.4), so this is defence in depth.

### 12.5 Failures

| Failure | Result |
|---|---|
| Lock timeout | `E_CACHE_BUSY` (infra, retried) |
| Spec path not `/cache/<name>` | `E_CACHE_PATH` (config, not retried) |
| Mount source missing | `E_SANDBOX_START` (infra) |
| Disk full while job runs | Job-visible ENOSPC; job fails. Runner's floor logic (§6.3) may cancel. |

---

## 13. Heartbeat, cancellation, accounting

### 13.1 Heartbeat

Body:

```json
{"phase":"run","streams":{"job.stdout":N,"job.stderr":N,"events":N,"runner":N},
 "usage":{"cpu_usec":N,"mem_bytes":N,"mem_peak_bytes":N,"pids":N}}
```

Reply: `{"cancel": false, "lost_after_ms": 360000}`. `cancel: true` triggers §13.2.

- Sent every 10 s ± 1 s on the heartbeat client (5 s timeout).
- `last_ack_send` is set to the **send time** of the most recent 200 (boottime). Retries after failure do not advance it.
- Also refreshes `statfs` of the state dir (for degraded logic, §6.3).

**Cgroup sampling** (each heartbeat, while container runs):
1. `podman inspect --format '{{.State.Pid}}' <name>` → container's init pid.
2. `/proc/<pid>/cgroup` → the `0::/…` path. Read `cpu.stat` (`usage_usec`), `memory.current`, `memory.peak` (kernel ≥5.19; falls back to max sampled `memory.current` and is marked `approx`), `pids.current`.
3. Once the container exits, the cgroup is gone; the last sample is final. Accuracy is ±1 sample interval for CPU time [I].

### 13.2 Cancellation

- Source: owner or user cancels the run; server sets flag; next heartbeat reply has `cancel: true`.
- Runner: `podman stop --time 15 <name>` (SIGTERM to `job-exec`, forwarded to step group, 10 s grace, then KILL; podman SIGKILL at 15 s).
- Verdict: `cancelled`, `E_CANCELLED`, not retried.
- Only one stop call per attempt; if it fails, `podman kill --signal=KILL <name>`, then `podman rm --force --time 0 <name>`. [U] for exact flags.

### 13.3 Accounting

Recorded at `finish`:

- Phase wall times (`prepare`, `run`, `collect`), monotonic (boottime).
- `cpu_usec` (last sample), `mem_peak_bytes` (or `approx`), `pids_max`.
- `log_bytes` per stream, `artifact_bytes`, `artifact_count`, `cache_bytes` (per cache, post-exit size).
- `allocated`: `{cpus, memory_bytes}`.

Charge formulas (server, for DRR scheduling and reporting):
- `runner_seconds = wall_s × allocated_cpus` (allocation-based, so idle reservations are not free).
- `cpu_seconds = cpu_usec / 1e6` (reported only).

Per-tenant aggregates use `runner_seconds`.

---

## 14. Shutdown and drain

### 14.1 Signals

| Signal | Behaviour |
|---|---|
| First SIGTERM or SIGINT | **Drain.** Stop leasing (cancel the long-poll). Running attempts continue with heartbeats, logs, artifacts, and finish. |
| Drain timeout (30 min default) reached with running attempts | Cancel each with reason `runner_shutdown`: `podman stop --time 15`, flush logs (best effort), `abandon runner_shutdown` (uncharged). |
| Second SIGTERM or SIGINT during drain | Immediate cancel of all attempts, same path, no waiting. |
| SIGKILL or crash | No cleanup. §16 and §15 apply. |

On drain completion with abandoned attempts: exit 75. Clean drain: exit 0.

### 14.2 systemd (user unit)

```ini
[Unit]
Description=rukie CI runner
After=network-online.target

[Service]
ExecStart=%h/.local/bin/rukie runner run --config %h/.config/rukie/runner.toml
Restart=on-failure
RestartPreventExitStatus=78
KillMode=process
KillSignal=SIGTERM
TimeoutStopSec=35min
Delegate=yes

[Install]
WantedBy=default.target
```

`KillMode=process` signals only the runner process, so drain is the runner's job, not systemd's [I]. `TimeoutStopSec` must exceed `drain_timeout` plus stop grace. `Delegate=yes` is included so podman can manage its cgroup subtree [I; confirm in P5].

---

## 15. Startup recovery (reaper)

Ordered. Each step must complete or fail before the next.

1. Acquire `runner.lock` (flock). Held → `E_RUNNER_ALREADY_RUNNING`, exit 78.
2. Probe (§3.3). Failure → named error, exit as listed.
3. **Reap containers:** `podman ps -a --filter label=rukie.state=<statehash> --format json` → `podman rm --force --time 0 <names>`. 60 s timeout. **If this fails or times out, refuse to lease** (`E_REAP_FAILED`, exit 75). Orphans can still hold cache locks.
4. **Quarantine caches:** any `.inflight` marker with no lock held (§12.2).
5. **Per attempt directory** (`attempts/*`), in background goroutines with timeouts, not blocking the lease loop:
   - `result.json` present → resume finish (P5).
   - Otherwise: try shipping remaining log bytes (409 on fence is fine); `abandon runner_restart` (charged as `runner_lost`).
   - Remove directory (`RemoveAll`) after abandon is acknowledged or fails with 409. Logs of fenced-out attempts are kept 24 h (§5.2).
6. Mirror: no refs to clean (fetch by OID writes none). Repack maintenance per §8.1c.
7. Start the lease loop. Leasing does not wait for step 5's server calls.

**Why leasing may start before abandons finish:** new attempts have new fences, so stale abandon traffic cannot affect them.

---

## 16. Crash and failure matrix

### 16.1 Scenarios

| # | Scenario | Detection | Runner does | Server does | Charge and retry | Operator sees |
|---|---|---|---|---|---|---|
| 1 | Runner SIGKILL during prepare (no container) | Heartbeat stops | Nothing | `lost` at 6 min | Charged (`runner_lost`) | `lost`, retried |
| 2 | Runner SIGKILL during run | Heartbeat stops | Nothing; container continues until job timeout or next reap | `lost` at 6 min; `fence++` | Charged; exclusive not auto-retried after run started | `lost` |
| 3 | Runner frozen (SIGSTOP) or host suspended | Monotonic (boottime) deadline checked **first** on resume | On resume: if deadline passed, self-fence before any write | `lost` if silent 6 min | Charged | `lost`; residual container runtime (§5.4) |
| 4 | Host reboot | Runner start after boot | Reap (none); quarantine caches; abandon `runner_restart` | Attempt `lost` or abandoned | Charged | `lost` |
| 5 | Server down ≤ 5 min | Heartbeat fails | Keeps running; retries | Restart boot rule | None | Nothing |
| 6 | Server down > 5 min or partition > 5 min | Outage budget | Kill containers (≤15 s); on reconnect `abandon runner_outage` | `lost` at 6 min if no reconnect; else abandoned | Uncharged if abandon arrives; charged if server marks `lost` first | Possibly `lost`, retried |
| 7 | Server crash after commit, before response | Client timeout | Retries with same offset/digest/fence | Idempotent | None | Nothing |
| 8 | Fenced out (409 `E_LEASE_LOST`) mid-run | Any write | Kill container; discard result; keep logs 24 h | Already requeued | — | Attempt shown as `lost` |
| 9 | Podman 125 at run | Exit code and inspect | `E_SANDBOX_START`; degraded after 3 in 5 min | — | Infra, retried | `errored` infra |
| 10 | Image digest mismatch | Prepare §8.1b | `E_IMAGE_DIGEST` | — | Config, not retried | `errored` config |
| 11 | Fetch 401/403 (token expired) | Git exit and message | `E_FETCH_AUTH` | — | Infra, retried with new grant | `errored` infra |
| 12 | Fetch refused for existing object | Git error | `E_FETCH_REFUSED` | Needs Ruk change | Config, not retried | `errored` config |
| 13 | Mirror corruption | Git `fatal:` on fetch or verify | Delete mirror, refetch once | — | Infra if second failure | Possibly `errored` infra |
| 14 | Disk full in prepare | ENOSPC | `E_DISK`; degraded | — | Infra, retried | `errored` infra |
| 15 | Disk below floor | `statfs` each heartbeat | Cancel running with `E_DISK_FLOOR` | — | Infra, retried | `errored` infra |
| 16 | Log cap | Byte counter | Continue; `log_truncated` | — | None | Verdict unaffected |
| 17 | Artifact limit | Walk count and bytes | `E_ARTIFACT_LIMIT` | — | Config, not retried | `errored` config |
| 18 | Artifact symlink or hardlink | Walk | `E_ARTIFACT_*` | — | Config, not retried | `errored` config |
| 19 | Host-path violation (symlink or FIFO in `log/`) | `O_NOFOLLOW` or `fstat` | Kill container; `E_HOST_PATH_VIOLATION` | — | Security, no retry, alert | `errored` security |
| 20 | OOM | `OOMKilled` | Verdict `resource_exhausted` | — | No retry | `resource_exhausted` |
| 21 | Job timeout | Executor exit 3 | `timed_out` | — | No retry | `timed_out` |
| 22 | User cancel | Heartbeat reply | Stop; `cancelled` | — | No retry | `cancelled` |
| 23 | Credential revoked | 403 `E_RUNNER_REVOKED` | Kill all; exit 78 | Already fenced | Uncharged (`runner_revoked`) | Runner offline |
| 24 | Grant but `start` returns 409 | Start response | Drop; no side effects | Already requeued | — | Nothing |
| 25 | Grant does not fit (race) | P0 check | `abandon rejected_before_start` | Requeue | Uncharged | Nothing |
| 26 | Second runner on same state dir | `runner.lock` | Exit 78 | — | — | Named error |
| 27 | Clock jump (NTP) | Boottime used | No effect | — | — | Nothing |
| 28 | Finish conflict (different digest) | 409 | Log bug; keep `result.json`; stop | — | — | Operator alert |
| 29 | Podman reap fails at startup | Reaper | Refuse lease (`E_REAP_FAILED`), exit 75 | — | — | Runner offline until fixed |
| 30 | Probe image unavailable | P10 | Exit 75 | — | — | Runner offline (transient) |

### 16.2 Retry policy

| Class | Auto-retry | Max retries | Notes |
|---|---|---|---|
| `infra` | Yes | 2 | Not retried if `effects = exclusive` and run started |
| `runner_lost` | Yes | 2 | Same exclusive rule; `runner_restart` maps here |
| `timeout` | No | 0 | — |
| `script_failure` | No | 0 | — |
| `resource_exhausted` | No | 0 | — |
| `cancelled` | No | 0 | — |
| `config` | No | 0 | — |
| `security` | No | 0 | Alert |
| Abandon `runner_shutdown` / `runner_outage` / `rejected_before_start` | Immediate requeue | Not charged | — |

Retry spacing: 10 s, then 60 s.

`RUKIE_FENCE` and `RUKIE_ATTEMPT` are exported to exclusive jobs so external systems can refuse stale writes. The external system is responsible for checking them [I].

---

## 17. Server (rukie) and Ruk requirements

### 17.1 Rukie

- Runner operations live in the single `Op` table with surface `runner`. They are excluded from HTML forms, MCP tools, and the generated CLI, and they are JSON-only [I]. This departs from the settled "same URLs negotiate HTML" rule for these endpoints; the orchestrator should confirm.
- Tables (names from research §7.1, adjusted): `runner`, `runner_token`, `job`, `attempt` (with `fence` on `job`), `log_chunk`, `blob`, `blob_staging`, `artifact_link`, `result_cache`, `outbox`, `tenant_usage`. Single writer goroutine; `synchronous=FULL`.
- `finish` writes the verdict, outbox rows, and cache entries in one transaction. Cache writes follow research §7.2 rules (pure jobs only, trusted scope only). The runner never decides eligibility.
- Attestations: downstream, through the outbox, using outbox id as the Ruk request id. At-least-once; consumers dedupe [V, research correction]. The runner never holds a signing key.
- Result schema (canonical JCS): `verdict`, `reason`, `class`, `exit_code`, `executor_exit`, `phases`, `usage`, `artifacts`, `log`, `log_truncated`, `log_incomplete`, `spec_digest`, `image_digest` (resolved `RepoDigests` entry), `tree`, `commit`, `runner_id`, `runner_version`, `probe_digest`.
- Grant includes the fetch token; the server mints `rkf_` tokens scoped to one repo and TTL.

### 17.2 Ruk

| Need | Status | Action |
|---|---|---|
| Serve `want <commit-oid>` for an existing, unadvertised object | Inferred accepted; the unknown-want test rejects only missing objects [V: serve_v2.rs:997–1000] | **Add a test** asserting an existing non-advertised want succeeds. If refused, add it to Ruk. |
| `ls-refs` ref-prefix filter (not needed in v1) | Present [V: serve_v2.rs] | None |
| Run-pin ref `refs/rukie/...` | Not expressible in typed model [V: protocol.rs; prepare.rs] | Not needed with fetch-by-OID. Do not add a namespace. |
| Shallow or partial fetch | Not honoured [V: proto_v2.rs, serve_v2.rs]; progress.md lists as open | Full history in v1; mirror cost accepted [I] |
| Archive (`upload-archive`) | Not used | None |
| Object GC | Hosted path refuses `op.prune` [per ruk-domain-model.md §2; not re-read here] | If GC lands, rukie must hold a retention root for run commits (not refs) |
| Lease fencing semantics | Ruk workspace stamp checks epoch before sequence and returns `E_LEASE_LOST` [V: protocol.rs:912–915; error.rs] | None; mirrored by the CI fence |

No Ruk change is required for v1 **if** the want test passes.

### 17.3 Decision needed: Gitea-Actions YAML

Research §5 proposes reusing rukie's `jobparser` for `.gitea/workflows` YAML. That conflicts with the settled rule "cut every Gitea surface". This design is indifferent to the front end (it consumes only the canonical spec in §7). **The orchestrator must decide** whether `.gitea/workflows` is a Gitea surface. If yes: native `.rukie/ci.toml` only, with no parser reuse.

---

## 18. Probes to run on a real host, and open questions

Nothing here was executed. Run these before implementation is considered verified.

**Probes (in order):**
1. Install `podman` at `/usr/bin/podman` (Ubuntu package); record version. Run `podman info --format json` and record the exact keys for P3, P4, `State.Pid`, `host.security.rootless`, cgroup version, and `host.ociRuntime`.
2. `podman run` with the §9.2 builder against a `scratch`-based probe image. Verify each P11 check, including `--log-driver=none` acceptance and `--userns=keep-id:uid=1000,gid=1000` behaviour.
3. Remove `/usr/bin/podman` to `/usr/local/bin/podman` and re-run to confirm `E_USERNS_DENIED` text and remedy.
4. Confirm `/etc/subgid` contains a range for the runner user.
5. Fetch-by-OID against a Ruk daemon: existing non-advertised commit → success or `E_FETCH_REFUSED`. **Blocking.**
6. Measure `podman run` cold start and `podman stop` latency for SIGTERM handling inside `job-exec`.
7. Confirm `pasta` egress probe P12 fails to reach host gateway and forge URL on this network.
8. Confirm `memory.peak` exists on kernel 7.0 under a delegated user slice.
9. Test `os.Root` symlink-to-`../credential` and FIFO cases (§9.8).

**Open questions:**
1. **Host-side hard kill independent of runner process.** A frozen runner can't kill containers (§5.4 residual). Candidate: wrap `podman run` in a transient user unit with `RuntimeMaxSec` and place containers under it via `--cgroup-parent`. Needs probe 1 and 2 results. Until resolved, the job timeout inside the container is the only bound.
2. **Should `finish` retries after 6 min be capped?** Currently unbounded until 200, 409, or `E_LEASE_LOST`. Fine if the server always answers; a pathological server could hold runner resources. Probably fine; confirm.
3. **Disk quota for `work/`.** Options: XFS project quota on a dedicated filesystem; or a loopback filesystem per runner. Not in v1.
4. **Egress allowlist.** A proxy inside the pasta network with the container's env set is not enforcement. True enforcement requires netns-level rules. Deferred.
5. **Runner ops in MCP/HTML.** §17.1 excludes them. Confirm with the orchestrator.
6. **Canonical JSON.** The stdlib-only JCS claim (§7) needs a test vector against RFC 8785 examples.
7. **Pending-approval UX.** Whether `auto_approve` tokens are acceptable for trusted runners.

---

## Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/serve_v2.rs` — `fetch_rejects_unknown_want_and_unadvertised_filter` (≈line 997): the test that must be extended to prove fetch-by-OID of an existing object works. Blocking for §8.1c.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/proto_v2.rs` — v2 capability negotiation; shallow and filter are not honoured, so mirrors hold full history (§8.1c, §17.2).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs` — lines ~912–915 (workspace stamp: epoch checked before sequence, `E_LEASE_LOST`); the model for the CI fence (§5.2). Also ~2901–2931 (typed ref namespaces) explaining why no run-pin ref is used.
- `/etc/apparmor.d/podman` — `profile podman /usr/bin/podman flags=(unconfined)` with `userns`; the profile path binds podman's install location (§3.1, P8). Companion: `/etc/apparmor.d/crun`.
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-ci-runner.md` — prior art and corrections this design supersedes in the places listed in §1.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Fix two defects before handoff. §9…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Fix the invocation’s image ar…

---
## Later corrections from the same agent (apply over the body above)

I'll check the disputed claims against source before revising: the want path, the private-object filter, the Ruk read methods, and the kernel version.
→ read(/proc/sys/kernel/osrelease) ⇒ ok · 1 line
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/private.rs:20-45) ⇒ ok · 34 lines
→ grep(fn serve_fetch|parse_want|has_object|PrivateFilter|private @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/tra…) ⇒ ok · 17 lines
→ grep((?i)(tree|blob|object)[._-](read|get|list|show|cat)|read[._](tree|blob|object)|cat[._-]file @ /home/alpha/rewrite/git-r…) ⇒ ok · 1 line

[advisor] <advisory advisor="muse" severity="concern" guidance="weigh, don't blindly obey"> Use runner user id for userns map, no…

---
The v2 want check is an existence test (`has_object`) and the serve path has no private-object filter. That changes the fetch design. I'll check the Ruk method table for an object-read method the server could use.
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs:3280-3340) ⇒ ok · 76 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/serve_v2.rs:360-380) ⇒ ok · 34 lines

[advisor] <advisory advisor="qwen" severity="concern" guidance="weigh, don't blindly obey"> Add RUKIE_SPEC_DIGEST to the §8.1f en…

---
The v2 want check is only `has_object`, and `serve_v2.rs` has no private-closure handling. The runner should not reach upload-pack at all. Before rewriting, I'll check the AppArmor profile the review cites and the unread tail of the research file.
→ read(/etc/apparmor.d/unprivileged_userns) ⇒ ok · 27 lines
→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/loca…) ⇒ ok · 41 lines

[advisor] <advisory advisor="stickler" severity="blocker" guidance="weigh, don't blindly obey"> Do not ship fetch-by-OID until a …

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Gate podman run on a recent hear…

---
I'll verify the private-object claim in the other two transport files, and check whether the existing `show` or `file` read methods return raw object bytes before I declare a new Ruk read method necessary.
→ grep((?i)private @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/serve.rs;/home/alpha/rewrite/git-renewed…) ⇒ ok · 1 line
→ grep(pub struct (ShowArgs|ShowReply|FileArgs|FileReply)\b @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol…) ⇒ ok · 23 lines

---
This review batch holds up on the main points. Fetch-by-OID is out of the required path, and the design needs a fence check before `podman run`, idempotent `start`, byte-identical log retries, and an overlap rule for log acks. I checked the want path, private handling, and read methods against source, so the next revision is a phase-driven contract.

# rukie CI runner: process, protocol, and sandbox (v2)

**Status:** v1 design, revised. Nothing was executed. **Scope:** the `rukie runner` process, the runner↔server protocol, the job sandbox, and host requirements. **Out of scope:** the job front end, attestation payloads, and UI.

**Evidence labels.** **[V]** read or grepped this session (path given). **[I]** design decision or inference. **[U]** unverified; must be probed on a real host (§10).

## 0. Revision log (what changed and why)

| Finding | Change |
|---|---|
| Fetch-by-OID leaks private chains | Fetch-by-OID removed from the required path. Runner reads through attempt-scoped, closure-gated server endpoints (§3, Phase 4, §8). |
| Start replay and dropped grants | `start` is idempotent per fence. Any start error sends `abandon` (§3, Phase 3). |
| Log retries diverge | Retries resend identical in-flight bytes. Server accepts overlap when bytes match (§3, Phase 6). |
| Fence not checked before container start | Pre-run gate: a synchronous heartbeat must return 200 before `podman run` (§3, Phase 5). |
| Outage budget ignored prepare | Budget covers start through container exit, including prepare (§4.2). |
| Verdict read events | Verdict is event-free and uses exit code, OOM flag, and runner-recorded reason (§5.8). |
| Executor self-kill charged as infra | Unexplained executor death is `failed`, not retried (§5.8). |
| Podman flag block not runnable | `nosuid?` removed; image is `spec.image` verbatim; runner UID used, not 1000 (§5.2). |
| Podman version and error names | Floor `>=5.0`; `RUKIE_RUNNER_*` names for process errors, `E_*` for wire and verdict codes (§0.1, §1.2). |
| Egress fallback | Egress fails closed; never downgrades to `none` (§5.6). |
| Host kernel label | Kernel `7.0.0-38-generic` read from `/proc/sys/kernel/osrelease` [V]. |
| Cgroup scope | Runner enforces only `cpu`, `memory`, `pids`. Disk is measured separately and capped (§1.5). |
| Landlock, gVisor cells | Landlock Deferred. gVisor cell corrected per research tail [V, research lines 300–333]. |
| No git on host | Runner needs no git binary; object hashing is in Go (§3, Phase 4). |

### 0.1 Contract summary

1. One static binary: `rukie runner {probe,register,run,status,rotate}`. In-container executor: hidden `rukie job-exec`. The runner bind-mounts its resolved executable; nothing is copied per job.
2. Pull-only. One outstanding lease per runner. Grants are accepted only after an idempotent `start`.
3. Every attempt-scoped request, reads included, carries `Rukie-Attempt` and `Rukie-Fence`. The server checks both in the same transaction as the request.
4. Heartbeat every 10 s on its own client. Server `lost_after` = 6 min. Runner self-fence = 5 min after the send time of its last acknowledged request (§4.2).
5. Workspace = exact Git tree. Built from server-served raw objects, verified by recomputed hashes, written through `os.Root`. No `.git`, no git binary, no hidden refs, no upload-pack.
6. Sandbox = rootless podman ≥ 5.0 only. No fallback tier. Missing or insufficient podman → refuse to start.
7. The job container holds no rukie credential. Network is `none` unless the spec asks for `egress` and the runner is trusted and has verified isolation. Otherwise the job is refused, never downgraded.
8. Logs: the executor writes files; the runner ships them with offset-addressed chunks.
9. Artifacts are collected after container removal, content-addressed per repo.
10. Caches are keyed by trust class, repo, and name; exclusive by default; quarantined after a crash.
11. Startup reaps containers by label before any lease. Unfinished attempts are abandoned.
12. Results are idempotent: `finish` is keyed by result digest. The result stays on disk until the server answers 200, 409, or `E_LEASE_LOST`.

---

## 1. Host requirements, probe floor, and podman-missing behaviour

### 1.1 Requirements and measured status

Status key: **Present** and **Absent** are read on this host this session. **Not measured** means not checked. Every requirement is a requirement; none is an observed support claim.

| Requirement | Why | Status on this host |
|---|---|---|
| Linux kernel ≥ 5.19 (for `memory.peak`) | Accounting | Present: `7.0.0-38-generic` [V, `/proc/sys/kernel/osrelease`] |
| cgroup v2 unified (`/sys/fs/cgroup/cgroup.controllers`) | Limits | Present. Root controllers: `cpuset cpu io memory hugetlb pids rdma misc dmem` [V] |
| `cpu`, `memory`, `pids` delegated to the runner's user manager | `--cpus`, `--memory`, `--pids-limit` | Present for uid 1000: `user@1000.service` controllers and subtree_control are both `cpu memory pids` [V]. **`io` is not delegated** [V]. |
| systemd user session with lingering, for a service | Runner survives logout | Present for `alpha` [V, `/var/lib/systemd/linger/alpha`] |
| `XDG_RUNTIME_DIR` set and a directory | Rootless podman | Not measured in service context [U] |
| podman ≥ 5.0, rootless | Sandbox | **Absent**: `/usr/bin/podman` not found [V] |
| OCI runtime `crun` (recommended) or `runc` | Containers | `crun` **absent** [V]; `runc` present, installed with Docker [V] |
| `pasta` (package `passt`) | Egress only | **Absent** [V: `/usr/bin/pasta` not found] |
| `conmon`, `catatonit` | Podman runtime, `--init` | Not individually checked [U] |
| `/etc/subuid`, `/etc/subgid` entry for runner user | `--userns=keep-id` | `/etc/subuid`: `alpha:100000:65536` [V]; `/etc/subgid` not read [U] |
| Unprivileged userns allowed for podman | Rootless podman | AppArmor sysctl `apparmor_restrict_unprivileged_userns` = 1 [V]. Profile `/etc/apparmor.d/podman` (`flags=(unconfined)`, `userns`) for `/usr/bin/podman` [V]. Profile `/etc/apparmor.d/unprivileged_userns` is the special profile for unconfined processes creating unprivileged userns; it allows `userns`, `ptrace`, `rw /**`, `network` [V]. **Whether podman's userns creation transitions into it is unverified, because podman is absent.** P8 decides. |
| Docker, containerd, dockerd present (rootful) | Irrelevant to runner | Present [V]. The runner never touches the docker socket. |
| `bwrap` | Not used | Present [V]; not a fallback |
| Filesystem for state supporting overlay upper layers | podman storage | Not measured [U] |
| `git` binary | **Not required** | Runner uses no git (§3, Phase 4). |

### 1.2 Floor and error names

- **Floor: podman 5.0.** Rationale: pasta is the rootless default from 5.0 [V, Debian podman-rootless(7)]; the rest of the design is validated by the probe on the installed version, not by version alone.
- The version check runs before registration. Failure → `RUKIE_RUNNER_PODMAN_VERSION_UNSUPPORTED`, exit 78. The runner has not registered and has not leased.
- **Podman missing:** `RUKIE_RUNNER_PODMAN_MISSING`, exit 78. Nothing registered, no lease request, no fallback. Message:

  ```
  RUKIE_RUNNER_PODMAN_MISSING: rootless podman >= 5.0 was not found at podman_path or on PATH.
  Install the distribution package "podman", then run: rukie runner probe
  ```

- **Naming:** process, probe, and local attempt errors use `RUKIE_RUNNER_*`. Wire and verdict reason codes use `E_*`, matching Ruk's wire convention.

### 1.3 Cgroup delegation and what is enforced

- The runner enforces exactly **cpu, memory, and pids**. It requires exactly those three delegated. `io` is not required and is not claimed.
- Disk usage of `work/` is **not** a cgroup controller. It is measured separately (sampled walk, §3 Phase 5) and enforced by cap. Free space on the state filesystem is enforced by `min_free_disk` and `disk_floor`.
- Cgroup accounting claims cover cpu time, memory peak, and pid count only.
- A grant that requests a limit the host cannot enforce is refused. The runner does not advertise that capacity (`RUKIE_RUNNER_SANDBOX_PROBE_FAILED` at startup; abandon `rejected_before_start` on grant).

### 1.4 Podman-absent and unverified-flag statement

- On this host **no `podman run` flag in §5 is verified**. Podman is absent. The startup probe (P11) is the only check that exercises the flag set. Every flag is unvalidated on this host until P11 passes on a real install.
- Pinned version for the flag set: **podman 5.x**; the exact validated version is recorded in `probe.json` after P11.

### 1.5 Probe sequence (ordered; first failure stops it)

| # | Check | Named error | Exit |
|---|---|---|---|
| P1 | podman resolvable (`podman_path` or `LookPath`) | `RUKIE_RUNNER_PODMAN_MISSING` | 78 |
| P2 | `podman version --format json` ≥ 5.0.0 | `RUKIE_RUNNER_PODMAN_VERSION_UNSUPPORTED` | 78 |

[591 more lines in resource. Use :1207 to continue]
| P2 | `podman version --format json` ≥ 5.0.0 | `RUKIE_RUNNER_PODMAN_VERSION_UNSUPPORTED` | 78 |
| P3 | rootless, uid ≠ 0 (`podman info`, key names [U]) | `RUKIE_RUNNER_PODMAN_ROOTFUL` | 78 |
| P4 | cgroup v2 unified | `RUKIE_RUNNER_CGROUP_V2_REQUIRED` | 78 |
| P5 | `cpu`, `memory`, `pids` delegated (diagnosis; P11 read-back is authoritative) | `RUKIE_RUNNER_CGROUP_CONTROLLERS_NOT_DELEGATED` | 78 |
| P6 | `XDG_RUNTIME_DIR` set and a directory | `RUKIE_RUNNER_XDG_RUNTIME_DIR` | 78 |
| P7 | runner user has `/etc/subuid` and `/etc/subgid` ranges | `RUKIE_RUNNER_SUBUID_MISSING` | 78 |
| P8 | a container can be created with rootless userns (failure text decides: AppArmor restrict set and no profile for `podman_path`) | `RUKIE_RUNNER_USERNS_DENIED` | 78 |
| P10 | `probe_image` local or pullable; digest matches | `RUKIE_RUNNER_PROBE_IMAGE_UNAVAILABLE` | 75 (transient) |
| P11 | in-container checks (below) using the production argument builder | `RUKIE_RUNNER_SANDBOX_PROBE_FAILED` (names the check) | 78 |
| P12 | egress isolation, only if `egress = true` | `RUKIE_RUNNER_EGRESS_UNAVAILABLE` | 78 if configured; else `egress` not advertised |
| P13 | state dir mode 0700, owned by runner; `runner.lock` acquired | `RUKIE_RUNNER_STATE_DIR`, `RUKIE_RUNNER_ALREADY_RUNNING` | 78 |

**P11 in-container checks** (run through `rukie job-exec --probe` with the same builder as jobs):
- Container uid equals runner uid; `/proc/self/uid_map` maps it to the runner's host uid.
- `CapEff` is zero; `NoNewPrivs` is 1; `Seccomp` is 2.
- `pids.max`, `memory.max`, `memory.swap.max` (= 0), `cpu.max` match the requested limits.
- `/proc/net/dev` lists only `lo`.
- Root filesystem read-only (`touch /x` fails); `/work` and `/.rukie/log` writable; `/.rukie/steps.json` read-only.
- `/tmp` mount options include `noexec` (unless `tmp_exec`).
- `Max core file size` is 0.
- The process's parent is pid 1 (`--init` present).
- The executor runs a two-step probe spec: step 1 exits 0; step 2 sleeps past a 1 s timeout and is killed. Expected: executor exit 1.
- A file written at `/.rukie/log/probe` is readable and owned by the runner uid on the host.

---

## 2. Configuration

| Key | Default | Limit | Failure |
|---|---|---|---|
| `url` | required | `https://` (`http://` only with `insecure_dev = true`) | `RUKIE_RUNNER_CONFIG`, exit 78 |
| `state_dir` | `$XDG_STATE_HOME/rukie-runner` | 0700, owned by runner | `RUKIE_RUNNER_STATE_DIR` |
| `podman_path` | `LookPath("podman")` | resolved to absolute path at probe | `RUKIE_RUNNER_PODMAN_MISSING` |
| `probe_image` | required | `name@sha256:<64 hex>` | `RUKIE_RUNNER_CONFIG` |
| `max_jobs` | 1 | 1–64 | `RUKIE_RUNNER_CONFIG` |
| `cpus`, `memory` | all CPUs; 80% of MemTotal (probe) | ≤ host | `RUKIE_RUNNER_CONFIG` |
| `min_free_disk` | 20 GiB | ≥ 1 GiB | stops leasing below this |
| `disk_floor` | 2 GiB | < `min_free_disk` | cancels running attempts below this (`E_DISK_FLOOR`) |
| `outage_budget` | 5m | ≤ 5m (I-1) | `RUKIE_RUNNER_CONFIG` |
| `drain_timeout` | 30m | ≤ 6h | `RUKIE_RUNNER_CONFIG` |
| `prepare_timeout` | 10m | ≤ 30m | `RUKIE_RUNNER_CONFIG` |
| `collect_timeout` | 10m | ≤ 30m | `RUKIE_RUNNER_CONFIG` |
| `workspace.max_bytes` | 8 GiB | enforced during materialize and by sampled walk | job `E_WORKSPACE_LIMIT` |
| `object_cache.max_bytes` | 20 GiB | LRU | evicts; reads fall back to fetch |
| `cache.max_bytes` | 50 GiB | LRU over idle cache dirs | evicts whole dirs; never locked |
| `egress` | false | requires P12 | `RUKIE_RUNNER_EGRESS_UNAVAILABLE` |

**Clocks.** Safety decisions use `CLOCK_BOOTTIME` through a stdlib `syscall.Syscall(SYS_CLOCK_GETTIME, …)` wrapper [I]. Go's monotonic clock excludes suspend. Wall clock is used only for display.

---

## 3. Phases

Each phase gives its steps, success, timeout, retry, terminal failure, and every fenced request it sends. "Fenced" means `Rukie-Attempt` and `Rukie-Fence` are present and checked.

### Phase 1: Startup (process entry)

- **Steps:** load and validate config; create or verify state dir; acquire `runner.lock`; resolve podman; run P1–P13; write `probe.json` (podman version, controller set, check results, advertised capabilities).
- **Success:** P1–P13 pass. Continue to Phase 2 if unregistered, otherwise to Phase 10 (restart recovery), then Phase 3.
- **Timeout:** each podman call 30 s; P11 in-container run 60 s; whole probe 3 min. A timed-out check fails with its own error (`RUKIE_RUNNER_PODMAN_UNRESPONSIVE` for P1–P3 timeouts).
- **Retry:** only P10 (image pull): 3 attempts, backoff 5 s, 15 s, 45 s. Then exit 75.
- **Terminal failure:** named error from §1.5 with its exit code. No registration, no lease, no container.
- **Fenced requests:** none.

### Phase 2: Register (once per state dir)

- **Steps:** `POST /runner/v1/register` with `rkt_` token and probe capabilities. On 201, write `credential` (0600, temp file, fsync, rename) and `runner.toml`.
- **Success:** 201 with `state: active` or `pending`.
- **Pending:** poll `GET /runner/v1/self` every 30 s; no lease; log `RUKIE_RUNNER_PENDING` once.
- **Timeout:** 30 s per request.
- **Retry:** network or 5xx: backoff 1 s to 30 s, full jitter, up to 10 min total, then exit 75. 429 honours `Retry-After`.
- **Terminal failure:** 401 `E_REG_TOKEN`, 409 `E_REG_TOKEN_USED`, 422 `E_CAPABILITY`, 426 `E_PROTOCOL` → exit 78.
- **Fenced requests:** none (no attempt exists).

### Phase 3: Lease, grant, start

- **Steps:**
  1. Long-poll `POST /runner/v1/lease` with free capacity (`slots`, `cpus`, `memory_bytes`), `disk_ok`, `degraded`, `probe_digest`. Server holds up to 25 s.
  2. On grant, check locally: `trust` equals runner's trust class; `allocated` fits free capacity; `object_format` is `sha1`; spec digest matches canonical bytes; requested limits are enforceable (§1.3).
  3. Send `POST attempts/{id}/start`. **Start is idempotent per (attempt, fence):** `leased` → `running`; `running` with same fence → 200 with no state change beyond `last_hb_recv`.
  4. On 200, set `last_ack_send` = send time of this request (boottime). The reply carries `cancel`; if true, the runner skips to Phase 8 with verdict `cancelled`, `E_CANCELLED`, and never materializes.
- **Trust or capacity mismatch (local):** send `abandon` with `rejected_before_start` (uncharged). The runner then sets `degraded` if this happens three times in one minute.
- **Spec digest or schema failure:** still call `start`, then Phase 8 with `errored`, `E_SPEC_INVALID`, class `config`. The job is marked failed, not requeued forever.
- **Success:** start 200 → Phase 4.
- **Timeout:** lease hold 25 s (client 35 s). Start: 10 s per try until the server's start deadline (60 s after grant).
- **Retry:** lease errors: backoff 1 s to 30 s, full jitter. Start network or 5xx: retry until the start deadline.
- **Terminal failure:**
  - Start returns 409 `E_LEASE_LOST`: fence was bumped (start deadline passed). **Drop the grant with no side effects.** No abandon is needed.
  - Start cannot be delivered before the deadline: send best-effort `abandon rejected_before_start` (leased → queued, uncharged) and drop the grant.
  - 403 `E_RUNNER_REVOKED`: kill all, exit 78. 401: exit 78. 426: exit 78.
- **Fenced requests:** `start` (fenced, idempotent); `abandon` (fenced, allowed in `leased` or `running`). `lease` is unfenced (no attempt exists yet).

### Phase 4: Materialize (prepare)

- **Steps:**
  1. **Image.** `podman image exists <spec.image>`; if absent, `podman pull --quiet <spec.image>`. Verify `RepoDigests` includes `spec.image`, and `Os/Architecture` equals spec platform. The spec image is used verbatim; it already contains the digest.
  2. **Commit.** Fenced `GET attempts/{id}/commit` returns the raw commit object. Verify `sha1("commit " + len + "\0" + bytes)` equals `grant.commit`. Parse the `tree` line; it must equal `grant.tree`.
  3. **Objects.** Walk the tree from `grant.tree`. For each tree, read from object cache or fenced `GET attempts/{id}/objects/{oid}`. Every object is hashed on receipt and on every cache read; mismatch → `E_OBJECT_HASH` (security).
  4. **Materialize.** Write the tree into `work/` through an `os.Root` (§5.9). Rules in §3.4.
  5. **Caches.** Open and lock declared caches (§6).
  6. **Attempt files.** Write `spec.json` (canonical bytes, 0600), `steps.json` (executor input, mounted read-only), `env` (0600, env file).
- **Object cache:** `<state>/objects/sha1/<xx>/<oid>` holds verified raw objects. Shared across jobs on this runner and trust class. Immutable, so it needs no locks; an evicted entry falls back to a fetch.
- **Success:** image verified; commit and tree verified; workspace matches the tree; caches locked; attempt files written.
- **Timeout:** `prepare_timeout` (10 min default). Heartbeats continue during prepare. The outage budget (§4.2) also applies. Both are checked before every object fetch.
- **Retry:**
  - Object GET: 3 retries per object, backoff 1 s, 2 s, 4 s. Concurrency 8.
  - Image pull: 3 tries.
  - Cache lock wait: up to `prepare_timeout`.
- **Terminal failure:**
  - Config: `E_IMAGE_DIGEST`, `E_OBJECT_NOT_IN_CLOSURE` (404 from endpoint), `E_OBJECT_FORMAT_UNSUPPORTED`, `E_SUBMODULE_UNSUPPORTED` (tree mode 160000), `E_TREE_MODE`, `E_TREE_PATH`, `E_CACHE_PATH`. All `errored`, class `config`, not retried.
  - Security: `E_OBJECT_HASH`, `E_TREE_MISMATCH` (commit or tree does not match computed hash). `errored`, class `security`, not retried, alert.
  - Resource: `E_WORKSPACE_LIMIT` (cumulative written bytes exceed `workspace.max_bytes`). `resource_exhausted`, not retried.
  - Infra (retried): `E_IMAGE_PULL`, `E_OBJECT_TRANSPORT`, `E_DISK`, `E_CACHE_BUSY`, `E_MATERIALIZE_IO`.
  - On terminal or infra failure: remove partial `work/`, release locks, Phase 8 with verdict.
- **Outage abort:** if the outage budget is exceeded during prepare, stop; remove partial files; no container exists. Send `abandon runner_outage` on reconnect (uncharged).
- **Fenced requests:**
  - `GET attempts/{id}/commit` (fenced read).
  - `GET attempts/{id}/objects/{oid}` (fenced read; server gates to attempt closure).
  - Heartbeat (fenced) every 10 s, phase `materialize`.

**Materialization rules (exact tree, no `.git`):**
- Walk trees in order. Entries have mode, name, oid.
- Mode `100644` → file, 0644. `100755` → file, 0755. `120000` → symlink whose target is the blob content (length ≤ 4096, no NUL), created only after its parent directory is verified through the root. Mode `40000` → directory. Mode `160000` → refused (`E_SUBMODULE_UNSUPPORTED`).
- Names must not contain `..`, be absolute, contain NUL, or equal `.git` (case-insensitive). Duplicate names in one tree are refused.
- Blobs are written from the verified bytes. LFS pointer blobs are written as pointer text; LFS is deferred.
- All writes go through an `os.Root` opened on `work/`. No container exists during materialize, so there is no concurrent writer. The root is used anyway, so the code path matches the untrusted-read rules in §5.9.

### Phase 5: Run (sandbox, executor, monitor)

- **Steps:**
  1. **Pre-run gate.** Send a synchronous fenced heartbeat. The gate must return 200 with `cancel: false`. Its send time becomes `last_ack_send`. **No container starts without a gate ack.**
  2. **Start container.** Run the single command in §5.2. Record `container_started` in `attempt.json`.
  3. **Monitor loop:** heartbeat every 10 s ± 1 s (fenced). Each heartbeat samples cgroup stats (§7). Every 60 s, a sampled walk of `work/` (cap 200 000 entries) checks `workspace.max_bytes`. Every heartbeat checks `statfs` against `disk_floor`.
  4. **Wait** for the `podman run` waiter. Then `podman inspect` for `State.ExitCode`, `State.OOMKilled`, and confirm the container is gone.
- **Success:** container exited and was removed. Phase 6 flushes logs; Phase 7 collects; Phase 8 reports the verdict from §5.8.
- **Timeouts:**
  - Executor deadline: spec `timeout` (default 60 min, max 24 h), enforced inside the container.
  - Host backstop: spec `timeout` + 2 min. On expiry: `podman stop --time 15`.
  - Container start must reach "running" within 60 s; otherwise `E_SANDBOX_START`.
- **Retry:**
  - Heartbeat failures do not stop the container until the outage budget is exceeded (5 min after `last_ack_send`). Then: `podman stop --time 15`; verdict none; `abandon runner_outage` on reconnect.
  - Gate failure: retry gate until the budget. Never start a container without an ack.
  - Podman 125 before container exists: `E_SANDBOX_START` (infra, retried).
- **Terminal failure:** §5.8 verdicts. Runner-recorded reasons take precedence (order in §5.8).
- **Cancel:** heartbeat reply `cancel: true` → record runner reason `cancel`; `podman stop --time 15`.
- **Fence-out:** any 409 `E_LEASE_LOST` → record nothing further; `podman stop --time 15`; discard the result; keep `log/` and `runner.log` 24 h for diagnosis. The runner sends no further attempt writes.
- **Fenced requests:** pre-run gate heartbeat (fenced); heartbeats (fenced).

**Goroutines during Phase 5 (each with a shutdown path):**

| Goroutine | Count | Exit |
|---|---|---|
| Podman waiter | 1 | Process exit; ctx cancel kills the `podman` client only; container is stopped explicitly |
| Heartbeat and cgroup sampler | 1 | Attempt ctx |
| Workspace sampler | 1 | Attempt ctx |
| Log shippers (Phase 6) | ≤4 | Attempt ctx; EOF after exit and flush |

### Phase 6: Stream (logs)

- **Steps:** the executor writes `log/job.stdout`, `log/job.stderr`, `log/events` (copies from pipes; cap 64 MiB per job stream, 4 MiB for `events`). The runner ships each file from its acknowledged offset. The runner's own messages go to `runner.log`, shipped as stream `runner`.
- **Chunk rules:**
  - Read ≤ 64 KiB from the file into an **in-flight buffer** (bytes and offset).
  - Send `PUT attempts/{id}/streams/{s}?offset=N` with the buffer. Set `Content-Digest` (RFC 9530 sha-256) on those exact bytes.
  - **On retry, resend the same buffer. Never re-read the file for the same offset.**
  - Read the next chunk only after a 200.
- **Server acceptance** (for request offset `N`, bytes `B`, length `L`, stored length `S`):
  - `N > S` → 409 `E_LOG_GAP` with `ack_offset = S`. Runner discards the in-flight buffer and resumes at `S`.
  - `N ≤ S`: the overlap `[N, min(N+L, S))` must equal stored bytes there. Mismatch → 409 `E_LOG_DIVERGED`. Otherwise append `B[S−N:]` if any. Reply 200 with `ack_offset = max(S, N+L)`.
  - Cap exceeded → 413 `E_LOG_LIMIT`.
  - Every accepted write is committed in SQLite (`synchronous=FULL`) before the 200.
- **Success:** after container exit, each stream's stored length equals its file size. Finish carries `{bytes, sha256}` per stream.
- **Timeout:** after container exit, wait up to 2 min for flush. Then `finish` with `log_incomplete: true`.
- **Retry:** backoff 1 s to 30 s, full jitter, identical buffer each time.
- **Terminal failure:**
  - `E_LOG_DIVERGED`: stop that stream; `log_truncated: true`. The verdict is unaffected.
  - `E_LOG_LIMIT`: same.
  - Non-regular file or symlink at a log path (§5.9): kill container; runner reason `host_path_violation`; verdict `errored`, `E_HOST_PATH_VIOLATION`, class `security`.
  - 409 `E_ATTEMPT_TERMINAL` or `E_LEASE_LOST`: stop.
- **Fenced requests:** `PUT attempts/{id}/streams/{s}?offset=N`. On restart only, `GET attempts/{id}/streams` (Phase 10).

**Read rule:** while the container runs, the runner opens log files with `O_RDONLY|O_NOFOLLOW|O_NONBLOCK`, then `fstat` must show a regular file. Anything else is a host-path violation.

### Phase 7: Collect (artifacts, caches, workspace)

- **Steps:**
  1. Confirm the container is gone (`podman inspect` not found). No writer exists.
  2. Final log read to EOF (Phase 6).
  3. **Artifacts:** for each declared path, walk under `work/` with `fs.WalkDir` (Lstat; symlinks not followed). Types: symlink → `E_ARTIFACT_SYMLINK`; regular file with `nlink > 1` → `E_ARTIFACT_HARDLINK`; FIFO, socket, device → `E_ARTIFACT_TYPE`. Count and bytes checked before hashing. Hash each file (sha256, streaming).
  4. **Upload** each distinct digest not yet committed: fenced `HEAD`; if absent, fenced `PUT` in 16 MiB chunks with `Upload-Offset`. Concurrency 4. Re-open through `os.Root` with `O_NOFOLLOW` to upload.
  5. Fenced `POST attempts/{id}/artifacts` with the item list. Server verifies every digest is staged and sizes match; links them to the attempt. They become visible at Phase 8 finish.
  6. **Caches:** remove `.inflight` marker, write `<name>.meta`, unlock. Measure size; if the cache total exceeds `cache.max_bytes`, evict whole idle dirs, least recently used first.
  7. **Workspace:** measure final size (reported). Remove `work/` with `os.RemoveAll` (does not follow symlinks).
- **Success:** artifacts linked; caches released; workspace removed.
- **Timeout:** `collect_timeout` (10 min default).
- **Retry:** fenced HEAD, PUT, and artifacts POST retried with backoff until the collect timeout.
- **Terminal failure:**
  - Config (not retried): `E_ARTIFACT_SYMLINK`, `E_ARTIFACT_HARDLINK`, `E_ARTIFACT_TYPE`, `E_ARTIFACT_LIMIT`, `E_ARTIFACT_PATH`.
  - Security: `E_HOST_PATH_VIOLATION`.
  - Infra (charged): upload still failing at collect timeout → `errored`, `infra`.
  - 409 `E_LEASE_LOST`: discard artifacts; staged bytes expire after 1 h.
- **Fenced requests:** `HEAD blobs/{repo}/sha256/{hex}` (fenced); `PUT blobs/{repo}/sha256/{hex}` (fenced); `POST attempts/{id}/artifacts` (fenced).

**Artifact rules:** `artifacts.paths` are relative to `/work`; files or directories; no globs in v1 [I]. Defaults: 1000 files, 512 MiB. Maximums: 10 000 files, 4 GiB. Deduplication is **per repo** (`blobs/<repo_id>/sha256/<hex>`) so `HEAD` is not a cross-repo existence oracle [I].

### Phase 8: Report (finish)

- **Steps:** build the result (canonical JSON, RFC 8785 rules, §4.5). Write `result.json` and fsync. Send fenced `POST attempts/{id}/finish` with the result digest. On 200, delete the attempt directory after a short grace (log copy retained only if fenced out).
- **Verdict and runner reason:** §5.8 defines verdicts. Abandoned attempts (outage, shutdown) produce no finish; they produce `abandon`.
- **Success:** 200 `{state}`. Attempt is terminal.
- **Timeout:** per request 30 s. The retry loop itself has no deadline.
- **Retry:** backoff 1 s doubling to 5 min, full jitter, until 200, 409 `E_FINISH_CONFLICT`, or 409 `E_LEASE_LOST`. Persists across restart because `result.json` is on disk. **The outage budget does not apply here:** no container exists, so there is nothing to stop.
- **Terminal failure:**
  - 409 `E_LEASE_LOST`: discard result; keep logs 24 h.
  - 409 `E_FINISH_CONFLICT` (different digest for same attempt) or 422 `E_RESULT_SCHEMA`: runner bug; stop; keep `result.json`; alert.
- **Fenced requests:** `POST attempts/{id}/finish` (fenced; idempotent by result digest).

**Result fields:** `verdict`, `reason`, `class`, `exit_code`, `executor_exit`, `runner_reason`, `phases` (start, end, boottime), `usage` (`cpu_usec`, `mem_peak_bytes` or `approx`, `pids_max`, `workspace_bytes`, `allocated`), `artifacts`, `log` (per stream `{bytes, sha256}`), `log_truncated`, `log_incomplete`, `spec_digest`, `image_digest` (matched `RepoDigests` entry), `commit`, `tree`, `runner_id`, `runner_version`, `probe_digest`.

### Phase 9: Drain (SIGTERM or SIGINT)

- **Steps:**
  1. Stop the lease loop (cancel the long-poll). Set `draining`. No new grants are accepted.
  2. Running attempts continue: heartbeats, log shipping, collect, finish.
  3. At `drain_timeout` with attempts still running: for each, record runner reason `shutdown`; `podman stop --time 15`; best-effort log flush; fenced `abandon runner_shutdown` (uncharged).
  4. Exit 0 if no abandons; exit 75 if any.
- **Second SIGTERM or SIGINT:** skip waiting. Same path as step 3 immediately.
- **Success:** all attempts reached finish or abandon.
- **Timeout:** `drain_timeout` (30 min default). Set systemd `TimeoutStopSec` to drain plus 5 min.
- **Retry:** abandon and finish retried for up to 60 s per attempt. If the server is unreachable, the abandon cannot be delivered; the server later marks `lost` and charges `runner_lost`. This is documented, not hidden.
- **Terminal failure:** second signal; or any panic (exit 1).
- **Fenced requests:** heartbeat (fenced), log flush `PUT streams` (fenced), `abandon` (fenced), `finish` (fenced).

### Phase 10: Restart (crash recovery; runs before first lease)

- **Steps:**
  1. Reap containers: `podman ps -a --filter label=rukie.state=<statehash> --format json`, then `podman rm --force --time 0 <names>`. If reaping fails, **refuse to lease**: `RUKIE_RUNNER_REAP_FAILED`, exit 75.
  2. Quarantine caches: any `<name>.inflight` marker with no lock held is moved to `quarantine/` and deleted in the background. The job gets a fresh cache.
  3. For each `attempts/*` directory, in background goroutines with timeouts:
     - `result.json` present → resume Phase 8 with the same digest.
     - Otherwise: fenced `GET attempts/{id}/streams` to learn stored offsets; ship remaining bytes (fenced PUT); fenced `abandon runner_restart` (charged as `runner_lost`); remove directory after acknowledgement or 409.
  4. Start the lease loop. Background cleanup does not block leasing; new attempts have new fences, so stale traffic cannot affect them.
- **Success:** reap complete; quarantine done; lease loop started.
- **Timeout:** reap call 60 s; reap total 5 min; per-attempt cleanup 60 s.
- **Retry:** reap retried 3 times, then exit 75. Per-attempt requests follow Phase 8 and Phase 9 rules.
- **Terminal failure:** `RUKIE_RUNNER_ALREADY_RUNNING` (exit 78); `RUKIE_RUNNER_REAP_FAILED` (exit 75).
- **Fenced requests:** `GET streams` (fenced read), `PUT streams` (fenced), `abandon` (fenced), `finish` (fenced).

---

## 4. Server state machine, fencing, and timers

### 4.1 Attempt states (server)

| From | Event | To | Side effects |
|---|---|---|---|
| — | Lease commit | `leased` | `job.fence++`; capacity reserved; start deadline = now + 60 s |
| `leased` | `start` (first or replay, same fence) | `running` | `last_hb_recv = now`; reply carries `cancel` |
| `leased` | Start deadline passed | Job `queued` (uncharged) | `fence++`; capacity released |
| `leased` | `abandon rejected_before_start` | Job `queued` (uncharged) | `fence++` |
| `running` | Heartbeat or any fenced request | `running` | `last_hb_recv = now` |
| `running` | `now − last_hb_recv > 6 min` | `lost` (charged `runner_lost`) | `fence++`; capacity released; retry policy §7 |
| `running` | `abandon runner_shutdown` or `runner_outage` | Job `queued` (uncharged) | `fence++` |
| `running` | `abandon runner_restart` | `lost` (charged) | `fence++`; retry policy §7 |
| `running` | Finish | Verdict state | Outbox rows for attestation, status, webhooks |
| `running` | Cancel by user or owner | Next heartbeat reply `cancel: true` | Runner records `cancel`; then Phase 8 `cancelled` |

`suspect` is a display flag: no heartbeat for 30 s. It does not requeue.

Server boot: all `running` attempts get `last_hb_recv = restart time`.

### 4.2 Fencing and I-1

**Fence rules:**
1. `job.fence` is monotonic per job. It increments on each grant, each `lost`, each requeue-producing abandon, and each start-deadline expiry.
2. Every attempt-scoped request (reads and writes) is checked in one `BEGIN IMMEDIATE` transaction: `Rukie-Attempt` = `job.current_attempt`; `Rukie-Fence` = `job.fence`; authenticated runner owns the attempt. Otherwise 409 `E_LEASE_LOST`. If the attempt belongs to another runner, the server returns 404, so attempt ids are not revealed.
3. Terminal attempts accept only an identical `finish` (same digest → 200). Anything else → 409 `E_ATTEMPT_TERMINAL`.
4. Fenced-out results are discarded, never adopted into the cache or published.
5. On 409 `E_LEASE_LOST` the runner stops the container, sends no further attempt writes, and retains logs 24 h.

**Outage budget scope (corrected).** The runner's self-fence budget applies from the first acknowledged request (`start`) until the container has exited. That covers prepare and run. It does not cover collect or finish, because no container remains. This matters because the server's `lost_after` applies to every running phase: without this coverage a runner partitioned during prepare could start a container after the server requeued.

**Timers:**

| Name | Value | Owner | Meaning |
|---|---|---|---|
| Long-poll hold | 25 s (client 35 s) | Server; runner | Lease |
| Start deadline | 60 s after grant | Server | Not started → requeue (uncharged) |
| Heartbeat interval | 10 s ± 1 s | Runner | Cadence |
| Heartbeat timeout | 5 s | Runner (own client) | Request deadline |
| Suspect | 30 s | Server | Display only |
| `lost_after` | 6 min since last received heartbeat or fenced request | Server | Fence bump, requeue |
| `outage_budget` | 5 min since send time of last acknowledged fenced request | Runner | Self-fence (kill container, no verdict) |
| Pre-run gate | Synchronous heartbeat before `podman run`; must return 200 | Runner | No container without an ack |
| Podman stop | `--stop-timeout=15` | Runner | SIGTERM, then KILL |
| Executor kill grace | 10 s | `job-exec` | Step group: SIGTERM, then SIGKILL |
| Job timeout | spec, default 60 min, max 24 h | `job-exec` | Exit 3 |
| Backstop | job timeout + 2 min | Runner | `podman stop` |
| Prepare timeout | 10 min (≤ 30 min) | Runner | Infra error |
| Collect timeout | 10 min (≤ 30 min) | Runner | Infra error |
| Log flush before finish | 2 min | Runner | Else `log_incomplete` |
| Finish retry | 1 s, doubling, cap 5 min, jitter | Runner | Until 200, 409 |
| Drain timeout | 30 min (≤ 6 h) | Runner | Phase 9 |
| Queue deadline | 6 h | Server | Queued jobs with no eligible runner fail as `errored`, `config` |
| Blob staging TTL | 1 h | Server | Staged bytes deleted |

**I-1.** The runner kills every container by `t_send + 5 min + 15 s` (budget plus stop timeout), where `t_send` is the send time of the last acknowledged request. The server marks the attempt `lost` no earlier than `t_recv + 6 min`, where `t_recv ≥ t_send`. Margin ≥ 45 s. The boot rule preserves the ordering: the restart time is after every acknowledged send before it.

**Residual.** A frozen runner process (SIGSTOP or cgroup freeze) cannot run its timer. The container then runs until its executor deadline. For exclusive effects, the external system must check `RUKIE_FENCE` (§5.7). A host-side kill independent of the runner process is an open question (§9).

### 4.3 Grant payload

```json
{"attempt":"…","fence":7,"job":"…","run":"…","repo_id":"…",
 "commit":"<40 hex>","tree":"<40 hex>","object_format":"sha1",
 "trust":"untrusted","allocated":{"cpus":2,"memory_bytes":4294967296,"slots":1},
 "lease_ttl_s":30,"spec_digest":"sha256:…","spec":{…JobSpec…}}
```

There is no fetch token. The runner's own credential plus the attempt's fence authorize all object reads.

### 4.4 Commit closure (server rule)

- **Closure of an attempt** = the commit object, its tree, and every tree and blob reachable from that tree. Gitlinks (commit-type entries) are **not** in closure. Parent commits are not in closure.
- The server computes closure once per attempt, when the first tree or object request arrives, and stores membership in `attempt_object(attempt, oid)`. Bounded at 1 000 000 objects; exceeding it → `E_CLOSURE_LIMIT` (config).
- Object requests for an OID outside closure → 404 `E_OBJECT_NOT_IN_CLOSURE`.
- **Run creation rule:** a run is created only for a commit that is reachable from an advertised ref of the repo at creation time. Closure of an authorized commit therefore contains no private-chain objects that the repo's readers could not already see via that branch [I].

### 4.5 Canonical bytes

Spec and result JSON use RFC 8785 JCS. Stdlib `encoding/json` with `SetEscapeHTML(false)` yields identical bytes when keys are ASCII `[a-z0-9_.-]` and values are strings, booleans, integers, arrays, or objects, with no floats [I]. A test vector is required (§9).

---

## 5. Sandbox

### 5.1 Choice

- **Rootless podman ≥ 5.0**, invoked as an absolute-path subprocess, one argument builder for probe and jobs. No daemon, no socket.
- **Not used:** bubblewrap (no fallback; its own docs say protection depends entirely on arguments [V, research]); gVisor (user-space kernel, costs on syscalls, VFS, network [V, research correction]); Firecracker (needs `/dev/kvm`, jailer, guest image); raw-syscall Go sandbox (a container runtime).
- gVisor may later be an explicit `sandbox` label, never a fallback [I].
- **Landlock: Deferred.** Landlock rulesets inherit across `fork` and `exec`, so applying it to the runner confines podman too, which would block storage and runtime paths [U: standard behaviour; not verified here]. A later design may confine only a non-podman worker, or grant podman's paths explicitly, tested by P11.

### 5.2 The command (single line, unverified on this host)

Placeholders: `<cache-mounts>` is one `--mount=type=bind,src=<cache dir>,dst=/cache/<name>,nosuid,nodev` per cache, in spec order. `<spec.image>` is `spec.image` verbatim (already `name@sha256:<64 hex>`). `<uid>:<gid>` is the runner's own uid and gid, resolved at probe.

```
podman run --rm --name rukie-<r8>-<attempt> --label rukie.state=<statehash> --label rukie.attempt=<attempt> --label rukie.fence=<fence> --pull=never --init --entrypoint=/.rukie/bin/rukie --user=<uid>:<gid> --userns=keep-id --workdir=/work --hostname=rukie-job --network=<none|pasta> --read-only --cap-drop=ALL --security-opt=no-new-privileges --ipc=private --pid=private --uts=private --cgroupns=private --cpus=<cpus> --memory=<mem_bytes> --memory-swap=<mem_bytes> --pids-limit=<pids> --shm-size=67108864 --tmpfs=/tmp:rw,noexec,nosuid,nodev,size=<tmp_bytes>,mode=1777 --tmpfs=/run:rw,nosuid,nodev,size=16777216,mode=0755 --ulimit=core=0:0 --log-driver=none --stop-signal=SIGTERM --stop-timeout=15 --env-file=<state>/attempts/<attempt>/env --mount=type=bind,src=<state>/attempts/<attempt>/work,dst=/work,nosuid,nodev --mount=type=bind,src=<state>/attempts/<attempt>/log,dst=/.rukie/log,nosuid,nodev --mount=type=bind,src=<state>/runtime/rukie,dst=/.rukie/bin/rukie,ro,nodev --mount=type=bind,src=<state>/attempts/<attempt>/steps.json,dst=/.rukie/steps.json,ro,nosuid,nodev <cache-mounts> <spec.image> job-exec --spec /.rukie/steps.json
```

Rules:
- `--tmpfs=/tmp` drops `noexec` only when spec `tmp_exec` is true (trusted runners only).
- `--network=pasta` only when spec `network = egress`, the runner is trusted, and P12 passed. Otherwise `--network=none`; see §5.6 for refusal.
- `--cpus`, `--memory`, `--memory-swap`, `--pids-limit` are passed only when the controller is delegated. Otherwise the job is refused (§1.3).

### 5.3 Flag rules (each flag, its reason, its failure rule)

| Flag | Reason | Failure rule |
|---|---|---|
| `--rm` | Container removed on exit | Leftover: reaped in Phase 10 |
| `--name rukie-<r8>-<attempt>` | Targeted stop | Collision → `E_SANDBOX_START` (infra) |
| `--label rukie.state/attempt/fence` | Reaper finds runner containers | Reap fails → Phase 10 refuses lease |
| `--pull=never` | Only pre-verified images run | Missing at run → `E_SANDBOX_START` |
| `--init` | pid 1 reaps and forwards signals | Missing init → P11 fails |
| `--entrypoint=/.rukie/bin/rukie` | Runs executor, not image entrypoint | Mount missing → exit 127 → `E_ENTRYPOINT` (config) |
| `--user=<uid>:<gid>` | Non-root; runner's own ids | Mismatch → P11 `uid_map` check fails |
| `--userns=keep-id` | Host uid maps to same uid in container; workspace owned by runner; no chown needed. Plain form; the `keep-id:uid=` option was not verified and is not used [U]. | Missing subuid → P7; userns denied → P8 |
| `--workdir=/work` | Default cwd | — |
| `--hostname=rukie-job` | Non-identifying | — |
| `--network=none` | No network | P11 `/proc/net/dev` check |
| `--network=pasta` | Only for `egress` | Missing `pasta` → refuse job (§5.6) |
| `--read-only` | Immutable root | P11 `touch` check |
| `--cap-drop=ALL` | No capabilities | P11 `CapEff` = 0 |
| `--security-opt=no-new-privileges` | No setuid gain | P11 `NoNewPrivs` = 1 |
| seccomp (not set) | Podman default profile | Spec or config `seccomp=unconfined` → rejected at validation; P11 `Seccomp` = 2 |
| AppArmor (not set) | Podman default container profile | `unconfined` rejected at validation |
| `--ipc/--pid/--uts/--cgroupns=private` | Namespace isolation (explicit) | P11 PPid = 1 check |
| `--cpus` | CPU quota | Controller absent → refuse grant; P11 `cpu.max` |
| `--memory`, `--memory-swap` (equal → no swap) | Memory limit | Controller absent → refuse; P11 `memory.max`, `memory.swap.max` = 0 |
| `--pids-limit` | Fork-bomb limit | Controller absent → refuse; P11 `pids.max` |
| `--shm-size=67108864` | Shared memory for tools | — |
| `--tmpfs=/tmp` | Scratch; `noexec` unless `tmp_exec` | Exceeds size → job ENOSPC (job failure) |
| `--tmpfs=/run` | Tools expecting `/run` | — |
| `--ulimit=core=0:0` | No core dumps to host `core_pattern` | P11 `Max core file size` = 0 |
| `--log-driver=none` | Container logs not stored by podman; executor files are the log | Value unsupported → P11 fails [U] |
| `--stop-signal=SIGTERM --stop-timeout=15` | Graceful stop, then SIGKILL | Used by cancel, shutdown, and outage |
| `--env-file` | Values not in argv | Missing file → `E_SANDBOX_START` |
| `--mount` (work, log, executor, steps, caches) | Declared paths only | Missing source → `E_SANDBOX_START` |
| `--device`, `--privileged`, `--volume`, `--group-add`, `--publish`, `--add-host`, `--sysctl`, `--cgroup-parent`, `unmask=` | **Never** | The builder has no code path that emits them |

### 5.4 Mounts

| Host source | Container | Mode | Container-writable | Runner reads |
|---|---|---|---|---|
| `attempts/<id>/work/` | `/work` | rw | yes | after exit only (Lstat walk) |
| `attempts/<id>/log/` | `/.rukie/log` | rw | yes | while running: `O_NOFOLLOW`, regular file |
| `runtime/rukie` | `/.rukie/bin/rukie` | ro | no | no |
| `attempts/<id>/steps.json` | `/.rukie/steps.json` | ro | no | no |
| `cache/<trust>/<repo>/<name>/` | `/cache/<name>` | rw | yes | after exit (size walk, eviction) |

Not visible to the container: `credential`, `objects/`, `runner.log`, `result.json`, `spec.json`, other attempts, runner state.

### 5.5 Identity

- Container uid and gid equal the runner's host uid and gid (`keep-id`). The container user cannot reach anything not mounted. Mounts are the only paths visible.
- Rootless podman means a container escape yields the runner user's privileges, including its credential. The blast radius is one runner's scope. Mitigation: trust-class separation and dedicated hosts for untrusted code [I].

### 5.6 Network and egress

- `network = none` (default): always allowed.
- `network = egress`: allowed only when (a) the runner's trust class is `trusted`, (b) P12 passed (forge host:port, host gateway, and `169.254.169.254` all unreachable from a pasta container), (c) `pasta` is present, and (d) the grant's `requires` includes `egress`.
- **Fail closed.** If any condition fails at grant time, the runner sends `abandon rejected_before_start` (uncharged). The runner never runs the job with `--network=none` in place of the requested egress. The runner stops advertising `egress`. Error: `RUKIE_RUNNER_EGRESS_UNAVAILABLE`.
- Egress is all-or-nothing in v1. A trusted job can exfiltrate its own workspace or cache through egress; trusted code is the trust boundary.

### 5.7 In-container executor (`rukie job-exec`)

**Inputs:** `/.rukie/steps.json` and environment variable `RUKIE_SPEC_DIGEST`.

**Startup:**
- If `RUKIE_SPEC_DIGEST` is missing, exit 4 (`E_EXECUTOR_SPEC_DIGEST_MISSING`).
- If `steps.json`'s digest differs from `RUKIE_SPEC_DIGEST`, exit 4 (`E_EXECUTOR_SPEC_MISMATCH`).

**Execution:**
- For each step in order: start the process in its own process group (`Setpgid`), with working directory (default `/work`) and environment (env file plus step env).
- Stdout and stderr are pipes. One copier goroutine per pipe appends to `log/job.stdout` or `log/job.stderr` with `O_APPEND|O_CREATE|O_WRONLY`, counting bytes. Past the cap, discard and count.
- Step timeout (optional): SIGTERM to group; after 10 s, SIGKILL.
- Job deadline timer (`timeout_ms`): on expiry, stop the current group as above and exit 3.
- A step failure (non-zero, timeout, exec error) without `continue_on_error` skips remaining steps.
- Appends JSON lines to `log/events` (advisory; see §5.8).

**Exit codes:** 0 all passed; 1 a step failed or timed out; 3 job deadline; 4 internal (spec digest, log write error, spawn error); 143 SIGTERM received.

**Signals:** on SIGTERM or SIGINT, forward SIGTERM to the current group; after 10 s, SIGKILL the group; exit 143. A second signal is handled the same way without waiting.

**Reaping:** `Wait` on own children; `--init` reaps orphaned grandchildren.

**Why exit codes are authoritative.** The container's exit status is produced by the init process from the executor's status. A step can kill the executor, which yields a non-zero status; it cannot produce exit 0 for a failing run [I]. `events` cannot change the verdict.

### 5.8 Verdict mapping (event-free; ordered)

Inputs the runner records itself, in `attempt.json` (not container-writable): `runner_reason` ∈ {`host_path_violation`, `workspace_limit`, `disk_floor`, `cancel`, `outage`, `shutdown`}. Container facts: `OOMKilled`, executor exit code, whether the container ever reached the executor.

Evaluate in this order; first match wins:

| # | Condition | Verdict | Reason | Class | Charged |
|---|---|---|---|---|---|
| 1 | `runner_reason = host_path_violation` | errored | `E_HOST_PATH_VIOLATION` | security | no; alert |
| 2 | `runner_reason = workspace_limit` | resource_exhausted | `E_WORKSPACE_LIMIT` | resource_exhausted | no |
| 3 | `runner_reason = disk_floor` | errored | `E_DISK_FLOOR` | infra | yes |
| 4 | `runner_reason = cancel` | cancelled | `E_CANCELLED` | cancelled | no |
| 5 | `runner_reason = outage` or `shutdown` | no finish; abandon | `runner_outage` / `runner_shutdown` | — | no |
| 6 | `OOMKilled = true` | resource_exhausted | `E_OOM` | resource_exhausted | no |
| 7 | executor 0 | passed | — | — | — |
| 8 | executor 1 | failed | `E_STEP_FAILED` | script_failure | no |
| 9 | executor 3 | timed_out | `E_JOB_TIMEOUT` | timeout | no |
| 10 | executor 4 | errored | `E_EXECUTOR` | infra | yes |
| 11 | executor 143 or 137, not matched above | failed | `E_EXECUTOR_SIGNALED` or `E_EXECUTOR_KILLED` | script_failure | no; operator review flag |
| 12 | container never reached executor; podman 125 | errored | `E_SANDBOX_START` | infra | yes |
| 13 | container never reached executor; exit 126 or 127 | errored | `E_ENTRYPOINT` | config | no |
| 14 | anything else | errored | `E_CONTAINER_UNKNOWN` | infra | yes |

Rows 11 and 14: a step can signal the executor, so SIGTERM or SIGKILL alone cannot be charged as infrastructure. Row 11 accepts that a kernel kill of the executor is also treated as a non-retried failure; that is the conservative trade [I].

`log/events` may name a timeout or failing step for display. It never selects a row.

### 5.9 Host-path rules (container-writable paths)

The container writes `work/`, `log/`, and cache directories. The runner reads host files on the same filesystem, so a symlink created in the container can target a host path (for example `../credential`).

1. **While running:** the runner opens only `log/job.stdout`, `log/job.stderr`, and `log/events` by name, with `O_RDONLY|O_NOFOLLOW|O_NONBLOCK`. `fstat` must show a regular file. Anything else → `host_path_violation`; kill the container. Intermediate directories are runner-owned and cannot be replaced from inside (only their contents are mounted).
2. **After exit:** no writer exists. Use `fs.WalkDir` (Lstat; does not follow symlinks) and open through an `os.Root`.
3. **Caches:** size walks use Lstat. Eviction uses `os.RemoveAll`, which removes symlinks without following them.
4. **Materialization:** writes through `os.Root` on `work/` (§3 Phase 4).
5. **Tests required:** symlink to `../credential` in `log/`; FIFO in `log/`; symlink in artifact paths; symlink in cache dir during eviction.

### 5.10 Not enforced in v1

| Gap | Mitigation |
|---|---|
| Per-job disk quota | Sampled walk (cap enforcement, §3 Phase 5), `min_free_disk`, `disk_floor` |
| I/O limits (`io` not delegated) | None; not claimed |
| Egress allowlist | Trusted-only egress with P12 |
| Secrets | Deferred; would use env file on trusted runs only |
| Image signatures | Digest pinning only |
| Devices, GPUs | Never allowed |

---

## 6. Caches

- **Layout:** `cache/<trust>/<repo_id>/<name>/`, mounted at `/cache/<name>`. Lock file `<name>.lock`.
- **Locks:** `exclusive` (default) → `flock(LOCK_EX)`; `shared` (opt-in, for tools documented as concurrency-safe) → `flock(LOCK_SH)`. Wait up to `prepare_timeout`; then `E_CACHE_BUSY` (infra).
- **Crash safety:** create `<name>.inflight` before mount; remove after exit and before unlock. A startup finding `.inflight` with no lock → quarantine (Phase 10).
- **Quota:** after exit, size measured with a capped Lstat walk; over `cache.max_bytes` → evict whole idle dirs, least recently used first (`<name>.meta`).
- **Trust:** path includes trust class; runners are exclusive per trust class.
- **Spec path** must be `/cache/<name>`; otherwise `E_CACHE_PATH` (config).

---

## 7. Heartbeat, cancellation, accounting

**Heartbeat request (fenced):**

```json
{"phase":"prepare|run|collect","streams":{"job.stdout":N,"job.stderr":N,"events":N,"runner":N},
 "usage":{"cpu_usec":N,"mem_bytes":N,"mem_peak_bytes":N,"pids":N},
 "workspace_bytes":N}
```

**Reply:** `{"cancel": bool, "lost_after_ms": 360000}`.

- Sent every 10 s ± 1 s on the heartbeat client (5 s timeout).
- `last_ack_send` is set to the send time of the last 200 (boottime). Retries after failure do not advance it.
- `workspace_bytes` is from the sampled walk (every 60 s; `null` between samples).
- `statfs` of the state dir is checked on every heartbeat for `disk_floor`.

**Cgroup sampling** (run phase only, when the container exists):
1. `podman inspect --format '{{.State.Pid}}' <name>` → container init pid [U: field name].
2. `/proc/<pid>/cgroup` → `0::/…`; read `cpu.stat` (`usage_usec`), `memory.current`, `memory.peak` (kernel ≥ 5.19; else max sampled `memory.current`, marked `approx`), `pids.current`.
3. After exit the cgroup is gone; the last sample is final. CPU time accuracy is ±1 sample interval [I].

**Accounting charge (server):** `runner_seconds = wall_s × allocated_cpus` (allocation-based, so idle reservations count). `cpu_seconds = cpu_usec / 1e6` (reported). Per-tenant aggregates use `runner_seconds`.

---

## 8. Ruk and rukie dependencies

### 8.1 Findings in Ruk (blocking for any forge smart-HTTP exposure)

**F-1. Upload-pack wants are existence-gated, not reachability- or advertisement-gated.**
- `ruk-git/src/transport/serve_v2.rs:372–376`: each `want` is parsed and checked with `has_object(store, oid)`. Absent → `not our ref`. Present → accepted, whether or not advertised [V].
- `serve_v2.rs`, `serve.rs`, and `proto_v2.rs` contain no `private` match (grep, case-insensitive) [V].
- `PrivateFilter` (`ruk-git/src/transport/private.rs`) is an explicit OID set; `PrivateClosure::expand` grows it [V]. The push path documents its use: "Callers with private state use `push_closure_filtered`" (`push.rs:792–795`) [V]. The serve path does not apply it.
- **Consequence:** any token that can run upload-pack against a repo can fetch any stored object by OID, including objects reachable only from private chains. This is not limited to runners.
- **Rule for rukie:** do not expose Ruk upload-pack, or any object-by-OID path, to runner tokens or other non-admin tokens until Ruk gates wants by reachability or advertisement, or applies private filtering on serve. A test must prove a private-chain want is refused.

### 8.2 Required Ruk method (R1)

- **Need:** a typed read that returns raw bytes and kind for a commit, tree, or blob OID, callable only by the rukie server principal.
- **Status:** not in the 47-method table [V: `ruk-core/src/protocol.rs`, method table]. `show` takes a `ShowTarget` and returns `ShowReply{view, body}`; `file` takes `RevisionRef` plus a path and returns `FileReply{view, body}` [V]. Neither is documented here as returning raw object bytes [I; `ShowTarget` variants not read].
- **Action:** add a Ruk read method (`object.read`) for the rukie server principal. Runner tokens must never reach it. R1 is blocking for Phase 4.

### 8.3 Rukie server requirements (contract with explicit responses)

| Endpoint | Server requirement | Failure responses |
|---|---|---|
| `GET attempts/{id}/commit` | Return raw commit object for `grant.commit`; closure rule §4.4 | 404 `E_NOT_FOUND` (not owned); 409 `E_LEASE_LOST`; 503 `E_OBJECT_TRANSPORT` (retryable) |
| `GET attempts/{id}/objects/{oid}` | Return raw bytes if `oid ∈ attempt_object`; set `Rukie-Object-Kind` | 404 `E_OBJECT_NOT_IN_CLOSURE`; 409 `E_LEASE_LOST`; 503 retryable |
| `GET attempts/{id}/streams` | Stored byte length per stream | 409 `E_LEASE_LOST` |
| `PUT attempts/{id}/streams/{s}?offset=N` | §3 Phase 6 rules | 409 `E_LOG_GAP`, `E_LOG_DIVERGED`, `E_ATTEMPT_TERMINAL`, `E_LEASE_LOST`; 413 `E_CHUNK_SIZE`, `E_LOG_LIMIT` |
| `HEAD blobs/{repo}/sha256/{hex}` | 200 committed, 404 absent | 409 `E_LEASE_LOST` |
| `PUT blobs/{repo}/sha256/{hex}` | Chunked; `Upload-Offset`; digest on finalize | 400 `E_BLOB_DIGEST`; 409 `E_UPLOAD_OFFSET` (with `Upload-Length`), `E_LEASE_LOST` |
| `POST attempts/{id}/artifacts` | Verify staged digests and sizes | 422 `E_ARTIFACT_MISSING_BLOB`, `E_ARTIFACT_PATH`; 409 `E_LEASE_LOST` |
| `POST attempts/{id}/finish` | Idempotent by result digest | 409 `E_FINISH_CONFLICT`, `E_LEASE_LOST`; 422 `E_RESULT_SCHEMA` |

### 8.4 Not used (removed from the design)

- No hidden refs, no run-pin refs, no `refs/rukie/...`. Ruk's typed bookmark namespace is `refs/heads/` only and tags are `refs/tags/` only [V: `protocol.rs` validate impls, `prepare.rs` tag check]. A pin would be a visible branch (`refs/heads/ci-pin-<run>`) and needs `bookmark.set` on the hosted path [U]. Rejected for v1 because it pollutes branch lists.
- No fetch-by-OID through upload-pack (§8.1).
- No git binary on the runner host.
- No `upload-archive` path (no source read for it).

### 8.5 Object format

v1 supports `sha1` only. A repo with another object format is refused at grant time: `E_OBJECT_FORMAT_UNSUPPORTED`. Whether Ruk's `HashKind` has a second variant was not read [U].

---

## 9. Open questions and probes

Nothing here was executed. Each item must be resolved before the design is called verified.

1. **R1 existence and shape.** Confirm whether `show` or `file` can return raw bytes for a given OID (read `ShowTarget` variants). If not, add `object.read` (§8.2).
2. **F-1 regression test.** A private-chain commit must be refused by any Ruk upload-pack path exposed to a non-admin token. Until that test passes, runner tokens must not reach upload-pack.
3. **Canonical JSON vector.** Verify the stdlib JCS claim (§4.5) against RFC 8785 examples.
4. **Podman flag set.** Install podman at `/usr/bin/podman`; record version; run P11. Verify `--userns=keep-id`, `--log-driver=none`, `State.Pid`, `podman info` keys, `--cpus` and `--memory` read-back, and the pasta path.
5. **Userns transition.** Confirm whether podman's userns creation transitions into `unprivileged_userns` on this host (P8 decides; podman is absent).
6. **Frozen-runner bound.** A host-side kill independent of the runner process. Candidate: transient user unit with `RuntimeMaxSec`, containers placed under it. Needs probe 4.
7. **Executor kill classification.** Row 11 treats kernel kills of the executor as non-retried failures. Measure how often that occurs before accepting it.
8. **Object cache verification cost.** Re-hashing on every cache read: measure on large trees.
9. **Closure size.** 1 000 000-object bound (§4.4): measure walk cost on a large repo.
10. **Egress allowlist and disk quota.** Deferred (§5.10).

---

## Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/serve_v2.rs` — lines 372–376: want check is `has_object` only. Basis for F-1. Also the test at ~997 for the regression (§8.1, §9.2).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/private.rs` — `PrivateFilter` and `PrivateClosure::expand`; the private-object semantics the serve path does not apply (§8.1).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs` — method table (no raw object read; `show` and `file` payloads at ~2340–2443); the place R1 would be added (§8.2).
- `/etc/apparmor.d/podman` and `/etc/apparmor.d/unprivileged_userns` — the two AppArmor profiles relevant to podman userns; whether podman's path transitions into the second is open (§1.1, §9.5).
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-ci-runner.md` — prior art. Its tail corrections (lines 300–333) are applied here: Landlock Deferred, gVisor cell corrected, action digest without `trust`, no exactly-once.