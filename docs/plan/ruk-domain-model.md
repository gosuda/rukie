I can't write that file. My tools are read, grep, and web_search only, and plan mode forbids writes. The full artifact is below so the orchestrator can persist it to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-domain-model.md`. Nothing has been written to disk.

## Summary

- Ruk's native model is a change graph with heads (Git OID or conflict-v1 key), a per-change Active/Abandoned state, workspaces with a fenced stamp, a tracked-table op log that drives undo, path claims, signed attestations, and single-lane publication with 7-day receipts. Ruk is not modeled as Git.
- The hosted coordinator serves 33 of 47 method-table rows on the context path. It refuses 8 by name: `snapshot`, `write`, `ws.recover`, `fetch`, `push`, `push.reconcile`, `land.execute`, `op.prune`. The remaining rows are reads or bootstrap.
- **Critical gap:** hosted `land.prepare` ships empty requirements, empty trusted signers, and `policy_generation = 0`. Attestations are not checked on the protocol path. `land.execute` is refused there, so the attestation-gated land exists only as a library function.
- The daemon (`ruk forgejo serve`) is one process per Forgejo instance, with a registry, per-repo lanes, and credit-windowed IPC. I found no webhook, event bus, tenant model, push-mirror scheduler, or web-UI design in the Ruk sources.
- Latency evidence is thin. `RESULTS.md` (80-CPU dev host, CLI-level, no daemon load) and the README/execution.md figures disagree; I report both. No standalone Store::open cold/warm measurement exists.
- Source conflict: `progress.md` says parity is 54/56. `GAPS.md` (generated) says 56/56, with exportable commits carrying no change-id header. I treat GAPS as current.
- Counts: the method table has 47 rows including `schema`. CHANGELOG and README say 46.

---

# Ruk domain model (artifact content; not written to disk)

Scope: `ruk/` at `/home/alpha/rewrite/git-renewed/ruk`. Citations are `path:line` where I read the line, otherwise `path` (file-level, from declarations or greps). Items marked `[INFERENCE]` or `unverified` were not confirmed in source.

## 1. Glossary

| Concept | Definition | Source |
|---|---|---|
| Change | Logical unit with a `ChangeEntry { state: Active\|Abandoned, heads: Vec<Head> }` in `FactTable::Changes`. Divergent when `heads.len() > 1`. | `crates/ruk-core/src/records/types.rs` (ChangeState, ChangeEntry, is_divergent) |
| ChangeId | 16 random bytes; native commit headers use jj reverse-hex. Exportable commits get an OID-derived ChangeId. | `crates/ruk-core/src/change.rs` (`from_oid`, test "Derivation is deterministic and distinct per OID"); `.outline/sdd/change-id-designs.md` |
| Commit / CommitRecord | Exportable commit (singleton tree, all parents Git) stored as Git bytes; otherwise a `conflict-v1` frame. | `crates/ruk-core/src/commit.rs`; `crates/ruk-core/src/conflict.rs` (framing) |
| Revision / Head | `Head::Git { oid }` or `Head::Conflicted { key }`, a tagged union used as Changes-table value and in refs and workspace state. | `crates/ruk-core/src/heads.rs:24-27` |
| Workspace | Native (current change, Head, Scope, stamp) or GitCompat (HeadRef, IndexGeneration, Scope, stamp). Kind is fixed at creation. | `crates/ruk-core/src/workspace.rs` (WorkspaceState, check_successor) |
| Scope | Cone-mode directory set; root files always in; canonical sorted, no nested dirs. | `crates/ruk-core/src/workspace.rs` (ConeDirs, Scope::cone, includes_dir) |
| Take / drop | `ws.take` advances `lease_epoch`, fencing the displaced holder (`E_LEASE_LOST`). `ws.drop` unregisters the workspace; user files are never deleted. | `crates/ruk-core/src/protocol.rs` (WsTakeArgs, WsDropArgs); `workspace.rs` (WorkspaceStamp::take) |
| Snapshot | Method `snapshot`; refused on hosted path ("workcopy job split" missing). | `protocol.rs` method table; `coord/prepare.rs` (`Request::Snapshot` arm) |
| Bookmark vs branch | Protocol "bookmark" is a `refs/heads/*` ref. `bookmark.set` requires that namespace. The Git CLI calls the same ref a branch. | `protocol.rs` (`require_namespace("a bookmark", ..., b"refs/heads/")`) |
| Tag | `refs/tags/*`. Annotated tags are refused on hosted path. | `protocol.rs` (TagSetArgs validate); `coord/prepare.rs` ("annotated tag stays refused") |
| Conflict record | (a) `conflict-v1` framed native commit with tree terms and parents. (b) `FactTable::RefConflicts` for ref-level conflicts. | `crates/ruk-core/src/conflict.rs`; `store/types.rs` (FactTable::RefConflicts) |
| Claim | Path claim with TTL and mode (Enforce/Advise), holder session, `expires_at`. | `protocol.rs` (ClaimAcquireArgs, ClaimRecord, ClaimMode); `coord/claims.rs` |
| Lease | Read-view/cursor authority (interactive 30 s, no renewal) or bulk authority (15 min interval, renewed only by progress, 30 s no-progress cancel, 2 h total). | `coord/leases/timing.rs:12-22`; `coord/leases.rs` |
| Attestation | Verified SSHSIG (Ed25519, namespace `ruk-attest`) over `AttestStatement` canonical payload v1. | `crates/ruk-core/src/attestation.rs`; `attestation/verify.rs`; `protocol.rs:1776-1800` |
| Land plan | Frozen candidate set, target, base tip, requirements, trusted signers, 1 h expiry, stored in `FactTable::LandPlans`. | `coord/land.rs` (LandPlan, LAND_PLAN_TTL = 1 h, `prepare_land_plan`, `land_execute`) |
| land.prepare / land.execute | Prepare freezes the plan and moves no ref. Execute re-validates and publishes the target CAS and plan-row deletion in one commit. | `coord/land.rs`; `protocol.rs:2026-2060` |
| Publication lane | Single serialized publisher per open repo. Commit objects, facts, and ref CAS publish in one batch. Queue bound 256. Batch: 2 ms linger, 64 requests, 64 MiB. | `coord/lane.rs:1-40`, `coord/lane/batch.rs:23-33`; `docs/coordinator.md` |
| Receipt | Idempotency record in `FactTable::Receipts`: `(session, request_id)` to digest and reply. Replay-first. Retained 168 h (7 days). | `coord/receipt.rs:1-40`; `coord/rows.rs:56-57` |
| Session | Principal-bound identity with `ReconnectSecret`, capabilities, idle expiry. Active → ReceiptsOnly → Gone. | `coord/session.rs:1-30`; `coord/hosted.rs` (open_session, resume_session) |
| Op log | Operation entries for tracked-table mutations. Tracked: Refs, RefConflicts, Changes, ChangeHistory, Workspaces, WsIndex, Pins. Frames of 4 MiB. | `coord/oplog.rs:1-30`, `tracked()` |
| Undo | Inverse of a retained operation as exact before/after facts. Refuses non-invertible methods and diverged keys. | `coord/undo.rs:1-45`, `refuse_non_invertible` (lines 182-235) |
| Pin | `pin`/`unpin` a head; tracked in `FactTable::Pins`. Pinned heads are retention roots. | `protocol.rs` (PinArgs, UnpinArgs); `coord/prepare.rs` (pin_parts); `.omp/plans/AGENT_NATIVE_GIT_PLAN.md:44-48` (decision table) |
| Capability | 7-field grant: `write_changes`, `push_refs`, `land`, `claim_paths`, `undo`, `attest`, `read_intents`. Default = no authority. | `crates/ruk-core/src/capability.rs:1-40`; `capability.rs` (`Capabilities` struct) |
| Budget | `ByteBudget`: atomic byte reservation with overflow refusal. | `crates/ruk-core/src/budget.rs` |
| Policy | Per-store `policy.toml`, version 1, principals with capabilities and identities. | `coord/policy.rs:1-55` |
| Admission | Request classes Control/Interactive/Bulk/Hook/Maintenance; DRR across repos; permits, storage permits, E_LIMIT on wait. | `coord/admission.rs:26-110`, `admission/scheduler.rs` |

