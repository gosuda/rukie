-- 0013_review_land.sql: F2 review and land DDL: intents, changes, patchsets,
-- anchored comments with a fallback bucket, asks, views, and land intents/runs.
--
-- Comment anchors are (change_id, head_oid, path, line); unmatched rows keep
-- their last-known position with unmatched = 1. Land plans run the
-- awaiting/executing/landed/failed/repreparing machine; the land worker is
-- keyed (repo_id, target_ref). Views persist the closed predicate JSON.
--
-- Policy data lives in bookmark_rules (mode, bypass_admin, required JSON,
-- version as the policy generation); policy.set bumps version.
CREATE TABLE intents (
  id               INTEGER PRIMARY KEY,
  public_id        TEXT    NOT NULL UNIQUE,
  repo_id          INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  number           INTEGER NOT NULL CHECK (number > 0),
  title            TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  body             TEXT    NOT NULL DEFAULT '',
  state            TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'blocked', 'closed')),
  contract         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(contract)),
  assignee_user_id INTEGER NULL REFERENCES users(id) ON DELETE SET NULL,
  created_by       INTEGER NOT NULL REFERENCES users(id),
  created_at       INTEGER NOT NULL CHECK (created_at > 0),
  updated_at       INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (repo_id, number)
) STRICT;

CREATE INDEX idx_intents_repo ON intents(repo_id);

CREATE TABLE changes (
  id              INTEGER PRIMARY KEY,
  public_id       TEXT    NOT NULL UNIQUE,
  repo_id         INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  change_key      TEXT    NOT NULL CHECK (length(change_key) BETWEEN 1 AND 128),
  target_branch   TEXT    NOT NULL CHECK (length(target_branch) BETWEEN 1 AND 255),
  author_user_id  INTEGER NOT NULL REFERENCES users(id),
  state           TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'landed', 'abandoned', 'conflicted')),
  head_oid        TEXT    NOT NULL DEFAULT '',
  patchset_count  INTEGER NOT NULL DEFAULT 0 CHECK (patchset_count >= 0),
  created_at      INTEGER NOT NULL CHECK (created_at > 0),
  updated_at      INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (repo_id, change_key)
) STRICT;

CREATE INDEX idx_changes_repo ON changes(repo_id);

CREATE TABLE patchsets (
  id         INTEGER PRIMARY KEY,
  change_id  INTEGER NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
  ordinal    INTEGER NOT NULL CHECK (ordinal > 0),
  head_oid   TEXT    NOT NULL CHECK (head_oid = '' OR length(head_oid) IN (40, 64)),
  created_at INTEGER NOT NULL CHECK (created_at > 0),
  UNIQUE (change_id, ordinal)
) STRICT;

CREATE TABLE change_comments (
  id            INTEGER PRIMARY KEY,
  public_id     TEXT    NOT NULL UNIQUE,
  change_id     INTEGER NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
  intent_id     INTEGER NULL REFERENCES intents(id) ON DELETE SET NULL,
  head_oid      TEXT    NOT NULL DEFAULT '',
  path          TEXT    NULL CHECK (path IS NULL OR length(path) BETWEEN 1 AND 4096),
  line          INTEGER NULL CHECK (line IS NULL OR line > 0),
  body          TEXT    NOT NULL CHECK (length(body) BETWEEN 1 AND 8192),
  author_user_id INTEGER NOT NULL REFERENCES users(id),
  remapped_from TEXT    NOT NULL DEFAULT '',
  unmatched     INTEGER NOT NULL DEFAULT 0 CHECK (unmatched IN (0, 1)),
  created_at    INTEGER NOT NULL CHECK (created_at > 0)
) STRICT;

CREATE INDEX idx_comments_change ON change_comments(change_id, head_oid);

