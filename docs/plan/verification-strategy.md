Verified in this pass:

- The daemon turns `CommandOpen` into `CliEnv { vars, cwd, stdin }` (`daemon/mod.rs:1371-1374`) and runs argv through the CLI with a `git` prefix, so `CommandOpen.env` is the command's environment.
- `ruk_ipc.go` and `ruk_capability.go` import only the standard library, so they can be copied into rukie unchanged.
- `serve_store` silently ignores every `-`-prefixed argument (`verbs_transport.rs:1859-1876`). Today `--stateless-rpc` is dropped, so a POST would get an advertisement.

## Advisory disposition

- **§5 `web/` blocker: valid.** Removed. The backstop scans `templates/` and `tests/testdata/`, which exist.
- **Reuse the Go IPC client: valid.** Byte-copy the two stdlib-only files into `internal/rukclient`, with a `diff` drift gate, then test against the real daemon.
- **PASS checks on every named gate (minimax, glm-flash, stickler): valid.** A `passgate` helper requires exit 0 and a PASS line for each name. Skips and zero-match runs fail.
- **Git wire through CommandOpen (minimax, stickler): valid.** The adapter goes through the daemon, so receive-pack runs under the daemon's per-repo serialization. The lane claim is itself gated by a concurrent-push test.
- **Byte-compare oracles (glm-flash): valid.** Replaced with semantic checks: ref sets and object-id sets.
- **nextest filter (qwen): valid.** Both oracle test names contain `stateless`.
- **`TestDumpRenderedPages` untagged (qwen): valid.** Moved behind a `render` build tag. It needs `RUKIE_RENDER_DIR`.
- **RUK_COORDINATOR per CommandOpen env (overlycritic, earlier): valid.** Each CLI-shaped `CommandOpen.env` carries `RUK_COORDINATOR=off`. Typed methods never read it.
- **net.rs as authority (overlycritic, earlier): valid.** The README sentence is stale. Dumb-http push is the named gap.
- **Stale or already applied:** the `-race` perf removal, the mutation flag removal, and the Makefile pin citations (now §1 only).

---

# verification-strategy.md — rukie rewrite on Ruk only

## 0. Conventions

- Every command block states its `cwd`. Ruk: `/home/alpha/rewrite/git-renewed/ruk`. New Go tree: `/home/alpha/rewrite/git-renewed/rukie`.
- Daemon: only `ruk forgejo serve --config <file.toml>` (`ruk/crates/ruk/src/main.rs:31-72`, executed `:201-217`). No `ruk serve` verb exists.
- Build tags: `integration` (real daemon, golden), `e2e` (end-to-end), `perf` (budgets, release binary), `render` (dumps captured responses). Untagged tests are unit tests and need no Ruk binary.
- **PASS rule.** A gate passes only when its exit code is 0 and every named test prints `--- PASS:` (Go) or `... ok` (libtest). Zero matches, skips, and missing tests fail. Run every named gate through this helper (bash):

```bash
passgate() {
  local -a needles=() cmd=()
  local sep=0 a
  for a in "$@"; do
    if [ "$sep" -eq 0 ]; then
      if [ "$a" = "--" ]; then sep=1; else needles+=("$a"); fi
    else
      cmd+=("$a")
    fi
  done
  local out s n
  out=$(mktemp) || return 1
  "${cmd[@]}" >"$out" 2>&1; s=$?
  cat "$out"
  if [ "$s" -ne 0 ]; then echo "passgate: exit $s" >&2; rm -f "$out"; return "$s"; fi
  for n in "${needles[@]}"; do
    if ! grep -Fq -- "$n" "$out"; then echo "passgate: no PASS line for: $n" >&2; rm -f "$out"; return 1; fi
  done
  rm -f "$out"
}
```

- Go PASS needle format: `--- PASS: TestName (`. Subtest: `--- PASS: TestParent/sub (`.
- No gate reads `/tmp` fixtures. Temp dirs come from `t.TempDir()`.
- `inference` marks proposals. Everything else is cited to a read source.

## 1. Deleted from `rukie/Makefile`

This file is cited only here. Targets that MUST NOT survive:

- **Frontend:** `frontend`, `vite`, `svg`, `svg-check`, `FRONTEND_*`, `watch-frontend`, `lint-frontend*`, `lint-js`, `lint-css`, `checks-frontend`, `lockfile-check`, `deps-frontend`, `node_modules`, `pnpm`.
- **E2E/JS:** `playwright`, `PLAYWRIGHT_*`, `test-e2e`, `test-frontend`.
- **Spec codegen:** `generate-swagger`, `swagger-check`, `swagger-validate`, `generate-openapi3`, `openapi3-check`, `lint-swagger`, `SWAGGER_*`, `OPENAPI3_SPEC`.
- **Legacy backend:** `backend`, `generate-backend`, `generate-go`, `test-backend*`, `test-integration*`, `test-migration`, `migrations.*`, `test-check`, `coverage`, `unit-test-coverage`, `GITEA_TEST_DATABASE`, `TEST_MYSQL_*`, `TEST_PGSQL_*`, `TEST_MSSQL_*`, `TEST_MINIO_*`, `TAGS`, `CGO_TAGS`, `BINDATA_*`.
- **Release/packaging:** `release*`, `DIST*`, `RELEASE_ENV`, `vendor`, `generate-manpage`, `generate-gitignore`, `generate-emoji`, `generate-images`, `generate-codemirror-languages`.
- **Foreign tooling:** `lint-templates`, `lint-md`, `lint-json`, `lint-yaml`, `lint-shell`, `lint-actions`, `lint-editorconfig`, `deps-tools`, `deps-py`, `.venv`, `watch`, `watch-backend`, `CONTAINER_RUNTIME`.
- **Replaced:** `lint-go` → G2. `fmt-check` → G5. `tidy-check` → G5. `security-check` → G4, now blocking (the deleted target ran `|| true`).

## 2. Go gates

Pins come from upstream release feeds, not the deleted Makefile:

- golangci-lint **v2.15.0** (`https://github.com/golangci/golangci-lint/releases`, newest 2026-10-16). Command shape read from v2.14.0 source; v2.15.0 is `inference`.
- govulncheck **v1.9.0** (`https://github.com/golang/vuln/releases.atom`, newest 2026-10-11).
- gremlins **v0.6.0** (`https://github.com/go-gremlins/gremlins/releases.atom`, newest 2025-12-06).

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
go version                                   # go.mod: go 1.27 / toolchain go1.27.2
go vet ./...                                 # G1a
go vet -tags 'integration e2e perf render' ./...   # G1b: type-checks tagged suites
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...   # G2
go test -race -count=1 -v ./... > /dev/null  # G3 runs untagged tests; see passgate below
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...   # G4 (blocking)
```

G3 with at least one PASS line:

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
passgate '--- PASS: ' -- go test -race -count=1 -v ./...
```

- G2 config: `run: build-tags: [integration, e2e, perf, render]` (`inference`: v2 key; verify on first run). No `formatters:` section is needed.

