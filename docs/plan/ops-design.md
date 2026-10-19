# rukie Operations Design (`ops-design.md`)

## 0. Scope and conventions

This document specifies rukie deployment and operations: configuration,
process topology, startup/shutdown, data-dir layout, backup/restore,
upgrades, observability, admin CLI, packaging, and systemd units.

Normative config language: TOML, parsed with `BurntSushi/toml` or
`pelletier/go-toml/v2` (decision left to K3; either is pure Go and
`CGO_ENABLED=0`-safe). Unknown keys are a **hard startup error**
(`config: unknown key`). All durations are integer milliseconds in TOML
(`*_ms`); this matches the Ruk daemon convention
(`handshake_timeout_ms`, see `DaemonConfig::from_toml` in
`ruk/crates/ruk/src/daemon/mod.rs`). All sizes are bytes.

Paths may be absolute or relative to the config file directory.
Relative paths resolve against the config file's parent dir, never CWD.

References to the Ruk daemon below mean the **forge-neutral `ruk serve`**
from Phase R1, the successor of today's `ruk forgejo serve`
(`ruk/crates/ruk/src/daemon/mod.rs` header docs,
`ruk/crates/ruk/src/main.rs` matches `ForgejoCommand::Serve { config }`
and reads the TOML file). Normative invocation in this document is
`ruk serve --config <file>`. Until R1 lands, the same binary spells it
`ruk forgejo serve --config <file>`; the supervisor MUST probe the
binary at startup (`ruk version` / `--help`) and use whichever spelling
the paired binary advertises, so the transition is a paired-binary
upgrade, not a flag day. Systemd units and examples below use the R1
spelling.

The daemon internally manages per-repository coordinator sidecars
(accelerator only: single linearizable publication lane per repo,
idle-TTL expiry, at-least-once delivery with idempotent replays keyed
by `(session id, request id)` against the lane receipt ledger; see
`ruk/docs/coordinator.md`). Coordinators are not separately configured,
supervised, or socketed by rukie; all coordinator behavior is covered
by the daemon's config and health probe. No ops surface exposes
`RUK_COORDINATOR` except pass-through for debugging (`off` only).

A critical source fact constrains §5: the v1 `snapshot` method records
a **workspace's current content as its change** — `SnapshotArgs {
binding }` → `SnapshotReply { outcome, change, changed_paths, state }`
(`ruk/crates/ruk-core/src/protocol.rs`), dispatched as a **mutation**
(`Method::Snapshot => mutation_request(...)` in
`ruk/crates/ruk/src/daemon/mod.rs`), and the current daemon refuses it
on covered paths. It is a working-copy operation, not a backup export,
and MUST NOT be used anywhere in backup/restore. An immutable
snapshot-export is **future R2 work** (§5.2 step 2).

---

## 1. Configuration file format

Single file, default path `/etc/rukie/config.toml`, overridable by
`--config` flag and `RUKIE_CONFIG` env (flag wins). Exactly one config
source per process; no `app.ini` layering, no env-per-key overrides
except `RUKIE_CONFIG` and secrets documented below.
`rukie serve --check-config` parses, validates, prints effective config
(`--show-effective` redacts secrets), and exits 0/2 without opening
ports, the store, or the daemon.

### 1.1 Top-level

| Key | Type | Default | Notes |
|---|---|---|---|
| `data_dir` | string (path) | **required**, no default | All state in §4. Created `0700` if missing. |
| `secret_key_file` | string (path) | **required**, no default | 32-byte raw file; `0600`, owned by service user. Session/token/webhook-signing master secret. |
| `log` | table | `{}` | §1.2 |
| `http` | table | `{}` | §1.3 |
| `ssh` | table | `{}` | §1.4 |
| `store` | table | `{}` | §1.5 |
| `ruk` | table | `{}` | §1.6; embeds daemon keys mirroring `DaemonConfig::from_toml` |
| `auth` | table | `{}` | §1.7 |
| `oidc` | array of tables | `[]` | §1.8 |
| `ci` | table | `{}` | §1.9 |
| `mirrors` | table | `{}` | §1.10 |
| `webhooks` | table | `{}` | §1.11 |
| `rate_limit` | table | `{}` | §1.12 |
| `telemetry` | table | `{}` | §1.13 |

### 1.2 `[log]`

| Key | Type | Default | Notes |
|---|---|---|---|
| `level` | string | `"info"` | One of `debug,info,warn,error`. `debug` never logs secret/token/password bytes. |
| `format` | string | `"json"` | Only `json` in production; `text` allowed for dev. slog JSON, one event per line, keys `time,level,msg,op,req_id,...` (§7). |
| `source` | bool | `false` | Add `source=file:line` (dev debugging; off in prod for throughput). |

### 1.3 `[http]`

| Key | Type | Default | Notes |
|---|---|---|---|
| `listen` | array of string | `["127.0.0.1:3000"]` | One or more `host:port`. First entry is canonical (used in absolute URLs, health logs, and the §3.4 admin proxy). Binding `0.0.0.0` allowed, logged at warn. Host installs keep loopback default; the §9 container image ships an image-default config overriding this to `["0.0.0.0:3000"]` so the published port has a listener. |
| `external_url` | string (URL) | `""` (derive from first listen + Host header) | Public base URL for webhook payloads, clone URLs, OIDC redirect. **Required** when OIDC or webhooks are enabled. |
| `behind_proxy` | bool | `false` | Trust `X-Forwarded-For/Proto` only from `trusted_proxies`. |
| `trusted_proxies` | array of string (CIDR) | `[]` | E.g. `["10.0.0.0/8"]`. Empty + `behind_proxy=true` = startup error. |
| `read_timeout_ms` | uint | `15000` | `net/http` ReadTimeout. |
| `read_header_timeout_ms` | uint | `10000` | ReadHeaderTimeout; must be `<= read_timeout_ms`. |
| `write_timeout_ms` | uint | `60000` | WriteTimeout. Streaming archives/browsing use flushing; long responses bounded by explicit byte caps where applicable, not by shortening this. |
| `idle_timeout_ms` | uint | `120000` | IdleTimeout. |
| `max_request_body_bytes` | uint64 | `33554432` (32 MiB) | Applies to all non-push HTTP bodies; push bodies flow through the Ruk pack path with its own credit windows. |
| `shutdown_drain_ms` | uint | `30000` | §3.3. |

### 1.4 `[ssh]`

rukie owns host keys and auth; Ruk owns pack bytes (settled: Git wire
served by Ruk pack code through the daemon).

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `false` | Off unless keys are configured. |
| `listen` | array of string | `["0.0.0.0:2222"]` (used only if `enabled`) | Same shape as `http.listen`. |
| `host_key_files` | array of string (paths) | `[]` | **Required if `enabled`.** At least one Ed25519 host key. RSA optional. Files `0600`. Missing file = startup error (never autogenerate silently; `rukie admin ssh-keygen` generates). |
| `max_sessions` | uint | `128` | Concurrent SSH sessions; excess refused with `ssh: too many sessions`. |
| `auth_timeout_ms` | uint | `30000` | Whole-auth deadline per connection. |
| `idle_timeout_ms` | uint | `300000` (5 min) | Kill idle session past deadline. |
| `shell` | string | `"internal"` | Only value. No shell escape; exec requests other than `git-upload-pack/receive-pack '<owner>/<repo>.git'` are rejected. Documents the cut Gitea builtin-SSH surface. |

### 1.5 `[store]` (metadata store; backend hidden behind one module, Phase K3)

| Key | Type | Default | Notes |
|---|---|---|---|
| `backend` | string | `"sqlite"` | One of `sqlite,pebble,postgres`. Non-SQLite backends compile in; selection is config-only. |
| `sqlite.path` | string (path) | `"<data_dir>/meta.sqlite"` | Ignored unless `backend=sqlite`. Pure-Go driver (no CGO), WAL mode. |
| `sqlite.max_open_conns` | uint | `16` | Pool cap; single-writer enforced by module regardless. |
| `sqlite.busy_timeout_ms` | uint | `5000` | `PRAGMA busy_timeout`. |
| `pebble.cache_bytes` | uint64 | `67108864` (64 MiB) | Block cache; ignored unless `backend=pebble`. Path is always `<data_dir>/meta.pebble`. |
| `postgres.dsn` | string | `""` | **Required if `backend=postgres`.** Never logged. `postgres://user:pass@host:5432/rukie?sslmode=require`. Password preferably via `postgres.dsn_file`. |
| `postgres.dsn_file` | string (path) | `""` | File holding DSN, `0600`. If set, `postgres.dsn` must be empty. |
| `postgres.max_open_conns` | uint | `16` | |
| `postgres.max_idle_conns` | uint | `4` | |
| `postgres.conn_max_lifetime_ms` | uint | `300000` (5 min) | |

