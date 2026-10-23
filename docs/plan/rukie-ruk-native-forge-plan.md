# Rukie: a Ruk-native forge for agents (full break from Gitea)

## Context

Rukie lives at `/home/alpha/rewrite/git-renewed/rukie`. It is a fork of Gitea at `ba4f8fa246` on branch `main`. The tree is clean. Git tracks 6371 files. Rukie stops being Gitea. It becomes a Go forge whose only repository backend is Ruk. Ruk lives at `/home/alpha/rewrite/git-renewed/ruk`. Ruk is Rust on branch `main` with a clean tree. Neither project has a release. Both repos may break any surface.

The new rukie serves many AI agents and the humans who supervise them. Every page renders baseline HTML from Go, so it reads and mutates without JavaScript; a ReScript + Preact application embedded in the binary enhances it in the browser (O41). One operation table serves humans as HTML and agents as JSON, Markdown, and MCP tools. A shell CLI comes from the same table. Every repository mirrors automatically to GitHub, GitLab, and Forgejo. Review, tasks, and context sharing use four nouns: Intent, Proof, View, Lease. They replace issues, pull requests, and kanban boards. Built-in CI must be more durable and more efficient than GitHub or GitLab CI.

This file is the execution spec. The planning corpus holds the field-level designs. Step 0 copies the corpus into the rukie repo. Where this file and a corpus file disagree, this file wins. Where two corpus files disagree, the `Canonical sources` table decides, and the `Overrides` table fixes every contradiction the 2026-10-20 review found.

## Settled decisions

The user confirmed these decisions. Do not reopen them during execution.

| Topic | Decision |
|---|---|
| Gitea surfaces | Cut all of them: API v1 and swagger, `app.ini`, the xorm schema and its migrations, Actions, package registries, the OAuth2 provider, Gitea webhook formats, the builtin SSH code, the `gitea` CLI, the Vue, Vite, and Tailwind frontend, all 28 locales, and the Docker, snap, contrib, and `.github` layout. Keep only the `/{owner}/{repo}.git` clone URL shape. |
| History | rukie commits land on top of the Gitea history. ruk commits land on top of ruk `main`. Every commit builds. The Go module path is `github.com/gosuda/rukie`. |
| Metadata store | SQLite through `modernc.org/sqlite` v1.60.1. One engine, no engine seam. |
| Architecture | A typed-client monolith. One static binary `rukie` runs as `serve`, as `runner`, and as the client CLI. A separate `ruk serve` daemon process owns the repositories over a Unix socket. Rukie supervises it. Rukie reads Ruk events through `events.tail`. Rukie writes a SQLite outbox in the same transaction as each domain write. |
| Ruk principal model | Ruk authenticates one OS peer through `SO_PEERCRED`. Rukie enforces per-user and per-token authorization. |
| Collaboration model | An Intent is a task object with an acceptance contract and links to zero or more Ruk changes. A Proof is a Ruk attestation. Proof kinds: `ci/<job>/<spec_digest>`, `review/approve`, `review/block`, `model/review`, `task/acceptance`. A View is a named saved query. A Lease is an intent-keyed Ruk claim over the declared paths in Enforce or Advise mode. Landing policy is data on each protected bookmark. The default policy requires one trusted human approval and passing CI. |
| Git wire | Smart HTTP and SSH. Ruk pack code serves both through the daemon. Go code never parses pkt-lines or packs. |
| Auth | Humans sign in with an argon2id password plus TOTP, or through OIDC. Agents use scoped tokens on human accounts. A token with `repo:create` may create and fork repositories into its owner's user namespace. There is no email. The inbox and webhooks carry notifications. The admin CLI resets passwords. |
| Organizations and teams | Orgs own namespaces. Org membership is membership in at least one team. Teams grant repo roles. |
| Mirrors | Each mirror has a host kind (`github`, `gitlab`, `forgejo`, `generic`), a mode, and a ref selection. Mode `exact` forces updates and prunes deletions. Mode `fast-forward-only` never forces and never prunes. Refs select `all`, the default branch plus tags, or a glob list. Sync runs after each ref update, debounced, and on a periodic reconcile. |
| CI | Built in. The queue uses leases, heartbeats, and fencing tokens. A content-addressed cache skips repeated work. Results land as signed Ruk attestations. Rootless podman sandboxes the jobs. Logs and artifacts are kept. A `matrix` table in `ci.toml` expands jobs. |
| Code search | A Ruk `search` method runs a bounded parallel scan and keeps no index. Rukie fans out per repository with a composite cursor. |
| Design | The Instrument direction: a Fluent-style workbench with layered shadows and a status palette mapped to Ruk states. High density. Webfonts load from Bunny Fonts. The UI language is ReScript on Preact; no-JS support is a functional fallback, enforced by test (O41). |
| Review unit | Ruk changes and stacks. Stock git pushes to `refs/for/<branch>` create changes. A branch view groups the changes of one branch. Landing N changes runs N serial land plans, never one batch. |
| Build order | Walking skeleton, review and land, mirrors, every feature, then dogfood on the rukie repo itself (O40). |
| Render validity | html-validate checks every fallback-rendered page through `npm exec`. Node builds the web app and is a build prerequisite. |

### Grounded set

A release channel verified each pin on 2026-10-20 (`docs/plan/review-pins.md`).

| Choice | Pin |
|---|---|
| Go | `go 1.27.0`, `toolchain go1.27.2` |
| Rust for ruk | 1.99.1 (`rust-toolchain.toml`) |
| Fallback views | `github.com/a-h/templ` v0.3.1070, a `tool` directive in `go.mod`; renders baseline HTML only (O41) |
| Web app | `rescript` 12.3.1, `preact` 11.0.1, `esbuild` 0.28.2; no TypeScript, no `@rescript/react`, no React (O41). Node 24 LTS is a build prerequisite |
| SQLite | `modernc.org/sqlite` v1.60.1 (bundles SQLite 3.53.4, FTS5 with trigram). Keep `modernc.org/libc` at v1.77.1, the version sqlite v1.60.1 requires. Never `go get -u modernc.org/libc`. |
| HTTP and OpenAPI | `github.com/danielgtaylor/huma/v2` v2.39.1 |
| MCP | `github.com/modelcontextprotocol/go-sdk` v1.8.0 |
| Markdown | `github.com/yuin/goldmark` v1.8.6, GFM, no raw HTML. v2.1.6 exists; stay on v1 per `rendering-design.md` decision 4. |
| Highlighting | `github.com/alecthomas/chroma/v2` v2.27.0, CSS classes |
| OIDC | `github.com/coreos/go-oidc/v3` v3.21.0, `golang.org/x/oauth2` v0.37.0 |
| Crypto and SSH | `golang.org/x/crypto` v0.58.0 |
| Diff parsing | `github.com/sourcegraph/go-diff` v0.9.0, `diff.ParseHunks` |
| TOML | `github.com/BurntSushi/toml` v1.6.0 |
| Lint | `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`. No `tool` directive: the golangci-lint docs discourage it. |
| Vulnerabilities | `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0` |
| HTML validity | `html-validate@11.16.4` through `npm exec`, Node 24.8.0 or later on the gate host |
| Rust test runner | `cargo-nextest` 0.9.148 |
| CI sandbox | podman 6.1.4 or later, rootless, cgroups v2 |
| Webfonts | Bunny Fonts: Space Grotesk 500, 600, 700; Source Sans 3 400, 600; IBM Plex Mono 400, 500, 600 |

Dropped: bluemonday, go-gitdiff, xorm, chi, kin-openapi, Vue, Vite, Tailwind, ESLint, Playwright, pnpm, the uv toolchain, Crowdin, the npm Renovate config, React, Next.js, and `@rescript/react` (O41). npm is in scope for `web/` only.

## Canonical sources

Each concern has one source. Corpus paths are relative to `rukie/docs/plan/` after Step 0.

| Concern | Source |
|---|---|
| Ruk changes, ids, files, schema impact | `ruk-changeset.md` §2 rows R01 to R48, decisions C1 to C19; corrected by O3 to O9 |
| rukie step order and ids | `phase-sequencing.md` §2 to §6; corrected by O1 and O37 |
| Green definition and pairing rule | `phase-sequencing.md` §11 and §12; corrected by O10 and O33 |
| Ruk client | `ruk-client-design.md` revision 2 |
| Store | `store-engine-design.md` |
| Operation table, errors, exit codes, ETags, idempotency, negotiation | `op-table-design.md` |
| Operation names and per-op fields | `usage-surface-draft.md` §3, plus the F2 name list in this file |
| Routes | `url-information-architecture.md` |
| Rendering, CSP, render budgets | `rendering-design.md`, stack corrected by O41 and `ui-rescript-design.md` |
| Web app architecture | `ui-rescript-design.md` (O41) |
| Tokens and visual language | `design-direction-instrument.md` |
| UI inventory and accessibility pass set | `autoplan-design.md` DES-03, DES-09, DES-11 to DES-21 |
| Auth, sessions, tokens, scopes | `auth-design.md` |
| Authorization | `permission-model.md` |
| Git wire | `ssh-smarthttp-design.md` |
| Mirrors | `mirror-design.md` |
| CI | `ci-control-plane-design.md`, `ci-runner-sandbox-design.md` |
| Webhooks, inbox | `webhook-design.md`, `inbox-design.md` |
| Review and task data | `review-task-data-model.md`, `abstraction-intent-proof-view-lease.md` |
| Forks, social, releases | `fork-social-releases-design.md` |
| Search | `ruk-search-design.md` |
| Operations, config, backup | `ops-design.md` |
| Gate commands and test names | `verification-strategy.md`; package paths corrected by O2 |
| README and docs tree | `docs-readme-draft.md` |
| Instruction files | `agents-md-draft.md` |

The remaining corpus files (ideation, arch sketches, the three design directions, research notes, review reports) are history. Do not build from them.

## Overrides

Each row fixes one contradiction that the 2026-10-20 review found. The row value wins over every corpus file.

| Id | Concern | Decided value | Conflict it settles |
|---|---|---|---|
| O1 | Step ids | Ruk work uses the row ids of `ruk-changeset.md` §2, written `CS-R01` to `CS-R48`. rukie work uses the ids and order of `phase-sequencing.md`: `K1` to `K10`, `F1` to `F14`, `U*`, `V*`, `D*`. `phase-sequencing.md` R ids (R1 to R20) are not used; the plan maps each to CS rows. Ids inside other corpus files are local to that file. | Plan R1-R13/K1-K6/F1-F10 collided with corpus ids (CEO, Eng, Design reviews). |
| O2 | Ruk client | `internal/ruk` per `ruk-client-design.md` revision 2: hand-written payloads, closed sets generated by `internal/ruk/internal/rukgen`, a drift gate, `internal/ruk/internal/fakedaemon` for client unit tests only. Not the byte-copy port into `internal/rukclient` (`phase-sequencing.md` K2, `verification-strategy.md` §4). Gate tests keep their names; their package path becomes `./internal/ruk/...`. | phase-sequencing K2 vs ruk-client-design. |
| O3 | Push and mirror credentials | `TransportAuth` on `InvocationOpen` (CS-R29) and the per-call `vars` map (CS-R30). Never in argv, env, args, digests, receipts, or logs. CS-R07 deletes `CommandOpen.env`. | Plan R11 `GIT_AUTH_*` env. |
| O4 | Ref feed | `events.tail` only (CS-R20: `after`, `limit` at most 1000, `wait_ms` at most 30000, `prefix`). No `heads-since`, no `op.feed`, no R-REFJOURNAL (C1). | Plan R5. |
| O5 | Receive-pack writes | Direct `Store::publish` under `lock_writer` (C15). No lane operation for pushes. `apply_refs` (`ruk-git/src/transport/session.rs:778-833`) passes `EventSource::ReceivePack`. | Plan R10. |
| O6 | Landing today | `land.prepare` is served in restricted form (`ruk-core/src/coord/prepare.rs:3447-3510`): extend it. `land.execute` is refused at `prepare.rs:296-301`: implement it (CS-R43, CS-R44). | Plan R10 claim. |
| O7 | Method count | 47 today (`protocol.rs:3286-3332`, equal to the `required` array). CS-R11 appends 14, so the count becomes 61. C17's "60" is wrong. README says 46 (`README.md:5`, `:64`). | C17, README. |
| O8 | Repo method kinds | `repo.init`, `repo.delete`, `repo.rename`, `repo.fork` are `MethodKind::Admin`. `repo.stat` is `MethodKind::Read`. `MethodKind` gains the `Admin` variant in CS-R11. | Plan R7. |
| O9 | Forward dependencies in the changeset | CS-R13 creates `resolve_search_root` in `coord/hosted.rs` with the signature at `ruk-search-design.md:76`; CS-R18 reuses it. CS-R29 lands before CS-R22. CS-R07 deletes `check_env_path_under_roots`; CS-R47 applies `openat2` containment to the new git-service path. | Changeset forward refs. |
| O10 | Ruk gates | `cargo fmt --all -- --check`; `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`; `cargo nextest run --workspace --locked` (nextest 0.9.148); `cargo test --workspace --locked --doc`. The first Ruk commit fixes every pre-existing clippy failure. | Plan R13 weaker gate. |
| O11 | Op names | `usage-surface-draft.md` §3 is the op-name source: dotted, lowercase, verb last (`repo.fork.create`, `team.member.add`, `team.repo.add`). New rows for Intent, Ask, View, Land, and change reads are fixed in the plan's op list. MCP names replace dots with underscores; a startup check rejects collisions. | Plan F1/F2/F3 names. |
| O12 | MCP endpoint | `/-/mcp`, POST only (stateless; GET and DELETE return 405). The token verifier returns `auth.TokenInfo` with `Scopes` and a non-zero `Expiration` and wraps every credential failure in `auth.ErrInvalidToken`. Auth failures on `/-/mcp` keep the SDK `text/plain` body and `WWW-Authenticate` header: the one exception to RFC 9457. | op-table §12.5 `/mcp`, usage-surface §1.2 `/api/mcp`. |
| O13 | MCP tool budget | The default tool set has at most 36 tools. Other toolsets load only through an explicit `toolsets` selection. A gate test counts default tools. | Register vs op-table §12.6. |
| O14 | Errors and exit codes | `op-table-design.md` §4 is the one catalogue (problem type, HTTP status, exit code, retryability, Ruk code). Add exit 78 `config-unavailable` (runner sandbox missing) to §4.4. | usage-surface §1.9, url-IA. |
| O15 | Idempotency | `Idempotency-Key` rows live 24 h (op-table §7). HTML `_idem` forwards as the Ruk `request_id`. | usage-surface §1.8 7 days. |
| O16 | ETags | Strong, quoted (op-table §8). `If-Match` compares inside the store transaction. | Plan K4 "weak". |
| O17 | Content negotiation | Explicit q-values decide. An absent `Accept` or a bare `*/*` yields JSON. Browsers list `text/html` explicitly and get HTML. Ties at equal q: JSON, then Markdown, then HTML (op-table §9.1). | url-IA rule 1 HTML default. |
| O18 | CLI sessions | Session-only ops (org, team, token minting, inbox settings, org repo creation) run from the CLI through a session file. `rukie auth login` writes the session cookie to `--session-file` (default `$XDG_STATE_HOME/rukie/session`, mode 0600). Every later command sends it. Bearer tokens use `--token-file`. | op-table §13 bearer only. |
| O19 | Scopes | `auth-design.md` §8.2 is the only scope vocabulary, with two changes. Rename `tasks:read` and `tasks:write` to `intents:read` and `intents:write`. Add `repo:create` (user answer 2026-10-20): a token with it may call `repo.create` and `repo.fork.create` with the token owner's user namespace as the destination; any other destination returns 403. Org repo creation stays session-only. Delete `PrincipalAgent` and `OnBehalf` from op-table §3.5: agents are tokens on human accounts. | autoplan-dx §2.4, op-table §3.5, usage-surface §3.4. |
| O20 | Search pagination | Search pages pass the Ruk `PageCursor` with its 30 s lease. They emit no `Link` header. An expired lease returns the catalogue's lease-lost problem; the client restarts from page one. | op-table §8 vs usage-surface §1.6. |
| O21 | Argon2id | `auth-design.md` §3.1: m=19456 KiB, t=2, p=1. Ignore `research-go-stack.md` §5 numbers. | research-go-stack §5. |
| O22 | Markdown | goldmark v1.8.6 (`rendering-design.md` decision 4). v2.1.6 exists; do not adopt it. | research-go-stack §5. |
| O23 | Tokens and motion | `design-direction-instrument.md` governs tokens. Spacing `{4,8,12,16,24,32,48}`. Radius `{4,8,12,16}`. Transitions only on `background-color`, `color`, `box-shadow`, `opacity`, `outline-color`, `transform`; at most 150 ms; easing `cubic-bezier(0.22,1,0.36,1)`; only under `prefers-reduced-motion: no-preference`. No keyframes on first paint. One CSS-only CI spinner. The instrument doc's 200 ms tier becomes 150 ms. | Plan K5 spacing 64; instrument 200 ms. |
| O24 | Fonts | Webfonts from Bunny Fonts (user answer 2026-10-20). The page layout emits one `<link rel="stylesheet" href="https://fonts.bunny.net/css?family=space-grotesk:500,600,700%7Csource-sans-3:400,600%7Cibm-plex-mono:400,500,600&display=swap" referrerpolicy="no-referrer">`. The CSP adds `https://fonts.bunny.net` to `style-src` and sets `font-src https://fonts.bunny.net`. Every `font-family` keeps the instrument fallback stack, so a blocked CDN degrades to local fonts. There is no config switch. Overrides rendering-design §4 and DES-08. | instrument (self-host) vs rendering-design §4 and DES-08 (system stacks). |
| O25 | Design precedence | `design-direction-instrument.md` and `url-information-architecture.md` override `autoplan-design.md` DES-01, DES-02, DES-06, DES-08. DES-03, DES-09, DES-11 to DES-21 apply. `design-direction-instrument.html` is illustrative only. | Design review 6, 7. |
| O26 | No-JS fallback contract | Superseded by O41. The fallback gates are `TestAllHTMLRoutesRenderWithoutJS` and `TestNoJSFallbackMutationsPost`: every HTML route carries rendered content and a working form POST without JavaScript. `zerojs.Assert` (the route-level no-script wrapper) is deleted. `trusted` and `htmlcheck` stay as the untrusted-content boundary: Markdown, highlights, and user content keep rejecting `script`, `on*`, `javascript:`, `vbscript:`, and non-allowlisted `data:` URLs; the CodeQL URL-scheme gap at `htmlcheck` is fixed in the same change. | Plan K5 wording. |
| O27 | Accessibility | WCAG 2.2 AA. Where a requirement is AAA-only (2.4.13), verify 2.4.7 and 1.4.11. DES-21 is the pass set. | Register sentence. |
| O28 | Meta refresh | Allowed only on declared live surfaces, at least 15 s, user-toggleable, suppressed on pages with an editable form (DES-03, DES-16). | Open question. |
| O29 | Line highlight | `?lines=` (url-IA, rendering-design). | autoplan-design `?hl=`. |
| O30 | Performance budgets | Render: per-route cold budgets in `rendering-design.md` §12.1, measured per §12.2. End to end: `verification-strategy.md` §7 (HTML at most 200 ms, JSON at most 100 ms, `info/refs` at most 150 ms, search at most 500 ms, p95 nearest-rank, at least 1000 samples after 100 warmups, `RUK_BIN=ruk/target/release/ruk`). Daemon path: CEO-08 (incremental fetch at most 7.5x and single-ref lookup at most 4x stock git on the same host). The old plan numbers are deleted. | Three tables. |
| O31 | Org membership | Membership in at least one team. The `owners` team has `admin` on all org repos. `team.member.add` implies org membership. `team.repo.add` and `team.repo.remove` grant roles. There is no `org.member.add`. | Plan F1. |
| O32 | Ledger | One tracked copy at `rukie/tasks/autoplan-ledger.jsonl`. Append-only. Keys `id`, `description`, `phase`. No status field: the plan register records resolved rows. | Step 0.2. |
| O33 | Go tools | `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...` and `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`. No `tool` directive for either. templ is a `tool` directive, added in the first templ commit. `github.com/BurntSushi/toml` v1.6.0. Keep `modernc.org/libc` at the version `modernc.org/sqlite` v1.60.1 requires (v1.77.1); never `go get -u` it. | Pins review. |
| O34 | Dogfood policy | Dogfood placement superseded by O40 (it runs after F14). Its bookmark policy requires one trusted human approval and passing `ci/*` proofs (F5 landed before dogfood). | Plan dogfood text. |
| O35 | Supervisor | Restart the `ruk serve` child at most 3 times with backoff 1 s, 2 s, 4 s, then exit non-zero. `/readyz` is false while the child is down. Daemon-backed requests fail fast with 503. Git-service streams never re-run; an unknown outcome reconciles through `Lookup`, then `events.tail` tip comparison (ops-design §2, ruk-client-design §11). | Plan K1. |
| O36 | Secret key | `rukie admin secret-gen` writes the key file (`/etc/rukie/secret.key`, outside `data_dir`). `rukie backup` never copies it and records its key id in the manifest. `rukie restore` refuses when the configured key id differs from the manifest. The operator guide tells operators to back up the key offline. | Eng review 5. |
| O37 | Build order | Superseded by O40 (user answer 2026-10-21). | Plan F order. |
| O40 | Build order | Dogfood last (user answer 2026-10-21): K1 to K10, F1 to F14 in `phase-sequencing.md` order, then the Dogfood gate, then the D block. Supersedes O37 and the F3-era entry condition; CI is already in the policy when dogfood starts. | O37 dogfood-early order. |
| O38 | Render validity gate | Keep html-validate (user answer 2026-10-20) against the fallback-rendered pages: `npm exec --yes --package=html-validate@11.16.4 -- html-validate --config .htmlvalidate.json --max-warnings 0 .rendered/*.html`. Node 24 LTS is a build prerequisite. `web/package.json` and its lockfile are committed (O41); the repo root holds no `package.json`. | Old stacks removal. |
| O39 | Op lexicon additions | Domains: the `usage-surface-draft.md` §1.1 list plus `change`, `intent`, `ask`, `view`, `land`, `proof`, and `ci`. No `task.*` op registers. Verbs stay in the §1.1 closed set, so the CI ops of `ci-control-plane-design.md` §11 rename: `ci.run.cancel` becomes `ci.run.update` with `state: cancelled`, `ci.log.read` becomes `ci.log.get`, `ci.artifact.read` becomes `ci.artifact.get`, `ci.runner.register` becomes `ci.runner.create`, `ci.spec.validate` becomes `ci.spec.verify`. | ci-control-plane §11 verbs. |
| O41 | UI stack | All UI application code is ReScript: no TypeScript, no hand-written JavaScript, no React, no Next.js. The component model is Preact 11.0.1 through the ReScript generic JSX transform (`rescript.json` `jsx.module = "Preact"`) with a local `Preact.res` interface externalling `preact/jsx-runtime`; hooks bind `preact/hooks` directly. `esbuild` 0.28.2 bundles `.res.js` output to ES modules, embedded in the `rukie` binary with `go:embed`. Go keeps rendering baseline HTML through templ: that layer is the no-JS fallback, pruned to it, and the ReScript app hydrates it. Every mutation keeps a working `<form action>` POST. The CSP gains `script-src 'self'` for the embedded bundle only. Zero-JS script rejection is deleted (O26). `web/` holds `rescript.json` and `package.json` with a committed lockfile; Node 24 LTS is a build prerequisite. Pins grounded at npm dist-tags 2026-10-21: `rescript` 12.3.1, `preact` 11.0.1, `esbuild` 0.28.2. | Settled "Every page renders without JavaScript", O26, O38 package.json ban, templ-as-whole-UI. |

