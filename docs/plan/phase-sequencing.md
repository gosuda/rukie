Adopted — R3 was an omission from §9's exact list: the slice contains K6 (upload/receive-pack over the daemon), and R3 removes the argv path in favor of the git-service envelope, so the S0 chain is R1→R2→R3→…→K6. Added. Final artifact (single change to §9):

---

# phase-sequencing.md — Ordered execution structure for the rukie↔ruk campaign

Grounded in: `arch-sketch-monolith.md` (§12 R1–R14, §13 C1–C14, §11 CI hedge), `autoplan-ceo.md` (S0–S11 register, CEO-01…25, C-D3, I-D2, R-1…R-11, V-doors; S-D1 verified at `:96` and `:310`), `ruk-serve-design.md` (§7 bump rules — single minor 1→2 bump, §8 slices 5–6 as separate buildable commits, §10.2 measure-first note), `ssh-smarthttp-design.md` (§8 R-items, §9 gaps), `mirror-design.md` (§2 RC-1…12, P0/P1/P2), `ci-control-plane-design.md` (§2, §13, §15), `ci-runner-sandbox-design.md` (`:399`, §17), `fork-social-releases-design.md` (§8.3), `ruk-search-design.md`, `store-engine-design.md`, `op-table-design.md` (§19), `rendering-design.md` (§14), `auth-design.md`, `permission-model.md`, `webhook-design.md`, `inbox-design.md`, `ops-design.md`, `verification-strategy.md` (§2–§9, phase→gate matrix), `autoplan-dx.md`, `autoplan-design.md` (DES-02), `url-information-architecture.md` (present; URL scheme authority: one URL/three renderings, `/-/` plumbing namespace, flat canonical pages, ServeMux grounding, CI routes `:188-192`, `:401-405`), `agents-md-draft.md` (one-concern-per-commit rule), `docs-readme-draft.md`, plus the campaign assignment's settled-decision list (2026-10-20 user answers).

## 0. Shape of the plan

Six phase labels, as required: **R** (ruk), **K** (rukie core), **F** (features), **U** (UI), **V** (verification), **D** (docs/publish readiness). The CEO register dissolves "Phase R first" and serial U/V stages (S-D3–S-D5, risk R-3); this plan keeps the required labels as **work streams**: R and K/F are the two repos' commit series; U and V interleave with F (each F commit ships its view and its gate — CEO-24/A-D3); D bookends. Every commit cross-references the CEO slice (S0–S11) it serves. **Every commit in either repo keeps both repos green** (§12); what varies per commit is the additional named gate. §13 crosswalks the arch sketch's canonical C1–C14 and R1–R14 onto this plan's IDs.

Ordering principle (CEO S-D3, I-D2): walking skeleton S0 → review loop S1 → mirrors + perf gate S2 → **dogfood** → orgs/scopes S3 → CI S4 → tasks S5 → events surfaces S6 → search S7 → SSH S8 → releases S9 → TOTP/OIDC S10 → social S11. R-side items land only as early as the rukie slice that needs them. URL shape authority is `url-information-architecture.md` (ratified by DES-02): routes land with K4 per that map.

---

## 1. Phase R — Ruk (`/home/alpha/rewrite/git-renewed/ruk`, Rust)

**Goal:** Forge-neutral `ruk serve` with hosted reads, streaming pack service, events, hosted landing, attest/claim extensions, repo lifecycle, search, and mirror transport — each landing just-in-time for its consumer slice, both repos' gates green throughout.

