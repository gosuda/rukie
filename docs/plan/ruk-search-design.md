Artifact path (no file-write tool exists in this session — only `read`/`grep`/`web_search` — so the complete artifact content follows for the main agent to persist verbatim):
`/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-search-design.md`

## Summary (advisories weighed; muse "no action" declined, all else acted on)
- Executor boundary fixed per blocker: core search MUST NOT touch daemon `BoundedExecutor` (dependency direction is `ruk -> ruk-core`; `dispatch_method` only awaits `coordinator.read`, daemon/mod.rs:2174). Chosen design: core-owned `SearchPool` (`tokio::sync::Semaphore` + `spawn_blocking`; `tokio` with `sync`/`rt-multi-thread` already in `ruk-core` deps) held by `HostedCoordinator`; daemon only plumbs `search_pool_size`. Rejected alternative (search in daemon dispatch) breaks lease/cursor/admission encapsulation.
- Scans stay in `HostedCoordinator::read` (stickler sidecar concern rebutted: `docs/coordinator.md` idle/burst behavior describes the CLI per-repo sidecar, not `HostedCoordinator`, which holds `Arc<Store>` persistently and whose `Store::read` is concurrent with publication; scans run on the pool via `spawn_blocking`, never holding the lane).
- Parallelism per qwen: shards are contiguous ranges over path-sorted order, limit applied during in-order emission, trailing shards cancelled — no post-merge truncation.
- Cursors use the existing `PageCursor` contract (`store/view/query/lease/position`), minted/validated like claim/attest/oplog; no custom cursor.
- `paths`+`globs` combined by one rule (minimax): pass `PathSelection` AND (no globs OR ≥1 include match) AND (no exclude match).
- Budget exhaustion always returns a (possibly empty) truncated page + resume cursor; never `E_LIMIT` (contradiction removed).
- Literal matching via `regex::escape` + `RegexBuilder` (memchr claim corrected: present transitively via `regex-automata` in lockfile, not a direct dep; no new deps beyond adding workspace `regex` to `ruk-core`).
- `coord/undo.rs` (`refuse_non_invertible`, no `_` arm) gains a `Method::Search` arm; same for daemon `decode_request_args` and a `read_gap` message arm.

## Artifact content (`ruk-search-design.md` — persist verbatim below)

# Ruk server code search (`search`) — design

## 1. Method and placement

New typed method `Search, "search", Read, SearchArgs, SearchReply;` in the `method_table!` in `ruk/crates/ruk-core/src/protocol.rs` (read-method precedent `Log/Diff/Show/File/Blame/Query`, protocol.rs:3326-3331). `Request::Search` / `Reply::Search` variants follow the macro-generated pattern (compiler directs any extra arms, e.g. request-digest matches). Name avoids the taken revset `query` (protocol.rs:2261). Kind `Read`: session-required, idempotency-key-free, served by `HostedCoordinator::read` (`ruk/crates/ruk-core/src/coord/hosted.rs:566`), dispatched inline by daemon `dispatch_method` (`ruk/crates/ruk/src/daemon/mod.rs:2174`).

## 2. Schema (`protocol.rs`, following `FileArgs`/`BlameArgs` conventions)

```rust
pub enum SearchPattern {
    Literal(ByteString),   // byte-exact needle; empty rejected (E_INVALID)
    Regex(String),         // Rust regex syntax, like `ruk grep`
}
pub struct SearchBudget {
    pub max_bytes_scanned: u64,  // default 256 MiB, cap 1 GiB
    pub max_blobs: u32,          // default 100_000
    pub deadline_ms: u64,        // default 5000, cap 30000
}
pub struct SearchArgs {
    pub at: RevisionRef,         // Head | Change (v1); Workspace → E_INVALID naming the gap
    pub pattern: SearchPattern,
    pub case_insensitive: bool,  // default false
    pub paths: PathSelection,    // All | Paths(Vec<RepoPath>)
    pub globs: Vec<String>,      // include globs, byte semantics of glob_match
    pub exclude_globs: Vec<String>,
    pub budget: SearchBudget,    // all fields optional-with-defaults wire-wise
    pub page: PageRequest,       // limit 1..=1000 (MAX_PAGE_SIZE), default 100
}
pub struct SearchHit {
    pub path: RepoPath,
    pub line: u32,               // 1-based
    pub column: u32,             // 1-based byte offset of match start
    pub preview: ByteString,     // first 1 KiB of the line; truncation marked
    pub blob: Oid,
}
pub enum ScanExhaustion { None, Budget, Deadline }
pub struct SearchReply {
    pub view: ViewInfo,
    pub hits: Page<SearchHit>,   // next.is_some() ⇔ more rows
    pub truncated: bool,         // == hits.next.is_some()
    pub exhausted: ScanExhaustion,
    pub scanned_blobs: u64, pub scanned_bytes: u64, pub skipped_blobs: u64,
}
```

