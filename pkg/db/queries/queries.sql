-- name: CreateUser :one
INSERT INTO users (username, password_hash, elo)
VALUES ($1, $2, 1200)
RETURNING id, username, password_hash, display_name, avatar_url, country,
          is_premium, elo, bio, last_login, created_at;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, elo, bio, last_login, created_at
FROM users
WHERE username = $1;

-- name: GetUserByID :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, elo, bio, last_login, created_at
FROM users
WHERE id = $1;

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

-- name: CountUserGames :one
SELECT COUNT(*) FROM games WHERE user_id = $1;

-- name: CountUserWins :one
SELECT COUNT(*) FROM games WHERE user_id = $1 AND status = 'checkmate';

-- name: CountUserDraws :one
SELECT COUNT(*) FROM games
WHERE user_id = $1
  AND status IN ('stalemate', 'draw50', 'draw_repetition', 'draw_insufficient');

-- name: UpsertGame :exec
INSERT INTO games (
    id, user_id, fen, history, history_san,
    engine_white, engine_black, white_think_time, black_think_time,
    status, assessments, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (id) DO UPDATE SET
    user_id          = EXCLUDED.user_id,
    fen              = EXCLUDED.fen,
    history          = EXCLUDED.history,
    history_san      = EXCLUDED.history_san,
    engine_white     = EXCLUDED.engine_white,
    engine_black     = EXCLUDED.engine_black,
    white_think_time = EXCLUDED.white_think_time,
    black_think_time = EXCLUDED.black_think_time,
    status           = EXCLUDED.status,
    assessments      = EXCLUDED.assessments,
    updated_at       = EXCLUDED.updated_at;

-- name: ListGames :many
SELECT id, user_id, fen, history, history_san,
       engine_white, engine_black, white_think_time, black_think_time,
       status, assessments, created_at, updated_at
FROM games
WHERE user_id = $1
ORDER BY updated_at DESC;

-- name: GetGame :one
SELECT id, user_id, fen, history, history_san,
       engine_white, engine_black, white_think_time, black_think_time,
       status, assessments, created_at, updated_at
FROM games
WHERE id = $1;

-- name: DeleteGame :execrows
-- Authorization is enforced by the handler via getGame() before this runs,
-- so we delete strictly by primary key. Re-filtering by user_id here was a
-- footgun for anonymous games that became owned (or vice-versa) across a
-- login/logout boundary — the row matches authz but not the DELETE filter,
-- and the API silently 204'd with nothing deleted.
DELETE FROM games
WHERE id = $1;
