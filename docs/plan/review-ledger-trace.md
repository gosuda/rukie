> `LedgerTrace` is running a newer turn. The output below is from its PREVIOUS run (published 18.6s ago), not the current one. Live progress: `history://LedgerTrace`; its next result auto-delivers to its owner.All 117 rows classified. Table verified: 25 CEO + 21 design + 18 DX + 53 ENG = 117 rows.

| id | class | plan location | evidence quote or missing item |
|---|---|---|---|
| CEO-01 | step | D2 | "Land the new `rukie/AGENTS.md` and the new `ruk/AGENTS.md`" |
| CEO-02 | orphan | — | No step marks `AGENT_NATIVE_GIT_PLAN.md:92-96` or old ruk/tasks rows superseded |
| CEO-03 | step | D1 | "Rewrite the README from the draft" |
| CEO-04 | decision | Settled decisions: Metadata store | "Use SQLite through `modernc.org/sqlite` v1.60.1" |
| CEO-05 | conflict | Approach | Row: "dissolve separate Phases R, U, V" vs plan: "Stream R changes ruk" (Stream R kept; no S0-S11 anywhere) |
| CEO-06 | orphan | — | No permanent S0 acceptance-script gate (JSON HTTP, MCP, CLI, HTML, push/clone, tree view) |
| CEO-07 | step | K3 | "the schema dump with a drift gate" |
| CEO-08 | orphan | — | No daemon-path budgets (7.5x incremental fetch, 4x single-ref) or load harness |
| CEO-09 | step | D2 | "Update the ruk README: the method count and `ruk serve`" |
| CEO-10 | step | R2 | "Remove all Forgejo residue per `docs/plan/ruk-forge-residue.md` section 5" |
| CEO-11 | orphan | — | No licence provenance rule for ported GPL Ruk code |
| CEO-12 | orphan | — | No per-op HTML template override; op table never scoped to S0 ops |
| CEO-13 | step | F1 | "carry scopes and expiry, and track last use" |
| CEO-14 | step | F4 | "Delivery is at-least-once; every consumer is idempotent" |
| CEO-15 | orphan | — | No mapping for symrefs, shallow fetch, LFS sync, reflog page (only refs.list exists) |
| CEO-16 | step | Dogfood gate | "After F6, host the rukie repo itself" |
| CEO-17 | step | Dogfood gate | "Mirror it to `gosuda/rukie` on GitHub" |
| CEO-18 | decision | Settled decisions: Review unit | "A branch view groups the changes of one branch" |
| CEO-19 | decision | Settled decisions: Architecture; step 0 item 2 | "A separate `ruk serve` daemon process owns the repositories"; "Close rows ENG-53 and CEO-19" |
| CEO-20 | step | K6 | "exec requests carry `git-upload-pack '<dir>'` or `git-receive-pack`" |
| CEO-21 | orphan | — | No /v0 versioning, webhook schema freeze, or changelog policy |
| CEO-22 | step | R5 | "Later method additions stay inside minor 2" |
| CEO-23 | decision | Settled decisions: Code search | "bounded parallel scan and keeps no index" |
| CEO-24 | step | Approach | "Each F commit ships its UI view and its gate" |
| CEO-25 | orphan | — | No gate blocking external users before the security review |
| DES-01 | conflict | Settled decisions: Design | Row: "dense typographic supervision console … no decorative chrome" vs plan: "a Fluent-style workbench with layered shadows" |
| DES-02 | step | K4 | "the `/-/` plumbing namespace, flat canonical pages" |
| DES-03 | step | K5 | "zerojs.Assert fails a test when a rendered page contains `<script`" |
| DES-04 | step | K5 | "the status palette mapped to Live, Ambiguous, Superseded, and Gap" |
| DES-05 | orphan | — | Only derived HTML exists; no hand-authored core-surface templates |
| DES-06 | step | K5 | "both schemes through `prefers-color-scheme`" |
| DES-07 | step | K5 | "Take the tokens from `docs/plan/design-direction-instrument.md` verbatim" |
| DES-08 | step | K5 | "tabular numerics, and the status palette" |
| DES-09 | step | Settled decisions: Design | "Motion is at most 150 ms of CSS transition" |
| DES-10 | step | K5 | "Build the layout and component tree" |
| DES-11 | step | K5 | "Render diffs from Ruk per-file patches through `go-diff` `ParseHunks`" |
| DES-12 | step | F2 | "Add the tree, blob, raw, blame, log, diff, compare, and archive pages" |
| DES-13 | step | F3 | "Surface proofs through `attest.list` and evaluate them" |
| DES-14 | step | F3 | "Add the branch view that groups a stack" |
| DES-15 | step | F3 | "Create the intents DDL and the ops `intent.create`" |
| DES-16 | step | F4; Approach | "Each F commit ships its UI view and its gate" |
| DES-17 | step | Register: Design | "The supervision surfaces (gap work-list, branch view, batch forms) ship with F3" |
| DES-18 | step | F5 | "Build the inbox per `docs/plan/inbox-design.md`" |
| DES-19 | step | F1 | "Verify TOTP per RFC 6238 with a replay guard" |
| DES-20 | step | F8 | "Add stars, watches, and forks through `repo.fork`" |
| DES-21 | step | Register: Design | "WCAG 2.2 AA applies, with 2.4.7 and 1.4.11 substituted" |
| DX-01 | step | K4 | "Build `internal/op` exactly per `docs/plan/op-table-design.md`" |
| DX-02 | step | K4 | "`Idempotency-Key` is store backed for 24 h" |
| DX-03 | step | F9; Register: DX | "MCP serves at most 36 tools in toolsets" |
| DX-04 | step | K4 | "maps dots to underscores, groups toolsets by domain, filters by scope" |
| DX-05 | step | K4 | "builds `rukie <domain> <verb> --json` with flags from the input schema" |
| DX-06 | step | F1; Settled decisions: Orgs | "carry scopes and expiry, and track last use" |
| DX-07 | step | K4 | "Content negotiation sits outside Huma" |
| DX-08 | orphan | — | No `/llms.txt`, `/llms-full.txt`, or per-op markdown generation |
| DX-09 | orphan | — | No standardized error recovery payloads (next command, etag/holder, trace_id) |
| DX-10 | step | K4 | "Serve `/openapi.json` and a server-rendered API reference" |
| DX-11 | orphan | — | No `rukie dev` one-command local loop |
| DX-12 | step | K1 | "An unknown key fails startup. Add `--check-config`" |
| DX-13 | orphan | — | Admin CLI covers only user/token-revoke/bootstrap; no token mint/list, repo verify/repair/reindex, attest verify, inbox send-test |
| DX-14 | step | K4 | "Keyset pagination emits `Link` headers" |
| DX-15 | orphan | — | No rate limiting anywhere in the plan |
| DX-16 | step | F5 | "a dead-letter state, a redelivery op" |
| DX-17 | orphan | — | No deprecation policy (Sunset header, dual-run window) |
| DX-18 | orphan | — | No DX parity gate asserting op-table lockstep |
| ENG-01 | decision | Settled decisions preamble | "The user confirmed these decisions on 2026-10-20" |
| ENG-02 | orphan | — | No ignore entry for the nested `ruk/rukie/` clone |
| ENG-03 | step | Step 0 items 4-5 | "Write `go.mod` with module `github.com/gosuda/rukie`" |
| ENG-04 | step | Verification | "go vet ./... && golangci-lint run ./... && go test -race" |
| ENG-05 | step | R2 | "Replace `ruk forgejo serve` with `ruk serve --config`" |
| ENG-06 | orphan | — | No host-credential subcommand (old R1b step 1 dropped) |
| ENG-07 | conflict | Settled decisions: Ruk principal model | Row: "require an HMAC over (challenge, asserted_principal, method)" vs plan: "Ruk authenticates one OS peer through `SO_PEERCRED`" |
| ENG-08 | step | R3 | "Give each stream its own task and a writer task" |
| ENG-09 | step | R3 | "Make typed replies wait for credit" |
| ENG-10 | orphan | — | No keepalives or subscription class exempt from the 60 s frame timeout |
| ENG-11 | orphan | — | No public ExecutorLane; RequestClass variants unmapped |
| ENG-12 | step | R13 | "cargo fmt --check, cargo clippy --workspace --all-targets, and cargo nextest" |
| ENG-13 | step | D3 | "Write the systemd units" |
| ENG-14 | step | K2 | "Embedded `NNNN_name.sql` migrations with checksums" |
| ENG-15 | step | K4 | "`mount_huma.go` is the only Huma import" |
| ENG-16 | step | F1 | "Hash with argon2id: 19456 KiB, t=2, p=1" |
| ENG-17 | step | F1 | "the bookmark guards G0 to G11, max over grants" |
| ENG-18 | step | K5 | "Ship `app.css` and a generated `chroma.css`, embedded" |
| ENG-19 | step | R6 | "Build tree and object readers over resolved revisions" |
| ENG-20 | orphan | — | bookmark.*, tag.*, fetch, push.reconcile, unpin, op.prune never admitted to the hosted path |
| ENG-21 | orphan | — | No crash test for lane receipts across a daemon kill |
| ENG-22 | step | R7; R8 | "Add `repo.init`, `repo.delete`, `repo.rename`, and `repo.stat`" |
| ENG-23 | orphan | — | No fail-never-skip harness contract for the real-daemon tests |
| ENG-24 | step | K3 | "The pool multiplexes 8 streams per connection" |
| ENG-25 | step | K3 | "the schema dump with a drift gate" |
| ENG-26 | step | F2 | "Add `repo.create`, `repo.fork`, `repo.delete`, `repo.rename`, and repo settings" |
| ENG-27 | step | R3 | "The `service` field carries `upload-pack`, `receive-pack`, or `upload-archive`" |
| ENG-28 | step | R4 | "Serve the hosted path through typed `HostPolicy` frames" |
| ENG-29 | step | K6 | "Serve SSH with `x/crypto/ssh`" |
| ENG-30 | step | K6 | "Serve the Smart HTTP endpoints `info/refs`, `git-upload-pack`, and `git-receive-pack`" |
| ENG-31 | orphan | — | No process-trace test for the rukie-plus-daemon boundary |
| ENG-32 | step | R6 | "Build the production graph index for `log` and `query`" |
| ENG-33 | step | R6 | "Serve hosted `file`, `log`, `diff`, `show`, `blame`, and `query`" |
| ENG-34 | step | F3 | "Add the branch view that groups a stack" |
| ENG-35 | step | R5 | "Add the `events.tail` long-poll read method" |
| ENG-36 | step | K2 | "outbox is written in the same transaction as domain rows" |
| ENG-37 | step | F5 | "jittered backoff, a dead-letter state, a redelivery op, and SSRF-safe dialing" |
| ENG-38 | step | F5; Settled decisions: Auth | "Build the inbox per `docs/plan/inbox-design.md`" |
| ENG-39 | orphan | — | No per-token or per-account token buckets (no rate limiting in plan) |
| ENG-40 | step | R10 | "Add a task and change key to `ClaimRecord`" |
| ENG-41 | step | F3 | "Wrap `land.prepare` and `land.execute`" |
| ENG-42 | step | F3; Settled decisions: Collaboration | "`intent.claim`, `intent.release`" |
| ENG-43 | step | F4 | "content-addressed artifacts, cache volumes keyed by trust class" |
| ENG-44 | conflict | R4 | Row: "Reuse MergeNotice unchanged" vs plan: "Retire frame kind 9 `MergeNotices`, per decision D10" |
| ENG-45 | step | R11; Settled decisions: Mirrors | "Accept per-call `GIT_AUTH_*` values in the command environment" |
| ENG-46 | step | F6 | "with a per-mirror debounce, plus a periodic reconcile" |
| ENG-47 | step | R9; F7 | "Implement `docs/plan/ruk-search-design.md` verbatim, per decision C5" |
| ENG-48 | step | F8; R8 | "Bind releases to Ruk tags with Markdown notes" |
| ENG-49 | step | F8 | "Add stars, watches, and forks through `repo.fork`" |
| ENG-50 | orphan | — | No repo import through offline Git conversion |
| ENG-51 | step | R5 | "Add the retention sweep" |
| ENG-52 | orphan | — | Only the runner kill exists (check 5); no kill-9 fault suite for push, webhook, attest, mirror, or ruk crash/lock/IO smoke |
| ENG-53 | decision | Step 0 item 2 | "Close rows ENG-53 and CEO-19 in that copy" |

