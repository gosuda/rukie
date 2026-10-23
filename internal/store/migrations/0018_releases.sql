CREATE TABLE releases (
  id INTEGER PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  public_id TEXT NOT NULL UNIQUE,
  tag_name TEXT NOT NULL,
  lower_tag_name TEXT NOT NULL,
  target_oid TEXT NOT NULL DEFAULT '',
  target_kind TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  notes_markdown TEXT NOT NULL DEFAULT '',
  notes_html TEXT NOT NULL DEFAULT '',
  is_prerelease INTEGER NOT NULL DEFAULT 0 CHECK (is_prerelease IN (0,1)),
  state TEXT NOT NULL CHECK (state IN ('draft','published')),
  author_id INTEGER NOT NULL,
  published_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  revision INTEGER NOT NULL DEFAULT 0,
  deleted_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX idx_releases_live_tag ON releases(repo_id, lower_tag_name) WHERE deleted_at = 0;
CREATE INDEX idx_releases_repo ON releases(repo_id, created_at DESC);
CREATE TABLE release_assets (
  id INTEGER PRIMARY KEY,
  public_id TEXT NOT NULL UNIQUE,
  release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  content_type TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('draft','published')),
  published_at INTEGER NOT NULL DEFAULT 0,
  path TEXT NOT NULL,
  uploader_id INTEGER NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX idx_release_asset_name ON release_assets(release_id, lower(name));
CREATE INDEX idx_release_assets_digest ON release_assets(sha256);
