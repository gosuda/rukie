# review-task-data-model.md: store schema and operation list for intents, proofs surfacing, views, leases, comments, asks, and land policy

Status: design proposal, 2026-10-20. Plan mode artifact; nothing implemented.

Settled inputs (not reopened):

- Collaboration abstraction (`abstraction-intent-proof-view-lease.md` §1): intent is its own object with acceptance contract, links to zero or more Ruk changes, typed facets (closed schema), state open/blocked/closed; closes when the contract evaluates satisfied. Proof is a Ruk attestation with rukie-conventional kinds. View is a named versioned saved query. Lease is a Ruk claim keyed by intent.
- Store engine (`store-engine-design.md` §§6, 7, 8): SQLite behind `internal/store`, one writer goroutine with group commit, closure based `Read`/`Write`, every table has `id INTEGER PRIMARY KEY` plus `public_id TEXT NOT NULL UNIQUE` (26-char ULID, only identifier crossing the boundary), time as INTEGER unix ms with `CHECK (x > 0)`, FTS5 external-content trigram tables with ai/ad/au triggers, outbox pattern for events.
- Op table (`op-table-design.md` §§3, 5, 10): one `Op[In,Out]` row per operation deriving HTTP plus JSON plus MCP plus CLI plus zero-JS HTML. `Kind` is `KindRead` (GET) or `KindMutation` (POST/PUT/PATCH/DELETE). Path is the canonical resource URL in ServeMux syntax. Every op carries exactly one `Scope`.
- Permissions (`permission-model.md` §§8, 9): task family K1..K6, change family C1..C7, bookmark rules G0..G11 with `BookmarkRule` plus `AttestationReq`, decision function `authz.Check` evaluated before every handler.
- Events (`inbox-design.md` §§2, 7, 8; `webhook-design.md` §§3, 4, 6): one `domain_events` source; inbox fan-out runs in the same transaction; webhook dispatch tails `seq` asynchronously.
- Ruk ground truth read in source this session: `ruk/crates/ruk-core/src/attestation/query.rs:285` `evaluate_requirements(store, subject, requirements, trusted, checkpoint)`, `:336` `evaluate_bounded_requirements` (validity-bounded variant; expired matches report `Gap::Expired`), `check_land_policy` (`E_POLICY` when no trusted signer named; `E_LIMIT` over caps); `ruk/crates/ruk-core/src/protocol.rs:1776` `AttestStatement.subject: Oid`, `:1826` `AttestListArgs.subject: Option<Oid>` (attestation subjects are OIDs only; change-ids are never valid lookup keys); `ruk/crates/ruk-core/src/coord/prepare.rs:297-300` rejects `land.execute` on the hosted preparation path with `E_HOSTED` (local leg publishes outside the lane, remote leg lives in transport); `ruk/crates/ruk-core/src/coord/land.rs:31-34` documents the intended execute path (daemon serving `LandExecuteArgs` loads the plan with `load_plan`, refuses `E_STALE` on candidate drift). Hosted-graph reads are unserved on the daemon (`ruk-backend-surface.md` §2.1: only `op.log`, `claim.list`, `attest.list` are served reads; `status`, `log`, `query`, `diff`, `show`, `file`, `blame` fail `E_HOSTED`).

Labels: `[SRC]` read in a cited file this session. `[D]` design decision. `[INFERENCE]` not observed, do not rely on without a spike.

## 1. DDL

Conventions applied from `store-engine-design.md` §§7, 8 without exception:

- Every table: `id INTEGER PRIMARY KEY` (rowid alias, FK target, FTS5 `content_rowid`). `public_id TEXT NOT NULL UNIQUE` (ULID handle for API/MCP/CLI/URLs). No ULID is ever a primary key or an FK target.
- Time columns: `INTEGER NOT NULL CHECK (x > 0)`, unix ms UTC. Domain code binds int64 only, never `time.Time`.
- FKs: `REFERENCES parent(id) ON DELETE ... ON UPDATE CASCADE` with explicit `ON DELETE` per table below. Enforcement is ON for ordinary migrations. Where a child stores `repo_id` beside an intent reference, a composite FK `(intent_id, repo_id)` enforces that both point at the same intent row, so no row can link a repo-7 intent to repo-8 metadata.
- `STRICT` tables. Single-quoted string literals only (`_dqs=0`). JSON columns carry `CHECK (json_valid(x))`.
- Migration files: `internal/store/migrations/NNNN_name.sql`, pattern `^([0-9]{4})_([a-z][a-z0-9_]*)\.sql$`, contiguous versions, no transaction control statements inside files.

Repos are assumed to exist (`repos` table owns `id`; Ruk repo identity lives in `repos.ruk_repo_id TEXT NOT NULL UNIQUE` per `store-engine-design.md` §7.1). Users are assumed to exist (`users` table owns `id`; `auth-design.md` §11).

### 1.1 intents

One row per task object. Linked changes live in `intent_changes` (§1.2), never inline, so zero-change intents are representable.

```sql
CREATE TABLE intents(
  id            INTEGER PRIMARY KEY,
  public_id     TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
  number        INTEGER NOT NULL CHECK (number > 0),
  title         TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  body          TEXT NOT NULL DEFAULT '' CHECK (length(body) <= 65536),
  state         TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','blocked','closed')),
  close_reason  TEXT NULL CHECK (close_reason IN ('completed','duplicate','wontfix','abandoned')),
  reporter_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  assignee_id   INTEGER NULL REFERENCES users(id) ON DELETE SET NULL ON UPDATE CASCADE,
  contract      TEXT NOT NULL DEFAULT '{"requirements":[]}' CHECK (json_valid(contract)),
  lease_mode    TEXT NOT NULL DEFAULT 'advise' CHECK (lease_mode IN ('enforce','advise')),
  claim_paths   TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(claim_paths)),
  version       INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  updated_at    INTEGER NOT NULL CHECK (updated_at > 0),
  closed_at     INTEGER NULL CHECK (closed_at IS NULL OR closed_at > 0),
  UNIQUE(repo_id, number),
  UNIQUE(id, repo_id)
) STRICT;

CREATE INDEX idx_intents_repo_state ON intents(repo_id, state, updated_at DESC, id DESC);
CREATE INDEX idx_intents_assignee ON intents(assignee_id, state) WHERE assignee_id IS NOT NULL;
CREATE INDEX idx_intents_reporter ON intents(reporter_id, repo_id);
```

Notes:

- `number` is the per-repo short number shown in URLs and webhook payloads (`webhook-design.md` §4 rows 8..10 use `task_number`). Assigned by `max(number)+1` inside the `Write` closure; the UNIQUE constraint is the arbiter under contention.
- `contract` stores the acceptance contract exactly as specified in §2. `claim_paths` stores the declared path list (JSON array of repo-relative strings) that `intent.claim` passes to Ruk `claim.acquire`.
- `version` is the optimistic-concurrency token surfaced as the op-table ETag source and `If-Match` input. Every mutation bumps it.
- `UNIQUE(id, repo_id)` is implied by the primary key but declared explicitly so it can serve as the parent key of the composite FKs in §§1.2, 1.4, 1.5. SQLite requires a FK parent key to be a PK or a UNIQUE constraint; without this declaration the composite enforcement below would be rejected at migration time.
- State `blocked` is stored, not derived: an intent becomes `blocked` when a linked ask with `blocks_claim = 1` is open (see §4), or when an operator marks it blocked. The gap work list (§6) reads both the stored state and the computed reasons.

