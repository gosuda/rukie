**The artifact is still unwritten: I have no write tool.** My tools are read-only (`read`, `grep`, `web_search`, `ast_grep`) and I have no IRC tool, so the file below cannot be created by me. Main must write the fenced markdown to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-metadata-store.md`.

**Changes in this version:**
- **bbolt release date.** I set it to 2026-06-21, not the feed's 2026-06-23.
  - The GitHub release page for v1.5.0 says "released this 21 Jun" and its CHANGELOG link ends in `#v1.5.0 2026-06-21` [B7].
  - The proxy's 2026-06-03 [B1] is probably the commit time and the feed's 2026-06-23 [B3] a feed timestamp; both are discarded, and that explanation is `M`.
- **modernc FTS5 and backup claims.** They are tag-scoped: `vendor.json` at v1.60.1 → libsqlite3 v1.15.0 `generator.go` (`SQLITE_ENABLE_FTS5`) and `Makefile` (3530400). The old master-based `M5` key is gone.

````markdown
# research-metadata-store.md — forge metadata store candidates for rukie
Date: 2026-10-20. Scope: Pebble, SQLite (modernc / ncruces), bbolt, PostgreSQL+pgx v5. No recommendation.
Confidence labels (not citations): H = read in a fetched primary source; M = inferred from source or a secondary page; U = unverified.
Citations are `[Xn]` keys; the URLs are in "Reference links". Ruk-local sources are given as file paths.
Not fetched (cells citing them are M at best): G11, G12, G13, P15, and the CHANGELOG-1.5 page linked from B7.

## 0. Context: how Ruk stores metadata (redb)
- ruk pins redb =4.4.0, vendored and patched: /home/alpha/rewrite/git-renewed/ruk/Cargo.toml L7,L38 [H]
- One `meta.redb` per repo store beside a `segments/` dir: ruk/crates/ruk-core/src/store/mod.rs L95-97 [H]
- "Segments hold object bytes, redb holds every mutable fact, and one serialized writer sequences publication": store/mod.rs L4-6 [H]
- redb opens in `ExclusiveWriter` mode. The whole-file lock is held for the Database lifetime, so two processes serialize, reads included: store/mod.rs L379-385 [H]
- Write txns call `set_durability(Durability::Immediate)`: store/metadata.rs L277, L369, L630, L660, L813, L1099 [H]
- Read txns are short; none is held across payload I/O: store/recovery.rs L8-9 [H]
- Coordinator `PublicationLane`: a single serialized lane per repo with "2ms group-commit linger, up to 64 requests or 64 MiB per batch"; idempotent request-id receipt ledger; 30 s lease-bound cursors: ruk/README.md L5, ruk/docs/coordinator.md L16-20 [H]
- Implication for rukie [M]:
  - Ruk already gives per-repo single-writer + group-commit semantics and a receipt ledger for repo state.
  - Forge metadata is cross-repo and relational (users→orgs→teams→tokens→tasks→reviews), a different shape from Ruk's per-repo KV facts.
  - The daemon holds a redb lock per store; the forge store is a separate database and must not share that lock.

