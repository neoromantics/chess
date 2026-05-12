-- Migration 000005: Restore legacy single-owner user_id column.
-- (Transitional only; data lost in DROP cannot be fully recovered from here)

ALTER TABLE games ADD COLUMN user_id BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_games_user_id ON games (user_id) WHERE user_id > 0;
