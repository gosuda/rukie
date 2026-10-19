I cannot write the artifact, since I have no write tool. The artifact below is **non-accepting**: the file inventory and the complete daemon-routed list are not established, because no git diff was available. The full text follows.

# Forgejo → Ruk Daemon Go Client: Reference (NON-ACCEPTING DRAFT)

Repo: `/home/alpha/rewrite/git-renewed/ruk/forgejo` (nested repo, branch `forgejo`, HEAD `e20fb7692f`, base `acd7bc15db`). Paths are relative to that root.

**Status:** This draft does not meet the acceptance criterion. The changed-file list (§2) and the complete routed-versus-stock lists (§4) are incomplete. Without `git log --stat acd7bc15db..HEAD`, neither can be enumerated. Everything else is either verified from file reads or labelled.

## 1. Commits (reflog-derived, unverified against git)

Seventeen commits form the chain from base to HEAD, in reflog order:

1. `08399162d1` route git operations through a ruk daemon backend when present
2. `47a9edb704` route ssh serv through the git backend and add `[git] RUK_SOCKET`
3. `5a942fb162` serv: pass absolute repo path so daemon backend can authorize it
4. `03216b85cf` serv: keep upload-archive on stock git; ensure daemon roots when backend is on
5. `5072201288` models: index the branch list order for branch pages
6. `97dbc57d70` modules/git: read one branch for the default-branch fallback
7. `213d5e2c19` modules/git: backend-conditional init spelling for the ruk daemon
8. `b65052306` services/repository: template generation under the ruk daemon backend (later refused, see §4)
9. `2938f64b74` services/wiki: refuse page writes under the ruk daemon backend (superseded by 11)
10. `036edef2eb` models/db: test initFunc, not initFuncs, in RegisterModel
11. `82b5a6646e` services/wiki: write pages through the ruk daemon backend
12. `7d57d35cf3` services/wiki: cover the ruk listing helpers
13. `c914ce2668` services/wiki: migrate pre-existing .wiki.git to the ruk store (amend of `ae1c1075df`, not in HEAD)
14. `025ff82c37` services/wiki: migrate wiki on read paths; drop import sweep
15. `f86d2ddcce` modules/git: share the stock-to-ruk import behind the wiki migration
16. `c98b1edc51` services: migrate stock main repos on resolve under the ruk backend
17. `e20fb7692f` services: open model repos through the migrating choke point

Dropped by `reset:` entries, so not in HEAD: `bc2a67f2`, `7138de74`, `fd641e95`, `6acdf236`, `9db15596`, `6b5078ca`, `7a2a719c`, `4d0ad727`.

## 2. Files (NOT COMPLETE)

**Verified new client files, `modules/git/`:**

| File | Size | Purpose |
|---|---|---|
| `ruk_ipc.go` | 742 lines (verified) | Frame codec, credit windows, wire structs, handshake, connection, outcome lookup |
| `backend_ruk.go` | 831 lines (verified) | `RukBackend`, pool, `RunCommand`, pumps, error mapping, typed helpers |
| `ruk_capability.go` | 3.4 KB | `RUK_SOCKET` config, capabilities |
| `ruk_migrate.go` | 3.7 KB | `EnsureRukStore`, `publishRukStore` |
| `ruk_roots.go` | 1.6 KB | Creates directories the daemon lists as `repo_roots` |
| `backend.go` | 508 B | `GitBackend` interface |
| `ruk_migrate_test.go` | 9.5 KB | Stub-daemon migration tests |
| `ruk_migrate_live_test.go` | 3.0 KB | Live-daemon proof |
| `ruk_wire_test.go` | 3.4 KB | Serv-argv wire test |
| `ruk_roots_test.go` | 1.3 KB | Roots helper test |
| `cmd/serv_ruk_test.go` | 2.1 KB | serv backend-selection tests (not read) |
| `services/wiki/wiki_ruk.go` | 11.5 KB | Wiki writes through the daemon (not read) |
| `services/wiki/wiki_ruk_test.go` | 2.1 KB | Wiki daemon test (not read) |
| `services/wiki/wiki_migrate.go` | 1.2 KB | `ensureWikiRukStore` (`:23`) |
| `services/wiki/wiki_migrate_test.go` | 9.9 KB | Wiki migration stub tests (not read) |
| `services/wiki/wiki_migrate_live_test.go` | 2.8 KB | Wiki migration live test (not read) |

**Changed-file candidates from directory mtimes (hints, not diffs).** Only listed directories are covered. Each entry carries its listed age.

- `modules/git`, aged 19 h to 1 d: `ruk_migrate_live_test.go` (19 h), `ruk_migrate_test.go` (19 h), `ruk_migrate.go` (19 h), `repo.go` (1 d), `backend_ruk.go` (1 d), `repo_branch.go` (1 d), `repo_branch_test.go` (1 d), `ruk_wire_test.go` (1 d), `ruk_roots_test.go` (1 d), `git.go` (1 d), `ruk_roots.go` (1 d), `ruk_ipc.go` (1 d), `command.go` (1 d), `ruk_capability.go` (1 d), `backend.go` (1 d).
- `modules/gitrepo`, aged 20 h: `main_test.go`, `gitrepo_test.go`, `gitrepo.go`.
- `modules/setting`, aged 1 d: `git.go`.
- `models/db`, aged 1 d: `engine.go`.
- `routers/web/repo`, aged 20 h: `githttp.go`.
- `services/wiki`, aged 20 h to 1 d: `wiki_migrate.go`, `wiki_migrate_live_test.go` (21 h), `wiki_migrate_test.go` (21 h), `wiki.go` (21 h), `wiki_ruk.go` (1 d), `wiki_ruk_test.go` (1 d).
- `services/repository`, aged 20 h to 1 d: `sync_fork.go`, `check.go` (20 h), `generate.go` (1 d).
- `cmd`, aged 20 h to 1 d: `serv.go` (20 h), `serv_ruk_test.go` (1 d).