### 1.6 `[ruk]` (Ruk daemon supervision + client)

rukie generates the daemon TOML from these keys and supervises the
child (§2). The pass-through keys mirror `DaemonConfig::from_toml` in
`ruk/crates/ruk/src/daemon/mod.rs`: from the daemon's perspective
`socket_path` and `repo_roots` are **required with no implicit default**
(missing either is `Error::Config`); from rukie's perspective they
default by interpolation from `data_dir` as below, and the rendered
file always sets both explicitly. The seven optional daemon keys and
their defaults are copied exactly: `allowed_uids` (default `[]`, see
security note), `max_connections` (default `128`,
`DEFAULT_MAX_CONNECTIONS`), `blocking_pool_size` (default `16`,
`DEFAULT_BLOCKING_POOL_SIZE`), `handshake_timeout_ms` (default `5000`),
`frame_timeout_ms` (default `60000`), `stream_window_bytes` (default
`262144` = 256 KiB, `INITIAL_STREAM_WINDOW_BYTES` in
`ruk/crates/ruk/src/daemon/ipc.rs`, valid range `1..=16777216` =
`MAX_STREAM_WINDOW_BYTES`, enforced by `DaemonConfig::validate`),
`retention_interval_ms` (default `60000`; `0` disables sweeps).

| Key | Type | Default | Notes |
|---|---|---|---|
| `binary_path` | string (path) | `"ruk"` (lookup `$PATH`) | Absolute path recommended in production/systemd. Must support `serve --config <file>` (R1 contract; pre-R1 spelling `forgejo serve --config`, see §0). Startup probes version and fails fast on mismatch (§6). |
| `repo_root` | string (path) | `"<data_dir>/repos"` | Single Ruk repo root. Passed as the daemon's sole `repo_roots` entry. Multi-root is not exposed (one forge, one root). |
| `socket_path` | string (path) | `"<data_dir>/run/ruk.sock"` | Unix socket the child binds; parent dials. Under `data_dir/run` so backup (§5) naturally excludes it. Parent creates `run/` `0700` `rukie:rukie`; socket itself mode `0770` group `rukie` at most. |
| `startup_timeout_ms` | uint | `15000` | Wait for socket + handshake probe (§3.1). |
| `restart_max_attempts` | uint | `3` | Consecutive child-crash restarts before rukie exits non-zero (systemd `Restart=on-failure` then takes over). |
| `restart_backoff_ms` | uint | `1000` | Linear backoff × attempt number. |
| `allowed_uids` | array of uint32 | `[]` | Daemon `allowed_uids`. **Security effect: `[]` means every local UID may connect** (SO_PEERCRED still recorded per peer in `ruk/crates/ruk/src/daemon/ipc.rs`, but nothing is refused). Safe only with a `0700`-rooted socket dir. Production MUST set `[<rukie-uid>]` in this key (i.e. in `/etc/rukie/config.toml`, consumed by the rendered daemon TOML — there is intentionally no `allowed_uids` setting in the systemd unit itself); §10 deploys the pinned value. |
| `max_connections` | uint | `128` | Daemon `max_connections`. Shed accept beyond. Size with `[ssh].max_sessions` + HTTP pack concurrency. |
| `blocking_pool_size` | uint | `16` | Daemon shared-executor blocking permits. |
| `handshake_timeout_ms` | uint64 | `5000` | Daemon stream-zero handshake deadline. Must be nonzero. |
| `frame_timeout_ms` | uint64 | `60000` | Daemon per-frame deadline. Must be nonzero. |
| `stream_window_bytes` | uint64 | `262144` (256 KiB) | Daemon starting credit window per stream direction. Valid `1..=16777216`. |
| `retention_interval_ms` | uint64 | `60000` | Daemon periodic retention-sweep interval. `0` disables (tests only, never prod). |
| `client_dial_timeout_ms` | uint | `5000` | rukie→daemon dial + handshake budget per new pool connection (rukie-side, not daemon). |
| `client_pool_size` | uint | `32` | Max cached daemon connections. |
| `client_request_timeout_ms` | uint | `120000` (2 min) | Per-op deadline for ordinary methods; pack upload/download override via `client_pack_timeout_ms`. |
| `client_pack_timeout_ms` | uint | `1800000` (30 min) | Push/fetch/clone/mirror pack operations. |

