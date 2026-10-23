> `IdeateSupervision` is running a newer turn. The output below is from its PREVIOUS run (published 33.0s ago), not the current one. Live progress: `history://IdeateSupervision`; its next result auto-delivers to its owner.# Ideate: Human Supervision at Scale (rukie)

Frame: one human, 20–200 agents. Zero-JS: server-rendered tables + forms. Replaces PR lists, kanban boards.

## 1. Risk-Ranked Attention Queue
Idea: table sorted by risk score (diff size × path sensitivity + open conflicts + missing attestations + claim contention). Each row is a form: Approve / Defer / Escalate. Header shows daily budget.
Rationale: PR lists demand equal attention per item; ranking spends scarce judgment where agents are most uncertain.
Basis: `rukie/models/issues/review.go:626`; `ruk/crates/ruk-core/src/conflict.rs:367`.

## 2. Batch Landing Parade
Idea: one form lists fully-attested low-risk changes with checkboxes; single submit approves the batch via the publication lane's group commit, each change keeping its own receipt.
Rationale: per-PR buttons don't scale to a fleet; one decision covers a batch while per-change receipts preserve evidence.
Basis: `ruk/crates/ruk-core/src/coord/land.rs:323`; `ruk/crates/ruk-core/src/coord/lane.rs:1`.

## 3. Evergreen Review (change-ids, not SHAs)
Idea: threads anchor to change-id + attestation subject; `show`/`diff` re-renders each thread against current content, deriving staleness per-thread from content, not from a push-triggered boolean.
Rationale: Gitea flags every review stale per push (rows kept, yellow, restored per matching SHA) — coarse and SHA-bound, so rebases orphan context; content-derived staleness keeps feedback placed.
Basis: `rukie/models/issues/review.go:626`, `:633`, `:217`; model `show` verified via jq.

## 4. Explain-This-Change Card
Idea: the `describe`-time description carries a what/why/blast-radius/evidence card linking CI attestations and blame; rendered in `<details>`, no JS, no thread archaeology.
Rationale: one screen per decision replaces hunting across threads, comments, CI logs.
Basis: `ruk/crates/ruk-core/src/protocol.rs:1416` (DescribeArgs); `:1807` (AttestationRecord); model `blame` verified via jq.

## 5. Claim-Board (leases, not columns)
Idea: "board" is a live render of `claim.list` × attestations: Unclaimed / Claimed (holder + TTL) / Attested-done; claim/release/attest are form posts.
Rationale: kanban cards lie (manual drag); lease state is verifiable and TTL expiry auto-returns stalled work.
Basis: `ruk/crates/ruk-core/src/protocol.rs:1709`; `ruk/crates/ruk-core/src/attestation/query.rs:50`.

## 6. Supervision Budget & Auto-Land Dial
Idea: per-team policy form — auto-land rule, escalation thresholds, daily cap; overflow waits or joins the next parade instead of paging the human.
Rationale: attention becomes an explicit auditable resource, not an overflowing inbox.
Basis: `ruk/crates/ruk-core/src/protocol.rs:819` (ClaimTtl); `:1790` (AttestAddArgs).

## 7. Supersede Radar
Idea: `query` + `claim.list` view grouping changes by overlapping claim paths, flagging superseded work; one form abandons losers via `abandon`.
Rationale: duplicate agent work is invisible in PR lists; overlap makes it triageable at a glance.
Basis: `ruk/crates/ruk-core/src/protocol.rs:1718` (ClaimRecord: holder/paths/expires_at); `:1757` (ClaimListArgs); model `query` + `abandon` verified via jq.

---
Handoff (not part of file): BLOCKER — no write tool exposed (read/grep/web_search only), target dir verified empty; please save body above to `ideate-supervision.md`.

### Critical Files for Implementation
- `rukie/models/issues/review.go` — stale/approval semantics replaced
- `rukie/routers/web/repo/pull_review.go` — comment/submit flows obsoleted
- `/tmp/ruk-model.json` — diff/show/blame/query/claim/attest/land/abandon inventory
- `ruk/crates/ruk-core/src/protocol.rs` — Claim/Attest/Describe types
- `ruk/crates/ruk-core/src/coord/lane.rs` — publication lane, group commit, receipts