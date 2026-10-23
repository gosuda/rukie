# Autoplan CEO review: rukie, an agent-first forge on Ruk

This is the complete content of `autoplan-ceo.md`. I'm in read-only plan mode with no write tool, so the orchestrator needs to save it under that name.

## 0. Inputs, scope of this phase, and ledger placement
- **Inputs.** The goal, the settled decisions and the draft skeleton came inline in the assignment. This review does not reopen the settled decisions. It answers four questions:
  1. What is the product, and what is outside it?
  2. What is the smallest end-to-end slice that proves it, so every later commit leaves a working product?
  3. In what order should features land, ranked by impact on heavy agentic engineering?
  4. What could sink the whole campaign?
- **Earlier plan that this one overrides.** `ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:92-96` records a previous autoplan whose binding decisions assume a Forgejo patch target. For example, line 94 says "The Forgejo patch surface is frozen as a checked-in inventory". This campaign drops Forgejo as the target (settled: remove Forgejo residue, use only rukie). Those decisions need an explicit "superseded" mark (CEO-02) so agents don't follow two plans that contradict each other.
- **Task-ID collision.** `ruk/tasks/autoplan-ledger.jsonl` already holds `CEO-01`..`CEO-03`, `DES-01`..`03`, `DX-01`..`03` and `ENG-01`..`06` from that earlier campaign, with different content.
  - This campaign's ledger is `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`, which does not exist yet and gets created.
  - The CEO IDs below start at `CEO-01` in that new ledger. They must **not** be appended to `ruk/tasks/autoplan-ledger.jsonl`.
- **This phase's output.** The register plus tasks. Writing to the ledger and amending the plan belong to the final autoplan step, after all four phases.

### Facts found in the trees
- **There is a working end-to-end prototype in another tree.** `ruk/forgejo/` is a Forgejo tree running on Ruk through a Go IPC client in `ruk/forgejo/modules/git/`: `ruk_ipc.go` (frames, credit windows), `backend_ruk.go` (session IDs, `RunCommand` over IPC), `ruk_capability.go` and `ruk_migrate.go`. `ruk/.outline/sdd/progress.md:23` marks item 10 ("Forgejo daemon and patch") done. Today it is the only proof that a Go forge can run on the Ruk daemon.
- **Forgejo residue in Ruk is spread across crates.**
  - `ForgejoCommand` and the `"ruk-forgejo-serve"` thread name in `crates/ruk/src/main.rs` and `crates/ruk/src/daemon/mod.rs`.
  - The handshake `profile: "forgejo"` in `crates/ruk/src/daemon/ipc.rs`.
  - Hook dispatch modelled on Forgejo in `crates/ruk-git/src/transport/{hooks,proc_receive,mirror}.rs`.
  - Mentions in the error-code docs in `crates/ruk-core/src/error.rs`.
- **Measured gaps are in fetch and lookup, not clone.** Numbers below are from `ruk/tasks/capacity/RESULTS.md` (newest run, commit `78a98e2`), p50, warm, CLI-level, on the 80-CPU host:

  | Workload | Ruk | git | Ratio |
  |---|---|---|---|
  | incremental `fetch` | 659 ms | 88 ms | **7.5×** |
  | single-ref `lookup` | 36 ms | 9 ms | **4×** |
  | `write` | 49 ms | 26 ms | 1.9× |
  | `clone` (10k branches) | 1,714 ms | 1,370 ms | 1.25× |
  | `adv-10k` | 58 ms | 53 ms | 1.1× |
  | `adv-1k` | 22 ms | 43 ms | 0.5× (Ruk faster) |
  | `page1` | 94 ms | 144 ms | 0.65× (Ruk faster) |

  - The README's higher figures (6.94 s clone, 990 ms fetch) come from an older run (commit `8487835b`) and are stale.
  - Capacity acceptance is **OPEN** (`ruk/.outline/sdd/progress.md:27`).
  - Daemon-path and load rows are not measured: adv-budget, no-starvation, http-429-503, crash-around-publish (`progress.md:88`; `RESULTS.md`, "Reading this honestly").
- **The README is partly stale on transports too.** It says the `http(s)://` client is an unimplemented gap. In fact `crates/ruk-git/src/transport/http.rs:1-12` implements a smart-http and LFS client over rustls, with Basic/Bearer auth read from the environment. It sends `Connection: close` on every request. `transport/mirror.rs` says the mirror verbs serve smart-http. So HTTPS push mirrors to GitHub are feasible now; what's missing is per-call credential injection (R4), and keep-alive is absent.
- **Open Ruk gaps that affect forge slices:**
  - shallow and partial fetch are still OPEN (`progress.md:21`);
  - LFS payload sync, symbolic refs, and wiki/template flows are OPEN (`progress.md:24`);
  - the Forgejo DB/browser migration gates are OPEN (`progress.md:26`);
  - there is no native bounded ref-list or ref-count IPC method, and no typed reflog page (`progress.md:94`).
- **The canonical rukie checkout is ambiguous.** `ruk/rukie/` is a near-copy of `/home/alpha/rewrite/git-renewed/rukie/`: same top-level layout, modified "1h ago" versus "26m ago".
- **Licences (headers read):**
  - `rukie/LICENSE` is MIT ("Copyright (c) 2016 The Gitea Authors / 2015 The Gogs Authors").
  - `ruk/forgejo/LICENSE` is GNU GPL v3.
  - Ruk is Apache-2.0 (`ruk/README.md`, License section).

## 1. Six-principle decision register

