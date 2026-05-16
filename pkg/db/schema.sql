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
    draws           INTEGER NOT NULL DEFAULT 0,
    -- is_bot identifies bot users seeded by the engine-fallback
    -- matchmaker. They have a real users row (so games.black_user_id
    -- FK resolves and the SPA sees a "real" opponent) but never log in
    -- and are filtered out of invite search. See cmd/game/bots.go.
    -- TODO(matchmaker-engine-fallback): drop column when the fallback
    -- is removed (tagged in matchmaker.go).
    is_bot          BOOLEAN NOT NULL DEFAULT FALSE,
    -- Operator account flag. Read at /api/user/me so the SPA can show
    -- the /admin link, and gated on every /api/admin/* endpoint.
    -- Bootstrap by SQL (one-off):
    --   UPDATE users SET is_admin = TRUE WHERE username = '<you>';
    -- See infra/README + the ADMIN bootstrap note. Default FALSE so a
    -- fresh signup is never an admin.
    is_admin        BOOLEAN NOT NULL DEFAULT FALSE
);
-- Drop the pre-Glicko-2 elo column on clusters that still have it.
-- Never read by current code; superseded by rating/rd/volatility.
ALTER TABLE users DROP COLUMN IF EXISTS elo;
-- Idempotent add for clusters that predate the bot-pool column.
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_bot BOOLEAN NOT NULL DEFAULT FALSE;
-- Idempotent add for clusters that predate the admin flag.
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS games (
    id                TEXT      PRIMARY KEY,
    fen               TEXT      NOT NULL,
    history           TEXT      NOT NULL DEFAULT '[]',
    history_san       TEXT      NOT NULL DEFAULT '[]',
    engine_white      BOOLEAN   NOT NULL,
    engine_black      BOOLEAN   NOT NULL,
    white_think_time  INTEGER   NOT NULL DEFAULT 1000,
    black_think_time  INTEGER   NOT NULL DEFAULT 1000,
    status            TEXT      NOT NULL,
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
    start_fen         TEXT    NOT NULL DEFAULT '',
    -- Spectator mode opt-in. When TRUE, any signed-in user (and the
    -- /watch/{id} anonymous path) can read /api/state and subscribe
    -- to the per-game WS. Mutations still require participant ownership
    -- — userOwnsGame stays strict; only the *read* surface relaxes.
    is_public         BOOLEAN NOT NULL DEFAULT FALSE,
    -- Persisted per-ply move-assessment verdicts (JSON array of
    -- PlyAssessment). Written once at the end of an /api/analyze run
    -- so reopening a finished game shows the verdicts without re-
    -- running the engine searches. Empty '[]' = "never analyzed" or
    -- "history changed since last analysis" (cleared by the next
    -- save that mutates rec.History). See cmd/game/analysis.go.
    assessments       TEXT      NOT NULL DEFAULT '[]'
);
-- Idempotent add for clusters that predate the start_fen column.
ALTER TABLE games ADD COLUMN IF NOT EXISTS start_fen TEXT NOT NULL DEFAULT '';
-- Idempotent add for clusters that predate the spectator-mode column.
ALTER TABLE games ADD COLUMN IF NOT EXISTS is_public BOOLEAN NOT NULL DEFAULT FALSE;
-- Re-introduced 2026-05-16 (Phase 3 of move assessment). Earlier
-- platforms had dropped this column when only the streaming Phase-1
-- verdicts existed; persisted CP-loss verdicts now warrant bringing
-- it back. ADD ... IF NOT EXISTS so it's safe on clusters that still
-- have it AND on clusters that don't.
ALTER TABLE games ADD COLUMN IF NOT EXISTS assessments TEXT NOT NULL DEFAULT '[]';
-- Drop the pre-SPA session_id column on clusters that still have it.
-- The column was never read or written by the current code path; this
-- is the rare "deliberate, idempotent drop" CLAUDE.md permits.
ALTER TABLE games DROP COLUMN IF EXISTS session_id;

-- Index support for ListGames (WHERE white_user_id=$1 OR black_user_id=$1
-- ORDER BY updated_at DESC). Without these, every Match-page load and
-- every WS reconnect that lands on a cache-miss sequential-scans the
-- games table. At low N it's fine; the indices land us in O(log N + k)
-- for k matching rows, which matters as the table grows.
CREATE INDEX IF NOT EXISTS games_white_user_id_idx ON games (white_user_id) WHERE white_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS games_black_user_id_idx ON games (black_user_id) WHERE black_user_id IS NOT NULL;
-- updated_at is the sort key — DESC indices let ORDER BY use the index.
CREATE INDEX IF NOT EXISTS games_updated_at_idx ON games (updated_at DESC);
-- Status is the predicate for "active games" / "past games" filters on
-- the Match page; tiny cardinality so partial indices are noise here.
CREATE INDEX IF NOT EXISTS games_status_idx ON games (status);

-- Invite expiry sweeper scans pending rows with expires_at <= NOW();
-- a partial index on the pending subset is the minimum-cost win.
CREATE INDEX IF NOT EXISTS invites_pending_expires_idx ON invites (expires_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS invites_to_user_pending_idx ON invites (to_user_id, created_at DESC) WHERE status = 'pending';

-- The previously-dead users.elo + games.session_id columns are dropped
-- via the ALTER TABLE DROP COLUMN IF EXISTS statements above. (The
-- games.assessments column was dropped during Phase-1 cleanup but is
-- back as of Phase 3 — see the ADD COLUMN IF NOT EXISTS just above.)
-- Adding new columns: prefer ADD COLUMN IF NOT EXISTS so the apply
-- stays idempotent.

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
