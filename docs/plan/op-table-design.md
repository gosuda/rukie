## Disposition of review findings

Not part of the file.

| Finding | Disposition |
|---|---|
| Keep to the six requested files; no speculative extras (overlycritic, t−2) | **Accepted.** The design now has exactly six files (§2). No extra files. |
| Use `gitea.dev` from go.mod in every import (minimax, default, stickler, overlycritic) | **Partly accepted.** `rukie/go.mod:1` is `module gitea.dev`, which I verified. The settled decision is still `github.com/gosuda/rukie`, so imports keep that path. The design now makes the module rename the first commit, before any `internal/op` code exists (§0). |
| Ignore the muse test note | **Ignored.** |
| Huma skips anonymous embedded fields, so cursor, limit, and If-Match will not populate (overlycritic, t−3) | **Accepted in effect.** Reserved parameters are explicit tagged fields, and nothing depends on embedded-field parameter handling (§3.2). I did not re-check Huma's recursion, per the instruction to stop API archaeology. |
| Store idempotency results only after execution starts; do not record 403/412/422 (minimax, t−3) | **Accepted in part.** The claim starts at handler entry. Authorization, validation, and precondition failures happen before that and are never recorded. Deterministic 4xx produced by execution are recorded, as Stripe does. Recording "every response, even 403" is rejected, because it would replay an authorization failure after the grant is fixed. |
| Negotiate once and rewrite Accept; the Transformer has no content-type argument; Huma's Negotiate is a second parser (minimax, t−4) | **Accepted, and it removes the envelope.** Huma serves JSON only. HTML and Markdown are rendered from the typed output after Huma returns (§5.4). There is no Transformer, no Format override, and no envelope. |
| Register should return `error`; Table owns duplicate checks; add compile-time assertions (overlycritic, t−5) | **Accepted for the first half.** `Register` returns `error`, and duplicates are checked there. Compile-time claims are limited to what Go can check (§16.1). I did not add assertions that check nothing. |
| `Op` should not copy research §7 (ID, Effect, `*In`, RawSchema) (glm-flash, qwen, minimax, stickler ×2, t−8 to t−10) | **Rejected as stale.** The current design already uses `Name`, `Kind`, `Handler func(ctx, Principal, In) (Out, error)`, `ETag`, and `Pagination`, with no `ID`, `Effect`, or `RawSchema`. |
| MCP tool names must replace dots (glm-flash, t−8) | **Rejected as already done.** The design maps `issue.create` to `issue_create`. |
| Drop SSE, webhooks, schema dump (glm-flash, t−8) | **Partly accepted.** SSE, webhooks, and `Emits` are already dropped. `ops.list` is kept only as the data source for the reference page, which is documentation, not a fifth surface (§15). |
| Add an auth mode for `anonymous` and `bootstrap` ops (default, t−9) | **Rejected for the table.** Those kinds are Ruk-protocol methods (`schema`, `session.open`). rukie opens its own Ruk session in the daemon client and does not expose them. Public and sign-in ops use the anonymous principal (§3.6). |
| Keep Huma registration in `mount_huma.go`; do not replace it with a custom dispatcher (overlycritic, t−7) | **Accepted.** Huma stays the JSON and OpenAPI path. Negotiation and HTML sit around it. |
| Choose where negotiation sits; Huma's Format cannot key views (minimax, t−7) | **Accepted.** Negotiation sits outside Huma, and views are keyed by op name (§9, §5.4). |
| Add a lightweight documented adapter for reserved headers (overlycritic) | **Not needed.** Explicit fields plus reflection-cached indices work and need no adapter. |
| Verify `transformAndWrite` applies Transformers (minimax, t−6) | **Obsolete.** No Transformer is used. |
| Declare kin-openapi's fate (stickler, t−10) | **Accepted.** It is removed. Huma is the only OpenAPI generator (§19). |
| Confirm the write path; the research file is a write-failure message (stickler, t−10) | **Acknowledged.** I have no write tool, and plan mode is read-only. The full design is returned as the final message for the orchestrator to save. The research file begins with a write-failure preamble before the report. |
| Stop API archaeology and draft now (overlycritic, t−6) | **Accepted.** No further tool calls were made for this revision. |

---

# op-table-design.md: rukie operation table and derived surfaces

**Revision 2.** Date 2026-10-20. Design only. Nothing is implemented.

**Labels.** [V] verified in source this session. [D] design decision. [I] inference. [?] open; needs a test or spike before reliance.

**Inputs.** `research-agent-api-codegen.md` §0–§7 (the file opens with a write-failure preamble; the report follows). Its §7 is used only for the mapping in §3.3. Huma v2.39.1 source. go-sdk v1.8.0 source. `rukie/routers/api/v1/api.go` (guards, lines 240–811). `rukie/models/auth/access_token_scope.go` (lines 17–27). `/tmp/ruk-model.json` (30 `error_codes` with `exit_status`; 47 `methods`: 1 anonymous, 2 bootstrap, 34 mutation, 10 read). `/tmp` is not durable; copy the file into the repo before the fidelity test depends on it.

---

## 0. Changes from revision 1

1. **Six files only** (§2). No speculative extras.
2. **Module path.** The settled module path is `github.com/gosuda/rukie`. `rukie/go.mod:1` still reads `module gitea.dev` [V]. The module rename is therefore the first commit, before any `internal/op` file is created. Import paths in this document use the target path. Before the rename, substitute `gitea.dev`.
3. **`Register` returns `error`.** It checks one op's shape and its duplicates against the table. `Table.Validate` re-runs the full set over all specs.
4. **Reserved parameters are explicit tagged fields** (cursor, limit, If-Match, Idempotency-Key). Nothing depends on Huma walking embedded structs (§3.2).
5. **Huma serves JSON only.** HTML and Markdown are rendered from the typed `Out` after Huma returns. There is no Transformer, no Format override, and no envelope (§5.4, §9).
6. **Negotiation happens once**, before authentication. `Accept` is rewritten to `application/json` before dispatch, so Huma never negotiates (§5.1).
7. **Idempotency claim starts at handler entry.** Only executed requests are recorded. Authorization, validation, and precondition failures are never recorded (§7).
8. **Unknown top-level body keys are rejected in the outer layer**, from the derived property set. This replaces the JSON format override (§5.1 step 9).
9. **kin-openapi is removed** from the module (§19).

---

## 1. Decisions

1. One generic table, `Op[In,Out]`, is the only place an operation is declared [D].
2. Huma v2 (`humago` on `net/http.ServeMux`) is the only HTTP and OpenAPI layer, imported only by `mount_huma.go` [D].
3. Every surface reduces to one canonical argument object (`Args`, which is the MCP input-schema instance) and then to one `*http.Request`. MCP invokes the server in-process through the same `http.Handler`. The CLI sends the same request over the network. Nothing bypasses the chain [D].
4. Huma is the parser, validator, and JSON writer. Authorization runs in a per-operation Huma middleware, which sees path parameters and runs before parsing [V: `Register` wraps the parse closure in `op.Middlewares.Handler`].
5. Form posts become JSON in the outer layer, before Huma runs [D].
6. Response headers, status rewrites (304, 303), and the HTML and Markdown renderings are applied by the outer layer when it flushes [D].
7. Negotiation is ours, once, before authentication. Huma's `Negotiate` never sees a choice [D].
8. The CLI uses stdlib `flag` with a generated help tree, not cobra [D].
9. Exit codes reuse Ruk's classes 2–5, with deliberate deviations (§4.4) [D].
10. HTML CSRF is an HMAC double-submit token (`_csrf`), plus Origin and Fetch-Metadata checks, both enforced in the outer layer [D].
11. Anonymous is a principal with fixed grants, not a special case. Every mutation has a scope [D].
12. Webhooks, SSE, and `Emits` are out of scope and deferred to a separate design [D].

---

## 2. Package layout

Module: `github.com/gosuda/rukie` (target). Package: `internal/op`. Six files, as requested.

| File | Contents | Imports |
|---|---|---|
| `table.go` | `Op[In,Out]`, `Kind`, `Concurrency`, `Guard`, `Params`, `Pagination[Out]`, `CLI`, `NoBody`, `Spec`, `Table`, `Register`, `Table.Validate`; `Scope`, `Scopes`, `Principal`; `Code`, `Problem`, `Error`, `FieldError`, Ruk map; `Document`, `Schema`, `DocumentFromOpenAPI`, `Localize`; compile-time assertions | stdlib |
| `negotiate.go` | `Media`, `Offered`, `Negotiate`; `Views` registry keyed by op name; `PageView[Out]`; generic Markdown renderer; generic HTML error page; reference page and problem page renderers | stdlib, `html/template` |
| `mount_huma.go` | **The only Huma import.** `Config`, `Server`, `Mount`, `ServeHTTP` pipeline, `Response`, auth, CSRF and Origin, body buffering, unknown-key check, idempotency (store interface, digest, claims), audit row, finish, Huma binding, `authorize` middleware, `invoke`, `problemBody`, `NewError` override, `SetCookie`, `SetHeader`, `WithTrusted`, `Invoke` | stdlib, `huma/v2`, `huma/v2/adapters/humago`, `huma/v2/conditional` if needed |
| `derive_mcp.go` | `Args`, `(Args).ToRequest`, `Invoker`, `LocalInvoker`, `RemoteInvoker` (shared with the CLI); schema flattening; `ToolName`; annotations; toolsets; `NewMCPServer`; `ToolResult`; `checkMCP` | `go-sdk/mcp`, `google/jsonschema-go` |
| `derive_cli.go` | command tree; `Parse`; `Run`; exit table; retry policy; `checkCLI` | stdlib `flag`, `net/url` |
| `derive_html.go` | field derivation; `DecodeForm`; form render contract; PRG and error mapping; GET filter forms; `checkForms` | stdlib |

Huma is imported only by `mount_huma.go`. go-sdk is imported only by `derive_mcp.go`. The derivations work on `Document` and `Spec` and do not need a running server. If `mount_huma.go` grows too large, splitting it is a later decision, not part of this design.

Domain operations live in `internal/api/<domain>.go`, each registered from `internal/api/register.go`. Guard constructors (`RepoReader`, `RepoWriter`, and so on) live in `internal/access`.

