# ideation-adjudication.md

I audited the critique of the rukie review, task, and context ideation against the source code. **The final set is 19 survivors and 38 rejections, covering all 57 candidates.** Two of the critique's verdicts rest on wrong facts, and I reversed both:

- **#34 "Privileged fenced expiry transition" is now rejected.** The sweeper it proposes already exists in Ruk.
- **#49 "Question as structured ask" now survives, in narrowed form.** The critique wrongly said the protocol shape it cites is not MCP.

Two advisors pushed back on these reversals. I weighed both pushbacks and document my reasoning in §1.3.

The biggest open problem is that survivor #37 ("Intent": a task is the same object as a change) conflicts with the settled decision that tasks are their own feature. That conflict decides how several other survivors are framed.

I could not write the file in this read-only session. The orchestrator should save this message verbatim as `local/ideation-adjudication.md`.

---

## 1. Audit of `ideation-critique.md`

### 1.1 Completeness: passes
- The verdict table has 57 rows, one per candidate, split 7+7+7+7+8+7+7+7 across the eight generators in pool order. Each row has exactly one verdict and a reason.
- I checked the candidate names against the final draft from each generator in `ideation-raw-pool.md`:
  - ideate-scale: its last correction block (pool ≈ lines 520–545) gives 8 candidates, matching rows #29–#36.
  - ideate-agent-dx: its final draft gives 7, matching #44–#50.
  - ideate-trust: its final draft gives 7, matching #51–#57.
- The critique file also contains advisor and harness noise and a "Later corrections" tail. The tail adds no verdicts, so I ignored it.

### 1.2 Consistency: passes, with the overlaps below explained
- No two survivors say the same thing. The closest pairs split cleanly:
  - **#8, #53 and #9 work at different layers of the same requirement evaluation.** #8 is the contract: which subjects and which requirements apply. #53 is freshness: the `valid_until` wrapper. #9 is the threshold: a quorum over trusted signers.
  - **#14 and #41 are different things.** #14 lists, for each subject, why its requirements are unmet (the `Gap` reasons). #41 is a saved revset that selects commits.
  - **#15, #19 and #50 are different things.** #15 is what one change's brief contains. #19 is how much any read returns. #50 is how work is handed over by IDs.
- There is one internal dependency. #40 ("Proof bundle") selects by exact commit OID, so conflicted heads never match it (`ruk/crates/ruk-core/src/revset/eval.rs:130-135`). #31 ("Conflicts as reviewable objects") is exactly those conflicted heads. Conflict review therefore needs its own evidence key, such as the change ID or the conflict-record hash. This is a design note, not a contradiction.

### 1.3 Accuracy: 17 cited bases spot-checked, 2 factual errors

| Survivor / row | Cited basis | Checked in | Result |
|---|---|---|---|
| #29 Path Leases | Enforce returns `E_CLAIMED`; Advise only warns | `coord/claims.rs:340-365` (`expires_at > now && holder != holder`, then Enforce → `EClaimed`, else `Warning::ClaimAdvisory`) | ✔ |
| #30 Exact Land Plans | One primary candidate, exact base, plan consumed on execute, 1 h lifetime | `coord/land.rs:11-22` (exactness contract), `:125-130` (`candidate()` = first member), `land_execute` docs | ✔ |
| #35 Change-keyed claims | `ClaimRecord` has no change ID | `protocol.rs:1720-1727` (fields: `claim, holder, paths, mode, expires_at`) | ✔ |
| #16 Why-Attached Blame | `BlameHunk` carries head, change and author | `protocol.rs:2463-2470` (`head`, `change: Option<ChangeId>`, `author`) | ✔ |
| #16 | Grammar for up to 32 provenance claims | `commit.rs:75-104` (`MAX_PROVENANCE_CLAIMS = 32`) | ✔ |
| #53 Validity-bounded wrapper | The protocol requirement has only kind and result; the bound lives in a wrapper | `protocol.rs:2037-2041` (`AttestRequirement {kind,result}`); `attestation/query.rs:84-100` (`BoundedRequirement`, "folds into it" when the protocol gains the field) | ✔ |
| #8 Contract | The revset only discovers subjects; `evaluate_requirements` is what authorizes | `attestation/query.rs:266-276` ("exposes ambiguity and is not land authorization"); `revset/eval.rs:139-158` | ✔ |
| #14 Gap work-list | `Gap` enum | `attestation/query.rs:59-80` (`NoLiveMatch`, `Ambiguous`, `Expired`) | ✔ |
| #9 Quorum | Ambiguity is scoped to one `(subject, kind, signer)` group | `attestation.rs:20-26` | ✔ |
| #40 Proof bundle | The subject is the exact commit OID | `revset/eval.rs:130-135` | ✔ (conflicted heads never match; see §1.2) |
| #31 Conflicts | `conflict-v1` tag on a framed record | `conflict.rs:60` (`FORMAT_TAG`) | ✔ |
| #42 Facet | Precedent for exclusive labels | `rukie/models/issues/label.go:84-91` (`Exclusive`, `ExclusiveOrder`), `:183-193` (`ExclusiveScope`) | ✔ |
| #22 rejection | `review.go:626` is only a stale flag | `rukie/models/issues/review.go:628-640` (`MarkReviewsAsStale` / `NotStale` by SHA) | ✔ rejection holds |
| #6 rejection | Undo refuses some methods and workspace rows | `coord/undo.rs:11-21` | ✔ rejection holds |
| #19 Budgeted Views | `ByteBudget` | `budget.rs:6-46` (reserve/limit, `E_LIMIT`) | ✔ |
| #15 Change Brief | `describe` reports every head of a divergent change | `ops/describe.rs:3-6` | ✔ |
| #55 in-toto | `ruk-attest` namespace check | `attestation/verify.rs:75-95` | ✔, with a feasibility caveat (§2) |
| #34 Privileged expiry | "Holder-only release plus lease lifetimes forces a NEW lane-privileged sweeper" | see below | **✘ factual error** |
| #49 Question as ask | "Cited `input_required`/`inputResponses` shape matches A2A, not MCP elicitation" | see below | **✘ factual error** |
| #21 rejection | Serving HTML, JSON and Markdown from the same URL is already settled | `agents-md-draft.md:19`; `autoplan-dx.md` DX-07 | ✔ rejection holds |
| #47 rejection | One `Op[In,Out]` table is already settled | `inbox-design.md:10` | ✔ rejection holds |

