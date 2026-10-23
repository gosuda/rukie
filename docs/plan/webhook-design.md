# Rukie Signed Webhooks — Design

## 1. Goals and non-goals

**Goals.** One signed-webhook implementation for the rewritten rukie (module path `github.com/gosuda/rukie`):

- Conform to [Standard Webhooks 1.0.0](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md) on the wire: headers `webhook-id`, `webhook-timestamp`, `webhook-signature`; `v1` HMAC-SHA256-base64 signatures over `msg_id.timestamp.payload-bytes`; `whsec_`-prefixed per-endpoint secrets.
- Cover every event the rewrite needs (catalogue in §4), with small thin JSON payloads.
- Durable at-least-once delivery: outbox rows committed in the same transaction as the domain event; a leased worker with jittered exponential backoff, a dead-letter state, and manual redelivery.
- Safe delivery: timeouts, bounded response capture, SSRF-proof dialing, no redirect following, per-endpoint concurrency limits, documented no-ordering, `webhook-id` idempotency.

**Non-goals / cut Gitea surfaces.** The Gitea implementation under `rukie/modules/webhook/` (`HookEventType` values such as `push`, `issues`, `pull_request_*`, `package`, `status`, `workflow_run`; `rukie/services/webhook/deliver.go` `X-Gitea-*` / `X-Gogs-*` / `X-Hub-Signature` hex headers; form-encoded / GET / PUT-Matrix variants; Slack/Discord/DingTalk/Telegram/MSTeams/Feishu/Matrix/WechatWork/Packagist requesters behind `webhookRequesters`; `PayloadVersion` 1/2 dual paths) is **deleted**, not adapted. Rukie v1 sends exactly one shape: `POST` + `application/json` + Standard Webhooks headers. No per-provider payload transformers, no `X-Gitea-*` compatibility headers, no `app.ini` `[webhook]` passthrough. Gitea's `hook_task` fire-and-forget queue (`rukie/services/webhook/webhook.go` `PrepareWebhooks` → `queue.WorkerPoolQueue[int64]` with `DeliverTimeout: 5`, `PagingNum: 10`, response body kept up to 1 MiB) is replaced by the outbox + leased worker below.

## 2. Wire contract (Standard Webhooks)

### 2.1 Headers

Every delivery is `POST <endpoint-url>` with:

| Header | Value |
|---|---|
| `webhook-id` | Unique per message, stable across retries: `msg_` + 26-char ULID (Crockford base32), e.g. `msg_01J9Z8X7Q6W5E4R3T2Y1U0I9O8`. Must not contain `.`. |
| `webhook-timestamp` | Attempt time, integer Unix seconds as ASCII (e.g. `1729450000`). Regenerated on every attempt (including retries); the event time lives inside the payload instead. |
| `webhook-signature` | Space-delimited list of one or two signatures (two only during secret rotation, §2.4). Each entry `v1,<base64(HMAC-SHA256(secret, canonical-bytes))>`. |
| `Content-Type` | `application/json; charset=utf-8`. The only content type. |

No other signing headers. In particular: no `X-Hub-Signature`, no hex encoding, no timestamp-in-body signing.

### 2.2 Canonical bytes and signature

For message with id `M`, attempt timestamp `T` (seconds), and exact HTTP body bytes `B` (minified JSON, no trailing newline):

```
canonical = M || "." || T || "." || B
sig       = base64_std(HMAC_SHA256(key = raw_secret_bytes, msg = canonical))
header    = "v1," + sig
```

- Sign the **bytes as sent**. JSON is serialized once (canonical encoder: `encoding/json` with `SetEscapeHTML(false)`, no indentation), stored in the outbox row, and the stored bytes are both signed and transmitted verbatim. Never re-serialize before signing.
- `M` and `T` are server-generated and never contain `.`.
- Verification guidance shipped to consumers (docs + `POST /api/v1/webhooks/:id/test` echo page): split `webhook-signature` on spaces, for each `v1,<b64>` entry compute the expected MAC and compare with `subtle.ConstantTimeCompare`; accept if any entry matches; then reject if `|now - T| > tolerance` (recommend 5 minutes); then dedupe on `webhook-id` (keep seen IDs ≥ 24 h). Parse-then-reserialize before verifying is a documented footgun: verify against the raw body.

### 2.3 Secrets (`whsec_`)

