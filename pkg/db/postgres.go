package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/neoromantics/chess/pkg/db/gen"
)

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// schemaSQL is the canonical schema, embedded into the binary at build
// time. Applied on every OpenPostgres() call under a Postgres advisory
// lock so multiple replicas can race the apply safely. Idempotent —
// every CREATE uses IF NOT EXISTS.
//
//go:embed schema.sql
var schemaSQL string

// withStatementTimeout appends a per-session statement_timeout to the
// DSN so a runaway SELECT (missing index, bad cursor, planner blowup)
// can't pin a pool connection forever. Postgres aborts the offending
// query, lib/pq surfaces it as an error, and the connection returns
// to the pool. If the operator already passed an `options` query param
// we respect it (assume they know what they're doing).
//
// 5s default is loose enough for any indexed lookup or paginated list
// but tight enough that a missing-index regression pages someone
// quickly instead of starving the pool silently.
func withStatementTimeout(dsn string, timeoutMS int) string {
	if timeoutMS <= 0 {
		return dsn
	}
	if !(strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")) {
		return dsn
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	q := u.Query()
	if q.Get("options") != "" {
		return dsn
	}
	q.Set("options", fmt.Sprintf("-c statement_timeout=%d", timeoutMS))
	u.RawQuery = q.Encode()
	return u.String()
}

// schemaLockID is an arbitrary 64-bit constant used with
// pg_advisory_lock() to serialize schema application across racing
// replicas. The number itself is meaningless; only its uniqueness
// within the lock keyspace matters. (This service uses no other
// advisory locks today, so collision risk is zero.)
const schemaLockID int64 = 0x4348455353534348 // "CHESSSCH" in ASCII

// PostgresStore is the sqlc-backed implementation of Store.
// All queries are generated from pkg/db/queries/*.sql into pkg/db/gen.
type PostgresStore struct {
	db *sql.DB
	q  *gen.Queries
}

// OpenPostgres opens a pooled connection to Postgres and returns a ready-to-use Store.
//
// Pool sizing here matters for distributed deployments: many small API
// pods sharing one Postgres mean we must cap each pod's open connections
// or we'll DoS the database under load. Only gateway and game-service
// open a pool (engine-worker is PG-free), so at HPA max we have
// 8 + 6 = 14 PG-using pods. With chess-db raised to max_connections=500
// the safe per-pod ceiling is 500 / 14 ≈ 35; we leave a buffer for
// PG-internal reservations (superuser + autovacuum). PG_MAX_OPEN_CONNS
// / PG_MAX_IDLE_CONNS lets ops tune without a code change when the
// topology shifts.
func OpenPostgres(dsn string) (Store, error) {
	dsn = withStatementTimeout(dsn, envInt("PG_STATEMENT_TIMEOUT_MS", 5000))

	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres: %w", err)
	}

	sqlDB.SetMaxOpenConns(envInt("PG_MAX_OPEN_CONNS", 30))
	sqlDB.SetMaxIdleConns(envInt("PG_MAX_IDLE_CONNS", 10))
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}

	if err := applySchema(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	return &PostgresStore{db: sqlDB, q: gen.New(sqlDB)}, nil
}

// applySchema runs the embedded schema.sql against the connected DB.
// Wrapped in a session-scoped advisory lock so racing replicas (e.g.
// two user-service pods coming up after a redeploy) serialize the
// apply. The schema itself uses CREATE TABLE IF NOT EXISTS so the
// non-leader pod's apply is a no-op rather than an error.
func applySchema(sqlDB *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	// Disable the global statement_timeout for this session: pg_advisory_lock
	// can block on a peer replica's schema apply, and a large CREATE INDEX
	// in schema.sql can easily exceed 5s on cold start. The ctx timeout
	// above (30s) is the real ceiling here.
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = 0"); err != nil {
		return fmt.Errorf("clear statement_timeout: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", schemaLockID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() {
		// Best-effort release; conn.Close() will free the lock anyway
		// via session end if this fails.
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", schemaLockID)
	}()

	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("execute schema: %w", err)
	}
	slog.Info("schema applied", "bytes", len(schemaSQL))
	return nil
}

func (s *PostgresStore) Close() error { return s.db.Close() }
func (s *PostgresStore) Ping() error  { return s.db.Ping() }

// === USERS ===

