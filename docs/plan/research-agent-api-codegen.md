I could not write the artifact: this session has `read`, `grep`, `web_search` and `ast_grep` only, and plan mode forbids writes, so no file exists at `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-agent-api-codegen.md` or at `local://`. The document below is the single consolidated version with every correction folded in. Save it verbatim to that path, or point me at a tool that can write.

Confidence labels: **H** means the primary source was fetched and read. **M** means partly verified or inferred. **L** means a vendor claim or unverified. Facts were collected 2026-10-20.

---

# Research: one operation table → HTTP/OpenAPI 3.1, MCP, agent CLI, HTML forms (Go)

## 0. Recommendation

**Build a rukie-owned typed operation table (`Op[In,Out]`). Each registration calls Huma v2 over `net/http` `ServeMux` (`humago`) for HTTP, OpenAPI 3.1, RFC 9457 errors and validation of native ops. The table itself derives MCP, the CLI and HTML forms.**

The single criterion: hand-write the least code on the paths whose correctness a spec defines (OpenAPI, problem+json, validation), and keep all dispatch metadata (scope, effect, guards, CLI/MCP/UI mapping) in our own table.

- **ogen and oapi-codegen fail the criterion.** They are spec-first, so a hand-written spec generates the Go stubs. That inverts the source of truth, adds a codegen step, and produces no MCP, CLI or forms.
- **A fully hand-rolled stack fails it too.** You would still have to build the OpenAPI document, problem+json, negotiation and exhaustive validation, which Huma already supplies. [M: engineering judgement]
- **Cost of Huma.** Its own micro-benchmark shows about 0.6 µs more per request than raw chi. Vendor-reported, not reproduced (M-L). That is negligible next to a Unix-socket round trip to Ruk.
- **Exit path.** Huma sits behind one `op.Mount(mux)` function. The table, the Go types and the derivations survive if Huma is replaced. [M]
- **Dialect.** MCP, OpenAPI 3.1 and Ruk's dumped model all use JSON Schema 2020-12, so schemas move between surfaces without dialect translation. MCP `inputSchema`/`outputSchema` default to 2020-12 [H: [MCP tools spec 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)]. Ruk's model declares `$schema: https://json-schema.org/draft/2020-12/schema` [H: `/tmp/ruk-model.json`].
- **Docs UI.** Huma's built-in docs renderer is JavaScript (Stoplight/Swagger/Scalar). The zero-JS rule means disabling it by blanking `DocsPath` and rendering the API reference server-side from the table. [M]

## 1. Libraries (version, license, last release)

