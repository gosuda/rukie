# Radical primitives: delete issues/PRs/kanban (7 candidates)
Frame: 3 nouns — `Intent`, `Proof`, `View` — cover everything. Contrast: `rukie/models/issues/` has ~45 files (`issue.go`/`pull.go`/`comment.go`/`label.go`/`milestone.go`/`stopwatch.go`…); `rukie/models/project/column.go:1-50` burns a table on card color regexes.

**1. Intent (task ≡ change, one object)**
Idea: No issue-then-PR. An `Intent` is born with a Ruk `ChangeId` and a machine-checkable acceptance predicate. `describe/edit/split/squash/rebase/abandon` mutate it; closing = landing the change, never a separate button. Empty intent (no commit yet) is just intent at generation 0.
Rationale: Kills issue/PR dualism (`Issue represents an issue or pull request`); agents never chase prose detached from code.
Basis: `ruk/crates/ruk-core/src/change.rs:60-83` (ChangeId identity); `ruk/crates/ruk-core/src/coord/oplog.rs:40-53` (Changes+ChangeHistory tracked).

**2. Verdict-as-property (review is not an object)**
Idea: No review/PR-approval tables. Review state is a derived query over attestations on the intent: `Live/Ambiguous/Superseded` per (subject,kind,signer). Landing requires `evaluate_requirements` to pass; disagreement = Ambiguous, never a "requested changes" row.
Rationale: Review can't drift after force-push — it is addressed to exact commits, supersession is signed, not a stale comment thread.
Basis: `ruk/crates/ruk-core/src/attestation.rs:20-32` (statuses, supersession); `ruk/crates/ruk-core/src/attestation.rs:50-63` (evaluate_requirements export).

**3. Lease (replaces assignee + status + WIP)**
Idea: `claim.acquire/release/list` with TTL is the only "who works on it / is it in progress". Claiming an intent leases its ChangeId; expiry auto-returns it to unclaimed. No assignee columns, no `in-progress` kanban drag.
Rationale: Assignees lie; leases are verifiable, expiring, and agent-claimable. Duplicate agent work becomes a visible double-claim conflict.
Basis: `ruk/crates/ruk-core/src/coord/claims.rs:1-63` (ClaimRow/ClockRow, TTL); `ruk/crates/ruk-core/src/workspace.rs:1-53` (stamp/lease-epoch fencing via `ws.take`).

**4. Proof bundle (CI + human evidence, one kind)**
Idea: CI results, test logs, and human "LGTM" are all `attest.add` rows over the exact subject commit. `attested(kind,result)` in revsets selects intents by evidence; no separate checks API vs comments.
Rationale: One evidence namespace agents can query; per-change evidence survives (PRs today bundle many changes and hide it).
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`attested(kind,result)`, `description()`, `change()` atoms); `ruk/crates/ruk-core/src/revset/eval.rs:14-22` (injected AttestedQuery).

**5. View (saved revset replaces boards, milestones, queues)**
Idea: No columns/sprints. A board is a named, versioned revset: `needs-review = mine() & ~attested(ci,pass)`, `sprint = descendants(milestone-tag) & ~landed`. Dragging a card = editing the stored query or tagging, not mutating row state.
Rationale: Kanban status is manual fiction; revset views are always true because they compute over graph + proofs.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:1-28` (full grammar, set ops `& ~ |`); `ruk/crates/ruk-core/src/revset/eval.rs:26-40` (deterministic newest-first paged eval).

**6. Facet (typed labels, not strings)**
Idea: Labels die as freeform strings (`label.go` + `issue_label.go` join tables). Facets are a closed schema on Intent: `severity:enum`, `area:path-glob`, `risk:u8`, queryable as revset predicates, validated at `describe`.
Rationale: Agents can filter/route deterministically; humans get a schema instead of 400 near-duplicate labels.
Basis: `rukie/models/issues/issue.go:48-53` (single struct for issue-or-PR sprawl); `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`files(glob)`, `description(text)` as typed predicate precedent).

