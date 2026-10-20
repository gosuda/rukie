CREATE TABLE repos (
  id            INTEGER PRIMARY KEY,
  owner_user_id INTEGER NULL REFERENCES users(id) ON DELETE SET NULL,
  org_id        INTEGER NOT NULL DEFAULT 0 CHECK (org_id >= 0),
  name          TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  visibility    TEXT    NOT NULL DEFAULT 'private' CHECK (visibility IN ('public', 'internal', 'private')),
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  updated_at    INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (org_id, owner_user_id, name)
) STRICT;

CREATE TABLE repo_grants (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role    TEXT    NOT NULL CHECK (role IN ('read', 'triage', 'write', 'maintain', 'admin')),
  PRIMARY KEY (repo_id, user_id)
) STRICT;

CREATE TABLE bookmark_rules (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  pattern      TEXT    NOT NULL CHECK (length(pattern) BETWEEN 1 AND 255),
  mode         TEXT    NOT NULL DEFAULT 'Open' CHECK (mode IN ('Open', 'LandOnly')),
  bypass_admin INTEGER NOT NULL DEFAULT 0 CHECK (bypass_admin IN (0, 1)),
  push_allow   TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(push_allow)),
  land_allow   TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(land_allow)),
  required     TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(required)),
  version      INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at   INTEGER NOT NULL CHECK (created_at > 0)
) STRICT;

CREATE INDEX idx_rules_repo ON bookmark_rules(repo_id);