### 1.1 Clarity
| # | Decision / open question | Grounds |
|---|---|---|
| C-D1 | **Product statement:** "A single-host forge where many AI agents and a few supervising humans propose, verify, review and land changes on Ruk repositories. Every action is one operation, reachable as HTML, JSON HTTP, MCP or CLI." Anything that does not serve that loop goes in a later slice. | Goal |
| C-D2 | **The review unit is the Ruk change (change-id).** A stack is an ordered set of changes. The branch view is a read-only projection that groups a branch's changes; it has no review state of its own. This stops two review models from drifting apart. | Settled: both changes/stacks and branch view |
| C-D3 | **CI v1 bar, stated as testable properties.** v1 must meet all four: (a) queued or running jobs survive a `rukie` restart and a runner crash with no human action; (b) step results are keyed by (tree OID, recipe hash, runner image digest) and reused across changes, rebases and forks; (c) each finished job writes exactly one Ruk attestation, readable without the metadata DB; (d) per-step retry is capped and never duplicates side effects. Anything beyond these four is out of v1. | "More resilient, durable and efficient than GitHub/GitLab CI" is not testable as written |
| C-D4 | **Search v1 has no persistent index.** It is a bounded, parallel scan inside Ruk over a revision's tree, with byte and time caps and a cursor. An index is allowed only after a measured p95 miss. | Settled: "lightweight, no heavy index" |
| C-D5 | **The earlier Forgejo-bound plan decisions are superseded** (`AGENT_NATIVE_GIT_PLAN.md:92-96`; old ledger IDs `CEO-01`..`03`). The merge-notice and effective-mode decisions carry over without Forgejo as a surface. The Forgejo patch-inventory freeze is retired. | §0 |
| C-Q1 | **Open:** which checkout is the source of truth for rukie, `git-renewed/rukie` or `ruk/rukie`? Settle it before K1. | Both exist |
| C-Q2 | **Open:** metadata store. **CEO recommendation: pure-Go SQLite** (single host, co-located with the Ruk Unix-socket daemon, transactional, zero ops). PostgreSQL stays possible behind the store module. Pebble would need a hand-built query layer for permissions, lists and pagination. The final call stays with ENG. | Settled: OPEN behind one module |

### 1.2 Impact
| # | Decision | Rationale |
|---|---|---|
| I-D1 | **The loop that matters most:** agent token → push to `refs/for/<branch>` → change → CI attestation → review → land, with conflicts and claims visible to the next agent. Features are ranked by how directly they shorten or de-risk that loop. | Goal |
| I-D2 | **Impact ranking:** (1) hosting + Smart HTTP + tokens; (2) changes/stacks review + land; (3) push mirrors (adoption without leaving GitHub, and they make dogfooding possible); (4) orgs/teams + scoped tokens; (5) built-in CI; (6) tasks with claims/leases; (7) events: signed webhooks, inbox, op-log tail; (8) code search; (9) SSH; (10) releases; (11) TOTP + OIDC; (12) explore/stars/watches/forks. | Agents need 1, 2, 4, 5, 6, 7; humans need 2 and 7; adoption needs 3 |
| I-D3 | **Dogfooding is the success metric.** From slice S2 on, ruk and rukie development moves onto self-hosted rukie, push-mirrored to GitHub. | Real use proves more than synthetic e2e |

### 1.3 Audience
| # | Decision | Rationale |
|---|---|---|
| A-D1 | **Primary user:** concurrent AI agents using scoped tokens on human accounts, through MCP, `rukie <op> --json` and the JSON API. **Secondary user:** supervising humans using zero-JS HTML. When an op's design must favour one, the agent shape wins and the HTML adapts. | Settled identity model |
| A-D2 | **Out of audience for now:** anonymous public open-source communities, LDAP/SAML/SCIM enterprises, email-driven workflows, and users expecting Gitea/Forgejo API v1. These go in the README as named non-goals. | Settled cuts |
| A-D3 | **Every slice ships both the agent write path and the human HTML read view.** | Supervision is the human job |

### 1.4 Risk
| # | Risk | Likelihood / impact | Decision / mitigation |
|---|---|---|---|
| R-1 | **Contract churn from rewriting Ruk and rukie at once.** | High / high | Contract first, per slice. The Ruk JSON schema (`ruk model`) plus the daemon IPC schema are the single source; the Go client types are generated from them; Ruk ships a conformance test for each daemon method. rukie pins an exact Ruk commit and builds it in CI. |
| R-2 | **Hot-path latency for agent fleets.** Incremental fetch is 7.5× git and single-ref lookup 4× (`RESULTS.md`). Clone (1.25×) and ref advertisement are near parity. Capacity acceptance is OPEN, and daemon-path admission and load are unmeasured (`progress.md:27,88`). | High / high | A perf gate before dogfood (CEO-08), aimed at **fetch and ref lookup over the daemon path**. Also measure clone/advertisement through the daemon, because the current numbers are CLI-only. Missing the gate puts Ruk perf work ahead of new rukie features. |
| R-3 | **Big-bang Ruk phase.** R1–R6 land before any rukie slice, delaying the first product. | High / medium | Dissolve "Phase R first". Each slice names only the Ruk pieces it needs (§2). |
| R-4 | **Losing the only working prototype** (`ruk forgejo serve` + `ruk/forgejo`) before rukie matches it. | Medium / high | Remove the Forgejo residue only after rukie S0 passes its e2e. R1's neutral rename lands as a clean cutover, and the Forgejo patch's handshake is updated or retired in the same change. |
| R-5 | **Licence contamination.** `ruk/forgejo` is GPL-3.0; rukie is MIT. | Medium / high | Port only code written by the campaign (the Ruk patch files, e.g. `ruk_ipc.go`), and record provenance in the commit message. Never copy Forgejo-origin code. rukie stays MIT with the Gitea/Gogs notice kept. |
| R-6 | **Speculative op-table abstraction** (K2 designed before any feature). | Medium / medium | K2 ships only the S0 ops, end-to-end on all four surfaces. Per-op HTML template overrides are allowed. |
| R-7 | **CI scope explosion.** | High / high | Bound by C-D3. Single host, namespace sandbox (the exact tool is an ENG choice). No runner fleet, no marketplace, no GitHub Actions YAML compatibility. |
| R-8 | **Executing agent-written code under tokens that carry human authority.** | Medium / critical | Token scopes ship in S0; per-operation and per-repo scopes in S3, before CI in S4. CI gets per-job, short-lived, repo-scoped credentials. Mirror credentials are injected per call (R4) and never persisted in Ruk or env files. |
| R-9 | **The Ruk daemon is a single point of failure.** | Medium / medium | Open question C-Q3. Recommendation: rukie supervises a `ruk serve` child by default, with an external-socket flag. |
| R-10 | **Nobody owns SSH termination** (Gitea SSH is cut; SSH must go through the Ruk daemon). | Medium / low | Defer to S8. ENG chooses between OpenSSH `AuthorizedKeysCommand` + `ForceCommand` piping to the daemon and an in-process Go SSH server. |
| R-11 | **Open Ruk gaps leak into forge features:** shallow/partial fetch, symbolic refs, LFS payload sync, bounded ref-list and reflog methods (`progress.md:21,24,94`). | Medium / medium | Assign each gap to the slice that needs it. The bounded ref-list goes in S0's R2 subset (branch pages); symbolic refs in S0 (HEAD/default branch); shallow fetch before dogfood, because CI checkouts need it. |