**7. Trail (oplog replaces threads/activity)**
Idea: No comment-thread-as-history. Every mutation is an oplog delta; discussion that changes nothing is a `note` attestation, never history. "What happened" = `op.log` replay, fully reconstructable.
Rationale: Threads scatter context across issue/PR/chat; a single append-only trail gives agents one causal order to read.
Basis: `ruk/crates/ruk-core/src/coord/oplog.rs:1-30` (fragmented op stream, quarantine on corruption).
Drops: none (all have inspected anchors). File write: no write/edit tool exposed in this harness — main agent, persist this verbatim to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — revset grammar hosting View/Facet/Proof predicates
- `ruk/crates/ruk-core/src/attestation.rs` — Verdict-as-property + Proof bundle semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease primitive (TTL, acquire/release)
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail history + tracked-table census
- `rukie/models/issues/issue.go` — sprawl contrast to delete (issue/PR dual struct)


[advisor] The following is an aggregated review from other models. Treat it critically as potentially incorrect or delayed feedba…

---
## Later corrections from the same agent (apply over the body above)

Stale critique claims directory-only grounding — verifying my anchors already cover concrete symbol bodies.
→ read(skill://advisory-triage) ⇒ ok · 43 lines

[advisor] <advisory advisor="minimax" severity="blocker" guidance="weigh, don't blindly obey"> Write the file; do not hand text b…

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Separate the three-noun recom…

---
# Radical primitives (revised): 3 nouns — `Intent`, `Proof`, `View`
Global set: everything is a property or derived query over these three. `Lease`/`Facet`/`Verdict` are not new primitives; `Trail`/`Board` are saved `View`s. This fixes the prior draft's 5-noun sprawl.

**1. Intent (task ≡ change)** — Nouns: `Intent` alone.
Idea: No issue-then-PR. An `Intent` is born with a Ruk `ChangeId` plus a machine-checkable acceptance predicate. It mutates via `describe/edit/split/squash/rebase/abandon`; closing = landing, never a separate button.
Rationale: Kills the `Issue represents an issue or pull request` dualism; agents never chase prose detached from code.
Basis: `ruk/crates/ruk-core/src/change.rs:60-83` (ChangeId identity); `ruk/crates/ruk-core/src/coord/oplog.rs:40-53` (Changes+ChangeHistory tracked).

**2. Verdict-as-property** — Nouns: `Proof` derived, no new noun.
Idea: Review is not an object. Verdict is computed over `Proof`s per (subject,kind,signer) as `Live/Ambiguous/Superseded`; `evaluate_requirements` gates landing. Disagreement reads as Ambiguous, never a "requested-changes" row.
Rationale: Verdicts can't drift after force-push; they address exact commits and supersede by signature, not stale threads.
Basis: `ruk/crates/ruk-core/src/attestation.rs:20-32` (statuses, supersession); `ruk/crates/ruk-core/src/attestation.rs:50-63` (evaluate_requirements).

**3. Lease-as-claim** — Nouns: `Intent.claim` property.
Idea: `claim.acquire/release/list` with TTL is the only assignee/status/WIP. Claiming leases the intent's ChangeId; expiry auto-returns it. No assignee columns, no kanban drag.
Rationale: Assignees lie; expiring leases are verifiable, agent-claimable, and make duplicate work a visible double-claim.
Basis: `ruk/crates/ruk-core/src/coord/claims.rs:1-63` (ClaimRow/ClockRow, TTL); `ruk/crates/ruk-core/src/workspace.rs:1-53` (lease-epoch fencing via `ws.take`).

**4. Proof bundle** — Nouns: `Proof` alone.
Idea: CI results and human LGTMs are both `attest.add` rows over the exact subject commit. `attested(kind,result)` selects intents by evidence; no separate checks-vs-comments APIs.
Rationale: One queryable evidence namespace with per-change granularity that bundled PRs hide.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`attested()`, `change()`, `description()` atoms); `ruk/crates/ruk-core/src/revset/eval.rs:14-22` (injected AttestedQuery).

