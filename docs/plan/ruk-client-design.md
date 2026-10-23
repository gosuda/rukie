# ruk-client-design.md: `internal/ruk`, the Go client for the Ruk host daemon (revision 2)

I could not save this file because plan mode has no write tool. This message is the complete artifact for Main to write to `ruk-client-design.md`.

**What changed from revision 1**
- **Multiplexing is a client capability.** `StreamsPerConn` defaults to 8 and is configurable. The daemon's one-active-stream-per-connection behavior is a scheduling caveat (§7.4), not an API limit. Multiplexing hides queueing latency and keeps credit per stream. It does not add parallel execution.
- **Outcome rules are re-derived from the daemon's ordering** (§10). Two defects found in source reads: a pending-frame replay stall (D15) and a post-run outcome overwrite (D16). Neither was reproduced.
- **`Command` no longer takes a caller-supplied request id.** The command ledger is process memory, so a re-run under the same id can execute twice after a daemon restart. Command mutations are reconciled with `Lookup` only (§11).

Line numbers were read this session. `≈` marks approximate lines. Items marked **[INFERENCE]** are not observed.

---

## 1. Decisions

1. **Public generics are package-level.** Go forbids type parameters on methods, so the API is `Call[Req, Resp](ctx, c, repo, method, req)`. Typed wrappers pin each Method to its Req/Resp pair.
2. **`Command` takes a stderr writer.** The daemon emits `Stderr` frames, and dropping them would lose diagnostics.
3. **Multiplexing is first-class.** Each pooled connection carries up to `StreamsPerConn` live streams (default 8). The reader demultiplexes by stream id. Each stream has its own stdin window and output queue. Placement avoids queueing where the pool can afford another connection (§7.1).
4. **Codegen: hand-written payloads, generated closed sets, and a drift gate** (§14).
5. **No automatic resend of a mutation the daemon may have accepted.** Typed mutations can be resent explicitly with the same id. Command mutations are never re-run. They are reconciled with `Lookup`.
6. **Outcome comes from client-observed facts,** not from the daemon's `outcome` label (§10).

---

## 2. Ground truth: the daemon wire

### 2.1 Framing
- Frame = `u32` BE length (counts bytes after the prefix) + `kind u8` + `stream_id u64` BE + payload.
- Constants: data payload ≤64 KiB (`ipc.rs:20`), control payload ≤1 MiB (`ipc.rs:23`), stream 0 is control (`ipc.rs:26`), initial window 256 KiB (`ipc.rs:29`), max window 16 MiB (`ipc.rs:32`), prefix 4 and header 9 (`ipc.rs:35`, `:38`), features (`ipc.rs:43`).
- Kinds (`ipc.rs:48`): 1 Open, 2 Accepted, 3 Stdin, 4 StdinEof, 5 Stdout, 6 Stderr, 7 WindowUpdate, 8 Cancel, 9 MergeNotices, 10 Exit, 11 Error. Data kinds are 3, 5, 6. The rest are JSON control.
- `read_frame` checks the announced length and the per-kind bound before reading the payload.

### 2.2 Handshake
- The first frame is `Open` on stream 0 with `HandshakeHello{protocol_major, protocol_minor, profile, object_formats, features}` (`ipc.rs` ≈405–420, `mod.rs:786–787`).
- The daemon replies `Accepted` on stream 0 with `HandshakeAccept`, after `negotiate` (`ipc.rs:443`, `mod.rs:794–806`).
- `negotiate` requires major 1 and a non-empty profile and object-format list. It echoes `object_formats` unchanged and intersects only `features`. `protocol_minor = min(1, hello.minor)`.
- **Any handshake failure closes the socket with no frame** (`mod.rs:794`, `?` on `negotiate`). The client sees EOF.
- Peer auth is `SO_PEERCRED` before the handshake (`ipc::authenticate_peer`). An unauthorized peer also sees EOF.

### 2.3 Typed invocation (`InvocationOpen`)
- `Open(N, InvocationOpen{repo, method, request_id?, args_json?, session_id?})` (`mod.rs:305–320`). `request_id` must parse as a lowercase UUID (`decode_request_args`, ≈2249–2290).
- `open_inner` (≈1972–2060) resolves the repo, then sends `Accepted(N, InvocationAccepted{stream_id, method, repo_handle})`, then calls `dispatch_method` (`mod.rs:2060`) inline. **Accepted is written before dispatch.** A failure before Accepted (unknown method, handle failure) is answered with `Error` plus `Exit` and never reaches dispatch.
- Success: `Stdout` carries the `ReplyEnvelope` JSON (`schema` and `session.open` are bare; §10), then `Exit(ExitReport{status:0, code:"OK", outcome:"Completed", completed_at})` (`send_exit`, ≈2391).
- Failure: `Error(N, ErrorReport{code, message})`, then `Exit(N, ExitReport{…})`. `send_error_exit` (≈2415–2444) sets `status` to 130 for `E_CANCELLED` and 1 otherwise, and always sets `outcome:"CancelledBeforeCommit"` (D2).
- The reply is written by `write_data_chunks` (≈2346–2368), which calls `consume_outbound` per 64 KiB chunk and **never reads the socket.** When the window is exhausted it returns `E_LIMIT` after partial output. The window is `stream_window_bytes`, default 256 KiB (`mod.rs:93`). Client grants cannot raise a typed reply's window (D1).