### 1.7 `[auth]`

| Key | Type | Default | Notes |
|---|---|---|---|
| `password_min_length` | uint | `12` | Argon2id passwords. Shorter rejected with RFC 9457 `auth/weak-password`. |
| `argon2_memory_kib` | uint32 | `65536` (64 MiB) | Argon2id m. |
| `argon2_iterations` | uint32 | `3` | Argon2id t. |
| `argon2_parallelism` | uint8 | `4` | Argon2id p. Retune only with a migration note; stored hashes carry params so rotation is lazy. |
| `totp_issuer` | string | `"rukie"` | TOTP enrollment issuer label. |
| `totp_required` | bool | `false` | If true, password login without enrolled TOTP is refused (except first-setup and admin reset flows). |
| `session_ttl_hours` | uint | `720` (30 d) | Cookie session lifetime; sliding refresh. |
| `token_prefix` | string | `"ruk_"` | Scoped agent/user token prefix (secret part is crypto-random, hashed at rest; only prefix + last-4 stored in clear). |

### 1.8 `[[oidc]]` (zero or more providers)

| Key | Type | Default | Notes |
|---|---|---|---|
| `name` | string | **required** | Unique per file; slug used in `/login/oidc/<name>` and account-link rows. |
| `issuer` | string (URL) | **required** | OIDC issuer; discovery at `<issuer>/.well-known/openid-configuration`. |
| `client_id` | string | **required** | |
| `client_secret_file` | string (path) | **required** | File holding the secret (`0600`); never inline in TOML. |
| `redirect_url` | string (URL) | `"<external_url>/login/oidc/<name>/callback"` | Must be registered at the provider. Requires `http.external_url`. |
| `scopes` | array of string | `["openid","profile","email"]` | `openid` forced in even if omitted. |
| `allow_signup` | bool | `true` | If false, only already-linked accounts may log in. |
| `admin_allow_list` | array of string | `[]` | OIDC `sub` values granted admin on first link (bootstrap convenience; empty = no auto-admin). |

### 1.9 `[ci]`

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `true` | Master switch; runners idle-drain when flipped off + SIGHUP (no kill of running jobs). |
| `default_queue` | string | `"default"` | Queue name jobs land on when unspecified. |
| `max_concurrent_jobs` | uint | `8` | Scheduler cap across all local runners. Queue depth beyond is backpressure (429 + `Retry-After` on submit, never silent drop). |
| `job_timeout_ms` | uint64 | `3600000` (1 h) | Kill + mark failed past deadline. |
| `log_retention_days` | uint | `90` | CI logs/results GC horizon. |
| `result_cache_bytes` | uint64 | `10737418240` (10 GiB) | Content-addressed result cache cap (§4 layout `cas/`); LRU evict. `0` disables cache (correct but slow). |
| `attest_on_success` | bool | `true` | Record results as Ruk attestations (settled feature). `false` is a debug escape hatch. |
| `[ci.runner]` | table | `{}` | Local runner config (same host as `rukie serve`; remote runners are out of V1). `{}` = run zero local runners (scheduler still accepts jobs; an external runner or a later config change drains them). Child keys below apply per local runner. |
| `ci.runner.binary_path` | string (path) | `"rukie-runner"` (`$PATH`) | Same release as server; version-checked on register. |
| `ci.runner.concurrency` | uint | `2` | Jobs per runner. |
| `ci.runner.work_dir` | string (path) | `"<data_dir>/ci-work"` | Sandboxed below; read-only repo snapshot + writable scratch. |
| `ci.runner.sandbox` | string | `"auto"` | `auto,none,ns,container`. `auto` = strongest available (user+mount+net namespaces; falls back loudly in logs, never silently to `none` when `auto`). |
| `ci.runner.network` | string | `"off"` | `off` default (egress-deny). `allow` only for explicit mirror/publish jobs. |
| `ci.runner.env_allow_list` | array of string | `[]` | Extra host env names passed through; everything else scrubbed. |

### 1.10 `[mirrors]`

| Key | Type | Default | Notes |
|---|---|---|---|
| `reconcile_interval_ms` | uint64 | `300000` (5 min) | Periodic reconcile per enabled mirror, staggered ±20% jitter. `0` disables periodic (push-triggered still fires). |
| `max_concurrent_pushes` | uint | `4` | Across all mirrors; per-mirror serialization regardless. |
| `push_timeout_ms` | uint64 | `600000` (10 min) | Per-mirror-push deadline; uses daemon push with caller-injected ephemeral credentials (R4), never stored creds on disk beyond the secret store. |
| `max_retries` | uint | `5` | Exponential backoff with jitter; after exhaustion the mirror is `degraded` (visible, webhook `mirror.degraded`). |

Per-mirror mode/ref-selection are per-repo settings (op table), not
global config.

### 1.11 `[webhooks]`

Standard Webhooks signed deliveries (settled).

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `true` | |
| `max_targets_per_event` | uint | `64` | Fan-out cap; excess targets get `webhook.dropped` ledger entry. |
| `delivery_timeout_ms` | uint64 | `10000` | Per-attempt POST deadline. |
| `max_retries` | uint | `8` | With exponential backoff + jitter; then dead-letter. |
| `max_payload_bytes` | uint64 | `1048576` (1 MiB) | Payloads beyond are delivered as event-stub + fetch-URL (never truncated JSON). |
| `signing_key_file` | string (path) | defaults to `secret_key_file` | Separate HMAC key for `webhook-signature` header (Standard Webhooks). `0600`. |
| `queue_depth` | uint | `4096` | Pending-delivery queue; full → shed new webhook events with metric + log (never block the mutating op). |

### 1.12 `[rate_limit]`

Token-bucket per key, enforced in-process before the op table.

| Key | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `true` | |
| `requests_per_minute` | uint | `600` | Per authenticated principal (user/token) across HTTP API + MCP. |
| `anonymous_per_minute` | uint | `60` | Per source IP for unauthenticated reads. |
| `push_per_minute_per_repo` | uint | `30` | Git push operations per repo (anti-flap). |
| `burst` | uint | `50` | Bucket burst above the per-minute rate. |
| `ban_minutes` | uint | `0` | `0` = never ban, only 429. Nonzero = temp-ban repeat 429 offenders (logged + metric). |