**Not listed, so unknown:** the top level of `services/` and `modules/`, `models/` outside `db`, and the rest of `routers/`. The branch-index change (commit 5) is not located.

**Verified call sites (grep, `:line`):**
- `cmd/serv.go:59` `func servUseDaemon(verb string) bool { return verb != "git-upload-archive" }`
- `cmd/serv.go:444` `git.EnsureRukStore(ctx, repoAbsPath, kind)`
- `modules/gitrepo/gitrepo.go:34` `OpenRepository` calls `git.EnsureRukStore(ctx, repoPath(repo), "repository")`
- `modules/gitrepo/gitrepo.go:44` `OpenWikiRepository` calls `git.EnsureRukStore(ctx, wikiPath(repo), "wiki")`
- `routers/web/repo/githttp.go:357` `return git.EnsureRukStore(ctx, h.getRepoDir(), kind)`, inside `ensureRukStore` (`:352`); called at `:464` and `:544`
- `services/repository/check.go:51` migrate-before-use call (logs a warning and creates a notice on failure)
- `services/repository/check.go:95` `git.EnsureRukStore(ctx, repo.RepoPath(), "repository")` for `git gc`
- `services/repository/sync_fork.go:35` and `:38` `EnsureRukStore` for base and fork
- `services/repository/generate.go:138-139` refuses template generation when `git.RukBackendActive()`; `:259` skips `update-server-info` when active
- `services/wiki/wiki.go:151` and `:324` `RukBackendActive()` branches (ruk-specific write and delete paths)
- `services/wiki/wiki_migrate.go:23` `git.EnsureRukStore(ctx, repo.WikiPath(), "wiki")`
- `modules/setting/git.go:29` `RukSocket string \`ini:"RUK_SOCKET"\``
- `modules/git/git.go:149-150` `defaultCommandExecutionTimeout` overridden from `setting.Git.Timeout.Default`

## 3. Client API (package `git`, module `forgejo.org`)

Verified from full `:raw` reads of `ruk_ipc.go` and `backend_ruk.go`, plus grep line citations.

**Constants (`ruk_ipc.go`):** `RukIPCProtocolMajor = 1`; `RukMaxDataChunkBytes = 64 KiB`; `RukMaxControlFrameBytes = 1 MiB`; `RukInitialCreditBytes = 256 KiB`; `RukFrameHeaderBytes = 9`; `RukControlStreamID = 0`; `RukStreamQueueDepth = 8`; `RukHandshakeTimeout = 10 * time.Second` (`ruk_ipc.go:45`); `RukControlOutcomeLookup = "outcome-lookup"`; `RukDirectionStdin/Stdout/Stderr`; `var ErrRukConnClosed`.

**Frame codec:**
```go
type RukFrameKind uint8 // Open=1 Accepted=2 Stdin=3 StdinEof=4 Stdout=5 Stderr=6 WindowUpdate=7 Cancel=8 MergeNotices=9 Exit=10 Error=11
type RukFrame struct { Kind RukFrameKind; StreamID uint64; Payload []byte }

[Showing lines 603-1204 of 1580 (50.0KB limit). Use :1205 to continue]
type RukFrame struct { Kind RukFrameKind; StreamID uint64; Payload []byte }
func RukEncodeFrame(kind RukFrameKind, streamID uint64, payload []byte) ([]byte, error)
func RukDecodeFrame(r io.Reader) (RukFrame, error)
```
Wire: u32 BE length, then kind:u8, stream_id:u64, payload. `RukDecodeFrame` checks the cap before allocating.

**Wire structs (JSON):** `RukHandshake{ProtocolMajor, Profile, ObjectFormats, Features}`; `func RukLocalHandshake() RukHandshake` (profile `"forgejo"`, features `credits`, `merge-notices`, `outcome-lookup`); `RukOpenRequest{SessionID, Repository, RequestID, Argv, Dir, Env, CommandDigest, StdoutCredit, StderrCredit}`; `RukAcceptedReply{StreamID, StdinCredit, InvocationID}`; `RukWindowUpdate{Direction, Bytes}`; `RukExitReport{ExitCode, Signal, Cancelled, Outcome, OperationIDs, InvocationID}`; `RukControlRequest{Type, InvocationID}`; `RukControlReply{Type, Found, Exit *RukExitReport}`.

**Errors:**
- `type RukRemoteError struct{ StreamID uint64; Code, Message string }` (`ruk_ipc.go:304-307`); `func (e *RukRemoteError) Error() string` (`ruk_ipc.go:311`).
- `type RukExitError struct{ Dir string; Code int; Stderr, InvocationID string }` (`backend_ruk.go:65-68`); `func (e *RukExitError) Error() string` (`:73`); `func (e *RukExitError) ExitCode() int` (`:81`); `func IsRukExitCode(err error, code int) bool`.
- `type RukUncertainError struct{ InvocationID, Reason string }`; `func (e *RukUncertainError) Error() string`; `func IsRukUncertain(err error) bool`.

**Credit window:** `func NewRukCreditWindow(initial int64) *RukCreditWindow`; `Available() int64`; `Acquire(ctx context.Context, n int64) error`; `Grant(n int64)`.

**Connection:**
```go
type RukConn struct{...}
func RukDialConn(ctx context.Context, socketPath string) (*RukConn, error)
func (c *RukConn) OpenStream(ctx context.Context, req RukOpenRequest) (*rukStream, error) // unexported return type
func (c *RukConn) Close() error
func (c *RukConn) IsBroken() bool
func (c *RukConn) ServerHandshake() RukHandshake
func RukLookupOutcome(ctx context.Context, socketPath, invocationID string) (RukExitReport, bool, error) // ruk_ipc.go:680
func (s *rukStream) RukNotices() [][]byte
```