### 1.5 Sequencing
| # | Decision |
|---|---|
| S-D1 | **Walking skeleton S0** (proven by stock `git`, `curl`, one MCP client and one browser). (a) `rukie admin user create` makes a user with an argon2id password. (b) The human logs in through HTML and creates a scoped token. (c) `repo.create` succeeds through **all four surfaces**: JSON HTTP (`curl` against the OpenAPI route), MCP, `rukie repo.create --json`, and an HTML form. (d) Stock `git push` and `git clone` work over Smart HTTP at `/{owner}/{repo}.git` with token Basic auth. (e) The HTML repo page shows the default branch's tree, a file, and a bounded branch list. (f) SQLite holds users, tokens and repos. (g) One `ruk serve` daemon is reached through the generated Go client. These stay green in CI from then on. |
| S-D2 | **Ruk pieces needed for S0:** R1 `ruk serve` with a forge-neutral handshake profile; repo create/delete; Smart HTTP upload/receive-pack over daemon invocations; tree/blob read; bounded paginated ref list; HEAD symref. |
| S-D3 | **Order after S0:** S1 review loop → S2 push mirrors + perf gate → **dogfood** → S3 orgs/teams/scopes → S4 CI → S5 tasks/claims → S6 events → S7 search → S8 SSH → S9 releases → S10 TOTP + OIDC → S11 social. |
| S-D4 | **Dissolve Phase U.** Design tokens, base layout and zero-JS form CSS land in S0. Each slice ships its own pages. |
| S-D5 | **Dissolve Phase V.** Each slice adds its e2e flow to the gate; budgets are set in S0 and checked every slice. A full security review blocks the move from dogfood to any external user. |
| S-D6 | **K1 (delete the Gitea tree) and S0 land as one short commit sequence.** Every commit builds and passes `go vet` and `go test ./...`. |

### 1.6 Reversibility
| # | Door | Type | Decision |
|---|---|---|---|
| V-1 | Deleting the Gitea tree | Two-way (it stays in git history) | Proceed (settled). |
| V-2 | Metadata schema | Two-way until the first external user | No compatibility migrations before v0.1. Dev data may be reset; one forward-only migration chain. |
| V-3 | Ruk on-disk store format | One-way once dogfood starts | Changes require an explicit store-version bump (the existing `E_VERSION` marker check, `progress.md:73`). The GitHub mirror (S2) is the exit path before dogfood. |
| V-4 | Op names, JSON shapes, MCP tool names | Two-way until external agents depend on them | `/v0` prefix. Breaking renames go in the changelog until v0.1. |
| V-5 | Webhook payloads, clone URL shape | One-way, external | Clone URL is settled. Event schemas are frozen per type when S6 lands. |
| V-6 | rukie ↔ Ruk daemon protocol | Internal, two-way | `protocol_major`/`minor` already exist in `daemon/ipc.rs`; with the Ruk pin, every bump is atomic. |
| V-7 | Store engine | Two-way behind the store module | SQLite now (C-Q2). Revisit only on a measured write-contention miss. |
| V-8 | Licence | Effectively one-way | MIT; no GPL-origin code (R-5). |

### 1.7 Open questions for the approval gate
- **C-Q1:** which rukie checkout is canonical?
- **C-Q2:** confirm SQLite.
- **C-Q3:** should rukie supervise the Ruk daemon by default?
- **R-10:** who terminates SSH (decide by S8)?

## 2. Amended skeleton (CEO view)
Each slice below builds on every commit and keeps the e2e flows of all earlier slices green.

| Slice | rukie | Ruk prerequisite | Proof |
|---|---|---|---|
| S0 skeleton | K1 tree delete + module path; K2 op table (repo.create/get, token.create, session.login) on JSON/MCP/CLI/HTML; K3 SQLite store; K4 generated daemon client; design tokens + layout | R1 `ruk serve`; repo create/delete; Smart HTTP over daemon; tree/blob read; bounded ref list; HEAD symref | `curl` + MCP + CLI + HTML repo.create; stock git clone/push; HTML tree view |
| S1 review loop | changes/stacks ops + pages, branch view projection, land | R5 receive notification + `refs/for` change creation; R2 compare/diff/merge-base; land.prepare/execute via daemon | push `refs/for` → change → review → land |
| S2 mirrors + perf | mirror ops, on-ref-update trigger + periodic reconcile | R4 per-call credentials + per-ref status; fetch/lookup perf; shallow fetch | exact and ff-only GitHub mirror; daemon-path budgets met → **dogfood** |
| S3 orgs/teams | orgs, teams, repo permissions, op/repo token scopes | — | token limited to one repo and op set |
| S4 CI | scheduler, runner, sandbox, CAS cache, logs | attest.add/list via daemon | C-D3 (a)–(d) |
| S5 tasks | tasks, claims as Ruk leases | R6 claim/attest extensions | two agents cannot claim one task |
| S6 events | Standard Webhooks, inbox | R2 op-log tail subscription | signed delivery + retry; inbox entries |
| S7 search | search op + page | R3 | bounded p95 on dogfood repos |
| S8 SSH | SSH entry | serve over daemon | stock git over ssh |
| S9 releases | releases, archives | R2 streaming archive | tarball download |
| S10 auth+ | TOTP, OIDC | — | login flows |
| S11 social | explore, stars, watches, forks | R2 fork | fork + star |

## 3. Implementation tasks (CEO)
These go into `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` (new file), not the `ruk/tasks` ledger.

