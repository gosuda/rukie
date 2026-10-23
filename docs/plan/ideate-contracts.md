## Contracts & state machines: 7 candidates

**1. Acceptance Contract (Hoare triple on a change)**
- *Idea:* Every task is `{Pre} change {Accept}`: typed preconditions (base ref, required files) plus acceptance predicates (revset + required attestation kinds). Ops: `contract.define`, `contract.amend` (versioned), `contract.check(subject)` returning satisfied/gap list.
- *Rationale:* Replaces prose issues with a machine-checkable "done"; agents self-verify before asking review, humans read one predicate list instead of threads.
- *Basis:* `ruk/crates/ruk-core/src/attestation/query.rs:285` (`evaluate_requirements` per-subject predicate check)

**2. Evidence Ledger (attestation-bound review)**
- *Idea:* Review verdicts are signed attestations pinned to the exact subject OID, with statuses Live/Superseded/Ambiguous; a rebase orphans old verdicts automatically instead of drifting comments.
- *Rationale:* Kills stale force-push review drift; per-change evidence is queryable, and disagreement is a first-class Ambiguous state, not emoji.
- *Basis:* `ruk/crates/ruk-core/src/attestation.rs:25-30` (Superseded/Ambiguous/Live group statuses)

**3. Land Gate (two-phase merge contract)**
- *Idea:* Landing = `land.prepare` freezes (candidates, base tip, attestation snapshot, 1h deadline) then `land.execute` re-validates live and refuses on drift. No direct push-to-main; every merge states its contract up front.
- *Rationale:* Replaces PR "green then merged something else" races with exact-candidate semantics; agents get typed `E_STALE`/`E_POLICY` declines they can act on.
- *Basis:* `ruk/crates/ruk-core/src/coord/land.rs:1-30` (exactness contract doc); `ruk/crates/ruk-core/src/protocol.rs:2026` (`LandPrepareArgs`)

**4. Bounded Freshness Predicates (expiring evidence)**
- *Idea:* Requirements carry `latest signed_at` bounds (`BoundedRequirement`): CI attestations older than N hours stop satisfying the gate, forcing re-run rather than trusting stale green.
- *Rationale:* CI logs rot; bounded predicates make "tested" mean "tested recently on this exact content," checkable by the daemon, not by humans eyeballing timestamps.
- *Basis:* `ruk/crates/ruk-core/src/attestation/query.rs:90` (`BoundedRequirement` with freshness bound)

**5. Lifecycle Automaton (no kanban columns)**
- *Idea:* A task/change moves only via typed transitions (proposed→evidenced→attested→landed/abandoned), each transition guarded by a predicate evaluation; illegal transitions are `E_INVALID`, never a drag-and-drop.
- *Rationale:* Status is derived from verifiable state (attestations + land outcome), so boards can't lie; agents can query "what transition is available and what evidence is missing."
- *Basis:* `ruk/crates/ruk-core/src/attestation/query.rs:99` (`RequirementOutcome::Satisfied` vs gap); `ruk/crates/ruk-core/src/coord/land.rs:48-63` (plan expiry/consumption transitions)

**6. Capability-Scoped Claim (lease as precondition)**
- *Idea:* Claiming a contract requires a Ruk lease plus a `policy.toml` capability (`write_changes`, `claim_keys`); the claim TTL (3600s) bounds the Hoare-triple's frame, and expiry auto-releases.
- *Rationale:* Replaces "assign to me" comments with an enforceable mutual-exclusion precondition; duplicate agent work becomes structurally impossible, and humans audit via policy file.
- *Basis:* `ruk/crates/ruk-core/src/coord/policy.rs:62-79` (policy.toml capabilities); `/tmp/ruk-model.json` (`claim_ttl_seconds: 3600` limit)

