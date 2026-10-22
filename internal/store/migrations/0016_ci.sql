CREATE TABLE ci_controller (id INTEGER PRIMARY KEY CHECK (id = 1), epoch INTEGER NOT NULL, fence INTEGER NOT NULL, state BLOB NOT NULL);
INSERT INTO ci_controller (id, epoch, fence, state) VALUES (1, 0, 0, '{}');