`Validate for SearchArgs`: pattern non-empty and ≤4 KiB; regex compiles; ≤32 globs total, each ≤1 KiB and valid under `glob_match` rules (`revset/eval.rs` glob section); `page.validate()`; budget within caps (over-cap rejected, never clamped — `PageLimit` precedent, protocol.rs). Reuses `RevisionRef` (protocol.rs:960), `PathSelection` (protocol.rs:950), `Page/PageRequest/PageCursor/QueryDigest` (protocol.rs:399-700), `RepoPath` (`index.rs`), `ViewInfo`.

### Filter-combination rule (exactly one)

A path is searched iff it passes `paths` (under `All`, or at/under one of `Paths`) **AND** (`globs` is empty **OR** the path matches ≥1 entry of `globs`) **AND** the path matches no entry of `exclude_globs`. Glob matching reuses the existing byte-level `*`/`?`/`**` whole-path semantics (`revset/eval.rs::glob_match`).

## 3. Cursor contract (existing `PageCursor`, no custom type)

- `hits: Page<SearchHit>`; continuation is `PageCursor { store, view, query, lease, position }` minted by the existing `HostedCoordinator::mint_read_lease` (hosted.rs) with a 30 s interactive lease, validated on resume by `leases.check_cursor` + session/authority + stale-view checks exactly like `read_claims_page` / `read_attestations_page` / `read_oplog_page` (hosted.rs:714-930).
- `query` digest = SHA-256 over canonical v1 input (precedent `claim_query_digest` / `attest_query_digest`, hosted.rs:939-979): `b"ruk.search/v1\x00"`, resolved root tree oid, pattern kind + raw bytes + `case_insensitive`, canonical `paths`, sorted globs + exclude globs, budget triple. Any change → digest mismatch → `E_FORBIDDEN` + `Fix::RestartQuery` (never silent skip).
- `position` (fits `MAX_CURSOR_POSITION_BYTES` 4096) encodes two frontiers: `scan_after` (last fully-sealed path; scan resumes after it) and `emit_after` (last emitted `(path, line)`; emitted rows are skipped). Zero-hit pages still carry a cursor at the sealed scan frontier, so a rare literal on a large repo always resumes past spent budget.
- Stale view (head advanced since first page), unknown/expired lease, or cross-session use → `E_FORBIDDEN` / `E_LEASE_LOST` + `Fix::RestartQuery`, matching existing read behavior (hosted.rs:540-566 docs).

## 4. Algorithm (`ruk-core/src/search.rs`, new; pure engine + pool)

1. **Resolve**: `resolve_search_root(store, at) -> (view, root_tree)` — v1 serves `Head`/`Change`; `Workspace` returns `E_INVALID` naming the workcopy-crawl gap (`read_gap` style, hosted.rs:1349-1360). Built as the shared helper future `file`/`blame`/`diff` producers will reuse (those gaps are documented at hosted.rs:1357-1358).
2. **Enumerate once**: iterative stack walk of the root tree in repository byte order using `workcopy/tree.rs::parse_tree` + `git_order`, same object format as `revset/eval.rs::parse_tree_entries`/`read_tree_bytes` (same `MAX_TREE_DEPTH`/`MAX_TREE_ENTRIES` guards). Keep only blob-backed entries (regular/executable); drop symlinks, gitlinks, conflicts. Apply the §2 filter during the walk so subtrees under excluded prefixes are pruned before any blob I/O.
3. **Prefilter without payload I/O**: `Store::object_len` locator lookup (`store/mod.rs`) per blob; skip (count in `skipped_blobs`) blobs >512 KiB. (`Store::read` does verified decode with no metadata txn spanning payload I/O; `iter_objects` census likewise — store/mod.rs:700-860.)
4. **Dedupe**: `HashMap<Oid, Vec<path>>` over surviving entries; each unique blob is scanned once and its line hits fanned out to every sorted path. (Tree-id keying makes any future cache self-invalidating.)
5. **Match**: one engine path — literal compiles to `regex::escape(needle)`, regex used as-is, both via `regex::bytes::RegexBuilder` (+ `case_insensitive` flag). No new direct dependencies: workspace pins `regex 1.13.1` (only `ruk` crate depends on it today — `crates/ruk/Cargo.toml`); `memchr`/`regex-automata` are already transitively in the lockfile, and the regex engine's internal literal prefilter applies `[INFERENCE]`. (Correction of earlier draft: memchr is not absent from the build graph, only not a direct dep.) Line-split on `\n` (1-based; `verbs_grep.rs::scan_impl` semantics); one row per matching line at first-match byte column. Binary rule (matches `ruk grep` whole-file NUL semantics, `verbs_grep.rs`): any NUL in the decoded blob (already ≤512 KiB) → skip. Lines match whole; preview = first 1 KiB of the line.
6. **Emit in total order**: global row order is `(path bytes, line)`. Fanned-out hits are merged in that order; truncation at `limit + 1` sets `truncated`/`next`.

