ALTER TABLE pending_pushes ADD COLUMN enqueued INTEGER NOT NULL DEFAULT 0 CHECK (enqueued IN (0, 1));