---

## 3. Operation table (`table.go`)

### 3.1 Types and registration

```go
package op

import (
	"context"
	"net/http"
	"reflect"
)

type Kind uint8

const (
	KindRead Kind = iota + 1
	KindMutation
)

type Concurrency uint8

const (
	ConcurrencyNone Concurrency = iota
	ConcurrencyIfMatchOptional
	ConcurrencyIfMatchRequired
)

// Params reads a path parameter. Huma supplies ctx.Param; MCP and the CLI
// reach the same values through the substituted path.
type Params func(name string) string

// Guard checks a resource relation. It runs after the scope check and before
// input parsing, so it may read path parameters only (§10.3).
type Guard func(ctx context.Context, p Principal, path Params) error

type Pagination[Out any] struct {
	Default int                                  // 100
	Max     int                                  // <= 1000
	Next    func(out Out) (cursor string, more bool)
}

type CLI struct {
	Path       []string // overrides the name-derived command path
	Positional []string // path parameters as positional arguments, in order
}

type NoBody struct{}

type Op[In, Out any] struct {
	Name        string // dotted, 2–3 segments: "issue.create", "repo.hook.create"
	Kind        Kind   // KindRead | KindMutation
	Method      string // GET for KindRead; POST|PUT|PATCH|DELETE for KindMutation
	Path        string // canonical resource URL, ServeMux syntax: "/{owner}/{repo}/issues/{number}"
	Scope       Scope  // required for every op; public reads use ScopePublicRead
	Guards      []Guard
	Summary     string // 1..120 characters
	Description string
	Problems    []Code // op-specific failure codes; the framework set is added (§4.7)
	Destructive bool   // mutations only; MCP destructiveHint
	Idempotent  bool   // naturally idempotent: MCP idempotentHint; CLI may retry without a key
	Concurrency Concurrency
	ETag        func(out Out) string // strong validator; nil = none; the framework quotes it
	Pagination  *Pagination[Out]     // nil = unpaginated; In must declare Cursor and Limit
	Location    func(out Out) string // Location header; HTML PRG target
	Status      int                  // 0 = default: POST 201, DELETE 204, otherwise 200
	Hidden      bool                 // served, but omitted from MCP and the CLI
	CLI         CLI
	Handler     func(ctx context.Context, p Principal, in In) (Out, error)
}

// Spec is the Huma-free, type-erased description that every derivation reads.
type Spec struct {
	Name, Summary, Description string
	Kind                       Kind
	Method, Path               string
	Scope                      Scope
	Problems                   []Code
	Destructive, Idempotent    bool
	Concurrency                Concurrency
	HasETag, HasLocation       bool
	PageDefault, PageMax       int
	Status                     int
	Hidden                     bool
	CLI                        CLI
	In, Out                    reflect.Type
	BodyKeys                   []string // top-level JSON body properties; filled from the OpenAPI document
	fields                     fieldIndex
}

type fieldIndex struct {
	cursor, limit, ifMatch, idempotencyKey []int // nil when the In type lacks the field
}

type Table struct {
	specs  []Spec
	byName map[string]int
	byRoute map[string]int // "METHOD /path/template" with placeholders normalized
	binds  []func(*mount) error
}

func NewTable() *Table

// Register validates one op against its own shape and against the table's
// existing names and routes, then records it.
func Register[In, Out any](t *Table, o Op[In, Out]) error

// Validate re-runs every per-op check over all specs and the table-wide rules
// (§16.2). Tests call it directly; Mount calls it first.
func (t *Table) Validate() error

// Specs returns a copy sorted by Name. Derivations use this, never the typed Op.
func (t *Table) Specs() []Spec
```

`Register` builds `specOf(o)`, caches the field indices, and stores a closure `func(m *mount) error { return mountOp(m, o, spec) }`. Go forbids type parameters on methods, so `Register` is a package function. The closure captures the typed op, so the request path does no type erasure.

### 3.2 Reserved parameters are explicit fields

Huma parameter discovery is never relied on for embedded structs. Each `In` declares the reserved fields it uses, as ordinary tagged fields:

```go
type ListIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	State  string `query:"state" enum:"open,closed,all" default:"open"`
	Cursor string `query:"cursor" doc:"Opaque cursor from a Link rel=next header"`
	Limit  int    `query:"limit" minimum:"1" maximum:"1000" doc:"Page size"`
}

type CreateIn struct {
	Owner          string `path:"owner"`
	Repo           string `path:"repo"`
	IdempotencyKey string `header:"Idempotency-Key" maxLength:"255" doc:"Retry key; honoured for 24 h"`
	Body           struct {
		Title string `json:"title" minLength:"1" maxLength:"255"`
		Body  string `json:"body,omitempty"`
	}
}

type UpdateIn struct {
	Owner   string `path:"owner"`
	Repo    string `path:"repo"`
	Number  int    `path:"number"`
	IfMatch string `header:"If-Match" doc:"ETag of the representation being replaced"`
	Body    struct {
		Title *string `json:"title,omitempty"`
	}
}
```

`Register` requires, by reflection:
- `Pagination != nil` ⇒ `Cursor` (`query:"cursor"`, string) and `Limit` (`query:"limit"`, int).
- `Concurrency != None` ⇒ `IfMatch` (`header:"If-Match"`, string).
- Every mutation ⇒ `IdempotencyKey` (`header:"Idempotency-Key"`, string).
- Every `path:"x"` tag ⇒ a `{x}` placeholder in `Path`, and the reverse.

Field indices are cached in `Spec.fields`. The per-request cost is `reflect.Value.FieldByIndex` on a known index. The cost is some repetition across `In` types, which is accepted in exchange for not depending on how Huma walks embedded structs.

### 3.3 Mapping from research §7

| Research §7 | This design | Reason |
|---|---|---|
| `ID`, operationId `issue-create` | `Name`; operationId equals `Name` | one identifier on every surface |
| `Effect Read/Write/Destructive` | `Kind` plus `Destructive` | matches Ruk's `kind` vocabulary |
| `Scope string` | `Scope` (closed type) | compile-time closed set |
| `Requires []Guard` taking `*In` | `Guards []Guard` taking `Params` and `Principal` | guards run before parsing |
| `RequiresIfMatch bool` | `Concurrency` | three states |
| `Problems []Problem` | `Problems []Code` | the catalogue carries status, retryability, and exit |
| `Emits` | dropped | webhooks are a separate design |
| `CLI`, `MCP`, `UI` specs | `CLI` overrides; MCP derived with no override; UI through `Views` | views are keyed by op name |
| `RawSchema` | deferred (§20, item 9) | Huma cannot express Ruk `$defs`, `oneOf`, or `const` |
| `Handler func(ctx, *In) (*Out, error)` | `Handler func(ctx, Principal, In) (Out, error)` | the assignment |
| `Idempotent bool` | `Idempotent` (semantic) | Idempotency-Key is honoured on every mutation regardless |

### 3.4 Usage

```go
if err := op.Register(t, op.Op[issue.CreateIn, issue.Out]{
	Name:     "issue.create",
	Kind:     op.KindMutation,
	Method:   http.MethodPost,
	Path:     "/{owner}/{repo}/issues",
	Scope:    op.ScopeIssueWrite,
	Guards:   []op.Guard{access.RepoWriter(units.Issues), access.NotArchived()},
	Summary:  "Open an issue",
	Problems: []op.Code{op.CodeArchived},
	Location: func(o issue.Out) string { return o.Path },
	Handler:  issues.Create,
}); err != nil {
	return err
}

if err := op.Register(t, op.Op[issue.ListIn, issue.ListOut]{
	Name:       "issue.list",
	Kind:       op.KindRead,
	Method:     http.MethodGet,
	Path:       "/{owner}/{repo}/issues",
	Scope:      op.ScopeIssueRead,
	Guards:     []op.Guard{access.RepoReader(units.Issues)},
	Summary:    "List issues",
	Pagination: &op.Pagination[issue.ListOut]{Default: 100, Max: 1000, Next: issue.ListNext},
	Handler:    issues.List,

[1265 more lines in resource. Use :1810 to continue]
	Handler:    issues.List,
}); err != nil {
	return err
}
```

### 3.5 Scopes and principals

```go
// Scope is a closed set. Values exist only as the package vars below; Scope{}
// is the zero value and fails Validate.
type Scope struct{ id uint8 }

var (
	ScopePublicRead    = Scope{1}
	ScopeAccountCreate = Scope{2} // anonymous: sign-up
	ScopeSessionCreate = Scope{3} // anonymous: sign-in
	ScopeSessionWrite  = Scope{4} // sign-out, session management
	ScopeUserRead      = Scope{5}
	ScopeUserWrite     = Scope{6}
	ScopeRepoRead      = Scope{7}
	ScopeRepoWrite     = Scope{8}
	ScopeRepoAdmin     = Scope{9}
	ScopeIssueRead     = Scope{10}
	ScopeIssueWrite    = Scope{11}
	ScopeReviewRead    = Scope{12} // Ruk changes and stacks, branch views
	ScopeReviewWrite   = Scope{13}
	ScopeTaskRead      = Scope{14}
	ScopeTaskWrite     = Scope{15}
	ScopeTaskClaim     = Scope{16} // Ruk lease
	ScopeOrgRead       = Scope{17}
	ScopeOrgWrite      = Scope{18} // includes teams
	ScopeCIRead        = Scope{19}
	ScopeCIWrite       = Scope{20}
	ScopeReleaseWrite  = Scope{21}
	ScopeMirrorAdmin   = Scope{22}
	ScopeWebhookAdmin  = Scope{23}
	ScopeInboxRead     = Scope{24}
	ScopeInboxWrite    = Scope{25}
	ScopeTokenAdmin    = Scope{26}
	ScopeSiteAdmin     = Scope{27}
)

func (s Scope) String() string // "issue:write"
func (s Scope) Valid() bool

type Scopes uint64 // bit i set means Scope{i} is granted; ids < 64

func (s Scopes) Has(x Scope) bool

type PrincipalKind uint8

const (
	PrincipalAnonymous PrincipalKind = iota + 1
	PrincipalHuman
	PrincipalAgent
	PrincipalService
)

type Principal struct {
	UserID   int64
	Kind     PrincipalKind
	TokenID  int64  // 0 for sessions and anonymous
	OnBehalf int64  // accountable human; equals UserID except for agents
	Grants   Scopes // sessions: every scope the user may hold; tokens: explicit subset
}

func (p Principal) Can(s Scope) bool

// anonymousGrants = {ScopePublicRead, ScopeSessionCreate, ScopeAccountCreate}.
```

