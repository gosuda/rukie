I could not create the artifact: this subagent session has only `read`, `grep`, and `web_search` — no write or shell tool. I verified `local://` lists files but cannot write. The complete artifact content is therefore inline below (paste into `.../local/ruk-backend-surface.md`).

---

# ruk-backend-surface.md

## 0. Scope and status

Read-only investigation of Ruk's typed v1 protocol and hosted daemon as a backend for the new Go forge. Sources: `/tmp/ruk-model.json`; `ruk/crates/ruk-core/src/protocol.rs`; `ruk/crates/ruk/src/daemon/{mod,ipc,registry,executor}.rs`; `ruk/crates/ruk-core/src/coord/{hosted,session,land,undo}.rs`; `ruk/crates/ruk/src/mcp/{mod,tools,server}.rs`; `ruk/crates/ruk/src/cli/STATUS.md`; `ruk/crates/ruk-core/src/revset/syntax.rs`.

**Headline for the forge:** the protocol is an *operation* API, not a *hosting* API. It has no repository lifecycle, no ref listing, and no commit-graph read path served by the daemon. Reads `status|log|diff|show|file|blame|query` are declared in the schema but the hosted coordinator refuses all of them with `E_HOSTED` (`coord/hosted.rs:1346-1360`). A forge must either (a) implement those readers over the store, or (b) use the `CommandOpen` git-shim lane (`git log`, `git cat-file`, `git ls-tree`, …) which is fully implemented in the CLI layer.

---

## 1. Daemon wire

### 1.1 Transport and framing

Unix domain socket, `socket_path` from TOML. One frame:

```
u32 BE length | kind u8 | stream_id u64 BE | payload
```
Length counts the 9-byte header + payload (`ipc.rs:FRAME_PREFIX_BYTES=4`, `FRAME_HEADER_BYTES=9`, `encode_frame`).

| Bound | Bytes | Source |
|---|---|---|
| data frame (Stdin/Stdout/Stderr) | 65 536 | `ipc.rs` `MAX_DATA_CHUNK_BYTES` |
| IPC control frame (all JSON) | 1 048 576 | `ipc.rs` `MAX_IPC_CONTROL_BYTES` |
| protocol control frame (schema/typed frames) | 16 777 216 | `lib.rs:18` `MAX_CONTROL_FRAME_BYTES`; model `limits.control_frame_bytes` |
| inline write edit / inline bytes | 1 048 576 | `protocol.rs` `MAX_INLINE_EDIT_BYTES` |

Frame kinds (`ipc.rs:48-107`): `Open=1, Accepted=2, Stdin=3, StdinEof=4, Stdout=5, Stderr=6, WindowUpdate=7, Cancel=8, MergeNotices=9, Exit=10, Error=11`. Unknown kind → `E_INVALID`.

Flow control: per-direction, per-stream credit windows. Initial `INITIAL_STREAM_WINDOW_BYTES = 256 KiB`, cap `MAX_STREAM_WINDOW_BYTES = 16 MiB` (`ipc.rs:33-39`). `WindowUpdate` payload `WindowCredit{credit_bytes (alias "bytes"), direction: ""|"stdin"|"stdout"|"stderr"}`; empty/`stdin` grants inbound, `stdout`/`stderr` grant outbound (`ipc.rs:340-348`, doc). Credit exhaustion → `E_LIMIT`; unknown stream → `E_INVALID`. `CommandAccepted.stdin_credit` and `InvocationOpen` streams start at the configured window.

### 1.2 Stream-zero handshake

1. Peer authenticated first: `SO_PEERCRED`; uid must be in `allowed_uids` (empty list ⇒ every local peer) else `E_FORBIDDEN`; missing creds → `E_IO` (`ipc.rs:authenticate_peer`; `mod.rs:784`).
2. Client sends `Open` on stream 0 with `HandshakeHello` (`ipc.rs:432-445`):

```json
{"protocol_major":1,"protocol_minor":0,"profile":"forgejo",
 "object_formats":["sha1"],"features":["credits","merge-notices","outcome-lookup"]}
```
`protocol_minor` defaults to 0 (the Go client omits it).
3. Daemon replies `Accepted` on stream 0 with `HandshakeAccept`:

```json
{"protocol_major":1,"protocol_minor":1,"profile":"forgejo",
 "object_formats":["sha1"],"features":["credits"]}
```
`protocol_minor = min(SCHEMA_MINOR=1, hello.protocol_minor)`; `features` = intersection with `SUPPORTED_FEATURES = ["credits","merge-notices","outcome-lookup"]`. `E_VERSION` on major mismatch; `E_INVALID` on empty `profile` or empty `object_formats` (`negotiate`, `ipc.rs`).
Handshake deadline: `handshake_timeout_ms` (default 5 000). If the first frame is not `Open` on stream 0 → `E_INVALID` (`mod.rs:786-791`). After the handshake each frame gets `frame_timeout_ms` (default 60 000).

### 1.3 Typed invocation streams (`InvocationOpen`)

`Open` payload (`mod.rs:304-320`):

| field | type | required | meaning |
|---|---|---|---|
| `repo` | string | yes | absolute path (starts `/`) **or** opaque handle from a previous open (`repo-N`) |
| `method` | string | yes | protocol method name, e.g. `status` |
| `request_id` | string? | no | idempotency key for mutations |
| `args_json` | string? | no | raw JSON document of the method arguments |
| `session_id` | string? | no | coordinator session (typed clients; git command streams never send it) |

`Accepted` (`mod.rs:323-331`): `{stream_id: u64, method: string, repo_handle: string}`.
Bulk bytes: a read reply's `ByteSource::streamed{stream, size}` opens a `Stdout` byte stream; large writes use `Content::staged{lease, oid, size}`.
`Exit` payload `ExitReport` (`mod.rs:351-362`): `{status: i32, code: string, outcome: "Completed"|"CancelledBeforeCommit", completed_at: u64}`.
`Error` payload `ErrorReport` (`mod.rs:364-370`): `{code: string, message: string}`.
`session.open` reply is `SessionOpened` (`mod.rs:598-608`): `{session, reconnect_secret, principal}` — note it deliberately omits `capabilities`/`idle_expires_at`, unlike the protocol `SessionOpenReply` (`mod.rs:590-597`). `session.resume` returns the full `ReplyEnvelope` with `SessionResumeReply{session, authority, capabilities}` (`mod.rs:2156-2170`).

Stream-zero control queries after the handshake (`ControlRequest`, `mod.rs:476-500`):

```json
{"type":"outcome-lookup","invocation_id":"..."}
→ Accepted on stream 0: {"type":"outcome-lookup","found":true,"exit":{...CommandExitReport}}
```
Any other `type` → `Error` frame on stream 0 with `E_INVALID`. `Cancel` on stream 0 cancels every stream; on a stream it cancels that stream.

### 1.4 Git-shaped command streams (`CommandOpen`)

`Open` payload (`mod.rs:389-416`):

| field | type | required | meaning |
|---|---|---|---|
| `session_id` | string | yes | accepted and ignored (reserved) |
| `repository` | string | yes | authorized repo path, canonicalized under roots |
| `request_id` | string | yes | idempotency key, replayed on reconnect |
| `argv` | string[] | yes | args **without** `argv[0]`, e.g. `["rev-parse","--git-dir"]` |
| `dir` | string | yes | working directory (must resolve under a root) |
| `env` | string[] | yes | `KEY=value`; the command sees **only** these (hermetic) |
| `command_digest` | string | yes | digest over argv+dir+env; reused id with different digest → `E_INVALID` |
| `stdout_credit` | u64 | yes | extra initial outbound credit |
| `stderr_credit` | u64 | yes | extra initial outbound credit |