| # | Concern | What lands | Gate |
|---|---|---|---|
| R1 | Stateless RPC | `serve_store` (`verbs_transport.rs:1859-1876`) parses `--stateless-rpc`/`--advertise-refs`; advertisement + one-shot POST | `stateless_info_refs_matches_oracle_refs`, `stateless_post_matches_oracle_objects` via passgate |
| R2 | Neutral rename | `ruk serve` verb, thread name, docs; handshake profile `"rukie"`, `"forgejo"` accepted one dev cycle behind compat flag; `<root>/<owner>/<repo>.git` registry test | clap test + both repos' gates (`ruk-serve-design.md` slice 1; ssh R-1) |
| R3 | Streaming core | argv path removed → git-service envelope; per-stream tasks + writer task (Cancel reaches running invocations); typed replies wait for credit, reply window in `InvocationOpen`, `MAX_IPC_CONTROL_BYTES` 1 MiB→16 MiB, `ByteSource::Streamed`; HOL fix or blessed one-RPC-per-connection pool | envelope tests; HOL test (`ruk-serve-design.md` slices 2–3; arch R3/R4) |
| R4 | ReceivePolicy | `ReceivePolicy` trait; `ScriptPolicy` wraps `ReceiveHooks`; CLI unchanged | `script_policy_matches_existing_hook_outputs` |
| R5a | Events foundation (schema-free) | `FactTable::Events`/`EventHead`, emission in `Store::publish` with `EventSource` (all call sites this commit), retention sweep, `oplog::tracked` exclusion — store-level only, no method exposed, no schema change (`ruk-serve-design.md` slice 5; one concern: store rewiring). Pre-land step: §10.2 "measure `Store::publish` call sites" | store-level event tests |
| R5b | **Single schema bump** minor 1→2 | One commit carrying **all** additive methods: `events.tail` (ascending `after` cursor + per-op ref deltas + attestation subjects — reads the R5a tables), `refs.list` (+prefix seek), HEAD symref resolution, `tree` read, `repo.stat`+`RepoMeta`, `MethodKind::Admin`; required array + count + consistency test in the same commit (`ruk-serve-design.md` slice 6 + §7 single-bump rule; one concern: protocol additions) | bump consistency test; S0 read hosted tests |
| R6 | Hosted read arms | `file`, `log`, `diff`, `show`, `blame`, `query` served by `HostedCoordinator::read`; `RevisionRef.ref{name}`; merge-base/compare into core; `read_gap` cleanup | per-read hosted tests + dispatch coverage (arch R2; CI-control R4) |
| R7 | Receive-pack host path | `HostPolicy` frames 12/13 + `ServiceReceipts` + sync-async bridge; git-service receive-pack over socket; hook env channel (`RUK_INVOCATION_ID/RUK_HOOK_CALLBACK/RUK_HOOK_TOKEN` + spool path); landed report; durable ref-update journal (R-REFJOURNAL) pre-`post-receive`; native `refs/for/<branch>` diversion (Change-Id trailer), `ng`-unless-committed; hook diagnostics; outcome ledger keyed `(repo, request_id)` with server-side digest | `host_pre_receive_per_ref_accept_reject`, `host_divert_writes_review_ref_only`, `host_timeout_rejects_all_fail_closed`, `receive_replay_same_digest_no_second_publish`, `git_service_receive_pack_round_trip_over_socket` (`ruk-serve-design.md` §6.7; ssh R-5.x/R-REFJOURNAL; arch R5/R6) |
| R8 | Hosted landing + policy | `land.prepare` optional `remote`; `land.execute` prepare arm; `[land] requirements` + trusted signers + `policy_generation` + `policy.set`; deterministic candidates | land prepare/execute hosted tests; `E_STALE` retry test (arch R8; CI-control R3/R5) |
| R9 | Attest extensions | Payload v2 with `evidence` digest (`ruk-attest-payload-v2`); `Capabilities.attest` namespace grants (`ci/`); `attest.list` kind-prefix filter | v1+v2 golden vectors; namespace-grant tests (CI-control §13 R1/R2/R6) |
| R10 | Claims extensions | Opaque subjects `task:<id>`; `claim.renew`; agent-token holder model; expiry/heartbeat | task-semantics claim tests (arch R13; ssh R-6.2) |
| R11a | Repo lifecycle (S0) | `repo.init{object_format}`, `repo.delete`, `repo.rename`; `admin_uids`; hosted mode refuses empty `allowed_uids`; `on_behalf_of` in op.log | admin dispatch tests; uid-refusal test (ssh R-2.4; threat-model §5.1–5.2) |
| R11b | Fork | `repo.fork{from}` + `import_sealed_snapshot` primitive; destination generation `= G` in meta **and** registry epoch; guard + full fact-row CAS revalidation; sealed/tail partition; base-ordered copy | atomicity + mixed-snapshot-refusal tests (fork §8.3.1) |
| R12 | Released-tag freeze | `FactTable::ReleasedTags` in next store schema revision; all tag-mutation paths consult (`tag.set/delete`, receive/proc-receive) | freeze race test: push bypass refused (fork §8.3.2) |
| R13a | Mirror P0 (CLI) | `--auth-stdin` creds + `AuthPolicy::{Explicit, Ambient}`; fail-closed namespace denylist (`require_push` wired); per-ref ff isolation; error taxonomy (`E_PUSH_REJECTED/UNCERTAIN`, `E_REMOTE_MOVED`, `E_REMOTE_RATE_LIMITED`, `ErrorDetail::Http`); Uncertain-only-when-bytes-sent; redaction/URL hygiene | scripted-listener per-status tests; canary-token test (mirror RC-1…6) |
| R13b | Typed push transport | `push`, `push.reconcile`, `push.mirror{mode, select}` transport jobs; ephemeral creds out of digest/receipts; redirect/CA roots; interrupt on lease loss; remote-address guard | push returns populated refs; reconcile `equal_new/equal_old/moved` (RC-7…10; arch R9) |
| R14 | Search | `Search` method + core-owned `SearchPool`; path-ordered shards; `PageCursor` resume; budget truncation | read + budget/truncation tests (`ruk-search-design.md`) |
| R15 | Archive streaming | `TarWriter` factored; zip transcoder; streamed dispatch; daemon `upload-archive` verb | streamed round-trip vs oracle tar (`ruk-serve-design.md` slice 8) |
| R16 | Error frames | Full `ErrorReply` (hints/fixes/detail); failed vs cancelled distinct | error-frame goldens (arch R10) |
| R17 | Quotas | Per-repo `{max_pack_bytes, max_objects, max_object_bytes, max_delta_depth, max_stdin_bytes}` on `CommandOpen.limits`, joined into `command_digest` | quota-bypass replay refused (ssh R-QUOTA) |
| R18 | Durable git outcomes | Receipt-backed outcome ledger for git streams (replaces in-memory 10k) | restart-mid-stream lookup test (arch R14) |
| R19 | Cutover residue | Delete `ruk/forgejo` prototype + `forgejo-patch-inventory`; drop `"forgejo"` compat; README/CHANGELOG rewrite; `.gitignore` cleanup | both repos' gates + README matches source (CEO-09/10). **Gated on rukie S0 e2e green** |
| R20 | Perf lane (parallel) | Daemon-path fetch/lookup/clone/adv budgets; load harness; incremental-fetch work | capacity budgets met; `RESULTS.md` refreshed (CEO-08) |

