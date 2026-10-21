ALTER TABLE oidc_states ADD COLUMN browser_sha256 TEXT NOT NULL DEFAULT '' CHECK (length(browser_sha256) = 0 OR length(browser_sha256) = 64);