**5. View (boards/queues as saved revsets)** — Nouns: `View` alone.
Idea: No columns/sprints tables. A board is a named versioned revset, e.g. `needs-review = mine() & ~attested(ci,pass)`. Moving a card edits the stored query or a tag, never row state.
Rationale: Kanban status is manual fiction; computed views are always true over graph+proofs.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:1-28` (set ops `& ~ |`); `ruk/crates/ruk-core/src/revset/eval.rs:26-40` (deterministic newest-first paged eval).

**6. Facet (typed labels)** — Nouns: `Intent.facet` property.
Idea: Freeform label strings die. Facets are closed schema on Intent (`severity:enum`, `area:path-glob`, `risk:u8`), validated at `describe` and usable as revset predicates.
Rationale: Deterministic routing/filtering for agents instead of 400 near-duplicate label strings.
Basis: `rukie/models/issues/issue.go:48-53` (issue-or-PR struct sprawl); `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`files(glob)` typed-predicate precedent).

**7. Trail (history as a View)** — Nouns: `View` over oplog.
Idea: No activity/comment tables as history. "What happened" is a saved `View` over the append-only op stream; non-mutating discussion is a `note` Proof, never history.
Rationale: One causal order for agents instead of context scattered across threads, feeds, and chat.
Basis: `ruk/crates/ruk-core/src/coord/oplog.rs:1-30` (fragmented op stream, quarantine); contrast `rukie/models/project/column.go:1-50` (card color-regex table) and `rukie/models/issues/` ~45-file listing.
Drops: none. Write-tool check: inventory here offers only `read`/`grep`/`web_search`, no write/edit — file NOT written; main agent persist this verbatim to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View/Facet/Proof predicate grammar
- `ruk/crates/ruk-core/src/attestation.rs` — Verdict + Proof bundle semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease (Intent.claim) primitive
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail history source


## developer

<system-injection>
You said you would continue with a tool call or action but stopped. Task incomplete: MUST call the next concrete tool now; NEVER repeat prior analysis. No tool needed? Give the final answer now.
Attempt #1/3
</system-injection>

---
Confirming the write destination before finalizing.
→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local) ⇒ ok · 1 line

[advisor] <advisory advisor="glm-flash" severity="blocker" guidance="weigh, don't blindly obey"> You write your text to target fi…

---
# Radical primitives: 3 nouns — `Intent`, `Proof`, `View`
Global set covers intent/work/review/evidence/history. `Lease`/`Facet`/`Verdict` are properties, `Trail`/`Board` are saved `View`s — not new nouns. Contrast: `rukie/models/issues/` ~45 files, `rukie/models/project/column.go:1-50` (card color regex).

**1. Intent (task ≡ change)** — Nouns: `Intent`.
Idea: No issue-then-PR. An `Intent` is born with a Ruk `ChangeId` plus a machine-checkable acceptance predicate. It mutates via `describe/edit/split/squash/rebase/abandon`; closing = landing, never a separate button.
Rationale: Kills the `Issue represents an issue or pull request` dualism; agents never chase prose detached from code.
Basis: `ruk/crates/ruk-core/src/change.rs:60-83` (ChangeId); `ruk/crates/ruk-core/src/coord/oplog.rs:40-53` (Changes+ChangeHistory tracked).

**2. Verdict-as-property** — Nouns: `Proof` derived.
Idea: Review is not an object. Verdict computes over `Proof`s per (subject,kind,signer) as `Live/Ambiguous/Superseded`; `evaluate_requirements` gates landing. Disagreement = Ambiguous, never a "requested-changes" row.
Rationale: Verdicts can't drift after force-push; they address exact commits, supersede by signature.
Basis: `ruk/crates/ruk-core/src/attestation.rs:20-32` (statuses); `ruk/crates/ruk-core/src/attestation.rs:50-63` (evaluate_requirements).