Schema-bump discipline: **exactly one** minor bump per additive batch — R5b is the single minor 1→2 commit with all its methods; R5a touches no schema; later additive needs (search R14, archive R15, admin batch) ride the same §7 batching rule — never one bump per method. R5a must precede R5b (`events.tail` reads the R5a tables); both are independently buildable per §8.
**Dependencies:** R1→R2→R3 precede all wire work; R5a→R5b→R6; R5b(reads)+R6 before F1; R5a+R5b before K7 and CI triggers; R7 before K6-real-pushes, F2, F10; R8/R9 before F2-land/F5; R10 before F7; R13a/b before F3; R14→F9; R15+R12→F11; R11b→F13; R19 needs S0 e2e; R20 feeds V5.
**Parallelism:** R5a can start right after R3; R13a, R20, R14, R15, R16, R17, R18, R10, R11b, R12 are independent of the spine and of each other — side lanes.

---

## 2. Phase K — rukie core (`/home/alpha/rewrite/git-renewed/rukie`, Go, on Gitea history)

**Goal:** Demolition pad plus the new core — Ruk client, SQLite store, op table + four surfaces, auth/access, git wire, events/queue/outbox, MCP+CLI, dev loop, ops — as `github.com/gosuda/rukie`, every commit building.

| # | Concern | What lands | Gate |
|---|---|---|---|
| K1 | Demolition pad | Delete the 6,366-file demolition set (all tracked files except `LICENSE`, `README.md`, `AGENTS.md`, `.gitignore`, `.gitattributes`, each rewritten); fresh `go.mod` (`module github.com/gosuda/rukie`, go 1.27/toolchain go1.27.2) + minimal `main.go` so the tree builds [inference on placeholder shape; set authoritative in `rukie-demolition-manifest.md`] | G1–G5 on minimal tree + Ruk gates; file-count check vs manifest (CEO S-D6) |
| K2 | Ruk client | Byte-copy port of `ruk_ipc.go` + `ruk_capability.go` (provenance recorded, CEO-11) into `internal/rukclient` + drift `diff` gates; `internal/rukproto` golden (`TestModelGolden`); `tools/rukgen` + vendored `schema.json`; fake-daemon tests | `TestRukClientFramingRoundTrip`; integration `TestDaemonLifecycle`, `TestDaemonRejectsRepoOutsideRoots`, `TestCommandOpenCommitBypassesCoordinator` |
| K3 | Store | `internal/store` on modernc.org/sqlite v1.60.1: single-writer group commit (≤2 ms/256 tx), `Collection`/`Index`, `Blobs`, visibility contract; one engine, no seam | writer-race + behavior tests (store-engine §10); §12 VERIFY list in-PR |
| K4 | Op table + app | `internal/op` (tags, Query/Command, `Key` idempotency, HTML nonce); Huma v2.39.1 on ServeMux; RFC 9457, cursors, ETag/If-Match, `CrossOriginProtection`; OpenAPI 3.1; `internal/web` skeleton with the `url-information-architecture.md` route map; `cmd/rukie serve` + TOML config + `--check-config` + `/readyz`; DX-18 parity gate | G1–G5; OpenAPI golden; parity red-on-drift (DX-01/02/12/18) |
| K5 | Auth + access | argon2id (m=19456,t=2,p=1); server-side sessions + `__Host-` cookies; scoped `rk_` tokens (hash-at-rest); `auth.Actor`; `authz.Check` per decision table; admin CLI `user create`/`reset-password` | auth contracts + `create`; permission INV property suite |
| K6 | Git wire | `internal/githttp`: `info/refs` + upload/receive-pack POST via the R3 git-service envelope (`CommandOpen` stateless RPC), gzip, `MaxBytesReader`, `request_id` digest replay, per-token/per-IP buckets | e2e `push` + `TestConcurrentPushesSerialize` |
| K7 | Events + queue + outbox | Op-log follower (`events.tail`), deterministic job ids via `PutIfAbsent`; `internal/queue` (webhook/mirror/land/ci-plan); token-authed shim callback; spool drain; lossy tip-comparison reconciler v1 | follower replay-dedupe; spool/reconciler tests |
| K8 | MCP + CLI | `derive_mcp` (go-sdk v1.8.0, ≤36 tools, annotations, scope filtering); generated `rukie <op> --json` (exit-code table, `--in/-`, `--dry-run/--idempotency-key`) | MCP conformance; CLI JSON goldens (DX-03/04/05) |
| K9 | Dev loop + agent docs | `rukie dev` (ephemeral daemon, seed, printed credentials); `/llms.txt`, `/llms-full.txt`; zero-JS `/docs` from the Huma spec | dev-loop CI timing; llms freshness (DX-08/10/11) |
| K10 | Ops | Supervise `ruk serve` (probe binary spelling; external-socket flag); systemd; graceful drain 40 s; backup/restore (never `snapshot`); data-dir layout | startup/shutdown tests; `--check-config` fail-fast |