**7. Gap Query (machine-readable "what's missing")**
- *Idea:* `contract.status` returns the `Gap` enum per requirement (`NoLiveMatch` vs conflicting statements) over exact OIDs, so an agent's next action is computed, not inferred from thread archaeology.
- *Rationale:* Turns review feedback into a queryable work list; agents and humans share one source of "done-ness" instead of scattered comments, CI tabs, and chat.
- *Basis:* `ruk/crates/ruk-core/src/attestation/query.rs:60` (`Gap::NoLiveMatch` variants)

*Note: could not write `ideate-contracts.md` — no file-write tool is exposed in this session; main agent should save the above verbatim.*

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/attestation/query.rs` — Requirement/Gap evaluation primitives to reuse for contract checks
- `ruk/crates/ruk-core/src/coord/land.rs` — Two-phase prepare/execute pattern for the land-gate abstraction
- `ruk/crates/ruk-core/src/attestation.rs` — Attestation lifecycle statuses grounding evidence-bound review
- `ruk/crates/ruk-core/src/coord/policy.rs` — Capability/claim model grounding scoped task leases
- `ruk/crates/ruk-core/src/protocol.rs` — Operation-table shapes (`AttestAddArgs`, `LandPrepareArgs`) for the JSON/MCP/CLI surface


[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

Valid point on the truncated model — pulling the exact method fields.
→ read(/tmp/ruk-model.json?q=.%24defs.LandPrepareArgs%20%7C%20keys) ⇒ error · 7 lines — Failed to execute JSON query: Error: expected nothing

[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Candidates 2, 3, 4, 6, 7 only rest…

---
Noted — sharpening the rukie-specific layer on top of Ruk primitives, and re-pulling the exact protocol fields.
→ read(/tmp/ruk-model.json?q=%24defs%7Ckeys) ⇒ error · 7 lines — Failed to execute JSON query: Error: undefined variable

---
→ read(/tmp/ruk-model.json?q=.%22%24defs%22%7Ckeys) ⇒ ok · 304 lines

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Separate wire fields from int…

---
Checking the exact wire-vs-coordinator split before finalizing.
→ read(/tmp/ruk-model.json?q=.%22%24defs%22.LandPrepareArgs.properties%7Ckeys) ⇒ ok · 6 lines
→ read(/tmp/ruk-model.json?q=.%22%24defs%22.AttestAddArgs.properties%7Ckeys) ⇒ ok · 4 lines

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Write the required file befor…

---
Verifying the coordinator-side request shape to keep the wire contract exact.
→ grep(struct LandPrepareRequest|pub candidates|pub requirements|pub policy|pub base|pub deadline|pub target @ /home/alpha/rew…) ⇒ ok · 49 lines

---
## Contracts & state machines (7 candidates)

**1. Acceptance Contract.** Rukie adds a `contract` record per task: `{Pre}` (base ref, scope revset) plus acceptance predicates (attestation kinds + revset checks), versioned via `contract.amend`. `contract.check(oid)` evaluates them live; landing is refused until all pass. Beats prose issues: "done" is computable, agents self-verify. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:285` (`evaluate_requirements`).

**2. Evidence-anchored verdict.** Rukie defines review-verdict attestation kinds (approve, approve-if-green, block-with-reason) posted via `attest.add` carrying only `{statement, signature}` on the exact subject OID. A rewrite supersedes old verdicts (Live/Superseded/Ambiguous) instead of drifting comments. Rukie's addition: the verdict-kind vocabulary + per-OID rendering. Basis: `/tmp/ruk-model.json` (`AttestAddArgs`: `signature`, `statement`); `ruk/crates/ruk-core/src/attestation.rs:25-30` (status groups).

**3. Enriched land gate.** Wire `land.prepare` carries only `{change, merge, remote, target}`; Rukie's forge layer enriches this into coordinator `LandPrepareRequest` with exact `candidates`, `requirements`, `trusted`, `policy_generation` resolved from the task contract. Execute re-validates live, refusing drift with typed declines. Rukie's addition: the enrichment service mapping contract→frozen plan. Basis: `/tmp/ruk-model.json` (`LandPrepareArgs`: `change, merge, remote, target`); `ruk/crates/ruk-core/src/coord/land.rs:74` (`LandPrepareRequest` with candidates/requirements).

