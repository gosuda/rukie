# Rukie Rewrite — DX Review (autoplan-dx.md)

## 1. Scope and method

This phase reviews **developer experience (human) and agent experience (AI)** for the planned
Rukie rewrite (agent-first Go forge on Ruk) against the goal, settled decisions, and draft
skeleton (Phases R/K/F/U/V). It does not reopen settled decisions: cut every Gitea surface
(API v1, swagger, app.ini, xorm, Actions, packages, OAuth2 provider, webhook formats, builtin
SSH, gitea CLI, Vue/Vite/Tailwind, locales) keeping only `/{owner}/{repo}.git`; land as
commits on Gitea history with every commit building; Ruk-only backend; heavy-agentic,
zero-JS, optimized-Go, best-interfaces constraints.

Grounding (read-only, cited):

- Ruk MCP tool table: 12 tools — 9 change ops + 3 reads — in
  `ruk/crates/ruk/src/mcp/tools.rs` (`TOOL_DESCRIBE_CHANGE`, `TOOL_NEW_COMMIT`,
  `TOOL_EDIT`/`SPLIT`/`SQUASH`/`REBASE`/`ABANDON`/`RESOLVE`/`REVERT`,
  `TOOL_REVSET_EVAL`, `TOOL_REFS_LIST`, `TOOL_ATTESTATION_QUERY`). Arguments decode
  directly into existing `ruk_core::ops` / `protocol::QueryArgs` /
  `protocol::PageRequest` / `protocol::AttestListArgs` types; mismatches are errors,
  never coerced.
- MCP stdio server in `ruk/crates/ruk/src/mcp/server.rs`: newline-delimited JSON-RPC 2.0,
  methods `initialize`, `tools/list`, `tools/call`, `ping` only; protocol version
  `2024-11-05`; `E_INVALID`-class failures → `INVALID_PARAMS` with `data.core_code`,
  other `CoreError`s → `isError: true` text starting with the code; framing bounded by
  `MAX_CONTROL_FRAME_BYTES` where one oversized line is answered with `E_LIMIT`.
- Local-degraded semantics documented in `ruk/crates/ruk/src/mcp/mod.rs`:
  `revset_eval` always `E_UNSUPPORTED_OPERATION`, `refs_list` pages without cursor
  binding, attestation query uses allow-all checkpoint. Lesson for Rukie: MCP tools MUST
  NOT silently degrade — either wire the coordinator/daemon path or do not expose the tool.
- Ruk error taxonomy in `/tmp/ruk-model.json`: `E_STALE`, `E_LEASE_LOST`,
  `E_REQUEST_REUSED`, `E_CONFLICTED`, `E_DIVERGENT`, `E_PUSH_REJECTED`,
  `E_PUSH_UNCERTAIN`, `E_CLAIMED`, `E_FORBIDDEN`, `E_POLICY`, `E_NOT_FOUND`,
  `E_INVALID`, `E_LIMIT`, `E_VERSION`, `E_DIRTY`, `E_CANCELLED`, `E_CORRUPT`, etc.,
  each with stable `exit_status` (0–5). Seed for CLI exit codes and HTTP↔MCP mapping.
- Pagination defaults in `/tmp/ruk-model.json` `limits`: `page_default` 100,
  `page_max` 1000. No keyset-cursor, `Link`-header, ETag, or idempotency convention
  exists yet — all are greenfield DX contracts proposed here, not existing behavior.
- Daemon reality in `ruk/crates/ruk/src/daemon/mod.rs` + `ipc.rs` + `registry.rs`:
  today `ruk forgejo serve` (Unix socket, TOML config with socket path/roots/runtime
  bounds, `InvocationOpen{repo, method}` + git-shaped `CommandOpen{session_id,
  repository, request_id, argv, dir, env, command_digest, *_credit}`, idempotent
  reconnect via `request_id` + digest mismatch refusal, credit windows
  256 KiB initial / 16 MiB max, features `credits, merge-notices, outcome-lookup`).
  R1 (`ruk serve`, Forgejo-neutral) is the DX seam: Rukie needs a stable, documented,
  versioned daemon client, not a copy of Forgejo residue.