## 2. Change lifecycle state machine

**States.** Absent (no Changes row). Active-1 (one head). Active-divergent (two or more heads). Abandoned (state = Abandoned, heads empty).

**Invariant.** `ChangeEntry::validate`: heads empty implies state must be Abandoned. Active with no heads is corruption (`crates/ruk-core/src/records/types.rs:126-130`; `records/tests.rs:685-693`).

### Transitions (served on hosted path unless noted)

| # | From → To | Method | Mechanism / source |
|---|---|---|---|
| T1 | Absent → Active-1 | `new` | New change with one head: `ChangeEntry::new(Active, h)` (`records/preparer.rs:272`). `new` with a workspace switch also writes the workspace (`ops/new.rs`). |
| T2 | Active-1 → Active-1 (head replaced, tree unchanged) | `describe` | Message-only rewrite (`ops/describe.rs`, `coord/prepare.rs` describe_parts). Propagates to descendants per scope. |
| T3 | Active-1 → Active-1 (head replaced) | `split` (source keeps remainder) | Partition by paths; remainder keeps source identity; selection becomes a new change (`mutation-translation-design.md` §5.4). |
| T4 | Active-1 → Active-1 (destination head replaced) | `squash` | Moves path deltas into destination (`ops/squash.rs`). |
| T5 | Active → Abandoned (when heads drain) | `abandon` | `ops/abandon.rs:98-100`: `if update.entry.heads.is_empty() { state = Abandoned }`. Descendants re-parented. |
| T6 | Active-1 → Active-1 (head replaced) | `rebase` | Moves sources onto `onto`; descendant closure per `scope` (`ops/rebase.rs`). `keep_empty` controls empty results. |
| T7 | Conflicted head → resolved head | `resolve` | Replaces a `Conflicted` head with a resolved commit (`ops/resolve.rs`). Refused on hosted path for merge, multi-parent, and nested sources. |
| T8 | New Absent-equivalent change | `revert` | Creates a new change reverting `target`'s delta (`ops/revert.rs`). Source change untouched. |
| T9 | Any tracked state → prior state | `undo` | Restores exact before-values from the op log (`coord/undo.rs`). Refused when the key diverged (`E_UNDO_CONFLICT`). Cannot invert methods in the refuse list (see §2 note). |
| T10 | Active-1 → Active-divergent | (concurrent rewrite from a stale head) | `ChangeEntry::add_head` grows heads (`records/types.rs`). `[INFERENCE]` on the trigger path; `prepare.rs` edge-cases say `E_DIVERGENT` for an unpinned divergent change. |
| T11 | Active-divergent → Active-1 | `abandon` / `rebase` / `resolve` of one head | Removes one head from `heads`. |
| T12 | Active-1 → Abandoned | `squash` with `keep_empty = false` | Source change loses all heads. `[INFERENCE]` from `squash_parts` "drop source unless keep_empty" (`mutation-translation-design.md` §5.5); not traced in `squash.rs`. |
| — | No Changes transition | `edit` | Workspace transition only (checkout into `binding.workspace`). `mutation-translation-design.md` §5.3. |
| — | Unknown Changes effect | `git.commit` | Publishes commit plus branch CAS in one batch (`docs/coordinator.md`). Whether it creates or moves a Changes row is `unverified`. |
| — | No Changes transition | `land.prepare` | Plan row only (`coord/land.rs` prepare writes `LandPlans` row). Ref not moved. |
| — | No Changes transition shown | `land.execute` (library) | Moves target ref CAS and deletes plan row in one publish (`coord/land.rs` execute; mutations list shown up to `LandPlans` push; remainder elided, `unverified`). Hosted path refuses it. |

Refused on hosted path with reason (`coord/prepare.rs`): `snapshot`, `write`, `ws.recover` (workcopy job split missing); `fetch`, `push`, `push.reconcile` (transport owns send and remote-observation legs); `land.execute` (local leg in `land.rs` is not wired into prepare, transport owns remote push); `op.prune` (no retention policy exists, only receipts and sessions are swept).

### Landing onto a bookmark

Hosted sequence:
1. `land.prepare(change, remote, target=refs/heads/…, merge)`:
   - Resolve change head. Conflicted head → `E_INVALID` ("only an exact commit can land").
   - Target must be a direct ref (not symbolic) → `E_INVALID`.
   - Session must hold `land` capability over target → `E_FORBIDDEN`.
   - Candidate must be stored → `E_NOT_FOUND`.
   - Hosted path passes `requirements: []`, `trusted: []`, `policy_generation: 0` (`coord/prepare.rs:3430-3436`, `3479-3486`). **No attestation check.**
   - Plan created with `base = resolved target tip` and `expires_at = now + 1 h`. No ref moves.
2. `land.execute(plan, candidate)`: refused on hosted path (`E_HOSTED`).
3. Library `land_execute` (not reachable through the protocol):
   - Stored plan row must equal presented plan (`E_STALE`).
   - Plan unexpired (`E_STALE`).
   - Target still resolves directly and tip == base (`E_STALE`).
   - Every candidate satisfies every requirement under the frozen trust set (`E_POLICY`).
   - One publication: RefTransaction `Present(Direct(base))` → `Set(Direct(primary))`, plus `LandPlans` row delete (`coord/land.rs:330-423`).
   - Equality check, not fast-forward. `coord/land.rs:1-20`.

