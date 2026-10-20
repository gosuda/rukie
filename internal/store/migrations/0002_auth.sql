CREATE TABLE users (
  id                   INTEGER PRIMARY KEY,
  name                 TEXT    NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 39),
  lower_name           TEXT    NOT NULL UNIQUE CHECK (length(lower_name) BETWEEN 1 AND 39),
  email                TEXT    NOT NULL DEFAULT '' CHECK (length(email) <= 255),
  full_name            TEXT    NOT NULL DEFAULT '' CHECK (length(full_name) <= 255),
  password_hash        TEXT    NOT NULL CHECK (length(password_hash) BETWEEN 1 AND 512),
  password_algo        TEXT    NOT NULL DEFAULT 'argon2id-v19',
  password_changed_at  INTEGER NOT NULL CHECK (password_changed_at > 0),
  must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
  totp_enabled         INTEGER NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0, 1)),
  oidc_only            INTEGER NOT NULL DEFAULT 0 CHECK (oidc_only IN (0, 1)),
  is_admin             INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  is_active            INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
  failed_login_count   INTEGER NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
  locked_until         INTEGER NULL CHECK (locked_until IS NULL OR locked_until > 0),
  created_at           INTEGER NOT NULL CHECK (created_at > 0),
  updated_at           INTEGER NOT NULL CHECK (updated_at > 0)
) STRICT;

CREATE TABLE sessions (
  id_hash             TEXT    PRIMARY KEY CHECK (length(id_hash) = 64),
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token          TEXT    NOT NULL CHECK (length(csrf_token) BETWEEN 1 AND 128),
  auth_level          TEXT    NOT NULL DEFAULT 'password',
  authed_at           INTEGER NOT NULL CHECK (authed_at > 0),
  totp_ok             INTEGER NOT NULL DEFAULT 0 CHECK (totp_ok IN (0, 1)),
  created_at          INTEGER NOT NULL CHECK (created_at > 0),
  last_seen_at        INTEGER NOT NULL CHECK (last_seen_at > 0),
  absolute_expires_at INTEGER NOT NULL CHECK (absolute_expires_at > 0),
  created_ip          TEXT    NOT NULL DEFAULT '',
  user_agent          TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_sessions_user_seen ON sessions(user_id, last_seen_at);

CREATE TABLE login_attempts (
  id             INTEGER PRIMARY KEY,
  username_lower TEXT    NOT NULL CHECK (length(username_lower) BETWEEN 1 AND 255),
  ip             TEXT    NOT NULL DEFAULT '',
  at             INTEGER NOT NULL CHECK (at > 0),
  success        INTEGER NOT NULL CHECK (success IN (0, 1)),
  reason         TEXT    NOT NULL DEFAULT 'password'
) STRICT;

CREATE INDEX idx_attempts_user_at ON login_attempts(username_lower, at);
CREATE INDEX idx_attempts_ip_at ON login_attempts(ip, at);
