-- Games are now strictly user-owned: anonymous sessions cannot create
-- or access a game. This migration removes the session_id column and
-- drops any rows that were created via the (now retired) anonymous path.

DELETE FROM games WHERE user_id = 0;

DROP INDEX IF EXISTS idx_games_session_id;

ALTER TABLE games DROP COLUMN IF EXISTS session_id;

-- The handler enforces user_id > 0; this constraint protects the table
-- from a future bug accidentally writing a zero.
ALTER TABLE games ADD CONSTRAINT games_user_id_positive CHECK (user_id > 0);
