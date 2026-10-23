> `PinGrounding` is running a newer turn. The output below is from its PREVIOUS run (published 19.2s ago), not the current one. Live progress: `history://PinGrounding`; its next result auto-delivers to its owner.## Register

| Dimension | Verdict | Reason |
|---|---|---|
| clarity | warn | Two Grounded-set rows are not pins: govulncheck ("latest tagged") and BurntSushi/toml (no version). `research-go-stack.md` disagrees with PLAN on goldmark and argon2id. |
| impact | warn | Putting golangci-lint in the main `go.mod` as a `tool` directive goes against the project's own install docs. |
| audience | pass | An executor needs exact versions. Findings 1–3 supply them. |
| risk | warn | All version pins are current, except goldmark, which is the intended line. Open risks: the golangci-lint setup, two unpinned rows, and MCP middleware behaviors. |
| sequencing | pass | Pins are consumed in the pad commit and later streams. No ordering conflict. |
| reversibility | pass | Pins live in `go.mod` and are cheap to change. |

## Pin table

Status values are only `current`, `stale`, `wrong` or `unverifiable`. "Date" is the GitHub release `published_at` where I read it. If the proxy tag time differs, I say so.

| pin | claimed | current | date | url | status |
|---|---|---|---|---|---|
| Go | 1.27.0 / go1.27.2 | go1.27.2. go1.27.0 was released 2026-08-19, go1.27.1 on 2026-09-01. | 1.27.2: 2026-10-08 | https://go.dev/doc/devel/release | current |
| Rust | 1.99.1 | 1.99.1. ruk `rust-toolchain.toml:2` and `Cargo.toml:16` match. | 2026-10-16 | https://github.com/rust-lang/rust/releases/tag/1.99.1 | current |
| templ | v0.3.1070 | v0.3.1070. No `/v2` module path (404). | tag 2026-10-04 (proxy) | https://github.com/a-h/templ/releases/tag/v0.3.1070 | current |
| modernc sqlite | v1.60.1 | v1.60.1 | 2026-09-29 | https://gitlab.com/cznic/sqlite/-/blob/v1.60.1/CHANGELOG.md | current |
| bundled SQLite | 3.53.4 | The driver bundles 3.53.4 (CHANGELOG v1.58.0 "Upgrade to SQLite 3.53.4"; v1.60.0 "SQLite stays 3.53.4"). Upstream SQLite is 3.54.1, which the driver does not bundle yet. | upstream 3.54.1: 2026-10-16 | https://sqlite.org/changes.html | current (the claim is about the bundle) |
| FTS5 in modernc | included | Built with `-DSQLITE_ENABLE_FTS5` (`lib/sqlite_linux_amd64.go:1`) | n/a | https://gitlab.com/cznic/sqlite/-/blob/v1.60.1/lib/sqlite_linux_amd64.go | current |
| trigram tokenizer in modernc | included | `lib/sqlite.go:7353` "Start of trigram implementation", `TTrigramTokenizer` at `:7355`. FTS5 docs §4.3.4 define it. I did not run `CREATE VIRTUAL TABLE`. | n/a | https://gitlab.com/cznic/sqlite/-/blob/v1.60.1/lib/sqlite.go | current (from source) |
| huma/v2 | v2.39.1 | v2.39.1. `/v3` path 404s. | 2026-07-29 | https://github.com/danielgtaylor/huma/releases/tag/v2.39.1 | current |
| MCP go-sdk | v1.8.0 | v1.8.0. `RequireBearerToken` is at `auth/auth.go:97`. `StreamableHTTPOptions.Stateless` is at `mcp/streamable.go:129-146`. `/v2` path 404s. | GitHub release published 2026-09-14. Proxy/pkg.go.dev tag time 2026-09-04. | https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0 | current |
| goldmark | v1.8.6 | v1.8.6 is the last v1 release, still maintained. `github.com/yuin/goldmark/v2` v2.1.6 is the newer major. | v1.8.6: 2026-09-03 (tag). v2.1.6: 2026-09-27 (GitHub). | https://github.com/yuin/goldmark/releases/tag/v2.1.6 | stale (one major behind; deliberate per `rendering-design.md`; see 4) |
| chroma/v2 | v2.27.0 | v2.27.0. `/v3` exists only as `v3.0.0-alpha.5`, so v2.27.0 is the newest stable. | 2026-06-17 | https://github.com/alecthomas/chroma/releases/tag/v2.27.0 | current |
| go-oidc/v3 | v3.21.0 | v3.21.0. `/v4` path 404s. | 2026-09-01 | https://github.com/coreos/go-oidc/releases/tag/v3.21.0 | current |
| x/oauth2 | v0.37.0 | v0.37.0 | tag 2026-08-25 | https://pkg.go.dev/golang.org/x/oauth2@v0.37.0 | current |
| x/crypto | v0.58.0 | v0.58.0 | tag 2026-10-09 | https://pkg.go.dev/golang.org/x/crypto@v0.58.0 | current |
| go-diff | v0.9.0 | v0.9.0. `func ParseHunks(diff []byte) ([]*Hunk, error)` is at `diff/parse.go:603`. `/v2` path 404s. | tag 2026-09-10 | https://github.com/sourcegraph/go-diff/releases/tag/v0.9.0 | current |
| golangci-lint | v2.15.0 | v2.15.0. `/v3` path 404s. Version is current; the `go tool` install method is discouraged (see 1). | 2026-10-16 | https://github.com/golangci/golangci-lint/releases/tag/v2.15.0 | current |
| govulncheck (x/vuln) | "latest tagged", no version | v1.9.0 | tag 2026-10-11 | https://pkg.go.dev/golang.org/x/vuln@v1.9.0/cmd/govulncheck | unverifiable (PLAN gives no pin; see 2) |
| BurntSushi/toml | no version | v1.6.0 (proxy path `!burnt!sushi`) | tag 2025-12-18 | https://github.com/BurntSushi/toml/releases/tag/v1.6.0 | unverifiable (PLAN gives no pin; see 3) |
| podman | ≥ 6.1.4 | v6.1.4 is the latest release. The repo is now `podman-container-tools/podman`. | 2026-10-12 | https://github.com/podman-container-tools/podman/releases/tag/v6.1.4 | current |
| Standard Webhooks | 1.0.0; `webhook-id`, `webhook-timestamp`, `webhook-signature`; `v1,` prefix | Spec says "Version: 1.0.0". Headers match. Signature format is `v1,<base64>`, space-delimited for rotation. Secrets are `whsec_`-prefixed, 24–64 bytes. | n/a | https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md | current |
| `http.CrossOriginProtection` | used | Added in Go 1.25 | 2025-08-12 | https://go.dev/doc/go1.25 | current |
| `http.ResponseController` | used | Added in Go 1.20 | 2023-02 | https://go.dev/doc/go1.20 | current |
| `go get -tool` / `go tool` | used | Added in Go 1.24 | 2025-02 | https://go.dev/doc/go1.24 | current |
| RFC 9457 | cited | Published August 2023, Standards Track, obsoletes RFC 7807 | 2023-08 | https://www.rfc-editor.org/rfc/rfc9457 | current |
| RFC 6238 | cited | Published May 2011, Informational | 2011-05 | https://www.rfc-editor.org/rfc/rfc6238 | current |
| Argon2id m=19456, t=2, p=1 | used | OWASP: "minimum configuration of 19 MiB of memory, an iteration count of 2, and 1 degree of parallelism"; the list shows "m=19456 (19 MiB), t=2, p=1". RFC 9106's 64 MiB profile is a different option. | n/a | https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html | current |

