Both drafts are below as complete files. I had no write tool in this run, so nothing was written to disk. Save this message as `agents-md-draft.md`.

## 1. `rukie/AGENTS.md`

````markdown
# rukie: agent rules

Rules for agents that change this repository. Ruk is the only repository backend.

## Scope

- Keep only the Gitea clone URL shape, `/{owner}/{repo}.git`. Remove every other inherited Gitea route, template, locale, package, and script. Never import a `gitea.dev` package. [Dieter Rams]

## Architecture

- Define every capability as an `Op` with `In` and `Out` types, in the one operation table in `internal/op`. MUST NOT add a route, MCP tool, CLI command, or HTML form outside that table. [Saul Bass]
- Adapters read the table only. HTTP runs on Huma v2 (v2.39.1) mounted on a `net/http` `ServeMux`. MUST NOT add another router or web framework. [Christopher Alexander]
- Validate each `In` value once, at the `Op` boundary, against an allowlist. [Tony Hoare]
- Serve HTML, JSON, and Markdown from one URL by content negotiation. Each format renders the same `Out` value. No handler branches on format. [Milton Erickson]
- Derive CLI flags (`rukie <op> --json`), MCP tool names, and form field names from the operation name and the `In` field names. MUST NOT invent a second spelling. [Otl Aicher]
- Return errors as RFC 9457 problem documents, for JSON and HTML alike. Use 400 for malformed input, 422 for rejected values, 401 for missing authentication, and 403 for missing permission. [Claude Shannon]
- Keep metadata (users, orgs, teams, tasks, tokens, webhooks, mirror settings) only in `internal/store`. No other package imports a database driver or runs a query. [David Parnas]
- Refs, objects, and changes live only in Ruk. The store MUST NOT copy ref or object state. [Tony Hoare]
- Reach Ruk only through `internal/ruk`. That client pins the Ruk protocol version and refuses a daemon that reports another version at startup. Smart HTTP and SSH handlers MUST NOT parse pkt-lines, packs, or objects. [David Parnas]
- Test Ruk-facing code against a real `ruk` daemon or CLI. Integration tests MUST NOT replace `internal/ruk` with a mock. [John Carmack]

## Zero JavaScript

- Rendered HTML MUST NOT contain `<script>` tags, `on*=` attributes, `javascript:` URLs, or links to JavaScript files. Forms submit with plain HTTP POST. [Dieter Rams]
- Every test that renders HTML calls `zerojs.Assert` from `internal/zerojs` on the response body. The helper fails the test on any construct listed above. [John Carmack]
- The repository MUST NOT contain a Node, npm, pnpm, or JavaScript bundler toolchain. [Italo Calvino]

## Code

- Write no comments in implementation code. [Dieter Rams]
- Exported identifiers MAY carry a doc comment. A doc comment states preconditions and error cases. Unexported identifiers MUST NOT carry a doc comment. [Tony Hoare]
- Wrap each error that a call returns with `fmt.Errorf("<enclosing function>: %w", err)`. The prefix names the function that adds the context. [John Carmack]
- Keep each function below cyclomatic complexity 10 and cognitive complexity 15. [Edsger W. Dijkstra]
- Take a capability from the standard library first. Then take a maintained, de-facto dependency. Write custom code last. Name the tier in the commit body when the choice is not obvious. [Herbert Simon]
- Every goroutine MUST have a shutdown path. It exits when its `context.Context` is cancelled, and its owner waits for it. Pass `context.Context` first to every function that does I/O. [Tony Hoare]
- Optimize only where a profile names the cost. Put the `go test -bench -benchmem` result in the commit body of each hot-path change. [Edward Tufte]
- Hash passwords with argon2id, memory 19456 KiB, time 2, parallelism 1. MUST NOT use lower parameters. [Herbert Simon]
- Never log passwords, tokens, TOTP secrets, or personal data. [Claude Shannon]

## Verification

- Before you claim a change works, run `golangci-lint fmt`, `go vet ./...`, `golangci-lint run ./...`, `go test -race -count=1 ./...`, and `govulncheck ./...`. Read each exit status. Report only what you observed. [Richard Feynman]
- Fix the cause when a check fails. MUST NOT add a `//nolint` directive or weaken a test. If a linter is wrong, ask the user to change `.golangci.yml`. [Richard Feynman]
- Prefer unit tests. Run one test with `go test -run '^TestName$' ./pkg/`. Keep each integration test under 2 seconds. Wait on a condition, never on `time.Sleep`. [Edsger W. Dijkstra]

