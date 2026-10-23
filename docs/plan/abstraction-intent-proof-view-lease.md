# Intent, Proof, View, Lease, and the RukBackend seam

Two specifications in one document: the collaboration nouns that replace issues, pull requests, and kanban, plus the human and agent surfaces built on them, and the rukie-to-ruk package boundary. Both are load-bearing for the rukie rewrite. Section 1 owns the product surface; section 2 owns the seam and is verbatim except punctuation fixes required by this document's prose rules.

## 1. Collaboration abstraction: Intent / Proof / View / Lease

### Boundary definition

Four nouns cover intent, work, review, evidence, and history. Everything else in the forge UI is a rendering of one of them, or one of the two supporting objects (Comment anchor, Ask) that exist only attached to a noun.

| Noun | Definition | Replaces |
|---|---|---|
| Intent | A unit of wanted work. Own object in the rukie store: typed acceptance contract, links to zero or more Ruk changes, closed facet schema, reporter, state (open, blocked, closed). Closes when its contract evaluates satisfied and a supervisor confirms. | Issue, task, ticket |
| Proof | A signed attestation. Commit subjects are Ruk `attest.add` statements whose subject is an exact commit OID (`protocol.rs:1776-1784`; `check_subjects` requires a stored commit, `attestation/prepare.rs:139-160`). No `attest.add` call can name a change-id. Subjects with no commit OID are signed rukie-store proofs carrying the same statement schema: `task/acceptance` keyed by (task-id, contract-digest) and conflict evidence keyed by (change-id, conflict-record hash), because conflicted heads never match OID-keyed `attested()` selectors (`revset/eval.rs:134-136`). CI verdicts, human approvals, blocks, and model reviews are the same primitive with different kinds from the closed vocabulary in the proof-kinds section. | CI check, LGTM, review state |
| View | A named, versioned query. Ruk revsets select commits (`revset/syntax.rs:1-28`, `revset/eval.rs:26-40`); a closed predicate set (intent state, facet, lease holder, gap reason) covers rukie data. Boards, queues, backlogs, and agent work-lists are Views. | Kanban board, milestone, saved search |
| Lease | A Ruk claim keyed by intent, covering the intent's declared paths in Enforce or Advise mode. Assignment, WIP limits, and duplicate-work prevention are lease evaluations. | Assignee, WIP column lock |

Settled product decisions resolve three adjudication questions:

- A task (Intent) is its own object linking zero or more changes and closes when its contract holds (settled decision). The `#37` merge of task and change is rejected: it cannot represent tasks with no change or spanning several (adjudication §4-A, §5 question 1 option b).
- Claiming an Intent makes one Ruk claim keyed by the task covering its declared paths, Enforce or Advise (settled decision). `ClaimRecord` has no task or change field today (`protocol.rs:1720-1727`); Ruk gains a task/change key on claims. Until that lands, rukie maps Intent to declared paths itself (design decision, recorded in the leak surface).
- Boards and queues are named saved Views: revsets for commits plus a closed predicate set for task state, facets, claims, and gap reasons (settled decision; §5 question 3 option b). Revset-only Views were rejected because a task with no commit is invisible to `attested()` (`revset/eval.rs:130-135`; adjudication §4-C).

Landing is policy-as-data per protected bookmark (settled decision): default one trusted human `review/approve` plus passing `ci/*` proofs, with rules that may let low-risk facets or paths land on signed CI evidence alone (§5 question 4, blend of options b and c).

Two supporting objects attach to the nouns and have no independent lifecycle:

- Comment anchor: a review comment anchored to (change-id, head, path, line), remapped on rewrite. A rukie store object, not a fifth noun. Specified in its own section.
- Ask: a durable typed question with options, a deadline, and a blocks flag, attached to an Intent, a Lease, or a change. Specified in its own section.

### Surface comparison

| Legacy concept | Abstraction concept |
|---|---|
| Issue | Intent with no linked changes |
| Pull request | Intent with linked changes; review is proofs over each change |
| Kanban column or board | View (saved query) |
| Assignee | Lease holder |
| Label | Typed facet on Intent (closed schema, validated on write; precedent `rukie/models/issues/label.go:84-91,183-193`) |
| CI check (flat pass/fail) | Proof with kind `ci/<job>/<spec_digest>` |
| Approval or LGTM | Proof with kind `review/approve`, signer is a trusted human |
| Milestone or sprint | View filtered by facet or deadline |
| Stale or outdated comment flag | Comment anchor remapped by `review.remap`; staleness derived from diff content, not push booleans (replaces the SHA-bound flag of `review.go:628-640`) |
| Needs-info tag or question | Ask object on the Intent or Lease |
| Next action for an agent | Gap row from the work-list View; gap reasons are first-class rows |
| Merge button | `land.prepare` plus `land.execute` on a frozen single-commit plan, gated by the bookmark's proof policy (hosted execution is currently refused; leak surface) |