**Critical question: tool install methods.**

- **golangci-lint discourages `go tool`.** Source: https://golangci-lint.run/docs/welcome/install/local/ (last updated 2026-10-19). The "Install from Sources" section says:
  > "Using `go install`/`go get`, "tools pattern", and `tool` command/directives installations aren't guaranteed to work. We recommend using binary installation."
  >
  > "**`go tool` usage recommendations** … We don't recommend using `go tool`. But if you want to use `go tool` to install and run golangci-lint (**once again we don't recommend that**), the best approach is to use a dedicated module or module file to isolate golangci-lint from other tools or dependencies."

  Reason 3 on that page: "When using the "tools pattern" or `tool` command/directives, the dependencies of a tool can modify the dependencies of another tool or your project. The resulting binary was not tested and is not guaranteed to work."

  The page offers "Method 1: dedicated module file" (`-modfile=golangci-lint.mod`) and "Method 2: dedicated module". PLAN's shared `go.mod` directive is the discouraged form.
- **templ endorses `go tool`.** https://templ.guide/quick-start/installation/ has a section "go install (as tool)": `go get -tool github.com/a-h/templ/cmd/templ@latest`, then "use `go tool templ`".
- **govulncheck: no guidance either way.** The README at tag v1.9.0 and https://go.dev/doc/tutorial/govulncheck both show only `go install golang.org/x/vuln/cmd/govulncheck@latest`. That is an omission, not a discouragement. Its `go.mod` (v1.9.0) requires x/mod, x/sync, x/telemetry, x/tools and x/sys. I did not measure the go.sum overlap with the main module.

