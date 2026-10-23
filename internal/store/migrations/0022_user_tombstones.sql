-- 0022_user_tombstones.sql: delayed user deletion behind admin.user.delete.
-- Soft-deleted users keep their row with is_active = 0 until purge_after,
-- when the sweeper hard-deletes them. Existing is_active gates already
-- refuse logins, tokens, and admin actions for inactive users.
ALTER TABLE users ADD COLUMN deleted_at INTEGER NULL CHECK (deleted_at IS NULL OR deleted_at > 0);
ALTER TABLE users ADD COLUMN purge_after INTEGER NULL CHECK (purge_after IS NULL OR purge_after > 0);

CREATE INDEX idx_users_deleted ON users(deleted_at);