### The Intent page and the change page

#### Shared page contract

Every page answers one primary question above the fold and offers exactly one primary action (the only `--accent-strong` button). Secondary actions are links or secondary buttons. Conventions are fixed so an operator learns one grammar:

- Above the fold means within roughly the first 700 px of a 1440 px canvas at Instrument density (base 14 px, data rows 28 px, badge 24 px, nav 48 px): identity line, answer line, action row, and the first data table.
- State is always the Instrument badge triple (icon plus label plus `--*-tint` fill), never color alone, mapped to `live`, `superseded`, `ambiguous`, `enforce`, `advise`, `confirmed`, `rejected`, `uncertain` where protocol enums exist.
- Ops-style tables keep the fixed column order `actor · op · repo · subject · status · age`; numerics are mono `tnum` right-aligned; times are relative mono with absolute `title`.
- `<meta refresh>` appears only on declared live surfaces, clamped at 15 s or more, with a persistent opt-out, and is suppressed on any page containing an editable form (WCAG 2.2.1 / F41; `nojs-interaction-patterns.md` §4).
- Empty states name what creates content ("push to `refs/for/<branch>` to open a change"); error states carry the `E_*` code plus a retry `<form>` POST; `uncertain` and `ambiguous` are rendered states, never flattened (autoplan-design risk decisions).
- Every mutating control is a `<form method="post">` ending in `303` PRG with `_idem` forwarded as the Ruk `request_id` (dedupe via `E_REQUEST_REUSED`, 7-day receipts per `/tmp/ruk-model.json`); every filter is a `<form method="get">` so the URL is the state.
- Capability-degraded rendering: where `read_intents` scopes hide other sessions' provenance or claims, the column renders `restricted`, never silently omits (`capability.rs`; autoplan-design audience decision).

#### Intent page: `/{o}/{r}/task/<id>`

Primary question: does the acceptance contract hold right now, and if not, what is the single next action?

Above the fold:

| Zone | Content |
|---|---|
| Identity line | `task/<id>` title, state badge, reporter `account · agent:<token>` or `account · web`, facets as chips |
| Answer line | Contract evaluation summary in one line: `3 of 5 requirements satisfied`, then `next:` naming the top gap row's clearing op |
| Action row | One primary button by viewer state: `Claim` (radio `enforce` or `advise`, TTL shown, capped by `claim_ttl_seconds=3600`) when unclaimed; `Release claim` when held by the viewer; `Accept task` when evaluation holds (posts `task.accept`, mints a `task/acceptance` proof); otherwise the top gap row's action link |
| First table | Contract requirement rows: `requirement · subject · proof · status · fresh-until`, gap rows interleaved in place |

Below the fold: linked changes table (`change-id · head · description · proofs · gap`), the Lease block (holder, mode, declared paths, expiry countdown, release form), open Asks with their answer forms, comment threads, and the filtered op log.

States: unclaimed intent renders the claim form; claimed shows holder `account · agent:<token>` plus mode plus `wait_until` when the viewer's claim was refused (`E_CLAIMED` fix mapping); contract unsatisfied shows gap rows, not a disabled-button mystery; closed shows the `task/acceptance` proof that closed it.

#### Change page: `/{o}/{r}/change/<id>`

Primary question: can this change land now, and if not, which proof is missing?

Above the fold:

| Zone | Content |
|---|---|
| Identity line | `change/<id>` plus description, author attribution, stack position `2 of 4`, state badge |
| Answer line | Policy evaluation vs the target bookmark: `needs review/approve (trusted human) · ci/build pass fresh until 14:02` |
| Action row | `Submit review` when the viewer holds a pending review (banner `N pending comments`); `Land` shown disabled with its blocking gap text when policy fails, enabled when it holds (honesty rule: never hidden, never dead); authors get the `refs/for/<branch>` push hint as copyable text |
| First table | Per-kind proof summary: `kind · result · signer · head · status · fresh-until`, with `ambiguous` rows always expanded because the signer disagrees with itself and supervision must see it, not average it |

Below the fold: the stacked diff (`?diff=unified|split` link pair, per-file `<details>`, per-line `<details>` comment forms, server-side viewed tracking POST per file, server-held pending review per `nojs-interaction-patterns.md` §2), the full attestation ledger (`live | superseded | ambiguous`), the revision list (each head an exact OID, divergent heads all shown per `ops/describe.rs:3-6`), evergreen comment threads, the `describe` edit form (a mutation; reads go through `show`, `diff`, `attest.list`), and the land section rendering the policy breakdown and, when the change sits in a stack, its serial plan position. The `Execute` control carries the hosted-execution caveat from the branch-view section: on the hosted daemon today it returns a typed refusal, rendered as an error state, never as a receipt.

