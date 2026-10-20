CREATE TABLE scoped_tokens (
  token_hash    TEXT    PRIMARY KEY CHECK (length(token_hash) = 64),
  public_prefix TEXT    NOT NULL CHECK (length(public_prefix) = 12),
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL DEFAULT '' CHECK (length(name) <= 64),
  scopes        TEXT    NOT NULL CHECK (length(scopes) BETWEEN 1 AND 512),
  expires_at    INTEGER NOT NULL CHECK (expires_at > 0),
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  created_ip    TEXT    NOT NULL DEFAULT '',
  last_used_at  INTEGER NULL CHECK (last_used_at IS NULL OR last_used_at > 0),
  last_used_ip  TEXT    NULL,
  revoked_at    INTEGER NULL CHECK (revoked_at IS NULL OR revoked_at > 0),
  public_only   INTEGER NOT NULL DEFAULT 0 CHECK (public_only IN (0, 1))
) STRICT;

CREATE INDEX idx_tokens_user_exp ON scoped_tokens(user_id, expires_at);

CREATE TABLE token_repos (
  token_hash TEXT    NOT NULL REFERENCES scoped_tokens(token_hash) ON DELETE CASCADE,
  repo_id    INTEGER NOT NULL CHECK (repo_id > 0),
  PRIMARY KEY (token_hash, repo_id)
) STRICT;

CREATE TABLE audit_events (
  id                  INTEGER PRIMARY KEY,
  at                  INTEGER NOT NULL CHECK (at > 0),
  actor_user_id       INTEGER NULL REFERENCES users(id) ON DELETE SET NULL,
  action              TEXT    NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
  target_type         TEXT    NOT NULL DEFAULT '',
  target_id           TEXT    NOT NULL DEFAULT '',
  ip                  TEXT    NOT NULL DEFAULT '',
  user_agent          TEXT    NOT NULL DEFAULT '',
  session_hash_prefix TEXT    NULL CHECK (session_hash_prefix IS NULL OR length(session_hash_prefix) = 12),
  result              TEXT    NOT NULL CHECK (length(result) BETWEEN 1 AND 32),
  details             TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(details))
) STRICT;

CREATE INDEX idx_audit_action_at ON audit_events(action, at);
CREATE INDEX idx_audit_actor_at ON audit_events(actor_user_id, at);