Daemon prepends `"git"` and routes to the bundled git-compatibility CLI: `crate::cli::run_captured(full_argv, CliEnv{vars, cwd})` (`mod.rs:1363-1372`). Therefore **every implemented git verb** is reachable: `upload-pack`, `receive-pack`, `upload-archive`, `rev-parse`, `cat-file`, `ls-tree`, `log`, `show`, `diff`, `blame`, `grep`, `archive`, `merge-base`, `ls-remote`, `fetch`, `push`, `clone`, `fast-export`, `fast-import`, `index-pack`, `pack-objects`, `for-each-ref`, `show-ref`, … (`cli/STATUS.md:46-145`, `cli/dispatch.rs`). Unimplemented verbs return the typed `Unsupported` error and exit 1 (`cli/STATUS.md:4-6`). `serve_argv` also accepts dashed argv[0] (`git-upload-pack`) for ssh-style invocation (`cli/mod.rs:263-304`).

`CommandAccepted` (`mod.rs:417-428`): `{stream_id, stdin_credit, invocation_id}`.
`CommandExitReport` (`mod.rs:429-443`): `{exit_code: i32, signal: string, cancelled: bool, outcome: string, operation_ids: string[], invocation_id: string}`.
Stdin is buffered to `MAX_COMMAND_STDIN_BYTES = 256 MiB` (`mod.rs:377`). A long-running command blocks sibling streams on the same connection (`mod.rs:1195-1199`).

Auth/hooks for `receive-pack`: there is **no protocol-level auth or hook channel**. Authorization is (a) socket peer uid and (b) `dir`/`repository` confinement under `repo_roots`; the git command itself is executed with a hermetic env. Server-side `pre-receive`/`post-receive` hooks are **not** documented as run by `receive-pack` in `cli/STATUS.md:120` (only `pre-commit`/`commit-msg`/`post-commit`/`post-merge`/`pre-push` are listed for client-side verbs). Per-ref accept/reject policy would have to be built in the Go forge layer (e.g. by exporting then applying through typed `push`/`land`), or added to Ruk. `unverified` whether any hook plumbing exists in `verbs_transport::receive_pack`.

### 1.5 Repository addressing and registry rules

- `repo` starting with `/` = absolute path; anything else = opaque handle (`registry.rs:173-201`).
- Absolute paths are canonicalized and must `starts_with` a configured root; otherwise `E_FORBIDDEN`; missing → `E_NOT_FOUND`; inaccessible → `E_IO` (`registry.rs:147-171`).
- Handles are `repo-{n}` monotonic, one per canonical path, reused; unknown handle → `E_NOT_FOUND`; handle path escaping roots → `E_FORBIDDEN` (`registry.rs:309-354`).
- **No create / init / delete / rename / fork.** The registry only resolves existing paths and opens `Store::open`. `Store::create` exists but is called only by tests/CLI (`registry.rs:600`, not reachable through IPC). A forge must create repositories itself (filesystem + `ruk` CLI) or extend Ruk.
- One `HostedCoordinator` per repo, opened lazily; its lane runner and retention task spawn once on the daemon runtime; idle eviction and `shutdown_all` retire it (`registry.rs:203-255`, `356-395`).
- Repository owner principal = on-disk uid of the repo root (Unix only; non-Unix → `E_HOSTED`) (`registry.rs:257-293`).

### 1.6 TOML config and concurrency

Keys (`mod.rs:166-215`): `socket_path` (required), `repo_roots` (required, non-empty), `allowed_uids` (default `[]`), `max_connections` (default 128), `blocking_pool_size` (default 16), `handshake_timeout_ms` (5 000), `frame_timeout_ms` (60 000), `stream_window_bytes` (256 KiB, must be 1..=16 MiB), `retention_interval_ms` (60 000; 0 disables).

Limits: `max_connections` sheds accepts; one shared `BoundedExecutor` semaphore of `blocking_pool_size` permits for all repos (`executor.rs`); Tokio runtime `worker_threads(4)`, `max_blocking_threads = blocking_pool_size*2` (`mod.rs:662-665`). Per-repo live sessions capped at `MAX_LIVE_SESSIONS = 128` (`coord/session.rs:31`). Per-daemon outcome ledger capped at `MAX_OUTCOME_RECORDS = 10_000` (`mod.rs` ~377).

---

## 2. Every method (47, model `methods[]`)

Kinds: `anonymous` (1), `bootstrap` (2), `read` (10), `mutation` (34). Paging (`protocol.rs`): `PageRequest{limit?: 1..=1000 (default 100), cursor?: PageCursor{store, view, query, lease, position}}`; `Page<T>{items, next?}`; `BoundedList<T>{total, items, truncated}` — mutations return bounded lists, never cursors. Every mutation is `Mutation<T>{request_id, args:T}` and every mutation reply begins with `MutationOutcome{receipt{session,request_id,digest,replayed,retained_until}, heads, stamps, warnings, provenance, operation?, identity?, merge?}`. Cursor is bound to store/view/query/lease and stops working when the lease expires; limit >1000 is rejected, not clamped.

### 2.1 Anonymous / bootstrap

| method | request | reply |
|---|---|---|
| `schema` | `SchemaArgs{method?: Method}` (none = whole model) | `SchemaReply{schema_version, methods: MethodDescriptor[], document: Schema}` |
| `session.open` | `SessionOpenArgs{capabilities?: Capabilities, claims: ProvenanceClaims, identity?: IdentityClaim}` | protocol: `SessionOpenReply{session, reconnect_secret, principal, capabilities, idle_expires_at}`; daemon sends `SessionOpened{session, reconnect_secret, principal}` (`mod.rs:598`) |
| `session.resume` | `SessionResumeArgs{session, reconnect_secret}` | `SessionResumeReply{session, authority: active{idle_expires_at}\|receipts_only{receipts_until}, capabilities}` |

`session.open` names no principal; identity comes from peer credentials.

### 2.2 Reads (10) — daemon serves only 3

| method | request fields | reply | daemon |
|---|---|---|---|
| `op.log` | `page?: PageRequest, session?: SessionId` | `OpLogReply{view, entries: Page<OperationEntry{id, method, parent?, session?, undoes?, committed_at}>, oldest_retained?}` | **served** (lease-bound paging) |
| `claim.list` | `holder?: ClaimHolderFilter("mine"\|"any"), page?` | `ClaimListReply{view, claims: Page<ClaimRecord{claim, holder, paths, mode, expires_at}>}` | **served** |
| `attest.list` | `kind?: AttestKind, signer?: SignerFingerprint, subject?: Oid, include_superseded?: bool (default false), page?` | `AttestListReply{view, records: Page<AttestationRecord{subject, kind, result, signer, payload_digest, signed_at, status, supersedes?}>}` | **served** |
| `status` | `workspace?: WorkspaceId, snapshot?: "auto"\|"skip", page?` | `StatusReply{view, workspace?: WorkspaceState, change?: ChangeHead, applying: bool, snapshot_operation?, paths: Page<PathStatus{path, change, conflicted}>}` | `E_HOSTED` (`hosted.rs:1346`) |
| `log` | `revset: RevsetText (1..=65536 B), page?` | `LogReply{view, entries: Page<CommitSummary{head, parents[], author, committer, subject, change?, conflicted, divergent}>}` | `E_HOSTED` |
| `query` | `revset, page?` | `QueryReply{view, matches: Page<QueryMatch{change, heads[]}>}` | `E_HOSTED` |
| `diff` | `left: RevisionRef, right: RevisionRef, paths: PathSelection("all"\|{paths[]}), page?` | `DiffReply{view, files: Page<FileDiff{path, old_path?, kind: added\|deleted\|modified\|renamed, conflicted, old?: DiffBlob{oid,executable}, new?: DiffBlob, body: "binary"\|unified{patch: ByteSource}}>}` | `E_HOSTED` |
| `show` | `target: {head}\|{change}\|{object}` | `ShowReply{view, body: {commit{head, change?, record: CommitRecord{kind,change_id,encoded,predecessor?,provenance?}}}\|{object{oid, kind, size, content: ByteSource}}}` | `E_HOSTED` |
| `file` | `at: RevisionRef{head}\|{change}\|{workspace}, path?: RepoPath, page?` | `FileReply{view, body: {blob{oid,executable,size,content}}\|{symlink{target}}\|{gitlink{commit}}\|{directory{entries: Page<TreeEntry{name,oid,kind}>}}\|{conflict{terms}}}` | `E_HOSTED` |
| `blame` | `at: RevisionRef, path: RepoPath, lines?: LineRange{start,end}, page?` | `BlameReply{view, hunks: Page<BlameHunk{start_line, line_count, head, original_path, author, change?}>}` | `E_HOSTED` |