## Step 0: land the corpus, then cut the pad

The corpus directory is `C=/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local`. Approval of this plan approves the three commits below exactly.

PRE: `git -C rukie status --porcelain` prints nothing. `git -C rukie rev-parse --short=10 HEAD` prints `ba4f8fa246`. `C` exists. If `C` is missing, stop and report. Do not rebuild the corpus from memory.

1. Corpus commit. Copy every file in `C` into `rukie/docs/plan/` except `autoplan-ledger.jsonl` and the three assembly drafts `plan-frame-draft.md`, `plan-overrides-draft.md`, and `plan-approach-draft.md`, which this plan supersedes. Copy `C/autoplan-ledger.jsonl` to `rukie/tasks/autoplan-ledger.jsonl`. Commit with subject `Land the rewrite planning corpus`. POST: `git -C rukie ls-files docs/plan | wc -l` equals `ls "$C" | wc -l` minus 4. `git -C rukie ls-files tasks` prints `tasks/autoplan-ledger.jsonl`. `git -C rukie rev-parse --short=10 HEAD~1` prints `ba4f8fa246`.
   Anchor: Christopher Alexander
2. Pad commit. The delete set is every path in `git -C rukie ls-tree -r --name-only ba4f8fa246` except these eight kept paths: `LICENSE`, `README.md`, `AGENTS.md`, `.gitignore`, `.gitattributes`, `go.mod`, `.golangci.yml`, `Makefile`. The delete set has 6363 paths. It includes `go.sum`, because the pad module has no dependencies; the first step that adds a dependency creates `go.sum` again. Stage the deletion with this command, run from `/home/alpha/rewrite/git-renewed`:
   Anchor: Dieter Rams

   ```bash
   git -C rukie ls-tree -r -z --name-only ba4f8fa246 \
     | grep -zvxE 'LICENSE|README\.md|AGENTS\.md|\.gitignore|\.gitattributes|go\.mod|\.golangci\.yml|Makefile' \
     | xargs -0 git -C rukie --literal-pathspecs rm --quiet --
   ```

   Then check `git -C rukie diff --cached --name-only --diff-filter=D | wc -l` prints 6363. Keep `LICENSE` unchanged. Write the other seven kept paths and three new files with this exact content or source:

   - `go.mod`: `module github.com/gosuda/rukie`, a blank line, `go 1.27.0`, a blank line, `toolchain go1.27.2`.
   - `cmd/rukie/main.go`: package `main`; `main` calls `fmt.Println("rukie")`.
   - `tools/passgate.sh`: `#!/usr/bin/env bash`, then `set -euo pipefail`, then the `passgate` function from `docs/plan/verification-strategy.md` §0 verbatim, then `passgate "$@"`. Mode 0755.
   - `README.md`: the replacement README block from `docs/plan/docs-readme-draft.md`. Keep every `{{name}}` placeholder. D1 fills them.
   - `AGENTS.md`: section 1 of `docs/plan/agents-md-draft.md` with its spine tags. Replace each bare tool command in it with the Makefile target that runs it: `make fmt`, `make vet`, `make lint`, `make test`, `make vuln`, `make check`.
   - `.gitignore`: four lines: `/rukie`, `/.rendered/`, `*.test`, `*.out`.
   - `.gitattributes`: one line: `* text=auto eol=lf`.
   - `.golangci.yml`: exactly this YAML. It has no depguard, gci, or revive rules.

     ```yaml
     version: "2"
     run:
       build-tags: [integration, e2e, perf, render]
     linters:
       default: standard
     formatters:
       enable: [gofmt, goimports]
     ```
   - `.htmlvalidate.json`: `{ "extends": ["html-validate:recommended"] }`.
   - `Makefile`: phony targets `fmt`, `vet`, `lint`, `test`, `vuln`, `check`. `fmt` runs `gofmt -w .`. `vet` runs `go vet ./...` and `go vet -tags integration,e2e,perf,render ./...`. `lint` runs the Lint pin with `run ./...`. `test` runs `tools/passgate.sh '--- PASS: ' -- go test -race -count=1 -v ./...`. `vuln` runs the Vulnerabilities pin with `./...`. `check` runs `vet`, `lint`, `test`, and `vuln`, then `test -z "$(gofmt -l .)"`, `go mod tidy -diff`, and `go mod verify`.

   Commit with subject `Clear the Gitea pad and stand up the rukie module`. The body ends with `BREAKING CHANGE: rukie is no longer Gitea. Every Gitea surface is removed.`

   POST: `git -C rukie ls-files ':!docs/plan' ':!tasks' | sort` prints exactly these 11 paths: `.gitattributes`, `.gitignore`, `.golangci.yml`, `.htmlvalidate.json`, `AGENTS.md`, `LICENSE`, `Makefile`, `README.md`, `cmd/rukie/main.go`, `go.mod`, `tools/passgate.sh`. `git -C rukie diff --name-only --diff-filter=D HEAD~1 HEAD | wc -l` prints 6363. In `rukie`: `go build ./...`, `make vet`, `make lint`, `make vuln`, `test -z "$(gofmt -l .)"`, `go mod tidy -diff`, and `go mod verify` exit 0. `make test` starts at K2, the first commit with tests; the pad has none.

3. Strict validation commit. Apply strict-validation-setup to rukie, whose only manifest after the pad is `go.mod`. The task id is `task-0e2cc1171807`: `task-` plus the first 12 hex digits of SHA-256 over the user's goal text below.
   Anchor: Tony Hoare

   - Replace `.golangci.yml` with exactly this YAML. It keeps the pad keys and adds the strict Go keys. The `comments` exclusion preset keeps the rule that internal code carries no comments; rukie publishes no Go package. `config verify` and `run` pass on the pad tree with golangci-lint v2.15.0.

     ```yaml
     version: "2"
     run:
       timeout: 5m
       go: "1.27"
       build-tags: [integration, e2e, perf, render]
     linters:
       default: standard
       enable: [errcheck, errorlint, exhaustive, gocritic, gosec, govet, ineffassign, revive, staticcheck, unused, wastedassign]
       settings:
         errorlint: {errorf: true, asserts: true, comparison: true}
         exhaustive: {default-signifies-exhaustive: false}
         govet: {enable-all: true}
         staticcheck: {checks: ["all"]}
       exclusions:
         presets: [comments]
     formatters:
       enable: [gofmt, goimports]
     ```

   - Append one line to `AGENTS.md`: `Per-task goals live in .agent-tasks/<task-id>/GOALS.md.` Write no task criteria into `AGENTS.md`.
   - Write `.agent-tasks/task-0e2cc1171807/GOALS.md` in the strict-validation-setup layout. The Goal section holds this text verbatim: "Make rukie @rukie/ diverge completely from gitea, uses only ruk as backend and highly optimized for Heavy Agentic Engineering. Also better designs and remove all the old stacks, and no-js UI, and highly optimize Go codebase, and most importantly best interfaces for agents and humans for Agentic-first Programming.". The criteria are:
     - SC-01: In rukie, `git grep -niI gitea -- ':!LICENSE' ':!docs/plan' ':!tasks' ':!.agent-tasks'` prints no line.
     - SC-02: `go list -deps ./...` lists no package path that contains `gitea`, `go-git`, or `git2go`, and `git grep -n 'exec.Command("git"' -- '*.go' ':!*_test.go'` prints no line.
     - SC-03: outside `web/`, `git ls-files` lists no `package.json`, `pnpm-lock.yaml`, `tsconfig.json`, `pyproject.toml`, `uv.lock`, or `flake.nix`, and no path that ends in `.ts`, `.tsx`, `.js`, `.mjs`, `.vue`, or `.py`. Inside `web/`, every application source path ends in `.res` or `.resi` (O41): no `.ts`, no hand-written `.js`/`.mjs`; `.res.js` output is a build artifact, not tracked.
     - SC-04: `TestAllHTMLRoutesRenderWithoutJS` and `TestNoJSFallbackMutationsPost` pass (O41), `rescript build` compiles `web/` clean, `esbuild` produces the embedded bundle, and html-validate reports zero errors and zero warnings on every fallback page that `TestDumpRenderedPages` writes.
     - SC-05: `TestNegotiationMatrix` passes (one URL returns HTML, JSON, and Markdown by `Accept`), and `TestMCPDefaultToolBudget` passes (`/-/mcp` lists at most 36 default tools, all derived from the operation table).
     - SC-06: every `TestAgentFlow` subtest passes against the real daemon.
     - SC-07: `TestPerfBudget` passes on the release binaries against the O30 budgets.
     The Verification section maps each criterion to its test file and to `go test -count=1 -run '^TestSCNN$' ./.agent-tasks/task-0e2cc1171807/tests` with `NN` set.
   - Write `.agent-tasks/task-0e2cc1171807/tests/sc_01_test.go` to `sc_07_test.go`, package `tests`. Each holds `func TestSCNN(t *testing.T) { t.Fatal("SC-NN unmet: <criterion>") }` with its number and criterion text. `go test ./...` skips dot directories, so `make check` stays green.
   - Add the Makefile target `goals`: `go test -count=1 -v ./.agent-tasks/task-0e2cc1171807/tests`. `check` does not run it. D7 makes it pass.

   Commit with subject `Set up strict validation and the task goals`. POST: `make lint` exits 0. `make goals` exits 1 and prints seven lines that start `SC-0` and contain `unmet`. `grep -c 'Per-task goals live in' AGENTS.md` prints 1.

   ruk gets no strict-validation write. The skill stops there with `config-conflict` (`[workspace.lints.rust] unsafe_code` is `deny`; the strict value `forbid` breaks the FFI site in `crates/ruk-core/src/physical.rs`) and with `task-test-location-unsupported` (cargo compiles no tests from `.agent-tasks/`).

## Approach