Gitea categories (`access_token_scope.go:17-27`: nine categories, each with Read or Write) map as follows:

| Gitea category | Replacement |
|---|---|
| Repository | `repo:*`, `review:*`, `task:*`, `ci:*` (split by domain) |
| Issue | `issue:read`, `issue:write` (tasks are separate) |
| Organization | `org:read`, `org:write` (teams under `org:write`) |
| User | `user:read`, `user:write` |
| Notification | `inbox:read`, `inbox:write` |
| Admin | `site:admin`, `token:admin` |
| Package, ActivityPub, Misc | dropped (cut surfaces) |
| — (new) | `session:*`, `account:create`, `public:read`, `release:write`, `mirror:admin`, `webhook:admin` |

Gitea's `tokenRequiresScopes(category...)` (`api.go:271`) was coarse. Here the scope is exactly one value per op.

### 3.6 Anonymous ops and Ruk's kinds

Ruk's `anonymous` and `bootstrap` method kinds (`schema`, `session.open`, `session.resume`) describe Ruk's own protocol. rukie opens and resumes its Ruk sessions inside the daemon client and never exposes those methods, so they are not table ops [D]. rukie's public and sign-in ops are ordinary ops whose principal is the anonymous principal (§3.5). The authentication step therefore never runs for them in a way that rejects them.

---

## 4. Error model (`table.go`)

### 4.1 Codes

`Code` is a closed set, built the same way as `Scope`.

```go
type Code struct{ slug string }

var (
	CodeInvalid          = Code{"invalid"}
	CodeValidation       = Code{"validation"}
	CodeUnauthorized     = Code{"unauthorized"}
	CodeForbidden        = Code{"forbidden"}
	CodeNotFound         = Code{"not-found"}
	CodeMethodNotAllowed = Code{"method-not-allowed"}
	CodeNotAcceptable    = Code{"not-acceptable"}
	CodeUnsupportedMedia = Code{"unsupported-media"}
	CodeTooLarge         = Code{"too-large"}
	CodeConflict         = Code{"conflict"}
	CodeLeaseConflict    = Code{"lease-conflict"}
	CodeInFlight         = Code{"idempotency-in-flight"}
	CodeStale            = Code{"stale"}
	CodeArchived         = Code{"archived"}
	CodePreconditionReq  = Code{"precondition-required"}
	CodeKeyReused        = Code{"idempotency-key-reused"}
	CodeUnsupported      = Code{"unsupported"}
	CodeLimitExceeded    = Code{"limit-exceeded"}
	CodeShallowHistory   = Code{"shallow-history"}
	CodeRateLimited      = Code{"rate-limited"}
	CodeInternal         = Code{"internal"}
	CodeCorrupt          = Code{"corrupt"}
	CodeUnavailable      = Code{"unavailable"}
	CodeRemoteUnknown    = Code{"remote-unknown"}
	CodeRemoteMoved      = Code{"remote-moved"}
	CodePushRejected     = Code{"push-rejected"}
)

type Problem struct {
	Code      Code
	Status    int
	Title     string
	Retryable bool
	Exit      int
	RukCodes  []string
}

func Problems() []Problem // the catalogue, ordered by slug
func LookupProblem(c Code) (Problem, bool)
func FromRuk(ruk string) (Code, bool) // the table in §4.3
```

### 4.2 Catalogue

| Code | HTTP | Exit | Retryable | Ruk codes | Notes |
|---|---|---|---|---|---|
| invalid | 400 | 65 | no | `E_INVALID` | malformed JSON, bad cursor |
| validation | 422 | 65 | no | — | field errors from Huma or the form decoder |
| unsupported-media | 415 | 65 | no | — | |
| too-large | 413 | 65 | no | — | body over `MaxBody` |
| unauthorized | 401 | 3 | no | — | |
| forbidden | 403 | 3 | no | `E_FORBIDDEN`, `E_POLICY` | |
| not-found | 404 | 1 | no | `E_NOT_FOUND` | also used for unreadable private resources |
| method-not-allowed | 405 | 1 | no | — | `Allow` is taken from the table |
| not-acceptable | 406 | 1 | no | — | no offered media type matches |
| conflict | 409 | 2 | no | `E_CONFLICTED`, `E_DIVERGENT`, `E_DIRTY`, `E_UNDO_CONFLICT`, `E_WORKSPACE_APPLYING`, `E_WORKSPACE_RECOVERY` | |
| lease-conflict | 409 | 3 | no | `E_LEASE_LOST`, `E_CLAIMED` | |
| idempotency-in-flight | 409 | 3 | yes | — | `Retry-After: 1` |
| stale | 412 | 3 | no | `E_STALE` | refetch, then retry |
| archived | 423 | 3 | no | — | from the `NotArchived` guard (`api.go:808`) |
| precondition-required | 428 | 3 | no | — | `If-Match` missing |
| idempotency-key-reused | 422 | 3 | no | `E_REQUEST_REUSED` | same key, different request |
| unsupported | 422 | 1 | no | `E_OBJECT_FORMAT`, `E_UNSUPPORTED_LFS`, `E_UNSUPPORTED_GIT_COMMAND`, `E_VERSION`, `E_HOSTED` | |
| limit-exceeded | 422 | 1 | no | `E_LIMIT` | |
| shallow-history | 422 | 4 | no | `E_SHALLOW` | |
| rate-limited | 429 | 4 | yes | — | `Retry-After` |
| internal | 500 | 1 | yes | `E_IO`, `E_CANCELLED` | never leaks detail |
| corrupt | 500 | 5 | no | `E_CORRUPT` | |
| unavailable | 503 | 4 | yes | `E_OFFLINE`, `E_CAPACITY`, `E_COORDINATOR_UNRESPONSIVE` | |
| remote-unknown | 502 | 4 | yes | `E_PUSH_UNCERTAIN` | push outcome unknown; reconcile |
| remote-moved | 409 | 4 | no | `E_REMOTE_MOVED` | refetch first |
| push-rejected | 409 | 4 | no | `E_PUSH_REJECTED` | |

HTTP statuses and exit classes for Ruk-originated codes are semantic inferences [I]. Ruk's model carries only `code` and `exit_status`, with no descriptions. Confirm with the Ruk maintainers before the table is frozen.

### 4.3 Ruk to rukie

| Ruk | Code | Ruk exit | rukie exit |
|---|---|---|---|
| E_STALE | stale | 3 | 3 |
| E_LEASE_LOST | lease-conflict | 3 | 3 |
| E_CLAIMED | lease-conflict | 3 | 3 |
| E_REQUEST_REUSED | idempotency-key-reused | 3 | 3 |
| E_FORBIDDEN | forbidden | 3 | 3 |
| E_POLICY | forbidden | 3 | 3 |
| E_CONFLICTED | conflict | 2 | 2 |
| E_DIVERGENT | conflict | 2 | 2 |
| E_DIRTY | conflict | 2 | 2 |
| E_UNDO_CONFLICT | conflict | 2 | 2 |
| E_WORKSPACE_APPLYING | conflict | 2 | 2 |
| E_WORKSPACE_RECOVERY | conflict | 2 | 2 |
| E_REMOTE_MOVED | remote-moved | 4 | 4 |
| E_PUSH_REJECTED | push-rejected | 4 | 4 |
| E_PUSH_UNCERTAIN | remote-unknown | 4 | 4 |
| E_OFFLINE | unavailable | 4 | 4 |
| E_SHALLOW | shallow-history | 4 | 4 |
| E_CORRUPT | corrupt | 5 | 5 |
| E_OBJECT_FORMAT | unsupported | 1 | 1 |
| E_UNSUPPORTED_LFS | unsupported | 1 | 1 |
| E_UNSUPPORTED_GIT_COMMAND | unsupported | 1 | 1 |
| E_VERSION | unsupported | 1 | 1 |
| E_HOSTED | unsupported | 1 | 1 |
| E_LIMIT | limit-exceeded | 1 | 1 |
| E_NOT_FOUND | not-found | 1 | 1 |
| E_IO | internal | 1 | 1 |
| E_CANCELLED | internal | 1 | 1 |
| E_INVALID | invalid | 1 | **65** |
| E_COORDINATOR_UNRESPONSIVE | unavailable | 1 | **4** |
| E_CAPACITY | unavailable | 1 | **4** |

Bold rows are the only deviations from Ruk's exit codes. `E_INVALID` is an input-data error. The other two are transient. Every row keeps the original Ruk code in the `ruk_code` extension member.

### 4.4 Exit table (CLI)

| Exit | Meaning |
|---|---|
| 0 | success, including 303 followed and 304 |
| 1 | general: not found, internal, unsupported, limit, not acceptable, method not allowed |
| 2 | conflict needing a human or merge decision |
| 3 | authority and precondition: unauthorized, forbidden, stale, lease, archived, key reuse, If-Match missing |
| 4 | transient or remote: unavailable, rate limited, remote state unknown or moved, push rejected, shallow |
| 5 | integrity: corrupt |
| 64 | CLI usage error (bad flags or `--input`); never sent to the server (sysexits `EX_USAGE`) |
| 65 | server rejected input: invalid, validation, too large, unsupported media (sysexits `EX_DATAERR`) |

### 4.5 Error values

```go
type FieldError struct {
	Location string // "body.title", "query.limit", "header.If-Match", "path.number"
	Message  string
	Value    any
}

type Error struct {
	Code       Code
	Detail     string
	Fields     []FieldError
	RetryAfter time.Duration
	RukCode    string
	cause      error
}

func Errorf(c Code, format string, args ...any) *Error
func Wrap(c Code, cause error, format string, args ...any) *Error
func (e *Error) Error() string
func (e *Error) Unwrap() error

var _ error = (*Error)(nil)
```

