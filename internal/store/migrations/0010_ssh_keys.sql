CREATE TABLE ssh_keys (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fingerprint TEXT    NOT NULL UNIQUE CHECK (length(fingerprint) BETWEEN 8 AND 128),
  key_type    TEXT    NOT NULL CHECK (length(key_type) BETWEEN 1 AND 64),
  public_key  TEXT    NOT NULL CHECK (length(public_key) BETWEEN 32 AND 16384),
  comment     TEXT    NOT NULL DEFAULT '' CHECK (length(comment) <= 512),
  created_at  INTEGER NOT NULL CHECK (created_at > 0),
  last_used_at INTEGER NULL CHECK (last_used_at IS NULL OR last_used_at > 0)
) STRICT;

CREATE INDEX idx_ssh_keys_user ON ssh_keys(user_id);