**Important:** `log` has **no path filter parameter**; path scoping is only via the revset `files(glob)` function (`revset/syntax.rs:20`). `diff` is a two-revision compare and produces either a **raw unified patch** (`unified.patch`) or `"binary"` — **no structured hunks**. A forge wanting rendered diffs must parse the patch or call `git diff` outputs.

### 2.3 Mutations (34)

Workspace / working copy:

| method | request | reply |
|---|---|---|
| `ws.new` | `root: ByteString, init: {native{parents[]}}\|{git_compat{head: HeadRef}}, scope?: Scope("full"\|{cone})` | `WsNewReply{outcome, workspace, state}` |
| `ws.scope` | `binding: WorkspaceBinding{workspace, stamp{sequence,lease_epoch}}, scope: Scope` | `WsScopeReply{outcome, state}` |
| `ws.take` | `workspace: WorkspaceId, expected?: WorkspaceStamp` | `WsTakeReply{outcome, state}` (fences holder by advancing lease epoch) |
| `ws.drop` | `binding` | `WsDropReply{outcome}` |
| `ws.recover` | `binding, action: "resume"\|"abort"\|{discard{paths[]}}` | `WsRecoverReply{outcome, state, paths: BoundedList<RecoverPath>}` |
| `snapshot` | `binding` | `SnapshotReply{outcome, change: ChangeHead, changed_paths: u64, state}` |
| `write` | `binding, edits: WriteEdit[{path, action: {put{content,executable?}}\|{symlink{target}}\|"delete"}]`; `Content = {inline{data}}\|{staged{object{lease,oid,size}}}` | `WriteReply{outcome, change, applied: u64, state}` |
| `edit` | `binding, target: ChangeRef{change, head?}` | `EditReply{outcome, state}` |

Change rewriting:

| method | request | reply |
|---|---|---|
| `new` | `parents: Head[], description: ByteString, identity?: IdentityClaim, workspace?: WorkspaceBinding` | `NewReply{outcome, created: ChangeHead, state?}` |
| `git.commit` | `tree: Oid, parents: Head[], author: Signature, committer: Signature, message: ByteString, change_id?: ChangeId, predecessor?: Head, ref_update?: GitCommitRefUpdate{ref, expected, target}` | `NewReply{outcome, created, state?}` (model result `NewReply`) |
| `describe` | `target: ChangeRef, description: ByteString, identity?, workspaces[]` | `RewriteReply{outcome, rewrites: BoundedList<HeadRewrite{change,old,new}>, abandoned, conflicted}` |
| `split` | `source: ChangeRef, paths: PathSelection, placement: "selected_first"\|"selected_second", description, identity?, merge?, workspaces[]` | `RewriteReply` |
| `squash` | `source, destination: ChangeRef, paths, description?, identity?, keep_empty?: bool, merge?, workspaces[]` | `RewriteReply` |
| `rebase` | `sources: ChangeRef[], scope: "source_and_descendants"\|"revisions_only", onto: Head[], keep_empty?, merge?, workspaces[]` | `RewriteReply` |
| `abandon` | `targets: ChangeRef[], merge?, workspaces[]` | `RewriteReply` |
| `resolve` | `target: ChangeRef, path: RepoPath, resolution: {supplied{content,executable?}}\|{side{index}}\|"accept_same", workspaces[]` | `ResolveReply{outcome, change: ChangeHead, remaining: BoundedList<RemainingConflict>}` |
| `revert` | `target: ChangeRef, onto: Head[], mainline?: u32, description?, identity?, merge?` | `RevertReply{outcome, created: ChangeHead}` |

Refs:

| method | request | reply |
|---|---|---|
| `bookmark.set` | `name: RefName (full refs/heads/…), target: {commit{oid}}\|{change{change,head?}}, expected: RefExpect("any" refused; "absent"\|{present{...}})` | `BookmarkSetReply{outcome, name, target: Oid}` |
| `bookmark.delete` | `name, expected` (`present` required) | `{outcome}` |
| `bookmark.resolve` | `name, expected_terms, resolution: "delete"\|{set{oid}}` | `{outcome}` |
| `tag.set` | `name: full refs/tags/…, target: Oid, expected, annotation?: {message, tagger?: IdentityClaim}` | `TagSetReply{outcome, name, target: RefTarget}` |
| `tag.delete` | `name, expected` (`present` required) | `{outcome}` |

Claims / attestations:

| method | request | reply |
|---|---|---|
| `claim.acquire` | `paths: RepoPath[], ttl: 1..=3600 s, mode?: "enforce"\|"advise"` | `ClaimAcquireReply{outcome, claim: ClaimRecord}` |
| `claim.release` | `claim: ClaimId` | `ClaimReleaseReply{outcome, released: ClaimRecord}` |
| `attest.add` | `statement: AttestStatement{subject: Oid, kind: AttestKind, result: AttestResult, signed_at, supersedes?}, signature: AttestSignature` (armored SSH, namespace `ruk-attest`, ed25519, ≤8192 B) | `AttestAddReply{outcome, record: AttestationRecord}` |

Transport / publication:

| method | request | reply |
|---|---|---|
| `fetch` | `remote: {named{name}}\|{url{url}}, refs: "all_branches"\|{patterns: RefPattern[]}, filter?: "full"\|"blob_none" (default blob_none), depth?: u32?` | `FetchReply{outcome, updates: BoundedList<RefDelta>, conflicts: BoundedList<RefConflictNote>, shallow: bool}` |
| `push` | `remote, updates: PushUpdate[{remote_ref, source: "delete"\|{commit{oid}}\|{change{change,head?}}, force?: bool, expected_old?: "absent"\|{present{oid}}}], atomicity?: "atomic"\|"non_atomic"` | `PushReply{outcome, plan: PlanId, state: prepared\|sending\|confirmed\|rejected\|uncertain, refs: BoundedList<PushRefOutcome{remote_ref,status}>}`; failures carry `ErrorDetail::push{plan,state,refs}` |
| `push.reconcile` | `plan: PlanId` | `PushReconcileReply{outcome, plan, state, observations: BoundedList<ReconcileObservation>}` |
| `land.prepare` | `change: ChangeRef, remote: RemoteSpec, target: RefName, merge?: MergeSelection` | `LandPrepareReply{outcome, plan, candidate: Oid, base: Oid, expires_at, policy_generation: u64, requirements: AttestRequirement[]}` |
| `land.execute` | `plan: PlanId, candidate: Oid` | `LandExecuteReply{outcome, plan, remote: PushRefOutcome, local: LandLocalState}`; candidate mismatch → `E_STALE` |

Operation log / pinning:

| method | request | reply |
|---|---|---|
| `undo` | `operation: OperationId` | `UndoReply{outcome, undone, restored: UndoSummary}`; non-undoable method → `E_UNDO_CONFLICT` (`coord/undo.rs:182-239`) |
| `op.prune` | `{}` | `OpPruneReply{outcome, pruned: u64, oldest_retained?}` |
| `pin` / `unpin` | `head: Head` | `{outcome}` |

### 2.4 Error codes (model `error_codes`, 30)