Ruk work uses the row ids CS-R01 to CS-R48 of `docs/plan/ruk-changeset.md` §2, and rukie work uses K1 to K10, F1 to F14, U, V, and D from `docs/plan/phase-sequencing.md`; the old plan ids R1 to R13 and the phase-sequencing ids R1 to R20 are not used (O1). Every commit in either repo keeps both repos green (`phase-sequencing.md` §12): the Ruk gates of O10 pass, and the rukie gates G1a to G5 and the tagged suites pass against the pinned Ruk commit in `tools/ruk.pin`. A CS row with schema impact `minor bump` or `wire frame change` touches the method table or the IPC schema, so it pairs with a rukie commit that bumps `tools/ruk.pin`, regenerates `internal/ruk/testdata/ruk-model.json`, and reruns `TestModelGolden` and `TestRukgenDrift` (§11). Ruk rows land just before the first rukie step that needs them, in the landing order that follows the cluster blocks. The build order is K1 to K10, F1 to F14, the Dogfood gate, then the D block (O40). Each F step ships its views and its gate, U and V items sit in the step that ships them, and the D items that need a finished product sit in the final D block. Op names come from `usage-surface-draft.md` §3 (O11), routes from `url-information-architecture.md` §4, errors from `op-table-design.md` §4 (O14), and scopes from `auth-design.md` §8.2 as changed by O19.

### Ruk schedule

One block per cluster of `ruk-changeset.md` §2. Each block lists its rows in landing order. The `lands before` column names the first rukie step that needs the row. The nine tables hold all 48 rows once. The landing order across clusters follows the block list after the tables.

Ruk cluster A: Forge-neutral daemon foundation.
   Anchor: J.R.R. Tolkien

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R01 | Rename `ruk forgejo serve` to `ruk serve`. Purge Forgejo residue and `forgejo-patch-inventory`. Add `rukie/` to `.gitignore`. Mark `AGENT_NATIVE_GIT_PLAN.md:92-96` superseded. First Ruk commit also fixes every pre-existing clippy failure. Ruk test: `forgejo_subcommand_is_rejected`. | K2 | O1, O7, O10; eng-ruk 8, 14 |
| CS-R02 | Add `DaemonConfig` keys with `deny_unknown_fields`: `admin_uids`, `transport_permits`, `hook_timeout_ms`, `hook_max_commands`, `default_branch`, `event_retention_seconds`, `archive_max_bytes`, `compare_max_commits`, `search_max_scan_bytes`, `search_pool_size`. | K2 | none |
| CS-R03 | Hosted mode refuses an empty `allowed_uids`. One OS peer (`SO_PEERCRED`) is the principal. Ruk test: `empty_allowed_uids_refuses_start`. | K2 | none |
| CS-R04 | IPC surface: profile `host` (echo negotiation), features `credits`, `hooks.v1`, `events.v1`, `archive.v1`. Retire kind 9 `MergeNotices`. Add kinds 12 `HookCall` and 13 `HookReply`. | K2 | eng-ruk 19 |

Ruk cluster B: Concurrency, streaming, and the git-service envelope.
   Anchor: David Parnas

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R05 | One task per stream plus one writer task owning the socket write half. `Cancel` reaches running invocations. | K6 | none |
| CS-R06 | Typed replies wait for credit. Use the existing `ByteSource::Streamed`. Align `MAX_IPC_CONTROL_BYTES` (1 MiB) with the 16 MiB schema limit. | K6 | eng-ruk 13 |
| CS-R07 | `CommandOpen` becomes the git-service envelope: `service`, `repository`, `session_id`, `request_id`, `git_protocol`, `advertise`, `stdout_credit`, `limits`. Delete the argv path, `CommandOpen.env`, `OutcomeLedger`, outcome-lookup, and `check_env_path_under_roots`. `advertise` writes the ref advertisement and ends. A stateless request reads one round and writes one response. Gates: `git_service_advertise_matches_oracle_refs`, `git_service_request_matches_oracle_objects`, `git_service_unknown_service_refused`. | K6 | O3, O9; eng-ruk 4, 5, 7 |

Ruk cluster C: Store foundation.
   Anchor: Christopher Alexander

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R08 | Store census revision: append `Events`, `EventHead`, `ServiceReceipts`, `RepoMeta`, `ReleasedTags` in one revision. | K6 | none |
| CS-R09 | `Store::publish` emits one event per `FactTable::Refs` mutation with an `EventSource`. Update `apply_refs` (`session.rs:778-833`) to pass `EventSource::ReceivePack` and update every other caller in the same commit. Add the retention sweep. | K6 | O5; eng-ruk 11 |
| CS-R10 | Durable `ServiceReceipts` replace `OutcomeLedger`. Key `(peer uid, canonical repository, request_id)`. Compute the digest daemon side over parsed commands and limits. | K6 | eng-ruk 6 |

Ruk cluster D: Protocol minor 2 and methods.
   Anchor: Claude Shannon

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R12 | Shared types: `CommitRev`, `KeysetPage<T>` (new), `RefTarget` renames, `ByteString`. Lands before CS-R11 because the R11 table rows name these types. | K7 | eng-ruk 16 |
| CS-R11 | The one `SCHEMA_MINOR` 1 to 2 commit. Add `MethodKind::Admin` and 14 table rows: `repo.stat`, `tree`, `refs.list`, `compare`, `search`, `archive`, `events.tail`, `repo.init`, `repo.delete`, `repo.rename`, `repo.fork`, `remote.refs`, `claim.renew`, `policy.set`. Update the `required` array, count 61, and the schema snapshot. Each unserved arm refuses with `E_HOSTED` until its own row serves it. | K7 | O7, O8; eng-ruk 15 |
| CS-R16 | `refs.list` with prefix and inclusive-start seek. Reply carries `as_of`, never ahead of the events head. | K7 | none |
| CS-R20 | `events.tail` long-poll: `after`, `limit` (at most 1000), `wait_ms` (at most 30000), `prefix`. It is the only ref feed. | K7 | O4; eng-ruk 1, 17 |
| CS-R13 | Hosted `file`, `log`, `diff`, `show`, `blame`, `query`, and `status`. Create `resolve_search_root` in `coord/hosted.rs` with the signature at `ruk-search-design.md:76`. Update the doc at `daemon/mod.rs:609-613` after the arms land. | F1 | O9; eng-ruk 10, 12, 14 |
| CS-R14 | `repo.stat` served as `MethodKind::Read`. | F1 | O8; eng-ruk 9 |
| CS-R15 | `tree` read with keyset paging. | F1 | none |
| CS-R21 | `op.log` feed: ascending `after` cursor, per-op ref deltas and attestation subjects for lane mutations. It is never the ref feed. | F2 | O4 |
| CS-R17 | `compare` read: merge-base, ahead and behind, bounded by `compare_max_commits`. | F2 | none |
| CS-R22 | `remote.refs` read for exact-mode prune. Credentials arrive only through CS-R29. | F3 | O3, O9 |
| CS-R18 | `search` read with `SearchPool` in `crates/ruk-core/src/search.rs` (new). Reuses `resolve_search_root`. | F9 | O9, O20; eng-ruk 16 |
| CS-R19 | Streamed `archive` read (tar, zip). | F11 | none |

Ruk cluster E: Repo lifecycle.
   Anchor: David Parnas

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R23 | Serve `repo.init`, `repo.delete`, `repo.rename` as `MethodKind::Admin`, gated by `admin_uids` before any coordinator call. | F1 | O8; eng-ruk 9 |
| CS-R25 | Released-tag freeze on all tag mutation paths. | F11 | none |
| CS-R24 | `repo.fork` as `MethodKind::Admin`, per `fork-social-releases-design.md` §2.3. | F13 | O8 |

Ruk cluster F: Receive path and hooks.
   Anchor: Tony Hoare

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R26 | `ReceivePolicy` trait. `ScriptPolicy` keeps CLI output byte-identical. `PROC_RECEIVE_PREFIXES` becomes configurable `["refs/for"]`. | K6 | none |
| CS-R27 | Hosted receive path through `HookCall` and `HookReply`. Receive-pack keeps direct `Store::publish` under `lock_writer`. Fail closed on timeout. Callback runs before any publish and holds no writer lock. | K6 | O5; eng-ruk 2 |

Ruk cluster G: Push and mirrors.
   Anchor: John Carmack

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R29 | `TransportAuth` on `InvocationOpen` with redacting `Debug`. Never in argv, env, args, digests, receipts, or logs. Lands before CS-R22. | F3 | O3, O9 |
| CS-R30 | Credential isolation: helpers and config paths from the per-call `vars` map. `AuthPolicy::{Explicit, Ambient}`. Hermetic daemon. | F3 | O3 |
| CS-R33 | Redaction: refuse userinfo URLs. Scrub errors, stderr, and `push_plans`. | F3 | none |
| CS-R31 | Mirror wire P0: denylist, per-ref fast-forward isolation, `Uncertain` only when bytes may have reached the server. | F3 | none |
| CS-R32 | Push errors: `E_PUSH_REJECTED`, `E_PUSH_UNCERTAIN`, `E_REMOTE_MOVED`. Add `ErrorDetail::Http`. No new `ErrorCode`. | F3 | none |
| CS-R34 | POST 3xx returns `E_REMOTE_MOVED{location}`. No cross-origin redirects on auth-bearing calls. Per-call PEM roots. | F3 | none |
| CS-R35 | Egress dial policy: default-deny private and loopback ranges, including IPv4-mapped IPv6. | F3 | none |
| CS-R28 | Hosted `push` and `push.reconcile` as transport jobs. Explicit-URL remotes only. | F3 | O3 |
| CS-R36 | Push jobs honor cancel. Lease loss cancels. Killed `Sending` rows become `Uncertain`. | F3 | none |
| CS-R37 | Streamed HTTPS push upload with bounded memory. | F3 | none |

Ruk cluster H: Attestations, claims, and landing.
   Anchor: Tony Hoare

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R38 | Attest capability namespace grants (`ci/`, `review/`). | F2 | none |
| CS-R40 | `AttestRequirement.valid_until`. Land gates call `evaluate_bounded_requirements`. | F2 | none |
| CS-R44 | Land requirements, trusted signers, and `policy_generation` reach `land.prepare`. `policy.set` is served. | F2 | O6; eng-ruk 3 |
| CS-R43 | Serve hosted `land.execute`: extend the restricted `land.prepare` (`prepare.rs:3447-3510`) and implement the execute path refused at `prepare.rs:296-301`. | F2 | O6; eng-ruk 3 |
| CS-R39 | Attest payload v2 with an `evidence` digest. v1 stays verifiable. | F5 | none |
| CS-R41 | `attest.list` kind-prefix filter. | F5 | none |
| CS-R42 | Task-keyed claims. Serve `claim.renew`. Wire `claims::sweep_page`. Raise the TTL ceiling. | F7 | none |

Ruk cluster I: Identity, hardening, and errors.
   Anchor: Tony Hoare

| CS row | change | lands before | plan correction |
|---|---|---|---|
| CS-R48 | IPC `Error` frames carry the full `ErrorReply` (hints, fixes, detail). Failed and cancelled outcomes are distinct. | K2 | none |
| CS-R47 | Apply `openat2` (`RESOLVE_BENEATH`, `RESOLVE_NO_SYMLINKS`) to the new git-service repository path and to registry opens. For new destinations, canonicalize the nearest existing ancestor and refuse symlink components below it. | K6 | O9; eng-ruk 5 |
| CS-R45 | Optional `on_behalf_of` on `InvocationOpen` and the envelope. Provenance only; never an authorization input. | K6 | none |
| CS-R46 | `env_clear` plus an allowlist for CLI script hooks. | K6 | none |

Landing order by step. Land the rows of one step in the order shown. Land the steps in the build order.
- K2: CS-R01, CS-R02, CS-R03, CS-R04, CS-R48.
- K6: CS-R05, CS-R06, CS-R07, CS-R47, CS-R45, CS-R08, CS-R09, CS-R10, CS-R26, CS-R46, CS-R27.
- K7: CS-R12, CS-R11, CS-R16, CS-R20.
- F1: CS-R13, CS-R14, CS-R15, CS-R23.
- F2: CS-R21, CS-R17, CS-R38, CS-R40, CS-R44, CS-R43.
- F3: CS-R29, CS-R30, CS-R33, CS-R31, CS-R32, CS-R34, CS-R35, CS-R28, CS-R36, CS-R37, CS-R22.
- F5: CS-R39, CS-R41.
- F7: CS-R42.
- F9: CS-R18.
- F11: CS-R19, CS-R25.
- F13: CS-R24.

`SCHEMA_MINOR` moves from 1 to 2 once, in CS-R11. The method count moves from 47 to 61 (O7). C17's "60" is wrong. `README.md:5` and `:64` say 46; D3 states 61.

Ruk gates for every row are the four O10 commands. Cluster gates of changeset §3 also apply:
- Clusters A, B, F, and G also run `cargo nextest run -p ruk`; cluster B runs the daemon suite twice.
- Every method row updates `method_table_is_consistent_and_covers_the_contract`, the `undo.rs` arm, the `decode_request_args` arm, and the schema snapshot in the same commit.
- Cluster C adds the store-format test.
- Cluster E adds the fork and freeze tests.
- Cluster F adds the §6.7 list.
- Cluster G adds the status matrix, the canary test, the DNS-rebinding stub, and a peak-RSS assertion for CS-R37.
- Cluster H adds the golden vectors.
- Cluster I adds the symlink-swap race and the M8 replay tests.

Rejected and deferred items (changeset §4):
- `op.feed` and `heads-since`: rejected (C1).
- `R-REFJOURNAL`: superseded by `FactTable::Events` (C1).
- `git.receive` through the lane: rejected (C15).
- Script-hook hosted path, `RUK_*` hook-env channel, hook-dir constructors, spool shims in Ruk: superseded by HostPolicy (C3). Spool, outbox, and reconciler live in rukie (K7).
- `push.mirror{mode, select}`: rejected (C8). Rukie computes refspecs.
- Separate `ref.get`: rejected (C14).
- Collab records in Ruk (`Records`, `RecordLeases`, `BlobSketch`, `record.*`): rejected by the monolith verdict.
- `fetch` transport job (pull mirrors): deferred.
- RC-11 atomic `best_effort` and RC-12 closure preflight: deferred (P2).
- SSH-key injection through `TransportAuth`: deferred (v2).
- Search result cache: deferred (v2 LRU).
- `BlameHunk` provenance display extension: no change.
- Last-commit-per-path tree metadata: open gap, unscheduled (see `Needs user yes`).
- Profile `"forgejo"` acceptance: rejected (C2).
- Push-plan lifecycle CLI verb: deferred.
- `--force-with-lease` CLI flag: deferred.
- General retry loop in Ruk: rukie owns backoff (F3).
- Client push-options on wire push: deferred (P2).
- LFS API server and LFS client auth beyond env: deferred (P2).
- HTTP proxy support: deferred (P2).

### rukie steps

Each Gate lists test names. The `passgate` needle for a test is `--- PASS: <Name> (`. A subtest uses `<Parent>/<sub>`. Skips and zero-match runs fail. Test packages sit under `/home/alpha/rewrite/git-renewed/rukie` unless a Ruk test name is shown in snake_case. The steps run in this order: K1 to K10, F1 to F14, the Dogfood gate block, then the D block.

K1 demolition pad: see Step 0.
   Anchor: Christopher Alexander
   Needs: nothing. Gate: the gates of that step.

K2 Ruk client.
   Anchor: David Parnas
   Lands: package `internal/ruk` per `ruk-client-design.md` revision 2 (O2). Payload types are hand-written.
   - `Call[Req, Resp](ctx, c, repo, method, req)` with typed wrappers.
   - A pool with `StreamsPerConn` default 8.
   - A handshake that sends profile `rukie`.
   - A codec for frame kinds 1 to 8 and 10 to 13. Kind 9 is retired.
   - Cancel rules of §7.7.
   - Sentinel errors for the closed error-code set, usable with `errors.Is`, carrying hints, fixes, and detail from CS-R48.
   - Outcome from client-observed facts (§10).
   - The retry matrix of §11: reads retry once, typed mutations resend the same request id, and the client never resends a mutation the daemon may have accepted.
   `internal/ruk/internal/rukgen` generates the closed sets from `internal/ruk/testdata/ruk-model.json`. `internal/ruk/internal/fakedaemon` serves client unit tests only. Do not create `internal/rukclient` or `internal/rukproto`. Do not copy `ruk_ipc.go`. The client is hand-written, so no GPL file enters rukie (CEO-11).
   The harness resolves `RUK_BIN`, spawns `"$RUK_BIN" serve --config <tmp>/daemon.toml` with `allowed_uids` set to the test uid, and fails when the binary is missing (ENG-23). `ruk-client-design.md` §2.4 and §2.5 describe the `CommandOpen` wire that CS-R07 deletes. K2 ships no `Command` type. K6 writes it against the envelope. `tools/ruk.pin` holds the exact Ruk commit and this commit sets the first pin. `.github/workflows/ci.yml` runs G1a to G5 and the O10 gates at the pinned commit (V1). D1: `ruk/AGENTS.md` lands in the CS-R01 commit.
   Needs: K1; CS-R01 to CS-R04 and CS-R48 (pair commit).
   Fixes: O2; CEO 7, 8; Eng rukie 4.
   UI: none.
   Gate: `TestRukClientFramingRoundTrip`, `TestModelGolden`, `TestRukgenDrift` in `./internal/ruk/`; `TestDaemonLifecycle`, `TestDaemonRejectsRepoOutsideRoots`, `TestDaemonRefusesUnlistedPeer` in `./internal/ruk/` with `-tags integration`. `TestCommandOpenCommitBypassesCoordinator` is dropped because CS-R07 deletes CLI-shaped `CommandOpen`.