**Dependencies:** K1→K2→{K3,K4}→K5→K6; K6 also needs R3 (envelope) on top of R1/R2; K7 needs R5a+R5b+K6; K8 needs K4; K9 needs K8+R2; K10 needs K4+R2.
**Parallelism:** K2 ∥ K3 (K2 targets the current daemon); K8 ∥ K5/K6; K10 ∥ F; D1/D2 anytime after K1.

---

## 3. Phase F — features (slices S0–S11; each commit ships agent write path **and** human HTML view, CEO-24)

| # | Concern (slice) | What lands | Gate |
|---|---|---|---|
| F1 | Repos + browsing (S0) | `repo.create/get/delete` on four surfaces; revision resolve + refs cache; file/tree/log pages; entries cache; ETag/304 | hosting/browsing matrix rows: §5 unit, render + html-validate, page/API budgets |
| F2 | Review + land (S1) | Change/stack ops + pages; branch view read-only projection; comments `(change-id, head, path, line)` with remap; `review/approve`, `model/review` attest; LandIntent machine (Awaiting→Executing→Landed/Failed/Repreparing, ≤3 re-prepares); land worker keyed `(repo, target)` | e2e `change`, `land`; 409 `requirements-unmet` problem |
| F3 | Push mirrors (S2) | Mirror ops (`mode exact\|ff_only`, `refs all\|default+tags\|globs`, denylist), trigger + debounce, reconcile ticker + jitter, per-host buckets, status pages, Codeberg gate | e2e `mirror` 4 scenarios (exact+prune, ff-refusal, globs, reconcile-restore) |
| F4 | Orgs/teams/scopes (S3) | Orgs (`owner`/`member`), teams, repo grants, op/repo token selectors | token limited to one repo + op set; worked resolutions |
| F5 | CI control plane (S4) | `.rukie/ci.toml`; triggers keyed `(tree, pipeline digest, image digest)`; durable queue + fences + epoch; CAS result cache; `ci.*` ops; sshsig attestation + outbox relay; **matrix expansion at trigger time, control plane only** (provenance below) | C-D3 (a)–(d); boot recovery A/B (`E_RECOVERING` pre-load); `ci_attest`; matrix-expansion golden (N jobs from one ci.toml axis set) |
| F6 | Runner + sandbox (S4) | `rukie runner` (probe/register/lease/run/status/rotate, hidden `job-exec`); podman sandbox; log chunk streaming; artifacts; cache volumes; crash reaper; **no matrix logic in the runner** (`ci-runner-sandbox-design.md:399`) | protocol goldens; P1–P13; crash matrix; kill-after-200 restart |
| F7 | Tasks + claims (S5) | Task ops with acceptance contract; claims via `task:<id>` subjects; renew; supersede radar; ask object | two agents cannot claim one task (lease contention); `create` |
| F8 | Events surfaces (S6) | Signed Standard Webhooks (`webhook-id/timestamp/signature`, HMAC-SHA256), retry/dead-letter, SSRF dialer guard; inbox sources S1–S11 + pages | e2e `webhooks` (signature, 500→retry same id); inbox tests; schemas frozen per type |
| F9 | Search (S7) | `search` op + page over R14 | search p95 ≤ 500 ms |
| F10 | SSH (S8) | SSH entry per CEO-20 decision (in-process Go SSH vs OpenSSH ForceCommand — decide by S8); key→user; git-shell over daemon streams | stock git over SSH e2e |
| F11 | Releases (S9) | Release 1:1 with Ruk tag; freeze enforcement; notes; archive download over R15 | tarball download e2e; R12 race gate |
| F12 | TOTP + OIDC (S10) | TOTP (RFC 6238, ±1, recovery codes); OIDC code+PKCE S256 | login flows; stub-provider OIDC unit |
| F13 | Social + explore (S11) | Forks (R11b), fork network + visibility cap, stars, watches, explore filtering | fork + star e2e; explore permission tests |
| F14 | Admin surface | Minimal operator pages; admin CLI as scoped ops (`token mint/revoke`, `repo verify`, `mirror run`, `attest verify`, `inbox send-test`) | scope-gated admin op tests (DX-13/14) |

