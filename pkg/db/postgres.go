package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migpostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"

	"github.com/neoromantics/chess/pkg/db/gen"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore is the sqlc-backed implementation of Store.
// All queries are generated from pkg/db/queries/*.sql into pkg/db/gen.
type PostgresStore struct {
	db *sql.DB
	q  *gen.Queries
}

// OpenPostgres opens a pooled connection to Postgres, runs embedded
// migrations to the latest version, and returns a ready-to-use Store.
//
// Pool sizing here matters for distributed deployments: many small API
// pods sharing one Postgres mean we must cap each pod's open connections
// or we'll DoS the database under load.
func OpenPostgres(dsn string) (Store, error) {
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres: %w", err)
	}

	// Reasonable defaults for an API pod under load. Tune via env later
	// if/when we have observability to point us at a different number.
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}

	if err := runMigrations(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return &PostgresStore{db: sqlDB, q: gen.New(sqlDB)}, nil
}

// runMigrations applies all embedded migrations through golang-migrate.
// Safe to call from every replica on startup — schema_migrations + the
// advisory lock golang-migrate takes ensures only one applies at a time.
func runMigrations(sqlDB *sql.DB) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrations source: %w", err)
	}
	defer src.Close()

	driver, err := migpostgres.WithInstance(sqlDB, &migpostgres.Config{})
	if err != nil {
		return fmt.Errorf("migrations driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func (s *PostgresStore) Close() error { return s.db.Close() }
func (s *PostgresStore) Ping() error  { return s.db.Ping() }

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

func (s *PostgresStore) SaveGame(g *GameRecord) error {
	return s.q.UpsertGame(context.Background(), gen.UpsertGameParams{
		ID:             g.ID,
		UserID:         g.UserID,
		Fen:            g.FEN,
		History:        g.History,
		HistorySan:     g.HistorySAN,
		EngineWhite:    g.EngineWhite,
		EngineBlack:    g.EngineBlack,
		WhiteThinkTime: int32(g.WhiteThinkTime),
		BlackThinkTime: int32(g.BlackThinkTime),
		Status:         g.Status,
		Assessments:    g.Assessments,
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
	})
}

func (s *PostgresStore) ListGames(userID int64) ([]GameRecord, error) {
	rows, err := s.q.ListGames(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]GameRecord, len(rows))
	for i, r := range rows {
		out[i] = gameFromRow(r)
	}
	return out, nil
}

func (s *PostgresStore) GetGame(id string) (*GameRecord, error) {
	row, err := s.q.GetGame(context.Background(), id)
	if err != nil {
		return nil, err
	}
	g := gameFromRow(row)
	return &g, nil
}

func (s *PostgresStore) DeleteGame(id string) (int64, error) {
	return s.q.DeleteGame(context.Background(), id)
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
	}
}

func gameFromRow(r gen.Game) GameRecord {
	return GameRecord{
		ID:             r.ID,
		UserID:         r.UserID,
		FEN:            r.Fen,
		History:        r.History,
		HistorySAN:     r.HistorySan,
		EngineWhite:    r.EngineWhite,
		EngineBlack:    r.EngineBlack,
		WhiteThinkTime: int(r.WhiteThinkTime),
		BlackThinkTime: int(r.BlackThinkTime),
		Status:         r.Status,
		Assessments:    r.Assessments,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

// Compile-time guarantee that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)
