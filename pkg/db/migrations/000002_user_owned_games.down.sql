-- Reverse of 000002. The deleted anonymous rows cannot be recovered.

ALTER TABLE games DROP CONSTRAINT IF EXISTS games_user_id_positive;

ALTER TABLE games ADD COLUMN session_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_games_session_id ON games (session_id) WHERE user_id = 0;
