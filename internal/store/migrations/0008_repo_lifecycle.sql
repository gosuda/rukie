ALTER TABLE repos ADD COLUMN description TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2048);
ALTER TABLE repos ADD COLUMN website TEXT NOT NULL DEFAULT '' CHECK (length(website) <= 2048);
ALTER TABLE repos ADD COLUMN topics TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(topics));
ALTER TABLE repos ADD COLUMN default_branch TEXT NOT NULL DEFAULT 'main' CHECK (length(default_branch) BETWEEN 1 AND 255);
ALTER TABLE repos ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1));
ALTER TABLE repos ADD COLUMN deleted_at INTEGER NULL CHECK (deleted_at IS NULL OR deleted_at > 0);
ALTER TABLE repos ADD COLUMN purge_after INTEGER NULL CHECK (purge_after IS NULL OR purge_after > 0);
ALTER TABLE repos ADD COLUMN ruk_path TEXT NOT NULL DEFAULT '' CHECK (length(ruk_path) <= 512);

CREATE INDEX idx_repos_owner ON repos(owner_user_id, name);
CREATE INDEX idx_repos_deleted ON repos(deleted_at);

CREATE TABLE repo_redirects (
  old_owner_user_id INTEGER NOT NULL,
  old_name          TEXT    NOT NULL CHECK (length(old_name) BETWEEN 1 AND 100),
  repo_id           INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  created_at        INTEGER NOT NULL CHECK (created_at > 0),
  PRIMARY KEY (old_owner_user_id, old_name)
) STRICT;
