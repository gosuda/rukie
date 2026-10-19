# Forks, Stars, Watches, Explore, and Releases on Ruk

## 0. Status, scope, non-goals

This document specifies the fork mechanism (and the Ruk changes it needs),
the fork-network model, stars, watches, explore, and releases for the
Ruk-backed rewrite of `rukie`. It is written against the Ruk tree as observed
(paths cited per claim). Derivations rather than quotes are marked
`[INFERENCE]`.

Settled decisions taken as input (not re-argued here): Gitea surfaces are
cut; only the `/{owner}/{repo}.git` clone URL shape survives; metadata lives
behind one new store module (SQLite / Pebble / PostgreSQL still open);
interfaces derive from one Go `Op[In,Out]` table; UI is zero-JS
server-rendered; identity is argon2id+TOTP or OIDC with scoped tokens on
human accounts; git wire is Smart HTTP + SSH served through the Ruk daemon;
CI results are Ruk attestations; mirrors, tasks, webhooks, inbox, and admin
CLI are specified elsewhere.

Non-goals of this document: the operation-table framework itself (Phase K2),
auth/session internals, org/team permission evaluation, CI scheduler/runner,
webhook delivery, mirror execution, and the full R2 read-method catalogue.
This document names the Ruk methods those features consume but specifies in
full the Ruk changes for forks (§2) and releases (§7): `repo.fork` plus the
released-tag freeze-table schema revision. Server-side code search is an R3
track owned by a separate design; §8.3 records only the dependency, not the
API.

Note on `physical.rs`: it lives at `ruk/crates/ruk-core/src/physical.rs`
(imported as `crate::physical` in `store/mod.rs`), **not** under `store/`,
and covers only pre-publication allocation reservation (module docs lines
1–24; Linux `KEEP_SIZE` / `KEEP_SIZE|UNSHARE_RANGE` split, Windows
observation-only receipt). It does **not** prove hardlink safety and is not
cited for it. Fork safety cites `store/segment.rs`, `store/compact.rs`, and
`store/recovery.rs` below.

Gitea's `services/repository/fork.go:ForkRepository` (load owner, create DB
row, `git.CloneManaged`, update counts) is **not** reused: Phase K1 deletes
the Gitea tree and `git.CloneManaged` (a client-side clone) cannot express a
Ruk store fork. Only the step shape (validate → create → copy → counts)
informs the new `internal/forks` orchestration in §8.2.

---

## 1. What Ruk's store actually looks like (findings with citations)

### 1.1 Physical layout: one directory per repository store

- A store is rooted at a `.ruk/store` directory. `Store::create` documents
  the layout precisely: the root, a `segments/` directory, a `meta.redb`
  file, and a `format` file, all created exclusively — an existing `format`,
  `meta.redb`, or `segments` makes creation fail and nothing is overwritten
  (`ruk/crates/ruk-core/src/store/mod.rs:Store::create` doc comment,
  `FORMAT_FILE` / `META_FILE` / `SEGMENTS_DIR` constants, `require_absent`
  at `store/mod.rs:320-321`).
- The `format` file is written **last** and is the creation proof: "A valid
  format proves that the earlier publications completed"
  (`store/mod.rs`, `Store::create` docs). Its bytes are the canonical
  three-line v1 marker
  `ruk-store 1\nobject-format <kind>\nstore-id <id>\n` with a generated
  non-nil `StoreId` per store
  (`ruk/crates/ruk-core/src/store/types.rs:StoreFormat::to_bytes`,
  `from_bytes`, `parse_store_identity` rejecting the nil identity).
  Consequence: **every store has a distinct immutable identity**; two stores
  are never the "same store" even with identical object bytes. Consequence
  for crash safety: a fork must build in a **staging directory** and rename
  into place only after its `format` is durable (§2.3 steps 2/6). "Retry is
  safe" means exactly this: `dest_path` itself is never half-built, retries
  mint a fresh staging name (e.g. `<dest>.fork-<uuid>`), and stale staging
  directories are never registered and never block a retry — they are
  removed best-effort or garbage-collected. Building directly at `dest_path`
  would leave `segments/` + `meta.redb` behind after a crash and make every
  retry fail `require_absent` until manual cleanup.
- A `Store` holds its root as a stored `PathBuf` (`store/mod.rs:162-163`)
  and every later publication writes through it
  (`store/mod.rs:1209`: `self.root.join(SEGMENTS_DIR)`). A handle opened at
  the staging path therefore **must not** survive the rename: segments
  written after the rename would go to a path that no longer exists. §2.3
  step 6 closes all staging handles before renaming and opens strictly at
  `dest_path` afterwards; registering or writing through a staging-rooted
  handle is forbidden.
- Per-repository processes also live beside the store, not inside `meta.redb`:
  the commit-coordinator sidecar binds `<store>/coordinator.sock`
  (`ruk/crates/ruk-core/src/coord/per_repo.rs:SOCK_NAME`), keeps a
  `cli-session` leaf, and the hosted daemon keeps `leases-scratch` under the
  repository root (`coord/hosted.rs:HostedCoordinator::open`). A fork must
  therefore never copy sockets, sessions, or scratch directories — only
  `format` (fresh), `meta.redb` (fresh, selectively populated), `segments/`
  (shared bytes, see §2), and the LFS sidecar (see §1.4).

### 1.2 Segments: immutable sealed prefixes plus one open tail

- Object bytes live in numbered segment files under `segments/`. The framing
  is a dense run of `len/kind/flags/crc + payload` records with an optional
  single trailing `RUKSEAL1` footer; nothing follows a footer
  (`ruk/crates/ruk-core/src/store/segment.rs` module docs, `SEAL_MAGIC`,
  `FOOTER_LEN`, `SEGMENT_TARGET_BYTES = 256 MiB` placement hint). A manifest
  carries `records_end` (end of the last record) and `committed_end` (equal
  to `records_end` while open, `records_end + 24` once sealed; same file).
