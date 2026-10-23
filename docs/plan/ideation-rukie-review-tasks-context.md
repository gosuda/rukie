# Ideation: review, tasks, and context sharing for rukie

Subject: better abstractions than issues, pull requests, and kanban boards for agentic coding in rukie.
Mode: brainstorm ideate. Generators: 8 (frames: Ruk-native mechanics, formal contracts, context and information, human supervision, reliability at agent scale, constraint removal, agent developer experience, evidence and trust). Raw candidates: 57. Critic: reject-by-default (local://ideation-critique.md). Adjudicator: local://ideation-adjudication.md.
Grounding: Ruk protocol schema (/tmp/ruk-model.json), ruk-core sources (change.rs, coord/claims.rs, coord/land.rs, attestation/*, revset/*, conflict.rs, budget.rs, commit.rs), rukie Gitea models for contrast, llmstxt.org, MCP 2026-07-28 spec, in-toto attestation spec.
Counts: 57 raw, 19 survivors, 38 rejected.

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