### 2.4 Command invocation (`CommandOpen`)
- `Open(N, CommandOpen{session_id, repository, request_id, argv, dir, env, command_digest, stdout_credit, stderr_credit})` (`mod.rs:388–412`). `request_id` is not parsed. An empty id is refused (`refuse_unclaimed`).
- `claim_request_slot` (`mod.rs:1147`, called ≈1241) returns `Run`, `Replay(report)`, `Busy`, or `DigestMismatch`. `Busy` and `DigestMismatch` are both refused with `E_INVALID` and do not write the ledger (D4).
- Order inside `command_inner` (≈1204–1330): claim → `validate_command_fs` on the blocking pool → **`Accepted`** → `serve_ready_command`.
- **The run happens only after `StdinEof`.** `collect_command_stdin` returns `Ready` only on `StdinEof` (≈1503–1560), and `serve_ready_command` runs the command only after that. A client that never writes `StdinEof` makes a run impossible.
- Stdin: each `Stdin` chunk is absorbed under the inbound window and answered with `WindowUpdate(N, {direction:"stdin", bytes})`.
- The run is synchronous on the blocking pool (`crate::cli::run_captured`, ≈1350–1400). **No frames are read during the run.**
- Success: `Exit(N, CommandExitReport{exit_code, signal:"", cancelled:false, outcome:"Completed", operation_ids:[], invocation_id})`. `outcome` is `"Completed"` for any exit code. `signal` and `operation_ids` are always empty (`CommandExitReport::success`, ≈430).
- Cancel during stdin: `cancel_command` records `CancelledBeforeCommit`. No run.
- **Cancel during output overwrites the run's row (D16).** `serve_ready_command` records `Completed` (≈1388), then `write_cmd_data` may return `WriteFate::Cancelled`, which calls `cancel_command`. That records `CancelledBeforeCommit` (≈1494–1499). The command has already run. Source-read; not reproduced.
- Output credit: `write_cmd_data` (≈1960–2010) waits for `WindowUpdate{direction:"stdout"|"stderr"}` by reading frames (`await_outbound_credit`). This is the only place the command path reads during output.
- Replay: `Accepted(N, CommandAccepted)` then the recorded `Exit`, without running (`replay_command`).

### 2.5 Control queries (stream 0)
- `Open(0, ControlRequest{type:"outcome-lookup", invocation_id})` returns `Accepted(0, ControlReply{type, found, exit?})` (`answer_control_open`, `mod.rs:873`). Also answered mid-command through `control_during_command`.
- `found` is true only for a terminal row. **Running and absent both return `found:false`** (D5).
- An unknown type or a malformed query gets `Error(0, ErrorReport)` (`write_control_error`) and does not close the connection.
- The ledger is process memory, capped at 10 000 rows (`MAX_OUTCOME_RECORDS`, ≈375).

### 2.6 Stream concurrency: the daemon's serial model
- **At most one accepted, non-terminal stream per connection.** Each connection task runs `handle_open` inline and awaits it (`serve_connection` ≈724–760, `handle_stream_frame` ≈1050–1100).
- While a command collects stdin or waits for output credit, frames for other streams are pushed into `pending` (`collect_command_stdin` ≈1520, `await_outbound_credit` ≈1930). `pending` is replayed after the terminal frame (`replay_pending` ≈1520–1540).
- While typed dispatch runs, nothing is read (§2.3). Other streams' frames wait in the socket.
- **D15 (replay stall), source-read, not reproduced:** `replay_pending` dispatches each buffered frame through `handle_stream_frame`. A replayed `Open` then reads its continuation from the **socket**, not from the rest of `pending`. So a buffered `Cancel` for that stream is not seen. The stream waits for stdin until `frame_timeout` (`DEFAULT_FRAME_TIMEOUT_MS = 60_000`, `mod.rs:53`), then `collect_command_stdin` returns `Err`, and `serve_connection` closes the connection.
- Only streams not yet accepted are affected in this path, because the previous accepted stream has already terminated.
- A `Cancel` for a finished stream produces a stray `Exit` (D6, `handle_stream_frame` Cancel arm). A late `Cancel` on a typed stream produces the same.

---

## 3. Defects and drift

| ID | Evidence | Client treatment | Daemon change |
|---|---|---|---|
| D1 | Typed replies capped at the window (256 KiB default); no socket reads during typed dispatch (§2.3). Schema reply is 118.3 KB. | `E_LIMIT` after partial Stdout is a hard error; discard partial bytes. Page large replies. | Read `WindowUpdate` during typed writes, or add `stdout_credit` to `InvocationOpen`. |
| D2 | `send_error_exit` hard-codes `outcome:"CancelledBeforeCommit"` (≈2437). `CommandExitReport::success` labels every completed run `"Completed"` (≈430). | Outcome is decided from facts (§10), never from the label. | Honest labels. |
| D3 | `refuse_command` records a refusal as `Done(exit 1, "Completed")` (≈1471). Replays return it. | Never re-use a request id after a terminal refusal. `Lookup` treats a recorded non-zero `Completed` as `Unknown`. | Do not record pre-run refusals as completed runs. |
| D4 | `Busy` and `DigestMismatch` both return `E_INVALID` (≈1290–1310). Model defines `E_REQUEST_REUSED`. | Do not treat `E_INVALID` as a retry hint. | Distinct codes. |
| D5 | `outcome-lookup` returns `found:false` for running and absent; no boot id (`answer_control_open`). Ledger is in memory. | `Lookup` absent is `Unknown`. Never re-run a command because lookup is absent. | Return `running|done|absent` and a boot id. |
| D6 | Stray `Exit` after a late `Cancel` (`handle_stream_frame` Cancel arm). | Reader counts and drops frames for unknown stream ids. | Ignore `Cancel` for finished streams. |
| D7 | `session.open` schema requires `capabilities` and `idle_expires_at`; daemon `SessionOpened` omits them (struct doc says so). | Client struct omits them. Allowlisted in the drift test. | Align schema or daemon. |
| D8 | `schema` result is `SchemaReply{schema_version, methods, document}` in the model; daemon returns the bare document (`dispatch_method`, Method::Schema arm). | Decode bare. Allowlisted. | Align. |
| D9 | Mutation `params` point at the `Mutation` wrapper (request_id inside args). Daemon takes bare args plus frame `request_id`. | Send bare args. Generated types exclude `request_id`. | Align schema. |
| D10 | Handshake failure closes with no frame (`mod.rs:794`). | EOF during handshake is `ErrHandshake` with a hint about peer uid and major version. | Send `Error` on stream 0 first. |
| D11 | Two error shapes: IPC `ErrorReport{code, message}` and schema `ErrorReply{code, message, hints, fixes, detail}`. | Decode both. | Unify. |
| D12 | `negotiate` echoes client `object_formats` instead of intersecting with server formats (model: `["sha1","sha256"]`). | Send `["sha1"]`; require the echo to match. | Intersect. |
| D13 | Model `limits.control_frame_bytes` = 16 MiB; IPC `MAX_IPC_CONTROL_BYTES` = 1 MiB (`ipc.rs:23`). | Use 1 MiB for IPC. | Document. |
| D14 | Pooled connections carry no control queries. Stream-zero `Accepted` or `Error` on a pooled connection is a violation. Lookup uses its own connection. | Pooled violation closes the connection. Lookup reads its answer synchronously. | n/a. |
| D15 | Replay stall (§2.6). | §7.7 C2: keep the connection; the stall costs only unaccepted streams, after `frame_timeout`. | Replay buffered frames for the stream from `pending`, not the socket. |
| D16 | Post-run cancel overwrites `Completed` with `CancelledBeforeCommit` (§2.4). | Commands that reached `StdinEof` and end cancelled are `Unknown` (§10). | Keep the run's recorded outcome. |

