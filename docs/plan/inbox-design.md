# Rukie Inbox & Notifications Design (no email)

## 0. Scope and constraints

Rukie is a ground-up rewrite on top of `/home/alpha/rewrite/git-renewed/rukie` (2-day-old Gitea fork, cut in full) with ONLY Ruk as repo backend (`/home/alpha/rewrite/git-renewed/ruk`). Settled decisions (do not reopen):

- Cut every Gitea surface (API v1, xorm schema, Actions, OAuth2 provider, Gitea webhook formats, Vue/Vite/Tailwind, locales). Keep only `/{owner}/{repo}.git` clone URL shape.
- Review unit is BOTH Ruk changes/stacks (Gerrit-like, change-ids; stock git pushes to `refs/for/<branch>`) AND branch view grouping a branch's changes; tasks replace issues (claimable through Ruk leases).
- No email delivery. Only in-app inbox + signed webhooks (Standard Webhooks).
- Interfaces: ONE Go `Op[In,Out]` operation table (Huma v2 on `net/http` ServeMux for HTTP + OpenAPI 3.1 + RFC 9457) derives HTML (zero JS), JSON API, MCP tools (`modelcontextprotocol/go-sdk v1.8.0`), generated `rukie <op> --json` CLI.
- Metadata store OPEN (SQLite pure-Go vs Pebble vs PostgreSQL) behind one store module.
- Git wire (Smart HTTP + SSH) served by Ruk pack protocol through the Ruk daemon.

This document specifies the inbox. It replaces Gitea's `models/activities/notification.go` / `notification_list.go` design (`Notification{UserID,RepoID,Status,Source,IssueID,CommitID,CommentID,UpdatedBy}`, `NotificationStatus{Unread,Read,Pinned}`, `NotificationSource{Issue,PullRequest,Commit,Repository}`, `FindNotificationOptions`, `CreateOrUpdateIssueNotifications`) and Gitea's `services/uinotification/notify.go` + `services/webhook/{notifier.go,deliver.go}` split with a single event-source design.

Grounded references:

- `rukie/models/activities/notification.go:24-135` — old status/source enums, `Notification` struct, `TableIndices()` on `(user_id,status,updated_unix)` plus single-column indices.
- `rukie/models/activities/notification_list.go:20-100` — `FindNotificationOptions{UserID,RepoID,IssueID,Status,Source,UpdatedAfterUnix,...}`, `ToConds()`, `ToOrders() = updated_unix DESC`, `CreateOrUpdateIssueNotifications` fan-out over `GetIssueWatchersIDs` + `GetRepoWatchersIDs` + participants minus unwatchers.
- `rukie/models/repo/watch.go:17-112` — `WatchMode{None,Normal,Dont,Auto,Custom}`, `WatchType{pull_requests,issues,releases}`, `Watch{UserID,RepoID,Mode,Include*}`, `IsWatchModeWatching = mode != None && != Dont`, default no-row = `participate`.
- `rukie/modules/references/references.go:32-33,205-250` — `mentionPattern` matching `@user` and `@org/team`, `FindAllMentionsMarkdown/Bytes`.
- `rukie/modules/webhook/type.go:10-28`, `rukie/services/webhook/notifier.go:1-120`, `rukie/services/webhook/deliver.go:1-83` — old `HookEventType` set, per-event `PrepareWebhooks` fan-out, HMAC `newDefaultRequest` delivery; all cut, replaced by Standard Webhooks.
- `rukie/models/repo/mirror.go:22-42`, `rukie/models/repo/pushmirror.go:20-61` — old pull `Mirror` vs `PushMirror{RepoID,RemoteName,last_update,last_error,interval}` shapes informing mirror-failure events.
- Ruk: `ruk/crates/ruk-core/src` attestation (`attest.add/list`, `ruk-attest` SSHSIG namespace), claims (`claim.acquire/release/list`, lease-bound cursors), `op.log` with 7-day receipts; `ruk/crates/ruk/src/daemon/registry.rs:1-120` hosted registry; `ruk/tasks/operation-manifest/README.md` operation-table precedent.
- Standard Webhooks: Svix-signed webhooks, per-endpoint secret, `webhook-id/timestamp/signature` verification headers (see `https://docs.svix.com/receiving/verifying-payloads/how`).