Responses: `429 application/problem+json` (RFC 9457) with
`Retry-After` seconds. MCP tools surface the same as a typed error.

### 1.13 `[telemetry]`

| Key | Type | Default | Notes |
|---|---|---|---|
| `otlp_endpoint` | string (URL) | `""` | Empty = OTLP exporter disabled (logs + `/metrics` still live). `http://localhost:4318` typical. |
| `otlp_protocol` | string | `"http/protobuf"` | `http/protobuf` or `grpc`. |
| `service_name` | string | `"rukie"` | OTel `service.name`. |
| `environment` | string | `"prod"` | `deployment.environment` resource attr. |
| `trace_sample_ratio` | float | `0.1` | `0.0..1.0`. High-volume pack ops sampled at this ratio; errors always sampled. |
| `metrics_addr` | string | `""` | Empty = serve `/metrics` on the main HTTP listeners. Set `127.0.0.1:9090` to split scrape traffic. |

### 1.14 Example (minimal production)

```toml
data_dir = "/var/lib/rukie"
secret_key_file = "/etc/rukie/secret.key"

[http]
listen = ["127.0.0.1:3000"]
external_url = "https://forge.example.com"

[store]
backend = "sqlite"

[ruk]
binary_path = "/usr/lib/rukie/ruk"
repo_root = "/var/lib/rukie/repos"
allowed_uids = [993]

[auth]
totp_required = true
```

Generated daemon TOML (never hand-edited; written to
`<data_dir>/run/ruk.toml` each start from `[ruk]`):

```toml
socket_path = "/var/lib/rukie/run/ruk.sock"
repo_roots = ["/var/lib/rukie/repos"]
allowed_uids = [993]
max_connections = 128
blocking_pool_size = 16
handshake_timeout_ms = 5000
frame_timeout_ms = 60000
stream_window_bytes = 262144
retention_interval_ms = 60000
```

---

## 2. Process topology decision

**Decision: rukie supervises a child `ruk serve` daemon process.**
systemd manages only `rukie`; `rukie` starts, health-gates, restarts
(within bounds), and stops exactly one daemon child. There is no
separately-managed sibling unit. (Pre-R1 the child argv spells the
subcommand `forgejo serve`; §0 covers the probe-and-pick transition.
Every "child `ruk serve`" below means whichever spelling the paired
binary advertises.)

### Why the supervised child wins

1. **Single versioned unit.** Packaging ships `rukie` + `ruk` binaries
   from the same release (§9). The parent execs its paired child and
   version-gates it (`ruk version` + handshake). A sibling unit can
   drift across partial upgrades (new server, old daemon) and serve
   wrong semantics with a green systemd status. The child makes skew
   structurally impossible.
2. **One config, one validation.** All daemon keys live in
   `rukie.toml [ruk]`; the parent renders the daemon TOML and passes
   `--config`. A sibling needs two files, two reload paths, and a
   second validation story. Every extra config seam is an outage seam.
3. **Socket lifecycle without races.** Parent creates `<data_dir>/run/`
   (`0700` `rukie:rukie`), removes a stale socket (only after proving
   no live daemon via connect-probe + PID liveness, never blind
   unlink), starts the child, and blocks `/readyz` until the handshake
   probe succeeds. Siblings race on `After=`/`Requires=` and on
   stale-socket cleanup after unclean shutdown.
4. **Coordinated graceful drain.** Shutdown order (§3.3) — stop
   accepting, drain HTTP/SSH, quiesce CI dispatch, flush store, then
   SIGTERM the child — needs one brain. Two units implement the same
   ordering across a process boundary with timeouts that can disagree.
5. **Credential hygiene.** Push/mirror paths inject ephemeral
   credentials per-call through the daemon IPC (R4). A supervised
   child bound to a `0700` socket dir with `allowed_uids=[rukie-uid]`
   has no ambient authority and no independent network identity to
   steal. A sibling invites operators to "just curl the daemon" and
   widen its exposure.
6. **Crash semantics stay boring.** Child crash → parent marks
   `/readyz` unready, fails in-flight daemon calls fast with RFC 9457
   `ruk/unavailable`, restarts up to `restart_max_attempts` with
   backoff, then exits so systemd restarts the whole unit cleanly.
   Sibling restarts hide half-dead states (server up, daemon flapping)
   behind two independent restart counters.

Rejected alternative (systemd sibling): better CPU/memory isolation
bookkeeping and independent `systemctl restart ruk-daemon` are real but
minor; cgroup accounting works per-process under one unit too, and
independent daemon restarts are *undesirable* — any daemon restart must
drain through the parent anyway.

Consequence for Ruk (R1): `ruk serve` must be foreground-by-default
(no self-daemonizing), accept `--config <toml>`, SIGTERM-drain
in-flight work, and exit `0` clean / `2` config error / `1` runtime
failure, so the parent can classify without parsing stderr.

---

## 3. Startup / shutdown order and graceful drain

### 3.1 Startup sequence (fail-fast, in order)

1. Parse + validate config (`--check-config` path included). Unknown
   keys, bad CIDRs, empty `data_dir`, missing `secret_key_file`
   (wrong perms are also fatal: must be `0600` or stricter),
   `behind_proxy` without `trusted_proxies`, `external_url` missing
   while OIDC/webhooks enabled → exit 2, log `config.invalid`.
2. `data_dir` layout ensure (§4): create missing dirs `0700`,
   refuse to start if `meta.*` exists but schema marker is newer than
   the binary (§6, downgrade refusal).
3. Run store migrations (§6) to the code-pinned version. Migration
   failure → exit 1, no ports opened.
4. Load/verify `secret_key_file` (32 bytes; anything else → exit 2).
5. Render `<data_dir>/run/ruk.toml`, spawn child
   `ruk serve --config` (or transitional `ruk forgejo serve --config`),
   wait up to `ruk.startup_timeout_ms` for socket accept + handshake
   probe. Probe failure → exit 1 (log child stderr tail, capped 4 KiB).
6. Open store pool, warm permission cache (§3.4), start mirror
   reconciler, webhook dispatcher, CI scheduler (paused until ready).
7. Bind HTTP (and SSH if enabled), flip `/readyz` true, log
   `rukie.ready version=<v> daemon=<ruk-v> store=<backend>@<ver>`.

If any step fails, previously-started pieces are torn down in reverse
(child SIGKILL after 5 s SIGTERM grace during *startup*; no drain —
nothing is serving yet).