A handler returns `error`. A `*Error` keeps its code. Any other error becomes `internal` and is logged with the request ID. A code that is neither declared on the op nor in the framework set is also converted to `internal`, logged, and reported by the conformance test. This keeps the OpenAPI error list accurate.

### 4.6 Wire format (RFC 9457)

```http
HTTP/1.1 412 Precondition Failed
Content-Type: application/problem+json

{
  "type": "/problems/stale",
  "title": "Precondition failed",
  "status": 412,
  "detail": "If-Match does not match the current revision",
  "instance": "/acme/widgets/issues/7",
  "code": "stale",
  "retryable": false,
  "request_id": "01J…",
  "ruk_code": "E_STALE",
  "errors": [{"location":"header.If-Match","message":"stale","value":"\"r41\""}]
}
```

- `type` is a relative URI reference (RFC 9457 §4.1). It resolves to `problem.show` (§15).
- `code`, `retryable`, `request_id`, `ruk_code`, and `errors` are extension members (RFC 9457 §3.2). `errors` keeps Huma's `{location, message, value}` shape.
- Finish always sets `Content-Type: application/problem+json` on JSON error responses [D]. It does not rely on Huma's `ContentTypeFilter`.

### 4.7 Framework-added problems

Every op automatically declares: `unauthorized`, `forbidden`, `not-acceptable`, `unsupported-media`, `rate-limited`, `internal`, `unavailable`, and `method-not-allowed` when the path has siblings. It also declares `invalid` and `validation` when the In type has inputs, `too-large` for mutations, `not-found` when the path has parameters, `precondition-required` and `stale` when Concurrency is set, and `idempotency-key-reused` and `idempotency-in-flight` for mutations. `Problems` adds only op-specific codes.

---

## 5. Request pipeline (`mount_huma.go`)

### 5.1 Steps

| # | Step | Reads | Failure |
|---|---|---|---|
| 0 | `recover` | panic | 500 `internal`; abort any claim |
| 1 | request ID | `X-Request-Id` (≤128 printable) or generated | — |
| 2 | `serveGit` | path `/{owner}/{repo}.git[/…]` | Smart HTTP (Ruk); not a table op |
| 3 | `route` | `mux.Handler(r)` pattern | 404, or 405 with `Allow` from the table |
| 4 | `negotiate` | `Accept`, `Content-Type`, op's views | 406, or 415 for urlencoded without a form |
| 5 | `authenticate` | `Authorization`, `Cookie` | 401 on a bad bearer; anonymous otherwise |
| 6 | `buffer` | unsafe methods only | 413 over `MaxBody` |
| 7 | `checkOriginCSRF` | unsafe method plus `Cookie` | 403 |
| 8 | `admitRate` | principal ID, or IP for anonymous | 429 + `Retry-After` |
| 9 | `adaptRequest` | urlencoded body; JSON top-level keys | 422 (`DecodeForm` errors, or unknown keys) |
| 10 | `idempotencyLookup` | mutation plus key | replay; 409 in flight; 422 reused |
| 11 | `dispatch` | `r` with `Accept: application/json` | Huma: authorize (403, 428), parse and validate (400, 422), handler wrapper (claim: 409, 422; then `Handler`) |
| 12 | `finish` | captured response, state | — |

Steps 4 and 5 are ordered as shown so that a 401 can be rendered in the negotiated media. Negotiation depends only on the op's views, not on the principal [D].

Requests with a trusted in-process principal (§5.2, `WithTrusted`) skip step 5. No request header can set a trusted principal or surface.

Step 9 applies to JSON mutation bodies only. It decodes the top-level keys into a map and rejects any key not in `Spec.BodyKeys` with 422 `validation`. Nested objects keep Huma's default behaviour, and that limit is documented [?, §20 item 10].

### 5.2 Shape

```go
type Surface uint8

const (
	SurfaceHTTP Surface = iota + 1
	SurfaceMCP
)

type Config struct {
	PublicOrigin  string        // "https://forge.example"; Origin checks compare against this
	Authenticator Authenticator
	Idempotency   IdempotencyStore
	Audit         Auditor
	Limiter       Limiter
	CSRFKey       []byte        // HMAC key, ≥32 bytes
	MaxBody       int64         // default 1 MiB
	MaxRecorded   int           // default 64 KiB
	Clock         func() time.Time
	Git           http.Handler  // Ruk-backed Smart HTTP; not part of the table
	Views         *Views
	Title, Version string
}

type Server struct {
	cfg    Config
	mux    *http.ServeMux
	api    huma.API
	routes map[string]*routeEntry // key: ServeMux pattern "GET /{owner}/{repo}/issues"
	allow  map[string][]string    // path template -> methods, for 405
	doc    *Document
}

type routeEntry struct {
	spec *Spec
}

func Mount(mux *http.ServeMux, t *Table, c Config) (*Server, error)
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)

type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Invoke runs one request through the full chain in-process. The principal and
// surface come from the caller's context, which only in-process code can set.
func (s *Server) Invoke(ctx context.Context, r *http.Request) (*Response, error)
func WithTrusted(ctx context.Context, p Principal, surf Surface) context.Context

// SetCookie and SetHeader are applied at finish, in the outer layer.
func SetCookie(ctx context.Context, c *http.Cookie)
func SetHeader(ctx context.Context, key, value string)
```

### 5.3 Request state

```go
type reqState struct {
	spec        *Spec
	media       Media
	principal   Principal
	surface     Surface
	requestID   string
	path        string
	query       url.Values
	ifNoneMatch string
	csrf        string     // expected _csrf value, for form views
	body        []byte     // buffered request body, unsafe methods only
	raw         url.Values // submitted form values, for re-render
	digest      [32]byte
	claim       *Claim     // set when execution starts
	replay      *Recorded  // set when step 10 finds a completed record
	out         any        // typed Out, set by the handler wrapper
	etag, location, link string
	header      http.Header
	failure     *Error     // set by a pre-dispatch step
}

type stateKey struct{}

func stateFrom(ctx context.Context) *reqState
```

One `reqState` per request, owned by one goroutine. It is passed through `r.Context()`, which the outer layer sets before calling the mux.

### 5.4 Finish (step 12)

Inputs: a captured response `(status, header, body)`, which comes from Huma, from a replay, or from an outer-layer failure; and the state. The output is the response written to the client.

1. **304.** GET or HEAD, status 200, `etag` set, and `If-None-Match` matches: write 304 with the `ETag` header and no body. This is checked before rendering.
2. **Media.**
   - **JSON:** pass through the captured status and body.
   - **HTML or Markdown, status ≥ 400:** decode the captured problem JSON. For HTML mutations with 422 and a form view, re-render the form with `ViewCtx.Input` and `Errors` at status 422. Otherwise render the error page or the Markdown error.
   - **HTML, status < 400, mutation:** 303 with `Location` (or the request path when empty) and no body.
   - **HTML, status < 400, read:** render the page view with `st.out`.
   - **Markdown, status < 400:** render generic Markdown from `st.out`. For a replay, `st.out` is nil, so the recorded JSON is decoded to `any` and rendered the same way.
   - A view error becomes a 500 problem in the requested media.
3. **Headers.** `ETag` (quoted), `Location`, `Link`, `st.header`, and every queued cookie. `Vary: Accept, Cookie`. `X-Content-Type-Options: nosniff`. For HTML only: CSP `default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; script-src 'none'`, plus `Referrer-Policy: same-origin`. `Content-Type` is `text/html; charset=utf-8`, `text/markdown; charset=utf-8`, or `application/json`, as applicable.
4. **Idempotency.** If `st.claim` is set, see §7.3.
5. **Audit.** Mutations use one row written in the same transaction as idempotency completion when there is a claim, or synchronously otherwise. Reads go through a bounded queue drained by one goroutine that exits on `ctx.Done()` after a final flush.
6. **Flush** the status, headers, and body.

---

## 6. Authentication, authorization, CSRF

### 6.1 Authenticator

```go
var ErrUnauthenticated = errors.New("unauthenticated")

type Authenticator interface {
	Bearer(ctx context.Context, secret string) (Principal, error) // "rkt_…"; hash compare
	Session(ctx context.Context, id string) (Principal, error)
}
```

Step 5, in order:
1. `Authorization: Bearer rkt_…` → `Bearer`. Failure → 401 with `WWW-Authenticate: Bearer error="invalid_token"`.
2. Cookie `__Host-rukie_session` → `Session`. Failure → anonymous, and clear the cookie.
3. Otherwise `Principal{Kind: PrincipalAnonymous, Grants: anonymousGrants}`.
4. A trusted in-process principal (§5.2) skips 1–3.

Git Smart HTTP accepts Basic auth with a token as the password, through the same `Authenticator`. Its handler is outside the table.

### 6.2 CSRF and Origin

- Cookie `__Host-rukie_csrf`: 32 random bytes, base64url. Attributes: `Secure; SameSite=Strict; Path=/`. Set on the first HTML GET that lacks it.
- Form field `_csrf` = `base64url(HMAC-SHA256(CSRFKey, cookieValue))`, compared with `hmac.Equal`.
- Rule: an unsafe request that carries any `Cookie` header must:
  - have `Origin` equal to `PublicOrigin`, or, if `Origin` is absent, `Sec-Fetch-Site` of `same-origin` or `none`; and
  - for urlencoded bodies, present a valid `_csrf`.
- Bearer-only requests are exempt. Step 7 enforces this.

### 6.3 Authorization (inside Huma; §10.3)

For every op, in order:
1. `Principal.Can(op.Scope)`. Failure → 403.
2. Each guard. Failure → the guard's `*Error`. Guards return `not-found` for resources the principal cannot read, and `forbidden` only for readable resources where the action is not allowed.
3. `ConcurrencyIfMatchRequired`, method not POST, and no `If-Match` → 428.

Authorization failures are written by the middleware, before the wrapper runs. Nothing is recorded for them (§7.3).

---

## 7. Idempotency

### 7.1 Store