## Findings

1. **severity: major.**
   - **where:** Grounded set "Lint" row (~line 49): "golangci-lint v2.15.0 and govulncheck … both pinned as `go tool` directives in `go.mod`". Also the Makefile `lint` target in step 5.
   - **evidence:** the golangci-lint install docs quoted above. golangci-lint v2.15.0 and x/vuln v1.9.0 declare `go 1.26.0` (their `go.mod`), so `go 1.27.0` is compatible.
   - **fix:** Replace the Lint row with:
     > "Lint | golangci-lint v2.15.0, installed through a dedicated module file `golangci-lint.mod`. The golangci-lint docs do not recommend `go tool`; where it is used they require a dedicated module file. govulncheck v1.9.0 and templ v0.3.1070 are `tool` directives in `go.mod`."

     Add to step 5: "Run `go mod init -modfile=golangci-lint.mod github.com/gosuda/rukie/golangci-lint`. Run `go get -tool -modfile=golangci-lint.mod github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`. Make `make lint` run `go tool -modfile=golangci-lint.mod golangci-lint run`. Never update golangci-lint dependencies by hand."

     This adds one file (`golangci-lint.mod`) plus its `.sum` inside the surface the plan already touches. It needs a user yes before execution. If the user declines, use the binary install `curl -sSfL https://golangci-lint.run/install.sh | sh -s v2.15.0`.

2. **severity: major.**
   - **where:** "govulncheck at the latest tagged `golang.org/x/vuln`".
   - **evidence:** https://proxy.golang.org/golang.org/x/vuln/@latest returns v1.9.0 (tag 2026-10-11). The docs show `go install …@latest` only, which is not a pin.
   - **fix:** Replace the span with "govulncheck `golang.org/x/vuln` v1.9.0, a `tool` directive in `go.mod` (`go get -tool golang.org/x/vuln/cmd/govulncheck@v1.9.0`; run `go tool govulncheck ./...`)."

3. **severity: minor.**
   - **where:** "TOML | `github.com/BurntSushi/toml` (decision: …)".
   - **evidence:** https://proxy.golang.org/github.com/!burnt!sushi/toml/@latest returns v1.6.0 (2025-12-18).
   - **fix:** Write "`github.com/BurntSushi/toml` v1.6.0".

4. **severity: minor.**
   - **where:** "Markdown | `github.com/yuin/goldmark` v1.8.6".
   - **evidence:**
     - Release v2.1.6 (2026-09-27, GitHub), module `github.com/yuin/goldmark/v2` (`go.mod:1`).
     - v2 README: v2 "is still in the early stages of release. If you are using an extension that does not support v2, please use v1." Maintenance policy: "will maintain bug fixes, including security fixes, up to one major version prior to the latest major version."
     - `research-go-stack.md` §5 recommends v2.1.6. `rendering-design.md` decision 4 deliberately keeps v1.8.6, because the existing pipeline uses v1 APIs and v2 is young.
     - I did not check v2's raw-HTML default.
   - **fix:** Append to the Markdown row: "v1.8.6 is the last v1 release and is still maintained. A newer major, `github.com/yuin/goldmark/v2` v2.1.6, exists. Keep v1.8.6, per `rendering-design.md` decision 4." Add to step 0: "After the copy, prepend to `docs/plan/research-go-stack.md` a line that names the superseded rows: goldmark (use v1.8.6) and argon2id parameters (use `auth-design.md` §3.1)."

