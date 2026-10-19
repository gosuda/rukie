BLOCKED on file write — corrected content below for the main agent to save. Reason: my tool inventory is `read`/`grep`/`web_search` only; no write/bash exists, so the target path still shows no file. I did not move the task by choice; I lack the tool. All basis anchors below were actually read.

Corrections applied per advisories: (a) candidate 3 scoped to supported facts — `conflict.rs:1-33` verified as framing+identity only (no merge behavior); syntax-aware behavior anchored instead in `merge/syntax/grammar.rs:205-274` + `Cargo.toml:72-91` (both read). (b) new change-keyed candidate marked explicitly NEW — `protocol.rs:1718-1724` verified: `ClaimRecord` holds claim/holder/paths/mode/expires_at, no change identifier, so supersession detection is a gap, not existing behavior. (c) candidate 6 corrected — verified `coord/claims.rs:198-225`: only the holder can release, including expired claims; verified `coord/leases/table.rs:1-12`: lease expiry fires cancel but releases nothing until the guard drops. Bulk-lease expiry does NOT free path claims; crashed agents CAN block repooled work until claim TTL or holder release.

Candidates (reliability at agent scale):

1. **Path Leases** — `claim.acquire` subtree leases: Enforce fails overlap (`E_CLAIMED`), Advise warns (`ClaimAdvisory`); rukie shows live lease maps so agents check-and-claim before editing. Beats silent collisions with a queryable lock table. Basis: `ruk/crates/ruk-core/src/coord/claims.rs:340-356`.
2. **Exact Land Plans** — `land.prepare` freezes target, candidate set, base tip, attestation predicates, 1h TTL; `land.execute` refuses drift (`E_STALE`), consumes plan (no double-land). Queue: speculative prepares, auto re-prepare, never silent re-plan. Basis: `ruk/crates/ruk-core/src/coord/land.rs:1-50`.
3. **Conflicts as reviewable objects (scoped)** — records are framed `conflict-v1` objects with hash identity (`conflict.rs:1-33` — framing only); syntax-aware merge comes from pinned tree-sitter grammars, ABI 13–15 vs 0.27.1. rukie renders each record as claimable unit (sides, grammar region, proposal). Beats hidden rebase states. Basis: `ruk/crates/ruk-core/src/conflict.rs:1-33`, `ruk/crates/ruk-core/src/merge/syntax/grammar.rs:205-274`, `ruk/Cargo.toml:72-91`.
4. **Group-commit lane** — one serialized lane/repo (2ms linger, ≤64 reqs, ≤64MiB), 7-day receipts on `(session, request_id)`, duplicate coalescing, replay returns stored reply. Retries never double-apply. Basis: `ruk/crates/ruk-core/src/coord/lane.rs:4-8`, `ruk/crates/ruk-core/src/coord/receipt.rs:1-7`.
5. **Crash-resume via fenced workspaces + op log** — `WorkspaceFence` (id + expected stamp; holder/epoch before sequence); op log before/after deltas, `undo` inverts retained ops, refuses moved keys (`E_UNDO_CONFLICT`). Dead-agent work listed resumable with provenance. Basis: `ruk/crates/ruk-core/src/coord/lane/candidate.rs:27-36`, `ruk/crates/ruk-core/src/coord/undo.rs:1-21`.
6. **Lease/claim separation + steward reaper (corrected)** — bulk leases (15-min on progress, 30s-silence cancel, 2h cap) cancel authority only; path claims survive until TTL or holder release. rukie must pair short claim TTLs with a session-death steward that releases dead holders' claims, else crashed agents block repool. Basis: `ruk/crates/ruk-core/src/coord/leases/timing.rs:12-24`, `ruk/crates/ruk-core/src/coord/claims.rs:198-225`, `ruk/crates/ruk-core/src/coord/leases/table.rs:1-12`.
7. **Change-keyed claims for supersession (NEW extension)** — Ruk claims key paths+holder only (`protocol.rs:1718-1724`), so duplicate/superseded agent work is invisible. rukie adds a claim→change-id edge: claiming agent attaches target change; supersede/abandon auto-marks dependent claims stale. Beats duplicate agent labor. Basis: `ruk/crates/ruk-core/src/protocol.rs:1718-1724` (gap anchor).
8. **Deficit round-robin admission** — per-class permits, 10ms quantum, 8-quanta cap, predictions settled vs measured CPU/payload. Runaway batches can't starve humans. Basis: `ruk/crates/ruk-core/src/coord/admission/scheduler.rs:1-10`, `ruk/crates/ruk-core/src/coord/admission/scheduler.rs:40-56`.