**Backend and pool (`backend_ruk.go`):**
```go
const DefaultRukPoolSize = 8
const RukStderrTailBytes = 8 * 1024
type RukBackend struct{ socketPath string; maxConns int; slots chan struct{}; idle []*RukConn; caps *RukCapabilities; ... } // fields at :119-125
func GetRukBackend() *RukBackend // :151
func RukBackendActive() bool
func (b *RukBackend) SocketPath() string
func (b *RukBackend) RunCommand(ctx context.Context, args []string, opts *RunOpts) error
func (b *RukBackend) Capabilities(ctx context.Context) (RukCapabilities, error)
func (b *RukBackend) RevParse(ctx context.Context, dir string, revArgs ...string) (string, error)
func (b *RukBackend) ForEachRef(ctx context.Context, dir string, patterns ...string) ([]RukRefEntry, error)
func (b *RukBackend) CatFileInfo(ctx context.Context, dir, object string) (RukObjectInfo, error)
func (b *RukBackend) CatFileBytes(ctx context.Context, dir, object string) ([]byte, error)
func (b *RukBackend) LsTree(ctx context.Context, dir, ref string, paths ...string) ([]RukTreeEntry, error)
type RukRefEntry struct{ ObjectName, RefName string }
type RukObjectInfo struct{ ObjectName, ObjectType string; Size int64 }
type RukTreeEntry struct{ Mode, ObjectType, ObjectName, Name string }
```

**Pool mechanics (read in `backend_ruk.go`):** the pool is per socket path, bounded by a `slots` channel (`:120`), with `idle []*RukConn` (`:122`) used as a stack. `acquire` (`:243`) takes a slot, then pops an idle conn, or dials and caches capabilities from the first handshake. `popIdle` (`:263`) discards broken conns. `releaseConn` (`:279`) returns healthy conns to `idle` and frees the slot. Broken conns are closed.

**Config and capabilities (`ruk_capability.go`):** `RukSocketEnvVar = "RUK_SOCKET"`; `func ConfigureRukBackend(socketPath string)`; `func ResetRukBackendConfig()`; `func RukSocketPath() string` (explicit override wins, else env); `func RukEnabled() bool`; `type RukCapabilities struct{ SocketPath string; ProtocolMajor int; Profile string; ObjectFormats, Features []string }` with `Supports(feature string) bool` and `SupportsObjectFormat(name string) bool`; `func RukNegotiateCapabilities(socketPath string, server RukHandshake) (RukCapabilities, error)`.

**Migration (`ruk_migrate.go`):** `const RukImportScratchPrefix = ".ruk-import-"`; `func IsRukStoreDir(dir string) bool`; `func IsStockGitDir(dir string) bool`; `func EnsureRukStore(ctx context.Context, dir, kind string) error`.

**Timeouts (cited):**
- Command default: `defaultCommandExecutionTimeout = 360 * time.Second` (`modules/git/command.go:36`). `setting.Git.Timeout.Default`, when positive, overrides it (`modules/git/git.go:149-150`). `RunCommand` uses it when `opts.Timeout <= 0` (`backend_ruk.go:349-350`).
- Handshake: `RukHandshakeTimeout` (`ruk_ipc.go:45`), used for the connection deadline in `RukDialConn` (`ruk_ipc.go:605`) and `RukLookupOutcome` (`ruk_ipc.go:692`). The deadline is the earlier of that and the context deadline.
- Outcome-lookup budget: when the command context is already done, `reconcileOutcome` wraps a background context with `RukHandshakeTimeout` (`backend_ruk.go:655`).

**Request mechanics:** the client sends git-argv command opens only (argv, dir, env, digest). It does not call the typed v1 methods. `CommandDigest` is SHA-256 over argv, dir and env, so a retry cannot swap the command. `RukFrameCancel` is sent best-effort on context cancellation, and the stream is failed with `context.Cause`. A `PipelineFunc` error cancels the run and is returned.

**Error mapping:** a non-zero exit gives `RukExitError`. A remote `Error` frame gives `RukRemoteError`. A connection break with a known invocation ID triggers one `RukLookupOutcome`. A found `Completed` record maps to normal exit semantics. Anything else gives `RukUncertainError`. There is no silent replay.

**`*os.File` sinks:** duplicated via `/proc/self/fd` on Linux or `/dev/fd` elsewhere. Regular files are shared without duplication. Windows, plan9, js and wasip1 fail fast.

## 4. Routing: daemon versus stock (NOT COMPLETE)

**Daemon rule (verified):** `modules/git/command.go:292-294` routes `Command` execution to `GetRukBackend().RunCommand` whenever `RUK_SOCKET` (or `ConfigureRukBackend`) is set. Every `git.NewCommand(...)` run therefore goes to the daemon under that rule. The number of callers is large and was not counted. I did not enumerate each caller, so the complete daemon list is not established.

**Stock git (verified production `exec.Command` sites, grep over the tree, tests excluded):**
- `cmd/serv.go:71` and `:79`: `git-upload-archive` runs stock git through `exec.CommandContext`. `servUseDaemon` (`:59`) excludes it.
- `build/backport-locales.go:54`: a build tool that runs `git checkout` through `exec.Command`. It bypasses the backend.
- Not git, so not routed either way: `services/mailer/mailer.go:343` (sendmail), `modules/ssh/ssh.go:93` (self-exec), `modules/markup/external/external.go:124` (external renderer).
- `modules/process/manager_exec.go:51` is a generic exec helper. Which git call sites use it is unverified.

**Refused under the daemon (verified):** template generation. `services/repository/generate.go:138-139` returns an error whenever `git.RukBackendActive()` is true. Commit 8 (`b65052306`) introduced the attempt, and the code now refuses. `generate.go:259` skips `update-server-info` when the backend is active.