- Ruk CLI in `ruk/crates/ruk/src/main.rs` (clap `Parser`/`Subcommand`): verbs `model`,
  `encode-conflict`, `forgejo serve`, MCP serve; `--input -` means stdin. This
  `--input -` + stdout-bytes convention is the ergonomics precedent the generated
  `rukie` CLI should keep.
- Rukie starting point in `rukie/`: module `gitea.dev`, go 1.27, chi v5,
  `kin-openapi`, **no Huma v2, no MCP go-sdk, no `Op` table** (`rukie/go.mod`).
  Routers in `rukie/routers/`, services in `rukie/services/`, templates in
  `rukie/templates/`, `web_src/` JS + Tailwind/Vite — all scheduled for deletion.
  This review guards the *new* surface and warns against re-importing Gitea DX failure
  modes (`app.ini` sprawl, N auth paths, swagger drift, xorm coupling).

Task-scoped ID note: DX-01…DX-18 below are scoped to `phase: DX` for this
`autoplan-dx.md` deliverable per its assignment contract. If any other ledger uses a
`DX-` prefix for a different scope, disambiguate by `(phase, id)`, not by renaming
tasks in this file.

## 2. DX findings by area

### 2.1 MCP tool set size and naming

12 Ruk tools today is healthy; the risk is Rukie exposing 80+ fine-grained ops (one per
HTTP route) and collapsing agent planning. Finding: cap the MCP surface explicitly as a
budget heuristic ([INFERENCE] — not a measured result in this repo: agents tend to get
less reliable past roughly 30–40 tools, so ≤36 is a heuristic budget, not an evidenced
threshold), compose aggressively (one `change_get` with `include` flags beats
`change_get` + `change_files` + `change_checks` + `change_comments`), fix naming once:
flat `snake_case`, `<noun>_<verb>` (`repo_create`, `task_claim`, `change_list`), close
enough to Ruk's `describe_change`/`new_commit` family to transfer intuition but
namespaced per domain so `task_list` and `change_list` do not collide in embedding
space. Every tool sets the real MCP `ToolAnnotations` fields — `title`,
`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint` — plus a verb-first
one-sentence `description`, and a stable-name contract (rename = breaking change with
alias + `Sunset`).

### 2.2 JSON API conventions (proposed contract)

No conventions exist yet; highest-leverage DX decision because HTTP, MCP, and CLI all
derive from the Op table. Finding: adopt one envelope and never vary it — `GET` lists
`{data, page:{next_cursor, prev_cursor, limit}}` plus RFC 8288
`Link: <…>; rel="next"` (and `prev`); cursors are **opaque, replayable within expiry,
bound to the query fingerprint** (sort/filter/scope/view) — never single-use, since
clients retry failures and re-click `Link` URLs; a cursor replayed with a different
query is rejected, an expired cursor returns `410 Gone` with a relist recovery, and
replay of the same cursor URL is safe and idempotent. Errors exclusively RFC 9457
`application/problem+json` (`type` URI per Ruk `E_*` code, `title`, `status`,
`detail`, `code`, `trace_id`, `recovery`). Optimistic concurrency via `ETag` on
`GET` for tasks, changes, releases, mirrors; `If-Match` required on mutating ops for
those resources (`412 Precondition Failed` on stale tag, `428 Precondition Required`
when the tag is missing); document both. `Idempotency-Key` required on all mutating
ops with explicit validation (missing/invalid key → `400`; reused key with a different
request digest → `422`, never executed twice) and honoured end-to-end by passing it
through as the daemon `request_id` (the existing replay driver in
`ruk/crates/ruk/src/daemon/mod.rs` `CommandOpen{request_id, command_digest}`).
Timestamps RFC 3339 UTC, IDs ULID/string, sort `?sort=-updated_at` allowlisted, filter
`?q=` only where Ruk search supports it. Document the Ruk `E_*` → HTTP status mapping
once (`E_NOT_FOUND`→404, `E_FORBIDDEN`/`E_POLICY`→403, `E_CLAIMED`/`E_LEASE_LOST`/
`E_STALE`→409, `E_INVALID`→400/422, `E_LIMIT`→429+`Retry-After` for rate/quota pressure
and →413 for oversized payloads/frames — cf. `mcp/server.rs` answering one line past
`MAX_CONTROL_FRAME_BYTES` with `E_LIMIT` — `E_VERSION`→409/412, `E_CORRUPT`→500,
`E_IO`/`E_OFFLINE`→503).

