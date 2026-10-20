CREATE TABLE orgs (
  id            INTEGER PRIMARY KEY,
  name          TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 39),
  lower_name    TEXT    NOT NULL UNIQUE CHECK (length(lower_name) BETWEEN 1 AND 39),
  display_name  TEXT    NOT NULL DEFAULT '' CHECK (length(display_name) <= 100),
  description   TEXT    NOT NULL DEFAULT '' CHECK (length(description) <= 400),
  website       TEXT    NOT NULL DEFAULT '' CHECK (length(website) <= 2048),
  visibility    TEXT    NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'private')),
  created_at    INTEGER NOT NULL CHECK (created_at > 0),
  updated_at    INTEGER NOT NULL CHECK (updated_at > 0)
) STRICT;

CREATE TABLE teams (
  id              INTEGER PRIMARY KEY,
  org_id          INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name            TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
  lower_name      TEXT    NOT NULL CHECK (length(lower_name) BETWEEN 1 AND 64),
  description     TEXT    NOT NULL DEFAULT '' CHECK (length(description) <= 400),
  permission      TEXT    NOT NULL DEFAULT 'read' CHECK (permission IN ('read', 'triage', 'write', 'maintain', 'admin')),
  repo_scope      TEXT    NOT NULL DEFAULT 'selected' CHECK (repo_scope IN ('all', 'selected')),
  can_create_repo INTEGER NOT NULL DEFAULT 0 CHECK (can_create_repo IN (0, 1)),
  created_at      INTEGER NOT NULL CHECK (created_at > 0),
  updated_at      INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (org_id, lower_name)
) STRICT;

CREATE TABLE team_members (
  team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (team_id, user_id)
) STRICT;

CREATE TABLE team_repos (
  team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  PRIMARY KEY (team_id, repo_id)
) STRICT;

CREATE TABLE org_redirects (
  old_lower_name TEXT    PRIMARY KEY CHECK (length(old_lower_name) BETWEEN 1 AND 39),
  org_id         INTEGER NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  created_at     INTEGER NOT NULL CHECK (created_at > 0)
) STRICT;

CREATE INDEX idx_teams_org ON teams(org_id);
CREATE INDEX idx_team_members_user ON team_members(user_id);
CREATE INDEX idx_team_repos_repo ON team_repos(repo_id);
