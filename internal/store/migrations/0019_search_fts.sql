CREATE VIRTUAL TABLE repos_fts USING fts5(body, tokenize='trigram case_sensitive 0');
CREATE VIRTUAL TABLE users_fts USING fts5(body, tokenize='trigram case_sensitive 0');
CREATE TRIGGER repos_fts_after_insert AFTER INSERT ON repos BEGIN
  INSERT INTO repos_fts(rowid, body) VALUES (NEW.id, lower(NEW.name || ' ' || coalesce(NEW.description, '') || ' ' || coalesce(NEW.topics, '') || ' ' || coalesce(NEW.website, '')));
END;
CREATE TRIGGER repos_fts_after_update AFTER UPDATE ON repos BEGIN
  UPDATE repos_fts SET body = lower(NEW.name || ' ' || coalesce(NEW.description, '') || ' ' || coalesce(NEW.topics, '') || ' ' || coalesce(NEW.website, '')) WHERE rowid = NEW.id;
END;
CREATE TRIGGER repos_fts_after_delete AFTER DELETE ON repos BEGIN
  DELETE FROM repos_fts WHERE rowid = OLD.id;
END;
CREATE TRIGGER users_fts_after_insert AFTER INSERT ON users BEGIN
  INSERT INTO users_fts(rowid, body) VALUES (NEW.id, lower(NEW.name || ' ' || coalesce(NEW.full_name, '')));
END;
CREATE TRIGGER users_fts_after_update AFTER UPDATE ON users BEGIN
  UPDATE users_fts SET body = lower(NEW.name || ' ' || coalesce(NEW.full_name, '')) WHERE rowid = NEW.id;
END;
CREATE TRIGGER users_fts_after_delete AFTER DELETE ON users BEGIN
  DELETE FROM users_fts WHERE rowid = OLD.id;
END;
INSERT INTO repos_fts(rowid, body) SELECT id, lower(name || ' ' || coalesce(description, '') || ' ' || coalesce(topics, '') || ' ' || coalesce(website, '')) FROM repos;
INSERT INTO users_fts(rowid, body) SELECT id, lower(name || ' ' || coalesce(full_name, '')) FROM users;