## 1. Versions, license, maintenance (as of 2026-10-20)
| | Pebble | modernc.org/sqlite | ncruces/go-sqlite3 | bbolt | PostgreSQL + pgx v5 |
|---|---|---|---|---|---|
| Latest tag / date | `pebble/v2` v2.1.7, 2026-08-24 [P1][P2]. v1 line stopped at v1.1.5, 2025-04-01 [P16] H | v1.60.1, 2026-09-29 [M1]. CHANGELOG head lists v1.61.0 (2026-09-30) [M2], but the proxy has no such tag, so v1.60.1 is latest tagged H | v0.35.6, 2026-09-23 [N1][N2]. Pre-1.0 H | v1.5.0, released **2026-06-21** (GitHub release page, "released this 21 Jun"; its CHANGELOG link ends `#v1.5.0 2026-06-21`) [B7] H. Other dates seen and not used: proxy 2026-06-03 [B1] (probably tagged-commit time, M) and feed 2026-06-23 [B3] (feed timestamp, M) | pgx v5.11.0, 2026-09-07 [G1]. PostgreSQL 18.6 current; 19 RC1 on 2026-10-15; PG14 EOL 2026-11-12 [G3] H |
| Bundled engine | n/a | SQLite 3.53.4: `vendor.json` at v1.60.1 vendors libsqlite3 v1.15.0 [M11], whose generator targets amalgamation 3530400 [M12]; SQLite 3.53.4 released 2026-07-24 [S3] H | SQLite 3.53.3 as of v0.35.2 [N2]; not re-confirmed for v0.35.6 H/U | n/a | pgx tested on PG 14-18 and CockroachDB [G2] H |
| License | BSD-3-Clause [P3] H | BSD-3-Clause; SQLite public domain; sqlite-vec MIT [M4] H | MIT [N5] H | MIT [B4] H | pgx MIT [G9]; PostgreSQL License [G3] H/M |
| Go requirement | v2.1.7 `go 1.23`; master `go 1.25.3` with no `/v2` suffix [P11] H | Go 1.26 required since v1.60.0 [M2] H | `go 1.26.0`, depends on `go-sqlite3-wasm/v6` [N7] H | `go 1.25.0`, toolchain go1.25.11 [B6] H | pgx `go 1.25.0` [G10] H |
| Maintenance | CockroachDB's engine; README "stable and production ready" [P4]; branches `crl-release-25.2…26.2` updated Oct 2026 [P13] H | Frequent releases; one primary maintainer; Tailscale and exe.dev sponsors [M2][M4] H | Frequent releases; one maintainer [N2] H | etcd-io project; v1.3/v1.4/v1.5 lines [B3] H | pgx tracks supported Go/PG versions [G2]; PG majors supported 5 years [G3] H |
| Pure Go / cgo | Pure Go core. zstd uses `DataDog/zstd` (cgo) when cgo is on; CGO_ENABLED=0 or tag `pebblegozstd` selects klauspost/compress [P5][P14] H | No cgo; SQLite C transpiled to Go [M4] H | No cgo; Wasm build translated by `wasm2go` [N3] H | Pure Go [B2] H | pgx pure Go [G2]; server is a separate process [G3] H |

## 2. Durability and crash recovery
- **Pebble:** `WriteOptions.Sync` defaults to true and syncs to disk; `NoSync` risks recent writes on crash [P6]. `WALBytesPerSync`, `WALMinSyncInterval` (options.go L1055-1065), optional `WALFailover` secondary dir, `DisableWAL`, `NoSyncOnClose` exist [P6]. Format-version marker and atomic manifest (fix notes in [P14]). Exact torn-WAL-tail handling not read [U]. H.
- **SQLite (modernc and ncruces):** WAL + `synchronous=FULL` is ACID/durable; WAL + `NORMAL` is consistent but may lose recent commits on power loss; app crashes never lose them [S2][S1]. The WAL-reset corruption bug is fixed in 3.51.3+ [S1]; both drivers bundle ≥3.53 [M11][M12][N2] H.
- **modernc specifics:**
  - POSIX locks are lost if any fd on the DB file is closed in-process. v1.58.0 added opt-in OFD locks (`MODERNC_SQLITE_OFD_LOCK=1` or `OFDLocking(true)`), default unchanged.
  - v1.60.0 turned an `-shm` mmap fault into an I/O error instead of a crash.
  - v1.56.0 patched an upstream SQLite 3.53.3 bug: a crash during the commit of a multi-database (ATTACH) transaction could leave a hot journal that `pager_playback()` deleted without replay. SQLite 3.53.4 fixed it upstream and v1.58.0 dropped the local patch [M2] H.