## 1. Notification sources (normative)

Every source emits a `DomainEvent` (see §7). Inbox fan-out and webhook dispatch both consume that row. No source writes inbox rows or HTTP posts directly.

| # | Source | Trigger | Default recipients | Watch/participation rule |
|---|---|---|---|---|
| S1 | Change activity | change created/updated, new version pushed to `refs/for/*` or branch, comment, review vote, land/merge, abandon, revert | author, reviewers, participants, repo watchers per level | `participate` (default, no watch row): only if participant/mentioned/assigned/requested. `all`: every change event in repo. `custom:changes`: opt-in. `ignore`: nothing except direct mention/request/assignment (override). Direct actor never notified of own action (except failure receipts addressed to them). |
| S2 | Branch-view activity | branch view opened/updated (group of changes landing toward a branch), branch-view comment | same as S1 resolved against each contained change's repo | Grouped under branch-view thread when event's primary subject is the branch view; otherwise per-change thread (see §3). |
| S3 | Task activity | task created/edited/closed/reopened, task comment, task assignment, task claim state (acquired/released/expired — see S8) | reporter, assignee(s), claimant, participants, watchers per level | Same watch ladder as S1 with `custom:tasks` bit. Assignment always notifies even on `ignore`. |
| S4 | Mentions | `@user` or `@org/team` in change description, comment, task body/comment, branch-view description, release notes | mentioned user; all members of mentioned org/team with visibility to the subject | Overrides `ignore`. Resolved via mention extraction (same grammar as `mentionPattern`: `@[-\w][-.\w]*` and `@org/team`, markdown-stripped) at event-ingest time, expanded against org/team membership snapshot stored on the event. Users without subject visibility are silently dropped (no existence oracle). |
| S5 | Review requests | user or team requested as reviewer on a change / branch view | requested users, all members of requested team | Always notifies (overrides `ignore`), stays until request removed or change landed/abandoned. Re-request after removal creates a new unread bump. |
| S6 | CI results on my changes | CI run finished for a change version I authored, review, or watch (`custom:ci` default ON for participants) | change author(s), uploader of that version, explicitly subscribed | Only terminal states (`pass/fail/error/cancelled`) notify by default; `started/running` never creates inbox rows (webhook-only, opt-in). Failure bumps thread to unread even if previously done. Flapping runs for the same version are coalesced (see §3). |
| S7 | Mirror failures for repos I administer | push-mirror `exact` or `fast-forward-only` sync per mirror fails; periodic reconcile drift detected | repo admins (owner + users/teams with admin on the repo), mirror creator | Notify on transition `ok→fail` and on every Nth consecutive failure (N=5) + on recovery (`fail→ok` single closing update). Success spam is forbidden. `SyncOnCommit`-triggered and periodic-reconcile failures share the same subject per `(repo,mirror)` so they group. |
| S8 | Claim expiry on my tasks | Ruk lease `claim.acquire` approaching expiry (warning) and expired/released by system | current claim holder, task assignee if different | Warning once at `expiry - warn_window` (default 1h, min 5m); expiry notice once. Renewal (`acquire` refresh by same holder) suppresses warning and does not bump unread if already unread. System `release-on-expiry` emits `task.claim.expired` which also unblocks the task for re-claim. |
| S9 | Releases I watch | release published/edited/deleted on watched repo | `all` watchers + `custom:releases` opt-ins + participants of release notes thread | Mirrors old `WatchReleases` bit. Pre-releases only notify `custom:releases` holders, never `participate`-only users. |
| S10 | Repo-level admin notices | repo created/deleted/renamed/transferred/forked to me, access granted/revoked, default-branch changed | affected user(s), new owner, admins | Always notifies; `ignore` does not apply (security-relevant). Transfer emits to both old-owner admins and new owner. |
| S11 | Webhook-endpoint health (admin) | owned webhook endpoint failing repeatedly / disabled | endpoint owner, repo/org admins | Same transition logic as S7 per endpoint. |

Out of scope (explicitly no inbox rows): push events to plain branches by others (unless S1/S6 applies via an open change tracking that branch), stars/watches/forks social events (activity feed only), CI progress heartbeats, successful mirror syncs (except recovery close-out).

