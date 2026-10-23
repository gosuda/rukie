-- 0014_comment_kind.sql: mark review verdicts apart from plain comments so
-- only review endpoint records count toward approval requirements.
ALTER TABLE change_comments ADD COLUMN kind TEXT NOT NULL DEFAULT 'comment'
CHECK (kind IN ('comment', 'review'));
