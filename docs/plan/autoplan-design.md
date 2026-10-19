Advisory fixes are valid on several points: method count (47, not 50 — verified by recount), motion budget missing from the register, WCAG 2.4.13 is AAA not AA (substitute 2.4.7 + 1.4.11), `AttestKind`/`AttestResult` are bounded free-text not enums, DES-20 ordering, the `describe`/`show` verb fix, the AGENTS.md toolchain conflict, and the ledger namespace collision. The "write the file" blockers misread my contract: this session has no write tool (only `read`/`grep`/`web_search`); per the assignment contract the orchestrator saves this message verbatim as `autoplan-design.md`. The corrected complete artifact follows.

```markdown
# Autoplan DESIGN review — rukie human-facing surface

Phase: design (DES). Scope: everything a human sees or touches — the zero-JS
server-rendered UI, information architecture, review and task surfaces,
density for supervising many agents, WCAG 2.2 AA, both color schemes, motion
budget, and design tokens as CSS custom properties with no build tool.
Evaluated against the six-principle register (clarity, impact, audience, risk,
sequencing, reversibility) over the goal, settled decisions, and draft plan
skeleton (Phases R/K/F/U/V).

**ID namespace note.** `ruk/tasks/autoplan-ledger.jsonl` already contains
DES-01..DES-03 with `"phase":"DES"` — those belong to the *Ruk project's own*
autoplan run (merge notices, syntax-merge settings, notebook notices). The
DES-01..DES-21 below are the *rukie-rewrite* namespace, emitted with
`"phase":"design"` as this review specifies; the phase-value mismatch between
the two ledgers (`"DES"` vs `"design"`) is recorded here, not reconciled —
reconcile if the ledgers ever merge.

## Grounding

### Verified evidence
- Ruk protocol: `/tmp/ruk-model.json` — **47 methods**. Defs include
  `ChangeId`, `ChangeHead`, `RevisionRef` (head | change | workspace),
  `AttestStatement{subject,kind,result,signed_at,supersedes}`,
  `AttestationStatus` enum `{live|superseded|ambiguous}`, `ClaimMode` enum
  `{enforce|advise}`, `PushRefStatus` enum `{confirmed|rejected|uncertain}`,
  `DiffReply{view,files}` paged via `PageRequest`. `AttestKind` and
  `AttestResult` are **bounded free text** (non-empty, no control chars, ≤
  control-frame limit) — *not* enums; rukie-level kind/result vocabulary is a
  convention, not a protocol guarantee. Limits: `page_default=100`,
  `page_max=1000`, `claim_ttl_seconds=3600`. `describe` is a *mutation*
  rewriting a change's description; the change-page read path is `show` +
  `diff` + `attest.list`.
- Capability ceiling: `ruk/crates/ruk-core/src/capability.rs` —
  `read_intents.{provenance,claims}` gates reading *other* sessions' private
  provenance/claim details; `attest` is a closed kind list per principal.
- Attestations: `ruk/crates/ruk-core/src/attestation*` — SSHSIG
  (`ssh-ed25519`, namespace `ruk-attest`), exact-OID subjects, explicit
  supersession, `ambiguous` on same-signer disagreement, stored rows
  re-verified on read (corrupt row = quarantine).
- Daemon: `ruk/crates/ruk/src/daemon/` — `InvocationOpen{repo, method}`,
  `CommandOpen`, credit windows, outcome ledger; profile string still
  `"forgejo"` (ipc.rs:643) pending R1.
- Review routing: `ruk-git/src/transport/proc_receive.rs` — `refs/for` and
  `refs/pull/` divert to proc-receive: the stock-git change-creation path.
- Mirrors: `ruk-git/src/transport/mirror.rs` — per-ref `{name, old, new}` +
  `uncertain` outcomes exist on the wire.
- Replaced UI: `rukie/web_src/css/themes/theme-gitea-*.css` proves the
  custom-property theme pattern; `rukie/templates/` (~30 dirs) is the surface
  being deleted. 58 templates use `form-fetch-action` (research doc) —
  handlers answer JSON instead of redirecting, the exact pattern being
  removed. `rukie/templates/repo/view_file.tmpl` already emits `chroma`
  classes — server-side highlighting exists in-tree.
- `rukie/AGENTS.md` is inherited Gitea guidance (tw-* utilities, locales,
  vitest/pnpm/make lint-css toolchain) — see the recorded exception below;
  it carries no binding constraints on the new UI.
- Zero-JS animation coverage: `prefers-reduced-motion` has **zero** matches in
  `rukie/web_src/css` while `modules/animations.css` defines spin/pulse
  keyframes — the rewrite must not reintroduce un-gated animation.

### Sibling artifacts (adopted)
- `local://ideate-supervision.md` — seven supervision proposals; adopted:
  risk-ranked attention queue (sort on ops console), batch landing parade
  (one form, per-change receipts), evergreen review threads (anchor to
  change-id + attestation subject; staleness derived from content, not push
  booleans), explain-this-change card in `<details>`, claim-board render of
  `claim.list` × attestations, supersede radar (overlapping claim paths →
  abandon losers). Parked: auto-land dial (policy feature, F-phase input).