### 1.1 Watch levels (replaces Gitea `WatchMode` + `Watch{Include*}`)

One row per `(user, repo)`; absent row = `participate` (identical default to `rukie/models/repo/watch.go`).

```
watch_level = ignore | participate | all | custom
custom_bits = { changes, tasks, releases, ci }
```

- `participate`: direct involvement only (author/reviewer/assignee/mentioned/requested/claimant) + S10 admin notices.
- `all`: every S1–S3,S6,S9 event in the repo. Equivalent to old `WatchModeNormal` with all `Include*` true.
- `custom`: `all` restricted to selected bits. `ci` defaults ON when user picks custom via UI preset "Participating + CI failures"; raw API allows any subset.
- `ignore` (= old `WatchModeDont`): suppresses S1–S3,S6,S9 unless S4/S5/assignment/S10 override. Per-subject mute (see §4) is preferred over repo `ignore`; UI offers "Mute this thread" before "Ignore repo".

Team/org watches are NOT a thing: watching is per-user. `@org/team` mentions resolve to per-user inbox rows via membership at emit time (snapshot on event, so later team changes don't rewrite history).

## 2. Fan-out strategy: write-time fan-out rows (decision)

**Decision: write-time fan-out.** Each `DomainEvent` synchronously (in the same store transaction as the state mutation that caused it) computes the recipient set and inserts/updates one `inbox_thread` + one `inbox_item` row per recipient. Reads are then a single indexed `WHERE user_id=? AND state=? ORDER BY bumped_at DESC` query with no joins, no permission re-evaluation, no mention re-parsing.

Rejected alternative: read-time query (derive "my notifications" at read from watches + participation + mentions + visibility). Rejected because (a) mentions require full-text scan per read; (b) team-membership and repo-visibility at read time would leak existence or force expensive authz joins per row; (c) agents poll frequently (MCP `list` every few seconds across many concurrent agents) — read-time fan-out turns every poll into a multi-table aggregation; (d) dedup/grouping "one thread per change/task" needs stable per-user thread state anyway.

### 2.1 Sizing (1k users × 10k events/day)

- Writes: 10k events/day ≈ 0.12 ev/s avg, ~5 ev/s plausible peak (CI bursts, mirror reconcile). Average fan-out ≈ 4 recipients (author + 1–2 reviewers/participants + 0–2 watchers; mentions/reviews are narrow, `all`-watchers are rare on an agent forge). ⇒ ~40k inbox_item writes/day, ~2k peak-hour writes/hour. Trivial for any of SQLite/Pebble/PostgreSQL behind the store module; single-row inserts, no cross-shard transaction.
- Reads: 1k users, agents polling `inbox.list` every 30–60s while active (say 200 active pollers) ≈ 3–7 qps of a covering-index range scan with `LIMIT 50`. Each read touches only the reader's rows. Hot path is `idx (user_id, state, bumped_at DESC)`.
- Storage: one `inbox_item` ≈ 200–300 bytes + JSON payload pointer (event id, not duplicated body). 40k/day × 250 B ≈ 10 MB/day before retention; 90-day retention ≈ <1 GB. `domain_events` (shared with webhooks) dominates; inbox rows are small because the rendered title/body is derived at read from the event + subject snapshot, not stored per recipient (only per-thread cached snippet, updated on bump).
- Correctness properties that force write-time: (i) recipient snapshot at emit (membership/visibility frozen on event — late permission revocation never retro-deletes delivered rows, only hides subject link target); (ii) idempotency key per `(event_id, user_id)` makes at-least-once emit safe; (iii) `bumped_at` ordering is a plain column update, no recomputation.

Scaling beyond: if users×events grow 100×, write-time cost grows with fan-out (bounded by watcher count) while read-time cost would grow with event volume per read — write-time wins harder. The only known write-time risk is a thundering `all`-watch repo (e.g. 1k watchers × release event = 1k rows in one tx). Mitigation: cap single-tx fan-out at 500 rows; overflow spills to a `fanout_pending` queue drained by a background worker within 60s, same idempotency keys, thread `bumped_at` set to event time so ordering is preserved. Webhook delivery is already async (see §8) so it never blocks this path.

### 2.2 Fan-out transaction

```
BEGIN (store tx)
  mutate subject (change/task/comment/review-request/claim/mirror-state/...)
  INSERT domain_events(id, seq, type, repo_id, subject, actor, payload, visibility_snapshot, idempotency_key)
  recipients = resolve(event)   # pure function of event + watch table + subject participant list + mention expansion
  FOR each recipient:
    UPSERT inbox_thread(user_id, subject_kind, subject_id) -> bump
    INSERT inbox_item(user_id, thread_id, event_id) ON CONFLICT (event_id,user_id) DO NOTHING
COMMIT
enqueue webhook deliveries (async, from committed event seq)
```

Recipient resolution never performs I/O beyond indexed store reads (watch rows for repo, participant list on subject, team member expansion from event snapshot). The actor is excluded except for failure receipts (S6/S7/S8 addressed TO the actor — those bypass the self-exclusion).

## 3. Dedup and grouping: one thread per subject

Thread key: `(user_id, subject_kind, subject_id)` where `subject_kind ∈ {change, branch_view, task, release, mirror, repo, webhook_endpoint, ci_run_group}` and `subject_id` is the stable store id (change-id for changes, NOT the version; task id; `(repo,ref)` for branch views; `(repo,mirror)` for mirrors).

Rules:

1. **Exactly one thread per subject per user.** New events on the same subject UPDATE the existing thread (`bumped_at = event.time`, `snippet`, `unread_count++` if state was `read/done` → back to `unread` per §4) and append one `inbox_item`. Never a second thread.
2. **Item dedup:** `UNIQUE(event_id, user_id)`. Retried emit, duplicate webhook redelivery, or double mention (body + comment) referencing the same event produce one row. Distinct events on the same subject produce distinct items under one thread (visible as thread history).
3. **Cross-event coalescing (no new unread bump):**
   - CI progress (`started/running`, repeated `pass` for same version): no inbox row at all.
   - Repeated failure for the same `(change, version)` (retries): first failure bumps; subsequent identical failures within 30 min append items WITHOUT bumping (thread stays at current state, counter shown).
   - Claim warning + expiry for the same lease epoch: two items, one bump (warning bumps; expiry bumps only if thread was read/done in between).
   - Mirror consecutive failures: items appended, bump only on transition and every 5th (see S7); recovery always bumps once with `kind=recovered`.
   - Review-request + mention in the same event: single item, `reasons = [review_requested, mentioned]` (array, not two rows).
4. **Mention expansion dedup:** `@org/team` + individual `@user` who is also a member ⇒ one item, reasons merged.
5. **Subject lifecycle closes threads implicitly:** change landed/abandoned, task closed, mirror deleted, claim released-and-reassigned — thread gets a final closing item (bump once) and `subject_closed_at` set; UI renders collapsed "closed" styling but state machine unchanged (user can still mark done).
6. **`reasons[]` per item** records why this recipient was included (`author, reviewer, participant, watcher_all, watcher_custom, mentioned, review_requested, assigned, claimant, admin, mirror_owner, endpoint_owner`). UI shows the top reason as "Why am I seeing this?"; API exposes all.

## 4. States: unread / read / done / snoozed (+ muted)

States per **thread** (items are append-only history; state lives on the thread). This differs from Gitea's per-notification `Status{Unread,Read,Pinned}` (`rukie/models/activities/notification.go:31-38`): `Pinned` is dropped (no evidence of use as workflow state), replaced by `done` + `snoozed` which agents and humans both need.

```
unread ──mark_read──▶ read ──mark_done──▶ done
  ▲                    │  ▲                 │
  │ bump               │  │ bump*            │ bump*
  │ (new event)        │  │ (new event       │ (failures, mentions,
  └──◀── all states ───┘  │  only for        │  requests, expiry
     (auto, see below)    │  high-priority)  │  always re-open)
                          ▼                  ▼
                       snoozed ──wake──▶ unread (bumped_at preserved, snooze_until cleared)
                          │ mark_read/mark_done apply immediately and clear snooze
```

- `unread`: needs attention. Default for new/ bumped threads. Badge counts `unread` only.
- `read`: seen, still open. Viewing the subject (opening the change/task page, `op change.get/task.get` via HTML or API with the thread's `last_seen_event` advanced) auto-marks `read` — same behavior as Gitea's `SetIssueReadBy` but per-thread and idempotent. Explicit "Mark as read" form does the same.
- `done`: archived/dismissed. Removed from default inbox filter. Any new event re-opens ONLY for high-priority reasons (`mentioned, review_requested, assigned, ci_failed, mirror_failed, claim_expiring/expired, recovered`); routine comments/votes on a `done` thread append items silently (no bump, no re-open) — this is the key anti-noise rule for heavy agent traffic.
- `snoozed`: `read`-like hiding until `snooze_until` (15m / 1h / 1d / 1w presets + explicit timestamp via API). A background sweeper (or lazy on-read promotion) flips expired snoozes to `unread`. New high-priority events break snooze early (same set as `done` re-open); routine events wait.
- `muted` (orthogonal flag, not a state): `muted=true` on a thread suppresses ALL bumps including high-priority except direct `assigned/review_requested` and S10 admin notices. One-click "Mute" per thread; repo-level `ignore` remains the coarse control.

Transition table (who can move):

| Op | unread→ | read→ | done→ | snoozed→ |
|---|---|---|---|---|
| mark_read | read | — | read (re-open as read) | read, clear snooze |
| mark_done | done | done | — | done, clear snooze |
| mark_unread | — | unread | unread | unread, clear snooze |
| snooze(until) | snoozed | snoozed | snoozed | snoozed (new deadline) |
| new routine event | bump (stay unread) / read→unread | unread | silent append | silent append |
| new high-priority event | bump | unread + bump | unread + bump | unread + bump, clear snooze |
| subject closed | append + stay/bump once | append + bump once | silent append (stays done) | silent append |

Bulk ops apply the same op to a set of thread ids atomically (all-or-nothing per thread, partial success reported per id).

## 5. Zero-JS inbox UI (HTML forms only)

Route prefix `/inbox` (HTML). All mutations are `<form method="post">` with hidden `thread_ids[]` + `op` + CSRF token; no JS, no fetch. Checkbox lists + per-thread buttons + filter bar (links, not JS tabs). Follows the Op-table HTML-form derivation (§6): every inbox op renders as a form.

Pages:

- `GET /inbox` — default filter `state=unread`. Layout: filter bar (`Unread | Read | Done | Snoozed | All`, `subject: change/task/...`, `repo:`, `reason:` as GET links preserving query), thread list (50/page, `?before=` cursor on `bumped_at`), each row: checkbox, state dot, subject link, snippet (last event title, server-escaped), reason tag ("mentioned you", "review requested", "CI failed", ...), repo, age, per-thread forms (`Read | Done | Snooze▾ | Mute`). Bottom: bulk bar (`[x] Select all on page`, `Mark read | Mark done | Snooze 1d | Unmute`, `Apply`). Empty state names the filter and links to watch settings.
- `GET /inbox/thread?id=` — thread detail: subject header + full item history (oldest→newest, each item: time, actor, event title, reason chips, link to subject anchor), state form row. Opening this page with `?mark=read` (default link target from list) advances `last_seen_event` → auto `read`.
- `GET /inbox/settings` — per-repo watch levels table (each row: repo, current level radio `participate/all/custom/ignore`, custom bits checkboxes, Save form per repo), global default for new repos, "Mute list" (unmute forms). No JS; custom-bits fieldset is always visible (progressive enhancement forbidden from hiding it behind JS).
- Header badge: server-rendered unread count (`N`) next to `Inbox` nav link on every page; agents ignore it (use API count).

Accessibility/perf budgets: full inbox page ≤ 60 KB HTML, ≤ 4 indexed queries (count + page + subject titles + repo names), no N+1 (batch loads like Gitea's `NotificationList.LoadAttributes` but server-side joined in the store layer). All times rendered server-side in user's (or instance-default) timezone with `<time datetime>` UTC attribute.

## 6. Agent access: MCP + HTTP API + CLI (from the Op table)

All inbox operations are entries in the ONE operation table, so HTML, JSON, MCP, CLI share schemas, authz, and validation. Human session cookie and scoped-token auth both accepted; agents use scoped tokens on human accounts (no agent principals — settled).

Ops (names; `In/Out` schemas shared):

| Op | In | Out | Notes |
|---|---|---|---|
| `inbox.list` | `state?, subject_kind?, repo?, reason?, limit(≤100, def 50), cursor?` | `{threads:[{id,subject_kind,subject_id,repo,state,muted,reasons,bumped_at,snippet,unread_items}], next_cursor, unread_count}` | Default `state=unread`. Cursor is opaque `(bumped_at,id)`. Agents poll with `state=unread&limit=50`; long-poll NOT offered (poll interval ≥15s enforced by rate limit, `429` + `Retry-After`). |
| `inbox.get` | `thread_id` | `{thread, items:[{event_id,type,actor,at,reasons,title}]}` | Reading via API does NOT auto-mark read (unlike HTML view); agents must call `mark`. Prevents poll-loops from clearing state. |
| `inbox.mark` | `thread_ids[], op: mark_read\|mark_done\|mark_unread\|mute\|unmute, snooze_until?` | `{updated:[ids], failed:[{id,error}]}` | Single + bulk share this op (HTML bulk form posts here). `snooze_until` required when `op` implies snooze (separate `inbox.snooze` alias allowed). Idempotent per thread. |
| `inbox.snooze` | `thread_ids[], until` | same | Validates `until` within 1m..90d future. |
| `inbox.count` | `—` | `{unread, snoozed_awake_pending}` | Cheap badge/poll gate; agents SHOULD call `count` before `list`. |
| `inbox.watch.set` | `repo, level, custom_bits?` | `{repo, level, custom_bits}` | Setting `ignore` requires explicit `confirm:true` (destructive-ish). |
| `inbox.watch.list` | `—` | `{watches:[...], default_level}` | |
| `inbox.settings` | `default_level?, claim_warn_window?` | `{...}` | Claim warning window per user (5m..24h, default 1h). |

MCP mapping: each op becomes one tool (`inbox_list`, `inbox_get`, `inbox_mark`, `inbox_snooze`, `inbox_count`, `inbox_watch_set`, ...) via `modelcontextprotocol/go-sdk v1.8.0`; input schema = op `In` JSON schema; output = op `Out`; errors = RFC 9457 problem details serialized into MCP error payload. CLI: `rukie inbox list --state unread --json`, `rukie inbox mark --id 12 --op mark_done --json`, etc. — generated, not hand-written.

Rate limits (agents): `inbox.list` 1 qps per token burst 5; `inbox.count` 2 qps; `mark/snooze` 5 qps. Exceeding returns RFC 9457 `rate-limited` with `Retry-After`. Humans browsing HTML share the same ops (same limits, cookie identity).

## 7. Store tables (store-module DDL, backend-agnostic)

Backend OPEN, so DDL is expressed in the store module's migration language (must compile to SQLite/Pebble/PostgreSQL). Column types below are logical (`id` = 64-bit int or UUID-string per store convention — new code MUST NOT reuse Gitea xorm tags; shown as SQL for review).

`domain_events` is the ONE event source shared by inbox and webhooks (§8). Inbox tables reference it; they never duplicate payloads.

```sql
-- THE event log. Append-only. seq is the global dispatch cursor.
CREATE TABLE domain_events(
  id            TEXT PRIMARY KEY,          -- ulid/uuid7, time-ordered
  seq           BIGINT NOT NULL UNIQUE,    -- store-assigned sequence (outbox cursor)
  type          TEXT NOT NULL,             -- change.created, change.commented, task.assigned,
                                           -- review.requested, ci.finished, mirror.failed/recovered,
                                           -- claim.expiring/expired, mention.*, release.published, repo.*, webhook.* ...
  at            TIMESTAMPTZ NOT NULL,
  actor_id      TEXT NOT NULL,             -- user id; 'system' for sweeper/reconcile
  repo_id       TEXT NULL,                 -- null for cross-repo/user events (rare)
  subject_kind  TEXT NOT NULL,             -- change|branch_view|task|release|mirror|repo|webhook_endpoint
  subject_id    TEXT NOT NULL,
  payload       JSONB NOT NULL,            -- event body (comment excerpt, vote, ci status, mirror error, claim epoch...)
  visibility    JSONB NOT NULL,            -- {repo_visibility, allowed_user_ids/org/team snapshot for @-expansion}
  idempotency_key TEXT NOT NULL UNIQUE,    -- (source, source_seq) e.g. ('op.log', <ruk seq>) or (mutation tx id)
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_events_seq ON domain_events(seq);
CREATE INDEX idx_events_subject ON domain_events(subject_kind, subject_id, at DESC);

-- Per-user watch levels (§1.1). Absent row = participate.
CREATE TABLE watches(
  user_id     TEXT NOT NULL,
  repo_id     TEXT NOT NULL,
  level       TEXT NOT NULL,               -- ignore|participate|all|custom
  custom_bits JSONB NOT NULL DEFAULT '[]', -- subset of [changes,tasks,releases,ci]
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id, repo_id)
);

-- One row per (user, subject). State machine in §4.
CREATE TABLE inbox_threads(
  id              TEXT PRIMARY KEY,        -- ulid
  user_id         TEXT NOT NULL,
  subject_kind    TEXT NOT NULL,
  subject_id      TEXT NOT NULL,
  repo_id         TEXT NULL,
  state           TEXT NOT NULL DEFAULT 'unread',  -- unread|read|done|snoozed
  muted           BOOLEAN NOT NULL DEFAULT FALSE,
  reasons         JSONB NOT NULL DEFAULT '[]',     -- union of recent item reasons
  snippet         TEXT NOT NULL DEFAULT '',        -- cached last-event title (re-rendered on bump)
  unread_items    INT NOT NULL DEFAULT 0,
  total_items     INT NOT NULL DEFAULT 0,
  bumped_at       TIMESTAMPTZ NOT NULL,
  snooze_until    TIMESTAMPTZ NULL,
  subject_closed_at TIMESTAMPTZ NULL,
  last_seen_event TEXT NULL,               -- domain_events.id watermark for auto-read
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(user_id, subject_kind, subject_id)
);
CREATE INDEX idx_threads_user_state_bump ON inbox_threads(user_id, state, bumped_at DESC, id DESC);
CREATE INDEX idx_threads_snooze ON inbox_threads(state, snooze_until) WHERE state='snoozed';

-- Append-only history. Dedup via (event_id, user_id).
CREATE TABLE inbox_items(
  id         TEXT PRIMARY KEY,
  thread_id  TEXT NOT NULL REFERENCES inbox_threads(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL,
  event_id   TEXT NOT NULL REFERENCES domain_events(id) ON DELETE RESTRICT,
  reasons    JSONB NOT NULL DEFAULT '[]',
  title      TEXT NOT NULL,                -- frozen at emit (subject renamed later doesn't rewrite history)
  bumped     BOOLEAN NOT NULL,             -- did this item move the thread (vs silent append)?
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(event_id, user_id)
);
CREATE INDEX idx_items_thread ON inbox_items(thread_id, created_at ASC);

-- Webhook endpoints + deliveries (Standard Webhooks). Same event source.
CREATE TABLE webhook_endpoints(
  id            TEXT PRIMARY KEY,
  owner_kind    TEXT NOT NULL,             -- repo|org|user|system
  owner_id      TEXT NOT NULL,
  repo_id       TEXT NULL,
  url           TEXT NOT NULL,
  secret-enc    TEXT NOT NULL,             -- per-endpoint signing secret (encrypted at rest, never returned)
  event_filter  JSONB NOT NULL DEFAULT '["*"]',  -- subscribed domain_event types / prefixes
  active        BOOLEAN NOT NULL DEFAULT TRUE,
  consecutive_failures INT NOT NULL DEFAULT 0,
  disabled_at   TIMESTAMPTZ NULL,
  created_by    TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE webhook_deliveries(
  id            TEXT PRIMARY KEY,
  endpoint_id   TEXT NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
  event_id      TEXT NOT NULL REFERENCES domain_events(id) ON DELETE RESTRICT,
  status        TEXT NOT NULL DEFAULT 'pending', -- pending|delivering|delivered|failed|abandoned
  attempts      INT NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error    TEXT NULL,
  response_code INT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(endpoint_id, event_id)
);
CREATE INDEX idx_deliveries_due ON webhook_deliveries(status, next_attempt_at) WHERE status IN ('pending','failed');
```

What was deliberately NOT carried over from Gitea: `Notification{IssueID,CommitID,CommentID}` polymorphic columns (replaced by `subject_kind/subject_id` + event payload), `NotificationStatusPinned`, per-row `UpdatedBy` (actor lives on the event), `Source` enum (replaced by event `type` + `reasons[]`), and the `u_s_uu` + seven single-column indices (replaced by the one covering index reads actually use).

## 8. Webhooks and inbox share one event source

```
mutation tx → domain_events row (committed) ─┬─▶ inbox fan-out (sync, same tx, §2.2)
                                             └─▶ webhook dispatcher (async, by seq cursor)
```

- Producers (change/task/CI/mirror/claim/release/repo code) emit `DomainEvent`s only. They never import inbox or webhook packages. The event's `type` vocabulary IS the webhook event vocabulary (documented in OpenAPI; e.g. `change.created`, `change.commented`, `change.review_requested`, `task.assigned`, `task.claim.expired`, `ci.finished`, `mirror.failed`, `mirror.recovered`, `release.published`). No second Gitea-style `HookEventType` enum.
- Inbox fan-out runs in-tx (sync) as above; webhook dispatch runs out-of-tx: a dispatcher tails `domain_events` by `seq` (durable cursor in store), matches `endpoint.event_filter`, inserts `webhook_deliveries(endpoint,event)` (idempotent on conflict), and delivers with retries.
- Delivery: signed per Standard Webhooks (per-endpoint secret; `webhook-id`, `webhook-timestamp`, `webhook-signature` headers; Svix-compatible verification). Payload = `{event_id, type, at, actor, repo, subject, payload}` JSON — same content inbox `inbox.get` items reference. `Content-Type: application/json` only (Gitea's form-encoded/GET/PUT matrix in `deliver.go` is cut).
- Retries: exponential backoff 1m→6h, 24h abandon; consecutive-failure counting disables endpoints at 50 fails (emits S11 inbox notice to owner/admins); manual redeliver replays from the stored event (events retained ≥ delivery window even under short inbox retention).
- Ordering: per-endpoint FIFO by `seq` best-effort; concurrent delivery across endpoints. Signature covers the exact bytes delivered (replay-safe).

## 9. Retention

- `domain_events`: 180 days (needed for webhook replay/audit and thread history titles). Then hard-delete; `inbox_items.title` preserves a frozen copy so old threads remain readable.
- `inbox_threads/items`: per-user `done` threads pruned after 90 days of no bump (configurable 30–365d); `unread/read/snoozed` never auto-pruned. `muted` threads follow `done` rule once done, else retained.
- `webhook_deliveries`: terminal rows (`delivered/abandoned`) 30 days; per-endpoint history page shows recent 50.
- Claim-warning events: warning without subsequent expiry still counts as activity (retained per thread rules).
- All retention runs as store-module background sweep (per-coordinator-style periodic task, cf. `registry.rs` retention pattern), emitting no events, never touching `unread`.

## 10. Non-goals / explicit cuts

No email, no digest mail, no `Pinned` status, no per-issue watch rows (repo-level only + implicit participation), no Gitea webhook event names/payloads, no form-encoded webhook delivery, no JS-driven inbox (filters, bulk select-all-across-pages, live badge refresh all server-side or absent).

### Critical Files for Implementation

- `rukie/models/activities/notification.go` — Old Notification struct/status/source to replace; grounds thread/item schema delta.
- `rukie/models/activities/notification_list.go` — Old fan-out (`CreateOrUpdateIssueNotifications`) and `FindNotificationOptions` read pattern to replace with write-time fan-out.
- `rukie/models/repo/watch.go` — Old `WatchMode/WatchType` ladder informing new `watches` levels table.
- `rukie/services/webhook/notifier.go` — Old per-callsite `PrepareWebhooks` fan-out to replace with single `domain_events` source.
- `rukie/services/webhook/deliver.go` — Old HMAC/form/GET delivery matrix to replace with Standard Webhooks signed JSON dispatch.


[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="qwen" severity="concern" guidance="weigh, don't blindly obey"> Merge CI failures into change thread.…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Remove ci_run_group from thread ke…

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Add fanout_pending to the store ta…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Write the file to disk now. F…