- **CEO-01:** Settle the canonical rukie checkout (`git-renewed/rukie` vs `ruk/rukie`) and record it in both AGENTS.md files. *Rationale:* two copies split commits (C-Q1).
- **CEO-02:** Mark the Forgejo-bound decisions in `AGENT_NATIVE_GIT_PLAN.md:92-96` and the old `ruk/tasks` ledger entries as superseded, and say which ones carry over. *Rationale:* avoids following two contradictory plans (C-D5).
- **CEO-03:** Write the product statement, named non-goals and audience in the rukie README. *Rationale:* fixes scope before code (C-D1, A-D1, A-D2).
- **CEO-04:** Record the SQLite store decision and the "no compatibility migrations before v0.1" rule. *Rationale:* unblocks K3 and keeps schema changes cheap (C-Q2, V-2).
- **CEO-05:** Restructure the plan into slices S0–S11, each with its Ruk prerequisites, and dissolve Phases R, U and V. *Rationale:* every commit keeps a working product; no big-bang Ruk phase (S-D3–S-D5, R-3).
- **CEO-06:** Make the S0 acceptance script (four-surface `repo.create`, stock git push/clone, HTML tree view) a permanent CI gate. *Rationale:* smallest proof of the product (S-D1).
- **CEO-07:** Make the daemon protocol contract-first: schema source of truth, generated Go client, per-method conformance tests, and a Ruk commit pin in rukie CI. *Rationale:* contains churn from rewriting both sides at once (R-1, V-6).
- **CEO-08:** Set daemon-path budgets, with incremental fetch and single-ref lookup as the primary targets plus clone/advertisement checks, add a daemon load harness, and gate dogfood on them. *Rationale:* fetch is 7.5× and lookup 4× git; capacity acceptance is OPEN (R-2).
- **CEO-09:** Refresh the stale `ruk/README.md` claims (transport, HTTP client, capacity numbers) from current source and `RESULTS.md`. *Rationale:* the stale README misdirects planning (§0 facts).
- **CEO-10:** Remove the Forgejo residue only after rukie S0 passes e2e, and land R1's neutral rename as a clean cutover. *Rationale:* keeps one working prototype at all times (R-4).
- **CEO-11:** Adopt the licence provenance rule: port only campaign-authored Ruk patch code from `ruk/forgejo`, keep rukie MIT, and record provenance. *Rationale:* avoids GPL-3.0 contamination (R-5, V-8).
- **CEO-12:** Limit K2 to the S0 ops on all four surfaces and allow per-op HTML template overrides. *Rationale:* prevents speculative abstraction (R-6).
- **CEO-13:** Ship token scopes in S0, and op/repo scopes plus orgs/teams in S3, before CI. *Rationale:* bounds agent authority before running agent code (R-8).
- **CEO-14:** Write the falsifiable CI v1 bar and the single-host sandbox scope. *Rationale:* makes "better CI" testable and bounded (C-D3, R-7).
- **CEO-15:** Map the open Ruk gaps (symrefs, bounded ref list, shallow fetch, LFS sync, reflog page) to the slices that need them. *Rationale:* prevents hidden blockers in forge features (R-11).
- **CEO-16:** Put push mirrors (S2) before dogfood and make the GitHub mirror mandatory for self-hosted ruk/rukie. *Rationale:* gives a guaranteed exit while the Ruk format is young (I-D2, V-3).
- **CEO-17:** Start dogfooding ruk and rukie on self-hosted rukie when S2 and the perf gate pass. *Rationale:* real agent-fleet use is the success metric (I-D3).
- **CEO-18:** Define the change as the review unit, with the branch view as a read-only projection. *Rationale:* stops two review models from drifting apart (C-D2).
- **CEO-19:** Decide whether rukie supervises the Ruk daemon by default (with an external-socket flag). *Rationale:* one-command install versus a single point of failure (C-Q3, R-9).
- **CEO-20:** Defer SSH to S8 and assign the termination design to ENG. *Rationale:* HTTP tokens cover agents (R-10).
- **CEO-21:** Version the op table as `/v0`, freeze webhook event schemas per type at S6, and changelog breaking renames. *Rationale:* controls the external one-way doors (V-4, V-5).
- **CEO-22:** Freeze the Ruk store format behind a store-version bump once dogfood starts. *Rationale:* user repo data is a one-way door (V-3).
- **CEO-23:** Bound search v1 to an index-free, capped, cursor-paginated Ruk scan. *Rationale:* "lightweight, no heavy index" (C-D4).

[Showing lines 1-413 of 660 (50.0KB limit). Use :414 to continue]
- **CEO-23:** Bound search v1 to an index-free, capped, cursor-paginated Ruk scan. *Rationale:* "lightweight, no heavy index" (C-D4).
- **CEO-24:** Require each slice to ship both the agent write path and the HTML read view, and to add its e2e flow to the gate. *Rationale:* humans must see what agents did (A-D3, S-D5).
- **CEO-25:** Block any external user on a security review of token scopes, the CI sandbox, mirror credentials and webhook signing. *Rationale:* multi-tenant code execution under human-authority tokens (R-8).

```jsonl
{"id":"CEO-01","description":"Settle canonical rukie checkout (git-renewed/rukie vs ruk/rukie) and record it in both AGENTS.md files","phase":"CEO"}
{"id":"CEO-02","description":"Mark Forgejo-bound decisions in ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:92-96 and old ruk/tasks ledger entries superseded, naming what carries over","phase":"CEO"}
{"id":"CEO-03","description":"Write product statement, named non-goals, and audience into rukie README","phase":"CEO"}
{"id":"CEO-04","description":"Record SQLite metadata store decision and no-compat-migrations-before-v0.1 rule","phase":"CEO"}
{"id":"CEO-05","description":"Restructure plan into vertical slices S0-S11 with per-slice Ruk prerequisites; dissolve separate Phases R, U, V","phase":"CEO"}
{"id":"CEO-06","description":"Make S0 acceptance script (repo.create via JSON HTTP, MCP, CLI, HTML; stock git push/clone; HTML tree view) a permanent CI gate","phase":"CEO"}
{"id":"CEO-07","description":"Make Ruk daemon protocol contract-first: schema source of truth, generated Go client, per-method conformance tests, Ruk commit pin in rukie CI","phase":"CEO"}
{"id":"CEO-08","description":"Set daemon-path budgets targeting incremental fetch (7.5x git) and single-ref lookup (4x git) plus clone/advertisement checks, with load harness; gate dogfood on them","phase":"CEO"}
{"id":"CEO-09","description":"Refresh stale ruk/README.md transport, HTTP client, and capacity claims from source and tasks/capacity/RESULTS.md","phase":"CEO"}
{"id":"CEO-10","description":"Remove Ruk Forgejo residue only after rukie S0 e2e passes; land R1 neutral ruk serve rename as clean cutover","phase":"CEO"}
{"id":"CEO-11","description":"Adopt licence provenance rule: port only campaign-authored Ruk patch code from GPL-3.0 ruk/forgejo, keep rukie MIT, record provenance","phase":"CEO"}
{"id":"CEO-12","description":"Limit K2 op table to S0 ops across JSON HTTP/OpenAPI, MCP, CLI, HTML; allow per-op HTML template overrides","phase":"CEO"}
{"id":"CEO-13","description":"Ship token scopes in S0 and op/repo-selector scopes plus orgs/teams in S3, before CI","phase":"CEO"}
{"id":"CEO-14","description":"Write falsifiable CI v1 bar (durable restart, CAS step cache key, one attestation per job, bounded idempotent retry) and single-host sandbox scope","phase":"CEO"}
{"id":"CEO-15","description":"Map open Ruk gaps (symrefs, bounded ref list, shallow fetch, LFS sync, reflog page) to the slices needing them","phase":"CEO"}
{"id":"CEO-16","description":"Order push mirrors (S2) before dogfood and require GitHub mirror for self-hosted ruk/rukie","phase":"CEO"}
{"id":"CEO-17","description":"Start dogfooding ruk and rukie development on self-hosted rukie when S2 and perf gate pass","phase":"CEO"}
{"id":"CEO-18","description":"Define change-as-review-unit model with branch view as read-only projection","phase":"CEO"}
{"id":"CEO-19","description":"Decide default supervision of Ruk daemon by rukie with external-socket flag","phase":"CEO"}
{"id":"CEO-20","description":"Defer SSH to S8 and assign termination design (OpenSSH ForceCommand vs in-process Go SSH) to ENG","phase":"CEO"}
{"id":"CEO-21","description":"Version op table as /v0, freeze webhook event schemas per type at S6, changelog every breaking rename","phase":"CEO"}
{"id":"CEO-22","description":"Freeze Ruk store format behind explicit store-version bump once dogfood starts","phase":"CEO"}
{"id":"CEO-23","description":"Bound search v1 to index-free capped cursor-paginated Ruk scan; allow index only after measured p95 miss","phase":"CEO"}
{"id":"CEO-24","description":"Require each slice to ship agent write path plus human HTML read view and add its e2e flow to the gate","phase":"CEO"}
{"id":"CEO-25","description":"Block any external user on security review of token scopes, CI sandbox, mirror credentials, webhook signing","phase":"CEO"}
```