```go
type Outcome uint8

const (
	OutcomeNew Outcome = iota + 1
	OutcomeReplay
	OutcomeInFlight
	OutcomeReused
)

// Recorded is the canonical result of an executed request. Body is always the
// JSON representation, whatever the request's media type.
type Recorded struct {
	Status   int
	Location string
	ETag     string
	Link     string
	Body     []byte
}

type Claim struct {
	ID    string // ULID; seeds the Ruk request_id (§7.5)
	Owner int64
	Key   string
}

type IdempotencyStore interface {
	// Lookup is read-only. It is step 10.
	Lookup(ctx context.Context, owner int64, key string, digest [32]byte, now time.Time) (Outcome, *Recorded, error)
	// Begin inserts an in-flight claim atomically. It returns OutcomeNew, or
	// OutcomeInFlight or OutcomeReused when a concurrent request won. Called from
	// the handler wrapper, after authorization and validation.
	Begin(ctx context.Context, owner int64, key string, digest [32]byte, now time.Time) (Claim, Outcome, error)
	// Complete stores the result and the audit row in one transaction.
	Complete(ctx context.Context, c Claim, rec Recorded, a AuditRow) error
	// Abort removes an in-flight claim so a retry re-executes.
	Abort(ctx context.Context, c Claim) error
}

const IdempotencyTTL = 24 * time.Hour

func Digest(method, requestURI string, body []byte) [32]byte
```

The store is a table with `UNIQUE(owner, key)`, a `digest` column, a state column (in-flight or completed), and `expires_at = created + 24h`. A sweeper goroutine deletes expired rows and exits on `ctx.Done()`.

### 7.2 Digest

`Digest` is SHA-256 over `method "\n" RequestURI "\n" compact(body)`. `compact` is `json.Compact` for valid JSON and the raw bytes otherwise. Form submissions are digested after `adaptRequest`, so a form retry and a JSON retry with the same payload match. Key order inside JSON is not normalized [?, §20 item 11].

### 7.3 Claim protocol

| Step | Stored state | Digest equal? | Result |
|---|---|---|---|
| 10 Lookup | none or expired | — | proceed (`OutcomeNew`) |
| 10 Lookup | completed | yes | replay the `Recorded` through finish; no dispatch |
| 10 Lookup | completed | no | 422 `idempotency-key-reused` |
| 10 Lookup | in flight | yes | 409 `idempotency-in-flight` |
| 10 Lookup | in flight | no | 422 `idempotency-key-reused` |
| wrapper `Begin` | none (raced) | — | claim; execute |
| wrapper `Begin` | any other | — | problem; no execution; **nothing recorded** |

Completion rules, applied in finish when a claim exists:
- **Recorded.** Execution started and the status is below 500 and not retryable. This includes deterministic 4xx from the handler, such as `stale`, `lease-conflict`, `forbidden` raised inside the handler, and `archived`. Stored with `Recorded.Body` as the JSON body.
- **Aborted.** A panic, any status ≥500, or any retryable problem. The client may retry, and the retry re-executes. Ruk's `request_id` (§7.5) keeps that safe at the Ruk layer.
- **Too large.** Body above `MaxRecorded` is aborted, so the retry re-executes.
- **Never stored.** Steps 3–9 and the authorization middleware. A corrected retry with the same key therefore runs normally. Callers must use a fresh key after a recorded 4xx they intend to correct. Tests cover this (§17).

### 7.4 Audit row

```go
type AuditRow struct {
	EventID        string // ULID
	RequestID      string
	At             time.Time
	PrincipalID    int64
	PrincipalKind  PrincipalKind
	TokenID        int64
	OnBehalf       int64
	Surface        Surface
	Op             string
	Method, Path   string
	Scope          string
	Decision       string // "allow" | "forbid:scope" | "forbid:guard" | "unauthenticated" | "rejected:<step>"
	IdempotencyKey string
	IfMatch        string
	Status         int
	Problem        string
	RukRequestID   string
	LatencyMicros  int64
}

type Auditor interface {
	Append(ctx context.Context, a AuditRow) error
}
```

Every request gets a row, including rejected ones, so the audit shows every decision.

### 7.5 Ruk linkage

Ops that call Ruk pass `request_id = "rukie:" + Claim.ID`. A retried mutation reaches Ruk with the same request ID. Ruk's `E_REQUEST_REUSED` (exit 3) maps to `idempotency-key-reused`. Whether Ruk deduplicates on `request_id` alone or on `request_id` plus payload is unverified [?, §20 item 8].

---

## 8. ETag, If-Match, and pagination

- **ETag.** `Op.ETag(out)` provides the validator. Finish sets it, quoted and strong, on reads and on the representation a mutation returns.
- **If-Match.** For `ConcurrencyIfMatchRequired`, the handler reads `in.IfMatch` and compares it inside its own store transaction (compare-and-swap on the revision). It must not compare outside the transaction. A mismatch returns `Errorf(CodeStale, …)` with `FieldError{Location: "header.If-Match"}`.
- **Missing If-Match.** 428, from the authorization middleware.
- **304.** Finish step 1, for reads only.
- **Pagination.** The wrapper normalizes `Limit` through `Spec.fields`. A zero value becomes `Pagination.Default`. A value above `Pagination.Max` returns `invalid` with `FieldError{Location: "query.limit"}`. The `Link` header is `<path?query&cursor=…>; rel="next"`, built in finish from `st.path` and `st.query`.
- **Keyset.** Forward-only. No `rel="prev"` and no totals. Cursors are base64url of a payload with an HMAC over `(sort key, last id)`, keyed from `CSRFKey` with a separate label, so cursors cannot be forged. Row authorization still happens per row [D].
- **Status defaults.** POST → 201; DELETE → 204 with `NoBody`; others → 200. `Op.Status` overrides. A mutation on HTML is always 303 (§5.4).

---

## 9. Negotiation and views (`negotiate.go`)

### 9.1 Media

```go
type Media uint8

const (
	MediaJSON Media = iota + 1
	MediaMarkdown
	MediaHTML
)

func (m Media) Type() string // "application/json" | "text/markdown" | "text/html"

// Offered: JSON and Markdown are always offered. HTML is offered when the op has
// a page view (reads) or a form (mutations).
func Offered(hasPage, hasForm bool, kind Kind) []Media

// Negotiate implements RFC 9110 §12.5.1: q-values; specificity (type/subtype
// beats type/* beats */*); q=0 excludes. A tie goes to the order in the Accept
// header; without one, JSON > Markdown > HTML.
func Negotiate(accept string, offered []Media) (Media, bool)

[662 more lines in resource. Use :2413 to continue]
func Negotiate(accept string, offered []Media) (Media, bool)
```

Special cases [D]:
- An absent `Accept` header counts as `*/*`.
- A urlencoded request body whose `Accept` is absent or `*/*` is a form submission and negotiates to HTML. If the op has no form, the response is 415.
- Negotiation is done once, in step 4. The outcome is stored in `reqState.media`. Huma never negotiates, because step 11 rewrites `Accept` to `application/json`.

### 9.2 Views

```go
type ViewCtx struct {
	Spec      *Spec
	Principal Principal
	RequestID string
	Path      string
	CSRF      string
	Input     url.Values // form re-render only
	Errors    []FieldError
}

type PageFunc func(w io.Writer, c ViewCtx, out any) error
type FormFunc func(w io.Writer, c ViewCtx) error // nil = derived layout (derive_html.go)
type MarkdownFunc func(w io.Writer, c ViewCtx, out any) error

type Views struct {
	pages    map[string]pageEntry
	forms    map[string]FormFunc
	markdown map[string]MarkdownFunc
}

type pageEntry struct {
	fn  PageFunc
	out reflect.Type // compared with Spec.Out at startup
}

func NewViews() *Views

// PageView binds a typed page to an op. Mount compares Out with Spec.Out, so
// a mismatch is a startup error rather than a 500 per request.
func PageView[Out any](v *Views, opName string, f func(io.Writer, ViewCtx, Out) error)
func (v *Views) Form(opName string, layout FormFunc)
func (v *Views) Markdown(opName string, f MarkdownFunc)
```

Views are keyed by op name, as the assignment requires. Startup rejects unknown names, page views on mutations, form views on reads (except GET filter forms, §14.4), and Markdown overrides for unknown names.

**Generic Markdown** is `RenderMarkdown(w io.Writer, v any) error`, reflection only. It handles structs (`- **field**: value` in field order), slices of structs (a table of up to six scalar columns), nested structs (indented lists), maps (keys sorted), and scalars (escaped). It works on typed values and on decoded JSON.

**Error pages.** `RenderErrorPage(w, c, p problemBody)` is generic HTML or Markdown. The views registry does not override it.

**Reference and problem pages.** `RenderReference` and `RenderProblem` are page renderers used by two read ops (§15).

---

## 10. Huma binding (`mount_huma.go`)

### 10.1 Config

`huma.DefaultConfig(c.Title, c.Version)`, with:
- `OpenAPIPath = "/openapi"`, which serves `/openapi.json` and `/openapi.yaml`. Huma also serves `/openapi-3.0.json` [?, §20 item 4].
- `DocsPath = ""`, which disables the Stoplight UI [V: `config.go`, the `DocsPath` comment].
- `DefaultFormat = "application/json"`. No other format is registered.
- No `Transformers`, no `Formats` override, and no global middleware.

Huma's default JSON format handles request and response JSON. Unknown top-level keys are rejected in step 9, not by a decoder override.

### 10.2 Errors

```go
// The only override: Huma calls NewError for parse and validation errors.
// Process-global, set once in Mount.
huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
	return problemFromHuma(status, msg, errs)
}

type problemBody struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"`
	Code      string       `json:"code"`
	Retryable bool         `json:"retryable"`
	RequestID string       `json:"request_id,omitempty"`
	RukCode   string       `json:"ruk_code,omitempty"`
	Errors    []FieldError `json:"errors,omitempty"`
}

func (p problemBody) GetStatus() int
func (p problemBody) Error() string