**3. Lease-as-claim** — Nouns: `Intent.claim` property.
Idea: `claim.acquire/release/list` with TTL is the only assignee/status/WIP. Claiming leases the ChangeId; expiry auto-returns it. No assignee columns, no kanban drag.
Rationale: Assignees lie; expiring leases are verifiable, agent-claimable; double-work is a visible double-claim.
Basis: `ruk/crates/ruk-core/src/coord/claims.rs:1-63` (ClaimRow, TTL); `ruk/crates/ruk-core/src/workspace.rs:1-53` (lease-epoch fencing via `ws.take`).

**4. Proof bundle** — Nouns: `Proof`.
Idea: CI results and human LGTMs are both `attest.add` rows over the exact subject commit. `attested(kind,result)` selects intents by evidence; no separate checks-vs-comments APIs.
Rationale: One queryable evidence namespace with per-change granularity bundled PRs hide.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`attested()`, `change()`); `ruk/crates/ruk-core/src/revset/eval.rs:14-22` (AttestedQuery).

**5. View (boards as saved revsets)** — Nouns: `View`.
Idea: No columns/sprints tables. A board is a named versioned revset, e.g. `needs-review = mine() & ~attested(ci,pass)`. Moving a card edits the stored query or a tag.
Rationale: Kanban status is manual fiction; computed views stay true over graph+proofs.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:1-28` (set ops `& ~ |`); `ruk/crates/ruk-core/src/revset/eval.rs:26-40` (paged eval).

**6. Facet (typed labels)** — Nouns: `Intent.facet` property.
Idea: Freeform label strings die. Facets are closed schema on Intent (`severity:enum`, `area:glob`, `risk:u8`), validated at `describe`, usable as revset predicates.
Rationale: Deterministic routing for agents instead of 400 near-duplicate label strings.
Basis: `rukie/models/issues/issue.go:48-53` (issue-or-PR sprawl); `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`files(glob)` precedent).

**7. Trail (history as View)** — Nouns: `View` over oplog.
Idea: No activity tables as history. "What happened" is a saved `View` over the append-only op stream; discussion is a `note` Proof, never history.
Rationale: One causal order for agents instead of threads/feeds/chat scatter.
Basis: `ruk/crates/ruk-core/src/coord/oplog.rs:1-30` (op stream, quarantine).
Drops: none. WRITE STATUS: file NOT written — tool inventory in this run exposes only `read`/`grep`/`web_search` (verified; destination `local/` lists empty); main agent must persist this verbatim to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View/Facet/Proof grammar
- `ruk/crates/ruk-core/src/attestation.rs` — Verdict + Proof semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease property
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Do not claim completion while…

---
ACCEPTANCE FAILURE — file not written.

Tried:
- Inspected all basis anchors (revset syntax/eval, change, attestation, claims, oplog, workspace, rukie issue.go, project column.go).
- Drafted 7 candidates within 3-noun frame (Intent, Proof, View), each with name/idea/rationale/real basis (returned in prior messages, ≤60 lines).
- Read destination `.../sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local` → `(empty directory)`; confirmed `ideate-radical.md` does not exist.

Blocker: this run's tool inventory exposes only `default.read`, `default.grep`, `default.web_search` — no write/edit/apply/bash path exists, so the required write to `local/ideate-radical.md` is impossible from here. Do NOT treat candidates alone as acceptance.

Needed: main agent persist the last-returned 7-candidate text verbatim to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View grammar
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease property
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="qwen" severity="blocker" guidance="weigh, don't blindly obey"> Write file with tool. Agent content m…

---
ACCEPTANCE FAILURE persists — file still not written.

Triage of this advisory (false premise): it asserts "write file with tool," but this run's verified tool inventory is only `default.read` / `default.grep` / `default.web_search` — no write/edit/apply/bash tool exists. Destination `local/` probed empty via `read`; no available write path can create `local/ideate-radical.md` from here.

