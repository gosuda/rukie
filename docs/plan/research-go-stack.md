I could not write `local://research-go-stack.md`: this role has no write tool, and plan mode is read-only. The full report is below, so Main can persist it as that artifact. Findings are as of 2026-10-19; live data came from endoflife.date, proxy.golang.org and the upstream repos. Labels: **V** = Verified in a primary source this session, **P** = Probable, **U** = Unverified.

## 0. Toolchain ground truth (release channel, release date, end of support)

| Item | Version / date | Support window | Channel |
|---|---|---|---|
| **Go 1.27** | 1.27.2 (2026-10-08); 1.27.0 on 2026-08-19 (V) | Supported until two newer majors exist, so about Go 1.29 (~Aug 2027) (P, inferred from the policy) | https://go.dev/doc/devel/release, https://endoflife.date/api/v1/products/go/ |
| Go 1.26 | 1.26.9 (2026-10-08); 1.26.0 on 2026-02-10 (V) | Until Go 1.28 (~Feb 2027) (P, inferred); endoflife.date has `eolFrom: null` (V) | same |
| Go 1.25 | 1.25.14 | **EOL 2026-08-19** (V) | same |

Rust, Podman, SSH and the Go library stack follow in their own sections.

- **Rust (a):** ruk pins `channel = "1.99.1"` (`/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml`).
  - Rust stable is 1.99.1, per https://releases.rs/ and the manifest at https://static.rust-lang.org/dist/channel-rust-stable.toml (`date = "2026-10-15"`). endoflife.date agrees: 1.99.0 on 2026-10-01 and 1.99.1 on 2026-10-15. **V**
  - The pin is current. Beta is 1.100.0, due 2026-11-12 (releases.rs).
  - Rust has no LTS. endoflife.date marks 1.98 EOL on 2026-10-01, i.e. only the newest stable is supported. https://endoflife.date/api/v1/products/rust/
- **Podman (c):** latest stable is **6.1.4** (2026-10-12). 6.1.0 shipped 2026-08-12.
  - 6.0 went EOL 2026-08-12 and 5.8 on 2026-09-26, so only the newest minor is maintained. https://endoflife.date/api/v1/products/podman/
  - Repo and release channel: https://github.com/podman-container-tools/podman/releases/tag/v6.1.4. The repo moved into a CNCF org, and the Go import path became `go.podman.io/podman/v6`. **V**
  - License is Apache-2.0 (P, not re-read).
  - Rootless `podman run` has no stability SLA in the notes I read, but the rootless stack changed in 6.0.0:
    - 6.0 removed slirp4netns, so pasta is now the rootless network stack. It also removed cgroups v1, CNI and iptables; this needs cgroups v2, netavark and nftables.
    - BoltDB was dropped for SQLite, with automatic migration.
    - 6.0.0 fixed CVE-2026-57231, a host-env leak from malformed image `Env` entries.
    - Rootless port forwarding via Pesto/pasta is **experimental** (`rootless_port_forwarder="pasta"`), with a cleanup bug fixed in 6.0.1.
    - Dropping the rootless pause process is also **experimental** (kernel ≥6.18, env-gated).
    - 6.1.0 fixed "containers which failed to start would not clean up".
    - Source: https://raw.githubusercontent.com/podman-container-tools/podman/main/RELEASE_NOTES.md (**V**).
  - Recommendation for CI sandboxing: pin ≥6.1.4 on cgroups v2 with netavark/pasta, and keep the default `rootlessport` forwarder. Treat rootless `podman run` as production-usable (P) but gate on a smoke test, because the networking stack was rewritten in 6.0. Whether the CI hosts meet the cgroups v2/nftables requirements is U.

## 1. Go language and stdlib features useful for a server

Release notes: https://go.dev/doc/go1.27 and https://go.dev/doc/go1.26 (V). Older features: go1.22, go1.23, go1.24 and go1.25 notes (V).

| Feature | Since | Use |
|---|---|---|
| `ServeMux` method and wildcard patterns, `Request.PathValue` | 1.22 | Router without a dependency |
| Range-over-func iterators; `slices`/`maps` iterator funcs; `unique` | 1.23 | Streaming pagination, interning |
| `os.Root` / `os.OpenRoot` (traversal-safe file ops) | 1.24 | Serving blobs and attachments from a data dir |
| `weak` package, `runtime.AddCleanup`, `testing.B.Loop`, tool directive (`go get -tool`) | 1.24 | Caches, benchmarks, pinning templ/golangci-lint |
| `testing/synctest` GA (`synctest.Test`, `Wait`) | 1.25 | Deterministic tests of timeouts and mirror retries; `synctest.Sleep` added in 1.27 |
| `http.CrossOriginProtection` | 1.25 | Token-less CSRF defence via Fetch metadata — fits a zero-JS form UI |
| `sync.WaitGroup.Go`; container-aware `GOMAXPROCS` | 1.25 | |
| Green Tea GC default (10–40% less GC overhead claimed); `new(expr)`; `errors.AsType[T]`; `slog.NewMultiHandler` | 1.26 | |
| ServeMux trailing-slash redirect now 307; `url.Parse` rejects `http://::1/` | 1.26 | Behaviour changes to test |
| **`encoding/json/v2` + `jsontext` are real packages**, and `encoding/json` is now backed by v2 | 1.27 | Opt-out is `GOEXPERIMENT=nojsonv2`. v2 rejects invalid UTF-8 and duplicate keys by default. `inline` tag renamed `embed`. |
| Generic methods; `uuid` package; goroutine-leak profile GA; `net/http` `Server.MaxHeaderValueCount`, HTTP/2 priorities; `httptest.NewTestServer`; `database/sql` `RowsColumnScanner`/`ConvertAssign` | 1.27 | |
| `crypto/mldsa`, ML-KEM hybrid TLS defaults | 1.26–1.27 | |

Notes:
- The json/v2 claim comes from the 1.27 notes, which describe it as "no longer experiment-gated". Its API is still evolving, since options and tags were renamed or removed late. Confidence P that using v1 `encoding/json` is the safe default for wire formats.
- Patch hygiene matters for a forge. 1.27.2 and 1.26.9 (2026-10-08) carry security fixes in `html/template`, `net/http`, `crypto/tls` and `os`. Track patch releases.
- Floors required by the stack: templ module `go 1.26.0`, goose ≥1.26, modernc ≥1.26, ncruces `go 1.26.0`, MCP SDK `go 1.25.0`, pgx ≥1.25. **Recommendation: `go 1.27.0` + toolchain go1.27.2.** `go mod init` defaults to N-1 (1.26 notes), so set it explicitly.

## 2. Templating — recommend **templ**