### 1.2 intent_changes

Links between intents and Ruk changes. Change identity is the Ruk change-id string (stable across rewrites), never the commit OID.

```sql
CREATE TABLE intent_changes(
  id            INTEGER PRIMARY KEY,
  public_id     TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
  intent_id     INTEGER NOT NULL,
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
  change_id     TEXT NOT NULL CHECK (length(change_id) BETWEEN 1 AND 128),
  head_oid      TEXT NULL CHECK (head_oid IS NULL OR length(head_oid) = 64),
  relation      TEXT NOT NULL DEFAULT 'implements' CHECK (relation IN ('implements','fixes','relates')),
  added_by      INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  UNIQUE(intent_id, change_id),
  FOREIGN KEY (intent_id, repo_id) REFERENCES intents(id, repo_id)
    ON DELETE CASCADE ON UPDATE CASCADE
) STRICT;

CREATE INDEX idx_intent_changes_intent ON intent_changes(intent_id, id);
CREATE INDEX idx_intent_changes_change ON intent_changes(repo_id, change_id);
```

Notes:

- `head_oid` is a cached observation (lowercase hex SHA-256, 64 chars, nullable for conflicted heads). It is refreshed on read from Ruk; it is never a trust input. Freshness and verdicts always come from live Ruk reads at evaluation time.
- The composite FK `(intent_id, repo_id)` is load-bearing: without it a valid SQLite row could link a repo-7 intent to repo-8 metadata, corrupting view scoping, authorization, and event fan-out. The FK fails the insert when the two disagree.
- Conflicted changes are keyed by `change_id` with `head_oid NULL`. Conflicted heads carry no OID attestation subject (see §2.2 rule 1): they fail every positive requirement and report gap `conflict` until resolved.

### 1.3 facets (schema rows) and intent_facets (values)

Facets are a closed schema: `facets` defines which keys exist per repo; `intent_facets` stores values. Unknown keys are rejected on write. This replaces the Gitea label free-for-all.

```sql
CREATE TABLE facets(
  id            INTEGER PRIMARY KEY,
  public_id     TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
  key           TEXT NOT NULL CHECK (key GLOB '[a-z][a-z0-9_]*' AND length(key) BETWEEN 1 AND 64),
  value_type    TEXT NOT NULL CHECK (value_type IN ('enum','bool','int','text')),
  allowed       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(allowed)),
  required      INTEGER NOT NULL DEFAULT 0 CHECK (required IN (0,1)),
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  UNIQUE(repo_id, key)
) STRICT;

CREATE TABLE intent_facets(
  id            INTEGER PRIMARY KEY,
  intent_id     INTEGER NOT NULL REFERENCES intents(id) ON DELETE CASCADE ON UPDATE CASCADE,
  facet_id      INTEGER NOT NULL REFERENCES facets(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  value         TEXT NOT NULL CHECK (json_valid(value)),
  updated_at    INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE(intent_id, facet_id)
) STRICT;

CREATE INDEX idx_facets_repo ON facets(repo_id, key);
CREATE INDEX idx_intent_facets_intent ON intent_facets(intent_id, facet_id);
CREATE INDEX idx_intent_facets_facet ON intent_facets(facet_id, value);
```

Notes:

- `allowed` is a JSON array of permitted values for `value_type = 'enum'`; empty array for non-enum types. Validation (type check, enum membership, required-key presence) runs in the op handler before the `Write` closure commits.
- `value` is a JSON scalar or small object (numbers, booleans, short strings). Facet values are deliberately not FTS-indexed; view predicates match them by equality.
- `intent_facets` needs no `repo_id`: both joins reach the repo through `intents` and `facets`, and the handler verifies the two parents share the repo before inserting.
- Seed set per repo (`[D]`): `risk` (enum low/medium/high), `area` (enum backend/frontend/docs/infra), `size` (enum xs/s/m/l). Repo admins extend via a later `facet.define` op (not in the §5 list; out of scope for this artifact).

### 1.4 comments

Anchored to `(change-id, head, path, line)`, remapped on rewrite (§3). Task-level comments (no change anchor) are rows with `change_id NULL`. Comments on conflicted changes carry `change_id` with `head_oid NULL`: conflict review threads anchor to the change-id and resolve to an OID when the conflict resolves.

```sql
CREATE TABLE comments(
  id            INTEGER PRIMARY KEY,
  public_id     TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
  intent_id     INTEGER NULL,
  change_id     TEXT NULL CHECK (change_id IS NULL OR length(change_id) BETWEEN 1 AND 128),
  head_oid      TEXT NULL CHECK (head_oid IS NULL OR length(head_oid) = 64),
  path          TEXT NULL CHECK (path IS NULL OR (length(path) BETWEEN 1 AND 1024)),
  line          INTEGER NULL CHECK (line IS NULL OR line > 0),
  line_end      INTEGER NULL CHECK (line_end IS NULL OR line_end >= line),
  remapped_from TEXT NULL CHECK (remapped_from IS NULL OR length(remapped_from) = 64),
  unmatched     INTEGER NOT NULL DEFAULT 0 CHECK (unmatched IN (0,1)),
  body          TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 32768),
  author_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  updated_at    INTEGER NOT NULL CHECK (updated_at > 0),
  CHECK (change_id IS NOT NULL OR head_oid IS NULL),
  CHECK ((change_id IS NULL) OR (path IS NOT NULL AND line IS NOT NULL) OR (path IS NULL AND line IS NULL)),
  FOREIGN KEY (intent_id, repo_id) REFERENCES intents(id, repo_id)
    ON DELETE CASCADE ON UPDATE CASCADE
) STRICT;

CREATE INDEX idx_comments_change ON comments(repo_id, change_id, path, line) WHERE change_id IS NOT NULL;
CREATE INDEX idx_comments_intent ON comments(intent_id, created_at) WHERE intent_id IS NOT NULL;
CREATE INDEX idx_comments_unmatched ON comments(repo_id, change_id) WHERE unmatched = 1;
```

Notes:

- Whole-change comments (review summary on a version) use `path NULL, line NULL` with the change anchor set. Task-only discussion uses `change_id NULL` plus `intent_id`.
- The first CHECK is one-directional by design: a head OID requires a change-id, but a change-id does not require a head. Rows with `change_id` set and `head_oid NULL` are conflict-anchored comments; they keep `(path, line)` as last-known positions against the conflicted change and resolve per §3 step 7. The previous bidirectional form wrongly left conflict review threads with no home.
- The composite FK applies only when `intent_id` is set: SQLite skips FK checks on rows with a NULL member, so change-only comments (`intent_id NULL`) are unaffected while intent-linked comments cannot name a foreign repo's intent.
- `remapped_from` records the previous `head_oid` when §3 moves the anchor; `unmatched = 1` marks the fallback bucket (§3 step 4). It stays NULL for conflict-anchored rows until they first attach to an OID.

### 1.5 asks

