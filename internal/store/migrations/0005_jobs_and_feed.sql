CREATE TABLE jobs (
  id               TEXT    PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
  kind             TEXT    NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
  repo_id          INTEGER NOT NULL CHECK (repo_id > 0),
  payload          TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
  state            TEXT    NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'leased', 'done', 'dead')),
  attempts         INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  lease_owner      TEXT    NOT NULL DEFAULT '',
  lease_expires_at INTEGER NOT NULL DEFAULT 0 CHECK (lease_expires_at >= 0),
  created_at       INTEGER NOT NULL CHECK (created_at > 0),
  updated_at       INTEGER NOT NULL CHECK (updated_at > 0)
) STRICT;

CREATE INDEX idx_jobs_claim ON jobs (state, lease_expires_at, created_at, id);

CREATE TABLE pending_pushes (
  invocation_id TEXT    PRIMARY KEY CHECK (length(invocation_id) BETWEEN 1 AND 128),
  repo_id       INTEGER NOT NULL CHECK (repo_id > 0),
  actor_id      INTEGER NOT NULL CHECK (actor_id >= 0),
  landed        TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(landed)),
  attempted_at  INTEGER NOT NULL CHECK (attempted_at > 0)
) STRICT;

CREATE INDEX idx_pushes_repo ON pending_pushes (repo_id);

CREATE TABLE ref_tips (
  repo_id    INTEGER NOT NULL CHECK (repo_id > 0),
  name       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 512),
  target     TEXT    NOT NULL CHECK (length(target) BETWEEN 1 AND 256),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0),
  PRIMARY KEY (repo_id, name)
) STRICT;

CREATE TABLE follower_seen (
  repo_id INTEGER NOT NULL CHECK (repo_id > 0),
  seq     INTEGER NOT NULL CHECK (seq >= 0),
  name    TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 512),
  new     TEXT    NOT NULL DEFAULT '' CHECK (length(new) BETWEEN 0 AND 256),
  seen_at INTEGER NOT NULL CHECK (seen_at > 0),
  PRIMARY KEY (repo_id, seq)
) STRICT;
