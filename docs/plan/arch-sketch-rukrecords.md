I've finished the design, but I couldn't write the file. This session only has read and search tools and no write tool, so `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/arch-sketch-rukrecords.md` doesn't exist yet. The full text is below between the `~~~~` fences; save it verbatim to that path.

**Summary of the sketch**
- **Thesis:** anything about a repository lives in that repository's Ruk store, and anything about the instance lives in rukie's local store. That covers tasks, review threads, review votes, CI results, release notes, repo settings (land policy) and mirror configs. Records are kept as one ref per record under a reserved `refs/ruk/r/<kind>/<id>` namespace. A new fact table, `Records`, indexes them and is written in the same batch as the ref update. Gerrit's NoteDb stores review data the same way.
- **What Ruk can store today:** its 27 fact tables are a fixed list. The existing `records` module only covers commits and changes, so there's no task, thread or release type. Attestations are signed SSH statements whose kind and result are free text, and they already export as `refs/notes/ruk-attest`. `claim.*` is a path lock tied to a Ruk session, and Ruk's leases are garbage-collection leases.
- **Task claims:** I added a new fenced lease, `record.lease.*`, rather than reusing `claim.acquire`. Path claims protect file writes and expire with the session; a task claim needs an actor, a fencing number and its own expiry.
- **New Ruk methods:**
  - `record.create/update/get/log/list/export/reindex`, plus a `RecordLeases` table.
  - `events.read`, backed by an `Events` table written in the same batch as each change.
  - `search`, backed by a small per-blob filter cache (`BlobSketch`).
  - `remote.ls`.
  - Host-level: `host.repo.*`, `host.catalog.list/rebuild`, `host.events.follow`.
- **Breaking changes to existing Ruk calls:**
  - Landing inside the hosted store (`RemoteSpec::Local`), with Ruk reading the land policy from the `settings` record instead of trusting the caller.
  - Requirements gain a scope (exact candidate, or the reviewed head after a clean rebase), a signer list and a minimum number of distinct approvers.
  - Attestation statement v2 gains `on_behalf_of` and `detail`.
  - Push gets a one-time credential that is never stored.
  - Each invocation carries the acting user.
  - `refs/for/` is handled natively, and direct pushes to land-only branches are refused.
- **Listing tasks across 500 repos:** a per-repo fan-out was rejected (cold store opens; estimated 0.5–3 s, a guess). Instead the daemon keeps one catalog fed from each repo's event table, which can be rebuilt. The query merges 500 per-repo ranges (I estimate about 10 ms), with a 60 ms p95 target that must be benchmarked.
- **Consistency:** there are no distributed transactions. Writes either go to Ruk first and the local store is derived from Ruk's events (with a per-repo cursor advanced in the same local transaction), or they are a local multi-step create/delete made safe by Ruk's request-id receipts. The doc lists 12 invariants, each with an owner and a repair path.
- **Go layout:** 20 packages in one direction: transports → operation table → features → infrastructure → platform. The metadata store choice (SQLite, Pebble or PostgreSQL) is hidden in `internal/meta`.
- **Flows:** (a) render a file is one Ruk `file` call (p95 40 ms warm). (b) Landing is `land.prepare`, then CI runs on the candidate and signs an attestation, then `land.execute` (p95 100 ms excluding CI). (c) A smart-HTTP push streams to Ruk's receive-pack, Ruk records the update and the events atomically, and the event follower then queues webhook, mirror, CI and inbox jobs.

**Decisions for you:**
- **Coupling (main risk):** task and review schemas become Rust types in Ruk, so every field change needs a Ruk release. Keeping records opaque would make Ruk unable to index or enforce them, so I chose typed kinds.
- **Gerrit-style secondary index:** the host catalog is a second, derived index. Without it, cross-repo listing is too slow.
- **Erasure / GDPR:** deleting or erasing content conflicts with immutable record history. This needs a `record.redact` design before tasks ship.
- **Ruk-blocked work:** most of the forge waits on these Ruk changes; the doc orders them so they land in 3 commits before rukie cuts over.

Unverified assumptions are marked `[inference]` in the doc, including timing estimates, reusing Ruk's LFS-object area for release assets, and whether GitHub and GitLab accept pushes to `refs/ruk/export/*`. The doc ends with a Critical Files section.

~~~~markdown
# Architecture sketch C: "Ruk as system of record"

Status: plan-mode sketch, 2026-10-20. Scope: rukie rewrite. Ruk may break.
Evidence: paths are relative to /home/alpha/rewrite/git-renewed/ruk unless noted.
Unverified claims are marked `[inference]`.

## 0. Thesis

