-- 0022_webhook_lease_owner.sql: fence webhook delivery completions on the claim owner.
-- All times are INTEGER Unix milliseconds.
ALTER TABLE webhook_messages ADD COLUMN lease_owner TEXT NOT NULL DEFAULT '';