var _ huma.StatusError = problemBody{}
```

`problemFromHuma` maps `huma.ErrorDetailer` values to `FieldError`. Huma's `Location` strings are already `body.<f>`, `query.<f>`, `path.<f>`, or `header.<h>`. Status 422 → `validation`; 400 → `invalid`; anything else → `internal`.

`problemBody` implements `huma.StatusError` without a `ContentType` method. Finish sets `Content-Type`.

### 10.3 Authorization middleware

```go
func authorize(s *Spec, guards []Guard) func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		st := stateFrom(ctx.Context())
		if err := checkScope(s, st.principal); err != nil {
			writeProblem(ctx, problemFor(st, s, err))
			return
		}
		for _, g := range guards {
			if err := g(ctx.Context(), st.principal, ctx.Param); err != nil {
				writeProblem(ctx, problemFor(st, s, asError(err)))
				return
			}
		}
		if s.Concurrency == ConcurrencyIfMatchRequired && s.Method != http.MethodPost && ctx.Header("If-Match") == "" {
			writeProblem(ctx, problemFor(st, s, Errorf(CodePreconditionReq, "If-Match is required")))
			return
		}
		next(ctx)
	}
}

// writeProblem uses the Context methods that humago implements: SetHeader, SetStatus, BodyWriter.
func writeProblem(ctx huma.Context, p problemBody) {
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(p.Status)
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(p)
}
```

Huma runs `Operation.Middlewares` before parsing and validation, so `ctx.Param` is available and the body has not been read [V].

### 10.4 Registration and the wrapper

```go
type wire[Out any] struct {
	Body Out
}

func mountOp[In, Out any](m *mount, o Op[In, Out], s Spec) error {
	huma.Register(m.api, huma.Operation{
		OperationID:   s.Name,
		Method:        s.Method,
		Path:          s.Path,
		Summary:       s.Summary,
		Description:   s.Description,
		Errors:        statusesOf(s.Problems),
		DefaultStatus: s.successStatus(),
		Security: []map[string][]string{
			{"bearer": {s.Scope.String()}},
			{"cookie": {}},
		},
		Extensions: map[string]any{
			"x-rukie-scope":    s.Scope.String(),
			"x-rukie-kind":     s.Kind.String(),
			"x-rukie-problems": codesOf(s),
		},
		Middlewares: huma.Middlewares{authorize(&s, o.Guards)},
	}, func(ctx context.Context, in *In) (*wire[Out], error) {
		return invoke(ctx, o, &s, in)
	})
	return nil
}

func invoke[In, Out any](ctx context.Context, o Op[In, Out], s *Spec, in *In) (*wire[Out], error) {
	st := stateFrom(ctx)
	if err := normalizePage(in, s); err != nil {
		return nil, problemFor(st, s, err)
	}
	if s.Kind == KindMutation {
		if err := beginIdempotent(ctx, st); err != nil {
			return nil, problemFor(st, s, err)
		}
	}
	out, err := o.Handler(ctx, st.principal, *in)
	if err != nil {
		return nil, problemFor(st, s, err)
	}
	st.out = out
	recordHeaders(st, o, out)
	return &wire[Out]{Body: out}, nil
}
```

`beginIdempotent` calls `Begin` only when the request has an idempotency key. It sets `st.claim` on `OutcomeNew`, and returns a problem for any other outcome. A panic in `Handler` reaches the outer `recover` (step 0), which aborts the claim.

`normalizePage` and the If-Match reads use `Spec.fields` indices with `reflect.Value.FieldByIndex`. `recordHeaders` sets `st.etag`, `st.location`, and `st.link`.

`problemFor` returns a `problemBody`. It converts undeclared codes to `internal` and logs them.

Huma's `Operation.Errors`, `Security`, `Extensions`, `Middlewares`, `DefaultStatus`, and `Metadata` exist in v2.39.1 [V for `Errors`, `Security`, `Middlewares`, `SkipValidateBody`, `Metadata`; `DefaultStatus` and `Extensions` are from research §2 and are unverified in this revision, §20 item 1].

### 10.5 Non-Huma errors

Errors raised before dispatch (steps 3–10) and errors raised by the authorization middleware go through `writeProblem` or the outer writer. Finish then sees the captured problem and renders it in the negotiated media. No Huma error hook is involved.

### 10.6 OpenAPI and the Huma-free boundary

```go
func (s *Server) openapiJSON() ([]byte, error) // json.Marshal(api.OpenAPI())
```

`DocumentFromOpenAPI` in `table.go` takes these bytes. No Huma type crosses into any other file. Huma's output is the only OpenAPI generator. There is no second one, and kin-openapi is not used (§19).

---

## 11. Document and schema (`table.go`)

```go
type Schema map[string]any // a decoded JSON Schema object

type Param struct {
	In       string // "path" | "query" | "header"
	Name     string
	Required bool
	Schema   Schema
}

type OperationSchema struct {
	Params   []Param
	Body     Schema // nil if none
	Response Schema // 200/201/204 body; nil if none
}

type Document struct {
	Operations map[string]OperationSchema // by Spec.Name
	Components map[string]Schema
}

func DocumentFromOpenAPI(raw []byte) (*Document, error)

// Localize returns a standalone schema: every "#/components/schemas/X" ref is
// rewritten to "#/$defs/X", the referenced schemas are copied into $defs, and
// $schema is removed. The result is 2020-12.
func (d *Document) Localize(s Schema) (Schema, error)
```

After the Document is built, Mount fills `Spec.BodyKeys` from each mutation's body properties.

---

## 12. MCP derivation (`derive_mcp.go`)

### 12.1 Shared argument model

```go
type Args map[string]json.RawMessage // the MCP input-schema instance

// ToRequest maps args onto the op's HTTP shape:
//   path params     -> substituted; each segment escaped with url.PathEscape
//   query params    -> query string; GET and HEAD take every non-path, non-body property
//   header params   -> "if_match" -> If-Match; "idempotency_key" -> Idempotency-Key
//   body properties -> JSON object for mutations
// The caller sets Accept.
func (a Args) ToRequest(s *Spec, d *Document, base *url.URL) (*http.Request, error)

type Response struct { // defined in mount_huma.go
	Status int
	Header http.Header
	Body   []byte
}

type Invoker interface {
	Invoke(ctx context.Context, r *http.Request) (*Response, error)
}

// LocalInvoker calls the server in-process with a bound principal.
type LocalInvoker struct {
	Server    *Server
	Principal Principal
}

// RemoteInvoker calls a rukie server over HTTP with a bearer token. The CLI and
// stdio MCP use it.
type RemoteInvoker struct {
	Base   *url.URL
	Token  string
	Client *http.Client
}
```

The flattening rule, shared by MCP and the CLI:
- Path and query parameters become properties with `x-rukie-in: path` or `query`.
- Header parameters become snake-cased properties: `If-Match` → `if_match`, `Idempotency-Key` → `idempotency_key`, with `x-rukie-in: header`.
- Body properties are merged at the top level. A name collision among these is a startup error.
- The top level has `additionalProperties: false`.

### 12.2 Surface

```go
type ToolFilter struct {
	Principal Principal
	Toolsets  []string // domain = first dot segment; empty = all
	ReadOnly  bool
}

func ToolName(name string) string // strings.ReplaceAll(name, ".", "_")

func InputSchema(s *Spec, d *Document) (Schema, error)
func OutputSchema(s *Spec, d *Document) (Schema, error)
func Annotations(s *Spec) *mcp.ToolAnnotations

func BuildTools(specs []Spec, d *Document, f ToolFilter) ([]*mcp.Tool, error)

// NewMCPServer returns a server for one principal. Its tool list is filtered by
// scope, toolset, read-only mode, and !Hidden.
func NewMCPServer(impl *mcp.Implementation, specs []Spec, d *Document, f ToolFilter, inv Invoker) (*mcp.Server, error)

func ToolResult(s *Spec, resp *Response) *mcp.CallToolResult
```

### 12.3 Field mapping

| MCP field | Source |
|---|---|
| `name` | `ToolName(Name)`, so `issue.create` → `issue_create` |
| `title` | `Summary` |
| `description` | `Description`, plus `\nCLI: rukie <path>` and `\nErrors: <codes>` |
| `inputSchema` | `InputSchema`, flattened, `$defs`, `additionalProperties: false` |
| `outputSchema` | `OutputSchema`, the 200 or 201 body, localized |
| `annotations.readOnlyHint` | `Kind == KindRead` |
| `annotations.destructiveHint` | `Destructive`; explicit `false` for reads |
| `annotations.idempotentHint` | `Idempotent \|\| Kind == KindRead` |
| `annotations.openWorldHint` | `false` (closed world) |
| filter | `!Hidden`, `Principal.Can(Scope)`, toolsets, read-only |

`ToolAnnotations` has `DestructiveHint *bool`, `IdempotentHint bool`, `OpenWorldHint *bool`, and `ReadOnlyHint bool` [V: go-sdk `protocol.go`]. Clients must treat annotations as untrusted [MCP spec].

The tool list is filtered by scope only. Guards need path values, which are unknown at list time. Calls are checked in full.

### 12.4 Calls

Each tool handler does: decode `req.Params.Arguments` into `Args` → `ToRequest` (with `Accept: application/json`) → `inv.Invoke` → `ToolResult`. Validation is Huma's, on the same request path as HTTP. MCP and HTTP cannot disagree on validation, except through the schema derivation, which §16 tests.

`ToolResult`:
- 2xx: `StructuredContent` is the decoded body, and `Content` has one text item with the same JSON.
- Non-2xx: `IsError: true`; `StructuredContent` and text are the problem JSON.
- An unknown tool name or malformed arguments is a JSON-RPC error (`-32602`). Every domain error is `isError`.

Arguments carry `if_match` and `idempotency_key` as properties. An MCP retry with the same key gets the same replay as HTTP.

### 12.5 Transports

- `/mcp`: `mcp.NewStreamableHTTPHandler(getServer, nil)`, registered as a non-op route. `getServer(r)` authenticates with bearer only (no cookies), builds `NewMCPServer` for that principal, and caches it by `(TokenID, Grants)`. Session lifecycle is the go-sdk handler's [?, §20 item 7].
- stdio: `rukie mcp --server URL --token-env RUKIE_TOKEN [--toolsets …] [--read-only]`. It uses `RemoteInvoker` and then `srv.Run(ctx, &mcp.StdioTransport{})`.

### 12.6 Toolsets

Domain = first dot segment: `issue`, `repo`, `review`, `task`, `ci`, `org`, `release`, `mirror`, `webhook`, `inbox`, `token`, `ops`, `problem`, `session`, `account`, `user`. `--toolsets` selects domains. `--read-only` removes mutations. The default is every domain with at least one permitted tool.

---

## 13. CLI derivation (`derive_cli.go`)

### 13.1 Command tree

```go
type Command struct {
	Name       string
	Children   map[string]*Command
	Spec       *Spec // non-nil for leaves
	Flags      []Flag
	Positional []string
}