**Ruk-specific implementations (verified by the branch in code):** wiki page rename and delete (`services/wiki/wiki.go:151`, `:324`), which branch to `rukWikiChange` and `deleteWikiPageRuk`. Other wiki and repository operations are routed by the `Command` rule, but per-operation behaviour is not verified.

**Migration (verified):** `EnsureRukStore(ctx, dir, kind)` returns nil unless the backend is active, `dir` has `objects/` or `.git/`, and `dir/.ruk` is absent. Otherwise it creates `dir/.ruk-import-*`, runs the daemon `migrate <dir> <scratch>` verb through `NewCommand`, and renames `scratch/.ruk` onto `dir/.ruk` only after exit 0. A failed import publishes nothing. A concurrent winner is accepted. Stale scratch directories are never swept. Callers are listed in §2.

**Still on stock git outside the `Command` path:** the items under "Stock git" above.

## 5. Tests

- **Stub daemon:** `serveRukStub` in `ruk_wire_test.go`, and `serveRukMigrate{Bare,Imported,Raced}` in `ruk_migrate_test.go`. Each handles the handshake, captures the open, sends accepted, drains stdin, and sends exit. Covered: serv argv on the wire, migrate success, failure, exit 0 without a store (loud failure), publish race, native-store skip, non-git skip. `TestEnsureRukRoots` covers the roots helper. None of these was run.
- **Live proof:** `TestEnsureRukStoreLiveDaemon` in `ruk_migrate_live_test.go`. It builds a stock bare fixture with the git binary, migrates it against a real daemon, checks the tip hash, and checks that a second call is a no-op. Run with:
  ```
  RUK_LIVE_SOCKET=/tmp/repomig/ruk.sock RUK_LIVE_ROOTS=/tmp/repomig/roots \
    go test -tags 'sqlite sqlite_unlock_notify' -run TestEnsureRukStoreLiveDaemon ./modules/git/
  ```
  The daemon must run as `ruk forgejo serve --config <toml>` with `repo_roots` covering `RUK_LIVE_ROOTS`. Not run.
- **Stub suite:** `go test -tags 'sqlite sqlite_unlock_notify' ./modules/git/ -run 'Ruk|ServRuk'`. Not run.

## 6. Known limits (from code comments, commit subjects, and §4)

- Git-argv only; there is no typed v1 method client.
- `upload-archive` always uses stock git.
- Template generation is refused under the daemon (`generate.go:138-139`). Commit 8 said otherwise.
- Windows, plan9, js and wasip1 cannot duplicate `*os.File` sinks.
- Regular-file sinks share one handle, so a caller that closes one early can break output.
- Crash-orphaned `.ruk-import-*` directories are never swept.
- The reflog shows rebase churn and dropped commits (§1).
- Uses `errors.AsType`, which needs a recent Go toolchain.

## 7. License (corrected)

- `ruk/forgejo/LICENSE` is **GNU GPL v3** (read: lines 1–2).
- **File headers are mixed.** Each file's own SPDX line states its license:
  - MIT: `modules/git/repo.go:4`, `modules/git/command.go:3`, `modules/git/git.go:3`. These are existing Gogs/Gitea-derived files, relicensed in the header.
  - GPL-3.0-or-later: `modules/git/diff_compare.go:2`.
  - MIT (new): `modules/git/ruk_ipc.go:2` and `modules/git/backend_ruk.go:2`.
- The repository as a whole is GPLv3 by `LICENSE`. The per-file SPDX lines are the only file-level grant. Whether a per-file MIT header is effective inside a GPLv3 tree is a legal question.
- The new ruk files import `forgejo.org/modules/{log,process,setting}` and build only in this tree, so copying them alone needs those imports removed.
- Ownership is not established. Headers say "The Forgejo Authors", not the user. Commits are authored as `metaphorics`. Nothing in the repo shows the user holds the copyright. Get legal review before copying anything into an MIT repository. Check each file's SPDX header before classifying it.

### Critical Files for Implementation
- `modules/git/ruk_ipc.go`: frame codec, credit, connection, handshake, outcome lookup
- `modules/git/backend_ruk.go`: pool, `RunCommand`, pumps, error mapping
- `modules/git/ruk_migrate.go`: `EnsureRukStore` stock-to-ruk migration
- `modules/git/command.go`: the routing rule at `:292-294`
- `modules/gitrepo/gitrepo.go`: migrating open choke point (`:34`, `:44`)

[advisor] <advisory advisor="muse" severity="nit" turns_ago="1" guidance="weigh, don't blindly obey"> Stop header sampling, send …

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Two fixes before you hand off: 1…

---
## Later corrections from the same agent (apply over the body above)

I'll make the reads the feedback asks for, then write the final text. There is no `write` or `edit` tool in my tool list, so I cannot create the file at the absolute path. Walking the top-level directories for mtimes comes first.
→ read(/home/alpha/rewrite/git-renewed/ruk/forgejo/modules) ⇒ ok · 530 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/forgejo/services) ⇒ ok · 371 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/forgejo/models) ⇒ ok · 316 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/forgejo/routers) ⇒ ok · 55 lines

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Remove “full :raw reads” and …

---
I cannot try `write` or `edit`: neither is in my tool list, and the only tools I have are `read`, `grep` and `web_search`. The artifact therefore stays inline below, marked non-accepting.

**Correction on elision:** the earlier raw reads of `ruk_ipc.go` (1–500 and 501–742) and `backend_ruk.go` (1–400 and 401–831) returned no `…elided…` markers. The elided output came from the non-raw declaration view, not from `:raw`. The `:raw` ranges are complete. I still do not claim full verification of the whole tree, and the file list below is incomplete.

# Forgejo → Ruk Daemon Go Client: Reference (NON-ACCEPTING DRAFT)