func (s *PostgresStore) CreateUser(username, passwordHash string) (*User, error) {
	row, err := s.q.CreateUser(context.Background(), gen.CreateUserParams{
		Username:     username,
		PasswordHash: passwordHash,
	})
	if err != nil {
		return nil, err
	}
	return userFromRow(row), nil
}

func (s *PostgresStore) GetUserByUsername(username string) (*User, error) {
	row, err := s.q.GetUserByUsername(context.Background(), username)
	if err != nil {
		return nil, err
	}
	return userFromRow(row), nil
}

func (s *PostgresStore) GetUserByID(id int64) (*User, error) {
	row, err := s.q.GetUserByID(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return userFromRow(row), nil
}

func (s *PostgresStore) SearchUsersByPrefix(prefix string) ([]UserSummary, error) {
	rows, err := s.q.SearchUsersByPrefix(context.Background(), prefix+"%")
	if err != nil {
		return nil, err
	}
	out := make([]UserSummary, len(rows))
	for i, r := range rows {
		out[i] = UserSummary{
			ID:          r.ID,
			Username:    r.Username,
			DisplayName: r.DisplayName,
			Country:     r.Country,
			Rating:      int(r.Rating),
		}
	}
	return out, nil
}

func (s *PostgresStore) UpdateUserProfile(id int64, displayName, bio, avatarURL, country string) error {
	return s.q.UpdateUserProfile(context.Background(), gen.UpdateUserProfileParams{
		ID:          id,
		DisplayName: displayName,
		Bio:         bio,
		AvatarUrl:   avatarURL,
		Country:     country,
	})
}

func (s *PostgresStore) UpdateLastLogin(id int64) error {
	return s.q.UpdateLastLogin(context.Background(), id)
}

func (s *PostgresStore) UpdatePassword(id int64, passwordHash string) error {
	return s.q.UpdatePassword(context.Background(), gen.UpdatePasswordParams{
		ID:           id,
		PasswordHash: passwordHash,
	})
}

func (s *PostgresStore) UpdateUserRating(u RatingUpdate) error {
	return s.q.UpdateUserRating(context.Background(), gen.UpdateUserRatingParams{
		ID:         u.UserID,
		Rating:     u.Rating,
		Rd:         u.RD,
		Volatility: u.Volatility,
		Wins:       u.Wins,
		Losses:     u.Losses,
		Draws:      u.Draws,
	})
}

func (s *PostgresStore) GetUserStats(id int64) (*UserStats, error) {
	row, err := s.q.CountUserGameStats(context.Background(), id)
	if err != nil {
		return nil, err
	}
	stats := &UserStats{
		GamesPlayed: int(row.Played),
		Wins:        int(row.Wins),
		Draws:       int(row.Draws),
	}
	stats.Losses = stats.GamesPlayed - stats.Wins - stats.Draws
	if stats.Losses < 0 {
		stats.Losses = 0
	}
	return stats, nil
}

// UpsertBot seeds a bot user row (idempotent across replicas / restarts).
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func (s *PostgresStore) UpsertBot(username, passwordHash string, rating int) (BotUser, error) {
	row, err := s.q.UpsertBot(context.Background(), gen.UpsertBotParams{
		Username:     username,
		PasswordHash: passwordHash,
		Rating:       float32(rating),
	})
	if err != nil {
		return BotUser{}, err
	}
	return BotUser{ID: row.ID, Username: row.Username, Rating: row.Rating}, nil
}

// ListBots returns the seeded bot pool ordered by rating ASC.
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func (s *PostgresStore) ListBots() ([]BotUser, error) {
	rows, err := s.q.ListBots(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]BotUser, len(rows))
	for i, r := range rows {
		out[i] = BotUser{ID: r.ID, Username: r.Username, Rating: r.Rating}
	}
	return out, nil
}

// === ADMIN DASHBOARD ===

func (s *PostgresStore) CountUsers() (int64, int64, error) {
	row, err := s.q.CountUsers(context.Background())
	if err != nil {
		return 0, 0, err
	}
	return row.Humans, row.Bots, nil
}

func (s *PostgresStore) ListBotStats() ([]BotStat, error) {
	rows, err := s.q.ListBotStats(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]BotStat, len(rows))
	for i, r := range rows {
		out[i] = BotStat{
			ID:          r.ID,
			Username:    r.Username,
			Rating:      int(r.Rating),
			GamesPlayed: r.GamesPlayed,
		}
	}
	return out, nil
}