Structured agent-to-human questions. An ask optionally blocks the intent's lease (`blocks_claim`); answering it unblocks (§4).

```sql
CREATE TABLE asks(
  id            INTEGER PRIMARY KEY,
  public_id     TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
  intent_id     INTEGER NOT NULL,
  question      TEXT NOT NULL CHECK (length(question) BETWEEN 1 AND 8192),
  options       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(options)),
  blocks_claim  INTEGER NOT NULL DEFAULT 0 CHECK (blocks_claim IN (0,1)),
  state         TEXT NOT NULL DEFAULT 'asked' CHECK (state IN ('asked','answered','expired')),
  answer        TEXT NULL CHECK (answer IS NULL OR length(answer) BETWEEN 1 AND 8192),
  answer_by     INTEGER NULL REFERENCES users(id) ON DELETE SET NULL ON UPDATE CASCADE,
  deadline_ms   INTEGER NOT NULL CHECK (deadline_ms > 0),
  created_by    INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  answered_at   INTEGER NULL CHECK (answered_at IS NULL OR answered_at > 0),
  version       INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  CHECK ((state = 'answered') = (answer IS NOT NULL AND answered_at IS NOT NULL)),
  CHECK ((state IN ('asked','expired')) OR (answer_by IS NOT NULL)),
  FOREIGN KEY (intent_id, repo_id) REFERENCES intents(id, repo_id)
    ON DELETE CASCADE ON UPDATE CASCADE
) STRICT;

CREATE INDEX idx_asks_intent ON asks(intent_id, state, deadline_ms);
CREATE INDEX idx_asks_deadline ON asks(state, deadline_ms) WHERE state = 'asked';
```

Notes:

- `options` is a JSON array of `{id, label}` objects (max 8, labels max 200 chars; enforced in handler). Free-text answers are allowed alongside option picks; `answer` stores either the selected option id plus optional note, or free text.
- `deadline_ms` is an absolute unix-ms deadline, mandatory at create (default 72 h from creation, max 30 d; enforced in handler).
- The composite FK keeps every ask in its intent's repo, so the blocking check in §4 and the inbox fan-out in §7 can trust `asks.repo_id` without rejoining.
- The `CHECK` rows keep `answered` tied to a present answer and timestamp, and keep `asked`/`expired` answer-free at the SQL level. `answer_by` is set on answer; expiry leaves it NULL.

### 1.6 views and view_versions

Named versioned saved queries. The head row names the view and its sharing; each edit appends a `view_versions` row. Running a view executes a pinned version.

```sql
CREATE TABLE views(
  id            INTEGER PRIMARY KEY,
  public_id     TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
  name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  owner_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE ON UPDATE CASCADE,
  shared        INTEGER NOT NULL DEFAULT 0 CHECK (shared IN (0,1)),
  head_version  INTEGER NOT NULL DEFAULT 1 CHECK (head_version > 0),
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  updated_at    INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE(repo_id, owner_id, name)
) STRICT;

CREATE TABLE view_versions(
  id            INTEGER PRIMARY KEY,
  view_id       INTEGER NOT NULL REFERENCES views(id) ON DELETE CASCADE ON UPDATE CASCADE,
  version       INTEGER NOT NULL CHECK (version > 0),
  revset        TEXT NOT NULL DEFAULT '' CHECK (length(revset) <= 65536),
  predicates    TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(predicates)),
  created_by    INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  UNIQUE(view_id, version)
) STRICT;

CREATE INDEX idx_views_repo_shared ON views(repo_id, shared, updated_at DESC);
CREATE INDEX idx_views_owner ON views(owner_id, repo_id);
CREATE INDEX idx_view_versions_view ON view_versions(view_id, version DESC);
```

Notes:

- `revset` is the raw Ruk revset selecting commits (may be empty for intent-only views). `predicates` is the closed predicate JSON object from §5 (`intent.state`, `facet.*`, `lease.holder`, `gap.*`, `assignee`, `updated_since_ms`, `limit`). Unknown predicate keys are rejected at create and at run.
- `head_version` always equals the latest `view_versions.version` for the view; both are bumped in the same `Write` closure. `view.run` accepts an explicit `version` or defaults to the head.

### 1.7 land_policies

Policy-as-data per protected bookmark. One row per rule; the row is the persisted form of `permission-model.md` §8 `BookmarkRule` plus the low-risk auto-land extension from the settled model.

```sql
CREATE TABLE land_policies(
  id                INTEGER PRIMARY KEY,
  public_id         TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
  repo_id           INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
  pattern           TEXT NOT NULL CHECK (length(pattern) BETWEEN 1 AND 256),
  mode              TEXT NOT NULL DEFAULT 'open' CHECK (mode IN ('open','land_only')),
  push_allow        TEXT NOT NULL DEFAULT '{"roles":[],"users":[],"teams":[]}' CHECK (json_valid(push_allow)),
  land_allow        TEXT NOT NULL DEFAULT '{"roles":[],"users":[],"teams":[]}' CHECK (json_valid(land_allow)),
  required          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(required)),
  low_risk_facets   TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(low_risk_facets)),
  low_risk_paths    TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(low_risk_paths)),
  bypass_admin      INTEGER NOT NULL DEFAULT 0 CHECK (bypass_admin IN (0,1)),
  rule_version      INTEGER NOT NULL DEFAULT 1 CHECK (rule_version > 0),
  updated_by        INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
  created_at        INTEGER NOT NULL CHECK (created_at > 0),
  updated_at        INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE(repo_id, pattern)
) STRICT;

CREATE INDEX idx_land_policies_repo ON land_policies(repo_id, pattern);
```

Notes:

- Match order G0 (exact before glob, longer before shorter, lower id first) is evaluated in Go over the repo's rows, not in SQL.
- `required` stores `AttestationReq[]` (`{kind, result, min_signers, trusted}`) verbatim from `permission-model.md` §8.
- `low_risk_facets` (for example `{"risk":"low"}`) plus `low_risk_paths` (glob list, for example `["docs/**"]`) define the settled low-risk lane: when the intent's facets match AND every changed path matches, `land.prepare` accepts signed CI evidence alone without a human approval requirement. The human approval requirement is otherwise the default: one trusted human `review/approve` plus passing CI. `low_risk_*` never exempts CI and never lowers the §9 role floor.
- `rule_version` bumps on every edit; `land.prepare` records the version it evaluated so drift between view display and land decision is auditable.

### 1.8 FTS5 trigram tables for intent title and body search

Follows `store-engine-design.md` §8 exactly: external-content table, `content_rowid = id`, `tokenize = 'trigram case_sensitive 0'`, three triggers, admin `rebuild` and `integrity-check` commands, single-quoted literals.

```sql
CREATE VIRTUAL TABLE intents_fts USING fts5(
  title, body,
  content = 'intents', content_rowid = 'id',
  tokenize = 'trigram case_sensitive 0'
);

CREATE TRIGGER intents_ai AFTER INSERT ON intents BEGIN
  INSERT INTO intents_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER intents_ad AFTER DELETE ON intents BEGIN
  INSERT INTO intents_fts(intents_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
END;
CREATE TRIGGER intents_au AFTER UPDATE OF title, body ON intents BEGIN
  INSERT INTO intents_fts(intents_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
  INSERT INTO intents_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
```

