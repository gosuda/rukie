I couldn't create `arch-sketch-monolith.md` because this read-only session has no file-writing tool. The full sketch is below; save it to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/arch-sketch-monolith.md`.

**Summary**
- **The monolith can't run its core flows on today's daemon.** I checked the code:
  - **Reads:** the daemon answers only three read methods (`op.log`, `claim.list`, `attest.list`). `file`, `log`, `diff`, `show`, `blame` and `query` return `E_HOSTED` (`hosted.rs:566-640`).
  - **Landing:** `land.execute` and `push` are refused on the hosted path (`prepare.rs:63-65`).
  - **Required attestations:** the policy file has no section for land requirements yet (`prepare.rs:3432-3435`).
- **Each connection does one thing at a time.** One git command blocks the whole connection (`mod.rs:1196-1203`), so the Go client keeps a pool of connections. A typed reply is limited to the 256 KiB starting credit, because the daemon doesn't wait for more credit. Control frames are capped at 1 MiB in the daemon but 16 MiB in the schema.
- **Pack data is not streamed today.** Git commands collect all input before running (cap 256 MiB) and capture all output before sending it (`mod.rs:377,1370`). That still works for smart HTTP. It cannot work for SSH, where the server has to talk before the client finishes sending, so streaming git service frames (R5) is a hard prerequisite.
- **Every forge user and agent reaches Ruk as the same OS user.** The daemon identifies callers only by Unix uid (`mod.rs:2017`), so rukie opens one Ruk session per user and repo and narrows that session's capabilities itself.

**What the sketch covers** (each with concrete names and Go signatures):
- **Topology:** `rukie serve` and a separate `ruk serve` process over a Unix socket, plus `rukie runner` on CI hosts.
- **Packages:** 20 under `internal/`, each with a one-line job and a dependency order.
- **The `internal/ruk` client:**
  - framing and handshake
  - connection pool
  - typed reads and mutations with request ids
  - full-duplex `GitStream` for pack data
  - cancellation driven by `context`
  - reconnect rules: reads retry once, mutations retry with the same request id, git streams look up their outcome after a dropped connection
- **The operation table:** one `op.Define[In, Out]` list derives the HTML forms, the JSON API, MCP tools and the CLI.
- **The `store` interface:** typed collections and content-addressed blob files, with the engine still undecided and hidden.
- **The three request flows** step by step, with exact Ruk calls.
- **Events:** Ruk's operation log is the single source of events. rukie keeps a per-repo position in its store, and job ids are derived from the operation id so a replay can't create duplicates.
- **Concurrency:** every goroutine kind with its limit and shutdown path, plus the shutdown order.
- **Caching:** ETags built from object ids, plus five caches and how each is invalidated.
- **Failure handling:** a table covering daemon down, store busy, mirror target 401/429, and more.
- **Speed targets (p95):** 50 ms for a page with warm caches, 150 ms cold.
- **Red-flag self-check**, the 14 Ruk prerequisites (R1–R14), and the commit order (C1–C14).

**Decisions you should sign off on:**
- **CI scope:** v1 would have one container executor, keep only logs as artifacts, and have no matrix builds. That's a scope cut, not a technical limit.
- **Web forms:** rely on Go's `http.CrossOriginProtection` instead of per-page form tokens, so pages stay cacheable.
- **JSON API shape:** `/api/{op}` endpoints alongside the HTML routes.

---

# Architecture sketch A — Typed-client monolith

## 0. What this sketch commits to

- **A1** One static Go binary `rukie`, built with `CGO_ENABLED=0`, in three modes:
  - `serve`: HTTP for HTML, JSON and MCP, git over smart HTTP, an SSH listener, the CI scheduler, and the mirror, webhook, land and event workers.
  - `runner`: CI executor on worker hosts.
  - `<op>`: the generated client CLI.
- **A2** `ruk serve` is the forge-neutral rename of `ruk forgejo serve`. It is a separate Rust process on the same host, reached over a Unix socket. The daemon authenticates callers with `SO_PEERCRED` (`ruk/crates/ruk/src/daemon/ipc.rs`, `authenticate_peer`).
- **A3** Forge metadata lives in one embedded store behind `internal/store`. SQLite and Pebble both fit the interface. PostgreSQL would also fit, but this sketch doesn't cover its network failure modes.
- **A4** Ruk sees every forge actor as one OS principal, the rukie uid. Requests can't name a principal: `ruk/crates/ruk-core/src/coord/policy.rs:1-11`, and `mod.rs:2017` uses `Principal::from_peer_uid(peer.uid)`. Each actor gets its own Ruk session carrying an identity claim, unverified provenance claims, and capabilities narrowed by rukie.
- **A5** Ruk changes R1–R14 (§12) land before or together with the rukie slices that need them. Every gap below names its R-item.

## 1. Process topology

```
 browsers   agents(MCP/JSON)   rukie CLI   git(HTTPS)    git(SSH)        rukie runner (CI hosts)
     \            |                |           |            |             | outbound HTTPS only
      +-----------+----------------+-----------+            |             |
                       :443 (HTTP/1.1+h2, TLS)           :22             |
 ┌─────────────────────────── rukie serve  (uid rukie) ───────────────────────────────┐
 │ web (HTML + /api) · mcp (/mcp) · gitwire (smart HTTP + SSH) · op.Table          │
 │ auth · access · render · store (single writer) · events follower                 │
 │ queue workers: webhook(16) mirror(4) land(4, keyed) ci-plan(2) · ci scheduler    │
 └───────────────┬───────────────────────────────────────────────────┬──────────────┘
                 │ /run/rukie/ruk.sock  (u32 BE len | kind u8 | stream u64 | payload)
 ┌───────────────▼────────────── ruk serve (uid rukie or ruk) ────────────────────┐
 │ Registry → per-repo HostedCoordinator + publication lane; pack server halves;    │
 │ push client (mirror egress → GitHub/GitLab/Forgejo)                              │
 └──────────────────────────────────────────────────────────────────────────────────┘
 /var/lib/rukie/meta/   store engine files              (rukie only)
 /var/lib/rukie/blobs/  content-addressed blobs: CI logs, release assets (rukie only)
 /var/lib/rukie/repos/<id[0:2]>/<repo-id>/   Ruk repositories (ruk owns; rukie never opens)
```

- **Units:**
  - `ruk.service` runs `ruk serve --socket /run/rukie/ruk.sock --roots /var/lib/rukie/repos --allowed-uid <rukie>`.
  - `rukie.service` has `Requires=ruk.service` and `After=ruk.service`. rukie doesn't supervise ruk; systemd restarts each one independently.
  - Repo directories are named by immutable repo id, so a rename never moves a directory.
- **Trust boundaries:**
  1. Internet to rukie: all authentication and authorization happens in rukie.
  2. rukie to ruk: the peer is checked by uid. Ruk trusts rukie to narrow each session's capabilities.
  3. ruk to mirror targets: credentials are passed per call and never stored by ruk (R9).
  4. Runners run untrusted code. They hold only per-lease tokens. The CI signing key never leaves `rukie serve`.