`step: 80, decision: 6, orphan: 27, conflict: 4, total: 117`

**Conflicts — proposed plan insertions**

- CEO-05: The plan keeps Stream R and streams R, K, F, D. Add the slice map S0 to S11, or record that streams replace slices.
- DES-01: The plan picks the Instrument direction. Record that it supersedes the operations-ledger art direction.
- ENG-07: The plan uses SO_PEERCRED with one socket peer. Record that it supersedes the HMAC credential and uid-separation scheme.
- ENG-44: The plan retires the MergeNotices frame. Record the supersession of the MergeNotice reuse row.

**Orphans — proposed plan insertions**

- CEO-02: Add a step that marks the old plan and old ledger rows superseded.
- CEO-06: Add a permanent gate that runs the S0 acceptance script on every commit.
- CEO-08: Add daemon-path budgets and a load harness, and gate dogfood on them.
- CEO-11: Add a licence rule that ports only campaign-authored Ruk code and records provenance.
- CEO-12: Add per-op HTML template overrides to the op table design.
- CEO-15: Add a table that maps each open Ruk gap to its slice.
- CEO-21: Add a versioning policy with /v0, frozen webhook schemas, and a changelog.
- CEO-25: Add a gate that blocks external users until the security review passes.
- DES-05: Add hand-authored templates for the core surfaces beside the derived forms.
- DX-08: Add generated llms.txt and per-op markdown with a freshness gate.
- DX-09: Add a recovery payload standard for all error surfaces.
- DX-11: Add a `rukie dev` command that starts and seeds a local instance.
- DX-13: Add the missing admin commands for tokens, repo repair, attest verify, and inbox tests.
- DX-15: Add rate limiting with 429 and Retry-After in the op middleware.
- DX-17: Add a deprecation policy with Sunset headers and a dual-run window.
- DX-18: Add a CI gate that checks the op table, routes, tools, CLI, and docs stay in lockstep.
- ENG-02: Add an ignore entry for the nested ruk/rukie clone.
- ENG-06: Add the host-credential subcommand with its file-mode test.
- ENG-10: Add stream keepalives and a subscription class exempt from the frame timeout.
- ENG-11: Add a public ExecutorLane that mirrors the RequestClass variants.
- ENG-20: Add hosted admission for the bookmark, tag, fetch, reconcile, unpin, and prune methods.
- ENG-21: Add a crash test that proves lane receipts survive a daemon kill.
- ENG-23: Add a harness rule that fails, never skips, when the daemon binary is missing.
- ENG-31: Add a process-trace test that proves no git process runs in the boundary.
- ENG-39: Add per-token and per-account token buckets with Retry-After.
- ENG-50: Add repo import through offline conversion with verify and rollback.
- ENG-52: Add a kill-9 fault suite for the push, webhook, CI, and mirror paths.