---

## 4. Prior art: Forgejo `modules/git` (learned, not copied)

- Read: `ruk_ipc.go` (742 lines), `backend_ruk.go` (831 lines), `ruk_capability.go`; file names of `ruk_migrate.go`, `ruk_roots.go`, and tests.
- Patterns confirmed: one reader goroutine per connection; per-chunk `WindowUpdate` acks; stdin credit starts from `Accepted`; a dedicated connection for outcome lookup; `RukUncertainError` carries the invocation id.
- Gaps avoided: stream-zero `Error` is fatal there; the output queue is a blocking enqueue of depth 8 that can stall the reader; exclusive checkout serializes each command; no typed calls; request ids are 16-byte hex, not UUID.
- License: the Go files carry `SPDX-License-Identifier: MIT` headers, but the brief says GPL-3.0-or-later. Confirm with legal. This design is clean-room.
- Commit subjects in `acd7bc15db..HEAD` (approximate, from the reflog; `--stat` not run): "route git operations through a ruk daemon backend when present"; "route ssh serv through the git backend and add [git] RUK_SOCKET"; "serv: pass absolute repo path so daemon backend can authorize it"; "modules/git: backend-conditional init spelling for the ruk daemon"; "services/wiki: write pages through the ruk daemon backend"; "services/wiki: migrate pre-existing .wiki.git to the ruk store"; "modules/git: share the stock-to-ruk import behind the wiki migration".

---

## 5. Package layout

```
internal/ruk/
  client.go          Client, Dial, Close, options, hooks
  call.go            Call, Retry, typed wrappers
  command.go         Command, CommandSpec, CommandResult, Lookup
  pool.go            placement, slot reservation, idle expiry
  conn.go            dial, handshake, write path, fault handling
  reader.go          per-connection reader and demux
  stream.go          stream state, byte queue, stdin window
  outcome.go         pure outcome functions (§10)
  frame.go           codec: Kind, PutHeader, ParseHeader, frame I/O
  errors.go          Code, Error, ExitError, sentinels
  handles.go         repo handle cache
  sessions.go        session cache and resume
  wire/              payload types (hand-written), ByteString, Oid
  wire/closed_gen.go generated: Method, Kind, Code, ExitStatus
  internal/rukgen/   stdlib generator and snapshot fetch
  internal/fakedaemon/  in-process fake (serial and concurrent modes)
  testdata/ruk-model.json  snapshot of the daemon `schema` document
```

---

## 6. Public API

```go
package ruk

func Dial(ctx context.Context, socketPath string, opts ...Option) (*Client, error)
func (c *Client) Close() error

type Option func(*config)
func WithProfile(name string) Option
func WithPool(maxConns, streamsPerConn int) Option // defaults 8, 8
func WithIdleTTL(d time.Duration) Option           // default 30s
func WithTimeouts(t Timeouts) Option
func WithHooks(h Hooks) Option

type Timeouts struct {
	Dial, Handshake, Write, CancelGrace, StdinGrace, LookupBudget time.Duration
}

type Hooks struct {
	OnCall              func(CallEvent)
	OnAccept            func(AcceptEvent)
	OnReconnect         func(ReconnectEvent)
	OnCreditWait        func(stream uint64, wait time.Duration)
	OnProtocolViolation func(err error)
}

type AcceptEvent struct {
	Wait   time.Duration // Open written to Accepted received
	Conn   uint64
	Queued bool          // another stream was live on the connection
}

type CallEvent struct {
	Method   wire.Method
	Duration time.Duration
	Attempts uint8
	Code     Code
	Outcome  Outcome
	OutBytes int64
}

type Outcome uint8

const (
	Unknown Outcome = iota // zero value: the safe default
	NotCommitted
	Committed
)

type Repo struct{ path, handle string }
func (c *Client) OpenRepo(ctx context.Context, absPath string) (Repo, error)

func Call[Req, Resp any](ctx context.Context, c *Client, repo Repo, m wire.Method, req Req) (Resp, error)
func Retry[Req, Resp any](ctx context.Context, c *Client, repo Repo, m wire.Method, requestID string, req Req) (Resp, error)

type CommandSpec struct {
	Dir  string
	Argv []string
	Env  []string
}

type CommandResult struct {
	RequestID string
	ExitCode  int
	Outcome   Outcome
}

func (c *Client) Command(ctx context.Context, repo Repo, spec CommandSpec, stdin io.Reader, stdout, stderr io.Writer) (CommandResult, error)
func (c *Client) Lookup(ctx context.Context, requestID string) (CommandResult, bool, error)
```