### Critical Files for Implementation
- `ruk/crates/ruk/src/daemon/mod.rs` and `ruk/crates/ruk/src/daemon/ipc.rs`: the neutral `ruk serve` rename, the handshake profile and the protocol version.
- `ruk/forgejo/modules/git/ruk_ipc.go` and `ruk/forgejo/modules/git/backend_ruk.go`: the Go client to port for K4, under the provenance rule.
- `ruk/crates/ruk-git/src/transport/smart_http.rs`, `push.rs` and `http.rs`: Smart HTTP serving for S0, plus per-call credentials and keep-alive for S2 mirrors.
- `ruk/crates/ruk-git/src/transport/proc_receive.rs`: `refs/for` change creation for the S1 review loop.
- `ruk/tasks/capacity/run.sh` and `RESULTS.md`, plus `ruk/.outline/sdd/progress.md`: the perf baseline and open gaps behind CEO-08 and CEO-15.

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Correct R-2 and CEO-07 before pub…

---
## Later corrections from the same agent (apply over the body above)

# Autoplan CEO review: rukie, an agent-first forge on Ruk

This is the complete `autoplan-ceo.md` content. I'm in read-only plan mode with no write tool, so the orchestrator needs to save it under that name.

The latest reviewer notes needed no further changes:
- **Clone vs fetch:** R-2 and CEO-08 already use the `RESULTS.md` numbers. Clone is 1.25× git, fetch is 7.5×, lookup is 4×. The gate targets fetch and lookup.
- **Duplicate IDs:** the CEO IDs go into a new ledger at `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`. They must not be appended to `ruk/tasks/autoplan-ledger.jsonl`, which already uses `CEO-01`..`03` for an older plan.

## 0. Inputs, scope of this phase, and ledger placement
- **Inputs.** The goal, the settled decisions and the draft skeleton came inline in the assignment. This review does not reopen the settled decisions. It answers four questions:
  1. What is the product, and what is outside it?
  2. What is the smallest end-to-end slice that proves it, so every later commit leaves a working product?
  3. In what order should features land, ranked by impact on heavy agentic engineering?
  4. What could sink the whole campaign?
- **Earlier plan that this one overrides.** `ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:92-96` records an earlier autoplan whose binding decisions assume a Forgejo patch target. Line 94 says "The Forgejo patch surface is frozen as a checked-in inventory". This campaign drops Forgejo as the target, so those decisions need an explicit "superseded" mark (CEO-02).
- **Task-ID collision.** `ruk/tasks/autoplan-ledger.jsonl` already holds `CEO-01`..`03`, `DES-01`..`03`, `DX-01`..`03` and `ENG-01`..`06` from that earlier campaign, with different content.
  - This campaign's ledger is `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`. It does not exist yet and gets created.
  - The CEO IDs below start at `CEO-01` in that new ledger only.
- **This phase's output.** The register plus tasks. Writing to the ledger and amending the plan belong to the final autoplan step, after all four phases.

### Facts found in the trees
- **There is a working end-to-end prototype in another tree.** `ruk/forgejo/` is a Forgejo tree running on Ruk through a Go IPC client in `ruk/forgejo/modules/git/`: `ruk_ipc.go`, `backend_ruk.go`, `ruk_capability.go` and `ruk_migrate.go`. `ruk/.outline/sdd/progress.md:23` marks "Forgejo daemon and patch" done.
- **Forgejo residue in Ruk is spread across crates.**
  - `ForgejoCommand` and the `"ruk-forgejo-serve"` thread name in `crates/ruk/src/main.rs` and `crates/ruk/src/daemon/mod.rs`.
  - The handshake `profile: "forgejo"` in `crates/ruk/src/daemon/ipc.rs`.
  - Hook dispatch modelled on Forgejo in `crates/ruk-git/src/transport/{hooks,proc_receive,mirror}.rs`.
  - Mentions in the error-code docs in `crates/ruk-core/src/error.rs`.
- **Measured gaps are in fetch and lookup, not clone.** Numbers below are from `ruk/tasks/capacity/RESULTS.md` (commit `78a98e2`), p50, warm, CLI-level, on the 80-CPU host:

  | Workload | Ruk | git | Ratio |
  |---|---|---|---|
  | incremental `fetch` | 659 ms | 88 ms | **7.5×** |
  | single-ref `lookup` | 36 ms | 9 ms | **4×** |
  | `write` | 49 ms | 26 ms | 1.9× |
  | `clone` (10k branches) | 1,714 ms | 1,370 ms | 1.25× |
  | `adv-10k` | 58 ms | 53 ms | 1.1× |
  | `adv-1k` | 22 ms | 43 ms | 0.5× (Ruk faster) |
  | `page1` | 94 ms | 144 ms | 0.65× (Ruk faster) |

  - The README's higher figures (6.94 s clone, 990 ms fetch, commit `8487835b`) are stale.
  - Capacity acceptance is OPEN (`progress.md:27`).
  - Daemon-path and load rows are not measured: adv-budget, no-starvation, http-429-503, crash-around-publish (`progress.md:88`).