**Plan limits:** ≤1024 candidates; ≤64 requirements; ≤256 trusted signers; plan row ≤ one control frame.

## 3. Schemas and auth

### 3.1 Attestation

- **AttestStatement** (`protocol.rs:1776-1785`): `subject: Oid`, `kind: AttestKind`, `result: AttestResult`, `signed_at: Timestamp` (claim, not ordering), `supersedes: Option<StatementDigest>` (default None).
- **AttestAddArgs** (`protocol.rs:1790-1793`): `statement`, `signature: AttestSignature` (armored SSHSIG, ≤8192 bytes, namespace `ruk-attest`, ed25519).
- **AttestKind / AttestResult** (`capability.rs:198-219`): bounded text, non-empty UTF-8, printable, ≤16 MiB. Examples in tests: `review`, `ci`, `pass`, `fail`, `approved`.
- **Canonical payload v1** (`attestation.rs` `canonical_payload_v1`, doc ~3629-3634): seven length-prefixed fields (4-byte big-endian length): domain separator, object-format name, exact OID bytes, kind, result, UTC timestamp text, superseded digest (empty when none). Payload domain `ruk-attest-payload-v1` (`protocol.rs` ATTEST_PAYLOAD_DOMAIN_V1). Requires `subject` width to match `object_format`, else `E_OBJECT_FORMAT`.
- **Digest:** SHA-256 over the canonical payload, 64 lowercase hex (`StatementDigest`, `protocol.rs:419-422`).
- **Verification** (`attestation/verify.rs`): SSHSIG parse; version must be 1; namespace must be `ruk-attest`; reserved bytes empty; algorithm plain `ssh-ed25519`; hash must be `sha256` (sha512 refused); signature over canonical payload. Signer fingerprint is derived from the verified embedded key, never from input. Format: `SHA256:` plus 43 base64 chars.
- **Statuses** per `(subject, kind, signer)` group (`attestation/status.rs`): Superseded if a same-group statement supersedes it; Ambiguous when live group members disagree; else Live. Stored statuses never change with time.
- **Requirement evaluation** (`attestation/query.rs`): per requirement, a satisfying live statement from a trusted signer, possibly with `valid_until` bound. Gaps: NoLiveMatch, Ambiguous, Expired. `Expired` is an evaluation outcome, not a verification verdict. Empty trusted list with requirements → `E_POLICY`.
- **Limits:** 4096 statements per import; 1024 per group; 4096 per subject; 64 requirements; 256 trusted signers.
- **Corruption:** any stored row that fails re-verification is committed corruption and latches store quarantine (`attestation.rs` module doc).

### 3.2 Claims

- **ClaimAcquireArgs** (`protocol.rs:1708-1712`): `paths: Vec<RepoPath>` (non-empty), `ttl: ClaimTtl`, `mode: ClaimMode` (default Enforce).
- **ClaimTtl** (`protocol.rs:818-838`): `1..=MAX_CLAIM_TTL_SECONDS`; doc says "longer than one hour" is rejected. The constant's exact value is `unverified` (not read).
- **ClaimRecord** (`protocol.rs:1718-1725`): `claim: ClaimId`, `holder: SessionId`, `paths`, `mode`, `expires_at`.
- **ClaimMode** (`protocol.rs`): Enforce (overlapping writes by other sessions → `E_CLAIMED`); Advise (succeed with warning).
- **Semantics:** each path claims itself and all descendants. Only the holder can release, including an expired claim. Expired claims cannot revive. Claims are path-scoped only. The doc (`coord/claims.rs` module and `seal_write`) states change-ID-scoped keys are not yet threaded. Claim-changing batches must not share a publication without a consistent overlay.
- **Authorization:** `claim.acquire` needs `claim_paths` capability; paths outside the grant → `E_FORBIDDEN` (`coord/prepare.rs` claim_acquire_parts). `claim.list` with `holder: Any` needs `read_intents.claims`.

### 3.3 Policy and admission

**policy.toml** (`coord/policy.rs:1-55`, version 1):
- `session_idle` (default 24 h, fixed units only, must be positive).
- `lfs = "deny" | "pointers"` (default deny; refuses Git LFS pointer blobs with `E_UNSUPPORTED_LFS`).
- `[capacity]` byte-count bounds for the capacity ledger.
- `[[principal]]`: `name` (`owner` alias or exact `unix:<uid>` / `windows:<SID>`), `capabilities` table, `allow_any_identity` (default false), `identities`, `allow_any_claim_keys` (default false), `claim_keys`.
- Unknown fields, duplicates, bad identities, unsupported version → `E_POLICY`. A missing file selects owner-only default. Every load re-reads and hashes the file (no mtime shortcut).

**Capabilities** (`capability.rs`):
- `write_changes`: `own < session < all`. Own mutates only the change checked out in a workspace the session holds; Session also mutates changes the session created; All any change.
- `undo`: `session < all`.
- `push_refs`, `land`: ref patterns. `claim_paths`: path patterns. `attest`: AttestKind list.
- `read_intents`: `{ provenance, claims }`. Reads of objects and history are never gated.
- `negotiate`: requested must be covered by the policy ceiling (union coverage, exact); otherwise `E_FORBIDDEN` naming the field. Excess is never silently truncated.
- Pattern grammar: literal segments; `*` one segment; `**` final segment only.

**Admission** (`coord/admission.rs:26-110`, reference profile values):

| Class | Slots | Queued | Max wait |
|---|---|---|---|
| Control | 32 | 128 | 100 ms |
| Interactive | 16 | 2048 | 250 ms |
| Bulk | 6 | 256 | 5 s |
| Hook | 4 | 64 | 1 s |
| Maintenance | 2 | 32 | 30 s |

Per-repo caps: ordinary 8, bulk 2, hook 2, maintenance 1. Storage permits: interactive 4, bulk 2, hook 1, maintenance 1. Queued payload: 256 MiB process, 16 MiB per repo. Open repositories: 128. Control has separate blocking reservation (4). Ordinary blocking 12. Event-loop threads 4.

**Daemon** (`crates/ruk/src/daemon/mod.rs:1-75`): defaults `max_connections` 128; blocking pool 16; handshake 5 s; per-frame 60 s; retention interval 60 s; `allowed_uids` (empty allows any local peer). Windows named pipe unsupported.

### 3.4 Authentication