## 5. Parallelism: path-ordered shards on a core-owned pool (NOT the daemon executor)

- **Explicit non-use**: the daemon's `BoundedExecutor` (`ruk/src/daemon/executor.rs::run` — "blocking pool behind one bulkhead") serves filesystem/CLI jobs and is unreachable from `ruk-core` (crate direction `ruk -> ruk-git -> ruk-core`); `dispatch_method` awaits `coordinator.read` directly. The design therefore adds no callable bridge to it.
- **Core-owned pool** (new, `ruk-core/src/search.rs`): `SearchPoolConfig { permits: u32 }` (default 8, max 64) + `SearchPool { sem: Arc<tokio::sync::Semaphore> }` (`tokio` with `sync` + `rt-multi-thread` is already a `ruk-core` dependency). `HostedCoordinator` gains a `search_pool: Arc<SearchPool>` field, built in `open()` from the default and overridable via `with_search_pool()` for the daemon to inject its configured size. `read()`'s `Request::Search` arm awaits the pool and runs shard scans in `tokio::task::spawn_blocking` closures; `cancel: &Cancel` is checked between shards.
- **Shards defined by path order**: the sorted unique-blob list (ordered by first-path ordinal) is cut into contiguous shards (e.g. 8 × ~2 k blobs or dynamic by remaining budget). Shards scan concurrently up to pool permits, but emission is strictly in shard order with the limit applied during ordered emission — never a post-merge truncation. Early stop: once the limit row is sealed and all earlier shards are sealed, trailing shards are cancelled internally (never surfaced); a worker never stops while an earlier path partition is unfinished, so no rows before the limit are lost and completion order cannot affect emitted rows. Per-function cyclomatic complexity <10; no unbounded tasks (one blocking task per shard, semaphore-gated).

## 6. Budgets and errors

- Enforcement: byte counter (`scanned_bytes` over decoded blob bytes) + blob counter + `deadline_ms` (checked per blob via the coordinator clock). Current shard runs to its boundary, then the page seals at the scan frontier.
- Exhaustion (budget or deadline) → normal reply: partial (possibly empty) `hits` page with `truncated: true`, `next` cursor at the sealed frontier, `exhausted: Budget | Deadline`. Resumption re-spends budget from the frontier. There is **no** `E_LIMIT`-on-exhaustion path.
- Hard errors: `E_INVALID` (bad regex/glob/pattern/budget/limit/cursor shape, `Workspace` revision in v1); `E_NOT_FOUND` (unknown revision/tree/blob); `E_FORBIDDEN` + `Fix::RestartQuery` (cursor binding/stale view); `E_LEASE_LOST` (expired lease); `E_CANCELLED`; `E_IO`/`E_CORRUPT` (store). `E_LIMIT` only from pre-existing page/budget validators.
- Empty literal pattern is `E_INVALID` (deliberate divergence from `grep`, which matches every line — a server full-dump default is refused).

## 7. Optional cache (v2, omitted in v1 unless p95 misses)

Process-local LRU in `search.rs` keyed `(root tree Oid, query digest)` → `Arc<Vec<SearchHit>>` (pre-truncation cap: 2 k rows), ≤8 entries / ≤1 MiB total via the `budget.rs::ByteBudget` pattern, content-addressed (self-invalidating). No durable tables, no `FactTable` additions — "no heavy persistent index" holds by construction.

## 8. Cost estimate `[INFERENCE]` and performance target