- **templ** v0.3.1070 (2026-10-04), MIT, single org-style repo, releases roughly weekly. V. Release channel: https://github.com/a-h/templ/releases/latest. Its `go.mod` says `go 1.26.0`, so generated code forces Go ≥1.26.
- **Generate workflow**:
  - `templ generate` turns `*.templ` into Go files (`_templ.go`); `templ fmt -fail .` gives CI format checking.
  - Install pinned via `go get -tool github.com/a-h/templ/cmd/templ@<ver>` and run with `go tool templ`.
  - `-include-version=false` makes output deterministic (upstream's own CI uses it). Flags `-watch`, `-proxy`, `-lazy` and `-w` are listed in the CLI docs.
  - https://templ.guide/developer-tools/cli/, https://templ.guide/quick-start/installation/ (V)
- **API needed**: `templ.Component` (`Render(ctx, w) error`), `templ.Handler(c, ...)`, `templ.ComponentFunc`, `templ.Join`, `templ.WithChildren`, `templ.URL` / `templ.SafeURL`, `templ.Raw`. Composition is plain Go function calls, `@Comp(args)` and `{ children... }`. https://templ.guide/syntax-and-usage/template-composition/ (V)
- **Streaming**: `templ.Handler(c, templ.WithStreaming())` plus `@templ.Flush() { ... }` flushable areas. The default is buffer-then-write, which lets you set a status code on render failure. https://templ.guide/server-side-rendering/streaming/ (V). Its "suspense" pattern needs Declarative Shadow DOM, which is a browser feature rather than JS. Cross-browser support is U.
- **Escaping**:
  - All attributes are HTML-escaped. URL attributes (`href`, `src`, `action`) are scheme-sanitized to `about:invalid#TemplFailedSanitizationURL`. https://templ.guide/syntax-and-usage/attributes/ (V)
  - `onClick`/`on*` and `script` templates emit `<script>`, which violates zero-JS. Ban them by convention or a lint rule; the exact lint mechanism is U.
- **Performance**:
  - Vendor benchmark (2023, M-series): templ 369 ns/op, 6 allocs vs html/template 2475 ns/op, 38 allocs. https://raw.githubusercontent.com/a-h/templ/main/benchmarks/templ/README.md (V that the claim exists; P that it is representative — old and self-published).
- **Type checking**: views are compiled Go functions with typed parameters, so field/type errors are compile errors.
- **html/template**: stdlib, with contextual auto-escaping and a "template authors trusted, data untrusted" model. https://raw.githubusercontent.com/golang/go/master/src/html/template/doc.go (V). Templates are parsed at runtime and `Execute` takes `any`. Field and typo errors surface at runtime (P by design). It also needed repeated security patches in 1.26.x/1.27.x (release history, V).
- **Pick templ**, for compile-time typing, streaming and less allocation. Cost: a codegen step in CI, a third-party tool pinned in `go.mod`, and a single-maintainer risk (P). Mitigation: generated files are plain Go, so a freeze is survivable.

## 3. Data layer

### SQLite without cgo

| | **modernc.org/sqlite** | **ncruces/go-sqlite3** |
|---|---|---|
| Latest tag | v1.60.1 (2026-09-29). Changelog on master lists v1.61.0 (2026-09-30) but the tag is not published (atom feed lists v1.60.1 as newest; U on a later tag). V | v0.35.6 (2026-09-23); **pre-1.0**. V |
| License | BSD-3 (+ SQLite public domain, sqlite-vec MIT) | MIT (pkg.go.dev for ext/fts5; the wasm2go helper is MIT) |
| Engine | SQLite 3.53.4 transpiled C→Go (ccgo). Requires Go ≥1.26. | SQLite 3.53.4 as Wasm→Go via `wasm2go`, no cgo, no wazero. `go 1.26.0`. |
| Support policy | "Only the latest release is supported" (SECURITY.md per changelog); SBOM and provenance shipped. | Single maintainer; Wasm sandbox means **higher memory per connection** (README caveat). |
| WAL / locking | WAL works. Fault in `-shm` mmap no longer crashes (v1.60.0). OFD locks opt-in via `MODERNC_SQLITE_OFD_LOCK=1` or `OFDLocking(true)`. | Pure-Go VFS: OFD locks on Linux/macOS (needs Linux ≥3.15), `mmap` shm for WAL on Unix. Windows WAL corruption fixed in v0.35.3. |
| Concurrency | `database/sql` pool; each connection has its own page cache and libc state — **bound with `SetMaxOpenConns`**. Pinned `modernc.org/libc` version must match this repo's `go.mod` (fragile). | goroutine-safe via `database/sql`; one conn must not be used concurrently. |
| FTS5 / JSON | **FTS5, JSON1, RTREE, GEOPOLY, STAT4, MATH, SESSION, sqlite-vec compiled in** (`libsqlite3` generator.go `-DSQLITE_ENABLE_FTS5`…). V | FTS5 via `ext/fts5.Register(conn)`; JSON built into the Wasm build; vec1, RTREE and others via `ext/*`. FTS5 appears to need explicit registration (P). |
| Performance | CPU-bound work ~1.3–2.0× slower than C (own docs). | "Competitive" per own README. |

Benchmark (cvilsmeier go-sqlite-bench, 2026-03-23, Go 1.26.0, **journal_mode=DELETE** not WAL, lower is better; https://github.com/cvilsmeier/go-sqlite-bench):
- Simple insert 1M rows: modernc 2419 ms vs ncruces 2719 ms.
- Real insert: modernc 1759 vs ncruces 1469.
- Concurrent query, N=8: modernc 2061 vs ncruces 2355.
- Large rows, N=50000: modernc 401 vs ncruces 151.
- Verdict: roughly a wash, and the author says "do not trust benchmarks". These numbers are not WAL numbers, so U for your workload.

**Recommendation: modernc.org/sqlite**, because it is semver-1.x, has the broadest compiled-in feature set including FTS5 and JSON, and has a documented security and SBOM policy. Keep the store behind a small interface so ncruces can replace it.
- Open with one writer connection and a reader pool, WAL on, `busy_timeout` set. Shorthand DSN params (`_journal_mode`, `_synchronous`, `_foreign_keys`, `_busy_timeout`) were added in v1.55.0, and `_busy_timeout` takes milliseconds ("5s" now fails). `StrictPragmas` is opt-in. V.
- The `_pragma=name(value)` DSN form is P.
- modernc `_time_format` behaviour is a gotcha: Go's monotonic suffix can break `WHERE ts = ?` unless `_time_format=string_no_monotonic` (unreleased v1.61.0) — use integer timestamps via `_time_integer_format` (V, v1.39.0).

### Postgres (fallback): `jackc/pgx/v5`
- v5.11.0 (2026-09-07), MIT (P), min Go 1.25. Uses Go 1.27 `driver.RowsColumnScanner` in `stdlib`. https://raw.githubusercontent.com/jackc/pgx/master/CHANGELOG.md (V).
- Active: 5.10.0 (2026-06-03) was a hardening release (`require_auth`, bounded decoders). 5.9.2 (2026-04-18) fixed a SQL injection via dollar-quoted placeholders in the **simple protocol** — keep the default extended protocol.
- 5.11.0 changes connection-string parsing to match libpq exactly, which can break existing DSNs.

### Migrations
- **golang-migrate** v4.20.1 (2026-09-09): CLI plus `iofs` source. Its sqlite driver uses modernc and wraps each migration in an implicit transaction by default (`x-no-tx-wrap` opts out). https://raw.githubusercontent.com/golang-migrate/migrate/master/database/sqlite/README.md (V). License is MIT (U).
- **goose** v3.29.0 (2026-10-11), MIT, **min Go 1.26**. `goose.NewProvider(dialect Dialect, db *sql.DB, fsys fs.FS, opts ...ProviderOption) (*Provider, error)` with `Up`, `Down`, `Status`, `HasPending`. Methods are concurrency-safe. Dialects include `DialectSQLite3` and `DialectPostgres`. V (`provider.go`, `dialect.go`).
- **Recommendation**:
  - For a SQLite-first forge with embedded SQL and forward-only migrations, write the **tiny in-tree runner**: `fs.FS` of `NNNN_name.sql`, a `schema_migrations(version, checksum, applied_at)` table, one tx per file, `BEGIN IMMEDIATE`, ~100 lines. This is a design judgement (P) that avoids a heavy dependency graph.
  - Use goose's `Provider` only if you commit to dual SQLite/Postgres. Skip golang-migrate: its URL-driver model fits an embedded runner poorly.

## 4. MCP

- **github.com/modelcontextprotocol/go-sdk v1.8.0** (2026-09-14), Apache-2.0 for new code / MIT for existing, `go 1.25.0`. V. Release channel: https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0. It supports spec 2026-07-28 plus 2025-11-25 down to 2024-11-05 (README compatibility table, V). Support policy: tracks supported Go versions.
- **MCP spec current revision: 2026-07-28.** https://modelcontextprotocol.io/specification/versioning (V).
  - It is a stateless revision: no protocol sessions, no GET stream, no SSE resumability, and server-to-client asks go through multi-round-trip results. https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http.md (V).
  - Roots, sampling and logging are deprecated (README, V).
- **Streamable HTTP server**:
  - `mcp.NewStreamableHTTPHandler(getServer func(*http.Request) *mcp.Server, opts *mcp.StreamableHTTPOptions) *StreamableHTTPHandler`.
  - Options include `Stateless bool`, `JSONResponse bool`, `EventStore`, `SessionTimeout`.
  - **Requests at 2026-07-28 are accepted only when `Stateless = true`** (docs/protocol.md, V). Plan a stateless handler.
  - `ServerOptions.SupportedProtocolVersions` can narrow versions.
  - `StreamableHTTPHandler` is a plain `http.Handler`, so it mounts on `ServeMux`. Origin validation is required by the spec; the SDK removed the `enableoriginverification`/`disablelocalhostprotection` escape hatches in v1.8.0 (V). `CrossOriginProtection` is the stdlib complement.
- **Auth (OAuth 2.1 resource server)**:
  - `auth.RequireBearerToken(verifier TokenVerifier, opts *RequireBearerTokenOptions) func(http.Handler) http.Handler`.
  - `type TokenVerifier func(ctx, token string, req *http.Request) (*TokenInfo, error)`.
  - `TokenInfo{Scopes, Expiration, UserID...}`, `auth.TokenInfoFromContext`.
  - `auth.ProtectedResourceMetadataHandler(*oauthex.ProtectedResourceMetadata)` serves RFC 9728 metadata; the middleware emits the `WWW-Authenticate` header with `resource_metadata`.
  - Spec requirements for us: servers must implement RFC 9728, validate that the token audience equals this server (RFC 8707), and send 401 for invalid tokens. https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization.md (V). **Audience checking is our `TokenVerifier`'s job, not the SDK's.**
  - Client-side OAuth is marked experimental in the README.
- **Tool schema from Go structs**:
  - `mcp.AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])`, with `ToolHandlerFor` returning `(*CallToolResult, Out, error)`.
  - Input and output JSON Schema are inferred from the Go types via `github.com/google/jsonschema-go v0.4.3` (`jsonschema:"desc"` struct tags, `jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: ...})` for overrides). Inputs are validated and outputs are placed in `StructuredContent`.
  - Constraints and custom schemas: docs/server.md (V).
- **Recommendation**: use the official SDK for HTTP + stdio, with stateless streamable HTTP behind `RequireBearerToken` and a verifier backed by the forge's own token store. Whether the existing `ruk mcp` stdio server could be reused is U.

## 5. Markdown / highlighting / sanitising

- **goldmark** has **two live majors**.
  - v2.1.6 (2026-09-27), whose v2.0.0 was the first official release on 2026-08-27. https://github.com/yuin/goldmark/releases/tag/v2.1.6, module `github.com/yuin/goldmark/v2`.
  - v1.8.6 (2026-09-03).
  - v2 README (V): "still in the early stages of release"; "if an extension doesn't support v2, use v1"; maintenance covers one major prior; CommonMark 0.31.2; AST nodes carry start positions; parser and renderer are separated; zero dependencies.
  - **GFM in v2**: `extension.GFM` = Table + Strikethrough + Linkify + TaskList, with footnotes, definition lists and typographer as separate extensions. The GFM extension does **not** filter disallowed raw HTML tags.
  - `html.WithUnsafe` is off by default, so raw HTML and dangerous links are not rendered.
  - v2 API: `parser.New(parser.WithExtensions(...))`, `html.New(html.WithExtensions(...))`, `doc := p.Parse(src)`, `r.Render(w, src, doc)`.
  - License MIT is P (not re-read). **Recommend v2** for a greenfield forge, with built-in GFM only; the AST/position model also helps agents. Risk: third-party ecosystem is v1-only (U on specific extensions).
- **chroma/v2** v2.27.0 (2026-06-17), master active through 2026-10-18 (latest commit); license MIT is P.
  - Tagging lags master by ~4 months (P).
  - Zero-JS pattern: `html.New(html.WithClasses(true), html.WithLinkableLineNumbers(...))` plus `formatter.WriteCSS(w, style)`. Light/dark via `WithModeClasses(true)`. The README example `quick.Highlight(w, code, "go", "html", "monokai")` also exists.
  - Detect language via `lexers.Match(filename)`, `lexers.Get(name)` or `lexers.Analyse(src)`. V.
- **bluemonday** v1.0.27 (2024-07-04); last commit on master 2025-04-04 ("transparent color"). **Low-activity**: no release in ~27 months and no commit in ~18 months (V). Stable but not obviously maintained.
  - Recommendation: don't add it by default. The pipeline goldmark (no unsafe) → templ escaping avoids needing a sanitizer. Add bluemonday only if you must allow raw HTML in issues, and then pin and fuzz it.
  - I did not find a more active Go sanitizer; that search was not exhaustive (U).

## 6. Auth

- **`golang.org/x/crypto` v0.58.0** (2026-10-09), BSD-3 (P).
- **argon2**: `argon2.IDKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte` (memory in KiB). Docs cite RFC 9106: first option t=1, m=2 GiB, p=4; second option t=3, m=64 MiB, p=4. V (argon2.go).
  - **A server cannot afford 2 GiB per login**, so use the second option and cap concurrent hashes with a semaphore (design judgement). Store parameters, salt and hash in a PHC-style string — the encoder is yours to write, since `x/crypto` has none.
- **go-oidc** v3.21.0 (2026-09-01), Apache-2.0.
  - API: `oidc.NewProvider(ctx, issuer)`, `provider.Endpoint()`, `provider.Verifier(&oidc.Config{ClientID: ...})`, `verifier.Verify(ctx, rawIDToken)`, `IDToken.Claims(&v)`, `provider.UserInfo(ctx, tokenSource)`. V.
- **golang.org/x/oauth2** v0.37.0 (2026-08-25). PKCE helpers: `oauth2.GenerateVerifier()`, `oauth2.S256ChallengeOption(v)`, `oauth2.VerifierOption(v)`, plus `Config.AuthCodeURL`/`Exchange`. V.
- **TOTP: `pquerna/otp`** v1.5.0 (2024-12-31). Last commit 2025-08-07 (comment typo). Its last real change was a Dec-2024 security fix (`io.ReadFull` for secret generation). Stable but dormant; license Apache-2.0 is P. The practical alternative is ~40 lines of RFC 6238 over `crypto/hmac`. Either way: per-account rate limiting and replay protection (last-used counter) are yours to write.
- **x/crypto/ssh — server suitability for a git SSH endpoint (b)**: **suitable (V).**
  - **Production precedent**: `rukie/modules/ssh/server.go` already runs a Gitea-style git SSH server on `gossh.NewServerConn` with `PublicKeyCallback`, session-only channels, and `exec` payload parsing via `gossh.Unmarshal`, with `exit-status` returned via `SendRequest("exit-status", ...)`. Lines 20–147 (V).
  - **Server API** (V, `ssh/server.go` + `example_test.go`):
    - `ssh.NewServerConn(net.Conn, *ServerConfig)` returns `(*ServerConn, <-chan NewChannel, <-chan *Request, error)`.
    - `ServerConfig.PublicKeyCallback(ConnMetadata, PublicKey) (*Permissions, error)` is where authorized keys are looked up; per-connection state goes in `Permissions.Extensions`.
    - Also: `VerifiedPublicKeyCallback`, `NoClientAuthCallback`, `KeyboardInteractiveCallback`, `AuthLogCallback`, `MaxAuthTries` (default 6), `PublicKeyAuthAlgorithms`, `AddHostKey`.
    - `ssh.SupportedAlgorithms()` excludes SHA-1 algorithms.
    - Cert support via `CertChecker`; `ssh.FingerprintSHA256`; `ssh.ParseAuthorizedKey`.
  - **Crypto defaults** (V, `ssh/common.go`): default kex includes `mlkem768x25519-sha256` (PQ hybrid) then `curve25519-sha256`; also `sk-ssh-ed25519`, `sk-ecdsa` keys and ML-DSA keys.
  - **Gaps you must write**:
    - No pre-auth connection or handshake deadline, and no per-IP rate limiting (use `net.Conn` deadlines and a limiter).
    - No exec-session helper: you pipe the channel's stdin/stdout/stderr yourself, handle EOF and `CloseWrite`, and send `exit-status`.
    - No SFTP, but git needs none.
    - Reject `pty-req`, `subsystem` and forwarding channels.
    - Handle the `env` request, because git v2 sends `GIT_PROTOCOL` (P, from git behaviour).
    - `PublicKeyCallback` may fire several times per connection (offer queries before signing), so keep it cheap and idempotent (P, from the source comment).
  - **Wiring to Ruk**: the daemon's `CommandOpen` for git-shaped commands is the natural target for a session's `exec` payload. This mapping is U (not verified in the ruk daemon source this session).

## 7. Diff parsing

- Ruk's typed `diff` reply is `files: Page<FileDiff>`; each `FileDiff` has `{path, old_path, kind, old/new DiffBlob{oid, executable}, conflicted, body: "binary" | {unified:{patch: ByteSource}}}`. So you get a **unified patch per file** plus the metadata structure, and only need a hunk parser. `/tmp/ruk-model.json` `$defs/FileDiff`, `DiffBody` (V).
- **sourcegraph/go-diff v0.9.0** (2026-09-10), MIT, active (7 PRs in this release). It only parses and prints, and does not compute diffs. API: `diff.ParseHunks([]byte) ([]*Hunk, error)`, `diff.ParseFileDiff`, `diff.ParseMultiFileDiff`, `diff.ReverseFileDiff`. V.
- **bluekeyes/go-gitdiff v0.9.0** (2026-07-18), MIT. Parses and applies git patches including binary fragments (`--binary`) in strict mode, with size limits on binary apply. `gitdiff.Parse(io.Reader) ([]*File, string, error)`, `TextFragment.Lines []Line{Op, Line}` with ops `OpContext`/`OpDelete`/`OpAdd`. V.
- **Recommendation**: `go-diff` `ParseHunks` on Ruk's per-file `patch` for rendering. If applying a patch is ever needed it should go through Ruk, so `go-gitdiff` is unnecessary. Alternative: a ~150-line in-tree hunk parser, which is trivial against Ruk's already-structured output. Word-level (intra-line) diff is not provided by either; the library for it is U.

## 8. golangci-lint v2

- **v2.15.0** (2026-10-16), immutable release; schema `version: "2"`. https://github.com/golangci/golangci-lint/releases/tag/v2.15.0 (V). License is GPL-3.0 (U — not re-read; run as an external tool).
- **Config schema** (https://golangci-lint.run/docs/configuration/file/, V): file names `.golangci.{yml,yaml,toml,json}`, validated by https://golangci-lint.run/jsonschema/golangci.jsonschema.json.
  - Top-level keys: `version: "2"`, `linters`, `formatters`, `issues`, `output`, `run`, `severity`.
  - `linters`: `default` = `standard|all|none|fast`, plus `enable`, `disable`, `settings`, `exclusions{generated, presets, rules, paths, warn-unused}`.
  - **Formatters** now live under `formatters`: `gci`, `gofmt`, `gofumpt`, `goimports`, `golines`, `swaggo`.
  - `issues`: `new`, `new-from-merge-base`, `fix`, `max-issues-per-linter`, etc.
  - `golangci-lint migrate` converts v1 files (comments are lost).
  - `stylecheck`, `gosimple` and `staticcheck` were merged into `staticcheck`.
- **Suggested set** for this forge:
  - `linters.default: standard`, plus `enable`: `gosec`, `noctx`, `sqlclosecheck`, `rowserrcheck`, `bodyclose`, `errorlint`, `sloglint`, `modernize`, `forbidigo`, `depguard`, `contextcheck`.
  - `formatters.enable: [gofumpt, goimports]`.
  - `depguard`/`forbidigo` can ban `html/template` and templ's JS helpers, and `os/exec` of `git`, to enforce the "no stock git" and "zero JS" rules.
  - Go 1.27 compatibility of this exact release is U (it bumps `x/tools` to 0.52, which suggests current).

## Recommendations summary

| Area | Pick | Why |
|---|---|---|
| Language | Go 1.27.2, `go 1.27.0` | Supported to ~Aug 2027; json/v2 + generic methods + 1.25–1.27 net/http security |
| Views | templ v0.3.1070 | Compiled, typed, streaming; vs runtime-checked html/template |
| DB | modernc.org/sqlite v1.60.1 (+ pgx v5.11.0 fallback) | Compiled-in FTS5/JSON, SQLite 3.53.4, 1.x semver |
| Migrations | In-tree runner; goose v3.29.0 only if dual DB | Minimal deps |
| MCP | go-sdk v1.8.0, stateless streamable HTTP, `RequireBearerToken` | Spec 2026-07-28 mandates stateless |
| Markdown | goldmark v2.1.6 + chroma v2.27.0 (CSS classes); skip bluemonday | v2 AST/positions; avoid a dormant sanitizer |
| Auth | argon2id (t=3, m=64 MiB, p=4), go-oidc v3.21.0, oauth2 v0.37.0, TOTP in-tree or pquerna/otp v1.5.0 | Per-RFC 9106 second option |
| SSH | x/crypto v0.58.0 `ssh.NewServerConn` | Same shape as rukie's `modules/ssh` |
| Diff | sourcegraph/go-diff v0.9.0 `ParseHunks` | Ruk already returns per-file unified patches |
| Lint | golangci-lint v2.15.0 | |
| Rust | 1.99.1 (matches ruk pin) | Current stable |
| CI sandbox | Podman ≥6.1.4 | Only latest minor maintained |

## Open questions (all U)
1. Is modernc v1.61.0 going to be tagged (changelog lists it), and does the pinned `modernc.org/libc` constraint conflict with other deps in `go.mod`?
2. WAL-mode benchmarks on our workload for modernc vs ncruces; the published ones use `journal_mode=DELETE`.
3. Which third-party goldmark extensions we need exist for v2.
4. License confirmations: goldmark, chroma, pgx, golang-migrate, golangci-lint (GPL?), pquerna/otp.
5. Does a viable maintained sanitizer replace bluemonday if raw HTML must be allowed?
6. Mapping of an SSH `exec` payload (`git-upload-pack '<repo>'`) onto the ruk daemon `CommandOpen`. Check `crates/ruk/src/daemon/`.
7. Browser support for Declarative Shadow DOM streaming for templ "suspense".
8. Whether the CI hosts meet Podman 6 prerequisites (cgroups v2, nftables, netavark, pasta).
9. WebAuthn/passkeys library choice was not in scope and is not researched.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin matches stable 1.99.1; bump when 1.100.0 lands (2026-11-12).
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — working `x/crypto/ssh` git-endpoint reference (public-key callback, exec, exit-status).
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema (per-file unified patch) and the typed method set to wrap for MCP tools.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/ipc.rs` — `CommandOpen`/`InvocationOpen` framing the SSH and HTTP front ends will call.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

→ read(https://gitlab.com/cznic/libsqlite3/-/raw/master/generator.go:196-222) ⇒ ok · 35 lines
→ read(https://gitlab.com/cznic/libsqlite3/-/raw/master/generator.go:380-400) ⇒ ok · 27 lines
→ read(https://go.dev/dl/?mode=json) ⇒ ok · 302 lines
→ read(https://raw.githubusercontent.com/jackc/pgx/master/LICENSE) ⇒ ok · 28 lines

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Verify three dates before this shi…

---
Two review points changed the report: the modernc FTS5 citation and the SSH evidence. The rest of the review is either already satisfied or not worth acting on. This message supersedes the earlier version where they differ.

## Corrections

- **modernc FTS5/JSON build flags.**
  - The libsqlite3 generator has two flag lists. The first one is at roughly lines 196–240 of `generator.go`. It carries `-DHAVE_USLEEP -DNDEBUG` and sets `-DSQLITE_ENABLE_FTS5`, `JSON1`, `GEOPOLY`, `RTREE`, `STAT4` and `MATH_FUNCTIONS` (line ~225 onward). I read its opening lines this turn, and I read the flag lines in the earlier grep. This is the library build. Source: https://gitlab.com/cznic/libsqlite3/-/raw/master/generator.go
  - The second list, at roughly line 404, adds `-DCONFIG_SLOWDOWN_FACTOR=10.0` (lines ~383–398), so it is the Tcl test-fixture build and not shipped. I am no longer citing it.
  - The `speedtest1` and Windows lists at lines ~558–687 set only `DBPAGE_VTAB` and `RTREE`, so they are not evidence for FTS5.
  - Status: **V** that the shipped library build requests FTS5 and JSON1; **U** whether the Windows targets use the same list; **U** at runtime until `CREATE VIRTUAL TABLE t USING fts5(x)` is run against v1.60.1. JSON is core SQLite since 3.38 and the driver bundles 3.53.4 (changelog), so JSON availability is not in doubt.
  - The generator is from `libsqlite3` master, not the tag vendored by v1.60.0 (libsqlite3 v1.15.0 per the changelog). Pinning to that tag is not done.
- **SSH evidence.** The claim that `x/crypto/ssh` suits a git SSH endpoint rests on the upstream `x/crypto/ssh` source, not on rukie.

[892 more lines in resource. Use :604 to continue]
- **SSH evidence.** The claim that `x/crypto/ssh` suits a git SSH endpoint rests on the upstream `x/crypto/ssh` source, not on rukie.
  - Primary sources: `ssh/server.go` (`ServerConfig`, `NewServerConn`, callbacks), `ssh/example_test.go` (`ExampleNewServerConn`) and `ssh/common.go`, all at https://raw.githubusercontent.com/golang/crypto/master/ssh/. Those files do not show an `exec` handler.
  - The rukie `modules/ssh/server.go` shows `exec` parsing via `gossh.Unmarshal` and an `exit-status` reply. It is a local checkout whose match to upstream Gitea I did not check, so it is corroboration only. The upstream equivalent to cite is `modules/ssh` in go-gitea/gitea, which I have not read.
  - Gaps stay as before: no per-IP rate limit, no handshake deadline, no exec-session helper.
- **Rust pin.** I read the manifest at https://static.rust-lang.org/dist/channel-rust-stable.toml (`date = "2026-10-15"`) and https://releases.rs/ ("Stable: 1.99.1"). Both agree with endoflife.date.
  - `ruk/rust-toolchain.toml` pins `1.99.1`. That equals the latest stable and is a point release of 1.99.0 (2026-10-01).
  - The pin is current, and next beta 1.100.0 is due 2026-11-12.
- **Go 1.27.2.** https://go.dev/dl/?mode=json lists `go1.27.2` first with `stable: true`, so it is the latest patch (V). Its release date of 2026-10-08 comes from the release-history page I read earlier.
- **pgx license.** https://raw.githubusercontent.com/jackc/pgx/master/LICENSE reads "MIT License" (V). `pgx` is now V, not P.
- **go-diff license.** The `sourcegraph/go-diff` LICENSE is MIT text (Sourcegraph, 2014) plus a second MIT notice for code adapted from python-unidiff (Matias Bordese, 2012). GitHub's "Other" label comes from the two notices. https://raw.githubusercontent.com/sourcegraph/go-diff/master/LICENSE (V). The go-diff parse path is **not decided**: Ruk's per-file `unified.patch` is what feeds `ParseHunks`, and an in-tree hunk parser is the alternative.
- **Podman 6.1.4.** I read the tag page https://github.com/podman-container-tools/podman/releases/tag/v6.1.4 (released 12 Oct; CVE-2026-107935 gvproxy fix, WSL 3 fix).
  - The rootless claims came from the 6.0.0 section of `RELEASE_NOTES.md`, which I did read: slirp4netns removed, pasta, experimental Pesto and pause-process changes.
  - "Rootless `podman run` is production-usable" remains **Probable**. I have no primary stability statement, and I did not read the podman.io blog.

## Review points rejected or left as-is

- Stale or already handled: Podman and Rust "not yet read", templ 404 guesses, the generator-flag hunt, grep-on-URL doubts (it works), and the proxy-vs-tag mismatch for modernc. The tag feed confirms v1.60.1 is the latest tag; the v1.61.0 changelog entry is open question 1.
- Not acted on, as low value for the decision: re-fetching the endoflife.date data with jq filters, a goldmark-highlighting v2 compatibility check, and a Windows-only ncruces issue-404 re-read. v0.35.3 notes say the Windows WAL corruption was fixed, and this is a Linux server. I did not re-read the issue.
- The "pquerna/otp used by Gitea/Forgejo" claim never appeared in the report. The in-tree RFC 6238 TOTP option is a design opinion, not a finding.
- The "write tool unavailable" point stands. My toolset here is `read`, `grep`, `web_search` and `ast_grep` only, so the report has to be persisted by Main.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin matches Rust stable 1.99.1; bump after 2026-11-12.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — local corroboration of the exec/exit-status pattern. Verify against upstream Gitea before relying on it.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/ipc.rs` — the framing the SSH and HTTP front ends will call.

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Fix three report entries before …

---
→ grep(struct (CommandOpen|InvocationOpen) @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon) ⇒ ok · 13 lines
→ read(https://raw.githubusercontent.com/golangci/golangci-lint/main/LICENSE:1-5) ⇒ ok · 10 lines

[advisor] <advisory advisor="stickler" severity="blocker" guidance="weigh, don't blindly obey"> Fix the report before Main saves …

---
Four points from the latest review are corrected below. The rest I checked against what I read this session, and they stand. Any other mislabelled claim is a labelling slip, not a missing source.

## Corrections

- **golangci-lint license: GPL-3.0 (V).** `LICENSE` on main opens "GNU GENERAL PUBLIC LICENSE, Version 3, 29 June 2007" (https://raw.githubusercontent.com/golangci/golangci-lint/main/LICENSE). The report listed this as U. Use it as an external tool, not a linked library.
- **Critical Files: the daemon file path was wrong.** `CommandOpen` is defined at `ruk/crates/ruk/src/daemon/mod.rs:390`, with a "Forgejo service session" doc comment. `InvocationOpen` is at `mod.rs:305`. `ipc.rs` holds the framing only. Replace that entry with `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs`. The decode and open handlers at about `mod.rs:1097` and `mod.rs:1207` come from the review; I did not read them. Open question 6 (mapping an SSH `exec` payload onto `CommandOpen`) points at `mod.rs`.
- **Podman EOL dates: secondary source.** The 5.8 EOL of 2026-09-26 and 6.0 EOL of 2026-08-12 come from endoflife.date only, so the label is V-secondary (https://endoflife.date/api/v1/products/podman/). Podman's own channel (https://github.com/podman-container-tools/podman/releases/tag/v6.1.4) shows the 6.1.4 tag (released 12 Oct) and the other release dates, but not those EOL dates.
- **Artifact still not written.** My tools are `read`, `grep`, `web_search` and `ast_grep`, so none can create `local://research-go-stack.md`. Main has to persist it.

## Review points rejected, with the source behind each

- **go-sdk v1.8.0 date.**
  - The release was published 2026-09-14T08:05:46Z, per the GitHub releases JSON (https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0). That is what the report states.
  - The proxy's 2026-09-04 is the tag's commit time.
  - I should have written both dates in the report.
- **x/crypto/ssh default kex.** The earlier `ssh/common.go` grep returned `defaultKexAlgos` at line 98, beginning with `KeyExchangeMLKEM768X25519` then `KeyExchangeCurve25519`. That is the default, not only `supportedKexAlgos`. https://raw.githubusercontent.com/golang/crypto/master/ssh/common.go (V).
- **"V labels with no visible source".** Each one has a source I read:
  - pgx 5.9.2 SQL injection (GHSA-j88v-2chj-qfwx): https://raw.githubusercontent.com/jackc/pgx/master/CHANGELOG.md, section "5.9.2 (April 18, 2026)".
  - goose minimum Go 1.26: https://raw.githubusercontent.com/pressly/goose/main/CHANGELOG.md, v3.28.0 "Minimum Go version is now 1.26".
  - goldmark "one major prior": https://raw.githubusercontent.com/yuin/goldmark/v2.1.6/README.md, "Maintenance policy".
  - ncruces WAL fix: https://github.com/ncruces/go-sqlite3/releases/tag/v0.35.3. The notes say the Windows WAL corruption (issue #404) is fixed and needs Windows 10 1803+ memory placeholders. I did not open issue #404 itself.
  - pquerna/otp `io.ReadFull` secret fix: commit 5971b1ef, "fix(security): Ensure the entire secret is random (#100)" (https://github.com/pquerna/otp/commit/5971b1ef1d6652fec2caed37f11e5cacd9249f78).
  - Podman CVE-2026-57231: https://raw.githubusercontent.com/podman-container-tools/podman/main/RELEASE_NOTES.md, 6.0.0 and 5.8.4.
  - The modernc and ncruces benchmark timings come from a third-party repo, https://github.com/cvilsmeier/go-sqlite-bench, run 2026-03-23 on SQLite's `journal_mode=DELETE`. That is a labelled cross-check (P), not a primary measurement and not a WAL result.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — local corroboration of the exec/exit-status pattern. Upstream `x/crypto/ssh` is the actual evidence; upstream Gitea `modules/ssh` is not yet read.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="stickler" severity="concern" turns_ago="1" guidance="weigh, don't blindly obey"> Attach the exact UR…

[advisor] <advisory advisor="overlycritic" severity="nit" guidance="weigh, don't blindly obey"> Fix one date before Main persists…

---
Rejected: the report's go-sdk v1.8.0 date of 2026-09-14 stands. It is the GitHub release's `published_at` (2026-09-14T08:05:46Z), from `https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0`.

The proxy's 2026-09-04T08:08:52Z is the tag's commit time. That is the same point I made in my last reply (go-sdk date), so I did not re-fetch or re-check anything for this one.

On the claim that every other date uses proxy time, that is wrong in two ways:
- **Release-page dates:** Go (go.dev release history), Podman (tag page), Rust (manifest) and golangci-lint (release page) were taken from release pages, not the proxy.
- **Proxy dates:** the remaining library dates were taken from proxy.golang.org, and I labelled them as tag commit time, not release dates.

For the go-sdk row, use release published 2026-09-14, tag commit 2026-09-04.

The artifact is still not written because I have no write tool. Main has to persist it from my earlier messages. The Critical Files correction there still applies: the daemon framing point is `ruk/crates/ruk/src/daemon/mod.rs`, not `ipc.rs`.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — local corroboration of the SSH exec/exit-status pattern; upstream `x/crypto/ssh` is the actual evidence.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="minimax" severity="nit" guidance="weigh, don't blindly obey"> Close the SSH evidence gap in one call…

---
→ grep("exec"|exit-status|NewServerConn|"env"|PublicKeyCallback|"session" @ https://raw.githubusercontent.com/go-gitea/gitea/m…) ⇒ ok · 44 lines

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Send one consolidated, final resea…

---
# research-go-stack.md — consolidated final (supersedes all earlier messages)

I could not write `local://research-go-stack.md`. My tools here are `read`, `grep`, `web_search` and `ast_grep`, so Main must persist this text. It was assembled on 2026-10-19 and includes every correction made since my first message.

Labels: **V** = verified in a primary source this session, **P** = probable, **U** = unverified. "Tag commit" dates come from proxy.golang.org and are commit times, not release dates.

## 0. Toolchains and support windows

| Item | Current | Dates | Support / EOL | Channel |
|---|---|---|---|---|
| **Go 1.27** | **1.27.2** (V: first stable entry in https://go.dev/dl/?mode=json) | 1.27.0 on 2026-08-19; 1.27.2 on 2026-10-08 | Supported until two newer majors exist, i.e. ~Go 1.29, ~Aug 2027 (P, inferred from the policy at https://go.dev/doc/devel/release) | https://go.dev/doc/devel/release |
| Go 1.26 | 1.26.9 (2026-10-08) | 1.26.0 on 2026-02-10 | Until Go 1.28, ~Feb 2027 (P). endoflife.date has `eolFrom: null` | same |
| Go 1.25 | 1.25.14 | | **EOL 2026-08-19** (V, endoflife.date `eolFrom`; secondary source) | https://endoflife.date/api/v1/products/go/ |
| **Rust stable** | **1.99.1** | 1.99.0 on 2026-10-01; 1.99.1 (point release) on 2026-10-15 | Only the newest stable is supported; 1.98 EOL 2026-10-01 (V-secondary, endoflife.date) | https://static.rust-lang.org/dist/channel-rust-stable.toml, https://releases.rs/ |
| **Podman** | **6.1.4** (V: https://github.com/podman-container-tools/podman/releases/tag/v6.1.4, 12 Oct) | 6.1.0 on 2026-08-12 | 6.0 EOL 2026-08-12, 5.8 EOL 2026-09-26 (V-secondary, https://endoflife.date/api/v1/products/podman/); only the newest minor is maintained | repo above |

**Rust pin (a)**
- The pin in `ruk/rust-toolchain.toml` is `channel = "1.99.1"`.
- It equals current stable. The manifest says `date = "2026-10-15"` and releases.rs says "Stable: 1.99.1". Beta is 1.100.0, due 2026-11-12. V.

**Podman rootless (c)**
- 6.0.0 removed cgroups v1, iptables, CNI and slirp4netns (rootless networking is now pasta), dropped BoltDB for SQLite, and fixed CVE-2026-57231. The 6.0.0 section of https://raw.githubusercontent.com/podman-container-tools/podman/main/RELEASE_NOTES.md (V) says so.
- Pesto/pasta rootless port forwarding and dropping the rootless pause process are marked **experimental**. The default forwarder is `rootlessport`. V, same file.
- 6.0.1 fixed Pesto rule cleanup, and 6.1.0 fixed cleanup after containers that fail to start. V, same file.
- 6.1.4 itself fixes a WSL 3 issue and bumps gvproxy for CVE-2026-107935.
- I found no primary stability statement for rootless `podman run`. **Probable** that it is usable. Recommend pinning ≥6.1.4 on cgroups v2 with netavark/pasta, and gating CI on a rootless smoke test.

## 1. Go language and stdlib useful for a server

Sources: https://go.dev/doc/go1.22, go1.23, go1.24, go1.25, go1.26, go1.27 (V).

| Feature | Since |
|---|---|
| `ServeMux` method/wildcard patterns, `Request.PathValue` | 1.22 |
| Range-over-func iterators, `slices`/`maps` iterator functions, `unique` | 1.23 |
| `os.Root`/`os.OpenRoot` (traversal-safe file ops), `weak`, `runtime.AddCleanup`, `testing.B.Loop`, `go get -tool` | 1.24 |
| `testing/synctest` GA (`Test`, `Wait`), `http.CrossOriginProtection` (token-less CSRF defence using Fetch metadata), `sync.WaitGroup.Go`, container-aware `GOMAXPROCS` | 1.25 |
| Green Tea GC default (10–40% less GC overhead claimed), `new(expr)`, `errors.AsType`, `slog.NewMultiHandler`, ServeMux trailing-slash redirect now 307, `url.Parse` stricter on colons in hosts | 1.26 |
| **`encoding/json/v2` and `jsontext` now regular packages**, with `encoding/json` backed by v2 (opt-out `GOEXPERIMENT=nojsonv2`) | 1.27 |
| Generic methods, `uuid` package, goroutine-leak profile GA, `Server.MaxHeaderValueCount`, HTTP/2 client priorities, `httptest.NewTestServer`, `synctest.Sleep`, `database/sql` `RowsColumnScanner`/`ConvertAssign` | 1.27 |

- json/v2 (1.27): stricter defaults reject invalid UTF-8 and duplicate keys. `inline` was renamed `embed`, and other tags and options were removed. The API is still evolving, so keep v1 semantics for wire formats (P).
- Patch hygiene: 1.27.2 and 1.26.9 carry fixes in `html/template`, `net/http`, `crypto/tls` and `os` (release history, V).
- Floors required by the stack: templ module `go 1.26.0`, goose ≥1.26, modernc ≥1.26, ncruces `go 1.26.0`, MCP SDK `go 1.25.0`, pgx ≥1.25. **Pick `go 1.27.0` with toolchain go1.27.2.** `go mod init` defaults to N−1 (1.26 notes), so set it explicitly.

## 2. Templating: recommend **templ**

- **templ v0.3.1070**, 2026-10-04 (GitHub release `published_at` and the proxy agree), MIT (repo metadata, V). Its `go.mod` says `go 1.26.0`, so generated code forces Go ≥1.26. https://github.com/a-h/templ/releases/latest
- **Workflow**:
  - `templ generate` writes `*_templ.go`; `templ fmt -fail .` checks formatting in CI.
  - Install with `go get -tool github.com/a-h/templ/cmd/templ@<ver>` and run `go tool templ`. Flags: `-include-version=false`, `-watch`, `-proxy`, `-lazy`, `-w`.
  - https://templ.guide/developer-tools/cli/ and https://templ.guide/quick-start/installation/ (V).
- **API**:
  - `templ.Component` (`Render(ctx, w) error`), `templ.Handler(c, ...)`, `templ.ComponentFunc`, `templ.Join`, `templ.WithChildren`/`GetChildren`.
  - `templ.URL`/`templ.SafeURL` and `templ.Raw`.
  - Composition is `@Comp(args)` and `{ children... }`.
  - https://templ.guide/syntax-and-usage/template-composition/ (V).
- **Streaming**: `templ.Handler(c, templ.WithStreaming())` plus `@templ.Flush() { ... }`. The default is buffer-then-write, so a status code can still be set on render failure. https://templ.guide/server-side-rendering/streaming/ (V). Its "suspense" example needs Declarative Shadow DOM, a browser feature; cross-browser support is U.
- **Escaping**:
  - All attributes are HTML-escaped, and URL attributes are scheme-sanitized to `about:invalid#TemplFailedSanitizationURL`. https://templ.guide/syntax-and-usage/attributes/ (V).
  - `on*` handlers and `script` templates emit `<script>`, which breaks zero-JS. Forbid them; how to lint for that is U.
- **Performance**: the vendor benchmark is dated 2023-08-17 (M-series). It gives templ 369 ns/op and 6 allocs vs html/template 2475 ns/op and 38 allocs. https://raw.githubusercontent.com/a-h/templ/main/benchmarks/templ/README.md. It is self-published and old, so **P**, and not a v0.3.1070 number.
- **html/template**: the stdlib has contextual auto-escaping and trusts template authors, not the data. Parse and execute happen at runtime, and `Execute` takes `any`. https://raw.githubusercontent.com/golang/go/master/src/html/template/doc.go (V).
- **Pick templ**: views are compiled, typed Go functions, rendering streams, and allocations are lower. The cost is a codegen step in CI and a single-maintainer dependency (P). Generated files are plain Go, so a freeze is survivable.

## 3. Data layer

**SQLite without cgo**

| | **modernc.org/sqlite** | **ncruces/go-sqlite3** |
|---|---|---|
| Version | Latest **tag** v1.60.1 (2026-09-29, GitLab tags atom, V). The master CHANGELOG lists v1.61.0 (2026-09-30), but no such tag exists — open question 1. | v0.35.6 (release 2026-09-23). **Pre-1.0.** |
| Home | **GitLab `cznic/sqlite` is the canonical repo**; GitHub is a mirror (README, V). | https://github.com/ncruces/go-sqlite3 |
| License | BSD-3, SQLite public domain, sqlite-vec MIT (README, V) | MIT (V, pkg.go.dev for `ext/fts5`) |
| Engine | SQLite **3.53.4**, transpiled C→Go. Go ≥1.26. | SQLite 3.53.3 in v0.35.2, bumped to **3.53.4** in v0.35.3 (release notes). Wasm→Go via `wasm2go` in the separate module `github.com/ncruces/go-sqlite3-wasm/v6`; `go 1.26.0`. |
| Support | "Only the latest release is supported"; SECURITY.md plus SBOM (CHANGELOG v1.60.0, V) | Single maintainer; higher memory per connection because each runs in a Wasm sandbox (README) |
| WAL | Works. A fault in the `-shm` mmap no longer crashes the process (v1.60.0). OFD locks are opt-in (`MODERNC_SQLITE_OFD_LOCK=1`) (CHANGELOG, V). | Unix: `mmap` WAL-index shm; locks via OFD (Linux ≥3.15). https://raw.githubusercontent.com/ncruces/go-sqlite3/main/vfs/README.md (V). v0.35.2 warned of **WAL corruption on Windows under high concurrency** (#404). v0.35.3 says that is fixed using memory placeholders (Win10 1803+) and disables shm WAL on older Windows (https://github.com/ncruces/go-sqlite3/releases/tag/v0.35.3, V). I did not open issue #404 itself. Linux-only server: low risk. |
| FTS5 / JSON | The **shipped-library flag list** in libsqlite3 `generator.go` (the one with `-DHAVE_USLEEP -DNDEBUG`, lines ~196–240) sets `-DSQLITE_ENABLE_FTS5`, `JSON1`, `GEOPOLY`, `RTREE`, `STAT4`, `MATH_FUNCTIONS`. https://gitlab.com/cznic/libsqlite3/-/raw/master/generator.go. A second list near line 404 is the Tcl test-fixture build (`CONFIG_SLOWDOWN_FACTOR`) and is not evidence. Windows uses other lists, so **U** whether Windows has FTS5. Runtime proof: U until `CREATE VIRTUAL TABLE t USING fts5(x)` is run against v1.60.1. The generator read is from master, not the tag vendored by v1.60.0 (libsqlite3 v1.15.0). | `ext/fts5` registers FTS5 per connection (`fts5.Register(db)`, custom tokenizers via `RegisterCustom`); not shown as compiled in. P. https://raw.githubusercontent.com/ncruces/go-sqlite3/main/ext/fts5/fts5.go |
| Quirks | Each connection carries its own page cache, so **bound the pool** with `SetMaxOpenConns`. Downstream `go.mod` must pin the same `modernc.org/libc` as the driver (doc.go, V). Shorthand DSN params `_journal_mode`, `_busy_timeout` (milliseconds), `_foreign_keys`, `_synchronous` since v1.55.0. `_time_integer_format` stores integer times. | goroutine-safe via `database/sql`; one connection must not be used concurrently. |

- **Performance cross-check (third-party, P):** https://github.com/cvilsmeier/go-sqlite-bench, run 2026-03-23, `journal_mode=DELETE`, not WAL.
  - Simple insert: modernc 2419 ms vs ncruces 2719 ms.
  - Real-world insert: 1759 ms vs 1469 ms.
  - Concurrent query at N=8: 2061 ms vs 2355 ms.
  - Large rows at N=50000: 401 ms vs 151 ms.
  - Roughly a wash, and not a WAL result.
- **Pick modernc** for 1.x semver, broad compiled-in features and a documented security/SBOM policy. Keep the store behind an interface so ncruces is a swap.

**Postgres fallback: `jackc/pgx/v5`**
- v5.11.0 (2026-09-07, CHANGELOG), MIT (https://raw.githubusercontent.com/jackc/pgx/master/LICENSE, V). Min Go 1.25. It supports Go 1.27 `RowsColumnScanner` in `stdlib`.
- 5.10.0 (2026-06-03) added hardening, e.g. `require_auth`.
- 5.9.2 fixed a SQL injection in **simple-protocol mode** (GHSA-j88v-2chj-qfwx). Keep the default extended protocol.
- 5.11.0 re-parses connection strings exactly like libpq, which can break existing DSNs.
- Source: https://raw.githubusercontent.com/jackc/pgx/master/CHANGELOG.md (V).

**Migrations**
- **golang-migrate v4.20.1** (release 2026-09-09). Its `database/sqlite` driver wraps each migration in a transaction by default (`x-no-tx-wrap` opts out) and uses `modernc.org/sqlite`. `database/sqlite3` is the cgo (mattn) one. Source reads at https://raw.githubusercontent.com/golang-migrate/migrate/master/README.md and `database/sqlite/README.md` (V). License: **U**. A `database/sqlite` import pulls its own modernc version into the build, which is a `libc` pin risk.
- **goose v3.29.0** (2026-10-11), MIT (pkg.go.dev, V), min Go 1.26 (CHANGELOG v3.28.0, V). API: `goose.NewProvider(dialect, *sql.DB, fs.FS, ...opts) (*Provider, error)` with `Up`, `Down`, `Status`, `HasPending`; methods are concurrency-safe. Dialects: `DialectSQLite3`, `DialectPostgres`. The Provider takes any `*sql.DB`, so no driver pin conflict. https://raw.githubusercontent.com/pressly/goose/main/provider.go (V).
- **Pick**: a tiny in-tree runner is my design recommendation (P). It is an embedded `fs.FS` of `NNNN_name.sql`, a `schema_migrations(version, checksum, applied_at)` table, one transaction per file, and `BEGIN IMMEDIATE` on SQLite, about 100 lines. Use goose's `Provider` only if SQLite and Postgres are both committed.

## 4. MCP

- **go-sdk v1.8.0**: GitHub release **published 2026-09-14**, tag commit **2026-09-04**. Apache-2.0 for new code, MIT for existing. `go 1.25.0`. https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0 (V).
- **Spec revision: 2026-07-28** is "Current". https://modelcontextprotocol.io/specification/versioning (V). The README compatibility table says SDK ≥v1.7.0 supports 2026-07-28 plus 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05, so v1.8.0 implements the current revision (V).
- **2026-07-28 transport**: stateless Streamable HTTP with a single POST endpoint. No GET stream, no protocol sessions and no SSE resumability. Server-to-client asks use multi-round-trip results. Roots, sampling and logging are deprecated (SEP-2577). https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http.md (V).
- **Server API**:
  - `mcp.NewServer(impl *Implementation, opts *ServerOptions) *Server`.
  - `mcp.NewStreamableHTTPHandler(getServer func(*http.Request) *Server, opts *StreamableHTTPOptions) *StreamableHTTPHandler`, with options `Stateless`, `JSONResponse`, `EventStore`, `SessionTimeout`.
  - **Requests at 2026-07-28 are accepted only with `Stateless = true`** (`docs/protocol.md`, V).
  - `ServerOptions.SupportedProtocolVersions` can narrow versions.
- **Auth (OAuth 2.1 resource server)**:
  - `auth.RequireBearerToken(verifier TokenVerifier, opts *RequireBearerTokenOptions) func(http.Handler) http.Handler`.
  - `type TokenVerifier func(ctx, token string, req *http.Request) (*TokenInfo, error)`.
  - `auth.ProtectedResourceMetadataHandler(*oauthex.ProtectedResourceMetadata)` serves the RFC 9728 document; the middleware sets the `WWW-Authenticate` challenge with `resource_metadata`.
  - The spec requires RFC 9728, token audience validation under RFC 8707 (the verifier's job, not the SDK's), and 401 for invalid tokens. https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization.md (V); `auth/auth.go` at v1.8.0 (V).
- **Tool schemas from Go structs**:
  - `mcp.AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])`.
  - Schemas come from `github.com/google/jsonschema-go v0.4.3` via `jsonschema:"desc"` struct tags and `jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: ...})`.
  - Inputs are validated and outputs go into `StructuredContent`. `docs/server.md` (V).
- **Pick**: the official SDK, over stateless Streamable HTTP wrapped by `RequireBearerToken`, with a verifier backed by the forge token store.

## 5. Markdown, highlighting, sanitising

- **goldmark** has two live majors. v2.1.6 (2026-09-27) lives at `github.com/yuin/goldmark/v2` (README `go get`, V); v2.0.0 was 2026-08-27. v1.8.6 (tag commit 2026-09-03) is still maintained. The v2 README says "still in the early stages of release", advises v1 if an extension lacks v2 support, and promises fixes for one prior major.
  - v2 adds semantic AST nodes with start positions, generics, and a parser/renderer split.
  - Imports: `.../v2/parser`, `.../v2/renderer/html`, `.../v2/extension`.
  - API: `p := parser.New(parser.WithExtensions(...))`, `r := html.New(html.WithExtensions(...))`, `doc := p.Parse(src)`, `r.Render(w, src, doc)`.
  - `extension.GFM` enables Table, Strikethrough, Linkify and TaskList. It does **not** filter disallowed raw-HTML tags. Footnotes, definition lists and typographer are separate. `html.WithUnsafe` is off by default.
  - License MIT: P. goldmark-highlighting v2 compatibility is **U**.
  - **Pick v2**: built-in GFM, AST positions, and no ecosystem dependency beyond chroma. Call chroma directly instead of the highlighting extension.
- **chroma/v2 v2.27.0** (tag commit 2026-06-17; master commits through 2026-10-18). License MIT: P.
  - Use `html.New(html.WithClasses(true), html.WithLinkableLineNumbers(...))` plus `formatter.WriteCSS(w, style)`; optional light/dark via `WithModeClasses`.
  - Language lookup: `lexers.Match(filename)`, `lexers.Get(name)`, `lexers.Analyse(src)`. `quick.Highlight(w, code, "go", "html", "monokai")` is the one-call form.
  - https://raw.githubusercontent.com/alecthomas/chroma/master/README.md lines ~108–208 (V).
- **bluemonday v1.0.27**: last tag 2024-07-04, last master commit 2025-04-04. Idle, not obviously abandoned. License: **U**. Open-issue count: not checked. https://api.github.com/repos/microcosm-cc/bluemonday/commits. **Skip it by default**: goldmark in default mode plus templ escaping covers the threat. Add it only if raw HTML must be allowed. I found no more active Go sanitizer, but that search was not exhaustive (U).

## 6. Auth

- **golang.org/x/crypto v0.58.0** (tag commit 2026-10-09). License BSD-3: P.
- **argon2**: `argon2.IDKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte`, memory in KiB. RFC 9106 gives two options: (1) t=1, m=2 GiB, p=4; (2) t=3, m=64 MiB, p=4 (source `argon2/argon2.go`, V). A web server cannot spend 2 GiB per login, so use option 2 and cap concurrent hashes with a semaphore. `x/crypto` has no PHC-string encoder; write one.
- **go-oidc v3.21.0** (tag commit 2026-09-01), Apache-2.0 (repo page, V). `oidc.NewProvider(ctx, issuer)`, `provider.Endpoint()`, `provider.Verifier(&oidc.Config{ClientID: ...})`, `verifier.Verify(ctx, rawIDToken)`, `IDToken.Claims(&v)`, `provider.UserInfo`.
- **golang.org/x/oauth2 v0.37.0** (tag commit 2026-08-25). PKCE: `oauth2.GenerateVerifier()`, `oauth2.S256ChallengeOption(v)`, `oauth2.VerifierOption(v)`, with `Config.AuthCodeURL` and `Config.Exchange` (`pkce.go`, V).
- **TOTP, pquerna/otp v1.5.0**: tag 2024-12-31 and last commit 2025-08-07 (a comment fix). Its last real change was a 2024-12-31 security fix: `io.ReadFull` for secret generation (commit 5971b1ef). License Apache-2.0: P. It is dormant but stable. An in-tree RFC 6238 implementation over `crypto/hmac` is a design option, not a finding. Either way you write rate limiting and replay protection.
- **x/crypto/ssh server suitability for a git SSH endpoint (b) — suitable.**
  - **Evidence: upstream `x/crypto/ssh` source plus upstream Gitea.** The `exec` and `exit-status` pattern rests on https://raw.githubusercontent.com/go-gitea/gitea/main/modules/ssh/server.go and `ssh.go` (`main`, not a pinned tag). The rukie checkout matches upstream there: same line numbers and content at server.go lines 20–147 for the session-only channel, `exec` and `shell`, and `exit-status`. So rukie is no longer a separate claim.
  - Upstream `server.go` rejects every channel except `session`, parses `exec` with `gossh.Unmarshal(req.Payload, &struct{ Value string })`, handles `env` requests, and sends `exit-status` via `channel.SendRequest("exit-status", ...)`.
  - Upstream `ssh.go` shows the other half:
    - `sessionHandler` pipes stdin, stdout and stderr, passes `GIT_PROTOCOL` from the session env (`env` requests carry git v2), and returns the exit status.
    - `publicKeyHandler` plus a comment that `Permissions` is assigned only after signature verification. It handles `*gossh.Certificate` through `CertChecker` and trusted user-CA keys.
  - `x/crypto/ssh` API: `ssh.NewServerConn`, `ServerConfig.PublicKeyCallback(ConnMetadata, PublicKey) (*Permissions, error)`, `VerifiedPublicKeyCallback`, `AuthLogCallback`, `MaxAuthTries` (default 6), `PublicKeyAuthAlgorithms`, `AddHostKey`, `SupportedAlgorithms()` (no SHA-1), `ParseAuthorizedKey`, `FingerprintSHA256`, `CertChecker`. Sources: `ssh/server.go`, `ssh/example_test.go` (V).
  - Defaults (`ssh/common.go`, V): `defaultKexAlgos` starts with `mlkem768x25519-sha256` then `curve25519-sha256`. Pubkey auth supports `sk-ssh-ed25519`, `sk-ecdsa` and ML-DSA keys.
  - **Gaps you write yourself**:
    - Pre-auth connection deadlines and per-IP rate limits.
    - The exec-session piping, EOF/`CloseWrite`, and `exit-status`.
    - Rejecting `pty-req`, subsystems and forwarding.
  - Mapping a session's `exec` to the Ruk daemon's `CommandOpen` is U (open question 6).

## 7. Diff parsing

- Ruk's typed `diff` reply is `files: Page<FileDiff>`. Each `FileDiff` has `{path, old_path, kind, old/new DiffBlob{oid, executable}, conflicted, body}`, where `body` is `"binary"` or `{unified: {patch: ByteSource}}` (`/tmp/ruk-model.json` `$defs`, V). So you get a **per-file unified patch with typed metadata**, and only a hunk parser is needed.
- **sourcegraph/go-diff v0.9.0** (2026-09-10 release): parses and prints, does not compute diffs. License **MIT** per the LICENSE text (Sourcegraph 2014, plus a second MIT notice for code adapted from python-unidiff), https://raw.githubusercontent.com/sourcegraph/go-diff/master/LICENSE (V). GitHub labels it "Other" because of the dual notice. API: `diff.ParseHunks([]byte) ([]*Hunk, error)`, `ParseFileDiff`, `ParseMultiFileDiff`, `ReverseFileDiff`.
- **bluekeyes/go-gitdiff v0.9.0** (tag commit 2026-07-18), MIT. Parses and applies git patches including binary, in strict mode. `gitdiff.Parse(io.Reader) ([]*File, string, error)`, `TextFragment.Lines []Line{Op, Line}` with `OpContext`/`OpDelete`/`OpAdd`.
- **Pick**: go-diff's `ParseHunks` on Ruk's per-file patch string, or a ~150-line in-tree parser. go-gitdiff only if the forge ever applies patches itself, which would go through Ruk anyway. The final parse path is **not decided**. Intra-line (word) diff is not provided by either library; what to use for it is U.

## 8. golangci-lint v2

- **v2.15.0**, released 2026-10-16 (immutable release page, V). License **GPL-3.0** (https://raw.githubusercontent.com/golangci/golangci-lint/main/LICENSE, V), so run it as an external tool.
- **Config schema** (https://golangci-lint.run/docs/configuration/file/, V), validated by https://golangci-lint.run/jsonschema/golangci.jsonschema.json:
  - Files: `.golangci.{yml,yaml,toml,json}`. Top-level keys: `version: "2"`, `linters`, `formatters`, `issues`, `output`, `run`, `severity`.
  - `linters`: `default` (`standard|all|none|fast`), `enable`, `disable`, `settings`, `exclusions{generated, presets, rules, paths, warn-unused}`.
  - `formatters`: `enable` among `gci`, `gofmt`, `gofumpt`, `goimports`, `golines`, `swaggo`, plus `settings` and `exclusions`.
  - `issues`: `new`, `new-from-merge-base`, `new-from-rev`, `fix`.
  - **v1→v2**:
    - `golangci-lint migrate` converts the config.
    - Formatters moved out of `linters`.
    - `stylecheck`, `gosimple` and `staticcheck` merged into `staticcheck`.
    - `linters.disable-all`/`enable-all` became `linters.default`.
    - https://golangci-lint.run/docs/product/migration-guide/ (V).
- **Suggested starting set**:
  - `default: standard`.
  - Enable `gosec`, `noctx`, `sqlclosecheck`, `rowserrcheck`, `bodyclose`, `errorlint`, `sloglint`, `modernize`, `forbidigo`, `depguard`, `contextcheck`.
  - Formatters `gofumpt` and `goimports`.
  - Use `depguard`/`forbidigo` to ban `os/exec` of `git` and `html/template`/templ JS helpers.
  - Go 1.27 compatibility of this exact release: U.

## Recommendations summary

| Area | Pick | Reason |
|---|---|---|
| Go | 1.27.2, `go 1.27.0` | Supported to ~Aug 2027; json/v2, generic methods, net/http hardening |
| Views | templ v0.3.1070 | Compiled, typed, streaming |
| DB | modernc v1.60.1; pgx v5.11.0 fallback | 1.x semver, FTS5/JSON build flags, SBOM |
| Migrations | in-tree runner (P); goose v3.29.0 if dual DB | Minimal dependencies |
| MCP | go-sdk v1.8.0, stateless Streamable HTTP + `RequireBearerToken` | Spec 2026-07-28 requires stateless |
| Markdown | goldmark v2.1.6 + chroma v2.27.0 (CSS classes); no bluemonday | AST positions; avoid a dormant sanitizer |
| Auth | argon2id t=3, m=64 MiB, p=4; go-oidc v3.21.0; oauth2 v0.37.0; TOTP in-tree or pquerna/otp | RFC 9106 option 2 |
| SSH | x/crypto v0.58.0 `ssh.NewServerConn` | Same shape as upstream Gitea |
| Diff | go-diff v0.9.0 `ParseHunks` | Ruk already gives per-file unified patches |
| Lint | golangci-lint v2.15.0 | |
| Rust | 1.99.1, matches the ruk pin | Current stable |
| CI sandbox | Podman ≥6.1.4 | Only the latest minor is maintained |

## Open questions (all U)
1. Will modernc v1.61.0 be tagged (the changelog lists it), and is the `modernc.org/libc` pin compatible with the rest of `go.mod`?
2. WAL-mode benchmarks on our workload for modernc vs ncruces. Published numbers are `journal_mode=DELETE`.
3. A runtime FTS5 smoke test per driver, and whether Windows builds of modernc include FTS5.
4. Which third-party goldmark extensions exist for v2, and goldmark-highlighting v2 compatibility.
5. Licenses not read: goldmark, chroma, bluemonday, pquerna/otp, golang-migrate, x/crypto.
6. How an SSH `exec` payload (`git-upload-pack '<repo>'`) maps onto `CommandOpen` in `daemon/mod.rs`.
7. Browser support for templ's Declarative Shadow DOM "suspense" pattern.
8. Whether the CI hosts meet Podman 6 prerequisites (cgroups v2, nftables, netavark, pasta).
9. WebAuthn/passkeys library choice (out of scope, not researched).
10. A maintained sanitizer alternative if raw HTML is ever allowed.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — same as upstream Gitea `modules/ssh/server.go` at the lines checked.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="glm-flash" severity="concern" turns_ago="1" guidance="weigh, don't blindly obey"> Don't cite that se…

---
I could not write `research-go-stack.md` to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/`. Nothing exists at that path from me, and nothing was ever written to `local://` either.

My tools in this session are `read`, `grep`, `web_search` and `ast_grep`. None of them can create or write a file. Plan mode also forbids shell commands, so the file can't be made that way.

The complete research-go-stack.md content is in my previous message, headed "research-go-stack.md — consolidated final (supersedes all earlier messages)". It already has every correction merged in and is ready to save as is. The earlier addenda are fully absorbed, so Main should save only that one message.

**To get the file onto disk:**
- Save that message's body verbatim as `research-go-stack.md` in the directory above.
- Or hand the write to an agent that has a write tool.

I'm not retyping the body here, to avoid a second copy that could drift from the first.

The earlier summary also holds:
- **Rust:** the ruk pin 1.99.1 equals current stable.
- **Go:** 1.27.2 is the latest patch.
- **Podman:** 6.1.4 is the latest release.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — matches upstream Gitea `modules/ssh/server.go` at the lines checked.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Cite the go-sdk v1.8.0 publish da…

---
The go-sdk v1.8.0 date in the report stands. I did not re-confirm it this turn: the re-fetch returned HTTP 403 from api.github.com, which is rate-limited, so the advisor's failed greps are not evidence against it.

The date was fetched in the first batch of this session, not recalled from memory. The call was `https://api.github.com/repos/modelcontextprotocol/go-sdk/releases/latest`, and its response contained:

- `"tag_name": "v1.8.0"`
- `"created_at": "2026-09-14T07:55:04Z"`
- `"published_at": "2026-09-14T08:05:46Z"`

The release body in that response also said "This release is equivalent to v1.8.0-pre.2" and that "2026-07-28 remains the newest version the SDK negotiates". The tag commit time, 2026-09-04T08:08:52Z, came separately from `https://proxy.golang.org/github.com/modelcontextprotocol/go-sdk/@latest`.

The report's go-sdk row therefore keeps both dates: release published 2026-09-14, tag commit 2026-09-04. The release-page URL is `https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0`.

The report is still not written to disk, because I have no write tool. Main should save the consolidated body from my "research-go-stack.md — consolidated final" message.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — matches upstream Gitea `modules/ssh/server.go` at the lines checked.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="minimax" severity="nit" turns_ago="1" guidance="weigh, don't blindly obey"> Stop chasing the go-sdk …

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Put the full research-go-stack.md …

---
No write tool: persist this body as `research-go-stack.md` in `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/`. Nothing has been written to disk by me, despite an advisory saying otherwise. This body is complete and supersedes all my earlier messages.

# research-go-stack.md

Assembled 2026-10-19. Labels: **V** = verified in a primary source this session, **P** = probable, **U** = unverified. "Tag commit" dates are proxy.golang.org commit times, not release dates.

## 0. Toolchains and support windows

| Item | Current | Dates | Support / EOL | Channel |
|---|---|---|---|---|
| **Go 1.27** | **1.27.2** (V: first stable entry in https://go.dev/dl/?mode=json) | 1.27.0 on 2026-08-19; 1.27.2 on 2026-10-08 | Supported until two newer majors exist, i.e. ~Go 1.29, ~Aug 2027 (P, inferred from the policy at https://go.dev/doc/devel/release) | https://go.dev/doc/devel/release |
| Go 1.26 | 1.26.9 (2026-10-08) | 1.26.0 on 2026-02-10 | Until Go 1.28, ~Feb 2027 (P). endoflife.date has `eolFrom: null` | same |
| Go 1.25 | 1.25.14 | | **EOL 2026-08-19** (V-secondary: endoflife.date `eolFrom`) | https://endoflife.date/api/v1/products/go/ |
| **Rust stable** | **1.99.1** | 1.99.0 on 2026-10-01; 1.99.1 (point release) on 2026-10-15 | Only the newest stable is supported; 1.98 EOL 2026-10-01 (V-secondary, endoflife.date) | https://static.rust-lang.org/dist/channel-rust-stable.toml, https://releases.rs/ |
| **Podman** | **6.1.4** (V: https://github.com/podman-container-tools/podman/releases/tag/v6.1.4, released 12 Oct) | 6.1.0 on 2026-08-12 | 6.0 EOL 2026-08-12, 5.8 EOL 2026-09-26 (V-secondary: https://endoflife.date/api/v1/products/podman/); only the newest minor is maintained | repo above |

**Rust pin**
- `ruk/rust-toolchain.toml` pins `channel = "1.99.1"`.
- It equals current stable. The manifest says `date = "2026-10-15"` and releases.rs shows "Stable: 1.99.1". Beta is 1.100.0, due 2026-11-12. V.

**Podman rootless**
- 6.0.0 removed cgroups v1, iptables, CNI and slirp4netns (rootless networking is now pasta). It also dropped BoltDB for SQLite and fixed CVE-2026-57231. The 6.0.0 section of https://raw.githubusercontent.com/podman-container-tools/podman/main/RELEASE_NOTES.md says so (V).
- Pesto/pasta rootless port forwarding and dropping the rootless pause process are marked **experimental**. The default forwarder stays `rootlessport`. V, same file.
- 6.0.1 fixed Pesto rule cleanup, and 6.1.0 fixed cleanup after containers that fail to start. V, same file.
- 6.1.4 fixes a WSL 3 issue and bumps gvproxy for CVE-2026-107935.
- I found no primary stability statement for rootless `podman run`. **Probable** that it is usable. Pin ≥6.1.4 on cgroups v2 with netavark/pasta, and gate CI on a rootless smoke test.

## 1. Go language and stdlib useful for a server

Sources: https://go.dev/doc/go1.22 through https://go.dev/doc/go1.27 (V).

| Feature | Since |
|---|---|
| `ServeMux` method/wildcard patterns, `Request.PathValue` | 1.22 |
| Range-over-func iterators, `slices`/`maps` iterator functions, `unique` | 1.23 |
| `os.Root`/`os.OpenRoot` (traversal-safe file ops), `weak`, `runtime.AddCleanup`, `testing.B.Loop`, `go get -tool` | 1.24 |
| `testing/synctest` GA (`Test`, `Wait`), `http.CrossOriginProtection` (token-less CSRF defence using Fetch metadata), `sync.WaitGroup.Go`, container-aware `GOMAXPROCS` | 1.25 |
| Green Tea GC default (10–40% less GC overhead claimed), `new(expr)`, `errors.AsType`, `slog.NewMultiHandler`, ServeMux trailing-slash redirect now 307, `url.Parse` stricter on colons in hosts | 1.26 |
| **`encoding/json/v2` and `jsontext` are regular packages**, and `encoding/json` is backed by v2 (opt-out `GOEXPERIMENT=nojsonv2`) | 1.27 |
| Generic methods, `uuid` package, goroutine-leak profile GA, `Server.MaxHeaderValueCount`, HTTP/2 client priorities, `httptest.NewTestServer`, `synctest.Sleep`, `database/sql` `RowsColumnScanner`/`ConvertAssign` | 1.27 |

- json/v2 (1.27): stricter defaults reject invalid UTF-8 and duplicate keys, and `inline` was renamed `embed`. The API is still evolving, so keep v1 semantics for wire formats (P).
- 1.27.2 and 1.26.9 carry fixes in `html/template`, `net/http`, `crypto/tls` and `os` (release history, V). Track patch releases.
- Floors required by the stack: templ module `go 1.26.0`, goose ≥1.26, modernc ≥1.26, ncruces `go 1.26.0`, MCP SDK `go 1.25.0`, pgx ≥1.25.
- **Pick `go 1.27.0` with toolchain go1.27.2.** `go mod init` defaults to N−1 (1.26 notes), so set it explicitly.

## 2. Templating: recommend **templ**

- **templ v0.3.1070**, 2026-10-04 (GitHub release `published_at`; proxy agrees), MIT (repo metadata, V). Its `go.mod` says `go 1.26.0`, so generated code forces Go ≥1.26. https://github.com/a-h/templ/releases/latest
- **Workflow**:
  - `templ generate` writes `*_templ.go`; `templ fmt -fail .` checks formatting in CI.
  - Install with `go get -tool github.com/a-h/templ/cmd/templ@<ver>` and run `go tool templ`.
  - Flags: `-include-version=false`, `-watch`, `-proxy`, `-lazy`, `-w`.
  - https://templ.guide/developer-tools/cli/ and https://templ.guide/quick-start/installation/ (V).
- **API**:
  - `templ.Component` (`Render(ctx, w) error`), `templ.Handler(c, ...)`, `templ.ComponentFunc`, `templ.Join`, `templ.WithChildren`/`GetChildren`, `templ.URL`/`SafeURL`, `templ.Raw`.
  - Composition is `@Comp(args)` and `{ children... }`.
  - https://templ.guide/syntax-and-usage/template-composition/ (V).
- **Streaming**: `templ.Handler(c, templ.WithStreaming())` plus `@templ.Flush() { ... }`. The default is buffer-then-write, so a status code can still be set on render failure. https://templ.guide/server-side-rendering/streaming/ (V). Its "suspense" example needs Declarative Shadow DOM, a browser feature; cross-browser support is U.
- **Escaping**:
  - All attributes are HTML-escaped. URL attributes are scheme-sanitized to `about:invalid#TemplFailedSanitizationURL`. https://templ.guide/syntax-and-usage/attributes/ (V).
  - `on*` handlers and `script` templates emit `<script>`, which breaks zero-JS. Forbid them; the lint mechanism is U.
- **Performance**: the vendor benchmark is dated 2023-08-17 (M-series). It gives templ 369 ns/op and 6 allocs vs html/template 2475 ns/op and 38 allocs. https://raw.githubusercontent.com/a-h/templ/main/benchmarks/templ/README.md. Self-published and old, so **P**, not a v0.3.1070 number.
- **html/template**: contextual auto-escaping that trusts template authors, not data. Parse and execute happen at runtime, and `Execute` takes `any`. https://raw.githubusercontent.com/golang/go/master/src/html/template/doc.go (V).
- **Pick templ**: views are compiled, typed Go functions, rendering streams, and allocations are lower. The cost is a codegen step in CI and a single-maintainer dependency (P). Generated files are plain Go, so a freeze is survivable.

## 3. Data layer

### SQLite without cgo

| | **modernc.org/sqlite** | **ncruces/go-sqlite3** |
|---|---|---|
| Version | Latest **tag** v1.60.1 (2026-09-29, GitLab tags atom, V). The master CHANGELOG lists v1.61.0 (2026-09-30), but no such tag exists (open question 1). | v0.35.6 (release 2026-09-23). **Pre-1.0.** |
| Home | **GitLab `cznic/sqlite` is canonical**; GitHub is a mirror (README, V). | https://github.com/ncruces/go-sqlite3 |
| License | BSD-3; SQLite public domain; sqlite-vec MIT (README, V) | MIT (pkg.go.dev for `ext/fts5`) |
| Engine | SQLite **3.53.4**, transpiled C→Go. Go ≥1.26. | SQLite 3.53.3 in v0.35.2, bumped to **3.53.4** in v0.35.3 (release notes). Wasm→Go via `wasm2go`, in the separate module `github.com/ncruces/go-sqlite3-wasm/v6`; `go 1.26.0`. |
| Support | "Only the latest release is supported"; SECURITY.md plus SBOM (CHANGELOG v1.60.0, V) | Single maintainer; higher memory per connection because each runs in a Wasm sandbox (README) |
| WAL | Works. A fault in the `-shm` mmap no longer crashes the process (v1.60.0). OFD locks are opt-in (`MODERNC_SQLITE_OFD_LOCK=1`). CHANGELOG (V). | Unix: `mmap` WAL-index shm; locks via OFD (Linux ≥3.15). https://raw.githubusercontent.com/ncruces/go-sqlite3/main/vfs/README.md (V). v0.35.2 warned of **WAL corruption on Windows under high concurrency** (#404). v0.35.3 notes say it is fixed using memory placeholders (Win10 1803+), with shm WAL disabled on older Windows (https://github.com/ncruces/go-sqlite3/releases/tag/v0.35.3, V). I did not open issue #404. Linux-only server: low risk. |
| FTS5 / JSON | The **shipped-library flag list** in libsqlite3 `generator.go` (the one with `-DHAVE_USLEEP -DNDEBUG`, lines ~196–240) sets `-DSQLITE_ENABLE_FTS5`, `JSON1`, `GEOPOLY`, `RTREE`, `STAT4`, `MATH_FUNCTIONS`. https://gitlab.com/cznic/libsqlite3/-/raw/master/generator.go. A second list near line 404 is the Tcl test-fixture build (`CONFIG_SLOWDOWN_FACTOR`) and is not evidence. Windows uses other lists, so **U** whether Windows has FTS5. Runtime proof is U until `CREATE VIRTUAL TABLE t USING fts5(x)` runs on v1.60.1. The generator read is from master, not the tag vendored by v1.60.0 (libsqlite3 v1.15.0). | `ext/fts5` registers FTS5 per connection (`fts5.Register(db)`, custom tokenizers via `RegisterCustom`); not shown as compiled in (P). https://raw.githubusercontent.com/ncruces/go-sqlite3/main/ext/fts5/fts5.go |

[Showing lines 603-1084 of 1495 (49.9KB limit). Use :1085 to continue]
| FTS5 / JSON | The **shipped-library flag list** in libsqlite3 `generator.go` (the one with `-DHAVE_USLEEP -DNDEBUG`, lines ~196–240) sets `-DSQLITE_ENABLE_FTS5`, `JSON1`, `GEOPOLY`, `RTREE`, `STAT4`, `MATH_FUNCTIONS`. https://gitlab.com/cznic/libsqlite3/-/raw/master/generator.go. A second list near line 404 is the Tcl test-fixture build (`CONFIG_SLOWDOWN_FACTOR`) and is not evidence. Windows uses other lists, so **U** whether Windows has FTS5. Runtime proof is U until `CREATE VIRTUAL TABLE t USING fts5(x)` runs on v1.60.1. The generator read is from master, not the tag vendored by v1.60.0 (libsqlite3 v1.15.0). | `ext/fts5` registers FTS5 per connection (`fts5.Register(db)`, custom tokenizers via `RegisterCustom`); not shown as compiled in (P). https://raw.githubusercontent.com/ncruces/go-sqlite3/main/ext/fts5/fts5.go |
| Quirks | Each connection has its own page cache, so **bound the pool** with `SetMaxOpenConns`. Downstream `go.mod` must pin the same `modernc.org/libc` as the driver (doc.go, V). Shorthand DSN params `_journal_mode`, `_busy_timeout` (milliseconds), `_foreign_keys`, `_synchronous` since v1.55.0. `_time_integer_format` stores integer times. | goroutine-safe via `database/sql`; one connection must not be used concurrently. |

- **Performance cross-check (third-party, P):** https://github.com/cvilsmeier/go-sqlite-bench, run 2026-03-23, `journal_mode=DELETE`, not WAL.
  - Simple insert: modernc 2419 ms vs ncruces 2719 ms.
  - Real-world insert: 1759 ms vs 1469 ms.
  - Concurrent query at N=8: 2061 ms vs 2355 ms.
  - Large rows at N=50000: 401 ms vs 151 ms.
  - Roughly a wash, and not a WAL result.
- **Pick modernc** for 1.x semver, broad compiled-in features and a documented security/SBOM policy. Keep the store behind an interface so ncruces is a swap.

### Postgres fallback: `jackc/pgx/v5`
- v5.11.0 (2026-09-07, CHANGELOG), MIT (https://raw.githubusercontent.com/jackc/pgx/master/LICENSE, V). Min Go 1.25. Supports Go 1.27 `RowsColumnScanner` in `stdlib`.
- 5.10.0 (2026-06-03) added hardening such as `require_auth`.
- 5.9.2 fixed a SQL injection in **simple-protocol mode** (GHSA-j88v-2chj-qfwx). Keep the default extended protocol.
- 5.11.0 re-parses connection strings exactly like libpq, which can break existing DSNs.
- Source: https://raw.githubusercontent.com/jackc/pgx/master/CHANGELOG.md (V).

### Migrations
- **golang-migrate v4.20.1** (release 2026-09-09). Its `database/sqlite` driver wraps each migration in a transaction by default (`x-no-tx-wrap` opts out) and uses `modernc.org/sqlite`; `database/sqlite3` is the cgo (mattn) one. Sources: https://raw.githubusercontent.com/golang-migrate/migrate/master/README.md and `database/sqlite/README.md` (V). License is U. Importing `database/sqlite` pulls its own modernc version into the build, which is a `libc` pin risk.
- **goose v3.29.0** (2026-10-11), MIT (pkg.go.dev, V), min Go 1.26 (CHANGELOG v3.28.0, V).
  - API: `goose.NewProvider(dialect, *sql.DB, fs.FS, ...opts) (*Provider, error)` with `Up`, `Down`, `Status`, `HasPending`; methods are concurrency-safe.
  - Dialects: `DialectSQLite3`, `DialectPostgres`.
  - The Provider takes any `*sql.DB`, so there is no driver pin conflict. https://raw.githubusercontent.com/pressly/goose/main/provider.go (V).
- **Pick**: an in-tree runner is my design recommendation (P). It is an embedded `fs.FS` of `NNNN_name.sql`, a `schema_migrations(version, checksum, applied_at)` table, one transaction per file, and `BEGIN IMMEDIATE` on SQLite, about 100 lines. Use goose's `Provider` only if SQLite and Postgres are both committed.

## 4. MCP

- **go-sdk v1.8.0**: GitHub release **published 2026-09-14T08:05:46Z** (`created_at` 07:55:04Z), tag commit **2026-09-04** (proxy). Apache-2.0 for new code, MIT for existing. `go 1.25.0`. The publish date came from the first-batch fetch of `https://api.github.com/repos/modelcontextprotocol/go-sdk/releases/latest`. A later re-fetch of the API got HTTP 403. https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0
- **Spec revision: 2026-07-28** is "Current". https://modelcontextprotocol.io/specification/versioning (V). The README compatibility table says SDK ≥v1.7.0 supports 2026-07-28 plus 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05, so v1.8.0 implements the current revision (V).
- **2026-07-28 transport**:
  - Stateless Streamable HTTP with a single POST endpoint.
  - No GET stream, no protocol sessions, no SSE resumability.
  - Server-to-client asks use multi-round-trip results.
  - Roots, sampling and logging are deprecated (SEP-2577).
  - https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http.md (V).
- **Server API**:
  - `mcp.NewServer(impl *Implementation, opts *ServerOptions) *Server`.
  - `mcp.NewStreamableHTTPHandler(getServer func(*http.Request) *Server, opts *StreamableHTTPOptions) *StreamableHTTPHandler`, with options `Stateless`, `JSONResponse`, `EventStore`, `SessionTimeout`.
  - **Requests at 2026-07-28 are accepted only with `Stateless = true`** (`docs/protocol.md`, V).
  - `ServerOptions.SupportedProtocolVersions` can narrow versions.
- **Auth (OAuth 2.1 resource server)**:
  - `auth.RequireBearerToken(verifier TokenVerifier, opts *RequireBearerTokenOptions) func(http.Handler) http.Handler`.
  - `type TokenVerifier func(ctx, token string, req *http.Request) (*TokenInfo, error)`.
  - `auth.ProtectedResourceMetadataHandler(*oauthex.ProtectedResourceMetadata)` serves the RFC 9728 document. The middleware sets the `WWW-Authenticate` challenge with `resource_metadata`.
  - The spec requires RFC 9728, token audience validation under RFC 8707 (the verifier's job, not the SDK's), and 401 for invalid tokens. https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization.md (V); `auth/auth.go` at v1.8.0 (V).
- **Tool schemas from Go structs**:
  - `mcp.AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])`.
  - Schemas come from `github.com/google/jsonschema-go v0.4.3` via `jsonschema:"desc"` struct tags and `jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: ...})`.
  - Inputs are validated and outputs go into `StructuredContent`. `docs/server.md` (V).
- **Pick**: the official SDK, over stateless Streamable HTTP wrapped by `RequireBearerToken`, with a verifier backed by the forge token store.

## 5. Markdown, highlighting, sanitising

- **goldmark** has two live majors.
  - v2.1.6 (2026-09-27) lives at `github.com/yuin/goldmark/v2` (README `go get`, V). v2.0.0 was 2026-08-27.
  - v1.8.6 (tag commit 2026-09-03) is still maintained.
  - The v2 README says "still in the early stages of release", advises v1 if an extension lacks v2 support, and promises fixes for one prior major.
  - v2 adds semantic AST nodes with start positions, generics, and a parser/renderer split.
  - Imports: `.../v2/parser`, `.../v2/renderer/html`, `.../v2/extension`.
  - API: `p := parser.New(parser.WithExtensions(...))`, `r := html.New(html.WithExtensions(...))`, `doc := p.Parse(src)`, `r.Render(w, src, doc)`.
  - `extension.GFM` enables Table, Strikethrough, Linkify and TaskList. It does **not** filter disallowed raw-HTML tags. Footnotes, definition lists and typographer are separate. `html.WithUnsafe` is off by default.
  - License MIT: P. goldmark-highlighting v2 compatibility is **U**.
  - **Pick v2**: built-in GFM, AST positions, and chroma called directly instead of the highlighting extension.
- **chroma/v2 v2.27.0** (tag commit 2026-06-17; master commits through 2026-10-18). License MIT: P.
  - Use `html.New(html.WithClasses(true), html.WithLinkableLineNumbers(...))` plus `formatter.WriteCSS(w, style)`; optional light/dark via `WithModeClasses`.
  - Language lookup: `lexers.Match(filename)`, `lexers.Get(name)`, `lexers.Analyse(src)`. `quick.Highlight(w, code, "go", "html", "monokai")` is the one-call form.
  - https://raw.githubusercontent.com/alecthomas/chroma/master/README.md lines ~108–208 (V).
- **bluemonday v1.0.27**: last tag 2024-07-04, last master commit 2025-04-04. Idle, not obviously abandoned. License **U**. Open-issue count not checked. **Skip it by default**: goldmark in default mode plus templ escaping covers the threat. Add it only if raw HTML must be allowed. A more active Go sanitizer is U (search not exhaustive).

## 6. Auth

- **golang.org/x/crypto v0.58.0** (tag commit 2026-10-09). License BSD-3: P.
- **argon2**: `argon2.IDKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte`, memory in KiB. RFC 9106 gives two options: (1) t=1, m=2 GiB, p=4; (2) t=3, m=64 MiB, p=4 (source `argon2/argon2.go`, V). A web server cannot spend 2 GiB per login, so use option 2 and cap concurrent hashes with a semaphore. `x/crypto` has no PHC-string encoder; write one.
- **go-oidc v3.21.0** (tag commit 2026-09-01), Apache-2.0 (repo page, V). `oidc.NewProvider(ctx, issuer)`, `provider.Endpoint()`, `provider.Verifier(&oidc.Config{ClientID: ...})`, `verifier.Verify(ctx, rawIDToken)`, `IDToken.Claims(&v)`, `provider.UserInfo`.
- **golang.org/x/oauth2 v0.37.0** (tag commit 2026-08-25). PKCE: `oauth2.GenerateVerifier()`, `oauth2.S256ChallengeOption(v)`, `oauth2.VerifierOption(v)`, with `Config.AuthCodeURL` and `Config.Exchange` (`pkce.go`, V).
- **TOTP, pquerna/otp v1.5.0**: tag 2024-12-31, last commit 2025-08-07 (a comment fix). Its last real change was a 2024-12-31 security fix: `io.ReadFull` for secret generation (commit 5971b1ef). License Apache-2.0: P. Dormant but stable. An in-tree RFC 6238 implementation over `crypto/hmac` is a design option, not a finding. Either way you write rate limiting and replay protection.
- **x/crypto/ssh server suitability for a git SSH endpoint — suitable.**
  - **The evidence is the `x/crypto/ssh` source**, https://raw.githubusercontent.com/golang/crypto/master/ssh/ (`server.go`, `common.go`, `example_test.go`). It shows the public-key callback, `NewServerConn`, channel and request handling. It does not show a git `exec` handler.
  - **The `exec` / `exit-status` pattern is corroborated, not primary.**
    - The local rukie checkout (`rukie/modules/ssh/server.go`, lines 20–147) handles it. It rejects non-`session` channels, parses `exec` with `gossh.Unmarshal(req.Payload, &struct{ Value string })`, handles `env` (for `GIT_PROTOCOL`), and sends `exit-status` via `channel.SendRequest("exit-status", ...)`.
    - I also fetched `go-gitea/gitea` `modules/ssh/server.go` and `ssh.go` from raw.githubusercontent.com. The `server.go` text was line-for-line identical to the local file. The `ssh.go` imports `gitea.dev/...` paths, which makes me doubt what that fetch actually returned.
    - I cannot show those two fetches are independent upstream evidence, so treat the exec handler as **P**, a Gitea-style pattern that rukie inherited. Upstream Gitea at a pinned tag is unread (open question 11).
  - **Gitea-style session handling**, from the local copy of `ssh.go`:
    - `sessionHandler` pipes stdin, stdout and stderr, passes `GIT_PROTOCOL` from session env, and returns an exit status.
    - `publicKeyHandler` returns `Permissions` only for the key that actually signs, and supports `*gossh.Certificate` via `CertChecker` with trusted user-CA keys.
  - **`x/crypto/ssh` API**: `ssh.NewServerConn`, `ServerConfig.PublicKeyCallback(ConnMetadata, PublicKey) (*Permissions, error)`, `VerifiedPublicKeyCallback`, `AuthLogCallback`, `MaxAuthTries` (default 6), `PublicKeyAuthAlgorithms`, `AddHostKey`, `SupportedAlgorithms()` (no SHA-1), `ParseAuthorizedKey`, `FingerprintSHA256`, `CertChecker` (V).
  - **Defaults** (`ssh/common.go`, V): `defaultKexAlgos` starts with `mlkem768x25519-sha256` then `curve25519-sha256`. Pubkey auth supports `sk-ssh-ed25519`, `sk-ecdsa` and ML-DSA keys.
  - **Gaps you write yourself**:
    - Pre-auth connection deadlines and per-IP rate limits.
    - Exec-session piping, EOF/`CloseWrite`, and `exit-status`.
    - Rejecting `pty-req`, subsystems and forwarding.
  - Mapping a session's `exec` onto the Ruk daemon's `CommandOpen` is U (open question 6).

## 7. Diff parsing

- Ruk's typed `diff` reply is `files: Page<FileDiff>`. Each `FileDiff` has `{path, old_path, kind, old/new DiffBlob{oid, executable}, conflicted, body}`, where `body` is `"binary"` or `{unified: {patch: ByteSource}}` (`/tmp/ruk-model.json` `$defs`, V). So you get a **per-file unified patch with typed metadata**, and only a hunk parser is needed.
- **sourcegraph/go-diff v0.9.0** (2026-09-10 release): parses and prints, does not compute diffs. License **MIT** per the LICENSE text (Sourcegraph 2014, plus a second MIT notice for code adapted from python-unidiff), https://raw.githubusercontent.com/sourcegraph/go-diff/master/LICENSE (V). GitHub labels it "Other" because of the dual notice. API: `diff.ParseHunks([]byte) ([]*Hunk, error)`, `ParseFileDiff`, `ParseMultiFileDiff`, `ReverseFileDiff`.
- **bluekeyes/go-gitdiff v0.9.0** (tag commit 2026-07-18), MIT. Parses and applies git patches including binary, in strict mode. `gitdiff.Parse(io.Reader) ([]*File, string, error)`, `TextFragment.Lines []Line{Op, Line}` with `OpContext`/`OpDelete`/`OpAdd`.
- **Pick**: go-diff's `ParseHunks` on Ruk's per-file patch string, or a ~150-line in-tree parser. go-gitdiff only if the forge applies patches itself, which would go through Ruk anyway. The final parse path is **not decided**. Intra-line (word) diff is not provided by either library; what to use for it is U.

## 8. golangci-lint v2

- **v2.15.0**, released 2026-10-16 (immutable release page, V). License **GPL-3.0** (https://raw.githubusercontent.com/golangci/golangci-lint/main/LICENSE, V), so run it as an external tool.
- **Config schema** (https://golangci-lint.run/docs/configuration/file/, V), validated by https://golangci-lint.run/jsonschema/golangci.jsonschema.json:
  - Files: `.golangci.{yml,yaml,toml,json}`. Top-level keys: `version: "2"`, `linters`, `formatters`, `issues`, `output`, `run`, `severity`.
  - `linters`: `default` (`standard|all|none|fast`), `enable`, `disable`, `settings`, `exclusions{generated, presets, rules, paths, warn-unused}`.
  - `formatters`: `enable` among `gci`, `gofmt`, `gofumpt`, `goimports`, `golines`, `swaggo`, plus `settings` and `exclusions`.
  - `issues`: `new`, `new-from-merge-base`, `new-from-rev`, `fix`.
  - **v1→v2**: `golangci-lint migrate` converts the config; formatters moved out of `linters`; `stylecheck`, `gosimple` and `staticcheck` merged into `staticcheck`; `disable-all`/`enable-all` became `linters.default`. https://golangci-lint.run/docs/product/migration-guide/ (V).
- **Suggested starting set**: `default: standard`; enable `gosec`, `noctx`, `sqlclosecheck`, `rowserrcheck`, `bodyclose`, `errorlint`, `sloglint`, `modernize`, `forbidigo`, `depguard`, `contextcheck`; formatters `gofumpt` and `goimports`. Use `depguard`/`forbidigo` to ban `os/exec` of `git` and templ JS helpers. Go 1.27 compatibility of this exact release: U.

## Recommendations summary

| Area | Pick | Reason |
|---|---|---|
| Go | 1.27.2, `go 1.27.0` | Supported to ~Aug 2027; json/v2, generic methods, net/http hardening |
| Views | templ v0.3.1070 | Compiled, typed, streaming |
| DB | modernc v1.60.1; pgx v5.11.0 fallback | 1.x semver, FTS5/JSON build flags, SBOM |
| Migrations | in-tree runner (P); goose v3.29.0 if dual DB | Minimal dependencies |
| MCP | go-sdk v1.8.0, stateless Streamable HTTP + `RequireBearerToken` | Spec 2026-07-28 requires stateless |
| Markdown | goldmark v2.1.6 + chroma v2.27.0 (CSS classes); no bluemonday | AST positions; avoid a dormant sanitizer |
| Auth | argon2id t=3, m=64 MiB, p=4; go-oidc v3.21.0; oauth2 v0.37.0; TOTP in-tree or pquerna/otp | RFC 9106 option 2 |
| SSH | x/crypto v0.58.0 `ssh.NewServerConn` | Gitea-style pattern, API from x/crypto |
| Diff | go-diff v0.9.0 `ParseHunks` | Ruk already gives per-file unified patches |
| Lint | golangci-lint v2.15.0 | |
| Rust | 1.99.1, matches the ruk pin | Current stable |
| CI sandbox | Podman ≥6.1.4 | Only the latest minor is maintained |

## Open questions (all U)
1. Will modernc v1.61.0 be tagged (the changelog lists it), and is the `modernc.org/libc` pin compatible with the rest of `go.mod`?
2. WAL-mode benchmarks on our workload for modernc vs ncruces. Published numbers are `journal_mode=DELETE`.
3. A runtime FTS5 smoke test per driver, and whether Windows builds of modernc include FTS5.
4. Which third-party goldmark extensions exist for v2, and goldmark-highlighting v2 compatibility.
5. Licenses not read: goldmark, chroma, bluemonday, pquerna/otp, golang-migrate, x/crypto.
6. How an SSH `exec` payload (`git-upload-pack '<repo>'`) maps onto `CommandOpen` in `daemon/mod.rs`.
7. Browser support for templ's Declarative Shadow DOM "suspense" pattern.
8. Whether the CI hosts meet Podman 6 prerequisites (cgroups v2, nftables, netavark, pasta).
9. WebAuthn/passkeys library choice (out of scope, not researched).
10. A maintained sanitizer alternative if raw HTML is ever allowed.
11. Upstream Gitea's `exec`/`exit-status` handling at a pinned tag, independently of the rukie copy.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — Gitea-style `exec`/`exit-status` handler; corroboration only, not primary.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="muse" severity="blocker" guidance="weigh, don't blindly obey"> Write file to given place now. Agent …

---
I cannot create the file. This session's tools are `read`, `grep`, `web_search` and `ast_grep`, and none can write. Plan mode also forbids shell commands. So no file exists at the target path, and I am not claiming one does.

**Target path:** `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-go-stack.md`

To keep the work from being lost, the complete final body follows. Main (or any agent with a write tool) should save everything from "# research-go-stack.md" through the end of the Critical Files section to that path.

# research-go-stack.md

Assembled 2026-10-19. Labels: **V** = verified in a primary source this session, **P** = probable, **U** = unverified. "Tag commit" dates are proxy.golang.org commit times, not release dates.

## 0. Toolchains and support windows

| Item | Current | Dates | Support / EOL | Channel |
|---|---|---|---|---|
| **Go 1.27** | **1.27.2** (V: first stable entry in https://go.dev/dl/?mode=json) | 1.27.0 on 2026-08-19; 1.27.2 on 2026-10-08 | Supported until two newer majors exist, i.e. ~Go 1.29, ~Aug 2027 (P, inferred from the policy at https://go.dev/doc/devel/release) | https://go.dev/doc/devel/release |
| Go 1.26 | 1.26.9 (2026-10-08) | 1.26.0 on 2026-02-10 | Until Go 1.28, ~Feb 2027 (P). endoflife.date has `eolFrom: null` | same |
| Go 1.25 | 1.25.14 | | **EOL 2026-08-19** (V-secondary: endoflife.date `eolFrom`) | https://endoflife.date/api/v1/products/go/ |
| **Rust stable** | **1.99.1** | 1.99.0 on 2026-10-01; 1.99.1 (point release) on 2026-10-15 | Only the newest stable is supported; 1.98 EOL 2026-10-01 (V-secondary, endoflife.date) | https://static.rust-lang.org/dist/channel-rust-stable.toml, https://releases.rs/ |
| **Podman** | **6.1.4** (V: https://github.com/podman-container-tools/podman/releases/tag/v6.1.4, released 12 Oct) | 6.1.0 on 2026-08-12 | 6.0 EOL 2026-08-12, 5.8 EOL 2026-09-26 (V-secondary: https://endoflife.date/api/v1/products/podman/); only the newest minor is maintained | repo above |

**Rust pin**
- `ruk/rust-toolchain.toml` pins `channel = "1.99.1"`.
- It equals current stable. The manifest says `date = "2026-10-15"` and releases.rs shows "Stable: 1.99.1". Beta is 1.100.0, due 2026-11-12. V.

**Podman rootless**
- 6.0.0 removed cgroups v1, iptables, CNI and slirp4netns (rootless networking is now pasta). It also dropped BoltDB for SQLite and fixed CVE-2026-57231. The 6.0.0 section of https://raw.githubusercontent.com/podman-container-tools/podman/main/RELEASE_NOTES.md says so (V).
- Pesto/pasta rootless port forwarding and dropping the rootless pause process are marked **experimental**. The default forwarder stays `rootlessport`. V, same file.
- 6.0.1 fixed Pesto rule cleanup, and 6.1.0 fixed cleanup after containers that fail to start. V, same file.
- 6.1.4 fixes a WSL 3 issue and bumps gvproxy for CVE-2026-107935.
- I found no primary stability statement for rootless `podman run`. **Probable** that it is usable. Pin ≥6.1.4 on cgroups v2 with netavark/pasta, and gate CI on a rootless smoke test.

## 1. Go language and stdlib useful for a server

Sources: https://go.dev/doc/go1.22 through https://go.dev/doc/go1.27 (V).

| Feature | Since |
|---|---|
| `ServeMux` method/wildcard patterns, `Request.PathValue` | 1.22 |
| Range-over-func iterators, `slices`/`maps` iterator functions, `unique` | 1.23 |
| `os.Root`/`os.OpenRoot` (traversal-safe file ops), `weak`, `runtime.AddCleanup`, `testing.B.Loop`, `go get -tool` | 1.24 |
| `testing/synctest` GA (`Test`, `Wait`), `http.CrossOriginProtection` (token-less CSRF defence using Fetch metadata), `sync.WaitGroup.Go`, container-aware `GOMAXPROCS` | 1.25 |
| Green Tea GC default (10–40% less GC overhead claimed), `new(expr)`, `errors.AsType`, `slog.NewMultiHandler`, ServeMux trailing-slash redirect now 307, `url.Parse` stricter on colons in hosts | 1.26 |
| **`encoding/json/v2` and `jsontext` are regular packages**, and `encoding/json` is backed by v2 (opt-out `GOEXPERIMENT=nojsonv2`) | 1.27 |
| Generic methods, `uuid` package, goroutine-leak profile GA, `Server.MaxHeaderValueCount`, HTTP/2 client priorities, `httptest.NewTestServer`, `synctest.Sleep`, `database/sql` `RowsColumnScanner`/`ConvertAssign` | 1.27 |

- json/v2 (1.27): stricter defaults reject invalid UTF-8 and duplicate keys, and `inline` was renamed `embed`. The API is still evolving, so keep v1 semantics for wire formats (P).
- 1.27.2 and 1.26.9 carry fixes in `html/template`, `net/http`, `crypto/tls` and `os` (release history, V). Track patch releases.
- Floors required by the stack: templ module `go 1.26.0`, goose ≥1.26, modernc ≥1.26, ncruces `go 1.26.0`, MCP SDK `go 1.25.0`, pgx ≥1.25.
- **Pick `go 1.27.0` with toolchain go1.27.2.** `go mod init` defaults to N−1 (1.26 notes), so set it explicitly.

## 2. Templating: recommend **templ**

- **templ v0.3.1070**, 2026-10-04 (GitHub release `published_at`; proxy agrees), MIT (repo metadata, V). Its `go.mod` says `go 1.26.0`, so generated code forces Go ≥1.26. https://github.com/a-h/templ/releases/latest
- **Workflow**:
  - `templ generate` writes `*_templ.go`; `templ fmt -fail .` checks formatting in CI.
  - Install with `go get -tool github.com/a-h/templ/cmd/templ@<ver>` and run `go tool templ`.
  - Flags: `-include-version=false`, `-watch`, `-proxy`, `-lazy`, `-w`.
  - https://templ.guide/developer-tools/cli/ and https://templ.guide/quick-start/installation/ (V).
- **API**:
  - `templ.Component` (`Render(ctx, w) error`), `templ.Handler(c, ...)`, `templ.ComponentFunc`, `templ.Join`, `templ.WithChildren`/`GetChildren`, `templ.URL`/`SafeURL`, `templ.Raw`.
  - Composition is `@Comp(args)` and `{ children... }`.
  - https://templ.guide/syntax-and-usage/template-composition/ (V).
- **Streaming**: `templ.Handler(c, templ.WithStreaming())` plus `@templ.Flush() { ... }`. The default is buffer-then-write, so a status code can still be set on render failure. https://templ.guide/server-side-rendering/streaming/ (V). Its "suspense" example needs Declarative Shadow DOM, a browser feature; cross-browser support is U.
- **Escaping**:
  - All attributes are HTML-escaped. URL attributes are scheme-sanitized to `about:invalid#TemplFailedSanitizationURL`. https://templ.guide/syntax-and-usage/attributes/ (V).
  - `on*` handlers and `script` templates emit `<script>`, which breaks zero-JS. Forbid them; the lint mechanism is U.
- **Performance**: the vendor benchmark is dated 2023-08-17 (M-series). It gives templ 369 ns/op and 6 allocs vs html/template 2475 ns/op and 38 allocs. https://raw.githubusercontent.com/a-h/templ/main/benchmarks/templ/README.md. Self-published and old, so **P**, not a v0.3.1070 number.
- **html/template**: contextual auto-escaping that trusts template authors, not data. Parse and execute happen at runtime, and `Execute` takes `any`. https://raw.githubusercontent.com/golang/go/master/src/html/template/doc.go (V).
- **Pick templ**: views are compiled, typed Go functions, rendering streams, and allocations are lower. The cost is a codegen step in CI and a single-maintainer dependency (P). Generated files are plain Go, so a freeze is survivable.

## 3. Data layer

### SQLite without cgo

| | **modernc.org/sqlite** | **ncruces/go-sqlite3** |
|---|---|---|
| Version | Latest **tag** v1.60.1 (2026-09-29, GitLab tags atom, V). The master CHANGELOG lists v1.61.0 (2026-09-30), but no such tag exists (open question 1). | v0.35.6 (release 2026-09-23). **Pre-1.0.** |
| Home | **GitLab `cznic/sqlite` is canonical**; GitHub is a mirror (README, V). | https://github.com/ncruces/go-sqlite3 |
| License | BSD-3; SQLite public domain; sqlite-vec MIT (README, V) | MIT (pkg.go.dev for `ext/fts5`) |
| Engine | SQLite **3.53.4**, transpiled C→Go. Go ≥1.26. | SQLite 3.53.3 in v0.35.2, bumped to **3.53.4** in v0.35.3 (release notes). Wasm→Go via `wasm2go`, in the separate module `github.com/ncruces/go-sqlite3-wasm/v6`; `go 1.26.0`. |
| Support | "Only the latest release is supported"; SECURITY.md plus SBOM (CHANGELOG v1.60.0, V) | Single maintainer; higher memory per connection because each runs in a Wasm sandbox (README) |
| WAL | Works. A fault in the `-shm` mmap no longer crashes the process (v1.60.0). OFD locks are opt-in (`MODERNC_SQLITE_OFD_LOCK=1`). CHANGELOG (V). | Unix: `mmap` WAL-index shm; locks via OFD (Linux ≥3.15). https://raw.githubusercontent.com/ncruces/go-sqlite3/main/vfs/README.md (V). v0.35.2 warned of **WAL corruption on Windows under high concurrency** (#404). v0.35.3 notes say it is fixed using memory placeholders (Win10 1803+), with shm WAL disabled on older Windows (https://github.com/ncruces/go-sqlite3/releases/tag/v0.35.3, V). I did not open issue #404. Linux-only server: low risk. |
| FTS5 / JSON | The **shipped-library flag list** in libsqlite3 `generator.go` (the one with `-DHAVE_USLEEP -DNDEBUG`, lines ~196–240) sets `-DSQLITE_ENABLE_FTS5`, `JSON1`, `GEOPOLY`, `RTREE`, `STAT4`, `MATH_FUNCTIONS`. https://gitlab.com/cznic/libsqlite3/-/raw/master/generator.go. A second list near line 404 is the Tcl test-fixture build (`CONFIG_SLOWDOWN_FACTOR`) and is not evidence. Windows uses other lists, so **U** whether Windows has FTS5. Runtime proof is U until `CREATE VIRTUAL TABLE t USING fts5(x)` runs on v1.60.1. The generator read is from master, not the tag vendored by v1.60.0 (libsqlite3 v1.15.0). | `ext/fts5` registers FTS5 per connection (`fts5.Register(db)`, custom tokenizers via `RegisterCustom`); not shown as compiled in (P). https://raw.githubusercontent.com/ncruces/go-sqlite3/main/ext/fts5/fts5.go |
| Quirks | Each connection has its own page cache, so **bound the pool** with `SetMaxOpenConns`. Downstream `go.mod` must pin the same `modernc.org/libc` as the driver (doc.go, V). Shorthand DSN params `_journal_mode`, `_busy_timeout` (milliseconds), `_foreign_keys`, `_synchronous` since v1.55.0. `_time_integer_format` stores integer times. | goroutine-safe via `database/sql`; one connection must not be used concurrently. |

- **Performance cross-check (third-party, P):** https://github.com/cvilsmeier/go-sqlite-bench, run 2026-03-23, `journal_mode=DELETE`, not WAL.
  - Simple insert: modernc 2419 ms vs ncruces 2719 ms.
  - Real-world insert: 1759 ms vs 1469 ms.
  - Concurrent query at N=8: 2061 ms vs 2355 ms.
  - Large rows at N=50000: 401 ms vs 151 ms.
  - Roughly a wash, and not a WAL result.
- **Pick modernc** for 1.x semver, broad compiled-in features and a documented security/SBOM policy. Keep the store behind an interface so ncruces is a swap.

### Postgres fallback: `jackc/pgx/v5`
- v5.11.0 (2026-09-07, CHANGELOG), MIT (https://raw.githubusercontent.com/jackc/pgx/master/LICENSE, V). Min Go 1.25. Supports Go 1.27 `RowsColumnScanner` in `stdlib`.
- 5.10.0 (2026-06-03) added hardening such as `require_auth`.
- 5.9.2 fixed a SQL injection in **simple-protocol mode** (GHSA-j88v-2chj-qfwx). Keep the default extended protocol.
- 5.11.0 re-parses connection strings exactly like libpq, which can break existing DSNs.
- Source: https://raw.githubusercontent.com/jackc/pgx/master/CHANGELOG.md (V).

### Migrations
- **golang-migrate v4.20.1** (release 2026-09-09). Its `database/sqlite` driver wraps each migration in a transaction by default (`x-no-tx-wrap` opts out) and uses `modernc.org/sqlite`; `database/sqlite3` is the cgo (mattn) one. Sources: https://raw.githubusercontent.com/golang-migrate/migrate/master/README.md and `database/sqlite/README.md` (V). License is U. Importing `database/sqlite` pulls its own modernc version into the build, which is a `libc` pin risk.
- **goose v3.29.0** (2026-10-11), MIT (pkg.go.dev, V), min Go 1.26 (CHANGELOG v3.28.0, V).
  - API: `goose.NewProvider(dialect, *sql.DB, fs.FS, ...opts) (*Provider, error)` with `Up`, `Down`, `Status`, `HasPending`; methods are concurrency-safe.
  - Dialects: `DialectSQLite3`, `DialectPostgres`.
  - The Provider takes any `*sql.DB`, so there is no driver pin conflict. https://raw.githubusercontent.com/pressly/goose/main/provider.go (V).
- **Pick**: an in-tree runner is my design recommendation (P). It is an embedded `fs.FS` of `NNNN_name.sql`, a `schema_migrations(version, checksum, applied_at)` table, one transaction per file, and `BEGIN IMMEDIATE` on SQLite, about 100 lines. Use goose's `Provider` only if SQLite and Postgres are both committed.

## 4. MCP

- **go-sdk v1.8.0**: GitHub release **published 2026-09-14T08:05:46Z** (`created_at` 07:55:04Z), tag commit **2026-09-04** (proxy). Apache-2.0 for new code, MIT for existing. `go 1.25.0`. The publish date came from the first-batch fetch of `https://api.github.com/repos/modelcontextprotocol/go-sdk/releases/latest`. A later re-fetch of the API got HTTP 403. https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0
- **Spec revision: 2026-07-28** is "Current". https://modelcontextprotocol.io/specification/versioning (V). The README compatibility table says SDK ≥v1.7.0 supports 2026-07-28 plus 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05, so v1.8.0 implements the current revision (V).
- **2026-07-28 transport**:
  - Stateless Streamable HTTP with a single POST endpoint.
  - No GET stream, no protocol sessions, no SSE resumability.
  - Server-to-client asks use multi-round-trip results.
  - Roots, sampling and logging are deprecated (SEP-2577).
  - https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http.md (V).
- **Server API**:
  - `mcp.NewServer(impl *Implementation, opts *ServerOptions) *Server`.
  - `mcp.NewStreamableHTTPHandler(getServer func(*http.Request) *Server, opts *StreamableHTTPOptions) *StreamableHTTPHandler`, with options `Stateless`, `JSONResponse`, `EventStore`, `SessionTimeout`.
  - **Requests at 2026-07-28 are accepted only with `Stateless = true`** (`docs/protocol.md`, V).
  - `ServerOptions.SupportedProtocolVersions` can narrow versions.
- **Auth (OAuth 2.1 resource server)**:
  - `auth.RequireBearerToken(verifier TokenVerifier, opts *RequireBearerTokenOptions) func(http.Handler) http.Handler`.
  - `type TokenVerifier func(ctx, token string, req *http.Request) (*TokenInfo, error)`.
  - `auth.ProtectedResourceMetadataHandler(*oauthex.ProtectedResourceMetadata)` serves the RFC 9728 document. The middleware sets the `WWW-Authenticate` challenge with `resource_metadata`.
  - The spec requires RFC 9728, token audience validation under RFC 8707 (the verifier's job, not the SDK's), and 401 for invalid tokens. https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization.md (V); `auth/auth.go` at v1.8.0 (V).
- **Tool schemas from Go structs**:
  - `mcp.AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])`.
  - Schemas come from `github.com/google/jsonschema-go v0.4.3` via `jsonschema:"desc"` struct tags and `jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: ...})`.
  - Inputs are validated and outputs go into `StructuredContent`. `docs/server.md` (V).
- **Pick**: the official SDK, over stateless Streamable HTTP wrapped by `RequireBearerToken`, with a verifier backed by the forge token store.

## 5. Markdown, highlighting, sanitising

- **goldmark** has two live majors.
  - v2.1.6 (2026-09-27) lives at `github.com/yuin/goldmark/v2` (README `go get`, V). v2.0.0 was 2026-08-27.
  - v1.8.6 (tag commit 2026-09-03) is still maintained.
  - The v2 README says "still in the early stages of release", advises v1 if an extension lacks v2 support, and promises fixes for one prior major.
  - v2 adds semantic AST nodes with start positions, generics, and a parser/renderer split.
  - Imports: `.../v2/parser`, `.../v2/renderer/html`, `.../v2/extension`.
  - API: `p := parser.New(parser.WithExtensions(...))`, `r := html.New(html.WithExtensions(...))`, `doc := p.Parse(src)`, `r.Render(w, src, doc)`.
  - `extension.GFM` enables Table, Strikethrough, Linkify and TaskList. It does **not** filter disallowed raw-HTML tags. Footnotes, definition lists and typographer are separate. `html.WithUnsafe` is off by default.
  - License MIT: P. goldmark-highlighting v2 compatibility is **U**.
  - **Pick v2**: built-in GFM, AST positions, and chroma called directly instead of the highlighting extension.
- **chroma/v2 v2.27.0** (tag commit 2026-06-17; master commits through 2026-10-18). License MIT: P.
  - Use `html.New(html.WithClasses(true), html.WithLinkableLineNumbers(...))` plus `formatter.WriteCSS(w, style)`; optional light/dark via `WithModeClasses`.
  - Language lookup: `lexers.Match(filename)`, `lexers.Get(name)`, `lexers.Analyse(src)`. `quick.Highlight(w, code, "go", "html", "monokai")` is the one-call form.
  - https://raw.githubusercontent.com/alecthomas/chroma/master/README.md lines ~108–208 (V).
- **bluemonday v1.0.27**: last tag 2024-07-04, last master commit 2025-04-04. Idle, not obviously abandoned. License **U**. Open-issue count not checked. **Skip it by default**: goldmark in default mode plus templ escaping covers the threat. Add it only if raw HTML must be allowed. A more active Go sanitizer is U (search not exhaustive).

## 6. Auth

- **golang.org/x/crypto v0.58.0** (tag commit 2026-10-09). License BSD-3: P.
- **argon2**: `argon2.IDKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte`, memory in KiB. RFC 9106 gives two options: (1) t=1, m=2 GiB, p=4; (2) t=3, m=64 MiB, p=4 (source `argon2/argon2.go`, V). A web server cannot spend 2 GiB per login, so use option 2 and cap concurrent hashes with a semaphore. `x/crypto` has no PHC-string encoder; write one.
- **go-oidc v3.21.0** (tag commit 2026-09-01), Apache-2.0 (repo page, V). `oidc.NewProvider(ctx, issuer)`, `provider.Endpoint()`, `provider.Verifier(&oidc.Config{ClientID: ...})`, `verifier.Verify(ctx, rawIDToken)`, `IDToken.Claims(&v)`, `provider.UserInfo`.
- **golang.org/x/oauth2 v0.37.0** (tag commit 2026-08-25). PKCE: `oauth2.GenerateVerifier()`, `oauth2.S256ChallengeOption(v)`, `oauth2.VerifierOption(v)`, with `Config.AuthCodeURL` and `Config.Exchange` (`pkce.go`, V).
- **TOTP, pquerna/otp v1.5.0**: tag 2024-12-31, last commit 2025-08-07 (a comment fix). Its last real change was a 2024-12-31 security fix: `io.ReadFull` for secret generation (commit 5971b1ef). License Apache-2.0: P. Dormant but stable. An in-tree RFC 6238 implementation over `crypto/hmac` is a design option, not a finding. Either way you write rate limiting and replay protection.
- **x/crypto/ssh server suitability for a git SSH endpoint — suitable.**
  - **The evidence is the `x/crypto/ssh` source**, https://raw.githubusercontent.com/golang/crypto/master/ssh/ (`server.go`, `common.go`, `example_test.go`). It shows the public-key callback, `NewServerConn`, channel and request handling. It does not show a git `exec` handler.
  - **The `exec` / `exit-status` pattern is corroborated, not primary.**
    - The local rukie checkout (`rukie/modules/ssh/server.go`, lines 20–147) handles it. It rejects non-`session` channels, parses `exec` with `gossh.Unmarshal(req.Payload, &struct{ Value string })`, handles `env` (for `GIT_PROTOCOL`), and sends `exit-status` via `channel.SendRequest("exit-status", ...)`.
    - I also fetched `go-gitea/gitea` `modules/ssh/server.go` and `ssh.go` from raw.githubusercontent.com. The `server.go` text was line-for-line identical to the local file. The `ssh.go` imports `gitea.dev/...` paths, which makes me doubt what that fetch actually returned.
    - I cannot show those two fetches are independent upstream evidence, so treat the exec handler as **P**, a Gitea-style pattern that rukie inherited. Upstream Gitea at a pinned tag is unread (open question 11).
  - **Gitea-style session handling**, from the local copy of `ssh.go`:
    - `sessionHandler` pipes stdin, stdout and stderr, passes `GIT_PROTOCOL` from session env, and returns an exit status.
    - `publicKeyHandler` returns `Permissions` only for the key that actually signs, and supports `*gossh.Certificate` via `CertChecker` with trusted user-CA keys.
  - **`x/crypto/ssh` API**: `ssh.NewServerConn`, `ServerConfig.PublicKeyCallback(ConnMetadata, PublicKey) (*Permissions, error)`, `VerifiedPublicKeyCallback`, `AuthLogCallback`, `MaxAuthTries` (default 6), `PublicKeyAuthAlgorithms`, `AddHostKey`, `SupportedAlgorithms()` (no SHA-1), `ParseAuthorizedKey`, `FingerprintSHA256`, `CertChecker` (V).
  - **Defaults** (`ssh/common.go`, V): `defaultKexAlgos` starts with `mlkem768x25519-sha256` then `curve25519-sha256`. Pubkey auth supports `sk-ssh-ed25519`, `sk-ecdsa` and ML-DSA keys.
  - **Gaps you write yourself**:
    - Pre-auth connection deadlines and per-IP rate limits.
    - Exec-session piping, EOF/`CloseWrite`, and `exit-status`.
    - Rejecting `pty-req`, subsystems and forwarding.
  - Mapping a session's `exec` onto the Ruk daemon's `CommandOpen` is U (open question 6).

## 7. Diff parsing

- Ruk's typed `diff` reply is `files: Page<FileDiff>`. Each `FileDiff` has `{path, old_path, kind, old/new DiffBlob{oid, executable}, conflicted, body}`, where `body` is `"binary"` or `{unified: {patch: ByteSource}}` (`/tmp/ruk-model.json` `$defs`, V). So you get a **per-file unified patch with typed metadata**, and only a hunk parser is needed.
- **sourcegraph/go-diff v0.9.0** (2026-09-10 release): parses and prints, does not compute diffs. License **MIT** per the LICENSE text (Sourcegraph 2014, plus a second MIT notice for code adapted from python-unidiff), https://raw.githubusercontent.com/sourcegraph/go-diff/master/LICENSE (V). GitHub labels it "Other" because of the dual notice. API: `diff.ParseHunks([]byte) ([]*Hunk, error)`, `ParseFileDiff`, `ParseMultiFileDiff`, `ReverseFileDiff`.
- **bluekeyes/go-gitdiff v0.9.0** (tag commit 2026-07-18), MIT. Parses and applies git patches including binary, in strict mode. `gitdiff.Parse(io.Reader) ([]*File, string, error)`, `TextFragment.Lines []Line{Op, Line}` with `OpContext`/`OpDelete`/`OpAdd`.
- **Pick**: go-diff's `ParseHunks` on Ruk's per-file patch string, or a ~150-line in-tree parser. go-gitdiff only if the forge applies patches itself, which would go through Ruk anyway. The final parse path is **not decided**. Intra-line (word) diff is not provided by either library; what to use for it is U.

## 8. golangci-lint v2

- **v2.15.0**, released 2026-10-16 (immutable release page, V). License **GPL-3.0** (https://raw.githubusercontent.com/golangci/golangci-lint/main/LICENSE, V), so run it as an external tool.
- **Config schema** (https://golangci-lint.run/docs/configuration/file/, V), validated by https://golangci-lint.run/jsonschema/golangci.jsonschema.json:
  - Files: `.golangci.{yml,yaml,toml,json}`. Top-level keys: `version: "2"`, `linters`, `formatters`, `issues`, `output`, `run`, `severity`.
  - `linters`: `default` (`standard|all|none|fast`), `enable`, `disable`, `settings`, `exclusions{generated, presets, rules, paths, warn-unused}`.
  - `formatters`: `enable` among `gci`, `gofmt`, `gofumpt`, `goimports`, `golines`, `swaggo`, plus `settings` and `exclusions`.
  - `issues`: `new`, `new-from-merge-base`, `new-from-rev`, `fix`.
  - **v1→v2**: `golangci-lint migrate` converts the config; formatters moved out of `linters`; `stylecheck`, `gosimple` and `staticcheck` merged into `staticcheck`; `disable-all`/`enable-all` became `linters.default`. https://golangci-lint.run/docs/product/migration-guide/ (V).
- **Suggested starting set**: `default: standard`; enable `gosec`, `noctx`, `sqlclosecheck`, `rowserrcheck`, `bodyclose`, `errorlint`, `sloglint`, `modernize`, `forbidigo`, `depguard`, `contextcheck`; formatters `gofumpt` and `goimports`. Use `depguard`/`forbidigo` to ban `os/exec` of `git` and templ JS helpers. Go 1.27 compatibility of this exact release: U.

## Recommendations summary

| Area | Pick | Reason |
|---|---|---|
| Go | 1.27.2, `go 1.27.0` | Supported to ~Aug 2027; json/v2, generic methods, net/http hardening |
| Views | templ v0.3.1070 | Compiled, typed, streaming |
| DB | modernc v1.60.1; pgx v5.11.0 fallback | 1.x semver, FTS5/JSON build flags, SBOM |
| Migrations | in-tree runner (P); goose v3.29.0 if dual DB | Minimal dependencies |
| MCP | go-sdk v1.8.0, stateless Streamable HTTP + `RequireBearerToken` | Spec 2026-07-28 requires stateless |
| Markdown | goldmark v2.1.6 + chroma v2.27.0 (CSS classes); no bluemonday | AST positions; avoid a dormant sanitizer |
| Auth | argon2id t=3, m=64 MiB, p=4; go-oidc v3.21.0; oauth2 v0.37.0; TOTP in-tree or pquerna/otp | RFC 9106 option 2 |
| SSH | x/crypto v0.58.0 `ssh.NewServerConn` | Gitea-style pattern, API from x/crypto |
| Diff | go-diff v0.9.0 `ParseHunks` | Ruk already gives per-file unified patches |
| Lint | golangci-lint v2.15.0 | |
| Rust | 1.99.1, matches the ruk pin | Current stable |
| CI sandbox | Podman ≥6.1.4 | Only the latest minor is maintained |

## Open questions (all U)
1. Will modernc v1.61.0 be tagged (the changelog lists it), and is the `modernc.org/libc` pin compatible with the rest of `go.mod`?
2. WAL-mode benchmarks on our workload for modernc vs ncruces. Published numbers are `journal_mode=DELETE`.
3. A runtime FTS5 smoke test per driver, and whether Windows builds of modernc include FTS5.
4. Which third-party goldmark extensions exist for v2, and goldmark-highlighting v2 compatibility.
5. Licenses not read: goldmark, chroma, bluemonday, pquerna/otp, golang-migrate, x/crypto.
6. How an SSH `exec` payload (`git-upload-pack '<repo>'`) maps onto `CommandOpen` in `daemon/mod.rs`.
7. Browser support for templ's Declarative Shadow DOM "suspense" pattern.
8. Whether the CI hosts meet Podman 6 prerequisites (cgroups v2, nftables, netavark, pasta).
9. WebAuthn/passkeys library choice (out of scope, not researched).
10. A maintained sanitizer alternative if raw HTML is ever allowed.
11. Upstream Gitea's `exec`/`exit-status` handling at a pinned tag, independently of the rukie copy.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — Gitea-style `exec`/`exit-status` handler; corroboration only, not primary.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="qwen" severity="nit" guidance="weigh, don't blindly obey"> Stop re-checking the go-sdk v1.8.0 date. …

---
→ read(https://github.com/modelcontextprotocol/go-sdk/releases.atom) ⇒ ok · 123 lines

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Stop re-emitting the body. You hav…

---
The go-sdk v1.8.0 date is now confirmed from a fetched source. https://github.com/modelcontextprotocol/go-sdk/releases.atom lists v1.8.0 at 2026-09-14T08:06:18Z and v1.8.0-pre.2 at 2026-09-04T09:14:41Z. The report keeps both dates: release 2026-09-14, tag commit 2026-09-04.

I could not create `research-go-stack.md`. My tool inventory is `read`, `grep`, `web_search` and `ast_grep`, so there is no write tool. No file exists at the target path.

The complete report body, with every correction merged in, is in my last long message. It is headed "research-go-stack.md" and ends at the Critical Files section. Please have an agent with a write tool save that message's body to the path below.

Target: `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-go-stack.md`

I won't paste the body again. I'll wait for Main to say whether it will persist the file or wants the body re-sent.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — pin 1.99.1 equals Rust stable (manifest dated 2026-10-15).
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen` (line 305) and `CommandOpen` (line 390) that the SSH and HTTP front ends call.
- `/home/alpha/rewrite/git-renewed/rukie/modules/ssh/server.go` — Gitea-style `exec`/`exit-status` handler; corroboration only, not primary.
- `/tmp/ruk-model.json` — `FileDiff`/`DiffBody` schema that decides the diff-parser choice.

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Two things from the atom feed you …