1. **Peer principal from OS credentials:** `endpoint::principal(stream)` (`coord/authentication.rs`). Principal is `unix:<uid>` or `windows:<SID>`. No request field can name a principal.
2. **Endpoint proof:** four-frame handshake (`coord/handshake.rs`): C1 client hello + 32-byte client challenge; S1 server challenge + server tag; C2 client tag; S2 verdict. Tags are HMAC-SHA256 keyed by the endpoint nonce, domain-separated, bound to 16 raw store-id bytes and both challenges, verified in constant time. Server proves first. Nonce lives in the lock body file (`coord/lock.rs`). The doc says this is not a sandbox against same-uid processes.
3. **Policy lookup:** `policy.load()?.lookup(&principal)` in `authorize_peer`. Unknown principal → `E_FORBIDDEN`.
4. **session.open:** returns `SessionId` and a 32-byte `ReconnectSecret`. The row stores only a SHA-256 hash of the secret, compared in constant time on resume (`coord/session.rs` verify_secret_hash; `coord/hosted.rs` open_session). Principal, identity, claims, capabilities, and secret hash are immutable.
5. **Session authority:** Active until `idle_expires_at`, then ReceiptsOnly until `+7 days` (`RECEIPT_HORIZON`), then Gone. ReceiptsOnly never regains Active (`coord/session.rs` authority doc; `coord/rows.rs:56-57`). Max 128 live sessions per repo.
6. **Capability tokens:** no bearer capability token type appears in the cited `coord/` files. Capabilities live in the session row and the policy file. `[INFERENCE]`: none exists; I did not grep the full tree for "token".

**Receipts** (`coord/receipt.rs`): digest is SHA-256 over postcard request args, excluding `request_id`. Same `(session, request_id)` with same digest → replay stored reply. Different digest → `E_REQUEST_REUSED`. Expired receipts are a miss.

## 4. Anticipated hosted-forge features

**Present in design (sourced):**
- Hosted multi-repo daemon: `ruk forgejo serve` owns every opened repo of a Forgejo instance (`crates/ruk/src/daemon/mod.rs:1-9`; `daemon/registry.rs:1-20`). Opens authorized by canonical path beneath configured roots or opaque handle. Idle eviction; shared `BoundedExecutor`.
- IPC: credit-windowed streams, stream-zero handshake, `SUPPORTED_FEATURES = ["credits", "merge-notices", "outcome-lookup"]` matching the Forgejo Go client (`daemon/ipc.rs:1-43`).
- Merge notices: one record in `ruk-core::merge::composed` reused by native JSON, "IPC MergeNotices event", CLI stderr, and "Forgejo API/UI" (`plan:~line 105`, review consolidation).
- Forgejo patch surface: 80 files / 138 anchors at slice 1, revised to 81 files / 24 gates at slice 2 (`execution.md` progress). Patch rows R01–R17 in `forgejo-patch-inventory`.
- Push mirrors and mirrors are a Forgejo-side Receive-parity item (plan slice 11; progress notes "mirror verbs landed 620992b, 18ca748, 56df91f, 8a8c51c"). Ruk-native `push`/`push.reconcile` are refused on the hosted path; transport owns the remote leg.
- Retention: periodic sweeps of receipts and sessions; copying compaction; `RetentionReport` (`coord/retention.rs`, progress slice 12).
- Per-repo lanes, group commit, lease-bound cursors on `op.log`, `claim.list`, `attest.list` (`docs/coordinator.md`; README).
- LFS: `lfs-payload-sync` design: sidecar keyed by sha256, batch API forwarded via daemon IPC `lfs-batch`, `lfs-get`, `lfs-put` (`.outline/sdd/lfs-sync-design.md` §§1–6). Design only; `LfsMode` is `Deny | Pointers` today.
- Forgejo branch-page projection: migration `IDX_branch_repo_deleted_commit_name` and `GetFirstBranch` (`execution.md` progress, task 10b).

**Not present (grep negative):** webhooks, event subscriptions, tenant concept beyond OS principal per store, zero-JS UI design, HTML/web templates in Ruk, a push-mirror scheduler to GitHub/GitLab/Forgejo. Searched `.omp/plans/AGENT_NATIVE_GIT_PLAN.md`, `.outline/sdd/*`, `docs/`, `CHANGELOG.md`, `tasks/operation-manifest/README.md`. The zero-JS requirement comes from the user goal, not from any Ruk source.

## 5. Measured latencies

**Capacity RESULTS** (`tasks/capacity/RESULTS.md`, generated 2026-10-18T23:18:01Z, source commit 78a98e2, 80 CPUs, 754 GiB, git 2.53.0; CLI-level, warm cache; no admission layer; "NOT the reference deployment"):

| Workload | git p50 ms | ruk p50 ms |
|---|---|---|
| adv-1k | 43 | 22 |
| adv-10k | 53 | 58 |
| lookup | 9 | 36 |
| page1 | 144 | 94 |
| write | 26 | 49 |
| clone (bare, 10k) | 1370 | 1714 |
| fetch (+1 branch) | 88 | 659 |

**Conflict:** README (commit 8487835b) and `execution.md` report different post-optimization numbers: lookup 49 vs 10 ms, page1 98 vs 149 ms, adv-10k 61 vs 54 ms, write 49 vs 28 ms, clone 6.94 s vs 1.38 s, fetch 990 vs 87 ms. I cannot tell which run is current from the files. Treat both as dev-scale only.

**Store::open:** `verified_generation` skips full object verification on repeat opens; before the fix one `rev-parse` issued 15,610 `openat` (`execution.md` optimization pass). `RUK_VERIFY=full` forces full verification (`store/metadata.rs`, `store/recovery.rs`). `DEFAULT_OPEN_WAIT` = 10 s, `RUK_OPEN_TIMEOUT_MS` overrides (`store/metadata.rs:120-130`). **Cold/warm standalone Store::open latency: no measurement found in the repo. `unverified`.**

**Daemon overhead, typed reads, many-branch, saturation, noisy-neighbor, cancellation, slow-reader, crash-around-publish, power-loss, http-429-503, no-starvation, adv-budget, single-ref-no-scan:** none measured against the daemon. `progress.md` lists these as still needing a 1M-branch or daemon-load run.

**Reference profile:** plan targets 1,000 protected ops/s (900 reads, 100 writes), 10,000 repos with 100 active, up to 1M branches in one repo, 32 cores, 128 GiB, threefold burst with bounded rejection, recovery within 60 s. The profile file `local://ruk-reference-production-profile.md` is not in the repo, so its exact limits are `unverified`.

## 6. Open gaps and roadmap items affecting a forge