10 k files × ~15 KiB avg ≈ 150 MiB decoded; unique blobs fewer after dedupe. Locator prefilter is metadata-only. Typical literal first page seals after a few MiB scanned (~100–300 ms single-shard `[INFERENCE]`); 8-permit pool keeps tail latency flat under load; pathological regex degrades to the 5 s deadline then resumes. Engineering bar: p95 page render ≤800 ms on the 10 k-file fixture (target to be validated by the perf test, §10).

## 9. Exact files to add/change in `ruk`

- ADD `crates/ruk-core/src/search.rs` — `SearchPool`/`SearchPoolConfig`, engine (enumerate/dedupe/match/emit), cursor position codec, query-digest constructor, `resolve_search_root`, unit tests.
- EDIT `crates/ruk-core/src/protocol.rs` — `SearchArgs`/`SearchPattern`/`SearchBudget`/`SearchHit`/`ScanExhaustion`/`SearchReply`, `Validate`, `method_table!` row, `Request::Search`/`Reply::Search` (+ any compiler-directed digest/match arms).
- EDIT `crates/ruk-core/src/lib.rs` — export `search`.
- EDIT `crates/ruk-core/src/coord/hosted.rs` — `search_pool` field, `open`/`with_search_pool`, `read()` `Request::Search` arm, explicit `read_gap` arm for `Search`.
- EDIT `crates/ruk-core/src/coord/undo.rs` — add `| Method::Search` to the `return Ok(())` (non-invertible read) list in `refuse_non_invertible` (:228-235; no `_` arm, so it fails to compile without this).
- EDIT `crates/ruk-core/Cargo.toml` — add `regex.workspace = true`.
- EDIT `crates/ruk/src/daemon/mod.rs` — `decode_request_args` read arm `Method::Search => Ok(Request::Search(...))` (:2332-2334 pattern); `DaemonConfig::search_pool_size` (+ builder, config-file key, validation mirroring `blocking_pool_size`, pool construction next to `BoundedExecutor::new`, :661).
- EDIT `crates/ruk/src/mcp/tools.rs` — `content_search` tool (`params_type: SearchArgs`), degraded local semantics documented like `attestation_query`.
- EDIT schema snapshot expectations regenerated from `protocol::model_schema_json` (fixture noted at `/tmp/ruk-model.json`).
- No changes to `store/` (no new tables), `physical.rs` (allocation reservations — untouched by search), `graph/`, or the daemon `BoundedExecutor`.

## 10. Test plan

- Unit (`search.rs`): literal/regex/`regex::escape`/case matrix; binary (NUL) skip; >512 KiB skip; §2 combination-rule matrix (prefix × include × exclude); dedupe fan-out to multiple paths; global order property test; cursor codec round-trip + mismatch rejection; exhaustion-with-zero-hits resumes past frontier; invalid inputs; pool permit bound (no more than N concurrent shard scans).
- Integration (daemon `tests.rs` + coordinator fixture style, cf. `prepare.rs::reads_have_no_preparation`): seed repo, serve `search`, walk all pages via cursors and assert equality with a single large-limit result; view-advance → `E_FORBIDDEN` + restart; expired lease → `E_LEASE_LOST`.
- Determinism: 20× concurrent identical searches byte-identical (shard completion order independence).
- Perf: synthetic 10 k-file repo; assert first page within default budget and p95 ≤800 ms `[INFERENCE]` target; assert scan touched no durable tables (store table list unchanged).
- Compile-guard: `undo` non-invertible classification test for `Method::Search`; schema JSON test updated.

### Critical Files for Implementation

- `ruk/crates/ruk-core/src/protocol.rs` — Search schema, Validate, method-table row, Request/Reply variants
- `ruk/crates/ruk-core/src/search.rs` (new) — SearchPool, engine, cursor codec, resolve helper, tests
- `ruk/crates/ruk-core/src/coord/hosted.rs` — Pool field, `read()` Search arm, lease-bound paging, `read_gap` arm
- `ruk/crates/ruk-core/src/coord/undo.rs` — `Method::Search` non-invertible arm (compile blocker)
- `ruk/crates/ruk/src/daemon/mod.rs` — Decode arm, `search_pool_size` config plumbing (no `BoundedExecutor` reuse)


[advisor] <advisory advisor="overlycritic" severity="concern" turns_ago="1" guidance="weigh, don't blindly obey"> Do not list MCP…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Fix shard ordering: first-pat…