Repo: `/home/alpha/rewrite/git-renewed/ruk/forgejo` (nested repo, branch `forgejo`, HEAD `e20fb7692f`, base `acd7bc15db`). Paths are relative to that root.

**Status:** not accepted. The file list in §2 is incomplete. The routed and stock lists in §4 are not exhaustive. The working tree was not walked in full, and no git diff was available.

## 1. Commits (reflog-derived, unverified against git)

Seventeen commits form the chain from base to HEAD, in reflog order:

1. `08399162d1` route git operations through a ruk daemon backend when present
2. `47a9edb704` route ssh serv through the git backend and add `[git] RUK_SOCKET`
3. `5a942fb162` serv: pass absolute repo path so daemon backend can authorize it
4. `03216b85cf` serv: keep upload-archive on stock git; ensure daemon roots when backend is on
5. `5072201288` models: index the branch list order for branch pages
6. `97dbc57d70` modules/git: read one branch for the default-branch fallback
7. `213d5e2c19` modules/git: backend-conditional init spelling for the ruk daemon
8. `b65052306` services/repository: template generation under the ruk daemon backend (later refused, see §4)
9. `2938f64b74` services/wiki: refuse page writes under the ruk daemon backend (superseded by 11)
10. `036edef2eb` models/db: test initFunc, not initFuncs, in RegisterModel
11. `82b5a6646e` services/wiki: write pages through the ruk daemon backend
12. `7d57d35cf3` services/wiki: cover the ruk listing helpers
13. `c914ce2668` services/wiki: migrate pre-existing .wiki.git to the ruk store (amend of `ae1c1075df`, not in HEAD)
14. `025ff82c37` services/wiki: migrate wiki on read paths; drop import sweep
15. `f86d2ddcce` modules/git: share the stock-to-ruk import behind the wiki migration
16. `c98b1edc51` services: migrate stock main repos on resolve under the ruk backend
17. `e20fb7692f` services: open model repos through the migrating choke point

Dropped by `reset:` entries, so not in HEAD: `bc2a67f2`, `7138de74`, `fd641e95`, `6acdf236`, `9db15596`, `6b5078ca`, `7a2a719c`, `4d0ad727`.

## 2. Files (INCOMPLETE; mtime walk of selected directories)

Method: `read` on directories shows each file's age. I took files aged 19 h to 1 d as candidates. The clone is about 3 d old, so 3 d files are probably base. Listings truncate with `… N more`, so the walk is partial. Ages are checkout or rewrite hints, not diffs.

**Verified new files (MIT headers, read):** all of `modules/git/ruk_*.go`, `backend.go`, `backend_ruk.go`, and their tests carry `Copyright 2026 The Forgejo Authors` and `SPDX-License-Identifier: MIT`. Header lines: `ruk_ipc.go:2`, `backend_ruk.go:2`.

**Candidate changed files, by directory (age from the listing):**

- `modules/git` (commits 1, 6, 7, 15): `ruk_migrate_live_test.go` 3.0 KB 20 h; `ruk_migrate_test.go` 9.5 KB 20 h; `ruk_migrate.go` 3.7 KB 20 h; `repo.go` 10.2 KB 1 d; `backend_ruk.go` 831 lines 1 d; `repo_branch_test.go` 6.2 KB 1 d; `repo_branch.go` 9.7 KB 1 d; `ruk_wire_test.go` 3.4 KB 1 d; `ruk_roots_test.go` 1.3 KB 1 d; `git.go` 15.6 KB 1 d; `ruk_roots.go` 1.6 KB 1 d; `ruk_ipc.go` 742 lines 1 d; `command.go` 17.6 KB 1 d; `ruk_capability.go` 3.4 KB 1 d; `backend.go` 508 B 1 d. The listing ends with `… 91 more`, so other changed files may be hidden.
- `modules/gitrepo` (commit 17): `main_test.go` 631 B 20 h; `gitrepo_test.go` 4.7 KB 20 h; `gitrepo.go` 2.6 KB 20 h.
- `modules/setting` (commit 2): `git.go` 3.2 KB 1 d.
- `cmd` (commits 2–4): `serv.go` 15.6 KB 20 h; `serv_ruk_test.go` 2.1 KB 1 d.
- `models/forgejo_migrations` (commit 5): `v18a_add_branch_list_index.go` 1.9 KB 1 d; `v18a_add_branch_list_index_test.go` 3.1 KB 1 d.
- `models/db` (commit 10): `engine.go` 13.5 KB 1 d.
- `routers/web/repo` (commit 17 area): `githttp.go` 18.2 KB 20 h.
- `services/wiki` (commits 9, 11–14): `wiki_migrate.go` 1.2 KB 20 h; `wiki_migrate_live_test.go` 2.8 KB 21 h; `wiki_migrate_test.go` 9.9 KB 21 h; `wiki.go` 12.8 KB 21 h; `wiki_ruk.go` 11.5 KB 1 d; `wiki_ruk_test.go` 2.1 KB 1 d.
- `services/repository` (commits 8, 16–17): `sync_fork.go` 2.9 KB 20 h; `check.go` 6.9 KB 20 h; `generate.go` 9.6 KB 1 d.
- `services/context` (no commit identified): `repo.go` 38.2 KB 1 d. Not explained by any listed commit.
- `services/pull` (no commit identified): `pull.go` 30.0 KB 20 h; `patch.go` 20.0 KB 20 h. Not explained by any listed commit.
- `services/funding` (no commit identified): `funding.go` 5.6 KB 20 h. Not explained by any listed commit.

**Directories not walked, so unknown:** `services/` top level beyond the entries above, `models/` other than `db` and `forgejo_migrations`, `routers/` other than `web/repo`, `templates/`, `options/`, `build/`, `tests/`, `web_src/`. Locale and config changes (if any) are unverified.