K3 Store.
   Anchor: Tony Hoare
   Lands: package `internal/store` per `store-engine-design.md`. `modernc.org/sqlite` v1.60.1. `go.mod` keeps `modernc.org/libc` at v1.77.1; never `go get -u` it (O33).
   - `Store` with `Read(ctx, fn)` and `Write(ctx, fn)` and typed accessors.
   - One pinned writer connection.
   - Group commit: batch at most 64, linger 2 ms, queue depth 256, one savepoint per request.
   - Batch statements run under `context.WithoutCancel`. Check request cancellation only at dequeue. `ROLLBACK TO` plus `RELEASE` isolates one failed request (Eng rukie 10).
   - Readers: `max(2, GOMAXPROCS)` clamped to 64.
   - Pragmas: WAL, `synchronous=FULL`, `foreign_keys=ON`, `busy_timeout=5000`, OFD locking. Size the WAL for held-snapshot checkpoint delay per §2.2.
   - Embedded `NNNN_name.sql` migrations with checksums. Refuse newer or changed versions.
   - ULIDs for public ids. Unix milliseconds for times.
   - FTS5 trigram tables on external content with triggers. Probe `tokenize='trigram case_sensitive 0'` and fall back to bare `trigram`. A startup probe fails fast without FTS5.
   - `Backup` with `VACUUM INTO`.
   - The outbox table is written in the same transaction as domain rows.
   Run `go mod download` for v1.60.1 once to close the pin check.
   Needs: K1. No Ruk row.
   Fixes: Eng rukie 10; O33.
   UI: none.
   Gate: in `./internal/store/`: `TestWriteRace_ManyWriters`, `TestWriteRace_CloseDuringTraffic`, `TestCanceledBeforeDequeue`, `TestBatchSavepointIsolation`, `TestCrashMidBatch`, `TestConnPragmas`, `TestMigrationRollback`, `TestSchemaTooNew`, `TestChecksumChanged`, `TestFTSProbe`, `TestFTSProbeErrorPath`, `TestFTSSurvivesVacuumInto`.

K4 Operation table, app skeleton, and render foundations.
   Anchor: Christopher Alexander
   Lands (op table and app):
   - `internal/op` with six files per `op-table-design.md` §2: `table.go`, `negotiate.go`, `mount_huma.go`, `derive_mcp.go`, `derive_cli.go`, `derive_html.go`. `mount_huma.go` is the only Huma import (v2.39.1). K8 mounts the MCP and CLI derivations.
   - `internal/api/register.go` and `internal/api/<domain>.go`.
   - `internal/access` for guards and rate limits.
   - `internal/config` with `github.com/BurntSushi/toml` v1.6.0 per `ops-design.md` §1. An unknown key fails startup. Add `rukie serve --check-config`.
   - `internal/logging` with slog JSON.
   - `rukie serve` binds HTTP, serves `/healthz` (in-process liveness) and `/readyz` (store ping, daemon handshake probe, migrations current; O35). Add `healthz` and `readyz` to the reserved-name registry.
   Routes: `url-information-architecture.md` §4 owns paths.
   - An op path is its canonical resource URL. The `/api/...` paths in `usage-surface-draft.md` §3 are not routes.
   - Rename path variables `o` and `r` to `owner` and `repo` in schemas, so CLI flags and MCP arguments read `--owner` and `--repo`.
   - Where the usage-surface writes `/-/login` or `/-/settings/...`, serve the flat url-IA route (`/login`, `/settings/...`).
   - Serve `GET /-/api` (op index) and `GET /-/api/openapi.json` (OpenAPI 3.1). Reserve `/-/mcp`.
   - Generate the reserved-name registry from the route table. Add the single-hop 301 for the `.git` suffix and the `a...b` alias that translates to `a::b`.
   Fixes (op table):
   - Names: usage-surface §3 (O11). Where `usage-surface-draft.md` has no row, the corpus design that names the op supplies the exact name (F5 uses `ci-control-plane-design.md` §11). The name check accepts the verbs of §1.1 plus `cancel`, `read`, `register`, and `validate`, which the CI design uses (see `Needs user yes`). Page-only routes with no usage-surface row register under their url-IA name and the parity gate lists them as `page-only`.
   - Errors: one catalogue, op-table §4 (O14). Add exit 78 `config-unavailable` to §4.4.
   - Idempotency keys live 24 h. HTML `_idem` forwards as the Ruk `request_id` (O15).
   - ETags are strong and quoted. `If-Match` compares inside the store transaction (O16).
   - Negotiation: explicit q-values decide. Absent `Accept` or bare `*/*` yields JSON. Ties go JSON, Markdown, HTML (O17).
   - Scopes: the 15 scopes of `auth-design.md` §8.2 as changed by O19: `intents:read`, `intents:write`, `repo:create`. Delete `PrincipalAgent` and `OnBehalf` from §3.5.
   - Search pages pass the Ruk cursor, emit no `Link` header, and return the lease-lost problem on expiry (O20).
   - Problem bodies carry `trace_id` (the request id) and a `recovery` object: `next` (exact next command), `current_etag`, `holder`, `expires_at`, `merge_base`, `backoff_ms`, `docs` (DX-09, DX-15).
   - Add the `Sunset` header mechanism. No op is released, so no alias ships (DX-17).
   - CSRF uses `http.CrossOriginProtection` plus an Origin check.
   - Per-token bucket 300 requests per minute, burst 600 (`auth-design.md` §8.3). Per-IP buckets: 5 login failures per 10 minutes, 60 `401`s per minute on git routes with a 10-minute cooldown (§10.2). Over-limit returns 429 with `Retry-After`. The hand-written bucket lives in `internal/access/ratelimit.go`; no new module (ENG-39). The per-account bucket is listed under `Needs user yes`.
   - The `Views` registry accepts one hand-authored page per op name beside the derived form (CEO-12, DES-05).
   - Add the DX parity gate (DX-18): the op table, url-IA routes, OpenAPI, CLI tree, and MCP tool list stay in lockstep.
   Lands (render, U0 to U3): packages under `internal/web/` per `rendering-design.md` §3.1 for the fallback layer only (`assets`, `asset`, `secheaders`, `trusted`, `htmlcheck`, `shell`, `ui`, `view`, `vm`, `highlight`, `markdown`, `diff`, `negotiate`); `zerojs` is deleted (O41) while `trusted`/`htmlcheck` keep guarding untrusted content.
   - templ v0.3.1070 as a `tool` directive, added in the first templ commit (O33). templ renders baseline HTML only; it is the no-JS fallback surface (O41).
   - goldmark v1.8.6 with no unsafe mode and no attribute syntax (O22), for fallback and API Markdown.
   - chroma v2.27.0 with CSS classes, for fallback and `*.patch`/raw surfaces.
   - go-diff v0.9.0 `ParseHunks` under budgets, for fallback diff pages.
   - The web application lives in `web/` (O41): ReScript sources only, `rescript.json` with `jsx.module = "Preact"`, a local `Preact.res` generic-JSX interface externalling `preact/jsx-runtime`, `esbuild` bundling to ES modules embedded via `go:embed`. App code is `.res` only; no `.ts`, no hand-written `.js`, no React, no `@rescript/react`. The app hydrates the fallback HTML it is served with.
   - Embedded `app.css` (at most 32 KiB) and generated `chroma.css` (at most 24 KiB), content-hashed and immutable. No Go build tool; the web app bundles through esbuild.
   - Tokens from `design-direction-instrument.md` with O23: OKLCH custom properties, both schemes via `prefers-color-scheme`, spacing `{4,8,12,16,24,32,48}`, radius `{4,8,12,16}`, tabular numerics. Transitions only on `background-color`, `color`, `box-shadow`, `opacity`, `outline-color`, `transform`, at most 150 ms, easing `cubic-bezier(0.22,1,0.36,1)`, only under `prefers-reduced-motion: no-preference`. No first-paint keyframes. One CSS-only CI spinner.
   - The status palette maps the Ruk attestation, gap, claim, and mirror states across the five roles.
   - Fonts per O24: the layout emits the one Bunny Fonts `<link rel="stylesheet" ... referrerpolicy="no-referrer">` of O24. The CSP is `default-src 'none'; style-src 'self' https://fonts.bunny.net; font-src https://fonts.bunny.net; img-src 'self' data:; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`. `script-src 'self'` admits the embedded same-origin bundle (O41); content routes serve `X-Content-Type-Options: nosniff` so a same-origin upload cannot become a script source. Every `font-family` keeps its fallback stack. There is no config switch.
   - Precedence: `design-direction-instrument.md` and `url-information-architecture.md` beat `autoplan-design.md` DES-01, DES-02, DES-06, DES-08. DES-03, DES-09, DES-11 to DES-21 apply. `design-direction-instrument.html` is illustrative only (O25).
   - The fallback contract of O41 replaces route-level `zerojs.Assert`: `TestAllHTMLRoutesRenderWithoutJS` and `TestNoJSFallbackMutationsPost`. `htmlcheck.Check` still runs on untrusted content inside the fallback and gains the URL-scheme fix (O26).
   - Meta refresh only on declared live surfaces, at least 15 s, user-toggleable, suppressed on pages with an editable form (O28).
   - Line highlight is `?lines=` (O29).
   - Record Ruk fixtures from the real daemon for `TestRenderRouteBudgets` (U0).
   - Every mutation ships a working `<form action>` POST; `TestNoJSFallbackMutationsPost` drives each form without JS.
   - U3 conventions: `?cursor=` plus `Link: rel=next`; `Vary: Accept` on every negotiated response.
   Needs: K2, K3. No Ruk row.
   Fixes: O11, O14 to O17, O19, O20, O22 to O29, O33; Design 1 to 4, 6; DX 6 to 8, 11, 15; CEO 10.
   UI: the generic HTML error page, the op index page `/-/api`, the problem pages `/-/problems/{slug}`.
   Gate: in `./internal/op/`: `TestTableRejectsDuplicateName`, `TestNegotiationMatrix`, `TestIdempotencyAuthFailureThenRetryExecutes`, `TestETagStrongQuoted`, `TestPaginationLink`, `TestProblemCatalogue`, `TestProblemRecoveryFields`, `TestTokenBucketRetryAfter`, `TestSunsetHeader`, `TestParityGate`, `TestOpenAPIGolden`. In `./internal/web/...`: `TestAllHTMLRoutesRenderWithoutJS`, `TestNoJSFallbackMutationsPost`, `TestRenderedPagesSecurityHeaders`, `TestStaticHeaders`, `TestCSSLayered`, `TestTokenContrast`, `TestAssetHash`, `TestEmbeddedAppBundle`, `TestNoJSAppSource`. In `web/`: `rescript build` clean and the `esbuild` bundle builds. Also `TestDumpRenderedPages` (tag `render`, `RUKIE_RENDER_DIR`) followed by `npm exec --yes --package=html-validate@11.16.4 -- html-validate --config .htmlvalidate.json --max-warnings 0 .rendered/*.html` (O38).

K5 Auth and access.
   Anchor: Tony Hoare
   Lands: package `internal/auth` per `auth-design.md`.
   - argon2id m=19456 KiB, t=2, p=1, PHC encoding. `auth-design.md` §3.1 owns the numbers (O21).
   - Server-side sessions behind `__Host-rukie-session`: idle 24 h sliding, absolute 30 d, rotation on login.
   - Lockout: 5 failures in 10 minutes locks 15 minutes, doubling to 24 h; dummy-hash equalizer.
   - Tokens: `rk_` plus 43 base64url characters, SHA-256 at rest, 12-character prefix, mandatory expiry (at most 366 d, default 90 d), last-use tracking.
   - Fresh-auth (15 minutes) gates.
   - The 15 scopes of O19. `auth.token.create` takes `repos` empty until F4.
   - Audit rows (`auth-design.md` §12).
   - `internal/access` gains `authz.Check` per `permission-model.md`: decision table, `BookmarkRule` guards G0 to G11, max over grants, public-only tokens, 404 for invisible resources. Invariants INV-1 to INV-25 run as a property suite.
   - DDL: users, sessions, tokens, `login_attempts`, audit, `repos`, `repo_grants`, `bookmark_rules`.
   Ops: `auth.login`, `auth.logout`, `auth.password.change`, `auth.token.list`, `auth.token.create`, `auth.token.revoke`, `auth.session.list`, `auth.session.revoke`, `user.get`, `user.self.update`, `user.self.delete`, `admin.user.create`, `admin.user.reset-password`, `admin.user.unlock`, `admin.user.update`, `admin.user.list`.
   Hand-written: `rukie bootstrap --config <file> --username <name> --password-stdin` creates the first admin through `admin.user.create` and refuses once an admin exists. Password-typed fields have `--<field>-stdin` and `--<field>-file`, never an inline flag. The `/login` page documents `rukie admin user reset-password --user <name>`. `/signup` is not registered (no self-signup).
   Needs: K3, K4. No Ruk row.
   Fixes: O19, O21; CEO 6; DX 14; Pins 5.
   UI: `/login`, `/logout` (GET confirm, POST), `/settings` (profile), `/settings/security` (password, sessions), `/settings/tokens` with `POST .../tokens/{id}/revoke`, `/{owner}` user profile, `/admin/users` list. TOTP, OIDC, and SSH-key pages land in F12 and F10.
   Gate: in `./internal/auth/` and `./internal/access/`: `TestArgon2idParams`, `TestPasswordLockout`, `TestSessionCookieHostPrefix`, `TestTokenScopeCeiling`, `TestPermissionInvariants`, `TestPublicOnlyTokenSees404`, `TestBootstrapRefusesSecondAdmin`, `TestPasswordFieldHasNoInlineFlag`.

K6 Git wire (Smart HTTP only).
   Anchor: John Carmack
   Lands: package `internal/githttp`.
   - Endpoints `info/refs`, `git-upload-pack`, `git-receive-pack` through the url-IA §4.8 catch-all. The `Git-Protocol` header feeds the envelope's `git_protocol`. gzip request bodies. `http.MaxBytesReader`. Context cancel sends IPC `Cancel`. HTTP budgets: 10 minutes for upload-pack, 5 minutes for receive-pack.
   - `internal/ruk` gains `Command` against the git-service envelope: `ServiceOpen{Service, Repository, SessionID, RequestID, GitProtocol, Advertise, StdoutCredit, Limits}`, stdin and stdout streams, stderr writer, credit accounting. Rukie chooses `request_id` as `push-<ulid>`.
   - The envelope carries the repo's effective `limits` (CS-R07, C10): `max_pack_bytes`, `max_objects`, `max_object_bytes`, `max_delta_depth`, `max_stdin_bytes`, `max_push_options`. The daemon digest covers them.
   - The HostPolicy handler answers `HookCall` frames. It returns per-ref verdicts (`accept`, `reject{reason}` of at most 512 bytes, or `divert`) from `authz`. It fails closed at `hook_timeout_ms`. Until F2 the handler rejects `refs/for/*` with reason `change review is not available`. F2 replaces that arm.
   - Gate seeding. Tests create repos with `ruk init` under the daemon `repo_roots` (CLI, `RUK_COORDINATOR=off`). They insert the `repos` and `bookmark_rules` rows directly. `repo.create` ships in F1, so no K6 or K7 test calls it.
   Git authorization (Eng rukie 6):
   - HTTP Basic: the username must equal the token owner, else 401. Never accept passwords, sessions, or TOTP codes here.
   - Fetch needs `repo:read`. Push needs `repo:write`. `token_repos` applies when non-empty.
   - Anonymous callers read public repos only. `internal` and `private` repos deny anonymous reads. Unreadable private repos return 404.
   - `refs/for/<bookmark>` ingress needs only read (G1).
   - A `LandOnly` rule denies direct push, except `BypassAdmin` with a `PushAllow` match (G4).
   - No self-approval (G5) applies at review and land time (F2).
   - Authorize before opening the daemon stream. The per-token bucket (K4) counts git requests. Per-IP: 60 `401`s per minute starts a 10-minute cooldown.
   No env var reaches the daemon stream, because CS-R07 deletes `CommandOpen.env`. `RUK_COORDINATOR=off` applies only to CLI seeding in tests.
   Needs: K2, K4, K5; CS-R05 to CS-R10, CS-R26, CS-R27, CS-R45 to CS-R47 (pair commit). Per-stream limits come from CS-R07 before any git route.
   Fixes: O3, O5; Eng rukie 6, 7; CEO 5, 7; ENG-31.
   UI: none. Browsers hitting a bare `.git` URL get the 301 to the repo page.
   Gate: `TestConcurrentPushesSerialize`, `TestGitBasicAuthUsernameMismatch401`, `TestGitAnonymousReadInternalDenied`, `TestGitLandOnlyDirectPushDenied`, `TestGitAuthorizesBeforeOpeningStream`, `TestGitStreamLimitsRefuseOversizePush`, `TestRefsForRejectedUntilReview`, `TestNoGitProcessInBoundary` (rukie spawns no child process during clone and push), `TestGitServiceUnknownServiceRefused`. Ruk side: the changeset §3 cluster F list (`host_pre_receive_per_ref_accept_reject`, `host_divert_writes_review_ref_only`, `host_timeout_rejects_all_fail_closed`, `receive_replay_same_digest_no_second_publish`, `frame_kind_9_rejected_on_any_stream`, and the rest).