**G5 — non-mutating format and tidy.** Nothing here writes files.

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
go build ./...
out=$(git ls-files -z '*.go' | xargs -0r gofmt -l) || exit 1
test -z "$out" || { printf '%s\n' "$out" >&2; exit 1; }
go mod tidy -diff
go mod verify
```

- `xargs -0r` runs nothing on an empty list. A gofmt parse error exits 2, which makes xargs exit 123, so the first `|| exit 1` fires. `$(...)` alone would hide that status.
- `go mod tidy -diff` (Go ≥1.23) reports required changes and exits non-zero without writing `go.mod` or `go.sum`.
- Limit: `git ls-files` sees only tracked files. Run G5 after `git add` or on a commit.

Goroutine rule: each goroutine-starting test registers a shutdown func. A `TestMain` leak check fails on leftovers (`inference`: `goleak` or stdlib polling).

## 3. Ruk gates

Working directory: `/home/alpha/rewrite/git-renewed/ruk`. The toolchain `1.99.1` comes from `rust-toolchain.toml`. nextest is a cargo subcommand, not a rustup component.

```sh
# cwd: /home/alpha/rewrite/git-renewed/ruk
cargo install cargo-nextest --version 0.9.148 --locked   # one-time; CI pins 0.9.148
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --all-features --locked -- -D warnings
cargo nextest run --workspace --locked
cargo test --workspace --locked
```

Source: `ruk/.github/workflows/ci.yml`. No `justfile` or `Makefile` exists in `ruk/`.

**Ruk prerequisite slice: stateless RPC.** Required before §6 step 2.

- Read: `serve_store` (`cli/verbs_transport.rs:1859-1876`) parses positionals and `--` only. Every `-`-prefixed argument is silently ignored ("carry no protocol meaning here"). Today `--stateless-rpc` is dropped, so a POST would receive an advertisement.
- The slice must parse and implement `--stateless-rpc` (read one request body, write one response, no advertisement) and `--advertise-refs` (write the `GET info/refs` body). Flag names follow git's `upload-pack` and `receive-pack` (`ruk/git/` is the oracle tree; `--advertise-refs` is `inference`).
- Dispatch: the daemon runs `git <argv>` through the CLI (`daemon/mod.rs:1347-1374`). The verb must resolve under that `git` prefix (`inference`; the gate below proves it).
- Oracle style: semantic, not byte-exact. Git advertises more capabilities than Ruk (`serve.rs:1-43` lists Ruk's set), so byte-compare would fail correct output.

Required tests (names are contracts):

- `stateless_info_refs_matches_oracle_refs`: parse the advertisement's (oid, refname) list; equal to `git upload-pack --stateless-rpc --advertise-refs` for the same repo. Capabilities ignored.
- `stateless_post_matches_oracle_objects`: a captured request body (wants, haves, `done`) yields a response whose pack passes `git index-pack --stdin`, and whose object-id set equals the oracle pack's.

```sh
# cwd: /home/alpha/rewrite/git-renewed/ruk
passgate 'stateless_info_refs_matches_oracle_refs ... ok' 'stateless_post_matches_oracle_objects ... ok' -- cargo test --workspace --locked -- stateless
```

**Protocol golden.** Regenerate only for intentional Ruk bumps:

```sh
# cwd: /home/alpha/rewrite/git-renewed/ruk
cargo build --bin ruk
./target/debug/ruk model --format json > /home/alpha/rewrite/git-renewed/rukie/internal/rukproto/testdata/ruk-model.golden.json
```

`ruk model --format json` is `Command::Model` (`main.rs:46-50`), rendered by `model_schema_document()` (`:170`, `:304-308`). `sonic-rs` uses `sort_keys` (`ruk/Cargo.toml`), so output should be byte-stable (`inference`).

## 4. Daemon harness and Go IPC client (tag `integration`)

Prerequisites:

```sh
# cwd: /home/alpha/rewrite/git-renewed/ruk
cargo build --bin ruk              # target/debug/ruk
cargo build --release --bin ruk    # target/release/ruk, §7 only
```

**Reuse the existing Go client.** The Forgejo tree already contains the client:

- `ruk/forgejo/modules/git/ruk_ipc.go`: stdlib-only imports (`context`, `encoding/binary`, `encoding/json`, `errors`, `fmt`, `io`, `net`, `sync`, `time`). Protocol major 1, chunk and control bounds, initial credit 256 KiB, frame kinds, `RukEncodeFrame`, `RukOpenRequest`.
- `ruk/forgejo/modules/git/ruk_capability.go`: stdlib-only imports. `ConfigureRukBackend(socketPath)`.

Port: copy both files byte-for-byte into `rukie/internal/rukclient/`, changing only the package clause. Drift gate:

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
diff <(grep -v '^package ' /home/alpha/rewrite/git-renewed/ruk/forgejo/modules/git/ruk_ipc.go) <(grep -v '^package ' internal/rukclient/ruk_ipc.go)
diff <(grep -v '^package ' /home/alpha/rewrite/git-renewed/ruk/forgejo/modules/git/ruk_capability.go) <(grep -v '^package ' internal/rukclient/ruk_capability.go)
```

Client gates: `TestRukClientFramingRoundTrip` (untagged, fake socket) and the integration tests below (real daemon). Exported API is exercised against the real daemon, not only fakes.

**Daemon config (TOML).** Keys from `daemon/mod.rs` `from_toml`: `socket_path`, `repo_roots` (required), `allowed_uids`, `max_connections`, `blocking_pool_size`, `handshake_timeout_ms`, `frame_timeout_ms`, `stream_window_bytes`, `retention_interval_ms`.

**Harness rules** (`inference`: layout and `TestMain` are proposals; the contract is fixed):