### 3.2 Readiness gating

- `/healthz` (§7) is true as soon as the HTTP listener exists
  (in-process only).
- `/readyz` requires: store ping OK + daemon handshake probe OK +
  migrations at current version + (SSH bound if enabled). CI scheduler
  and mirror reconciler start dispatching only after `/readyz` flips.
- During child restarts (§2), `/readyz` flips false immediately;
  existing HTTP keep-alives stay open but new daemon-dependent ops get
  `503 ruk/unavailable` + `Retry-After: 5`.

### 3.3 Shutdown / drain (SIGTERM, then SIGINT = faster)

1. SIGTERM → stop accepting new HTTP/SSH connections; return `503`
   on new ones during drain (load balancers shift away).
2. SSH sessions: refuse new execs, wait for in-flight pack ops up to
   `http.shutdown_drain_ms` (pack ops can be long; 30 s default is a
   floor — operators raising `client_pack_timeout_ms` should raise
   drain proportionally).
3. HTTP: `srv.Shutdown(ctx[drain])`; streaming archives finish or are
   cut at deadline (client retries with offset params).
4. CI scheduler: pause dispatch, let running jobs finish up to
   `ci.job_timeout_ms` remainder but no longer than drain + 60 s;
   past that, jobs are checkpointed `paused` (requeued on next start,
   never silently marked failed).
5. Webhook dispatcher: flush in-memory queue to store-backed outbox
   (delivery retries resume after restart; nothing acknowledged-but-
   unsent is lost).
6. Store: checkpoint (SQLite `PRAGMA wal_checkpoint(TRUNCATE)`),
   close pool.
7. Child: SIGTERM, wait 10 s, SIGKILL. Remove socket only if parent
   is exiting cleanly *and* no other process holds it (dev-mode
   shared `data_dir` guard).
8. Exit 0. Second signal (SIGINT during drain) halves remaining
   deadlines once, then proceeds — never strands systemd's
   `TimeoutStopSec` (unit sets 90 s; drain default 30 s leaves margin).

### 3.4 Admin mutations and the live permission cache

`rukie serve` holds an exclusive `data_dir` lock (`<data_dir>/run/lock`).
Every `rukie admin` mutating command (§8) first checks the lock:

- **Server live:** the command proxies through the live server —

[447 more lines in resource. Use :6031 to continue]
- **Server live:** the command proxies through the live server —
  loopback HTTP against the first `http.listen` entry (same machine,
  operator token via `RUKIE_ADMIN_TOKEN` env or `--token` flag, never
  the config file) — so the mutation flows through the op table, bumps
  the grant epoch, and lands in the audit log exactly like a UI/API
  mutation. Direct-store writes while the lock is held are refused
  (exit 3, `admin/server-live`).
- **Server offline:** the command mutates the store directly; the next
  `serve` start warms caches from the store, so nothing stale survives
  a restart.

Cache rules in the live server:

- Permission grants cache in-process with a 30 s TTL, keyed by
  `(principal, repo, permission)` plus the global **grant epoch**;
  every grant-affecting mutation (role change, disable/enable,
  repo visibility, team membership) bumps the epoch, so cached grants
  invalidate immediately — a disabled user cannot ride a stale grant.
- Session/token **revocations are never cached**: every request checks
  the live revocation set. `user disable` and `token revoke` therefore
  take effect on the next request, not at TTL expiry.
- SIGHUP reloads tunables (log level, rate limits, webhook/CI queue
  settings) only; grants, revocations, and keys are never SIGHUP-gated.

---

## 4. Data directory layout

All under `data_dir` (`0700`, `rukie:rukie`). Nothing outside except
config, secrets, and stdout/stderr logs.

```
<data_dir>/
  meta.sqlite{,-wal,-shm}   # backend=sqlite (or meta.pebble/ dir; postgres: only local cache here)
  meta.version              # code-pinned schema marker (text, e.g. "7"); see §6
  repos/                    # Ruk repo root ([ruk].repo_root); ONLY the daemon writes here
  blobs/                    # release artifacts, content-addressed sha256:<hex> (2-level fanout)
  cas/                      # CI result cache, content-addressed, bounded by ci.result_cache_bytes
  ci-work/                  # runner scratch (ci.runner.work_dir); wiped per job, never backed up
  ci-logs/                  # retained job logs (ci.log_retention_days horizon)
  webhook-outbox/           # durable pending deliveries (store rows preferred; dir is spillover)
  mirrors/                  # per-mirror state (last reconciled OID map, ephemeral cred cache 0600)
  tmp/                      # uploads, archive staging; cleaned on start (older than 24 h)
  backups/                  # default output dir for `rukie admin backup`
  run/                      # EXCLUDED from backups: ruk.sock, ruk.toml (rendered), lock, *.pid
```

Rules: `repos/` is daemon-owned — admin tools and CI read it only via
daemon methods (or read-only snapshots), never direct file mutation.
`run/` is ephemeral. Everything else is store- or content-owned and
covered by §5.

---

## 5. Backup and restore

### 5.1 What "consistent" means

Two systems advance independently: the metadata store (tasks, changes,
users, mirrors, outbox) and the Ruk store (`repos/`). A backup is a
**paired snapshot** `(meta@t_M, ruk@t_R)` plus a manifest recording
both timestamps, versions, and content hashes. Restore replays forward:
Ruk is the source of truth for repository bytes; the metadata store is
reconciled against it (never the reverse). `t_M` and `t_R` MUST denote
one linearizable point, not two independent timestamps — hence the
write barrier in §5.2 step 0. Without it, restore could pair metadata
rows with refs that never coexisted.

A further constraint: an op-log position marker alone does NOT make
filesystem copying atomic. Packs and refs copied from a live `repos/`
while writes continue can be torn (mixed generations), and no replay
position repairs torn files at restore. Hence the copy phase (§5.2
step 3) MUST read from an immutable source, never from live `repos/`.

### 5.2 Online backup (`rukie admin backup`)

0. **Write barrier (required, before both snapshots).** Set a
   process-wide `backup_in_progress` flag: block *new* mutating ops
   (push, change write/split/squash/rebase/abandon/resolve, task-claim
   mutations, attestation writes, mirror state advances) with `503
   backup/in-progress` + `Retry-After: 10`; wait for in-flight
   mutations to commit or roll back (bounded by
   `client_request_timeout_ms`; long pack pushes are the bound —
   barrier wait is logged, and past 60 s the backup aborts rather than
   stalling the forge). Reads, CI polling, and webhook deliveries
   continue. Only when zero mutations are in flight, capture the
   journal boundary: the metadata WAL position and the per-repo daemon
   `op.log` tail positions — these jointly define `(t_M, t_R)`. The
   barrier is held through steps 1–2 (both fast) and released before
   step 3.