func (s *PostgresStore) CountRecentSignups() (int64, int64, error) {
	row, err := s.q.CountRecentSignups(context.Background())
	if err != nil {
		return 0, 0, err
	}
	return row.Day, row.Week, nil
}

func (s *PostgresStore) ListRecentSignups() ([]AdminSignup, error) {
	rows, err := s.q.ListRecentSignups(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]AdminSignup, len(rows))
	for i, r := range rows {
		out[i] = AdminSignup{
			ID:          r.ID,
			Username:    r.Username,
			DisplayName: r.DisplayName,
			Country:     r.Country,
			Rating:      int(r.Rating),
			CreatedAt:   r.CreatedAt,
		}
	}
	return out, nil
}

func (s *PostgresStore) CountActiveGames() (int64, error) {
	return s.q.CountActiveGames(context.Background())
}

func (s *PostgresStore) DeleteUser(id int64) (int64, error) {
	return s.q.DeleteUser(context.Background(), id)
}

func (s *PostgresStore) InsertAdminAction(actorID *int64, actorUsername, action string, targetID *int64, targetUsername, detail string) error {
	return s.q.InsertAdminAction(context.Background(), gen.InsertAdminActionParams{
		ActorUserID:    int64PtrToNull(actorID),
		ActorUsername:  actorUsername,
		Action:         action,
		TargetUserID:   int64PtrToNull(targetID),
		TargetUsername: targetUsername,
		Detail:         detail,
	})
}

func (s *PostgresStore) ListAdminActions() ([]AdminAction, error) {
	rows, err := s.q.ListAdminActions(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]AdminAction, len(rows))
	for i, r := range rows {
		out[i] = AdminAction{
			ID:             r.ID,
			ActorUserID:    nullToInt64Ptr(r.ActorUserID),
			ActorUsername:  r.ActorUsername,
			Action:         r.Action,
			TargetUserID:   nullToInt64Ptr(r.TargetUserID),
			TargetUsername: r.TargetUsername,
			Detail:         r.Detail,
			CreatedAt:      r.CreatedAt,
		}
	}
	return out, nil
}

func (s *PostgresStore) ListAdminActionsBefore(cursor time.Time) ([]AdminAction, error) {
	rows, err := s.q.ListAdminActionsBefore(context.Background(), cursor)
	if err != nil {
		return nil, err
	}
	out := make([]AdminAction, len(rows))
	for i, r := range rows {
		out[i] = AdminAction{
			ID:             r.ID,
			ActorUserID:    nullToInt64Ptr(r.ActorUserID),
			ActorUsername:  r.ActorUsername,
			Action:         r.Action,
			TargetUserID:   nullToInt64Ptr(r.TargetUserID),
			TargetUsername: r.TargetUsername,
			Detail:         r.Detail,
			CreatedAt:      r.CreatedAt,
		}
	}
	return out, nil
}

func (s *PostgresStore) ListActiveGamesAdmin() ([]AdminLiveGame, error) {
	rows, err := s.q.ListActiveGamesForAdmin(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]AdminLiveGame, len(rows))
	for i, r := range rows {
		out[i] = AdminLiveGame{
			ID:            r.ID,
			WhiteUserID:   nullToInt64Ptr(r.WhiteUserID),
			BlackUserID:   nullToInt64Ptr(r.BlackUserID),
			WhiteUsername: r.WhiteUsername.String,
			BlackUsername: r.BlackUsername.String,
			TimeControl:   r.TimeControl,
			Rated:         r.Rated,
			CreatedAt:     r.CreatedAt,
			UpdatedAt:     r.UpdatedAt,
		}
	}
	return out, nil
}

// === GAMES ===

// defaultListGamesLimit is the per-page cap when the caller doesn't
// specify one. Tight enough that the dashboard renders fast even for
// power users with thousands of historical rows; bigger pages should
// page through explicitly with the cursor.
const defaultListGamesLimit = 50

