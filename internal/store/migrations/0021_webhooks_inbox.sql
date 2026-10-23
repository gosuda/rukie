-- 0021_webhooks_inbox.sql: webhook endpoints/messages/attempts and inbox watches/threads/items/settings.
-- All times are INTEGER Unix milliseconds. Payload cap 256 KiB enforced by CHECK.

CREATE TABLE webhook_endpoints (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
  scope TEXT NOT NULL CHECK (scope IN ('repo','owner')),
  repo_id INTEGER NULL REFERENCES repos(id) ON DELETE CASCADE,
  owner_id INTEGER NULL,
  owner_kind TEXT NOT NULL DEFAULT '' CHECK (owner_kind IN ('','user','org')),
  url TEXT NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
  url_hash BLOB NOT NULL,
  secret_enc BLOB NOT NULL CHECK (length(secret_enc) > 0),
  prev_secret_enc BLOB NULL,
  prev_secret_expires_at INTEGER NULL CHECK (prev_secret_expires_at IS NULL OR prev_secret_expires_at > 0),
  event_types TEXT NOT NULL CHECK (json_valid(event_types)),
  ref_globs TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(ref_globs)),
  active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  concurrency INTEGER NOT NULL DEFAULT 4 CHECK (concurrency BETWEEN 1 AND 16),
  timeout_seconds INTEGER NOT NULL DEFAULT 20 CHECK (timeout_seconds BETWEEN 5 AND 60),
  note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 256),
  created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  last_status TEXT NOT NULL DEFAULT 'never' CHECK (last_status IN ('never','ok','failing','disabled')),
  consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  disabled_at INTEGER NULL CHECK (disabled_at IS NULL OR disabled_at > 0),
  disable_reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL CHECK (created_at > 0),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0),
  CHECK (
    (scope = 'repo' AND repo_id IS NOT NULL AND owner_id IS NULL AND owner_kind = '') OR
    (scope = 'owner' AND owner_id IS NOT NULL AND repo_id IS NULL AND owner_kind IN ('user','org'))
  )
) STRICT;
CREATE INDEX idx_webhook_endpoints_repo ON webhook_endpoints(repo_id, active);
CREATE INDEX idx_webhook_endpoints_owner ON webhook_endpoints(owner_id, active);
CREATE INDEX idx_webhook_endpoints_urlhash ON webhook_endpoints(url_hash);

CREATE TABLE webhook_messages (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 4 AND 64),
  endpoint_id TEXT NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
  outbox_seq INTEGER NOT NULL,
  event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 1 AND 64),
  event_time INTEGER NOT NULL CHECK (event_time > 0),
  payload BLOB NOT NULL CHECK (length(payload) <= 262144),
  state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','inflight','delivered','dead')),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at INTEGER NOT NULL CHECK (next_attempt_at > 0),
  lease_expires_at INTEGER NOT NULL DEFAULT 0 CHECK (lease_expires_at >= 0),
  last_error TEXT NOT NULL DEFAULT '',
  last_status_code INTEGER NULL,
  delivered_at INTEGER NULL CHECK (delivered_at IS NULL OR delivered_at > 0),
  redelivery_of TEXT NULL,
  updated_at INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (outbox_seq, endpoint_id)
) STRICT;
CREATE INDEX idx_webhook_messages_poll ON webhook_messages(state, next_attempt_at);
CREATE INDEX idx_webhook_messages_endpoint ON webhook_messages(endpoint_id, id);

CREATE TABLE webhook_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id TEXT NOT NULL REFERENCES webhook_messages(id) ON DELETE CASCADE,
  attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
  started_at INTEGER NOT NULL CHECK (started_at > 0),
  finished_at INTEGER NOT NULL CHECK (finished_at > 0),
  attempt_timestamp INTEGER NOT NULL DEFAULT 0 CHECK (attempt_timestamp >= 0),
  http_status INTEGER NULL,
  error TEXT NOT NULL DEFAULT '',
  req_headers TEXT NOT NULL DEFAULT '',
  resp_headers BLOB NULL,
  resp_body BLOB NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0)
) STRICT;
CREATE INDEX idx_webhook_attempts_message ON webhook_attempts(message_id, attempt_no);

CREATE TABLE watches (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  level TEXT NOT NULL CHECK (level IN ('ignore','participate','all','custom')),
  custom_bits TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(custom_bits)),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0),
  PRIMARY KEY (user_id, repo_id)
) STRICT;

CREATE TABLE inbox_threads (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('change','branch_view','task','release','mirror','repo','webhook_endpoint','ci_run_group')),
  subject_id TEXT NOT NULL CHECK (length(subject_id) BETWEEN 1 AND 512),
  repo_id INTEGER NULL REFERENCES repos(id) ON DELETE CASCADE,
  state TEXT NOT NULL DEFAULT 'unread' CHECK (state IN ('unread','read','done','snoozed')),
  muted INTEGER NOT NULL DEFAULT 0 CHECK (muted IN (0,1)),
  reasons TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(reasons)),
  snippet TEXT NOT NULL DEFAULT '',
  unread_items INTEGER NOT NULL DEFAULT 0 CHECK (unread_items >= 0),
  total_items INTEGER NOT NULL DEFAULT 0 CHECK (total_items >= 0),
  bumped_at INTEGER NOT NULL CHECK (bumped_at > 0),
  snooze_until INTEGER NOT NULL DEFAULT 0 CHECK (snooze_until >= 0),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (user_id, subject_kind, subject_id)
) STRICT;
CREATE INDEX idx_inbox_threads_user_state ON inbox_threads(user_id, state, bumped_at DESC, id DESC);
CREATE INDEX idx_inbox_threads_snooze ON inbox_threads(state, snooze_until);

CREATE TABLE inbox_items (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 64),
  thread_id TEXT NOT NULL REFERENCES inbox_threads(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  event_id TEXT NOT NULL CHECK (length(event_id) BETWEEN 1 AND 128),
  event_type TEXT NOT NULL DEFAULT '',
  actor_id INTEGER NOT NULL DEFAULT 0,
  actor_name TEXT NOT NULL DEFAULT '',
  reasons TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(reasons)),
  title TEXT NOT NULL DEFAULT '',
  bumped INTEGER NOT NULL DEFAULT 0 CHECK (bumped IN (0,1)),
  created_at INTEGER NOT NULL CHECK (created_at > 0),
  UNIQUE (event_id, user_id)
) STRICT;
CREATE INDEX idx_inbox_items_thread ON inbox_items(thread_id, created_at);

CREATE TABLE inbox_settings (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  default_level TEXT NOT NULL DEFAULT 'participate' CHECK (default_level IN ('ignore','participate','all','custom')),
  claim_warn_window_secs INTEGER NOT NULL DEFAULT 3600 CHECK (claim_warn_window_secs BETWEEN 300 AND 86400),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0)
) STRICT;