**CI scope provenance (explicit):** v1 CI is single-host, one rootless-podman executor, logs+artifacts, bounded by C-D3's four properties (CEO-14); no runner fleet, no marketplace, no Actions-YAML compatibility (CEO R-7). `arch-sketch-monolith.md` §11 proposed excluding matrix "pending user sign-off" — that same exclusion list also dropped artifacts and CAS cache, both of which the settled decision list (2026-10-20 user answers, this campaign's binding input) restores; the settled list likewise includes "matrix supported via ci.toml matrix expansion," and is therefore the later signed override of the sketch's hedge. Matrix is **in scope, control-plane only**: trigger-time ci.toml axis expansion producing N ordinary jobs on the same single-host queue; the runner stays matrix-free (`ci-runner-sandbox-design.md:399` — expansion "belongs to the front end"); run pages display the jobs matrix (`url-information-architecture.md:189,402`). **D2 lands a ledger line recording this override** so the decision is repo-visible.
**Dependencies:** F1←R5b(reads)+R6+U1/U2; F2←R7+R8; F3←R13a/b+K7; F4←F1; F5←R9+K7(+R6); F6←F5; F7←R10; F8←K7+F1; F9←R14; F10←R7; F11←R15+R12; F12←K5; F13←R11b. **Dogfood starts after F3+V5** (CEO-16/17); GitHub mirror mandatory for self-hosted ruk/rukie from then on.
**Parallelism:** F4 ∥ F5-prep; F9 ∥ F10 ∥ F11 after their R items; F12/F13/F14 leaf-parallel after dogfood; U4 families ride each F commit.

---

## 4. Phase U — UI (interleaved stream; CEO S-D4 dissolves a serial UI phase — label kept for the design-system lane)

**Goal:** Zero-JS Instrument/"operations ledger" (DES-01) system: tokens, shell, safety helpers, family views riding F commits, AA close.

| # | Concern | What lands | Gate |
|---|---|---|---|
| U0 | Spikes (no routes) | templ vs html/template decision; lexer throughput calibration; exact `.tmpl` count; Ruk fixture recording | decision + calibration numbers committed (rendering C0) |
| U1 | Safe-render foundations | `htmlcheck`, `trusted` (AST confinement), `secheaders` (exact CSP, new router only), `markdown/` (goldmark v1.8.6, no unsafe/attribute) | self-tests; confinement test; exact-CSP test (rendering C1) |
| U2 | Tokens + shell | `assets`/`asset`, `app.css` (custom properties, both schemes), `shell`, `/static/*`; no build tool | `TestRenderedPagesNoScript`, `TestRenderedPagesSecurityHeaders` (rendering C2) |
| U3 | Interaction conventions | URL taxonomy per `url-information-architecture.md` (with K4 routes); `?after=` + `Link: rel=next`; per-line `<details>` comment forms; `?hl=N-M` + `#L`; meta-refresh ≥15 s; one URL/three renderings (`Vary: Accept`) | nojs-pattern tests; negotiation matrix test (op-table §"Negotiation matrix") |
| U4 | Family views (ride F) | One family per F commit: blob/tree → README → log → diff unified/split → blame → changes/branches → tasks → orgs/teams → explore → admin → status | html-validate on dumps; per-page budget |
| U5 | Zero-JS closure | `TestAllHTMLRoutesZeroJS` on every route; registry test: every op has a page test | rendering final gate (legacy already deleted at K1) |
| U6 | AA pass | WCAG 2.2 AA sweep: contrast 4.5:1/3:1, focus-visible, keyboard walk, ≥24 px targets, 400 % reflow, reduced-motion, forced-colors | DES-21 checklist — the V-phase launch gate executed here |

**Dependencies:** U1/U2 before F1's first HTML; U3 with K4/F1; U5 after F14; U6 after U5. **Parallelism:** U0/U1/U2 ∥ K2/K3; U6 ∥ D.

---

## 5. Phase V — verification (gate-owner stream; CEO S-D5 dissolved the serial phase — label kept)

**Goal:** Own passgate discipline, the permanent growing e2e, perf/capacity budgets, security review, mutation testing.

| # | Concern | What lands | Gate |
|---|---|---|---|
| V1 | Gate plumbing | `passgate`; CI: G1–G5 both-repos rule, tagged suites (`integration`/`e2e`/`perf`/`render`), Ruk commit pin (CEO-07) | CI red-green on injected failure |
| V2 | S0 e2e gate | `TestAgentFlow/create` + `/push`; permanent (CEO-06) | passgate both |
| V3 | Render validation | `TestDumpRenderedPages` + html-validate 11.16.4 (Node test-only) | dump count ≥ op count; exit 0 |
| V4 | Perf harness | `TestPerfBudget` (release, no `-race`, ≥1000 samples): HTML ≤200 ms, JSON ≤100 ms, info/refs ≤150 ms, search ≤500 ms p95; `RESULTS.md` | pass at S0 calibration |
| V5 | Capacity gate | Daemon-path fetch/lookup/clone/adv budgets + load harness (with R20) — **blocks dogfood** | budgets met; table appended (CEO-08) |
| V6 | E2e growth | Subtests land with their slices: `change` (F2), `ci_attest` (F5), `land` (F2), `mirror` (F3), `webhooks` (F8) | full `TestAgentFlow` 7/7 |
| V7 | Security review | Token scopes, CI sandbox, mirror creds, webhook signing (threat-model) — **blocks external users** | report + fixes landed (CEO-25) |
| V8 | Hardening + mutation | Release-binary perf re-run; gremlins PR + weekly (land/mirror/auth/webhook); govulncheck blocking | §7 green on release; efficacy ≥80 %, survivors killed/documented |