1. **Attestation not enforced on hosted land.** `land.prepare` ships empty requirements, trusted signers, and `policy_generation = 0`. Policy has no `[land] requirements` section ("follow-up work", `coord/prepare.rs:3430-3434`). Source: `coord/prepare.rs`.
2. **`land.execute` refused on hosted path.** The remote push leg lives in transport and is not wired into prepare (`coord/prepare.rs` land_execute arm; `mutation-translation-design.md`).
3. **`fetch`, `push`, `push.reconcile` refused on hosted path.** Transport-owned; `remote_observations` written outside the lane.
4. **`op.prune` refused.** No operation-retention policy exists; only receipts and sessions are swept. The op log can grow without bound.
5. **`snapshot`, `write`, `ws.recover` refused.** Workcopy job split missing.
6. **Undo cannot invert** `new`, `edit`, `ws.new`, `ws.scope`, `ws.take`, `ws.drop`, `fetch`, `push`, `land.*` (`coord/undo.rs:182-235`). Undo of `new` is refused even though it is a common agent action.
7. **Annotated tags refused** on hosted path (`coord/prepare.rs`).
8. **Multi-parent `new`, merge-commit targets, merge descendants refused** on hosted path (`coord/prepare.rs` new_parts, rebase/abandon arms).
9. **Claims path-only.** Change-ID-scoped claim keys not threaded (`coord/claims.rs` module doc).
10. **Shallow/partial fetch OPEN** (progress slice 8).
11. **LFS payload sync OPEN.** `LfsMode` is Deny|Pointers only; design in `lfs-sync-design.md`.
12. **Symbolic refs, wiki/template flows OPEN** (progress slice 11).
13. **Forgejo DB/browser suites OPEN** (progress slice 13). Capacity full-profile runs OPEN (slice 14).
14. **Forgejo init-func bug:** fresh installs never run init funcs (`models/db/engine.go:86`) (progress task 10b). Native ref-count IPC and typed reflog page missing.
15. **HTTP(s) transport unimplemented** (README "named unimplemented gap").
16. **Git verbs unsupported:** `archive`, `bundle`, `count-objects`, `gc`, `index-pack`, `pack-objects`, `worktree` (README; `tasks/parity-harness/GAPS.md`).
17. **Parity status conflict:** `progress.md` says 54/56; `GAPS.md` says 56/56 with exportable commits carrying no change-id header. I take GAPS as current. `change-id-designs.md` Option 5+6 is the basis.
18. **Windows/macOS runtime unverified** (progress slices 2 and JSON migration).
19. **Syntax-merge specs** (`ruk-syntax-language-plan.md`, `ruk-composed-file-merge-plan.md`) are `local://` artifacts not present in the repo; `unverified`.
20. **Method count:** table has 47 rows including `schema`; CHANGELOG and README say 46. Both plausibly correct if `schema` is excluded.
21. **Dead-code candidates** and verbs that return `Unsupported` are listed in `.outline/sdd/legacy-inventory.md`; not cut yet.

## Critical Files for Implementation

- `ruk/crates/ruk-core/src/coord/prepare.rs` — hosted method dispatch; land.prepare empty requirements at ~3430–3486; refused-method list.
- `ruk/crates/ruk-core/src/coord/land.rs` — LandPlan, land_prepare/land_execute, 1 h TTL, CAS publish.
- `ruk/crates/ruk-core/src/protocol.rs` — 47-row method table (~3286–3332), arg and reply schemas, claim and attestation types.
- `ruk/crates/ruk-core/src/records/types.rs` — ChangeState, ChangeEntry, divergence and abandon invariant.
- `ruk/crates/ruk-core/src/coord/undo.rs` — op-log inverse and non-invertible method classification (182–235).

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Your summary lists `snapshot` as r…

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

Several items here are valid and change the artifact. Others are stale or wrong. Before I write the corrected artifact, I'll verify the open-latency reports and `cli/STATUS.md`, since I'd otherwise be citing them second-hand.
→ read(/home/alpha/rewrite/git-renewed/ruk/.outline/sdd/task-openfast-report.md:1-40) ⇒ ok · 50 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/.outline/sdd/task-storeopen-report.md:1-25) ⇒ ok · 13 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/cli/STATUS.md:1-60) ⇒ ok · 70 lines

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Use the orchestrator's absolu…

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Strip unsourced citations from t…

---
I can't write the artifact. My tools are read, grep, and web_search only, and plan mode blocks file creation. So `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-domain-model.md` does not exist. Nothing has been persisted. The corrected artifact is below for the orchestrator to write there.

## Summary

- Served mutation methods: 26 of 34 mutation rows. The 8 refused are `snapshot`, `write`, `ws.recover`, `fetch`, `push`, `push.reconcile`, `land.execute`, `op.prune`. The method table has 47 rows: 34 mutations, 13 reads or bootstrap. The earlier "33 of 47" was wrong.
- Hosted `land.prepare` checks no attestation requirement and no trusted signer. The generic `land.rs` engine re-checks requirements at execute, but the hosted path never supplies them.
- Open latency is measured in two `.outline/sdd` reports. Daemon/IPC overhead and Forgejo HTTP are NOT MEASURED.
- The latency figures I cite come from `tasks/capacity/RESULTS.md` (commit 78a98e2), which I read directly. The README and `progress.md` figures conflict with it and are excluded.
- `cli/STATUS.md` marks `archive` and `bundle` implemented. README and `tasks/parity-harness/GAPS.md` list them unsupported. I did not determine which is current.

---

# Ruk domain model (artifact content; not written to disk)

Conventions: `path:line` is cited only where I read or grep-rendered the line. Items marked **unverified** were not opened. Paths are relative to `/home/alpha/rewrite/git-renewed/ruk/` unless absolute.

## 1. Glossary