K7 Events, queue, and outbox.
   Anchor: Christopher Alexander
   Lands: packages `internal/events` and `internal/queue`. Job kinds: `hook_delivery`, `mirror_run`, `repo_fork`, `repo_purge`, `archive_build`, `land`, `ci_plan`. Job ids are deterministic (`INSERT ... ON CONFLICT DO NOTHING`).
   - Follower: one long-poll `events.tail` per repo. The follower stores its cursor in the same transaction as the outbox rows it creates. On `expired`, snapshot with `refs.list.as_of`, then resume. Delivery is idempotent by `(name, new)`.
   - Spool-first handler for post-receive notifications. It answers the HostPolicy post-receive `HookCall`. HostPolicy replaces hook scripts (C3). Post-receive stays the rukie notification path. Order:
     1. fsync a spool record `{invocation_id, repo, landed[], actor, attempted_at}` under `data_dir/spool/` first;
     2. make one callback attempt inside the hook budget: one SQLite transaction resolves the invocation from `pending_pushes` and enqueues CI scan, webhook rows, mirror jobs, and inbox rows, all or none;
     3. remove the spool record on success; leave it on failure.
     No hook token exists. `HookCall` frames ride the stream the daemon authenticated by `SO_PEERCRED`.
   - Timer drain every 30 s.
   - Tip-comparison reconciler every 5 minutes per repo: compare `refs.list` tips to stored `ref_tips`. Enqueue a synthetic `ref.updated` with `source=reconcile` when no event is in flight.
   - Ruk-first write order: for every operation that changes Ruk and rukie state, call Ruk first with a stable `request_id`, then commit the SQLite transaction (domain rows plus outbox) second. After a crash between the two, the retry replays the Ruk call from its receipt and then commits.
   - Invocation-id dedupe: dedupe on `invocation_id` so a retried callback or spool replay never double-enqueues (Eng rukie 2).
   - Client rules (O35, client half): a git-service stream never re-runs. An unknown outcome reconciles through `Lookup` (same `request_id` and digest returns the stored `ServiceReceipts` report), then `events.tail` tip comparison.
   Needs: K3, K6; CS-R12, CS-R11, CS-R16, CS-R20 (pair commit).
   Fixes: Eng rukie 2, 3; ENG-21, ENG-52 (push path).
   UI: none.
   Gate: `TestFollowerReplayDedupe`, `TestSpoolSurvivesRukieCrash`, `TestReconcilerRepairsMissedPush`, `TestRukFirstWriteOrderCrash`, `TestEventsTailExpiredResnapshot`, `TestServiceReceiptsSurviveDaemonKill`, `TestFaultKillNine/push`. K7 tests use the K6 seeding path. Ruk side: `events_survive_restart` and cluster C store-format tests.

K8 MCP and CLI.
   Anchor: Claude Shannon
   Lands (MCP):
   - `/-/mcp` is POST only, stateless Streamable HTTP (`StreamableHTTPOptions{Stateless: true}`). GET and DELETE return 405 (O12).
   - `auth.RequireBearerToken` verifier returns `auth.TokenInfo` with `Scopes` and a non-zero `Expiration`. It wraps every credential failure in `auth.ErrInvalidToken`, else the SDK returns 500. Auth failures on `/-/mcp` keep the SDK `text/plain` body and `WWW-Authenticate` header. That is the one RFC 9457 exception (O12).
   - Tool name is the op name with `.` replaced by `_`. A startup check rejects collisions. Toolsets are op domains. Annotations: `readOnlyHint`, `destructiveHint`, `idempotentHint`. Byte-returning ops return a `resource_link`. Mutations take `idempotency_key`.
   - Toolset domains are the first segment of an op name. The domain list is the `usage-surface-draft.md` §1.1 list (`auth user org team repo code branch tag search release webhook mirror star explore inbox admin`) plus `change`, `intent`, `ask`, `view`, `land`, `proof`, and `ci`. F2 and F5 name the added domains. The settled noun `intent` replaces the provisional `task` domain, so no `task.*` op registers. The 16-domain list of `op-table-design.md` §12.6 is superseded.
   - The default toolset holds at most 36 tools. Other toolsets load only through an explicit `toolsets` selection (O13). The default is exactly these 36: `code.rev.resolve`, `code.tree.get`, `code.blob.get`, `code.blame.get`, `code.log.list`, `code.commit.get`, `code.diff.get`, `code.compare.get`, `branch.list`, `branch.get`, `repo.get`, `change.list`, `change.get`, `change.diff.get`, `change.stack.list`, `change.comment.list`, `change.comment.create`, `change.review.create`, `branch.change.list`, `intent.list`, `intent.get`, `intent.create`, `intent.update`, `intent.state.set`, `intent.lease.create`, `intent.lease.update`, `intent.lease.delete`, `intent.change.add`, `ask.create`, `ask.list`, `view.list`, `view.result.list`, `land.plan.create`, `land.run.create`, `proof.list`, `search.code`. Tools whose op does not exist yet join as their step lands.
   - `rukie mcp --server URL --token-env RUKIE_TOKEN [--toolsets ...] [--read-only]` is the stdio server.
   Lands (CLI):
   - `rukie <domain> [<sub>] <verb>` with space-separated words and flags from the input schema.
   - `--json`, `--input -` for JSON on stdin, `--dry-run`, `--idempotency-key`, `--if-match`, `--cursor` or `--all`, `--server`, `--config`.
   - The exit table of op-table §4 including 78.
   - Session-only ops run through a session file (O18). `rukie auth login` writes the session cookie to `--session-file` (default `$XDG_STATE_HOME/rukie/session`, mode 0600). Every later command sends it. Bearer tokens use `--token-file`.
   - With `--config <file>` and no `--server`, admin ops run in-process through `LocalInvoker`.
   - Hand-written names reserved against the derived tree: `serve`, `runner`, `job-exec`, `bootstrap`, `dev`, `backup`, `restore`, `mcp`, `admin secret-gen`, `admin ssh-keygen`, `admin migrate`, `admin status`, `admin reconcile`, `admin gc`, `admin repo import`. A startup check rejects a collision.
   Needs: K4, K5.
   Fixes: O12, O13, O18; DX 2, 3, 9, 17; Pins 6.
   UI: none.
   Gate: `TestMCPConformance`, `TestMCPDefaultToolBudget` (counts default tools, fails above 36), `TestMCPRouteMethods`, `TestMCPAuthFailureShape`, `TestMCPNameCollisionRejected`, `TestCLIJSONGoldens`, `TestCLISessionFile`, `TestExitCodeTable`, `TestHandWrittenNamesDoNotCollide`.

K9 Dev loop and agent docs.
   Anchor: Milton Erickson
   Lands:
   - `rukie dev`: ephemeral `ruk serve` on a temp socket and temp roots. It seeds user `dev` plus a token with `repo:create`, `repo:read`, `repo:write`, `intents:read`, `intents:write`. It prints URLs and credentials. `rukie dev reset` and `rukie dev seed` are subcommands. CI records the startup time (DX-11). F1 extends the seed with repo `dev/hello`.
   - `GET /llms.txt` and `GET /llms-full.txt`, generated from the op table. `/llms.txt` holds the agent start-here path (instance URL, token, `/-/mcp`, CLI, negotiation). `/llms-full.txt` holds per-op Markdown. Add both names to the reserved-name registry (DX-08).
   - `/-/api` becomes the server-rendered API reference (DX-10).
   - `tools/docgen` writes `docs/reference/operations/*.md` from the op table (D4).
   - The parity gate grows to include the reference pages and `llms.txt` entries.
   Needs: K8; CS-R01.
   Fixes: DX-08, DX-10, DX-11, DX-18.
   UI: `/-/api` reference pages.
   Gate: `TestDevLoopStarts` (tag `integration`), `TestLLMSFreshness`, `TestReferenceMatchesTable`, `TestParityGate` (extended).

K10 Ops.
   Anchor: Dieter Rams
   Lands:
   - Supervisor `internal/supervisor`. It runs `ruk serve --config <rendered daemon toml>` as a child. CS-R01 removed the old spelling, so there is no spelling probe. A `[ruk] socket` setting selects an external daemon with no child.
   - O35: restart the child at most 3 times with backoff 1 s, 2 s, 4 s, then exit non-zero. `/readyz` is false while the child is down. Daemon-backed requests fail fast with 503 and `Retry-After: 5`. Git-service streams never re-run. An unknown outcome reconciles through `Lookup`, then `events.tail` tip comparison. This corrects the linear backoff text of `ops-design.md` §1.6.
   - The child runs with a hermetic environment: `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=` empty, empty `HOME`, no `GIT_AUTH_*`. F3 relies on this env together with CS-R30. `allowed_uids` lists the rukie uid.
   - Startup waits 15 s for the socket and handshake. Graceful drain 40 s in the order of `ops-design.md` §3.3. SIGHUP reloads the log level.
   - Systemd units. One Dockerfile: distroless, non-root, digest-pinned bases, both binaries.
   - Data-dir layout. `rukie admin migrate [--dry-run]`, `rukie admin status [--json]`, `rukie admin reconcile [--from-manifest <dir>]`.
   - Downgrade refusal: exit 3 when `meta.version` on disk is newer than the binary.
   - Secret key (O36): `rukie admin secret-gen --out <file>` writes a 32-byte key at mode 0600 and refuses to overwrite without `--force`. The default path is `/etc/rukie/secret.key`, outside `data_dir`. The key id is the lowercase hex of the first 8 bytes of SHA-256 over the key. `rukie backup` (`VACUUM INTO`, repos, manifest) never copies the key and records its key id in the manifest. `rukie restore <dir> [--force]` refuses when the configured key id differs from the manifest. The operator guide tells operators to back up the key offline.
   Needs: K4, K7; CS-R01. No new Ruk row.
   Fixes: O35, O36; Eng rukie 3, 5; CEO 10.
   UI: none.
   Gate: `TestSupervisorRestartCap`, `TestReadyzFalseWhileChildDown`, `TestDaemonCallsFailFast503`, `TestGitServiceStreamNeverReruns`, `TestGracefulDrain`, `TestBackupNeverCopiesKey`, `TestRestoreRefusesKeyMismatch`, `TestDowngradeRefused`, `TestCheckConfigFailsFast`.

F1 Repos and browsing (S0 skeleton).
   Anchor: Christopher Alexander
   Lands:
   - Repo ops: `repo.create`, `repo.get`, `repo.list`, `repo.update`, `repo.visibility.set`, `repo.rename`, `repo.delete`, `repo.restore`, `repo.collaborator.list`, `repo.collaborator.add`, `repo.collaborator.remove`.
   - Code ops: `code.rev.resolve`, `code.tree.get`, `code.blob.get`, `code.raw.get`, `code.blame.get`, `code.log.list`, `code.commit.get`, `code.diff.get`.
   - Ref ops: `branch.list`, `branch.get`, `branch.create`, `branch.update`, `branch.delete`, `tag.list`, `tag.get`, `tag.create`, `tag.delete`.
   - `repo.create` is a session op for self or an org owner, and a token op with `repo:create`. A token may create only in its owner's user namespace; any other destination returns 403 (O19). Order is Ruk first: call `repo.init`, then commit the repo row, the default `bookmark_rules` row, and the `repo.created` outbox row. With `init_readme`, write the README commit through Ruk `git.commit`.
   - `repo.delete` soft-deletes and returns 202 with `purge_after`. A `repo_purge` job calls Ruk `repo.delete`.
   - `code.tree.get` ships without `with_last_commit`. Raw responses are immutable for full-oid revs.
   - There is no `CommandOpen` fallback after CS-R07. Every hosted read is a typed row, so browsing blocks on CS-R13, CS-R14, CS-R15 (Eng rukie 11).
   - Compare lands in F2 (`code.compare.get`) and archive in F11.
   - `rukie dev` seed gains `dev/hello`.
   Needs: K5, K6, K7, K8, K10; CS-R13, CS-R14, CS-R15, CS-R23 (pair commit).
   Fixes: O8, O19, O29, O30; CEO 6, 8; Eng rukie 8, 9, 11; V2 to V4.
   UI: repo home with the README, tree, blob with `?lines=`, raw, blame, log, commit with `/diff` and `/patch`, branches, tags, `/new/repo`, `/{o}/{r}/settings`, `/{o}/{r}/settings/collaborators`. Each page is hand-authored on top of the derived op data (DES-05).
   Gate: `TestAgentFlow/create`, `TestAgentFlow/push`, `TestAdminMethodsRefusedForNonAdminPeer` (the daemon runs with the test uid in `allowed_uids` and absent from `admin_uids`; `repo.init` returns forbidden), `TestRepoCreateTokenNamespaceOnly`, `TestRepoCreateRukFirst`, `TestAllHTMLRoutesRenderWithoutJS`, `TestNoJSFallbackMutationsPost`, `TestDumpRenderedPages` plus the html-validate command of O38, `TestPerfBudget` (release binary, no `-race`, at least 1000 samples after 100 warmups; HTML at most 200 ms, JSON at most 100 ms, `info/refs` at most 150 ms, p95 nearest-rank; per-route render budgets of `rendering-design.md` §12.1 on the reference runner).

