-- Idempotent schema. Embedded into the binary and applied on every
-- service boot via OpenPostgres() under a Postgres advisory lock so
-- multiple replicas can race the apply safely. Edits should prefer
-- additive ADD COLUMN IF NOT EXISTS over table rewrites; column drops
-- need a separate, deliberate one-off migration.
CREATE TABLE IF NOT EXISTS users (
    id              BIGSERIAL PRIMARY KEY,
    username        TEXT      UNIQUE NOT NULL,
    password_hash   TEXT      NOT NULL,
    display_name    TEXT      NOT NULL DEFAULT '',
    avatar_url      TEXT      NOT NULL DEFAULT '',
    country         TEXT      NOT NULL DEFAULT '',
    is_premium      BOOLEAN   NOT NULL DEFAULT FALSE,
    elo             INTEGER   NOT NULL DEFAULT 1200,
    bio             TEXT      NOT NULL DEFAULT '',
    last_login      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    rating          REAL    NOT NULL DEFAULT 1500,
    rd              REAL    NOT NULL DEFAULT 350,
    volatility      REAL    NOT NULL DEFAULT 0.06,
    games_played    INTEGER NOT NULL DEFAULT 0,
    wins            INTEGER NOT NULL DEFAULT 0,
    losses          INTEGER NOT NULL DEFAULT 0,
    draws           INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS games (
    id                TEXT      PRIMARY KEY,
    session_id        TEXT      NOT NULL DEFAULT '',
    fen               TEXT      NOT NULL,
    history           TEXT      NOT NULL DEFAULT '[]',
    history_san       TEXT      NOT NULL DEFAULT '[]',
    engine_white      BOOLEAN   NOT NULL,
    engine_black      BOOLEAN   NOT NULL,
    white_think_time  INTEGER   NOT NULL DEFAULT 1000,
    black_think_time  INTEGER   NOT NULL DEFAULT 1000,
    status            TEXT      NOT NULL,
    assessments       TEXT      NOT NULL DEFAULT '[]',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    white_user_id     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    black_user_id     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    time_control      TEXT    NOT NULL DEFAULT 'engine',
    rated             BOOLEAN NOT NULL DEFAULT FALSE,
    result            TEXT    NOT NULL DEFAULT '*',
    -- Empty string = "standard starting position" (the common case).
    -- A non-empty FEN means the game was set up via the board editor
    -- and history replays from there. Required for the editor feature
    -- because rec.FEN is the *current* position; without start_fen we
    -- can't replay subsequent moves correctly.
    start_fen         TEXT    NOT NULL DEFAULT ''
);
-- Idempotent add for clusters that predate the start_fen column.
ALTER TABLE games ADD COLUMN IF NOT EXISTS start_fen TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS invites (
  id           UUID PRIMARY KEY,
  from_user_id BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  to_user_id   BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  time_control TEXT        NOT NULL,
  rated        BOOLEAN     NOT NULL,
  status       TEXT        NOT NULL,
  game_id      TEXT        REFERENCES games(id) ON DELETE SET NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at   TIMESTAMPTZ NOT NULL,
  resolved_at  TIMESTAMPTZ
);