- Sealed segments are immutable by construction. The compaction design states
  the invariant explicitly: "Only sealed segments join the retire set: a
  sealed `records_end` is final, so the unlocked copy never reads past the
  snapshot extent even when a segment rolls mid-pass"
  (`ruk/crates/ruk-core/src/store/compact.rs` module docs, lines ~1–20).
  Bulk copying runs without the writer lock "against immutable committed
  prefixes" (same file). The open/active session segment is always excluded
  from the retire set. **The open/active tail segment is therefore never
  hardlinked into a fork**: a parent append after the fork would otherwise
  mutate bytes visible to the child. Tail-resident objects are copied by
  value (§2.3). Sealed-source copying plus retired-file unlinking is the
  established pattern (`store/compact.rs:121-124`: "a single redb transaction
  replaces every locator, retires every source manifest, and advances the
  generation"); fork's import primitive follows the same atomic-install shape
  (§2.3 step 4a), minus any claim about persisting the next segment id.
- Recovery corroborates: on every `Store::open` each non-retired manifest's
  segment file must exist and hold at least its committed extent; only an
  open segment's uncommitted tail is ever truncated
  (`ruk/crates/ruk-core/src/store/recovery.rs` module docs). Unknown files
  in `segments/` are "never read, repaired, or deleted" (same file) — a fork
  that links only the **manifest-named sealed** segments is safe against
  crash orphans. The continuation segment id is writer memory recomputed as
  max-manifest-id-plus-one on open (`store/mod.rs:191-192`,
  `store/recovery.rs:326-329`): the import installs the linked manifests
  themselves and the id follows on first open. No id-handoff transaction
  exists or is claimed.
- Segment ids are monotonically allocated and never reused; a cached
  descriptor can never alias a newer segment (`SegmentFileCache` docs in
  `recovery.rs`). A racing creator that finds `AlreadyExists` advances past
  the name (`store/mod.rs:ActiveSegment` creation path, ~1600).
- Locator/generation invariant: a locator whose `generation` exceeds the
  store `generation` is corrupt (`store/metadata.rs:432-435`: "object …
  was published at generation {} above the store generation {}"). An import
  that installs source locators verbatim while leaving the destination
  generation at zero (or any value below the source generation `G`) makes
  every imported read fail recovery. §2.3 step 4a therefore sets the
  destination generation to `G` explicitly, **including** the in-memory
  generation registry/guard epoch (see below).
- Reader-epoch invariant: `Store::create` seeds the in-memory
  `GenerationRegistry` at 0 (`store/mod.rs:334`) while `Store::open` seeds it
  from `meta.generation()` (`store/mod.rs:423`); publication and compaction
  advance it on every commit (`store/mod.rs:1286`, `store/compact.rs:198,
  623`). An import that writes meta generation `G` without advancing the
  registry leaves reader pins on the create-time instance deriving epoch 1
  while meta reads `G`. The import primitive must therefore call the same
  advance path (e.g. `guards.advance_published_generation(G)`) as part of
  step 4a, under the destination writer lock, before any reader can pin.
- Prune advances the generation: `prune_unreachable` commits a new
  generation (`store/compact.rs:188-199`: `set_generation(new_generation)`).
  Locator-only prunes are therefore caught by the §2.3 generation check, not
  by link-time verification (see §2.3 step 1b).

### 1.3 Metadata: `meta.redb`, 27 fact tables, publication-owned object state

- All mutable state besides object bytes lives in `meta.redb`: publication-
  owned `objects` (Oid → `ObjectLocation`, postcard-encoded), `segments`
  (id → `SegmentManifest`), `generation` / `verified_generation`, `pending`
  capacity-ledger rows, plus one `&[u8] → &[u8]` table per `FactTable`
  variant named by `FactTable::table_name`
  (`ruk/crates/ruk-core/src/store/metadata.rs` module docs and constants
  `OBJECTS`, `SEGMENTS`, `GENERATION`, `VERIFIED_GENERATION`,
  `fact_definition`). `PENDING` rows are "written only through the
  pending-commit path and `PublicationTxn::replace_pending`, never through a
  `FactMutation`" (`store/metadata.rs:199-203`). Manifest rows are per store
  (id → manifest carrying sealed flag and extents); a fork installs manifest
  rows plus object rows into a **fresh** `meta.redb` and hardlinks the sealed
  segments they name.
- The closed table census is today 27 entries
  (`store/types.rs:FactTable::ALL`):
  `refs, ref_conflicts, remote_observations, changes, imported_changes,
  change_history, commits, ops, op_head, workspaces, claims, ws_index,
  ws_status, ws_dirs, receipts, sessions, attestations, push_plans,
  land_plans, published_heads, leases, ws_apply, reflogs, promisor, shallow,
  pins, lfs_objects`, and the enum docs state the census rule explicitly:
  "a new table arrives only with a schema revision" and "Publication owns
  objects/segments/generation; they are deliberately not expressible here"
  (`store/types.rs:FactTable` docs). Consequence for releases: the
  released-tag freeze marker specified in §7.1(5)–(5a) **cannot** live in an
  ad hoc table. It is specified below as a new `FactTable::ReleasedTags`
  census member introduced in the next schema revision (key/value encodings,
  owner field, migration, and every caller named in §7.1(5)–(6) and §8.3).
  There is no "Ruk-side marker without a Ruk schema change" option in this
  design.
- Hard constraints on the fork copy path (all must hold simultaneously):
  - `Store::publish` accepts **only** `Vec<PreparedObject>` plus
    `&[FactMutation]` over `FactTable`
    (`store/mod.rs:911-930`: `pub fn publish(&self, objects:
    Vec<PreparedObject>, facts: &[FactMutation])`). It appends new records;
    there is no adopt/import/install path for manifests or locators in
    `store/` today.
  - `PreparedObject` fields are private: "nothing outside
    preparation/publication can fabricate a prepared object"
    (`store/types.rs:PreparedObject` docs). Callers cannot synthesize a
    locator-pointing record; they can only submit content bytes through
    preparation and let publication mint the locator.
  - A delta base "must already be committed in this store, with the same
    kind and one level shallower than the delta. A prepared object moved
    from another store, or one naming a same-batch base, fails here"
    (`store/mod.rs:check_delta_base`, ~1086–1088). Copying deltas in the
    same batch as their bases fails; bases must commit first.
  - Duplicates of committed objects "are accepted when kind and length
    agree, keep their original immutable bytes" (`store/mod.rs:publish`
    docs). Re-submitting an Oid that already has a locator therefore keeps
    the existing locator — including a dangling one. The sealed/tail
    partition in §2.3 exists to keep this from ever firing across the
    import boundary: step 4b submits **only** `O_tail` Oids, disjoint from
    the imported `O_sealed` Oids, and the primitive asserts disjointness
    before installing. There is no "duplicate insertion rejection" to rely
    on; overlap would silently preserve the wrong locator.
  - Facts-only publications do **not** advance the generation ("the
    generation advances only when new locators publish" —
    `store/mod.rs:publish` docs, ~878–880). `bookmark.set`, `tag.set`, and
    every other facts-only mutation can therefore move refs, changes, and
    review state at a constant `G`. A fork-consistency check on generation
    alone misses exactly these races and can copy a mixed snapshot (new refs
    with old objects or vice versa). §2.3 steps 1–1b therefore pair the
    generation check with a fact-version (CAS) revalidation covering every
    copied fact row — not generation alone. Conversely, object additions and
    locator-only prunes **do** advance the generation (new locators publish;
    `prune_unreachable` commits a new generation per
    `store/compact.rs:188-199`), so the generation check is the primary path
    for object churn while link-time extent re-verification is
    defense-in-depth.
  - Consequence: there is **no** facts-only `publish` that installs
    hardlinked-segment manifests or locators. A fork that shares sealed
    bytes requires a new internal store primitive that installs them under
    the writer lock with verification — specified in §2.3 step 4a.
- Paged reads have no snapshot: `Store::fact_page(table, after, budget)`
  (`store/mod.rs:847`) is a per-call bounded page, and the generation read
  (`store/mod.rs:generation`, ~1037) is a separate call. Copying refs and
  objects across many pages while the source accepts publications mixes
  generations unless the copier pins and validates. §2.3 therefore names a
  snapshot mechanism (GenerationGuard + generation check **plus** fact-row
  CAS revalidation), not bare `M(G), O(G), F(G)` names.
- Reads and copies otherwise go through the narrow surface:
  `Store::read(oid)`, `Store::object_len(oid)`,
  `Store::iter_objects() -> Vec<(Oid, ObjectKind, u64)>` (object census),
  `Store::read_chunks(oid, on_chunk)` (bounded streaming reads;
  `store/stream.rs`), `Store::fact(key)` / `Store::fact_page(table, after,
  budget)` (lexicographic cursor pages over raw key bytes;
  `store/mod.rs`, `store/metadata.rs:fact_page`).
- Keys that matter for fork copying:
  - `refs`: raw ref-name bytes → encoded target
    (`coord/prepare.rs` tests use `b"refs/heads/main"` keys;
    `refs.rs` pages `FactTable::Refs` with `fact_page`).
  - `changes`: 16-byte raw `ChangeId` (`records/keys.rs:change_key`);
    `change_history`: `(ChangeId, Head)` contiguous keys so "all membership
    entries for a given change sort together" (same file);
    `commits`: `Head`-tagged keys (`head_key`); `imported_changes`: raw `Oid`.
  - `attestations`: `(subject, kind, signer, digest)` statement keys plus an
    order index row per statement (attestation `prepare_add` stages row +
    order mutations; `attestation/work.rs` refills by paging
    `FactTable::Attestations`).
  - `claims`: `CLOCK_KEY` plus `R`-prefixed index rows and record rows
    (`coord/claims.rs` paging and fence logic).
  - `ops` / `op_head`, `receipts`, `sessions`, `reflogs`, `pins`,
    `leases`, `ws_*`, `push_plans`, `land_plans`, `published_heads`,
    `ref_conflicts`, `promisor`, `shallow`, `remote_observations` are all
    per-store coordination or observation state, not repository content.

### 1.4 LFS sidecar: content-addressed files plus presence rows

- LFS payloads are **not** first-class blobs (an LFS oid is always sha256
  while the store keys by its own `object_format`, "so a payload cannot be a
  first-class blob without an identity lie"). They live at
  `<store>/lfs/objects/<aa>/<bb>/<sha256-hex>` mirroring `.git/lfs/objects/`,
  with presence tracked in `FactTable::LfsObjects` as postcard-encoded
  `LfsObjectRecord{size, complete}` rows keyed by the 64 hex digits
  (`ruk/crates/ruk-core/src/store/lfs.rs` module docs, `sidecar_path`,
  `record_key`, `encode_record`). Writes are atomic per object
  (temp + rename + one fact-row publication). `complete=false` rows mark

[1155 more lines in resource. Use :18694 to continue]
  private keeps stars (unlike Gitea's `ClearRepoStars` precedent) — the
…
  (temp + rename + one fact-row publication). `complete=false` rows mark
  known-but-absent payloads and "are never fatal to the push".
- Consequence: payload files are content-addressed and immutable once
  verified — safe to hardlink into a fork — while presence rows are cheap
  metadata. Unlike object locators, `lfs_objects` **is** a `FactTable`, so
  its rows *can* travel through the normal `publish` facts path.

### 1.5 Protocol surface relevant to this design

- Ref verbs: `bookmark.set` (full `refs/heads/` name, conditional `expected`,
  unconditional `Any` refused), `bookmark.delete` (`Present` required),
  `bookmark.resolve` (conflict terms as CAS), `tag.set` (full `refs/tags/`
  name; annotated tags are real Git tag objects with `object/type/tag/tagger`
  layout mirroring `git mktag`; `expected: RefExpect` makes a conditional
  move representable as `Present(old)`), `tag.delete`
  (`ruk/crates/ruk-core/src/protocol.rs:BookmarkSetArgs`,
  `BookmarkDeleteArgs`, `BookmarkResolveArgs`, `TagSetArgs`, `TagDeleteArgs`;
  `coord/prepare.rs` tag-rendering docs).
- Review verbs: `new`, `describe`, `edit`, `split`, `squash`, `rebase`,
  `abandon`, `resolve`, `revert`, change-keyed tables above.
- Coordination verbs: `claim.acquire` (paths claim themselves and everything
  below; `Enforce` fails overlapping writes with `E_CLAIMED`, `Advise`
  warns), `claim.release`, `claim.list`, `attest.add` (signature over
  `ruk-attest` canonical payload; private keys never reach the coordinator),
  `attest.list` (subject/kind/signer filters, `include_superseded`),
  `pin`/`unpin`, `land.prepare`/`land.execute`, `undo`, `op.log`/`op.prune`,
  `push`/`push.reconcile`, `fetch`.
- Read verbs: `status`, `log{revset, page}`, `query{revset, page}`,
  `diff{left, right, paths, page}`, `show`, `file{at, path?, page}`
  (blob/symlink/gitlink/**directory listing**/conflict), `blame`
  (same file, `FileArgs`/`BlameArgs`). Paging everywhere is
  `PageRequest{limit, cursor}` with `effective_limit` and server `Page<T>`
  envelopes (same file, `PageRequest`).
- Revset today covers commit-graph queries (`heads/roots/ancestors/
  descendants`, message-substring scans via `scan_records(... contains
  ...)`, path filtering via `Files(...)` with Bloom prefiltering
  `bloom_negative`, `commit_changed_paths` real content comparison —
  `revset/eval.rs`). It is a **graph/message/path** query engine, not a
  blob-content search engine.
- Git-wire evidence: push clients send per-ref CAS updates and parse exact
  per-ref outcomes (`ruk-git/src/transport/push.rs:PushUpdate{name, old,
  new}`, `PushWireRequest`, `PushState{Prepared,Sending,Confirmed,Rejected,
  Uncertain}`); push mirrors map to full set-plus-delete update lists
  (`transport/mirror.rs:push_mirror_updates`, `prune_for_spec`); `refs/for/*`
  and `refs/pull/*` divert to the `proc-receive` hook rather than normal ref
  updates (`transport/proc_receive.rs:is_proc_receive_ref`,
  `PROC_RECEIVE_PREFIXES` — `refs/tags/*` takes the normal update path, so a
  tag freeze enforced only in a rukie op would miss `git push` updates);
  archive streaming serves tar over sideband with bounded `read_chunks`
  reads (`transport/archive.rs`).
- Daemon shape today: `ruk forgejo serve` owns every opened repo of one
  Forgejo instance; `Registry` canonicalizes configured roots, resolves each
  open to a canonical path beneath them, and lazily opens one
  `HostedCoordinator` per repo with a spawned lane runner
  (`ruk/src/daemon/registry.rs`, `ruk/src/daemon/mod.rs:InvocationOpen{repo,
  method}`, `dispatch_method`, `coord_call_site`). `HostedCoordinator::open`
  binds `store_root/policy.toml` and a `leases-scratch` dir
  (`coord/hosted.rs`). Phase R1 renames this to forge-neutral `ruk serve`;
  the fork/search methods specified here target that daemon, not the
  Forgejo-specific path policy. Ruk stores and the metadata store share no
  transaction (§8.1, §1.5): any protocol that "checks the releases table in
  the same transaction that issues `tag.set`" is unimplementable as stated.
  Cross-store guards must be ordered owner-checked CAS + compensation
  (§7.1(5)), with Ruk-side state checked inside the Ruk transaction.

### 1.6 Legacy (Gitea) shapes being replaced — cut, do not port

- `Repository{OwnerID, LowerName, NumStars/NumWatches/NumForks, IsPrivate,
  IsFork, ForkID, ...}` (`rukie/models/repo/repo.go:Repository`).
- `Star{UID, RepoID UNIQUE(s)}` + `StarRepo` transactional toggle
  (`rukie/models/repo/star.go`).
- `Watch{UserID, RepoID UNIQUE(watch), Mode, IncludePullRequests/
  IncludeIssues/IncludeReleases}` with five modes
  (`WatchModeNone/Normal/Dont/Auto/Custom`) and `WatchType`
  (`pull_requests/issues/releases`) (`rukie/models/repo/watch.go`).
- `Release{RepoID, TagName, LowerTagName, Target, Sha1, Note, IsDraft,
  IsPrerelease, IsTag, Attachments}` + `Attachment{UUID, RepoID}`
  (`rukie/models/repo/release.go`, `attachment.go`).
- `Notification{UserID, RepoID, Status, Source, IssueID, CommitID,
  CommentID}` with `(user_id, status, updated_unix)` index
  (`rukie/models/activities/notification.go`).
- These xorm shapes inform the replacement table designs below but **no**
  xorm tag, `app.ini` key, or Gitea counter name survives. Counters are
  re-specified as transactionally maintained columns in the new store module.

---

## 2. Fork mechanism: decision and specification

### 2.1 Options considered

| # | Option | Sketch | Verdict |
|---|--------|--------|---------|
| A | Full store copy | `Store::create` dest, then copy every object byte (re-prepare + publish, bases before deltas per `check_delta_base`) and every wanted fact row | Correct but O(repository bytes) write I/O and CPU (rehash, reseal 256 MiB segments). Forking a 10 GiB repo writes 10 GiB. Rejected as the default; retained as the `EXDEV`/fallback path per segment (§2.3 step 3). |
| B | **Hardlinked sealed segments + fresh metadata via a new internal import primitive (chosen)** | `Store::create` in a staging directory (renamed into place only after `format` is durable), hardlink each manifest-named **sealed** segment file, install sealed manifests + sealed locators + destination generation (meta **and** guard registry) through a new `import_sealed_snapshot` primitive (not `publish`), copy selected fact rows + tail objects via `publish` with base ordering, hardlink verified LFS payloads; the open tail is never linked | O(metadata rows) work; object bytes shared until either repo seals new segments or compacts. Correct given §1.2 immutability plus the §1.3 `publish` limitation. **Chosen.** |
| C | Shared object pool with per-repo refs | One segment pool + N `meta.redb` files, or one meta with repo-id-prefixed keys | Rejected. Breaks load-bearing per-store invariants: `StoreFormat.id` is the store's immutable identity (`types.rs`); recovery, `verified_generation`, quarantine, the capacity `pending` ledger, the single serialized publication writer (`store/mod.rs:Store` docs), and the per-store coordinator socket/lane (`per_repo.rs`, `hosted.rs`) are all per-`Store`. Cross-repo key prefixing would also leak quarantine and generation across repos and complicate `prune_unreachable`/compaction retirement accounting. No evidence any layer supports multi-tenancy inside one `meta.redb`. |

### 2.2 Why B is the cheapest correct fork (store citations)

1. Sealed segment bytes never change: compaction copies "against immutable
   committed prefixes" and only sealed segments join the retire set with
   final `records_end` (`store/compact.rs` docs). Hardlinking a sealed file
   therefore shares bytes that neither the source nor the fork can mutate in
   place — future writes on either side go to a new active segment or to
   compaction-fresh segments, leaving the shared inode's data intact. On
   reflink/COW filesystems the `physical.rs` platform contract unshares
   **overwrites inside a file** (`KEEP_SIZE|UNSHARE_RANGE`); fork never
   overwrites shared bytes in place, so that path is irrelevant to sharing
   safety — cited here only to distinguish reservation from sharing, not as
   proof of it. `[INFERENCE: no in-place segment overwrite path exists;
   segment writes go through SegmentWriter append + seal + flush +
   directory-entry publish per store/mod.rs publication order.]`
2. Identity and coordination state stay per-repo: the fork gets a fresh
   `Store::create` (fresh `StoreId`, empty `pending`, fresh manifests), a
   destination `generation` set to the forked source generation `G` in both
   meta and the in-memory registry (§2.3 step 4a — required by the
   `metadata.rs:432-435` locator-generation invariant and the
   `GenerationRegistry` seeding at `store/mod.rs:334`/`423` + advance paths
   at `store/mod.rs:1286` / `store/compact.rs:198,623`), and a fresh
   `verified_generation`, so recovery, quarantine, retention clocks,
   sessions, leases, and the publication lane are all independent from day
   one (§1.1, §1.3). The `pending` ledger is never copied
   (publication-owned like `objects`/`segments`,
   `store/metadata.rs:199-203`).
3. Sealed locators are small relative to bytes: each is an Oid → postcard
   `ObjectLocation` row. Installing the sealed subset through the §2.3
   primitive costs O(object count), not O(byte count), and reuses the
   existing `iter_objects` census pattern that mirror/LFS tooling already
   relies on (`store/mod.rs:iter_objects`). The primitive installs them
   atomically, so sealed delta bases and deltas land together and
   `check_delta_base` is satisfied by construction for the sealed subset
   (bases are committed in the same snapshot, not smuggled in as same-batch
   prepared objects). The tail subset is handled separately by content copy
   in base-first commit order (§2.3 steps 3–4b).
4. LFS payloads are content-addressed by sha256 hex with atomic
   temp+rename writes (`store/lfs.rs`); hardlinking verified (`complete=true`)
   payload files is safe for the same immutability reason as segments, and
   their presence rows (a real `FactTable`) travel through normal `publish`.
5. Crash orphans cannot leak into the fork: recovery ignores unknown files
   in `segments/` (`store/recovery.rs`); the fork links only segment ids
   named by the source's committed manifests at the fork generation, never a
   directory listing. Compaction cannot unlink a linked source segment
   mid-fork because the fork pins it first (§2.3 step 1).
6. Generation alone is **not** a snapshot: facts-only publications do not
   advance it (§1.3), so §2.3 pairs it with a fact-row CAS revalidation over
   every copied row. Either check alone is unsound; together they are
   sufficient for a bounded-retry fork. Object churn in both directions is
   covered by the generation check (additions publish new locators;
   `prune_unreachable` commits a new generation per
   `store/compact.rs:188-199`); link-time extent re-verification is
   defense-in-depth, not the prune path.

### 2.3 New Ruk method: `repo.fork` (server-side, atomic from the caller's view)

Fork **MUST** be a server-side daemon method, not a client-side
read-all/write-all loop, because only the daemon host can hardlink segment
files and install manifests/locators without shipping gigabytes to the
caller. It belongs to Phase R2 (forge read/admin methods) alongside
repo create/delete/rename.

**Method:** `repo.fork` (mutation; requires a forge-admin or explicit
fork-permission capability — permission model owned by the auth design, but
the method enforces at minimum "caller may read source refs AND create repos
under the destination root", plus the visibility cap in §3.1).

**Args (logical; exact JSON schema owned by the Ruk protocol crate):**

```text
repo.fork {
  source:      RepoSelector        // canonical path or handle the daemon registry resolves
  dest_path:   String              // final filesystem path under a configured root; parent must exist.
                                   // Built in a staging sibling directory and renamed into place (§2.3 steps 2/6).
  object_format: "sha1" | "sha256" // MUST equal source today; cross-format fork is refused (v1 scope)
  refs:        "all" | "default-and-tags" | "none"   // default "all"
  default_branch: String?          // required when refs != "none"; validated as refs/heads/<name>
  include_lfs: bool                // default true: link verified payloads + copy presence rows
  verify:      "full"              // v1: always full. The first open after an import always pays
                                   // full verification (store/recovery.rs docs), so no "cached"
                                   // option is offered; a cached-verification fork would promise
                                   // a skipped pass the opener cannot skip.
}
```

**Locator partition (normative, used by steps 3–4):** the photographed
locator set `O(G)` is partitioned by the photographed manifest list `M(G)`:

```text
O_sealed(G) = { (oid, loc) in O(G) | loc.segment in M_sealed(G) }
O_tail(G)   = O(G) \ O_sealed(G)   // resident only in the source open/active tail
```

Only `O_sealed(G)` is ever installed verbatim. `O_tail(G)` is never
installed (its segments are not linked); each member is re-created by
content copy in step 4b, which mints a **fresh** destination locator. The two
sets are disjoint by construction and the primitive asserts disjointness:
step 4b submits only `O_tail` Oids, so the duplicate-tolerance rule in
`publish` ("duplicates … keep their original immutable bytes") can never
silently keep a dangling locator — there is nothing to collide with.

**Semantics (normative):**

1. Snapshot the source under pin. Register a `GenerationGuard` **before**
   opening the source redb read transaction (`store/guards.rs`: guard first,
   then locator lookup — otherwise compaction can retire and unlink a sealed
   segment between the manifest read and the link). Under the guard, read
   the source `generation` G_start, the committed sealed-segment manifest
   list M(G) (sealed entries only; the open tail manifest is photographed
   solely to compute the `O_tail` partition, never to link), the `objects`
   locator set O(G) partitioned as above, and the selected fact rows F(G)
   (§2.4) via bounded `fact_page` iteration. The guard pins every sealed
   segment in M(G) against the compaction reclaim path for the fork's
   duration. Publications may still commit concurrently; they are detected,
   not blocked, per step 1b.
   - 1b. Generation check **plus fact-row CAS revalidation** (both required
     because `fact_page` pages and the generation read are separate calls
     with no multi-call snapshot, **and** because facts-only publications do
     not advance the generation per `store/mod.rs:publish` docs — a pure
     generation check misses `bookmark.set` / `tag.set` / change mutations
     that land at a constant `G` and would copy mixed refs with stale
     objects). After the paged photograph completes: (i) re-read the source
     generation G_end; (ii) re-read **every** photographed fact key in F(G)
     by point lookup (`Store::fact`) and compare key+value bytes for exact
     equality (missing row, changed value, or unexpected extra row detected
     by re-paging the copied key ranges all count as movement). If
     `G_end != G_start` **or** any fact row differs, discard the
     photographed sets and retry from step 1 (bounded retries, e.g. 3, then
     fail with `E_STALE`-family "source moved during fork; retry"). Object
     churn is covered by the generation leg: additions publish new locators
     (generation advances) and `prune_unreachable` commits a new generation
     (`store/compact.rs:188-199`), so locator-only prunes trip `G_end`
     directly; step-4a manifest/extent re-verification is defense-in-depth
     against storage damage, not the prune-detection path. The reply reports
     `source_generation: G` actually forked so the caller can detect
     staleness. A `fork_at_generation` pinning arg is out of v1 scope.
     `[INFERENCE: no cross-method snapshot handle exists today; guard +
     generation check + full fact-row CAS revalidation is the minimal
     correct protocol with current pieces. Cost is ~2× the copied fact rows
     on the success path; fork is rare enough that correctness dominates.]`
2. `Store::create` the destination **in a staging sibling directory** (e.g.
   `<dest_path>.fork-<uuid>/store`), with a fresh `store-id`; fail if the
   staging path or the final `dest_path` already exists in any component
   (`format`, `meta.redb`, `segments` under `require_absent`). Nothing is
   created directly at `dest_path` until step 6.
3. For each sealed segment id in M(G): hardlink
   `source/segments/<id>` → `staging/segments/<id>` (same id preserves the
   `ObjectLocation.segment` references photographed in `O_sealed(G)`).
   If a hardlink fails with `EXDEV` (cross-device), fall back to a byte copy
   of that segment file and record `copied_segments: [...]` in the reply.
   The source's open/active tail segment is **never** linked. Every member
   of `O_tail(G)` is instead read from the source (`read`/`read_chunks`) and
   queued for re-append to the dest's fresh active segment in step 4b.
4. Install into the staging dest, in bounded batches, through **two** paths
   — this split is load-bearing (§1.3):
   - (a) **New internal primitive, e.g. `Store::import_sealed_snapshot`:**
     installs **only** the hardlinked sealed-segment manifests plus their
     `O_sealed(G)` locators verbatim and sets the destination `generation`
     to `G` (the forked source generation) **in meta and in the in-memory
     generation registry/guard epoch** (e.g.
     `guards.advance_published_generation(G)`), via the publication-owned
     commit path under the destination writer lock (mirroring the compaction
     atomic-install shape in `store/compact.rs:121-124` and the advance
     calls at `store/mod.rs:1286` / `store/compact.rs:198,623`).
     Rationale for both writes: imported locators carry source generations
     `≤ G`; `store/metadata.rs:432-435` rejects any locator whose
     generation exceeds the store generation as corrupt, so leaving the
     fresh store at generation zero (or any value below `G`) would fail
     every imported read at recovery — hence meta is set to exactly `G`
     without rewriting locators; and `Store::create` seeds the registry at
     0 (`store/mod.rs:334`) while `Store::open` seeds it from meta
     (`store/mod.rs:423`), so an import that skips the registry advance
     leaves reader pins deriving epoch 1 against meta `G` — hence the
     registry is advanced to `G` in the same primitive, before any reader
     can pin. `O_tail(G)` is explicitly excluded here: importing it would
     retain locators to the unlinked source tail. The primitive asserts the
     `O_sealed ∩ O_tail = ∅` partition before installing. Each linked file's
     committed extent (record framing, seal footer,
     `records_end`/`committed_end`) is re-verified against the photographed
     manifest before install. `Store::publish` **cannot** perform this step:
     it accepts only prepared objects + `FactTable` mutations
     (`store/mod.rs:911-930`) while `objects`/`segments`/`generation` are
     publication-owned (`store/metadata.rs:199-203`,
     `store/types.rs:FactTable` docs), and `PreparedObject` cannot be
     fabricated outside preparation (`store/types.rs`). The primitive (or
     equivalent `repo.fork`-internal transaction) is a required R2
     deliverable; `store/` currently has no adopt/import/install function.
     It runs **before** the destination's first `Store::open` recovery, and
     recovery then validates the installed state like any committed store
     (first open pays full verification per `store/recovery.rs`; the
     continuation segment id is recomputed from the installed manifests on
     that open per `store/mod.rs:191-192` + `store/recovery.rs:326-329`, so
     no separate id handoff is stored or transacted).
   - (b) **Normal `publish`:** tail objects from step 3 (fresh
     `PreparedObject`s built by preparation from source bytes — never
     fabricated locators) published bases-first across one or more commits
     (advancing generation past `G`) so each delta's base is already
     committed (satisfying `check_delta_base` which rejects same-batch
     bases), then the validated F(G) fact rows (§2.4) with
     `FactExpectation::Absent` (dest is fresh apart from step 4a, so every
     fact insert is create-only; any collision is corruption, fail closed),
     plus `complete=true` LFS presence rows (a real `FactTable`, so no
     primitive is needed for them). `pending` ledger rows are never copied.
     The F(G) rows installed here are exactly the rows revalidated in step
     1b: the CAS check and the install are ordered (validate → install into
     staging → no further source dependency), so a source facts-only publish
     that lands between validation and install cannot corrupt the fork — at
     worst the fork reflects the validated snapshot while the source has
     moved on, which the reported `source_generation: G` makes explicit.
5. LFS (when `include_lfs`): for each `lfs_objects` presence row with
   `complete=true` whose payload file verifies, hardlink (or copy on
   `EXDEV`) the sidecar file into the staging tree and publish the row via
   path (b); skip `complete=false` rows (fork does not inherit known-absent
   payloads; they re-resolve lazily via fetch).
6. Finish staging, then rename, then open at the final path (strict order).
   Drop/close **all** staging-rooted `Store` handles; fsync the staging
   `format` (already durable per `Store::create`) and its parent directory;
   **atomically rename the staging directory into `dest_path`**; then
   `Store::open` **at `dest_path`** (running the standard
   recovery/verification pass — always the full pass on first open after
   import) and register that dest-rooted handle with the daemon registry.
   Recovery sees the imported sealed manifests/locators (generation `G`)
   plus the re-appended tail objects as ordinary committed state. Never open
   at the staging path and rename underneath the live handle, and never
   register a staging-rooted handle: `Store.root` is a stored `PathBuf`
   (`store/mod.rs:162-163`) and later publications write via
   `self.root.join(SEGMENTS_DIR)` (`store/mod.rs:1209`), so a renamed-under
   handle would write segments to a path that no longer exists.
7. Reply: `{ dest_store_id, source_generation: G, objects: N,
   sealed_objects: N_sealed, tail_objects: N_tail,
   refs_copied: [...], segments_linked: [...], segments_copied: [...],
   lfs_linked: N, generation: dest_generation }` where `dest_generation ≥ G`.
8. Failure atomicity: any failure before the step-6 rename completes removes
   the staging directory best-effort; a leftover staging directory is never
   registered and never advertised, and — because retries mint a fresh
   staging name — never blocks a retry. `dest_path` itself is either absent
   or a fully renamed, verified store — never a half-built one. Guard
   release is unconditional (drop / scope exit) on all paths.

**What `repo.fork` deliberately does NOT do:** it does not create any rukie
metadata (org/repo rows, upstream link, permissions), does not copy live
coordination state (see §2.4), and does not notify anyone. The rukie
`fork-repo` op calls `repo.fork` first, then creates metadata transactionally
(§3.1), removing the staged/renamed Ruk dest store on metadata failure
(compensating delete — there is no shared Ruk/metadata transaction, §1.5).

### 2.4 Fact-row filter: what crosses the fork and what stays behind

Copied via path (b) (content + review continuity, all covered by the step-1b
CAS revalidation):

- `refs` — filtered by the `refs` arg (all direct + symbolic refs; conflicts
  excluded — see below). Symbolic refs are copied as symbolic (name → name),
  direct refs as name → Oid.
- `commits`, `changes`, `change_history`, `imported_changes` — **all rows**.
  Rationale: a fork must preserve the review graph (change-ids, divergence
  markers, imported-git mapping) so the fork's log/query/diff/history views
  work offline and so changes proposed back to upstream resolve against the
  same change-ids. These tables are pure content history with no liveness.
- `remote_observations` — NO in v1 (observations of third-party remotes are
  source-repo-specific; the fork re-observes on first fetch/push).
  `[INFERENCE: safe default; copying them would also be harmless but
  misleading.]`
- `reflogs` — NO (fresh history; the fork's creation is its first reflog
  event, recorded by rukie metadata, not Ruk).

Never copied (fresh trust / liveness domain per fork):

- `attestations` — **NO.** Attestations are signer statements about exact
  objects. Although still cryptographically valid after a fork, copying them
  would present the fork as CI-green without ever running the fork's
  pipeline, and `AttestationStatus{Live,Superseded,Ambiguous}` plus the
  supersession graph are pipeline-state, not content. The fork starts with
  zero attestations; CI re-runs on first landing/push. (If a future "trusted
  upstream attestation import" is wanted, it is an explicit, audited op —
  not a fork side effect.)
- Released-tag freeze markers — **NO** (despite being Ruk-side state).
  Rationale: a freeze marker is meaningless without its owning release row,
  and release rows (rukie metadata, §7.4) are never copied into the fork.
  Copying the marker would freeze the fork's tag with no release to explain
  it and no clear path: deleting a non-existent release cannot unfreeze it.
  The fork's tags therefore start unfrozen; publishing a release in the fork
  creates a fork-local marker under §7.1(5). If marker provenance is ever
  needed for audit, the fork's `fork_of_repo_id` chain (§3.1) already
  records where the tag came from.
- `claims` (incl. clock), `sessions`, `receipts`, `leases`, `leases`-scratch,
  `workspaces`, `ws_index`, `ws_status`, `ws_dirs`, `ws_apply`,
  `ops`, `op_head`, `push_plans`, `land_plans`, `published_heads`,
  `ref_conflicts`, `pins`, `promisor`, `shallow` — **NO.** All are live
  coordination, publication, or observation state bound to the source store's
  generation, sessions, and operator pins. The fork starts unpinned,
  unclaimed, with no workspaces and no in-flight plans.
- `lfs_objects` — presence rows for `complete=true` only (see §2.3 step 5).

Signed-off rule of thumb: **history crosses; liveness, trust, and freeze
state do not — the fork re-earns them.**

---

## 3. Fork network model

### 3.1 Upstream link and visibility cap (rukie metadata)

- `repos` row per repository (see §8): fork rows store
  `fork_of_repo_id NULL | <parent repo id>` and `fork_root_repo_id`
  (transitive root for "network" views), set once at fork time, immutable
  thereafter. Ruk itself stores **no** fork linkage (stores are independent
  by design, §2.2 item 2); the network is a rukie-metadata graph over
  independent Ruk stores.
- **Visibility cap (normative).** A fork must never be more visible than its
  upstream: `private` upstream ⇒ fork must be `private`; `internal` upstream
  ⇒ fork must be `private` or `internal` (never `public`); `public` upstream
  ⇒ any visibility. The `fork-repo` op validates the requested visibility
  against the source's visibility and refuses over-visible forks ("private
  history cannot be republished as public"). Rationale: a fork copies full
  history (§2.4); without the cap any reader of a private repo could publish
  its entire content as a public repo. Changing the upstream to *more*
  restrictive later does not retroactively restrict existing forks, but is
  surfaced in the fork-network UI ("upstream is now private").
- Deleting a parent does not cascade: children keep their stores and rows;
  `fork_of_repo_id` becomes dangling → surfaced as "upstream deleted"
  (foreign key `ON DELETE SET NULL` + `upstream_deleted_at` display flag).
  Deleting a repo with children is allowed but requires explicit
  confirmation when `forks_count > 0`.
- Forks are otherwise first-class repos: own permissions (within the cap),
  own watches/stars/releases/tasks/CI, own default branch (initialized from
  source default; may diverge afterward). There is no "sync to upstream"
  bit — syncing is an explicit fetch+merge operation (§3.2), never
  automatic.

### 3.2 Compare across forks (read-only; never mutates the base store)

- Compare is a read path, not stored state: `compare(base_repo, base_ref,
  head_repo, head_ref)` resolves both tips via the Ruk read surface, then:
  1. Ensure object availability **without mutating either repo**: the daemon
     materializes the union closure of the two tips into an **ephemeral
     scratch store** (or equivalent transient read view), never by fetching
     head objects into the base store. Publishing head objects into the base
     repo would grow a read-permission caller's target segments and quota
     and pollute its generation history — unacceptable side effects for a
     read path. `[INFERENCE: exact scratch-store primitive is an R2
     deliverable — this design requires "give a transient view the closure
     of tips T1 (store A) + T2 (store B)" without touching either repo.]`
     Where object sets are small, the rukie server may instead stream the
     closures through `read_chunks` into the scratch view.
  2. Compute merge-base + ahead/behind + `diff` page inside the scratch view
     using the existing `diff`/`log`/`query` machinery (revset
     `ancestors/descendants`, path filter `Files(...)`, real content
     comparison in `commit_changed_paths` — `revset/eval.rs`). The scratch
     store is destroyed when the compare completes; nothing persists.
- Fork compare pages show: ahead/behind counts, merge-base short id,
  file-diff page (reuse the change/branch diff renderer), and "changes not
  in upstream" (the head-only commits grouped by change-id, reusing the
  branch-view grouping from the changes design). Cross-fork compare of
  private repos requires read permission on **both** sides; the op checks
  both before touching Ruk.

### 3.3 Changes from a fork targeting upstream bookmarks

- Decision: **a change proposed from a fork lives in the upstream (target)
  repo**, not in the fork. Rationale: Ruk `changes`/`refs`/`commits` facts
  are per-store; there is no cross-store ref. The fork is an *object source*,
  the upstream store is the *review store*.
- Flow (`propose-change-from-fork` op):
  1. Validate: caller can read fork head ref and can create changes in
     upstream (permission check per the org/team design).
  2. Transfer the closure: daemon-side fetch of the fork tip's object closure
     into the upstream store (unlike compare, this transfer is an intended
     persistent write, gated on the caller's upstream write permission;
     idempotent — duplicates of committed objects are accepted when
     kind+length agree per `Store::publish` docs; delta bases transfer with
     their deltas in base-first commit order per `check_delta_base`).
  3. Create the change in upstream via the standard change verbs
     (`new`/`describe` or equivalent for pushed commits), recording
     provenance **in rukie metadata only**: `changes.provenance_fork_repo_id`,
     `provenance_fork_ref`, `provenance_fork_tip_oid`. The Ruk change record
     itself carries no fork pointer (keeps Ruk forge-neutral for R1).
  4. Subsequent pushes to the fork branch update the upstream change via an
     explicit "update from fork" action (re-fetch + rebase/squash/edit per
     normal review flow), not by polling.
- `refs/for/<branch>` stock-git pushes: the git receive path diverts
  `refs/for/*` to the review backend rather than performing ref updates
  (precedent: `transport/proc_receive.rs` AGit diversion). In the new forge,
  a push to `refs/for/<branch>` on **any** repo (fork or upstream) creates
  or updates a change in **that** repo targeting `<branch>` — the R5 daemon
  notification (`receive-pack` host notification of ref updates +
  `refs/for/*` change creation) carries: `{repo, pusher, for_branch,
  new_tip, push_options}` to rukie, which runs the §3.3 flow (creating the
  change in the pushed-to repo). Cross-repo targeting (`refs/for/` naming an
  upstream branch from a fork push) is NOT supported at the wire level; the
  UI/API op in step 3 above is the cross-repo path. This keeps the receive
  path simple and auditable.

---

## 4. Stars

### 4.1 Semantics

- A star is a public-or-private bookmark with no notification effect: "user U
  starred repo R at time T". Stars are visible per repo (count + stargazer
  list) and per user (starred-repo list, explore signal). Starring a private
  repo you can see is allowed; your star on a private repo is visible only
  to users who can see that repo (no leakage through the public stargazer
  API).
- Star/unstar is idempotent (re-starring is a no-op returning the current
  row; unstarring a non-star is a no-op). Self-starring your own repo is
  allowed (matches forge convention; no reason to forbid).

### 4.2 Tables (logical; owned by the store module)

```text
stars(
  user_id   BIGINT NOT NULL,
  repo_id   BIGINT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, repo_id),
  INDEX (repo_id, created_at)      -- stargazer list, keyset on (created_at, user_id)
  INDEX (user_id, created_at)      -- user's starred list
)
repos.star_count BIGINT NOT NULL DEFAULT 0   -- maintained transactionally (see §4.3)
```

- No surrogate id: the composite PK is the edge. This replaces Gitea's
  `Star{ID, UID, RepoID UNIQUE(s)}` (`models/repo/star.go`) with the same
  uniqueness and one fewer index.
- Private-repo stars need no extra column: visibility is derived at read
  time from the repo's visibility + membership (same rule as explore, §6).

### 4.3 Counter maintenance (transactional)

- `star-repo` / `unstar-repo` ops run in **one metadata transaction**:
  insert/delete the `stars` edge (guarded by PK conflict = idempotent
  no-op) **and** `repos.star_count = repos.star_count ± 1` in the same
  commit. The count column is the read path for badges, explore sort, and
  repo headers; the edge table is the source of truth for reconciliation
  (periodic `COUNT(*)` audit job corrects drift; drift is a bug, not a
  design input).
- Deleting a repo deletes its star edges + count in the same repo-deletion
  transaction (hard delete of edges; no tombstones in v1). Making a repo
  private keeps stars (unlike Gitea's `ClearRepoStars` precedent) — the
  read-time visibility filter (§4.1) handles privacy without destroying
  data. `[INFERENCE: Gitea clears stars on privatize (`star.go:
  ClearRepoStars`); the new design keeps them because filtering is cheap
  and un-privatizing would otherwise lose history.]`

### 4.4 Ops / interfaces (derived from the op table)

- `star-repo{repo}` → `{starred: true, star_count}`; `unstar-repo{repo}`;
  `get-star{repo, user?}` (viewer star state for rendering buttons);
  `list-stargazers{repo, cursor, limit}` (permission: repo visible);
  `list-starred-repos{user, cursor, limit}` (permission: public repos always;
  private repos only to viewers who can see them — the op filters, it does
  not leak existence beyond "no access" vs "empty" distinction owned by the
  auth design).
- MCP: `star_repo`, `unstar_repo`, `list_stargazers` (read-only-safe except
  the first two). CLI: `rukie star owner/repo`, `rukie unstar`, `rukie
  stargazers`. HTML: star button is a plain form POST (zero JS), count is
  server-rendered text.

---

## 5. Watches and the inbox feed

### 5.1 Watch levels (replace Gitea's five modes + three flags)

Gitea's model (`models/repo/watch.go`: `WatchModeNone/Normal/Dont/Auto/
Custom` × `IncludePullRequests/IncludeIssues/IncludeReleases`) is expressive
but confusing (four "watching" modes, a separate Auto that silently appears).
The new model has **four levels**, one dimension:

```text
watch_level:  all | participating | releases | ignore
  all           -- every event on the repo (pushes, changes, tasks, releases, CI completions)
  participating -- only events where you participate (mentioned, assigned, author, reviewer)
                    + releases published                       (DEFAULT for new watches)
  releases      -- only release-published events
  ignore        -- nothing, not even participation (overrides all)
  (no row)      -- no subscription. You get inbox rows ONLY for direct mentioned/assigned
                   events (§5.3, last no-row rule), never repo-wide fan-out. This is
                   strictly narrower than `participating`, which additionally delivers
                   release-published events and events on threads you author/review.
```

- Rationale: three positive levels cover observed use (watch everything /
  just tell me when I'm involved / just ship notes); `ignore` covers mute.
  The Gitea `Auto` (auto-watch on interaction) is dropped in v1 — it is the
  largest source of surprise notifications; users opt in explicitly.
  `[INFERENCE: product judgment, flagged for review — re-adding auto-watch
  as an opt-in user preference is a small follow-up.]`
- Changing level is idempotent upsert; valid transitions are unconstrained
  (any → any, including → `ignore`).

### 5.2 Tables

```text
watches(
  user_id    BIGINT NOT NULL,
  repo_id    BIGINT NOT NULL,
  level      SMALLINT NOT NULL,          -- enum all/participating/releases/ignore
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  updated_at TIMESTAMP NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, repo_id),
  INDEX (repo_id, level)                 -- fan-out scan per event
  INDEX (user_id, updated_at)
)

[552 more lines in resource. Use :19297 to continue]
watches(
…
)
repos.watch_count BIGINT NOT NULL DEFAULT 0  -- counts rows with level != 'ignore'
                                            -- (watchers, not mutes)
```

- `watch_count` maintenance mirrors stars (§4.3): same-transaction edge
  upsert + delta, where moving to/from `ignore` decrements/increments and
  moves between positive levels leave the count unchanged. Deleting a repo
  hard-deletes its watch rows.

### 5.3 Fan-out: which watchers get an inbox row per event

Event classes (owned by their feature designs; enumerated here for routing):

```text
push (bookmark/tag update) | change_opened/updated/merged/abandoned |
change_reviewed | task_opened/updated/closed/assigned | release_published |
ci_completed (pass/fail on watched branch/change) | mentioned | assigned
```

Routing rule evaluated at event-commit time (in the op that commits the
event, after the store transaction, before returning — enqueue, not deliver):

- `all` watchers of the repo → row for every class except `mentioned`/
  `assigned` duplicates (dedupe: one inbox row per (user, event), with
  `reason` = most specific: `assigned` > `mentioned` > `all`).
- `participating` watchers → row only when they are the actor's target
  (assignee, reviewer requested, mentioned `[@user]`, author of the change/
  task on status transitions) plus every `release_published`.
- `releases` watchers → row only for `release_published`.
- `ignore` → never. No-row users → row only for direct `mentioned`/`assigned`
  (participation without subscription; see the narrowed `(no row)` definition
  in §5.1 — never repo-wide fan-out, never releases).
- Actors never get a row for their own event (no self-notify), uniformly.
  `[INFERENCE: simplest consistent rule.]`
- Private-repo events only fan out to users who can see the repo (evaluated
  at enqueue time; permission changes later hide the repo link but keep the
  row, rendered as "inaccessible repository" — no content leak).

### 5.4 Inbox (replaces email + Gitea notifications)

```text
inbox(
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL,
  repo_id    BIGINT NOT NULL,
  event_kind SMALLINT NOT NULL,     -- enum of §5.3 classes
  event_ref  JSONB NOT NULL,        -- {change_id | task_id | release_id | ref+oids...}
  reason     SMALLINT NOT NULL,     -- all | participating | releases | mentioned | assigned
  is_read    BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMP NOT NULL DEFAULT now(),
  INDEX (user_id, is_read, id DESC) -- inbox list, keyset on id
  INDEX (user_id, created_at)
)
```

- Enqueue is transactional with the event where cheap (same store
  transaction appends outbox rows), delivered asynchronously (a sweeper
  materializes `inbox` rows; webhooks fire from the same outbox — owned by
  the webhook design). Inbox list/read/mark-all-read ops are keyset-paginated
  (`WHERE user_id=? AND id < ? ORDER BY id DESC LIMIT ?`).
- Unread count is `COUNT(*) WHERE user_id=? AND is_read=false` (no counter
  column — counts are small and exactness matters more than badge latency;
  `[INFERENCE: acceptable to at least 10k rows/user; revisit with a counter
  if profiles demand]`).

---

## 6. Explore (repository discovery)

### 6.1 Visibility filtering (normative)

- Repo visibility is `public | internal | private` (owned by the
  users/orgs/permissions design; referenced here):
  - `public`: listed to everyone including anonymous; content readable to
    everyone.
  - `internal`: listed to every authenticated user; content readable to every
    authenticated user; invisible to anonymous (treated as nonexistent).
  - `private`: listed only to members (explicit repo/org-team grant);
    invisible to all others (treated as nonexistent — same "no access vs
    empty" rule as §4.4).
- Every explore query applies the visibility predicate **in the store query**,
  not as post-filtering (no page-size or timing leak; no N+1 permission
  checks per row — membership is a join against a materialized
  `visible_repo_ids(viewer)` set or a `member_repo_ids` CTE, owned by the
  permissions design).

### 6.2 Sort keys (exactly three + one default)

```text
recently_landed  -- repos.last_landed_at DESC  (0 = never landed; sorts last; see below)
stars            -- repos.star_count DESC
activity         -- repos.last_activity_at DESC (any ref update, change/task/release/CI event)
recently_created -- repos.created_at DESC      (DEFAULT landing sort; stable, ungameable)
```

- `last_landed_at` is `BIGINT NOT NULL DEFAULT 0` (unix seconds; 0 means
  never landed), **not** nullable. Rationale: keyset pagination in §6.3 uses
  `WHERE (sort_key, id) < (?, ?)`; NULL sort keys drop never-landed rows
  after the first page under `NULLS LAST` ordering. With `NOT NULL DEFAULT
  0`, `DESC` ordering places never-landed repos last naturally and every row
  participates in the keyset. The same rule applies to any future nullable
  sort key: coerce to a non-null sentinel, never paginate over NULLs.
- `last_landed_at`: updated when a change lands / a branch fast-forwards via
  push / a mirror reconcile advances the default branch (the landing op owns
  this write). `last_activity_at` (`NOT NULL DEFAULT 0`, same convention):
  updated on the same plus change/task/release/CI events (the event-enqueue
  path owns this write — same transaction as the event row where possible).
- No "trending" score, no full-text repo search, no topic search in v1.
  Owner/name prefix filter (`WHERE lower(owner_name||'/'||name) LIKE
  lower(?)||'%'`) is supported as an optional query param on all sorts.
  `[INFERENCE: prefix filter is cheap with a functional index and covers the
  "find the repo I half-remember" case; full-text is future work.]`

### 6.3 Keyset pagination (normative)

- All explore lists use keyset cursors, never OFFSET: cursor =
  `(sort_key_value, repo_id)` with `WHERE (sort_key, id) < (?, ?)` for DESC
  sorts. The op returns `{rows, next_cursor?, has_more}`. Page size default
  25, max 100 (same envelope shape as Ruk `Page<T>` for consistency).
- Required indexes (logical; the store module maps to SQLite/Pebble/PG):
  `(visibility, last_landed_at DESC, id DESC)`,
  `(visibility, star_count DESC, id DESC)`,
  `(visibility, last_activity_at DESC, id DESC)`,
  `(visibility, created_at DESC, id DESC)`, plus the owner/name prefix index.
  Counts (`total`) are NOT returned (no `COUNT(*)` over the whole corpus per
  page view — "has more" only).

---

## 7. Releases

### 7.1 Binding to a Ruk tag (normative)

- A release is bound 1:1 to a Ruk tag: `releases.tag_name` ↔
  `refs/tags/<tag_name>` in the repo's Ruk store. Invariants enforced by the
  release ops:
  1. **Publish requires the tag to exist** and to resolve (through annotated
     tag objects) to a commit Oid. The publish flow resolves the tag via the
     Ruk read surface (R2 `tag.resolve` / `bookmark.resolve`-analog for tags)
     and stores `target_oid` + `target_kind (annotated|lightweight)`.
  2. One non-deleted release row per `(repo_id, lower(tag_name))`
     (case-insensitive uniqueness, matching the Gitea `lower_tag_name`
     precedent). Re-publishing the same tag after tag-move is refused
     ("tag already released"); the operator deletes the release first
     (explicit, audited) or cuts a new tag.
  3. Deleting the Ruk tag out from under a published release does NOT delete
     the release — the release page shows "tag deleted" with the pinned
     `target_oid` and immutable assets intact (releases are archival).
  4. Draft releases reference an intended tag name that may not exist yet;
     the name is validated for ref legality at draft creation
     (`refs/tags/` namespace rules from `protocol.rs:TagSetArgs`
     validation).
  5. **Released tags are frozen inside Ruk, not in the rukie op, keyed by
     owning release.** Once a tag has a non-deleted release, `tag.set`
     (move, representable as `TagSetArgs{expected: Present(old)}` in
     `protocol.rs`), `tag.delete`, and `git push` to `refs/tags/<name>` are
     refused while the freeze marker exists. Rationale: Ruk stores and the
     metadata store share no transaction (§8.1, §1.5), so a "check the
     releases table in the same transaction that issues `tag.set`" design
     has a check-to-use race (tag moves between the metadata check and the
     Ruk set; publish races pin a stale target), and misses the `git push`
     path entirely (`refs/tags/*` takes the normal update path —
     `transport/proc_receive.rs` diverts only `refs/for` and `refs/pull`).
     The freeze marker therefore lives in Ruk and is checked inside the Ruk
     tag transaction: a new Ruk-side freeze record keyed by tag name with
     fields `{owner_release_id, target_oid, frozen_at}` (exact table
     defined in §7.1(5a)) is written by the publish flow and consulted by
     every tag-mutation path (`tag.set`, `tag.delete`, receive-pack tag
     updates) with CAS semantics. The owner is the metadata release row id
     of the publishing draft (known before the marker write, since publish
     promotes an existing draft row): marker writes use create-if-absent CAS
     and fail when a live marker with a different owner exists, with the
     error naming the owning release id; a retry of the same draft's publish
     whose marker already names its own id is idempotent success on the
     marker step (same owner + same target), not a conflict. The rukie
     publish flow is then an ordered protocol, not a distributed
     transaction: (i) resolve + pin tag target in Ruk, (ii) write the freeze
     marker `{owner: <draft id>, target}` in Ruk (CAS on the tag name, fails
     if the tag moved under the resolver or a live marker with another owner
     exists), (iii) insert/promote the metadata release row, (iv) on
     metadata failure, remove the freeze marker **only if its owner still
     equals the publishing draft id** (owner-checked compensation; never
     blind-delete — a concurrent second publisher's live marker must
     survive). A leftover marker fails closed toward "tag frozen, no release
     visible" and is cleared by an audited repair op that verifies the owner
     row is absent, soft-deleted, or a discarded draft (§7.1(6a)) — never by
     blind deletion. The release page additionally renders a drift banner if
     the live tag ever disagrees with the pinned target, covering operator
     bypasses at the storage layer — but refusal inside Ruk is the primary
     rule. Concurrent second publishes for the same tag are rejected in
     order: the Ruk CAS rejects the second marker write first (marker step
     precedes the metadata step), with the live owner's id; a second row
     that somehow reaches the metadata step is additionally rejected by
     `UNIQUE(repo_id, lower_tag_name)` — neither path removes the live
     marker.
  - (5a) **Schema revision (normative).** `store/types.rs:FactTable` is
    closed: "a new table arrives only with a schema revision". The freeze
    marker is therefore a new census member, e.g.
    `FactTable::ReleasedTags` with table name `"released_tags"`,
    introduced in the next store schema revision after the current 27-table
    census (§1.3). Key: raw `refs/tags/<name>` bytes (same namespace rules
    as `refs`, so tag-name validity is shared). Value: postcard-encoded
    `ReleasedTagMarker{owner_release_id: i64, target_oid: Oid bytes,
    frozen_at: Timestamp}`. Migration: empty table on upgrade (no backfill;
    existing tags are unfrozen until released under the new code). Callers,
    exhaustively: `tag.set` preparation + lane (refuse while a live marker
    exists; error names `owner_release_id`), `tag.delete` (same), the
    receive-pack/`proc-receive` tag-update path (same check — this is the
    `git push refs/tags/*` path §1.5 proves the rukie op misses), publish
    (owner-keyed create-if-absent CAS write, idempotent on same-owner
    retry), draft-discard/delete/compensation/repair (owner-checked removal,
    §7.1(6)–(6a)), and `repo.fork` (explicitly skipped, §2.4). Release
    publishing without this revision is unspecified — there is no "marker
    without a table" implementation.
  6. **Soft-delete unfreezes with an owner check.** `delete-release`
     (soft-delete, §7.2) removes the Ruk freeze marker for
     `refs/tags/<name>` **only if the marker owner equals the deleting
     release id** as part of its protocol: (i) soft-delete the metadata row
     (`deleted_at` set, the only allowed post-publish mutation), (ii) remove
     the Ruk marker for that tag conditional on
     `marker.owner == <deleting release id>`, (iii) on marker-removal
     failure or owner mismatch, surface an audited "release deleted, tag
     still frozen — run repair" state rather than silently unfreezing
     another release's marker. Without the owner check, a
     publish-during-delete (old release deleted while a re-published same
     tag already wrote a new marker) would remove the live marker and
     unfreeze the tag; without any removal, a deleted release would block
     any re-release or move of its tag forever (the §7.1(2) "delete first,
     then re-cut" path requires the unfreeze). The `delete-release` op owns
     both steps (§8.2); a repair op clears orphan markers left by crashes
     after verifying ownership (§7.1(6a)).
  - (6a) **Draft-discard and repair (normative).** The crash window
    marker-written-but-publish-never-committed leaves a marker owned by a
    live draft: no published row explains the freeze, and the §7.1(6)
    soft-delete path (published rows only) cannot clear it. Two paths close
    it, and repair distinguishes them by owner-row state: (i)
    `discard-release-draft` op (new in §8.2): hard-deletes a draft row and
    removes its Ruk marker **conditional on
    `marker.owner == <discarding draft id>`** — the defined clearing path
    for the live-draft case; (ii) publish retry of the same draft id:
    idempotent marker success (same owner + same target, §7.1(5)) followed
    by the normal promote; (iii) `repair-released-tags` op: clears a marker
    only when its owner row is absent entirely or is soft-deleted
    (`deleted_at` set); a marker whose owner row is a **live draft or live
    published release is never cleared by repair** — repair reports
    "owned by live draft <id>: retry publish or run discard-release-draft"
    / "owned by live release <id>" instead. A marker owned by a live draft
    therefore always has a defined clearing path (retry, discard, or
    promote), and repair can never unfreeze a live owner's tag.

### 7.2 States, notes, and published-release immutability (normative)

- States: `draft → published`, plus `deleted` (soft delete: row kept with
  `deleted_at` for audit, assets retained per §7.3, freeze marker cleared per
  §7.1(6)); drafts additionally support hard-delete via
  `discard-release-draft` (§7.1(6a)), which clears an owned marker. No other
  states. `IsPrerelease`/`IsTag` boolean soup from Gitea
  (`release.go:Release`) is replaced by one `is_prerelease BOOLEAN` flag
  chosen at publish time and frozen thereafter.
- **A published release is fully immutable.** After `state` becomes
  `published`, no field of the release row may change — not `title`,
  `notes_markdown`, `notes_html`, `target_oid`, `is_prerelease`, asset list,
  or any other column. The only permitted post-publish mutation is the
  soft-delete transition (`deleted_at` set; row retained for audit; marker
  cleared per §7.1(6)). There is no `notes_edited_at`, no
  `edit-release-notes` op, and no title/flag fix-up path: a typo in notes or
  title is fixed by deleting the release and cutting a corrected one (new
  row; same tag name only after the old row is soft-deleted per §7.1(2)),
  or by cutting a new tag. Draft rows are fully mutable (title, notes,
  `is_prerelease` intent, draft assets) until the publish transaction
  commits; discarding a draft is a hard delete plus owner-checked marker
  removal (§7.1(6a)), not a state transition.
- Rationale: the task requires releases "immutable once published". Scoping
  immutability to "assets only" would leave `notes_markdown`, `notes_html`,
  and `title` silently mutable after agents and humans have pinned, linked,
  and attested to the release page. Full-row immutability (except the
  audited delete marker) is the only reading that satisfies the requirement
  literally and keeps agent-visible release state deterministic.

### 7.3 Assets: content-addressed, streaming, immutable

- Storage root: `<data-dir>/releases/sha256/<aa>/<bb>/<hex>` (mirrors the
  Ruk LFS sidecar fan-out `<store>/lfs/objects/<aa>/<bb>/<hex>`,
  `store/lfs.rs:sidecar_path` — same operator familiarity, same inode
  distribution). A SQLite/Pebble/PG row points at the content; the bytes are
  never in the metadata store.
- Upload: streaming `PUT` with chunked hashing (SHA-256 computed on the fly;
  size enforced against `release.max_asset_bytes`, default 2 GiB,
  configurable per instance; uploads exceeding the limit abort mid-stream
  and delete the temp file). Temp file is
  `<data-dir>/releases/tmp/<uuid>.part`; on completion the server verifies
  `size` + `sha256`, then atomically renames to the content address
  (same-device rename; `EXDEV` fallback copies + fsyncs, mirroring the fork
  fallback in §2.3). Dedupe is automatic: identical bytes land on the same
  path (link count > 1 is normal; never delete by path while any asset row
  references the digest — refcount in the asset row set, deletion by
  mark-and-sweep GC, not unlink-on-delete).
- Download: `GET` with full **Range request** support (via `http.ServeContent`
  semantics: `Accept-Ranges`, `206` partial, `If-Range`/`ETag: "<sha256>"`).
  No signed-URL indirection in v1 (auth is evaluated on the GET by the op;
  private-repo assets require repo read permission per byte range).
- Immutability: once the release is `published`, asset rows are append-only —
  no replace, no delete-then-reupload under the same name, and per §7.2 no
  asset additions at all. Adding a file post-publish is refused ("release is
  immutable; cut a new release"). Draft assets are mutable (replace/delete
  allowed) until publish, at which point every asset file is chmodded
  read-only as a defense-in-depth marker (the op-level refusal is the real
  enforcement; perms are the backstop). Discarded drafts (§7.1(6a)) delete
  their draft-only asset rows; published assets are retained per the
  mark-and-sweep rule even after soft-delete.
- Asset rows:

```text
release_assets(
  id            BIGSERIAL PRIMARY KEY,
  release_id    BIGINT NOT NULL REFERENCES releases(id),
  name          VARCHAR(255) NOT NULL,       -- display filename; UNIQUE(release_id, lower(name))
  sha256        CHAR(64) NOT NULL,           -- hex; path derived, not stored
  size_bytes    BIGINT NOT NULL,
  content_type  VARCHAR(127) NOT NULL,       -- sniffed at upload, stored for download headers
  uploader_id   BIGINT NOT NULL,
  created_at    TIMESTAMP NOT NULL DEFAULT now(),
  UNIQUE (release_id, lower(name)),
  INDEX (sha256)                             -- dedupe/GC scan
)
```

### 7.4 Release tables

```text
releases(
  id              BIGSERIAL PRIMARY KEY,      -- owner id referenced by the Ruk freeze marker (§7.1(5)-(6), §7.1(5a)/(6a))
  repo_id         BIGINT NOT NULL,
  tag_name        VARCHAR(255) NOT NULL,      -- exact case as in refs/tags/
  lower_tag_name  VARCHAR(255) NOT NULL,      -- UNIQUE(repo_id, lower_tag_name) WHERE deleted_at IS NULL
  target_oid      VARCHAR(64),                -- hex commit at publish; NULL while draft-without-tag
  target_annotated BOOLEAN NOT NULL DEFAULT false,
  title           VARCHAR(255) NOT NULL DEFAULT '',
  notes_markdown  TEXT NOT NULL DEFAULT '',   -- Markdown source; frozen at publish (§7.2)
  notes_html      TEXT NOT NULL DEFAULT '',   -- rendered at publish; frozen at publish (§7.2)
  is_prerelease   BOOLEAN NOT NULL DEFAULT false,  -- chosen at publish; frozen at publish
  state           SMALLINT NOT NULL,          -- draft | published
  author_id       BIGINT NOT NULL,            -- draft creator / publisher
  published_at    TIMESTAMP,
  created_at      TIMESTAMP NOT NULL DEFAULT now(),
  deleted_at      TIMESTAMP,                  -- soft delete; the ONLY post-publish mutation allowed;
                                              -- clearing it also clears the owner-checked Ruk marker (§7.1(6))
  UNIQUE (repo_id, lower_tag_name) WHERE deleted_at IS NULL,
  INDEX (repo_id, state, published_at DESC)   -- repo release list
  INDEX (repo_id, created_at DESC)            -- includes drafts for authors
)
```

- Publishing is an ordered owner-checked protocol across two stores (no
  shared transaction, §7.1(5)): resolve tag via Ruk → write the owner-keyed
  Ruk freeze marker first (CAS on the tag name with `owner = <draft id>`,
  failing closed on a live different owner — so a concurrent second
  publisher fails here, at its marker write, before ever reaching the
  metadata insert; a retry of the same draft id with the same target
  succeeds idempotently) → set `state=published, target_oid, notes_html,
  published_at` (plus final title/notes/`is_prerelease` from the draft) in
  one metadata transaction (additionally guarded by the §7.4 `UNIQUE`, which
  rejects a concurrent second row that survived its marker CAS — neither
  rejection path removes the live marker) → freeze asset perms (best effort)
  → enqueue `release_published` fan-out (§5.3) + webhook event; on metadata
  failure, remove the freeze marker only if its owner still equals the
  publishing draft id (compensation). Creating a release from a tag that
  does not resolve is `E_NOT_FOUND` with remedy "push the tag first" (RFC
  9457 problem detail via the op layer). Deleting is the mirror owner-checked
  protocol (§7.1(6)): metadata soft-delete → Ruk marker removal conditional
  on owner → repair op on partial failure (§7.1(6a)). Discarding is the draft
  analogue: hard-delete the draft row → marker removal conditional on the
  discarding draft id. No removal path is blind: every `remove marker` step
  names the expected owner and refuses on mismatch, so concurrent publishes
  and publish-during-delete can never unfreeze a live tag.

---

## 8. Store tables and Go package boundaries

### 8.1 Metadata tables owned by this design (summary)

Owned by the **store module** (one logical schema mapped to SQLite / Pebble /
PostgreSQL — physical mapping is the store-module design's job, not this
document's):

```text
repos(...)                          -- exists (auth/repos design); this doc adds:
  repos.star_count, repos.watch_count, repos.forks_count,
  repos.last_landed_at BIGINT NOT NULL DEFAULT 0,
  repos.last_activity_at BIGINT NOT NULL DEFAULT 0,
  repos.fork_of_repo_id NULL|BIGINT, repos.fork_root_repo_id NULL|BIGINT
stars(user_id, repo_id, created_at)                    PK(user_id, repo_id)
watches(user_id, repo_id, level, created_at, updated_at) PK(user_id, repo_id)
inbox(id, user_id, repo_id, event_kind, event_ref JSONB, reason, is_read, created_at)
releases(...)  + release_assets(...)                   (§7.4)
outbox(...)    -- shared with webhooks/tasks design; this doc only requires:
                  enqueue(event_kind, repo_id, event_ref) in the committing op's transaction
```

- All counters (`star_count`, `watch_count`, `forks_count`) are maintained
  in the same transaction as their edge writes. `forks_count` increments in
  the fork transaction (§3.1's metadata half) and decrements (never below 0)
  on repo delete.
- Asset bytes and Ruk stores are **outside** the metadata store: assets under
  `<data-dir>/releases/`, Ruk stores under the daemon's repository roots
  (`Registry.roots`, `daemon/registry.rs`). The metadata store holds paths/
  digests/ids only. Cross-store protocols (release publish/delete/discard,
  tag freeze/unfreeze) are therefore ordered owner-checked CAS +
  compensation, never a single commit spanning both stores.

### 8.2 Go package boundaries (new tree; Gitea packages are deleted per K1)

```text
internal/store/          -- the ONE metadata-store module (schema, migrations, tx helpers).
                           Tables above live here as queries; no other package imports SQL/Pebble/PG.
                           Enforces §7.2: UPDATE on releases WHERE state=published is rejected
                           unless it sets deleted_at (and even then only once, NULL→timestamp).
internal/ruk/            -- daemon client: session handling, repo.fork, read methods
                           (file/log/diff/query/blame), tag resolve + owner-checked released-tag
                           freeze/unfreeze marker (§7.1(5)-(6), §7.1(5a)/(6a) schema revision),
                           search-dependency calls, fetch-closure.
                           Owns Ruk error → RFC 9457 mapping. No HTML, no HTTP routing.
internal/ops/            -- Op[In,Out] registry + Huma wiring (K2). This doc contributes op specs:
                           fork-repo, compare, propose-change-from-fork,
                           star-repo, unstar-repo, list-stargazers, list-starred-repos,
                           set-watch-level, list-watches,
                           list-explore (+4 sort keys), inbox-list/mark-read,
                           create-release(-draft), upload-release-asset (streaming),
                           publish-release, delete-release (soft-delete + owner-checked marker removal),
                           discard-release-draft (hard-delete draft + owner-checked marker removal),
                           repair-released-tags (owner-verified orphan cleanup, §7.1(6a)),
                           download-release-asset (ranges).
                           NOTE: there is deliberately NO edit-release-notes /
                           edit-release-title / promote-prerelease op in v1.
internal/social/         -- stars + watches + fan-out rule (§4-§5). Calls internal/store,
                           enqueues to outbox. No Ruk calls.
internal/explore/        -- explore query builder (visibility predicate, 4 sorts, keyset).
                           Calls internal/store only.
internal/releases/       -- release state machine, Markdown render-at-publish, asset
                           staging/hash/rename/GC, Range serving helper. Calls internal/store
                           + internal/ruk (tag verify + owner-keyed freeze/unfreeze/discard marker)
                           + outbox. Owns the §7.2 published-row freeze, the §7.1(6) delete-unfreeze
                           protocol, and the §7.1(6a) discard/repair paths
                           (refuse-then-delete-and-recreate guidance + repair op).
internal/forks/          -- fork orchestration: ruk.repo.fork (staging + rename) → metadata tx
                           (visibility cap per §3.1) → rollback on failure. Calls internal/ruk
                           + internal/store.
internal/inbox/          -- inbox list/read/counters. Calls internal/store only.
                           (Fan-out enqueue lives in internal/social + each event source.)
```

- Dependency direction: `ops → {forks, social, explore, releases, inbox} →
  {store, ruk}`. `social/explore/releases/forks/inbox` never import each
  other (fan-out goes through the `outbox` rows + event envelope, not Go
  calls). HTML templates and MCP/CLI adapters consume `ops` results only —
  no package reaches past the op boundary for these features.

### 8.3 Ruk changes required by this design (not just `repo.fork`)

This design requires **two** Ruk changes, both with explicit schema/method
contracts — release publishing is not implementable on `repo.fork` alone:

1. **`repo.fork`** — specified normatively in §2.3–§2.4 (R2 scope),
   **including** the `Store::import_sealed_snapshot` (name owned by the Ruk
   implementer) internal primitive from §2.3 step 4(a) with destination
   generation set to `G` in meta **and** registry epoch, the GenerationGuard
   pin + generation check **plus full fact-row CAS revalidation** from §2.3
   steps 1–1b, the sealed/tail locator partition with disjointness assert,
   and the base-ordered tail copy. Without the primitive the linked bytes are
   unreachable: `publish` cannot install manifests/locators by design (§1.3);
   without the generation value the installed locators fail the
   `metadata.rs:432-435` invariant; without the CAS revalidation a
   facts-only source publish at constant generation copies a mixed snapshot;
   without the registry advance reader pins derive the wrong epoch.
2. **Released-tag freeze table (schema revision + all callers).** The
   §7.1(5a) `FactTable::ReleasedTags` census member (`"released_tags"`,
   key `refs/tags/<name>`, postcard value
   `{owner_release_id, target_oid, frozen_at}`) in the next store schema
   revision, consulted by **every** tag-mutation path — `tag.set`,
   `tag.delete`, the receive-pack/`proc-receive` tag-update path (refuse
   while a live marker exists; error names the owner), written by publish
   (owner-keyed create-if-absent CAS, idempotent on same-owner retry),
   removed owner-checked by discard/delete/compensation/repair (§7.1(6)–(6a)),
   and explicitly skipped by `repo.fork` (§2.4). Any implementation that
   ships release publishing without this revision, or that checks the freeze
   only in the rukie op, reintroduces the §7.1(5) check-to-use race and the
   `git push` bypass.
3. **Server-side code search — R3 dependency, not specified here.** The
   settled feature set requires Ruk-implemented lightweight code search (no
   heavy index). This design needs no search semantics for forks, stars,
   watches, explore (repo-level listing, §6), or releases, so the search
   method (name, pattern language, limits, snippet transport) is owned by
   the R3 search design. `internal/ruk` carries the call once R3 lands;
   nothing in §§2–7 depends on its shape.

---

## 9. Acceptance mapping

- **Fork mechanism justified with store citations:** §1 (layout incl.
  `require_absent` staging rationale + stored-root rename ordering,
  sealed immutability via `compact.rs` + `segment.rs` + `recovery.rs`,
  identity via `types.rs` + `store/mod.rs`, LFS via `store/lfs.rs`,
  `publish` limitation + duplicate-tolerance + facts-only-generation rule
  via `store/mod.rs:911-930` + `store/mod.rs:publish` docs (~878–880) +
  `store/metadata.rs:199-203` + `store/types.rs:FactTable/PreparedObject`
  docs incl. the closed-census rule, delta-base rule via
  `store/mod.rs:check_delta_base`, atomic-install shape via
  `store/compact.rs:121-124` + prune-generation commit via
  `store/compact.rs:188-199`, guard ordering via `store/guards.rs`,
  recomputed next id via `store/mod.rs:191-192` +
  `store/recovery.rs:326-329`, forced full first verification via
  `store/recovery.rs`, locator-generation invariant via
  `store/metadata.rs:432-435`, registry seeding/advance via
  `store/mod.rs:334/423/1286` + `store/compact.rs:198,623`) → §2.1 decision
  table (A/B/C with verdicts) → §2.3–§2.4 normative `repo.fork` semantics
  (staging + close-rename-open-at-dest, import primitive limited to
  `O_sealed` with disjointness assert, destination generation `= G` in meta
  and registry, guard + generation check + full fact-row CAS revalidation,
  base ordering, unfrozen fork tags) and row filter.
- **Stars specified:** §4 (semantics, tables, transactional counters, ops).
- **Watches specified:** §5.1–§5.2 (levels with unambiguous no-row rule,
  tables, counters) + §5.3–§5.4 (watch levels feeding the inbox, inbox
  schema).
- **Explore specified:** §6 (visibility filtering, 3+1 sort keys over
  non-null sentinels, keyset pagination + indexes).
- **Releases specified:** §7 (tag binding invariants + Ruk-side owner-keyed
  released-tag freeze/unfreeze/discard/repair via ordered CAS protocols with
  §7.1(5a) schema revision, draft/published states, Markdown notes frozen at
  publish with the whole row, content-addressed SHA-256 assets, streaming
  upload with size limit, Range downloads, full post-publish immutability
  except soft-delete, tables).
- **Store tables + Go package boundaries:** §8 (incl. the two required Ruk
  changes with the freeze-table revision, plus discard/repair ops).

---

### Critical Files for Implementation

- `ruk/crates/ruk-core/src/store/mod.rs` — `Store::create` exclusivity (`require_absent`), stored `root` (162-163) + `root.join(SEGMENTS_DIR)` writes (1209) forcing close-rename-open-at-dest, `publish` limits (911-930) + duplicate-tolerance + facts-only-generation docs (~878-880), `check_delta_base` ordering rule, `fact_page`/`generation` non-atomicity forcing §2.3 generation + CAS revalidation, in-memory `next_segment_id` (191-192), registry seeding (334/423) and advance (1286)
- `ruk/crates/ruk-core/src/store/metadata.rs` — publication-owned `objects`/`segments`/`pending` tables (199-203), locator-generation invariant (432-435); the fork copier must set destination generation to `G`, never leave it at zero
- `ruk/crates/ruk-core/src/store/types.rs` — `FactTable::ALL` census + closed-census rule, publication-ownership docs, `PreparedObject` privacy; the fork row filter, the import primitive boundary, and the §7.1(5a) `ReleasedTags` revision all depend on this file
- `ruk/crates/ruk-core/src/store/compact.rs` — sealed-segment immutability invariant plus the atomic locator/manifest/generation install shape (121-124), prune-generation commit (188-199), and registry advances (198/623) the import primitive mirrors; do not change retire-set rules without re-reviewing §2
- `ruk/crates/ruk-core/src/store/guards.rs` — `GenerationGuard` registration-before-read ordering that §2.3 step 1 pins sealed segments with, plus the published-generation advance the import must call
- `ruk/crates/ruk-core/src/store/recovery.rs` — first-open full verification (forcing `verify: full`), max-id-plus-one next-id derivation (326-329), unknown-file ignore rule
- `ruk/crates/ruk-core/src/store/lfs.rs` — LFS sidecar layout and presence rows; fork payload linking and release-asset layout both mirror it
- `ruk/crates/ruk-core/src/protocol.rs` — `bookmark/tag` arg shapes (incl. `TagSetArgs.expected` for the §7.1(5) freeze), `PageRequest`/`Page<T>` envelope, and where `repo.fork` + freeze-marker schemas must land
- `ruk/crates/ruk-git/src/transport/proc_receive.rs` — `refs/for` + `refs/pull` diversion scope proving `refs/tags/*` needs its own Ruk-side freeze check


[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Define draft discard cleanup …