- **The README is partly stale on transports too.** It calls the `http(s)://` client unimplemented. In fact `crates/ruk-git/src/transport/http.rs:1-12` implements smart-http and LFS over rustls, with Basic/Bearer auth read from the environment and `Connection: close` on every request. HTTPS push mirrors are feasible now; what's missing is per-call credential injection (R4), and keep-alive is absent.
- **Open Ruk gaps that affect forge slices:**
  - shallow and partial fetch (`progress.md:21`);
  - LFS payload sync, symbolic refs, and wiki/template flows (`progress.md:24`);
  - Forgejo DB/browser migration gates (`progress.md:26`);
  - no bounded ref-list or ref-count IPC method, and no typed reflog page (`progress.md:94`).
- **The canonical rukie checkout is ambiguous.** `ruk/rukie/` is a near-copy of `/home/alpha/rewrite/git-renewed/rukie/`.
- **Licences (headers read):**
  - `rukie/LICENSE` is MIT (Gitea/Gogs).
  - `ruk/forgejo/LICENSE` is GNU GPL v3.
  - Ruk is Apache-2.0 (`ruk/README.md`).

## 1. Six-principle decision register

### 1.1 Clarity
| # | Decision / open question | Grounds |
|---|---|---|
| C-D1 | **Product statement:** "A single-host forge where many AI agents and a few supervising humans propose, verify, review and land changes on Ruk repositories. Every action is one operation, reachable as HTML, JSON HTTP, MCP or CLI." Anything else goes in a later slice. | Goal |
| C-D2 | **The review unit is the Ruk change (change-id).** A stack is an ordered set of changes. The branch view is a read-only projection with no review state of its own. | Settled: both review views |
| C-D3 | **CI v1 bar, stated as testable properties.** v1 must meet all four: (a) queued or running jobs survive a `rukie` restart and a runner crash; (b) step results are keyed by (tree OID, recipe hash, runner image digest) and reused across changes, rebases and forks; (c) each finished job writes exactly one Ruk attestation, readable without the DB; (d) per-step retry is capped and never duplicates side effects. Anything else is out of v1. | "More resilient, durable and efficient than GitHub/GitLab CI" is not testable as written |
| C-D4 | **Search v1 has no persistent index.** It is a bounded, parallel Ruk scan with byte and time caps and a cursor. An index is allowed only after a measured p95 miss. | Settled |
| C-D5 | **The earlier Forgejo-bound plan decisions are superseded.** The merge-notice and effective-mode decisions carry over without Forgejo as a surface. The patch-inventory freeze is retired. | §0 |
| C-Q1 | **Open:** which rukie checkout is canonical? | Both exist |
| C-Q2 | **Open:** metadata store. **CEO recommendation: pure-Go SQLite** (single host, co-located with the Unix-socket daemon, transactional, zero ops). PostgreSQL stays possible behind the store module; Pebble lacks a query layer. ENG makes the final call. | Settled: OPEN |

### 1.2 Impact
| # | Decision | Rationale |
|---|---|---|
| I-D1 | **The loop that matters most:** agent token → push to `refs/for/<branch>` → change → CI attestation → review → land, with conflicts and claims visible to the next agent. | Goal |
| I-D2 | **Impact ranking:** (1) hosting + Smart HTTP + tokens; (2) review + land; (3) push mirrors; (4) orgs/teams + scoped tokens; (5) CI; (6) tasks/claims; (7) events; (8) search; (9) SSH; (10) releases; (11) TOTP + OIDC; (12) social. | Agents need 1, 2, 4–7; humans 2 and 7; adoption 3 |
| I-D3 | **Dogfooding is the success metric.** From S2 on, ruk and rukie development moves onto self-hosted rukie, push-mirrored to GitHub. | Real use proves the product |

### 1.3 Audience
| # | Decision | Rationale |
|---|---|---|
| A-D1 | **Primary user:** agents using scoped tokens via MCP, the CLI and JSON. **Secondary user:** humans using zero-JS HTML. If an op must favour one, the agent shape wins. | Settled identity model |
| A-D2 | **Non-goals for now:** anonymous open-source communities, LDAP/SAML/SCIM, email workflows, users expecting Gitea/Forgejo API v1. | Settled cuts |
| A-D3 | **Every slice ships both the agent write path and the human HTML read view.** | Supervision |

### 1.4 Risk
| # | Risk | Likelihood / impact | Mitigation |
|---|---|---|---|
| R-1 | **Contract churn from rewriting both sides at once.** | High / high | Contract-first schema; generated Go client; per-method conformance tests in Ruk; rukie pins an exact Ruk commit and builds it in CI. |
| R-2 | **Hot-path latency for agent fleets.** Fetch is 7.5× and lookup 4× git; clone (1.25×) and advertisement are near parity. Capacity acceptance is OPEN and the daemon path is unmeasured. | High / high | Perf gate before dogfood, aimed at **daemon-path fetch and lookup**, plus daemon-path clone/advertisement checks. Missing the gate puts Ruk perf work ahead of rukie features. |
| R-3 | **Big-bang Ruk phase.** | High / medium | Dissolve "Phase R first"; each slice names only the Ruk pieces it needs. |
| R-4 | **Losing the only working prototype.** | Medium / high | Remove the Forgejo residue only after S0 e2e passes; R1's rename lands as a clean cutover. |
| R-5 | **GPL contamination** (`ruk/forgejo` is GPL-3.0, rukie is MIT). | Medium / high | Port only code written by the campaign, record provenance, and keep rukie MIT with the Gitea/Gogs notice. |
| R-6 | **Speculative op-table abstraction.** | Medium / medium | K2 ships only the S0 ops on all four surfaces; per-op HTML overrides are allowed. |
| R-7 | **CI scope explosion.** | High / high | Bound by C-D3: single host, namespace sandbox, no runner fleet, no GitHub Actions YAML. |
| R-8 | **Agent-written code running under tokens that carry human authority.** | Medium / critical | Token scopes in S0; op/repo scopes in S3, before CI. CI gets short-lived per-job credentials. Mirror credentials are injected per call and never persisted. |
| R-9 | **Daemon is a single point of failure.** | Medium / medium | C-Q3: rukie supervises `ruk serve` by default, with an external-socket flag. |
| R-10 | **Nobody owns SSH termination.** | Medium / low | Defer to S8; ENG chooses OpenSSH ForceCommand or an in-process Go SSH server. |
| R-11 | **Open Ruk gaps leak into forge features.** | Medium / medium | Ref list and symrefs in S0; shallow fetch before dogfood; LFS and reflog where needed. |

