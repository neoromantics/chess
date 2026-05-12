-- Platform expansion: PvP, Glicko-2 ratings, time controls, invites.
--
-- Strategy: additive. We keep games.user_id around for one more release so
-- old code paths still resolve; 000004 will drop it after handlers fully
-- migrate to white_user_id / black_user_id.

-- Glicko-2 rating fields. Defaults match the standard Glicko-2 priors so
-- a new user's first opponent doesn't see absurd RD swings.
ALTER TABLE users
  ADD COLUMN rating       REAL    NOT NULL DEFAULT 1500,
  ADD COLUMN rd           REAL    NOT NULL DEFAULT 350,
  ADD COLUMN volatility   REAL    NOT NULL DEFAULT 0.06,
  ADD COLUMN games_played INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN wins         INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN losses       INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN draws        INTEGER NOT NULL DEFAULT 0;

-- Dual-player games. white/black_user_id are NULL when that side is the
-- engine, which gives us a single games table for engine + PvP play.
ALTER TABLE games
  ADD COLUMN white_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN black_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN time_control  TEXT    NOT NULL DEFAULT 'engine',
  ADD COLUMN rated         BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN result        TEXT    NOT NULL DEFAULT '*';
  -- result: '1-0' | '0-1' | '1/2-1/2' | '*' (ongoing/unknown)

-- Backfill existing single-user games: the human played whichever side is
-- not the engine. (If both engine_white and engine_black were true, neither
-- column gets set — those are engine-vs-engine demos and stay unowned.)
UPDATE games SET white_user_id = user_id WHERE user_id > 0 AND engine_white = FALSE;
UPDATE games SET black_user_id = user_id WHERE user_id > 0 AND engine_black = FALSE;

-- Either side must be a real user OR an engine. No "ghost" games.
ALTER TABLE games
  ADD CONSTRAINT games_has_player CHECK (
    white_user_id IS NOT NULL OR engine_white = TRUE
  ),
  ADD CONSTRAINT games_has_opponent CHECK (
    black_user_id IS NOT NULL OR engine_black = TRUE
  );

-- Hot indexes for the upcoming matchmaking & profile views.
CREATE INDEX IF NOT EXISTS games_white_user_idx ON games(white_user_id) WHERE white_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS games_black_user_idx ON games(black_user_id) WHERE black_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS games_updated_idx   ON games(updated_at DESC);

-- Invites: one row per direct-challenge attempt. Status & expires_at let
-- us reason about 60s TTL and surface pending invites in the UI without
-- touching Redis (Redis is the realtime delivery channel; PG is the audit
-- trail and survives reconnects).
CREATE TABLE invites (
  id           UUID PRIMARY KEY,
  from_user_id BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  to_user_id   BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  time_control TEXT        NOT NULL,
  rated        BOOLEAN     NOT NULL,
  status       TEXT        NOT NULL,
  game_id      TEXT        REFERENCES games(id) ON DELETE SET NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at   TIMESTAMPTZ NOT NULL,
  resolved_at  TIMESTAMPTZ,
  CHECK (from_user_id <> to_user_id),
  CHECK (status IN ('pending', 'accepted', 'declined', 'expired', 'cancelled'))
);

CREATE INDEX invites_to_pending_idx
  ON invites(to_user_id, created_at DESC)
  WHERE status = 'pending';

CREATE INDEX invites_from_pending_idx
  ON invites(from_user_id, created_at DESC)
  WHERE status = 'pending';

-- Sweeper helper: pending invites whose expires_at has passed should be
-- swept by a leader-elected goroutine. Indexing on expires_at keeps that
-- query cheap.
CREATE INDEX invites_expiry_idx
  ON invites(expires_at)
  WHERE status = 'pending';