Search query construction reuses the store-engine helper shape (`searchTerms` split on Unicode whitespace, max 8 terms, terms with 3 or more runes as FTS5 phrases, shorter terms as `instr()` predicates; `lower()` ASCII-only limitation documented):

```sql
-- long terms present: FTS5 plus repo scope plus short-term predicates
SELECT t.id, t.public_id, t.repo_id, t.number, t.title, t.state, t.version,
       t.created_at, t.updated_at,
       snippet(intents_fts, 1, '[', ']', '...', 12) AS snip,
       bm25(intents_fts) AS rank
FROM intents_fts
JOIN intents t ON t.id = intents_fts.rowid
WHERE intents_fts MATCH ?1 AND t.repo_id = ?2
ORDER BY rank
LIMIT ?3;
```

Comment bodies and ask questions are not FTS-indexed in v1 (`[D]`; intent title plus body covers the work-list search need; comment search arrives via `change.list` filters if a proven need appears).

## 2. Acceptance contract shape and evaluation function

### 2.1 Contract JSON

Stored in `intents.contract`. One canonical shape:

```json
{
  "requirements": [
    {
      "kind": "ci/backend-tests/9f2c41ab",
      "result": "pass",
      "quorum": 1,
      "valid_until_ms": 1785432100000,
      "trusted_signers": ["role:write"]
    },
    {
      "kind": "review/approve",
      "result": "approved",
      "quorum": 1,
      "valid_until_ms": 1785432100000,
      "trusted_signers": ["team:backend-owners"]
    },
    {
      "kind": "task/acceptance",
      "result": "accepted",
      "quorum": 1,
      "valid_until_ms": null,
      "trusted_signers": ["user:42"]
    }
  ]
}
```

Field rules:

- `kind`: dotted Ruk attestation kind. Convention from the settled model: `ci/<job>/<spec_digest>`, `review/approve`, `review/block`, `model/review`, `task/acceptance`. Ruk treats kind as bounded free text; the convention is enforced in rukie handlers, not in Ruk.
- `result`: required `AttestResult` string (`pass`, `approved`, `accepted`, `blocked`, job-specific results). A `review/block` requirement with result `absent` expresses "no blocking review may exist"; see evaluation rule 5 below.
- `quorum`: minimum count of distinct trusted signers with a live satisfying attestation. Must be 1 or more, max 16 (`[D]` rukie extension; Ruk's own evaluator reports per-requirement satisfied or not over the trusted set, see §2.2).
- `valid_until_ms`: freshness bound (unix ms) selecting the bounded evaluator. A requirement with a bound uses bounded semantics; `null` selects unbounded semantics (`[D]`; null allowed only for `task/acceptance`). The unbounded evaluator never expires a statement, so time checks MUST NOT go through it.
- `trusted_signers`: non-empty list of `role:<name>`, `team:<id>`, or `user:<id>` entries resolved to `SignerFingerprint` values before evaluation. Empty means nobody counts: Ruk returns `E_POLICY` for trustless evaluations (`query.rs:check_land_policy`), and rukie rejects such contracts at write time. Max 64 requirements and 256 trusted signers per evaluation (Ruk plan limits, `ruk-domain-model.md` §2).

Negative requirements (`review/block` with result `absent`) carry `quorum: 1` and count signers holding a live blocking attestation; any 1 such signer fails the contract.

### 2.2 Go evaluation function signature

Ruk provides two evaluators, read in source this session (`ruk/crates/ruk-core/src/attestation/query.rs:285,336`):

```go
// Ruk source, quoted for the composition contract:
pub fn evaluate_requirements(
    store: &Store,
    subject: &Oid,
    requirements: &[AttestRequirement],
    trusted: &[SignerFingerprint],
    checkpoint: &Checkpoint<'_>,
) -> Result<LandEvaluation, CoreError>
// Never expires a statement; time checks MUST NOT use this form.

pub fn evaluate_bounded_requirements(
    store: &Store,
    subject: &Oid,
    requirements: &[BoundedRequirement], // requirement plus valid_until
    trusted: &[SignerFingerprint],
    checkpoint: &Checkpoint<'_>,
) -> Result<LandEvaluation, CoreError>
// A live match counts only when signed_at is at or before valid_until;
// otherwise the requirement reports Gap::Expired.
```

rukie is Go and cannot link the Rust evaluators; it reaches Ruk through the `internal/ruk` daemon client. `EvalAcceptance` therefore takes an explicit evaluator adapter: the adapter loads live facts through served Ruk calls and applies the same per-requirement semantics (bounded form when `valid_until_ms` is set, unbounded form only when null). Using unbounded semantics for a timed requirement ignores expiry and lets old CI pass as fresh; this routing is load-bearing, not stylistic. Hosted `land.prepare` ships empty requirements today (`ruk-domain-model.md` §6 gap 1; `permission-model.md` §8: "rukie stores the rule, Ruk evaluates the facts"). Until Ruk serves policyful prepare, rukie evaluates through the adapter locally; when the daemon serves policyful prepare, the same adapter delegates without changing callers.

```go
// Evaluator loads the live facts an evaluation needs through the Ruk client.
// Implementations MUST surface Ruk and transport failures as errors, never
// as an unsatisfied verdict: attest.list failure, E_POLICY, E_LIMIT, and
// ctx cancellation are all errors. Only evaluated requirements produce gaps.
type Evaluator interface {
  // LiveAttestations returns Live statements for one subject OID,
  // mirroring Ruk subject_statements (superseded chains excluded).
  LiveAttestations(ctx context.Context, repoPath string, subjectOID string) ([]Attestation, error)
  // ResolveSigners maps contract signer names (role:<n>, team:<id>,
  // user:<id>) to SignerFingerprint values for one repo.
  ResolveSigners(ctx context.Context, repoID int64, names []string) ([]SignerFingerprint, error)
}

// EvalAcceptance evaluates an intent's contract against live Ruk facts
// composed with the bookmark policy row governing the land target.
// It returns an error, never a verdict, when facts cannot be loaded or the
// contract cannot be evaluated: attest.list failure, E_POLICY (trustless or
// over-limit contract slipped past write validation), E_LIMIT, and ctx
// cancellation all propagate as errors. A failed evaluation blocks
// intent.close and land.prepare; it MUST NOT be read as unsatisfied.
func EvalAcceptance(
  ctx context.Context,
  ev Evaluator,
  links []SubjectLink,   // linked (change-id, head OID) pairs; empty OID = conflicted
  c *Contract,           // parsed intents.contract
  p *LandPolicy,         // land_policies row matching the target (nil when unprotected)
  now time.Time,         // evaluation instant; compared against valid_until_ms
) (AcceptanceVerdict, error)
```

Supporting types:

```go
type Requirement struct {
  Kind           string   // e.g. "ci/backend-tests/9f2c41ab"
  Result         string   // required AttestResult, or "absent" for negative requirements
  Quorum         int      // distinct trusted signers, >= 1 ([D] rukie extension over Ruk's satisfied/not)
  ValidUntilMs   *int64   // nil selects unbounded semantics; set selects bounded semantics
  TrustedSigners []Signer // role:<n> | team:<id> | user:<id>, resolved to SignerFingerprint
}

type Contract struct {
  Requirements []Requirement // max 64 (Ruk MAX_LAND_REQUIREMENTS)
}

// SubjectLink names one linked change. HeadOID is empty for conflicted heads.
type SubjectLink struct {
  ChangeID string
  HeadOID  string
}

type AcceptanceVerdict struct {
  Satisfied   bool
  Satisfying  []string      // requirement indices satisfied, as "req[i]:kind=result xN"
  Missing     []GapReason   // one entry per unsatisfied requirement (§6)
  PolicyGen   int64         // land_policies.rule_version evaluated (0 when p == nil)
  EvaluatedAt int64         // unix ms
}
```

Evaluation rules (normative):

1. Per linked subject with a non-empty head OID, load Live attestations via the evaluator (served Ruk `attest.list` with `subject = head OID`). Subjects with an empty head OID (conflicted heads) are never queried by change-id: the list API takes `Option<Oid>` only, so a change-id lookup would be rejected. They fail every positive requirement and yield gap `conflict`. Superseded statements do not count; only the live head of each chain counts (mirrors Ruk `subject_statements` Live reads).
2. A requirement with `valid_until_ms == null` uses unbounded semantics; a requirement with a bound uses bounded semantics: a live match counts only when `signed_at` is at or before the bound and the bound has not passed at `now`. An all-expired match set yields gap `stale_attestation` (Ruk `Gap::Expired`).
3. A requirement is satisfied when at least `quorum` distinct resolved signers hold a counted match. `quorum: 1` reproduces Ruk's own verdict exactly; higher quorums are the rukie extension for multi-reviewer policy.
4. G5 no self-approval: for `kind = review/approve`, the change author's signatures never count toward quorum.
5. G6 exact subject: rebase or a new patch set issues a new OID, so prior attestations stop counting. No carry-over except via a fresh attestation.
6. Negative requirements (`result = absent`, used with `review/block`): satisfied when zero trusted signers hold a live `review/block` attestation on the subject. Any 1 such attestation fails the requirement and yields gap `blocked_review`.
7. Low-risk lane: when the land policy row's `low_risk_facets` matches the intent's facets AND every file in the plan diff matches `low_risk_paths`, requirements of kind `review/approve` are waived for `land.prepare` only if at least one CI requirement is satisfied by signed CI evidence. The waiver is recorded in the verdict (`Waived: ["req[1]:review/approve:low-risk"]` appended to `Satisfying`). CI requirements are never waived.
8. The verdict is per intent across ALL linked subjects: every linked change must satisfy every positive requirement. An intent with zero linked changes can only close via a `task/acceptance` requirement (investigation or ops work with no code).
9. Evaluator errors (attest.list failure, signer-resolution failure, E_POLICY, E_LIMIT, ctx cancellation) return `(AcceptanceVerdict{}, err)`. Callers (`intent.close`, `land.prepare`, `view.run` gap computation) treat error as blocking: close and prepare refuse, and gap rows render `evaluation_error` instead of a gap list. An error MUST NOT be coerced into `Satisfied: false` with per-requirement gaps, which would misreport a transport failure as missing proofs.

`[INFERENCE]` Ruk `AttestStatement` has no predicate field today; predicate bodies ride inside `result` or a digest until the planned schema minor bump (`abstraction-intent-proof-view-lease.md` §1 usage contract). The `ci/<job>/<spec_digest>` kind embeds the spec digest in the kind string for exactly this reason.

## 3. Comment remap rule on change rewrite

When a change is rewritten (rebase, amend, new patch set), every comment anchored to `(change-id, old head)` must resolve to the identical `(path, line)` in the new head or fall into the unmatched bucket. The algorithm performs exact line-identity mapping through the Ruk diff and nothing else: no offset guessing, no nearby-line anchoring, no rename following, no blame tiebreak. A comment that cannot be matched exactly is preserved in the unmatched bucket, never moved to a guessed line.

Inputs: `change_id`, `old_head` OID, `new_head` OID, repo path. Runs inside the `change.updated` event transaction (same `Write` closure that updates `intent_changes.head_oid`).

1. Load all comments with `change_id` and `head_oid = old_head` and `unmatched = 0`, ordered by `(path, line)`.
2. Compute the file diff between the two heads via the Ruk `diff` call with `left = RevisionRef{head: old_head}`, `right = RevisionRef{head: new_head}`, `paths = PathSelection{all}`. While the hosted daemon answers `E_HOSTED` for graph reads, the client issues the same call through `CommandOpen` git-compat verb `git diff old_head new_head --unified=3` and parses unified hunks in Go. The call site is a single function so the swap to the typed method is mechanical. No other Ruk method is involved in the map.
3. From the hunks, build the exact line-identity map per path. A unified diff with 3 context lines omits most unchanged lines from its hunks, and an omitted line is unchanged by definition, so hunk coverage alone is not the map:
   a. Parse every hunk header `@@ -a,b +c,d @@` in order. Context lines inside a hunk map old to new one to one. Added lines map nothing; deleted lines map nothing. Track the cumulative delta (`new_start - old_start` accumulated over prior hunks) for the path.
   b. An old line covered by a hunk maps only when it is a context line, to its identical new line number. A covered line that is deleted or changed maps nothing.
   c. An old line covered by no hunk is unchanged by construction: its new number is the old number plus the cumulative delta of all prior hunks in that path. This is identity arithmetic over unchanged content, not a heuristic.
   d. A path absent from the diff entirely is unchanged: verify it still exists in the new head (via `CommandOpen` `git show new_head:path` existence check, or typed `show`/`file` when served). When it exists, every anchor maps by line identity with delta zero. When it does not exist, the path is gone and its anchors cannot map.
4. For each comment with a file anchor `(path, line)`:
   a. The map yields an identical new line (rules 3b..3d): update `head_oid = new_head`, `line = mapped line`, `remapped_from = old_head`, `unmatched = 0`.

[Showing lines 2943-3518 of 3640 (50.0KB limit). Use :3519 to continue]
   a. The map yields an identical new line (rules 3b..3d): update `head_oid = new_head`, `line = mapped line`, `remapped_from = old_head`, `unmatched = 0`.
   b. Otherwise (old line deleted or changed, path removed, path renamed, or path unverifiable in the new head): keep the last-known `(path, line)`, set `head_oid = new_head`, `remapped_from = old_head`, `unmatched = 1`. Renames are not followed: a comment anchored to a path that no longer exists at that exact path is unmatched by definition.
5. Whole-change comments (`path NULL`) only bump `head_oid` to the new head; they never enter the unmatched bucket.
6. Emit one `domain_events` row of type `change.comment.remapped` per change rewrite carrying `{change_id, old_head, new_head, moved: N, unmatched: M}` so inbox threads can note "M comments need re-anchoring" when `M > 0`.
7. Conflict-anchored rows (`head_oid NULL`, step 4 source of §1.4) are skipped by this algorithm: with no old OID there is no diff to map through. They keep `head_oid NULL` until the conflict resolves to a real head, at which point file-anchored rows attach to the new head with `unmatched = 1` (no diff proves identity across the conflict boundary) and whole-change rows attach with `unmatched = 0`.

## 4. Asks lifecycle and lease unblocking

States: `asked`, `answered`, `expired`. Transitions:

| From | To | Trigger | Effect |
|---|---|---|---|
| — | `asked` | `ask.create` | Intent gains an open ask; if `blocks_claim = 1`, intent state becomes `blocked` and `intent.claim` denies with `lease-hold` reason until resolved |
| `asked` | `answered` | `ask.answer` by a human with `triage` or above on the repo | Answer stored; `answered_at` set; blocking cleared if no other blocking ask is open; intent returns to `open` |
| `asked` | `expired` | Sweeper or lazy on-read promotion when `now > deadline_ms` | Blocking cleared the same way; expiry emits `ask.expired` event; the intent does NOT auto-close |
| `expired` | `asked` | `ask.create` again (new row; expired rows are never resurrected) | Fresh question with a fresh deadline |

Rules:

- Exactly one open (`asked`) blocking ask is enough to block: `intent.claim` checks `SELECT count(*) FROM asks WHERE intent_id = ? AND state = 'asked' AND blocks_claim = 1`; nonzero denies with problem code `lease-hold`.
- Answering is human-only. The answering principal must satisfy `task.edit` (K3, `triage`) on the repo. Agents cannot answer their own asks; the agent that created the ask is recorded in `created_by` for audit.
- An answered ask unblocks the lease in the same `Write` transaction: `ask.answer` updates the ask row, recomputes the open-blocking count, flips `intents.state` from `blocked` back to `open` when the count reaches zero, and emits `ask.answered` plus `intent.reopened` (when the state flips) to `domain_events`. No second call is needed; the next `intent.claim` observes the cleared state.
- Expiry never answers: an expired ask keeps its question for the record but no longer blocks. The worker that flips `asked` to `expired` emits `ask.expired` and applies the same state-recompute as an answer.
- Non-blocking asks (`blocks_claim = 0`, polling a preference or offering options) never touch intent state; they only emit events for inbox delivery.

## 5. Operation list

Path prefix convention from `op-table-design.md` §18: canonical resource URLs, no `/api` prefix. Intent routes nest under the repo: `/{owner}/{repo}/intents`. Scope names follow the op-table closed `Scope` style; the lattice level in parentheses is the `permission-model.md` §6 minimum.

`Backing` names the serving call. Served Ruk typed methods are `claim.acquire`, `claim.release`, `claim.list`, `attest.add`, `attest.list`, `land.prepare`, `op.log` (`ruk-backend-surface.md` §2.1). Graph reads (`show`, `diff`, `blame`, `query`, `file`, `log`, `status`) return `E_HOSTED` on the hosted daemon today, so every op below that needs them routes through the `CommandOpen` git-compat verb in the `Hosted` column until the read seam lands. `land.execute` is refused on the hosted preparation path (`prepare.rs:297-300`, the `uncovered` arm); ops 21 and 22 name it as the intended method and their live path needs the transport-owner job implementation (`land.rs:31-34`). `Backing` SQ means a store query inside the op's `Write` or `Read` closure.

| # | Op name | Kind / method / path | Scope (lattice) | Input fields | Output fields | Backing | Hosted |
|---|---|---|---|---|---|---|---|
| 1 | `intent.create` | Mutation POST `/{owner}/{repo}/intents` | ScopeIntentWrite (`triage`, K1) | `title`, `body`, `assignee?`, `facets{}`, `contract?`, `lease_mode`, `claim_paths[]`, `idempotency_key?` | `intent{public_id, number, state, version}`, `location` | SQ insert intents plus intent_facets; validates facets against facets rows | Served (store only) |
| 2 | `intent.update` | Mutation PATCH `/{owner}/{repo}/intents/{number}` | ScopeIntentWrite (`triage`, K3) | `title?`, `body?`, `assignee?`, `facets{}?`, `contract?`, `lease_mode?`, `claim_paths[]?`, `if_match` (version) | `intent{... version+1}` | SQ update; assignee checked against read floor (`permission-model.md` K3) | Served (store only) |
| 3 | `intent.close` | Mutation POST `/{owner}/{repo}/intents/{number}/close` | ScopeIntentWrite (`triage`, K3) | `close_reason`, `if_match` | `intent{state: closed}`, `verdict` (from §2 eval) | `EvalAcceptance` via evaluator (served `attest.list` by head OID); conflicted subjects are not queried and fail positive requirements (§2.2 rule 1); evaluator errors refuse close (§2.2 rule 9); close denies unless verdict satisfied or reason is `duplicate`/`wontfix` with maintainer (`write`) sign-off | Served (`attest.list` is served) |
| 4 | `intent.reopen` | Mutation POST `/{owner}/{repo}/intents/{number}/reopen` | ScopeIntentWrite (`write`, K3-reopen per C6-analog) | `if_match` | `intent{state: open}` | SQ update; emits `intent.reopened` | Served (store only) |
| 5 | `intent.claim` | Mutation POST `/{owner}/{repo}/intents/{number}/claim` | ScopeIntentWrite (`triage`, K4 plus G11) | `ttl_s`, `if_match` | `lease{claim_id, holder, paths, mode, expires_at}` | Ruk `claim.acquire` with `paths` from intent, `mode` from `lease_mode`, keyed by intent public id; denies on open blocking ask (§4) or contested claim | Served (`claim.acquire`) |
| 6 | `intent.release` | Mutation POST `/{owner}/{repo}/intents/{number}/release` | ScopeIntentWrite (`triage` own / `maintain` forced, K5) | `force?`, `if_match` | `released{claim_id, forced}` | Ruk `claim.release`; holder identity from live `claim.list`, never cached | Served (`claim.release`, `claim.list`) |
| 7 | `intent.link_change` | Mutation POST `/{owner}/{repo}/intents/{number}/changes` | ScopeIntentWrite (`triage`, K3) | `change_id`, `relation`, `head_oid?` | `link{public_id, change_id, head_oid}` | SQ insert intent_changes; `head_oid` verified by `CommandOpen` `git show` (typed `show` when served) and refreshed on next view | `E_HOSTED` for typed `show`; `CommandOpen` `git show` serves |
| 8 | `view.create` | Mutation POST `/{owner}/{repo}/views` | ScopeViewWrite (`triage`) | `name`, `revset?`, `predicates{}`, `shared?` | `view{public_id, version: 1}` | SQ insert views plus view_versions | Served (store only) |
| 9 | `view.run` | Read GET `/{owner}/{repo}/views/{id}?version?&cursor?&limit?` | ScopeViewRead (`read`, K2-analog) | `version?`, predicate overrides (`gap?`, `state?`, `assignee?`, `q?`), `cursor?`, `limit?` | `{items:[intent\|change rows with gap reasons], next_cursor}` | SQ for intent predicates plus FTS (§1.8) plus `CommandOpen` git reads for commit predicates; `EvalAcceptance` via evaluator for gaps; attestation facts from served `attest.list` by OID, never by change-id; evaluator errors render `evaluation_error` per row (§2.2 rule 9) | `E_HOSTED` for typed `query`; `CommandOpen` `git log` serves; `attest.list`/`claim.list` served |
| 10 | `view.share` | Mutation POST `/{owner}/{repo}/views/{id}/share` | ScopeViewWrite (`maintain`; sharing is admin-visible) | `shared: true\|false`, `if_match` | `view{shared}` | SQ update; emits `view.shared` | Served (store only) |
| 11 | `ask.create` | Mutation POST `/{owner}/{repo}/intents/{number}/asks` | ScopeIntentWrite (`triage`, K3) | `question`, `options[]?`, `blocks_claim?`, `deadline_ms?` | `ask{public_id, state: asked, deadline_ms}` | SQ insert asks; flips intent to `blocked` when blocking | Served (store only) |
| 12 | `ask.answer` | Mutation POST `/{owner}/{repo}/asks/{id}/answer` | ScopeIntentWrite (`triage`, human-only) | `answer`, `if_match` | `ask{state: answered}`, `intent{state}` | SQ update ask plus intent state recompute in one tx (§4) | Served (store only) |
| 13 | `comment.create` | Mutation POST `/{owner}/{repo}/changes/{change_id}/comments` | ScopeChangeComment (`triage`, C3) | `intent_number?`, `head_oid?` (null for conflicted heads), `path?`, `line?`, `line_end?`, `body` | `comment{public_id, anchor}` | SQ insert comments; anchor validated by `CommandOpen` `git show` when a head OID is present (typed `show` when served); conflict-anchored rows (`head_oid NULL`) skip OID validation | `E_HOSTED` for typed `show`/`file`; `CommandOpen` `git show` serves |
| 14 | `comment.list` | Read GET `/{owner}/{repo}/changes/{change_id}/comments?unmatched_only?&cursor?&limit?` | ScopeChangeRead (`read`, C2) | `unmatched_only?`, `head_oid?`, `cursor?`, `limit?` | `{comments:[...], next_cursor}` | SQ range scan on `idx_comments_change`; includes conflict-anchored rows (`head_oid NULL`) alongside OID-pinned rows | Served (store only) |
| 15 | `change.list` | Read GET `/{owner}/{repo}/changes?state?&author?&limit?` | ScopeChangeRead (`read`, C2) | `state?`, `author?`, `cursor?`, `limit?` | `{changes:[{change_id, head, state}], next_cursor}` | `CommandOpen` `git log` (typed `query` with revset when served) | `E_HOSTED` for typed `query`; `CommandOpen` serves |
| 16 | `change.show` | Read GET `/{owner}/{repo}/changes/{change_id}` | ScopeChangeRead (`read`, C2) | — | `{change_id, head, state, parents, author, subject}` | `CommandOpen` `git show` (typed `show` when served) | `E_HOSTED` for typed `show`; `CommandOpen` serves |
| 17 | `change.diff` | Read GET `/{owner}/{repo}/changes/{change_id}/diff?against?` | ScopeChangeRead (`read`, C2) | `against?` (base OID or `parent`) | `{files:[{path, old_path?, kind}], patch}` | `CommandOpen` `git diff` returning raw unified patch (typed `diff` when served; body is raw unified patch or `binary`, no structured hunks) | `E_HOSTED` for typed `diff`; `CommandOpen` serves |
| 18 | `change.blame` | Read GET `/{owner}/{repo}/changes/{change_id}/blame?path?&start?&end?` | ScopeChangeRead (`read`, C2) | `path`, `start?`, `end?` | `{hunks:[{start_line, line_count, head, author, change_id}]}` | `CommandOpen` `git blame -L` (typed `blame` when served) | `E_HOSTED` for typed `blame`; `CommandOpen` serves |
| 19 | `branch.view` | Read GET `/{owner}/{repo}/branches/{branch}` | ScopeChangeRead (`read`, C2) | `branch` | `{branch, base_oid, open_changes:[...], gaps:[...]}` | `CommandOpen` git reads for branch members plus §6 gap per member; groups the stack landing toward the branch | `E_HOSTED` for typed `query`; `CommandOpen` serves; `attest.list`/`claim.list` served |
| 20 | `land.prepare` | Mutation POST `/{owner}/{repo}/changes/{change_id}/land/prepare` | ScopeLand (`write`, C7 plus G3/G4/G9) | `target`, `intent_number`, `merge?`, `idempotency_key?` | `{plan_id, candidate, base, expires_at, requirements[], policy_version, waived[]}` | `EvalAcceptance` via evaluator, then Ruk `land.prepare`; evaluator errors refuse prepare (§2.2 rule 9); rejects conflicted heads; single-commit plans only; stacks land serially bottom-up | Served (`land.prepare`) |
| 21 | `land.execute` | Mutation POST `/{owner}/{repo}/changes/{change_id}/land/execute` | ScopeLand (`write`, C7) | `plan_id`, `candidate` | `{landed_oid, target, plan_deleted}` | Intended method Ruk `land.execute` (`LandExecuteArgs`, plan loaded with `load_plan`, `E_STALE` on candidate drift) | `E_HOSTED` on the hosted preparation path (`prepare.rs:297-300`, the `uncovered` arm); live path needs the transport-owner job implementation (`land.rs:31-34`) |
| 22 | `land.admin_execute` | Mutation POST `/{owner}/{repo}/changes/{change_id}/land/admin-execute` | ScopeAdmin (`admin`, site-admin only) | `target`, `candidate`, `reason` | `{landed_oid, audit_id}` | Intended method Ruk `land.execute` path with named override; audit-logged; requires non-empty reason | `E_HOSTED` as op 21; same transport-owner implementation carries the override |
| 23 | `policy.get` | Read GET `/{owner}/{repo}/policies/{pattern}` | ScopePolicyRead (`read`) | `pattern` | `{pattern, mode, push_allow, land_allow, required[], low_risk_*, rule_version}` | SQ select land_policies | Served (store only) |
| 24 | `policy.set` | Mutation PUT `/{owner}/{repo}/policies/{pattern}` | ScopePolicyWrite (`maintain`, G10) | `mode`, `push_allow`, `land_allow`, `required[]`, `low_risk_facets?`, `low_risk_paths?`, `if_match` (rule_version) | `{pattern, rule_version+1}` | SQ upsert land_policies; setting `bypass_admin = true` is rejected in v1 (`[D]`; the column exists but the op refuses it, keeping G9 default-deny) | Served (store only) |

Guards (op-table `Guards`, evaluated before parsing per `op-table-design.md` §10.3): every repo-scoped op carries the repo-visibility guard plus `authz.Check` as the first handler step (`permission-model.md` §14 single enforcement point). Anonymous callers deny on all mutations and reach only public-repo reads, matching the §9 `anon` column.

Idempotency: mutations accept `Idempotency-Key` per `op-table-design.md` §7 (`intent.create`, `land.prepare`, `land.execute` REQUIRED; others optional). Ruk-bound ops pass `request_id = "rukie:" + Claim.ID` so a retried mutation replays against Ruk receipts.

## 6. Gap work-list op: how view.run computes gap reasons

`view.run` with predicate `gap` returns only intents or changes exhibiting at least one gap. Gaps are computed, never stored. Closed predicate set for views: `intent.state`, `facet.<key>`, `lease.holder`, `gap.<reason>`, `assignee`, `updated_since_ms`, plus the revset for commit selection.

Per intent, evaluated in order (first match is the primary display reason; all matches are returned):

| Gap reason | Computed how |
|---|---|
| `unlinked` | Intent is `open` with zero `intent_changes` rows and no `task/acceptance` requirement in its contract |
| `blocked_ask` | Open blocking ask exists (`asks` with `state = 'asked'`, `blocks_claim = 1`); payload carries `ask_id` and `deadline_ms` |
| `blocked_review` | Live `review/block` attestation on any linked head OID (negative requirement fails) |
| `lease_held` | Live Ruk claim covers the intent's paths held by another principal (`claim.list` filtered by path overlap); payload carries holder and `expires_at` |
| `lease_missing` | Intent is `open` with linked changes but no live claim by anyone (nobody is working it) |
| `needs_ci` | Any positive `ci/*` requirement unsatisfied on any linked head OID (missing or expired attestation) |
| `needs_review` | Any `review/approve` requirement unsatisfied on any linked head OID |
| `stale_attestation` | Attestation exists with matching kind and result but every match is expired for its bound (Ruk `Gap::Expired`), or the head OID moved after signing (G6 invalidation) |
| `conflict` | Any linked change has an empty head OID (conflicted; attestation lookup is OID-only so no proof can satisfy it) |
| `expiring_lease` | Live own claim expires within the warning window (default 1 h, mirrors `inbox-design.md` S8); informational, pairs with the claim-warning event |
| `policy_drift` | The `rule_version` recorded at `land.prepare` differs from the current `land_policies.rule_version` for the target |
| `evaluation_error` | The evaluator returned an error for this intent (transport or Ruk failure); payload carries a support correlation id, never proof content |

Per change (for `branch.view` and change-scoped views): the change subset of the above (`blocked_review`, `needs_ci`, `needs_review`, `stale_attestation`, `conflict`, `lease_held`) computed from its own head facts without joining the intent.

Freshness rule: attestation facts are read live from Ruk at `view.run` time (`attest.list` per head OID, `claim.list` per path set). The store never caches verdicts, so a View can never show "gate satisfied" while `land.prepare` refuses, per the `abstraction-intent-proof-view-lease.md` §1 leak-surface requirement. Both call `EvalAcceptance` with the same per-requirement bounded or unbounded routing.

## 7. Event rows emitted to domain_events

One `domain_events` row per trigger, written in the same `Write` transaction as the domain mutation (`inbox-design.md` §2.2, `webhook-design.md` §6.4). Inbox fan-out consumes the row synchronously in-transaction; webhook dispatch tails `seq` asynchronously. Vocabulary reuses the webhook catalogue names (`webhook-design.md` §4) extended with the intent, ask, view, and land rows below (`[D]`; no second enum exists).

| `type` | Trigger | `data` fields | Inbox default recipients (`inbox-design.md` §1 sources) |
|---|---|---|---|
| `intent.created` | `intent.create` commits | `intent_id`, `number`, `title`, `reporter` | S3 task participants plus watchers |
| `intent.updated` | `intent.update` commits | `intent_id`, `number`, `changed_fields[]` | S3, silent append unless assignee or contract changed (then bump) |
| `intent.closed` | `intent.close` commits | `intent_id`, `number`, `close_reason`, `verdict_summary` | S3 closing item, sets `subject_closed_at` |
| `intent.reopened` | `intent.reopen` or ask-answer unblock (§4) | `intent_id`, `number`, `reason` | S3 bump |
| `intent.claimed` | Ruk `claim.acquire` through `intent.claim` | `intent_id`, `number`, `holder`, `lease_id`, `lease_expires_at`, `mode` | S3 plus S8 warning schedule |
| `intent.claim_expired` | Lease expiry observed (sweeper or lazy) | `intent_id`, `number`, `lease_id` | S8 holder plus assignee; unblocks for re-claim |
| `intent.released` | `intent.release` commits | `intent_id`, `number`, `forced` | S3 |
| `intent.linked` | `intent.link_change` commits | `intent_id`, `number`, `change_id`, `relation` | S1 change participants |
| `change.commented` | `comment.create` on a change | `change_id`, `version`, `comment_id`, `path?`, `line?`, `body_preview` (first 500 chars) | S1 thread bump with `reasons = [commented]` |
| `change.comment.remapped` | §3 remap runs (`moved` or `unmatched` nonzero) | `change_id`, `old_head`, `new_head`, `moved`, `unmatched` | S1 silent append unless `unmatched > 0` (then bump) |
| `ask.asked` | `ask.create` with `blocks_claim = 1` | `intent_id`, `ask_id`, `deadline_ms` | S3 bump to assignee plus reporter; overrides `ignore` like S5 |
| `ask.answered` | `ask.answer` commits | `intent_id`, `ask_id`, `answer_by` | S3 bump; clears `blocked` per §4 |
| `ask.expired` | Deadline passes | `intent_id`, `ask_id` | S3 silent append; clears `blocked` per §4 |
| `view.shared` | `view.share` sets `shared = 1` | `view_id`, `name`, `owner` | No inbox row (queryable only); webhook-only |
| `land.prepared` | `land.prepare` succeeds | `change_id`, `intent_id`, `plan_id`, `candidate`, `target`, `policy_version` | S1 participants |
| `land.executed` | `land.execute` succeeds (maps to catalogue `change.landed`) | `change_id`, `target`, `landed_oid`, `landed_by` | S1 closing item per linked intent thread |
| `policy.updated` | `policy.set` commits | `pattern`, `rule_version`, `updated_by` | S10 admin notice to repo admins |

Retention follows `inbox-design.md` §9: `domain_events` 180 days, thread items per thread rules, terminal webhook deliveries 30 days. `intent.linked` on a `LandOnly` target additionally fans out to the bookmark watchers via the `ref.updated` subscription path, not as a duplicate event.

### Critical Files for Implementation

- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/store-engine-design.md` — Identifier, time, FTS5 trigram, and outbox conventions the §1 DDL follows verbatim
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/permission-model.md` — K1..K6 and C1..C7 floors, G0..G11 bookmark semantics, and BookmarkRule plus AttestationReq shapes behind §§2, 5, 6
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/ruk-backend-surface.md` — Served versus E_HOSTED Ruk methods (attest.list, claim.*, land.prepare, op.log served; diff, show, blame, query via CommandOpen fallback; land.execute refused on the hosted preparation path) behind §§3, 5, 6
- `ruk/crates/ruk-core/src/attestation/query.rs` — `evaluate_requirements` and `evaluate_bounded_requirements` signatures plus `Gap::Expired` semantics behind §2.2
- `ruk/crates/ruk-core/src/protocol.rs` — `AttestStatement.subject: Oid` and `AttestListArgs.subject: Option<Oid>` behind §2.2 rule 1 (no change-id attestation lookups)
- `ruk/crates/ruk-core/src/coord/prepare.rs` — `land.execute` E_HOSTED refusal behind §5 ops 21 and 22
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/inbox-design.md` — domain_events source, write-time fan-out, thread keys, and S1..S11 recipient rules behind §7
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/op-table-design.md` — Op row shape, Kind, Scope, idempotency, and derived-surface rules behind §5