## 2. Go package layout (`cmd/rukie` + 20 internal packages)

| Package | Responsibility (one line) |
|---|---|
| `cmd/rukie` | `main`: calls `app.Main(os.Args)`; nothing else |
| `internal/app` | Config (TOML + env), wiring, `Run(ctx)` supervision, shutdown order |
| `internal/ruk` | Typed Unix-socket client for `ruk serve`: frames, pool, sessions, generated types and methods, git streams |
| `internal/store` | The only persistence module: typed collections, snapshot reads, single-writer updates, blob files |
| `internal/op` | Operation table kernel: `Spec`, `Define`, decoders (form, JSON, MCP args, CLI flags), JSON Schema / OpenAPI 3.1, RFC 9457 problems |
| `internal/auth` | argon2id passwords, TOTP, OIDC redirect flow, web sessions, scoped tokens, admin password reset |
| `internal/access` | Users, orgs, teams, collaborators; `Can(actor, action, resource)`; token scope intersection; mapping to Ruk capabilities and sessions |
| `internal/render` | Markdown (goldmark + bluemonday), highlighting (chroma), diff rendering, binary detection, size limits |
| `internal/web` | `net/http` server: HTML and `/api/{op}` routes derived from `op.Table`, middleware, ETag/304, embedded templates + CSS |
| `internal/mcp` | MCP Streamable HTTP endpoint `/mcp` derived from `op.Table` (rev 2026-07-28: POST-only, no sessions) |
| `internal/cli` | `rukie <op> --flags --json` client and `serve`/`runner`/`admin` subcommands |
| `internal/gitwire` | Smart HTTP (`info/refs`, `git-upload-pack`, `git-receive-pack`) and SSH (`x/crypto/ssh`) piped to `ruk.GitStream` |
| `internal/events` | Per-repo Ruk op-log follower: cursor in store, turns ops into forge events and enqueues jobs |
| `internal/queue` | Durable job queue on `store` (claim with lease, backoff, dead-letter) + bounded worker pools + wake-ups |
| `internal/repo` | Repos: create, fork, delete, settings, browse (tree/file/log/blame/diff), search, releases, stars, watches, explore |
| `internal/review` | Changes and stacks, branch view, comments, approvals, land intents and the land worker |
| `internal/task` | Tasks (replacing issues), claims through Ruk leases |
| `internal/ci` | Pipeline parsing, content-keyed scheduling, runner lease protocol, logs, attestation signing |
| `internal/mirror` | Push mirrors: config, triggers, periodic reconcile, per-host rate buckets, state machine |
| `internal/webhook` | Subscriptions, Standard Webhooks signing, delivery with retries, SSRF guard |
| `internal/inbox` | In-app notifications (no email) |

**Dependency order** (an import is allowed only to the right):
`app` → {`web`, `mcp`, `cli`, `gitwire`} → feature packages {`repo`, `review`, `task`, `ci`, `mirror`, `webhook`, `inbox`, `events`} → {`op`, `access`, `auth`, `render`, `queue`} → {`ruk`, `store`}.

- `ruk` and `store` import no other internal package.
- Each feature package exports `Ops(deps) []op.Op` and, where it has a worker, `Run(ctx) error`.

## 3. The `internal/ruk` client

### 3.1 Wire facts it is built on (current daemon)

| Fact | Source |
|---|---|
| Frame: `u32 BE length` counting kind + stream + payload; kind byte 1..11 (`Open, Accepted, Stdin, StdinEof, Stdout, Stderr, WindowUpdate, Cancel, MergeNotices, Exit, Error`) | `ipc.rs:46-134` |
| Data payload ≤ 64 KiB; control JSON ≤ 1 MiB; stream 0 = handshake and control | `ipc.rs:20-26` |
| Credit window starts at 256 KiB per direction, max 16 MiB | `ipc.rs:29-32` |
| Handshake: `Open` on stream 0 with `HandshakeHello{protocol_major, protocol_minor, profile, object_formats, features}`; answered by `Accepted{…features ∩ ["credits","merge-notices","outcome-lookup"]}`; wrong major is `E_VERSION` | `ipc.rs:43,427-470` |
| Typed call: `InvocationOpen{repo, method, request_id?, args_json?, session_id?}` → `Accepted{stream_id, method, repo_handle}` → `Stdout` chunks of a JSON `ReplyEnvelope` → `Exit{status, code:"OK", outcome, completed_at}`; failure is `Error{code, message}` + `Exit` | `mod.rs:305-370, 1972-2088` |
| Typed reply bytes use outbound credit, and the daemon does **not** wait for more, so replies over the 256 KiB starting window fail `E_LIMIT` | `mod.rs:2346-2373` (`write_data_chunks`) |
| Typed dispatch runs inline, so `Cancel` frames are unread until it finishes | `mod.rs:2016,2072` |
| Git command: `CommandOpen{session_id (ignored), repository, request_id, argv, dir, env, command_digest, stdout_credit, stderr_credit}`; input collected to EOF (≤ 256 MiB), output captured, then streamed | `mod.rs:390-416, 377, 1337-1405` |
| One command blocks its whole connection | `mod.rs:1196-1203` |
| Outcome lookup on stream 0: `{type:"outcome-lookup", invocation_id}` → `{found, exit?}`; the ledger is in memory, capped at 10 000 entries | `mod.rs:374, 479-497, 813-860` |
| Idle connections die after a 60 s frame deadline; at most 128 connections | `mod.rs:44,53,780-805` |
| `Cancel` on a finished stream still produces `Exit(130)` for that stream id | `mod.rs:998-1012` |
| `session.open` returns a bare `SessionOpened{session, reconnect_secret, principal}`, not an envelope | `mod.rs:599-607, 2119-2141` |

### 3.2 Public Go API

