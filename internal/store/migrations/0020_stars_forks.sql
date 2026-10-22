-- 0020_stars_forks.sql: social edges and fork lifecycle metadata.
ALTER TABLE repos ADD COLUMN fork_of_repo_id INTEGER NULL REFERENCES repos(id) ON DELETE SET NULL;
ALTER TABLE repos ADD COLUMN fork_root_repo_id INTEGER NULL REFERENCES repos(id) ON DELETE SET NULL;
ALTER TABLE repos ADD COLUMN state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'forking', 'deleted'));
ALTER TABLE repos ADD COLUMN star_count INTEGER NOT NULL DEFAULT 0 CHECK (star_count >= 0);
ALTER TABLE repos ADD COLUMN forks_count INTEGER NOT NULL DEFAULT 0 CHECK (forks_count >= 0);

UPDATE repos SET state = 'deleted' WHERE deleted_at IS NOT NULL;

CREATE TABLE stars (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  starred_at INTEGER NOT NULL CHECK (starred_at > 0),
  PRIMARY KEY (user_id, repo_id)
) STRICT;
CREATE INDEX idx_stars_repo_time ON stars(repo_id, starred_at DESC, user_id DESC);
CREATE INDEX idx_stars_user_time ON stars(user_id, starred_at DESC, repo_id DESC);
CREATE INDEX idx_repos_fork_parent ON repos(fork_of_repo_id, created_at DESC, id DESC);
CREATE INDEX idx_repos_explore ON repos(visibility, updated_at DESC, id DESC);
CREATE INDEX idx_repos_explore_created ON repos(visibility, created_at DESC, id DESC);
CREATE INDEX idx_repos_explore_stars ON repos(visibility, star_count DESC, id DESC);
CREATE INDEX idx_repos_explore_name ON repos(visibility, name COLLATE NOCASE, id);
CREATE INDEX idx_orgs_explore ON orgs(visibility, lower_name, id);