**4. Freshness-bound CI evidence.** Rukie's built-in CI posts result attestations; contracts require them as `BoundedRequirement` (kind + result + max `signed_at` age). Stale green stops satisfying the gate, forcing re-run on current content. Rukie's addition: CI→contract binding and "re-verify" action. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:90` (`BoundedRequirement`).

**5. Derived lifecycle automaton.** Rukie computes task state (proposed→evidenced→attested→landed/abandoned) from attestations, claims, and land outcomes; transitions are guarded, illegal ones rejected, plans consumed on execution so double-land is impossible. No manual kanban columns; status can't lie. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:99` (`RequirementOutcome`); `ruk/crates/ruk-core/src/coord/land.rs:104` (frozen plan + base).

**6. Lease-scoped task claim.** Rukie binds `task.claim` to a Ruk lease (TTL 3600s) gated by `policy.toml` capabilities/claim-keys on scoped human-account tokens; expiry auto-releases, duplicate claims fail. Replaces "assign to me" comments with mutual exclusion. Basis: `ruk/crates/ruk-core/src/coord/policy.rs:62-79` (capabilities); `/tmp/ruk-model.json` (`claim_ttl_seconds: 3600`).

**7. Gap work-list.** Rukie exposes per-task `Gap` evaluation (`NoLiveMatch` vs conflicting statements) as an MCP/API resource: the exact missing-evidence list and next action for the claiming agent, server-rendered with zero JS. Turns review feedback into a query, not thread archaeology. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:60` (`Gap` enum).

*File-write blocked: no file-writing tool is granted in this session (only read/grep/web_search); attempted jq verification via `read` instead. Main agent: save the above to `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ideate-contracts.md`.*

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/attestation/query.rs` — Requirement/Gap evaluation for contract checks
- `ruk/crates/ruk-core/src/coord/land.rs` — Coordinator request/plan shapes for the enrichment gate
- `ruk/crates/ruk-core/src/coord/policy.rs` — Capability/claim model for scoped task leases
- `ruk/crates/ruk-core/src/protocol.rs` — Operation-table shapes for the JSON/MCP/CLI surface


[advisor] <advisory advisor="default" severity="blocker" guidance="weigh, don't blindly obey"> Write the missing file now with yo…

[advisor] <advisory advisor="muse" severity="blocker" guidance="weigh, don't blindly obey"> Write the file now. File: /home/alpha…

---
Attempting the required file write now.
→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local) ⇒ ok · 1 line

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Add revset basis to one acceptance…