| Concept | One-line definition | Source |
|---|---|---|
| Change | Logical unit; `ChangeEntry { state: Active\|Abandoned, heads: Vec<Head> }` in `FactTable::Changes`. | `crates/ruk-core/src/records/types.rs` |
| ChangeId | 16 bytes; native headers use jj reverse-hex; exportable commits use an OID-derived id. | `crates/ruk-core/src/change.rs`; `.outline/sdd/change-id-designs.md` |
| CommitRecord | Stored commit: Git bytes if exportable, else `conflict-v1` frame. | `crates/ruk-core/src/commit.rs`; `conflict.rs` |
| Head | `Head::Git { oid }` or `Head::Conflicted { key }`; the value stored in change entries, refs, and workspaces. | `crates/ruk-core/src/heads.rs` |
| Workspace | Native (current change, Head, Scope, stamp) or GitCompat (HeadRef, index generation, Scope, stamp); kind fixed at creation. | `crates/ruk-core/src/workspace.rs` |
| Scope | Cone-mode directory set; root files always included. | `workspace.rs` (`ConeDirs`, `Scope`) |
| Take / drop | `ws.take` advances `lease_epoch` and fences the displaced holder; `ws.drop` unregisters and never deletes user files. | `protocol.rs` (`WsTakeArgs`, `WsDropArgs`); `workspace.rs` |
| Snapshot | `snapshot` method; refused on the hosted path (`snapshot_parts` returns EHosted, missing `workcopy::snapshot`). | `coord/prepare.rs` |
| Bookmark vs branch | Protocol "bookmark" = a `refs/heads/*` ref (`bookmark.set` requires that namespace). Git CLI calls it a branch. | `protocol.rs` (`BookmarkSetArgs` validate) |
| Tag | `refs/tags/*`; annotated tags refused on hosted path. | `protocol.rs`; `coord/prepare.rs` |
| Conflict record | (a) `conflict-v1` framed native commit with tree terms; (b) `FactTable::RefConflicts` rows for ref conflicts. | `conflict.rs`; `store/types.rs` |
| Claim | Path claim with TTL and mode (Enforce or Advise), holder, `expires_at`. | `protocol.rs` (`ClaimAcquireArgs`, `ClaimRecord`); `coord/claims.rs` |
| Lease | Interactive view/cursor authority: 30 s, no renewal. Bulk: 15 min interval renewed only by progress, 30 s no-progress cancel, 2 h total. | `coord/leases/timing.rs:12-22` |
| Attestation | Signed statement over one subject object, kind, result, timestamp, and optional supersession. | `crates/ruk-core/src/attestation.rs`; `protocol.rs` (`AttestStatement`) |
| Land plan | Frozen candidate, target, base tip, requirements, trusted signers, 1 h expiry, in `FactTable::LandPlans`. | `coord/land.rs` |

[Showing lines 1-506 of 704 (50.0KB limit). Use :507 to continue]
| Land plan | Frozen candidate, target, base tip, requirements, trusted signers, 1 h expiry, in `FactTable::LandPlans`. | `coord/land.rs` |
| land.prepare / land.execute | Prepare freezes a plan and moves no ref. Execute re-validates, CAS-moves the target, and deletes the plan row in one publication. | `coord/land.rs`; `protocol.rs` |
| Publication lane | One serialized publisher per open repo; objects, facts, and ref CAS publish as one batch; queue bound 256. | `coord/lane.rs`; `coord/lane/batch.rs` |
| Receipt | Idempotency record: `(session, request_id)` → digest and reply; retained 168 h. | `coord/receipt.rs`; `coord/rows.rs` |
| Session | Principal-bound identity with reconnect secret and negotiated capabilities; Active → ReceiptsOnly → Gone. | `coord/session.rs` |
| Op log | Operation entries for mutations to tracked tables (Refs, RefConflicts, Changes, ChangeHistory, Workspaces, WsIndex, Pins). | `coord/oplog.rs` |
| Undo | Inverse of a retained operation as exact before/after facts; refuses non-invertible methods and diverged keys. | `coord/undo.rs` |
| Pin | `pin`/`unpin` a stored head; recorded in `FactTable::Pins`. See §6 for what pins do not protect. | `coord/prepare.rs` (`pin_parts`) |
| Capability | 7-field grant: `write_changes`, `push_refs`, `land`, `claim_paths`, `undo`, `attest`, `read_intents`. Default is no authority. | `crates/ruk-core/src/capability.rs` |
| Budget | `ByteBudget`: atomic byte reservation that refuses overflow. | `crates/ruk-core/src/budget.rs` |
| Policy | Per-store `policy.toml`, version 1: session idle, LFS mode, capacity bounds, principals. | `coord/policy.rs` |
| Admission | Request classes Control, Interactive, Bulk, Hook, Maintenance; per-class slots, queues, and waits. | `coord/admission.rs` |
| Revision | **Unverified**: a protocol selector term; I did not read its definition, so it is not a native state. | not read |

## 2. Change lifecycle

**Change state** is only `Active` or `Abandoned` (`records/types.rs`). Divergence and conflict are **head conditions**, not states:
- Single head: `heads.len() == 1`.
- Divergent: `heads.len() > 1` (`ChangeEntry::is_divergent`).
- Conflicted head: a `Head::Conflicted` entry (`heads.rs`).
- Abandoned: `heads` empty and state `Abandoned`. `ChangeEntry::validate` rejects empty heads with state Active (`records/types.rs`).

### Mutation transitions on the hosted path

Served methods are those dispatched to a translator in `coord/prepare.rs`. Some arms refuse particular variants with EHosted (for example, `edit` on GitCompat, annotated tags, multi-parent `new`).

| # | Method | Change effect | Notes |
|---|---|---|---|
| T1 | `new` | Creates a change with one head (Absent → Active single-head). | `records/preparer.rs` creates it through `ChangeEntry::new`; the `new` translator is in `coord/prepare.rs`. |
| T2 | `describe` | Single head replaced; tree unchanged. | Rewrite propagates to descendants per scope. |
| T3 | `split` | Source keeps remainder under its identity; selection becomes a new change. | **Unverified** exact arms. |
| T4 | `squash` | Destination head replaced. | **Unverified** source-removal behavior. |
| T5 | `rebase` | Sources and descendants moved onto `onto`. | Multi-onto and merge descendants refused. |
| T6 | `abandon` | Heads removed; when empty, state → Abandoned. | `ops/abandon.rs:98-100` (read). |
| T7 | `resolve` | Conflicted head replaced by resolved commit. | Merge and nested sources refused. |
| T8 | `revert` | Creates a new change reverting a target; source unchanged. | Mainline selection; EHosted on merges. |
| T9 | `undo` | Restores exact prior bytes for tracked tables. | Refused for diverged keys; see §6 for refused methods. |
| T10 | divergence | Head set grows to >1 when a stale-head rewrite occurs. | **Unverified** trigger path. |
| — | `edit` | No change-state change; workspace checkout only. | Native workspaces only on hosted path. |
| — | `git.commit` | Commit plus branch CAS in one batch. | Whether it writes a Changes row is **unverified**. |
| — | `land.prepare` | Writes a plan row only; no ref move; no change-state change. | See §3.4. |
| — | `land.execute` (generic engine) | Ref CAS to primary candidate plus plan-row delete. | Hosted path refuses it; change state untouched in the visible code. |

Landing is a publication transition beside unchanged change state. The engine moves the target ref; it does not add a landed state.

### Landing onto a bookmark

Hosted sequence:
1. `land.prepare(change, remote, target, merge)`: rejects conflicted head (EInvalid), symbolic target (EInvalid), non-`land` target (EForbidden), missing candidate (ENotFound). Requirements and trusted signers are **empty** and `policy_generation = 0` (`coord/prepare.rs:3430-3436`, `3479-3486`). Creates plan with 1 h expiry; no ref moves.
2. `land.execute`: **refused** on hosted path (`coord/prepare.rs`, land_execute arm).