```go
package ruk

type Config struct {
	Socket       string
	Profile      string
	MaxConns     int
	IdleTimeout  time.Duration
	DialBackoff  Backoff
	ReplyLimit   int
	StdoutWindow uint64
}

type Backoff struct{ Min, Max time.Duration }

type Client struct {
	cfg    Config
	idle   chan *conn
	slots  chan struct{}
	gate   breaker
	nextID atomic.Uint64
	done   chan struct{}
}

func New(cfg Config) (*Client, error)
func (c *Client) Run(ctx context.Context) error
func (c *Client) Check(ctx context.Context) (SchemaVersion, error)
func (c *Client) Close() error
func (c *Client) Repo(path string) *Repo

type Repo struct {
	c      *Client
	path   string
	handle atomic.Pointer[string]
}

type Session struct {
	ID          SessionID
	secret      ReconnectSecret
	Caps        Capabilities
	IdleExpires time.Time
}

type SessionSpec struct {
	Identity *IdentityClaim
	Claims   map[string]string
	Caps     *Capabilities
}

func (r *Repo) OpenSession(ctx context.Context, spec SessionSpec) (*Session, error)
func (r *Repo) ResumeSession(ctx context.Context, s *Session) (SessionAuthority, error)
func (s *Session) Export(seal func([]byte) ([]byte, error)) ([]byte, error)
func ImportSession(b []byte, open func([]byte) ([]byte, error)) (*Session, error)

type RequestID string

func NewRequestID() RequestID
func DeriveRequestID(parts ...string) RequestID

type FileResult struct {
	Reply   FileReply
	Content io.ReadCloser
}

func (r *Repo) File(ctx context.Context, s *Session, a FileArgs) (*FileResult, error)
func (r *Repo) Log(ctx context.Context, s *Session, a LogArgs) (LogReply, error)
func (r *Repo) Diff(ctx context.Context, s *Session, a DiffArgs) (DiffReply, error)
func (r *Repo) RefList(ctx context.Context, s *Session, a RefListArgs) (RefListReply, error)
func (r *Repo) OpLog(ctx context.Context, s *Session, a OpLogArgs) (OpLogReply, error)
func (r *Repo) AttestList(ctx context.Context, s *Session, a AttestListArgs) (AttestListReply, error)
func (r *Repo) AttestAdd(ctx context.Context, s *Session, id RequestID, a AttestAddArgs) (AttestAddReply, error)
func (r *Repo) LandPrepare(ctx context.Context, s *Session, id RequestID, a LandPrepareArgs) (LandPrepareReply, error)
func (r *Repo) LandExecute(ctx context.Context, s *Session, id RequestID, a LandExecuteArgs) (LandExecuteReply, error)
func (r *Repo) ClaimAcquire(ctx context.Context, s *Session, id RequestID, a ClaimAcquireArgs) (ClaimAcquireReply, error)
func (r *Repo) PushMirror(ctx context.Context, s *Session, id RequestID, a PushMirrorArgs, cred Credential) (PushReply, error)
func (r *Repo) PushReconcile(ctx context.Context, s *Session, id RequestID, a PushReconcileArgs) (PushReconcileReply, error)
func (r *Repo) Search(ctx context.Context, s *Session, a SearchArgs) (SearchReply, error)

type Credential struct {
	URLPrefix string
	Header    string
}

func read[A, R any](ctx context.Context, r *Repo, s *Session, method string, a A) (R, error)
func mutate[A, R any](ctx context.Context, r *Repo, s *Session, method string, id RequestID, a A) (R, error)
```

- **Generated code.** All protocol types and the per-method wrappers are generated: `go generate` runs `go run ./tools/rukgen internal/ruk/schema.json`, producing `types_gen.go` and `methods_gen.go`.
  - The schema is pinned from `ruk model`; today that is `/tmp/ruk-model.json`, schema_version 1.1.
  - `oneOf` variants become structs of `*Variant` pointer fields with `omitempty`.
  - `RefList`, `PushMirror` and `Search` are proposed methods (R2, R9, R12).
- **Schema drift check.** `Check` makes a handshake and calls `schema`. It refuses to start if the major version differs or the method set differs from the pinned schema.

### 3.3 Streaming for pack data

```go
type Service string

const (
	UploadPack    Service = "upload-pack"
	ReceivePack   Service = "receive-pack"
	UploadArchive Service = "upload-archive"
)

type GitRequest struct {
	Service       Service
	GitProtocol   string
	StatelessRPC  bool
	AdvertiseRefs bool
	Session       *Session
	RequestID     RequestID
	MaxInput      int64
}

type GitResult struct {
	ExitCode   int
	Cancelled  bool
	Operations []OperationID
}

type GitStream struct {
	cn      *conn
	id      uint64
	credit  chan uint64
	stdout  *io.PipeReader
	stderr  boundedBuffer
	exit    chan exitOrErr
	release func(reuse bool)
}

func (r *Repo) Git(ctx context.Context, req GitRequest) (*GitStream, error)
func (g *GitStream) Write(p []byte) (int, error)
func (g *GitStream) CloseWrite() error
func (g *GitStream) Read(p []byte) (int, error)
func (g *GitStream) Stderr() []byte
func (g *GitStream) Wait() (GitResult, error)
func (g *GitStream) Close() error
```

- **Writing:** `Write` splits input into `Stdin` frames of at most 64 KiB and blocks on credit. It starts with `CommandAccepted.stdin_credit` and is refilled by the daemon's per-chunk `{direction:"stdin", bytes}` grants.
- **Reading:** a per-stream reader goroutine feeds `Read` from an `io.Pipe`. The client returns stdout credit (`{direction:"stdout", bytes}`) only after the caller has consumed the bytes, so buffering is bounded by the window.
- **Today (R5 not yet landed):**
  - `Git` sends `CommandOpen` with `argv=[service, "--stateless-rpc"[, "--advertise-refs"], "."]`, plus `env=["GIT_PROTOCOL=…"]` and `command_digest = sha256(argv‖dir‖env)`. The exact argv the ruk CLI accepts is an inference.
  - It works for stateless smart HTTP, because the request body is complete before the reply.
  - The daemon buffers both directions.
  - SSH cannot work: the client waits for the ref advertisement before sending, while the daemon waits for end-of-input (inference from protocol v0 shape).
  - `GitResult.Operations` comes from `CommandExitReport.operation_ids`, which is empty today (`mod.rs:441-442`) until R6.
- **After R5:** the same API maps to a full-duplex `git.serve` stream with no client change.

### 3.4 Connections, cancellation, reconnect