`E_STALE` 3, `E_LEASE_LOST` 3, `E_REQUEST_REUSED` 3, `E_CONFLICTED` 2, `E_DIVERGENT` 2, `E_OBJECT_FORMAT` 1, `E_REMOTE_MOVED` 4, `E_PUSH_REJECTED` 4, `E_PUSH_UNCERTAIN` 4, `E_OFFLINE` 4, `E_UNDO_CONFLICT` 2, `E_CLAIMED` 3, `E_FORBIDDEN` 3, `E_POLICY` 3, `E_UNSUPPORTED_LFS` 1, `E_UNSUPPORTED_GIT_COMMAND` 1, `E_COORDINATOR_UNRESPONSIVE` 1, `E_CAPACITY` 1, `E_CORRUPT` 5, `E_NOT_FOUND` 1, `E_INVALID` 1, `E_IO` 1, `E_LIMIT` 1, `E_VERSION` 1, `E_DIRTY` 2, `E_WORKSPACE_APPLYING` 2, `E_WORKSPACE_RECOVERY` 2, `E_CANCELLED` 1, `E_SHALLOW` 4, `E_HOSTED` 1.

`ErrorReply{code, message, hints[], fixes: Fix[], detail?: ErrorDetail}`. Fixes are machine-readable: `refresh_expected{workspace?}`, `take_workspace{workspace}`, `"new_request_id"`, `recover_workspace{workspace}`, `reconcile_push{plan}`, `select_head{change,heads}`, `resolve_conflicts{change}`, `"restart_query"`, `"prepare_land_again"`, `"fetch_objects"`, `wait_until{until}`.

### 2.5 Capabilities, limits, revset

`Capabilities` (`$defs.Capabilities`): `attest: AttestKind[]`, `claim_paths: PathPattern[]`, `land: RefPattern[]`, `push_refs: RefPattern[]`, `read_intents: {claims: bool, provenance: bool}`, `undo: UndoScope|null`, `write_changes: WriteScope|null` (`null` = read-only). Limits: page default 100 / max 1000; inline edit 1 MiB; claim TTL ≤3600 s; control frame 16 MiB; revset ≤64 KiB; cursor position ≤4096 B; JSON nesting ≤128 (`MAX_JSON_NESTING`).

Revset grammar (`revset/syntax.rs:1-45`): atoms `@`, `@-`, symbol/branch/tag/commit/change name, quoted string, functions `heads, roots, ancestors, descendants, bookmarks([pat]), remote_bookmarks([pat]), tags([pat]), conflicted(), divergent(), mine(), session(id), change(prefix), description(text), files(glob), attested(kind,result)`; ranges `x::y` (DAG) and `x..y`; unary `~`; `&` and `~`; union `|`. Chained ranges rejected; depth ≤128; nodes ≤65 536. Cursor is an opaque offset `ruk-revset-v1:<n>` valid only against an unchanged graph (`revset/eval.rs:124,257`).

---

## 3. Gap table for a Go forge

| Need | Status |
|---|---|
| List repositories | **GAP.** Registry has no enumeration RPC; it only resolves paths/handles (`registry.rs:173-201`). Forge DB owns the list. |
| Create / init repo | **GAP.** No protocol method; registry never creates (`registry.rs:203-255`). `Store::create` exists (`registry.rs:600`, test only). Use CLI `git init` / direct store API. |
| Delete / rename / fork repo | **GAP.** Nothing in `protocol.rs` method table or registry. |
| List refs / bookmarks / tags with pagination | **GAP** in protocol: no `refs.list`/`bookmark.list`/`tag.list` (method table has only set/delete/resolve). Workaround: `CommandOpen` `for-each-ref` / `show-ref` (`cli/STATUS.md:79,145`), or MCP `refs_list` (local-only, cursor binding not enforced — `mcp/tools.rs:57-59`). |
| Read directory listing at revision | **Covered by `file`** with a directory `path` → `directory.entries: Page<TreeEntry>`; root = omit `path`. Daemon: `E_HOSTED` (`hosted.rs:1357`). CLI fallback `ls-tree`. |
| Read blob (size, binary detection) | **Covered by `file`** `{blob{oid, executable, size, content: ByteSource}}`; no binary flag — detect from bytes/`DiffBody::binary` only for diffs. Daemon: `E_HOSTED`. CLI `cat-file`. |
| Commit log, path filter, pagination | **`log` covers pagination via `PageRequest`; path filter only via revset `files(glob)`**; no `path` argument. Daemon `E_HOSTED`. CLI `git log --follow` works (`cli/STATUS.md:93`). |
| Diff between two revisions | **`diff` covered** (left/right `RevisionRef`, `PathSelection`), but body is **raw unified patch** (`unified.patch: ByteSource`) or `"binary"` — **no structured hunks**. Daemon `E_HOSTED`. |
| Merge-base / compare | **GAP** in protocol. CLI `merge-base` (`cli/STATUS.md:100`) and `range-diff` (`:117`) via `CommandOpen`. |
| Blame | **Covered by `blame`** (`at`, `path`, `lines`, paging) but daemon `E_HOSTED`. CLI `blame` implemented (`cli/STATUS.md:49`). |
| Grep / code search | **GAP** in protocol. CLI `grep` is a subset (no pathspecs, always Rust regex, one commit scope — `cli/STATUS.md:85`). A forge-wide index must be built separately. |
| Archive (tar/zip) download | **GAP** in protocol; `upload-archive` + `archive` implemented in CLI, zip is client-side transcode of tar (`cli/STATUS.md:47`, `verbs_transport.rs:12`). Reachable via `CommandOpen`. |
| change-id ↔ commit-id mapping | **Covered by `show`** (`commit.change?`, `record.change_id`) and `log`/`blame` entries (`change?`), `query` returns `{change, heads[]}`. Daemon `E_HOSTED` for all. |
| Conflict records read | **Covered by `file`** → `{conflict{terms}}` (the algebra, not marker rendering) and `status.paths[].conflicted`; resolution via `resolve` (`Resolution`). Daemon `E_HOSTED` for `file`/`status`; `resolve` is a covered mutation. |
| Subscribe to ref updates / op-log tail (mirrors, webhooks) | **GAP.** No subscription/tail method; `op.log` is cursor-paged only, and cursors expire with their lease (`protocol.rs PageCursor`). Forge must poll `op.log` (served) or hook the CLI mutation path. **This is the single biggest missing primitive for push-mirrors and webhooks.** |
| Repo size / stats | **GAP** in protocol. CLI `count-objects`, `fsck`, `for-each-ref` via `CommandOpen`. |
| LFS | **GAP** in protocol: only `E_UNSUPPORTED_LFS` and CLI `lfs-payload-sync` (`main.rs:135-148`; `cli/STATUS.md`). Requires `lfs = "full"` policy; no batch API RPC. |

Additional forge-critical gaps: no fork/PR/review/issue model (correct — that belongs in the forge), no ref enumeration, no hook delivery, no daemon-side object GC scheduling beyond per-repo retention.

---

## 4. MCP server

Transport: newline-delimited JSON-RPC 2.0 on stdio (`mcp/server.rs:1-72`). Methods: `initialize`, `notifications/initialized` (accepted, not answered), `tools/list`, `tools/call`, `ping`; anything else `METHOD_NOT_FOUND` (-32601), malformed `PARSE_ERROR`/`INVALID_REQUEST`, bad params `INVALID_PARAMS` (-32602) with `data.core_code`, tool failures as `isError:true` results whose text starts with the CoreError code. Opened with `ruk mcp --repo <path>` (`main.rs:81-89`). It opens the store directly, **not** a coordinator session (`mcp/mod.rs:17-25`).

12 tools (`mcp/tools.rs:73-137`):

| tool | maps to |
|---|---|
| `describe_change` | `ops::describe` (`DescribeRequest`) |
| `new_commit` | `ops::new` (`NewRequest`) |
| `edit` / `split` / `squash` / `rebase` / `abandon` / `resolve` / `revert` | corresponding `ops::*Request` |
| `revset_eval` | `protocol::QueryArgs`; **always fails** `E_UNSUPPORTED_OPERATION` (validation only) |
| `refs_list` | `protocol::PageRequest` → `refs::page_refs`; cursor binding not enforced locally |
| `attestation_query` | `protocol::AttestListArgs` → `attestation::list`; allow-all checkpoint |