**Dependencies:** V1 with K1; V2 with K6+F1; V3 with U2; V4 after F1; V5 with R20+F3; V7 after F8. **Parallelism:** V8 weekly from F2.

---

## 6. Phase D — docs / publish readiness

**Goal:** Publications true before anyone outside the campaign looks at either repo.

| # | Concern | What lands | Gate |
|---|---|---|---|
| D1 | Agent rules | `rukie/AGENTS.md` (36 rules) + `ruk/AGENTS.md` (26); canonical-checkout note (CEO-01) | rule counts + advisory checklist |
| D2 | Plan hygiene + scope ledger | New ledger `/home/alpha/rewrite/git-renewed/tasks/autoplan-ledger.jsonl`; mark `AGENT_NATIVE_GIT_PLAN.md:92-96` + old `ruk/tasks` entries superseded (CEO-02); **record the CI-scope override line**: settled answers supersede arch §11's hedge — artifacts, CAS cache, and control-plane ci.toml matrix expansion are in; runner stays matrix-free | ledger populated incl. override line; no contradictory live plans |
| D3 | README + docs IA | Replacement README (fixed order); docs IA; `examples/local.toml` + env file shipped **with the service implementation** | every README example executes; doccheck scenarios runnable |
| D4 | Generated reference | `docs/reference/operations` from the op table; `/docs`, `/llms.txt` freshness | drift gate: reference ≡ op table |
| D5 | Changelog + policy | `CHANGELOG.md` (implemented only); versioning/deprecation (`/v0`, `Sunset`, dual-run window); licence provenance (CEO-11, DX-17) | changelog audit; provenance complete |
| D6 | Publish gates | Isolated install run; examples captured; links resolved; no numeric placeholders; GitHub metadata only after user approval | docs-readme pre-publication checklist green |

**Dependencies:** D1/D2 after K1; D3 needs S0 real; D6 last. **Parallelism:** D1/D2/D5 writing ∥ K/F.

---

## 7. Cross-phase dependency graph

```
R1 → R2 → R3 → R5a → R5b → R6 → R7 → R8 → R9 ─→(F5)      R-side spine
      │                     └→(K7, needs R5a+R5b)
      │         └→ R13a → R13b ─→(F3)  R10→(F7)  R14→(F9)  R15+R12→(F11)  R11b→(F13)
K1 → K2 → K4 → K5 → K6(needs R1+R2+R3) → F1 → F2 → F3 + V5 → DOGFOOD → F4→F5→F6→F7→F8→F9…F14
      └→ K3 ↗                └→ K7(needs R5a+R5b)         U1→U2→(F-family views)→U5→U6
V1→V2→V3→V4 ──────────────────────────────→ V5 → V6 → V7 → V8
D1/D2 ∥ → D3(S0) → D4 → D5 → D6
R19 gated on S0-e2e-green; R20 ∥ feeds V5
```

## 8. (b) Critical path

**R1 → R2 → R3 → R5a → R5b → R6 → R7 → R8 → F2(land)**, crossed with **K1 → K2 → K4 → K5 → K6 → F1 → F2 → F3 + V5 → dogfood → F5 (needs R9) → V7 → D6**.

Binding edges: K6 blocks on R1+R2+R3 (stateless RPC, rename, git-service envelope); F1 blocks on R5b's read subset + R6 + U1/U2; F2 blocks on R7 (receive path + `refs/for`) and R8 (hosted land); dogfood blocks on F3+V5. The R3→R7→R8 chain is the longest, riskiest R-side sequence — staff it first and keep it continuously busy; the K-side chain to F1 is shorter and absorbs R-side slippage only up to the K6/F1 joins.

## 9. (c) Smallest end-to-end slice (S-D1/S-D2, verified at `autoplan-ceo.md:96` and `:310`)

Exact commits: **R1, R2, R3 (streaming core — K6's upload/receive-pack runs over the R3 git-service envelope, so it precedes all wire work), R5a, R5b (R5a precedes R5b because `events.tail` reads the R5a tables; the demo *uses* only R5b's `refs.list` + HEAD symref + `tree` subset — `events.tail`/`repo.stat` ride R5b and are first exercised by K7/F5), R6 (file read arm), R11a (repo.init/delete minimal), K1, K2, K3, K4, K5, K6, F1 (subset: repo.create/get + file/tree/branch-list pages), U1, U2, V1, V2.**


[Showing lines 2130-2486 of 2543 (49.5KB limit). Use :2487 to continue]