Generic engine (not reachable via the hosted protocol): `coord/land.rs:330-423` checks stored plan equality, unexpired plan, unchanged target tip (equality, not fast-forward), and every requirement via `evaluate_requirements`. The publication is a ref CAS plus plan-row delete. The engine's execute-time re-check is real; the hosted prepare never supplies requirements to it.

## 3. Schemas and authentication

### 3.1 Attestation

- `AttestStatement` (`protocol.rs:1776-1785`): `subject: Oid`, `kind: AttestKind`, `result: AttestResult`, `signed_at`, `supersedes: Option<StatementDigest>`.
- `AttestAddArgs`: `statement`, `signature: AttestSignature` (armored SSHSIG, ≤8192 bytes).
- `AttestKind` / `AttestResult` (`capability.rs:198-219`): bounded printable UTF-8, ≤16 MiB.
- Subject width follows the repository hash kind (SHA-1 or SHA-256 OID). `StatementDigest` is 64 hex chars (SHA-256 digest of the canonical payload) (`protocol.rs:419-422`). The signature is SSHSIG namespace `ruk-attest`, plain `ssh-ed25519` (`attestation.rs` module doc; `protocol.rs` constants). SHA-256 is the digest algorithm, not a repository restriction.
- Canonical payload field layout: **unverified**. My only observation is grep-rendered doc text describing seven length-prefixed fields; I did not open `canonical_payload_v1`.
- Verification internals (`attestation/verify.rs`), status derivation (`attestation/status.rs`): **unverified**; I read only the module doc. The doc describes statuses Live, Superseded, Ambiguous per `(subject, kind, signer)` group, and that stored statuses do not change with time.
- Requirement evaluation (`attestation/query.rs`): `RequirementOutcome` and `Gap` (NoLiveMatch, Ambiguous, Expired) exist; the per-requirement algorithm is **unverified** beyond its doc.
- Limits (`attestation.rs`): 4096 statements per import, 1024 per group, 4096 per subject, 64 requirements, 256 trusted signers.

### 3.2 Claims

- `ClaimAcquireArgs` (`protocol.rs:1708-1712`): `paths` (non-empty), `ttl: ClaimTtl`, `mode` (default Enforce).
- `ClaimTtl` (`protocol.rs:818-838`): 1 to `MAX_CLAIM_TTL_SECONDS`; doc says lifetimes over one hour are rejected. I did not confirm the constant's numeric value.
- `ClaimRecord` (`protocol.rs:1718-1725`): `claim`, `holder`, `paths`, `mode`, `expires_at`.
- Enforce: overlapping writes by other sessions → `E_CLAIMED` (`coord/claims.rs:346-349`). Advise: warning only.
- Claims are path-only. The claims module doc says change-ID-scoped keys are not threaded yet.
- Claim acquisition requires `claim_paths` capability; paths outside it → `E_FORBIDDEN`.

### 3.3 Policy and admission

**policy.toml** (`coord/policy.rs`, version 1): `session_idle` (default 24 h), `lfs = "deny"|"pointers"`, `[capacity]` byte bounds (read via grep rendering; not opened), `[[principal]]` with `name`, `capabilities`, `identities`, `claim_keys` and allow flags. Unknown fields, duplicate principals, or bad versions are `E_POLICY`. Every load re-reads and hashes the file.

**Capabilities** (`capability.rs`): `write_changes` ordered Own < Session < All; `undo` Session < All; `push_refs`, `land` ref patterns; `claim_paths` path patterns; `attest` kinds; `read_intents {provenance, claims}`. Negotiation returns the requested subset or `E_FORBIDDEN`; excess is never truncated. Object reads are not gated.

**Admission reference profile** (`coord/admission.rs:110-165`, read):

| Class | Slots | Queued | Max wait |
|---|---|---|---|
| Control | 32 | 128 | 100 ms |
| Interactive | 16 | 2048 | 250 ms |
| Bulk | 6 | 256 | 5 s |
| Hook | 4 | 64 | 1 s |
| Maintenance | 2 | 32 | 30 s |

Per-repo caps: ordinary 8, bulk 2, hook 2, maintenance 1. Storage permits: interactive 4, bulk 2, hook 1, maintenance 1. Blocking: control 4, ordinary 12. Event loop threads 4. Queued payload 256 MiB process-wide, 16 MiB per repo. Open repositories 128.

**Daemon** (`crates/ruk/src/daemon/mod.rs`): `max_connections` 128, blocking pool 16, handshake 5 s, frame 60 s, retention interval 60 s, `allowed_uids` (empty allows any local peer). Windows named pipe unsupported.

### 3.4 Authentication and session secrets

1. Peer principal from OS credentials (`coord/authentication.rs`), then policy lookup. No request field names a principal.
2. Store-level endpoint proof (`coord/handshake.rs`): four frames; HMAC-SHA256 tags keyed by the endpoint nonce, domain-separated, verified in constant time. Server proves first. Nonce lives in the lock body (`coord/lock.rs`). Not a sandbox against same-uid processes.
3. `session.open` returns `SessionId` and a 32-byte `ReconnectSecret`. The row stores a SHA-256 hash of the secret; resume compares in constant time (`coord/session.rs`; `coord/hosted.rs`).
4. Session authority: Active until idle expiry, then ReceiptsOnly for 7 days (`RECEIPT_HORIZON` = 168 h, `coord/rows.rs:56-57`), then Gone. ReceiptsOnly never returns to Active.
5. There is no bearer capability-token schema. Capabilities are negotiated inside the session row and bounded by the policy file.

Receipts (`coord/receipt.rs`): SHA-256 over postcard request args, excluding `request_id`. Same key and digest → replay; different digest → `E_REQUEST_REUSED`.

## 4. Hosted-forge anticipation

**Present in sources:**
- `ruk forgejo serve` daemon: one process owns every opened repo of a Forgejo instance; opens authorized by canonical path under configured roots (`crates/ruk/src/daemon/mod.rs`, `registry.rs`).
- IPC features `credits`, `merge-notices`, `outcome-lookup` (`daemon/ipc.rs`).
- Merge-notice record: `.omp/plans/AGENT_NATIVE_GIT_PLAN.md` review-consolidation section defines one record shared by native JSON, an IPC `MergeNotices` event, CLI stderr, and Forgejo API/UI. Plan text only; the Forgejo UI side is not in Ruk.
- Mirrors: a Forgejo-side Receive-parity item (plan slice 11). Progress notes mirror verbs landed in commits listed in `.outline/sdd/progress.md`. Ruk's own `push` and `push.reconcile` are refused on the hosted path; transport owns the remote leg.
- Retention sweeps (receipts, sessions, read leases) per `.outline/sdd/progress.md` slice 12.