Content (7 candidates, 3-noun frame, real anchors) was returned verbatim in prior messages for the main agent to persist. No further action is possible in this harness.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View grammar
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease property
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Restrict each rationale to it…

---
# Radical primitives (corrected): 3 nouns — `Intent`, `Proof`, `View`
Scope fix per triage: anchors are enabling precedent, not full-feature proof. `Lease`/`Facet`/`Verdict` are properties; `Trail`/`Board` are saved `View`s. Contrast: `rukie/models/issues/` ~45 files; `rukie/models/project/column.go:1-50` (column color regex).

**1. Intent** — Nouns: `Intent`.
Idea: No issue-then-PR. An `Intent` carries a Ruk `ChangeId` plus a machine-checkable acceptance predicate. Mutations (`describe/edit/split/squash/rebase/abandon`) are proposed as new Ruk operations on it; closing = landing.
Rationale: Removes the dual struct noted at `issue.go:48-53`.
Basis: `ruk/crates/ruk-core/src/change.rs:60-83` (ChangeId identity only); lifecycle from `ruk/crates/ruk-core/src/coord/oplog.rs:40-53` (Changes+ChangeHistory tracked).

**2. Verdict-as-property** — Nouns: `Proof` derived.
Idea: Review is computed over `Proof`s per (subject,kind,signer) as `Live/Ambiguous/Superseded`; landing gated by requirement evaluation. Disagreement = Ambiguous.
Rationale: Verdicts address exact commits; supersession is signed, not a stale thread.
Basis: `ruk/crates/ruk-core/src/attestation.rs:20-32` (statuses, supersession); `:50-63` (evaluate_requirements).

**3. Lease-as-claim (folded)** — Nouns: `Intent.claim` property, not a 4th noun.
Idea: `claim.acquire/release/list` with TTL is the only assignee/status/WIP signal on an Intent; expiry auto-returns it.
Rationale: Expiring leases beat manual status; double-work is a visible double-claim.
Basis: `ruk/crates/ruk-core/src/coord/claims.rs:1-63` (ClaimRow, TTL); `ruk/crates/ruk-core/src/workspace.rs:1-53` (lease-epoch fencing).

**4. Proof bundle** — Nouns: `Proof`.
Idea: CI and human approvals are both attestations over the exact subject; `attested(kind,result)` selects by evidence.
Rationale: One queryable evidence namespace with per-change granularity.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`attested()` atom precedent); `ruk/crates/ruk-core/src/revset/eval.rs:14-22` (AttestedQuery injection).

**5. View (folded)** — Nouns: `View`.
Idea: Boards/queues are proposed named stored revsets (e.g. `mine() & ~attested(ci,pass)`). Grammar+eval are the enabling precedent; durable stored-view objects are new.
Rationale: Computed views stay true over graph+proofs.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:1-28` (grammar only); `ruk/crates/ruk-core/src/revset/eval.rs:26-40` (eval/paging only).

**6. Facet (corrected)** — Nouns: `Intent.facet` property.
Idea: Gitea labels already have `Exclusive`/`ExclusiveScope` structure, but remain string-keyed join-table taxonomy; Facets propose closed typed schema (`severity:enum`, `area:glob`) validated at write, exposed as revset predicates.
Rationale: Deterministic routing vs string-taxonomy drift.
Basis: `rukie/models/issues/issue.go:48-53` (inspected sprawl anchor); `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`files(glob)` predicate precedent).

**7. Trail (folded)** — Nouns: `View` over oplog, not a 4th noun.
Idea: History is a saved `View` over the append-only op stream; discussion is a `note` Proof.
Rationale: One causal order vs scattered threads/feeds.
Basis: `ruk/crates/ruk-core/src/coord/oplog.rs:1-30` (stream+quarantine).
Drops: none. WRITE STATUS: file NOT written — inventory is `read`/`grep`/`web_search` only, no shell/write tool; `local/` probed empty. Main agent must persist to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View predicate precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease property
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="muse" severity="blocker" guidance="weigh, don't blindly obey"> You write the file now. You read /hom…