### 2.3 Generated CLI ergonomics

Settled: generated `rukie <op> --json`. Finding: generation must guarantee five
invariants or agents will route around the CLI: (a) `rukie <op> --help` shows the same
schema as OpenAPI/MCP (one source, the Op table); (b) `--json` prints a stable,
sorted-key, newline-terminated JSON document on stdout and NOTHING else (logs/hints to
stderr); human mode is a lossy projection, never the parse target; (c) input via
`--in -|@file` (stdin default when piped, mirroring Ruk's `--input -`), `--set
key=value` for small patches; (d) exit codes mirror the Ruk `exit_status` taxonomy
from `/tmp/ruk-model.json` (0 ok, 1 invalid/not-found, 2 conflict/divergent/dirty, 3
stale/lease/forbidden, 4 transport/push/offline, 5 corrupt) plus documented 64/66/77
for usage/unavailable where the taxonomy is silent — one table, asserted by a parity
test; (e) `--dry-run`, `--trace-id`, `--idempotency-key`, `--wait` (long ops: CI,
mirrors, land) behave identically on every mutating op. CLI talks to the same HTTP
surface (or Unix socket), never a second code path.

### 2.4 Token scopes

Settled: scoped tokens on human accounts, no agent principals, argon2id+TOTP or OIDC.
Finding: scopes are the agent permission model — small, mnemonic, deny-by-default:
`repo:read|write`, `change:read|write|land`, `task:read|claim`, `ci:read|trigger|admin`,
`mirror:read|run`, `release:read|write`, `org:read|admin`, `admin:*`, each with
`expires_in`, optional `repo:`/`org:` confinement and `cidr:` bind. `token_mint`
requires the minter to already hold every granted scope (no escalation); agent mints
are short-lived by default (hours, not months); every 401/403 names the missing scope
plus the exact mint command (see 2.7). Long-lived tokens exist only for mirrors/runners
with audience restriction.

### 2.5 Content negotiation (text/markdown alternates for agents)

Agents waste tokens on HTML and hallucinate against truncated JSON. Finding: every read
op serves `Accept: application/json` (default), `text/markdown`, `text/plain`, with a
`?format=` escape hatch for `curl`/CLI; HTML remains the human default in browsers
only. Markdown projections are generated from the same Op schema (front-matter +
sections), deterministic and diffable — code browsing, diff, blame, task, change, and
CI-log reads MUST all have markdown alternates. `Vary: Accept` + `Content-Location`
set correctly so caches do not poison agents with HTML.

### 2.6 llms.txt

Finding: generate, never hand-write. `GET /llms.txt` (tight index: what Rukie is, auth,
top 20 ops, links), `GET /llms-full.txt` (every op: name, one-liner, args, scopes,
idempotency, OpenAPI anchors), plus per-resource `*.md` alternates from 2.5 — all
rendered from the Op table at build/test time with a freshness test (spec drift = red
CI). Include an explicit "agent start here" path: `auth → repo_resolve →
change_list|task_list → claim → edit via Ruk → attest → land`, because the end-to-end
agent flow (Phase V) is the llms.txt acceptance test.

### 2.7 Error messages that name recovery

Ruk's typed `E_*` codes are good raw material; bare 500s/"internal error" are the
anti-pattern. Finding: every error names **what happened, why, what to do next, and
where it is tracked**: problem body carries `recovery` (exact next command, e.g.
`rukie task claim --task …`), `docs` anchor, and `trace_id`; CLI prints
`error E_LEASE_LOST: task claimed by <holder>, expires <ts>. Retry: <cmd>. See: <url>
(trace <id>)` — never just the code. Stale/lease/conflict errors MUST include
`current_etag`, `holder`, `expires_at`, `merge_base`/`divergence` where known, because
agents cannot recover from "conflict" without the numbers. `410` cursor-expired and
`422` idempotency-digest-mismatch errors likewise carry the exact relist/retry command.

### 2.8 Discoverability (OpenAPI rendered server-side)

Settled: Huma v2 → OpenAPI 3.1, zero-JS HTML. Finding: the OpenAPI document IS the docs
— serve it server-rendered (`/api/openapi.json`, `/docs` HTML with zero JS, per-op
`/docs/ops/<name>` reusing the app templates), with `?q=` server-side filter (no client
JS), HTML `<form>` try-it for safe methods, and copy-paste `curl` / `rukie` / MCP-JSON
equivalents on every op page. No separate hand-written API guide that can drift; the
parity gate (§4 DX-18) enforces it.

### 2.9 Local development loop (one command: rukie + ruk daemon)

Today running anything requires a Gitea checkout plus a Forgejo-coupled daemon config.
Finding: `go run ./cmd/rukie dev` (one command) MUST bring up an ephemeral Ruk daemon
(`ruk serve` on a temp Unix socket, temp roots, fixed seed), `rukie serve` against it,
seeded demo org/repo/tasks/changes, printed URLs + admin credentials + token, and `dev
reset`/`dev seed` subcommands. No Docker, no Postgres, no email, no OAuth round-trip in
the default path. The contract is *one command to first green agent flow*, timed in CI.

### 2.10 Operator DX (config file, admin CLI)

Finding: one config file (TOML, single documented path + `RUKIE_CONFIG`), `RUKIE_*` env
overrides with a printed precedence table, `rukie config check` failing fast with
line-numbered hints (especially socket path, roots canonicalization — the exact
`Registry::new` failure modes in `daemon/registry.rs` — and store DSN), and an admin CLI
covering the 3 a.m. cases without email or UI: `admin user reset-password`,
`admin token mint|revoke|list`, `admin repo verify|repair|reindex-search`,
`admin mirror run|retry|status`, `admin attest verify`, `admin inbox send-test`.
First boot prints the admin password once and forces rotation; every admin action is an
Op too (auditable, MCP-gated, scope-checked), not a side channel.

## 3. Six-principle register

Columns: Clarity (usable without asking?), Impact (flow throughput moved?), Audience
(who benefits first?), Risk (what breaks if wrong?), Sequencing (when must it land?),
Reversibility (cost of changing later?). Verdicts: adopt / adopt-with-guard / defer.

| # | Decision | Clarity | Impact | Audience | Risk | Sequencing | Reversibility | Verdict |
|---|----------|---------|--------|----------|------|------------|---------------|---------|
| D1 | One `Op` table derives HTTP+MCP+CLI+HTML; no hand-written second surface | High: one name, one schema everywhere | Very high: kills triple-drift | Agents + CLI scripters first, humans second | Medium: table mistakes propagate to all four surfaces | First (K2); everything else hangs off it | Low: renames break all clients; version + alias discipline required | Adopt with guard (DX-01, DX-18) |
| D2 | RFC 9457 problem envelope + `recovery` + `trace_id` on every error | High: uniform, actionable | High: unblocks autonomous retry | Agents first | Low: additive field; risk is verbose-but-useless text | With Op core (K2), before features | High: fields additive; type URIs versioned | Adopt (DX-02, DX-09) |
| D3 | Replayable opaque keyset cursors bound to query + expiry, with `Link` headers | High: `Link` URLs just work on retry/re-click | High: listing correctness at scale | Agents paging CI logs/search | Medium: binding bugs leak across queries; expiry handling must be taught | With first list op (repos/changes/tasks) | Medium: cursor format opaque → replaceable; header contract sticky | Adopt with guard (DX-02; mismatch→400/422, expired→410 + relist recovery) |
| D4 | `ETag`/`If-Match` on tasks, changes, releases, mirrors; `412`/`428` documented | Medium: conditional-write load | High: prevents lost-update claim/land races | Concurrent agents | Low-medium: 412 handling must be taught | With tasks + review (F) | Medium: weak→strong ETag path | Adopt (DX-02) |
| D5 | `Idempotency-Key` required on mutations, validated, daemon `request_id` passthrough | Medium: key discipline is new | Very high: safe retries for CI/mirror/land | Agents + runners | Medium: key-scope bugs cause false dedup or double-apply | With Op mutations (K2), before CI/mirrors | Low-medium: scope/digest rule versioned | Adopt with guard (DX-02; digest rule from `CommandOpen.command_digest`) |
| D6 | MCP budget ≤36 tools (heuristic, see §2.1), `<noun>_<verb>`, real `ToolAnnotations` hints | High: small predictable surface | Very high: agent task success | Agents only | Medium: over-merging makes mush; under-merging explodes count | At MCP derivation (K2), frozen before CI/search ops | Low: renames break prompts; alias + Sunset | Adopt with guard (DX-03, DX-04) |
| D7 | CLI invariants: `--json` purity, `--in -`, mirrored exit codes | High: scriptable or useless | High: CLI is the non-MCP agent path | Agents + operators | Low: pretty-printing tempts impurity | With CLI generation (K2) | High: flags additive; exit table frozen early | Adopt (DX-05) |
| D8 | Least-privilege scope matrix, short-lived agent tokens, minter-holds-scopes | Medium: memorized once | High: safe delegation to fleets | Humans delegating to agents | High: scope gaps brick fleets; sprawl voids model | With auth (F), before any fleet test | Low: granted tokens live on; renames need compat | Adopt with guard (DX-06; compat aliases) |
| D9 | `Accept: markdown/plain` alternates + `?format=` | High: obvious, curlable | Medium-high: token + parse savings | Agents reading code/diff/logs | Low: projection bugs mislead; generate from schema | With browsing/review reads (F) | High: additive media types | Adopt (DX-07) |
| D10 | Generated `/llms.txt` + `/llms-full.txt` + freshness test | High: start-here file | Medium: onboarding + evals | New agents, evals | Low: stale file misleads; gate prevents | After first ops exist (F), before Phase V | High: regenerable | Adopt (DX-08) |
| D11 | Server-rendered OpenAPI docs, zero JS, per-op curl/rukie/MCP snippets | High: docs = spec | Medium-high: discovery without JS | Humans + agents via markdown | Low-medium: template work, no protocol risk | Spec endpoint at K2, pages with U | High: presentation-only | Adopt (DX-10) |
| D12 | `rukie dev` one-command loop (ephemeral daemon + seed) | Very high: clone→flow in minutes | Very high: contributor + loop velocity | Contributors, Phase V harness | Medium: seed/daemon drift; hermetic temp dirs mitigate | Early (K4/dev), before feature work | High: dev-only surface | Adopt (DX-11) |
| D13 | Single TOML config + `RUKIE_*` overrides + `config check` | High: one file, loud validation | High: install/upgrade success | Operators | Medium: config-key churn; validate + compat | With `serve` (K1/K4) | Medium: keys versioned, unknown-key reject with hint | Adopt (DX-12) |
| D14 | Admin CLI for password/token/repo/mirror/attest as scoped Ops | High: 3 a.m. operability | High: no-email operability | Operators | Low: must not become a side-channel (scope-gate it) | With auth + repos (F) | High: additive subcommands | Adopt (DX-13) |
| D15 | Shared list conventions (sort/filter/limit caps 100/1000 from Ruk limits) | Medium: documented once | Medium: consistency everywhere | SDK/CLI authors | Low | With store + first lists | High | Adopt (DX-14) |
| D16 | 429+`Retry-After` + quota errors with backoff guidance in `recovery` | High: explicit waits | Medium: fleet politeness | Agents at scale | Low | With CI/search hot paths | High | Adopt (DX-15) |
| D17 | Webhook `webhook_send_test` + replay + signed Standard Webhooks examples | Medium | Medium: integration velocity | Integrators | Low-medium: secret handling in examples | With webhooks (F) | High | Adopt (DX-16) |
| D18 | `Sunset` + changelog + versioned OpenAPI for deprecations | Medium | Medium: safe evolution | All clients | Low if policy set early | Policy at K2, first use as needed | Low-medium: policy is the reversibility | Adopt (DX-17) |
| D19 | Parity gate: Op table → HTTP+MCP+CLI+docs+llms.txt asserted in CI; implement at K2, enforce thereafter | High (maintainers) | Very high: prevents all drift | Maintainers | Low: test maintenance | K2 (build alongside DX-01/DX-02; §4 DX-18 list position is capstone, implementation is early) | High: test-only | Adopt (DX-18) |

Cross-cutting judgment: D1/D2/D5/D6/D19 are load-bearing and MUST land in K2 before
feature work; D3/D4/D7 ride with them — including the DX-18 parity gate, which is
implemented at K2 and enforced continuously even though it is listed last in §4 as the
enforcement capstone. D8/D13/D14 unblock fleets and operators in F. D9/D10/D11 make
Phase U/V measurable. Irreversible choices are op names, cursor/header contracts, and
scope strings — hence alias + `Sunset` + compat discipline in DX-01/DX-03/DX-06/DX-17.
Cursor replayability (D3) is deliberate: safe retry beats single-use strictness. The
≤36 tool cap is a heuristic budget (§2.1), not measured fact. `E_LIMIT` maps by cause:
429 for rate/quota pressure, 413 for oversized payloads/frames.

## 4. DX tasks (sequential; DX-18 implemented at K2 per D19, listed last as capstone)

DX-01 — Freeze the Op-table contract (naming `<noun>_<verb>`, envelope, RFC 9457 error shape with `recovery`+`trace_id`, versioning/alias rule) and write it as the single deriving source for HTTP+MCP+CLI+HTML. Rationale: prevents four-surface drift; all DX work hangs off this decision.
DX-02 — Implement HTTP conventions once in shared helpers: RFC 9457 problems, replayable opaque keyset cursors bound to query+expiry with `Link` next/prev, `ETag`/`If-Match` with 412/428, validated `Idempotency-Key` with daemon `request_id` passthrough, `E_LIMIT`→429 for rate/quota with `Retry-After` and →413 for oversized payloads, 429+`Retry-After`. Rationale: uniform retry/concurrency semantics agents can code against.
DX-03 — Cap and name the MCP surface (≤36 tools heuristic budget, `<noun>_<verb>`, merge reads via `include`, stable names with alias+Sunset on rename) derived strictly from the Op table with no degraded-semantics tools. Rationale: keeps agent tool selection reliable; bans Ruk-style silent local degradation.
DX-04 — Set real MCP `ToolAnnotations` on every tool (`title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`, verb-first description, scope hints) and verify annotations via `go-sdk` v1.8.0 types. Rationale: correct hint fields drive agent planning and destructive-action gating.
DX-05 — Ship generated `rukie <op>` CLI invariants: `--json` stdout purity, `--in -|@file` + `--set`, mirrored Ruk `E_*` exit-code table, `--dry-run/--trace-id/--idempotency-key/--wait` on all mutations. Rationale: makes the CLI the scriptable non-MCP agent path.
DX-06 — Define and enforce the token-scope matrix (`repo`/`change`/`task`/`ci`/`mirror`/`release`/`org`/`admin`, confinement, short-lived agent defaults, minter-holds-scopes) with missing-scope errors naming the mint command. Rationale: least-privilege delegation to agent fleets without privilege escalation.
DX-07 — Serve `Accept: application/json|text/markdown|text/plain` + `?format=` on all read ops, generated from Op schemas with `Vary`/`Content-Location` correctness. Rationale: cuts agent token cost and HTML-parse failures on code/diff/log reads.
DX-08 — Generate `/llms.txt`, `/llms-full.txt`, and per-op markdown from the Op table with an agent-start-here path and a freshness CI gate. Rationale: canonical onboarding surface for new agents and eval harnesses.
DX-09 — Standardize error recovery payloads: every error carries exact next command, `current_etag`/`holder`/`expires_at`/`merge_base` where applicable, docs anchor, and `trace_id` across HTTP/MCP/CLI. Rationale: autonomous agents can only self-recover when errors name the fix.
DX-10 — Render OpenAPI 3.1 server-side (zero JS `/docs`, per-op pages with curl/rukie/MCP-JSON snippets, `?q=` filter) directly from the Huma spec with no hand-written API guide. Rationale: discoverability without drift or client JS.
DX-11 — Deliver one-command local loop `rukie dev` (ephemeral `ruk serve` on temp socket+roots, seeded org/repo/tasks, printed URLs/credentials, `dev reset|seed`) timed in CI. Rationale: contributor and Phase-V agent-loop velocity; kills Gitea-plus-daemon setup sprawl.
DX-12 — Ship single-TOML config with `RUKIE_*` overrides, precedence table, and `rukie config check` failing fast with line-numbered hints (socket/roots/DSN). Rationale: install-time DX; surfaces daemon registry misconfiguration before serve.
DX-13 — Ship audited admin CLI (`user reset-password`, `token mint|revoke|list`, `repo verify|repair|reindex-search`, `mirror run|retry|status`, `attest verify`, `inbox send-test`) as scoped Ops. Rationale: no-email operability for the 3 a.m. cases without privilege side-channels.
DX-14 — Lock shared list conventions (sort allowlist, `q` scoping to Ruk search capability, limit default 100 / max 1000 inherited from Ruk limits, replayable cursors) in one helper + doc. Rationale: consistent paging/filtering for every SDK/CLI author.
DX-15 — Standardize rate-limit/quota errors (429+`Retry-After`, quota body, `recovery.backoff_ms`) on CI/search/mirror hot paths with agent backoff guidance. Rationale: fleet politeness under heavy agentic load.
DX-16 — Ship webhook DX (`webhook_send_test`, delivery replay, Standard Webhooks signature examples per receiver language) alongside the webhook feature. Rationale: integration velocity for inbox-less consumers.
DX-17 — Publish versioning/deprecation policy (versioned OpenAPI, `Sunset` header + changelog entry + dual-run window for renames) and apply it to the first renamed op/scope. Rationale: makes op names, cursors, and scopes safely evolvable.
DX-18 — Add the DX parity gate in CI asserting Op table → HTTP routes + MCP tools + CLI commands + docs pages + llms.txt stay in lockstep (drift = red); implement at K2 alongside DX-01/DX-02 and enforce thereafter. Rationale: the enforcement mechanism for every decision above.

### Critical Files for Implementation

- `ruk/crates/ruk/src/mcp/tools.rs` — 12-tool typed table + decode-no-coercion precedent; seed for MCP budget/naming/annotations (DX-03, DX-04)
- `ruk/crates/ruk/src/mcp/server.rs` — JSON-RPC method set, protocol version, `E_LIMIT` framing precedent and CoreError→RPC mapping to mirror in Huma/MCP/CLI errors (DX-02, DX-09)
- `ruk/crates/ruk/src/daemon/mod.rs` (`CommandOpen{request_id, command_digest}` + `InvocationOpen`) — existing replay driver that `Idempotency-Key` passthrough must preserve; source of query-bound, expirable, replayable cursor requirements (DX-02, DX-11)
- `/tmp/ruk-model.json` — `E_*`→`exit_status` taxonomy and `page_default`/`page_max` limits grounding CLI codes and list conventions (DX-05, DX-14)
- `rukie/go.mod` — current module surface (chi, no Huma/MCP SDK): insertion point for Huma v2, `modelcontextprotocol/go-sdk`, generated CLI, and the parity gate (DX-01, DX-18)

```jsonl
{"id":"DX-01","description":"Freeze the Op-table contract (naming `<noun>_<verb>`, envelope, RFC 9457 error shape with `recovery`+`trace_id`, versioning/alias rule) and write it as the single deriving source for HTTP+MCP+CLI+HTML.","phase":"DX"}
{"id":"DX-02","description":"Implement HTTP conventions once in shared helpers: RFC 9457 problems, replayable opaque keyset cursors bound to query+expiry with `Link` next/prev, `ETag`/`If-Match` with 412/428, validated `Idempotency-Key` with daemon `request_id` passthrough, `E_LIMIT`→429 for rate/quota with `Retry-After` and →413 for oversized payloads, 429+`Retry-After`.","phase":"DX"}
{"id":"DX-03","description":"Cap and name the MCP surface (≤36 tools heuristic budget, `<noun>_<verb>`, merge reads via `include`, stable names with alias+Sunset on rename) derived strictly from the Op table with no degraded-semantics tools.","phase":"DX"}
{"id":"DX-04","description":"Set real MCP `ToolAnnotations` on every tool (`title`, `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`, verb-first description, scope hints) and verify annotations via `go-sdk` v1.8.0 types.","phase":"DX"}
{"id":"DX-05","description":"Ship generated `rukie <op>` CLI invariants: `--json` stdout purity, `--in -|@file` + `--set`, mirrored Ruk `E_*` exit-code table, `--dry-run/--trace-id/--idempotency-key/--wait` on all mutations.","phase":"DX"}
{"id":"DX-06","description":"Define and enforce the token-scope matrix (`repo`/`change`/`task`/`ci`/`mirror`/`release`/`org`/`admin`, confinement, short-lived agent defaults, minter-holds-scopes) with missing-scope errors naming the mint command.","phase":"DX"}
{"id":"DX-07","description":"Serve `Accept: application/json|text/markdown|text/plain` + `?format=` on all read ops, generated from Op schemas with `Vary`/`Content-Location` correctness.","phase":"DX"}
{"id":"DX-08","description":"Generate `/llms.txt`, `/llms-full.txt`, and per-op markdown from the Op table with an agent-start-here path and a freshness CI gate.","phase":"DX"}
{"id":"DX-09","description":"Standardize error recovery payloads: every error carries exact next command, `current_etag`/`holder`/`expires_at`/`merge_base` where applicable, docs anchor, and `trace_id` across HTTP/MCP/CLI.","phase":"DX"}
{"id":"DX-10","description":"Render OpenAPI 3.1 server-side (zero JS `/docs`, per-op pages with curl/rukie/MCP-JSON snippets, `?q=` filter) directly from the Huma spec with no hand-written API guide.","phase":"DX"}
{"id":"DX-11","description":"Deliver one-command local loop `rukie dev` (ephemeral `ruk serve` on temp socket+roots, seeded org/repo/tasks, printed URLs/credentials, `dev reset|seed`) timed in CI.","phase":"DX"}
{"id":"DX-12","description":"Ship single-TOML config with `RUKIE_*` overrides, precedence table, and `rukie config check` failing fast with line-numbered hints (socket/roots/DSN).","phase":"DX"}
{"id":"DX-13","description":"Ship audited admin CLI (`user reset-password`, `token mint|revoke|list`, `repo verify|repair|reindex-search`, `mirror run|retry|status`, `attest verify`, `inbox send-test`) as scoped Ops.","phase":"DX"}
{"id":"DX-14","description":"Lock shared list conventions (sort allowlist, `q` scoping to Ruk search capability, limit default 100 / max 1000 inherited from Ruk limits, replayable cursors) in one helper + doc.","phase":"DX"}
{"id":"DX-15","description":"Standardize rate-limit/quota errors (429+`Retry-After`, quota body, `recovery.backoff_ms`) on CI/search/mirror hot paths with agent backoff guidance.","phase":"DX"}
{"id":"DX-16","description":"Ship webhook DX (`webhook_send_test`, delivery replay, Standard Webhooks signature examples per receiver language) alongside the webhook feature.","phase":"DX"}
{"id":"DX-17","description":"Publish versioning/deprecation policy (versioned OpenAPI, `Sunset` header + changelog entry + dual-run window for renames) and apply it to the first renamed op/scope.","phase":"DX"}
{"id":"DX-18","description":"Add the DX parity gate in CI asserting Op table → HTTP routes + MCP tools + CLI commands + docs pages + llms.txt stay in lockstep (drift = red); implement at K2 alongside DX-01/DX-02 and enforce thereafter.","phase":"DX"}
```


[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Remove duplicate `429+Retry-A…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Give rate and quota errors their …

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Remove the (phase, id) disambigua…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Name R1 as a DX-11 prerequisite. …

[advisor] <advisory advisor="stickler" severity="nit" guidance="weigh, don't blindly obey"> Delete the repeated "429+Retry-After"…