| Lib | Version | License | Last release | Notes |
|---|---|---|---|---|
| `danielgtaylor/huma/v2` | v2.39.1 | MIT | 2026-07-29 (`published_at`; tag time 13:42Z via [proxy](https://proxy.golang.org/github.com/danielgtaylor/huma/v2/@latest)) | `go 1.25.0`; 4,469 stars; 138 open issues; last push 2026-10-15. [H: [release](https://api.github.com/repos/danielgtaylor/huma/releases/latest), [repo](https://api.github.com/repos/danielgtaylor/huma)] |
| `ogen-go/ogen` | v1.24.0 | Apache-2.0 | 2026-08-07 | OpenAPI-first; README badge "stable". [H: [release](https://api.github.com/repos/ogen-go/ogen/releases/latest)] |
| `oapi-codegen/oapi-codegen/v2` | v2.8.0 | Apache-2.0 | 2026-07-17 | Initial OpenAPI 3.1 support. Requires Go 1.25 and generated-code runtime ≥ v1.6.0. Built on `kin-openapi` v0.142.0. [H: [release](https://api.github.com/repos/oapi-codegen/oapi-codegen/releases/latest)] |
| `modelcontextprotocol/go-sdk` | v1.8.0 | Apache-2.0 for new code; MIT for legacy contributions; CC-BY-4.0 docs (LICENSE; GitHub reports `NOASSERTION`) | release published 2026-09-14; tag commit 2026-09-04 | Newest negotiated MCP version `2026-07-28`. Depends on `google/jsonschema-go` v0.4.3. [H: [LICENSE](https://raw.githubusercontent.com/modelcontextprotocol/go-sdk/main/LICENSE), [release](https://api.github.com/repos/modelcontextprotocol/go-sdk/releases/latest), [proxy](https://proxy.golang.org/github.com/modelcontextprotocol/go-sdk/@latest), [go.mod](https://raw.githubusercontent.com/modelcontextprotocol/go-sdk/main/go.mod)] |
| `google/jsonschema-go` | v0.4.3 | MIT | 2026-04-17 | Pre-1.0; stdlib-only; infer, validate, construct. [H: [proxy](https://proxy.golang.org/github.com/google/jsonschema-go/@latest), [README](https://raw.githubusercontent.com/google/jsonschema-go/main/README.md)] |
| `invopop/jsonschema` | v0.14.0 | MIT | 2026-04-23 | Go types → JSON Schema reflector. Default draft unverified. [H: [proxy](https://proxy.golang.org/github.com/invopop/jsonschema/@v/v0.14.0.info)] |
| `spf13/cobra` | v1.10.2 | Apache-2.0 | 2025-12-04 | Slow cadence; ubiquitous. [H: [release](https://api.github.com/repos/spf13/cobra/releases/latest), [pkg.go.dev](https://pkg.go.dev/github.com/spf13/cobra)] |
| `alecthomas/kong` | v1.16.1 | MIT | 2026-08-09 | Struct-tag CLI. [H: [proxy](https://proxy.golang.org/github.com/alecthomas/kong/@latest), [pkg.go.dev](https://pkg.go.dev/github.com/alecthomas/kong)] |
| Standard Webhooks spec | 1.0.0 | Apache-2.0 | n/a | [H: [spec](https://raw.githubusercontent.com/standard-webhooks/standard-webhooks/main/spec/standard-webhooks.md)] |

## 2. Huma v2

**Maintenance [H]**
- Releases in 2026, from the release list: v2.37.1 (02-19), v2.37.2 (02-20), v2.37.3 (03-26), v2.38.0 (05-15), v2.39.0 (07-15), v2.39.1 (07-29). There is no v3 yet.
- Maintainer bus-factor is moderate [M].
- `go.mod` lists many router modules (gin, fiber, echo, chi, mongo-driver and others) as indirect dependencies. Only imported adapter packages compile, and `humago` imports none of them. The module graph still adds supply-chain noise. [H: [go.mod](https://raw.githubusercontent.com/danielgtaylor/huma/main/go.mod)]

**OpenAPI 3.1 generation [H]**
- Served endpoints: `/openapi.json`, `/openapi.yaml`, `/openapi-3.0.json`, `/openapi-3.0.yaml`. [openapi-generation.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/openapi-generation.md)
- Handler shape is `func(context.Context, *In) (*Out, error)`. [operations.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/operations.md)
- `huma.Operation` carries `Metadata`, `Extensions` (`x-` fields), `Errors []int`, `Security`, `Hidden`, `MaxBodyBytes`, `DefaultStatus` and `SkipValidateBody`. [openapi.go](https://raw.githubusercontent.com/danielgtaylor/huma/main/openapi.go)
- **Schema limits.** `huma.Schema` is a fixed struct. It has no public field for `$defs`, `propertyNames`, `unevaluated*` or `prefixItems`; only `Extensions` carries extra keywords, and those are emitted but never enforced by Huma's validator. [schema.go](https://raw.githubusercontent.com/danielgtaylor/huma/main/schema.go)
- `huma.SchemaProvider` lets a Go type supply its own schema. [schema-customization.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/schema-customization.md)

**RFC 9457 errors [H]**
- Errors are `application/problem+json` with `status`, `title`, `detail` and an `errors[]` array of `{location,message,value}`. `huma.NewError` can be overridden. [response-errors.md](https://huma.rocks/features/response-errors/)
- `huma.ErrorWithHeaders` attaches headers (for example `Retry-After`); any error implementing `huma.HeadersError` gets headers added.
- The default model also emits `$schema` and a `Link: …; rel="describedBy"` header. Whether both can be fully suppressed without forking is unverified.

**Router adapters [H]**
- `humago` covers Go 1.22+ `http.ServeMux`; others cover chi, echo, fiber, gin, gorilla/mux, httprouter and bunrouter. [bring-your-own-router.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/bring-your-own-router.md)

**Content negotiation [H docs; untested]**
- JSON by default; `+json` suffixes also match. CBOR is opt-in through a blank import of `formats/cbor`. Huma's docs cite RFC 7049, which RFC 8949 obsoletes.
- Recommendation: defer CBOR and start with JSON only. [response-serialization.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/response-serialization.md)
- Request bodies: JSON, plus multipart (`huma.MultipartFormFiles`). **`application/x-www-form-urlencoded` is not handled**: `huma.go` has no match and the docs cover only multipart. HTML forms therefore need a hand-written adapter. [H: [request-inputs.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/request-inputs.md), `huma.go` grep]

**Conditional requests [H]**
- `conditional.Params` supports If-Match, If-None-Match and the date preconditions. `PreconditionFailed(etag, modified)` yields 304 for reads and 412 for writes. ETag generation is the application's job. Huma does not enforce 428. [conditional-requests.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/conditional-requests.md)

**SSE [H]**
- `sse.Register` documents typed events in the OpenAPI spec. v2.39.0 flushes headers before the handler runs, supports comments for keep-alive, and works on fasthttp. [release](https://api.github.com/repos/danielgtaylor/huma/releases/latest), [SSE docs](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/server-sent-events-sse.md)

**Performance [M-L]**
- Vendor-reported on huma.rocks, no hardware or date, not reproduced: Huma v2 on chi at 2777 ns/op, 1718 B/op, 29 allocs; raw chi at 2143 ns/op, 2370 B/op, 29 allocs; Huma v1 at 112,086 ns/op. [benchmarks](https://huma.rocks/why/benchmarks/)
- A competitor's post claims about 1.49× faster in a full HTTP cycle against Huma v2.39.1. Rate that **L**. [forum post](https://forum.golangbridge.org/t/introducing-gofast-build-time-code-generation-for-validation-and-openapi-benchmarked-against-huma/42406)
- `humago` on `ServeMux` was not benchmarked separately: unverified.

**Gaps rukie must fill:** Idempotency-Key, 428 enforcement, a Link-pagination helper, scope and guard metadata, MCP, CLI, HTML forms, the webhook catalogue.

## 3. Alternatives

**ogen v1.24.0 (Apache-2.0, 2026-08-07) [H]**
- No reflection or `interface{}`; code-generated JSON through `go-faster/jx`; generated validation; a static radix router; OpenTelemetry; typed `Opt*` wrappers.
- SSE: generated **client only**. [README](https://raw.githubusercontent.com/ogen-go/ogen/main/README.md)
- OpenAPI 3.1: never claimed in the README. A Sep 2025 discussion says there is no explicit statement. **L / unverified**. [discussion #1410](https://github.com/ogen-go/ogen/discussions/1410)
- It generates its own router, so mixing with HTML routes means mounting by prefix.
- Fastest option; spec-first with no MCP, CLI or forms.

**oapi-codegen v2.8.0 (Apache-2.0, 2026-07-17) [H]**
- Spec-first. The v2.8.0 notes list: initial OpenAPI 3.1 support with webhooks and callbacks, a std `net/http` server target, a strict server, and a `kin-openapi` dependency that the notes describe as solo-maintained.
- Breaking changes in v2.8.0: trailing-slash stdhttp routes are anchored with `{$}`, and security-scope context emission is off by default.
- Same source-of-truth inversion as ogen.

**Hand-rolled net/http + schema reflection**
- `google/jsonschema-go` v0.4.3: infer from Go types, validate, stdlib-only. `invopop/jsonschema` v0.14.0: reflect only.
- The OpenAPI document, problem+json, negotiation and exhaustive validation must be hand-built. [inference]
- Fits only the narrow MCP tool-schema job.

**MCP go-sdk schema inference [H]**
- Generic `mcp.AddTool[In,Out]` infers the input schema from `In` through `google/jsonschema-go` (`jsonschema.ForType`), reading `jsonschema` struct tags for descriptions, and infers the output schema from `Out`. [server.go](https://raw.githubusercontent.com/modelcontextprotocol/go-sdk/main/mcp/server.go)
- **The low-level `(*Server).AddTool(t *Tool, h ToolHandler)` accepts a caller-supplied `InputSchema any`**, including `json.RawMessage`, which must have `type: "object"`. The caller unmarshals and validates arguments and sets `Content`, `StructuredContent` and `IsError`. That is the hook for table-derived schemas.
- v1.8.0 allows any valid JSON Schema as `OutputSchema` (SEP-2106) and adds `ServerOptions.SetCacheable` and `ServerOptions.SupportedProtocolVersions`. [release](https://api.github.com/repos/modelcontextprotocol/go-sdk/releases/latest)

## 4. How others share (or don't share) definitions

**github-mcp-server [H/M]**
- Hand-written tools on `go-sdk` v1.8.0, `jsonschema-go` v0.4.3 and `go-github` v92. [go.mod](https://raw.githubusercontent.com/github/github-mcp-server/main/go.mod)
- Each `ServerTool` wraps an `mcp.Tool` plus toolset metadata. Schemas are inferred from Go types with injected enums and cached. Read-only status comes from `Annotations.ReadOnlyHint`. [pkg/inventory/server_tool.go](https://raw.githubusercontent.com/github/github-mcp-server/main/pkg/inventory/server_tool.go) (read through a rendered image; M)
- A scope-policy layer hides tools the token's OAuth scopes cannot use: `RequireAll`, `PublicRead`, `NoScopes`, `DynamicChallenge`, and a `ScopeHierarchy` where `repo` implies `public_repo`. [pkg/scopes/scopes.go](https://raw.githubusercontent.com/github/github-mcp-server/main/pkg/scopes/scopes.go), [docs/scope-filtering.md](https://github.com/github/github-mcp-server/blob/main/docs/scope-filtering.md)

**gh CLI [H]**
- Hand-written cobra commands on `cli/go-gh/v2` v2.16.2, cobra v1.10.2 and pflag. It does not import `github-mcp-server`. [go.mod](https://raw.githubusercontent.com/cli/cli/trunk/go.mod)
- `--json fields`, `--jq` and `--template` come from `cmdutil.AddJSONFlags(cmd, &exporter, fields)`, which validates field names and supplies completion. [json_flags.go](https://raw.githubusercontent.com/cli/cli/trunk/pkg/cmdutil/json_flags.go)

**Conclusion [M].** GitHub shares no operation registry across the MCP server, the CLI and the REST schema. Each is hand-maintained over the same API, and drift is caught by tests. Rukie's table removes that drift by construction.

**CLI-from-registry pattern.** Huma's docs say `OperationID` becomes a Restish command, `restish api op --param=value`, which is a generic client driven by OpenAPI at runtime. [operations.md](https://raw.githubusercontent.com/danielgtaylor/huma/main/docs/docs/features/operations.md) For rukie, build cobra commands at startup by iterating the table; cobra's imperative API fits a runtime registry. Whether kong has a dynamic-command API is unverified.

**Rukie's current baseline [H/M].**
- Module `gitea.dev`, `go 1.27`; chi v5.3.2, `go-chi/cors`, urfave/cli v3 and `kin-openapi` v0.149.0 are in `go.mod`. No Huma or MCP SDK is present. [H: `/home/alpha/rewrite/git-renewed/rukie/go.mod`]
- `routers/api/v1/api.go` registers hundreds of routes by hand on a `web.Router`, with a per-route middleware chain such as `reqToken()`, `reqRepoWriter(unit.TypeCode)`, `mustNotBeArchived()`, `bind(api.XxxOption{})`, and `tokenRequiresScopes(auth_model.AccessTokenScopeCategory…)`. Scopes are coarse categories. The API description is generated from `swagger:` comments, separate from the handlers. [M: read through a rendered image]
- This router is what the table replaces. The existing `api.XxxOption` structs are natural `In` types, and the guards must move into the table (see §7, `Requires`).

## 5. Agent-friendly HTTP conventions

| Convention | Source / status | Rukie rule |
|---|---|---|
| Errors | **RFC 9457** (Standards Track, obsoletes 7807): §3.1 members `type`/`status`/`title`/`detail`/`instance`; §3.2 extension members. [H: [rfc9457](https://www.rfc-editor.org/rfc/rfc9457.txt)] | Always `application/problem+json`. Map each Ruk `E_*` code to a problem `type` URI. Our own extension members, permitted by §3.2 and not defined by the RFC: `code`, `retryable`, `retry_after_ms`, and an `errors[]` list with Huma-style `location`. |
| Idempotency | **`draft-ietf-httpapi-idempotency-key-header-07`**: expired Internet-Draft, no intended RFC status, expired 2026-04-18. Status is H; stability is L. [H: [datatracker](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/)] | Use as a de-facto convention. The draft: header value is an RFC 8941 String; missing key → 400; same key with a different payload → 422; retried while in flight → 409; completed retry replays the stored result; publish the key TTL. Scope the key as (token, key). Ruk's `E_REQUEST_REUSED` and `request_digest_version` suggest it already deduplicates by request id, so map the header to it (M: inferred from `/tmp/ruk-model.json`). |
| Pagination | **RFC 8288** `Link` (obsoletes 5988), `rel="next"`/`"previous"`. [H: [rfc8288](https://www.rfc-editor.org/rfc/rfc8288.txt)] | Keyset cursors only: opaque `?cursor=`, plus `Link: <…>; rel="next"`, plus `next_cursor` in the body (MCP `tools/list` also pages with `cursor`/`nextCursor`). No total counts by default. Respect Ruk's `page_default` 100 and `page_max` 1000. |
| Optimistic concurrency | **RFC 9110** §8.8.3 ETag, §13.1.1 If-Match (strong comparison), §15.5.13 412. **RFC 6585** §3 defines 428. [H: [rfc9110](https://www.rfc-editor.org/rfc/rfc9110.txt), [rfc6585](https://www.rfc-editor.org/rfc/rfc6585.txt)] | Strong ETag on every mutable resource. Mutating ops with `RequiresIfMatch`: missing header → 428, mismatch → 412 (Ruk's `E_STALE` maps here). ETag appears as `etag` in MCP output and as `--if-match` in the CLI. |
| Rate limiting | RFC 9110 §10.2.3 Retry-After; RFC 6585 §4 429. [H] `draft-ietf-httpapi-ratelimit-headers-11` is an active Internet-Draft, expiring 2026-11-24. [H: [datatracker](https://datatracker.ietf.org/api/v1/doc/document/draft-ietf-httpapi-ratelimit-headers/)] | Per-token buckets. 429 with `Retry-After`. `RateLimit*` fields are optional extras only, since the draft is unstable. |
| Agent identity | **RFC 9421** HTTP Message Signatures (Standards Track, Feb 2024): `Signature-Input`, `Signature`, `Accept-Signature`; §7.2.2 on replay. [H: [rfc9421](https://www.rfc-editor.org/rfc/rfc9421)] | Optional proof-of-possession tier above bearer tokens. The token record stores the agent's public key. Cover `@method`, `@target-uri`, `@authority`, `created`, `expires` and `nonce`. RFC 9421 does not cover message content directly and relies on the Digest specification, so also cover **`content-digest` (RFC 9530)** on every request with a body; otherwise the body is unsigned. RFC 9530 itself was not fetched (M). |
| Webhooks | **Standard Webhooks v1.0.0**. [H] | Headers `webhook-id`, `webhook-timestamp`, `webhook-signature`. Sign `msg_id.timestamp.payload`. HMAC-SHA256 `v1,` with `whsec_` secrets (24–64 bytes), or ed25519 `v1a,` with `whsk_`/`whpk_` (the spec prefers asymmetric). Space-delimited signatures allow zero-downtime rotation. Per-endpoint keys. Payload `{type,timestamp,data}` with dot-delimited types; thin payloads preferred for auditability. Retry with exponential backoff spanning days; 410 disables the endpoint; 429 throttles; 15–30 s timeout; HTTPS only. |
| Event tail | **WHATWG HTML §9.2** SSE: `text/event-stream`, `id:`, `retry:`, `Last-Event-ID` on reconnect (§9.2.4), HTTP 204 stops reconnecting. [H: [spec](https://html.spec.whatwg.org/multipage/server-sent-events.html)] | `GET /api/v1/events?since=<cursor>` with event `id` equal to the audit/event-log cursor, so tails resume exactly and share keyset semantics with pagination. Browser `EventSource` takes only a URL and `withCredentials`, so it cannot send `Authorization`. That suits the zero-JS rule: SSE is an agent-only surface. |

## 6. Token design for agents

**GitHub fine-grained PAT [H]** ([management docs](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens), [permissions table](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens))
- One resource owner per token (user or organization).
- Repository access is all, selected or public-only. Permissions are per resource at read, write or admin.
- Expiry is 1–366 days, default 30 in the pre-fill URL docs. Infinite lifetime is allowed unless a policy forbids it. Tokens unused for a year are removed automatically.
- Organization approval can hold a token in `pending` state.
- The REST API returns `X-Accepted-GitHub-Permissions` to say which permission an endpoint needs.

**GitLab [H]**
- Classic scopes (`api`, `read_api`, `read_repository`, `write_repository`, `self_rotate`, …) apply to personal, group and project tokens. [scopes](https://docs.gitlab.com/security/tokens/access_token_scopes/)
- Fine-grained PATs went GA in 19.2: resource plus CRUD permission pairs, grouped into Group-and-project, User and Global boundaries. Every request passes **two checks**: the token holds the permission, and the owning user can perform the operation. [fine-grained](https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens/)
- Expiry defaults to 365 days with an admin-configurable maximum. Rotation makes the old token inactive immediately. Usage info shows last-used time and the last five distinct IPs. Service-account tokens are separate. [PAT docs](https://docs.gitlab.com/user/profile/personal_access_tokens/)

**Proposed rukie token record [M: design synthesis]**
- `id` (`rkt_…`, visible) plus a stored secret hash.
- `principal_id` and `principal_kind` (human | agent | service).
- `on_behalf_of` (the accountable human) and `parent_token_id` (child scopes must be a subset of the parent's).
- `scopes[]` as `resource:action` strings equal to the table's `Op.Scope` values, e.g. `repo:read`, `issue:write`, `push:write`, `token:admin`.
- A `resources` allow-list (orgs, repos, ref patterns).
- Mandatory `expires_at`, with a short default for agents (e.g. 7 days), and `not_before`.
- Optional `ip_cidrs`, `rate_policy`, `bound_key` (RFC 9421 key thumbprint) and `max_uses`.
- Lifecycle fields: `rotated_from`, `revoked_at/by/reason`, `last_used_at/ip`, `use_count`.
- Dual check, GitLab-style: the token holds the scope **and** the principal's own repo permission allows the operation.
- Migration: map today's coarse `AccessTokenScopeCategory` values to the finer scopes.

**Audit row [M: design]**
- Identity: `event_id` (ULID), `ts`, `request_id`, `token_id`, `principal_id/kind`, `on_behalf_of`, optional `agent_run_id`.
- Operation: `surface` (http|mcp|cli|html), `op_id`, `method`, path template, resource ids, `effect`.
- Authorization: `scope_required`, `scope_granted`, `guards_evaluated`, `decision` with reason.
- Request controls: `idempotency_key`, `if_match`, RFC 9421 `keyid`.
- Result: `status`, problem `type`, `latency_ms`.
- Provenance: Ruk op id and request digest (link to Ruk's `op.log`), `ip`, `user_agent`.
- Append-only; exposed through the SSE tail and queryable.

## 7. The operation table and its derived surfaces

```go
type Op[In, Out any] struct {
    ID, Summary, Description string   // "issue.create" -> operationId "issue-create"
    Method, Path    string            // Go 1.22 ServeMux pattern
    Scope           string            // "issues:write"
    Requires        []Guard           // token, repo reader/writer(unit), admin, owner, not-archived
    Effect          Effect            // Read | Write | Destructive
    Idempotent      bool              // supports Idempotency-Key
    RequiresIfMatch bool
    Problems        []Problem         // type URI, HTTP status, retryable, Ruk code
    Emits           []EventType       // webhook/SSE catalogue
    CLI             CLISpec           // verb path, aliases, positional params
    MCP             MCPSpec           // name override, hidden
    UI              UISpec            // Form bool, page template
    RawSchema       *RukSchema        // non-nil for Ruk-proxied ops; see below
    Handler         func(context.Context, *In) (*Out, error)
}
```

`op.Register(table, op)` stores the op, calls `huma.Register` with `Operation{OperationID, Method, Path, Security, Errors, Extensions{x-rukie-scope, x-rukie-effect}, Metadata{"op": &meta}}`, and wraps the handler in the shared chain: authN → scope + `Requires` guard check → idempotency → audit → handler. `Requires` replaces the per-route middleware in today's `api.go`; the MCP `tools/list` filter, the CLI help and the OpenAPI `security` entries all read it.

| Surface | Derived from | Mechanism | Dispatch |
|---|---|---|---|
| A. JSON API `/api/v1/**` | `Method`, `Path`, `In`/`Out` | Huma on `humago` | Huma parses and validates, then the shared chain runs |
| A2. `/openapi.json` (3.1), `/openapi-3.0.json` | same | Huma | Render the docs page server-side |
| A3. Errors | `Problems[]` | `huma.NewError` override, RFC 9457 | All surfaces |
| B. MCP (`/mcp` streamable HTTP, `rukie mcp` stdio) | `ID`, `Summary`, `Effect`, merged params and body schemas, `Scope`, `Requires` | low-level `Server.AddTool` | In-process: decode, validate, call the shared chain (no HTTP hop) |
| C. CLI `rukie <noun> <verb> --json` | `CLI`, `In` schema, `Scope` | cobra commands built at startup | HTTP client to the API, never the DB, so auth, scope and audit are identical |
| D. HTML forms | `UI.Form`, `In` schema | server-side template | Hand-written urlencoded decoder, then the same chain |
| E. SSE `/api/v1/events` | `Emits[]` | Huma `sse.Register` | Agents only |
| F. Webhook catalogue | `Emits[]` | OpenAPI `webhooks` section plus signed delivery | Standard Webhooks sender |
| G. `rukie schema` / `GET /api/v1/ops` | the table | JSON dump | n/a |

**Ruk-proxied ops — one rule: the surface that enforces a schema must hold the real schema.** [H/M]
- Ruk's v1 model is already a typed table: 47 methods (34 `mutation`, 10 `read`, 2 `bootstrap`, 1 `anonymous`), 302 `$defs`, about 30 error codes with `exit_status`. It uses 75 `anyOf`, 49 `oneOf`, 41 `const` and one `propertyNames`, with refs of the form `#/$defs/X`. [H: `/tmp/ruk-model.json`]
- `huma.Schema` cannot carry `$defs` or `propertyNames` and would not validate them, so do **not** route Ruk schemas through `SchemaProvider`.
- Instead, `Op.RawSchema` holds the raw Ruk schema plus its `$defs` bundle.
  - MCP gets that schema embedded with its own `$defs` (same dialect).
  - Ruk stays the validation authority; set `Operation.SkipValidateBody: true` for these ops.
  - The served OpenAPI is post-processed to insert the schemas into `components.schemas`, rewriting `#/$defs/X` to `#/components/schemas/X`. This is a design option, unverified (L).
- Native ops (accounts, tokens, issues, webhooks) use plain Go structs with full Huma validation.
- Ruk's `kind` (read vs mutation) seeds `Effect`; `error_codes[].exit_status` seeds the CLI exit-code map.
- Before relying on this, run a fidelity test: take one Ruk method through the OpenAPI path and diff it against the source schema.

**B. MCP details [H spec; M mapping]**
- Tool name equals `ID`. The spec allows `[A-Za-z0-9_.-]`, 1–128 characters, case-sensitive, unique per server. [MCP tools spec](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)
- For native ops, `inputSchema` is one flat object: path, query and header params become properties tagged `x-rukie-in`, merged with the body's properties. A name collision panics at startup.
- **Do not copy Huma's OpenAPI refs as-is.** Huma emits `$ref: "#/components/schemas/X"`, and those targets do not exist inside a single tool schema. Resolve them into a local `$defs`, rewrite refs to `#/$defs/X`, and strip the `$schema` property Huma adds to bodies. Add a startup test that runs every result through the go-sdk `jsonschema-go` `Resolve()`. This is an unverified design risk until that test exists.
- `outputSchema` is the response body schema. Return `structuredContent` plus a serialized text block, as the spec suggests.
- Annotations: `readOnlyHint` from `Effect==Read`, `destructiveHint` from `Destructive`, `idempotentHint` from `Idempotent`. Clients treat annotations as untrusted. [MCP spec]
- `tools/list` may vary by presented authorization, so filter by token scopes and guards (GitHub's pattern). Keep ordering deterministic. [MCP spec]
- Domain errors return `isError: true` with the problem JSON. Only unknown tools or malformed arguments use JSON-RPC `-32602`.
- Optionally mark `repo`/`org` with `x-mcp-header` so streamable HTTP mirrors them as `Mcp-Param-*` headers for routing. [MCP spec]
- The low-level `AddTool` leaves argument validation to the caller; use the same validator as HTTP. [go-sdk `Server.AddTool` doc]

**C. CLI details [M]**
- Path params become positional arguments. Scalar query and body fields become `--kebab-case` flags. Complex bodies take `--body @file|-`.
- Global flags: `--json` (always a machine envelope), `--jq`, `--if-match`, `--idempotency-key` (auto-generated for non-idempotent ops), `--token`, `--server`. Modeled on gh's `--json`/`--jq`.
- Exit codes come from the problem type, reusing Ruk's `exit_status` mapping (E_STALE=3, E_CONFLICTED=2, E_OFFLINE=4, and so on). [H: `/tmp/ruk-model.json`]

**D. HTML forms details [M: design]**
- `string` → text input, with `minLength`/`maxLength`/`pattern`/`required` as HTML attributes. `enum` → select. `boolean` → checkbox. Integer → number input with min/max. Array of strings → textarea, one per line.
- Hidden fields: a CSRF token, an `Idempotency-Key` (a fresh UUID per render) and `_etag`, which becomes `If-Match`.
- POST/Redirect/GET. Validation failures re-render the form, mapping Huma-style `errors[].location` to field messages. A 412 renders a conflict page.
- A schema-driven decoder parses `application/x-www-form-urlencoded`, since Huma does not.
- Read pages are normal templates that consume the same `Out` types by calling the handler directly.

**Gates to add [M]**
- Every op has `Scope`, `Requires`, `Effect`, a summary and at least one declared problem.
- MCP names are unique, within 128 characters and the allowed charset. Flatten collisions and CLI flag collisions fail the build.
- Every generated schema validates against the 2020-12 meta-schema.
- A conformance test sends the same `In` through HTTP, MCP, CLI and HTML and asserts identical outputs and audit rows.
- A golden test on nullability and `$defs` shape, since Huma's 3.1 nullability output is unverified.
- The Ruk fidelity round-trip described above.

## 8. Open items (unverified)

- ogen's OpenAPI 3.1 coverage.
- `humago` ServeMux overhead specifically.
- Huma's support for the top-level OpenAPI `webhooks` section (surface F); otherwise inject it by post-processing.
- Whether kong has a dynamic-command API.
- `invopop/jsonschema`'s default draft.
- RFC 9530 requirements for signed bodies.
- Whether rukie's Idempotency-Key maps 1:1 onto Ruk request ids.
- Whether Huma's `Link` and `$schema` defaults can be suppressed without forking.
- What `kin-openapi` v0.149.0 is used for in rukie today, and whether any `api.go` handlers survive the rewrite as `Handler` functions.

---

### Critical Files for Implementation

- `/tmp/ruk-model.json` — Ruk schemas, error codes and exit statuses for the raw-schema ops and the fidelity test.
- `/home/alpha/rewrite/git-renewed/rukie/routers/api/v1/api.go` — the route and guard inventory to be re-expressed as table entries.
- `<rukie>/internal/op/table.go` (to create) — `Op[In,Out]` with `Requires []Guard` and `RawSchema`, plus the registry.
- `<rukie>/internal/op/mount_huma.go` (to create) — the only Huma import: `humago` mounting, the problem+json override, `SkipValidateBody` for Ruk ops, and OpenAPI post-processing.
- `<rukie>/internal/op/derive_mcp.go` (to create) — ref-to-`$defs` rewrite, params-plus-body merge and the `Resolve()` startup test.