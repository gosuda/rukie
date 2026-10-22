ALTER TABLE intents RENAME COLUMN created_by TO reporter_id;
ALTER TABLE intents RENAME COLUMN assignee_user_id TO assignee_id;
ALTER TABLE intents ADD COLUMN close_reason TEXT CHECK (close_reason IN ('completed','duplicate','wontfix','abandoned'));
ALTER TABLE intents ADD COLUMN lease_mode TEXT NOT NULL DEFAULT 'advise' CHECK (lease_mode IN ('enforce','advise'));
ALTER TABLE intents ADD COLUMN claim_paths TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(claim_paths));
ALTER TABLE intents ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE intents ADD COLUMN closed_at INTEGER CHECK (closed_at IS NULL OR closed_at > 0);
CREATE TRIGGER intents_canonical_insert BEFORE INSERT ON intents
WHEN length(NEW.public_id) <> 26 OR length(NEW.body) > 65536
BEGIN
  SELECT RAISE(ABORT, 'intent public_id or body violates canonical constraints');
END;
CREATE TRIGGER intents_canonical_update BEFORE UPDATE OF public_id, body ON intents
WHEN length(NEW.public_id) <> 26 OR length(NEW.body) > 65536
BEGIN
  SELECT RAISE(ABORT, 'intent public_id or body violates canonical constraints');
END;
CREATE UNIQUE INDEX idx_intents_id_repo ON intents(id, repo_id);

CREATE TABLE intent_changes_v17 (
 id INTEGER PRIMARY KEY,
 public_id TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
 intent_id INTEGER NOT NULL,
 repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
 change_id TEXT NOT NULL CHECK (length(change_id) BETWEEN 1 AND 128),
 head_oid TEXT NULL CHECK (head_oid IS NULL OR length(head_oid) = 64),
 relation TEXT NOT NULL DEFAULT 'implements' CHECK (relation IN ('implements','fixes','relates')),
 added_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
 created_at INTEGER NOT NULL CHECK (created_at > 0),
 UNIQUE(intent_id, change_id),
 FOREIGN KEY(intent_id, repo_id) REFERENCES intents(id, repo_id) ON DELETE CASCADE ON UPDATE CASCADE
) STRICT;
INSERT INTO intent_changes_v17(intent_id, repo_id, change_id, head_oid, added_by, created_at, public_id)
SELECT ic.intent_id, i.repo_id, c.change_key, NULLIF(ic.head_oid, ''), i.reporter_id, i.created_at, printf('%026d', ic.rowid)
FROM intent_changes ic JOIN intents i ON i.id=ic.intent_id JOIN changes c ON c.id=ic.change_id;
DROP TABLE intent_changes;
ALTER TABLE intent_changes_v17 RENAME TO intent_changes;
CREATE INDEX idx_intent_changes_intent ON intent_changes(intent_id, id);
CREATE INDEX idx_intent_changes_change ON intent_changes(repo_id, change_id);

CREATE TABLE facets (
 id INTEGER PRIMARY KEY,
 public_id TEXT NOT NULL UNIQUE CHECK (length(public_id) = 26),
 repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
 key TEXT NOT NULL CHECK (key GLOB '[a-z][a-z0-9_]*' AND length(key) BETWEEN 1 AND 64),
 value_type TEXT NOT NULL CHECK (value_type IN ('enum','bool','int','text')),
 allowed TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(allowed)),
 required INTEGER NOT NULL DEFAULT 0 CHECK (required IN (0,1)),
 created_at INTEGER NOT NULL CHECK (created_at > 0), UNIQUE(repo_id,key)
) STRICT;
CREATE TABLE intent_facets (
 id INTEGER PRIMARY KEY,
 intent_id INTEGER NOT NULL REFERENCES intents(id) ON DELETE CASCADE ON UPDATE CASCADE,
 facet_id INTEGER NOT NULL REFERENCES facets(id) ON DELETE RESTRICT ON UPDATE CASCADE,
 value TEXT NOT NULL CHECK (json_valid(value)), updated_at INTEGER NOT NULL CHECK (updated_at > 0),
 UNIQUE(intent_id,facet_id)
) STRICT;
CREATE INDEX idx_facets_repo ON facets(repo_id,key);
CREATE INDEX idx_intent_facets_intent ON intent_facets(intent_id,facet_id);
CREATE INDEX idx_intent_facets_facet ON intent_facets(facet_id,value);
INSERT INTO facets(public_id,repo_id,key,value_type,allowed,required,created_at)
SELECT printf('%026X', repos.id*16+1), repos.id, 'risk', 'enum', '["low","medium","high"]', 0, strftime('%s','now')*1000 FROM repos
UNION ALL SELECT printf('%026X', repos.id*16+2), repos.id, 'area', 'enum', '["backend","frontend","docs","infra"]', 0, strftime('%s','now')*1000 FROM repos
UNION ALL SELECT printf('%026X', repos.id*16+3), repos.id, 'size', 'enum', '["xs","s","m","l"]', 0, strftime('%s','now')*1000 FROM repos;