- `local://nojs-interaction-patterns.md` — 10-workflow catalogue; adopted:
  `?hl=N-M` server highlight + `#L` scroll, per-line `<details>` comment
  forms, `?after=` keyset + `Link: rel=next`, meta refresh clamped ≥15 s with
  opt-out (chunked HTML streaming **rejected**: pins workers, unframed on
  HTTP/2), dedicated GET confirm page for destructive actions (popover is
  always non-modal → loses), POST-only undo via `undo`/`op.log`, GOV.UK-style
  error summary + `_idem` form field mapped to Ruk `request_id` (E_REQUEST_REUSED
  dedupe), `/jump` ranked page instead of accesskeys,
  `formaction`/`formtarget=_blank` previews preserving drafts,
  `user-select: all` copy blocks, inbox + Atom feed. Datalist is
  suggestion-only (NVDA+Firefox don't announce it); server-rendered results
  are the real path.
- `local://research-agent-forge-patterns.md` — gaps noted for design inputs:
  `If-Match`/`Idempotency-Key`/`problem+json` absent (Huma covers
  problem+json; `_idem` covers idempotency); chroma highlighting already
  present; Codeberg/SourceHut LLM-content policies are a mirror-target risk
  (noted for mirrors UX copy).

## Decision register

### 1. Clarity

Decisions:
- **Aesthetic direction: "operations ledger."** The signature surface is a
  dense, typographic register of attested operations — the supervision
  console is the signed-in home page, not an admin afterthought. Tables over
  cards, monospace identity columns (OIDs, change-ids, refs, signer
  fingerprints), left-aligned asymmetric layout, tinted neutrals (never pure
  #000/#fff), one restrained accent, fixed status vocabulary. Anti-marketing:
  no hero sections, glassmorphism, gradients, or decorative icon-heads.
- **UI vocabulary = protocol vocabulary where enums exist**: `live`,
  `superseded`, `ambiguous`; `enforce`/`advise`; `confirmed`/`rejected`/
  `uncertain`; `change`, `revision`, `stack`, `land`. Since `AttestKind`/
  `AttestResult` are free text, rukie pins a *conventional* vocabulary —
  kinds `review`, `ci`, `task`; results `approved`, `changes-requested`,
  `pass`, `fail` — rendered as attested statements, not a hidden state
  machine. `describe` (mutation) vs `show`/`diff`/`attest.list` (reads) is
  recorded so forms POST describe while pages read show.
- **Agent attribution**: every actor renders `account · agent:<token-name>`
  or `account · web`; provenance claims (≤32 pairs) render as a provenance
  block on change/task/commit rows. No agent principals → no "bot" badge;
  the token name is the badge.
- **Closed zero-JS interaction inventory** (DES-03): `<details>` disclosure
  and menus; GET-form filtering/sorting/pagination with `?after=` cursors +
  `Link: rel=next`; POST-303-GET mutations with `_idem`→`request_id`;
  dedicated GET confirm pages for destructive actions (no modals, no
  `<dialog>`, no `:target`/`:checked` state tricks, no popover-confirm);
  `user-select: all` / read-only inputs for copyable text; `<datalist>` as
  suggestion-only with server results as the real path; `title` tooltips;
  `?hl=N-M`+`#L` server-side line highlight; `/jump` ranked page instead of
  accesskeys; `formaction`/`formtarget=_blank` draft-preserving previews;
  meta refresh only on declared live surfaces.
- **Motion budget (explicit)**: transitions only — opacity/transform/
  background-color, ≤150 ms, ease-out; **no** keyframe animations on first
  paint; one permitted looping animation (CI spinner glyph, CSS-only);
  everything disabled under `@media (prefers-reduced-motion: reduce)` via a
  global `transition-duration: 0s; animation: none` rule. Scroll is
  `auto` (never `smooth`). This replaces the stock CSS's un-gated
  spin/pulse animations.
- **Attestation display rule**: each statement row shows
  `kind · result · signer fingerprint · signed_at · live|superseded|ambiguous`;
  `ambiguous` always expanded, never collapsed — the signer disagrees with
  itself and supervision must see it, not average it.

Open questions:
- **`<meta refresh>` legality inside "zero-JS"**: markup, not script, but it
  *is* automated behavior. Proposal: inside the contract, clamped ≥15 s,
  always user-toggleable, default-on only for ops console + running-job
  views, and suppressed on any page containing an editable form. Ratify.
- Browser-native date inputs: ugly but zero-JS-correct; acceptable only in
  admin/filter contexts.

### 2. Impact

Decisions:
- **The supervision console is the differentiator**: risk-ranked attention
  queue (sort by diff size × open conflicts × missing attestations × claim
  contention), held claims across repos, running CI, `uncertain`/`rejected`
  mirror backlog, `ambiguous` attestations — one fixed-column table each,
  filterable by actor. "What are my agents doing?" answers in one page.
- **Batch landing parade** adopted: one form listing fully-attested low-risk
  changes, single submit lands the batch through the publication lane's
  group commit with per-change receipts preserved.
- **Evergreen review threads** adopted: comments anchor to change-id +
  attestation subject; `show`/`diff` re-derive per-thread staleness from

[183 more lines in resource. Use :604 to continue]
  attestation subject; `show`/`diff` re-derive per-thread staleness from
  content, so rebases don't orphan feedback the way SHA-bound staleness does.
- Review impact is borne by signed attestations on exact OIDs — stronger
  than PR review, and the branch view is the PR-mental-model bridge; the
  explain-this-change card (`<details>` on the change page) replaces thread
  archaeology.
- **Density is a supervision feature**: ~32 px rows, tabular numerals, ≤5
  chip shapes, fixed column order `actor · op · repo · subject · status ·
  age` across all ops tables.
- The settled cuts shrink the surface: no swagger UI, no packages, no wiki,
  no Actions log-streaming — ~30 templates, not Gitea's ~200.

Open questions:
- **Cross-repo supervision cost**: `claim.list`/`op.log`/`attest.list` are
  per-repo calls. UI assumes ≤100-repo fan-out within one page budget via
  metadata-store caching; larger instances need a shadow index or a
  Ruk-side cross-repo query (R-phase input).
- **"Since my last review"** inter-revision diff needs a per-user
  last-reviewed pointer — metadata-store state, spec in F-phase.

### 3. Audience

Decisions:
- **Primary: supervising humans** (one human, 20–200 agents per the ideate
  sibling) — claim/attestation/CI visibility and intervention (release
  claim, abandon change, cancel job) in ≤2 navigations.
- **Secondary: contributors** pushing via Ruk or stock git
  (`refs/for/<branch>`) — the review surface must not require Ruk knowledge.
- **Tertiary: agent operators reading via browser** — same console, same
  attribution. Agents use CLI/MCP/JSON, never HTML.
- **Capability-degraded rendering**: `read_intents` gates other sessions'
  private provenance/claims; the console renders explicit "restricted"
  markers rather than hiding columns — silent omission is a supervision lie.
- Empty states guide ("push to `refs/for/<branch>` to open a change", with
  clone URL); never "nothing here".

Open questions:
- First-run/zero-user state: admin CLI creates the first user; auth pages
  must handle the empty instance (F-phase auth task).
- Anonymous browsing: proposed explore-only; console is signed-in only.

### 4. Risk

Decisions:
- **No-JS pattern discipline** as designed above; the sibling catalogue's
  streaming rejection and popover verdict are incorporated.
- **Density vs. WCAG 2.2 AA**: 24×24 px target minimum (2.5.8, AA); focus
  *visible* (2.4.7, AA) with indicator ≥3:1 against adjacent colors
  (1.4.11, AA non-text contrast); status never color-only (1.4.1) — glyph +
  text on every chip; error summary links to fields (3.3.1); both schemes
  ≥4.5:1 text contrast (1.4.3) verified mechanically.
- **Auto-refresh risk**: meta refresh restricted to read-only GET surfaces,
  clamped ≥15 s, suppressed when a form is present.
- **Uncertain-state honesty**: `uncertain` push outcomes and daemon-
  unreachable states are first-class statuses, never silently retried or
  flattened; stale renders carry a timestamp.
- **Corruption honesty**: quarantined-store states get a designed
  "repo degraded" banner, not a 500.
- **AGENTS.md exception (recorded)**: the inherited rules (`tw-*` utilities
  implying the Tailwind build, `pnpm exec vitest`, `make lint-css`,
  locale-only-in-en-US) are dead the moment K1 lands — the no-build
  custom-property layer is the design's decided replacement. Record the
  exception in the rewrite so reviewers don't flag hand-written CSS as a
  rules violation.
- Palette risk is low: one accent + 5 status hues per scheme; color-
  deficiency safety via text+shape, not extra palettes.

Open questions:
- **Human review-signing key**: per-user forge-held ed25519 vs. server key +
  "on behalf of" provenance — security trade-off for F-phase auth review.
- **Diff page cost**: `DiffReply` is paged, but 10k-file changes need
  file-level pagination + anchors; budgets are V-phase input.
- **`read_intents` on the UI session itself**: instance-policy decision —
  if the web session lacks provenance rights, even admins see "restricted".

### 5. Sequencing

Decisions:
- Draft Phase U splits into **U0 contract → U1 foundations → U2 supervision
  core → U3 breadth → U4 verify**:
  - U0 (before any template): art direction (DES-01), URL taxonomy (DES-02),
    interaction inventory (DES-03), vocabulary (DES-04), derived-vs-authored
    ratification (DES-05). All low-reversibility — frozen first.
  - U1: tokens + palettes (DES-06, DES-07), type (DES-08), component markup
    contract incl. motion (DES-09), page shell (DES-10), diff engine
    (DES-11) — the diff engine is the hardest shared component and gates
    both browsing and review.
  - U2: ops console (DES-17), inbox (DES-18), change page (DES-13), stack/
    branch views (DES-14), task page (DES-15), CI (DES-16), repo code
    surfaces (DES-12) — each lands with or immediately after its backing
    ops (K2/K4, then F-phase domains).
  - U3: auth/account (DES-19), remaining surfaces (DES-20).
  - U4: AA pass + reduced-motion + forced-colors + no-JS sweep (DES-21).
- **Templates land in the same commit as their ops** — "every commit
  builds" extends to "every op has its surface"; the derived path covers
  the long tail.
- U2 ordering inside: ops console first (differentiator, exercises tables/
  chips/claims hardest), change page second (exercises diff engine +
  attestation ledger).

Open questions: none blocking — sequencing is fully decidable within U-phase.

### 6. Reversibility

Decisions:
- **Freeze-first** (expensive to change): URL taxonomy; status/attestation
  vocabulary incl. the conventional kind/result strings; `account ·
  agent:<token>` attribution; derived-vs-authored contract; semantic token
  names; the closed interaction inventory.
- **Iterate-freely**: palette values, spacing/scale, component styling,
  per-page layout, icon glyphs — pure CSS or template-internal.
- URLs are durable because agents bookmark/share them — taxonomy is the
  UI's public API.
- Zero-JS itself is the irreversible constraint: if a surface ever demands
  script, the escape is a plan revision, not a `<script>` tag — every
  needed pattern exists in the inventory (validated by the sibling
  catalogue against 10 real workflows).

Open questions:
- Token naming (`--fg`/`--surface-2`/`--status-ok`/`--mono` semantic set vs.
  Gitea-style `--color-*`) — proposed in DES-06, locked in U0.
- Third "high-contrast" scheme: not at launch; `forced-colors` +
  `prefers-contrast` media queries cover it (DES-21 verifies).

## DES tasks

- DES-01 — Ratify the "operations ledger" art direction: dense typographic supervision console as signed-in home, tables over cards, monospace identity columns, one accent + 5-status vocabulary, no decorative chrome; every downstream token/template decision depends on it and it is the project's visual identity.
- DES-02 — Fix the URL taxonomy and navigation model: `/owner/repo` tabs `code|changes|branches|tasks|ci|releases|mirrors|settings`, `change/<id>` and `task/<id>` as direct children, global `/explore`, `/inbox`, `/review`, `/ops`, `/-/admin`, `/-/jump`, `GET`-only reads, `?after=` cursor pagination, full filter state in the URL; URLs are the UI's public API — cheapest to freeze before any template exists.
- DES-03 — Publish the closed zero-JS interaction inventory (adopting the sibling catalogue): `<details>` disclosures/menus, GET-form filters + `Link: rel=next`, POST-303-GET with `_idem`→`request_id`, dedicated GET confirm pages for destructive actions (no modals, no `<dialog>`, no popovers, no `:target`/`:checked` state), `user-select: all` copy blocks, `<datalist>` suggestion-only, `?hl=N-M`+`#L` highlight, `formaction`/`formtarget=_blank` previews, meta-refresh ≥15 s opt-in on live surfaces only; prevents per-page re-invention and a11y-damaging CSS tricks.
- DES-04 — Pin the vocabulary: protocol enums verbatim (`live|superseded|ambiguous`, `enforce|advise`, `confirmed|rejected|uncertain`, change/revision/stack/land); conventional attestation kinds/results (`review`/`ci`/`task`, `approved`/`changes-requested`/`pass`/`fail`) since protocol accepts free text; `account · agent:<token>` actor label; `describe`=POST mutation vs `show`/`diff`/`attest.list`=reads recorded; eliminates synonym drift across UI/docs/API.
- DES-05 — Ratify the derived-vs-authored split (plan-level decision, gates U1): op registry derives forms + result views for every op; hand-authored templates for ~15 core surfaces (auth, code, change, stack, task, CI job, ops console, inbox, review queue); both consume identical op data so HTML and JSON cannot diverge; resolves the tension between "one table derives everything" and designed-surface quality.
- DES-06 — Define the token contract: private `--_*` primitives, public semantic tokens (`--fg`, `--fg-muted`, `--surface-1..3`, `--border`, `--accent`, `--status-*`, `--mono`), cascade layers, light/dark via `prefers-color-scheme` + per-user `data-scheme` override and `color-scheme` property; names lock in U0 while values stay iterable.
- DES-07 — Author light and dark palettes: tinted neutrals (no pure #000/#fff), one non-default accent, five status hues, every fg/surface pair ≥4.5:1 verified mechanically in both schemes; status safety via text+shape so no color-deficiency palette variants are needed.
- DES-08 — Set typography: system UI + system monospace stacks only (no webfonts — build-free, no external fetches), five-step scale, `tabular-nums` numeric columns, absolute timestamps + `title`; matches the ledger direction and keeps identity columns scannable.
- DES-09 — Write the component markup contract incl. motion budget: status chips (glyph+text), dense ops tables with fixed column order `actor · op · repo · subject · status · age`, avatars, pagination, GOV.UK-style error panels, provenance/attestation ledger rows, explain-this-change `<details>` card, filter bars, ~24 inline SVG icons (`aria-hidden` default); motion = opacity/transform/color transitions ≤150 ms ease-out, no animation on first paint, one CSS-only CI spinner, global `prefers-reduced-motion` kill.
- DES-10 — Build the page shell: header with global nav + inbox/ops/jump entries, repo tab bar, skip-to-content link, landmarks (`header/nav/main/footer`), `<title>` convention `repo · surface`, degraded-repo banner slot; every page inherits chrome and a11y structure from one shell.
- DES-11 — Build the diff and code-render engine: stacked single-column diff from `DiffReply{files}` pages, file headers with anchors, context-collapse via server links, binary/LFS fallbacks, server-side chroma-class syntax highlighting (already in-tree via view_file heritage; ownership flagged as R-input); hardest component, gates browsing and review.
- DES-12 — Design repo code surfaces: tree listing, file view with blame toggle (`LineRange`), branch/tag pickers as `<details>` menus, clone URL block (smart-HTTP + SSH), commit log, compare/merge-base page, archive download, search results; replaces deleted `templates/repo/` on R2 read methods.
- DES-13 — Design the change page: status header + effective attestation per kind, revision list (each = exact OID), stacked diff with revision-range selector ("since last review"), attestation ledger incl. superseded/ambiguous, evergreen discussion threads anchored to change-id + subject with content-derived staleness, describe-edit form (POST `describe`), land action gated by `evaluate_requirements`; the product's core human loop.
- DES-14 — Design stack and branch views: `/changes` grouped by stack in dependency order with per-change status, `/branches/<name>` grouping a branch's changes (the PR-mental-model bridge), `/review` global queue filtered by attestation requirements awaiting the viewer; makes the Gerrit-like model navigable without Ruk knowledge.
- DES-15 — Design the task surface (claim-board): task list as live `claim.list` × attestation render — columns `unclaimed | claimed (holder+TTL) | attested-done` are derived states not drag targets — task page claim block with holder `account · agent:<token>`, path scope, `enforce|advise`, TTL countdown (3600 s default), claim/release POSTs, linked changes/CI; the agent work-intake surface.
- DES-16 — Design CI surfaces: run list, job page with step table, plain-text log page with opt-in meta-refresh (≥15 s) while running + terminal state on finish (no streaming — rejected), per-repo CI tab, result attestations (`kind:ci`, `pass|fail`) rendered on the subject change; replaces the deleted Actions UI.
- DES-17 — Design the ops console: risk-ranked attention queue (diff size × conflicts × missing attestations × claim contention), batch landing parade form (group commit, per-change receipts), held claims across repos, running CI, mirror `uncertain|rejected` backlog, supersede radar (overlapping claim paths → abandon), `ambiguous` queue — fan-out bounded by metadata-store cache (R-input: cross-repo cost); the surface the product exists for.
- DES-18 — Design inbox + feeds: notification rows (review requested, claim overlap, task claimed-by, CI fail on owned change, mention), read/unread, per-row mark-read POST, repo filter, Atom feeds for inbox/explore; the only push channel since email is cut.
- DES-19 — Design auth/account surfaces: argon2id login + TOTP step-up, OIDC redirect entry, token management listing `token-name · capability scopes · last-used`, profile, user settings incl. per-user scheme + console refresh toggles; token scoping is where humans grant agents their ceilings.
- DES-20 — Design remaining surfaces: org/team pages (members, repo lists, capability grants), mirror settings + status (per-mirror `exact|ff-only`, ref selection `all|default+tags|globs`, per-ref `confirmed|rejected|uncertain` history + LLM-content-policy copy where relevant), releases (tag + attestation), explore/stars/watches/forks, admin console via derived path; completes the ~30-page inventory.
- DES-21 — WCAG 2.2 AA verification pass: contrast sweep both schemes (4.5:1 text per 1.4.3, 3:1 non-text per 1.4.11), focus-visible per 2.4.7, keyboard walk of every core flow (no traps — `<details>` + dedicated confirm pages only), ≥24 px targets per 2.5.8, 400 % reflow, `prefers-reduced-motion` + `forced-colors` runs, error-summary/label audit; AA is a V-phase launch gate verified here on finished surfaces.

## Ledger

```jsonl
{"id":"DES-01","description":"Ratify the \"operations ledger\" art direction: dense typographic supervision console as signed-in home, tables over cards, monospace identity columns, one accent + 5-status vocabulary, no decorative chrome; every downstream token/template decision depends on it and it is the project's visual identity.","phase":"design"}
{"id":"DES-02","description":"Fix the URL taxonomy and navigation model: /owner/repo tabs code|changes|branches|tasks|ci|releases|mirrors|settings, change/<id> and task/<id> as direct children, global /explore, /inbox, /review, /ops, /-/admin, /-/jump, GET-only reads, ?after= cursor pagination, full filter state in the URL; URLs are the UI's public API — cheapest to freeze before any template exists.","phase":"design"}
{"id":"DES-03","description":"Publish the closed zero-JS interaction inventory (adopting the sibling catalogue): <details> disclosures/menus, GET-form filters + Link: rel=next, POST-303-GET with _idem→request_id, dedicated GET confirm pages for destructive actions (no modals, no <dialog>, no popovers, no :target/:checked state), user-select: all copy blocks, <datalist> suggestion-only, ?hl=N-M+#L highlight, formaction/formtarget=_blank previews, meta-refresh >=15 s opt-in on live surfaces only; prevents per-page re-invention and a11y-damaging CSS tricks.","phase":"design"}
{"id":"DES-04","description":"Pin the vocabulary: protocol enums verbatim (live|superseded|ambiguous, enforce|advise, confirmed|rejected|uncertain, change/revision/stack/land); conventional attestation kinds/results (review/ci/task, approved/changes-requested/pass/fail) since protocol accepts free text; account · agent:<token> actor label; describe=POST mutation vs show/diff/attest.list=reads recorded; eliminates synonym drift across UI/docs/API.","phase":"design"}
{"id":"DES-05","description":"Ratify the derived-vs-authored split (plan-level decision, gates U1): op registry derives forms + result views for every op; hand-authored templates for ~15 core surfaces (auth, code, change, stack, task, CI job, ops console, inbox, review queue); both consume identical op data so HTML and JSON cannot diverge; resolves the tension between one-table-derives-everything and designed-surface quality.","phase":"design"}
{"id":"DES-06","description":"Define the token contract: private --_* primitives, public semantic tokens (--fg, --fg-muted, --surface-1..3, --border, --accent, --status-*, --mono), cascade layers, light/dark via prefers-color-scheme + per-user data-scheme override and color-scheme property; names lock in U0 while values stay iterable.","phase":"design"}
{"id":"DES-07","description":"Author light and dark palettes: tinted neutrals (no pure #000/#fff), one non-default accent, five status hues, every fg/surface pair >=4.5:1 verified mechanically in both schemes; status safety via text+shape so no color-deficiency palette variants are needed.","phase":"design"}
{"id":"DES-08","description":"Set typography: system UI + system monospace stacks only (no webfonts — build-free, no external fetches), five-step scale, tabular-nums numeric columns, absolute timestamps + title; matches the ledger direction and keeps identity columns scannable.","phase":"design"}
{"id":"DES-09","description":"Write the component markup contract incl. motion budget: status chips (glyph+text), dense ops tables with fixed column order actor · op · repo · subject · status · age, avatars, pagination, GOV.UK-style error panels, provenance/attestation ledger rows, explain-this-change <details> card, filter bars, ~24 inline SVG icons (aria-hidden default); motion = opacity/transform/color transitions <=150 ms ease-out, no animation on first paint, one CSS-only CI spinner, global prefers-reduced-motion kill.","phase":"design"}
{"id":"DES-10","description":"Build the page shell: header with global nav + inbox/ops/jump entries, repo tab bar, skip-to-content link, landmarks (header/nav/main/footer), <title> convention repo · surface, degraded-repo banner slot; every page inherits chrome and a11y structure from one shell.","phase":"design"}
{"id":"DES-11","description":"Build the diff and code-render engine: stacked single-column diff from DiffReply{files} pages, file headers with anchors, context-collapse via server links, binary/LFS fallbacks, server-side chroma-class syntax highlighting (already in-tree via view_file heritage; ownership flagged as R-input); hardest component, gates browsing and review.","phase":"design"}
{"id":"DES-12","description":"Design repo code surfaces: tree listing, file view with blame toggle (LineRange), branch/tag pickers as <details> menus, clone URL block (smart-HTTP + SSH), commit log, compare/merge-base page, archive download, search results; replaces deleted templates/repo/ on R2 read methods.","phase":"design"}
{"id":"DES-13","description":"Design the change page: status header + effective attestation per kind, revision list (each = exact OID), stacked diff with revision-range selector (since last review), attestation ledger incl. superseded/ambiguous, evergreen discussion threads anchored to change-id + subject with content-derived staleness, describe-edit form (POST describe), land action gated by evaluate_requirements; the product's core human loop.","phase":"design"}
{"id":"DES-14","description":"Design stack and branch views: /changes grouped by stack in dependency order with per-change status, /branches/<name> grouping a branch's changes (the PR-mental-model bridge), /review global queue filtered by attestation requirements awaiting the viewer; makes the Gerrit-like model navigable without Ruk knowledge.","phase":"design"}
{"id":"DES-15","description":"Design the task surface (claim-board): task list as live claim.list × attestation render — columns unclaimed | claimed (holder+TTL) | attested-done are derived states not drag targets — task page claim block with holder account · agent:<token>, path scope, enforce|advise, TTL countdown (3600 s default), claim/release POSTs, linked changes/CI; the agent work-intake surface.","phase":"design"}
{"id":"DES-16","description":"Design CI surfaces: run list, job page with step table, plain-text log page with opt-in meta-refresh (>=15 s) while running + terminal state on finish (no streaming — rejected), per-repo CI tab, result attestations (kind:ci, pass|fail) rendered on the subject change; replaces the deleted Actions UI.","phase":"design"}
{"id":"DES-17","description":"Design the ops console: risk-ranked attention queue (diff size × conflicts × missing attestations × claim contention), batch landing parade form (group commit, per-change receipts), held claims across repos, running CI, mirror uncertain|rejected backlog, supersede radar (overlapping claim paths → abandon), ambiguous queue — fan-out bounded by metadata-store cache (R-input: cross-repo cost); the surface the product exists for.","phase":"design"}
{"id":"DES-18","description":"Design inbox + feeds: notification rows (review requested, claim overlap, task claimed-by, CI fail on owned change, mention), read/unread, per-row mark-read POST, repo filter, Atom feeds for inbox/explore; the only push channel since email is cut.","phase":"design"}
{"id":"DES-19","description":"Design auth/account surfaces: argon2id login + TOTP step-up, OIDC redirect entry, token management listing token-name · capability scopes · last-used, profile, user settings incl. per-user scheme + console refresh toggles; token scoping is where humans grant agents their ceilings.","phase":"design"}
{"id":"DES-20","description":"Design remaining surfaces: org/team pages (members, repo lists, capability grants), mirror settings + status (per-mirror exact|ff-only, ref selection all|default+tags|globs, per-ref confirmed|rejected|uncertain history + LLM-content-policy copy where relevant), releases (tag + attestation), explore/stars/watches/forks, admin console via derived path; completes the ~30-page inventory.","phase":"design"}
{"id":"DES-21","description":"WCAG 2.2 AA verification pass: contrast sweep both schemes (4.5:1 text per 1.4.3, 3:1 non-text per 1.4.11), focus-visible per 2.4.7, keyboard walk of every core flow (no traps — <details> + dedicated confirm pages only), >=24 px targets per 2.5.8, 400 % reflow, prefers-reduced-motion + forced-colors runs, error-summary/label audit; AA is a V-phase launch gate verified here on finished surfaces.","phase":"design"}
```
```

**Persistence note:** this session exposes only `read`, `grep`, and `web_search` — no write tool exists, matching the assignment contract ("Return your COMPLETE artifact (markdown) as your final message … the orchestrator saves it under the file name given in your task"). The document above is the complete `autoplan-design.md` content to persist.

### Critical Files for Implementation

- `/tmp/ruk-model.json` — 47-method protocol schema: enum vocabularies (`AttestationStatus`, `ClaimMode`, `PushRefStatus`), free-text `AttestKind`/`AttestResult`, `RevisionRef`, `DiffReply`/`PageRequest` paging that every status word and surface pins to.
- `ruk/crates/ruk-core/src/capability.rs` — `Capabilities`/`ReadIntents` fields drive the token-scoping UI (DES-19) and the console's capability-degraded "restricted" rendering rule (DES-17).
- `ruk/crates/ruk-git/src/transport/proc_receive.rs` — `refs/for`/`refs/pull/` diversion semantics: how stock-git pushes become the changes the review UI must explain and render.
- `ruk/crates/ruk/src/daemon/mod.rs` — `InvocationOpen`/`CommandOpen`/outcome ledger behind every supervision table; daemon-unreachable states need the designed degraded banner.
- `rukie/AGENTS.md` + `rukie/web_src/css/` — the inherited Tailwind/locale/pnpm rules being superseded by the no-build custom-property layer (recorded exception), and the token-layering precedent to replace.