- **ncruces specifics:** from-scratch pure-Go VFS using OFD locks on Linux, "not as battle tested as the original" [N4]. Optional per-page checksums [N4]. WAL corruption under heavy concurrency on Windows fixed in v0.35.3 (#404) [N2] H.
- **bbolt:** two-phase commit: dirty pages + fsync, then checksummed meta page + fsync. `DB.NoSync` is the only unsafe option. Known ext4 fast-commit corruption on Linux 5.10-5.17 without listed kernel fixes [B2] H.
- **PostgreSQL:** WAL; `synchronous_commit` selectable per transaction; async-commit risk window up to 3× `wal_writer_delay`; `fsync=off` can corrupt after a crash [G4] H.

## 3. Concurrency, write throughput, group commit
- **Pebble:** concurrent writers via a lock-free commit queue; seqnum-ordered visibility; atomic batch visibility; snapshots (`NewSnapshot`); iterators give a point-in-time view; indexed batches [P7][P8]. Single process (directory LOCK) [P17]. README lists "Transactions" as not implemented [P4]. No grouped batches: each batch is written to the WAL individually; sync cost is amortized by the pipeline; `WALMinSyncInterval` delays syncs to coalesce them [P7][P6]. Large batches become flushable batches [P7]. H. Exact fsync-sharing mechanics beyond the doc [U].
- **SQLite (both):** one writer at a time; in WAL mode readers and the writer do not block each other; WAL needs a single host, no network FS [S1]. No built-in group commit; with FULL each commit syncs the WAL [S1] H. App-level single-writer goroutine with batching is the usual pattern [M].
- **modernc:** `database/sql` pool; set `SetMaxOpenConns`; each connection has its own page cache and libc state; `sql.Conn.Raw` connections are not goroutine-safe [M3] H.
- **ncruces:** goroutine-safe if no connection is shared; higher memory per connection [N3]. WAL needs mmap shm; dot-lock option copies shared memory and suits moderate concurrency [N4] H.
- **bbolt:** one read-write txn at a time, unlimited MVCC read txns; exclusive file lock so one process only; `DB.Batch()` coalesces concurrent `Update` calls (function may rerun; must be idempotent) [B2] H.
- **PostgreSQL:** MVCC, many concurrent writers [G11, not fetched] M. Automatic WAL group commit; `commit_delay` widens the window [G4] H.
- **Queue/lease patterns:**
  - PostgreSQL: `FOR UPDATE SKIP LOCKED` is documented for "queue-like table" consumers [G7] H.
  - SQLite: claim via `BEGIN IMMEDIATE` or `UPDATE … RETURNING`, serialized by the single writer [M].
  - bbolt and Pebble: read-modify-write inside one write txn (bbolt) or app-level locking/CAS (Pebble) [M].
- **Measured throughput (SQLite drivers only)** [M9]: run 2026-03-23, i7-1165G7, NVMe, Go 1.26, library versions per its go.mod (not necessarily latest); lower is better; **journal_mode=DELETE, synchronous=FULL, not WAL**; the author says "do not trust benchmarks, write your own":

| Scenario (ms) | mattn (cgo) | modernc | ncruces |
|---|---|---|---|
| Simple: insert 1M rows in 1 txn / query all | 1480 / 871 | 2419 / 758 | 2719 / 850 |
| Real: 100 users, 1 txn each, then joins (insert / query) | 1586 / 104 | 1759 / 130 | 1469 / 129 |
| Complex (insert / query) | 812 / 998 | 1554 / 1068 | 1749 / 1263 |
| Concurrent query, N=8 goroutines | 2395 | 2061 | 2355 |

- modernc's own CPU-time ratios vs C SQLite: about 2.0× (unindexed ORDER BY LIMIT), 1.9× (GROUP BY), 1.3× (indexed correlated subquery) [M3] H.
- No forge-shaped benchmark was fetched for Pebble, bbolt or PostgreSQL [U]. Pebble nightly benchmarks exist [P15] but were not opened.

## 4. Read path
- **Pebble:** LSM with block cache and bloom filters. `IterOptions{LowerBound, UpperBound, SkipPoint, PointKeyFilters, KeyTypes, OnlyReadGuaranteedDurable, UseL6Filters}`; `SeekGE/SeekPrefixGE/SeekLT/First/Last/Next` (+`WithLimit`). Iterators pin memtables and files (long-lived ones leak); use snapshots for long views [P6][P8][P18]. L0 backlog stalls writes (`L0StopWritesThreshold`) [P6] H.
- **SQLite:** B-tree pages in OS cache, planner-driven index use; modernc offers a pluggable page cache [M2] H.
- **bbolt:** mmap'd copy-on-write B+tree; values are zero-copy but valid only during the txn; read-heavy workloads good, random writes can be slow; long read txns block page reclamation [B2] H.
- **PostgreSQL:** network or Unix-socket round trip plus shared-buffers cache; pgx has automatic prepared-statement caching and binary protocol support [G2] H; latency numbers [U].

## 5. Query, index, constraint, FTS detail
- **Pebble:** no secondary indexes, constraints or FTS (absent from the README feature list [P4]) M. Custom `Comparer`, `Merger`, block-property collectors available [P6] H. You maintain index keys in the same batch for atomicity [M].
- **SQLite:** full SQL, planner, partial/expression indexes; JSON enabled in the modernc build [M12] H. FKs are per-connection `PRAGMA foreign_keys=1` [M9]. 3.53 can add/remove NOT NULL and CHECK via ALTER [S3] H.
- **modernc FTS5:** at v1.60.1, `vendor.json` [M11] points at libsqlite3 v1.15.0 whose generator passes `-DSQLITE_ENABLE_FTS5` (L201, L380) [M12] H. No test or doc confirming FTS5 end-to-end was found; a third-party FTS5 tokenizer adapter targets modernc [M10] M.
- **ncruces FTS5:** separate opt-in package `ext/fts5` [N6]; split out of the core build in v0.35.0 (breaking); custom FTS5 tokenizers since v0.35.4 [N2] H.
- **bbolt:** keys sorted bytewise; cursors with Seek, prefix and range scans; no indexes, constraints or FTS [B2] H/M.
- **PostgreSQL:** B-tree/GIN/GiST/partial/expression indexes [G12, not fetched] M; full FK/UNIQUE/CHECK [G13, not fetched] M; built-in `tsvector`/`tsquery` with GIN [G5] H.

## 6. Online backup / snapshot / replication
- **Pebble:** `DB.Checkpoint(destDir, WithFlushedWAL(), WithRestrictToSpans(…))` hard-links SSTs and copies WAL/MANIFEST/OPTIONS; space overhead grows with compaction [P10] H. README lists "Backups" as not implemented in the RocksDB sense [P4].
- **SQLite:** Online Backup API, `VACUUM INTO`, `sqlite3_rsync` [S4]. modernc: `backup.go` with `Backup.Step/Finish` at tag v1.60.1 [M6]. ncruces: `Conn.Backup`, `BackupInit`, `Restore` [N8]; Litestream read-replica VFS [N4] H.
- **bbolt:** `Tx.WriteTo` from a read txn is a consistent hot backup that does not block other readers or writers [B2] H.
- **PostgreSQL:** file-system-level backup and continuous archiving; pg_dump [G6] H.

## 7. Operational footprint
- **Pebble:** a directory of WAL/SST/MANIFEST files; large tuning surface (memtable, L0, compaction concurrency, cache, deletion pacing) [P6]; format-major-version ratcheting on upgrades [P4] H.
- **SQLite:** one file plus `-wal`/`-shm`, same host only [S1]. modernc requires pinning `modernc.org/libc` to the module's version [M2] H.
- **bbolt:** one file with an exclusive lock; high RSS from mmap [B2] H.
- **PostgreSQL:** separate server process with pooling (pgxpool), migrations, and pg_upgrade or dump/reload for majors [G2][G3]. Breaks the single-binary goal unless run as a sidecar [M]. Forgejo's go.mod carries pgx v5.11.0 [G8] H.

## 8. Known production users (each project confirmed by its own go.mod `module` line)
- **Pebble:** CockroachDB (default engine since v20.2) [P4]. go-ethereum: `module github.com/ethereum/go-ethereum`, requires `github.com/cockroachdb/pebble v1.1.5` (L16) and `github.com/cockroachdb/pebble/v2 v2.1.4` (L17) [P12] H.
- **modernc:** PocketBase: `module github.com/pocketbase/pocketbase`, `modernc.org/sqlite v1.60.1` [M7]. Gitea: `module gitea.dev`, requires both `github.com/mattn/go-sqlite3 v1.14.52` and `modernc.org/sqlite v1.60.1` [M8]. Tailscale is a named sponsor [M4] H.
- **ncruces:** no named production user verified [U].
- **bbolt:** etcd: `module go.etcd.io/etcd/v3`, `go.etcd.io/bbolt v1.5.0` direct require (L27) [B5]. README lists Consul, Containerd, InfluxDB, Portainer, Buildkit, NATS Streaming, etc. [B2] H. (Tailscale's go.mod [T1] lists `hashicorp/raft-boltdb/v2` directly and bbolt only as `// indirect`, which does not establish bbolt use, so it is excluded.)
- **PostgreSQL + pgx:** Forgejo: `module forgejo.org`, `github.com/jackc/pgx/v5 v5.11.0` [G8] H. Gitea's PG driver was not confirmed [U].

## 9. Pebble specifics
- **Module path / versioning.**
  - Tagged releases use `github.com/cockroachdb/pebble/v2` (v2.0.0–v2.1.7) [P1][P2]; v1 frozen at v1.1.5 [P16].
  - `master` and the `crl-release-*` branches use the unversioned path [P11][P13], and nothing newer than v2.1.7 is tagged [P2].
  - Release notes describe v2.x tags as "used in production by CockroachDB 24.3.0 / 25.3.x" [P2], so tag-pinned external consumers lag the CockroachDB branches.
  - pkg.go.dev reports "highest tagged major version is v2" for the unversioned path [P19] H.
- **API stability promise.** I found no written external-user API or compatibility policy in the README [P4], release notes [P2], docs listing [P20] or CONTRIBUTING. The README says it is "focused on performance and internal usage by CockroachDB" [P4]. The only compatibility text covers on-disk formats [P4]. Absence claim: M.
- **Format major versions.** The master README table runs FormatMostCompatible(1) … FormatColumnarBlocks(19). v2 and master support 13-19; v1 supports 1-16. v2 cannot open RocksDB DBs or formats below 13. Upgrades via `RatchetFormatMajorVersion` are permanent. The README's CockroachDB table lists 24.3 as latest-supported FormatColumnarBlocks [P4]. v2.0.8 and v2.1.5 fixed `Open` treating a format-major-version-1 directory as a fresh store and overwriting it; it now returns an error [P14] H. Table read from master; v2.1.7 README not separately fetched, M.
- **Default format is the lowest supported.** When `Options.FormatMajorVersion` is `FormatDefault`, `Open` sets it to `FormatMinSupported` (`FormatMinForSharedObjects` if shared-object creation is on): options.go L1495-1499 at v2.1.7 [P6]; the README says the same [P4]. rukie must set `FormatMajorVersion` explicitly or call `RatchetFormatMajorVersion` after open to get newer formats (e.g. columnar blocks) [H].
- **CockroachDB compatibility guarantee for external users:** none stated. Format compatibility is described only across CockroachDB-supported ranges [P4]. External API/semver guarantees are undocumented [M].
- **Commit pipeline.** Mutex-guarded sequence assignment and WAL append, then concurrent memtable apply, then in-order publication through a lock-free commit queue; no batch groups [P7] H.
- **Iterators with bounds.** `IterOptions` as in section 4 [P6]; `NewIter`/`NewIterWithContext`/`NewSnapshot`/`NewIndexedBatch` [P8]; `SeekGE`, `SeekPrefixGE`, `SeekLT`, `First`, `Last`, `Next` with `WithLimit` variants [P18] H.

## 10. Per-criterion comparison table (every cell cited; `[Xn]` = link in "Reference links")

| Criterion | Pebble v2.1.7 | modernc sqlite v1.60.1 | ncruces v0.35.6 | bbolt v1.5.0 | PostgreSQL 18 + pgx v5.11.0 |
|---|---|---|---|---|---|
| Latest release / date | v2.1.7, 2026-08-24 [P1][P2] H | v1.60.1, 2026-09-29 [M1]; CHANGELOG lists v1.61.0 but no such tag on the proxy [M2] H | v0.35.6, 2026-09-23 [N1][N2] H | v1.5.0, 2026-06-21 [B7] H (proxy 06-03 [B1] and feed 06-23 [B3] differ; see §1) | pgx v5.11.0, 2026-09-07 [G1]; PG 18.6 current, 19 RC1 [G3] H |
| License | BSD-3 [P3] H | BSD-3, SQLite public domain [M4] H | MIT [N5] H | MIT [B4] H | pgx MIT [G9]; PostgreSQL License [G3] H/M |
| Maintenance | CockroachDB engine, branches updated Oct 2026 [P13]; README "production ready" [P4] H | Frequent releases, Tailscale/exe.dev sponsors [M2][M4] H | Frequent releases, pre-1.0 [N2] H | etcd-io, v1.3/1.4/1.5 lines [B3] H | pgx follows supported Go/PG versions [G2]; PG 5-year support [G3] H |
| Pure Go vs cgo | Pure Go; cgo zstd when cgo on, `pebblegozstd` or CGO_ENABLED=0 avoids [P5][P14] H | No cgo, transpiled C [M4] H | No cgo, wasm2go [N3] H | Pure Go [B2] H | pgx pure Go [G2]; server separate process [G3] H |
| Go requirement | go 1.23 (v2.1.7); master has no `/v2` [P11] H | Go 1.26 required [M2] H | go 1.26.0 [N7] H | go 1.25.0 [B6] H | pgx go 1.25.0 [G10] H |
| Durability | WAL, `Sync` default true [P6]; `WALMinSyncInterval`, WALFailover [P6] H | WAL+FULL durable, NORMAL may lose on power loss [S2][S1] H; OFD locks opt-in [M2] H | Same SQLite semantics [S2]; custom Go VFS [N4] H | Two fsyncs per commit [B2]; ext4 fast-commit caveat [B2] H | WAL, per-txn `synchronous_commit` [G4] H |
| Writers / concurrency | Concurrent via commit queue [P7]; single process (dir lock) [P17] H | One writer, readers concurrent in WAL [S1] H; bound pool [M3] H | Same [S1]; goroutine-safe per connection [N3] H | One RW txn, many readers [B2] H | Many writers, MVCC [G11] M (page not fetched) |
| Snapshots / transactions | `NewSnapshot`, iterator point-in-time views, no transactions [P8][P4] H | WAL read snapshots [S1] H | Same [S1] H | MVCC read txns [B2] H | MVCC, isolation levels [G11] M (page not fetched) |
| Group commit | Batch committed individually; sync amortized by pipeline [P7]; `WALMinSyncInterval` [P6] H/U | None; sync on each commit with FULL [S1] H | None [S1] H | `DB.Batch()` [B2] H | Automatic, `commit_delay` [G4] H |
| Throughput evidence | None fetched [U] | DELETE-mode bench: 1M inserts 2.4 s [M9] H | Same bench: 2.7 s [M9] H | None fetched; README says random writes slow [B2] H/U | None fetched [U] |
| Read latency | LSM + bloom + cache; no numbers [P4] M/U | About 1.3–2× C SQLite CPU [M3] H | No own measurement; same bench as modernc shows same order of magnitude, e.g. concurrent N=8 2355 vs 2061 ms [M9] M | mmap, zero-copy reads [B2] H | Network RTT + cache; no numbers; pgx statement caching [G2] M/U |
| Secondary indexes | None; key-encoding by hand (absent from feature list) [P4] M | Full SQL indexes [M12][M3] H | Full SQL indexes [N3] H | None; buckets [B2] H | B-tree/GIN/GiST etc. [G12] M (page not fetched) |

[Showing lines 933-1331 of 1420 (49.8KB limit). Use :1332 to continue]
| Secondary indexes | None; key-encoding by hand (absent from feature list) [P4] M | Full SQL indexes [M12][M3] H | Full SQL indexes [N3] H | None; buckets [B2] H | B-tree/GIN/GiST etc. [G12] M (page not fetched) |
| FK / UNIQUE / CHECK | None, app-level (absent from feature list) [P4] M | Yes, FK pragma per connection [M9]; ALTER add/remove NOT NULL/CHECK in 3.53 [S3] H | Yes [N3][S3] H | None [B2] M | Full constraints [G13] M (page not fetched) |
| Full-text search | None (absent from feature list) [P4] M | FTS5 compiled in at v1.60.1 [M11][M12]; e2e unconfirmed [M10] H/M | FTS5 via `ext/fts5` import [N6][N2] H | None [B2] M | Built-in tsvector/GIN [G5] H |
| Online backup | `DB.Checkpoint` hard links [P10] H | Backup API in `backup.go` at v1.60.1 [M6]; VACUUM INTO [S4] H | `Conn.Backup`, Litestream [N8][N4] H | `Tx.WriteTo` hot backup [B2] H | pg_dump, base backup, PITR [G6] H |
| Ops footprint | LSM dir, many tuning knobs [P6] H | One file + wal/shm, same host [S1]; libc pin [M2] H | Same [S1]; higher memory per connection [N3] H | One file, one process [B2] H | External server; pg_upgrade/dump for majors [G3] H |
| Known users | CockroachDB [P4]; go-ethereum go.mod [P12] H | PocketBase [M7]; Gitea lists it with mattn [M8] H | Unverified [U] | etcd go.mod [B5]; README list [B2] H | Forgejo go.mod [G8] H |
| API / format stability | `/v2` tags; no written external API policy [P4][P11]; one-way format ratchet; opens at lowest format by default [P4][P6] H/M | Breaking `lib` const removals in v1.60.0 [M2] H | v0.x; FTS5 split was breaking [N2] H | Semver; API and file format fixed [B2] H | pgx semver [G2]; major PG needs pg_upgrade [G3] H |

## 11. Caveats / gotchas
- **Pebble:** the unversioned-path branches are not semver tags, so pin `/v2` tags or accept pseudo-version drift [P11][P13]. cgo zstd is the default with cgo; use `-tags pebblegozstd` to stay cgo-free [P5][P14]. Format defaults to the lowest supported; set it explicitly [P6]. Transactions, secondary indexes and constraints must be built in app code [P4] H/M.
- **modernc:** `modernc.org/libc` must be pinned to the module's own version; Go 1.26 required since v1.60.0; the `lib` API churns [M2]. libsqlite3 master has moved to amalgamation 3540100 (3.54.1) [M13], which is unreleased in modernc and not what v1.60.1 ships [M11][M12] H.
- **ncruces:** FTS5 and R*Tree are not included unless you import the ext packages [N2]. Memory per connection is higher [N3]. It is v0.x, so breaking changes are plausible [N2]. The custom Go VFS is less battle-tested than the C one [N4]. Windows WAL corruption #404 was fixed only in v0.35.3 [N2] H.
- **bbolt:** unsafe on ext4 fast-commit kernels 5.10–5.17 without the listed fixes; single process; no indexes or FTS [B2] H.
- **PostgreSQL:** external dependency that breaks single-binary deployment unless a sidecar is accepted [M].
- **Dates:** "latest" values come from proxy.golang.org and Atom feeds except bbolt, which uses the GitHub release page [B7]. The GitHub REST API returned 403, so some data comes from feeds [H].
- **Not done:** no benchmarks were run (plan mode); no Pebble/bbolt/Postgres forge-workload numbers; PostgreSQL MVCC, index and constraint pages (G11-G13), the Pebble nightly benchmark page (P15) and the bbolt CHANGELOG-1.5 page were not fetched.

## Reference links
- P1 https://proxy.golang.org/github.com/cockroachdb/pebble/v2/@latest
- P2 https://github.com/cockroachdb/pebble/releases
- P3 https://raw.githubusercontent.com/cockroachdb/pebble/master/LICENSE
- P4 https://raw.githubusercontent.com/cockroachdb/pebble/master/README.md
- P5 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/internal/compression/zstd_cgo.go
- P6 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/options.go
- P7 https://raw.githubusercontent.com/cockroachdb/pebble/master/docs/rocksdb.md
- P8 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/db.go
- P10 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/checkpoint.go
- P11 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/go.mod ; master: https://raw.githubusercontent.com/cockroachdb/pebble/master/go.mod
- P12 https://raw.githubusercontent.com/ethereum/go-ethereum/master/go.mod
- P13 https://github.com/cockroachdb/pebble/branches
- P14 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/internal/compression/zstd_nocgo.go ; https://github.com/cockroachdb/pebble/tags.atom
- P15 https://cockroachdb.github.io/pebble/ (NOT fetched)
- P16 https://proxy.golang.org/github.com/cockroachdb/pebble/@latest
- P17 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/open.go
- P18 https://raw.githubusercontent.com/cockroachdb/pebble/v2.1.7/iterator.go
- P19 https://pkg.go.dev/github.com/cockroachdb/pebble
- P20 https://github.com/cockroachdb/pebble/tree/master/docs
- M1 https://proxy.golang.org/modernc.org/sqlite/@latest
- M2 https://gitlab.com/cznic/sqlite/-/raw/master/CHANGELOG.md
- M3 https://gitlab.com/cznic/sqlite/-/raw/master/doc.go
- M4 https://gitlab.com/cznic/sqlite/-/raw/master/README.md
- M6 https://gitlab.com/cznic/sqlite/-/raw/v1.60.1/backup.go
- M7 https://raw.githubusercontent.com/pocketbase/pocketbase/master/go.mod
- M8 https://raw.githubusercontent.com/go-gitea/gitea/main/go.mod
- M9 https://github.com/cvilsmeier/go-sqlite-bench (rollback-journal mode, FULL sync)
- M10 https://pkg.go.dev/github.com/hakazv/sqlite-fts5-bigram/driver/modernc (page body not extracted; M-level evidence from a search snippet)
- M11 https://gitlab.com/cznic/sqlite/-/raw/v1.60.1/vendor.json
- M12 https://gitlab.com/cznic/libsqlite3/-/raw/v1.15.0/generator.go ; https://gitlab.com/cznic/libsqlite3/-/raw/v1.15.0/Makefile
- M13 https://gitlab.com/cznic/libsqlite3/-/raw/master/Makefile (ZIP = sqlite-amalgamation-3540100.zip)
- N1 https://proxy.golang.org/github.com/ncruces/go-sqlite3/@latest
- N2 https://github.com/ncruces/go-sqlite3/releases.atom
- N3 https://raw.githubusercontent.com/ncruces/go-sqlite3/main/README.md
- N4 https://raw.githubusercontent.com/ncruces/go-sqlite3/main/vfs/README.md
- N5 https://raw.githubusercontent.com/ncruces/go-sqlite3/main/LICENSE
- N6 https://raw.githubusercontent.com/ncruces/go-sqlite3/main/ext/README.md
- N7 https://raw.githubusercontent.com/ncruces/go-sqlite3/v0.35.6/go.mod
- N8 https://raw.githubusercontent.com/ncruces/go-sqlite3/main/backup.go
- B1 https://proxy.golang.org/go.etcd.io/bbolt/@v/v1.5.0.info
- B2 https://raw.githubusercontent.com/etcd-io/bbolt/main/README.md
- B3 https://github.com/etcd-io/bbolt/releases.atom
- B4 https://raw.githubusercontent.com/etcd-io/bbolt/main/LICENSE
- B5 https://raw.githubusercontent.com/etcd-io/etcd/main/go.mod
- B6 https://raw.githubusercontent.com/etcd-io/bbolt/v1.5.0/go.mod
- B7 https://github.com/etcd-io/bbolt/releases/tag/v1.5.0 (CHANGELOG link on that page: https://github.com/etcd-io/bbolt/blob/main/CHANGELOG/CHANGELOG-1.5.md, not fetched)
- G1 https://proxy.golang.org/github.com/jackc/pgx/v5/@latest
- G2 https://raw.githubusercontent.com/jackc/pgx/master/README.md
- G3 https://www.postgresql.org/support/versioning/
- G4 https://www.postgresql.org/docs/current/wal-async-commit.html
- G5 https://www.postgresql.org/docs/current/textsearch-intro.html
- G6 https://www.postgresql.org/docs/current/backup.html
- G7 https://www.postgresql.org/docs/current/sql-select.html
- G8 https://codeberg.org/forgejo/forgejo/raw/branch/forgejo/go.mod
- G9 https://raw.githubusercontent.com/jackc/pgx/master/LICENSE
- G10 https://raw.githubusercontent.com/jackc/pgx/v5.11.0/go.mod
- G11 https://www.postgresql.org/docs/current/mvcc-intro.html (NOT fetched)
- G12 https://www.postgresql.org/docs/current/indexes.html (NOT fetched)
- G13 https://www.postgresql.org/docs/current/ddl-constraints.html (NOT fetched)
- S1 https://sqlite.org/wal.html
- S2 https://sqlite.org/pragma.html
- S3 https://sqlite.org/releaselog/3_53_4.html
- S4 https://sqlite.org/backup.html
- T1 https://raw.githubusercontent.com/tailscale/tailscale/main/go.mod
````

### Critical Files for Implementation
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-metadata-store.md` — target artifact, unwritten (content above)
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/store/metadata.rs` — Ruk's redb usage and durability pattern
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/store/mod.rs` — single-writer and exclusive-lock semantics
- `/home/alpha/rewrite/git-renewed/ruk/docs/coordinator.md` — group-commit lane and receipt ledger
- `/home/alpha/rewrite/git-renewed/ruk/Cargo.toml` — redb pin and vendoring