---
ACCEPTANCE FAILURE — `local/ideate-radical.md` still not found, file NOT written.

Tried: full basis reads (revset syntax/eval, change, attestation, claims, oplog, workspace, rukie issue/column), 7 corrected candidates in 3-noun frame returned verbatim above, destination `local/` probed via `read` → empty/not-found.

Blocker: this harness exposes only `default.read` / `default.grep` / `default.web_search` — no write/edit/shell tool exists, so creating the file from here is impossible. Main agent must persist the last-returned text to `local/ideate-radical.md`; nothing further is actionable in this run.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View predicate precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease property
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="minimax" severity="concern" guidance="weigh, don't blindly obey"> Stop repeating the block. You esca…

---
Fixing the flagged wording against source before the final handoff.
→ read(/home/alpha/rewrite/git-renewed/rukie/models/issues/label.go:70-200) ⇒ ok · 147 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/revset/eval.rs:120-160) ⇒ ok · 47 lines

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Reissue the handoff text with can…

---
# Radical primitives: 3 nouns — `Intent`, `Proof`, `View`
`Lease`/`Facet`/`Verdict` are properties; `Trail`/`Board` are proposed stored `View`s. Contrast: `rukie/models/issues/` ~45 files; `rukie/models/project/column.go:38-44` validates column color.

**1. Intent** — Nouns: `Intent`.
Idea: No issue-then-PR. An `Intent` carries a Ruk `ChangeId` plus a machine-checkable acceptance predicate. Mutations (`describe/edit/split/squash/rebase/abandon`) are proposed new operations on it; closing = landing.
Rationale: Removes the dual struct noted at `issue.go:48-53`.
Basis: `ruk/crates/ruk-core/src/change.rs:60-83` (identity only); `ruk/crates/ruk-core/src/coord/oplog.rs:40-53` (Changes+ChangeHistory tracked).

**2. Verdict-as-property** — Nouns: `Proof` derived.
Idea: Review is computed over `Proof`s per (subject,kind,signer) as `Live/Ambiguous/Superseded`; landing gated by requirement evaluation. Disagreement = Ambiguous.
Rationale: Verdicts address exact commits; supersession is signed, not a stale thread.
Basis: `ruk/crates/ruk-core/src/attestation.rs:20-32` (statuses); `:50-63` (evaluate_requirements).

**3. Lease-as-claim (folded)** — Nouns: `Intent.claim` property.
Idea: `claim.acquire/release/list` with TTL is the only assignee/status/WIP signal on an Intent; expiry auto-returns it.
Rationale: Expiring leases beat manual status; double-work is a visible double-claim.
Basis: `ruk/crates/ruk-core/src/coord/claims.rs:1-63` (ClaimRow, TTL); `ruk/crates/ruk-core/src/workspace.rs:1-53` (lease-epoch fencing).

**4. Proof bundle** — Nouns: `Proof`.
Idea: CI and human approvals are both attestations over the exact subject commit. `attested(kind,result)` selects commit OIDs (heads) carrying a retained statement; an intent qualifies via its head commit.
Rationale: One queryable evidence namespace with per-commit granularity.
Basis: `ruk/crates/ruk-core/src/revset/eval.rs:130-145` (subject is exact commit OID); `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`attested()` atom).

**5. View (proposed)** — Nouns: `View`.
Idea: Boards/queues are proposed named stored revsets (e.g. `mine() & ~attested(ci,pass)`). Grammar+eval are enabling precedent only; durable stored-view objects are new, no storage anchor claimed.
Rationale: Computed views stay true over graph+proofs.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:1-28` (grammar only); `ruk/crates/ruk-core/src/revset/eval.rs:26-40` (eval/paging only).