Dropped: none. 8 candidates, all anchors read-verified; NEW items labeled.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/claims.rs` — path-lease contention + holder-only release
- `ruk/crates/ruk-core/src/coord/land.rs` — exact land plans + drift refusal
- `ruk/crates/ruk-core/src/merge/syntax/grammar.rs` — pinned syntax-merge grammars
- `ruk/crates/ruk-core/src/protocol.rs` — ClaimRecord gap (no change id)
- `ruk/crates/ruk-core/src/coord/leases/table.rs` — lease/claim expiry separation


[advisor] <advisory advisor="minimax" severity="concern" turns_ago="2" guidance="weigh, don't blindly obey"> Frame asks "claims b…

[advisor] <advisory advisor="muse" severity="concern" guidance="weigh, don't blindly obey"> Fix wrong link. You link claim to wro…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Candidate 6 is wrong: bulk leases…

---
## Later corrections from the same agent (apply over the body above)

Verifying the lease-vs-claim separation before correcting candidate 6.
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/claims.rs:1050-1080) ⇒ ok · 42 lines

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Candidate 2: state that a land pl…

---
Verifying all three flagged anchors before issuing corrections.
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/land.rs:8-30) ⇒ ok · 29 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/land.rs:325-350) ⇒ ok · 37 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/conflict.rs:55-65) ⇒ ok · 17 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/Cargo.toml:25-55) ⇒ ok · 40 lines
→ grep(team|per.task|tenant|principal @ /home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/admission/scheduler.rs) ⇒ ok · 1 line

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Stop source reads and revise …

---
BLOCKED on file write — content below is fully revised; main agent must save it. No write/bash tool exists in my inventory (`read`/`grep`/`web_search` only), so the target path still has no file. Not a choice; a capability gap.

Corrections applied (all anchors read-verified):
- Cand 2: plan publishes ONLY the primary candidate (first sorted member) — `land.rs:12-13`, exec "moves the target to the primary candidate" (`land.rs:336-339`). A stack needs sequential per-commit plans, not one plan landing the stack.
- Cand 3: `conflict.rs:59-60` is only `FORMAT_TAG`; syntax-merge proof is the crate pins (`ruk-core/Cargo.toml:24-58`) plus `merge/syntax/grammar.rs:205-274`. Cited accordingly.
- Cand 6: bulk leases do NOT control path claims. Claims expire by own TTL (`expires_at > now`, `claims.rs:340-356`); leases verify listing cursors only (`claims.rs:1062-1066`); release is holder-only incl. expired (`claims.rs:198-225`); lease expiry cancels authority but releases nothing until guard drops (`leases/table.rs:1-12`). Dead-agent reaper is NEW (no code behind it) — labeled.
- Cand 8: scheduler is deficit round-robin OVER REPOSITORIES (`scheduler.rs:1-2`); grep `team|per.task|tenant|principal` in scheduler.rs: no match. Per-team budgets are NEW/rukie-side — labeled.

Candidates (reliability at agent scale):

1. **Path Leases** — `claim.acquire` subtree leases: Enforce fails overlap (`E_CLAIMED`), Advise warns; live lease maps for check-and-claim. Basis: `coord/claims.rs:340-356`.
2. **Exact Land Plans (single-commit)** — freezes target, sorted/dedup candidate set, base tip, predicates, 1h TTL; exec publishes primary candidate only, refuses drift (`E_STALE`), consumes plan. Stack landing = ordered per-commit plans with auto re-prepare. Basis: `coord/land.rs:12-22`, `coord/land.rs:336-353`.
3. **Conflicts as reviewable objects (scoped)** — records are framed `conflict-v1` objects (tag+hash identity); syntax-aware merge via pinned tree-sitter grammars. Renders as claimable units. Basis: `conflict.rs:59-60`, `crates/ruk-core/Cargo.toml:24-58`, `merge/syntax/grammar.rs:205-274`.
4. **Group-commit lane** — one lane/repo (2ms, ≤64 reqs, ≤64MiB), 7-day `(session, request_id)` receipts, coalescing, replay. Basis: `coord/lane.rs:4-8`, `coord/receipt.rs:1-7`.
5. **Crash-resume via fenced workspaces + op log** — `WorkspaceFence` (holder/epoch before sequence); op deltas + `undo`, refuses moved keys. Dead work listed resumable with provenance. Basis: `coord/lane/candidate.rs:27-36`, `coord/undo.rs:1-21`.
6. **Claim-TTL expiry + steward reaper (NEW)** — claims die by THEIR TTL, not leases; expired still need holder release. rukie: short agent claim TTLs + session-death steward releasing dead holders' claims, else repool blocks. Basis: `coord/claims.rs:340-356`, `coord/claims.rs:198-225` (existing); steward NEW.
7. **Change-keyed claims for supersession (NEW)** — `ClaimRecord` = claim/holder/paths/mode/expires_at, no change id, so duplicate work invisible. Add claim→change-id edge; supersede/abandon marks dependents stale. Basis: `protocol.rs:1718-1724` (gap).
8. **Per-repo admission fairness + rukie-side team budgets (split)** — deficit round-robin per repo (10ms quantum, 8-quanta cap); per-team fleet budgets are NEW policy above it. Basis: `coord/admission/scheduler.rs:1-10`, `coord/admission/scheduler.rs:40-56` (existing); team budgets NEW.

Dropped: none. 8 candidates; NEW vs existing labeled throughout.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/claims.rs` — lease semantics + holder-only release
- `ruk/crates/ruk-core/src/coord/land.rs` — single-primary land plans
- `ruk/crates/ruk-core/src/merge/syntax/grammar.rs` — syntax-merge pins
- `ruk/crates/ruk-core/src/protocol.rs` — ClaimRecord gap (no change id)
- `ruk/crates/ruk-core/src/coord/admission/scheduler.rs` — per-repo fairness