**LOC:** line counts exist only for `ruk_ipc.go` (742) and `backend_ruk.go` (831). All other sizes are byte counts from the listing. Line counts for the rest are unverified.

**Verified call sites (grep, `:line`):**
- `cmd/serv.go:59` `func servUseDaemon(verb string) bool { return verb != "git-upload-archive" }`
- `cmd/serv.go:444` `git.EnsureRukStore(ctx, repoAbsPath, kind)`
- `modules/gitrepo/gitrepo.go:34` `OpenRepository` calls `git.EnsureRukStore(ctx, repoPath(repo), "repository")`
- `modules/gitrepo/gitrepo.go:44` `OpenWikiRepository` calls `git.EnsureRukStore(ctx, wikiPath(repo), "wiki")`
- `routers/web/repo/githttp.go:357` `return git.EnsureRukStore(ctx, h.getRepoDir(), kind)`, inside `ensureRukStore` (`:352`); called at `:464` and `:544`
- `services/repository/check.go:51` migrate-before-use call (logs a warning and creates a notice on failure)
- `services/repository/check.go:95` `git.EnsureRukStore(ctx, repo.RepoPath(), "repository")` for `git gc`
- `services/repository/sync_fork.go:35` and `:38` `EnsureRukStore` for base and fork
- `services/repository/generate.go:138-139` refuses template generation when `git.RukBackendActive()`; `:259` skips `update-server-info` when active
- `services/wiki/wiki.go:151` and `:324` `RukBackendActive()` branches (ruk-specific write and delete paths)
- `services/wiki/wiki_migrate.go:23` `git.EnsureRukStore(ctx, repo.WikiPath(), "wiki")`
- `modules/setting/git.go:29` `RukSocket string \`ini:"RUK_SOCKET"\``
- `modules/git/git.go:149-150` `defaultCommandExecutionTimeout` overridden from `setting.Git.Timeout.Default`

## 3. Client API (package `git`, module `forgejo.org`)

Read from `:raw` ranges of `ruk_ipc.go` (1–500, 501–742) and `backend_ruk.go` (1–400, 401–831). No elision markers appear in those ranges. Grep line citations are given where used.

**Constants (`ruk_ipc.go`):** `RukIPCProtocolMajor = 1`; `RukMaxDataChunkBytes = 64 KiB`; `RukMaxControlFrameBytes = 1 MiB`; `RukInitialCreditBytes = 256 KiB`; `RukFrameHeaderBytes = 9`; `RukControlStreamID = 0`; `RukStreamQueueDepth = 8`; `RukHandshakeTimeout = 10 * time.Second` (`ruk_ipc.go:45`); `RukControlOutcomeLookup = "outcome-lookup"`; `RukDirectionStdin/Stdout/Stderr`; `var ErrRukConnClosed`.

**Frame codec:**
```go
type RukFrameKind uint8 // Open=1 Accepted=2 Stdin=3 StdinEof=4 Stdout=5 Stderr=6 WindowUpdate=7 Cancel=8 MergeNotices=9 Exit=10 Error=11
type RukFrame struct { Kind RukFrameKind; StreamID uint64; Payload []byte }
func RukEncodeFrame(kind RukFrameKind, streamID uint64, payload []byte) ([]byte, error)
func RukDecodeFrame(r io.Reader) (RukFrame, error)
```
Wire: u32 BE length, then kind:u8, stream_id:u64, payload. `RukDecodeFrame` checks the cap before allocating.

**Wire structs (JSON):** `RukHandshake{ProtocolMajor, Profile, ObjectFormats, Features}`; `func RukLocalHandshake() RukHandshake` (profile `"forgejo"`, features `credits`, `merge-notices`, `outcome-lookup`); `RukOpenRequest{SessionID, Repository, RequestID, Argv, Dir, Env, CommandDigest, StdoutCredit, StderrCredit}`; `RukAcceptedReply{StreamID, StdinCredit, InvocationID}`; `RukWindowUpdate{Direction, Bytes}`; `RukExitReport{ExitCode, Signal, Cancelled, Outcome, OperationIDs, InvocationID}`; `RukControlRequest{Type, InvocationID}`; `RukControlReply{Type, Found, Exit *RukExitReport}`.

**Errors:**
- `type RukRemoteError struct{ StreamID uint64; Code, Message string }` (`ruk_ipc.go:304-307`); `func (e *RukRemoteError) Error() string` (`ruk_ipc.go:311`).
- `type RukExitError struct{ Dir string; Code int; Stderr, InvocationID string }` (`backend_ruk.go:65-68`); `func (e *RukExitError) Error() string` (`:73`); `func (e *RukExitError) ExitCode() int` (`:81`); `func IsRukExitCode(err error, code int) bool`.
- `type RukUncertainError struct{ InvocationID, Reason string }`; `func (e *RukUncertainError) Error() string`; `func IsRukUncertain(err error) bool`.

**Credit window:** `func NewRukCreditWindow(initial int64) *RukCreditWindow`; `Available() int64`; `Acquire(ctx context.Context, n int64) error`; `Grant(n int64)`.

**Connection:**
```go
type RukConn struct{...}
func RukDialConn(ctx context.Context, socketPath string) (*RukConn, error)
func (c *RukConn) OpenStream(ctx context.Context, req RukOpenRequest) (*rukStream, error) // unexported return type
func (c *RukConn) Close() error
func (c *RukConn) IsBroken() bool
func (c *RukConn) ServerHandshake() RukHandshake
func RukLookupOutcome(ctx context.Context, socketPath, invocationID string) (RukExitReport, bool, error) // ruk_ipc.go:680
func (s *rukStream) RukNotices() [][]byte
```