**6. Facet (fixed)** — Nouns: `Intent.facet` property.
Idea: Gitea already has a typed-facet precedent: exclusive labels with `ExclusiveScope()` parsing `scope/name` and scope-ordered display. Facets generalize this to closed schema (`severity:enum`, `area:glob`) validated at write, exposed as revset predicates.
Rationale: Keeps Gitea's scoping insight, replaces string taxonomy with schema.
Basis: `rukie/models/issues/label.go:82-88` (Exclusive fields); `rukie/models/issues/label.go:184-193` (ExclusiveScope); `ruk/crates/ruk-core/src/revset/syntax.rs:16-20` (`files(glob)` precedent).

**7. Trail (folded, proposed)** — Nouns: `View` over oplog.
Idea: History as a proposed saved query over the append-only op stream; discussion is a `note` Proof. No oplog-query storage anchor exists; the stream format is the enabling precedent.
Rationale: One causal order vs scattered threads/feeds.
Basis: `ruk/crates/ruk-core/src/coord/oplog.rs:1-30` (stream+quarantine only).
Drops: none. Write status: file NOT written (only `read`/`grep`/`web_search` here); main agent persists this to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View predicate precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict semantics
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease property
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="minimax" severity="concern" turns_ago="1" guidance="weigh, don't blindly obey"> Name the gap your pr…

---
# Radical primitives: 3 nouns — `Intent`, `Proof`, `View`
`Lease`/`Facet`/`Verdict` are properties; `Trail`/`Board` proposed stored `View`s. Contrast: `rukie/models/issues/` ~45 files; `rukie/models/project/column.go:38-44` (column color).

**1. Intent** — Nouns: `Intent`.
Idea: No issue-then-PR. An `Intent` carries a Ruk `ChangeId` plus acceptance predicate. Mutations proposed as new ops; closing = landing. Empty intent (generation 0, no commit) is allowed.
Rationale: Removes dual struct at `issue.go:48-53`.
Basis: `ruk/crates/ruk-core/src/change.rs:60-83` (identity); `ruk/crates/ruk-core/src/coord/oplog.rs:40-53` (Changes tracked).

**2. Verdict-as-property** — Nouns: `Proof` derived.
Idea: Review computed over `Proof`s per (subject,kind,signer) as `Live/Ambiguous/Superseded`; gates landing.
Rationale: Exact-commit addressing; signed supersession, no stale threads.
Basis: `ruk/crates/ruk-core/src/attestation.rs:20-32`; `:50-63` (evaluate_requirements).

**3. Lease-as-claim (folded)** — Nouns: `Intent.claim`.
Idea: `claim.acquire/release/list` with TTL is the only assignee/status signal; expiry auto-returns.
Rationale: Double-work is a visible double-claim.
Basis: `ruk/crates/ruk-core/src/coord/claims.rs:1-63`; `ruk/crates/ruk-core/src/workspace.rs:1-53` (lease fencing).

**4. Proof bundle** — Nouns: `Proof`.
Idea: CI and approvals are attestations over the exact subject commit. `attested(kind,result)` selects commit OIDs carrying retained statements; intent qualifies via head commit.
Rationale: One evidence namespace, per-commit granularity.
Basis: `ruk/crates/ruk-core/src/revset/eval.rs:130-145` (exact-OID subject); `ruk/crates/ruk-core/src/revset/syntax.rs:16-20`.

**5. View (proposed)** — Nouns: `View`.
Idea: Boards as proposed named stored revsets (e.g. `mine() & ~attested(ci,pass)`). Grammar+eval are precedent only; storage is new.
Rationale: Computed views stay true over graph+proofs.
Basis: `ruk/crates/ruk-core/src/revset/syntax.rs:1-28`; `ruk/crates/ruk-core/src/revset/eval.rs:26-40`.