1. `TestMain` resolves `RUK_BIN` (default `/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk`). It fails fast with the `cargo build` line if the binary is missing.
2. Each test gets `t.TempDir()` holding `roots/`, `ruk.sock`, `daemon.toml`. It spawns `"$RUK_BIN" forgejo serve --config <tmp>/daemon.toml`, polls for the socket (≤5 s, 10 ms backoff), and kills the process group in `t.Cleanup`.
3. **Coordinator.** Typed InvocationOpen methods never consult `RUK_COORDINATOR`. Only the CLI reads it (`cli/coordinator.rs:54`, `ctx.env.get`). A CLI-shaped command dispatched through CommandOpen gets its environment from `CommandOpen.env` (`daemon/mod.rs:1329-1374`: `ReadyCommand.vars` → `CliEnv.vars`; the command sees only those entries, per the `CommandOpen` doc at `:390-420`). Therefore every CLI-shaped CommandOpen env MUST contain `RUK_COORDINATOR=off`. Setting it on the daemon process has no effect on those commands. The pattern is `ruk/crates/ruk/tests/coordinator.rs:335-342` (`off_forces_direct_publish`: no `.ruk/coordinator.sock`).
4. Seeding uses the CLI directly with `RUK_COORDINATOR=off` (`git init`, `add`, `commit`). Assertions use `internal/rukclient` typed reads (`status`, `log`, `show`).
5. `t.Parallel()` is fine. Each test owns its daemon, socket, and roots.

Gates:

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
passgate '--- PASS: TestDaemonLifecycle (' '--- PASS: TestDaemonRejectsRepoOutsideRoots (' '--- PASS: TestCommandOpenCommitBypassesCoordinator (' -- env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk go test -race -count=1 -tags integration -v ./tests/integration/
passgate '--- PASS: TestModelGolden (' -- env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk go test -count=1 -tags integration -v -run '^TestModelGolden$' ./internal/rukproto/
passgate '--- PASS: TestRukClientFramingRoundTrip (' -- go test -race -count=1 -v -run '^TestRukClientFramingRoundTrip$' ./internal/rukclient/
```

## 5. Zero-JS UI gates

**Structural helper** `assertNoJS(t, body)` (`inference`: `internal/webuitest`). Runs on response bytes, not templates:

- Fails on case-insensitive `<script`, on `on*=` attributes (`\son[a-z]+\s*=`), `javascript:`, `<iframe`, `<object`, `<embed`, `srcdoc=`.
- Exactly one `<html>` and one `<body>`. Every `<form>` has `method` and `action`. Every `input`, `select`, `textarea` has `name`.
- Headers on every 200 HTML response and redirect (`inference`: value proposed): `Content-Security-Policy: default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; style-src 'self'; object-src 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`.
- Registry test: fails the build when an operation lacks a page test that calls the helper.

Unit gate (untagged):

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
passgate '--- PASS: TestRenderedPagesNoScript (' '--- PASS: TestRenderedPagesSecurityHeaders (' -- go test -race -count=1 -v -run '^(TestRenderedPagesNoScript|TestRenderedPagesSecurityHeaders)$' ./internal/web/
```

**HTML validity: html-validate on captured responses.** `TestDumpRenderedPages` (tag `render`) drives every operation through the full router with `httptest.NewRecorder`, then writes each response body to `$RUKIE_RENDER_DIR`. It fails when the file count is below the operation count. This validates what the server sends, not template source.

