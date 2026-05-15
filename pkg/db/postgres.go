package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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
// or we'll DoS the database under load. With chess-db raised to
// max_connections=500 and 3 services * up-to-6 replicas via HPA, the
// safe ceiling per pod is roughly 500 / 18 ≈ 27. The defaults below
// leave headroom; PG_MAX_OPEN_CONNS / PG_MAX_IDLE_CONNS lets ops tune
// without a code change when the topology shifts.
func OpenPostgres(dsn string) (Store, error) {
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres: %w", err)
	}

	sqlDB.SetMaxOpenConns(envInt("PG_MAX_OPEN_CONNS", 25))
	sqlDB.SetMaxIdleConns(envInt("PG_MAX_IDLE_CONNS", 5))
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
	ctx := context.Background()
	played, err := s.q.CountUserGames(ctx, id)
	if err != nil {
		return nil, err
	}
	wins, _ := s.q.CountUserWins(ctx, id)
	draws, _ := s.q.CountUserDraws(ctx, id)
	stats := &UserStats{
		GamesPlayed: int(played),
		Wins:        int(wins),
		Draws:       int(draws),
	}
	stats.Losses = stats.GamesPlayed - stats.Wins - stats.Draws
	if stats.Losses < 0 {
		stats.Losses = 0
	}
	return stats, nil
}

// === GAMES ===

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
		Assessments:    g.Assessments,
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
		StartFen:       g.StartFEN,
	})
}

func (s *PostgresStore) ListGames(userID int64) ([]GameRecord, error) {
	rows, err := s.q.ListGames(context.Background(), userID)
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
			Assessments:    r.Assessments,
			CreatedAt:      r.CreatedAt,
			UpdatedAt:      r.UpdatedAt,
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
		Assessments:    r.Assessments,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}, nil
}

func (s *PostgresStore) DeleteGame(id string) (int64, error) {
	return s.q.DeleteGame(context.Background(), id)
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

func (s *PostgresStore) AcceptInvite(inviteID uuid.UUID, toUserID int64, gameID string) (int64, error) {
	return s.q.AcceptInvite(context.Background(), gen.AcceptInviteParams{
		ID:       inviteID,
		ToUserID: toUserID,
		GameID:   sql.NullString{String: gameID, Valid: gameID != ""},
	})
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
		UserID:       r.ID,
		Username:     r.Username,
		PasswordHash: r.PasswordHash,
		DisplayName:  r.DisplayName,
		AvatarURL:    r.AvatarUrl,
		Country:      r.Country,
		IsPremium:    r.IsPremium,
		Elo:          int(r.Elo),
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