1. **Metadata snapshot (under the barrier):** take the snapshot with
   the backend-native online primitive —
   SQLite: `VACUUM INTO '<backups>/<ts>/meta.sqlite'` (works under WAL
   with readers active); Pebble: `Checkpoint()`; Postgres:
   `pg_dump --format=custom` to the staging dir.
2. **Ruk positions (under the barrier; strictly read-only).** For every
   repo, query the daemon's `op.log` tail (read-only) and record the
   position. This query MUST NOT be the v1 `snapshot` method: `snapshot`
   records a workspace change (`SnapshotArgs{binding}` →
   `SnapshotReply{outcome, change, changed_paths, state}`) and dispatches
   as a mutation, so calling it here would append an op *past* `t_R`
   and corrupt the boundary; moreover the current daemon refuses it on
   covered paths. Wherever a read takes a workspace snapshot policy
   (`SnapshotPolicy` in `ruk/crates/ruk-core/src/protocol.rs`), pass
   `skip`, so the position query itself appends no op. R2 MUST add a
   dedicated read-only snapshot-export (materializing an immutable
   per-repo view plus its op-log position); until it exists, step 3
   uses source A or B below.
3. **Copy phase (barrier released; reads come from an immutable source
   only).** Two supported sources — the operator's choice is recorded
   in the manifest (`snapshot_source`); copying live `repos/` with the
   barrier released is **forbidden** (torn packs/refs):
   - **A. Filesystem atomic snapshot (preferred until the R2 export
     exists).** Snapshot the `repos/` filesystem (ZFS/btrfs/LVM snapshot
     to an operator-managed staging path outside `data_dir`), copy from
     the frozen snapshot into `<backups>/<ts>/repos/` with per-file
     sha256, then destroy the snapshot. Barrier hold stays sub-minute;
     the slow copy runs concurrently with resumed mutations.
   - **B. Offline copy.** Stop `rukie serve`, copy `repos/` directly,
     restart. Required when neither the R2 export nor a filesystem
     snapshot facility is available.
   Stream `blobs/` similarly (content-addressed, so incremental
   `rsync --ignore-existing` is safe). Exclude `run/`, `ci-work/`,
   `tmp/`. Mutations landing after the boundary proceed concurrently
   (source A) and are reconciled forward from the recorded op-log
   positions at restore — the snapshot contents themselves cannot tear
   because their source is frozen.
4. **Manifest:** write `manifest.toml` with
   `{rukie_version, ruk_version, schema_version, t_M, t_R, barrier_id,
   barrier_hold_ms, snapshot_source, per-repo op-log positions, sha256
   of meta snapshot, file count}`. Then `fsync` the directory and log
   `backup.complete` (including barrier hold time in ms for SLO
   tracking).
5. Scheduler- or cron-driven (systemd timer in §10; no built-in cron
   daemon inside `serve`).

Typical small-site cost: one sub-minute barrier + one `VACUUM INTO` +
incremental snapshot copy; no maintenance window.

### 5.3 Restore (`rukie admin restore <dir>`)

Offline-only: refuses if any `rukie serve` holds the `data_dir` lock.

1. Verify manifest hashes; refuse on mismatch (explicit `--force`
   required to proceed past warnings, and then only logs loudly).
   Refuse manifests without a `barrier_id` (pre-barrier legacy
   backups): they cannot prove `t_M`/`t_R` coexisted — restore only
   with `--force` followed by a full `reconcile` and operator review.
2. Restore `repos/` first, then `blobs/`, then the metadata snapshot
   into place.
3. Start store, run `rukie admin reconcile --from-manifest`:
   for each repo, compare stored refs/change-ids against daemon
   `status/log` starting from the manifest's op-log positions;
   re-create missing repo rows, mark changes whose Ruk commits
   vanished (operator deleted repos out-of-band) as `orphaned`
   (never resurrected from stale metadata), replay post-boundary
   mutations forward, and replay webhook outbox entries newer than
   `t_M`. **Replay key:** every replayed mutation reuses its original
   `(session id, request id)`, so the per-repo lane receipt ledger
   deduplicates it ("same session, same id"; `ruk/docs/coordinator.md`,
   "Idempotent replays") — a replayed op applies once, never twice.
4. Run migrations if the backup schema is older (§6 allows upgrade-on-
   restore; downgrade past the binary is refused).
5. Print a reconcile report; operator confirms before `rukie serve`
   starts. RPO = last backup; RPO for repo bytes can be tightened to
   ~zero by combining backups with push mirrors (§1.10) to an
   independent remote.

### 5.4 `gc`

`rukie admin gc [--dry-run]` coordinates both stores: daemon-side
object compaction/prune per repo (via Ruk gc/retention paths, honoring
pins and `retention_interval_ms`) followed by metadata GC (expired CI
logs past `log_retention_days`, dead-lettered webhooks past 30 d,
orphaned blob rows unreferenced for 7 d). `--dry-run` prints bytes
reclaimable and rows affected; default prints before/after. Never runs
automatically inside `serve` (explicit op only).

---

## 6. Upgrades and schema migrations

- Migrations live in the `rukie` binary, versioned monotonically,
  recorded in `<data_dir>/meta.version` and a `schema_migrations`
  table (version, checksum, applied_at). On every start, `serve`
  migrates **up** to the code-pinned version before opening ports.
- Each migration is transactional per backend (SQLite: single
  transaction; Postgres: single transaction; Pebble: batch + marker),
  with a pre-migration online backup prompt: if `backups/` has no
  snapshot newer than 24 h, log `upgrade.no-recent-backup` at warn
  (never block unattended restarts on it).
- **Downgrade refusal:** if `meta.version` on disk is *newer* than the
  binary's pinned version, `serve` and all `admin` mutating commands
  exit 3 with `upgrade.downgrade-refused have=<disk> want=<bin>`.
  Override: none (restore an older backup instead). Read-only
  inspection commands (`admin status`) still work.
- Ruk compatibility: `serve` startup runs `ruk version`, requires
  `ruk_version >= minimum_ruk_version` compiled in (bumped by R-lane
  changes), and logs both versions at `rukie.ready`. Daemon protocol
  breakage is a major-version event: refuses to start with
  `ruk.incompatible` rather than serving half-working methods.