CREATE TABLE IF NOT EXISTS comments (
 id INTEGER PRIMARY KEY,
 public_id TEXT NOT NULL UNIQUE CHECK (length(public_id)=26),
 repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
 intent_id INTEGER NULL,
 change_id TEXT NULL CHECK (change_id IS NULL OR length(change_id) BETWEEN 1 AND 128),
 head_oid TEXT NULL CHECK (head_oid IS NULL OR length(head_oid)=64),
 path TEXT NULL CHECK (path IS NULL OR length(path) BETWEEN 1 AND 1024),
 line INTEGER NULL CHECK (line IS NULL OR line>0),
 line_end INTEGER NULL CHECK (line_end IS NULL OR line_end>=line),
 remapped_from TEXT NULL CHECK (remapped_from IS NULL OR length(remapped_from)=64),
 unmatched INTEGER NOT NULL DEFAULT 0 CHECK (unmatched IN (0,1)),
 body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 32768),
 author_id INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
 created_at INTEGER NOT NULL CHECK (created_at>0), updated_at INTEGER NOT NULL CHECK (updated_at>0),
 CHECK (change_id IS NOT NULL OR head_oid IS NULL),
 CHECK ((change_id IS NULL) OR (path IS NOT NULL AND line IS NOT NULL) OR (path IS NULL AND line IS NULL)),
 FOREIGN KEY(intent_id,repo_id) REFERENCES intents(id,repo_id) ON DELETE CASCADE ON UPDATE CASCADE
) STRICT;
CREATE INDEX IF NOT EXISTS idx_comments_intent_change ON comments(repo_id,change_id,path,line);
CREATE INDEX IF NOT EXISTS idx_comments_intent_ref ON comments(intent_id,created_at);
CREATE INDEX IF NOT EXISTS idx_comments_unmatched_intent ON comments(repo_id,change_id) WHERE unmatched=1;

DROP INDEX IF EXISTS idx_asks_intent;
ALTER TABLE asks RENAME TO asks_v13;
CREATE TABLE asks (
 id INTEGER PRIMARY KEY,
 public_id TEXT NOT NULL UNIQUE CHECK(length(public_id)=26),
 repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE ON UPDATE CASCADE,
 intent_id INTEGER NOT NULL,
 question TEXT NOT NULL CHECK(length(question) BETWEEN 1 AND 8192),
 options TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(options)),
 blocks_claim INTEGER NOT NULL DEFAULT 0 CHECK(blocks_claim IN (0,1)),
 state TEXT NOT NULL DEFAULT 'asked' CHECK(state IN ('asked','answered','expired')),
 answer TEXT NULL CHECK(answer IS NULL OR length(answer) BETWEEN 1 AND 8192),
 answer_by INTEGER NULL REFERENCES users(id) ON DELETE SET NULL ON UPDATE CASCADE,
 deadline_ms INTEGER NOT NULL CHECK(deadline_ms>0),
 created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT ON UPDATE CASCADE,
 created_at INTEGER NOT NULL CHECK(created_at>0),
 answered_at INTEGER NULL CHECK(answered_at IS NULL OR answered_at>0),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 CHECK((state='answered')=(answer IS NOT NULL AND answered_at IS NOT NULL)),
 CHECK((state IN ('asked','expired')) OR answer_by IS NOT NULL),
 FOREIGN KEY(intent_id,repo_id) REFERENCES intents(id,repo_id) ON DELETE CASCADE ON UPDATE CASCADE
) STRICT;
INSERT INTO asks(id,public_id,repo_id,intent_id,question,options,blocks_claim,state,answer,answer_by,deadline_ms,created_by,created_at,answered_at)
SELECT a.id,a.public_id,i.repo_id,a.intent_id,a.question,a.options,a.blocks_claim,
a.state,
CASE WHEN a.state='answered' THEN COALESCE(NULLIF(a.answer,''),'[legacy answer]') ELSE NULL END,a.answered_by,
COALESCE(a.expires_at,a.created_at+259200000),a.created_by,a.created_at,
CASE WHEN a.state='answered' THEN a.created_at ELSE NULL END
FROM asks_v13 a JOIN intents i ON i.id=a.intent_id;
DROP TABLE asks_v13;
CREATE INDEX idx_asks_intent ON asks(intent_id,state,deadline_ms);
CREATE INDEX idx_asks_deadline ON asks(state,deadline_ms) WHERE state='asked';