Semantics:
- `OpenRepo` sends `schema` with `repo=absPath`, reads `Accepted` for `repo_handle`, drains to `Exit`, and keeps the connection. The body is about 118 KB, so this is a one-time cost per repo. Relative paths are rejected.
- `Call` with a read method may retry automatically on transport loss before a terminal frame (§11). With a mutation it never retries automatically. `Retry` resends the same request id and the same args, and only the caller invokes it.
- `Command` always mints a fresh UUID request id. It never auto-retries and never re-runs an id.
- **`err == nil` iff `Outcome == Committed`.** A non-zero exit returns `*ExitError{Code, Outcome, Invocation, StderrTail}`. `IsExitCode(err, n)` checks the code.
- `Lookup` opens a dedicated connection, handshakes, sends the `outcome-lookup` control query, and reads the answer synchronously. It starts no goroutine.
- `Dial` handshakes eagerly and places that connection in the pool. Missing `credits` or `outcome-lookup` features are `ErrHandshake`.

Typed wrapper example:

```go
func (c *Client) Status(ctx context.Context, repo Repo, a wire.StatusArgs) (wire.StatusReply, error) {
	return Call[wire.StatusArgs, wire.StatusReply](ctx, c, repo, wire.MethodStatus, a)
}
```

The compiler cannot check that a Method pairs with its Req/Resp types, so the drift test checks it (§15).

---

## 7. Connection model and concurrency

### 7.1 Placement
`pool.acquire(ctx)` snapshots live connections under `pool.mu`. It then reserves a stream slot on one connection under that connection's `mu`, in this order:
1. An idle connection (zero live streams).
2. A new connection, if fewer than `MaxConns` exist. Dial and handshake run outside all locks.
3. The least-loaded connection with a free slot.
4. Otherwise, wait on `pool.changed` (closed and replaced on each release or dial) or on `ctx`.

Rules 1–2 are the scheduling mitigation for §7.4. A stream avoids queueing behind another stream whenever the pool can afford a connection.

### 7.2 Connection and stream state
```go
type conn struct {
	nc         net.Conn
	wmu        sync.Mutex // one frame on the socket at a time
	mu         sync.Mutex // streams, nextID, live, broken, idle
	streams    map[uint64]*stream
	nextID     uint64
	live       int
	broken     bool
	idle       *time.Timer
	readerDone chan struct{}
	maxStreams int
}

type stream struct {
	id     uint64
	mu     sync.Mutex // queue, facts, window
	state  streamState // opening | accepted | terminal | orphan
	win    window      // stdin credit; single waiter (the stdin pump)
	queue  byteQueue   // output, byte-bounded
	facts  streamFacts
	signal chan struct{} // capacity 1; wakes the caller
	done   chan struct{} // closed at terminal or connection fault
}
```
- Stream ids are monotonic per connection from 1 and never reused.
- `Accepted` gives `stdin_credit` = the daemon's window size W. Output the daemon may send is bounded by `W + stdout_credit + stderr_credit`, because the outbound window starts at W and grows by the grants in `CommandOpen` (§2.4). The client sets each command stream's queue bound to that sum, and 256 KiB for typed streams (D1). Exceeding the bound is a daemon contract violation: close the connection.
- The client sends `stdout_credit = stderr_credit = 256 KiB`. With defaults, the bound is 768 KiB.

### 7.3 Goroutines and locks
| Goroutine | Started | Shutdown path |
|---|---|---|
| Per-connection reader | after handshake | exits on any read error. `conn.close()` closes `nc`, which unblocks it. `Client.Close` waits up to 2 s on `readerDone`. |
| Stdin pump | `Command` with a non-nil stdin, after Accepted | exits on EOF, on stream terminal (its context is cancelled at terminal), or on write error. Its only unbounded wait is the caller's `stdin.Read`. `Command` returns after `StdinGrace` (1 s) and abandons the pump, matching `os/exec`'s `WaitDelay`. |

No other goroutines exist. Idle expiry uses `time.AfterFunc`, stopped on reuse, so no long-lived goroutine is created. Handshake and `Lookup` are synchronous.

Locks: `pool.mu` (connection list, `changed`), `conn.mu` (streams, nextID, live, broken, idle timer), `conn.wmu` (socket writes), `stream.mu` (queue, facts, window). No goroutine holds two of these at once. The reader finds the stream under `conn.mu`, releases it, then updates under `stream.mu`. Socket writes never happen under `conn.mu` or `stream.mu`.

### 7.4 Daemon serial execution: a scheduling caveat
From §2.3, §2.4, and §2.6: at most one accepted, non-terminal stream runs per connection. Other Opens wait in the daemon. Typed dispatch reads nothing while it runs.

Consequences the client handles:
- **Accepted latency on a busy connection** is roughly the remaining life of its active stream. Placement (§7.1) avoids this when a connection can be opened. `AcceptEvent.Queued` makes it observable.
- **Multiplexing hides latency,** not execution cost. Queued streams keep their own credit and their own deadlines, so one stream's stall does not consume another stream's window.
- **A stream's context covers its queue time.** A stream that times out while queued is cancelled under §7.7.
- **The daemon's pending buffer stays bounded.** The client never writes stdin before Accepted, because the stdin window is zero until then. So the daemon holds only Opens and cancels for waiting streams: at most about two frames per stream, bounded by `StreamsPerConn`.

### 7.5 Reader
Per frame:
1. Read 13 bytes and `ParseHeader`. Length and kind bounds are checked before the payload is read. Data payloads go into pooled 64 KiB buffers. Control payloads are sized exactly.
2. Stream 0 on a pooled connection is a violation: close the connection (D14).
3. Unknown stream id: count a stray via `OnProtocolViolation`, discard the payload.
4. Dispatch by kind:
   - `Accepted`: `opening → accepted`. Store W (must be > 0, else violation) and the invocation id. Compute the queue bound. Signal the opener. Orphans keep their state and the reader ignores the frame.
   - `Stdout` / `Stderr`: orphans discard the payload with no ack. Otherwise, if queued + len exceeds the bound, close the connection. Otherwise append and signal the caller.
   - `WindowUpdate`: direction `stdin` or empty grants W. Any other direction from the daemon is a violation.
   - `MergeNotices`: append up to 64 KiB, then set a truncated flag.
   - `Error`: record the refusal (code and message). The terminal `Exit` follows.
   - `Exit`: decode by key (`exit_code` → `CommandExitReport`; `status` → `ExitReport`; neither or both → violation). Set terminal, remove the stream from the table, close `done`, and signal.
   - `Open`, `Cancel`, `Stdin`, `StdinEof`, `Accepted` from the daemon: violation.