### Views: compose, save, share, evaluate

The View object (design decision; lives in the rukie store because its predicate set mixes Ruk revsets with rukie data, and stored views are new Ruk work per survivor #41):

```text
view { id, repo_scope (repo | owner | instance), name, version,
       definition { revset?, predicates[] },
       presentation { columns[], sort, group_by? },
       owner, visibility (private | shared), created_at, updated_at }
```

The closed predicate set (settled: task state, facets, claims, gap reasons, plus subject kind and the revset itself):

| Predicate | Domain | Side evaluated |
|---|---|---|
| `revset = "<expr>"` | commits | Ruk (`revset/eval.rs:26-40`) |
| `subject = change | task | branch` | row type | rukie |
| `task.state = open | blocked | closed` | Intents | rukie |
| `facet = <key>:<value>` | Intents | rukie, validated on write |
| `lease.holder = <account>`, `lease.mode = enforce | advise`, `lease.state = held | expired | unclaimed` | claims | Ruk `claim.list` |
| `gap = <reason>` from the gap vocabulary | evaluation layer | rukie over Ruk `Gap` (`attestation/query.rs:59-80`) |
| `ask.state = open | answered | expired` | Asks | rukie |
| `proof.kind = <kind>`, `proof.status = live | superseded | ambiguous` | proofs | Ruk `attest.list` plus the rukie proof store |

Compose, name, save, share without JavaScript:

- `GET /{o}/{r}/view` (also the `/changes`, `/tasks`, `/review` list surfaces) renders one `<form method="get">` whose inputs are the predicate fields plus an `Advanced: revset` input mapping 1:1 to the revset language. Submitting produces `?subject=task&task.state=open&facet=risk:low&revset=...` so the URL is the compiled view: bookmarkable, pasteable to an agent, never an opaque `?saved=7` that hides its filter (`nojs-interaction-patterns.md` §3).
- The results page carries a `Save view` POST form (name, visibility, optional description). Name collision or a revset parse error re-renders with the GOV.UK error summary and preserved input.
- `/{o}/{r}/view/<name>` is the canonical shared URL; `?v=N` pins a version. `view/<name>/edit` is the same GET form prefilled; saving bumps `version` and the header shows `v3`. Sharing a shared view is copying the canonical URL from a `user-select: all` block; private views are visible only to the owner and that account's scoped tokens.
- Sorting is header links toggling `dir`; paging is `?after=` keyset cursors only (offset drifts while agents mutate), emitting `<link rel="next">`, a visible Next link, and a `Link: rel="next"` header. UI cursors are stateless HMAC-signed so they outlive Ruk's 30 s lease-bound backend cursors; `restart_query` re-issues rather than errors.

Agent receipt of the same view: one operation, one result.

- `view.eval { scope, name | definition, cursor }` returns `{columns, rows[], next_cursor, evaluated_at, budget}` where each row is a typed subject: `{subject: "change", change_id, head, ...}` or `{subject: "task", task_id, ...}`.
- MCP exposes it as the `view_eval` tool with the op's input schema; the result is a single tool result per page. CLI is generated: `rukie view eval gaps --json` prints the same `Out` schema. HTML, MCP, and CLI share one operation table and cannot diverge (the inbox-design §6 precedent).
- Evaluation is budgeted: Ruk `ByteBudget` (`budget.rs:6-46`) bounds every read; a partial evaluation returns `next_cursor` and never a truncated row.

Named default views ship per repo (design decision): `gaps` (the work-list), `needs-review` (changes missing `review/approve` the viewer can give), `unclaimed` (intents with no live Lease), `my-claims`. They are ordinary Views: inspectable, forkable, and evaluable by agents identically to user-saved ones.

### The gap work-list

The work-list is a View, not a subsystem: `view.eval` with predicate `gap:*` over the repo. Gap reasons are first-class rows, each carrying the machine-checkable next action.

Gap vocabulary (Ruk-native entries cite `attestation/query.rs:59-80`; rukie additions are marked):

| Gap code | Source | Meaning | Clearing op |
|---|---|---|---|
| `missing_proof:<kind>` | Ruk `Gap::NoLiveMatch` | requirement names a kind with no live matching proof | `review.submit`, `ci.run`, or `task.accept` |
| `proof_expired:<kind>` | Ruk `Gap::Expired` (`query.rs:72-78`) | matching proofs exist but all are signed after the requirement's `valid_until` bound; rukie's `max_age` policy field is expressed as a computed cutoff, not a proof that ages out | re-attest via `attest.add` path |
| `ambiguous:<kind>` | Ruk `Gap::Ambiguous` | same signer produced disagreeing proofs (`attestation.rs:20-26`) | signer posts superseding proof |
| `conflict` | rukie (merge eval) | head is conflicted; the `conflict-v1` record (`conflict.rs:60`) is the reviewable object and its proofs key on the record hash | rebase or resolve |
| `lease_blocked` | rukie (claims) | overlapping Enforce claim held by another session | wait to `wait_until`, or negotiate release |
| `needs_input:<ask-id>` | rukie (asks) | a `blocks`-flagged Ask is unanswered | `ask.answer` |

Row shape, human and agent identical:

```text
{ subject: {kind, id, title}, gap: {code, detail},
  action: {op, args_hint}, blocking: [refs], deadline?, age }
```

The contract: `action.op` names the operation whose success clears the row, and `action.args_hint` carries the arguments the UI would prefill. An agent reads a row and knows the call to make without inference (design decision; the adjudication's #14 asked for the gap reason as the next action, this makes it executable).

Ordering is deterministic, not a heuristic rank: sort by `(severity, deadline asc, age asc)` where severity is `ambiguous > proof_expired > needs_input > conflict > missing_proof > lease_blocked` (design decision).

Surfaces:

- Human: `/{o}/{r}/work` renders the `gaps` View; each row links to the subject page anchored at the offending requirement row. The ops console (`/ops`) reuses the same rows aggregated across repos.
- Agent: `work.next {scope, limit}` is sugar for `view.eval("gaps")` and returns the same row schema; `rukie work next --json` likewise. Rate limits follow the inbox precedent (per-token qps with RFC 9457 `rate-limited` plus `Retry-After`).

### Proof kinds: closed vocabulary and freshness

`AttestKind` and `AttestResult` are bounded free text at the protocol layer (`capability.rs:201`), so rukie pins the vocabulary as a convention: the table below is the closed kind set, and adding a kind is a spec revision plus an `attest` capability allowlist update (`attest` is a closed kind list per principal, `capability.rs`). `AttestStatement` has no predicate-body field (`protocol.rs:1776-1784`); predicate bodies ride inside `result` or inside the kind's digest until the planned schema minor bump (survivor #55 caveat).

Proofs have two storage classes chosen by subject, not by kind (design decision):

| Subject encoding | Stored where | Bound until |
|---|---|---|
| commit OID | Ruk attestation via `attest.add` (`protocol.rs:1776-1784`) | the head rewrites; the OID stops matching automatically |
| `{change_id, conflict_record_hash}` | rukie-store proof, same statement schema | the change's current conflict record differs from the recorded hash |
| `{task_id, contract_digest}` | rukie-store proof, same statement schema | the acceptance contract is edited |

Change-id subjects do not exist in Ruk attestations: `AttestStatement.subject` is an `Oid` and `check_subjects` requires a stored commit (`attestation/prepare.rs:139-160`), so a conflicted head can never carry one (`revset/eval.rs:134-136`). Consequently no kind in the table below is minted through `attest.add` on a non-commit subject; where the subject has no OID, the same kind and result are issued as a rukie-store proof on the encodings above. If Ruk's `AttestStatement` gains a non-commit subject variant in a schema bump, conflict proofs migrate to `attest.add` and this table collapses to one class (named protocol extension).

| Kind | Signer | Result | Subject | Freshness rule |
|---|---|---|---|---|
| `ci/<job>/<spec_digest>` | CI runner key in `ruk-attest` namespace (`attestation/verify.rs:75-95`) | `pass`, `fail`, `error` | exact commit OID | `max_age` per bookmark policy, evaluated as a `valid_until` cutoff computed at check time (`eval_time − max_age`); `spec_digest` pins the job spec so a spec change mints a new kind and old passes never satisfy it |
| `review/approve` | trusted human key | `approved` | exact commit OID (head); against a conflicted head, the same kind is issued as a rukie proof on `{change_id, conflict_record_hash}` | bound to the subject: a rewrite or a changed conflict record stops matching automatically; signer may post a superseding statement |
| `review/block` | trusted human key | `blocked` | exact commit OID (head); conflicted heads use the rukie proof subject above | live until the same signer supersedes it; a live block fails any quorum (`no live block`, survivor #9) |
| `model/review` | account key with `agent:<token>` provenance | `approved`, `changes-requested` | same subject rules as `review/approve` | subject-bound like human review; counts toward quorum only where bookmark policy admits it (default policy does not) |
| `task/acceptance` | supervisor key named by policy | `accepted` | (task-id, contract-digest), stored in the rukie store | bound to the contract digest: editing the acceptance contract invalidates it; optional `valid_until` |

Freshness is four mechanisms, applied in order:

- Subject binding: OID proofs stop matching when a head rewrites, with no bookkeeping. Conflict proofs bind to the record hash, so they survive head moves while the conflict content is identical and stop matching when it changes (content-derived, the same rule as comment anchors; design decision). Task proofs bind to the contract digest.
- Validity bound: `BoundedRequirement.valid_until` (`attestation/query.rs:90-94`) is a signing-time cutoff, the latest `signed_at` a requirement accepts; `Gap::Expired` fires when every matching statement is signed after the bound (`query.rs:72-78`). Proofs do not expire merely because time passes. Rukie's policy layer wraps every requirement in its bound, and a bookmark `max_age` becomes `valid_until = evaluation_time − max_age`, computed fresh at each evaluation. `coord/land.rs:253,387` calls `evaluate_requirements` today, not the bounded variant, so the wrapper lives in rukie.
- Spec pinning: `ci/<job>/<spec_digest>` bakes the job definition into the kind string.
- Supersession and ambiguity: `supersedes` is explicit; same-signer disagreement yields `ambiguous` (`attestation.rs:20-26`), which satisfies nothing and surfaces in the UI expanded.

Rendered status vocabulary is the protocol enum `live | superseded | ambiguous`; `expired` is a rukie evaluation outcome derived from bounds, matching `Gap::Expired`.

Policy-as-data (settled decision): each protected bookmark carries `{requirements: [{kind_pattern, signer_set, quorum, max_age}], overrides: [{facet|path, requirements}]}`. Default is one trusted human `review/approve` plus all required `ci/*` live; an override may drop the human requirement for named low-risk facets or paths where signed CI alone lands. The View's `gate satisfied` column evaluates through the same rukie policy function the landing path consults; today that function is the only enforcement point, because hosted `land.prepare` snapshots an empty requirement set (leak surface).

### The structured ask

The Ask object (survivor #49, narrowed; durable rukie store object):

```text
ask { id, repo, subject: {task | claim | change, ref},
      asker (account · agent:<token>), created_at,
      question, options[]: {key, label} (closed set),
      free_text_allowed, deadline?,
      blocks: none | claim | task,
      state: open | answered | expired | cancelled,
      answer?: {option_key | text, responder, answered_at} }
```

Zero-JS answer form: the Ask renders as a panel on its subject page and on the responder's inbox thread (asks emit `DomainEvent`s through the existing event source; `inbox-design.md` has no such object, adjudication §1.3). The form is `<form method="post" action="/ask/<id>/answer">` with one radio per option, a `<textarea>` enabled only when `free_text_allowed`, the deadline as `<time datetime>`, and `_idem` plus CSRF per the forms contract. Answered asks render the option chip plus responder attribution; expired asks render `expired` and may be re-issued by the asker via `ask.create` (new object, history preserved by link).

Blocking semantics are honest about the primitive: Ruk claims cannot pause, so `blocks: claim` is a rukie-level effect. While a `blocks`-ask is open, the subject's `work.next` row is `needs_input:<ask-id>` and the holder's gap list shows only that row; the Lease itself still expires on its TTL. `blocks: task` marks the Intent `blocked`; `blocks: none` is advisory.

Agent round-trip, two channels:

- In-band: MCP 2026-07-28 multi round-trip requests (`InputRequiredResult`, `inputResponses`, `requestState`; modelcontextprotocol.io schema, per the adjudication's correction). When a blocked agent calls the op the Ask is attached to (typically `work.next` or `task.claim`), the server may return `InputRequiredResult` carrying the ask payload and `requestState`; the agent's own client answers via `inputResponses` and the original call resumes. In-band answers reach the agent's own client only.
- Durable: `ask.answer {ask_id, option_key | text}` is the out-of-band op for the supervising human (or a different token) answering later in the web UI; `ask.list`, `ask.create`, `ask.cancel` complete the op set. `view.eval` predicate `ask.state` lists asks for boards.

An answer lands as an attestation only when the Ask's subject is a commit OID (attestation subjects are exact OIDs, `protocol.rs:1776-1784`). On a conflicted change it lands as a rukie proof keyed by the conflict-record hash. On tasks and claims the answer is a store row that a contract may name as a requirement by predicate, not by proof kind.

### Comment anchoring and remap

Covers the adjudication's uncovered gap (§4-F and survivor #1 narrowed): inline review comments that survive rewrites of a change.

Anchor object (rukie store):

```text
comment { id, thread_id, change_id, head, path, line, side: new | old,
          context (up to 3 lines, snapshot at write),
          body, author, created_at,
          anchor_state: live | stale, anchor_history[]: {head, line, at} }
```

Rules:

- Anchors key on `change_id` because change IDs persist across rewrites (`change.rs:60-83`); `head` records which revision the line number is valid on; `side` distinguishes new-file lines from removed lines.
- `review.remap` runs whenever `show` or `diff` observes the change's head move from `H` to `H'` (design decision: lazy, on read, plus an explicit op for batch). For each anchor on `path` it walks the `H..H'` diff.
- Remap outcomes: the line maps to exactly one line, so the anchor moves to `(H', L')` and records history; the content moved inside the file with a unique match, so the anchor re-anchors and flags `moved`; the line was edited, the file was deleted, or no unique mapping exists, so the anchor becomes `stale` at its last position. Nothing is deleted and nothing is silently orphaned; the stored `context` snapshot keeps stale threads readable.
- `anchor_state` is derived per current head and is reversible: a later head that restores the line flips the thread back to `live` (design decision). This is content-derived staleness, replacing the SHA-bound stale flag (`review.go:628-640`) and folding in rejected #24 without its missing primitive.
- Stale threads render collapsed in a `<details>` with an `outdated since <head>` badge at the top of the file's thread list; replying is allowed; a thread is `resolved` only by user action, never derived.
- Threads group comments by anchor; a pending review holds draft comments server-side until `review.submit` publishes them with a verdict, and the verdict mints `review/approve` or `review/block` on the head OID, or on `{change_id, conflict_record_hash}` when the head is conflicted (`nojs-interaction-patterns.md` §2 pending-review pattern).

### The branch view

`/{o}/{r}/branches/<name>` answers what lands, and in what order. It is the PR-mental-model bridge over the change model (autoplan DES-14).

Above the fold: branch name, target bookmark, count of open changes, aggregate evaluation (`2 of 5 changes land-ready`), and the primary action `Prepare next` (enabled only when the bottom unlanded change's policy holds).

The stack table is ordered bottom to top of stack:

| Column | Content |
|---|---|
| position | ordinal in the serial plan sequence |
| change | `change-id` plus one-line description |
| head | exact OID, all divergent heads listed (`ops/describe.rs:3-6`) |
| proofs | per-kind chips from the closed vocabulary |
| gap | top gap row or `land-ready` |
| plan | `not prepared`, `prepared (expires <time>)`, `executed <receipt>`, `refused <E_*>`, `failed <E_*>` |

Landing is presented serially because it is serial: a stack lands as N `land.prepare` plus `land.execute` plans with exactly one candidate each, since the plan's primary is the first member of the sorted candidate set (`coord/land.rs:11-22,125-130`); batch approval was rejected (#23; adjudication §4-D). The honest presentation:

- `Prepare next` opens the dedicated GET confirm page (consequences computed fresh: candidate OID, parent, the 1 h plan expiry) then POSTs `land.prepare`. `Execute` POSTs `land.execute` and on success PRG-returns the per-change receipt.
- Hosted execution caveat (verified, `coord/prepare.rs:296-301`; socket-asserted `daemon/tests.rs:1900-1954`): on the hosted daemon `land.execute` is currently refused pending a prepare/job split, and hosted `land.prepare` ships empty requirements with `policy_generation = 0` (`coord/prepare.rs:3430-3434,3486`). The page renders that refusal as an `E_HOSTED` error state naming the missing split, keeps the plan in `prepared` until its expiry, and never shows a receipt that did not happen. Rukie-side gate evaluation is the enforcement point until the split lands.
- The next row becomes preparable only after the previous executes and the stack re-evaluates; where execution is refused, the sequence is blocked visibly at that row rather than skipped.
- A `land all` submit may drive the sequence server-side, but the ledger always shows each plan separately, stops at the first failed or refused row, and resumes from that row. There is never a single control implying one atomic land.
- Expired plans render `plan expired` with a `prepare again` retry (the `prepare_land_again` fix maps to a button); `E_STALE` re-derives the page; `uncertain` outcomes are first-class statuses, never flattened.

### Escape hatches

| Hatch | Treatment |
|---|---|
| Raw Ruk reads (`show`, `diff`, `blame`, `log`, `query`, `search`) | Exposed passthrough ops |
| Raw revset in any View | Exposed: the revset field is the View expression language itself |
| Unsaved view composition | Exposed: the GET form URL carries full predicate state |
| Admin force-land | Named override `land.adminExecute`, audited, site-admin only |
| Hand-editing proofs | Sealed: correction is a superseding proof |
| Attesting non-commit subjects in Ruk | Sealed: impossible at the protocol layer today (`attestation/prepare.rs:139-160`); the rukie proof store is the only path until a schema bump adds a subject variant |
| Answering asks out of band | Sealed: only `ask.answer` closes an Ask; chat or verbal answers leave it open |
| Deleting comments or anchors | Sealed: anchors transition to `stale`; they are never deleted |

### Leak surface and trade-offs

- Policy evaluation (quorum, freshness, facet rules) lives in rukie although the evidence lives in Ruk. Leak accepted: Ruk keeps `evaluate_requirements` as the authorization primitive (kind and result over trusted signers); rukie composes it with per-bookmark policy data. A View can show `gate satisfied` while `land.prepare` still refuses on policy drift; the View evaluates through the same code path to keep them equal. Callers must know both directions exist, and that the shared path is rukie-side (see the hosted-land row below).
- Hosted landing is partial (verified): `land.prepare` on the hosted path freezes the candidate with empty requirements, empty trusted signers, and `policy_generation = 0` because `PrincipalPolicy` carries no land-requirement fields (`coord/prepare.rs:3430-3434,3486`; a `[land] requirements` policy section is flagged follow-up in-source). `land.execute` is refused on the hosted path pending a prepare/job split (`coord/prepare.rs:296-301`; `daemon/tests.rs:1900-1954`). Consequences: rukie's per-bookmark policy evaluation is currently the only gate enforcement; `land.execute` callers and the branch view must render the typed refusal, not a receipt.
- Conflicts cannot carry Ruk attestations at all: the subject must be a stored commit OID (`attestation/prepare.rs:139-160`) and conflicted heads never match `attested()` (`revset/eval.rs:134-136`). Conflict proofs are rukie-store objects keyed by `{change_id, conflict_record_hash}`; `attest.list` and direct Ruk callers never see them. Any caller evaluating a gate must include the rukie proof store, not only Ruk attestations.
- Views over intents with no commits query the rukie store only; views over commits query Ruk. The View compiler names which side each predicate hits, and `view.eval` rows can mix both.
- Leases are advisory to humans (Advise) unless the intent enforces them; callers must state the mode (`claims.rs:340-365`: Enforce returns `E_CLAIMED`, Advise only warns).
- Task-keyed claims need a Ruk schema change: `ClaimRecord` has no task or change field (`protocol.rs:1720-1727`). Ruk gains the key per the settled decision; until it lands, rukie maps Intent to declared paths itself, so lease semantics differ in enforcement strength between the two stages.
- Prerequisite Ruk bug: `claims::sweep_page` exists (`claims.rs:475-516`) but has no production caller; hosted maintenance sweeps only receipts and sessions (`hosted.rs:53,1254-1255`). Around 1000 unswept claims on one subtree make every acquisition under it fail `E_LIMIT` (per-acquisition `Work` budget `claims.rs:290-296`, expired rows charged before expiry checks `claims.rs:330-343`, `MAX_PAGE_SIZE` 1000 at `claims.rs:75-79` and `protocol.rs:80`). Inference from code, not executed. Task claiming at scale depends on wiring this sweeper; it is a Ruk fix, not a rukie abstraction.
- `task/acceptance`, conflict proofs, Asks, and Comment anchors are rukie-store objects while CI and head-bound review proofs are Ruk attestations: two stores, one evaluation path. Rukie-store proofs are signed with the same ed25519 key material but verified by rukie, not by Ruk's stored-row re-verification read path.
- Comment remap is computed rukie-side over Ruk diffs; Ruk knows nothing about anchors, so `blame`/`diff` clients see no anchor data.
- MCP round-trips deliver an Ask only to the agent's own client (MCP 2026-07-28 MRTR); supervised answering flows through the durable object. A deployment without the web UI loses the supervisor channel but nothing else.
- `proof_expired` and `gate satisfied` can disagree across a restart because bounds are caller-side (`coord/land.rs:253,387` evaluates unbounded today): rukie wraps every requirement in `BoundedRequirement`, but a direct Ruk caller sees the unbounded result.

### Usage contract

- Use the four nouns for all new product surface; Comment anchors and Asks exist only attached to a noun. Bypass only through the raw-read passthrough ops and the View expression language.
- Every surface binds the one operation table: HTML form, MCP tool, and CLI flag share input schema, authorization, validation, and `Out` schema. HTML and JSON cannot diverge; the agent interface is the same operation the human page posts.

[Showing lines 980-1430 of 3934 (49.6KB limit). Use :1431 to continue]
- Every surface binds the one operation table: HTML form, MCP tool, and CLI flag share input schema, authorization, validation, and `Out` schema. HTML and JSON cannot diverge; the agent interface is the same operation the human page posts.
- What the implementer must still understand: proof kinds and results are a rukie-level convention (`AttestKind` is bounded free text, `capability.rs:201`), `valid_until` is a signing-time cutoff not a wall-clock expiry and `max_age` must be converted to a cutoff at evaluation, proof predicate bodies ride inside `result` or a digest until Ruk's `AttestStatement` gains a predicate field (planned schema minor bump, `protocol.rs:1776-1784`), and change-id or record-keyed subjects live in the rukie proof store until a Ruk non-commit subject variant exists.
- Operational facts callers inherit: claim TTL at most `claim_ttl_seconds=3600`; `op.log`, `claim.list`, and `attest.list` cursors are 30 s lease-bound so `restart_query` is normal flow, not an error; mutations dedupe by `request_id` with 7-day receipts; all reads are `ByteBudget`-bounded (`budget.rs:6-46`, `/tmp/ruk-model.json`); hosted `land.execute` is refused until the prepare/job split lands, so landing surfaces must handle the typed refusal.
- The zero-JS contract is mechanical: `Content-Security-Policy: script-src 'none'; form-action 'self'; base-uri 'none'` on every response, so a script tag is a browser-enforced failure, not a convention. Every interaction is a link, a GET form, a POST-to-303 form, or a `<details>` disclosure; `<dialog>` modals, `:target` fake modals, popover confirms, and `accesskey` are banned by the interaction inventory.
- Any page or View that renders a gate verdict evaluates it through the same rukie policy function the landing path consults.

## 2. RukBackend seam (rukie ↔ ruk)

### Boundary definition

One Go package `internal/ruk` hides the entire Ruk surface: the daemon Unix-socket protocol (framing, credit windows, handshake, outcome lookup), the typed v1 methods, the git-service streams (`upload-pack`, `receive-pack`, `upload-archive`), and generated request/response types. No other rukie package parses a frame, a pkt-line, or a pack.

### Escape hatches

- Schema evolution: client pins `SCHEMA_MAJOR/MINOR` and refuses a mismatched daemon at startup (configuration; the paired-binary upgrade path, `ops-design.md` §0).
- Direct `ruk` CLI for one-off operator tasks: documented seam (admin CLI may shell out; the server never does).

### Leak surface

- Reply size: typed replies are bounded by the daemon's 256 KiB initial stream window; large reads must page (`KeysetPage`). Callers must design pagination up front.
- One active stream per connection in the daemon today; the client hides this behind a connection pool: callers see concurrency, the daemon serializes per connection.
- Receive-pack hooks surface as typed `HostPolicy` callbacks + `HookCall`/`HookReply` frames: rukie implements policy; the daemon executes ref application.

### Usage contract

Every feature reaches Ruk through `internal/ruk`. Tests exercise the real daemon (integration build tag); no mock of `internal/ruk` exists.
```

---

## Resolutions this round

- **`valid_until` semantics corrected.** `BoundedRequirement.valid_until` is the latest accepted `signed_at` (`query.rs:90-94`); `Gap::Expired` means every match was signed after the bound (`query.rs:72-78`). `max_age` is now specified as a rukie-side bound converted to `valid_until = eval_time − max_age` at each evaluation; the gap row, kinds table, freshness bullet, and usage contract all carry the corrected semantics. Citation corrected to `:90-94`.
- **Hosted land gap recorded.** New verified leak row: empty requirements/signers and `policy_generation = 0` at `coord/prepare.rs:3430-3434,3486`; `land.execute` refused pending prepare/job split at `coord/prepare.rs:296-301`, socket-asserted at `daemon/tests.rs:1900-1954`. Branch view gained the refusal state (`refused <E_*>` plan status, `E_HOSTED` error rendering, sequence blocks visibly); gate-equality claims now name rukie-side evaluation as the current enforcement point.
- **Nits fixed.** `(ruk ie store)` corrected to `(rukie store)`; `/branch/` renamed `/branches/` per DES-14; title restored to "RukBackend seam" matching the verbatim §2 header.
- **Attribution corrected.** All "user-confirmed" labels replaced with "(settled decision)"; the settled-decisions paragraph no longer claims in-session confirmation.
- **Verdict-mint line** now routes conflicted-head verdicts to the record-hash subject, consistent with the kinds table.

### Critical Files for Implementation

- `ruk/crates/ruk-core/src/coord/prepare.rs` — `land_prepare_parts` ships empty requirements with `policy_generation = 0` (`:3430-3434,3486`) and `land.execute` refuses at `:296-301`: the hosted-land gap the spec now records.
- `ruk/crates/ruk-core/src/attestation/query.rs` — `Gap` enum (`:59-80`), `BoundedRequirement.valid_until` signing-time cutoff (`:90-94`), `evaluate_requirements` (`:266-290`): gap vocabulary and the freshness semantics just corrected.
- `ruk/crates/ruk-core/src/protocol.rs` — `AttestStatement` (`:1776-1784`) Oid-only subject, `ClaimRecord` (`:1720-1727`) missing task/change keys: the two schema gaps routed around.
- `ruk/crates/ruk-core/src/attestation/prepare.rs` — `check_subjects` (`:139-160`) requires stored commits; the reason conflict proofs are rukie-store objects.
- `ruk/crates/ruk-core/src/coord/claims.rs` — `sweep_page` (`:475-516`) with no production caller and the `E_LIMIT` scan path (`:290-296,330-343`): the prerequisite fix for task claiming.

[advisor] <advisory advisor="default" guidance="weigh, don't blindly obey"> Fix two surviving defects before the file write; this…