func (s *PostgresStore) SaveGame(g *GameRecord) error {
	return s.q.UpsertGame(context.Background(), gen.UpsertGameParams{
		ID:             g.ID,
		WhiteUserID:    int64PtrToNull(g.WhiteUserID),
		BlackUserID:    int64PtrToNull(g.BlackUserID),
		Fen:            g.FEN,
		History:        g.History,
		HistorySan:     g.HistorySAN,
		EngineWhite:    g.EngineWhite,
		EngineBlack:    g.EngineBlack,
		WhiteThinkTime: int32(g.WhiteThinkTime),
		BlackThinkTime: int32(g.BlackThinkTime),
		TimeControl:    g.TimeControl,
		Rated:          g.Rated,
		Status:         g.Status,
		Result:         defaultString(g.Result, "*"),
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
		StartFen:       g.StartFEN,
		IsPublic:       g.IsPublic,
		Assessments:    defaultString(g.Assessments, "[]"),
		Imported:       g.Imported,
	})
}

func (s *PostgresStore) ListGames(userID int64, cursor time.Time, limit int) ([]GameRecord, error) {
	if limit <= 0 {
		limit = defaultListGamesLimit
	}
	rows, err := s.q.ListGames(context.Background(), gen.ListGamesParams{
		Column1: userID,
		Column2: cursor,
		Column3: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]GameRecord, len(rows))
	for i, r := range rows {
		out[i] = GameRecord{
			ID:             r.ID,
			WhiteUserID:    nullToInt64Ptr(r.WhiteUserID),
			BlackUserID:    nullToInt64Ptr(r.BlackUserID),
			FEN:            r.Fen,
			StartFEN:       r.StartFen,
			History:        r.History,
			HistorySAN:     r.HistorySan,
			EngineWhite:    r.EngineWhite,
			EngineBlack:    r.EngineBlack,
			WhiteThinkTime: int(r.WhiteThinkTime),
			BlackThinkTime: int(r.BlackThinkTime),
			TimeControl:    r.TimeControl,
			Rated:          r.Rated,
			Status:         r.Status,
			Result:         r.Result,
			CreatedAt:      r.CreatedAt,
			UpdatedAt:      r.UpdatedAt,
			IsPublic:       r.IsPublic,
			Assessments:    r.Assessments,
			Imported:       r.Imported,
		}
	}
	return out, nil
}