5. The reader never writes to the socket. Every reader action is local.

On violation, `conn.fail(err)` marks the connection broken, closes `nc`, and closes `done` on every live stream. Each stream's outcome comes from §10 facts.

### 7.6 Writes and credit
- Each frame goes through `conn.wmu` with a `Write` deadline: `PutHeader` into a 13-byte array, then one write of header plus payload.
- Stdin pump: acquire window credit, write `Stdin` frames, and before writing `StdinEof` set `facts.eofAttempted = true`. A partial write counts as attempted.
- Output acks: after each user write, the caller sends `WindowUpdate{direction, bytes}`. Typed streams get no acks (D1).

### 7.7 Cancellation
- **C0. Open not yet written:** nothing is sent. The slot is released.
- **C1. Typed stream, Open written:** send `Cancel` and return. Reads return the context error; mutations return `Unknown`. The stream becomes an orphan. The reader drops its data without acks and removes it at terminal. The daemon's later stray `Exit` is discarded (D6).
- **C2. Command, cancelled before `StdinEof` was written:** send `Cancel`, return `NotCommitted` at once, and mark the stream an orphan. **Keep the connection.** The daemon cannot run the command without `StdinEof`, which this client never wrote. The one harmful case is D15: if the orphan's Open and Cancel were both buffered behind an accepted stream, the daemon stalls for up to `frame_timeout` and then closes the connection. Only streams not yet accepted are affected. Closing the connection now would instead kill an accepted sibling, so it is not done. A re-cancel on Accepted was considered and rejected for v1: it needs reader-side writes, which §7.5 forbids, and it is not tested.
- **C3. Command, `StdinEof` attempted:** send `Cancel`, then keep draining output on the caller goroutine until terminal or `CancelGrace` (2 s). A terminal frame is reported as received. It may be `Completed` with exit 0, and then the result is `Committed`. Otherwise close the connection and return `Unknown`. Then `Lookup`. Costs: the one accepted sibling becomes `Unknown`, and unaccepted siblings become `NotCommitted`.

---

## 8. Framing codec

```go
const (
	PrefixBytes       = 4
	HeaderBytes       = 9
	FrameBytes        = PrefixBytes + HeaderBytes
	MaxDataPayload    = 64 << 10
	MaxControlPayload = 1 << 20
	ControlStream     = 0
	DefaultWindow     = 256 << 10
	MaxWindow         = 16 << 20
)

type Kind uint8

const (
	KindOpen Kind = iota + 1
	KindAccepted
	KindStdin
	KindStdinEOF
	KindStdout
	KindStderr
	KindWindowUpdate
	KindCancel
	KindMergeNotices
	KindExit
	KindError
)

func (k Kind) IsData() bool
func PutHeader(dst *[FrameBytes]byte, k Kind, stream uint64, payloadLen int) error
func ParseHeader(src *[FrameBytes]byte) (k Kind, stream uint64, payloadLen int, err error)
```

- `PutHeader` writes `total = 9 + payloadLen` as BE `u32`, then the kind, then the stream id as BE `u64`. It rejects a payload above the kind's bound. No allocations.
- `ParseHeader` rejects `total < 9`, unknown kinds, and payloads above the kind's bound. It returns `payloadLen = total - 9`. No allocations.
- Reading 13 bytes at once is equivalent to the daemon's prefix-then-header reads.
- Data payloads come from a `sync.Pool` of `*[MaxDataPayload]byte`. Control payloads are allocated exactly.
- Parse failures are `ErrProtocol`. EOF at a frame boundary is `ErrClosed`. EOF mid-frame is transport loss.

---

## 9. Handshake fields

Client `HandshakeHello`:
- `protocol_major`: 1
- `protocol_minor`: 1
- `profile`: `"rukie"` by default (`WithProfile`). The daemon does not interpret it (§16, item 9).
- `object_formats`: `["sha1"]`. The daemon echoes the list (D12).
- `features`: `["credits","merge-notices","outcome-lookup"]`.

Checks on `HandshakeAccept`:
- `protocol_major == 1`, else `ErrVersion`.
- `features` contains `credits` and `outcome-lookup`, else `ErrHandshake`.
- `object_formats` equals what was sent (D12), else `ErrHandshake`.
- EOF during handshake: `ErrHandshake` wrapping `io.ErrUnexpectedEOF`, with a message naming the two causes (unauthorized peer uid, or handshake rejected).

Handshake deadline: `min(ctx, Timeouts.Handshake = 5 s)`, matching `DEFAULT_HANDSHAKE_TIMEOUT_MS` (`mod.rs:50`).

---

## 10. Errors and the outcome rule

### 10.1 Error surface
The model defines 30 codes and exit statuses in `/tmp/ruk-model.json` `error_codes`:

| Exit | Codes |
|---|---|
| 1 | E_OBJECT_FORMAT, E_UNSUPPORTED_LFS, E_UNSUPPORTED_GIT_COMMAND, E_COORDINATOR_UNRESPONSIVE, E_CAPACITY, E_NOT_FOUND, E_INVALID, E_IO, E_LIMIT, E_VERSION, E_CANCELLED, E_HOSTED |
| 2 | E_CONFLICTED, E_DIVERGENT, E_UNDO_CONFLICT, E_DIRTY, E_WORKSPACE_APPLYING, E_WORKSPACE_RECOVERY |
| 3 | E_STALE, E_LEASE_LOST, E_REQUEST_REUSED, E_CLAIMED, E_FORBIDDEN, E_POLICY |
| 4 | E_REMOTE_MOVED, E_PUSH_REJECTED, E_PUSH_UNCERTAIN, E_OFFLINE, E_SHALLOW |
| 5 | E_CORRUPT |