Demo enabled — S-D1 (a)–(g), verbatim register: (a) `rukie admin user create` makes a user with an argon2id password; (b) the human logs in through HTML and creates a scoped token; (c) `repo.create` succeeds through all four surfaces — JSON HTTP (curl against the OpenAPI route), MCP, `rukie repo.create --json`, HTML form; (d) stock `git push` and `git clone` work over Smart HTTP at `/{owner}/{repo}.git` with token Basic auth; (e) the HTML repo page shows the default branch's tree, a file, and a bounded branch list; (f) SQLite holds users, tokens and repos; (g) one `ruk serve` daemon is reached through the generated Go client. These stay green in CI from then on (CEO-06); green S0 releases R19.

## 10. (d) Risk ordering — R items K/F blocks on

1. **Hosted read methods** (R5b/R6; arch R2; CI-control R4 "blocks change triggers") — blocks F1 browsing, CI triggers/reconcile/base resolution, review branch-view grouping.
2. **Receive-pack streaming** (R3+R7; ssh R-7.1: until it lands, the 256 MiB stdin cap contradicts the 2 GiB ingest bound — "large pushes cannot arrive via daemon") — blocks K6 real pushes, F2, F10 SSH, runner fetch, mirrors. Hardest prerequisite; schedule first.
3. **Events** (R5a foundation + R5b `events.tail` + R-REFJOURNAL in R7; arch R7) — blocks K7 outbox, webhook/mirror/CI triggers, inbox. `op.log` never covers git pushes (git-wire applies bypass the coordinator); the journal is the attributable source; reconciler v1 is deliberately lossy tip-comparison until it lands.
4. **Attest predicate body** (R9 evidence v2 + namespace grants; CI-control R1/R2 "blocks D3") — blocks F5 attestations and the `ci/*` land-gate requirements (with R8); the namespace grant keeps policy edits off the per-spec-change path.
5. **Claims key** (R10 `task:<id>` subjects + `claim.renew`; arch R13) — blocks F7 tasks/leases and task-claim protection.
6. Next tier: **R8 hosted landing** (F2-land), **R13a/b** (F3 → dogfood), **R11a** (S0 itself), **R11b+R12** (S11/S9 only).

## 11. (e) Pairing rule — rukie ↔ ruk upgrades

- **Contract-first pin (CEO-07):** `ruk model --format json` → `ruk-model.golden.json` (byte-stable, sonic-rs `sort_keys`) + IPC schema are the single source; `tools/rukgen` generates Go types; rukie CI pins an **exact Ruk commit**. Any R commit touching the method table/IPC pairs with a rukie commit (pin bump + golden regen + `rukgen` + drift gates); both repos green before the pair counts as landed.
- **Schema bumps** (`ruk-serve-design.md` §7): Method row append → `SCHEMA_MINOR`; optional field on read/new method → minor; optional field on an **existing mutation** → minor **plus** `REQUEST_DIGEST_VERSION` bump (or move secrets to the open frame); required-field change/rename/semantics → major; durable row structs (Receipts/Session/Capabilities/PushPlans/OpHeader/OpDelta) → row-version bump, not schema minor; new `FactTable` → store-format review; new `ErrorCode` avoided (closed set). **Additive methods batch into one bump commit per batch** — minor 1→2 is R5b alone (R5a is schema-free). IPC frame-kind/feature/profile changes ship only with a schema bump — "only rukie and ruk talk IPC, and they ship together."
- **Daemon rename cutover (R2/R19):** add `ruk serve` + profile `"rukie"` while accepting `"forgejo"` one dev cycle behind a compat flag; the rukie supervisor **probes the binary** (`ruk version`/`--help`) and uses whichever spelling the paired binary advertises — "a paired-binary upgrade, not a flag day" (ops-design §0). Compat alias and `ruk/forgejo` prototype are removed only after rukie S0 e2e is green (CEO-10, R-4), in R19.
- **No compatibility migrations before v0.1** (CEO-04/V-2): dev data may reset; one forward-only chain. Ruk on-disk format freezes at dogfood behind an explicit store-version bump (CEO-22/V-3); the GitHub mirror (S2) is the exit path until then. Op table stays `/v0`; webhook schemas freeze per type at F8/S6 (CEO-21).

## 12. Green definition — **both repositories after every commit** (assignment Target: "each commit keeping both repos green")