---

## 7. Observability

### 7.1 Logs

- `slog` JSON to stdout/stderr only (no log files; journald/docker
  own rotation). One JSON object per line. Required keys:
  `time, level, msg`; contextual keys `op` (op-table name),
  `req_id` (per-request UUID, propagated to daemon calls and webhook
  deliveries), `repo`, `principal`, `dur_ms`, `code` (RFC 9457 code on
  errors). Secret/token/password/OIDC-secret bytes are never logged;
  a `redactKeys` allowlist test guards this.
- Levels: `debug` (per-frame daemon chatter excluded even here —
  too hot; gated behind a separate `RUKIE_IO_DEBUG=1` env), `info`
  (requests at 1 line each, lifecycle events including
  `backup.barrier-hold-ms`), `warn` (degraded, retries, slow-op >1 s),
  `error` (5xx, daemon unavailable, migration failure).

### 7.2 Traces and metrics (OTLP)

- OpenTelemetry via OTLP (`telemetry.otlp_endpoint`,
  `http/protobuf` default). Tracer spans per op-table invocation,
  with child spans for `store.query`, `ruk.<method>`,
  `webhook.deliver`, `ci.schedule`, `backup.barrier`.
  `trace_sample_ratio` default 0.1; error spans always exported.
  Resource attrs: `service.name=rukie`,
  `deployment.environment`, `service.version`, `store.backend`.
- Metrics: OTel meter + a Prometheus-text `/metrics` (same process,
  or split via `metrics_addr`). Exemplars link to traces where the
  SDK supports it.

### 7.3 Key metrics (bounded cardinality — no per-user/per-repo label explosion)

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `rukie_http_requests_total` | counter | `op,code` | `op` = op-table name or `static`; `code` = status class (`2xx..5xx`). |
| `rukie_http_duration_ms` | histogram | `op` | Buckets `5,25,100,500,2000,10000`. |
| `rukie_ruk_calls_total` | counter | `method,code` | `method` = daemon method (`status,write,push,...` — bounded by protocol schema). |
| `rukie_ruk_call_duration_ms` | histogram | `method` | Same buckets. |
| `rukie_daemon_up` | gauge | — | 1 = handshake probe passing. Backs `/readyz`. |
| `rukie_store_up` | gauge | — | 1 = store ping passing. |
| `rukie_store_migration_version` | gauge | — | Current schema version (§6). |
| `rukie_ssh_sessions` | gauge | — | Current sessions; no per-key labels. |
| `rukie_ci_jobs_active` | gauge | `state` | `state ∈ {queued,running}`. |
| `rukie_ci_jobs_total` | counter | `result` | `result ∈ {success,failure,killed,timeout}`. |
| `rukie_ci_queue_depth` | gauge | `queue` | `queue` label bounded: configured queue names only (default 1). |
| `rukie_webhooks_pending` | gauge | — | Outbox depth. Alert > `queue_depth*0.8`. |
| `rukie_webhooks_deliveries_total` | counter | `result` | `result ∈ {delivered,retrying,dead_letter,dropped}`. |
| `rukie_mirrors_total` | counter | `result` | `result ∈ {ok,failed,degraded}`. No per-URL labels (host allowlist in logs only). |
| `rukie_rate_limited_total` | counter | `class` | `class ∈ {auth,anon,push}`. |
| `rukie_backup_last_success_unixtime` | gauge | — | Alert if stale > 26 h. |
| `rukie_backup_barrier_hold_ms` | histogram | — | Steps 0–2 barrier hold time; alert on p99 growth (mutation pile-up). |

Forbidden labels: user id/name, repo path, token prefix, IP,
user-agent, error message text. High-cardinality detail stays in
logs/traces keyed by `req_id`.

### 7.4 Health endpoints (unauthenticated, localhost-safe)

- `GET /healthz` — **liveness, in-process only.** Returns 200
  `{"status":"ok"}` iff the HTTP server is running. Checks nothing
  else (no store, no daemon) so orchestrators never kill a process
  that is alive but waiting on a dependency.
- `GET /readyz` — readiness. Checks store ping + daemon handshake
  probe + schema-current + SSH bound (if enabled). 200
  `{"ready":true,"store":"ok","daemon":"ok","schema":7}` or 503 with
  the failing check named. Used by load balancers and pre-publish
  smoke. Returns 503 with `backup/in-progress` named while the §5.2
  barrier is held so balancers drain mutations away rather than
  failing them.
- `GET /metrics` — Prometheus text (scrape; no auth by default when
  bound to loopback/`metrics_addr`, else behind admin auth — decision
  left to F-auth but default deny-remote).

---

## 8. Admin CLI

`rukie admin <subcommand>`; all subcommands take `--config` (same
resolution as `serve`) plus `--token` / `RUKIE_ADMIN_TOKEN` for the
§3.4 live-server proxy, and honor the downgrade refusal (§6).
Mutating subcommands are idempotent where marked (†). Machine output
via `--json` (stable keys); human tables by default. Exit codes:
`0` ok, `2` config/usage, `3` downgrade-refused/offline-guard /
`admin/server-live` (direct-store write attempted while the lock is
held), `1` runtime.

| Subcommand | Description |
|---|---|
| `admin user create --login <l> [--admin] [--password-stdin]` | † Create user; password read from stdin or TTY (never argv). Prints login + created_at (never the password). |
| `admin user reset-password --login <l> [--password-stdin]` | Set/replace password hash (argon2id params from `[auth]`); revokes sessions. Admin-only; users rotate their own via the op table. |
| `admin user disable --login <l>` / `admin user enable --login <l>` | † Lock/unlock login, token auth, OIDC link. Proxied live per §3.4: sessions revoked and grant epoch bumped before the command returns. |
| `admin token revoke --prefix <p> \| --user <l> --all \| --id <id>` | Revoke scoped tokens by prefix/user/all-for-user/id. Lists last-4 only. Proxied live per §3.4. |
| `admin repo import --from-git <url\|path> --owner <o> --name <n> [--default-branch <b>]` | Import a stock-git repo (clone → Ruk native store via daemon create + push path, R2/R4). Idempotent on `(owner,name)` with `--upsert` (else error if exists). Verifies HEAD + tag count after import. |
| `admin repo fork-local --owner <o> --repo <r> --to-owner <o2> [--name <n>]` | Server-side fork via daemon (no re-upload). |
| `admin gc [--dry-run] [--repo <o/r>]` | §5.4. Scoped to one repo or global. |
| `admin backup [--out <dir>] [--no-blobs]` | §5.2. Prints manifest path + hashes + barrier hold time. `--no-blobs` for metadata-only drills. |
| `admin restore <dir> [--force]` | §5.3. Offline-guarded (data-dir lock). |
| `admin reconcile [--from-manifest <dir>]` | Post-restore or ad-hoc metadata↔Ruk consistency pass; prints orphaned/created rows. |
| `admin status [--json]` | Read-only: versions (rukie, ruk, schema), `/readyz` equivalent, queue depths, last backup time. Safe during downgrade-refusal. |
| `admin ssh-keygen --out <file>` | Generate Ed25519 host key `0600` for `[ssh].host_key_files`. |
| `admin secret-gen --out <file>` | Generate 32-byte `secret_key_file` / webhook signing key `0600`. Refuses to overwrite without `--force`. |
| `admin migrate [--dry-run]` | Apply pending migrations offline (same code as startup path); `--dry-run` lists pending versions. |