MCP is a thin local adapter: it has **no** session, claim, push, fetch, land, or workspace tools, and no mapping to daemon methods. It is not usable as the forge's agent surface without extension.

---

## 5. Recommendations for the Go forge (derived)

1. **Do not treat Ruk's typed protocol as the forge's only read API.** Implement reads over `CommandOpen` (git compat: `log`, `cat-file`, `ls-tree`, `diff`, `blame`, `grep`, `merge-base`, `archive`) *or* extend `HostedCoordinator::read`. This is the fastest path to a working HTML UI.
2. **Use typed mutations for writes** (`new`, `git.commit`, `bookmark.set`, `land.prepare`, `claim.*`, `attest.*`) — they provide idempotent receipts, CAS, and structured errors that a git shell lane cannot.
3. **Add a ref-listing and op-log subscription method to Ruk first.** Without them, mirroring and webhooks require polling and `for-each-ref`, and every agent UI needs a refs endpoint.
4. **Own repository lifecycle in the forge**: creation, deletion, rename, fork, quota, and LFS. Ruk deliberately exposes none of it.
5. **Structured diff hunks** must be produced in Go (parse `unified.patch`) or added to Ruk; today only raw patches exist.

---

### Critical files for implementation

- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs` — canonical method table, envelopes, paging, limits; the contract any Go client must mirror.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/ipc.rs` — framing, frame kinds, handshake, credit windows; the only place wire details are authoritative.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen`/`CommandOpen` dispatch, `dispatch_method`, stream-zero control, config.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/hosted.rs` — which reads/mutations are actually served vs `E_HOSTED` (`read_gap`, `prepare_for_dispatch`).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/cli/STATUS.md` — exact list of git verbs reachable through `CommandOpen`, with limitations.

---

### Summary (≤40 lines)

- Artifact **not written**: no write/exec tool in this subagent; content above is complete and ready to save as `.../local/ruk-backend-surface.md`.
- Wire: Unix socket; `u32 BE len | kind u8 | stream u64 BE | payload`; data frames ≤64 KiB, IPC control ≤1 MiB, protocol control ≤16 MiB; credits 256 KiB initial / 16 MiB cap.
- Handshake: peer `SO_PEERCRED` against `allowed_uids`; stream-0 `Open` `HandshakeHello{protocol_major, protocol_minor=0, profile, object_formats, features}` → `Accepted` `HandshakeAccept` with minor `min(1,hello)` and feature intersection `credits|merge-notices|outcome-lookup`.
- Invocation: `repo` = absolute path or `repo-N`; `Accepted{stream_id,method,repo_handle}`; `Exit{status,code,outcome,completed_at}`; `Error{code,message}`; `session.open` returns `{session,reconnect_secret,principal}` only.
- Commands: prepends `git`, routes to git-compat CLI; any implemented verb (`upload-pack`, `receive-pack`, `upload-archive`, `cat-file`, `log`, `archive`, …); hermetic env; no protocol auth/hooks for `receive-pack`.
- Registry: canonicalize under `repo_roots`; `E_FORBIDDEN`/`E_NOT_FOUND`; **no create/init/delete/rename/fork**.
- Config: `socket_path`, `repo_roots`, `allowed_uids`, `max_connections=128`, `blocking_pool_size=16`, `handshake_timeout_ms=5000`, `frame_timeout_ms=60000`, `stream_window_bytes`, `retention_interval_ms=60000`.
- 47 methods: 1 anonymous, 2 bootstrap, 10 reads, 34 mutations; all field lists above.
- Daemon serves only `op.log`, `claim.list`, `attest.list`; `status/log/query/diff/show/file/blame` fail `E_HOSTED`.
- 30 error codes with exit statuses; page 100 default/1000 max; revset ≤64 KiB, jj-like grammar incl. `files(glob)`.
- Gaps: repo lifecycle, ref listing, op-log tail/subscription, grep, archive RPC, repo stats, LFS, structured diff hunks, merge-base.
- MCP: 12 tools over local store; `revset_eval` always unsupported; no session/push/fetch/land tools.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/hosted.rs:255-470) ⇒ ok · 226 lines

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Verify every citation. These paths…

---
→ grep(^ @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/cli) ⇒ ok · 1 line
→ grep(fn prepare_mutation_with|Request::(Snapshot|Write|New|GitCommit|BookmarkSet|BookmarkDelete|BookmarkResolve|TagSet|TagDe…) ⇒ ok · 502 lines

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Correct two schema claims bef…

---
All actionable findings verified. Two corrections to my earlier answer: mutation coverage is now read from the actual `prepare_mutation_with` match arms (`coord/prepare.rs:248-305`), and receive-pack hooks are affirmatively absent (`verbs_hooks` contains only pre-commit/commit-msg/post-commit/pre-push/post-merge/post-checkout). `MergeNotices` is negotiated but never sent (only inbound reject arms). MCP `revset_eval` returns `E_INVALID` with `E_UNSUPPORTED_OPERATION` in the message.

No write/exec tool exists in this subagent session, so the file cannot be created here. Full artifact content follows.

---

# ruk-backend-surface.md

## 1. Daemon wire

### 1.1 Transport, framing, flow control

Unix domain socket at `socket_path`. One frame:

```
u32 BE length | kind u8 | stream_id u64 BE | payload
```
Length counts the 9-byte header plus payload (`ipc.rs` `FRAME_PREFIX_BYTES=4`, `FRAME_HEADER_BYTES=9`, `encode_frame`).

| Bound | Bytes | Source |
|---|---|---|
| data frame (Stdin/Stdout/Stderr) | 65 536 | `ipc.rs` `MAX_DATA_CHUNK_BYTES` |
| IPC control frame (all JSON on any stream) | 1 048 576 | `ipc.rs:23` `MAX_IPC_CONTROL_BYTES` |
| protocol control frame (schema/typed envelopes) | 16 777 216 | `ruk-core/src/lib.rs:18` `MAX_CONTROL_FRAME_BYTES`; model `limits.control_frame_bytes` |
| inline write edit | 1 048 576 | `protocol.rs` `MAX_INLINE_EDIT_BYTES` |
| CommandOpen stdin | 268 435 456 | `mod.rs:377` `MAX_COMMAND_STDIN_BYTES`, enforced in `collect_command_stdin` (`mod.rs:1780`) |

Frame kinds (`ipc.rs:48-107`): `Open=1, Accepted=2, Stdin=3, StdinEof=4, Stdout=5, Stderr=6, WindowUpdate=7, Cancel=8, MergeNotices=9, Exit=10, Error=11`. Unknown kind → `E_INVALID`.

**`MergeNotices` is dead on the wire.** `SUPPORTED_FEATURES` advertises `merge-notices` (`ipc.rs:41`), but under `daemon/` the kind appears only in *reject* arms (`mod.rs:850,1018,1754,1830,1959`); there is no send site. A client must not wait for notices; merge information arrives only in typed replies (`MutationOutcome.merge`).

Flow control: per-direction, per-stream credit windows; `INITIAL_STREAM_WINDOW_BYTES = 256 KiB`, cap `MAX_STREAM_WINDOW_BYTES = 16 MiB` (`ipc.rs:33-39`). `WindowUpdate` payload `WindowCredit{credit_bytes (alias "bytes"), direction: ""|"stdin"|"stdout"|"stderr"}`; empty/`stdin` grants inbound, `stdout`/`stderr` grants outbound. Credit exhaustion → `E_LIMIT`; unknown stream → `E_INVALID`.

### 1.2 Stream-zero handshake

1. `SO_PEERCRED` peer authentication; uid must be in `allowed_uids` (empty ⇒ every local peer), else `E_FORBIDDEN`; missing creds → `E_IO` (`ipc.rs:authenticate_peer`; `mod.rs:784`).
2. Client sends `Open` on stream 0 with `HandshakeHello`:

```json
{"protocol_major":1,"protocol_minor":0,"profile":"forgejo",
 "object_formats":["sha1"],"features":["credits","merge-notices","outcome-lookup"]}
