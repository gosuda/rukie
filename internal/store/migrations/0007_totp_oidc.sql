CREATE TABLE user_totp (
  user_id              INTEGER NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret_sealed        TEXT    NOT NULL CHECK (length(secret_sealed) BETWEEN 1 AND 512),
  pending_secret_sealed TEXT   NULL CHECK (pending_secret_sealed IS NULL OR length(pending_secret_sealed) BETWEEN 1 AND 512),
  last_counter         INTEGER NOT NULL DEFAULT -1 CHECK (last_counter >= -1),
  created_at           INTEGER NOT NULL CHECK (created_at > 0),
  verified_at          INTEGER NULL CHECK (verified_at IS NULL OR verified_at > 0)
) STRICT;

CREATE TABLE recovery_codes (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_sha256 TEXT NOT NULL UNIQUE CHECK (length(code_sha256) = 64),
  used_at     INTEGER NULL CHECK (used_at IS NULL OR used_at > 0),
  created_at  INTEGER NOT NULL CHECK (created_at > 0)
) STRICT;

CREATE INDEX idx_recovery_user ON recovery_codes (user_id);

CREATE TABLE oidc_providers (
  id                  TEXT NOT NULL PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
  issuer              TEXT NOT NULL UNIQUE CHECK (length(issuer) BETWEEN 8 AND 512),
  client_id           TEXT NOT NULL CHECK (length(client_id) BETWEEN 1 AND 512),
  client_secret_sealed TEXT NULL CHECK (client_secret_sealed IS NULL OR length(client_secret_sealed) BETWEEN 1 AND 1024),
  scopes              TEXT NOT NULL DEFAULT 'openid profile email' CHECK (length(scopes) BETWEEN 1 AND 512),
  allow_signup        INTEGER NOT NULL DEFAULT 1 CHECK (allow_signup IN (0, 1))
) STRICT;

CREATE TABLE oidc_links (
  issuer    TEXT    NOT NULL CHECK (length(issuer) BETWEEN 8 AND 512),
  subject   TEXT    NOT NULL CHECK (length(subject) BETWEEN 1 AND 512),
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  linked_at INTEGER NOT NULL CHECK (linked_at > 0),
  PRIMARY KEY (issuer, subject)
) STRICT;

CREATE INDEX idx_oidc_links_user ON oidc_links (user_id);

CREATE TABLE oidc_states (
  state_sha256 TEXT NOT NULL PRIMARY KEY CHECK (length(state_sha256) = 64),
  provider_id  TEXT    NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE,
  nonce        TEXT    NOT NULL CHECK (length(nonce) BETWEEN 1 AND 256),
  verifier     TEXT    NOT NULL CHECK (length(verifier) BETWEEN 43 AND 128),
  link_user_id INTEGER NULL REFERENCES users(id) ON DELETE CASCADE,
  return_to    TEXT    NOT NULL DEFAULT '/' CHECK (length(return_to) BETWEEN 1 AND 1024),
  created_at   INTEGER NOT NULL CHECK (created_at > 0),
  expires_at   INTEGER NOT NULL CHECK (expires_at > 0)
) STRICT;

CREATE INDEX idx_oidc_states_expiry ON oidc_states (expires_at);

CREATE TABLE oidc_ip_blocks (
  ip          TEXT    NOT NULL PRIMARY KEY CHECK (length(ip) BETWEEN 1 AND 128),
  blocked_until INTEGER NOT NULL CHECK (blocked_until > 0),
  created_at  INTEGER NOT NULL CHECK (created_at > 0)
) STRICT;