- Format: `whsec_` + base64-std (no padding issues: keep `=` padding) of **32 random bytes** from `crypto/rand`. 32 bytes sits inside the spec's 24–64 byte window and keeps every endpoint uniform. Example: `whsec_MfK... (43 chars)`.
- Generated server-side on endpoint creation and shown **once** in the create response / UI. Stored encrypted at rest (AES-256-GCM with the instance KEK, same mechanism as the rest of the store module's secret columns; never logged, never returned by read APIs — only a `secret_last4` fingerprint for display).
- Secrets are **unique per endpoint**. No global/shared webhook secret. Import paths must mint fresh secrets, never copy Gitea's plaintext `Secret` column (`rukie/models/webhook/webhook.go` `Webhook.Secret TEXT`).
- An optional `previous_secret_enc` + `previous_secret_expires_at` supports zero-downtime rotation (§2.4). At most two active secrets per endpoint.

### 2.4 Rotation

1. Operator calls `webhook.endpoint.rotate-secret` (or UI "rotate"): the current secret moves to `previous_secret_*` with a 24 h expiry; a fresh `whsec_` secret becomes current and is shown once.
2. During the overlap window the sender signs **twice** and emits both entries: `v1,<sig-current> v1,<sig-previous>`. Consumers trying each entry in turn see no downtime.
3. After expiry the worker drops the old signature and NULLs `previous_secret_*`. The rotate op is idempotent-safe: rotating twice in the window replaces the previous secret and re-shows the newest current secret once.

### 2.5 HTTPS policy

- Production default: **HTTPS endpoints only**. Plain `http://` endpoint creation is rejected unless the instance flag `webhooks.allow_insecure_http` is set (dev/test instances only). The flag is surfaced in the admin UI and the create-endpoint op error (`E_INVALID`, "http endpoints disabled").
- TLS verification is always on. There is no `SKIP_TLS_VERIFY` equivalent (Gitea's `setting.Webhook.SkipTLSVerify` is cut). A private-CA bundle may be configured instance-wide by the admin; it applies to all endpoints equally, never per endpoint.

## 3. Payload envelope

All events share one envelope (Standard Webhooks recommendation: `type` + `timestamp` + `data`):

```json
{
  "type": "ref.updated",
  "timestamp": "2026-10-20T08:14:03.120Z",
  "data": { "...event-specific thin fields..." }
}
```

- `type`: catalogue name from §4 (dot-delimited `[a-z0-9_]` segments).
- `timestamp`: **event** time (when the domain change committed), RFC 3339 Nano UTC. Distinct from the `webhook-timestamp` attempt header; retries keep `timestamp` fixed and rotate the header.
- `data`: thin fields only — IDs, names, OIDs/change-ids, URLs back into the API/UI. No file contents, no diffs, no full user records. Target: **< 20 KiB** per body; hard cap **256 KiB** (see §7). Consumers fetch details via the API/MCP with the provided IDs.
- Common `data` base present in every event: `repo { id, full_name, url }`, `actor { id, name, kind }` (`kind`: `human` | `token:<token-prefix>`; agent actions appear as the owning human's token, never a separate principal), plus `event_id` (server UUID for support correlation, distinct from `webhook-id`).

## 4. Event catalogue

Endpoint subscriptions are an explicit set of these names (`event_types[]`). No wildcards in v1 except the UI convenience "all" which expands to the full list at save time (so later-added event types do not silently start flowing). Every name below is the `type` field and the subscription key.

| # | `type` | Trigger (single outbox fan-out per trigger) | `data` fields |
|---|---|---|---|
| 1 | `ref.updated` | Any ref create/update/delete landing in Ruk (branch push, tag push/delete, force-update, mirror of a stock-git `refs/for/*` push resolving to changes — the ref event still fires). One message per ref per push batch. | `ref` (full, e.g. `refs/heads/main`), `ref_kind` (`branch`\|`tag`), `old_oid` (zero-OID `000…0` on create), `new_oid` (zero-OID on delete), `change_kind` (`created`\|`updated`\|`deleted`\|`force_updated`), `pusher`, `compare_url`, `branch_view_url` (for branches; groups that branch's open changes). Optional endpoint-side `ref_globs` filter (§5) applies only to this type. |
| 2 | `change.opened` | Ruk change (or pushed stack member) created, including each member of a `refs/for/<branch>` push that yields a change-id. | `change_id`, `version` (1), `title`, `branch`, `base_ref`, `author`, `change_url`, `branch_view_url`. |
| 3 | `change.updated` | New version of a change (rebase/amend/push) **or** title/description/base edit. Both bump `version`. | `change_id`, `version`, `previous_version`, `update_kind` (`commits`\|`metadata`\|`both`), `base_ref`, `candidate_oid`, `editor`, `change_url`. |
| 4 | `change.landed` | `land.execute` succeeds for a change. | `change_id`, `version`, `target_ref`, `landed_oid`, `base_oid_at_land`, `landed_by`, `change_url`. |
| 5 | `change.abandoned` | Change abandoned (explicit abandon op). | `change_id`, `version`, `abandoned_by`, `reason` (optional, ≤ 1 KiB), `change_url`. |
| 6 | `review.submitted` | Review (approval, request-changes, or comment-with-verdict) recorded on a change version. Plain comments without a verdict do **not** fire. | `change_id`, `version`, `review_id`, `verdict` (`approved`\|`changes_requested`\|`commented`), `reviewer`, `body_preview` (first 500 chars, may be empty), `review_url`. |
| 7 | `attestation.added` | Any Ruk attestation admitted (`attest.add`) outside the CI writer is still reported uniformly, including `kind: ci` results the CI pipeline records. | `subject_kind` (`change`\|`commit`), `subject` (change-id or OID), `attest_kind`, `result`, `signer_fingerprint`, `recorded_by`, `attestation_url`. |
| 8 | `task.created` | Task row created. | `task_id`, `task_number` (per-repo short number), `title`, `state` (`open`), `assignee` (nullable), `created_by`, `task_url`. |
| 9 | `task.claimed` | Ruk lease acquired for a task (`claim.acquire` through the task). | `task_id`, `task_number`, `holder` (human whose token holds the lease), `lease_id`, `lease_expires_at` (RFC 3339), `task_url`. |
| 10 | `task.closed` | Task closed with a reason. | `task_id`, `task_number`, `close_reason` (`completed`\|`duplicate`\|`wontfix`\|`abandoned`), `closed_by`, `task_url`. |
| 11 | `ci.run.state_changed` | Built-in CI run transitions: `queued → running → (passed \| failed \| cancelled)`. Retried runs keep the same `run_id` and bump `attempt`. | `run_id`, `attempt`, `repo_ref`, `commit_oid`, `change_id` (nullable), `pipeline`, `state`, `conclusion` (nullable until terminal), `duration_ms` (nullable), `run_url`, `attestation_digest` (present on terminal states; points at the Ruk attestation recording the result). |
| 12 | `release.published` | Release row published. | `release_id`, `tag`, `name`, `notes_preview` (first 1 KiB), `assets [{ name, size_bytes, url }]`, `published_by`, `release_url`. |
| 13 | `mirror.sync_failed` | A push-mirror sync to GitHub/GitLab/Forgejo fails (per-mirror, after the per-attempt retry inside the mirror worker gives up for that trigger; periodic-reconcile failures also fire). Successes do **not** fire. | `remote_name`, `remote_host`, `mode` (`exact`\|`fast_forward_only`), `refspec_summary`, `trigger` (`push`\|`reconcile`), `error` (sanitized, no creds, ≤ 2 KiB), `consecutive_failures`, `next_reconcile_at`, `repo_url`. The endpoint URL's credentials are never included. |
| 14 | `repo.created` | Repository created (including forks; `fork_of` set then). | `repo_id`, `visibility`, `default_branch`, `fork_of` (nullable full_name), `created_by`, `repo_url`. |
| 15 | `repo.deleted` | Repository deleted (tombstone payload; sent before row removal commits, delivered after). | `repo_id`, `full_name`, `visibility`, `deleted_by`, `last_default_tip` (nullable OID; informational only — the objects may be gone when the consumer reads this). |

Deliberately absent in v1: per-commit-comment events, per-line review threads, star/watch/fork-social events, inbox notifications, OIDC/account lifecycle. Social actions are API-queryable; they do not get webhook types until a proven consumer need.

## 5. Endpoint configuration model

One endpoint row per (scope, URL) subscription:

- `scope`: `repo` (one repository) or `owner` (user/org — fires for all current and future repos under it). No instance-global endpoints in v1 (admins use an owner scope on each org).
- `url`: `https://…` (http only with `allow_insecure_http`), max 2048 chars, no userinfo (`user@` rejected), only default or explicit ports 1–65535; DNS resolved at send time under the §8 policy.
- `event_types[]`: non-empty subset of §4. `ref_globs[]` (optional, only meaningful with `ref.updated`; gitignore-style globs matched against the full ref, e.g. `refs/heads/*`; empty = all refs).
- `active` bool, `concurrency` (1–16, default 4), `timeout_seconds` (5–60, default 20; bounded by instance max).
- `secret_enc` (+ rotation pair), `note` (operator label), `created_by`, timestamps, `last_status` (`never`|`ok`|`failing`|`disabled`), `consecutive_failures`, `disabled_at`/`disable_reason` (e.g. `410-gone`, `undeliverable`).

Ops surface (each is one `Op` row deriving HTTP + MCP + CLI + HTML form, per the operation-table contract): `webhook.endpoint.create`, `webhook.endpoint.update`, `webhook.endpoint.delete`, `webhook.endpoint.list`, `webhook.endpoint.get`, `webhook.endpoint.rotate-secret`, `webhook.endpoint.test` (sends a synthetic `endpoint.tested` ping with a fresh `webhook-id` to verify signing without touching the outbox), `webhook.message.list`, `webhook.message.get`, `webhook.message.redeliver`, `webhook.attempt.list`. Permission: repo endpoints need repo admin; owner endpoints need owner admin; redeliver needs the same scope's admin.

## 6. Store tables and transactional outbox

Hidden behind the single store module (`Store.Webhooks()` interface); identical logical schema on SQLite / PostgreSQL / Pebble (Pebble maps each table to a key-prefix collection with the same field sets).

### 6.1 `webhook_endpoints`

```
id               INTEGER PK
scope            TEXT  ('repo' | 'owner')
repo_id          INTEGER NULL  (set iff scope='repo')
owner_id         INTEGER NULL  (set iff scope='owner')
url              TEXT  (max 2048)
url_hash         BLOB  (SHA-256 of normalized URL; index for duplicate detection)
secret_enc       BLOB  (AES-GCM, nonce-prefixed)
prev_secret_enc  BLOB NULL
prev_secret_expires_at TIMESTAMPTZ NULL
event_types      JSON  (array of §4 names, validated)
ref_globs        JSON  (array of strings, max 32)
active           BOOL  (default true)
concurrency      SMALLINT (1..16, default 4)
timeout_seconds  SMALLINT (5..60, default 20)
note             TEXT  (max 256)
created_by       INTEGER (user id)
created_at / updated_at  TIMESTAMPTZ
last_status      TEXT  ('never'|'ok'|'failing'|'disabled')
consecutive_failures INTEGER (default 0)
disabled_at / disable_reason  NULL-able
```

Indexes: `(repo_id, active)`, `(owner_id, active)`, `url_hash`. Invariants enforced in the store layer: exactly one of `repo_id`/`owner_id` set per scope; `event_types` non-empty and drawn from §4; `ref_globs` empty unless `ref.updated ∈ event_types`.

### 6.2 `webhook_messages` (outbox + delivery log)

One row per (domain event × subscribed endpoint) — the fan-out is materialized at trigger time inside the same DB transaction as the domain mutation:

```
id               INTEGER PK
webhook_id       TEXT UNIQUE  ('msg_<ULID>'; the webhook-id header; stable across retries)
endpoint_id      INTEGER FK → webhook_endpoints (ON DELETE CASCADE for repo/owner removal)
event_type       TEXT  (§4 name)
event_time       TIMESTAMPTZ  (payload timestamp field)
payload          BLOB  (exact bytes sent & signed; JSON; ≤ 256 KiB)
state            TEXT  ('pending' | 'inflight' | 'delivered' | 'dead')
attempt_count    INTEGER (default 0)
next_attempt_at  TIMESTAMPTZ  (drives the worker poll; set by backoff table)
lease_owner      TEXT NULL  (worker instance id)
lease_expires_at TIMESTAMPTZ NULL
last_error       TEXT NULL  (≤ 2 KiB, sanitized)
last_status_code INTEGER NULL
created_at / updated_at / delivered_at (NULL-able)
redelivery_of    TEXT NULL  (webhook-id of the dead message this was re-driven from, if via redeliver-clone; NULL normally)
```

Indexes: `(state, next_attempt_at)` (the worker poll), `(endpoint_id, id)`, `(event_type, created_at)`. Retention: `delivered`/`dead` rows kept 30 days (instance-configurable 7–90), then hard-deleted by the janitor; `webhook_attempts` rows die with their message.

### 6.3 `webhook_attempts` (bounded per-message history)

```
id            INTEGER PK
message_id    INTEGER FK → webhook_messages (ON DELETE CASCADE)
attempt_no    INTEGER (1-based)
started_at / finished_at TIMESTAMPTZ
attempt_timestamp INTEGER (the webhook-timestamp header value used)
http_status   INTEGER NULL (NULL on transport error / policy denial)
error         TEXT NULL (sanitized; transport/policy/timeout text)
req_headers   JSON (redacted: signatures stored as "v1,<omitted>" presence flags, never values)
resp_headers  JSON (first 8 KiB, lowercase names)
resp_body     BLOB (first 64 KiB, remainder truncated with marker)
duration_ms   INTEGER
```

Only the latest 10 attempts per message are retained (older pruned on insert). This table is the debugging surface for `webhook.attempt.list` and the endpoint "recent deliveries" UI.

### 6.4 Transactional enqueue (the outbox rule)

Every domain mutation that fires §4 events (ref update commit, change transition, review insert, attestation admit, task insert/claim/close, CI state transition, release publish, mirror failure record, repo create/delete) **inserts the fanned-out `webhook_messages` rows in the same store transaction** that persists the mutation. If the transaction rolls back, no message exists. If it commits, the message is at least `pending` with `next_attempt_at = commit_time`. There is no separate "publish then enqueue" step and no in-memory queue in the enqueue path (the Gitea pattern of `PrepareWebhooks` pushing to `hookQueue` after commit — losable on crash — is the failure mode this replaces). A `repo.deleted` message is inserted in the deleting transaction before the repo row is tombstoned so the payload's `repo_id` still resolves at render time.

## 7. Delivery worker

### 7.1 Claim with lease

- N worker loops per instance (instance flag `webhooks.workers`, default 8; each loop handles one message at a time) poll every 250 ms–1 s: `SELECT … WHERE state='pending' AND next_attempt_at <= now ORDER BY next_attempt_at LIMIT 64 FOR UPDATE SKIP LOCKED` (Postgres) / equivalent atomic claim on SQLite/Pebble via the store interface; set `state='inflight', lease_owner=<instance-uuid>, lease_expires_at=now+5min`. A sweeper re-queues `inflight` rows whose lease expired (crashed worker) back to `pending` with the same `attempt_count`.
- At most `concurrency` in-flight messages per endpoint (counted in the claim query: skip endpoints already at their limit). Global in-flight cap `webhooks.global_max_inflight` (default 256) bounds total sockets.

### 7.2 Attempt execution

1. Load endpoint (skip if `!active` → requeue with backoff; skip if deleted → mark message `dead`/`undeliverable-endpoint-removed`).
2. Render headers: fresh `webhook-timestamp = now_seconds`; signatures over stored `payload` bytes with current secret (+ previous if unexpired).
3. `POST` with the §8 SSRF-safe client and the endpoint's `timeout_seconds` (clamped to instance max 60 s).
4. Record a `webhook_attempts` row; update message per §7.4.

### 7.3 Backoff, max attempts, dead-letter

Jittered exponential backoff (spec-compatible schedule, ±20 % uniform jitter applied per delay):

| Attempt just failed | Delay before next attempt | Cumulative ≈ |
|---|---|---|
| 1 (initial) | ~5 s | 5 s |
| 2 | ~5 min | 5 min |
| 3 | ~30 min | 35 min |
| 4 | ~2 h | 2 h 35 min |
| 5 | ~5 h | 7 h 35 min |
| 6 | ~10 h | 17 h 35 min |
| 7 | ~14 h | 31 h 35 min |
| 8 | ~20 h | 51 h 35 min |
| 9 | ~24 h | ~75 h (~3 days) |
| 10 | — (dead-letter) | — |

- `max_attempts = 10`. After the 10th failure: `state='dead'`, `last_error` set, endpoint `consecutive_failures++`. No further automatic attempts.
- `Retry-After` (seconds or HTTP-date, capped at 24 h) on a 429/503 response **overrides** the table delay for that step (still jittered ±10 %).
- Manual redelivery: `webhook.message.redeliver` on a `dead` (or `delivered`, for replay debugging) message resets it to `pending` with `attempt_count=0`, fresh `next_attempt_at=now`, same `webhook-id` and payload (idempotency-safe: receivers dedupe on the unchanged id). The `redelivery_of` self-link is set only when redelivering a `delivered` message for debugging (clone row, new id avoided — same id reused deliberately). Test pings (`endpoint.test`) are the only messages that mint a throwaway id.

### 7.4 Status handling

- `2xx` → `delivered` (set `delivered_at`), endpoint `consecutive_failures=0`, `last_status='ok'`.
- `410 Gone` → `dead` with `last_error='endpoint gone (410)'`, and the **endpoint is auto-disabled** (`active=false`, `disable_reason='410-gone'`, `disabled_at=now`) with an inbox notice to endpoint admins. Re-enabling requires `endpoint.update(active=true)`, which resets counters.
- `429 / 502 / 503 / 504` → retryable per §7.3 (honor `Retry-After`); endpoint `last_status='failing'`.
- Other `3xx/4xx/5xx` and transport errors (timeout, DNS, refused, TLS) → retryable per schedule. Any `3xx` is a **failure**: the client follows zero redirects (§8), so a `301` means the operator must update the URL, not the sender.
- Endpoint auto-suspension on sustained failure: `consecutive_failures >= 50` across messages (rolling) sets `last_status='failing'` + inbox notice; at `>= 200` the endpoint is auto-disabled (`disable_reason='undeliverable'`) with notice. Counters reset on any `2xx` or manual re-enable.

## 8. Timeouts

| Budget | Value |
|---|---|
| Per-attempt total (`http.Client.Timeout`) | endpoint `timeout_seconds`, default **20 s**, min 5, max 60 (spec recommends 15–30 s; 20 s default with configurability covers slow CI receivers without head-of-line blocking, bounded by per-endpoint concurrency). |
| TCP connect | 5 s |
| TLS handshake | 5 s |
| Response-header wait | 10 s (within total) |
| DNS resolution | 5 s per resolution |
| Request body write | within total (bodies ≤ 256 KiB, single flush) |

Timeouts are transport failures → retryable per §7.3, recorded as `error='timeout after Ns'`.

## 9. Response capture limits

- Response headers kept: first **8 KiB** serialized (lowercased names, multi-values joined with `,`).
- Response body kept: first **64 KiB**; the worker reads up to 64 KiB + 1 byte — if more arrives, it discards the remainder and appends a `…[truncated at 65536 bytes]` marker. (Gitea's 1 MiB capture is cut as unbounded for a high-agent-volume forge.)
- Request payload hard cap: **256 KiB** at enqueue (larger domain events must already be thin; the store layer rejects oversize with `E_INVALID` so the bug surfaces at the feature, not the worker).
- `last_error` / per-attempt `error`: ≤ 2 KiB, sanitized (no secret material, no endpoint credentials, internal paths stripped).
- Delivery history UI pages through `webhook_attempts` (latest 10/message); full bodies are never stored.

## 10. SSRF protection

Replaces Gitea's allow/block-list egress posture (`modules/egress/policy/`, `NewWebhookPolicy`) with a default-deny-private design. Every webhook dial passes all gates below; a denial is recorded as a transport error (`error='denied by egress policy: …'`) and retried on schedule (a DNS change can legitimately unblock), except malformed-URL cases which dead-letter immediately.

1. **URL validation.** Scheme must be `https` (or `http` iff `allow_insecure_http`); reject userinfo (`@`), control characters, URLs longer than 2048 chars, and non-HTTP(S) schemes. Port must parse 1–65535. The hostname must not be empty.
2. **Resolve-then-check.** A dedicated resolver (`net.Resolver`, 5 s timeout, no search-domain leakage: `PreferGo` with instance nameserver config) resolves **all** A/AAAA records for the hostname. **Every** returned address must pass the deny list; one denied address denies the whole delivery (an attacker round-robining one good + one evil record gets nothing). IP literals skip resolution and are checked directly.
3. **Deny list (checked per address).** Deny: loopback (`127.0.0.0/8`, `::1/128`), RFC 1918 private (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), link-local (`169.254.0.0/16` — covers the `169.254.169.254` cloud-metadata endpoint — and `fe80::/10`), CGNAT (`100.64.0.0/10`), unique-local (`fc00::/7`), unspecified (`0.0.0.0/8`, `::/128`), multicast (`224.0.0.0/4`, `ff00::/8`), reserved/documentation (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`, `2001:db8::/32`), and any address in the instance's `webhooks.extra_deny_cidrs`. There is no allow-list that can re-permit a denied range. An instance flag `webhooks.allow_private_targets` (default false, test instances only) bypasses the deny list for loopback/RFC 1918 to enable hermetic integration tests.
4. **Dial-the-checked-IP (TOCTOU/DNS-rebinding close).** The custom `DialContext` dials the **validated IP literal** (not the hostname), with `ServerName` set to the original hostname for SNI/TLS verification and the HTTP `Host` header preserved. The socket is bound after a second deny-list check on the concrete `AddrPort` at connect time (covers a resolver/dialer race). No proxy is consulted for webhook traffic (Gitea's `PROXY_URL` path is cut: a proxy would re-introduce an SSRF bypass around the dial check).
5. **No redirects.** `CheckRedirect` returns `http.ErrUseLastResponse` unconditionally (zero followed). A `3xx` response is a failed attempt per §7.4. The `Location` value is captured only in truncated headers for debugging; it is never fetched, never resolved, and never validated-then-followed — "re-check on redirect" is satisfied by refusal: there is no second fetch whose target could differ from the checked one.
6. **No credentialed exfiltration.** The sender never attaches `Authorization` from the endpoint config (Gitea's `HeaderAuthorizationEncrypted` per-webhook header is cut); the signature headers are the only auth material. Redirect-following being off also closes the classic `Authorization`-leak-on-redirect class.

## 11. Concurrency and ordering

- **Per-endpoint concurrency:** semaphore of `endpoint.concurrency` (default 4, max 16) enforced in the claim path; messages for one endpoint may be attempted concurrently up to the limit (retries of message A do not head-of-line-block message B).
- **Global cap:** `webhooks.global_max_inflight` (default 256) across all endpoints; excess claimable work waits for the next poll.
- **Ordering: none — documented.** Messages may be attempted concurrently and retries interleave arbitrarily; a redelivered older event may arrive after a newer one. Receivers MUST NOT infer order from arrival. Ordering affordances provided instead: `data` carries monotonic per-object versioning where it matters (`change.updated.version`, `ci.run.attempt`, `task` state machine), and `timestamp` (event time) lets consumers sort. This is stated verbatim in the consumer docs and the HTML endpoint form.

## 12. Idempotency for receivers

- `webhook-id` is the idempotency key: fixed at enqueue, immutable, reused across all automatic retries and manual redeliveries of the same message. `endpoint.test` pings are the sole exception (fresh id, synthetic `endpoint.tested` type).
- Consumers should store seen IDs for ≥ 24 h (recommended 7 days; `delivered` server rows live 30 days for support correlation via `event_id`).
- The `webhook-timestamp` header MUST NOT be used as a dedupe key (it rotates per attempt by design, as the replay-attack defense).

## 13. Go package boundary

New code only; Gitea's `models/webhook/`, `modules/webhook/`, `services/webhook/` are deleted with the cut surfaces.

```
internal/webhook/
  signer.go     Sign(msgID, ts, body, secret) / Verify(...)  (constant-time compare;
                rotation-aware multi-secret verify; whsec_ parse+generate)
  ssrf.go       Policy (deny CIDRs, flags), ResolveAndCheck(ctx, host),
                DialContext (dial-validated-IP + connect-time recheck),
                NewClient(timeout) (redirect-refusing http.Client)
  service.go    EnqueueTx (fan-out insert; called inside domain txns),
                Endpoint CRUD validation, rotation, test-ping
  worker.go     Claim loop + lease sweeper + attempt executor +
                backoff/Retry-After/dead-letter transitions + janitor
internal/store/
  webhook.go    Webhooks() interface: endpoints CRUD, InsertMessages(tx, …),
                ClaimDue(ctx, batch, globalCap), RecordAttempt(...),
                MarkDelivered/MarkDead/Requeue, Redeliver, PruneHistory
                (one interface; SQLite / PostgreSQL / Pebble backends behind it)
internal/ops/
  webhookops.go Op[In,Out] registrations for the §5 op list
                (Huma v2 HTTP + OpenAPI 3.1 + RFC 9457 errors;
                 MCP tools auto-derived; `rukie webhook <op> --json`;
                 zero-JS HTML forms derived from the same Op schemas)
```

Dependency direction: `internal/ops → internal/webhook → internal/store`. The worker never touches Ruk directly; it reads stored payload bytes. Feature code (changes, tasks, CI, mirrors, releases, repos) depends only on `webhook.EnqueueTx` + the store txn — it never dials, signs, or resolves DNS. Signing/SSRF code has no DB imports (pure functions over bytes + policy), keeping it unit-testable without a store.

## 14. Security and privacy notes

- Secrets encrypted at rest, shown once, redacted in every log/API/attempt record (presence flags only).
- Payloads contain no secret material, no tokens, no mirror credentials, no password hashes; mirror errors sanitized at enqueue.
- `repo.deleted` payloads carry only tombstone identifiers (objects may already be GC-eligible).
- Endpoint URLs are admin-scoped secrets in effect (they may contain paths an attacker could spam): list/get APIs require the scope's admin; the delivery log redacts query strings longer than 256 chars in the UI (full value retained server-side for debugging by admins only).
- Abuse bounds: max 32 endpoints per repo scope / 64 per owner scope (instance flags); payload cap §9; per-endpoint + global concurrency §11; auto-disable §7.4.

## 15. Observability

- Counters per (event_type, outcome): `enqueued`, `delivered`, `failed_attempt`, `dead_lettered`, `redelivered`; histograms of attempt latency and attempts-to-delivery; gauge of `pending`/`inflight` depth and per-endpoint `consecutive_failures`.
- Structured logs joinable on `webhook_id` + `event_id` + `endpoint_id` + `attempt_no`.
- Admin surfaces: endpoint detail shows recent attempts (from `webhook_attempts`), current backoff position, lease state, and the exact bytes of the next payload (admin-only).

## 16. Test plan (for the implementing lane)

- Golden vectors: spec's example `msg_id.timestamp.payload` HMAC-SHA256 (independently computed), rotation dual-signature accept, constant-time path covered by timing-smoke test.
- Unit: envelope schema validation for all 15 types; oversize-payload rejection; glob filtering; backoff table + jitter bounds; `Retry-After` parsing (delta + date + garbage); 410-disable; redelivery id reuse.
- SSRF: table of hostnames/IPs covering every deny CIDR, mixed good+evil DNS answers (must deny), DNS-rebinding simulation (resolve-time-good/dial-time-evil via fake resolver → must deny at connect check), redirect chain (must stop at first 3xx), userinfo/non-HTTP scheme rejections, metadata-IP `169.254.169.254` denial.
- Durability: kill -9 the worker mid-`inflight` → lease sweeper re-drives with the same `webhook-id`; domain-txn rollback leaves zero messages; 10-failure walk reaches `dead` on schedule (compressed clock in test).
- Conformance: verify sample deliveries with the official Standard Webhooks consumer libraries (at least Go + Python) in CI.

---

### Critical Files for Implementation

- `rukie/services/webhook/deliver.go` — Current Gitea signing headers (`X-Gitea-*`, HMAC hex) and single-shot `Deliver` being replaced; defines the exact behavior to cut.
- `rukie/services/webhook/webhook.go` — Current `PrepareWebhooks` fan-out + `WorkerPoolQueue` enqueue path whose crash-losable pattern the transactional outbox replaces.
- `rukie/models/webhook/webhook.go` — Current `Webhook`/`HookTask` xorm schema being deleted; reference for fields that must NOT leak into the new design (plaintext `Secret`, `Meta`, provider `Type`).
- `rukie/modules/egress/policy/policy.go` — Current dial-time policy engine; reference for the resolve-then-dial + connect-time recheck mechanics the new `internal/webhook/ssrf.go` reimplements in default-deny-private form.
- `rukie/modules/setting/webhook.go` — Current `[webhook]` knobs (`DeliverTimeout: 5`, `SkipTLSVerify`, `PAGING_NUM`) being replaced by the timeout/rotation/concurrency flags in §§5–8.


[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…