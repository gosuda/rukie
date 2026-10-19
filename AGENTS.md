# rukie: agent rules

Rules for agents that change this repository. Ruk is the only repository backend. The operation-table, zero-JS, store, and Ruk-client rules describe the rewrite target; the code they name does not exist yet.

## Scope

- Keep only the Gitea clone URL shape, `/{owner}/{repo}.git`. Remove every other inherited Gitea route, template, locale, package, and script, as the settled rewrite decision requires. [Dieter Rams]

## Build, test, and lint

- Before you claim a Go change works, run `make fmt`, `make vet`, `make lint`, `make test`, and `make vuln`, or the single `make check` gate. `make fmt` runs `gofmt -w .`; `make vet` runs `go vet` on untagged and tagged builds; `make lint` runs golangci-lint v2; `make test` runs `go test -race -count=1` through `tools/passgate.sh`; `make vuln` runs govulncheck. Read each exit status. Report only what you observed. [Richard Feynman]
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
Per-task goals live in .agent-tasks/task-0e2cc1171807/GOALS.md.
