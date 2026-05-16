-- name: CreateUser :one
INSERT INTO users (username, password_hash, created_at, last_login)
VALUES ($1, $2, NOW(), NOW())
RETURNING id, username, password_hash, display_name, avatar_url, country,
          is_premium, bio, last_login, created_at,
          rating, rd, volatility, games_played, wins, losses, draws;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, bio, last_login, created_at,
       rating, rd, volatility, games_played, wins, losses, draws
FROM users
WHERE username = $1;

-- name: GetUserByID :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, bio, last_login, created_at,
       rating, rd, volatility, games_played, wins, losses, draws
FROM users
WHERE id = $1;

-- name: SearchUsersByPrefix :many
-- For invite autocomplete. Case-insensitive prefix match, capped.
SELECT id, username, display_name, country, rating
FROM users
WHERE username ILIKE $1
ORDER BY username
LIMIT 10;

-- name: UpdateUserProfile :exec
UPDATE users
SET display_name = $2,
    bio          = $3,
    avatar_url   = $4,
    country      = $5
WHERE id = $1;

-- name: UpdateLastLogin :exec
UPDATE users SET last_login = NOW() WHERE id = $1;

-- name: UpdatePassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: UpdateUserRating :exec
-- Glicko-2 outcome write. Called by the leader-elected rating updater
-- after a rated game completes. All four fields move atomically so we
-- never expose a half-updated rating to readers.
UPDATE users
SET rating       = $2,
    rd           = $3,
    volatility   = $4,
    games_played = games_played + 1,
    wins         = wins   + $5,
    losses       = losses + $6,
    draws        = draws  + $7
WHERE id = $1;

-- name: CountUserGameStats :one
-- Single-trip stats aggregation. Replaces the prior three separate COUNT
-- queries (played / wins / draws). FILTER clauses run inside the same
-- table scan, so the planner reads the games rows once and emits four
-- counters; losses are derived on the Go side as played-(wins+draws).
-- Explicit BIGINT casts so sqlc infers int64 (not sql.NullInt64).
SELECT
  COUNT(*) AS played,
  COUNT(*) FILTER (
    WHERE (white_user_id = $1::BIGINT AND result = '1-0')
       OR (black_user_id = $1::BIGINT AND result = '0-1')
  ) AS wins,
  COUNT(*) FILTER (
    WHERE result = '1/2-1/2'
       OR status IN ('stalemate', 'draw50', 'draw_repetition', 'draw_insufficient')
  ) AS draws
FROM games
WHERE white_user_id = $1::BIGINT
   OR black_user_id = $1::BIGINT;

-- name: UpsertGame :exec
INSERT INTO games (
    id, white_user_id, black_user_id,
    fen, history, history_san,
    engine_white, engine_black, white_think_time, black_think_time,
    time_control, rated, status, result,
    created_at, updated_at, start_fen
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
ON CONFLICT (id) DO UPDATE SET
    white_user_id    = EXCLUDED.white_user_id,
    black_user_id    = EXCLUDED.black_user_id,
    fen              = EXCLUDED.fen,
    history          = EXCLUDED.history,
    history_san      = EXCLUDED.history_san,
    engine_white     = EXCLUDED.engine_white,
    engine_black     = EXCLUDED.engine_black,
    white_think_time = EXCLUDED.white_think_time,
    black_think_time = EXCLUDED.black_think_time,
    time_control     = EXCLUDED.time_control,
    rated            = EXCLUDED.rated,
    status           = EXCLUDED.status,
    result           = EXCLUDED.result,
    start_fen        = EXCLUDED.start_fen,
    updated_at       = EXCLUDED.updated_at;

-- name: ListGames :many
-- Games where the user is on either side. Cursor-paginated: callers pass
-- $2 as "newer than" (typically the last seen updated_at) or NOW() for the
-- first page, and $3 as the page size. NULL/zero $2 falls back to NOW().
-- The (updated_at, id) tie-break keeps pagination stable when multiple
-- rows share an updated_at.
SELECT id, white_user_id, black_user_id,
       fen, history, history_san,
       engine_white, engine_black, white_think_time, black_think_time,
       time_control, rated, status, result,
       created_at, updated_at, start_fen
FROM games
WHERE (white_user_id = $1::BIGINT OR black_user_id = $1::BIGINT)
  AND updated_at < COALESCE($2::TIMESTAMPTZ, NOW())
ORDER BY updated_at DESC, id DESC
LIMIT $3::INT;

-- name: GetGame :one
SELECT id, white_user_id, black_user_id,
       fen, history, history_san,
       engine_white, engine_black, white_think_time, black_think_time,
       time_control, rated, status, result,
       created_at, updated_at, start_fen
FROM games
WHERE id = $1;

-- name: DeleteGame :execrows
-- Authorization is enforced by the handler via getGame() before this runs,
-- so we delete strictly by primary key.
DELETE FROM games
WHERE id = $1;

-- === INVITES ===
-- Direct user-to-user challenges. PG row is the durable record so a
-- recipient who's offline sees the invite when they reconnect; Redis
-- pub/sub on user.evt.{id} is the realtime push when they're online.

-- name: CreateInvite :one
INSERT INTO invites (id, from_user_id, to_user_id, time_control, rated, status, expires_at)
VALUES ($1, $2, $3, $4, $5, 'pending', $6)
RETURNING id, from_user_id, to_user_id, time_control, rated, status, game_id,
          created_at, expires_at, resolved_at;

-- name: GetInvite :one
SELECT id, from_user_id, to_user_id, time_control, rated, status, game_id,
       created_at, expires_at, resolved_at
FROM invites
WHERE id = $1;

-- name: ListPendingInvitesForUser :many
-- The reconnect-handshake backlog query. Returns invites the user hasn't
-- yet acted on, newest first.
SELECT id, from_user_id, to_user_id, time_control, rated, status, game_id,
       created_at, expires_at, resolved_at
FROM invites
WHERE to_user_id = $1 AND status = 'pending' AND expires_at > NOW()
ORDER BY created_at DESC;

-- name: ListPendingInvitesFromUser :many
SELECT id, from_user_id, to_user_id, time_control, rated, status, game_id,
       created_at, expires_at, resolved_at
FROM invites
WHERE from_user_id = $1 AND status = 'pending' AND expires_at > NOW()
ORDER BY created_at DESC;

-- name: AcceptInvite :execrows
-- Atomic accept: only the recipient may accept, and only while pending.
-- game_id is recorded so both clients can navigate to the new game.
UPDATE invites
SET status      = 'accepted',
    game_id     = $3,
    resolved_at = NOW()
WHERE id = $1 AND to_user_id = $2 AND status = 'pending' AND expires_at > NOW();

-- name: DeclineInvite :execrows
UPDATE invites
SET status      = 'declined',
    resolved_at = NOW()
WHERE id = $1 AND to_user_id = $2 AND status = 'pending';

-- name: CancelInvite :execrows
-- Sender-initiated cancel; e.g. they closed the tab or sent by mistake.
UPDATE invites
SET status      = 'cancelled',
    resolved_at = NOW()
WHERE id = $1 AND from_user_id = $2 AND status = 'pending';

-- name: ExpireStaleInvites :many
-- Called periodically by the leader-elected invite sweeper. RETURNING
-- gives us the affected rows in one trip so we can publish per-invite
-- expired events without a second SELECT.
UPDATE invites
SET status      = 'expired',
    resolved_at = NOW()
WHERE status = 'pending' AND expires_at <= NOW()
RETURNING id, from_user_id, to_user_id, time_control, rated, status, game_id,
          created_at, expires_at, resolved_at;