[advisor] <advisory advisor="overlycritic" severity="concern" turns_ago="2" guidance="weigh, don't blindly obey"> Make the reaper…

---
BLOCKED on file write (no write/bash tool in inventory) — revised content below for main agent to save.

Correction accepted: `coord/claims.rs:198-225` (read-verified) rejects any releaser whose session differs from the holder (`E_FORBIDDEN`, including expired claims). So the reaper CANNOT call existing holder-only release — candidate 6 revised to a NEW privileged, fenced expiry transition.

Revised candidates (reliability at agent scale):

1. **Path Leases** — `claim.acquire` subtree leases: Enforce fails overlap (`E_CLAIMED`), Advise warns; live lease maps for check-and-claim. Basis: `coord/claims.rs:340-356`.
2. **Exact Land Plans (single-commit)** — freezes target, sorted/dedup set, base tip, predicates, 1h TTL; exec publishes primary candidate only, refuses drift, consumes plan. Stacks = ordered per-commit plans. Basis: `coord/land.rs:12-22`, `coord/land.rs:336-353`.
3. **Conflicts as reviewable objects (scoped)** — framed `conflict-v1` records; syntax-aware merge via pinned grammars. Claimable units. Basis: `conflict.rs:59-60`, `crates/ruk-core/Cargo.toml:24-58`, `merge/syntax/grammar.rs:205-274`.
4. **Group-commit lane** — one lane/repo (2ms, ≤64 reqs, ≤64MiB), 7-day receipts, coalescing, replay. Basis: `coord/lane.rs:4-8`, `coord/receipt.rs:1-7`.
5. **Crash-resume via fenced workspaces + op log** — `WorkspaceFence`; op deltas + `undo`, refuses moved keys. Basis: `coord/lane/candidate.rs:27-36`, `coord/undo.rs:1-21`.
6. **Privileged fenced expiry transition (NEW)** — claims die by TTL but only the holder can release (`coord/claims.rs:198-225` forbids foreign releasers), and lease expiry releases nothing (`leases/table.rs:1-12`). So rukie needs a NEW lane-privileged sweeper: on proven session death + claim TTL elapsed, the lane itself (not any session) emits the removal facts with exact expectations, fenced on the claim row + session-liveness row, emitting an op-log entry for audit. Short agent claim TTLs bound the blockage window. Basis: `coord/claims.rs:198-225`, `coord/claims.rs:340-356`, `coord/leases/table.rs:1-12` (existing constraints); sweeper NEW.
7. **Change-keyed claims for supersession (NEW)** — `ClaimRecord` lacks change id; add claim→change-id edge, supersede/abandon marks dependents stale. Basis: `protocol.rs:1718-1724` (gap).
8. **Per-repo fairness + team budgets (split)** — deficit round-robin per repo; per-team budgets NEW policy above. Basis: `coord/admission/scheduler.rs:1-10`, `:40-56` (existing); team budgets NEW.

