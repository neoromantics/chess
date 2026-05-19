-- name: CreateUser :one
INSERT INTO users (username, password_hash, created_at, last_login)
VALUES ($1, $2, NOW(), NOW())
RETURNING id, username, password_hash, display_name, avatar_url, country,
          is_premium, bio, last_login, created_at,
          rating, rd, volatility, games_played, wins, losses, draws, is_bot, is_admin;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, bio, last_login, created_at,
       rating, rd, volatility, games_played, wins, losses, draws, is_bot, is_admin
FROM users
WHERE username = $1;

-- name: GetUserByID :one
SELECT id, username, password_hash, display_name, avatar_url, country,
       is_premium, bio, last_login, created_at,
       rating, rd, volatility, games_played, wins, losses, draws, is_bot, is_admin
FROM users
WHERE id = $1;

-- name: SearchUsersByPrefix :many
-- For invite autocomplete. Case-insensitive prefix match, capped.
-- Bot users (is_bot=true) are excluded — they're internal stand-ins
-- for the engine-fallback matchmaker and shouldn't appear in invite
-- search results.
SELECT id, username, display_name, country, rating
FROM users
WHERE username ILIKE $1
  AND is_bot = FALSE
ORDER BY username
LIMIT 10;

-- name: UpsertBot :one
-- Idempotent bot seed. Runs once per game-service boot. ON CONFLICT
-- DO UPDATE (instead of DO NOTHING) is required for RETURNING to fire
-- on the already-seeded path; we only flip is_bot to TRUE (the rest
-- of the row is left intact). Returning id+username+rating so the
-- caller can populate its in-memory pool without a follow-up SELECT.
INSERT INTO users (username, password_hash, rating, is_bot)
VALUES ($1, $2, $3, TRUE)
ON CONFLICT (username) DO UPDATE
  SET is_bot = TRUE
RETURNING id, username, rating;

-- name: ListBots :many
-- Read-side of the bot pool. Game-service calls this at boot to warm
-- its in-memory bot cache. Cheap (small N, indexable predicate) and
-- only runs at startup; we don't poll.
SELECT id, username, rating
FROM users
WHERE is_bot = TRUE
ORDER BY rating;

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
--
-- Excludes rows where imported=TRUE. Those rows have been rewritten via
-- /api/load_pgn and carry whatever result the PGN encoded (often a famous
-- master game); counting them would let any user inflate their stats by
-- loading a winning PGN into one of their engine rows. The imported flag
-- is reset to FALSE whenever /api/new wipes the row, so a fresh game
-- played on the same row id starts counting again.
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
WHERE (white_user_id = $1::BIGINT OR black_user_id = $1::BIGINT)
  AND imported = FALSE;

