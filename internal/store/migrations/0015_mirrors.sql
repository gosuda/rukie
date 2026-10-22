-- 0015_mirrors.sql: mirror configuration, credentials, runs, and ref state.
-- Generic jobs already carry leases and fencing, so mirror work uses jobs rather
-- than a separate mirror_jobs table.
CREATE TABLE mirrors (
  id                         INTEGER PRIMARY KEY,
  public_id                  TEXT    NOT NULL UNIQUE,
  repo_id                    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name                       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  target_url                 TEXT    NOT NULL,
  host_kind                  TEXT    NOT NULL,
  username                   TEXT    NOT NULL DEFAULT '',
  mode                       TEXT    NOT NULL,
  ref_selection              TEXT    NOT NULL,
  globs_json                 TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(globs_json)),
  include_notes              INTEGER NOT NULL DEFAULT 0 CHECK (include_notes IN (0, 1)),
  protect_default_branch     INTEGER NOT NULL DEFAULT 0 CHECK (protect_default_branch IN (0, 1)),
  enabled                    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  sync_on_ref_update         INTEGER NOT NULL DEFAULT 0 CHECK (sync_on_ref_update IN (0, 1)),
  create_if_missing          INTEGER NOT NULL DEFAULT 0 CHECK (create_if_missing IN (0, 1)),
  policy_ack                 INTEGER NOT NULL DEFAULT 0 CHECK (policy_ack IN (0, 1)),
  adopt_existing_refs        INTEGER NOT NULL DEFAULT 0 CHECK (adopt_existing_refs IN (0, 1)),
  adopted                    INTEGER NOT NULL DEFAULT 0 CHECK (adopted IN (0, 1)),
  debounce_seconds           INTEGER NOT NULL DEFAULT 0 CHECK (debounce_seconds >= 0),
  max_delay_seconds          INTEGER NOT NULL DEFAULT 0 CHECK (max_delay_seconds >= 0),
  reconcile_interval_seconds INTEGER NOT NULL DEFAULT 0 CHECK (reconcile_interval_seconds >= 0),
  state                      TEXT    NOT NULL DEFAULT 'idle',
  consecutive_failures       INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  last_error_class           TEXT    NOT NULL DEFAULT '',
  last_error                 TEXT    NOT NULL DEFAULT '',
  last_http_status           INTEGER NOT NULL DEFAULT 0 CHECK (last_http_status >= 0),
  last_attempt_at            INTEGER NOT NULL DEFAULT 0,
  last_success_at            INTEGER NOT NULL DEFAULT 0,
  next_run_at                INTEGER NOT NULL DEFAULT 0,
  dirty_since                INTEGER NOT NULL DEFAULT 0,
  last_manual_at             INTEGER NOT NULL DEFAULT 0,
  event_cursor               INTEGER NOT NULL DEFAULT 0,
  created_at                 INTEGER NOT NULL CHECK (created_at > 0),
  updated_at                 INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (repo_id, name)
) STRICT;

CREATE INDEX idx_mirrors_repo ON mirrors(repo_id);

CREATE TABLE mirror_credentials (
  mirror_id   INTEGER PRIMARY KEY REFERENCES mirrors(id) ON DELETE CASCADE,
  key_version INTEGER NOT NULL CHECK (key_version > 0),
  nonce       BLOB    NOT NULL,
  ciphertext  BLOB    NOT NULL,
  last4       TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL CHECK (created_at > 0),
  rotated_at  INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE mirror_runs (
  id               INTEGER PRIMARY KEY,
  public_id        TEXT    NOT NULL UNIQUE,
  mirror_id        INTEGER NOT NULL REFERENCES mirrors(id) ON DELETE CASCADE,
  kind             TEXT    NOT NULL,
  state            TEXT    NOT NULL,
  trigger          TEXT    NOT NULL,
  dry_run          INTEGER NOT NULL DEFAULT 0 CHECK (dry_run IN (0, 1)),
  plan_digest      TEXT    NOT NULL DEFAULT '',
  confirmed_digest TEXT    NOT NULL DEFAULT '',
  attempt_no       INTEGER NOT NULL DEFAULT 0 CHECK (attempt_no >= 0),
  ruk_plan         TEXT    NOT NULL DEFAULT '',
  outcome_class    TEXT    NOT NULL DEFAULT '',
  outcome_summary  TEXT    NOT NULL DEFAULT '',
  counts_json      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(counts_json)),
  started_at       INTEGER NOT NULL DEFAULT 0,
  finished_at      INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL CHECK (created_at > 0),
  updated_at       INTEGER NOT NULL CHECK (updated_at > 0)
) STRICT;

CREATE INDEX idx_runs_mirror ON mirror_runs(mirror_id);

CREATE TABLE mirror_refstate (
  mirror_id           INTEGER NOT NULL REFERENCES mirrors(id) ON DELETE CASCADE,
  ref                 TEXT    NOT NULL,
  local_oid           TEXT    NOT NULL DEFAULT '',
  remote_oid          TEXT    NOT NULL DEFAULT '',
  status              TEXT    NOT NULL,
  reason              TEXT    NOT NULL DEFAULT '',
  reason_class        TEXT    NOT NULL DEFAULT '',
  first_failed_at     INTEGER NOT NULL DEFAULT 0,
  last_attempt_at     INTEGER NOT NULL DEFAULT 0,
  last_success_at     INTEGER NOT NULL DEFAULT 0,
  consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  PRIMARY KEY (mirror_id, ref)
) STRICT;

CREATE INDEX idx_refstate_mirror ON mirror_refstate(mirror_id);

CREATE TABLE mirror_attempts (
  id          INTEGER PRIMARY KEY,
  run_id      INTEGER NOT NULL REFERENCES mirror_runs(id) ON DELETE CASCADE,
  attempt_no  INTEGER NOT NULL CHECK (attempt_no > 0),
  ruk_plan    TEXT    NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL CHECK (started_at > 0),
  finished_at INTEGER NOT NULL DEFAULT 0,
  outcome     TEXT    NOT NULL DEFAULT '',
  summary     TEXT    NOT NULL DEFAULT ''
) STRICT;
