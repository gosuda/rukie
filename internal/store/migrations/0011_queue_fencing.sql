-- 0011_queue_fencing.sql: lease-token fencing, backoff scheduling, and
-- dead-lettering for the job queue; drops the follower_seen audit table.
--
-- Numbers are assigned centrally: F1 owns 0007, F12 owns 0008, F4 owns
-- 0009, F10 owns 0010. Gaps between applied versions are expected while
-- lanes land out of order; see loadMigrations.
ALTER TABLE jobs ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN run_after INTEGER NOT NULL DEFAULT 0 CHECK (run_after >= 0);
DROP TABLE follower_seen;
