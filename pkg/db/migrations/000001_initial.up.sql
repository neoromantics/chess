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
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS games (
    id                TEXT      PRIMARY KEY,
    user_id           BIGINT    NOT NULL DEFAULT 0,
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
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_games_user_id    ON games (user_id)    WHERE user_id > 0;
CREATE INDEX IF NOT EXISTS idx_games_session_id ON games (session_id) WHERE user_id = 0;
CREATE INDEX IF NOT EXISTS idx_games_updated_at ON games (updated_at DESC);