```
`protocol_minor` defaults to 0 (the Go client omits it).
3. Daemon replies `Accepted` on stream 0:

```json
{"protocol_major":1,"protocol_minor":1,"profile":"forgejo",
 "object_formats":["sha1"],"features":["credits"]}
```
`protocol_minor = min(SCHEMA_MINOR=1, hello.protocol_minor)`; `features` = intersection with `SUPPORTED_FEATURES = ["credits","merge-notices","outcome-lookup"]`. `E_VERSION` on major mismatch; `E_INVALID` on empty `profile` or empty `object_formats`. First frame must be `Open` on stream 0, else `E_INVALID` (`mod.rs:786-801`).

Deadlines: handshake `handshake_timeout_ms` (default 5 000), then per-frame `frame_timeout_ms` (default 60 000).

### 1.3 Typed invocation streams

`InvocationOpen` (`mod.rs:304-320`):

| field | type | required |
|---|---|---|
| `repo` | string (absolute path starting `/`, or `repo-N` handle) | yes |
| `method` | string | yes |
| `request_id` | string? | no (mutations) |
| `args_json` | string? | no |
| `session_id` | string? | no (typed clients only) |

`InvocationAccepted{stream_id, method, repo_handle}`.
`ExitReport{status: i32, code: string, outcome: "Completed"|"CancelledBeforeCommit", completed_at: u64}`.
`ErrorReport{code: string, message: string}`.
`session.open` reply over the daemon is `SessionOpened{session, reconnect_secret, principal}` — it deliberately omits capabilities and expiry (`mod.rs:590-608`). `session.resume` returns the full `ReplyEnvelope` carrying `SessionResumeReply{session, authority, capabilities}`.

Bulk bytes: `ByteSource{inline{data}|streamed{stream,size}}`; large writes use `Content::staged{lease,oid,size}`.

Stream-zero control (`ControlRequest`): `{"type":"outcome-lookup","invocation_id":...}` → `Accepted` `ControlReply{type, found, exit?}`; any other type → `Error` frame with `E_INVALID`. `Cancel` on stream 0 cancels all streams, on a stream cancels that stream.

### 1.4 Git-shaped command streams (`CommandOpen`)

Fields (`mod.rs:389-416`): `session_id` (accepted and ignored), `repository`, `request_id`, `argv` (no `argv[0]`), `dir`, `env` (hermetic `KEY=value`), `command_digest`, `stdout_credit`, `stderr_credit`.

Dispatch: the daemon prepends `"git"` and calls `crate::cli::run_captured(argv, CliEnv{vars, cwd})` (`mod.rs:1363-1372`). There is no allowlist — the accepted set is the git-compat verb table (`cli/dispatch.rs`, `cli/mod.rs:263-304`). Every implemented verb is reachable, including `upload-pack`, `receive-pack`, `upload-archive`, `rev-parse`, `cat-file`, `ls-tree`, `log`, `show`, `diff`, `blame`, `grep`, `archive`, `merge-base`, `ls-remote`, `fetch`, `push`, `clone`, `fast-export`, `fast-import`, `index-pack`, `pack-objects`, `for-each-ref`, `show-ref` (`cli/STATUS.md:46-145`). Unknown verbs return the typed `Unsupported` error (`E_UNSUPPORTED_GIT_COMMAND`-class, exit 1).

`CommandAccepted{stream_id, stdin_credit, invocation_id}`.
`CommandExitReport{exit_code: i32, signal: string, cancelled: bool, outcome: string, operation_ids: string[], invocation_id: string}`.
Idempotency: a reused `request_id` replays the terminal report; same id with a different `command_digest` → `E_INVALID`; concurrent duplicate → `E_INVALID` (`mod.rs:1214-1290`). A long-running command blocks sibling streams on the same connection.

**receive-pack auth and hooks:** there is no protocol auth or hook channel. Authorization is (a) socket peer uid against `allowed_uids` and (b) `repository`/`dir`/`env` confinement under `repo_roots`. `receive_pack` (`cli/verbs_transport.rs:1696-1760`) runs **no** `pre-receive`, `update`, `post-receive`, or `proc-receive` hook; the hook module (`cli/verbs_hooks.rs`) implements only `pre-commit`, `commit-msg`, `post-commit`, `pre-push`, `post-merge`, `post-checkout`. Receive-side policy (branch protection, per-ref reject, push options) must live in the Go forge layer or be added to Ruk; the only store-side policy input is LFS mode.

### 1.5 Repository addressing and registry

- `repo` starting `/` = absolute path; otherwise opaque handle (`registry.rs:173-201`).
- Paths canonicalized and must `starts_with` a configured root: else `E_FORBIDDEN`; missing `E_NOT_FOUND`; inaccessible `E_IO` (`registry.rs:147-171`).
- Handles `repo-{n}`, monotonic, deduplicated per canonical path; unknown `E_NOT_FOUND`; escaped handle `E_FORBIDDEN` (`registry.rs:309-354`).
- **No create / init / delete / rename / fork RPC.** Only `Store::open` on existing paths. `Store::create` is test/CLI-only (`registry.rs:600`).
- One `HostedCoordinator` per repo, lazily opened; lane runner + retention task spawn once; idle eviction and `shutdown_all` retire (`registry.rs:203-255,356-395`).
- Repository owner principal = on-disk uid of the repo root (Unix only; else `E_HOSTED`) (`registry.rs:257-293`).

### 1.6 Config and concurrency

TOML keys (`mod.rs:166-215`): `socket_path`*, `repo_roots`* (non-empty), `allowed_uids` `[]`, `max_connections` 128, `blocking_pool_size` 16, `handshake_timeout_ms` 5 000, `frame_timeout_ms` 60 000, `stream_window_bytes` 256 KiB (1..=16 MiB), `retention_interval_ms` 60 000 (0 disables). Defaults at `mod.rs:44-57`.

One shared `BoundedExecutor` semaphore of `blocking_pool_size` permits for all repositories (`executor.rs`); Tokio `worker_threads(4)`, `max_blocking_threads = 2*pool` (`mod.rs:662-665`); per-repo live sessions ≤128 (`coord/session.rs:31`); outcome ledger ≤10 000 records.

---

[153 more lines in resource. Use :604 to continue]
---

## 2. Methods (47)

Kinds: 1 anonymous, 2 bootstrap, 10 read, 34 mutation. Every mutation is `Mutation<T>{request_id, args}` wrapped; every mutation reply begins with `MutationOutcome{receipt{session,request_id,digest,replayed,retained_until}, heads, stamps, warnings, provenance, operation?, identity?, merge?}`.

Paging: `PageRequest{limit?: 1..=1000 (default 100), cursor?: PageCursor{store, view, query, lease, position}}`; `Page<T>{items, next?}`; `BoundedList<T>{total, items, truncated}` for mutation-external excerpts. Bindings prevent cursor reuse across store/view/query/lease; >1000 is rejected, not clamped.

### 2.1 Anonymous / bootstrap

| method | request | reply |
|---|---|---|
| `schema` | `{method?: Method}` | `{schema_version, methods: MethodDescriptor[], document: Schema}` |
| `session.open` | `{capabilities?: Capabilities, claims?: ProvenanceClaims (default {}), identity?: IdentityClaim}` | protocol `{session, reconnect_secret, principal, capabilities, idle_expires_at}`; daemon `{session, reconnect_secret, principal}` |
| `session.resume` | `{session: SessionId, reconnect_secret}` | `{session, authority: {active{idle_expires_at}}|{receipts_only{receipts_until}}, capabilities}` |

### 2.2 Reads

Daemon routes **every** read to `coordinator.read` with no fallback (`mod.rs:2172-2175`); `read_gap` refuses all but three.

| method | request | reply | hosted |
|---|---|---|---|
| `op.log` | `{page?, session?}` | `{view, entries: Page<{id, method, parent?, session?, undoes?, committed_at}>, oldest_retained?}` | **served** |
| `claim.list` | `{holder?: "mine"\|"any", page?}` | `{view, claims: Page<{claim, holder, paths[], mode, expires_at}>}` | **served** |
| `attest.list` | `{kind?, signer?, subject?, include_superseded?=false, page?}` | `{view, records: Page<{subject, kind, result, signer, payload_digest, signed_at, status, supersedes?}>}` | **served** |
| `status` | `{workspace?, snapshot?: "auto"\|"skip", page?}` | `{view, workspace?, change?, applying, snapshot_operation?, paths: Page<{path, change, conflicted}>}` | **E_HOSTED** |
| `log` | `{revset: 1..=65536 B, page?}` | `{view, entries: Page<{head, parents[], author, committer, subject, change?, conflicted, divergent}>}` | **E_HOSTED** |
| `query` | `{revset, page?}` | `{view, matches: Page<{change, heads[]}>}` | **E_HOSTED** |
| `diff` | `{left: RevisionRef, right: RevisionRef, paths: "all"\|{paths[]}, page?}` | `{view, files: Page<{path, old_path?, kind, conflicted, old?, new?, body: "binary"\|{unified{patch: ByteSource}}}>}` | **E_HOSTED** |
| `show` | `{target: {head}\|{change}\|{object{oid}}}` | `{view, body: {commit{head, change?, record}} \| {object{oid, kind, size, content}}}` | **E_HOSTED** |
| `file` | `{at: {head}\|{change}\|{workspace}, path?, page?}` | `{view, body: {blob{oid,executable,size,content}}\|{symlink{target}}\|{gitlink{commit}}\|{directory{entries: Page<TreeEntry>}}\|{conflict{terms}}}` | **E_HOSTED** |
| `blame` | `{at, path, lines?: {start,end}, page?}` | `{view, hunks: Page<{start_line, line_count, head, original_path, author, change?}>}` | **E_HOSTED** |

`RevisionRef` = `{head}\|{change: ChangeRef}\|{workspace}`. `TreeEntry{name, oid, kind: tree|regular|executable|symlink|gitlink}`. `FileDiff.kind ∈ added|deleted|modified|renamed`. **No structured hunks**; **no `path` filter on `log`** (use revset `files(glob)`).

### 2.3 Mutations — served set read from `prepare_mutation_with` (`coord/prepare.rs:248-305`)

**Served (29):** bookmark.set, bookmark.delete, bookmark.resolve, tag.set, tag.delete, pin, unpin, attest.add, new, git.commit, claim.acquire, claim.release, ws.new, ws.scope, ws.take, ws.drop, snapshot, write, ws.recover, describe, edit, split, squash, rebase, abandon, resolve, revert, undo, land.prepare.

**Unserved (5):** `fetch` (no transfer producer), `push` (plan codec lives in ruk-git transport), `push.reconcile` (network observation), `land.execute` (local leg publishes eagerly outside the lane; remote leg in transport), `op.prune` (no operation-retention policy) — each returns `E_HOSTED` with a named reason.

| method | request | reply |
|---|---|---|
| `ws.new` | `{root, init: {native{parents[]}}\|{git_compat{head}}, scope?: "full"\|{cone}}` | `{outcome, workspace, state}` |
| `ws.scope` | `{binding{workspace,stamp}, scope}` | `{outcome, state}` |
| `ws.take` | `{workspace, expected?: stamp}` | `{outcome, state}` |
| `ws.drop` | `{binding}` | `{outcome}` |
| `ws.recover` | `{binding, action: "resume"\|"abort"\|{discard{paths[]}}}` | `{outcome, state, paths: BoundedList}` |
| `snapshot` | `{binding}` | `{outcome, change, changed_paths, state}` |
| `write` | `{binding, edits: [{path, action: {put{content,executable?}}\|{symlink{target}}\|"delete"}]}` | `{outcome, change, applied, state}` |
| `edit` | `{binding, target: ChangeRef{change, head?}}` | `{outcome, state}` |
| `new` | `{parents: Head[], description, identity?, workspace?}` | `{outcome, created, state?}` |
| `git.commit` | `{tree: Oid, parents[], author, committer, message, change_id?, predecessor?, ref_update?: {ref, expected, target}}` | `{outcome, created, state?}` |
| `describe` | `{target, description, identity?, workspaces[]}` | `{outcome, rewrites, abandoned, conflicted}` |
| `split` | `{source, paths, placement: "selected_first"\|"selected_second", description, identity?, merge?, workspaces[]}` | `{outcome, rewrites, abandoned, conflicted}` |
| `squash` | `{source, destination, paths, description?, identity?, keep_empty?, merge?, workspaces[]}` | same |
| `rebase` | `{sources[], scope: "source_and_descendants"\|"revisions_only", onto: Head[], keep_empty?, merge?, workspaces[]}` | same |
| `abandon` | `{targets[], merge?, workspaces[]}` | same |
| `resolve` | `{target, path, resolution: {supplied{content,executable?}}\|{side{index}}\|"accept_same", workspaces[]}` | `{outcome, change, remaining}` |
| `revert` | `{target, onto: Head[], mainline?, description?, identity?, merge?}` | `{outcome, created}` |
| `bookmark.set` | `{name: refs/heads/…, target: {commit{oid}}\|{change{...}}, expected: "absent"\|{present{...}}}` (`"any"` refused) | `{outcome, name, target}` |
| `bookmark.delete` | `{name, expected (present required)}` | `{outcome}` |
| `bookmark.resolve` | `{name, expected_terms, resolution: "delete"\|{set{oid}}}` | `{outcome}` |
| `tag.set` | `{name: refs/tags/…, target: Oid, expected, annotation?: {message, tagger?}}` | `{outcome, name, target}` |
| `tag.delete` | `{name, expected (present required)}` | `{outcome}` |
| `claim.acquire` | `{paths[], ttl: 1..=3600, mode?: "enforce"\|"advise"}` | `{outcome, claim}` |
| `claim.release` | `{claim: ClaimId}` | `{outcome, released}` |
| `attest.add` | `{statement{subject, kind, result, signed_at, supersedes?}, signature}` | `{outcome, record}` |
| `undo` | `{operation: OperationId}` | `{outcome, undone, restored}`; non-undoable → `E_UNDO_CONFLICT` |
| `pin` / `unpin` | `{head}` | `{outcome}` |
| `land.prepare` | `{change, remote, target, merge?}` | `{outcome, plan, candidate, base, expires_at, policy_generation, requirements[]}` |
| `fetch` (gap) | `{remote: {named{name}}\|{url{url}}, refs: "all_branches"\|{patterns[]}, filter?: "full"\|"blob_none", depth?}` | `{outcome, updates, conflicts, shallow}` |
| `push` (gap) | `{remote, updates[{remote_ref, source: "delete"\|{commit{oid}}\|{change{...}}, force?, expected_old?}], atomicity?}` | `{outcome, plan, state, refs}`; failures carry `ErrorDetail::push` |
| `push.reconcile` (gap) | `{plan}` | `{outcome, plan, state, observations}` |
| `land.execute` (gap) | `{plan, candidate}` | `{outcome, plan, remote, local}`; mismatch → `E_STALE` |
| `op.prune` (gap) | `{}` | `{outcome, pruned, oldest_retained?}` |

`WriteEdit.path` uniqueness and file/directory collision rules are enforced by `Validate` (`protocol.rs`). `RefExpect`/`RemoteExpect` are compare-and-swap: unconditional updates are refused.

### 2.4 Errors, capabilities, limits, revset

30 codes (model `error_codes`): `E_STALE` 3, `E_LEASE_LOST` 3, `E_REQUEST_REUSED` 3, `E_CONFLICTED` 2, `E_DIVERGENT` 2, `E_OBJECT_FORMAT` 1, `E_REMOTE_MOVED` 4, `E_PUSH_REJECTED` 4, `E_PUSH_UNCERTAIN` 4, `E_OFFLINE` 4, `E_UNDO_CONFLICT` 2, `E_CLAIMED` 3, `E_FORBIDDEN` 3, `E_POLICY` 3, `E_UNSUPPORTED_LFS` 1, `E_UNSUPPORTED_GIT_COMMAND` 1, `E_COORDINATOR_UNRESPONSIVE` 1, `E_CAPACITY` 1, `E_CORRUPT` 5, `E_NOT_FOUND` 1, `E_INVALID` 1, `E_IO` 1, `E_LIMIT` 1, `E_VERSION` 1, `E_DIRTY` 2, `E_WORKSPACE_APPLYING` 2, `E_WORKSPACE_RECOVERY` 2, `E_CANCELLED` 1, `E_SHALLOW` 4, `E_HOSTED` 1.

`ErrorReply{code, message, hints[], fixes[], detail?}`. Machine fixes: `refresh_expected`, `take_workspace`, `"new_request_id"`, `recover_workspace`, `reconcile_push`, `select_head`, `resolve_conflicts`, `"restart_query"`, `"prepare_land_again"`, `"fetch_objects"`, `wait_until`.

`Capabilities{attest[], claim_paths[], land[], push_refs[], read_intents{claims,provenance}, undo: UndoScope|null, write_changes: WriteScope|null}` (`null` write = read-only).

Limits: page 100/1000; inline 1 MiB; claim TTL ≤3600 s; control frame 16 MiB; revset 64 KiB; cursor 4096 B; JSON nesting 128.

Revset (`revset/syntax.rs:1-45`): `@`, `@-`, symbols, quoted strings; functions `heads roots ancestors descendants bookmarks remote_bookmarks tags conflicted divergent mine session change description files attested`; `x::y`, `x..y`; `~x`; `&`, `~`; `|`. No chained ranges; depth ≤128; ≤65 536 nodes. Cursor `ruk-revset-v1:<offset>`, graph-bound.

---

## 3. Gap table (schema vs hosted distinguished)

| Need | Schema | Hosted |
|---|---|---|
| List repositories | GAP | GAP — registry has no enumeration; forge DB owns it |
| Create / init repo | GAP | GAP — `Store::create` unreachable via IPC (`registry.rs:600` test only) |
| Delete / rename / fork | GAP | GAP |
| List refs/bookmarks/tags, paginated | GAP (only set/delete/resolve exist) | CLI `for-each-ref`/`show-ref` via `CommandOpen`; MCP `refs_list` local-only |
| Directory listing at revision | `file` → `directory.entries` | **E_HOSTED**; CLI `ls-tree` |
| Blob read (size, binary) | `file` → `blob{...}`; no binary flag | **E_HOSTED**; CLI `cat-file` |
| Commit log, pagination | `log` + `PageRequest` | **E_HOSTED** |
| Commit log, path filter | revset `files(glob)` only | **E_HOSTED**; CLI `log --follow` |
| Diff two revisions | `diff`; raw unified patch or `"binary"`, **no hunks** | **E_HOSTED** |
| Merge-base / compare | GAP | CLI `merge-base`, `range-diff` |
| Blame | `blame` | **E_HOSTED**; CLI `blame` |
| Grep / code search | GAP | CLI `grep` subset (no pathspecs, Rust regex, one commit); no index |
| Archive tar/zip | GAP | CLI `archive`/`upload-archive` (zip is client-side transcode) |
| change-id ↔ commit-id | `show`/`log`/`blame` `change?`, `query` | **E_HOSTED** |
| Conflict records | `file` `{conflict{terms}}`, `status.paths[].conflicted` | **E_HOSTED** for reads; `resolve` served |
| Ref-update subscription / op-log tail | GAP — no subscribe; `op.log` is cursor-paged with lease expiry | `op.log` served (poll only) |
| Mirror trigger / webhooks | GAP — `merge-notices` negotiated but never emitted | GAP |
| Repo size / stats | GAP | CLI `count-objects`, `fsck` |
| LFS | GAP (only `E_UNSUPPORTED_LFS`; CLI `lfs-payload-sync`, `lfs="full"` policy) | CLI only |
| Push / fetch from forge | schema exists | **GAP** (`E_HOSTED`; use `CommandOpen` push/fetch) |
| Land execute | schema exists | **GAP** (prepare served, execute not) |
| Undo | schema | served except fetch/push/land |
| Op pruning | schema exists | **GAP** (no retention policy) |

---

## 4. MCP server

Newline-delimited JSON-RPC 2.0 on stdio (`mcp/server.rs`). Methods: `initialize`, `notifications/initialized` (no reply), `tools/list`, `tools/call`, `ping`; otherwise `METHOD_NOT_FOUND`; bad params `INVALID_PARAMS` with `data.core_code`; tool failures as `isError:true` results starting with the CoreError code. Launched `ruk mcp --repo <path>` (`main.rs:81-89`). Opens the store directly, **not** a coordinator session (`mcp/mod.rs:17-25`).

12 tools (`mcp/tools.rs:73-137`): `describe_change`, `new_commit`, `edit`, `split`, `squash`, `rebase`, `abandon`, `resolve`, `revert` → `ruk_core::ops::*`; `revset_eval` → `QueryArgs` (always returns `E_INVALID` whose message carries `E_UNSUPPORTED_OPERATION`, `mcp/tools.rs:286-293`); `refs_list` → `PageRequest` (local paging, cursor binding unenforced); `attestation_query` → `AttestListArgs` (allow-all checkpoint).

No tools for sessions, claims, push, fetch, land, worktrees, or daemon methods.

---

### Critical files for implementation

- `ruk/crates/ruk-core/src/protocol.rs` — method table, envelopes, paging, limits.
- `ruk/crates/ruk/src/daemon/ipc.rs` — framing, frame kinds, handshake, credits.
- `ruk/crates/ruk/src/daemon/mod.rs` — invocation/command dispatch, control stream, config.
- `ruk/crates/ruk-core/src/coord/prepare.rs` — the authoritative served-mutation arm list (248-305).
- `ruk/crates/ruk-core/src/coord/hosted.rs` — `read_gap` (1346-1360); served reads only `op.log`/`claim.list`/`attest.list`.

---

### Summary

- **No artifact file was written**: this session has only read/grep/web_search.
- Handshake: peer uid → stream-0 `HandshakeHello` → `HandshakeAccept` (minor `min(1,hello)`, features ∩ `credits|merge-notices|outcome-lookup`).
- Frames: 4-byte BE length + kind + u64 stream; data ≤64 KiB, IPC control ≤1 MiB, protocol control ≤16 MiB; credits 256 KiB→16 MiB; CommandOpen stdin ≤256 MiB.
- `MergeNotices` is negotiated but never sent (reject arms only).
- `CommandOpen` has no allowlist: argv is `git <argv>` into the compat CLI, so all implemented verbs work; receive-pack runs **no** receive-side hooks; auth is peer uid + path/env confinement.
- Registry: path or `repo-N`; no create/delete/rename/fork; config `socket_path`+`repo_roots` required, 128 connections, 16 blocking permits.
- Served reads: `op.log`, `claim.list`, `attest.list` only; `status/log/query/diff/show/file/blame` → `E_HOSTED`.
- Served mutations: 29/34; `fetch`, `push`, `push.reconcile`, `land.execute`, `op.prune` → `E_HOSTED`.
- Gaps for the forge: repo lifecycle, ref listing, diff hunks, merge-base, grep index, archive RPC, stats, LFS, and **any ref/op-log subscription for mirrors and webhooks**.