func (s *PostgresStore) GetGame(id string) (*GameRecord, error) {
	r, err := s.q.GetGame(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return &GameRecord{
		ID:             r.ID,
		WhiteUserID:    nullToInt64Ptr(r.WhiteUserID),
		BlackUserID:    nullToInt64Ptr(r.BlackUserID),
		FEN:            r.Fen,
		StartFEN:       r.StartFen,
		History:        r.History,
		HistorySAN:     r.HistorySan,
		EngineWhite:    r.EngineWhite,
		EngineBlack:    r.EngineBlack,
		WhiteThinkTime: int(r.WhiteThinkTime),
		BlackThinkTime: int(r.BlackThinkTime),
		TimeControl:    r.TimeControl,
		Rated:          r.Rated,
		Status:         r.Status,
		Result:         r.Result,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
		IsPublic:       r.IsPublic,
		Assessments:    r.Assessments,
		Imported:       r.Imported,
	}, nil
}

func (s *PostgresStore) DeleteGame(id string) (int64, error) {
	return s.q.DeleteGame(context.Background(), id)
}

func (s *PostgresStore) SetGameVisibility(id string, isPublic bool) (int64, error) {
	return s.q.SetGameVisibility(context.Background(), gen.SetGameVisibilityParams{
		ID:       id,
		IsPublic: isPublic,
	})
}

// === INVITES ===

func (s *PostgresStore) CreateInvite(id uuid.UUID, fromUserID, toUserID int64, timeControl string, rated bool, expiresAt time.Time) (*Invite, error) {
	row, err := s.q.CreateInvite(context.Background(), gen.CreateInviteParams{
		ID:          id,
		FromUserID:  fromUserID,
		ToUserID:    toUserID,
		TimeControl: timeControl,
		Rated:       rated,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return nil, err
	}
	return inviteFromRow(row), nil
}

func (s *PostgresStore) GetInvite(id uuid.UUID) (*Invite, error) {
	row, err := s.q.GetInvite(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return inviteFromRow(row), nil
}

func (s *PostgresStore) ListPendingInvitesForUser(userID int64) ([]Invite, error) {
	rows, err := s.q.ListPendingInvitesForUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = *inviteFromRow(r)
	}
	return out, nil
}

func (s *PostgresStore) ListPendingInvitesFromUser(userID int64) ([]Invite, error) {
	rows, err := s.q.ListPendingInvitesFromUser(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = *inviteFromRow(r)
	}
	return out, nil
}

// AcceptInviteWithGame wraps UpsertGame + AcceptInvite in a single
// transaction. If AcceptInvite affects zero rows (invite no longer
// pending), we roll back so no orphan game survives. The prior
// implementation used a compensating SaveGame + AcceptInvite + best-
// effort DeleteGame, which left an orphan if the delete itself failed.
func (s *PostgresStore) AcceptInviteWithGame(inviteID uuid.UUID, toUserID int64, g *GameRecord) (int64, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	qtx := s.q.WithTx(tx)
	if err := qtx.UpsertGame(ctx, gen.UpsertGameParams{
		ID:             g.ID,
		WhiteUserID:    int64PtrToNull(g.WhiteUserID),
		BlackUserID:    int64PtrToNull(g.BlackUserID),
		Fen:            g.FEN,
		History:        g.History,
		HistorySan:     g.HistorySAN,
		EngineWhite:    g.EngineWhite,
		EngineBlack:    g.EngineBlack,
		WhiteThinkTime: int32(g.WhiteThinkTime),
		BlackThinkTime: int32(g.BlackThinkTime),
		TimeControl:    g.TimeControl,
		Rated:          g.Rated,
		Status:         g.Status,
		Result:         defaultString(g.Result, "*"),
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
		StartFen:       g.StartFEN,
		IsPublic:       g.IsPublic,
		Assessments:    defaultString(g.Assessments, "[]"),
		Imported:       g.Imported,
	}); err != nil {
		return 0, err
	}
	rows, err := qtx.AcceptInvite(ctx, gen.AcceptInviteParams{
		ID:       inviteID,
		ToUserID: toUserID,
		GameID:   sql.NullString{String: g.ID, Valid: true},
	})
	if err != nil {
		return 0, err
	}
	if rows == 0 {
		// Invite no longer pending — let the deferred Rollback take it.
		return 0, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rows, nil
}

func (s *PostgresStore) DeclineInvite(inviteID uuid.UUID, toUserID int64) (int64, error) {
	return s.q.DeclineInvite(context.Background(), gen.DeclineInviteParams{
		ID:       inviteID,
		ToUserID: toUserID,
	})
}

func (s *PostgresStore) CancelInvite(inviteID uuid.UUID, fromUserID int64) (int64, error) {
	return s.q.CancelInvite(context.Background(), gen.CancelInviteParams{
		ID:         inviteID,
		FromUserID: fromUserID,
	})
}

func (s *PostgresStore) ExpireStaleInvites() ([]Invite, error) {
	rows, err := s.q.ExpireStaleInvites(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = *inviteFromRow(r)
	}
	return out, nil
}

// --- mappers between sqlc-generated row types and the Store DTOs ---

func userFromRow(r gen.User) *User {
	return &User{
		ID:           r.ID,
		Username:     r.Username,
		PasswordHash: r.PasswordHash,
		DisplayName:  r.DisplayName,
		AvatarURL:    r.AvatarUrl,
		Country:      r.Country,
		IsPremium:    r.IsPremium,
		Bio:          r.Bio,
		LastLogin:    r.LastLogin,
		CreatedAt:    r.CreatedAt,
		Rating:       r.Rating,
		RD:           r.Rd,
		Volatility:   r.Volatility,
		GamesPlayed:  int(r.GamesPlayed),
		Wins:         int(r.Wins),
		Losses:       int(r.Losses),
		Draws:        int(r.Draws),
		IsAdmin:      r.IsAdmin,
	}
}

func inviteFromRow(r gen.Invite) *Invite {
	inv := &Invite{
		ID:          r.ID,
		FromUserID:  r.FromUserID,
		ToUserID:    r.ToUserID,
		TimeControl: r.TimeControl,
		Rated:       r.Rated,
		Status:      r.Status,
		CreatedAt:   r.CreatedAt,
		ExpiresAt:   r.ExpiresAt,
	}
	if r.GameID.Valid {
		gid := r.GameID.String
		inv.GameID = &gid
	}
	if r.ResolvedAt.Valid {
		t := r.ResolvedAt.Time
		inv.ResolvedAt = &t
	}
	return inv
}

func int64PtrToNull(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullToInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Compile-time guarantee that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)