## Docs

- Write user-facing text (README, CLI help, API reference, tutorials, UI text) in ISO 24495-1 plain language: short sentences, active voice, common words. [Milton Erickson]
- Write maintainer text (doc comments, ADRs, commit bodies, design notes, these rules) in ASD-STE100: one meaning per word, one instruction per sentence. [J.R.R. Tolkien]
- Every number in a doc (count, timing, version) MUST match a command you ran or a tracked file you name. [Richard Feynman]

## Commits and pull requests

- Each commit MUST build, pass its tests, and hold one concern. Split schema, handler, and lint changes into separate commits. Stage only that concern's paths. [Edsger W. Dijkstra]
- Write commit subjects in capitalized imperative. Target 50 characters, maximum 72, no trailing period. Wrap bodies at 72 characters. Say what changed and why, never how. [Claude Shannon]
- Reference issues, PRs, and tasks by full URL. Never use a bare number such as `#12`. Close work with `Closes <full URL>`. Add `BREAKING CHANGE: <what broke>` when an observable surface changes. [J.R.R. Tolkien]
- PR titles follow the subject rule. PR bodies say what changed and why, in under 1000 characters, with no file or task lists. [Edward Tufte]
- Use the global git identity. Pass no `--author`, `-c user.name`, `-c user.email`, or `GIT_*` identity override. Add no trailer that names an agent, including `Co-Authored-By`, `Signed-off-by`, and `Assisted-by`. [Kenya Hara]
- Do not rewrite published history unless the user asks. Push fixes as new commits. [Christopher Alexander]
- Ask the user before you remove a surface a consumer uses, discard data, or run a one-time data migration. [Kenya Hara]
````

## 2. `ruk/AGENTS.md`

````markdown
# ruk: agent rules

Rules for agents that change this repository. Ruk is an agent-native version control system in Rust.

## Layout and state

- Keep the dependency direction `ruk` to `ruk-git` to `ruk-core`. `ruk-core` MUST NOT depend on `ruk-git` or `ruk`. [David Parnas]
- Change repository state only through `Store::publish` or the publication lane. MUST NOT write `.ruk/` files directly. [Tony Hoare]
- Keep one writer per repository. Make each write idempotent by its request id, so a replay is safe. [Edsger W. Dijkstra]
- Keep `unsafe` in the FFI modules that already use it: `crates/ruk-core/src/physical.rs`, `crates/ruk-core/src/coord/windows.rs`, and `crates/ruk-core/src/merge/syntax/vue_ffi.rs`. Inside them, follow the lint table in `Cargo.toml`. Adding `unsafe` anywhere else needs the user's approval. [David Parnas]

## Toolchain and gates

- Pin Rust 1.99.1 in `rust-toolchain.toml`. Change the pin only after CI passes on Ubuntu, macOS, and Windows. [John Carmack]
- Before you claim a change works, run `cargo fmt --all -- --check`, `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`, `cargo nextest run --workspace --locked`, and `cargo test --workspace --locked`. Install cargo-nextest 0.9.148 as CI does. Report only what you observed. [Richard Feynman]
- Pass `--locked` to every cargo build, test, and clippy run. A build MUST NOT change `Cargo.lock` as a side effect. [Tony Hoare]
- Declare each shared dependency once in the `workspace.dependencies` table, pinned with `=`. Crates inherit it with `workspace = true`. [David Parnas]
- Do not relax the lints in the `workspace.lints` table. Use an expect attribute with a reason, never an allow attribute. [Edward Tufte]
- Run one test with `cargo nextest run --workspace --locked -E 'test(=<name>)'`. [Edsger W. Dijkstra]

## Errors, input, and tasks

- Return typed errors with `thiserror` from library crates. Use `anyhow` only at the `ruk` binary boundary. [Claude Shannon]
- Validate each external input before parsing: CLI arguments, control frames, pack data, and daemon requests. Give each new parser a byte or nesting limit. [Tony Hoare]
- Do not add `todo!`, `unimplemented!`, `dbg!`, or placeholder code. A placeholder is a defect, not a stub. [Richard Feynman]
- Every spawned thread or task MUST have a shutdown path and a join point. [Edsger W. Dijkstra]

## Compatibility and evidence