- **Ruk gates** (run after every commit, either repo): `cargo fmt --all -- --check`; `cargo clippy --workspace --all-targets --all-features --locked -- -D warnings`; `cargo nextest run --workspace --locked`; `cargo test --workspace --locked` (nextest 0.9.148; toolchain 1.99.1). Oracle tests use external `git` test-only; semantic ref-set/object-set comparison, never byte-exact.
- **rukie gates** (run after every commit, either repo): G1a `go vet ./...`; G1b `go vet -tags 'integration e2e perf render' ./...`; G2 golangci-lint v2.15.0; G3 `go test -race -count=1 -v ./...` with ≥1 `--- PASS:` via passgate; G4 govulncheck v1.9.0 blocking; G5 gofmt-clean + `go mod tidy -diff` + `go mod verify` (after `git add`). Tagged: `integration` (real daemon, `RUK_BIN`), `e2e`, `perf` (release binary, **no** `-race`), `render` (`RUKIE_RENDER_DIR`). Skips and zero-match runs fail.
- **Why both, always:** rukie's tagged suites execute a Ruk binary, so an R commit can break rukie's gates (the mechanism behind CEO-07's pin); Cargo gates after a rukie-only commit are cheap and confirm the pinned pair still holds. A commit is green only when **both** gate sets pass against the commit's paired pin.
- **Discipline:** a paired change (R commit + rukie pin bump) lands as one atomic pair; flakes follow rerun-`-count=3`-record-fix-or-quarantine; no unexplained `//nolint`, no `t.Skip` without an issue link.

## 13. Crosswalks — canonical arch-sketch orders ↔ this plan

**C1–C14 (`arch-sketch-monolith.md` §13) → plan IDs** — every canonical commit accounted for:

| Arch | Content (abridged) | Plan IDs | Note |
|---|---|---|---|
| C1 | `internal/ruk` client, rukgen, fake-daemon tests | K2 | |
| C2 | `internal/store` | K3 | SQLite settled (spine verdict) |
| C3 | `internal/op`, `web` skeleton, `app`, new `cmd/rukie` | K4 | Old `main.go` does not survive to a late cutover: the settled demolition front-loads its deletion into K1 |
| C4 | `auth` and `access` | K5 | |
| C5 | `repo` browsing (arch-needs R2, R3) | F1 + U4 (blob/tree/log families) | fed by R5b reads + R6 |
| C6 | `gitwire` HTTP (arch-needs R1, R6) | K6 | arch-R1 = plan R2 (rename); arch-R6 refs/for = plan R7; also needs plan R1 (stateless) + R3 (envelope); fetch works after R1+R2+R3, refs/for changes after R7 |
| C7 | SSH (arch-needs R5) | F10 | arch-R5 full-duplex = plan R3+R7 |
| C8 | `events`, `queue`, `webhook` (arch-needs R7) | K7 (events/queue/outbox) + F8 (webhook+inbox surfaces) | arch-R7 op.log = plan R5a+R5b |
| C9 | `review` and landing (arch-needs R8) | F2 | arch-R8 = plan R8 |
| C10 | `ci` | F5 + F6 | |
| C11 | `mirror` (arch-needs R9) | F3 | arch-R9 = plan R13a+R13b |
| C12 | `task` (arch-R13), `inbox`, releases, search (arch-R12), explore | F7, F8 (inbox), F11 (releases), F9 (search), F13 (explore/social) | arch-R13 claims = plan R10; arch-R12 search = plan R14; releases also need plan R15+R12 |
| C13 | `mcp` and `cli` | K8 | |
| C14 | Cutover: delete Gitea trees, module rename to `github.com/gosuda/rukie`, `go mod tidy` | K1 (front-loaded pad) + R19 (Ruk-side residue) | The settled demolition set deletes all 6,366 files (incl. `routers/`, `models/`, `web_src/`, old `cmd/*`, `main.go`) and lands the module rename in the pad commit, so C14's substance is absorbed into K1; "every commit builds" is preserved by the minimal `main.go` placeholder |

**Arch R1–R14 (§12) → plan R-IDs:** arch-R1→R2; arch-R2→R5b(ref.list/ref.get)+R6; arch-R3→R3; arch-R4→R3; arch-R5→R3+R7; arch-R6→R7; arch-R7→R5a+R5b (+R-REFJOURNAL in R7); arch-R8→R8; arch-R9→R13a+R13b; arch-R10→R16; arch-R11→R11a+R11b; arch-R12→R14; arch-R13→R10; arch-R14→R18. Plan-only items beyond the arch list (sourced elsewhere): R1 stateless RPC (verification-strategy §3), R4 ReceivePolicy (ruk-serve slice 4), R12 ReleasedTags (fork §8.3.2), R15 archive streaming (ruk-serve slice 8), R17 quotas (ssh R-QUOTA), R19 cutover (CEO-10), R20 perf lane (CEO-08). Coverage: C1–C14 fully mapped; no canonical commit is dropped or reordered below its stated prerequisite.

---

### Critical Files for Implementation

- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk/src/daemon/mod.rs` — argv-path removal, git-service envelope, HostPolicy frames, outcome ledger, digest/limits: spine of R3/R7/R17 and the pairing surface for `internal/rukclient`.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs` — method table, `SCHEMA_MINOR`, `required` array, arg/reply types: the R5b single bump and every R6/R8–R15 commit.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/hosted.rs` — hosted read arms (R6), read-gap cleanup, lease/cursor minting shared by search and paging.
- `/home/alpha/rewrite/git-renewed/ruk/crates/ruk-git/src/transport/serve.rs` — receive-pack halves, hook phases, per-ref reports: R4/R7, R-REFJOURNAL, fail-closed proc-receive semantics.
- `/home/alpha/rewrite/git-renewed/rukie/go.mod` — demolition-pad target, `github.com/gosuda/rukie` rename, the whole dependency cut/add surface (Huma, go-sdk, modernc sqlite) for K1–K4.