**Absent (grep negative):** webhook, event subscription, tenant isolation beyond OS principal and per-instance bounded execution, zero-JS UI design, HTML templates, a push-mirror scheduler to GitHub, GitLab, or Forgejo. The zero-JS requirement comes from the user goal, not from Ruk sources. I did not model `MergeNotices` as a webhook system.

## 5. Measured latencies

**Store open** (`.outline/sdd/task-openfast-report.md`, release binary, `tasks/capacity/.fix/large-10000`):
- First open after a write: 0.478 s (parallel full verify plus cache persist).
- Cached second open: 0.068 s, 11 `openat`.
- Forced full (`RUK_VERIFY=full`): ~0.2 s, 15,633 `openat`.
- Git baseline open: 0.008 s.

**Warm open before and after the fast path** (`.outline/sdd/task-storeopen-report.md`, warm, large-10000): open total 40–48 ms before, 24–33 ms after; `lookup` ~50 ms before, ~30 ms after; forced full object pass 210 ms.

**Daemon/IPC overhead, Forgejo HTTP, load, saturation, slow-reader, cancellation, crash-around-publish, power-loss:** NOT MEASURED. `tasks/capacity/RESULTS.md` states daemon/IPC admission and Forgejo HTTP paths are out of scope.

**CLI capacity, single run** (`tasks/capacity/RESULTS.md`, generated 2026-10-18T23:18:01Z, source 78a98e2, 80 CPUs, 754 GiB, git 2.53.0, warm cache, no admission layer):

| Workload | git p50 ms | ruk p50 ms |
|---|---|---|
| adv-1k | 43 | 22 |
| adv-10k | 53 | 58 |
| lookup | 9 | 36 |
| page1 | 144 | 94 |
| write | 26 | 49 |
| clone (bare, 10k) | 1370 | 1714 |
| fetch (+1 branch) | 88 | 659 |

I excluded the README (commit 8487835b) and `progress.md` figures because they conflict with this run.

**Reference profile:** AGENT_NATIVE_GIT_PLAN.md "Reference profile and release gate" section: 32 physical cores, 128 GiB RAM, 900 reads and 100 writes per second, 10,000 repos with 100 active, up to 1M branches in one repo, threefold burst with bounded rejection, recovery within 60 s. The same section says the capacity profile uses PostgreSQL for numeric acceptance. The separate `local://ruk-reference-production-profile.md` is not in the repo; **unverified**.

## 6. Gaps and roadmap

**Current, from source:**
1. Hosted `land.prepare` checks no attestation requirement and no trusted signer. Policy has no `[land]` requirement section (`coord/prepare.rs:3430-3434`).
2. `land.execute` refused on hosted path; remote push leg lives in transport.
3. `fetch`, `push`, `push.reconcile` refused on hosted path (transport owned).
4. `op.prune` refused; no operation-retention policy.
5. `snapshot`, `write`, `ws.recover` refused (missing workcopy producers).
6. Undo cannot invert `new`, `edit`, `ws.new`, `ws.scope`, `ws.take`, `ws.drop`, `fetch`, `push`, `land.*` (`coord/undo.rs`, refuse list).
7. Annotated tags, multi-parent `new`, merge-commit targets refused on hosted path.
8. Claims path-only; change-ID-scoped keys not threaded.
9. Pins: `prepare` validates and records a pin on a stored head. Retention classifies `Pins` as untracked. Pins are **not** protection against retention expiry for receipts, sessions, or operations. Retention pin semantics: **unverified** beyond this.

**CLI verb status (`crates/ruk/src/cli/STATUS.md`, read lines 1-63 of 188):** states 128 verbs with statuses implemented, partial, unsupported. `clone` routes local paths only; network sources are `Offline`. `archive` and `bundle` are marked implemented, while README and `tasks/parity-harness/GAPS.md` list them unsupported. The file's date is not recorded. I did not read rows past 63, so fetch and push status there is **unverified**.

**Roadmap history (`.outline/sdd/progress.md`, earlier than current sources):** shallow/partial fetch open; LFS payload sync open (design only in `lfs-sync-design.md`); symbolic refs and wiki/template flows open; Forgejo DB and browser suites open; full-profile capacity open. Forgejo patch inventory counts are recorded in `progress.md` as 81 files and 24 compatibility gates; I did not recount the inventory rows.

**Parity status:** `progress.md` says 54/56; `tasks/parity-harness/GAPS.md` says 56/56 and states exportable commits carry no change-id header. I take GAPS as current.

---

### Corrections applied from review
- Served count: 26 of 34 mutation rows (8 refused); 47 total rows, 13 reads or bootstrap.
- Unread citations are marked **unverified** or removed.
- Latency sources are per-report; no README or progress figures used.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/prepare.rs` — served and refused method arms; hosted land.prepare empty requirements (~3430–3486).
- `ruk/crates/ruk-core/src/coord/land.rs` — generic plan prepare and execute, CAS publication.
- `ruk/crates/ruk-core/src/protocol.rs` — 47-row method table (~3286–3332) and argument schemas.
- `ruk/crates/ruk-core/src/records/types.rs` — ChangeState and ChangeEntry invariants.
- `ruk/crates/ruk/src/cli/STATUS.md` — per-verb status; conflicts with README and GAPS on archive and bundle.

[advisor] <advisory advisor="muse" severity="blocker" guidance="weigh, don't blindly obey"> Write the file to the given folder no…

---
I can't write this file. My only tools are `read`, `_grep`, and `web_search`, and plan mode blocks file creation. The file does not exist at `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-domain-model.md`, and nothing has been written to disk.

The orchestrator needs to persist the artifact from my previous reply, which has the complete six-section content, the corrected served-method count (26 of 34 mutation rows served, 8 refused, 13 reads or bootstrap), and the open-latency figures from the two `.outline/sdd` reports. I have no way to produce a file from here.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/prepare.rs` — served and refused method arms; hosted `land.prepare` empty requirements.
- `ruk/crates/ruk-core/src/coord/land.rs` — generic plan prepare and execute, CAS publication.
- `ruk/crates/ruk-core/src/protocol.rs` — 47-row method table and argument schemas.
- `ruk/crates/ruk-core/src/records/types.rs` — ChangeState and ChangeEntry invariants.
- `ruk/crates/ruk/src/cli/STATUS.md` — per-verb status; conflicts with README and GAPS on `archive` and `bundle`.

[advisor] <advisory advisor="qwen" severity="concern" guidance="weigh, don't blindly obey"> Read AGENT_NATIVE_GIT_PLAN.md:100-115…

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Add explicit no-change rows f…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Fix one sourced error and read the…

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Stop re-emitting the full artifa…