---
Grounding the acceptance predicate in the revset engine.
→ grep(struct RevsetText|RevsetText @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/protocol.rs) ⇒ ok · 24 lines
→ grep(production_attested|fn attested|attested\( @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/revset/eval.rs) ⇒ ok · 115 lines

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Candidate 2 is wrong: Ambiguous a…

---
**BLOCKER — file unwritten:** `.../local/ideate-contracts.md` does not exist (target dir verified empty). No write-capable tool is granted in this session (only `read`/`grep`/`web_search`); all prior "write" advisories could not be executed. Main agent must save the text below. All corrections applied.

## Contracts & state machines (7 candidates)

**1. Revset-scoped acceptance contract.** Rukie's `contract` record names its subject set with a `RevsetText` expression (served by `log`/`query`) and its predicates with `attested(kind,result)`, evaluated by `production_attested` over retained statements. `contract.check` returns per-subject pass/gap. Replaces prose issues with a computable Hoare triple. Basis: `ruk/crates/ruk-core/src/protocol.rs:768` (`RevsetText`); `ruk/crates/ruk-core/src/revset/eval.rs:148` (`production_attested`); `ruk/crates/ruk-core/src/attestation/query.rs:285` (`evaluate_requirements`).

**2. Quorum over live verdicts (corrected).** Verdicts are `attest.add` `{statement, signature}` rows on the exact OID. `Ambiguous` applies only *within* one `(subject,kind,signer)` group; two reviewers disagreeing = two `Live` statements across groups. Rukie's addition: a quorum rule over trusted signers (e.g. 2 approvals, zero live `block`). Basis: `ruk/crates/ruk-core/src/attestation.rs:22-27` (group-scoped Ambiguous); `/tmp/ruk-model.json` (`AttestAddArgs`: `signature`, `statement`).

**3. Enriched land gate.** Wire `land.prepare` carries only `{change, merge, remote, target}`; Rukie's forge layer enriches it into coordinator `LandPrepareRequest` (`candidates`, `requirements`, `trusted`, `policy_generation`) resolved from the contract, then `land.execute` re-validates live and refuses drift. Rukie's addition: the contract→plan enrichment service. Basis: `/tmp/ruk-model.json` (`LandPrepareArgs`: `change, merge, remote, target`); `ruk/crates/ruk-core/src/coord/land.rs:74` (`LandPrepareRequest`).

**4. Bounded evidence + key hygiene (corrected).** `BoundedRequirement`'s `latest signed_at` is an acceptance filter, not timestamp verification — timestamps are signed claims and no code picks winners, so a lying CI key passes any bound it signs. Rukie therefore pairs bounds with a trusted-signer allowlist, short-lived CI keys, and `policy_generation` rotation/revocation. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:90` (`BoundedRequirement`); `ruk/crates/ruk-core/src/attestation.rs:17` (timestamps are signed claims).

**5. Derived lifecycle automaton.** Task state (proposed→evidenced→attested→landed/abandoned) is computed from attestations, claims, and land outcomes; transitions are guarded, illegal ones rejected, plans consumed on execution. No manual kanban columns. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:99` (`RequirementOutcome`); `ruk/crates/ruk-core/src/coord/land.rs:104` (frozen candidates/base/requirements).

**6. Lease-scoped task claim.** Rukie binds `task.claim` to a Ruk lease (TTL 3600s) gated by `policy.toml` capabilities/claim-keys on scoped human-account tokens; expiry auto-releases, duplicates fail. Replaces "assign to me" with mutual exclusion. Basis: `ruk/crates/ruk-core/src/coord/policy.rs:62-79` (capabilities); `/tmp/ruk-model.json` (`claim_ttl_seconds: 3600`).

**7. Revset-named gap work-list.** Rukie evaluates `Gap` (`NoLiveMatch` vs conflict) per subject *of the contract's revset*, exposing the missing-evidence list as an MCP/API resource and zero-JS page. Agents get next actions; humans see one "done-ness" source. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:60` (`Gap`); `ruk/crates/ruk-core/src/revset/eval.rs:140` (production `attested(kind,result)` lookup).

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/attestation/query.rs` — Requirement/Gap evaluation for contract checks
- `ruk/crates/ruk-core/src/revset/eval.rs` — `attested()` revset binding for predicate scopes
- `ruk/crates/ruk-core/src/coord/land.rs` — Coordinator request/plan shapes for the enrichment gate
- `ruk/crates/ruk-core/src/coord/policy.rs` — Capability/claim model for scoped task leases


[advisor] <advisory advisor="overlycritic" severity="concern" turns_ago="1" guidance="weigh, don't blindly obey"> Bind candidate …

[advisor] <advisory advisor="qwen" severity="blocker" guidance="weigh, don't blindly obey"> Write the file now. File /home/alpha/…

---
Writing the required file now.
→ default.write({}) ⇒ error · 1 line — Tool default.write not found

---
→ default.write({}) ⇒ error · 1 line — Tool default.write not found

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Stop extra reads. Write the file…

---
Saving the contracted file via the file tool.
→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/loca…) ⇒ error · 1 line — Path '/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf751…

---
File confirmed absent; attempting write with the granted file mechanism.
→ grep(ideate-contracts @ /home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8db…) ⇒ ok · 1 line

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Add explicit `Preconditions`,…

---
**BLOCKER — file unwritten:** `.../local/ideate-contracts.md` absent (dir verified empty); only `read`/`grep`/`web_search` granted, so Main must save this text.

## Contracts & state machines (7 candidates)

**1. Revset-scoped acceptance contract.** Idea: Rukie `contract` record holds `Pre` (base ref, lease held) and acceptance predicates over a `RevsetText` subject set (via `log`/`query`), including `attested(kind,result)` evaluated by `production_attested`. Pre: contract version pinned, lease live. Accept: `contract.check` all-satisfied. Ev: predicate report + revset expression. Beats prose issues. Basis: `ruk/crates/ruk-core/src/protocol.rs:768`; `ruk/crates/ruk-core/src/revset/eval.rs:148`; `ruk/crates/ruk-core/src/attestation/query.rs:285`.

**2. Quorum over live verdicts.** Idea: verdicts are `attest.add` `{statement,signature}` on exact OID; `Ambiguous` is single-`(subject,kind,signer)`-group only, cross-reviewer disagreement = multiple `Live` rows. Pre: subject OID frozen. Accept: quorum over trusted signers (e.g. 2 approvals, 0 live blocks). Ev: retained Live statements. Basis: `ruk/crates/ruk-core/src/attestation.rs:22-27`; `/tmp/ruk-model.json` (`AttestAddArgs`).

**3. Enriched land gate.** Idea: wire `land.prepare` sends only `{change,merge,remote,target}`; forge layer enriches to `LandPrepareRequest` (`candidates,requirements,trusted,policy_generation`) from the contract; execute re-validates live. Pre: plan unexpired, base tip equal. Accept: all candidates meet requirements. Ev: stored plan + outcome. Basis: `/tmp/ruk-model.json` (`LandPrepareArgs`); `ruk/crates/ruk-core/src/coord/land.rs:74`.

**4. Bounded evidence + key hygiene.** Idea: `BoundedRequirement` age bound is an acceptance filter only — timestamps are signed claims, a lying key passes any bound. Pre: signer in trusted set, key fresh/ unrevoked. Accept: live matching statement within bound. Ev: retained statements + policy generation. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:90`; `ruk/crates/ruk-core/src/attestation.rs:17`.

**5. Derived lifecycle automaton.** Idea: state (proposed→evidenced→attested→landed/abandoned) computed from attestations/claims/land outcomes; guarded transitions, plans consumed on execution. Pre: transition legal from current derived state. Accept: guard predicate satisfied. Ev: evaluation + consumed plan row. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:99`; `ruk/crates/ruk-core/src/coord/land.rs:104`.

**6. Lease-scoped task claim.** Idea: `task.claim` binds a Ruk lease (TTL 3600s) gated by `policy.toml` capabilities on scoped tokens; expiry auto-releases. Pre: capability present, no live holder. Accept: lease acquired. Ev: claim record. Basis: `ruk/crates/ruk-core/src/coord/policy.rs:62-79`; `/tmp/ruk-model.json` (`claim_ttl_seconds: 3600`).

**7. Revset-named gap work-list.** Idea: `Gap` evaluated per subject of the contract's revset, exposed as MCP/API + zero-JS page. Pre: revset resolves non-empty. Accept: zero gaps. Ev: per-subject gap list. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:60`; `ruk/crates/ruk-core/src/revset/eval.rs:140`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/attestation/query.rs` — requirement/gap evaluation
- `ruk/crates/ruk-core/src/revset/eval.rs` — `attested()` revset binding
- `ruk/crates/ruk-core/src/coord/land.rs` — plan shapes for the gate
- `ruk/crates/ruk-core/src/coord/policy.rs` — capability/claim model


[advisor] <advisory advisor="minimax" severity="nit" turns_ago="3" guidance="weigh, don't blindly obey"> Stop probing write tools…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Stop retrying `default.write`…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Do not probe with `grep`; it …

---
**NOT COMPLETE — file still unwritten (blocker).** Target `.../local/ideate-contracts.md` verified absent (`read` → not found; parent dir reads empty). Only `read`/`grep`/`web_search` are granted in this session; `default.write` returns "tool not found" and `grep` cannot create files. I cannot satisfy the `file written` predicate from here. Draft below is complete and ready for Main (which holds a writer) to save verbatim — do not treat chat text as the artifact.

## Draft for Main to save as `ideate-contracts.md` (7 candidates)

**1. Revset-scoped acceptance contract.** Rukie `contract` record holds `Pre` (base ref, lease held) and acceptance predicates over a `RevsetText` subject set (via `log`/`query`), including `attested(kind,result)` evaluated by `production_attested`. Pre: contract version pinned, lease live. Accept: `contract.check` all-satisfied. Ev: predicate report + revset expression. Beats prose issues. Basis: `ruk/crates/ruk-core/src/protocol.rs:768`; `ruk/crates/ruk-core/src/revset/eval.rs:148`; `ruk/crates/ruk-core/src/attestation/query.rs:285`.

**2. Quorum over live verdicts.** Verdicts are `attest.add` `{statement,signature}` on exact OID; `Ambiguous` is single-`(subject,kind,signer)`-group only, cross-reviewer disagreement = multiple `Live` rows. Pre: subject OID frozen. Accept: quorum over trusted signers (e.g. 2 approvals, 0 live blocks). Ev: retained Live statements. Basis: `ruk/crates/ruk-core/src/attestation.rs:22-27`; `/tmp/ruk-model.json` (`AttestAddArgs`).

**3. Enriched land gate.** Wire `land.prepare` sends only `{change,merge,remote,target}`; forge layer enriches to `LandPrepareRequest` (`candidates,requirements,trusted,policy_generation`) from the contract; execute re-validates live. Pre: plan unexpired, base tip equal. Accept: all candidates meet requirements. Ev: stored plan + outcome. Basis: `/tmp/ruk-model.json` (`LandPrepareArgs`); `ruk/crates/ruk-core/src/coord/land.rs:74`.

**4. Bounded evidence + key hygiene.** `BoundedRequirement` age bound is an acceptance filter only — timestamps are signed claims, a lying key passes any bound. Pre: signer in trusted set, key fresh/unrevoked. Accept: live matching statement within bound. Ev: retained statements + policy generation. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:90`; `ruk/crates/ruk-core/src/attestation.rs:17`.

**5. Derived lifecycle automaton.** State (proposed→evidenced→attested→landed/abandoned) computed from attestations/claims/land outcomes; guarded transitions, plans consumed on execution. Pre: transition legal from derived state. Accept: guard predicate satisfied. Ev: evaluation + consumed plan row. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:99`; `ruk/crates/ruk-core/src/coord/land.rs:104`.

**6. Lease-scoped task claim.** `task.claim` binds a Ruk lease (TTL 3600s) gated by `policy.toml` capabilities on scoped tokens; expiry auto-releases. Pre: capability present, no live holder. Accept: lease acquired. Ev: claim record. Basis: `ruk/crates/ruk-core/src/coord/policy.rs:62-79`; `/tmp/ruk-model.json` (`claim_ttl_seconds: 3600`).

**7. Revset-named gap work-list.** `Gap` evaluated per subject of the contract's revset, exposed as MCP/API + zero-JS page. Pre: revset resolves non-empty. Accept: zero gaps. Ev: per-subject gap list. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:60`; `ruk/crates/ruk-core/src/revset/eval.rs:140`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/attestation/query.rs` — requirement/gap evaluation
- `ruk/crates/ruk-core/src/revset/eval.rs` — `attested()` revset binding
- `ruk/crates/ruk-core/src/coord/land.rs` — plan shapes for the gate
- `ruk/crates/ruk-core/src/coord/policy.rs` — capability/claim model


[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Candidate 1: do not gate contract…

---
Verifying both concerns against the source before correcting.
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/attestation/query.rs:240-280) ⇒ ok · 55 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/revset/eval.rs:130-158) ⇒ ok · 35 lines

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Stop repeating the list. Fix Cand…

---
Confirming the revocation anchors, then shipping the corrected pair.
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/land.rs:108-120) ⇒ ok · 21 lines
→ grep(narrowed_policy_revokes|revokes_the_next_authorization @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/…) ⇒ ok · 5 lines

---
**BLOCKER (unchanged):** file unwritten — no writer granted; Main must save. Below are the two corrected candidates; 2, 3, 5, 6, 7 stand as in the prior draft.

**1. Revset-named, requirements-gated contract (corrected).** Revset `attested(kind,result)` names/discovers subjects only — it "exposes ambiguity and is not land authorization," and per-signer ambiguity collapses to true in the revset hook. The gate MUST be `evaluate_requirements`/`evaluate_bounded_requirements` with an explicit trusted-signer set per subject of the revset. Pre: trusted set non-empty (else `E_POLICY`), requirements frozen with the contract version. Accept: requirements all-satisfied on every revset subject. Ev: `RequirementEvaluation` vector + revset expression. Otherwise the contract passes what land refuses. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:250-253` ("not land authorization"); `ruk/crates/ruk-core/src/revset/eval.rs:140-146` (ambiguity collapses to true); `ruk/crates/ruk-core/src/attestation/query.rs:285` (trusted-signer evaluation).

**4. Bounded evidence + forge-managed allowlist (corrected).** Dropped: "key fresh/unrevoked" Pre and "`policy_generation` rotation/revocation" — ruk-core has no signer-key revocation. `policy_generation` versions the requirements, and the narrowing test revokes principal authority (capabilities), not signer keys; a compromised CI key verifies forever wherever its fingerprint stays listed. Mitigation lives at the rukie layer: forge-managed signer allowlist (stop embedding the fingerprint in future trusted sets, rotate CI keys out-of-band); bounds remain acceptance filters only. Pre: signer allowlisted in this contract's trusted set. Accept: live matching statement within bound. Ev: retained statements + allowlist version. Basis: `ruk/crates/ruk-core/src/attestation/query.rs:90`; `ruk/crates/ruk-core/src/attestation.rs:17`; `ruk/crates/ruk-core/src/coord/land.rs:115` (`policy_generation`); `ruk/crates/ruk-core/src/coord/policy.rs:1975` (revokes authorization, not keys).

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/attestation/query.rs` — `attested()` vs `evaluate_requirements` split
- `ruk/crates/ruk-core/src/revset/eval.rs` — revset hook semantics for discovery-only use
- `ruk/crates/ruk-core/src/coord/land.rs` — trusted-set freezing per plan
- `ruk/crates/ruk-core/src/coord/policy.rs` — principal (not signer-key) authority model


[advisor] <advisory advisor="overlycritic" severity="concern" turns_ago="3" guidance="weigh, don't blindly obey"> Make strict aut…

---
Already incorporated — no new action. The prior delivery gates strict authorization on `evaluate_requirements`/`evaluate_bounded_requirements` with an explicit trusted set and reserves revset `attested()` for subject discovery, citing `attestation/query.rs:250-253`, `revset/eval.rs:140-146`, and `query.rs:285`. Main can assemble: corrected 1 + 4 from the last message, 2/3/5/6/7 from the draft before it. File-write blocker stands (no writer granted; Main must save).


[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Do not imply policy rotation …

[advisor] <advisory advisor="default" severity="concern" guidance="weigh, don't blindly obey"> Send one complete draft next, not …

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Candidate 5: ground its states in…