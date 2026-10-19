> `IdeateRukNative` is running a newer turn. The output below is from its PREVIOUS run (published 3m1s ago), not the current one. Live progress: `history://IdeateRukNative`; its next result auto-delivers to its owner.TARGET: /home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ideate-ruk-native.md

BLOCKED WRITE: session exposes read/grep/search only — no write tool. Verified target dir exists and is empty via directory read; parent must persist the body below verbatim.

# Ruk-native mechanics — 7 candidates

**1. Change-stack review.** The review unit is a change-id plus its current head OIDs; each change carries per-head evidence, and comments anchor to `(change-id, head)` so rewrites remap instead of orphaning. Ops: `review.open(change)`, `review.pin(head)`, `review.remap` on rewrite.
Rationale: kills PR bundling and force-push comment drift; agents query exactly what to verify per change.
Basis: `ruk/crates/ruk-core/src/change.rs:72` (ChangeId); `/tmp/ruk-model.json:1` methods `new/describe/edit/split/squash/rebase/resolve`.

**2. Claim-task.** A task IS a claim: acquiring path- or change-scoped lease (`enforce`/`advise`, TTL ≤3600s) is assignment; expiry auto-unblocks and overlapping writes fail `E_CLAIMED` with a `wait_until` fix. Ops: `task.claim(paths)`, `task.release`, `task.list(mine|any)`.
Rationale: replaces prose issues plus manual kanban with a store-enforced lease — duplicate agent work becomes impossible, not merely visible.
Basis: `ruk/crates/ruk-core/src/coord/claims.rs:139` (`seal_acquire`), `:253` (`canonical_paths`); `/tmp/ruk-model.json:1` `claim_ttl_seconds:3600`.

**3. Attestation-gated done.** "Done" is a predicate over live attestations (`ci/pass`, `review/approve` per subject OID) evaluated by `evaluate_requirements`; superseded/ambiguous statuses resolve disputes without threads. No status column exists — the gate reads the store.
Rationale: machine-checkable done for agents, still human-readable; CI and review are the same primitive.
Basis: `ruk/crates/ruk-core/src/attestation/query.rs:256` (`attested`), `:285` (`evaluate_requirements`); `ruk/crates/ruk-core/src/protocol.rs:1798` (`AttestationStatus`).

**4. Frozen land-plan approval.** Approving freezes an exact candidate OID + base tip + attestation predicates into a 1-hour plan; drift refuses `E_STALE`, execution consumes the plan and emits an idempotent receipt. Ops: `review.freeze(change)`, `review.execute(plan)`.
Rationale: reviewers approve reviewed bytes, not a moving branch; agents safely retry/execute without re-asking.
Basis: `ruk/crates/ruk-core/src/coord/land.rs:67` (`LAND_PLAN_TTL`), `:346` (`land_execute` exactness).

**5. Scoped review workspace.** Each review mints an isolated workspace with cone `Scope` over the change's paths and a `WorkspaceStamp` fence; `ws.take` assigns a reviewer (epoch bump displaces), `ws.drop` returns it. Reviewer materializes exactly the change.
Rationale: context sharing = materialized scope, not pasted snippets; concurrent reviewers cannot clobber each other.
Basis: `ruk/crates/ruk-core/src/workspace.rs:250` (`Scope`), `:96` (`WorkspaceStamp`), `:599` (`check_successor`).

**6. Op-log timeline with exact undo.** History shown is the op stream over tracked tables (refs, changes, workspaces, pins); a bad landing or review action inverts exactly via `prepare_undo`, refusing `E_UNDO_CONFLICT` if the world moved. Ops: `timeline.show(range)`, `timeline.undo(op)`.
Rationale: replaces scattered threads/CI logs with one replayable, reversible record; mistakes are facts with inverses.
Basis: `ruk/crates/ruk-core/src/coord/oplog.rs:48` (`tracked` census); `ruk/crates/ruk-core/src/coord/undo.rs:67` (`prepare_undo`).

**7. Conflict-record handoff.** A conflicted change is a first-class review object: the `conflict-v1` record (odd-term algebra, not markers) is assigned like a task, and resolution publishes atomically with `expected_terms` CAS. Ops: `review.handoff(conflict)`, `review.resolve(terms+resolution)`.
Rationale: conflicts stop being chat ("can you fix main?") and become addressed, ownable work with atomic close.
Basis: `ruk/crates/ruk-core/src/conflict.rs:367` (`ConflictRecord`); `/tmp/ruk-model.json:1` `bookmark.resolve` (`expected_terms`).