```go
type Code string
const CodeStale Code = "E_STALE" // one constant per code, generated

var ErrStale = codeSentinel(CodeStale) // one sentinel per code, generated

func (c Code) ExitStatus() int

type Error struct {
	Code       Code
	Op         string
	Message    string
	Hints      []string
	Invocation string
	Outcome    Outcome
	Err        error
}
func (e *Error) Error() string
func (e *Error) Unwrap() error
func (e *Error) Is(target error) bool

type ExitError struct {
	Code       int
	Outcome    Outcome
	Cancelled  bool
	Invocation string
	StderrTail string
}
func IsExitCode(err error, code int) bool

var (
	ErrClosed    = errors.New("ruk: client is closed")
	ErrNoDaemon  = errors.New("ruk: daemon socket is unreachable")
	ErrHandshake = errors.New("ruk: handshake failed")
	ErrProtocol  = errors.New("ruk: protocol violation")
)
```
- `Error.Is` matches by `Code` against the sentinel.
- An unknown code string from the daemon is `ErrProtocol` wrapping the raw code. It is never guessed.
- Every error is wrapped at the call site: `fmt.Errorf("ruk %s: %w", op, err)`.

### 10.2 Outcome from facts
The daemon's `outcome` label is not used for decisions (D2, D16). Outcomes are pure functions over facts the client observed. They live in `outcome.go` and are tested as tables.

```go
type typedFacts struct {
	Attempted bool   // Open write was attempted
	Accepted  bool   // Accepted received
	Terminal  bool   // Exit received
	Success   bool   // Exit OK, status 0, Completed, body success
	Refusal   string // code of an Error frame or failure body, if any
}

type cmdFacts struct {
	EOFAttempted bool
	Terminal     *CommandExitReport
}
```

Typed mutation, fresh id:

| Facts | Outcome |
|---|---|
| Open not attempted | NotCommitted |
| Terminal success | Committed |
| Terminal refusal, no Accepted | NotCommitted (the daemon finished the Open before dispatch) |
| Terminal refusal after Accepted, code in the pre-dispatch set | NotCommitted |
| Terminal refusal after Accepted, other code | Unknown |
| Attempted, no terminal (loss, context, timeout) | Unknown |

The pre-dispatch set is {E_INVALID, E_NOT_FOUND, E_FORBIDDEN, E_VERSION, E_LIMIT, E_REQUEST_REUSED, E_HOSTED, E_OBJECT_FORMAT, E_UNSUPPORTED_LFS, E_UNSUPPORTED_GIT_COMMAND}. **[INFERENCE]** These fail before submission. Confirm with the ruk owners.

A typed Open attempted and not accepted can still be dispatched later if the connection stays up (the daemon processes buffered Opens). So an unaccepted typed mutation whose caller gave up is `Unknown`, not `NotCommitted`.

Command, fresh id:

| Facts | Outcome |
|---|---|
| Terminal `Completed`, exit 0, not cancelled | Committed |
| `StdinEof` never attempted | NotCommitted (no run is possible; §2.4) |
| Anything else (cancel, refusal, non-zero exit, loss after `StdinEof`) | Unknown |

Lookup: `found` and `Completed`, exit 0, not cancelled → Committed. Any other found row → Unknown (D3, D16). Not found → Unknown (D5).

Typed body decoding: the envelope is `{schema_version, request_id?, body}`. `body` must be exactly one of `{"success": {<method>: payload}}` or `{"failure": ErrorReply}`, and the success key must equal the method name. Otherwise `ErrProtocol`. Bare results: `session.open` decodes to `SessionOpened`, `schema` decodes to the model document, and `session.resume` is enveloped (`dispatch_method`, ≈2109–2160). A `failure` body is classified by its code with the same post-Accepted rule.

---

## 11. Retry, reconnect, handles, sessions

**Retry matrix.**

| Failure | Read (typed) | Mutation (typed) | Command |
|---|---|---|---|
| Dial or handshake | retry up to 3, backoff 50 ms → 1 s with jitter | same | same |
| Open not attempted | retry | retry | retry (caller) |
| Open attempted, no terminal | retry once (reads are idempotent) | `Unknown`. The caller may call `Retry` with the same id and args. | `Unknown`. `Lookup`. Never re-run. |
| Terminal refusal | handle or session flow (below) | per §10 | per §10 |

**Typed `Retry`.** Replays the recorded reply. Typed replies are stored durably in receipts (`protocol.rs` module docs: "Non-schema replies are stored durably in receipts"). **[INFERENCE]** That durability survives a daemon restart. Confirm (§16, item 5).

**Command reconcile.** `Lookup(id)` opens a dedicated connection, handshakes, sends `outcome-lookup`, and reads the answer. Found and successful → Committed with the recorded exit. Otherwise → Unknown. The client never re-runs a command, because the ledger is in memory: after a restart, absence cannot be told apart from never-started (D5).

**Handles.** `OpenRepo` yields the handle. Calls send the handle, or the path if none. On `E_NOT_FOUND` with a handle (restart or eviction), the client re-opens by stored path once and retries once. This failure occurs before Accepted, so the mutation was not dispatched and retrying is safe.

**Sessions.** Keyed by canonical repo path. Entry: `{SessionId, ReconnectSecret, authority}`. The secret's `String()` returns `"<redacted>"` and it is never logged.
- First use: `session.open` with `{}`, decoded to `SessionOpened`. Capabilities and `idle_expires_at` are absent by design (D7).
- On `E_FORBIDDEN` or `E_NOT_FOUND` from a session-bound call: `session.resume(reconnect_secret)` once, then retry once. If resume fails, `session.open` once, then retry once.
- If resume reports non-active authority, mutations fail locally with `ErrForbidden` ("session lacks active authority") until a fresh open.