### 1.5 Sequencing
| # | Decision |
|---|---|
| S-D1 | **Walking skeleton S0.** (a) An admin CLI creates a user with an argon2id password. (b) The human logs in through HTML and creates a scoped token. (c) `repo.create` succeeds through JSON HTTP (`curl`), MCP, the CLI and an HTML form. (d) Stock `git push` and `git clone` work over Smart HTTP at `/{owner}/{repo}.git` with token auth. (e) The HTML page shows the tree, a file and a bounded branch list. (f) SQLite holds users, tokens and repos. (g) One `ruk serve` daemon is reached through the generated Go client. These stay green in CI from then on. |
| S-D2 | **Ruk pieces needed for S0:** R1 `ruk serve` with a neutral profile; repo create/delete; Smart HTTP over the daemon; tree/blob read; bounded ref list; HEAD symref. |
| S-D3 | **Order after S0:** S1 review → S2 mirrors + perf gate → **dogfood** → S3 orgs/scopes → S4 CI → S5 tasks → S6 events → S7 search → S8 SSH → S9 releases → S10 TOTP/OIDC → S11 social. |
| S-D4 | **Dissolve Phase U.** Tokens and layout land in S0; each slice ships its own pages. |
| S-D5 | **Dissolve Phase V.** Each slice adds its e2e flow to the gate; a security review comes before any external user. |
| S-D6 | **K1 and S0 land as one short sequence**, with every commit building and its tests passing. |

### 1.6 Reversibility
| # | Door | Type | Decision |
|---|---|---|---|
| V-1 | Deleting the Gitea tree | Two-way | Proceed. |
| V-2 | Metadata schema | Two-way before v0.1 | No compatibility migrations before v0.1. |
| V-3 | Ruk store format | One-way once dogfood starts | Changes require an explicit store-version bump (`E_VERSION`, `progress.md:73`); the GitHub mirror is the exit path. |
| V-4 | Op, JSON and MCP names | Two-way before external agents depend on them | `/v0` prefix; breaking renames go in the changelog. |
| V-5 | Webhook payloads, clone URL shape | One-way, external | Freeze each event schema when S6 lands. |
| V-6 | Daemon protocol | Internal, two-way | Version handshake plus Ruk pin. |
| V-7 | Store engine | Two-way | SQLite now. |
| V-8 | Licence | Effectively one-way | MIT; no GPL code. |

### 1.7 Open questions for the approval gate
- **C-Q1:** which rukie checkout is canonical?
- **C-Q2:** confirm SQLite.
- **C-Q3:** should rukie supervise the Ruk daemon by default?
- **R-10:** who terminates SSH (decide by S8)?

## 2. Amended skeleton (CEO view)
| Slice | rukie | Ruk prerequisite | Proof |
|---|---|---|---|
| S0 | K1 tree delete; K2 ops on JSON/MCP/CLI/HTML; K3 SQLite; K4 client; tokens + layout | R1; repo create/delete; Smart HTTP; tree/blob; ref list; symref | four-surface repo.create; git clone/push; HTML tree |
| S1 | changes/stacks, branch view, land | R5; R2 diff/compare/merge-base; land via daemon | `refs/for` → change → land |
| S2 | mirrors, ref-update trigger + reconcile | R4; fetch/lookup perf; shallow fetch | GitHub mirror; budgets met → dogfood |
| S3 | orgs, teams, scopes | — | scoped token enforced |
| S4 | CI | attest via daemon | C-D3 (a)–(d) |
| S5 | tasks/claims | R6 | exclusive claim |
| S6 | webhooks, inbox | op-log tail | signed delivery |
| S7 | search | R3 | bounded p95 |
| S8 | SSH | serve over daemon | git over ssh |
| S9 | releases | streaming archive | tarball |
| S10 | TOTP, OIDC | — | login flows |
| S11 | social, forks | fork | fork + star |

## 3. Implementation tasks (CEO)
These go into `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl` only.

- **CEO-01:** Settle the canonical rukie checkout and record it in both AGENTS.md files. *Rationale:* two copies split commits (C-Q1).
- **CEO-02:** Mark the Forgejo-bound decisions in `AGENT_NATIVE_GIT_PLAN.md:92-96` and the old `ruk/tasks` ledger entries as superseded, naming what carries over. *Rationale:* avoids following two contradictory plans (C-D5).
- **CEO-03:** Write the product statement, non-goals and audience in the rukie README. *Rationale:* fixes scope (C-D1, A-D1, A-D2).
- **CEO-04:** Record SQLite and the "no compatibility migrations before v0.1" rule. *Rationale:* unblocks K3 (C-Q2, V-2).
- **CEO-05:** Restructure the plan into slices S0–S11 and dissolve Phases R, U and V. *Rationale:* every commit keeps a working product (S-D3–S-D5, R-3).
- **CEO-06:** Make the S0 acceptance script a permanent CI gate. *Rationale:* smallest product proof (S-D1).
- **CEO-07:** Make the daemon protocol contract-first, with a generated Go client, conformance tests and a Ruk pin. *Rationale:* contains churn (R-1, V-6).
- **CEO-08:** Set daemon-path budgets for fetch and lookup plus clone/advertisement checks, add a load harness, and gate dogfood on them. *Rationale:* fetch is 7.5× and lookup 4× git; acceptance is OPEN (R-2).
- **CEO-09:** Refresh the stale `ruk/README.md` transport and capacity claims. *Rationale:* the stale README misdirects planning (§0).
- **CEO-10:** Remove the Forgejo residue after S0 e2e passes; land R1 as a clean cutover. *Rationale:* keeps one working prototype (R-4).
- **CEO-11:** Adopt the licence provenance rule. *Rationale:* avoids GPL contamination (R-5, V-8).
- **CEO-12:** Limit K2 to the S0 ops on four surfaces, with HTML overrides allowed. *Rationale:* prevents speculative abstraction (R-6).
- **CEO-13:** Token scopes in S0; op/repo scopes plus orgs/teams in S3, before CI. *Rationale:* bounds agent authority (R-8).
- **CEO-14:** Write the CI v1 bar and the sandbox scope. *Rationale:* makes CI testable and bounded (C-D3, R-7).
- **CEO-15:** Map the open Ruk gaps to the slices that need them. *Rationale:* prevents hidden blockers (R-11).
- **CEO-16:** Put mirrors before dogfood and require the GitHub mirror. *Rationale:* guaranteed exit (I-D2, V-3).
- **CEO-17:** Start dogfooding after S2 and the perf gate. *Rationale:* success metric (I-D3).
- **CEO-18:** Define the change as the review unit, with the branch view as a read-only projection. *Rationale:* one review model (C-D2).
- **CEO-19:** Decide whether rukie supervises the daemon by default. *Rationale:* install simplicity versus a single point of failure (C-Q3, R-9).
- **CEO-20:** Defer SSH to S8 and assign its design to ENG. *Rationale:* HTTP covers agents (R-10).
- **CEO-21:** `/v0` op versioning, webhook schema freeze at S6, and a changelog of renames. *Rationale:* external one-way doors (V-4, V-5).
- **CEO-22:** Freeze the store format behind a version bump once dogfood starts. *Rationale:* user data is a one-way door (V-3).
- **CEO-23:** Bound search v1 to an index-free, capped scan. *Rationale:* "lightweight" (C-D4).
- **CEO-24:** Each slice ships the agent write path plus the HTML read view, plus its e2e flow. *Rationale:* human supervision (A-D3, S-D5).
- **CEO-25:** Block any external user on a security review. *Rationale:* multi-tenant code execution (R-8).