**Error 1 (#34): reject.** The critique kept #34 because holder-only release (`claims.rs:207-208`) seemed to force a new privileged sweeper. Four facts contradict that:
- **Expired claims already stop blocking.** Enforcement only applies while `expires_at > now` (`claims.rs:350`).
- **Leftover index keys of an expired claim cannot collide with a new claim.** Every acquisition gets a fresh claim ID (`ClaimId::generate`, `claims.rs:883`), and every index key ends in that ID (`row_keys`, `claims.rs:595-608`). The exact-key check at `:169-177` therefore never trips on another claim's leftovers.
- **A non-holder sweeper already exists.** `claims::sweep_page` (`claims.rs:475-516`) deletes records and index keys of expired claims under lane ownership, CAS-fenced. It is tested by `claims/tests.rs:424`, `stale_expired_cleanup_cannot_remove_a_replaced_record`.
- **The critique cited the wrong file for lifetimes.** `leases/timing.rs` covers view, cursor and bulk lease lifetimes, not claim TTLs.

Two advisors argued that leftover keys block re-acquisition. That would only hold if claim IDs were reused, and they are not. The real leftover issue is smaller. `sweep_page` seems to have no caller in the hosted coordinator: `hosted.rs:53,1254` sweeps only receipts and sessions. That is an inference from a grep that found no other caller. Wiring it in is an implementation task for Ruk, not an abstraction decision. This is the same rule the critique used to reject #32 and #33 ("describes existing Ruk behavior").

**Error 2 (#49): survive, narrowed.** MCP 2026-07-28 does define `InputRequiredResult`, `inputResponses` and `requestState` as "Multi Round-Trip Requests" (https://modelcontextprotocol.io/specification/2026-07-28/schema, https://modelcontextprotocol.io/specification/draft/basic/patterns/mrtr). The A2A attribution is false.

The re-check against the critique's four criteria:
1. **Basis:** it supports a typed, resumable question delivered to the calling client.
2. **Feasibility:** it is feasible.
3. **Duplication:** no survivor covers a human answering an agent's question. The inbox design only covers notifications (`inbox-design.md`, sources S1–S11).
4. **Decision value:** it adds an object to the task model.

The narrowing: MCP's round-trip answers the agent's own client. A supervising human answering later in the web UI is a new, durable rukie object, and MCP's mechanism only delivers the question in-band. The candidate's idea that an "answer lands as attestation" is dropped unless the question is about a commit, because attestations need an exact commit as subject (`protocol.rs:1776-1784`).

### 1.4 Scope check against settled decisions
The settled decisions (`rukie-ruk-native-forge-plan.md`, *Settled decisions*; `inbox-design.md:8-10`):
- Review covers Ruk changes and stacks plus a branch view.
- Tasks exist and can be claimed through Ruk leases.
- Agents are scoped tokens on human accounts.
- The UI uses zero JavaScript.
- There is one operation table.

All survivors fit except the conflicts listed in §4.

---

## 2. Final survivor set (19)

**Review (6)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 37 | Intent: task is the same object as a change *(conflicts with settled tasks, §4-A)* | One object, born with a `ChangeId` and an acceptance predicate; closing it means landing it | `ruk-core/src/change.rs:60-83`; `coord/oplog.rs:40-53` |
| 8 | Revset-named, requirements-gated contract | A revset finds the subjects; `evaluate_requirements` with an explicit trusted set authorizes the landing | `attestation/query.rs:266-290`; `revset/eval.rs:139-158` |
| 9 | Quorum over live verdicts | A new rukie threshold rule, e.g. "N trusted approvals and no live block", on top of Ruk's ambiguity scoped to one signer | `attestation.rs:20-26` |
| 30 | Exact Land Plans (serial, one commit each) | A stack lands as a sequence of single-commit plans, never as one plan | `coord/land.rs:11-22,125-130` |
| 31 | Conflicts as reviewable objects (scoped) | The framed `conflict-v1` record plus syntax-aware three-way merge become a claimable unit of review | `conflict.rs:60`; `merge/syntax/grammar.rs:205-277` |
| 40 | Proof bundle | CI results and human approvals are the same kind of `attest.add` evidence, keyed to the exact commit | `revset/eval.rs:130-135` (needs a non-OID key for conflicts, §1.2) |

**Tasks (5)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 29 | Path Leases | The standard mutual-exclusion primitive: Enforce/`E_CLAIMED` vs Advise/warning | `coord/claims.rs:340-365` |
| 35 | Change-keyed claims for supersession | Add a claim→change link so that superseding or abandoning a change marks dependent claims stale | `protocol.rs:1720-1727` (no change field) |
| 53 | Validity-bounded evaluation wrapper | `valid_until` on the caller side; `Gap::Expired` kills evidence that stays green forever | `attestation/query.rs:66-100` |
| 14 | Revset-named gap work-list | Per-subject `Gap` reasons as the agent's next action, served over MCP/API and as a zero-JS page | `attestation/query.rs:59-80` |
| 49 | Question as structured ask *(corrected, narrowed)* | A durable, typed question on a task or claim (options, deadline, whether it blocks the claim). MCP round-trips deliver it in-band; a human answers it through a zero-JS form | MCP 2026-07-28 schema (MRTR); `inbox-design.md` has no such object |

**Context sharing (5)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 16 | Why-Attached Blame | Each blame hunk carries its change ID, description and provenance claims | `protocol.rs:2463-2470`; `commit.rs:75-104` |
| 19 | Budgeted Views | Every read has an explicit server-enforced budget and either completes or returns a continuation | `budget.rs:6-46` |
| 15 | Change Brief | A small curated brief per change that links to the full evidence; divergent heads are shown | `ops/describe.rs:3-6`; llmstxt.org |
| 41 | View | A board is a named, versioned revset. Stored views are new Ruk work | `revset/syntax.rs:1-28`; `revset/eval.rs:26-40` |
| 50 | Handoff bundle by IDs | Hand over change ID, claim ID, log cursor and attestation IDs; the next agent resumes `session.resume`-style without re-reading | `/tmp/ruk-model.json` (`session.open/resume`), per the critique; I did not re-open it |

**Cross-cutting (3)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 42 | Facet | A closed, typed label schema checked on write and usable as revset predicates | `rukie/models/issues/label.go:84-91,183-193` |
| 48 | Scope-filtered toolsets | Tool lists filtered per request by token scope, in a fixed order | `ruk/crates/ruk/src/mcp/tools.rs` TOOL_TABLE; github-mcp-server toolsets |
| 55 | in-toto-typed CI claims | Built-in CI posts typed predicate kinds instead of plain pass/fail strings | `attestation/verify.rs:75-95`; in-toto spec. **Caveat:** `AttestStatement` has no predicate-body field (`protocol.rs:1776-1784`). `AttestKind` is free UTF-8 (`capability.rs:201`), so the predicate type fits as a kind, but the predicate body needs a Ruk schema change or a digest stored in `result` |

---

## 3. Rejections (38)

Row numbers refer to the critique table. "Dup" means the candidate restates a survivor.

| # | Candidate | Reason |
|---|---|---|
| 1 | Change-stack review | Dup of #37; also already settled as the review unit. **Gap:** no survivor covers comments anchored to (change ID, head) that move with rewrites |
| 2 | Claim-task | Dup of #29 |
| 3 | Attestation-gated done | Dup of #8 |
| 4 | Frozen land-plan approval | Dup of #30 |
| 5 | Scoped review workspace | Minting a workspace per review is not feasible across a fleet |
| 6 | Op-log timeline with exact undo | Overclaims that everything can be undone (`undo.rs:11-21`) |
| 7 | Conflict-record handoff | Dup of #31 |
| 10 | Enriched land gate | Dup of #30 |
| 11 | Bounded evidence + allowlist | Dup of #53 |
| 12 | Derived lifecycle automaton | Its states map to no Ruk type and it adds no mechanism |
| 13 | Lease-scoped task claim | Dup of #29 |
| 17 | Evidence-Dedup Index | Dup of #40 |
| 18 | Handoff Packet | Dup of #50 |
| 20 | Saved Lenses | Dup of #41 |
| 21 | Dual-Render Objects | Restates the settled one-URL content negotiation |
| 22 | Risk-Ranked Attention Queue | No cited file contains a risk formula |
| 23 | Batch Landing Parade | A lane group-commit is serialization, not batch approval |
| 24 | Evergreen Review | No primitive for content-derived staleness. Same comment-anchoring gap as #1 |
| 25 | Explain-This-Change Card | Dup of #15 |
| 26 | Claim-Board | Dup of #29 |
| 27 | Supervision Budget & Auto-Land Dial | No policy engine supports it |
| 28 | Supersede Radar | Dup of #35 |
| 32 | Group-commit lane | Describes existing Ruk behavior |
| 33 | Crash-resume via fencing + op log | Describes existing Ruk behavior |
| **34** | **Privileged fenced expiry transition** | **Reversed from survive.** Expiry already unblocks, claim IDs are fresh, and `claims::sweep_page` already exists (§1.3). What remains is wiring it into hosted maintenance, an implementation task for Ruk |
| 36 | Per-repo fairness + team budgets | Team budgets have nowhere to be enforced |
| 38 | Verdict-as-property | Dup of #9 |
| 39 | Lease-as-claim | Dup of #29 |
| 43 | Trail | Dup of #41 |
| 44 | Lease-claimed tasks | Dup of #29 |
| 45 | Attestation-defined done | Dup of #8 |
| 46 | Frozen-candidate review | Dup of #30 |
| 47 | One op table, three surfaces | Restates the settled single operation table |
| 51 | Exact-subject land gates | Dup of #30 |
| 52 | Same-signer ambiguity rule | Restates an existing Ruk primitive |
| 54 | Signed supersession only | Restates `prepare_batch` |
| 56 | SLSA-graded provenance | L3 isolation and key infrastructure are out of scope |
| 57 | Policy-as-code land policy | Dup of #8 |

Totals: 19 survive and 38 are rejected, 57 in all. Compared with the critique, #34 moved to rejected and #49 moved to survivors.

---

## 4. Contradictions with settled decisions and between survivors

- **A. #37 Intent vs "tasks exist as their own feature."** The settled decision includes tasks claimable through Ruk leases (`inbox-design.md:8`: "tasks replace issues"; the inbox subject kinds keep `task` separate from `change`). Intent makes task and change one object and makes closing mean landing. It cannot represent tasks that never produce a change (investigation, ops, a question), or tasks spanning several changes. Several other survivors are framed around Intent and need re-framing whichever way this goes: #42 (facets on Intent), #41 (boards over Intents), and #14 (gaps per Intent).
- **B. #29 and #35 vs "claimable tasks."** Ruk claims are keyed by **paths** and held by a **session** (`protocol.rs:1720-1727`). #35 adds a change key but no task key. A token on a human account claiming a task needs either a task key in `ClaimRecord` or a rukie-side mapping from task to paths. Neither survivor provides this.
- **C. #41 View vs non-change tasks.** Revsets select commits, so a task with no commit yet, or a conflicted change, is invisible to a View and to `attested()` (`revset/eval.rs:130-135`; the radical generator called this a known hole). A board of tasks needs predicates over rukie's own data, or Option A in §4-A.
- **D. #30 vs the branch view.** Serial single-commit plans cover stacks. "Land this branch" in the branch view must therefore mean "N serial plans" and never one batch, which also follows from #23 being rejected. The UI must not suggest otherwise.
- **E. #40 vs #31.** As noted in §1.2, conflict review needs an evidence key other than the commit OID.
- **F. Coverage gap, not a contradiction.** Inline review comments that survive rewrites of a change (the ideas behind rejected #1 and #24) have no surviving abstraction, yet the settled review unit needs them. The plan should take this up as a design item, not as a revived candidate.

---

## 5. Questions the user must answer

1. **What is a task?**
   - (a) A task is the same object as a change (#37 as written). Every task starts as a change, and landing closes it.
   - (b) A task is its own object linking zero or more changes. It carries the acceptance contract (#8) and closes when the contract is satisfied. #37 is reduced to "a change carries an acceptance predicate".
   - (c) Hybrid: a task is its own object, and it closes automatically when its single linked change lands with the contract satisfied.
2. **What does an agent claim?**
   - (a) Paths only (#29 as-is). The task shows who holds overlapping path claims.
   - (b) A task or change ID (#35 extended with a task key in Ruk `ClaimRecord`), with path claims optional.
   - (c) Both: claiming a task acquires its path claims in Enforce or Advise mode under one claim record.
3. **What powers boards and queues?**
   - (a) Revset Views only (#41). Anything without a commit is not on a board.
   - (b) Views extended with predicates over rukie's own data (task state, #42 facets, claims). Ruk revsets stay for commits.
   - (c) No saved views. Fixed server-rendered queues (needs review, my claims, gaps).
4. **When does a change count as reviewed?**
   - (a) A quorum per contract (#9) with freshness bounds (#53), so N trusted human approvals and no live block.
   - (b) One trusted approval by default, with quorum as an opt-in per repo or path.
   - (c) Facet-driven: low-risk facets (#42) can land on signed CI evidence alone (#55); others need a human quorum.

---

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/claims.rs`: claim enforcement, holder-only release, the existing `sweep_page` (decides #29, #35, the #34 reversal, and question 2)
- `ruk/crates/ruk-core/src/protocol.rs`: the `ClaimRecord` gap, `AttestStatement` without a predicate body, `BlameHunk` (decides #35, #55, #16)
- `ruk/crates/ruk-core/src/attestation/query.rs`: `Gap`, `BoundedRequirement`, `evaluate_requirements` (decides #8, #9, #14, #53)
- `ruk/crates/ruk-core/src/coord/land.rs`: serial single-commit land plans (decides #30 and the branch-view landing semantics)
- `ruk/crates/ruk-core/src/revset/eval.rs`: `attested()` keyed to the exact commit OID; conflicted and pre-commit work is invisible (decides #40, #41, question 3)

[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Fix survivor #16 wording: cur…

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> In §1.3 #34, change "leftover is…

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Fix §1.3 Error 1. "Expired claims …

[advisor] <advisory advisor="stickler" severity="blocker" guidance="weigh, don't blindly obey"> Correct #1 and #21 before handoff…

---
## Later corrections from the same agent (apply over the body above)

# ideation-adjudication.md

I audited the critique for completeness, consistency, accuracy and scope, and applied its verdicts except where a cited basis is wrong. **The final set is 20 survivors and 37 rejections, covering all 57 candidates.** Four rows rested on factual errors:

- **#1 "Change-stack review" now survives, in narrowed form.** The critique called it a duplicate of #37, but #37 does not anchor comments, and #1 does.
- **#34 "Privileged fenced expiry transition" is now rejected.** The sweeper it proposes already exists in Ruk. The real problem is that nothing calls it in production, and that is a bug in Ruk that blocks tasks (§1.3).
- **#49 "Question as structured ask" now survives, in narrowed form.** The protocol shape it cites is MCP 2026-07-28, not A2A.
- **#21 "Dual-Render Objects" stays rejected for a different reason.** Content negotiation is not a settled user decision. It is already proposed as DX-07 in `autoplan-dx.md`.

The biggest open problem is that survivor #37 ("Intent": a task is the same object as a change) conflicts with the settled decision that tasks are their own feature. That conflict decides how several other survivors are framed.

I could not write the file in this read-only session. The orchestrator should save this message verbatim as `local/ideation-adjudication.md`.

---

## 1. Audit of `ideation-critique.md`

### 1.1 Completeness: passes
- The verdict table has 57 rows, one per candidate, split 7+7+7+7+8+7+7+7 across the generators in pool order. Each row has exactly one verdict and a reason.
- The names match each generator's final draft in `ideation-raw-pool.md`:
  - ideate-scale: its last correction block gives 8 candidates, matching #29–#36.
  - ideate-agent-dx: its final draft matches #44–#50.
  - ideate-trust: its final draft matches #51–#57.
- The "Later corrections" tail of the critique adds no verdicts.

### 1.2 Consistency: passes, with these notes
- No two survivors say the same thing. The closest groups do different jobs:
  - #8 is the contract (which subjects and requirements apply), #53 is freshness (`valid_until`), and #9 is the threshold (a quorum of signers).
  - #14 lists, per subject, why its requirements are unmet (the `Gap` reasons). #41 is a saved revset that selects commits.
  - #15 is one change's brief, #19 is a budget on any read, and #50 is a handoff by IDs.
  - #1 decides where comments anchor. #30 decides how landing works.
- **#53 against the critique's own rule for existing behavior (rows #32, #33, #52, #54).** The `BoundedRequirement` wrapper does exist in Ruk (`attestation/query.rs:84-100`). Landing still evaluates without the bound, though: `coord/land.rs:253,387` call `evaluate_requirements`, not `evaluate_bounded_requirements`. Making land gates check freshness is a real decision for both Ruk and rukie, so the survivor stands.
- **#40 vs #31.** #40 selects evidence by exact commit OID, and conflicted heads never match (`revset/eval.rs:130-135`). Reviewing a conflict (#31) therefore needs a different evidence key, such as the change ID or the conflict-record hash.
- **#31 is limited in scope.** A conflict record can be a claimable piece inside a change or stack review. It must not become a fourth top-level review unit, because the settled units are changes, stacks and a branch view.

### 1.3 Accuracy: 18 cited bases checked, 4 rows corrected

| Row | Cited basis | Checked in | Result |
|---|---|---|---|
| #29 | Enforce returns `E_CLAIMED`; Advise only warns | `coord/claims.rs:340-365` | ✔ |
| #30 | Single primary candidate, exact base, plan consumed on execute | `coord/land.rs:11-22,125-130`. The primary is the **first member of the sorted, deduplicated set**, not the top of the stack, so each plan in a stack must hold exactly one candidate | ✔ (sharpened) |
| #35 | `ClaimRecord` has no change ID | `protocol.rs:1720-1727` | ✔ |
| #16 | Change ID and author on blame hunks; provenance claims | `protocol.rs:2463-2470`: `head`, `change: Option<ChangeId>`, `original_path`, `author`. There is **no description or claims field**. `commit.rs:75-104` defines the grammar for up to 32 claims, which blame would have to be **extended** to attach | ✔ (wording fixed in §2) |
| #53 | Requirement has only kind and result; bound lives in a wrapper | `protocol.rs:2037-2041`; `attestation/query.rs:66-100` | ✔ |
| #8 | The revset only discovers; `evaluate_requirements` authorizes | `attestation/query.rs:266-290`; `revset/eval.rs:139-158` | ✔ |
| #14 | `Gap` enum | `attestation/query.rs:59-80` | ✔ |
| #9 | Ambiguity is scoped to one `(subject, kind, signer)` group | `attestation.rs:20-26` | ✔ |
| #40 | Subject is the exact commit OID | `revset/eval.rs:130-135` | ✔ |
| #31 | `conflict-v1` record | `conflict.rs:60` | ✔ |
| #42 | Exclusive-label precedent | `rukie/models/issues/label.go:84-91,183-193` | ✔ |
| #22 rejection | `review.go:626` is only a stale flag | `rukie/models/issues/review.go:628-640` | ✔ rejection holds |
| #6 rejection | Undo refuses some methods | `coord/undo.rs:11-21` | ✔ rejection holds |
| #19 | `ByteBudget` | `budget.rs:6-46` | ✔ |
| #15 | `describe` shows every head of a divergent change | `ops/describe.rs:3-6` | ✔ |
| #55 | `ruk-attest` namespace | `attestation/verify.rs:75-95` | ✔, with a caveat (§2) |
| **#1** | "Duplicate of #37 Intent" | Pool #1 anchors comments to (change ID, head) and remaps them on rewrite. #37 says nothing about comments or anchoring | **✘ → survives** |
| **#34** | "Holder-only release forces a NEW lane-privileged sweeper" | see below | **✘ → rejected** |
| **#49** | "Cited shape matches A2A, not MCP" | MCP 2026-07-28 defines `InputRequiredResult`, `inputResponses` and `requestState` as multi round-trip requests (https://modelcontextprotocol.io/specification/2026-07-28/schema) | **✘ → survives (narrowed)** |
| **#21** | "Restates the settled same-URL content negotiation" | The Settled decisions table in `rukie-ruk-native-forge-plan.md` has no such row. It appears only in drafts: `agents-md-draft.md:19` and `autoplan-dx.md` DX-07 | **✘ basis → still rejected, new reason** |

**#1: survives, narrowed.** The review unit itself is already settled. What #1 adds is new: review comments and evidence anchor to (change ID, head), and `review.remap` moves them when a change is rewritten, so they are not orphaned. No survivor covers this, and the settled change/stack review unit needs it. Rejected #24 ("Evergreen Review") is folded into this one; its own rejection reason (no primitive for content-derived staleness) is still correct. The basis for #1 is that change IDs persist across rewrites (`change.rs:60-83`, which the critique checked).

**#34: rejected.** Three things are true in the code:
- **No key collision.** Each acquisition gets a fresh claim ID (`claims.rs:883`), and every index key ends with that ID (`claims.rs:595-608`). The exact-key check (`:169-177`) therefore never trips on another claim's leftovers.
- **Expired claims stop enforcing.** Overlap is only enforced while `expires_at > now` (`:350`).
- **The sweeper already exists.** `claims::sweep_page` (`claims.rs:475-516`) deletes expired records and their index keys under lane ownership, without needing the holder. It is tested (`claims/tests.rs:424`).

The candidate's novelty claim ("NEW sweeper") is false, and the misattributed `leases/timing.rs` basis is unrelated: that file covers view, cursor and bulk leases, not claims.

**The remaining issue is a blocking bug in Ruk, not cleanup.** The hosted coordinator's maintenance only sweeps receipts and sessions (`hosted.rs:53,1254-1255`), and grep finds no production caller of `claims::sweep_page`. Meanwhile:
- `check_paths` builds one `Work` budget per acquisition (`claims.rs:290-296`).
- `check_range` charges every index row it scans to that budget, expired rows included, before it checks expiry (`claims.rs:330-343`).
- `Work::charge` fails once the count exceeds `MAX_PAGE_SIZE`, which is 1000 (`claims.rs:75-79`, `protocol.rs:80`).

So once roughly 1000 unswept claims pile up on one subtree, every acquisition under it fails with `E_LIMIT` for all agents. That is an inference from the code; I did not run it. The fix is to wire `claims::sweep_page` into hosted maintenance. It is a required Ruk fix and a prerequisite for task claiming, but it is not a new abstraction. That is the same rule the critique used for #32 and #33.

**#49: survives, narrowed.** The narrowing:
- **In-band delivery:** MCP round-trips only answer the agent's **own client**.
- **New object:** a durable, typed question attached to a task or claim (options, deadline, whether it blocks the claim), answered by a human through a zero-JS form. The inbox design has no such object (`inbox-design.md` §1, sources S1–S11).
- **Dropped:** the candidate's "answer lands as an attestation", unless the question is about a commit. Attestations need an exact commit as subject (`protocol.rs:1776-1784`).

**#21: stays rejected, for a different reason.** Serving HTML, JSON and Markdown from one URL is already in the plan pipeline as autoplan DX-07, and #15 covers the agent-facing brief. It is a rendering-format decision, not an abstraction for review, tasks or context, so it fails criterion 4.

### 1.4 Scope
The settled decisions:
- Review covers changes and stacks plus a branch view.
- Tasks exist and can be claimed through Ruk leases.
- Agents are scoped tokens on human accounts.
- The UI uses zero JavaScript.
- There is one operation table.

All survivors fit except the conflicts in §4.

---

## 2. Final survivor set (20)

**Review (7)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 1 | Change-anchored review threads *(corrected)* | Comments and evidence anchor to (change ID, head) and are remapped on rewrite, never orphaned | `change.rs:60-83`; Gitea's SHA-bound stale flag `review.go:628-640` is what it replaces |
| 37 | Intent *(conflicts with settled tasks, §4-A)* | One object for task and change, carrying an acceptance predicate; landing closes it | `change.rs:60-83`; `coord/oplog.rs:40-53` |
| 8 | Requirements-gated contract | A revset discovers the subjects; `evaluate_requirements` with a trusted set authorizes landing | `attestation/query.rs:266-290`; `revset/eval.rs:139-158` |
| 9 | Quorum over live verdicts | A new rukie rule: N trusted approvals and no live block | `attestation.rs:20-26` |
| 30 | Exact Land Plans (serial, one commit each) | A stack lands as N serial plans with one candidate each, because the primary is the first sorted member | `coord/land.rs:11-22,125-130` |
| 31 | Conflicts as reviewable sub-units | A conflict is a claimable piece inside a change review, not a top-level review unit | `conflict.rs:60`; `merge/syntax/grammar.rs:205-277` |
| 40 | Proof bundle | CI results and human approvals are the same kind of evidence, keyed to the exact commit | `revset/eval.rs:130-135` (conflicts need another key) |

**Tasks (5)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 29 | Path Leases | The standard way to keep agents from editing the same paths at once. Depends on the sweeper fix from §1.3 | `coord/claims.rs:340-365` |
| 35 | Change-keyed claims | Link claims to changes so superseding or abandoning a change marks dependent claims stale | `protocol.rs:1720-1727` |
| 53 | Validity-bounded evaluation | Move land gates to bounded evaluation so `Gap::Expired` stops evidence staying green forever | `attestation/query.rs:66-100`; `land.rs:253,387` currently unbounded |
| 14 | Gap work-list | Per-subject `Gap` reasons as the agent's next action, over MCP/API and as a zero-JS page | `attestation/query.rs:59-80` |
| 49 | Question as structured ask *(corrected)* | A durable, typed question on a task or claim, delivered in-band over MCP and answered by a human through a form | MCP 2026-07-28 schema |

**Context sharing (5)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 16 | Why-Attached Blame | **Extend** blame hunks (today: head, optional change ID, author) with the change description and provenance claims | `protocol.rs:2463-2470`; `commit.rs:75-104` |
| 19 | Budgeted Views | Every read has a budget enforced by the server and either completes or returns a continuation | `budget.rs:6-46` |
| 15 | Change Brief | A small curated brief per change that links to full evidence; divergent heads are shown | `ops/describe.rs:3-6`; llmstxt.org |
| 41 | View | A board is a named, versioned revset; storing views is new Ruk work | `revset/syntax.rs:1-28`; `revset/eval.rs:26-40` |
| 50 | Handoff bundle by IDs | Hand over change, claim, cursor and attestation IDs; the next agent resumes without re-reading | `session.open/resume` per the critique's model check |

**Cross-cutting (3)**
| # | Survivor | Rationale | Evidence |
|---|---|---|---|
| 42 | Facet | A closed, typed label schema checked on write and usable as revset predicates | `label.go:84-91,183-193` |
| 48 | Scope-filtered toolsets | Tool lists filtered per request by token scope, in a fixed order | `ruk/src/mcp/tools.rs` TOOL_TABLE; github-mcp-server |
| 55 | in-toto-typed CI claims | CI posts typed predicate kinds. **Caveat:** Ruk statements have no predicate body (`protocol.rs:1776-1784`); the kind is free UTF-8 (`capability.rs:201`), but the body needs a Ruk schema change or a digest | `attestation/verify.rs:75-95`; in-toto spec |

---

## 3. Rejections (37)

| # | Reason |
|---|---|
| 2, 13, 26, 39, 44 | Duplicate of #29 |
| 3, 45, 57 | Duplicate of #8 |
| 4, 10, 46, 51 | Duplicate of #30 |
| 7 | Duplicate of #31 |
| 11 | Duplicate of #53 |
| 17 | Duplicate of #40 |
| 18 | Duplicate of #50 |
| 20, 43 | Duplicate of #41 |
| 25 | Duplicate of #15 |
| 28 | Duplicate of #35 |
| 38 | Duplicate of #9 |
| 24 | No primitive for content-derived staleness; its anchoring idea is now covered by #1 |
| 5 | A workspace per review is not feasible across a fleet |
| 6 | Overclaims that every operation can be undone (`undo.rs:11-21`) |
| 12 | Its states map to no Ruk type |
| 21 | *Corrected reason:* already planned as DX-07; a rendering concern, not an abstraction |
| 22 | No risk formula exists in any cited file |
| 23 | A group commit is serialization, not batch approval |
| 27 | No policy engine supports it |
| 32, 33, 52, 54 | Describe existing Ruk behavior |
| **34** | *Corrected:* the sweeper already exists (`claims.rs:475-516`). The missing hosted wiring is a blocking Ruk bug (§1.3), not an abstraction |
| 36 | Team budgets have nowhere to be enforced |
| 47 | Restates the settled single operation table |
| 56 | SLSA L3 isolation and keys are out of scope |

That is 37 rejections and 20 survivors, 57 in all.

---

## 4. Contradictions with settled decisions

- **A. #37 vs "tasks are their own feature."** The settled plan lists both review and tasks, and the inbox design keeps `task` separate from `change`. Intent cannot represent tasks that produce no change or that span several changes. #42, #41 and #14 are framed around Intent and need re-framing either way.
- **B. #29/#35 vs "tasks claimable through Ruk leases."** Claims are keyed by path and held by a session. #35 adds a change key but no task key, and the token-to-session mapping is undefined.
- **C. #41 vs tasks with no commit.** Revsets only see commits, so a task with no commit and a conflicted change are both invisible to a View.
- **D. #30 vs "land this branch."** In the branch view this must mean N serial plans with one candidate each, never a batch.
- **E. #29 depends on wiring `claims::sweep_page` into hosted maintenance.** Without it, claiming eventually fails with `E_LIMIT` (§1.3).

---

## 5. Questions the user must answer

1. **What is a task?**
   - (a) A task is the same object as a change (#37 as written).
   - (b) A task is its own object linking zero or more changes and carrying the #8 contract; #37 is reduced to "a change carries a predicate".
   - (c) Hybrid: a task is its own object that closes automatically when its single linked change lands with the contract satisfied.
2. **What does an agent claim?**
   - (a) Paths only (#29).
   - (b) A task or change ID, by adding a task key to Ruk's `ClaimRecord`.
   - (c) Both: claiming a task acquires its path claims under one record.
3. **What powers boards?**
   - (a) Revset Views only; work with no commit is not shown.
   - (b) Views extended with predicates over rukie's own task, facet and claim data.
   - (c) Fixed server-rendered queues and no saved views.
4. **When does a change count as reviewed?**
   - (a) A quorum per contract (#9) with freshness bounds (#53).
   - (b) One trusted approval by default, with quorum as an opt-in.
   - (c) Facet-driven: low-risk facets can land on signed CI evidence alone (#55); others need a human quorum.

---

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/claims.rs`: claim enforcement, the existing `sweep_page`, and the scan budget behind `E_LIMIT` (#29, #35, the #34 bug, question 2)
- `ruk/crates/ruk-core/src/coord/hosted.rs`: where `claims::sweep_page` must be wired into maintenance
- `ruk/crates/ruk-core/src/attestation/query.rs`: `Gap`, `BoundedRequirement`, evaluation (#8, #9, #14, #53)
- `ruk/crates/ruk-core/src/coord/land.rs`: sorted primary candidate, unbounded evaluation at `:253,387` (#30, #53)
- `ruk/crates/ruk-core/src/protocol.rs`: `ClaimRecord`, `BlameHunk`, `AttestStatement` (#35, #16, #55)