**Backend and pool (`backend_ruk.go`):**
```go
const DefaultRukPoolSize = 8
const RukStderrTailBytes = 8 * 1024
type RukBackend struct{ socketPath string; maxConns int; slots chan struct{}; idle []*RukConn; caps *RukCapabilities; ... } // fields at :119-125
func GetRukBackend() *RukBackend // :151
func RukBackendActive() bool
func (b *RukBackend) SocketPath() string
func (b *RukBackend) RunCommand(ctx context.Context, args []string, opts *RunOpts) error
func (b *RukBackend) Capabilities(ctx context.Context) (RukCapabilities, error)
func (b *RukBackend) RevParse(ctx context.Context, dir string, revArgs ...string) (string, error)
func (b *RukBackend) ForEachRef(ctx context.Context, dir string, patterns ...string) ([]RukRefEntry, error)
func (b *RukBackend) CatFileInfo(ctx context.Context, dir, object string) (RukObjectInfo, error)
func (b *RukBackend) CatFileBytes(ctx context.Context, dir, object string) ([]byte, error)
func (b *RukBackend) LsTree(ctx context.Context, dir, ref string, paths ...string) ([]RukTreeEntry, error)
type RukRefEntry struct{ ObjectName, RefName string }
type RukObjectInfo struct{ ObjectName, ObjectType string; Size int64 }
type RukTreeEntry struct{ Mode, ObjectType, ObjectName, Name string }
```

**Pool mechanics:** the pool is per socket path. A `slots` channel bounds it (`backend_ruk.go:120`). `idle []*RukConn` (`:122`) is used as a stack. `acquire` (`:243`) takes a slot, then pops an idle conn, or dials and caches capabilities from the first handshake. `popIdle` (`:263`) discards broken conns. `releaseConn` (`:279`) returns healthy conns to `idle` and frees the slot. Broken conns are closed.

**Config and capabilities (`ruk_capability.go`):** `RukSocketEnvVar = "RUK_SOCKET"`; `func ConfigureRukBackend(socketPath string)`; `func ResetRukBackendConfig()`; `func RukSocketPath() string` (explicit override wins, else env); `func RukEnabled() bool`; `type RukCapabilities struct{ SocketPath string; ProtocolMajor int; Profile string; ObjectFormats, Features []string }` with `Supports(feature string) bool` and `SupportsObjectFormat(name string) bool`; `func RukNegotiateCapabilities(socketPath string, server RukHandshake) (RukCapabilities, error)`.

**Migration (`ruk_migrate.go`):** `const RukImportScratchPrefix = ".ruk-import-"`; `func IsRukStoreDir(dir string) bool`; `func IsStockGitDir(dir string) bool`; `func EnsureRukStore(ctx context.Context, dir, kind string) error`.

**Timeouts (cited):**
- Command default: `defaultCommandExecutionTimeout = 360 * time.Second` (`modules/git/command.go:36`). `setting.Git.Timeout.Default`, when positive, overrides it (`modules/git/git.go:149-150`). `RunCommand` uses it when `opts.Timeout <= 0` (`backend_ruk.go:349-350`).
- Handshake: `RukHandshakeTimeout` (`ruk_ipc.go:45`), used for the connection deadline in `RukDialConn` (`ruk_ipc.go:605`) and `RukLookupOutcome` (`ruk_ipc.go:692`). The deadline is the earlier of that and the context deadline.
- Outcome-lookup budget: when the command context is already done, `reconcileOutcome` wraps a background context with `RukHandshakeTimeout` (`backend_ruk.go:655`).

**Request mechanics:** the client sends git-argv command opens only (argv, dir, env, digest). It does not call the typed v1 methods. `CommandDigest` is SHA-256 over argv, dir and env, so a retry cannot swap the command. `RukFrameCancel` is sent best-effort on context cancellation, and the stream is failed with `context.Cause`. A `PipelineFunc` error cancels the run and is returned.

**Error mapping:** a non-zero exit gives `RukExitError`. A remote `Error` frame gives `RukRemoteError`. A connection break with a known invocation ID triggers one `RukLookupOutcome`. A found `Completed` record maps to normal exit semantics. Anything else gives `RukUncertainError`. There is no silent replay.

**`*os.File` sinks:** duplicated via `/proc/self/fd` on Linux or `/dev/fd` elsewhere. Regular files are shared without duplication. Windows, plan9, js and wasip1 fail fast.

## 4. Routing: daemon versus stock (NOT EXHAUSTIVE)

**Daemon rule (verified):** `modules/git/command.go:292-294` sends `Command` execution to `GetRukBackend().RunCommand` whenever a socket is configured. Every `git.NewCommand(...)` run therefore goes to the daemon under that rule. I did not enumerate the callers, so the daemon-routed list is a rule, not a list.

**Stock git (verified production `exec.Command` sites, grep, tests excluded):**
- `cmd/serv.go:71` and `:79`: `git-upload-archive` runs stock git through `exec.CommandContext`. `servUseDaemon` (`:59`) excludes it.
- `build/backport-locales.go:54`: a build tool runs `git checkout` through `exec.Command`. It bypasses the backend.
- Not git, so not routed either way: `services/mailer/mailer.go:343` (sendmail), `modules/ssh/ssh.go:93` (self-exec), `modules/markup/external/external.go:124` (external renderer).
- `modules/process/manager_exec.go:51` is a generic exec helper. Which git callers use it is unverified.

**Refused under the daemon (verified):** template generation. `services/repository/generate.go:138-139` returns an error when `git.RukBackendActive()` is true. Commit 8 introduced the attempt, and the code now refuses. `generate.go:259` skips `update-server-info` when active.

**Ruk-specific implementations (verified by the branch in code):** wiki rename and delete (`services/wiki/wiki.go:151`, `:324`) branch to `rukWikiChange` and `deleteWikiPageRuk`.