- Ruk MAY break compatibility. Record each change to a protocol method, error code, or CLI verb in `CHANGELOG.md`. Start a breaking entry with `**Breaking:**`. [Tony Hoare]
- `CHANGELOG.md` lists implemented, unreleased behavior only. MUST NOT list planned work. [Richard Feynman]
- Claim a Git-compatible behavior only when `tasks/parity-harness` shows it passing against Git 2.53.0. Claim a CI gate only when `.github/workflows/ci.yml` runs it. Store, Miri, fuzz, and soak gates stay unclaimed until CI runs them. [John Carmack]
- Quote performance numbers only from a tracked `tasks/*/RESULTS.md`. Name the commit, host, and cache state. Report p50, p95, p99, and max, never a mean alone. [Edward Tufte]
- Label warm-cache, single-host CLI results as dev-scale. MUST NOT present them as deployment or daemon load results. [John Carmack]
- Every count in a doc (methods, verbs, grammars) MUST match the command or tracked file that produces it. [Richard Feynman]

## Commits and docs

- Each commit MUST build, pass its tests, and hold one concern. Split schema, handler, and lint changes into separate commits. [Edsger W. Dijkstra]
- Write commit subjects in capitalized imperative. Target 50 characters, maximum 72, no trailing period. Wrap bodies at 72 characters. Say what changed and why, never how. [Claude Shannon]
- Use the global git identity. Pass no per-command identity. Add no trailer that names an agent. [Kenya Hara]
- Do not rewrite published history unless the user asks. Push fixes as new commits. [Christopher Alexander]
- Write README, CLI help, and tutorials in ISO 24495-1 plain language. [Milton Erickson]
- Write design notes, ADRs, and these rules in ASD-STE100: one meaning per word, one instruction per sentence. [J.R.R. Tolkien]
- Ask the user before you change the store format, run a data migration, or remove a command or protocol method. [Kenya Hara]
````

## Notes for the orchestrator

- **Rule counts:** rukie has 36 rules, ruk has 26. Each rule ends with exactly one anchor tag.
- **Advisory handling:**
  - I did not remove the exported-API doc-comment exception. Your settled rule permits doc comments on exported API only, so the rule keeps that exception.
  - I did not add a `SAFETY` exception. ruk already has `unsafe` in `physical.rs`, `coord/windows.rs`, and `merge/syntax/vue_ffi.rs`, with `#[expect(unsafe_code)]` and `SAFETY:` comments. The workspace denies undocumented unsafe in `ruk/Cargo.toml`. Forbidding `unsafe` would rewrite Windows FFI, so that is your decision.
  - I did ban `//nolint` directives. Rukie has no comment exception there.
- **Config conflicts for rukie cutover:** `rukie/.golangci.yml` denies `encoding/json` and `gitea.dev` paths, which conflicts with stdlib-first. Its `revive exported` rule forces doc comments. Its `gci` prefix names `gitea.dev`.
- **Dropped Gitea rules:**
  - Conventional Commits, the `enhance` type, and `Assisted-by`, per your instruction.
  - The agent-attribution line on comments. It names an agent, so it conflicts with the global rule. Restore it only if you decide that.
  - Copyright year headers, because they are comments.
  - Locale, TypeScript, Tailwind, and e2e rules, because they belong to Gitea's JS UI.
  - Make targets, replaced by direct commands.
  - The `64 * 1024` style rule and the modern-Go-features preference.
- **Conflicts resolved:**
  - Global `Closes #N` versus the repo's full-URL rule. The global file says a host repo's convention wins. I kept full URLs.
  - ruk's history uses lowercase `area:` subjects (for example `coord: per-repo sidecar`, in `ruk/.git/logs/HEAD`). It states no convention. I applied the global default.
- **Discrepancy:** ruk's method count is 46 in `ruk/README.md` and `ruk/CHANGELOG.md`. Your brief says 47. Reconcile it. The rule requires counts to match the source.
- **Proposed names:** `internal/op`, `internal/store`, `internal/ruk`, and `internal/zerojs` do not exist yet. They are my proposals.
- **Verified:** The nextest `-E 'test(=name)'` filterset is in the nexte.st docs. I did not verify the `golangci-lint fmt` subcommand against an installed binary.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/AGENTS.md` — Replace with the rukie file above. Removes Gitea rules.
- `/home/alpha/rewrite/git-renewed/rukie/.golangci.yml` — Depguard, gci, and revive rules conflict with the new rules.
- `/home/alpha/rewrite/git-renewed/ruk/.github/workflows/ci.yml` — Source of the Rust gate commands and the Store/Miri/fuzz claim limit.
- `/home/alpha/rewrite/git-renewed/ruk/Cargo.toml` — Workspace lints and pinned dependencies that the Ruk rules cite.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/physical.rs` — Existing `unsafe` FFI site that the `unsafe` rule scopes.

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="minimax" severity="blocker" guidance="weigh, don't blindly obey"> Three invented facts block the bui…