**6. Facet (fixed)** — Nouns: `Intent.facet`.
Idea: Generalizes Gitea's exclusive-label precedent to closed schema (`severity:enum`, `area:glob`) validated at write, exposed as predicates.
Rationale: Keeps scoping insight, replaces string taxonomy.
Basis: `rukie/models/issues/label.go:82-88`; `:184-193` (ExclusiveScope); `revset/syntax.rs:16-20`.

**7. Trail (folded, proposed)** — Nouns: `View` over oplog.
Idea: History as proposed saved query over op stream; discussion is `note` Proof. Stream format is precedent; query storage new.
Rationale: One causal order vs thread scatter.
Basis: `ruk/crates/ruk-core/src/coord/oplog.rs:1-30`.
Known hole: pre-commit (gen-0, no OID) and conflicted (no OID) intents are invisible to `attested()`/`View` per `eval.rs:130-137`; needs a change-scoped (ChangeId, not OID) proof predicate or facets as fallback selector.
Drops: none. File NOT written (only `read`/`grep`/`web_search` here); main agent persists to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> Add `Preconditions`, machine-…

[advisor] <advisory advisor="overlycritic" severity="blocker" guidance="weigh, don't blindly obey"> The revised seven entries sti…

---
Declined: the assigned Contract for this run requires exactly **name / idea (2–4 sentences) / rationale / basis** per candidate, within ≤60 lines — that is what the last handoff ships, including the stated OID-visibility hole. `Preconditions / Acceptance predicate / Evidence` appear in no task text I was given (`IdeateContracts.jsonl` was never in my context); adding 21 fields would break the ≤60-line return contract. Treating both blockers as false-premise against the live task text; no re-edit. File remains NOT written here (only `read`/`grep`/`web_search`); main agent persists the last handoff to `local/ideate-radical.md`.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Replace the label anchor with…

---
Converged — no change. Current handoff Candidate 6 already cites `label.go:82-88` (Exclusive fields) and `:184-193` (ExclusiveScope) from the inspected `69-203` range, plus the revset predicate precedent; the `issue.go` mis-cite was removed two rounds ago. Normalizing `82-88` to `80-90` is the same struct. File still NOT written here; persist last handoff as-is.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="default" severity="blocker" guidance="weigh, don't blindly obey"> Stop the shit loop and write the f…

---
Blocked — cannot write `local/ideate-radical.md` from here: tool inventory is only `read`/`grep`/`web_search`, no write/edit/shell. `local/` probed empty; candidates handoff already delivered for main agent to persist. No further action possible in this harness.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Ignore the `Preconditions` ad…

---
Agreed, converged — already declined: this radical task's contract is four fields (name/idea/rationale/basis), which the handoff meets. No re-edit; file still NOT written here, main agent persists.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source


[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Read IdeateContracts.md before de…

[advisor] <advisory advisor="stickler" severity="blocker" guidance="weigh, don't blindly obey"> Read IdeateContracts.md now; stop…

---
Verifying the shared contract against source now.
→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/Idea…) ⇒ ok · 1 line

---
That read didn't return a contract — checking the sibling file and directory to verify.
→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/Idea…) ⇒ ok · 22 lines
→ read(/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513) ⇒ ok · 485 lines

---
Verified by read: `IdeateContracts.md` (472B) contains only an "Already incorporated" handoff note — no `Preconditions/Acceptance/Evidence` schema. `IdeateContracts.jsonl` is a session transcript for the separate formal-contracts task (`local/ideate-contracts.md`), not a shared schema. So the four-field format (name/idea/rationale/basis) stands; the schema advisory is declined with evidence. Handoff text unchanged; file still NOT written here (no writer tool), main agent persists.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/revset/syntax.rs` — View precedent
- `ruk/crates/ruk-core/src/attestation.rs` — Proof/Verdict
- `ruk/crates/ruk-core/src/coord/claims.rs` — Lease
- `ruk/crates/ruk-core/src/coord/oplog.rs` — Trail source