F2 Review and land (S1).
   Anchor: Christopher Alexander
   Settled op names, listed once (O11). Reads need `repo:read`. Writes need `repo:write` unless noted.
   - Compare: `code.compare.get` (Ruk `compare`, CS-R17). It serves `/{o}/{r}/compare/{range}`, including the `a...b` alias.
   - Changes: `change.list`, `change.get`, `change.diff.get`, `change.blame.get`, `change.stack.list`, `branch.change.list` (the branch view), `change.comment.list`, `change.comment.create`, `change.review.create` (`verdict` `approve`, `block`, `comment`; `model` bool records `model/review` and never satisfies a human-approval requirement), `change.state.set` (open or abandoned).
   - Proofs: `proof.list`.
   - Landing: `land.plan.create` (Ruk `land.prepare`), `land.plan.get`, `land.plan.list`, `land.run.create` (Ruk `land.execute`; boolean `stack`), `land.run.list`. `admin.land.run.create` needs `admin:write` and a site admin, takes a non-empty `reason`, and is audit-logged with the waived requirements.
   - Policy (session ops, role `maintain`): `repo.policy.list`, `repo.policy.get`, `repo.policy.set`.
   - Views (read `repo:read`, write `intents:write`): `view.list`, `view.get`, `view.result.list`, `view.create`, `view.update`, `view.delete`, `view.visibility.set`.
   - Intents (`intents:read` or `intents:write`, ship in F7): `intent.list`, `intent.get`, `intent.create`, `intent.update`, `intent.state.set`, `intent.lease.create`, `intent.lease.update`, `intent.lease.delete`, `intent.change.add`, `intent.change.remove`.
   - Asks (ship in F7): `ask.list`, `ask.get`, `ask.create`, `ask.update` (cancel), `ask.answer.create` (human session op).
   - Old to new: `intent.close` and `intent.reopen` become `intent.state.set`; `intent.claim` becomes `intent.lease.create`; `intent.release` becomes `intent.lease.delete`; `intent.link_change` becomes `intent.change.add`; `ask.answer` becomes `ask.answer.create`; `view.run` becomes `view.result.list`; `view.share` becomes `view.visibility.set`; `change.show` becomes `change.get`; `change.diff` becomes `change.diff.get`; `change.blame` becomes `change.blame.get`; `change.review.submit` becomes `change.review.create`; `change.land` and `land.prepare` and `land.execute` become `land.plan.create` and `land.run.create`; `land.admin_execute` becomes `admin.land.run.create`; `policy.get` and `policy.set` become `repo.policy.get` and `repo.policy.set`; `comment.create` and `comment.list` become `change.comment.create` and `change.comment.list`.
   Lands:
   - Change ingress. The HostPolicy handler diverts `refs/for/<bookmark>` to a review ref `refs/changes/<nn>/patchset/<k>`. It creates or updates the change synchronously inside the hook budget (at most 10 s). It answers `ng` unless the transaction committed (`ssh-smarthttp-design.md` §5.5).
   - Comments anchor to (change-id, head, path, line). Remap them on rewrite per `review-task-data-model.md` §3: match positions through the typed Ruk diff between heads, and keep unmatched rows in a fallback bucket.
   - `review/approve`, `review/block`, and `model/review` proofs go through Ruk `attest.add`.
   - The `LandIntent` machine: Awaiting, Executing, Landed, Failed, Repreparing (at most 3 re-prepares). The land worker is keyed `(repo, target)`.
   - N serial land plans. A stack of N changes lands bottom-up as N pairs of `land.plan.create` and `land.run.create`. The ledger shows each plan. The sequence stops at the first failed or refused row and resumes from it. With `stack=true`, the server drives the sequence; it never runs one batch plan. An expired plan shows `plan expired` with a prepare-again button.
   - The default bookmark policy is data. `repo.create` writes a `bookmark_rules` row for the default branch: Mode `LandOnly`, `BypassAdmin` false, `Required` one `review/approve` from a trusted signer who is not the author (G5). `ci/*` joins `Required` when F5 lands (O34). `repo.policy.set` rejects `bypass_admin=true` in v1. A policy change calls Ruk `policy.set` (CS-R44) and bumps `policy_generation`.
   - Unmet requirements return problem `conflict` (409) with `reason` `requirements-unmet` and the gap list in `recovery` (O14). A second execution after drift returns `stale` (412, `E_STALE`).
   - The gap work-list is `view.result.list` with predicate `gap:*`. Gap reasons are computed from live `attest.list` and `claim.list`, never cached.
   Needs: F1; CS-R17, CS-R21, CS-R38, CS-R40, CS-R44, CS-R43 (pair commit).
   Fixes: O6, O11, O14, O34; CEO 9; DX 5; Design 5; Eng rukie 11.
   UI: `/{o}/{r}/changes`, `/changes/{cid}`, `/changes/{cid}/v/{n}`, `/changes/{cid}/diff/{range}`, `/changes/{cid}/stack`, `/{o}/{r}/branches/{name...}` (tip, recent log, open stacked changes, land-queue position, protection), `/{o}/{r}/compare/{range}`, `/{o}/{r}/settings/protected` (policy editor), `/changes` (cross-repo queue), `/{o}/{r}/views` and `/{o}/{r}/views/{id}` (compose form works without JS; the ReScript app adds the interactive composer). `GET /` signed in is the ops console (`site.home`). It shows the inbox count, the review queue, my changes, claimable intents, recent CI failures, the gap work-list, and the batch landing parade form. One submission runs N serial land plans. Comment forms are per-line `<details>` forms with the pending-review banner. The routes `/rebase` and `/watch` of url-IA §4.7 have no settled op and are not shipped.
   Gate: `TestAgentFlow/change` (asserts the comment remap after a rewrite), `TestAgentFlow/land` (asserts a second execution after drift fails `stale`), `TestCompareThreeDotAlias`, `TestLandStackRunsNSerialPlans`, `TestLandRequirementsUnmetProblem`, `TestNoSelfApproval`, `TestRefsForRequiresOnlyRead`, `TestDefaultPolicyIsData`, `TestRefsForDivertAnswersNgUnlessCommitted`. Ruk side: the `host_*` list and land prepare and execute hosted tests with an `E_STALE` retry test. `TestRefsForRejectedUntilReview` is deleted.

F3 Push mirrors (S2).
   Anchor: John Carmack
   Lands: package `internal/mirror` per `mirror-design.md`.
   - Ops: `mirror.list`, `mirror.create`, `mirror.get`, `mirror.update`, `mirror.delete`, `mirror.sync`, `mirror.run.list`, `mirror.run.get`. Scopes: `mirrors:read`, `mirrors:trigger`. Status is `mirror.get` and `mirror.run.get`; there is no `mirror.status`.
   - Host kinds `github`, `gitlab`, `forgejo`, `generic`. Modes `exact` and `fast-forward-only`. The wire and UI spelling is `fast-forward-only`, not `ff_only` or `fast_forward_only`. `ref_selection` values are `all`, `default_branch_tags`, `globs`. `exact` requires a confirmed dry-run when the plan has deletes or the target is non-empty. A `forgejo` kind on codeberg.org is refused unless `policy_ack=true`.
   - Credentials are sealed with AES-256-GCM under the secret key file and decrypted only in memory. They travel through `TransportAuth` on the call (CS-R29) and never through argv, env, args, digests, receipts, or logs (O3).
   - Triggers: ref update with a per-mirror debounce of at least 10 s, periodic reconcile, config change, manual sync.
   - Sync algorithm: compute the desired ref set from local `refs.list` and remote `remote.refs`. Exclude `refs/pull/*`, `refs/merge-requests/*`, `refs/for/*`, `refs/ruk/*`. `exact` forces and prunes. `fast-forward-only` reports `diverged` and never overwrites. Per-ref results with named rejection reasons: GitHub workflow scope, 100 MiB limit, protected branch.
   - Retry with jittered backoff (rukie owns it). Stop on 401 or 403 with state `needs_attention` and never retry. Honor `Retry-After` on 429. Cap concurrency per host. A POST 3xx re-bases the mirror URL.
   Needs: F2, K7, K10; CS-R29, CS-R30, CS-R33, CS-R31, CS-R32, CS-R34, CS-R35, CS-R28, CS-R36, CS-R37, CS-R22 (pair commit).
   Fixes: O3; Eng rukie 1, 3, 5; CEO 4, 7.
   UI: `/{o}/{r}/settings/mirrors`, `/settings/mirrors/{id}`, `/settings/mirrors/{id}/runs/{run}`, `POST .../sync`. The console gains the mirror backlog panel.
   Gate: `TestAgentFlow/mirror` with subtests `exact_prune`, `ff_only_refusal`, `globs`, `reconcile_restore`, and `stops_on_401`; `TestMirrorHonorsRetryAfter`, `TestMirrorCredentialCanary`, `TestMirrorCodebergNeedsPolicyAck`, `TestFaultKillNine/mirror`. Ruk side: the cluster G gate.

F4 Orgs, teams, and token selectors (S3).
   Anchor: Tony Hoare
   Lands: org and team ops with the names of `usage-surface-draft.md` §3.3 (O31, O11).
   - Org ops: `org.create`, `org.get`, `org.list`, `org.update`, `org.rename`, `org.delete`, `org.member.list`, `org.member.remove`, `org.leave`.
   - Team ops: `team.list`, `team.create`, `team.get`, `team.update`, `team.delete`, `team.member.list`, `team.member.add`, `team.member.remove`, `team.repo.list`, `team.repo.add`, `team.repo.remove`. There is no `org.member.add`, `team.grant`, or `team.revoke`.
   - Org membership is membership in at least one team. The `owners` team has `admin` on all org repos. `team.member.add` implies org membership. `team.repo.add` and `team.repo.remove` grant roles. `team.member.add` sends an inbox `membership` notice.
   - `authz.Check` gains team grants, max over grants, and `repo_scope` all or selected. `auth.token.create` enables the `repos` selector (`token_repos`). Org and team ops are session ops (O18).
   - There are no invite routes. `/invites/{token}` is not shipped because `team.member.add` adds members directly.
   Needs: F1, K5. No Ruk row.
   Fixes: O31, O11; DX 4; CEO 4.
   UI: `/{owner}` org profile, `/{owner}/-/members`, `/{owner}/-/teams`, `/{owner}/-/teams/{team}`, `/{owner}/-/settings`, `/settings/orgs`, `/new/org`, and the team grants view on `/{o}/{r}/settings/collaborators`.
   Gate: `TestTokenLimitedToOneRepoAndOpSet`, `TestAuthzWorkedResolutions`, `TestOrgOwnersTeamAdmin`, `TestTeamMemberAddImpliesOrgMember`, `TestLastOwnerGuard`.

F5 CI control plane (S4).
   Anchor: John Carmack
   Lands: package `internal/ci` per `ci-control-plane-design.md`.
   - Delivery is at-least-once and every consumer is idempotent. Triggers are keyed `(tree, pipeline digest, image digest)`. The job file is `.rukie/ci.toml`, validated by a JSON Schema that MCP and the CLI expose.
   - `matrix` expands at trigger time in the control plane into N ordinary jobs. The runner stays matrix-free. Append one ledger row recording that matrix, artifacts, and the content-addressed cache are in scope (supersedes the arch §11 hedge).
   - Untrusted runs read the job file from the base commit. Only trusted runs write the cache and read secrets. The proof kind is `ci/<job>/<spec_digest>`. Record verdicts only for final pass, final fail, and cache hits.
   - Durable queue with leases, heartbeats, fencing tokens, an epoch, and hierarchical start-time fair queueing. Boot recovery A and B with `E_RECOVERING` before load.
   - The outbox relay fixes `signed_at` in the outbox row so replays are byte-identical. The SSHSIG encoder matches the `protocol.rs` fixtures.
   - Finish rules (Eng rukie 12): same attempt and same `result_digest` returns the stored reply with `replayed: true`. A different digest on a terminal attempt returns `E_RESULT_CONFLICT`. A stale fence returns `E_LEASE_LOST`. Fenced-out results never cache or attest.
   - CI joins the bookmark policy: `Required` gains `ci/*` proofs (O34).
   - Op names follow `ci-control-plane-design.md` §11 with the O39 verb renames, because `usage-surface-draft.md` §3 has no CI rows: `ci.run.list`, `ci.run.get`, `ci.run.create`, `ci.run.update` (cancel through `state: cancelled`), `ci.run.retry`, `ci.job.list`, `ci.log.get`, `ci.artifact.list`, `ci.artifact.get`, `ci.spec.verify`, `ci.spec.get`, `ci.runner.create`, `ci.runner.list`, `ci.runner.revoke` (O39). Scopes `ci:read`, `ci:trigger`; the three runner ops need `admin:write`. The verbs `cancel`, `read`, `register`, and `validate` are outside the §1.1 closed set (see `Needs user yes`).
   Needs: F2, K7; CS-R38 (F2), CS-R39, CS-R41 (pair commit).
   Fixes: O11, O28, O34; Eng rukie 12; Design 5; D2 ledger line.
   UI: `/{o}/{r}/ci`, `/ci/runs/{id}` (jobs matrix, cache hits), `/ci/runs/{id}/jobs/{j}` (step table), `/jobs/{j}/log` (plain text, opt-in meta refresh at least 15 s while running), `/ci/runs/{id}/artifacts/{name}`, `badge.svg`, the repo CI tab, `/{o}/{r}/settings/ci`, `/ci` (cross-repo queue), `/admin/queue`.
   Gate: `TestCIDurableRestart`, `TestCICacheKeyHit`, `TestCIOneAttestationPerJob`, `TestCIBoundedRetry`, `TestCIBootRecoveryA`, `TestCIBootRecoveryB`, `TestCIFinishReplayAndConflict`, `TestCIMatrixExpansionGolden`, `TestSSHSIGFixtureParity`, `TestAgentFlow/ci_attest` (a fake runner; a failed job blocks land with the 409 `conflict` problem), `TestFaultKillNine/ci_attest`.

F6 Runner and sandbox (S4).
   Anchor: John Carmack
   Lands: hand-written `rukie runner probe|register|run|status|rotate` and the hidden `rukie job-exec`, per `ci-runner-sandbox-design.md`.
   - Pull-only leases. Fencing on every attempt-scoped write. 10 s heartbeat. Self-fence at 5 minutes.
   - Rootless podman only (at least 6.1.4, cgroups v2). When podman is missing, exit 78 `config-unavailable` with a named error (O14). Do not add a fallback sandbox.
   - Exact-tree workspace from a per-repo bare mirror. No credential inside the container.
   - Host-visible log files, tailed with digest-checked chunks. Content-addressed artifacts. Cache volumes keyed by trust class. Startup reaper.
   - Runner protocol endpoints (outside the op table): `lease`, `heartbeat`, `log.append`, `artifact.put`, `finish` under `/-/ci/v1/`. url-IA owns the `/-/` plumbing namespace, so this replaces `/_/ci/v1/` of the CI design.
   - The runner is matrix-free.
   Needs: F5. No Ruk row.
   Fixes: O14; DX 15; Design 5.
   UI: `/admin/runners` (registration tokens, labels, drain).
   Gate: `TestRunnerProtocolGolden`, `TestSandboxP01` to `TestSandboxP13` (P1 to P13 of the sandbox design), `TestRunnerCrashMatrix`, `TestRunnerKillAfter200Restart`, `TestRunnerKillRequeuesWithNewFence`, `TestCICacheHitAttestsWithoutRun`, `TestRunnerProbeExit78`.

F7 Intents, leases, and asks (S5).
   Anchor: Christopher Alexander
   Lands: the intents DDL per `review-task-data-model.md` and the ops named in F2.
   - Lease modes: `Enforce` and `Advise`. `intent.lease.create` calls Ruk `claim.acquire` with the intent's declared paths, the mode from `lease_mode`, keyed by the intent public id (CS-R42). Enforce returns `E_CLAIMED`, mapped to problem `lease-conflict` (409) naming holder and expiry. Advise only warns. `intent.lease.update` calls `claim.renew`. `intent.lease.delete` calls `claim.release` with holder identity from live `claim.list`.
   - Asks: `ask.create` with `blocks_claim` flips the intent to `blocked`. A claim then returns `lease-conflict` with `reason` `lease-hold`. `ask.answer.create` is human-only (`triage` or above) and unblocks the lease in the same transaction. An expired ask never answers; it stops blocking.
   - `intent.state.set` to closed evaluates the acceptance contract (`task/acceptance` proof). It denies unless the verdict holds, or the reason is `duplicate` or `wontfix` with maintainer sign-off.
   - Lifecycle events map to the catalogue types `task.created`, `task.claimed`, `task.closed`.
   - The console gains the supersede radar and the cross-repo claims panel (DES-17).
   Needs: F2, F4; CS-R42 (pair commit).
   Fixes: O11, O19; CEO 9.
   UI: `/{o}/{r}/tasks`, `/tasks/new`, `/tasks/{n}`, `POST .../claim|release|comment|state|edit`, `/tasks` (cross-repo queue), `POST /{o}/{r}/asks/{id}/answer`. url-IA names the routes `tasks`; the ops keep the settled noun `intent`.
   Gate: `TestTwoAgentsCannotClaimOneIntent`, `TestLeaseEnforceReturnsClaimed`, `TestLeaseAdviseWarnsOnly`, `TestAskAnswerUnblocksLease`, `TestAskExpiryDoesNotAnswer`, `TestIntentCloseNeedsAcceptance`. Ruk side: claim acquire, renew, and sweep tests.