type FlagKind uint8

const (
	FlagBool FlagKind = iota + 1
	FlagInt
	FlagNumber
	FlagString
	FlagEnum
	FlagRepeated
	FlagJSON
)

type Flag struct {
	Name, Property string
	Kind           FlagKind
	Required       bool
	Enum           []string
	Help           string
}

func BuildCommands(specs []Spec, d *Document) (*Command, error)
```

Path: `Name` is split on `.`. The last segment is the verb and the earlier ones are nouns. `issue.create` → `rukie issue create`. `repo.hook.create` → `rukie repo hook create`. `CLI.Path` overrides. A leaf and a group at the same path is a startup error.

### 13.2 Flags

| Schema property | CLI |
|---|---|
| path parameter | positional, in URL order, or `CLI.Positional` |
| `boolean` | `--name` / `--no-name` |
| `integer`, `number` | `--name N`, parse-checked (exit 64) |
| `string` | `--name VALUE` |
| `enum` | `--name VALUE`, checked against the enum (exit 64) |
| array of scalars | repeatable `--name VALUE` |
| `object` | `--name @file`, `--name -`, or `--name '{json}'` |
| `if_match` | `--if-match` |
| `idempotency_key` | `--idempotency-key` |

Flag names are `kebab(property)`. A collision with a global flag is a startup error.

### 13.3 Global flags and stdin

`--server URL` (env `RUKIE_SERVER`), `--token-file PATH` (env `RUKIE_TOKEN`; there is no `--token VALUE`, so secrets stay out of shell history), `--json`, `--input FILE|-` (a complete `Args` object; flags override fields), `--if-match`, `--idempotency-key`, `--timeout`, `--help`.

Reserved names: `server`, `token-file`, `json`, `input`, `if-match`, `idempotency-key`, `timeout`, `help`. A property that collides with a reserved name is a startup error.

### 13.4 Parse, run, output

```go
type Invocation struct {
	Leaf   *Command
	Args   Args
	Global GlobalOpts
}

func Parse(root *Command, argv []string) (*Invocation, error) // stdlib flag.FlagSet per leaf; usage errors wrap ErrUsage
func Run(ctx context.Context, inv *Invocation, c Invoker, stdout, stderr io.Writer) int
```

- Default human output sends `Accept: text/markdown`. Agents and humans therefore see the same rendering.
- `--json` prints the body on success. On failure it prints the problem JSON on stdout. Stderr carries logs and, for mutations, the idempotency key printed once.
- Retries: transport errors, and 429 or 503 with `Retry-After`, up to three attempts. Mutations reuse the same `Idempotency-Key` on every attempt.
- `Run` returns the exit code from §4.4. `main` calls `os.Exit(Run(...))`.

---

## 14. HTML forms and pages (`derive_html.go`)

### 14.1 Field derivation

```go
type FieldKind uint8

const (
	FieldText FieldKind = iota + 1
	FieldPassword
	FieldNumber
	FieldCheckbox
	FieldSelect
	FieldTextarea
	FieldHidden
)

type Field struct {
	Name, Label, Help string
	Kind              FieldKind
	Required          bool
	Min, Max          *float64
	MinLen, MaxLen    int
	Pattern           string
	Options           []string
	Multiple          bool // textarea: one value per line
	Value             string
	Checked           bool
	Errors            []string
}

func FormFields(s *Spec, d *Document, values url.Values, errs []FieldError) ([]Field, error)
```

| Schema | Field |
|---|---|
| `string` | `FieldText`; `format: password` → `FieldPassword` |
| `string` with `enum` | `FieldSelect` |
| `boolean` | `FieldCheckbox` (absent means false) |
| `integer`, `number` | `FieldNumber`, with min, max, and step |
| array of `string` | `FieldTextarea`, `Multiple` |
| `required` | `required` attribute; server validation stays authoritative |
| `minLength`, `maxLength`, `pattern` | attributes |

Hidden fields, added automatically:
- `_csrf`, on every form.
- `_etag`, when Concurrency is set and the op has an ETag. The value is the current ETag from the page's `ViewCtx`.
- `_idem`, on mutations. A fresh random value per render, so a double submit replays instead of duplicating.

A web-capable op (one with a page or a form) has only path parameters and body properties. `If-Match` and `Idempotency-Key` become hidden fields. Nested objects are a startup error for web-capable ops; such ops are API-only.

### 14.2 Errors and PRG

- Field errors: each `FieldError` with location `body.<field>` attaches to that field, which gets `aria-invalid="true"` and `aria-describedby`. Other errors go in a summary at the top of the form (GOV.UK error-summary pattern).
- Success: 303 to `Location`, or to the request path (§5.4).
- Validation (422, from step 9 or from Huma): the form is re-rendered with `ViewCtx.Input` and `Errors`, status 422.
- Stale (412): a conflict page, with a link to the current resource.
- Other problems: the generic error page, at the problem's status.
- Flash messages: a signed cookie set through `SetCookie`, read and cleared by the next GET. No JavaScript.

### 14.3 Form decoding

```go
type Reserved struct{ CSRF, ETag, IdempotencyKey string }

