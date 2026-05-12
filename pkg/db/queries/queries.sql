-- name: CreateUser :one
INSERT INTO users (username, password_hash, elo)
VALUES ($1, $2, 1200)
RETURNING id, username, password_hash, display_name, avatar_url, country,
          is_premium, elo, bio, last_login, created_at,
          rating, rd, volatility, games_played, wins, losses, draws;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, elo, bio, last_login, created_at,
       rating, rd, volatility, games_played, wins, losses, draws
FROM users
WHERE username = $1;

-- name: GetUserByID :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, elo, bio, last_login, created_at,
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

-- name: CountUserGames :one
-- Explicit BIGINT cast on $1 so sqlc infers int64 (not sql.NullInt64) for
-- the param — white_user_id is nullable but the user-id we filter by is not.
SELECT COUNT(*) FROM games
WHERE white_user_id = $1::BIGINT
   OR black_user_id = $1::BIGINT
   OR user_id       = $1::BIGINT;

-- name: CountUserWins :one
SELECT COUNT(*) FROM games
WHERE (white_user_id = $1::BIGINT AND result = '1-0')
   OR (black_user_id = $1::BIGINT AND result = '0-1')
   OR (user_id       = $1::BIGINT AND status = 'checkmate' AND result IN ('*', '1-0', '0-1'));

-- name: CountUserDraws :one
SELECT COUNT(*) FROM games
WHERE (white_user_id = $1::BIGINT OR black_user_id = $1::BIGINT OR user_id = $1::BIGINT)
  AND (result = '1/2-1/2'
       OR status IN ('stalemate', 'draw50', 'draw_repetition', 'draw_insufficient'));

-- name: UpsertGame :exec
-- white_user_id / black_user_id supersede user_id but we keep both populated
-- during the Phase 1 transition. user_id will be dropped in a later migration.
INSERT INTO games (
    id, user_id, white_user_id, black_user_id,
    fen, history, history_san,
    engine_white, engine_black, white_think_time, black_think_time,
    time_control, rated, status, result, assessments,
    created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT (id) DO UPDATE SET
    user_id          = EXCLUDED.user_id,
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
    assessments      = EXCLUDED.assessments,
    updated_at       = EXCLUDED.updated_at;

-- name: ListGames :many
-- Games where the user is on either side OR (transitional) the legacy
-- single-user owner. ORDER BY updated_at DESC matches the dashboard view.
SELECT id, user_id, white_user_id, black_user_id,
       fen, history, history_san,
       engine_white, engine_black, white_think_time, black_think_time,
       time_control, rated, status, result, assessments,
       created_at, updated_at
FROM games
WHERE white_user_id = $1::BIGINT
   OR black_user_id = $1::BIGINT
   OR user_id       = $1::BIGINT
ORDER BY updated_at DESC;

-- name: GetGame :one
SELECT id, user_id, white_user_id, black_user_id,
       fen, history, history_san,
       engine_white, engine_black, white_think_time, black_think_time,
       time_control, rated, status, result, assessments,
       created_at, updated_at
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

-- name: ExpireStaleInvites :execrows
-- Called periodically by the leader-elected invite sweeper. Returns the
-- count so the sweeper can publish per-invite expired events if desired.
UPDATE invites
SET status      = 'expired',
    resolved_at = NOW()
WHERE status = 'pending' AND expires_at <= NOW();