F8 Webhooks and inbox (S6).
   Anchor: Christopher Alexander
   Lands: package `internal/webhook` per `webhook-design.md`.
   - Standard Webhooks 1.0.0. `webhook-id` is `msg_` plus ULID. `webhook-signature` is `v1,<base64 HMAC-SHA256(id.timestamp.body)>`. Secrets are `whsec_`, sealed under the secret key.
   - The event catalogue of `usage-surface-draft.md` §4.
   - An outbox with a leased worker (5-minute leases), jittered backoff 5 s, 5 m, 30 m, 2 h, 5 h, 10 h, 14 h, 20 h, 24 h, `max_attempts=10` then `dead`, and `Retry-After` overrides.
   - SSRF-safe dialing with a private-range deny list and no redirects.
   - Ops: `webhook.endpoint.create|list|get|update|delete|rotate-secret|test`, `webhook.message.list|get|redeliver`, `webhook.attempt.list`.
   - Inbox per `inbox-design.md`: `inbox.list`, `inbox.get`, `inbox.mark`, `inbox.snooze`, `inbox.count`, `inbox.watch.set`, `inbox.watch.list`, `inbox.settings`. Watch levels, participation rules, write-time fan-out in the same transaction, one thread per `(user, subject)`, states unread, read, done, snoozed, and muted. Rates: `list` 1 qps with burst 5, `count` 2 qps, `mark` and `snooze` 5 qps, over-limit 429. MCP and CLI reach all of them.
   - Freeze webhook schemas per type (CEO-21).
   - Site-wide hooks (`/admin/hooks`) are not shipped. `webhook.endpoint.create` takes scope `repo` or `owner` only.
   Needs: K7, F1, F4; domain events from F2, F3, F5, F7.
   Fixes: O15, O17; DX-16; CEO-21.
   UI: `/inbox`, `/inbox/thread?id=`, `/inbox/settings` with bulk forms that work without JS; `/{o}/{r}/settings/hooks` with `/{id}` and `/{id}/messages/{mid}`; `/{owner}/-/settings/hooks`.
   Gate: `TestAgentFlow/webhooks` (signature, one 500 then a retry with the same id and a 200, the watcher's inbox row), `TestWebhookSignatureStandardRecipe`, `TestWebhookSSRFDenyList`, `TestWebhookNoRedirects`, `TestWebhookSchemasFrozen`, `TestInboxFanOutSameTx`, `TestInboxStatesAndSnooze`, `TestFaultKillNine/webhook`.

F9 Search (S7).
   Anchor: Claude Shannon
   Lands: `search.code`, `search.repos`, `search.users`. `search.code` binds to Ruk `search` (CS-R18). A single-repo query passes the Ruk `PageCursor` verbatim. A multi-repo query fans out per repository and returns a composite cursor that holds one Ruk cursor per repo. Search pages emit no `Link` header. An expired lease returns problem `lease-conflict`, and the client restarts from page one (O20). Skip blobs over 512 KiB. Keep no cache. `search.repos` and `search.users` use FTS5 trigram tables.
   Needs: F1; CS-R18 (pair commit).
   Fixes: O20; DX 16; ENG-47.
   UI: `/explore/code`, `/search` (unified `?type=`), `/{o}/{r}/search`.
   Gate: `TestSearchCodeSingleRepoCursorVerbatim`, `TestSearchExpiredLeaseProblem`, `TestSearchNoLinkHeader`, `TestSearchFanOutCompositeCursor`, `TestPerfBudget` (search p95 at most 500 ms).

F10 SSH (S8).
   Anchor: John Carmack
   Lands: package `internal/gitssh` on `golang.org/x/crypto/ssh` v0.58.0. This is the first and only SSH listener (K6 has none).
   - A public-key callback maps the key fingerprint to a user. The session carries scopes `repo:read` and `repo:write` limited to the user's access. An unknown key gets `Permission denied (publickey)` and an audit row.
   - Exec requests `git-upload-pack '<path>'` and `git-receive-pack '<path>'` run through the same authorization core and daemon streams as K6. Authorize before opening the stream. `refs/for` creates changes synchronously.
   - Idle timeout 10 minutes, keepalive 60 s, `[ssh].max_sessions`.
   - Key types per `auth-design.md` §9. `rukie admin ssh-keygen --out <file>` writes an Ed25519 host key at mode 0600.
   - Ops `auth.sshkey.list`, `auth.sshkey.add`, `auth.sshkey.remove`.
   Needs: F1, K6.
   Fixes: CEO 5; Eng rukie 6.
   UI: `/settings/keys`, `POST .../keys/{id}/delete`, `/{owner}.keys` (text/plain, suffix check inside the owner handler).
   Gate: `TestSSHStockGitCloneAndPush`, `TestSSHUnknownKeyGenericDenied`, `TestSSHKeyScopeCeiling`, `TestSSHRefsForCreatesChange`.

F11 Releases (S9).
   Anchor: Dieter Rams
   Lands: ops `release.list`, `release.get`, `release.create`, `release.update`, `release.delete`, `release.asset.list`, `release.asset.create`, `release.asset.get`, `release.asset.delete`, and `code.archive.get`.
   - A release binds 1:1 to a Ruk tag. Publishing writes the freeze marker through CS-R25. Delete and discard remove it, owner-checked.
   - Notes are Markdown. Assets are content-addressed by SHA-256, with streaming upload under `asset_max_bytes`, `Repr-Digest`, range requests, a draft state, and immutability once published.
   - `code.archive.get` streams tar and zip. ETag is `<commit>:<format>`.
   Needs: F1; CS-R19, CS-R25 (pair commit).
   Fixes: ENG-48.
   UI: `/{o}/{r}/releases`, `/releases/new`, `/releases/latest` (301), `/releases/tag/{name...}`, `/releases/edit/{name...}`, `POST /releases/delete/{name...}`, `/releases/attachments/{uuid}`, `/{o}/{r}/archive/{rev}?format=zip|tar|tar.gz`.
   Gate: `TestReleaseTarballDownloadE2E`, `TestReleaseAssetRangeAndDigest`, `TestReleaseImmutableOncePublished`, `TestReleasedTagFreezeRace` (a `git push` tag bypass is refused).

F12 TOTP and OIDC (S10).
   Anchor: Tony Hoare
   Lands:
   - TOTP: RFC 6238, SHA-1, 6 digits, 30 s, plus or minus 1 step, `last_counter` replay guard, 10 single-use recovery codes, secrets sealed under the secret key. The enrollment shows the `otpauth://` URI as copyable text with no QR image.
   - OIDC: authorization code, PKCE S256, `state`, and `nonce` with `go-oidc/v3` v3.21.0 and `x/oauth2` v0.37.0. Join on `allow_signup`. 20 bad-state callbacks per 10 minutes per IP block that IP for 1 hour.
   - Ops: `auth.totp.verify`, `auth.totp.enroll`, `auth.totp.disable`, `auth.recovery.regenerate`, `auth.oidc.start`, `auth.oidc.callback`, `auth.oidc.link`, `admin.user.totp-reset`.
   Needs: K5, K10.
   Fixes: DES-19.
   UI: `/login/totp`, `/login/oidc/{provider}`, `/login/oidc/{provider}/callback`, and the TOTP and OIDC sections of `/settings/security`.
   Gate: `TestTOTPRFC6238Vectors`, `TestTOTPReplayRejected`, `TestTOTPLockoutKillsPending`, `TestRecoveryCodesSingleUse`, `TestOIDCStubProviderLogin`, `TestOIDCPKCEAndNonce`, `TestOIDCStateAbuseBlock`.

F13 Social and explore (S11).
   Anchor: Dieter Rams
   Lands: ops `star.add`, `star.remove`, `star.list`, `repo.fork.create`, `repo.fork.list`, `explore.repos`, `explore.users`, `explore.orgs`. Watching uses `inbox.watch.set`.
   - `repo.fork.create` follows `fork-social-releases-design.md` §2.3 and calls Ruk `repo.fork` (CS-R24). `default_branch_only` selects refs `default-and-tags`, else `all`. The fork carries state `forking` until the `repo_fork` job ends. A fork cannot be wider than its source.
   - `repo.fork.create` is a session op, or a token op with `repo:create` into the owner's user namespace (O19). Any other destination returns 403.
   - Explore shows public repos to anonymous callers and filters by visibility for others, with keyset pagination.
   - Stars, watches, and forks emit no webhook events.
   Needs: F1, F4; CS-R24 (pair commit).
   Fixes: O19; ENG-49.
   UI: `/explore/repos|users|orgs|topics`, `/explore/topics/{name}`, `/{o}/{r}/stars`, `/watchers`, `/forks`, `/fork`, and the star and watch forms in the repo header.
   Gate: `TestForkAndStarE2E`, `TestExplorePermissionMatrix`, `TestForkVisibilityCap`, `TestForkTokenNamespaceOnly`. Ruk side: `repo_fork_copies_all_heads_and_tags`, the EXDEV fallback, and the staging-crash retry.

F14 Admin surface and closure.
   Anchor: Otl Aicher
   Lands:
   - Admin ops from `usage-surface-draft.md` §3.13: `admin.user.delete`, `admin.repo.list`, `admin.repo.purge`, `admin.job.list`, `admin.job.retry`, `admin.audit.list`, `admin.health.get`.
   - New admin ops (DX-13): `admin.token.list`, `admin.token.create`, `admin.token.revoke`, `admin.repo.verify`, `admin.search.sync`, `admin.mirror.sync`, `admin.mirror.retry`, `admin.attest.verify`, `admin.inbox.test`. There is no `repair` op because no Ruk method backs it.
   - Command spelling: derived admin ops use `rukie admin <sub> <verb>` (for example `rukie admin user create --name ada-agent --password-stdin`). The hand-written `rukie admin ...` commands are `secret-gen`, `ssh-keygen`, `migrate`, `status`, `reconcile`, `gc`, and `repo import`. The reserved-name check of K8 stops any overlap. The `rukie admin user create --login` form of `ops-design.md` is replaced by the derived form. `admin repo fork-local` is not shipped because `repo.fork.create` covers it.
   - `rukie admin repo import --from-git <url|path> --owner <o> --name <n> [--default-branch <b>]` pushes with stock `git push --mirror` to the local endpoint using a one-shot admin token. It verifies HEAD and tag count. It deletes the repo on failure (ENG-50).
   - `rukie admin gc [--dry-run] [--repo <o/r>]`.
   - U5: `TestAllHTMLRoutesRenderWithoutJS` on every route, and a registry test that every op has a page test. U6: the DES-21 sweep: contrast 4.5:1 and 3:1, `focus-visible`, a keyboard walk, targets of at least 24 px, 400 percent reflow, reduced motion, forced colors. WCAG 2.2 AA applies; where 2.4.13 is AAA-only, verify 2.4.7 and 1.4.11 (O27).
   - V8: release-binary perf re-run, `gremlins` weekly on `land`, `mirror`, `auth`, `webhook` (efficacy at least 80 percent), `govulncheck` blocking.
   - url-IA routes `impersonate` and `/admin/config` have no settled op and are not shipped.
   Needs: F1 to F13; CS-R21 for the oplog page.
   Fixes: DX-13, ENG-50, CEO-15; Design 5.
   UI: `/admin`, `/admin/users`, `/admin/users/{id}` with reset-password and disable, `/admin/orgs`, `/admin/repos`, `/admin/audit`, `/admin/monitor`, `/{o}/{r}/-/ops`, `/{o}/{r}/-/claims`, `/{o}/{r}/-/attestations`, `/{o}/{r}/-/refs`.
   Gate: `TestAllHTMLRoutesRenderWithoutJS` (every route), `TestEveryOpHasPageTest`, `TestAdminOpScopes`, `TestAdminRepoImportRollback`, `TestAAChecklist`, `TestPerfBudget` (release binary), gremlins and `govulncheck` clean.

Dogfood gate.
   Anchor: John Carmack
   Lands: the V5 capacity gate. `tests/perf/daemonpath_test.go` holds `TestDaemonPathBudget` and a load harness. With the release `ruk` binary on the same host, it compares daemon-path incremental fetch, single-ref lookup, clone, and `info/refs` against stock `git`. It appends the table to `internal/bench/RESULTS.md`.
   Entry condition: all implementation is landed (O40): the gates of F1 to F14 pass, V5 passes, and the O30 daemon-path budgets pass. `TestDaemonPathBudget` (tag `perf`, release binary) measures incremental fetch at most 7.5 times and single-ref lookup at most 4 times stock git on the same host (CEO-08). The changeset has no Ruk perf row. If a budget fails, stop and report; the fix needs a new Ruk row.
   Host and config:
   - The host is the developer workstation. The instance URL is `http://127.0.0.1:3000`. The config path is `/home/alpha/rewrite/git-renewed/dogfood/rukie.toml`.
   - The config sets `[http] listen = ["127.0.0.1:3000"]`, `data_dir = "/home/alpha/rewrite/git-renewed/dogfood/data"`, and `secret_key_file = "/home/alpha/rewrite/git-renewed/dogfood/secret.key"`.
   - Order of setup: `rukie admin secret-gen --out /home/alpha/rewrite/git-renewed/dogfood/secret.key`; `rukie serve --config /home/alpha/rewrite/git-renewed/dogfood/rukie.toml --check-config`; `rukie bootstrap --config /home/alpha/rewrite/git-renewed/dogfood/rukie.toml --username ada --password-stdin`.
   Accounts and repos:
   - The bootstrap admin is `ada`. `ada` is the approver.
   - Create account `ada-agent` with `rukie admin user create --config <path> --name ada-agent --password-stdin`. Agents push changes with `ada-agent` tokens. G5 forbids the author from approving, so `ada` approves.
   - Create repos `ada/rukie` and `ada/ruk`. Push both trees with stock git over Smart HTTP.
   - The `main` policy requires one trusted human approval and passing `ci/*` proofs (F5 landed before dogfood under O40).
   Landing:
   - Every later commit arrives as a change through `refs/for/main`. It lands through `land.plan.create` plus `land.run.create`, which wrap Ruk `land.prepare` plus `land.execute`.
   GitHub mirror:
   - The mirror to `gosuda/rukie` waits for explicit user consent. Ask the user before the first outward sync. Sync only after an explicit yes.
   - The credential is a fine-grained GitHub token for `gosuda/rukie` with contents write. The user enters it through `mirror.create` on stdin or the form. Rukie seals it. No tool reads it from `gh` or the environment.
   - Mirror defaults: `fast-forward-only` and `default_branch_tags`.
   - Every other outward target needs its own explicit yes. No external user gets access before V7 passes (CEO-25).
   After this gate:
   - The Ruk store format changes only behind an explicit store-version bump.
   - The op table stays `/v0`.
   - Webhook schemas freeze per type at F8.
   Needs: F1 to F14, V5; CS-R22, CS-R28 for the mirror.
   Fixes: O30, O34; CEO 2, 3; Eng rukie 14.
   UI: none new.
   Gate: `TestDaemonPathBudget`, then the F1 to F14 gates re-run against the live instance.

D Docs and readiness (items that need the finished product).
   Anchor: Milton Erickson
   D1 landed with K2 and the pad commit. D2 landed with CS-R01 (CEO-02), the ledger copy, and F5 (override row). D4 landed with K9.
   D3 README and docs IA: build the tree per `docs-readme-draft.md`.
   - The quickstart runs under 5 minutes: `rukie bootstrap`, `rukie auth login`, `rukie auth token create --scopes repo:create,repo:read,repo:write`, `rukie repo create`, stock `git push`.
   - Also tutorials, how-to guides, the generated reference, the concepts of Intent, Proof, View, and Lease, an agent guide, and an operator guide with the offline key backup (O36).
   - Ship `examples/local.toml` and the env file.
   - Build `cmd/rukie-doccheck` (DX 10) so the README commands `./bin/rukie-doccheck measure ...` run as written. It runs every README and quickstart example and the scenario files. Every number comes from `internal/bench/RESULTS.md`. The method count is 61.
   D5 changelog and policy: `CHANGELOG.md` lists shipped changes only. Document `/v0` versioning, the deprecation rule (`Sunset`, a changelog entry, a dual-run window), and the licence provenance line that no file is ported from `ruk/forgejo` (CEO-11).
   D6 publish gates: isolated install run, examples captured, links resolved, no numeric placeholders, README claims match source in both repos (CEO-09, CEO-10). V7 security review report (token scopes, CI sandbox, mirror credentials, webhook signing, `threat-model.md`) with fixes landed (CEO-25). `gh repo edit` description and topics only after explicit user consent.
   Needs: F14, V5, V7.
   Fixes: DX 10; CEO 10; Pins 2.
   UI: none.
   D7 task goals: replace the body of each `.agent-tasks/task-0e2cc1171807/tests/sc_0N_test.go` with the check that its `GOALS.md` criterion names. Run each check as a subprocess from the module root.
   Gate: `TestDocCheckScenarios`, `TestReadmeNumbersMeasured`, `TestChangelogMentionsOnlyShippedOps`, the D6 checklist, and `make goals` exits 0.

## Critical files and anchors

- `ruk/crates/ruk-core/src/protocol.rs`: the method table (`:3286-3332`, 47 rows), the `required` array (`:5432-5478`), the consistency test (`:5417-5484`), `SCHEMA_MINOR` (`:68`, value 1). Every schema change lands here first.
- `ruk/crates/ruk/src/daemon/mod.rs`: `InvocationOpen` (`:305`), `CommandOpen` (`:390`), `OutcomeLedger` (`:540`), `check_env_path_under_roots` (`:1589`). CS-R07 and CS-R10 replace the last three.
- `ruk/crates/ruk-core/src/coord/prepare.rs`: `land.execute` refusal (`:296-301`) and the restricted `land_prepare_parts` (`:3447-3510`).
- `rukie/internal/op/table.go`: the one operation registry. Every surface derives from it.
- `rukie/docs/plan/`: the corpus. Resolve disputes through `Canonical sources`, then `Overrides`.

## Verification

### Gates

Green means every command below exits 0 after every commit, once its suite has at least one test. Run the Ruk gates in `/home/alpha/rewrite/git-renewed/ruk`. Run the rukie gates in `/home/alpha/rewrite/git-renewed/rukie`. `passgate` fails on a non-zero exit, on a missing PASS needle, and on a run with zero matching tests.

```bash
# Ruk
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --all-features --locked -- -D warnings
cargo nextest run --workspace --locked
cargo test --workspace --locked --doc

# rukie: vet (untagged and all tags), lint, tests, govulncheck, gofmt, go mod tidy -diff, go mod verify
# The ENG-63/64 scaffold adds `build-web` (npm ci + rescript build + esbuild into
# internal/web/assets) and makes `check` depend on it; from that commit the gate
# runs `make build-web` first so `go build` always sees the generated bundle (O41).
make check

# rukie integration (real daemon), from K2 on
cargo build --manifest-path ../ruk/Cargo.toml --locked -p ruk
env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk RUK_COORDINATOR=off \
  tools/passgate.sh '--- PASS: ' -- go test -race -count=1 -v -tags integration ./...

# rukie end to end, from K6 on
env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk RUK_COORDINATOR=off GIT_EXEC="$(command -v git)" \
  tools/passgate.sh '--- PASS: ' -- go test -race -count=1 -v -tags e2e ./tests/e2e/

# rukie render validity, from the first HTML page on
env RUKIE_RENDER_DIR="$PWD/.rendered" \
  tools/passgate.sh '--- PASS: TestDumpRenderedPages (' -- go test -count=1 -v -tags render -run '^TestDumpRenderedPages$' ./internal/web/
test "$(ls .rendered/*.html 2>/dev/null | wc -l)" -gt 0
npm exec --yes --package=html-validate@11.16.4 -- html-validate --config .htmlvalidate.json --max-warnings 0 .rendered/*.html
```

Performance runs only at the gates of F1, F2, F3, the Dogfood gate, and D: build `ruk` with `--release`, then run `env RUK_BIN=/home/alpha/rewrite/git-renewed/ruk/target/release/ruk tools/passgate.sh '--- PASS: TestPerfBudget (' -- go test -count=1 -tags perf -timeout 30m -v -run '^TestPerfBudget$' ./internal/bench/`. No `-race`. The budgets are O30.

A Ruk commit that changes the method table pairs with a rukie commit. The rukie commit bumps the pinned Ruk commit, regenerates `internal/ruk/testdata/ruk-model.json` with `ruk model --format json > internal/ruk/testdata/ruk-model.json`, and passes the drift gates. Both commits are green before the pair counts as landed.

### New-behavior checks

Each check runs after the named step. `$RUK` is `/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk`.

1. After K2. cwd `/home/alpha/rewrite/git-renewed/ruk`, env `RUK_COORDINATOR=off`.
   Command: `cargo nextest run --workspace --locked -E 'test(forgejo_subcommand_is_rejected)'`
   Pass: nextest reports `1 passed`. The test asserts that `ruk forgejo serve` exits 2 through clap and that `ruk serve --help` lists `--config`.
2. After F1. cwd `/home/alpha/rewrite/git-renewed/rukie`, env `RUK_BIN=$RUK GIT_EXEC=$(command -v git) RUK_COORDINATOR=off`.
   Command: `tools/passgate.sh '--- PASS: TestAgentFlow/create (' '--- PASS: TestAgentFlow/push (' -- go test -race -count=1 -tags e2e -v -run '^TestAgentFlow$/^(create|push)$' ./tests/e2e/`
   Pass: both PASS lines print and the command exits 0. The `push` subtest pushes `main` to `http://ada:<token>@127.0.0.1:<port>/ada/hello.git` with stock git and asserts a `ref.updated` outbox row for `refs/heads/main` within 2 s (one poll window with `wait_ms` 1000).
3. After K4. cwd `/home/alpha/rewrite/git-renewed/rukie`, env `RUK_COORDINATOR=off`.
   Command: `tools/passgate.sh '--- PASS: TestNegotiationMatrix (' '--- PASS: TestAllHTMLRoutesRenderWithoutJS (' '--- PASS: TestTableRejectsDuplicateName (' -- go test -race -count=1 -v -run '^(TestNegotiationMatrix|TestAllHTMLRoutesRenderWithoutJS|TestTableRejectsDuplicateName)$' ./internal/op/ ./internal/web/...`
   Pass: three PASS lines. The matrix shows one URL returns HTML for `Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8`, JSON for `application/json`, `*/*`, and an absent `Accept`, and Markdown for `text/markdown`. A table with a duplicate op name fails startup.
4. After F2. cwd `/home/alpha/rewrite/git-renewed/rukie`, env `RUK_BIN=$RUK GIT_EXEC=$(command -v git) RUK_COORDINATOR=off`.
   Command: `tools/passgate.sh '--- PASS: TestAgentFlow/change (' '--- PASS: TestAgentFlow/land (' -- go test -race -count=1 -tags e2e -v -run '^TestAgentFlow$/^(change|land)$' ./tests/e2e/`
   Pass: both PASS lines. `ada-agent` pushes `HEAD:refs/for/main` with stock git and opens a change. A comment on a line remaps after the author rewrites the change to a new head. `ada` approves, which satisfies the default policy. `land.plan.create` plus `land.run.create` land the change. A second `land.run.create` of the same plan after drift returns `stale` (`E_STALE`). The CI proof joins the policy in check 5.
5. After F6. cwd `/home/alpha/rewrite/git-renewed/rukie`, env `RUK_BIN=$RUK RUK_COORDINATOR=off`.
   Command: `tools/passgate.sh '--- PASS: TestRunnerKillRequeuesWithNewFence (' '--- PASS: TestCIFinishReplayAndConflict (' '--- PASS: TestCICacheHitAttestsWithoutRun (' -- go test -race -count=1 -tags integration -v -run '^(TestRunnerKillRequeuesWithNewFence|TestCIFinishReplayAndConflict|TestCICacheHitAttestsWithoutRun)$' ./internal/ci/...`
   Pass: three PASS lines. After a mid-job kill the lease expires and the job requeues with a new fencing token. The old runner's late writes are refused with `E_LEASE_LOST`. A repeated `finish` with the same digest returns the stored reply. A repeated `finish` with a different digest returns `E_RESULT_CONFLICT`. A cache hit records a passing attestation without a run.
6. After F3. cwd `/home/alpha/rewrite/git-renewed/rukie`, env `RUK_BIN=$RUK GIT_EXEC=$(command -v git) RUK_COORDINATOR=off`.
   Command: `tools/passgate.sh '--- PASS: TestAgentFlow/mirror (' '--- PASS: TestAgentFlow/mirror/stops_on_401 (' -- go test -race -count=1 -tags e2e -v -run '^TestAgentFlow$/^mirror$' ./tests/e2e/`
   Pass: both PASS lines plus the four scenario subtests. The fake host is `git http-backend` over Smart HTTP. `exact` force-updates and prunes. `fast-forward-only` records `diverged` and does not overwrite. A 401 stops the mirror with a named error. The webhook clause of the old check moved to check 7.
7. After F8. cwd `/home/alpha/rewrite/git-renewed/rukie`, env `RUK_BIN=$RUK GIT_EXEC=$(command -v git) RUK_COORDINATOR=off`.
   Command: `tools/passgate.sh '--- PASS: TestAgentFlow/create (' '--- PASS: TestAgentFlow/push (' '--- PASS: TestAgentFlow/change (' '--- PASS: TestAgentFlow/ci_attest (' '--- PASS: TestAgentFlow/land (' '--- PASS: TestAgentFlow/mirror (' '--- PASS: TestAgentFlow/webhooks (' -- go test -race -count=1 -tags e2e -v -run '^TestAgentFlow$' ./tests/e2e/`
   Pass: seven PASS lines. The run uses these exact spellings against a fresh instance at `http://127.0.0.1:3000`:
   - `rukie bootstrap --config "$CFG" --username ada --password-stdin` (hand-written local setup command, not op-derived).
   - `rukie auth login --server http://127.0.0.1:3000 --username ada --session-file "$S"`, with the password on stdin.
   - `rukie auth token create --server http://127.0.0.1:3000 --session-file "$S" --name agent --scopes repo:create,repo:read,repo:write,intents:read --expires-at <rfc3339> --json`. Token minting is a session op with fresh auth.
   - `rukie repo create --server http://127.0.0.1:3000 --token-file "$T" --owner ada --name hello --json`. The token carries `repo:create` and the destination is its owner's namespace.
   - `git push` of `main` and of `HEAD:refs/for/main` over Smart HTTP with Basic auth. Git push travels the git wire, not the op table.
   - An MCP `tools/call` of `change_list` and then `change_review_create` (`model=true`) as POST `/-/mcp` with `Authorization: Bearer <token>`.
   - CI attests through the runner.
   - A human approval in the fallback-rendered UI.
   - `rukie land plan create --owner ada --repo hello --change <cid> --json` then `rukie land run create --owner ada --repo hello --plan <id> --json`, with the change pushed by `ada-agent` and approved by `ada`.
   - The mirror updates on the fake host.
   - A webhook delivery verifies with the Standard Webhooks recipe, and the watcher's inbox row appears.
   Every op-derived step uses the one operation table. The git wire, `bootstrap`, `runner`, `job-exec`, `backup`, `restore`, and `dev` are the hand-written exceptions.

## Assumptions and contingencies

- Assumption: one host runs the deployment. This drove SQLite and the supervised daemon. If multi-host becomes a requirement, add a PostgreSQL adapter to the store package then.
- Assumption: agents use tokens on human accounts. If per-agent identity becomes a requirement, add an `agents` table and keep tokens. Do not add Ruk principals.
- Assumption: webfonts load from Bunny Fonts on every page. Each page view sends the viewer's address to `fonts.bunny.net`. If the user later forbids third-party requests, delete the font `<link>` and the two CSP entries. The fallback stacks stay.
- Contingency: if the corpus directory `C` is missing at Step 0, stop and report.
- Contingency: if `cargo clippy ... -D warnings` fails on lints that predate this plan, fix them in the first Ruk commit. If a fix needs a design change, stop and report.
- Contingency: if the CS-R13 hosted graph index exceeds its slice, ship browsing on `file`, `diff`, `show`, and `blame` first and land `log` pagination after. The skeleton does not block on it.
- Contingency: if rootless podman proves unstable on the CI host, keep the runner refusal (exit 78, `config-unavailable`). The control plane marks required jobs with `effects != none` as policy-skipped. Do not add a fallback sandbox.
- Contingency: if `events.tail` long-poll latency fails its budget, add a daemon-side wait queue. Do not add `MergeNotices` back.
- Deferred: a per-account token bucket (ENG-39). K4 ships per-token (300 per minute, burst 600) and per-IP buckets from the corpus. The corpus gives no per-account rate. Add the bucket when a measured rate exists.
- Deferred: the last commit per tree entry (`code.tree.get` with `with_last_commit`). `ruk-changeset.md` §4 lists it with no Ruk row. F1 ships the tree without it.
- Contingency: if `TestDaemonPathBudget` fails at the Dogfood gate, stop and report. The fix needs a new Ruk row; the changeset has no daemon-path performance row.
- Approval of this plan approves exactly: the three Step 0 commits; local commits on rukie on top of the Gitea history and on ruk on top of ruk `main`; and local work only. Each outward act needs its own consent at the time it happens: any `git push`; creating or editing `gosuda/rukie` (repository, description, topics); the first sync of the dogfood GitHub mirror; any webhook, mirror, or OIDC test against a real third-party host; any release or package publish. Tests dial only local fakes.

## Autoplan decision register

Round 1 registers live in `docs/plan/autoplan-ceo.md`, `autoplan-design.md`, `autoplan-dx.md`, and `autoplan-eng.md`. Round 2 (2026-10-20) reports live in `docs/plan/review-ceo.md`, `review-design.md`, `review-dx.md`, `review-eng-ruk.md`, `review-eng-rukie.md`, `review-pins.md`, and `review-ledger-trace.md`.

| Phase | Round 2 outcome (2026-10-20) |
|---|---|
| CEO | Build order is dogfood early (user). Step 0 keeps its corpus and gate. Approval lists each outward act. SSH moves to F10, TOTP and OIDC to F12, orgs and teams to F4. The Dogfood gate names its host, accounts, policy, and consent. |
| Design | Tokens match the Instrument doc: spacing ends at 48, motion is 150 ms on six named properties. Webfonts come from Bunny Fonts (user). The WCAG rule, meta refresh, and the UI surfaces of each step are explicit. html-validate stays (user). Round 3 (2026-10-21): the UI language is ReScript on Preact via the generic JSX transform; the Go fallback renders baseline HTML; `trusted`/`htmlcheck` stay as the untrusted-content boundary. |
| DX | One op lexicon: usage-surface names, closed verbs, seven added domains. One error catalogue, strong ETags, 24 h idempotency keys, JSON as the default for `*/*`, a CLI session file, and `/-/mcp` with a 36-tool default budget. Scopes gain `repo:create` (user) and rename `tasks:*` to `intents:*`. |
| Engineering | Ruk work follows changeset rows CS-R01 to CS-R48 with code-grounded corrections: no `heads-since`, direct publish for receive-pack, `TransportAuth`, the restricted `land.prepare`, 47 to 61 methods. rukie gains the K7 spool and reconciler, supervisor caps, the secret-key backup rule, git-wire authorization, and the store cancellation rule. Pins are verified; lint and govulncheck run through `go run`. Strict validation covers rukie. Round 3 (2026-10-21): `web/` scaffolds with the generic-JSX Preact interface and esbuild-to-`go:embed`; `zerojs` is deleted, `trusted`/`htmlcheck` retained with the CodeQL `data:` fix; dogfood moves after F14 (O40). |

| Phase | Round 3 outcome (2026-10-21) |
|---|---|
| CEO | UI stack is ReScript-only, embedded bundle, no second runtime in production; dogfood deferred until all implementation lands (O40). |
| Design | Instrument tokens unchanged; interactive layer is Preact hydrating marked subtrees; `trusted`/`htmlcheck` remain the untrusted-content boundary. |
| DX | App consumes the op table JSON only; no fragment API, no client router; `/-/api` and llms.txt unchanged. |
| Engineering | `web/` + `make build-web` before `go build`; new gates `TestAllHTMLRoutesRenderWithoutJS`, `TestNoJSFallbackMutationsPost`, `TestEmbeddedAppBundle`, `TestNoJSAppSource`. |

The ledger lives at `rukie/tasks/autoplan-ledger.jsonl` after Step 0. Round 1 wrote 117 rows. Round 2 appends CEO-26, CEO-27, DES-22 to DES-24, DX-19 to DX-22, and ENG-54 to ENG-62. Rows ENG-53 and CEO-19 are resolved by the Metadata store and Architecture decisions. The ledger has no status field; this register records resolutions.

### Ledger disposition (round 1 orphans and conflicts)

- CEO-02: step K2 (the CS-R01 commit marks `AGENT_NATIVE_GIT_PLAN.md:92-96` and the old `ruk/tasks` rows superseded).
- CEO-05: superseded by the stream model: streams R, K, F, U, V, D interleave, and Ruk rows land in the schedule table, not in a separate phase.
- CEO-06: step F1 (`TestAgentFlow/create` and `/push` stay permanent).
- CEO-08: step Dogfood gate (`TestDaemonPathBudget`).
- CEO-11: superseded by O2 (K2 is hand-written, so no GPL Ruk code enters rukie); D5 records the provenance line.
- CEO-12: step K4 (`Views` registry with per-op hand-authored pages).
- CEO-15: step F1 for symrefs and bounded ref lists (CS-R14, CS-R16 in K7); step F14 for the reflog page (CS-R21); shallow fetch and LFS sync deferred, no settled consumer (changeset §4).
- CEO-21: step K4 (`/v0`, `Sunset`), step F8 (schema freeze per type), D5 (changelog policy).
- CEO-25: step D (V7 security review blocks external access).
- DES-01: superseded by the settled Instrument design decision and O25.
- DES-05: step K4 (derived forms) and each F step (hand-authored core surfaces).
- DX-08: step K9.
- DX-09: step K4.
- DX-11: step K9.
- DX-13: step F14.
- DX-15: step K4.
- DX-17: step K4 (mechanism) and D5 (policy text).
- DX-18: step K4 (extended in K9).
- ENG-02: step K2 (CS-R01 adds `rukie/` to `ruk/.gitignore`).
- ENG-06: superseded by the settled `SO_PEERCRED` single-peer decision (no host-credential file).
- ENG-07: superseded by the settled `SO_PEERCRED` single-peer decision (no HMAC challenge).
- ENG-10: deferred, CS-R20 caps `wait_ms` at 30000, under the 60 s frame timeout, and no subscription class has a consumer.
- ENG-11: deferred, no rukie caller needs a public `ExecutorLane`.
- ENG-20: step F3 (CS-R28 push and `push.reconcile`) and step F2 (CS-R43 `land.execute`); `bookmark.*` and `tag.*` are already served; `fetch`, `unpin`, and `op.prune` deferred (changeset §4).
- ENG-21: step K7 (`TestServiceReceiptsSurviveDaemonKill`).
- ENG-23: step K2 (the harness fails, never skips).
- ENG-31: step K6 (`TestNoGitProcessInBoundary`).
- ENG-39: step K4 (per-token and per-IP buckets); the per-account bucket is under `Needs user yes`.
- ENG-44: superseded by changeset D10 (kind 9 `MergeNotices` retired in CS-R04).
- ENG-50: step F14 (`rukie admin repo import`).
- ENG-52: step K7 (push), step F3 (mirror), step F5 (CI attest), step F8 (webhook), as `TestFaultKillNine/<path>`; Ruk crash smoke in the cluster C gate.