Every mutating admin command appends an audit row (who/when/what/args-
redacted) to the metadata store, visible in the admin UI and via the op
table.

---

## 9. Packaging

- **Binaries:** two files from one release: `rukie` (Go,
  `CGO_ENABLED=0`, static) and `ruk` (Rust, musl static or
  `x86_64-unknown-linux-musl`; glibc-free so distroless runs it).
  New module path `github.com/gosuda/rukie` (K1). `go.mod` toolchain
  pinned; `cargo --locked` for `ruk`. Release artifacts include SBOM
  and `sha256sums.txt`.
- **Dockerfile (one, distroless, non-root, digest-pinned):**
  multi-stage — `golang:<pin>@sha256:<digest>` builds `rukie`,
  `rust:<pin>@sha256:<digest>` builds `ruk`, final
  `gcr.io/distroless/static-debian13:<digest>` (or equivalent). Every
  image reference is normatively `name:tag@sha256:<64-hex>` —
  digest-pinned, never a floating tag; the `<pin>`/`<digest>`
  placeholders in this design doc are filled with concrete values by
  the K-phase implementation (a design doc MUST NOT hardcode digests:
  any 64-hex written here would be stale-or-fabricated on arrival;
  fabrication is forbidden, so acceptance verifies the *form*
  `tag@sha256:hex` plus non-root/static properties in the delivered
  Dockerfile, not these placeholders). The final stage copies both
  binaries + a default `config.example.toml` whose image-default sets
  `http.listen = ["0.0.0.0:3000"]` (overriding the host-safe loopback
  default from §1.3, otherwise the published port has no listener).
  `USER 65532:65532` (`nonroot`),
  `ENTRYPOINT ["/usr/bin/rukie","serve"]`, `EXPOSE 3000`,
  `VOLUME /var/lib/rukie`,
  `HEALTHCHECK --interval=10s CMD ["/usr/bin/rukie","admin","status"]`.
  No shell, no package manager, no JS toolchain in the final image.
  Base digests are bumped by a scheduled PR, never floating tags.
- **Layout on disk (deb/tar parity):** `/usr/bin/rukie`,
  `/usr/lib/rukie/ruk`, `/etc/rukie/config.toml` (example on fresh
  install, never overwritten on upgrade), `/var/lib/rukie/` (data).

---

## 10. systemd units

One unit. The daemon is a child, not a sibling (§2). Peer lockdown for
the daemon socket lives in `/etc/rukie/config.toml`
(`[ruk].allowed_uids = [<rukie-uid>]`, rendered into the daemon TOML),
not in the unit file — the unit only guarantees the process identity
(`User=rukie`) that the pinned UID refers to.

`rukie.service`:

```ini
[Unit]
Description=rukie — agent-first Go forge (supervises ruk serve child)
Documentation=https://example.com/rukie/docs/ops
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=rukie
Group=rukie
UMask=0077
ExecStart=/usr/bin/rukie serve --config /etc/rukie/config.toml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5s
StartLimitBurst=5
StartLimitIntervalSec=120s
TimeoutStopSec=90s
# SIGHUP semantics: reload log level, rate limits, webhook/CI tunables.
# data_dir/store/ruk socket changes require a restart (logged loudly).
KillSignal=SIGTERM
AmbientCapabilities=
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/rukie /etc/rukie
ConfigurationDirectory=rukie
StateDirectory=rukie
LogsDirectory=rukie
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

`rukie-backup.timer` + `rukie-backup.service` (daily):

```ini
# rukie-backup.timer
[Unit]
Description=Daily rukie backup
[Timer]
OnCalendar=daily
RandomizedDelaySec=1800
Persistent=true
[Install]
WantedBy=timers.target
```

```ini
# rukie-backup.service
[Unit]
Description=rukie daily backup
[Service]
Type=oneshot
User=rukie
Group=rukie
ExecStart=/usr/bin/rukie admin backup --config /etc/rukie/config.toml
NoNewPrivileges=true
```

Notes: `StateDirectory=rukie` maps to `/var/lib/rukie` (matches
`data_dir` default); operators pointing `data_dir` elsewhere add an
override drop-in with `ReadWritePaths=`. `allowed_uids=[<rukie-uid>]`
in `rukie.toml [ruk]` + `ProtectSystem=strict` gives the daemon socket
exactly one legal peer. Fresh installs run `rukie admin secret-gen`
and `rukie admin user create --admin` before first start (documented in
the install runbook, not automated — admin creation must be an explicit
human act).

### Critical Files for Implementation

- `ruk/crates/ruk/src/daemon/mod.rs` — DaemonConfig keys/defaults/validate mirrored by `[ruk]` table
- `ruk/crates/ruk/src/daemon/ipc.rs` — Stream window default (256 KiB) and 16 MiB cap constraining `stream_window_bytes`
- `ruk/crates/ruk/src/main.rs` — Serve subcommand spelling and `--config` handling for the supervised child
- `rukie/go.mod` — New module path `github.com/gosuda/rukie`, Go toolchain pin, pure-Go deps for static build
- `rukie/Dockerfile` — Single distroless non-root digest-pinned image shipping both binaries


[advisor] <advisory advisor="overlycritic" severity="concern" turns_ago="1" guidance="weigh, don't blindly obey"> Reconcile path …

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Include webhook delivery ackn…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Move the filesystem snapshot into…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Limit the replay-dedupe claim to …

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Name an existing read method for …