---

## 12. Timeouts and limits

| Name | Default | Basis |
|---|---|---|
| Dial | 2 s | context-bounded |
| Handshake | 5 s | daemon default (`mod.rs:50`) |
| Write (per frame) | 10 s | client choice; daemon frame timeout is 60 s (`mod.rs:53`) |
| CancelGrace | 2 s | §7.7 C3 |
| StdinGrace | 1 s | §7.3 |
| LookupBudget | 5 s | §11 |
| IdleTTL | 30 s | §7.1 |
| MaxConns | 8 | prior art uses 8 |
| StreamsPerConn | 8 | §7.4 |
| Output queue bound | W + stdout + stderr credits (768 KiB default) | §7.2 |
| Typed output bound | 256 KiB | D1 |

Call-level deadlines are the caller's context. The daemon imposes no deadline during typed dispatch. A stream's stdin must keep flowing within 60 s, or the daemon drops the connection.

---

## 13. Metrics hooks

Hooks are plain function fields; nil is a no-op. `CallEvent` and `AcceptEvent` are passed by value. `OnCall` fires once per terminal state of `Call`, `Retry`, `Command`, or `Lookup`. `OnAccept` fires on each Accepted, with `Queued` set when another stream was live. This measures the §7.4 cost directly. `OnCreditWait` fires when a stdin `acquire` blocks. `OnProtocolViolation` fires on `ErrProtocol` and on stray frames (D6).

---

## 14. Codegen decision

**Pick: hand-written payload types, generated closed sets, and a drift gate.**

- **Generated** (stdlib-only generator in `internal/rukgen`): `wire.Method` constants (47), a `Kind()` table (1 anonymous, 2 bootstrap, 10 read, 34 mutation), `Code` constants (30), the `ExitStatus` table, and sentinels. Input: `testdata/ruk-model.json`, fetched from a running daemon by `rukgen -fetch <socket> -out testdata/ruk-model.json`, which uses this client.
- **Hand-written:** payload structs for the roughly 20 of 47 methods rukie uses, `ByteString`, `Oid`, `ReconnectSecret`, `SessionOpened`, and the envelope decoders.
- **Drift gate:** a unit test reflects over registered payload types and compares JSON field names and `required` sets with the snapshot's `$defs`, honoring an allowlist (D7, D8, D9, D11). A live test compares the daemon's `schema` with the snapshot. A generator test regenerates and diffs.

**Why not full generation from JSON Schema:**
1. The schema is not reliable for replies. D7 and D8 are wrong in the schema, and generation would faithfully emit wrong types.
2. Full generation must handle externally tagged `oneOf` (`ReplyBody`, `Reply`, `EditAction`), generic instantiations (`Mutation2..34`, `BoundedList2..12`, `Page2..9`), `ByteString`'s string-or-`{hex}` union, `Merge<T>` alternating arrays, and pattern-restricted newtypes. That generator would be about as large as this client, and it would have to meet the cyclomatic-under-10 rule.
3. A third-party generator violates stdlib-first.

[97 more lines in resource. Use :1810 to continue]
3. A third-party generator violates stdlib-first.
4. Hand-written structs carry domain names and only the fields rukie uses.

**Revisit if** rukie needs more than about 60% of the methods, or ruk ships a Go-target schema without the defects above.

**Generated-file marker:** the generated files carry the Go `Code generated … DO NOT EDIT.` line that tooling requires. This is an exception to the no-comments rule and needs Main's sign-off.

---

## 15. Test plan

**Unit tests (no daemon), run with `-race`:**
- Codec: round-trip all 11 kinds. Boundaries: data 64 KiB accepted, 64 KiB+1 rejected before payload read; control 1 MiB accepted; total < 9 rejected; unknown kind rejected; short read. `testing.AllocsPerRun` == 0 for `PutHeader` and `ParseHeader`. Fuzz `ParseHeader` and `read_frame` over arbitrary bytes and deadlines.
- Byte queue: exactly the bound is accepted, bound+1 is a violation, and orphan data does not grow the queue.
- Window: acquire blocks until grant, context cancel while waiting, close fails the waiter, no partial consumption.
- Outcome tables: every row of §10.2 for typed and command facts, plus Lookup found and not found.
- Errors: all 30 codes map to their sentinels under `errors.Is`; an unknown code is `ErrProtocol`; `ExitStatus` equals the model table.
- Placement: an idle connection is preferred; a new connection is opened before queueing while below `MaxConns`; at the cap the least-loaded connection is chosen; a context expiring while waiting returns the context error.
- Drift: payload field sets and `required` sets against the snapshot, with allowlist; Method↔Resp pairing against `methods[].params` and `result`.
- Goroutines: count before and after `Dial`/`Close` and after a command with stdin that the caller abandons. A stdin reader that returns on close must let the pump exit.

**Fake daemon (`internal/fakedaemon`), over a Unix socket in `t.TempDir()`, short enough for the 108-byte `sun_path` limit.** It has two modes:
- **Serial (faithful):** one active stream. Frames for other streams go to a pending list, replayed after the terminal frame. A replayed Open reads its continuation from the socket. This reproduces D15 by default, and a flag selects the fixed replay behavior.
- **Concurrent:** streams processed concurrently with per-stream credit, for multiplexing and credit tests.

The fake must reproduce `pending` replay. Otherwise its results are not evidence about the real daemon.