**Migration (verified):** `EnsureRukStore(ctx, dir, kind)` returns nil unless the backend is active, `dir` has `objects/` or `.git/`, and `dir/.ruk` is absent. Otherwise it creates `dir/.ruk-import-*`, runs the daemon `migrate <dir> <scratch>` verb through `NewCommand`, and renames `scratch/.ruk` onto `dir/.ruk` only after exit 0. A failed import publishes nothing. A concurrent winner is accepted. Stale scratch directories are never swept. Callers are listed in §2.

## 5. Tests

- **Stub daemon:** `serveRukStub` in `ruk_wire_test.go`, and `serveRukMigrate{Bare,Imported,Raced}` in `ruk_migrate_test.go`. Each handles the handshake, captures the open, sends accepted, drains stdin, and sends exit. Covered: serv argv on the wire, migrate success, failure, exit 0 without a store (loud failure), publish race, native-store skip, non-git skip. `TestEnsureRukRoots` covers the roots helper. None of these was run.
- **Live proof:** `TestEnsureRukStoreLiveDaemon` in `ruk_migrate_live_test.go`. It builds a stock bare fixture with the git binary, migrates it against a real daemon, checks the tip hash, and checks that a second call is a no-op. Run with:
  ```
  RUK_LIVE_SOCKET=/tmp/repomig/ruk.sock RUK_LIVE_ROOTS=/tmp/repomig/roots \
    go test -tags 'sqlite sqlite_unlock_notify' -run TestEnsureRukStoreLiveDaemon ./modules/git/
  ```
  The daemon must run as `ruk forgejo serve --config <toml>` with `repo_roots` covering `RUK_LIVE_ROOTS`. Not run.
- **Stub suite:** `go test -tags 'sqlite sqlite_unlock_notify' ./modules/git/ -run 'Ruk|ServRuk'`. Not run.

## 6. Known limits (from code comments, commit subjects, and §4)

- Git-argv only; there is no typed v1 method client.
- `upload-archive` always uses stock git.
- Template generation is refused under the daemon (`generate.go:138-139`), although commit 8 says it was added.
- Windows, plan9, js and wasip1 cannot duplicate `*os.File` sinks.
- Regular-file sinks share one handle, so a caller that closes one early can break output.
- Crash-orphaned `.ruk-import-*` directories are never swept.
- The reflog shows rebase churn and dropped commits (§1).
- Uses `errors.AsType`, which needs a recent Go toolchain.

## 7. License (as recorded)

- The user states that the code was authored by their own agents. I record that as given.
- `ruk/forgejo/LICENSE` is GNU GPL v3 (read: lines 1–2).
- **File headers are per file, not uniform:**
  - MIT (existing files, read): `modules/git/repo.go:4`, `modules/git/command.go:3`, `modules/git/git.go:3`.
  - GPL-3.0-or-later (existing file, read): `modules/git/diff_compare.go:2`.
  - MIT (new ruk files, read): `ruk_ipc.go:2`, `backend_ruk.go:2`, and the other `ruk_*.go` and `backend.go` files.
- Modified files therefore do not share one license. Only the per-file headers state the grant. A blanket "modified files are GPL" statement is wrong.
- The new ruk files import `forgejo.org/modules/{log,process,setting}`, so they build only in this tree. Copying any of them into another repository would need those imports removed.
- Copying modified files into another repository needs a rights decision by the copyright holder of each upstream file.

### Critical Files for Implementation
- `modules/git/ruk_ipc.go`: frame codec, credit, connection, handshake, outcome lookup
- `modules/git/backend_ruk.go`: pool, `RunCommand`, pumps, error mapping
- `modules/git/ruk_migrate.go`: `EnsureRukStore` stock-to-ruk migration
- `modules/git/command.go`: the routing rule at `:292-294`
- `modules/gitrepo/gitrepo.go`: migrating open choke point (`:34`, `:44`)

[advisor] <advisory advisor="muse" severity="nit" turns_ago="1" guidance="weigh, don't blindly obey"> List changed files by mtime…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Add the branch-index files to…

---
The draft already includes these files, so the advisory's "not located" claim is out of date. I checked the migration's contents with a read-only grep.

**Correction to the draft:** the branch-index change is in §2 as `models/forgejo_migrations` (commit 5): `v18a_add_branch_list_index.go` (1.9 KB, 1 d) and `v18a_add_branch_list_index_test.go` (3.1 KB, 1 d). The earlier "not located" wording referred to the draft before that edit.

**Verified content (`models/forgejo_migrations/v18a_add_branch_list_index.go`):**
- Registers migration "add (repo_id, is_deleted, commit_time DESC, name) index for the branch list order" (`:15-18`).
- Index name `IDX_branch_repo_deleted_commit_name` (`:23`), on table `branch`, columns `(repo_id, is_deleted, commit_time DESC, name)` (`:50-54`).
- `addBranchListIndex` (`:30`) runs `CREATE INDEX IF NOT EXISTS` on SQLite and PostgreSQL (`:32-34`). On MySQL it checks the catalog first, because MySQL has no `IF NOT EXISTS` for indexes (`:36-43`).
- Fresh databases skip migrations, so the index is absent there (`:29`).

The file is a schema migration, not a daemon client change. It only supports the branch-list ordering in commit 5. I did not read the `_test.go` file.

The rest of the draft is unchanged. It is still **non-accepting**. The changed-file set covers only the directories I listed, and the routed and stock lists in §4 are not exhaustive.

[advisor] <advisory advisor="minimax" severity="nit" turns_ago="1" guidance="weigh, don't blindly obey"> Add v18a_add_branch_list…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Delete the false elision claim fr…

[advisor] <advisory advisor="muse" severity="nit" guidance="weigh, don't blindly obey"> Send final text once, stop extra greps. Y…

[advisor] <advisory advisor="qwen" severity="concern" guidance="weigh, don't blindly obey"> Apply the user's stated premise inste…

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> License section still wrong. You…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Stop re-emitting the full artifact…