- **Pool.**
  - `slots` (cap `MaxConns`, default 64 so it stays under the daemon's 128) bounds the number of connections. `idle` (same cap) holds connections ready for reuse.
  - Each connection carries one invocation at a time, which matches the daemon's inline serving.
  - Idle connections older than `IdleTimeout` (30 s, under the daemon's 60 s frame deadline) are closed by `Run`'s reaper ticker.
- **Deadlines and cancellation.** Every invocation does `stop := context.AfterFunc(ctx, func(){ nc.SetDeadline(time.Unix(1, 0)) })`. When `ctx` ends, blocked reads and writes return at once. The client sends `Cancel` as a best effort and **discards** the connection. It never reuses a cancelled connection, which avoids the stray `Exit(130)` (`mod.rs:998-1012`). Typed dispatch cannot be interrupted today (R4); because mutations are idempotent, abandoning them is safe.
- **Breaker.** When a dial fails (ENOENT, ECONNREFUSED, handshake timeout), the client opens `gate` with exponential backoff (`DialBackoff` 100 ms → 5 s, with jitter). While it's open, callers get `ErrUnavailable` immediately instead of queueing. One probe dial is allowed per backoff step.
- **Retry rules:**
  - **Reads:** a transport error before `Exit` means one retry on a new connection if `ctx` is still alive.
  - **Mutations:** `RequestID` is mandatory (the daemon refuses without one). After a transport error, retry the same session, id and args up to 3 times (50/200/800 ms). Ruk replays the receipt (`ReceiptRef.replayed = true`). Still failing means `ErrUncertain{RequestID}`, and the caller's queue job retries later with the same id while the receipt is still retained.
  - **Session expiry:** `E_FORBIDDEN` "unknown session" or "lacks active authority" leads to `ResumeSession`, and if that fails, `OpenSession` and one retry. Mutations needing the old receipts resume with the stored secret.
  - **Git streams:** a broken connection after `Accepted` means: dial a new connection, send outcome-lookup on stream 0 for `RequestID`, and poll every 250 ms for up to 30 s.
    - `found` returns the recorded exit.
    - Still unknown returns `ErrUncertain`, and `gitwire` runs a landed check (refs equal the pushed new oids).
    - The ledger is lost when the daemon restarts (R14).
  - **Stale repo handles** (`E_NOT_FOUND`/`E_FORBIDDEN` on a handle): clear the handle and reopen by path.

### 3.5 Errors

```go
type Code string

func (c Code) Error() string

const (
	EStale                  Code = "E_STALE"
	ENotFound               Code = "E_NOT_FOUND"
	EForbidden              Code = "E_FORBIDDEN"
	EPolicy                 Code = "E_POLICY"
	EConflicted             Code = "E_CONFLICTED"
	EDivergent              Code = "E_DIVERGENT"
	EClaimed                Code = "E_CLAIMED"
	ELimit                  Code = "E_LIMIT"
	EInvalid                Code = "E_INVALID"
	ECapacity               Code = "E_CAPACITY"
	ECoordinatorUnresponsive Code = "E_COORDINATOR_UNRESPONSIVE"
	EHosted                 Code = "E_HOSTED"
	ECancelled              Code = "E_CANCELLED"
	ECorrupt                Code = "E_CORRUPT"
	EPushRejected           Code = "E_PUSH_REJECTED"
	EPushUncertain          Code = "E_PUSH_UNCERTAIN"
)

type Error struct {
	Code    Code
	Message string
	Method  string
	Repo    string
	Detail  *ErrorDetail
}

func (e *Error) Error() string
func (e *Error) Is(target error) bool

var (
	ErrUnavailable = errors.New("ruk: daemon unavailable")
	ErrUncertain   = errors.New("ruk: outcome uncertain")
	ErrProtocol    = errors.New("ruk: protocol violation")
	ErrTooLarge    = errors.New("ruk: reply exceeds limit")
)
```

[421 more lines in resource. Use :604 to continue]
```

- The full code set comes from `/tmp/ruk-model.json` `error_codes`.
- `Detail` stays nil until R10, because today's `ErrorReport` carries only `code` and `message` (`mod.rs:2414-2440`).

### 3.6 Sessions per actor

- `access.RukSession(ctx, actor, repo) (*ruk.Session, error)` is the only function that opens sessions. The cache key is `(repoID, actorID, capsDigest)`.
- **Session arguments:**
  - `Identity = {name, email}`, where email is `<user>@users.noreply.<host>` (no email delivery exists).
  - `Claims = {"rukie.actor": "u:<id>", "rukie.via": "web|token:<id>|ssh:<key-id>"}`.
  - `Caps = access.CapsFor(actor, repo)`: the actor's repo permission intersected with the token's scopes, expressed as `write_changes`, `land`, `push_refs`, `attest`, `claim_paths`.
- **Repo policy** (written at `repo.init`/`policy.set`, R8/R11): the rukie uid principal gets the full ceiling, `allow_any_identity = true`, `claim_keys = ["rukie.actor", "rukie.via"]` (`policy.rs:25-46`).
- **Task claims** need the holder session to outlive a rukie restart. That session is exported (sealed with an instance key) into the `task` claim row.

## 4. Operation table (the center)

```go
package op

type Kind uint8

const (
	Query Kind = iota + 1
	Command
)

type Spec struct {
	Name    string
	Kind    Kind
	Route   string
	Scope   access.Scope
	View    string
	Agent   bool
	Summary string
}

type Call struct {
	Actor       access.Actor
	Key         string
	IfNoneMatch string
}

type Result[Out any] struct {
	Out      Out
	ETag     string
	Status   int
	Redirect string
}

type Handler[In, Out any] func(ctx context.Context, c *Call, in In) (Result[Out], error)

type Op interface {
	Spec() Spec
	InSchema() Schema
	OutSchema() Schema
	Invoke(ctx context.Context, c *Call, d Decoder) (Outcome, error)
}

func Define[In, Out any](s Spec, h Handler[In, Out]) Op
func Precondition(c *Call, etag string) error

type Decoder interface{ Decode(dst any) error }

func FormDecoder(r *http.Request) Decoder
func JSONDecoder(r *http.Request) Decoder
func ArgsDecoder(args json.RawMessage) Decoder
func FlagsDecoder(args []string) Decoder

type Table struct{ ops []Op; byName map[string]Op }

func NewTable(groups ...[]Op) (*Table, error)
func (t *Table) Lookup(name string) (Op, bool)
func (t *Table) OpenAPI() ([]byte, error)

type Problem struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Detail string            `json:"detail,omitempty"`
	Code   string            `json:"code"`
	Fields map[string]string `json:"fields,omitempty"`
}

func ProblemFrom(err error) Problem
var ErrNotModified = errors.New("op: not modified")
```

- **Input fields.** `In` fields use tags such as `op:"owner,path"`, `op:"title,form,required,max=256"`. Allowed field types are scalars, `encoding.TextUnmarshaler` ids, `time.Time` and `[]string`. Nested structs mark an op JSON-only, which `NewTable` checks at startup.
- **Derived surfaces:**
  - **HTML:** `Query` ops become `GET Spec.Route`; `Command` ops become `POST Spec.Route` returning `303 → Result.Redirect`. Go 1.22+ `ServeMux` patterns.
  - **JSON:** `GET /api/{name}?…` for `Query` (cacheable, ETag) and `POST /api/{name}` for `Command`. OpenAPI 3.1 is generated from the schemas. Errors use `application/problem+json`.
  - **MCP:** `POST /mcp`. `tools/list` returns the ops with `Agent=true`. `tools/call` uses `Invoke` with `ArgsDecoder` and returns `Out` as `structuredContent`. Closing the stream cancels `ctx` (spec: https://modelcontextprotocol.io/specification/draft/basic/transports/streamable-http.md).
  - **CLI:** `rukie <name> --flags [--json]` calls `/api/{name}` with `RUKIE_URL` and `RUKIE_TOKEN`.
- **Idempotency.** `Command` ops need a `Key`:
  - JSON and MCP send `Idempotency-Key`.
  - HTML forms carry a deterministic nonce `HMAC(webSession, opName, resourceETag)`, so pages stay byte-stable and cacheable.
  - Results are kept for 24 h in the `idem` collection. Ruk request ids are `DeriveRequestID(actor, Key, step)`.
- **Cross-site requests.** `net/http`'s `CrossOriginProtection` (Sec-Fetch-Site/Origin check, Go 1.25+, https://go.dev/src/net/http/csrf.go) replaces per-page CSRF tokens. Combined with SameSite=Lax cookies, this keeps zero-JS forms safe.

## 5. Store module

```go
package store

type Config struct{ Dir string }

type DB struct{ eng engine; w chan writeReq; blobs *Blobs }

func Open(ctx context.Context, cfg Config) (*DB, error)
func (db *DB) Run(ctx context.Context) error
func (db *DB) Close() error
func (db *DB) View(ctx context.Context, fn func(*Tx) error) error
func (db *DB) Update(ctx context.Context, fn func(*Tx) error) error
func (db *DB) Blobs() *Blobs

type Index[T any] struct {
	Name   string
	Key    func(*T) []byte
	Unique bool
}

type Collection[T any] struct{ name string; key func(*T) []byte; idx []Index[T] }

func Define[T any](name string, key func(*T) []byte, idx ...Index[T]) *Collection[T]
func (c *Collection[T]) Get(tx *Tx, key []byte) (T, error)
func (c *Collection[T]) GetBy(tx *Tx, index string, key []byte) (T, error)
func (c *Collection[T]) Scan(tx *Tx, index string, prefix []byte, p Page) ([]T, Cursor, error)
func (c *Collection[T]) Put(tx *Tx, v *T) error
func (c *Collection[T]) PutIfAbsent(tx *Tx, v *T) (bool, error)
func (c *Collection[T]) Delete(tx *Tx, key []byte) error

type Blobs struct{ dir string }

func (b *Blobs) Put(ctx context.Context, r io.Reader, max int64) (Digest, int64, error)
func (b *Blobs) Open(d Digest) (*os.File, error)
func (b *Blobs) Append(name string, p []byte) (int64, error)

var (
	ErrNotFound = errors.New("store: not found")
	ErrBusy     = errors.New("store: busy")
	ErrConflict = errors.New("store: unique conflict")
)
```

Every engine must honor this contract:
- `View` reads a consistent snapshot.
- `Update` runs serializably on a single writer goroutine with group commit (≤ 2 ms linger or 256 transactions, one fsync per batch). Its closure may do only store work: no `ruk`, no network.
- Enqueueing onto the writer fails with `ErrBusy` after 100 ms when the channel (cap 1024) is full.
- Sorted listings (explore by stars or by update time) are maintained indexes, not ad-hoc queries.

## 6. Request flows

### (a) Render a file at a revision — `GET /alice/proj/src/main/cmd/x.go`

1. **Route.** `web` matches op `repo.file`: `Query`, Route `/{owner}/{repo}/src/{rev}/{path...}`, View `file`. Middleware runs in order:
   - request id
   - in-flight semaphore (512)
   - `CrossOriginProtection`
   - `auth.Actor(r)`: cookie, `Bearer` token, or anonymous
2. **Decode.** `FormDecoder` fills `repo.FileIn{Owner, Repo, Rev, Path}`.
3. **Authorize.** `access.RepoFor(ctx, actor, owner, name, access.Read)` does `store.View` then `RepoByName` then a visibility and permission check. It returns `repo.Ref{ID, Path, Public}`.
4. **Resolve the revision.**
   - A full hex oid is used as `commit` directly, with `immutable = true`.
   - Otherwise `refs.Resolve(repoID, "refs/heads/"+rev)`. On a miss it calls `ruk.RefList` (R2) and fills the refs cache, which is keyed by `ViewInfo.view`.
5. **Find the entry.** Look up `entries.Get(commit, path)`. On a miss, call `ruk.File{at:{head:{oid: commit}}, path}` (R2) and store `(blobOid, size, executable, kind)`. The content stays attached for step 7.
6. **Precondition.** `op.Precondition(c, W/"f.<blobOid>.<buildID>.<viewerKey>")` returns `ErrNotModified`, and `web` writes **304** without touching Ruk content.
   - The viewer key is the first 8 bytes of SHA-256(actor id, permission version, theme).
   - JSON and raw responses use the strong ETag `"<blobOid>"`.
7. **Render** with `render.Blob(name, oid, size, content)`:
   - NUL in the first 8 KiB: binary stub with a raw link.
   - Up to 1 MiB: chroma-highlighted HTML using CSS classes.
   - `.md`: goldmark, then bluemonday.
   - Over 1 MiB: plain text.
   - Over 8 MiB: raw only.
   - Fragments are cached by `(blobOid, renderer, version)`.
8. **Respond.** `html/template` executes into a pooled buffer and is written with `Content-Length`.
   - Ref-relative URLs get `Cache-Control: private, no-cache`.
   - Oid-addressed raw URLs get `public, max-age=31536000, immutable` for public repos and `private, max-age=3600` otherwise.

Ruk round trips: 0 with warm caches, at most 2 cold.

### (b) Land a change with required attestations — `POST /alice/proj/changes/{change}/land`

1. **Decode.** Op `review.land` (`Command`) gets `In{Owner, Repo, Change ChangeID, Target string, Head Oid}`. `Head` is the head the user saw.
2. **Authorize.** `access.Can(actor, LandTo(target))` uses branch rules from the store. The rules list the required attestation kinds; trusted signers are configured into the Ruk policy (R8). Then `sess = access.RukSession(...)` with `Caps.land = [target]`.
3. **Prepare.** `ruk.LandPrepare(sess, Derive(actor, Key, "prepare"), {change:{change, head:{git:Head}}, target})`. With R8, `remote` is absent, meaning this repo. The reply is `{plan, candidate, base, expires_at, requirements}` (`/tmp/ruk-model.json` `LandPrepareReply`).
4. **Pre-check attestations.** `ruk.AttestList(sess, {subject: candidate})` is checked against `requirements` and the trusted signer fingerprints. Only `live` records count; `superseded` and `ambiguous` do not.
5. **Decide.**
   - All met: go to step 7.
   - A `ci/*` requirement missing: call `ci.Ensure(repo, candidate, kinds)`. If a passing run exists for the same `(tree, pipeline digest, image digest)`, the CI signer attests `candidate` immediately. Otherwise a `ci.plan` job is queued.
   - A human requirement missing (e.g. `review/approve`): return **409** problem `requirements-unmet` with the list.
6. **Record the intent.** One `store.Update` writes `LandIntent{id, repo, change, target, plan, candidate, base, expiresAt, state: Awaiting, actorSessionRef, attempt: 0}`. Reply **303** to the change page ("landing: waiting for ci/…"), or **202** with the intent JSON.
   - When the events follower (§6c step 4) sees an `attest.add` op for a subject with waiting intents, it queues `land.attempt{intent}`.
7. **Execute.** The land worker is keyed so that only one runs per `(repo, target)`. It calls `ruk.LandExecute(sess, Derive(intent, "exec", attempt), {plan, candidate})`.
   - `published`: state becomes `Landed`.
   - `E_STALE` (base moved, plan expired, or candidate mismatch): re-prepare with `attempt+1`, giving a new candidate that CI must attest again. At most 3 attempts, then state `Failed` plus an inbox notice to the actor.
   - `E_NOT_FOUND` on the plan: if the target tip equals `candidate`, state becomes `Landed` (an earlier attempt's reply was lost). Otherwise re-prepare.
   - `ErrUnavailable` or `ErrUncertain`: the job retries with the **same** request id.
8. **Fan out.** The ref delta on `target` reaches the events follower, which emits webhooks `change.landed` and `ref.updated`, marks mirrors dirty, starts post-land CI, and notifies watchers' inboxes.

**CI attestation** (inside `ci`): `AttestStatement{subject: candidate, kind: "ci/<pipeline>/<job>", result: "pass"|"fail", signed_at}` is signed with an ed25519 SSH key in namespace `ruk-attest` (sshsig via `x/crypto/ssh`), then sent with `ruk.AttestAdd(serviceSession, Derive(job, attempt), …)`. The private key stays in `rukie serve` (`AttestAddArgs` doc in `/tmp/ruk-model.json`).

### (c) Push over smart HTTP, triggering mirror, webhook and CI

1. **Advertise.** `GET /alice/proj.git/info/refs?service=git-receive-pack`:
   - `gitwire` authenticates with `Basic` (username + token; passwords are refused for git) and checks `access.Can(actor, Push)`.
   - It calls `ruk.Git({ReceivePack, AdvertiseRefs: true, StatelessRPC: true})` and writes the `# service=git-receive-pack` pkt-line prefix followed by the stream.
2. **Push.** `POST /alice/proj.git/git-receive-pack`:
   - Handles `Content-Encoding: gzip` and caps input with `http.MaxBytesReader` (default 2 GiB; 256 MiB is enforced by the daemon until R5).
   - Calls `g := ruk.Git({ReceivePack, StatelessRPC: true, Session: actorSession, RequestID: NewRequestID()})`.
   - One goroutine copies the body into `g` and then calls `CloseWrite`. The handler copies `g` to `w` with `http.NewResponseController(w).Flush()` after each read.
   - `res, err := g.Wait()`.
   - `refs/for/<branch>` commands are turned into Ruk changes inside Ruk (R6). The `remote:` sideband lines carry change ids.
3. **Kick.** `events.Kick(repoID)` is a non-blocking send. `res.Operations` is only a hint; the op log is the authority.
4. **Follow the op log.** An events follower calls `ruk.OpLog(svcSession, {after: cursor, include_deltas: true, page.limit: 100})` (R7). For each op, one `store.Update`:
   - appends `Event{repo, op, seq, type, refs, changes}`
   - for each matching webhook: `queue.Put(webhook.deliver, id = H(hook, op, seq))`
   - for each mirror whose ref selection matches: `mirror.dirty_since = op.committed_at` and `queue.Put(mirror.sync, id = H(mirror))` (at most one pending)
   - for each updated branch or change head: `queue.Put(ci.plan, id = H(repo, commit, trigger))`
   - inbox rows for watchers
   - `cursor = op.id`
   
   Replaying after a crash produces the same ids, so `PutIfAbsent` deduplicates.
5. **Webhook worker.**
   - Claims a job with a 60 s lease, builds `rukie.event.v1` JSON, and adds Standard Webhooks headers: `webhook-id` = job id, `webhook-timestamp`, `webhook-signature: v1,<base64 HMAC-SHA256(secret, id.ts.body)>` (https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md).
   - POSTs with a 10 s timeout. A dialer `Control` hook blocks loopback, private and link-local addresses unless the admin allowlists them.
   - 2xx: done. 410: disable the hook. 429/5xx/timeout: retry with exponential backoff and jitter, 8 attempts over about 24 h (inference), then dead-letter and an inbox notice.
6. **Mirror worker.**
   - Waits on a per-host token bucket, then calls `ruk.PushMirror(svcSession, Derive(mirror, dirty_since), {remote: {url}, mode: exact|ff_only, select: all|default_and_tags|globs[]}, cred)` (R9, reusing `push_mirror_updates` in `ruk-git/src/transport/mirror.rs`). The result is a `PushReply{plan, state, refs}`.
   - `confirmed`: clear `dirty` if `dirty_since` hasn't advanced.
   - `rejected` in ff_only mode: state `diverged`.
   - `uncertain`: `push.reconcile{plan}` after 30 s.
   - Periodic reconcile: a ticker (default 15 min, minimum 5 min per mirror) enqueues `mirror.sync` for every mirror; dedup ids absorb duplicates.
7. **CI.**
   - The `ci.plan` worker reads `.rukie/ci.toml` at the commit (`ruk.File`) and computes `jobKey = H(treeOid, jobDef, imageDigest)`.
   - A passing result already stored for `jobKey` means it signs and adds the attestation with no run. Otherwise it puts a `CIJob`.
   - Runners call op `ci.lease`: a 30 s long-poll that returns `{job, lease (60 s), fetchToken}`.
   - The runner fetches through upload-pack into a persistent per-repo cache, runs the steps in podman, streams `ci.log` chunks (appended to `Blobs`), and sends `ci.heartbeat` every 15 s and `ci.complete{result}` at the end.
   - The scheduler signs the statement and calls `attest.add`. The follower then sees the attest op, which kicks land intents (b6) and sends webhook `ci.completed`.

## 7. Concurrency model

| Goroutine(s) | Count / bound | Wake-up | Shutdown path |
|---|---|---|---|
| `http.Server` accept + per-connection | ≤ 2048 conns (limited listener); handlers ≤ 512 in flight (semaphore → 503) | — | `Shutdown(ctx 25s)` then `Close` |
| SSH accept loop | 1 | — | `ln.Close()` |
| SSH connections | ≤ 256 (semaphore) | — | ctx cancel → `conn.Close()`; wait group |
| `ruk` stream reader | 1 per live `GitStream` (≤ `MaxConns` = 64) | frames | terminal frame or connection close; `Close` joins |
| `ruk` idle reaper | 1 | 10 s ticker | `Client.Close` closes `done` |
| `store` writer | 1; queue cap 1024 → `ErrBusy` | channel | `Close`: drain the channel, commit, close engine |
| `events` dispatcher + followers | 1 + 8 | dirty set (map keyed by repo, bounded by repo count) + `chan struct{}` cap 1 | ctx cancel; a follower finishes its current page (≤ 100 ops) |
| `queue` pools | webhook 16, mirror 4, land 4 (keyed by `(repo, target)`), ci-plan 2 | `chan struct{}` cap 1 per kind + timer at the next `due_at` | ctx cancel → stop claiming; in-flight jobs get 10 s, then are abandoned (lease expires, job is idempotent) |
| CI scheduler + lease reaper | 1 + 1 | 5 s ticker | ctx |
| Tickers: mirror reconcile, session/idem/receipt GC | 1 each | ticker | ctx |
| MCP / CLI / runner long-poll | per HTTP request, under the 512 handler limit; long-polls ≤ 30 s, count ≤ registered runners | — | `http.Server.Shutdown` |

- **Queues** live in the store; channels are only wake-up signals, sent without blocking.
- **Backpressure:**
  - More than 10 000 pending deliveries per hook disables the hook with a notice.
  - At most one pending sync per mirror.
  - At most 1 000 queued CI jobs per repo; past that, older superseded change revisions are cancelled first.
- **Shutdown** (SIGTERM, 40 s budget, under systemd's `TimeoutStopSec = 45`):
  1. Stop the listeners: HTTP `Shutdown` and SSH close.
  2. Cancel the worker ctx.
  3. Stop the events followers.
  4. `ruk.Client.Close()`: cancel and discard in-flight connections.
  5. `store.Close()`: flush the writer.
- `app.Run` uses `errgroup.WithContext`. The first component error triggers the same ordered shutdown.

## 8. Caching

| What | Key | Value / bound | Invalidation |
|---|---|---|---|
| Refs | `repoID` | `{view OperationID, map[ref]Oid}`, LRU 10k repos | follower on any op with ref deltas; own mutation replies (`MutationOutcome.heads`) |
| Tree entries | `(commitOid, path)` | `{kind, oid, size, exec}`, LRU 1M entries | never (content-addressed) |
| Rendered blobs | `(blobOid, renderer, rendererVersion)` | HTML fragment, LRU 256 MiB | never; a version bump changes the key |
| Rendered diffs | `(oldTree, newTree, pathFilter)` | fragments, LRU 128 MiB | never |
| Ruk sessions | `(repoID, actorID, capsDigest)` | `*ruk.Session`, LRU 50k | Ruk forbids → resume or reopen |

**ETags:**
- blob, raw, JSON: `"<oid>"`
- tree: `"t.<treeOid>"`
- log page: `"l.<viewOp>.<cursorDigest>"`
- change page: `"c.<headOid>.<attestViewOp>"`
- HTML: `W/"<content-etag>.<buildID>.<viewerKey>"`

Handlers compute the ETag **before** any heavy work (`op.Precondition`). Authorization always runs before a cache lookup, so cache keys never grant access.

## 9. Failure paths

| Failure | Detected as | User sees | Recovery |
|---|---|---|---|
| Ruk daemon down | dial ENOENT/ECONNREFUSED → `ruk.ErrUnavailable` | HTML 503 page; JSON problem `…/problems/backend-unavailable` with `Retry-After: 5`; git HTTP 503; SSH `fatal:` exit 128; MCP `isError` | breaker backoff; `/readyz` fails; workers pause claims without counting attempts |
| Daemon restart mid-call | EOF | reads transparent; mutations transparent (receipt replay) | git stream outcome-lookup misses (ledger lost) → landed check → `ok` or `ng` to client; follower catches up from cursor |
| Daemon shedding load | `E_CAPACITY`, `E_LIMIT` (admission), `E_COORDINATOR_UNRESPONSIVE` | 503 `Retry-After: 2` | worker backoff |
| Protocol version skew | `E_VERSION` or schema mismatch in `Check` | startup refuses; at runtime 503 | operator upgrade |
| Typed reply too large | `E_LIMIT` (256 KiB window) or `ErrTooLarge` | 413 for that view, with a raw link | client clamps `page.limit`; fixed by R3 |
| Store busy | `store.ErrBusy` | mutations 503 `Retry-After: 1`; reads unaffected | job retried without counting an attempt; follower cursor not advanced |
| Store I/O or disk full | wrapped error | 500; `/readyz` fails | operator; nothing half-written (atomic `Update`) |
| Mirror 401/403 | `E_PUSH_REJECTED` + remote status (R9 detail) | mirror page `auth_failed`; inbox to repo admins; webhook `mirror.failed` | paused until `mirror.update` sets a new credential or `mirror.sync` is run manually |
| Mirror 429/503 | remote status + `Retry-After` (R9) | `throttled` badge | `next = now + max(RetryAfter, backoff)`; host bucket rate halved for 10 min |
| Mirror diverged (ff_only) | `PushRefStatus.rejected` | `diverged` | never forced; owner chooses `exact` or fixes the target |
| Mirror uncertain | `PushState.uncertain` | `syncing` | `push.reconcile`: `equal_new` → confirmed, `equal_old` → resend, `moved` → diverged |
| Op log pruned past cursor | `cursor < oldest_retained` | events carry `gap: true` | full ref diff against the stored ref snapshot synthesizes the missing events |
| CI runner lost | lease expiry (60 s) | job `retrying` | requeue with `attempt+1` (infra failures, up to 3); attest request id per `(job, attempt)` |
| Land base moved / plan expired | `E_STALE` | intent `re-preparing` | bounded re-prepare (3) |
| OIDC provider down | token or discovery error | "sign-in provider unavailable" | password + TOTP still works; existing sessions unaffected |
| Webhook target blocked (SSRF) | dialer `Control` refusal | delivery `blocked` | admin allowlist |

## 10. Performance budgets (server-side p95, handler only)

- **File, tree and change pages:** 50 ms warm, 150 ms cold (≤ 2 Ruk round trips, blob ≤ 256 KiB).
- **JSON queries:** 30 ms warm.
- **304 revalidation:** 5 ms.
- **Command overhead excluding Ruk mutation time:** 20 ms.
- **Push to webhook enqueued:** 1 s.
- **Push to mirror sync started:** 5 s.

All are measured by a per-op histogram, `rukie_op_seconds{op,surface}`.

## 11. Red-flag self-check

- **Hidden complexity**
  - *Op table reflection across four surfaces:* this is the main risk. Field types are constrained, `NewTable` checks every op against every surface at startup, there are no per-surface overrides except `View`, and one table-driven test covers op × surface.
  - *Op log as outbox across two databases:* the only producer is the follower. The cursor and the jobs commit in one `Update`, and job ids are deterministic. Request handlers never write events.
  - *Land orchestration:* an explicit `LandIntent.State` enum (`Awaiting → Executing → Landed | Failed`, plus `Repreparing`) with one transition table.
- **Leaky abstraction**
  - Ruk sessions and capabilities appear only through `access.RukSession`. Ruk ids (`Oid`, `ChangeID`, `OperationID`) are deliberately part of the domain vocabulary.
  - Daemon limits (one invocation per connection, 256 KiB typed reply, buffered commands) are hidden by the pool and paging clamps. SSH cannot be hidden, so R5 is a hard prerequisite.
  - `store` exposes no SQL, so join-like listings must be maintained indexes. Risk: indexes multiply. Mitigation: the list of collections and indexes is reviewed as one file.
- **Unbounded scope**
  - CI "more resilient than GitHub/GitLab" is bounded for v1 to:
    - content-keyed deduplication
    - lease and heartbeat
    - infrastructure retries
    - signed attestations that survive restarts
    - one podman executor
    - logs only
  
  Matrix fan-out, artifact and cache services, and autoscaling are **excluded pending user sign-off**.
  - R1–R14 are a Ruk program in their own right. Core flows are blocked on R2, R3, R5, R7 and R8.
- **Premature generality**
  - The engine-neutral `store` is justified only by the open store decision.
  - `queue` has four consumers (webhook, mirror, land, ci).
  - No plugin system, no multi-node, no PostgreSQL failure handling.
- **Missing error handling (remaining)**
  - Git stream outcomes across daemon restarts (R14).
  - CI signing-key rotation, which needs a policy trusted-signer update (R8).
  - `signed_at` is a claim, not an ordering authority (`AttestStatement` doc), so ordering never relies on it.
  - Cache timing could reveal that a blob exists in another private repo. Low impact; documented.
- **Structural**
  - Single host, vertical scaling only. The ceiling is the daemon defaults: 128 connections, 16 blocking permits (`mod.rs:44-47`).
  - Ruk sees one principal, and actor claims are unverified (`ProvenanceClaims`), so the audit trail depends on rukie.

## 12. Ruk prerequisites

- **R1** Rename to forge-neutral `ruk serve`, profile `rukie`. Drop the Forgejo-shaped aliases and hook-script assumptions.
- **R2** Hosted reads for `file`, `log`, `diff`, `show`, `blame`, `query`, plus a new `ref.list`/`ref.get`. `RevisionRef.ref{name}`. Today only `op.log`, `claim.list` and `attest.list` are served (`ruk-core/src/coord/hosted.rs:614-640`).
- **R3** Typed replies wait for credit and accept a reply window in `InvocationOpen`. Align `MAX_IPC_CONTROL_BYTES` (1 MiB, `ipc.rs:23`) with the schema's `control_frame_bytes` (16 MiB). Large bodies go as `ByteSource::Streamed`.
- **R4** Per-stream tasks (multiplexing) so `Cancel` reaches running invocations.
- **R5** Full-duplex `git.serve{service, protocol, stateless, session}` wired to `serve_upload_pack` and `serve_receive_pack`, which are already generic over `Read`/`Write` (`ruk-git/src/transport/serve.rs:199,514`). No input collection (`mod.rs:377`) and no full output capture (`mod.rs:1370`).
- **R6** Native `refs/for/<branch>` diversion into Ruk changes (Change-Id trailer mapping), replacing the `proc-receive` script (`proc_receive.rs`). Git streams are bound to the actor session. Exit reports fill `operation_ids`.
- **R7** `op.log` with an ascending `after` cursor and per-op ref deltas and attestation subjects (`OperationEntry` today has only id, method, time, parent, session, undoes).
- **R8** Hosted landing: optional `remote` (meaning this repo), hosted `land.execute`, a `[land] requirements` + trusted signers policy section and a `policy.set` method (`prepare.rs:63-65,3432-3435`), and deterministic candidates for a given (head, base).
- **R9** Hosted `push`/`push.reconcile`, an ephemeral credential kept out of the digest and receipts, `push.mirror{mode, select}`, and error detail with remote HTTP status and `Retry-After`.
- **R10** Error frames carry the full `ErrorReply` (hints, fixes, detail). Failed and cancelled outcomes are distinguished.
- **R11** `repo.init{object_format}`, `repo.delete`, `repo.fork{from}` with object sharing.
- **R12** Lightweight `search{pattern, literal|regex, path_glob, at, page}`: bounded scan, results cached by tree oid, no heavy index.
- **R13** Claims on opaque subjects (`task:<id>`) plus `claim.renew`. Today claims are path-only with TTL ≤ 3600 s.
- **R14** A durable or receipt-backed outcome for git streams; the current ledger is in memory with 10k entries (`mod.rs:374`).

## 13. Commit sequence (on Gitea history, every commit builds)

- **C1** `internal/ruk` (frames, pool, typed calls, generated types, fake-daemon tests) and `tools/rukgen`.
- **C2** `internal/store`, once the store decision lands. The interface above is fixed now.
- **C3** `internal/op`, `internal/web` skeleton, `internal/app`, and a new `cmd/rukie`. The old `main.go` is untouched.
- **C4** `auth` and `access`.
- **C5** `repo` browsing (needs R2, R3).
- **C6** `gitwire` HTTP (needs R1, R6).
- **C7** SSH (needs R5).
- **C8** `events`, `queue`, `webhook` (needs R7).
- **C9** `review` and landing (needs R8).
- **C10** `ci`.
- **C11** `mirror` (needs R9).
- **C12** `task` (R13), `inbox`, releases, search (R12), explore.
- **C13** `mcp` and `cli`.
- **C14** Cutover:
  - Delete `routers/`, `models/`, `modelmigration/`, `services/`, `modules/`, `templates/`, `web_src/`, `options/`, the old `cmd/*`, the JS toolchain files, and `main.go`.
  - Rename the module from `gitea.dev` (`rukie/go.mod:1`).
  - Run `go mod tidy`. Go 1.27, toolchain go1.27.2, from `rukie/go.mod:3-5`.

### Critical Files for Implementation
- `ruk/crates/ruk/src/daemon/mod.rs` — payload structs, the inline typed and command dispatch, outcome ledger, limits; the Go client mirrors these.
- `ruk/crates/ruk/src/daemon/ipc.rs` — frame codec, credit windows, handshake, peer auth.
- `/tmp/ruk-model.json` — the protocol schema `tools/rukgen` generates the Go types from; vendored as `internal/ruk/schema.json`.
- `ruk/crates/ruk-core/src/coord/hosted.rs` and `ruk/crates/ruk-core/src/coord/land.rs` — hosted read and mutation coverage and land semantics behind R2, R7, R8.
- `ruk/crates/ruk-git/src/transport/serve.rs` and `ruk/crates/ruk-git/src/transport/mirror.rs` — pack server halves (R5) and mirror ref mapping (R9).

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Add the step that creates human at…

[advisor] <advisory advisor="default" severity="nit" guidance="weigh, don't blindly obey"> Drop the refs-cache entry in `gitwire`…

[advisor] <advisory advisor="muse" severity="nit" guidance="weigh, don't blindly obey"> Fix note shape. Resend sketch deltas only…

[advisor] <advisory advisor="overlycritic" severity="nit" guidance="weigh, don't blindly obey"> Fix the layering conflict: `op` i…

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Make `ruk.Git` create a new `Req…

[advisor] <advisory advisor="qwen" severity="concern" guidance="weigh, don't blindly obey"> Do not retry an in-flight mutation on…

[advisor] <advisory advisor="qwen" severity="concern" guidance="weigh, don't blindly obey"> Correct the §2 dependency rule. The s…

[advisor] <advisory advisor="qwen" severity="nit" guidance="weigh, don't blindly obey"> Make `gitwire` invalidate the refs cache …

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Remove the features the request d…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Replace claim_keys with allow_any…

[advisor] <advisory advisor="stickler" severity="nit" guidance="weigh, don't blindly obey"> Fix §6(b) step 3: write the change he…