- `.htmlvalidate.json`: `{ "extends": ["html-validate:recommended"] }` (`https://html-validate.org/usage/index.html`).
- Exit status: an error-severity rule makes the CLI exit non-zero (`https://html-validate.org/usage/cli.html`, the `error` severity row). `--max-warnings 0` fails on warnings (same page, `--max-warnings`).
- Node requirement per npm registry metadata for html-validate 11.16.4: `engines.node` = `^22.22.0 || >= 24.8.0`. Test-only; the product ships no JavaScript.

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
passgate '--- PASS: TestDumpRenderedPages (' -- env RUKIE_RENDER_DIR="$PWD/.rendered" go test -count=1 -v -tags render -run '^TestDumpRenderedPages$' ./internal/web/
test "$(ls .rendered/*.html 2>/dev/null | wc -l)" -gt 0
npm exec --yes --package=html-validate@11.16.4 -- html-validate --config .htmlvalidate.json --max-warnings 0 .rendered/*.html
```

The final command's exit status is the gate status.

**Source backstop.** Scans template and fixture roots that exist in the current tree. A missing root exits 2 and fails loudly by design. On the pre-cutover tree this gate fails when Gitea templates contain `<script` (`inference`), which is expected. When templates move, update the root list in the same commit.

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
grep -rEi '<script|srcdoc=|<iframe' --include='*.tmpl' --include='*.html' templates/ tests/testdata/; s=$?; case $s in 0) echo "forbidden markup in source" >&2; exit 1;; 1) ;; *) exit "$s";; esac
```

## 6. End-to-end agent flow (tag `e2e`)

Test `TestAgentFlow` in `tests/e2e/` (`inference`: path). Subtests are self-contained. Everything binds `127.0.0.1` on ephemeral ports. Prerequisites: debug `RUK_BIN` (§4), git ≥2.40, the §3 Ruk slice landed.

`git-http-backend` is resolved as `rukie/tests/integration/migrate_test.go:186-189` does: `git --exec-path`, then `<exec-path>/git-http-backend`.

Full gate (all seven subtests):

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
passgate '--- PASS: TestAgentFlow/create (' '--- PASS: TestAgentFlow/push (' '--- PASS: TestAgentFlow/change (' '--- PASS: TestAgentFlow/ci_attest (' '--- PASS: TestAgentFlow/land (' '--- PASS: TestAgentFlow/mirror (' '--- PASS: TestAgentFlow/webhooks (' -- env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk GIT_EXEC="$(command -v git)" go test -race -count=1 -tags e2e -v -run '^TestAgentFlow$' ./tests/e2e/
```

**Step 1 — `create`.** Create a repo through the JSON API as an argon2id+TOTP user with a scoped agent token. OIDC is covered by a unit test with a stub provider. Assert only the `/{owner}/{repo}.git` clone URL shape.

**Step 2 — `push` over smart HTTP with stock git.** Git-wire verbs go through the daemon, not per-request processes.

- **2a — Ruk slice:** §3 `stateless_*` tests PASS.
- **2b — adapter.** `internal/githttp` (`inference`) uses `internal/rukclient` to open a `CommandOpen` on the daemon. It does not spawn `ruk` per request.
  - `GET /{owner}/{repo}.git/info/refs?service=git-upload-pack|git-receive-pack`: `Content-Type: application/x-<svc>-advertisement`. The adapter writes the `# service=<svc>` pkt-line and a flush (`inference`), then streams `CommandOpen` argv `["upload-pack", "--advertise-refs", "<dir>"]` (or receive-pack).
  - `POST .../git-upload-pack|git-receive-pack`: `Content-Type: application/x-<svc>-request`. Decompress `Content-Encoding: gzip`. Stream the body as stdin of `CommandOpen` argv `["<verb>", "--stateless-rpc", "<dir>"]`. Stdout becomes the response with `application/x-<svc>-result`.
  - `CommandOpen` fields (`daemon/mod.rs:390-420`): `repository` is the authorized path; `dir` is the repo; `env` is exactly `["RUK_COORDINATOR=off"]` plus whatever the CLI requires (`inference`); `request_id` is `sha256(repo ‖ service ‖ body)`, so a git retry replays the recorded terminal report instead of applying twice; `command_digest` covers argv, dir, env. The daemon refuses a reused request id with a different digest.
  - **Lane.** The daemon owns each repo and serializes publication per repo (`daemon/mod.rs:1-7`). Whether a `CommandOpen` receive-pack holds that lane is unverified. Gate: `TestConcurrentPushesSerialize` (eight concurrent pushes to one repo through the adapter). Every push must be accepted or cleanly refused with `ng`. The final ref must equal one of the pushed tips. `git fsck` must pass.
  - Authorization is the adapter's: scoped token, write scope for receive-pack. No CGI `REMOTE_USER`.
  - Protocol v0 only. `smart_http.rs:898-900` names protocol v2 over HTTP as a gap. Stock git falls back to v0 (`inference`; the gate proves it).
- **2c — gate:** the `push` and `TestConcurrentPushesSerialize` names PASS (commands in the full gate and below).

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
passgate '--- PASS: TestConcurrentPushesSerialize (' -- env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk go test -race -count=1 -tags e2e -v -run '^TestConcurrentPushesSerialize$' ./tests/e2e/
```

**Step 3 — `change`.** `git push origin HEAD:refs/for/main` opens a change with a change-id. Assert it appears in the change view and in the branch-grouped view for `main`.

**Step 4 — `ci_attest`.** A fake runner (`inference`: `rukie ci-run --local`) runs one passing job and one job that fails then retries, recording `attest.*` entries. Poll until visible. Assert a failed job blocks land with `409` and an RFC 9457 `application/problem+json` body.

**Step 5 — `land`.** `POST .../land` runs `land.prepare` then `land.execute`. Assert the branch advances, the change closes, and `undo` is available.

**Step 6 — `mirror`** to a fake GitHub served by git-http-backend.

- **Authority:** `ruk-git/src/transport/net.rs:7-10` routes `http(s)://` URLs to the stateless smart-http client in `smart_http.rs`, which implements client RPCs (`smart_http.rs:1-8`). Push over dumb-http stays refused (`net.rs:9-10`). `ruk/README.md`'s line that calls `http(s)://` a named unimplemented gap is stale and is not authority.
- **Expected behavior:** smart-http push to a git-http-backend remote succeeds. A dumb-http push is refused. A regression in the smart-http client is a Ruk defect, filed against Ruk.
- Mirror sync runs through the daemon as a `CommandOpen` push with `RUK_COORDINATOR=off` in its env, the same as step 2.
- **Shim environment** (`ruk/git/http-backend.c`):

```
GIT_PROJECT_ROOT=<parent of fake-github.git>   # migrate_test.go:205-208 pattern
GIT_HTTP_EXPORT_ALL=1                          # http-backend.c:814-816: else 404 "Repository not exported"
REMOTE_USER=rukie-e2e                          # http-backend.c:288-290: else receive-pack 403 "Service not enabled"
```

`migrate_test.go:205-208` sets only the first two, which is enough for fetch but not push. `REMOTE_USER` is an unauthenticated test assertion; the shim binds `127.0.0.1` only.

Scenarios, asserted against `fake-github.git`:

1. `exact` with selection `all`: push a branch, delete a branch, push a tag. The remote matches exactly, including prune.
2. `fast-forward-only` with `default-branch+tags`: a forced push is rejected and recorded. The remote is unchanged.
3. Glob `refs/heads/release/*`: only matching refs mirror.
4. Both triggers (on ref update, and a 1 s reconcile): delete a ref directly in `fake-github.git`; the reconcile restores it within 5 s.

**Step 7 — `webhooks`.** A local `httptest.Server` receives Standard Webhooks deliveries. Verify `webhook-signature` (HMAC-SHA256) and `webhook-timestamp` (`inference`: header names). Assert signed deliveries for push, change-opened, CI-completed, land, and mirror-synced. For one delivery, return 500 once, then assert a retry with the same event id and a 200.

## 7. Performance budgets (tag `perf`)

Budgets (warm daemon; ≤1k commits and ≤10k files; all `inference`, calibrated in the first perf phase):

- p95 HTML page render ≤ 200 ms
- p95 JSON API read ≤ 100 ms
- p95 `info/refs` (adv-1k shape) ≤ 150 ms
- p95 server-side search ≤ 500 ms, top-N plus count

Ruk floor, from `ruk/README.md` capacity table: adv-1k p50 22 ms; page1 p50 98 ms.

**No `-race`.** The race detector inflates latency, including in-process `httptest` handlers.

```sh
# cwd: /home/alpha/rewrite/git-renewed/ruk
cargo build --release --bin ruk

# cwd: /home/alpha/rewrite/git-renewed/rukie
passgate '--- PASS: TestPerfBudget (' -- env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/release/ruk go test -count=1 -tags perf -timeout 30m -v -run '^TestPerfBudget$' ./internal/bench/
env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/release/ruk go test -count=1 -tags perf -run='^$' -bench=. -benchtime=1000x ./internal/bench/
```

The harness is stdlib `httptest` plus hand-rolled p50/p95/p99 over ≥1000 samples after 100 warmups. The nightly run uses `-count=3` and appends to `internal/bench/RESULTS.md` (`inference`).

## 8. Mutation testing (weekly, not a merge gate)

gremlins v0.6.0. The flags used are the long names read from `cmd/unleash.go`: `--diff`, `--exclude-files`, `--threshold-efficacy`, `--output`, `--dry-run`. `cmd/gremlins/main.go` is the main package.

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
mkdir -p .gremlins
```

**PR scope** (mutates only lines changed against the base):

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
gremlins unleash --diff origin/main --threshold-efficacy 80 --output .gremlins/pr.json .
```

**Weekly scope** targets `land`, `mirror`, `auth`, `webhook` (`inference`: package names). gremlins has no package-include flag, so the run excludes every other `internal/*` directory by path regex:

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
set --
for dir in $(find internal -mindepth 1 -maxdepth 1 -type d | grep -Ev '^internal/(land|mirror|auth|webhook)$'); do
  set -- "$@" --exclude-files "^$dir/"
done
gremlins unleash --dry-run "$@" --output .gremlins/dry.json .
```

Verify before trusting: `--exclude-files` path semantics and the dry-run output format are unverified (`inference`). Inspect `.gremlins/dry.json`. Confirm that no RUNNABLE mutant names a file outside the four target directories. Then run the weekly job:

```sh
# cwd: /home/alpha/rewrite/git-renewed/rukie
gremlins unleash "$@" --threshold-efficacy 80 --output .gremlins/weekly.json .
```

The run takes hours on a module this size. Target ≥80% efficacy (`inference`). Each survivor is killed by a new assertion or documented as equivalent.

## 9. Phase → gate matrix

| Phase | Must pass (all named tests PASS via `passgate`) |
|---|---|
| Ruk stateless slice | `stateless_info_refs_matches_oracle_refs`, `stateless_post_matches_oracle_objects` |
| Client port (`internal/rukclient`) | drift `diff`s; `TestRukClientFramingRoundTrip`; `TestDaemonLifecycle` |
| Scaffold, op table, RFC 9457, OpenAPI 3.1 | G1–G5; OpenAPI spec test |
| Metadata seam | G1–G5; same suite on ≥2 backends (`inference`) |
| Identity | G1–G5; auth contracts; `create` |
| Hosting, browsing | G1–G5; §5 unit; §5 render + html-validate; §7 page and API budgets |
| Git wire | G1–G5; `push`; `TestConcurrentPushesSerialize`; `TestCommandOpenCommitBypassesCoordinator` |
| Changes, branch view | G1–G5; `change` |
| Tasks, orgs, teams | G1–G5; §4 lease contention; `create` |
| CI attestations | G1–G5; `ci_attest`, `land` |
| Mirrors | G1–G5; `mirror` (all scenarios) |
| Releases, explore, search | G1–G5; §5; §7 search budget |
| Webhooks, MCP, `rukie <op> --json` | G1–G5; `webhooks`; CLI JSON goldens |
| Hardening | §7 green on release binary; §8 triaged; docs and changelog |

A phase touching Ruk also runs §3 at `/home/alpha/rewrite/git-renewed/ruk`.

## 10. Known gaps and risks

- Ruk has no HTTP server. The adapter is rukie code over the daemon.
- The per-repo lane for `CommandOpen` receive-pack is unverified until `TestConcurrentPushesSerialize` passes.
- The `git` argv prefix on `CommandOpen` must resolve `upload-pack` and `receive-pack` (`inference`).
- The gremlins exclusion semantics and dry-run format are unverified.
- golangci-lint v2.15.0 command shape is read from v2.14.0 source.
- html-validate adds Node (test-only).
- gremlins is 0.x; pinned to v0.6.0.

## 11. Red/green and flake policy

- Tests land first where a seam exists. Each commit names the gate that proves it.
- Untagged `go test` runs with `-race -count=1`. Tagged functional suites run with `-race`. Perf never runs with `-race`.
- Flakes: rerun with `-count=3`, record the seed and output, then fix or quarantine behind a tracked task. No `//nolint` without explanation. No `t.Skip` without an issue link. No timeout increases in place of a race fix.
- CI keeps `go test -json`, the govulncheck report, the weekly gremlins report, and the perf table.

### Critical Files for Implementation

- `ruk/crates/ruk/src/cli/verbs_transport.rs` — `serve_store` (1859-1876) silently ignores `-` flags; the Ruk slice must parse `--stateless-rpc` and `--advertise-refs` here.
- `ruk/crates/ruk/src/daemon/mod.rs` — `CommandOpen` (386-420), `ReadyCommand` → `CliEnv` (1329-1374). Defines how `RUK_COORDINATOR=off` reaches CLI commands.
- `ruk/forgejo/modules/git/ruk_ipc.go` — the stdlib-only client to port byte-for-byte into `internal/rukclient`.
- `ruk/crates/ruk-git/src/transport/net.rs` — lines 7-10 are the HTTP(S) client authority.
- `ruk/crates/ruk/src/cli/coordinator.rs` — line 54, the only reader of `RUK_COORDINATOR`.