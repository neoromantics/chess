-- Migration 000005: Drop legacy single-owner user_id column.
-- Handlers have migrated to the dual white_user_id / black_user_id model.

ALTER TABLE games DROP COLUMN IF EXISTS user_id;
DROP INDEX IF EXISTS idx_games_user_id;