-- name: UpsertGame :exec
INSERT INTO games (
    id, white_user_id, black_user_id,
    fen, history, history_san,
    engine_white, engine_black, white_think_time, black_think_time,
    time_control, rated, status, result,
    created_at, updated_at, start_fen, is_public, assessments, imported
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
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
    is_public        = EXCLUDED.is_public,
    assessments      = EXCLUDED.assessments,
    imported         = EXCLUDED.imported,
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
       created_at, updated_at, start_fen, is_public, assessments, imported
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
       created_at, updated_at, start_fen, is_public, assessments, imported
FROM games
WHERE id = $1;

-- name: SetGameVisibility :execrows
-- Owner-gated spectator toggle. Handler validates the caller is a
-- participant before invoking this; we still scope by id alone because
-- the predicate is row-id, not row-id+user.
UPDATE games SET is_public = $2 WHERE id = $1;

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

-- name: CountUsers :one
-- Two counts in one trip via FILTER, so the admin overview can show
-- "real signups" and "seeded bots" as separate numbers. The original
-- single-COUNT version inflated "Total users" by the 12-row bot pool;
-- splitting matches the is_bot=FALSE filter ListRecentSignups +
-- CountRecentSignups already apply.
SELECT
  COUNT(*) FILTER (WHERE is_bot = FALSE)::BIGINT AS humans,
  COUNT(*) FILTER (WHERE is_bot = TRUE)::BIGINT  AS bots
FROM users;

-- name: ListBotStats :many
-- Per-bot stats for the admin "Seeded bots" panel. Returns the static
-- rating set at seed time plus a games-played count derived live from
-- the games table (bot games are rated=false so users.games_played
-- never increments via the rating updater). The LEFT JOIN keeps bots
-- with zero games in the result.
SELECT u.id, u.username, u.rating,
       COUNT(g.id)::BIGINT AS games_played
FROM users u
LEFT JOIN games g
  ON g.white_user_id = u.id OR g.black_user_id = u.id
WHERE u.is_bot = TRUE
GROUP BY u.id, u.username, u.rating
ORDER BY u.rating;

-- name: CountRecentSignups :one
-- Two windows in one trip: signups in the last 24h and last 7d. The
-- FILTER clauses share the scan so this is cheaper than two separate
-- COUNTs and keeps the dashboard responsive even as users grows.
-- Excludes bots so the count reflects real product signups.
SELECT
  COUNT(*) FILTER (WHERE created_at > NOW() - INTERVAL '24 hours')::BIGINT AS day,
  COUNT(*) FILTER (WHERE created_at > NOW() - INTERVAL '7 days')::BIGINT  AS week
FROM users
WHERE is_bot = FALSE;

-- name: ListRecentSignups :many
-- The 20 most recent non-bot signups for the admin dashboard's
-- "Recent signups" panel. Returning rating so we can sanity-check
-- that the default 1500 didn't get scribbled in by an early-edit bug.
SELECT id, username, display_name, country, created_at, rating
FROM users
WHERE is_bot = FALSE
ORDER BY created_at DESC
LIMIT 20;

-- name: CountActiveGames :one
-- Games currently in flight. Used by the admin overview to spot a
-- regression where everything's "ongoing" because the finish-status
-- write path broke. Bot games count as active for this purpose —
-- they're a real load on engine-worker.
SELECT COUNT(*)::BIGINT AS active
FROM games
WHERE status = 'ongoing';

-- name: InsertAdminAction :exec
-- Audit row written by every destructive /api/admin/* call. Both the
-- actor's username and the target's username are denormalized at
-- write time so a later cleanup deleting either user doesn't void
-- the history. detail is a free-form JSON or plain string the handler
-- can set per-action ("confirm_username mismatch", "cascade rows=N").
INSERT INTO admin_actions (
    actor_user_id, actor_username, action,
    target_user_id, target_username, detail
)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListAdminActions :many
-- Most recent 50 actions, for the audit panel. Anything older is
-- archived implicitly by the index + LIMIT — we don't expect this
-- table to grow fast enough to need pagination yet.
SELECT id, actor_user_id, actor_username, action,
       target_user_id, target_username, detail, created_at
FROM admin_actions
ORDER BY created_at DESC
LIMIT 50;

-- name: DeleteUser :execrows
-- Hard-delete a user. Relies on the schema's existing FK behaviour:
--   games.{white,black}_user_id  ON DELETE SET NULL  (preserves the
--     game history with the slot anonymized — both players' replays
--     stay readable; the opponent sees "deleted user" on the seat).
--   invites.{from,to}_user_id    ON DELETE CASCADE   (pending invites
--     to/from the deleted user are removed outright).
-- Wrap in an audit-log write at the handler level; this query is
-- intentionally bare so the caller controls the transaction.
DELETE FROM users WHERE id = $1;

-- name: ListActiveGamesForAdmin :many
-- Active-games panel on /admin. Joins users for the player usernames
-- so the SPA renders "alice vs bob" without a second roundtrip. Caps
-- at 50: at our scale that's "every active game"; if we ever blow
-- past that, swap in cursor pagination.
SELECT g.id,
       g.white_user_id, g.black_user_id,
       uw.username AS white_username,
       ub.username AS black_username,
       g.time_control, g.rated,
       g.created_at, g.updated_at
FROM games g
LEFT JOIN users uw ON uw.id = g.white_user_id
LEFT JOIN users ub ON ub.id = g.black_user_id
WHERE g.status = 'ongoing'
ORDER BY g.updated_at DESC
LIMIT 50;

-- name: ListAdminActionsBefore :many
-- Pagination cursor for the audit panel. Pass a non-zero $1 to fetch
-- the next page; the first page calls ListAdminActions (no cursor).
-- The (created_at, id) tie-break keeps the order stable when two rows
-- share a created_at to the microsecond.
SELECT id, actor_user_id, actor_username, action,
       target_user_id, target_username, detail, created_at
FROM admin_actions
WHERE created_at < $1
ORDER BY created_at DESC
LIMIT 50;

-- ===== Studies =====

-- name: CreateStudy :one
-- Insert a new study. tree is a JSON blob (root node shape is
-- {"children": [...]}); the handler validates the shape before passing
-- it through. source_game_id + source_ply are optional — leave them
-- NULL for a pure "save setup" flow that didn't come from a game.
INSERT INTO studies (user_id, name, start_fen, tree, source_game_id, source_ply, position_label)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, user_id, name, start_fen, tree, source_game_id, source_ply, is_public, position_label, created_at, updated_at;

-- name: GetStudy :one
-- Fetch one study by id. The handler is responsible for the ownership
-- check (existence-leak rule: non-owner sees 404, not 403) — this
-- query doesn't scope by user_id so the handler can return the
-- distinct "missing row" vs "wrong owner" cases internally for
-- telemetry while presenting both as 404 externally.
SELECT id, user_id, name, start_fen, tree, source_game_id, source_ply, is_public, position_label, created_at, updated_at
FROM studies
WHERE id = $1;

-- name: ListStudiesForUser :many
-- Studies belonging to one user, newest first. tree is included so
-- the SPA list view can render a position preview without a second
-- round-trip; if the list ever grows large enough that the JSON
-- payload is the bottleneck, switch to a tree-omitting projection.
SELECT id, user_id, name, start_fen, tree, source_game_id, source_ply, is_public, position_label, created_at, updated_at
FROM studies
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT 200;

-- name: UpdateStudy :execrows
-- Partial-update: name + tree + position_label (the other fields are
-- immutable after creation). Scoped by id AND user_id so a non-owner
-- can't modify someone else's row by guessing the UUID — affecting 0
-- rows is the silent rejection. updated_at bumps on any change.
UPDATE studies
SET name = $3, tree = $4, position_label = $5, updated_at = NOW()
WHERE id = $1 AND user_id = $2;

-- name: SetStudyVisibility :execrows
-- Owner-only toggle for the is_public flag (powers the "shareable
-- link" UX). Same anti-leak shape as UpdateStudy: non-owner = 0 rows
-- affected = silent rejection.
UPDATE studies
SET is_public = $3, updated_at = NOW()
WHERE id = $1 AND user_id = $2;

-- name: DeleteStudy :execrows
-- Hard-delete, scoped by id AND user_id. Same anti-leak pattern as
-- UpdateStudy — affected rows == 0 means "not yours or not found."
DELETE FROM studies
WHERE id = $1 AND user_id = $2;
