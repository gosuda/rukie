PRAGMA application_id = 1381321545;

CREATE TABLE outbox (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  public_id   TEXT    NOT NULL UNIQUE,
  kind        TEXT    NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
  body        TEXT    NOT NULL CHECK (json_valid(body)),
  occurred_at INTEGER NOT NULL CHECK (occurred_at > 0)
) STRICT;

CREATE TABLE outbox_consumers (
  name       TEXT    PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 64),
  cursor     INTEGER NOT NULL DEFAULT 0 CHECK (cursor >= 0),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0)
) STRICT;