// DecodeForm converts a urlencoded body to canonical Args plus reserved control
// values and the raw values. Unknown fields are a 422. Repeated keys fill array
// properties. Checkbox presence means true. Numeric parse errors become FieldErrors.
func DecodeForm(s *Spec, d *Document, body []byte) (Args, Reserved, url.Values, error)
```

Step 9 then:
- Replaces the body with the JSON encoding of `Args` and sets `Content-Type: application/json`.
- Sets `If-Match` from `_etag` and `Idempotency-Key` from `_idem`.
- Verifies `_csrf` in step 7, before this point.
- Keeps the raw values in `st.raw` for re-rendering.

Huma never sees a urlencoded body.

### 14.4 GET filter forms

A read op with a page view may register a form. The form uses GET, its action is the op path, and its fields come from the query parameters. Filters are plain links and forms, with no JavaScript [D].

---

## 15. OpenAPI, reference, and problem pages

- **`/openapi.json`** (OpenAPI 3.1): Huma. Each `operationId` is `Name`. Extensions: `x-rukie-scope`, `x-rukie-kind`, `x-rukie-problems`. Components: `bearer` (HTTP bearer), `cookie` (apiKey in a cookie), and a `Problem` schema for error responses. No webhooks.
- **`ops.list`** (`GET /ops`): a read op whose JSON view lists the table, with CLI and MCP names. Its page view is the server-rendered API reference, grouped by domain, showing method, path, scope, kind, summary, parameters, body fields, response shape, problem links, and a `curl` example. Its Markdown view is the same content. This is documentation built on the table, not a fifth surface.
- **`problem.show`** (`GET /problems/{slug}`): one page per catalogue entry, with title, status, retryability, exit class, and Ruk codes. The `type` URIs in §4.6 resolve here.

Nothing uses JavaScript. The CSP in §5.4 enforces that for HTML responses.

---

## 16. Startup checks and compile-time guarantees

### 16.1 Compile time

What the compiler enforces:
- `Scope` and `Code` are closed. Their fields are unexported, so only the package vars are valid values. A typo in a scope or code name does not compile. `Scope{}` and `Code{}` (zero values) are rejected at startup.
- `Op[In,Out]` ties `Handler`, `ETag`, `Location`, and `Pagination.Next` to the same `In` and `Out` types. Mismatches do not compile.
- `Handler` takes a `Principal` and returns `error`. The `error` is not constrained to `*Error`; a plain error becomes `internal` (§4.5).
- `PageView[Out]` binds a page to its output type. `Mount` also compares the type with `Spec.Out` (§9.2).
- `var _ huma.StatusError = problemBody{}` and `var _ error = (*Error)(nil)`.

What the compiler does not enforce, and startup checks instead: names, route shapes, existence of reserved struct fields and their tags, duplicate routes, and every derived-surface rule.

### 16.2 Structural (`Register`, and `Table.Validate` for all ops)

1. `Name` matches `^[a-z]+(\.[a-z]+){1,2}$`, length ≤128, and is unique. Names contain no underscores, so the dot-to-underscore MCP name is unique too.
2. `Summary` is 1–120 characters.
3. `KindRead` ⇒ `Method == GET`. `KindMutation` ⇒ `Method` ∈ {POST, PUT, PATCH, DELETE}.
4. `Destructive` ⇒ `KindMutation`.
5. `Scope.Valid()` for every op.
6. `Problems` after framework additions is non-empty, and each code is in the catalogue (guaranteed by type, checked anyway for zero values).
7. `Path` begins with `/`. The normalized `METHOD + path`, with every placeholder replaced by `{}`, is unique.
8. Placeholders in `Path` and `path:"…"` tags in `In` are a bijection.
9. `In` and `Out` are struct types.
10. `Pagination != nil` ⇒ `In` has `Cursor` and `Limit`; `1 ≤ Default ≤ Max ≤ 1000`.
11. `Concurrency != None` ⇒ `ETag != nil`, and `In` has `IfMatch`.
12. Every mutation `In` has `IdempotencyKey`.
13. `Location != nil` for every mutation that has a form.
14. `Status` is 0, or a valid success status for the method.
15. `Handler`, every element of `Guards`, and `Pagination.Next` are non-nil where required.
16. Reserved top-level words are not used as path prefixes: `ops`, `problems`, `mcp`, `openapi.json`, `static`, `session`, `account`, `explore`, `favicon.ico`, `robots.txt`, `.well-known`.
17. No path segment ends in `.git`.
18. Route conflicts that the normalized check misses (for example `/{a}/b` and `/x/{b}`) are caught when `Mount` registers the routes, because `ServeMux` panics. `Mount` recovers the panic and returns it as an error.

### 16.3 Surface (`Mount`, after the Document exists)

19. Component schema names are unique. Huma derives them from Go type names.
20. Every `Localize`d input and output schema validates against the 2020-12 meta-schema [?, §20 item 5].
21. MCP: `ToolName` values are unique, and `Resolve()` succeeds on every tool schema (`checkMCP`).
22. CLI: leaf paths are unique, no group collides with a leaf, flag names are unique per leaf, none collides with a reserved global, and positional names are path parameters (`checkCLI`).
23. Views: every view name is an op; each `PageView` type equals `Spec.Out`; page views are on reads only; form views are on mutations only; GET filter forms are on reads with a page view (`checkViews`, in negotiate.go).
24. Web-capable ops have flat inputs, and mutations have `Location`. Form layouts can be derived for every such op (`checkForms`).
25. `Hidden` ops are still routable and declare their problems.

---

## 17. Test plan

- **Table tests.** Each rule in §16 has a negative test: duplicate name, duplicate route, mutation without scope, zero `Code`, undeclared code, path and tag mismatch, missing `Cursor`, view type mismatch, form on a read, and mux conflict.
- **Golden.** The OpenAPI document, the MCP tool list, and the CLI help tree. Changes require explicit regeneration.
- **Conformance.** The same `Args` through HTTP JSON, MCP (`LocalInvoker`), the CLI (`RemoteInvoker` against a test server), and the HTML form (`DecodeForm`). Assert identical `Out` JSON and identical audit rows, except for surface and request ID.
- **Negotiation matrix.** `Accept` values × op kind × view presence. Include browser headers, `*/*`, `text/markdown;q=0.9, application/json`, `text/html` on an op with no view (406), and a urlencoded body with `*/*` (form, or 415 when no form exists).
- **Idempotency.**
  - New, then replay.
  - Concurrent same-key requests: one executes, one gets 409.
  - Different body: 422.
  - **Authorization failure, then a corrected retry with the same key: the retry executes.** This is the regression test for the 403 case.
  - **Validation failure, then a corrected retry: executes.**
  - **Handler 412, then a retry with the same key and body: replays the 412.** This documents the deterministic-4xx rule.
  - 5xx or retryable problem: aborted, and the retry executes.
  - Panic: aborted.
  - Body above `MaxRecorded`: aborted.
  - TTL expiry.
- **Single negotiation.** Assert that Huma receives `Accept: application/json` on every dispatch, and that no media decision is made after step 4.
- **Unknown keys.** A top-level key outside `BodyKeys` returns 422; a nested unknown key keeps Huma's default behaviour (test documents it).
- **CSRF and Origin.** Cookie without `_csrf`, wrong `_csrf`, bad Origin, missing Origin with `Sec-Fetch-Site: cross-site`, bearer-only (exempt), and login with and without the pre-session cookie.
- **ETag.** 304 on match; 412 on a stale write under concurrent writers; 428 on a missing `If-Match`.
- **Pagination.** Cursor round trip; forged cursor rejected; `limit` above max rejected; `Link` correct with extra query parameters.
- **Ruk fidelity.** Take one Ruk method through the OpenAPI path and diff its schema against `/tmp/ruk-model.json`, after copying that file into the repo.
- **Fuzzing** (stdlib `testing.F`). The `Accept` parser, `DecodeForm`, the cursor decoder, and `Digest` over arbitrary bodies.
- **Performance.** A benchmark of the chain with a stub store. Huma writes JSON even for HTML requests, which is one extra encode per HTML response; measure it and accept it, or revisit (§20 item 12).

---

## 18. Routing constraints


[59 more lines in resource. Use :3016 to continue]

- Resource URLs are canonical: `/{owner}/{repo}/issues`. The same URL negotiates JSON, Markdown, and HTML. There is no `/api` prefix.
- **Git clone URL** `/{owner}/{repo}.git`. `ServeMux` wildcards must occupy a whole segment [H: net/http pattern syntax], so `{repo}.git` cannot be a pattern. Step 2 routes any path whose second segment ends in `.git` to Smart HTTP before the mux runs. Owner and repo names may not end in `.git`.
- **Reserved top-level words** (§16.2 item 16) keep owners from shadowing routes. Owner-name validation lives outside this package and must enforce the same list.
- `GET /{owner}` and `GET /explore`: the literal segment is more specific, so `ServeMux` does not report a conflict. The reserved-word check still keeps the intent clear.
- `mux.Handler(r)` returns the pattern for matched routes and an empty pattern for 404 and 405 [H: Go docs; verify against go1.27]. Step 3 uses the pattern as the key into `routes`, and the path template for `Allow`. The mux matches twice (`Handler`, then `ServeHTTP`). That is acceptable at this scale.
- No CORS. Browser JavaScript from other origins is not a client of this forge [D].

---

## 19. Dependencies

| Module | Use | Where |
|---|---|---|
| `github.com/danielgtaylor/huma/v2` v2.39.1 | JSON API, OpenAPI 3.1, parsing and validation | `mount_huma.go` only |
| `…/huma/v2/adapters/humago` | ServeMux adapter | `mount_huma.go` only |
| `github.com/modelcontextprotocol/go-sdk` v1.8.0 | MCP server, streamable HTTP, stdio | `derive_mcp.go` only |
| `github.com/google/jsonschema-go` v0.4.3 | schema `Resolve` and validation (a transitive dependency of go-sdk) | `derive_mcp.go` |
| stdlib | everything else | — |

**Removed in the rename commit:**
- `github.com/getkin/kin-openapi` v0.149.0 (`rukie/go.mod:37`) is removed. Its role in rukie today is Gitea's OpenAPI tooling [I]; that tooling is cut with the Gitea API. Keeping it would make two OpenAPI generators, and they could disagree. Huma is the only generator. Schema checks use the 2020-12 meta-schema and golden files (§16.3 item 20).
- Other Gitea-only dependencies (`go-chi/chi`, `go-chi/cors`, `urfave/cli/v3`, and the rest) follow the same rule: each is removed in the same commit if its only consumer is cut Gitea code. Confirm each with `go mod why` at the rename [I].

No new dependency is added beyond Huma, go-sdk, and jsonschema-go.

---

## 20. Open items

1. **Huma `DefaultStatus` and `Extensions`** exist in v2.39.1, per research §2. They are not re-read in this revision.
2. **Huma 204 with `NoBody`:** confirm that no body is written.
3. **Huma `Link: rel="describedBy"` and `$schema`** on responses: confirm whether they are emitted. Finish appends the framework's `Link` to any header Huma sets, so the two should coexist. Test it.
4. **Huma `/openapi-3.0.json`:** confirm whether it can be disabled. If it cannot, it stays, and the reason is documented.
5. **Huma OpenAPI 3.1 output:** golden-test `$defs`, nullability, and `type` arrays for optional fields.
6. **`ServeMux` pattern return** for 404, 405, and HEAD in go1.27 (§18).
7. **go-sdk v1.8.0:** `AddTool` with a caller-supplied raw JSON Schema (per the doc comment [V]); `Resolve()` in jsonschema-go; session lifecycle and timeouts.
8. **Ruk semantics:** the 30-code mapping (§4.3) is inferred from names. Confirm with Ruk. Confirm whether Ruk deduplicates on `request_id` alone (§7.5).
9. **Ruk sum types** (`DiffBody`, `anyOf`, `oneOf`, `const`): Huma's schema model cannot carry `$defs` or `propertyNames`. Options are a `huma.SchemaProvider` on the Go type, or a `RawSchema` escape hatch. Decide in the Ruk-proxy design.
10. **Nested unknown keys.** Only top-level keys are rejected (§5.1 step 9). Extending that to nested objects is possible, but it is a separate decision.
11. **Idempotency digest.** `json.Compact` does not sort keys, so two bodies that differ only in key order digest differently. Acceptable for agents; document it.
12. **Double JSON encoding for HTML and Markdown.** Huma encodes the JSON response, which is discarded for HTML and Markdown. If the benchmark (§17) shows this matters, a later design can allow the handler wrapper to skip Huma for non-JSON media. That is not done now, because it would bypass Huma's validation on the response path.
13. **Huma panic recovery.** Confirm whether Huma recovers handler panics itself. If it does not, step 0 is the only recovery.
14. **Cursor key rotation.** Rotating `CSRFKey` invalidates outstanding cursors. A dedicated key with a grace window is an option if that matters.
15. **Rate limiter keying.** Trust `X-Forwarded-For` only from configured proxies. Not specified here.
16. **Reserved routes versus future owners.** The reserved list is a snapshot. Adding a top-level route later must be checked against existing owners.
17. **`/tmp/ruk-model.json`** is not durable. Copy it into the repo before the fidelity test depends on it.
18. **Research file.** `research-agent-api-codegen.md` begins with a write-failure preamble, and its §5–§7 are partly truncated in the copy read for this design. This design uses only the content it could read.

---

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/go.mod` — line 1 (`module gitea.dev`, changed to the target path in the rename commit) and line 37 (`kin-openapi v0.149.0`, removed).
- `/home/alpha/rewrite/git-renewed/rukie/routers/api/v1/api.go` — guard and route inventory (lines 240–811), to re-express as table entries and guard constructors. Mapping: `reqRepoReader`→`RepoReader`, `reqRepoWriter`→`RepoWriter`, `reqAdmin`→`RepoAdmin`, `reqOwner`→`RepoOwner`, `reqSiteAdmin`→`SiteAdmin`, `reqOrgOwnership`→`OrgOwner`, `mustNotBeArchived`→`NotArchived`. `reqPackageAccess` and `reqBasicOrRevProxyAuth` are dropped: packages are cut, and reverse-proxy auth is not in the settled identity list.
- `/home/alpha/rewrite/git-renewed/rukie/models/auth/access_token_scope.go` — the nine Gitea categories (lines 17–27) that the new `Scope` catalogue replaces (§3.5).
- `/tmp/ruk-model.json` — the 30 `error_codes` with `exit_status` (§4.3) and the 47 `methods` by kind. Copy into the repo before use.
- `<rukie>/internal/op/table.go` (to create) — `Op[In,Out]`, `Register`, `Table`, `Spec`, and the error catalogue. It is the first file to write, because every other file depends on its types.

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Correct the claim that Huma n…