Dropped: none. 8 candidates; NEW vs existing labeled.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/claims.rs` — holder-only release forces privileged sweeper
- `ruk/crates/ruk-core/src/coord/land.rs` — single-primary land plans
- `ruk/crates/ruk-core/src/merge/syntax/grammar.rs` — syntax-merge pins
- `ruk/crates/ruk-core/src/protocol.rs` — ClaimRecord gap (no change id)
- `ruk/crates/ruk-core/src/coord/admission/scheduler.rs` — per-repo fairness


[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> Anchor syntax-aware three-way…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Candidate 2 implies stack landing…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Stop re-sending the full candidat…

[advisor] <advisory advisor="overlycritic" severity="concern" guidance="weigh, don't blindly obey"> State that expired claim rows…

[advisor] <advisory advisor="qwen" severity="concern" guidance="weigh, don't blindly obey"> Fix candidate 4 anchor for 7-day clai…

[advisor] <advisory advisor="stickler" severity="concern" guidance="weigh, don't blindly obey"> Candidate 2: state that stack pla…

[advisor] <advisory advisor="glm-flash" severity="blocker" guidance="weigh, don't blindly obey"> Write the file now. File: /home/…

---
Verifying the three newly cited anchors in one pass.
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/merge/syntax/mod.rs:1-30) ⇒ ok · 35 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/land.rs:120-135) ⇒ ok · 26 lines
→ read(/home/alpha/rewrite/git-renewed/ruk/crates/ruk-core/src/coord/rows.rs:50-62) ⇒ ok · 19 lines

---
BLOCKED — file unwritten (no write/bash tool in inventory; target path still not found). Single final send below for main agent to save. All anchors read-verified this run.

1. **Path Leases** — subtree leases: Enforce/`E_CLAIMED` vs Advise/warning; live lease maps. Basis: `coord/claims.rs:340-356`.
2. **Exact Land Plans (serial, single-commit)** — primary = first frozen member (`land.rs:125-130`); exec publishes it only (`land.rs:12-13`); base must equal live tip exactly (`land.rs:14-16`), so stack plans run SERIALLY — a plan prepared ahead refuses `E_STALE` once its predecessor lands; 1h TTL, plan consumed on exec. Basis: `coord/land.rs:12-22`, `coord/land.rs:125-130`, `coord/land.rs:336-353`.
3. **Conflicts as reviewable objects (scoped)** — `conflict.rs:59-60` is tag-only; three-way behavior lives in `merge/syntax/mod.rs:1-22` (syntax-assisted `{base,ours,theirs}` merge, conservative declines); grammars pinned per-language. Basis: `conflict.rs:59-60`, `merge/syntax/mod.rs:1-22`, `merge/syntax/grammar.rs:205-274`, `crates/ruk-core/Cargo.toml:24-58`.
4. **Group-commit lane** — one lane/repo (2ms, ≤64 reqs, ≤64MiB); receipts replay (`receipt.rs:1-7`); 7-day horizon = 168h (`rows.rs:56-57`); coalescing. Basis: `coord/lane.rs:4-8`, `coord/receipt.rs:1-7`, `coord/rows.rs:56-57`.
5. **Crash-resume via fenced workspaces + op log** — `WorkspaceFence`; op deltas + `undo`, refuses moved keys. Basis: `coord/lane/candidate.rs:27-36`, `coord/undo.rs:1-21`.
6. **Privileged fenced expiry transition (NEW)** — expired rows do NOT block (`expires_at > now` gates enforcement, `claims.rs:340-356`); only LIVE rows block repooling, expired rows linger for cleanup; but only the holder can release (`claims.rs:198-225`), lease expiry releases nothing (`leases/table.rs:1-12`). Hence NEW lane-privileged sweeper on proven session death + TTL elapsed, exact expectations, op-logged. Basis: `coord/claims.rs:198-225`, `coord/claims.rs:340-356`, `coord/leases/table.rs:1-12`; sweeper NEW.
7. **Change-keyed claims for supersession (NEW)** — `ClaimRecord` lacks change id; add claim→change-id edge, supersede marks dependents stale. Basis: `protocol.rs:1718-1724` (gap).
8. **Per-repo fairness + team budgets (split)** — deficit round-robin per repo (10ms quantum, 8-quanta cap); per-team budgets NEW above. Basis: `coord/admission/scheduler.rs:1-10`, `:40-56`; team budgets NEW.

### Critical Files for Implementation
- `ruk/crates/ruk-core/src/coord/claims.rs` — TTL gating + holder-only release
- `ruk/crates/ruk-core/src/coord/land.rs` — serial single-primary plans
- `ruk/crates/ruk-core/src/merge/syntax/mod.rs` — three-way merge behavior
- `ruk/crates/ruk-core/src/protocol.rs` — ClaimRecord gap
- `ruk/crates/ruk-core/src/coord/admission/scheduler.rs` — per-repo fairness