```jsonl
{"id":"CEO-01","description":"Settle canonical rukie checkout (git-renewed/rukie vs ruk/rukie) and record it in both AGENTS.md files","phase":"CEO"}
{"id":"CEO-02","description":"Mark Forgejo-bound decisions in ruk/.omp/plans/AGENT_NATIVE_GIT_PLAN.md:92-96 and old ruk/tasks ledger entries superseded, naming what carries over","phase":"CEO"}
{"id":"CEO-03","description":"Write product statement, named non-goals, and audience into rukie README","phase":"CEO"}
{"id":"CEO-04","description":"Record SQLite metadata store decision and no-compat-migrations-before-v0.1 rule","phase":"CEO"}
{"id":"CEO-05","description":"Restructure plan into vertical slices S0-S11 with per-slice Ruk prerequisites; dissolve separate Phases R, U, V","phase":"CEO"}
{"id":"CEO-06","description":"Make S0 acceptance script (repo.create via JSON HTTP, MCP, CLI, HTML; stock git push/clone; HTML tree view) a permanent CI gate","phase":"CEO"}
{"id":"CEO-07","description":"Make Ruk daemon protocol contract-first: schema source of truth, generated Go client, per-method conformance tests, Ruk commit pin in rukie CI","phase":"CEO"}
{"id":"CEO-08","description":"Set daemon-path budgets targeting incremental fetch (7.5x git) and single-ref lookup (4x git) plus clone/advertisement checks, with load harness; gate dogfood on them","phase":"CEO"}
{"id":"CEO-09","description":"Refresh stale ruk/README.md transport, HTTP client, and capacity claims from source and tasks/capacity/RESULTS.md","phase":"CEO"}
{"id":"CEO-10","description":"Remove Ruk Forgejo residue only after rukie S0 e2e passes; land R1 neutral ruk serve rename as clean cutover","phase":"CEO"}
{"id":"CEO-11","description":"Adopt licence provenance rule: port only campaign-authored Ruk patch code from GPL-3.0 ruk/forgejo, keep rukie MIT, record provenance","phase":"CEO"}
{"id":"CEO-12","description":"Limit K2 op table to S0 ops across JSON HTTP/OpenAPI, MCP, CLI, HTML; allow per-op HTML template overrides","phase":"CEO"}
{"id":"CEO-13","description":"Ship token scopes in S0 and op/repo-selector scopes plus orgs/teams in S3, before CI","phase":"CEO"}
{"id":"CEO-14","description":"Write falsifiable CI v1 bar (durable restart, CAS step cache key, one attestation per job, bounded idempotent retry) and single-host sandbox scope","phase":"CEO"}
{"id":"CEO-15","description":"Map open Ruk gaps (symrefs, bounded ref list, shallow fetch, LFS sync, reflog page) to the slices needing them","phase":"CEO"}
{"id":"CEO-16","description":"Order push mirrors (S2) before dogfood and require GitHub mirror for self-hosted ruk/rukie","phase":"CEO"}
{"id":"CEO-17","description":"Start dogfooding ruk and rukie development on self-hosted rukie when S2 and perf gate pass","phase":"CEO"}
{"id":"CEO-18","description":"Define change-as-review-unit model with branch view as read-only projection","phase":"CEO"}
{"id":"CEO-19","description":"Decide default supervision of Ruk daemon by rukie with external-socket flag","phase":"CEO"}
{"id":"CEO-20","description":"Defer SSH to S8 and assign termination design (OpenSSH ForceCommand vs in-process Go SSH) to ENG","phase":"CEO"}
{"id":"CEO-21","description":"Version op table as /v0, freeze webhook event schemas per type at S6, changelog every breaking rename","phase":"CEO"}
{"id":"CEO-22","description":"Freeze Ruk store format behind explicit store-version bump once dogfood starts","phase":"CEO"}
{"id":"CEO-23","description":"Bound search v1 to index-free capped cursor-paginated Ruk scan; allow index only after measured p95 miss","phase":"CEO"}
{"id":"CEO-24","description":"Require each slice to ship agent write path plus human HTML read view and add its e2e flow to the gate","phase":"CEO"}
{"id":"CEO-25","description":"Block any external user on security review of token scopes, CI sandbox, mirror credentials, webhook signing","phase":"CEO"}
```

### Critical Files for Implementation
- `ruk/crates/ruk/src/daemon/mod.rs` and `ruk/crates/ruk/src/daemon/ipc.rs`: the neutral `ruk serve`, handshake profile and protocol version.
- `ruk/forgejo/modules/git/ruk_ipc.go` and `ruk/forgejo/modules/git/backend_ruk.go`: the Go client to port for K4, under the provenance rule.
- `ruk/crates/ruk-git/src/transport/smart_http.rs`, `push.rs` and `http.rs`: Smart HTTP serving for S0, plus per-call credentials and keep-alive for S2 mirrors.
- `ruk/crates/ruk-git/src/transport/proc_receive.rs`: `refs/for` change creation for S1.
- `ruk/tasks/capacity/run.sh` and `RESULTS.md`, plus `ruk/.outline/sdd/progress.md`: the perf baseline and open gaps behind CEO-08 and CEO-15.

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Create `autoplan-ceo.md` and …