---
## Later corrections from the same agent (apply over the body above)

The review contains several claims that conflict with what I already verified, and some that are stale. I'll re-check the disputed items before revising: the ruk `.gitignore` scope, the rukie e2e helper the advisories cite, the rukie lint make targets, and whether an operation table already exists in rukie.
→ read(/home/alpha/rewrite/git-renewed/ruk/.gitignore) ⇒ ok · 7 lines
→ read(/home/alpha/rewrite/git-renewed/rukie/tests/e2e/utils.ts:215-225) ⇒ ok · 21 lines
→ read(/home/alpha/rewrite/git-renewed/rukie/Makefile:280-320) ⇒ ok · 47 lines
→ grep(type Op\[|OperationTable|operation table|\bOp\[In @ /home/alpha/rewrite/git-renewed/rukie;/home/alpha/rewrite/git-renew…) ⇒ ok · 7 lines

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Remove the exported-doc-comme…

---
The `.gitignore` check changes the scope. It lists `tasks/` as an ignored oracle, so my earlier "tracked `tasks/*/RESULTS.md`" rule cannot be right. Before I state that rukie has no zero-JS helper, I'll grep for one.
→ grep((?i)zero.?js|nojs|no_js|<script|javascript: @ /home/alpha/rewrite/git-renewed/rukie/tests;/home/alpha/rewrite/git-renew…) ⇒ ok · 251 lines

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Verify or delete three unverified …

---
The review mixes valid corrections with stale or wrong ones. I checked the disputed points and revised both files. I could not write `agents-md-draft.md` (no write tool, and plan mode), so the full artifact is below for you to save.

## Changes from the last draft

- **Unsafe:** The ban is gone. Unsafe code now follows the lint pattern (`expect` plus `// SAFETY:`). I dropped the file list because it was incomplete.
- **Scope:** I removed every rule outside your named topics. That removes argon2, RFC 9457, percentiles, complexity, goroutines, Huma, content negotiation, the CHANGELOG-per-commit rule, the ask-gate, the single-test command, `golangci-lint fmt`, and the ruk write-path rule.
- **Package and module names:** No rule names a module path, a Ruk client path, or a helper. Rules say "the store package" and "the Ruk client package".
- **Zero-JS:** Scoped to new HTML. The file states that no zero-JS helper exists, and it does not name one.
- **Ruk gates:** Written in CI form. The brief's short forms are mapped to them.
- **Ruk scope:** Limited to `crates/`, `.github/`, and top-level docs. `git/`, `jj/`, `forgejo/`, and `tasks/` are excluded, per `ruk/.gitignore`.
- **Commit subjects:** Kept, per the global rule.

## 1. `rukie/AGENTS.md`

````markdown
# rukie: agent rules

Rules for agents that change this repository. Ruk is the only repository backend. The operation-table, zero-JS, store, and Ruk-client rules describe the rewrite target; the code they name does not exist yet.

## Scope

- Keep only the Gitea clone URL shape, `/{owner}/{repo}.git`. Remove every other inherited Gitea route, template, locale, package, and script, as the settled rewrite decision requires. [Dieter Rams]

## Build, test, and lint

- Before you claim a Go change works, run `go vet ./...`, `golangci-lint run ./...` (golangci-lint v2), `go test -race -count=1 ./...`, and `govulncheck ./...`. Read each exit status. Report only what you observed. [Richard Feynman]
- Fix the cause when a check fails. MUST NOT add a `//nolint` directive or weaken a test to pass. If a linter is wrong, ask the user to change `.golangci.yml`. [Edward Tufte]

## Zero JavaScript

- New HTML pages in the rewrite MUST NOT contain `<script>` tags, `on*` event attributes, `javascript:` URLs, or links to JavaScript files. Gitea's JavaScript (`web_src/`, the `lint-js` Makefile target) is a Gitea surface to cut. Do not extend it. [Italo Calvino]
- No zero-JS test helper exists yet. Gitea's `assertNoJsError` in `tests/e2e/utils.ts` checks only the `.js-global-error` banner, not script tags. Add a helper that fails on the constructs above with the first HTML page, and call it from every HTML render test. [John Carmack]

## Operation table

- Define every capability as an operation in one operation table, with typed input and output. Add no route, MCP tool, CLI command, or HTML form outside that table. No such table exists yet. [Saul Bass]
- Adapters for HTTP, MCP, CLI, and HTML read that table. An adapter MUST NOT add a route, tool, or command name that the table lacks. [Christopher Alexander]

## Comments and errors

- New implementation code has no comments. Existing Gitea comments stay until you rewrite the code they describe. [Dieter Rams]
- Exported identifiers MAY carry a doc comment that states the contract, including preconditions. The rewrite brief allows doc comments on exported API only, so no other doc comments. [Tony Hoare]
- Wrap each error with context before you return it. The prefix is the enclosing function name, as in `fmt.Errorf("<function name>: %w", err)`. [Claude Shannon]

## Store and Ruk access

- Metadata access goes only through the store package. No other package imports a database driver or runs a query. [David Parnas]
- Ruk access goes only through the Ruk client package. Smart HTTP and SSH handlers call that package and never parse packs, pkt-lines, or objects. [David Parnas]

## Commits and pull requests

- Write each commit subject in capitalized imperative. Keep it to 72 characters, target 50, with no trailing period. Wrap the body at 72 characters and say what changed and why, never how. [Claude Shannon]
- Each commit MUST hold one concern and MUST build with its tests passing. Stage only that concern's paths. [Edsger W. Dijkstra]
- Commit with the global git identity. Pass no per-command identity. Add no trailer that names an agent, such as `Co-Authored-By`, `Signed-off-by`, or `Assisted-by`. [Kenya Hara]
- Reference issues, PRs, and tasks by full URL, never by bare number. Close work with `Closes <full URL>`. [J.R.R. Tolkien]
- PR titles follow the subject rule. PR bodies follow the body rule and stay under 1000 characters. [Edward Tufte]
- Do not rewrite published history unless the user asks. Push fixes as new commits. [Christopher Alexander]

## Docs

- Write user-facing text (README, CLI help, API reference, tutorials, UI text) in ISO 24495-1 plain language: short sentences, active voice, common words. [Milton Erickson]
- Write maintainer text (doc comments, ADRs, commit bodies, design notes, these rules) in ASD-STE100: one meaning per word, one instruction per sentence. [J.R.R. Tolkien]
````

## 2. `ruk/AGENTS.md`

````markdown
# ruk: agent rules

Rules for agents that change this repository. These rules cover `crates/`, `.github/`, and top-level docs. The zero-JS, operation-table, store, and Ruk-client rules belong to rukie and do not apply here.

## Scope

- Do not change `git/`, `jj/`, `forgejo/`, or `tasks/`. `.gitignore` marks them as reference oracles, "never part of this repository". [David Parnas]

## Build, test, and lint

- Build with the toolchain pinned in `rust-toolchain.toml`, Rust 1.99.1. [John Carmack]
- Before you claim a Rust change works, run the three CI gates exactly as written. Format: `cargo fmt --all -- --check`. Lint: `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`. Test: `cargo nextest run --workspace --locked`. The brief's short forms (`cargo fmt --check`, `cargo clippy`, `cargo nextest run --workspace`) name these same gates. Read each exit status. [Richard Feynman]
- Suppress a lint only with an `expect` attribute that gives a reason. Do not relax the lint tables in `Cargo.toml`, and do not use `allow` attributes; `allow_attributes` is denied. [Edward Tufte]

## Unsafe code

- Unsafe code in workspace crates follows the pattern the lints require: an `expect` attribute naming `unsafe_code` with a reason, and a `// SAFETY:` comment that names the invariant. `Cargo.toml` denies `undocumented_unsafe_blocks`. [Tony Hoare]
- Vendored crates under `crates/ruk-core/vendor/` are outside this pattern. [David Parnas]

## Comments and errors

- Add no comments to implementation code, except the `// SAFETY:` comment that `undocumented_unsafe_blocks` requires. Leave existing comments alone unless you change that code. [Dieter Rams]
- Library crates return typed errors built with `thiserror`. Add context when an error crosses a crate boundary. [John Carmack]

## Commits and docs

- Write each commit subject in capitalized imperative. Keep it to 72 characters, target 50, with no trailing period. Wrap the body at 72 characters and say what changed and why, never how. [Claude Shannon]
- Each commit MUST hold one concern and MUST build with its tests passing. [Edsger W. Dijkstra]
- Commit with the global git identity. Pass no per-command identity, and add no trailer that names an agent. [Kenya Hara]
- Do not rewrite published history unless the user asks. Push fixes as new commits. [Christopher Alexander]
- Write README, CLI help, and tutorials in ISO 24495-1 plain language. [Milton Erickson]
- Write design notes, ADRs, and these rules in ASD-STE100: one meaning per word, one instruction per sentence. [J.R.R. Tolkien]
````

## Notes for the orchestrator

- **Rule counts:** rukie has 20 rules and ruk has 14. Each rule ends with one anchor, and every anchor uses the full Influence name from `/home/alpha/.claude/skills/spine/SKILL.md`.
- **Rejected advisory points:**
  - **Doc comments:** The brief you gave permits them. Its engineering bar reads "no comments in implementation code (doc comments on exported API only)". I kept that exception.
  - **`normalizeTestHtml`:** I did not cite it. It is not a zero-JS check, and I did not open it.
  - **Operation table:** No `Op` table exists in rukie. The only "typed operation table" I found is in `ruk/crates/ruk/src/main.rs:83`, which exposes MCP tools. It is ruk's own table, so I did not cite it.
  - **Writing the file:** I had no write tool, and plan mode is read-only. This message is the artifact.
- **Facts I verified:**
  - `rukie/go.mod:1` declares `module gitea.dev`. The brief's target `github.com/gosuda/rukie` is not in any file I read. I left module paths out of the rules, so the choice does not block the agent. You should record the cutover decision in the cutover commit.
  - `rukie/Makefile` lint targets (`lint-go` at lines 314-316, `lint-js` at line 285) exist. `lint-go` runs `go run ./tools/lint-go-all.go`. If the Makefile survives the rewrite, its `lint-go` target must run the same four Go checks.
  - `rukie/web_src/` and `lint-js` are Gitea's JavaScript. The zero-JS rule scopes to new HTML for that reason.
  - `ruk/.gitignore` lists `tasks/` as ignored. My earlier rule that cited `tasks/*/RESULTS.md` as tracked was wrong. I removed it.
  - Ruk already has many `//` comments, for example around `physical.rs:345`. The no-comments rule is forward-only for this reason.
  - Unsafe sites are not limited to the three files I named earlier. My grep shows `physical.rs`, `coord/windows.rs`, and `merge/syntax/vue_ffi.rs`. The review also cites vendor files, which I did not re-verify. The file no longer lists sites, so this does not affect the rule text.
- **Unverified from the review, not used in the rules:**
  - Gitea's argon2 settings. The review reports `argon2$2$65536$8$50` in `rukie/modules/auth/password/hash/setting.go`. The cutover must keep verifying existing password hashes. I did not open that file.
  - `chi` in `rukie/go.mod`.
  - `huma` absent from `rukie/go.mod`.
- **Conflict left open:** The global footer `Closes #N` conflicts with the repo's full-URL rule. The global file says the host repo's convention wins, so the rukie rule uses `Closes <full URL>`.
- **Dropped Gitea rules:** Conventional Commits and `Assisted-by`, per your instruction. Also the agent-attribution line on comments, the copyright-year header, and the locale, TypeScript, and e2e rules.

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/rukie/AGENTS.md` — Replace with the rukie file above.
- `/home/alpha/rewrite/git-renewed/rukie/.golangci.yml` — Gitea depguard, gci, and revive rules conflict with the new Go rules.
- `/home/alpha/rewrite/git-renewed/ruk/.github/workflows/ci.yml` — Source of the three CI gate commands the ruk rules copy.
- `/home/alpha/rewrite/git-renewed/ruk/Cargo.toml` — Workspace lints (`unsafe_code`, `undocumented_unsafe_blocks`, `allow_attributes`) that the unsafe and lint rules cite.
- `/home/alpha/rewrite/git-renewed/ruk/.gitignore` — Defines the excluded oracle trees that the ruk scope rule cites.