5. **severity: minor.**
   - **where:** F1 (~line 117): "Hash with argon2id: 19456 KiB, t=2, p=1."
   - **evidence:**
     - PLAN matches OWASP and `auth-design.md` §3.1 (marked normative). The OWASP numbers are confirmed in the table.
     - `research-go-stack.md` §5 says t=3, m=64 MiB, p=4 (RFC 9106 option 2). Step 0 copies that file too, so the corpus contradicts PLAN.
   - **fix:** In F1 add: "`docs/plan/auth-design.md` §3.1 owns the argon2id parameters. Ignore the argon2id numbers in `research-go-stack.md`."

6. **severity: minor.**
   - **where:** F9 (~line 134): "stateless Streamable HTTP with `RequireBearerToken` backed by the token store."
   - **evidence:** `auth/auth.go:41,97` and the `verify` body (`auth/auth.go:~125-170`):
     - Zero `Expiration` is rejected with 401 ("token missing expiration") unless `AllowMissingExpiration` is set.
     - A verifier error that does not unwrap to `auth.ErrInvalidToken` becomes HTTP 500.
     - Failures use `http.Error`, which is `text/plain`. That conflicts with the "RFC 9457 errors apply everywhere" line (~line 214).
     - `mcp/streamable.go:138`: in stateless mode GET and DELETE return 405.
   - **fix:** Add to F9:
     > "The verifier returns `auth.TokenInfo` with `Scopes` and a non-zero `Expiration`. It wraps every credential failure in `auth.ErrInvalidToken`. If a token has no expiry, set `AllowMissingExpiration: true`. Set `StreamableHTTPOptions{Stateless: true}`. The `/-/mcp` route accepts POST only. Auth failures on `/-/mcp` keep the SDK's `text/plain` body and `WWW-Authenticate` header."

     Add to the Design line: "Exception: `/-/mcp` auth failures."

7. **severity: minor** (conditional).
   - **where:** SQLite row.
   - **evidence:**
     - PLAN pins only `modernc.org/sqlite`. The sqlite `go.mod:9` requires `modernc.org/libc v1.77.1`.
     - CHANGELOG (v1.60.0): "downstream `go.mod` files must pin the same `modernc.org/libc` version this repository's `go.mod` does".
   - **fix:** Add one sentence only if the user accepts it: "`go.mod` keeps `modernc.org/libc` at the version `modernc.org/sqlite` v1.60.1 requires (v1.77.1). `go mod tidy` selects it. Never run `go get -u modernc.org/libc`."

## Ledger rows

- Replace the Lint row in the Grounded set with the dedicated `golangci-lint.mod` setup and add the step 5 commands.
- Add `golangci-lint.mod` plus its `.sum` to the pad-commit file list, and ask the user to approve the new files.
- Pin govulncheck at `golang.org/x/vuln` v1.9.0 and write the `go tool govulncheck` usage into the Makefile `vuln` target.
- Pin `github.com/BurntSushi/toml` at v1.6.0 in the Grounded set.
- Add the goldmark v1-versus-v2 note to the Markdown row, and add the "superseded rows" header step for `research-go-stack.md` to step 0.
- Add the F1 sentence naming `auth-design.md` §3.1 as owner of the argon2id parameters.
- Add the F9 verifier contract (`Expiration`, `ErrInvalidToken`, POST-only stateless route) and the `/-/mcp` RFC 9457 exception.
- Add the `modernc.org/libc` v1.77.1 sentence to the SQLite row if the user accepts it.

### Critical Files for Implementation

- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/rukie-ruk-native-forge-plan.md` — Grounded set, step 5, F1, F9 and the Design line are the edit sites.
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-go-stack.md` — holds the goldmark v2.1.6 and argon2id t=3/m=64MiB/p=4 rows that conflict with PLAN.
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/auth-design.md` — §3.1 has the normative argon2id parameters.
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/rendering-design.md` — decision 4 explains why goldmark v1.8.6 is kept.
- `/home/alpha/rewrite/git-renewed/ruk/rust-toolchain.toml` — confirms the Rust 1.99.1 pin at line 2.