Ruk stores everything about one repository that must move with that
repository: tasks, review threads, review votes, CI results, release notes,
land policy, and mirror configuration. Each repository's Ruk store holds these
as versioned records. Typed Ruk methods publish the record ref, the index rows,
and a change-feed row in one lane batch. rukie's local store ("meta") holds only
instance facts: identity, sessions, tokens, orgs/teams/grants, the social graph,
secrets, and work queues derived from Ruk events. Precedent: Gerrit NoteDb
keeps review metadata in per-change refs and adds a secondary index for
cross-project queries
(https://gerrit.googlesource.com/gerrit/+/be911167d6a0aa6b8b568ae06a60221e24d945f5/Documentation/note-db.txt).

## 1. Grounding: what Ruk stores today

- Mutable state is a closed set of 27 redb fact tables, `FactTable`
  (crates/ruk-core/src/store/types.rs:229-300). The doc comment says: "Closed: a new
  table arrives only with a schema revision". Owners postcard-encode the values.
- `records` covers only commits, changes, imported changes, and change history
  (crates/ruk-core/src/records.rs:1-11, records/types.rs). No task, thread, or
  release type exists.
- Attestations: a statement is {subject Oid, kind, result, signed_at,
  supersedes}. It is signed as an SSHSIG with ssh-ed25519 in namespace `ruk-attest`
  (protocol.rs:1776-1793, attestation.rs:1-41). Ruk verifies a row each time it
  reads it. Rows have Live, Superseded, or Ambiguous status. Bounds:
  MAX_LAND_REQUIREMENTS=64 and MAX_TRUSTED_SIGNERS=256 (attestation.rs:64-77).
  Rows use two key families: subject-major and order index
  (attestation/layout.rs:1-14). Export is streamed (attestation/export.rs).
  `refs/notes/ruk-attest` exports as an ordinary ref
  (crates/ruk-git/src/export/refs.rs:4-6, export/report.rs:49-50). Kind and
  result are free UTF-8 strings (capability.rs:201-216).
- `claim.*` creates path-prefix write locks. The holder is a Ruk `SessionId`,
  with a TTL and an Enforce or Advise mode (protocol.rs:1700-1767). `Leases` are
  GC-root and object leases (coord/leases.rs:1-23). Neither primitive is a
  task-assignment lease.
- Land: `land.prepare(change, remote: RemoteSpec, target)` freezes the exact
  candidate set and the base tip. It also freezes the attestation predicates and
  an expiry one hour away. `land.execute` checks that the live tip still equals
  the base. It consumes the plan row atomically, so a second executor loses
  (coord/land.rs:1-45, protocol.rs:2026-2073). `RemoteSpec` is `Named|Url`
  only (protocol.rs:979-982). The caller supplies the attestation policy
  (land.rs:25-26).
- Each repo has one linearizable publication lane. A batch carries objects,
  facts, and a ref CAS together. Receipts make a replay with the same session
  and request id idempotent (docs/coordinator.md).
- Daemon `ruk forgejo serve` keeps a multi-repo registry. It opens each
  coordinator lazily and evicts idle ones (crates/ruk/src/daemon/mod.rs:1-10,
  registry.rs:1-14). Its opens are
  `InvocationOpen{repo, method, request_id, args_json, session_id}`
  (daemon/mod.rs:305-320) and `CommandOpen{argv, request_id, command_digest,
  credits}` (mod.rs:389-413). It negotiates the features credits,
  merge-notices, and outcome-lookup (daemon/ipc.rs:40-44).
- `file{at: RevisionRef, path}` returns one of Blob{oid,size,content},
  Directory, Symlink, Gitlink, or Conflict (protocol.rs:2404-2443).
- AGit pushes to `refs/for/<branch>` are diverted to a proc-receive hook
  (crates/ruk-git/src/transport/proc_receive.rs, transport/serve.rs).
- Capabilities gate ref namespaces by pattern (capability.rs:1253-1256).
- The method table has no search method (protocol.rs:3285-3333).
- A Go IPC client already exists: forgejo/modules/git/ruk_ipc.go
  (`RukOpenRequest`, `RukConn.OpenStream`).

## 2. Placement

### 2.1 In Ruk (per repo, versioned, replicated with the repo)

| Object | Ruk form | Why here |
|---|---|---|
| code, refs, changes, stacks, change history | existing tables | already canonical |
| Task | record `task`, ref `refs/ruk/r/task/<n>` | belongs to repo history; agents edit it beside code; moves with the repo |
| Task claim | fact table `RecordLeases` (not replicated); history logs acquire and release only | renewals churn and must not create commits |
| Review threads and comments | record `review`, ref `refs/ruk/r/review/<change-id>` | anchored to commits and paths that Ruk owns (Gerrit precedent) |
| Review votes | attestation kind `review`, result `approve`/`reject`, signed by the forge review key with `on_behalf_of` | land evaluation reads them inside Ruk |
| CI results | attestation kind `ci/<job>`, result `pass`/`fail`, signed by the forge CI key | settled decision |
| Release | tag via `tag.set`, plus record `release` at `refs/ruk/r/release/<tag>` (notes and asset manifest) | notes are history |
| Release assets | Ruk LFS-object area: content-addressed, outside the git DAG `[inference: LfsObjects reusable]` | binaries must not enter history or mirrors |
| Repo settings: default branch, description, protections (land policy) | singleton record `settings` at `refs/ruk/r/settings/repo` | Ruk enforces land, so the caller must not supply the policy |
| Mirror config: url, mode, ref selection, carry_records, credential ref | record `mirror` at `refs/ruk/r/mirror/<ulid>`; never exported | push plans already live in Ruk (`PushPlans`); config is repo policy |
| Change feed | fact table `Events` | outbox that rukie reads |
| Search sketches | fact table `BlobSketch` (derived, droppable) | keyed by blob oid, so shared across revisions |

### 2.2 In meta (instance-local; the store type is hidden in one Go package)

users (argon2id hash, TOTP secret, disabled flag), oidc_identities, ssh_keys,
web_sessions, tokens (hash, scopes, repo filter, expiry), orgs, teams,
team_members, team_repos, collaborators, repos (id ULID, owner, name,
store_id, visibility, fork_parent, state), stars, watches, secrets (sealed
with the instance key-encryption key: mirror credentials, webhook secrets, CI
secrets, attest signing keys), webhooks (subscription and secret ref),
ruk_sessions (per-repo Ruk service session plus reconnect secret, so receipts
survive restarts), feed_cursors, jobs (unified leased queue), inbox,
ci_runs and ci_log_chunks (operational only; results are attestations).

Webhook config stays local, but mirror config goes to Ruk. A webhook endpoint
is instance plumbing: it carries a secret, and replicating it would leak
internal URLs. Mirror mode and ref selection are repository policy that Ruk
executes through `push`. Mirror credentials stay in meta, and the record holds
only `SecretRef`.

Stars, watches, and forks stay in meta. They are instance social graph, not
repo history.

## 3. Ruk changes (breaking allowed)

### 3.1 Record model: new module `crates/ruk-core/src/collab/`

- Closed `enum RecordKind { Task, Review, Release, Settings, Mirror }`.
  `RecordKey{kind, id}` maps to ref `refs/ruk/r/<kind>/<id>`. Ids by kind:
  - task: per-repo `u64` sequence, allocated in the lane;
  - review: ChangeId;
  - release: tag name;
  - settings: `repo`;
  - mirror: ULID.
- Each mutation adds one commit on the record ref. The tree holds
  `record.json` (canonical JSON snapshot, kind schema v1) and `event.json`
  (op, actor, at, request_id). It also holds `body.md` and
  `comments/<seq>.md` where present. Author is the actor display name plus
  `<ActorRef>`; committer is `ruk`.
- New `FactTable::Records`, using the attestation two-family pattern:
  - `0x00 kind` -> next sequence;
  - `0x01 kind ‖ id` -> {rev Oid, snapshot postcard ≤64 KiB without bodies, updated_gen};
  - `0x02 kind ‖ state ‖ updated_desc ‖ id`;
  - `0x03 kind ‖ assignee ‖ state ‖ updated_desc ‖ id`.
- New `FactTable::RecordLeases`: `kind ‖ id` -> {holder ActorRef, token_id,
  epoch u64, expires_at}. Expiry is evaluated at read and update time on the
  coordinator clock (coord/clock.rs).
- New `FactTable::Events`: `generation u64 BE ‖ seq u16` -> one of
  `RefUpdated{name, old, new}`, `ChangeUpdated{change, heads}`,
  `RecordChanged{key, rev, summary}`, `AttestationAdded{subject, kind, digest}`,
  `LandExecuted{plan, target, commit}`, or `LeaseChanged{key, holder?}`.
  Rows are written in the same batch as the mutation. Retention is age plus
  size; reads before the oldest retained row fail with `E_GAP`.
- New `FactTable::BlobSketch`: blob oid -> 256-byte trigram bloom, computed
  lazily.
- Reserved namespace `refs/ruk/**` (ref grammar from refs.rs:1-21):
  - only `record.*` methods may write it;
  - receive-pack, `update-ref`, and `bookmark.set` refuse with `E_RESERVED`;
  - default advertisement hides it; it is served only to an explicit
    ref-prefix request.
- `undo` of an operation that touched record refs re-derives their `Records`
  rows in the same batch.

Kind schemas v1, owned by Ruk and validated in Ruk:

```
Task     {number, title≤256, state: Open|Closed{reason: Done|WontDo|Duplicate{of}},
          labels: BTreeSet<Label>, assignees: BTreeSet<ActorRef>,
          links: Vec<Change(ChangeId)|Commit(Oid)|Task(u64)>, created_by, created_at,
          updated_at, comment_count}
Review   {change: ChangeId, target: RefName, state: Open|Landed{commit}|Abandoned,
          threads: Vec<Thread{id, anchor: Line{commit, path, side, lines}|Change,
          resolved, comments: Vec<u32>}>, reviewers: BTreeSet<ActorRef>}
Release  {tag, title, draft, prerelease, assets: Vec<{name, sha256, size, media_type}>}
Settings {default_branch, description, protections: Vec<Protection{pattern: RefPattern,
          land_only: bool, require: Vec<AttestRequirement>}>, ci_config_path}
Mirror   {url, mode: ExactForcePrune|FastForwardOnly,
          refs: All|DefaultAndTags|Globs(Vec<RefPattern>), carry_records: bool,
          credential: SecretRef, reconcile_interval_s}
ActorRef = "rukie:<instance-uuid>:u:<user-ulid>" (never a username)
```

### 3.2 New methods (append-only, protocol.rs:3123-3130)

| method | kind | args -> reply |
|---|---|---|
| `record.create` | Mutation | {kind, body, id?} -> {key, rev, number?} |
| `record.update` | Mutation | {key, expected_rev, ops: Vec<RecordOp>, fence?: epoch} -> {rev}; `E_STALE` or `E_FENCED` |
| `record.get` | Read | {key, at?} -> {rev, snapshot, body} |
| `record.log` | Read | {key, page} -> Page<RecordEvent> |
| `record.list` | Read | {kind, filter{state, assignee, label, updated_after}, order, page} -> Page<RecordSummary> |
| `record.lease.acquire` | Mutation | {key, ttl, holder: ActorRef, token_id} -> {epoch, expires_at}; `E_LEASED{holder, expires_at}` |
| `record.lease.renew` | Mutation | {key, epoch, ttl} -> {expires_at} |
| `record.lease.release` | Mutation | {key, epoch} -> {} |
| `record.export` | Mutation | {kinds} -> writes aggregate snapshot refs `refs/ruk/export/<kind>` (one commit per kind; tree of all records) |
| `record.reindex` | Mutation | {} -> rebuilds `Records` from `refs/ruk/r/**` |
| `events.read` | Read | {after, limit} -> {events, next, oldest_retained}; `E_GAP` |
| `search` | Read | {at, pattern: Literal\|Regex, paths: Vec<PathGlob>, case, budget{max_bytes, max_matches, max_ms}} -> streamed matches plus truncation reason |
| `remote.ls` | Read | {remote, auth} -> remote refs (needed for mirror prune) |

`RecordOp` is one of: SetTitle, SetBody, SetState, AddLabel, RemoveLabel,
Assign, Unassign, AddComment{thread?}, AddThread{anchor}, ResolveThread,
AddLink, AddReviewer, SetSettings, SetMirror.

Host methods are served by the daemon, not per repo:
- `host.repo.create{store_id, path}`, `host.repo.fork{source, store_id}`
  (objects shared by hardlink or alternates `[inference]`), `host.repo.delete`;
- `host.catalog.list`, `host.catalog.rebuild`;
- `host.events.follow`: a long-lived stream of {repo_handle, generation}.

Changes to existing calls:
1. Add `RemoteSpec::Local`. In Local mode, `land.prepare` loads requirements from
   the protections in `Settings` that match the target, and refuses any
   caller-supplied policy.
2. `AttestRequirement` gains `scope: Candidate|SourceHead`,
   `signers: Vec<SignerFingerprint>`, and `min_distinct_actors: u8`.
   SourceHead accepts statements on the change head the candidate was rebased
   from, and only when Ruk's rebase was conflict-free.
3. `AttestStatement` v2 gains `on_behalf_of: Option<ActorRef>` and
   `detail: Option<String>` (log URL). This brings `canonical_payload_v2`.
4. `PushArgs.auth: Option<EphemeralAuth>`: excluded from the receipt digest and
   never persisted.
5. `InvocationOpen.actor` and `CommandOpen.actor`. Ruk honours them only from
   a principal holding the new capability `assert_actor` (policy.rs principals).
6. Native `refs/for/<branch>` handling: create or update a change by its
   Change-Id trailer, with no proc-receive callback, and emit `ChangeUpdated`.
7. Receive-pack refuses a direct push to a `land_only` ref (`E_LAND_ONLY`).
8. `land.prepare` reply gains `gaps: Vec<RequirementGap>`. `land.execute` also
   moves the `Review` record to `Landed` in the same batch.

### 3.3 Daemon host catalog: `crates/ruk/src/daemon/catalog.rs`

- Its own redb at `<root>/.ruk-host/catalog.redb`.
- Key families:
  - `C1 repo ‖ kind ‖ state ‖ updated_desc ‖ id`;
  - `C2 kind ‖ state ‖ assignee ‖ updated_desc ‖ repo ‖ id`;
  - `C3 kind ‖ state ‖ updated_desc ‖ repo ‖ id`;
  - counters `K repo ‖ kind ‖ state`;
  - per-repo cursor `P repo -> generation`.
- An applier task tails each repo's `Events`. The coordinator notifies it in
  process after publication. At startup it scans repos whose cursor is behind.
- It is derived state: `host.catalog.rebuild` recreates it.
- Shutdown goes through the registry's `shutdown_and_drain`.

## 4. Query and listing performance

The workload: list open tasks across the user's 500 visible repos, 50 per
page, newest first.

1. rukie computes the visible handle set from meta (user -> teams -> grants,
   plus public repos), cached per user for 30 s.
2. rukie calls `host.catalog.list{repos, kind: task, state: open, assignee?,
   order: updated_desc, limit: 50, cursor}`.
3. Strategy:
   - up to 2000 repos: k-way merge of C1 prefixes (500 seeks, a 500-entry heap,
     50 pops);
   - assignee given: scan the C2 prefix and filter by a repo hash set;
   - more than 2000 repos, or "all public": scan C3 and filter.
4. Estimate `[inference]`: about 5–10 ms in catalog plus about 2 ms IPC/JSON
   plus about 3 ms render. Target: p95 ≤ 60 ms server time. Gate: a benchmark
   with 500 repos × 2000 tasks before locking the design.
5. Freshness: catalog lag target p99 < 1 s. The reply returns `stale_repos`
   (cursor behind the notified generation), and the UI marks those rows
   "updating".
6. Badges read the K counters in O(1).

Rejected: fan out `record.list` per repo. That means 500 invocations, plus lazy
coordinator opens after idle eviction (registry.rs:6-11). Estimated 0.5–3 s
`[inference]`.

Per-repo `record.list` is one seek on `0x02` plus 50 rows; p95 ≤ 15 ms.

Code search:
- `search` walks the tree at the revision and skips blobs over 1 MiB and
  binary blobs.
- It checks `BlobSketch` for literals of 3 or more bytes and for regexes with
  required trigrams. Sketches are shared across revisions because blobs are
  content-addressed.
- Search is budget-bounded and streamed.
- Cross-repo search is a rukie fan-out over visible repos with concurrency 8.
  It stops at 100 matches or 2 s and flags partial results.
- Target: p95 ≤ 300 ms for a 50k-file repo with warm sketches `[inference]`.
- Footprint: 256 B per text blob, droppable.

## 5. Consistency between Ruk and meta

Rule: Ruk is authoritative for repo-scoped facts, and meta for instance-scoped
facts. Every value derived across the boundary carries a cursor and can be
rebuilt. No distributed transaction exists. Writes take one of two shapes:
- Ruk-first: the mutation lands in Ruk, and meta derives from `Events` in one
  meta transaction that also advances the cursor.
- Meta saga: Ruk calls are keyed by request id, and Ruk receipts make each
  retry idempotent.

| # | Invariant | Enforced by | Repair |
|---|---|---|---|
| I1 | An active repo row has a store with the same `store_id` (format file, store/types.rs:349-360) | `rukc` checks on first open | reaper: row without store becomes `broken`; store without row is moved to quarantine after 24 h |
| I2 | Repo create: row `provisioning`, then `host.repo.create`, then row `active` | `repo` saga | reaper retries or rolls back after 10 min |
| I3 | `ActorRef` resolves to a meta user, or renders as a ghost | read time | none |
| I4 | Jobs and the cursor advance in one meta transaction; job key unique on (repo, generation, seq, consumer) | `feed` | re-read from the cursor |
| I5 | `E_GAP` triggers a per-consumer reconcile | `feed` | mirror: reconcile all; CI: rescan open changes and protected tips for missing attestations; webhook: send `repo.resync`; inbox: post a gap notice |
| I6 | `SecretRef` in a mirror record exists | `mirror` at push time | status `credential_missing` |
| I7 | A lease holder's token is valid | `task` | token revoke enqueues `record.lease.release`; otherwise expiry |
| I8 | Catalog matches `Records` | Ruk applier cursor | `host.catalog.rebuild` |
| I9 | `Records` matches `refs/ruk/r/**` | same lane batch | `record.reindex` |
| I10 | Ruk trusts `actor` only from the rukie principal | capability `assert_actor` | none |
| I11 | Rename or transfer touches meta only (store directory named by repo ULID) | `repo` | none |
| I12 | HTTP `Idempotency-Key` / MCP call id = Ruk `request_id` under a persisted per-repo Ruk session | `rukc` | receipts replay |

## 6. Go package layout (20 packages; imports point downward only)

```
cmd/rukie            serve | admin (user, reset-password, totp-reset) | ssh-keys | git-shell |
                     client mode `rukie <op> --json` (built at runtime from the embedded op table)
internal/op          Op definitions (name, HTTP route, input/output types, scope, authz, handler);
                     OpenAPI 3.1 + MCP schema emission; the single source of every interface
internal/httpapi     JSON transport over op; RFC 9457 problem+json; Idempotency-Key
internal/web         html/template, server-rendered forms, zero JS, embedded CSS
internal/mcpserver   MCP tools over op (streamable HTTP + stdio)
internal/auth        argon2id, TOTP (RFC 6238), OIDC code flow, web sessions, scoped tokens
internal/meta        the ONLY metadata-store package: Store interface + one implementation
internal/rukc        Ruk IPC client: frames, credits, outcome lookup, typed Invoke, command streams;
                     Go types generated from `ruk schema`; ported from forgejo/modules/git/ruk_ipc.go
internal/record      Go mirror of the Ruk kinds, ActorRef, RecordOp builders
internal/repo        lifecycle saga, browse (file/tree/blame/log/diff), forks, stars, watches,
                     explore, code-search fan-out
internal/task        task ops, record leases
internal/review      changes, stacks, branch view (revset), threads, votes, land orchestration
internal/gitwire     smart HTTP + SSH git-shell splice to Ruk CommandOpen
internal/feed        host.events.follow + events.read -> meta jobs (transactional cursor)
internal/jobs        leased job runner: claim, heartbeat, fence epoch, backoff, dead letter
internal/webhook     Standard Webhooks signer and deliverer
internal/mirror      mirror plan, push, reconcile, periodic ticker
internal/ci          config, scheduler, runner long-poll API, result reuse, attest
internal/sign        SSHSIG ssh-ed25519 signer for the review and CI keys
internal/platform    config, slog, clock, lifecycle Group (context + WaitGroup; every goroutine registers)
```

Import order: transports → `op` → domain packages (`repo`, `task`, `review`,
`ci`, `mirror`, `webhook`) → `record`, `rukc`, `meta`, `sign`, `jobs` →
`platform`. `feed` imports `meta`, `rukc`, and `jobs`.

SSH runs through OpenSSH: `AuthorizedKeysCommand rukie ssh-keys %f` emits
`restrict,command="rukie git-shell --key <id>"`. git-shell calls
`rukie serve` over `/run/rukie/internal.sock`, so meta stays single-process,
which a Pebble lock requires.

Example op:
`op.Def[TaskClaimIn, TaskClaimOut]{Name: "task.claim",
Route: op.Post("/{owner}/{repo}/-/tasks/{n}/claim"), Scope: "task:write",
Handler: task.Claim}`.

## 7. Request flows

### (a) Render a file at a revision. Target p95 ≤ 40 ms warm, ≤ 70 ms cold store; 256 KiB cap

1. `GET /{o}/{r}/-/blob/{rev}/{path...}` reaches `web`, which runs op `file.show`.
2. `auth.FromRequest` resolves the session cookie or bearer token to a user,
   or to anonymous.
3. `repo.Resolve(o, r)` reads the meta `repos` row (LRU, 5 s, invalidated on
   rename). `meta.Permission(user, repo)` must return read or better.
4. One Ruk call: `rukc.Invoke(handle, "file", {at: rev, path})` returns
   `FileBody`:
   - Blob: render as text (no NUL in the first 8 KiB), with `#L<n>` anchors;
   - Directory: 302 to `/-/tree`;
   - Conflict: render the conflict terms;
   - over 256 KiB: link to `/-/raw` (streamed).
5. Caching:
   - rev is a full oid: `ETag: "<blob oid>"` and `Cache-Control: immutable`
     (public repos) or `private`;
   - symbolic rev: `no-cache` plus ETag.
6. Failures:
   - `E_NOT_FOUND`: 404;
   - daemon unreachable: 503 with Retry-After;
   - store quarantined: 503 `repo-quarantined`;
   - admission `E_LIMIT`: 429 with Retry-After.

### (b) Land a change with required attestations. Target p95 ≤ 100 ms excluding CI

Precondition: the `Settings` protection for `refs/heads/main` has
`land_only: true` and two requirements:
- `{kind: review, result: approve, scope: SourceHead, signers: [review-key], min_distinct_actors: 1}`;
- `{kind: ci/test, result: pass, scope: Candidate, signers: [ci-key], min_distinct_actors: 1}`.

1. The caller sends `change.land{owner, repo, change, target}` by form POST,
   CLI, or MCP. Authz: write permission plus token scope `change:land`.
2. `review.Land` calls `land.prepare{change: ChangeRef{change, head},
   remote: Local, target}` with the actor and `request_id = Idempotency-Key`.
   Ruk rebases the stack, which becomes the candidate set (land.rs:12-13). It
   freezes the plan, loads requirements from `Settings`, and returns
   `{plan, candidate, base, expires_at, requirements, gaps}`.
3. If `gaps` is non-empty, `review` writes in one meta transaction:
   - job `land.drive{repo, plan, candidate}`;
   - job `ci.run{repo, commit: candidate, job: test}`, deduplicated on
     (repo, candidate, job).
   It then responds 202 with a status page that refreshes through
   `<meta http-equiv=refresh>`.
4. The `ci` runner finishes. `ci` checks the job lease epoch. `sign` produces
   the statement `{subject: candidate, kind: ci/test, result: pass, detail:
   log URL}`. `attest.add` then writes `Attestations` and
   `Events.AttestationAdded`.
   Result reuse: if `ci_cache(tree oid, job digest)` has a pass, the CI key
   re-signs for the new subject, and the job does not run.
5. `feed` sees `AttestationAdded` for a subject held by a pending `land.drive`
   and wakes that job.
6. `land.drive` calls `land.execute{plan, candidate}`. Ruk re-evaluates the
   requirements live and checks base equality. In one batch it moves the ref,
   consumes the plan, sets `Review.Landed`, and writes `Events`
   (RefUpdated, LandExecuted).
7. `feed` fans out: mirrors, webhooks (`change.landed`), post-merge CI, and
   reviewer inbox items.

Failures:
- `E_STALE` (base moved): `land.drive` re-prepares, at most 3 times, then
  notifies. A new candidate needs a new CI run.
- CI fail: the plan is dropped and the status page shows the failing statement.
- Expired after one hour: re-prepare.
- Untrusted signer: 422.
- Ruk crash mid-execute: replay the same `request_id`, through outcome-lookup or
  the receipt.
- Concurrent executors: the loser fails on the plan-row expectation
  (land.rs:43-45).

### (c) Smart-HTTP push triggering mirror, webhook, and CI. Target p95 ≤ 30 ms overhead over pack transfer

1. `GET /{o}/{r}.git/info/refs?service=git-receive-pack`:
   - `gitwire` takes Basic auth (any username, password = token);
   - meta token lookup checks scope `repo:write` and permission;
   - `rukc.Command(handle, ["receive-pack", "--stateless-rpc",
     "--advertise-refs", "."], actor)` streams back with the service pkt-line
     header.
2. `POST /{o}/{r}.git/git-receive-pack`:
   - the body is gunzipped if needed and piped as Stdin frames under the
     credit window;
   - stdout streams back.
   A client disconnect sends Cancel. Ruk then reports either
   `CancelledBeforeCommit` or a completed result, recoverable by outcome-lookup.

[1260 more lines in resource. Use :604 to continue]
   `CancelledBeforeCommit` or a completed result, recoverable by outcome-lookup.
3. Inside Ruk:
   - objects go to quarantine;
   - `refs/ruk/**` is refused;
   - a push to a `land_only` ref is refused;
   - a push to `refs/for/<b>` creates or updates a change;
   - the ref CAS, `ChangeUpdated` / `RefUpdated` events, and the report-status
     are written in one batch.
   The client gets success only after durable publication.
4. Asynchronously, `host.events.follow` emits {repo, generation}. `feed` calls
   `events.read{after: cursor}`. In one meta transaction it inserts:
   - `webhook.deliver` per matching subscription; job key = event id, which is
     also the `webhook-id`;
   - `mirror.push` per mirror whose ref selection matches; coalesced per
     (repo, mirror);
   - `ci.plan{repo, ref, oid}`;
   - `inbox.fanout`.
   It then advances the cursor.
5. `mirror` worker:
   1. `record.get mirror`, then the secret from meta;
   2. ExactForcePrune: `remote.ls`, then force updates for selected refs, then
      deletes for remote refs that are absent locally;
   3. FastForwardOnly: non-force updates only;
   4. `carry_records` adds `record.export`, then `refs/ruk/export/*`;
   5. `push{remote: Url, updates, atomicity: NonAtomic, auth}`;
   6. a push that leaves Uncertain schedules `push.reconcile{plan}`;
   7. a non-ff rejection in FastForwardOnly mode sets status `diverged` and
      notifies repo admins.
   A periodic ticker (default 1 h) enqueues reconcile runs.
6. `webhook` worker:
   - POST with `webhook-id`, `webhook-timestamp`, and
     `webhook-signature: v1,<base64 HMAC-SHA256(secret, id.ts.body)>`
     (https://www.standardwebhooks.com/);
   - 2xx means done;
   - otherwise exponential backoff over 24 h, then dead letter and an inbox
     notice; after N consecutive dead letters the hook is auto-disabled.
7. `ci` scheduler:
   - reads `.rukie/ci.toml` at the oid via `file`, then enqueues `ci.run` per
     job;
   - runners long-poll and hold leased jobs, renewed by heartbeat;
   - a lost runner's lease expires and the job is requeued with a new epoch;
   - a late result with a stale epoch is refused before signing.

## 8. Failure paths

| Failure | Behaviour |
|---|---|
| Ruk daemon down | repo pages, API, and git return 503 with Retry-After; login and settings keep working; the feed pauses with cursors kept |
| Daemon restart mid-invocation | reconnect, then outcome-lookup by `request_id` (ipc.rs:40-44); a mutation replays its receipt |
| Lane admission full (`E_LIMIT`) | 429 or 503 with Retry-After; jobs back off |
| Store quarantine (corrupt attestation latch, attestation.rs:16-17) | repo read-only (503 on writes); admin inbox notice |
| Record CAS lost (`E_STALE`) | HTML re-renders the form with the current state; API returns 409 with the current `rev` |
| Lease fenced or expired (`E_FENCED`, `E_LEASED`) | 409 naming the holder and expiry |
| `E_GAP` on events | per-consumer reconcile (I5) |
| Meta down | 503 everywhere except the git read path for public repos `[inference: allow]` |
| Mirror auth, network, or divergence | status recorded on the job; backoff; inbox notice |
| Webhook endpoint down | backoff, then dead letter |
| CI runner lost | lease expiry, then requeue; fencing blocks stale signatures |
| Land base drift, expiry, or failing requirement | re-prepare up to 3 times, else stop and notify |
| Disk full in Ruk (`E_IO`) | 503; publication is atomic, so there is no partial state |

## 9. Red-flag self-check

1. **Coupling (accepted, main cost).** Task and Review schemas live in Rust, so
   every field change needs a Ruk release. Alternative: opaque JSON plus declared
   index fields. That weakens validation and policy enforcement. Revisit if
   fields churn weekly.
2. **Second index (mitigated).** The host catalog duplicates `Records`. It is
   derived and rebuildable. Gerrit made the same move (NoteDb plus a secondary
   index).
3. **Erasure and GDPR (open, blocker for tasks GA).** Record history is
   immutable git. Deleting the ref leaves objects until retention collects them,
   and mirrors that carried records keep copies. A `record.redact` design
   (history rewrite plus a tombstone event) is required.
4. **Namespace bypass (mitigated).** Writing `refs/ruk/**` through raw git would
   skip the index and policy. Ruk must refuse it in every write path:
   receive-pack, `update-ref`, `bookmark.set`, and `undo`.
5. **Actor ids leak through carried records (mitigated).** `carry_records` is
   off by default. `ActorRef` contains no username.
6. **Ref count.** One ref per task means about 100k refs for a large repo. That
   is fine in redb `[inference]`, but it is hidden from advertisement. Mirrors
   receive aggregate `refs/ruk/export/*` refs, not per-record refs.
   GitHub/GitLab acceptance of custom refs is unverified `[inference]`.
7. **Divergent replicas (excluded in v1).** Replication is push-only, so record
   merge is not needed. Importing a repo that already has records requires an
   empty destination or a fast-forward, else `E_DIVERGED`.
8. **Merge-queue latency.** Candidate-scope CI re-runs after every base move.
   Mitigations: SourceHead scope for review, and tree-keyed CI result reuse.
9. **Lease clock.** A single-host coordinator clock is acceptable. Multi-host
   leases would need fencing, which already exists through epochs.
10. **Ruk is on the critical path for nearly every page.** The p95 targets
    assume one Ruk round trip per page. Pages that need N calls must batch
    (record.list) or use the catalog.
11. **Engineering bar:** packages map to small functions; every goroutine goes
    through `platform.Group`; no secret ever enters Ruk.

## 10. Commit sequence

1. Ruk (Ruk repo, 3 commits):
   1. `collab` module, Records/RecordLeases/Events tables, `record.*`,
      `events.read`;
   2. `RemoteSpec::Local`, requirement scope, Attest v2, actor assertion,
      native `refs/for`, `land_only`;
   3. host catalog, `search` with BlobSketch, `remote.ls`, push auth.
2. rukie: `platform`, `meta`, `rukc`, `op` skeleton. Delete the Gitea surfaces
   in the same cutover commit series. Every commit builds.
3. auth → repo/gitwire → record/task/review → feed/jobs → webhook/mirror/ci →
   transports (web, httpapi, mcpserver, CLI).

## Critical Files

- `crates/ruk-core/src/store/types.rs`: closed FactTable list (new tables go here)
- `crates/ruk-core/src/protocol.rs`: method table, attestation/land/push/file args
- `crates/ruk-core/src/coord/land.rs` and `crates/ruk-core/src/attestation.rs` (+ `attestation/layout.rs`): land plans and attestation storage (the pattern for `Records`)
- `crates/ruk/src/daemon/mod.rs`, `registry.rs`, `ipc.rs`: daemon frames, actor field, host catalog
- `forgejo/modules/git/ruk_ipc.go`: Go IPC client to port into `internal/rukc`
~~~~

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/store/types.rs` — the fixed fact-table list where `Records`, `RecordLeases`, `Events` and `BlobSketch` get added.
- `ruk/crates/ruk-core/src/protocol.rs` — the method table (new methods are appended at the end) and the attestation, land, push and file argument types.
- `ruk/crates/ruk-core/src/coord/land.rs` and `ruk/crates/ruk-core/src/attestation.rs` (with `attestation/layout.rs`) — land plans, requirement checks, and the two-key-family layout that `Records` copies.
- `ruk/crates/ruk/src/daemon/mod.rs` (with `registry.rs` and `ipc.rs`) — the open-request structs that gain `actor`, plus where the host catalog and event stream go.
- `ruk/forgejo/modules/git/ruk_ipc.go` — the existing Go client to port into `internal/rukc`.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

→ grep(LAND_PLAN_TTL @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/land.rs) ⇒ ok · 11 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/export/report.rs:30-70) ⇒ ok · 50 lines
→ grep(fn tracked|=> (true|false) @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/oplog.rs:30-110) ⇒ ok · 34 lines
→ grep(hideRefs|hide_refs|hidden_ref|ref-prefix|ref_prefix @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport) ⇒ ok · 115 lines

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Write `Events` rows in the st…

---
→ grep(fn advertise|advertise_refs|for_each_ref|all_refs @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/se…) ⇒ ok · 5 lines
→ grep(type RukOpenRequest @ /home/alpha/rewrite/git-renewed/ruk/rukie/modules/git) ⇒ ok · 1 line
→ grep(fn publish|pub fn commit|FactTable::Refs @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/store/mod.rs) ⇒ ok · 99 lines

[advisor] <advisory advisor="minimax" severity="blocker" guidance="weigh, don't blindly obey"> Fix flow (b) and §3.2. Ruk `land.p…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Cut search, forks, SSH, Explore a…

---
The sketch is revised to address the review findings that held up against the code. I still couldn't write the file: this session only has read and search tools. Save the text between the `~~~~` fences verbatim to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/arch-sketch-rukrecords.md`. It replaces the earlier draft.

**What changed, each checked against the Ruk source:**
- **Landing was wrong in the first draft.** Ruk sorts land candidates by object id and lands the lowest one (`coord/land.rs:131-132`, `239-241`). It does not check that the landed commit descends from the base (`401-416`), and every candidate must meet every requirement (`386-400`). The sketch now has Ruk rebase the stack itself, store an explicit tip, check ancestry before landing, and say per requirement whether it applies to the tip or to each change.
- **A busy main branch could stall landing for good.** Plans expire after a fixed hour (`land.rs:67`), and execution needs the branch tip unchanged. The fix:
  - allow one live plan per branch;
  - make protected branches land-only, so direct pushes can't move the tip;
  - read the plan expiry from the branch protection settings instead of a constant.
- **Attestations don't replicate today, and the first draft implied they did.** Git export drops the rows (`ruk-git/src/export/report.rs:49-51`), and nothing in Ruk writes `refs/notes/ruk-attest`. The sketch adds `attest.add` writing that notes ref, plus `attest.import` and `attest.reindex`, which re-verify every statement on the way in.
- **All human votes would have collided.** The forge key signs every vote, so reviewers would share one attestation group and conflicting votes would all be marked "Ambiguous". The group key now includes `on_behalf_of`.
- **Change events are recorded in `Store::publish` (`store/mod.rs:911`), not per method.** Commands like `tag`, `update-ref` and `rebase` publish outside the lane, so without this they would emit no events and trigger no webhooks, mirrors or CI.
- **Hidden refs are a new requirement.** Ref advertisement (`serve.rs:106`) hides nothing, so every record ref would be advertised on clone and push. Pushes to `refs/ruk/**` and `refs/notes/ruk-attest` are now refused always, because the socket peer is always rukie.
- **The acting user reaches Ruk on every call.** Both `InvocationOpen` and `CommandOpen` gain an `actor` field; today `CommandOpen.session_id` is ignored and the Go client sends a fixed service session.
- **Idempotency keys are scoped per user.** The Ruk `request_id` is now a hash of actor, operation and `Idempotency-Key`, so two users sending the same key don't share a receipt.
- **The Go client is reused, not redesigned.** The existing client (`ruk/forgejo/modules/git/ruk_ipc.go`) only runs git-style commands; the typed-method path is listed as new Go work.
- **File rendering covers every `FileBody` case.** Inline and streamed blobs, symlinks, submodule links, directories (rendered from the same call, no redirect) and conflicts are all handled. The ETag is the blob id.
- **Webhook config moved into Ruk** as a `hook` record that is never exported; its secret stays local. Six local-store tables still sit outside the "identity, sessions, tokens, membership, queues" list, and each is now a named red flag with a reason.
- **New red flags:**
  - whoever compromises rukie or its signing key can forge every approval and CI result;
  - one landing at a time per branch caps throughput.
- **Repo settings can only tighten what the operator allows.** They never grant capabilities; Ruk's `policy.toml` stays the authority.
- **Smaller fixes:** the exported record tree is sharded and updated incrementally, and undo restores record refs while re-deriving their index.

I kept code search, forks, SSH and CI result reuse, trimmed, despite a suggestion to cut them. They are settled features that need Ruk-side support.

~~~~markdown
# Architecture sketch C: "Ruk as system of record"

Status: plan-mode sketch, revised 2026-10-20. Paths are relative to /home/alpha/rewrite/git-renewed/ruk.
`[inference]` marks claims that have not been verified in code.

## 0. Thesis

Everything about one repository that must move with it lives in that repository's Ruk store:
- tasks, review threads, review votes, CI results;
- release notes, land policy, mirror config, webhook config.

The canonical form is git objects under reserved refs. Each record is one ref, `refs/ruk/r/<kind>/<id>`. Attestations live in the notes ref `refs/notes/ruk-attest`.

Fact-table rows are derived indexes. They are written in the same store commit and can be rebuilt from the refs.

rukie's local store ("meta") holds only instance facts: identity, sessions, tokens, orgs/teams/grants, and queues derived from Ruk events. The deviations are listed in §9.

Precedent: Gerrit NoteDb keeps review metadata in refs and adds a secondary index for cross-project queries
(https://gerrit.googlesource.com/gerrit/+/be911167d6a0aa6b8b568ae06a60221e24d945f5/Documentation/note-db.txt).

## 1. What Ruk does today

**Fact tables.** There are 27 redb fact tables, and the set is closed (crates/ruk-core/src/store/types.rs:229-300). Only Refs, RefConflicts, Changes, ChangeHistory, Workspaces, WsIndex and Pins are op-log tracked. Attestations, Claims, Leases and LandPlans are not (coord/oplog.rs:48-78).

**Records module.** `records` covers commits, changes and history only (records.rs:1-11). No task, thread or release type exists.

**Attestations.**
- A statement is {subject Oid, kind, result, signed_at, supersedes}, signed as SSHSIG ssh-ed25519 in namespace `ruk-attest`. The signer is derived from the key (protocol.rs:1776-1793, attestation.rs:1-41).
- Groups are keyed by (subject, kind, signer). Disagreeing live statements in one group are all `Ambiguous` (attestation.rs:20-27).
- Kind and result are free strings (capability.rs:201-216).
- **Not replicated.** Git export lists "Verified attestation rows" as lost (ruk-git/src/export/report.rs:49-51). Only a pre-existing `refs/notes/ruk-attest` ref is copied, and nothing in ruk-core writes that ref.

**Claims and leases.**
- Claims are path locks held by a SessionId (protocol.rs:1700-1767).
- Leases are GC-root leases (coord/leases.rs:1-23).
- Neither is a task-claim primitive.

**Land.**
- Core `land_prepare` takes candidates from the caller and does no rebase in core. It sorts and dedups them by Oid (land.rs:239-241), so the primary candidate is the lowest Oid (land.rs:131-132).
- Every candidate must satisfy every requirement (land.rs:386-400).
- Execute checks that the tip equals the base, then moves the target to the primary candidate. It does not check that the primary descends from the base (land.rs:380-416).
- Plans expire after a fixed TTL, `LAND_PLAN_TTL` = 1h (land.rs:67).
- `RemoteSpec` is `Named|Url` only (protocol.rs:979-982).

**Publication.**
- Each repo has one lane, and its batches are atomic.
- `tag`, `update-ref`, `notes`, `rebase`, `revert` and `fast-import` publish directly, outside the lane (docs/coordinator.md).
- Every commit goes through `Store::publish` (store/mod.rs:911).

**Daemon.**
- `InvocationOpen{repo, method, request_id, args_json, session_id}` (daemon/mod.rs:305-320).
- `CommandOpen{argv, ...}`, whose `session_id` is "accepted and ignored" (mod.rs:389-413).
- Outcome lookup on reconnect (ipc.rs:40-44).

**Go client.** forgejo/modules/git/ruk_ipc.go:246-253 (`RukOpenRequest`) is argv-only. backend_ruk.go:403 sends the fixed `rukServiceSessionID`.

**Ref advertisement.** The v0 advertisement (ruk-git/src/transport/serve.rs:106) has no hidden-ref filter. v2 `ref-prefix` is only a client-side filter (serve_v2.rs:419-422).

**File reads.** `file{at, path}` returns `FileBody::{Blob{oid,size,content: ByteSource}, Symlink, Gitlink, Directory{entries}, Conflict{terms}}` (protocol.rs:2404-2443).

**Missing.** There is no search method (protocol.rs:3285-3333).

## 2. Placement

### 2.1 In Ruk (per repo; canonical = refs and objects)

| Object | Canonical form | Derived index | Exported to mirrors |
|---|---|---|---|
| Task | `refs/ruk/r/task/<n>` | `Records` | if `carry_records` |
| Task claim | fact `RecordLeases`; acquire and release are also appended to task history | none | no |
| Review threads and comments | `refs/ruk/r/review/<change-id>` | `Records` | if `carry_records` |
| Votes, CI results | attestations; canonical = `refs/notes/ruk-attest` | `Attestations` rows | if `carry_records` |
| Release notes and asset manifest | `refs/ruk/r/release/<tag>` plus the tag | `Records` | if `carry_records` |
| Release assets | Ruk LFS-object area, outside the DAG `[inference: reuse LfsObjects]` | none | no |
| Settings (default branch, protections) | `refs/ruk/r/settings/repo` | `Records` | no |
| Mirror config | `refs/ruk/r/mirror/<ulid>` (holds `SecretRef` only) | `Records` | never |
| Webhook config | `refs/ruk/r/hook/<ulid>` (holds `SecretRef` only) | `Records` | never |
| Change feed | none; derived per publish | fact `Events` | no |
| Search sketch | none | fact `BlobSketch` (cache) | no |

### 2.2 In meta

**Identity:** users (argon2id, TOTP), oidc_identities, ssh_keys.

**Sessions and tokens:** web_sessions, tokens (hash, scopes, repo filter), and ruk_sessions (one Ruk service session per repo plus its reconnect secret).

**Membership:** orgs, teams, team_members, team_repos, collaborators.

**Queues:** jobs (leased) and feed_cursors.

**Deviations, D1–D6 (§9):**
- repos registry;
- stars and watches;
- secrets;
- CI run metadata and logs;
- inbox;
- org-level hooks, if wanted.

## 3. Ruk changes (breaking allowed)

### 3.1 Records: new module `crates/ruk-core/src/collab/`

**Kinds.** `RecordKind` is closed: `{Task, Review, Release, Settings, Mirror, Hook}`.

**Ids:**
- task: a u64 sequence allocated in the lane;
- review: ChangeId;
- release: tag name;
- settings: `repo`;
- mirror and hook: ULID.

**Commit layout.** One mutation is one commit on the record ref. The tree holds:
- `record.json` (canonical snapshot);
- `event.json` (op, actor, at, request_id);
- `body.md`;
- `comments/<seq>.md`.

**`Records` index (derived)** is rebuilt by `record.reindex`. Its key families:
- `0x00 kind` → next sequence number;
- `0x01 kind‖id` → {rev, snapshot ≤64 KiB};
- `0x02 kind‖state‖updated_desc‖id`;
- `0x03 kind‖assignee‖state‖updated_desc‖id`.

**`RecordLeases`.** Key `kind‖id` → {holder ActorRef, token_id, epoch, expires_at}, on the coordinator clock. It is not op-tracked and not replicated.

**`Events` (derived inside `Store::publish`).**
- **Covers every writer.** Lane and direct publishes alike append rows in the same redb transaction. `publish` gains an `origin: Origin{actor, session, method, request_id}` parameter.
- **Rows are derived from the batch's mutations:**
  - `Refs` → `RefUpdated{name, old, new}`;
  - `Refs` under `refs/ruk/r/` → `RecordChanged{key, rev}`;
  - `Changes` → `ChangeUpdated`;
  - Attestations order-index inserts → `AttestationAdded{subject, kind, signer, on_behalf_of}`;
  - a `LandPlans` delete together with a Refs move → `LandExecuted`.
- **Key:** generation‖seq.
- **Retention:** by age and size. Reads older than the retention floor fail with `E_GAP`.
- **Shared feed:** this one table feeds both the host catalog and rukie.

**Reserved namespaces.** `refs/ruk/**` and `refs/notes/ruk-attest` are written only by `record.*` and `attest.*`. Receive-pack, `update-ref`, `bookmark.set` and `notes` refuse them with `E_RESERVED`. The refusal is unconditional, because the daemon peer is always rukie.

**Undo.** Refs are op-tracked, so undo restores record refs and re-derives their `Records` rows in the same batch. Its `Events` follow from the ref mutations.

**Authority split.** Settings never grant capability. `policy.toml` (unversioned, operator-owned, policy.rs:23-49) stays the only capability authority. Protections can only add land constraints.

**Schemas (v1, validated in Ruk).** `ActorRef` is `"rukie:<instance-uuid>:u:<user-ulid>"`.

```
Task     {number, title≤256, state: Open|Closed{Done|WontDo|Duplicate{of}}, labels, assignees: Set<ActorRef>,
          links: Vec<Change|Commit|Task>, created_by, created_at, updated_at, comment_count}
Review   {change, target, state: Open|Landed{commit}|Abandoned, reviewers,
          threads: Vec<{id, anchor: Line{commit, path, side, lines}|Change, resolved, comments: Vec<u32>}>}
Release  {tag, title, draft, prerelease, assets: Vec<{name, sha256, size, media_type}>}
Settings {default_branch, description, protections: Vec<Protection>}
Protection {pattern: RefPattern, land_only: bool, plan_ttl: Duration(≤24h, default 1h),
            require: Vec<LandRequirement>}
Mirror   {url, mode: ExactForcePrune|FastForwardOnly, refs: All|DefaultAndTags|Globs, carry_records, credential: SecretRef, reconcile_interval}
Hook     {url, events: Vec<EventKind>, active, secret: SecretRef}
```

### 3.2 Methods and breaking changes

**New methods** (append-only, protocol.rs:3123-3130):

| Method | Kind | Contract |
|---|---|---|
| `record.create` | Mutation | {kind, body, id?} → {key, rev, number?} |
| `record.update` | Mutation | {key, expected_rev, ops: Vec<RecordOp>, fence?} → {rev}; fails `E_STALE` or `E_FENCED` |
| `record.get` | Read | {key, at?} → record |
| `record.log` | Read | {key, page} → history |
| `record.list` | Read | {kind, filter, order, page} |
| `record.lease.acquire` | Mutation | {key, ttl, holder, token_id} → {epoch, expires_at}; fails `E_LEASED` |
| `record.lease.renew` | Mutation | {key, epoch, ttl} |
| `record.lease.release` | Mutation | {key, epoch} |
| `record.export` | Mutation | {kinds}; incrementally edits `refs/ruk/export/<kind>`, a tree sharded `<id mod 256>/<id>`, applying only the records changed since its stored cursor |
| `record.reindex` | Mutation | rebuilds `Records` |
| `events.read` | Read | {after, limit} → {events, next, oldest_retained}; fails `E_GAP` |
| `attest.import` | Mutation | {notes_commit}; verifies each note via `parse_export` → `prepare_batch` and moves the notes ref only if all verify |
| `attest.reindex` | Mutation | rebuilds `Attestations` rows from the notes ref |
| `remote.ls` | Read | {remote, auth} → refs (needed for prune) |
| `search` | Read | {at, pattern, paths, case, budget} → streamed matches |

**Host methods (daemon):**
- `host.repo.create`, `host.repo.fork` (shares objects `[inference]`), `host.repo.delete`;
- `host.catalog.list`, `host.catalog.rebuild`;
- `host.events.follow` → stream of {repo, generation}.

**Breaking changes to existing calls:**

1. **`attest.add` writes the notes ref.** One batch holds the rows plus a commit on `refs/notes/ruk-attest`. Each note is the JSON `AttestAddArgs` array for one subject commit, sorted by digest.
2. **Attestation statement v2** adds `on_behalf_of: Option<ActorRef>` and `detail: Option<String>`.
   - The group key becomes `(subject, kind, signer, on_behalf_of)`, so each reviewer is a separate group under the shared forge key.
   - Supersession stays inside one group.
   - The existing rules are unchanged: one exact commit subject, SSHSIG namespace `ruk-attest`, signer derived from the key.
3. **Hosted land** (`RemoteSpec::Local`):
   - **Prepare** computes the stack itself: the change plus its unlanded ancestor changes, ordered bottom→top. It rebases them in order onto the target tip, and a conflict fails `E_CONFLICT{change}`.
   - **Stored plan:**
     - `stack: Vec<{change, source_head, rebased, clean: bool}>`;
     - an explicit `tip`, which replaces "first of the sorted set";
     - `base`;
     - requirements loaded from the matching `Protection`; caller-supplied policy is refused.
   - **`LandRequirement`** is {kind, result, signers, `min_distinct_actors`, `applies_to: Tip|EachChange`, `scope: Exact|SourceHeadIfClean`}. CI is typically Tip+Exact; review is EachChange+SourceHeadIfClean.
   - **Prepare reply** gains `gaps: Vec<Gap{commit, requirement}>`, reusing `attestation::Gap`.
   - **Execute** requires all of:
     - the tip equals the base;
     - the tip descends from the base;
     - every `rebased` commit lies in `base..tip`.
     It then re-evaluates requirements per `applies_to`. In one batch it moves the target, consumes the plan, and sets `Review.Landed` for every stacked change.
   - **One live plan per target.** `LandPlans` gains the key `0x02 target → plan`. A second prepare fails `E_BUSY{plan}`.
   - **TTL** comes from `Protection.plan_ttl`; the constant `LAND_PLAN_TTL` is removed.
4. **Protected refs.** Receive-pack refuses direct pushes to a `land_only` ref (`E_LAND_ONLY`), so the base moves only through `land.execute`.
5. **Actor on every open.** `InvocationOpen.actor` and `CommandOpen.actor` are honoured only from a principal holding the new capability `assert_actor`. The actor is recorded in `OperationEntry` and in the Events origin.
6. **Native `refs/for/<branch>`.** A Change-Id trailer creates or updates the change, with no proc-receive callback.
7. **Hidden refs.** Prefixes `refs/ruk/` and `refs/notes/ruk-attest` are omitted from the v0 advertisement (serve.rs:106) and from v2 `ls-refs` (serve_v2.rs).
8. **Push credentials.** `PushArgs.auth: Option<EphemeralAuth>` is excluded from the digest and never persisted.

### 3.3 Host catalog: `crates/ruk/src/daemon/catalog.rs`

- Its own redb at `<root>/.ruk-host/catalog.redb`.
- Key families:
  - `C1 repo‖kind‖state‖updated_desc‖id`;
  - `C2 kind‖state‖assignee‖updated_desc‖repo‖id`;
  - `C3 kind‖state‖updated_desc‖repo‖id`;
  - counters `K repo‖kind‖state`;
  - cursors `P repo → generation`.
- The applier tails each repo's `Events`. The coordinator notifies it in-process. At startup it catches up any repo whose cursor is behind.
- The catalog is derived and can be rebuilt.
- It drains on `shutdown_and_drain` (registry.rs:12-14).

## 4. Query and listing performance

**Use case:** open tasks across the 500 repos a user can see, page of 50, newest first.

1. rukie computes the visible repo set from meta and caches it for 30 s.
2. rukie calls `host.catalog.list{repos, kind: task, state: open, assignee?, order: updated_desc, limit: 50}`.
3. The catalog picks a strategy:
   - ≤2000 repos: k-way merge over C1 prefixes;
   - assignee filter: C2 prefix scan filtered by a repo hash set;
   - otherwise: C3 scan with a filter.
4. Badge counts read K counters in O(1).

**Cost estimate** `[inference]`:
- 500 seeks, a 500-entry heap and 50 decodes: about 5–10 ms;
- IPC: about 2 ms;
- render: about 3 ms.

**Target:** p95 ≤ 60 ms server-side. Before locking the design, run a benchmark gate with 500 repos × 2000 tasks.

**Freshness:** p99 lag < 1 s. The reply lists `stale_repos`.

**Rejected alternatives:**
- Fanning out per-repo `record.list` means 500 invocations plus cold coordinator opens: about 0.5–3 s `[inference]`.
- Keeping task copies in meta.

**Per-repo `record.list`:** p95 ≤ 15 ms.

**Code search (settled feature, kept minimal):**
- `search` walks the tree at a revision. It skips binary blobs and blobs over 1 MiB.
- It consults a lazy 256-byte trigram bloom per blob oid. Blob oids are content-addressed, so the bloom is shared across revisions.
- Search is budget-bounded.
- Cross-repo search fans out with concurrency 8 and stops at 100 hits or 2 s.

## 5. Consistency between Ruk and meta

**Rule:** Ruk is authoritative for repo scope, and meta for instance scope. Everything derived across that boundary carries a cursor and can be rebuilt. There are no distributed transactions. A write takes one of two shapes:
- **Ruk-first:** later work is derived from `Events`. The meta jobs and the cursor advance in one meta transaction.
- **Meta saga:** Ruk calls are idempotent because each carries a request id.

| # | Invariant | Enforcement | Repair |
|---|---|---|---|
| I1 | An active repo row's `store_id` equals the Ruk store-id | checked at first open | reaper marks the row `broken`, or quarantines the store dir after 24 h |
| I2 | Repo create runs `provisioning` → `host.repo.create` → `active` | `repo` saga | retry or roll back after 10 min |
| I3 | Every ActorRef resolves to a user, or renders as a ghost | at read time | none |
| I4 | Jobs and the cursor commit in one meta transaction; unique on (repo, gen, seq, consumer) | `feed` | re-read from the cursor |
| I5 | `E_GAP` triggers reconcile | `feed` | mirrors re-sync; CI rescans open stacks and protected tips; hooks get `repo.resync`; inbox gets a notice |
| I6 | Every `SecretRef` exists | at use | status `credential_missing` |
| I7 | A lease holder's token is valid | token revoke enqueues `record.lease.release` | expiry |
| I8 | Catalog matches `Events` | applier cursor | `host.catalog.rebuild` |
| I9 | `Records` matches the record refs | same `publish` transaction | `record.reindex` |
| I10 | `Attestations` rows match `refs/notes/ruk-attest` | same `attest.add` batch; `attest.import` verifies | `attest.reindex` |
| I11 | Ruk trusts the actor only from rukie | `assert_actor` capability | none |
| I12 | `request_id = SHA-256(actor‖op‖Idempotency-Key)` (or MCP call id) under the per-repo service session | `rukc` | receipts replay; no cross-user collision |
| I13 | Rename or transfer touches only meta; store dirs are named by repo ULID | `repo` | none |

## 6. Go package layout (20 packages; imports point down only)

| Package | Responsibility |
|---|---|
| `cmd/rukie` | serve, admin, ssh-keys, git-shell; client mode `rukie <op> --json` driven by the embedded op table |
| `internal/op` | op table (name, route, in/out types, scope, authz, handler); emits OpenAPI 3.1 and MCP schemas |
| `internal/httpapi` | JSON API, RFC 9457 errors, Idempotency-Key |
| `internal/web` | html/template, forms, zero JS |
| `internal/mcpserver` | MCP over the op table |
| `internal/auth` | argon2id, TOTP, OIDC, sessions, scoped tokens |
| `internal/meta` | the only metadata-store package; one interface, one implementation |
| `internal/rukc` | Ruk client (below) |
| `internal/record` | Go mirrors of the Ruk record kinds, ActorRef |
| `internal/repo` | lifecycle saga, browse, forks, stars, watches, explore, search fan-out |
| `internal/task` | tasks and leases |
| `internal/review` | changes, stacks, branch view, threads, votes, land queue |
| `internal/gitwire` | smart HTTP; SSH via git-shell over `/run/rukie/internal.sock` |
| `internal/feed` | `host.events.follow` + `events.read` → jobs |
| `internal/jobs` | leased runner with heartbeat, fence epoch, backoff, dead letter |
| `internal/webhook` | Standard Webhooks |
| `internal/mirror` | plan, push, reconcile, ticker |
| `internal/ci` | config, scheduler, runner API, result reuse |
| `internal/sign` | SSHSIG signer for the review and CI keys |
| `internal/platform` | config, slog, clock, lifecycle Group |

**`internal/rukc`:**
- Frames, credits and outcome lookup are ported from forgejo/modules/git/ruk_ipc.go.
- New Go work:
  - a typed `Invoke` (`InvocationOpen` with `method`, `args_json`, `session_id`, `actor`);
  - per-repo session open/resume;
  - `actor` on command opens;
  - Go types generated from `ruk schema`.

**Dependency order:** transports → `op` → domain → {`record`, `rukc`, `meta`, `sign`, `jobs`} → `platform`. Every goroutine registers with `platform.Group`.

## 7. Request flows

### (a) Render a file at a revision. p95 ≤ 40 ms warm, ≤ 70 ms cold store

1. `GET /{o}/{r}/-/blob/{rev}/{path...}` reaches `web` op `file.show`.
2. `auth` resolves the user. `repo.Resolve` reads the meta row (LRU, 5 s) and checks permission.
3. One call: `rukc.Invoke(handle, "file", {at: rev, path})`. Each `FileBody` arm renders as follows:
   - `Blob`, Inline content: render as text, with `#L<n>` anchors. Show binary blobs (NUL in the first 8 KiB) as a download link.
   - `Blob`, Streamed content, or `size` > 256 KiB: show a size notice and `/-/raw` (streamed with credits).
   - `Symlink`: show the target as a link to the resolved path at the same revision.
   - `Gitlink`: show the submodule commit oid with no link `[inference: no submodule URL resolution]`.
   - `Directory`: render the tree page from `entries` in the same response; paging comes from the `page` cursor.
   - `Conflict`: render each term of the `Merge` (side label + content or "absent") with a "conflicted at this revision" banner.
4. Caching:
   - Blob: `ETag: "<Blob.oid>"`. When rev is a full oid, add `Cache-Control: immutable` (public repos) or `private`.
   - Symbolic rev: `no-cache` plus the ETag.
5. Failures:
   - `E_NOT_FOUND` → 404;
   - daemon down → 503 with Retry-After;
   - store quarantined → 503;
   - `E_LIMIT` → 429.

### (b) Land a change with required attestations. p95 ≤ 100 ms excluding CI

Example protection on `refs/heads/main`, with `land_only` set:
- `review=approve`, EachChange, SourceHeadIfClean, `[review-key]`, `min_distinct_actors=1`;
- `ci/test=pass`, Tip, Exact, `[ci-key]`.

1. The client sends `change.land{repo, change, target}`. Authz requires write permission and scope `change:land`.
2. `review` enqueues `land.drive{repo, target, change}`. The land queue is FIFO per (repo, target), and only one is active at a time.
3. The active job calls `land.prepare{change, remote: Local, target}`.
   - Ruk computes the stack, rebases it, and stores plan {tip, stack, base, requirements, expires_at}. It returns the plan with `gaps`.
   - If another plan is live, Ruk returns `E_BUSY` and the job waits.
4. For each gap:
   - **Tip + CI gap:** enqueue `ci.run{commit: tip, job}`, deduplicated.
     - **Result reuse:** if `ci_cache(tree, job digest)` already holds a pass, the CI key re-signs for the tip and the job does not run.
   - **EachChange review gap:** the change page shows "needs approval". Votes made through op `review.vote` become `attest.add` statements (forge review key, `on_behalf_of`) on `source_head`.
5. The CI runner finishes. `ci` checks the job epoch, `sign` builds the statement {subject: tip, kind, result, detail: log URL}, and rukie calls `attest.add`. This publishes the notes commit, the rows and `Events.AttestationAdded`.
6. `feed` wakes the active `land.drive`. It calls `land.execute{plan, tip}`. Ruk checks:
   - tip equals base;
   - ancestry;
   - live per-requirement evaluation.
   In one batch it moves main, consumes the plan and sets `Review.Landed`. Events: RefUpdated and LandExecuted.
7. `feed` fans out to mirrors, hooks (`change.landed`), post-merge CI and the inbox. The queue advances.

Why this does not starve: main is `land_only`, so only `land.execute` moves it, and plans are serialized per target. So `E_STALE` can only follow an operator override, and the queue re-prepares. The TTL is `plan_ttl`, and an expired plan is re-prepared.

Failures:
- CI fail → the plan is dropped and the queue advances;
- an untrusted signer → 422;
- `E_CONFLICT` on rebase → the change is marked "needs rebase";
- a crash mid-execute → the same `request_id` replays the receipt, or a second executor loses the plan-row expectation (land.rs:43-45).

### (c) Push over smart HTTP, triggering mirror, webhook and CI. Overhead p95 ≤ 30 ms beyond pack transfer

1. `GET info/refs?service=git-receive-pack`:
   - `gitwire` Basic auth, with the token as password;
   - scope `repo:write` and permission check;
   - `rukc.Command(handle, ["receive-pack", "--stateless-rpc", "--advertise-refs", "."], actor)`. The advertisement omits hidden prefixes.
2. `POST git-receive-pack`: the body is piped as Stdin frames under the credit window, and the reply is streamed. On client disconnect rukie sends Cancel; the result is either `CancelledBeforeCommit` or a completed run found by outcome lookup.
3. Inside Ruk:
   - quarantine incoming objects;
   - refuse `refs/ruk/**` and `refs/notes/ruk-attest`, plus `land_only` targets;
   - `refs/for/<b>` → change create/update;
   - CAS the refs;
   - `Store::publish` appends RefUpdated and ChangeUpdated, with `origin.actor` set to the pusher.
   rukie reports success only after the commit is durable.
4. `host.events.follow` emits a notification. `feed` calls `events.read{after}` and, in one meta transaction, inserts:
   - `webhook.deliver` per matching Hook record (the job key is the event id, which is also the `webhook-id`);
   - `mirror.push`, coalesced per (repo, mirror);
   - `ci.plan{repo, ref, oid}`;
   - `inbox.fanout`.
   It then advances the cursor.
5. `mirror` job:
   1. Read the record and the secret.
   2. ExactForcePrune: `remote.ls`, then force updates plus deletes for the selected refs. FastForwardOnly: non-force updates only.
   3. With `carry_records`: `record.export`, then push `refs/ruk/export/*` and `refs/notes/ruk-attest`.
   4. Call `push{remote: Url, updates, NonAtomic, auth}`.
   5. An `Uncertain` result goes to `push.reconcile`. A non-ff rejection in FastForwardOnly mode sets `diverged` and notifies admins.
   6. A periodic ticker runs reconcile.
6. `webhook` job:
   - POST with `webhook-id`, `webhook-timestamp` and `webhook-signature: v1,<base64 HMAC-SHA256>` (https://www.standardwebhooks.com/);
   - retry with backoff for 24 h, then dead letter and an inbox notice.
7. `ci` job:
   - read `.rukie/ci.toml` at the oid via `file`;
   - lease jobs to long-polling runners with a heartbeat;
   - a lost runner leads to requeue with a new epoch;
   - a late result with a stale epoch is refused before signing.

## 8. Failure paths

| Failure | Behaviour |
|---|---|
| Ruk daemon down | repo pages, API and git return 503 with Retry-After; identity pages keep working; feed pauses with cursors kept |
| Daemon restart mid-call | outcome lookup by `request_id`; mutations replay their receipts |
| `E_LIMIT` admission | 429/503; jobs back off |
| Store quarantined (attestation verification latch) | repo read-only; admin inbox notice |
| `E_STALE` on a record | the HTML form re-renders with current state; the API returns 409 with `rev` |
| `E_FENCED` or `E_LEASED` | 409 with holder and expiry |
| `E_GAP` | reconcile (I5) |
| A direct publish outside the lane | still produces Events, because they are derived in `Store::publish` |
| Forged or invalid note on `attest.import` | the notes ref move is refused and the repo keeps its old notes |
| Meta down | 503 (git reads included; no meta means no authz) |
| Mirror auth, network or divergence failure | status recorded, backoff, notice |
| Webhook endpoint down | backoff, then dead letter |
| CI runner lost | requeue; the fence blocks stale signatures |
| Land `E_CONFLICT`, `E_BUSY` or expiry | needs-rebase status / wait / re-prepare |
| Ruk disk full (`E_IO`) | 503; publication is atomic |


[657 more lines in resource. Use :1207 to continue]

## 9. Red-flag self-check

1. **rukie can forge every vote and CI result (accepted with mitigations).** The forge review and CI keys sign on behalf of users, and `min_distinct_actors` counts actors that rukie asserts. A compromised rukie, or a leaked key, can forge every approval that land accepts. Mitigations:
   - separate review and CI keys, sealed with the instance key-encryption key and never exported;
   - an audit Event for every signature;
   - protections may list user-held keys instead: agents sign client-side and `review.vote` accepts a pre-signed statement;
   - an option for runner-held CI keys, with the job epoch inside `detail`.
2. **Coupling.** Kind schemas live in Rust, so every field change needs a Ruk release. Accepted as the cost of Ruk-enforced validation and land policy.
3. **Second index.** The host catalog is derived from `Events`. Gerrit made the same move.
4. **Erasure and GDPR (blocks tasks GA).** Record history is immutable git, and carried mirrors keep copies. A `record.redact` design is required: history rewrite plus a tombstone event.
5. **Meta deviations from "identity, sessions, tokens, membership, queues":**
   - **D1 repos registry:** the namespace cannot live inside the repo it names.
   - **D2 stars and watches:** per-user preferences, classed under identity.
   - **D3 secrets:** credentials, classed with tokens; they must never replicate.
   - **D4 CI run metadata and logs:** operational, adjacent to queues; results are attestations.
   - **D5 inbox:** a derived queue.
   - **D6 org-level hooks:** only if needed; repo hooks live in Ruk.
6. **Land throughput.** Serial per-target landing caps throughput at one CI cycle per landing. Next step: batch the queued stacks into one plan (the candidate set supports up to 1024; land.rs:70) with bisect on failure.
7. **Hidden refs are a hard dependency.** Without §3.2 item 7, every clone and push advertises every record ref.
8. **Ref count.** About 100k task refs is fine in redb `[inference]`. Export refs are sharded and updated incrementally. Whether GitHub and GitLab accept `refs/ruk/export/*` is unverified `[inference]`.
9. **Divergent replicas.** Excluded in v1: replication is push-only, and `attest.import`/`record.import` need fast-forward or an empty destination (`E_DIVERGED`).
10. **Ruk on every page.** p95 targets assume one Ruk call per page. Multi-object pages use `record.list` or the catalog.
11. **Engineering bar:**
    - no secret enters Ruk;
    - goroutines register with `platform.Group`;
    - `Settings` never grants capability.

## 10. Commit sequence

1. **Ruk, commit 1:** `collab` module; `Records`, `RecordLeases` and `Events` (inside `Store::publish`); `record.*`; `events.read`; reserved and hidden namespaces.
2. **Ruk, commit 2:** attest v2 group key and notes bridge (`attest.import`, `attest.reindex`); hosted land (stack rebase, tip, ancestry, `applies_to`/`scope`, per-target single plan, `plan_ttl`); `land_only`; actor on both opens; native `refs/for`.
3. **Ruk, commit 3:** host catalog; `search` with `BlobSketch`; `remote.ls`; push auth; `record.export`.
4. **rukie:** `platform` → `meta` → `rukc` (typed path) → `op` → auth → repo/gitwire → record/task/review → feed/jobs → webhook/mirror/ci → transports. Gitea surfaces are deleted in the same commit series, and every commit builds.

## Critical Files

- `crates/ruk-core/src/store/types.rs`, `store/mod.rs:911`: fact tables; `Store::publish` emits Events
- `crates/ruk-core/src/coord/land.rs`: candidate ordering, TTL, execute checks to rework
- `crates/ruk-core/src/attestation.rs` (+ `attestation/layout.rs`, `export.rs`): group key, notes bridge, index pattern
- `crates/ruk/src/daemon/mod.rs`, `crates/ruk-git/src/transport/serve.rs`: actor on opens, hidden refs, reserved-namespace refusal
- `forgejo/modules/git/ruk_ipc.go`: Go client to port and extend with typed invocations
~~~~

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/land.rs` — today it orders candidates by object id, uses a fixed 1h expiry and has no ancestry check; landing needs a stack rebase, an explicit tip and one plan per branch.
- `ruk/crates/ruk-core/src/store/mod.rs` and `ruk/crates/ruk-core/src/store/types.rs` — where change events must be derived inside `Store::publish`, and where the new tables (`Records`, `RecordLeases`, `Events`, `BlobSketch`) go.
- `ruk/crates/ruk-core/src/attestation.rs` (with `attestation/layout.rs` and `attestation/export.rs`) — the group key gains `on_behalf_of`, plus the `refs/notes/ruk-attest` bridge and the index pattern that `Records` copies.
- `ruk/crates/ruk/src/daemon/mod.rs` and `ruk/crates/ruk-git/src/transport/serve.rs` — `actor` on both opens, hidden-ref advertisement, and unconditional refusal of reserved namespaces.
- `ruk/forgejo/modules/git/ruk_ipc.go` — the existing git-command-only Go client to port into `internal/rukc` and extend with typed calls.

[advisor] <advisory advisor="qwen" severity="blocker" guidance="weigh, don't blindly obey"> Correct flow (b) and §3.2. land.prepa…

---
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/prepare.rs:3420-3490) ⇒ ok · 88 lines

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Emit `Events` rows at the store co…

---
I've revised the sketch again after checking the Ruk source. This session has no write tool, so the file still isn't on disk. Save the text between the `~~~~` fences verbatim to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/arch-sketch-rukrecords.md`; it replaces both earlier drafts.

**What changed in this round:**
- **Hosted landing is thinner than I had described.** `land_prepare_parts` (`coord/prepare.rs:3431-3484`) does three things only:
  - it freezes the change's current head as the single candidate, with no rebase;
  - it takes the live branch tip as the base;
  - it sends empty requirements and trusted signers, with `policy_generation = 0`, and calls the policy source "follow-up work".

  The reply's doc comment says the base is the "tip the candidate was rebased onto" (`protocol.rs:2049`), but no code does that. The grounding section now says this. The proposed work is now an explicit list of new Ruk items:
  - resolve the stack;
  - rebase it in order with a conflict check, which also yields the "clean" flag that review scoping needs;
  - store an explicit tip;
  - check that the tip descends from the base;
  - load requirements from branch protection and enforce them;
  - report gaps per candidate.
- **CI runners now have a way to fetch the candidate.** `land.prepare` writes a hidden ref `refs/ruk/land/<plan>` pointing at the tip, which also stops the commit being garbage-collected. `land.execute` or the expiry sweep deletes it. Runners download the source as a tar file through rukie's runner API, streamed from Ruk's upload-archive server (`ruk-git/src/transport/mod.rs`, `[inference: archive serves arbitrary oids]`).
- **The event sequence number is now `u32`, one row per ref changed.** A `u16` overflows when one push carries something like 70k tags, and one generation can hold up to 64 lane requests (`coord/lane/batch.rs:24`).
- **The forge-key red flag now offers a separate signer process.** A `rukie-signer` process would hold the keys, rate-limit signing and audit every signature. That is in addition to user-held keys and runner-held CI keys.

**Already handled in the previous revision, no change needed:**
- change events are written inside `Store::publish`, so direct publishes are covered;
- when the local store is down, everything returns 503, including git reads.

~~~~markdown
# Architecture sketch C: "Ruk as system of record"

Status: plan-mode sketch, revision 3, 2026-10-20.
Paths are relative to /home/alpha/rewrite/git-renewed/ruk.
`[inference]` marks a claim not checked against code.

## 0. Thesis

Each repository's Ruk store holds everything about that repository that must move with it:
tasks, review threads, review votes, CI results, release notes, land policy, mirror config and webhook config.

The canonical form is git objects under reserved refs:
- one ref per record: `refs/ruk/r/<kind>/<id>`;
- attestations: notes ref `refs/notes/ruk-attest`.

Fact-table rows are derived indexes. They are written in the same store commit as the refs and can be rebuilt from them.

rukie's local store ("meta") holds only instance facts:
- identity, sessions, tokens;
- orgs, teams, grants;
- queues derived from Ruk events.

Deviations from that list are in §9.

Precedent: Gerrit NoteDb keeps change metadata in refs and adds a secondary index for cross-project queries
(https://gerrit.googlesource.com/gerrit/+/be911167d6a0aa6b8b568ae06a60221e24d945f5/Documentation/note-db.txt).

## 1. What Ruk does today

### Storage
- Fact tables: 27 redb tables, a closed set (crates/ruk-core/src/store/types.rs:229-300).
- Op-log tracked tables (coord/oplog.rs:48-78):
  - tracked: Refs, RefConflicts, Changes, ChangeHistory, Workspaces, WsIndex, Pins;
  - not tracked: Attestations, Claims, Leases, LandPlans.
- The `records` module covers only commits, changes and history (records.rs:1-11). There is no task, thread or release type.
- Every commit goes through `Store::publish` (store/mod.rs:911).
- Commands that publish directly, outside the lane: `tag`, `update-ref`, `notes`, `rebase`, `revert`, `fast-import` (docs/coordinator.md).
- Lane batching: up to 64 requests per generation (coord/lane/batch.rs:24).

### Attestations
- A statement is {subject Oid, kind, result, signed_at, supersedes} (protocol.rs:1776-1793, attestation.rs:1-41).
- Signature: SSHSIG, ssh-ed25519, namespace `ruk-attest`. The signer is derived from the key.
- Grouping: one group per (subject, kind, signer). Disagreeing live statements in a group become `Ambiguous` (attestation.rs:20-27).
- Kind and result are free strings (capability.rs:201-216).
- Attestations are **not replicated**:
  - git export reports attestation rows as lost (ruk-git/src/export/report.rs:49-51);
  - export copies `refs/notes/ruk-attest` only if it already exists;
  - nothing in ruk-core writes that ref.

### Claims and leases
- Claims are path locks held by a session (protocol.rs:1700-1767).
- Leases are GC-root leases (coord/leases.rs:1-23).
- Neither is a task-claim primitive.

### Land
- **Core** (coord/land.rs):
  - candidates come from the caller and are sorted by Oid (239-241);
  - the primary candidate is `first()`, i.e. the lowest Oid (131-132);
  - every candidate must meet every requirement (386-400);
  - execute checks that the tip equals the base, then moves the target to the primary;
  - there is no ancestry check (380-416);
  - plan TTL is the constant `LAND_PLAN_TTL` = 1h (67).
- **Hosted path** (`land_prepare_parts`, coord/prepare.rs:3431-3484):
  - freezes the change's resolved head as the single candidate;
  - takes the live target tip as the base;
  - does **not rebase**;
  - sends empty requirements and trusted signers, with `policy_generation = 0`;
  - checks only the `land` capability ref pattern.
- The reply's doc comment says "target tip the candidate was rebased onto" (protocol.rs:2049). No code does that.
- `RemoteSpec` has only `Named` and `Url` (protocol.rs:979-982).

### Daemon and Go client
- `InvocationOpen{repo, method, request_id, args_json, session_id}` (daemon/mod.rs:305-320).
- `CommandOpen.session_id` is "accepted and ignored" (daemon/mod.rs:389-413).
- Outcome lookup on reconnect (ipc.rs:40-44).
- The Go client `forgejo/modules/git/ruk_ipc.go:246-253` speaks only the argv form (`RukOpenRequest`). It sends a fixed `rukServiceSessionID` (backend_ruk.go:403).

### Transport and reads
- The v0 ref advertisement has no hidden-ref filter (ruk-git/src/transport/serve.rs:106).
- v2 `ref-prefix` is only a client-side filter (serve_v2.rs:419-422).
- An upload-archive server exists (ruk-git/src/transport/mod.rs).
- `file{at, path}` returns `FileBody` = Blob{oid, size, content: ByteSource} | Symlink | Gitlink | Directory | Conflict (protocol.rs:2404-2443).
- There is no search method (protocol.rs:3285-3333).

## 2. Placement

### 2.1 In Ruk (canonical form is refs plus objects)

| Object | Canonical form | Derived index | Exported to mirrors |
|---|---|---|---|
| Task | `refs/ruk/r/task/<n>` | `Records` | if `carry_records` |
| Task claim | fact table `RecordLeases`; acquire and release also appended to task history | none | no |
| Review threads and comments | `refs/ruk/r/review/<change-id>` | `Records` | if `carry_records` |
| Votes, CI results | attestations; canonical form `refs/notes/ruk-attest` | `Attestations` rows | if `carry_records` |
| Release notes and asset manifest | `refs/ruk/r/release/<tag>` plus the tag | `Records` | if `carry_records` |
| Release assets | Ruk LFS-object area, outside the commit graph `[inference: LfsObjects reusable]` | none | no |
| Settings (default branch, protections) | `refs/ruk/r/settings/repo` | `Records` | no |
| Mirror config | `refs/ruk/r/mirror/<ulid>`; holds `SecretRef` only | `Records` | never |
| Webhook config | `refs/ruk/r/hook/<ulid>`; holds `SecretRef` only | `Records` | never |
| Land candidate pin | `refs/ruk/land/<plan>` | `LandPlans` | never |
| Change feed | none (derived) | fact table `Events` | no |
| Search sketch | none (cache) | fact table `BlobSketch` | no |

### 2.2 In meta
- **Identity:** users (argon2id hash, TOTP), oidc_identities, ssh_keys.
- **Sessions and tokens:** web_sessions; tokens (hash, scopes, repo filter); ruk_sessions (one Ruk service session per repo plus its reconnect secret).
- **Membership:** orgs, teams, team_members, team_repos, collaborators.
- **Queues:** jobs (leased), feed_cursors.
- **Deviations D1–D6** (see §9):
  - repos registry;
  - stars and watches;
  - secrets;
  - CI run metadata and logs;
  - inbox;
  - org-level hooks, only if needed.

## 3. Ruk changes (breaking allowed)

### 3.1 Records: new module `crates/ruk-core/src/collab/`

Record kinds:
- `RecordKind` is closed: {Task, Review, Release, Settings, Mirror, Hook}.
- Id per kind:
  - task: u64 sequence, allocated in the lane;
  - review: ChangeId;
  - release: tag name;
  - settings: the literal `repo`;
  - mirror and hook: ULID.

Commit per mutation:
- Each mutation is one commit on the record ref.
- The commit tree holds `record.json` (canonical snapshot), `event.json` (op, actor, at, request_id), `body.md`, and `comments/<seq>.md`.

`Records` index:
- Derived; rebuilt by `record.reindex`.
- Key families:
  - `0x00 kind` → next sequence number;
  - `0x01 kind‖id` → {rev, snapshot ≤64 KiB};
  - `0x02 kind‖state‖updated_desc‖id`;
  - `0x03 kind‖assignee‖state‖updated_desc‖id`.

`RecordLeases`:
- Key `kind‖id` → {holder, token_id, epoch, expires_at}, timed by the coordinator clock.
- Not tracked by the op log and not replicated.

`Events`:
- Emitted inside `Store::publish`, in the same redb transaction, for every writer: lane batches and direct publishes alike.
- `publish` gains `origin: Origin{actor, session, method, request_id}`.
- Rows are derived from the commit's fact mutations:

  | Mutation | Event |
  |---|---|
  | `Refs` | `RefUpdated{name, old, new}` |
  | `Refs` under `refs/ruk/r/` | `RecordChanged{key, rev}` |
  | `Changes` | `ChangeUpdated` |
  | `Attestations` order-index insert | `AttestationAdded{subject, kind, signer, on_behalf_of}` |
  | `LandPlans` delete plus a `Refs` move | `LandExecuted` |

- Key: `generation u64 BE ‖ seq u32`, one row per ref changed. A 70k-tag push fits.
- Retention by age and size. A read older than the oldest retained row fails with `E_GAP`.
- This one table feeds both the host catalog and rukie.

Reserved namespaces:
- `refs/ruk/**` and `refs/notes/ruk-attest` are written only by `record.*`, `attest.*` and `land.*`.
- These refuse them with `E_RESERVED`: receive-pack, `update-ref`, `bookmark.set`, `notes`.
- The refusal is unconditional, because rukie is always the peer.

Undo:
- Restores record refs, because Refs are tracked.
- Re-derives `Records` rows in the same batch.
- Events follow from the restored refs.

Authority:
- Settings never grant a capability.
- `policy.toml` (unversioned, operator-owned, policy.rs:23-49) stays the only capability authority.
- Protections can only add land constraints.

Schemas, v1, validated inside Ruk. `ActorRef` is `"rukie:<instance-uuid>:u:<user-ulid>"`.

```
Task     {number, title≤256, state: Open|Closed{Done|WontDo|Duplicate{of}}, labels, assignees: Set<ActorRef>,
          links: Vec<Change|Commit|Task>, created_by, created_at, updated_at, comment_count}
Review   {change, target, state: Open|Landed{commit}|Abandoned, reviewers,
          threads: Vec<{id, anchor: Line{commit, path, side, lines}|Change, resolved, comments: Vec<u32>}>}
Release  {tag, title, draft, prerelease, assets: Vec<{name, sha256, size, media_type}>}
Settings {default_branch, description, protections: Vec<Protection>}
Protection {pattern, land_only: bool, plan_ttl (≤24h, default 1h), require: Vec<LandRequirement>}
LandRequirement {kind, result, signers: Vec<SignerFingerprint>, min_distinct_actors: u8,
                 applies_to: Tip|EachChange, scope: Exact|SourceHeadIfClean}
Mirror   {url, mode: ExactForcePrune|FastForwardOnly, refs: All|DefaultAndTags|Globs, carry_records, credential: SecretRef, reconcile_interval}
Hook     {url, events, active, secret: SecretRef}
```

### 3.2 Methods and breaking changes

New methods, appended to the method table (protocol.rs:3123-3130):

| Method | Kind | Contract |
|---|---|---|
| `record.create` | Mutation | {kind, body, id?} → {key, rev, number?} |
| `record.update` | Mutation | {key, expected_rev, ops, fence?} → {rev}; errors `E_STALE`, `E_FENCED` |
| `record.get` | Read | |
| `record.log` | Read | |
| `record.list` | Read | {kind, filter, order, page} |
| `record.lease.acquire` | Mutation | {key, ttl, holder, token_id} → {epoch, expires_at}; error `E_LEASED` |
| `record.lease.renew` | Mutation | |
| `record.lease.release` | Mutation | |
| `record.export` | Mutation | incremental `refs/ruk/export/<kind>`; tree sharded as `<id mod 256>/<id>`; touches only records changed since its cursor |
| `record.reindex` | Mutation | |
| `events.read` | Read | {after, limit} → {events, next, oldest_retained}; error `E_GAP` |
| `attest.import` | Mutation | verifies every note via `parse_export` → `prepare_batch`; moves the notes ref only if every note verifies |
| `attest.reindex` | Mutation | |
| `remote.ls` | Read | |
| `search` | Read | {at, pattern, paths, case, budget}, streamed |

Daemon-level host methods:
- `host.repo.create`;
- `host.repo.fork`, sharing objects `[inference]`;
- `host.repo.delete`;
- `host.catalog.list`, `host.catalog.rebuild`;
- `host.events.follow`.

Breaking changes to existing methods:

1. **`attest.add` writes the notes ref.**
   - In one batch: the attestation rows plus a commit on `refs/notes/ruk-attest`.
   - Note format: one per subject; a JSON `AttestAddArgs` array sorted by digest.
2. **Attestation v2.**
   - Adds `on_behalf_of: Option<ActorRef>` and `detail: Option<String>`.
   - Group key becomes (subject, kind, signer, on_behalf_of).
   - Unchanged: one exact commit subject, SSHSIG namespace `ruk-attest`, signer derived from the key.
3. **Hosted land with `RemoteSpec::Local`.** All of this is new Ruk work; today's hosted prepare does none of it (§1):
   1. *Stack resolution.* The change plus its unlanded ancestor changes, ordered bottom to top.
   2. *Rebase with conflict check.* Each change is rebased in order onto the target tip.
      - Records `clean` per entry. This flag is the signal `SourceHeadIfClean` needs.
      - A conflict fails `E_CONFLICT{change}`.
   3. *Explicit tip.* The plan stores `stack: Vec<{change, source_head, rebased, clean}>`, `tip` and `base`. `candidate()` returns `tip`; first-of-sorted-set is removed.
   4. *Enforced requirements.* Requirements and signers are loaded from the matching `Protection`, replacing the empty vectors. `policy_generation` becomes the settings record rev. Caller-supplied policy is refused.
   5. *Per-candidate evaluation.* Evaluation follows `applies_to` and `scope`. The reply gains `gaps: Vec<Gap{commit, requirement}>`, reusing `attestation::Gap`.
   6. *Candidate pin.* `refs/ruk/land/<plan>` → tip, in the same batch.
   7. *Execute checks.* Execute requires:
      - tip equals base;
      - tip descends from base;
      - every `rebased` commit is in `base..tip`;
      - requirements re-evaluated live.

      Then, in one batch: move the target, consume the plan, delete the pin, set `Review.Landed`.
   8. *One live plan per target.* `LandPlans` gets a `0x02 target → plan` key. A second prepare fails `E_BUSY{plan}`. An expiry sweep deletes expired plans and their pins.
   9. *TTL from protection.* `Protection.plan_ttl` replaces `LAND_PLAN_TTL`.
4. **Land-only refs.** Receive-pack refuses a direct push to a `land_only` ref with `E_LAND_ONLY`.
5. **Actor on opens.** `InvocationOpen.actor` and `CommandOpen.actor`.
   - Honoured only when the caller holds a new `assert_actor` capability.
   - Recorded in `OperationEntry` and in the Events origin.
6. **Native `refs/for/<branch>`.** Uses the Change-Id trailer; no proc-receive callback.
7. **Hidden prefixes.** `refs/ruk/` and `refs/notes/ruk-attest` are left out of the v0 advertisement (serve.rs:106) and of v2 `ls-refs`.
8. **Ephemeral push credentials.** `PushArgs.auth: Option<EphemeralAuth>`: excluded from the request digest and never persisted.

### 3.3 Host catalog: `crates/ruk/src/daemon/catalog.rs`

- Storage: its own redb file at `<root>/.ruk-host/catalog.redb`.
- Key families:
  - `C1 repo‖kind‖state‖updated_desc‖id`;
  - `C2 kind‖state‖assignee‖updated_desc‖repo‖id`;
  - `C3 kind‖state‖updated_desc‖repo‖id`;
  - `K repo‖kind‖state` → count;
  - `P repo` → generation (cursor).
- Feed: an applier tails each repo's `Events`.
- Derived: rebuilt by `host.catalog.rebuild`.
- Shutdown: drained in `shutdown_and_drain` (registry.rs:12-14).

## 4. Query and listing performance

**Workload: open tasks across 500 visible repos.**

1. rukie computes the visible repo set from meta and caches it for 30 s.
2. rukie calls `host.catalog.list{repos, kind: task, state: open, assignee?, order: updated_desc, limit: 50}`.
3. The catalog picks a strategy:
   - ≤2000 repos: k-way merge of the C1 prefixes;
   - assignee given: C2 scan, filtered by a hash set of repos;
   - otherwise: C3 scan with a filter.
4. Badge counts come from K in O(1).

Estimate `[inference]`: about 5–10 ms in the catalog, about 2 ms IPC, about 3 ms render.
Target: p95 ≤ 60 ms server time. Gate: benchmark 500 repos × 2000 tasks.
Freshness target: p99 < 1 s. Replies list `stale_repos`.

Rejected alternatives:
- Per-repo fan-out: estimated 0.5–3 s `[inference]`.
- Task copies kept in meta.

**Per-repo `record.list`:** p95 ≤ 15 ms.

**Code search:**
- `search` walks the tree and skips binary blobs and blobs over 1 MiB.
- It uses a lazily built 256-byte trigram bloom per blob oid.
- Cross-repo search fans out with concurrency 8 and stops at 100 hits or 2 s.

## 5. Consistency between Ruk and meta

Rule: Ruk is authoritative for repo-scoped data; meta for instance-scoped data. Anything derived across the boundary carries a cursor and can be rebuilt. There are no distributed transactions. Writes take one of two paths:
- **Ruk-first, then Events:** the jobs and the cursor commit in one meta transaction.
- **Meta saga:** idempotent Ruk calls via `request_id`.

| # | Invariant | Enforced by | Repair |
|---|---|---|---|
| I1 | An active repo row's `store_id` equals the Ruk store's | check on first open | reaper (`broken` state, or quarantine after 24 h) |
| I2 | Repo create goes `provisioning` → `host.repo.create` → `active` | saga | retry or roll back after 10 min |
| I3 | Every ActorRef resolves, or renders as ghost | read time | none |
| I4 | Jobs and cursor share one meta transaction; job key unique on (repo, gen, seq, consumer) | `feed` | re-read from cursor |
| I5 | `E_GAP` triggers reconcile | `feed` | mirror re-sync; CI rescans open stacks and protected tips; `repo.resync` hook; inbox notice |
| I6 | Every `SecretRef` exists | checked at use | `credential_missing` status |
| I7 | Lease holder's token is valid | token revocation enqueues lease release | lease expiry |
| I8 | Catalog matches Events | applier cursor | `host.catalog.rebuild` |
| I9 | `Records` matches record refs | same `publish` transaction | `record.reindex` |
| I10 | `Attestations` matches the notes ref | same batch; `attest.import` verifies | `attest.reindex` |
| I11 | Ruk trusts the actor only from rukie | `assert_actor` capability | none |
| I12 | `request_id = SHA-256(actor‖op‖Idempotency-Key)` | `rukc` | receipts replay; no cross-user collision |
| I13 | Rename and transfer touch meta only; store directories are named by ULID | `repo` | none |
| I14 | Each land pin exists only while its plan lives | same batch as the plan; expiry sweep | sweep |

## 6. Go package layout (20 packages; imports point down only)

| Package | Responsibility |
|---|---|
| `cmd/rukie` | serve, admin, ssh-keys, git-shell, client mode `rukie <op> --json` |
| `internal/op` | operation table; emits OpenAPI 3.1 and MCP schemas |
| `internal/httpapi` | JSON API, RFC 9457 errors, Idempotency-Key |
| `internal/web` | html/template, forms, zero JS |
| `internal/mcpserver` | MCP over the op table |
| `internal/auth` | argon2id, TOTP, OIDC, sessions, scoped tokens |
| `internal/meta` | the only metadata-store package |
| `internal/rukc` | Ruk daemon client (see note below) |
| `internal/record` | Go types for the record kinds, ActorRef |
| `internal/repo` | lifecycle, browse, forks, stars, watches, explore, search fan-out |
| `internal/task` | tasks and their leases |
| `internal/review` | changes, stacks, branch view, threads, votes, land queue |
| `internal/gitwire` | smart HTTP; SSH git-shell over `/run/rukie/internal.sock` |
| `internal/feed` | event follower |
| `internal/jobs` | leased job runner |
| `internal/webhook` | Standard Webhooks |
| `internal/mirror` | push mirrors |
| `internal/ci` | scheduler, runner API (`source.tar` streamed from Ruk upload-archive at the job oid), result reuse |
| `internal/sign` | SSHSIG signing client; talks to the optional `rukie-signer` process |
| `internal/platform` | config, slog, clock, lifecycle Group |

`internal/rukc`:
- Port from forgejo/modules/git/ruk_ipc.go: frames, credits, outcome lookup.
- New work:
  - typed `Invoke` (`method`, `args_json`, `session_id`, `actor`);
  - per-repo session open and resume;
  - `actor` on command opens;
  - Go types generated from `ruk schema`.

Every goroutine registers with `platform.Group`.

## 7. Request flows

### (a) Render a file at a revision

Target: p95 ≤ 40 ms warm, ≤ 70 ms with a cold store.

1. `GET /{o}/{r}/-/blob/{rev}/{path...}` reaches the `web` op `file.show`.
2. `auth` resolves the user. `repo.Resolve` reads the meta repo row (LRU, 5 s) and checks permission.
3. One call: `rukc.Invoke(handle, "file", {at: rev, path})`. Each `FileBody` variant has its own rendering:

   | Variant | Rendering |
   |---|---|
   | `Blob`, inline content | text with `#L<n>` anchors; binary (NUL in first 8 KiB) becomes a download link |
   | `Blob`, streamed content or size > 256 KiB | size notice plus a link to `/-/raw` |
   | `Symlink` | target, linked at the same revision |
   | `Gitlink` | submodule commit oid |
   | `Directory` | tree page built from `entries` in the same response |
   | `Conflict` | each merge term, under a "conflicted" banner |

4. Caching headers:
   - `ETag: "<Blob.oid>"`;
   - a full-oid revision gets `Cache-Control: immutable` (public repos) or `private`;
   - a symbolic revision gets `no-cache`.
5. Failures:
   - `E_NOT_FOUND` → 404;
   - daemon down → 503 with Retry-After;
   - quarantined store → 503;
   - `E_LIMIT` → 429.

### (b) Land a change with required attestations

Target: p95 ≤ 100 ms, excluding CI time.

Example protection for `main`: `land_only`, plus two requirements:
- `review=approve`: EachChange, SourceHeadIfClean, signer `[review-key]`, `min_distinct_actors=1`;
- `ci/test=pass`: Tip, Exact, signer `[ci-key]`.

1. `change.land{repo, change, target}`. Authorization: write permission plus scope `change:land`.
2. `review` enqueues `land.drive`. The queue is FIFO per (repo, target), with one active entry.
3. The active job calls `land.prepare{change, remote: Local, target}`. Ruk:
   - resolves the stack;
   - rebases it, recording `clean` per entry;
   - freezes the plan;
   - writes the pin `refs/ruk/land/<plan>`;
   - returns `gaps`.

   If another plan is live, the result is `E_BUSY` and the job waits.
4. Filling the gaps:
   - **Tip CI gap:** enqueue `ci.run{commit: tip, job}`. The runner downloads `source.tar` at `tip`; the pin keeps it reachable.
     - Result reuse: if `ci_cache(tree, job digest)` already holds a pass, the CI key re-signs for the tip instead.
   - **Review gap per change:** `review.vote` sends `attest.add` on `source_head`, with `on_behalf_of`.
5. When CI passes:
   - `ci` checks the job epoch;
   - `sign` produces a statement on `tip` with `detail` = log URL;
   - `attest.add` publishes the notes commit, the rows and an `AttestationAdded` event.
6. `feed` wakes `land.drive`, which calls `land.execute{plan, tip}`. Ruk:
   - checks tip == base, ancestry, and the live requirements;
   - in one batch: moves main, consumes the plan, deletes the pin, sets `Review.Landed`;
   - emits `RefUpdated` and `LandExecuted`.
7. `feed` fans out to mirrors, hooks, post-merge CI and the inbox. The queue advances.

Why the queue cannot starve:
- `land_only` means only `land.execute` moves `main`.
- Plans are serialized per target.
- Expired plans re-prepare.

Failures:
- CI failure: drop the plan and its pin.
- Untrusted signer: 422.
- `E_CONFLICT`: the change is marked "needs rebase".
- Crash: receipt replay. A second executor fails on the plan-row expectation (land.rs:43-45).

### (c) Smart-HTTP push → mirror, webhook, CI

Target: p95 ≤ 30 ms overhead on top of the pack transfer.

1. `GET info/refs?service=git-receive-pack`:
   - Basic auth with a token;
   - `repo:write` scope;
   - `rukc.Command(handle, ["receive-pack", "--stateless-rpc", "--advertise-refs", "."], actor)`;
   - hidden prefixes are left out of the advertisement.
2. `POST git-receive-pack`:
   - the request body is streamed as Stdin frames under the credit window;
   - on client disconnect, send Cancel, then use outcome lookup.
3. Ruk processes the push:
   - quarantine the objects;
   - refuse reserved and `land_only` refs;
   - `refs/for/<b>` creates or updates a change;
   - CAS the refs;
   - `Store::publish` emits Events with `origin.actor`.

   rukie reports success only after this is durable.
4. `host.events.follow` → `events.read{after}`. One meta transaction enqueues:
   - `webhook.deliver`, keyed by event id (also the `webhook-id`);
   - `mirror.push`, coalesced per mirror;
   - `ci.plan`;
   - `inbox.fanout`.

   Then it advances the cursor.
5. Mirror job:
   - mode `ExactForcePrune`: `remote.ls`, then force updates and deletes;
   - mode `FastForwardOnly`: non-force updates only;
   - if `carry_records`: `record.export`, then push the export refs and the notes ref;
   - call `push{Url, updates, NonAtomic, auth}`;
   - `Uncertain` → `push.reconcile`;
   - non-fast-forward rejection → status `diverged`;
   - a periodic ticker runs reconcile.
6. Webhook job:
   - headers `webhook-id`, `webhook-timestamp`, `webhook-signature: v1,<base64 HMAC-SHA256>` (https://www.standardwebhooks.com/);
   - retry with backoff for 24 h, then dead-letter.
7. CI job:
   - read `.rukie/ci.toml` at the oid;
   - runners long-poll for leased jobs;
   - a lost runner leads to requeue;
   - a stale epoch is refused before signing.

## 8. Failure paths

| Failure | Behaviour |
|---|---|
| Ruk daemon down | repo pages, API and git return 503 with Retry-After; identity pages still work; the feed pauses |
| Daemon restart mid-call | outcome lookup or receipt replay |
| `E_LIMIT` | 429/503; jobs back off |
| Store quarantined | repo becomes read-only; admin inbox notice |
| `E_STALE` on a record | form re-renders, or 409 with `rev` |
| `E_FENCED`, `E_LEASED` | 409 naming the holder |
| `E_GAP` | reconcile (I5) |
| Direct publish outside the lane | still emits Events (via `Store::publish`) |
| Forged note on `attest.import` | notes ref move is refused |
| Meta down | 503 everywhere, including git (no name→store map, no authz) |
| Mirror auth, network or divergence error | status recorded, backoff, notice |
| Webhook endpoint down | backoff, then dead-letter |
| CI runner lost | requeue; fencing blocks stale signatures |
| Land `E_CONFLICT`, `E_BUSY` or expiry | needs-rebase / wait / re-prepare; pin swept |
| Ruk `E_IO` (e.g. disk full) | 503; publish is atomic |

## 9. Red-flag self-check

1. **Forged approvals.** One forge review key signs every vote, and rukie asserts `on_behalf_of`. `min_distinct_actors` counts rukie's claims, not independent signatures. A compromised rukie or a leaked key can forge any approval or CI result.

   Mitigations:
   - keys live in a separate `rukie-signer` process (Unix socket, per-actor rate limit, audit Event per signature, key never in the rukie process);
   - review and CI keys are separate;
   - protections may require user-held keys (agents sign client-side; `review.vote` accepts pre-signed statements);
   - optionally, runner-held CI keys with the job epoch in `detail`.
2. **Coupling.** Schemas live in Rust, so every field change needs a Ruk release. Accepted.
3. **Second index.** The host catalog is derived. Gerrit made the same choice.
4. **Erasure / GDPR.** Record history is immutable, and mirrors keep copies. `record.redact` is required before tasks reach GA.
5. **Meta deviations:**
   - D1 repos registry: a namespace can't live inside the repo it names;
   - D2 stars and watches: user preferences;
   - D3 secrets: credentials, which must not replicate;
   - D4 CI run metadata and logs: operational data;
   - D5 inbox: a derived queue;
   - D6 org-level hooks: only if needed.
6. **Land throughput.** Serial per-target landing allows one CI cycle per landing. Next step: batch queued stacks into one plan, with bisect on failure.
7. **Hidden refs** (§3.2 item 7) are a hard dependency.
8. **Ref count.** About 100k task refs is OK in redb `[inference]`. Exports are sharded and incremental. Whether GitHub and GitLab accept `refs/ruk/export/*` is unverified `[inference]`.
9. **Divergent replicas.** Excluded in v1. Imports must fast-forward or target an empty store (`E_DIVERGED`).
10. **Ruk on every page.** Pages make one Ruk call each; lists use `record.list` or the catalog.
11. **Hosted land is largely unbuilt.** It needs stack, rebase, tip, requirements and pin (§3.2 item 3). Flow (b) depends on all of it.

## 10. Commit sequence


[54 more lines in resource. Use :1810 to continue]

1. **Ruk, commit 1:**
   - `collab` module;
   - `Records`, `RecordLeases`, `Events` (emitted in `Store::publish`);
   - `record.*`, `events.read`;
   - reserved and hidden namespaces.
2. **Ruk, commit 2:**
   - attestation v2 and the notes bridge;
   - hosted land (stack, rebase with clean flag, tip, ancestry, enforced requirements, gaps, pin, single plan per target, `plan_ttl`);
   - `land_only`;
   - actor on opens;
   - native `refs/for`.
3. **Ruk, commit 3:**
   - host catalog;
   - `search` / `BlobSketch`;
   - `remote.ls`;
   - push auth;
   - `record.export`.
4. **rukie**, in order:
   - `platform` → `meta` → `rukc` (typed calls) → `op`;
   - auth → repo/gitwire;
   - record/task/review → feed/jobs → webhook/mirror/ci;
   - transports.

   Gitea surfaces are deleted in the same series. Every commit builds.

## Critical Files

- `crates/ruk-core/src/coord/prepare.rs:3431-3484` and `coord/land.rs` — hosted prepare (no rebase, empty requirements) and core plan semantics to rework
- `crates/ruk-core/src/store/mod.rs:911`, `store/types.rs` — `Store::publish` emits Events; new fact tables
- `crates/ruk-core/src/attestation.rs` (+ `layout.rs`, `export.rs`) — group key, notes bridge
- `crates/ruk/src/daemon/mod.rs`, `crates/ruk-git/src/transport/serve.rs` — actor on opens, hidden refs, reserved-namespace refusal
- `forgejo/modules/git/ruk_ipc.go` — Go client to port and extend with typed calls
~~~~

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/prepare.rs` (lines 3431-3484) and `ruk/crates/ruk-core/src/coord/land.rs`: hosted prepare today freezes one head, doesn't rebase and has empty requirements; core landing orders candidates by object id and has no ancestry check. Both need rework.
- `ruk/crates/ruk-core/src/store/mod.rs` (line 911) and `ruk/crates/ruk-core/src/store/types.rs`: change events are emitted inside `Store::publish`, and the new tables go here.
- `ruk/crates/ruk-core/src/attestation.rs` (with `attestation/layout.rs` and `attestation/export.rs`): the attestation group key gains `on_behalf_of`, plus the bridge to and from the `refs/notes/ruk-attest` ref.
- `ruk/crates/ruk/src/daemon/mod.rs` and `ruk/crates/ruk-git/src/transport/serve.rs`: `actor` on both open requests, hidden-ref advertisement, and always refusing pushes to reserved namespaces.
- `ruk/forgejo/modules/git/ruk_ipc.go`: the existing Go client, which only runs git commands; port it into `internal/rukc` and add typed calls.

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Attach `Origin` to each reque…

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Add `land.abandon{plan}` (Mutation…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Add `land.cancel{plan}`. It delete…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Exclude `refs/notes/ruk-attest` an…

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Define `clean` as patch-id equal…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Add `land.cancel`. Without it, a …

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Shard the `refs/notes/ruk-attest`…