Fake-daemon test cases:
- **M1 (serial):** 8 commands on one connection complete in Open order. Accepted latency of queued streams is at least the active stream's duration. Deadlines are honored.
- **M2 (concurrent):** 4 streams interleave output. Each queue is bounded. A blocked caller on stream A does not delay stream B's delivery.
- **M3 (credit):** stdin larger than the window on two streams. Each stdin pump blocks independently.
- **M4 (typed orphan):** a typed mutation cancelled after Open is attempted returns `Unknown`. The late stray `Exit` is ignored.
- **M5 (command orphan before EOF):** returns `NotCommitted` at once. The connection stays usable for siblings.
- **M6 (contract violation):** a daemon sending more output than the bound causes a violation. The connection closes, and each live stream's outcome follows §10.2.
- **M7 (D15):** serial mode with a buffered Open and Cancel behind an active stream and a short `frame_timeout`. Assert the current defect: the connection closes after the timeout and unaccepted siblings are `NotCommitted`. With the fixed flag, assert that the Cancel is honored promptly.
- **M8 (D16):** a command cancelled during output with credit exhausted. Assert the recorded row is `CancelledBeforeCommit` even though the run completed. With the fixed flag, `Lookup` reports `Completed`.

**Integration (real binary):** gated by `RUKIE_RUK_INTEGRATION=1`. `RUK_BIN` defaults to `/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk`. Setup: `ruk init <repo>` (verb table entry `name:"init"`, `needs_repo:false`, `dispatch.rs` ≈380–384). Write a TOML config with `socket_path`, `repo_roots`, `allowed_uids`, `frame_timeout_ms` (2000 for the D15 test), and `retention_interval_ms = 0`. Then run `ruk forgejo serve --config <toml>` (`main.rs:201–214`). The subcommand is renamed later, so the test takes argv from configuration. Cases:
- Handshake, and `schema` equals the snapshot.
- `session.open`, then `session.resume`.
- Typed `ws.new` with a request id, then `Retry` with the same id returns the same reply. This tests the durability assumption.
- `hash-object -w --stdin` with more than 256 KiB of stdin (credit grants). Verify with `cat-file -t`.
- `cat-file -p` of a blob above 768 KiB (output acks).
- Two commands on the pool: the second is placed on a new connection (§7.1), and `AcceptEvent.Queued` is false.
- D15: stream A is `hash-object --stdin` with stdin held open, so A is accepted and collecting. Open B and cancel it. Send A's `StdinEof`. Assert that B's connection closes after `frame_timeout` and B is `NotCommitted`. A's object is written.
- D16: cancel a command during output with credit exhausted. Verify the side effect (an object was written) and that `Lookup` reports `CancelledBeforeCommit`.
- Typed reply above 256 KiB asserts the current `E_LIMIT` (D1). Update when fixed.
- Kill the daemon mid-command: the command outcome is `Unknown`, and `Lookup` after restart reports absent.

**Build gates for Main (not run by me):** `go test -race ./internal/ruk/...`, `go vet ./internal/ruk/...`, fuzz targets with a short `-fuzztime`, and the generator diff check.

---

## 16. Daemon change requests

Ruk may break compatibility. These are candidates, in priority order.
1. **Replay stall (D15):** replay buffered frames for a stream from `pending`, not the socket, so a buffered Cancel is honored.
2. **Honest outcomes (D2, D16):** keep the run's recorded outcome when output is cancelled. Labels must reflect whether the run happened. `Completed` only with the real exit.
3. **Typed reply window (D1):** read `WindowUpdate` during typed writes, or add `stdout_credit` to `InvocationOpen`.
4. **Concurrency:** serve streams concurrently, or keep per-stream pending state with correct replay. Until then, §7.4's serial model is the daemon's behavior, and the client supports multiplexing either way.
5. **Lookup states and boot id (D5):** return `running|done|absent` and a per-process boot id. Document whether the typed receipt store survives restart, which §11's `Retry` relies on.
6. **Cancel honored during run:** pass a cancellation token into `run_captured` and into typed dispatch (C3).
7. **Refusal rows (D3):** do not record pre-run refusals as completed runs.
8. **Error codes (D4):** `E_REQUEST_REUSED` for digest mismatch and a distinct code for busy.
9. **Handshake failures (D10):** send `Error` on stream 0 before closing.
10. **Object formats (D12):** intersect with server formats.
11. **Profile semantics:** confirm the forge-neutral profile name for `ruk serve`.
12. **Schema alignment (D7, D8, D9, D11):** fix `session.open` reply, `schema` result shape, mutation params, and unify the error shape.
13. **Stray frames (D6):** ignore `Cancel` and credit frames for finished streams.
14. **Output size:** confirm there is a bound on `run_captured` stdout. None was seen. **[INFERENCE]**
15. **Module and Go line:** `rukie/go.mod` says `module gitea.dev` and `go 1.27` (lines 1 and 3). The brief says `go 1.27.0`. Confirm the target module path and go line.

---

## 17. Verification (for Main, not run by me)
- Save this as `ruk-client-design.md`.
- Regenerate the snapshot from a running daemon and diff it against `testdata/ruk-model.json`.
- Confirm the stdlib-only constraint for the generator and the client.
- Confirm the license posture for the Forgejo prior art (§4).
- Run the M-series fake-daemon tests in both modes, then the integration cases in §15.

---

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs`: dispatch, ledger, Cancel and Exit semantics, `replay_pending` (D15), the output-overwrite path (D16), typed reply writes (D1), and the connection loop.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/ipc.rs`: frame constants, codec, credit windows, `negotiate`, and handshake structs.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/registry.rs`: path and handle resolution, and the `ENotFound`/`EForbidden` split that drives reopen.
- `/tmp/ruk-model.json`: 30 error codes and 47 methods; the drift baseline to snapshot into `testdata/`.
- `/home/alpha/rewrite/git-renewed/rukie/go.mod`: module path and go line that decide where `internal/ruk` lives.

[advisor] <advisory advisor="stickler" severity="blocker" guidance="weigh, don't blindly obey"> Remove E_LIMIT from the §10 NotCo…

[advisor] <advisory advisor="qwen" severity="blocker" guidance="weigh, don't blindly obey"> Do not ship §10: E_LIMIT is still in …

[advisor] <advisory advisor="minimax" severity="blocker" guidance="weigh, don't blindly obey"> Remove E_LIMIT from the NotCommitt…

[advisor] <advisory advisor="minimax" severity="blocker" guidance="weigh, don't blindly obey"> Remove E_LIMIT from the §10 NotCom…