CREATE TABLE intent_changes (
  intent_id INTEGER NOT NULL REFERENCES intents(id) ON DELETE CASCADE,
  change_id INTEGER NOT NULL REFERENCES changes(id) ON DELETE CASCADE,
  head_oid  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (intent_id, change_id)
) STRICT;

CREATE TABLE asks (
  id          INTEGER PRIMARY KEY,
  public_id   TEXT    NOT NULL UNIQUE,
  intent_id   INTEGER NOT NULL REFERENCES intents(id) ON DELETE CASCADE,
  question    TEXT    NOT NULL CHECK (length(question) BETWEEN 1 AND 2048),
  options     TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(options)),
  blocks_claim INTEGER NOT NULL DEFAULT 0 CHECK (blocks_claim IN (0, 1)),
  state       TEXT    NOT NULL DEFAULT 'asked' CHECK (state IN ('asked', 'answered', 'expired')),
  answer      TEXT    NOT NULL DEFAULT '',
  answered_by INTEGER NULL REFERENCES users(id) ON DELETE SET NULL,
  expires_at  INTEGER NULL CHECK (expires_at IS NULL OR expires_at > 0),
  created_by  INTEGER NOT NULL REFERENCES users(id),
  created_at  INTEGER NOT NULL CHECK (created_at > 0)
) STRICT;

CREATE INDEX idx_asks_intent ON asks(intent_id);

CREATE TABLE views (
  id            INTEGER PRIMARY KEY,
  public_id     TEXT    NOT NULL UNIQUE,
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  owner_user_id INTEGER NULL REFERENCES users(id) ON DELETE SET NULL,
  shared        INTEGER NOT NULL DEFAULT 0 CHECK (shared IN (0, 1)),
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  updated_at    INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (repo_id, name)
) STRICT;

CREATE TABLE view_versions (
  id         INTEGER PRIMARY KEY,
  view_id    INTEGER NOT NULL REFERENCES views(id) ON DELETE CASCADE,
  version    INTEGER NOT NULL CHECK (version > 0),
  revset     TEXT    NOT NULL DEFAULT '',
  predicates TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(predicates)),
  created_at INTEGER NOT NULL CHECK (created_at > 0),
  UNIQUE (view_id, version)
) STRICT;

CREATE TABLE land_intents (
  id                INTEGER PRIMARY KEY,
  public_id         TEXT    NOT NULL UNIQUE,
  repo_id           INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  target_ref        TEXT    NOT NULL CHECK (length(target_ref) BETWEEN 1 AND 255),
  change_id         INTEGER NULL REFERENCES changes(id) ON DELETE SET NULL,
  candidate_oid     TEXT    NOT NULL DEFAULT '',
  base_oid          TEXT    NOT NULL DEFAULT '',
  policy_generation INTEGER NOT NULL DEFAULT 1 CHECK (policy_generation > 0),
  state             TEXT    NOT NULL DEFAULT 'awaiting' CHECK (state IN ('awaiting', 'executing', 'landed', 'failed', 'repreparing')),
  reprepares        INTEGER NOT NULL DEFAULT 0 CHECK (reprepares >= 0),
  gap               TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(gap)),
  created_by        INTEGER NOT NULL REFERENCES users(id),
  created_at        INTEGER NOT NULL CHECK (created_at > 0),
  updated_at        INTEGER NOT NULL CHECK (updated_at > 0)
) STRICT;

CREATE INDEX idx_land_intents_repo ON land_intents(repo_id, target_ref);

CREATE TABLE land_runs (
  id         INTEGER PRIMARY KEY,
  public_id  TEXT    NOT NULL UNIQUE,
  plan_id    INTEGER NOT NULL REFERENCES land_intents(id) ON DELETE CASCADE,
  state      TEXT    NOT NULL DEFAULT 'awaiting' CHECK (state IN ('awaiting', 'executing', 'landed', 'failed')),
  detail     TEXT    NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL CHECK (created_at > 0)
) STRICT;

CREATE INDEX idx_land_runs_plan ON land_runs(plan_id);
