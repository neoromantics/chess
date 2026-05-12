package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type PostgresStore struct {
	db *sql.DB
}

func OpenPostgres(dsn string) (Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}

	s := &PostgresStore{db: db}
	if err := s.init(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return s, nil
}

func (s *PostgresStore) init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '',
			country TEXT NOT NULL DEFAULT '',
			is_premium BOOLEAN NOT NULL DEFAULT FALSE,
			elo INTEGER NOT NULL DEFAULT 1200,
			bio TEXT NOT NULL DEFAULT '',
			last_login TIMESTAMP NOT NULL DEFAULT NOW(),
			created_at TIMESTAMP NOT NULL
		);

		CREATE TABLE IF NOT EXISTS games (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL DEFAULT 0,
			session_id TEXT NOT NULL DEFAULT '',
			fen TEXT NOT NULL,
			history TEXT NOT NULL,
			history_san TEXT NOT NULL,
			engine_white BOOLEAN NOT NULL,
			engine_black BOOLEAN NOT NULL,
			white_think_time INTEGER NOT NULL DEFAULT 1000,
			black_think_time INTEGER NOT NULL DEFAULT 1000,
			status TEXT NOT NULL,
			assessments TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);

		-- Migrations for existing tables
		ALTER TABLE users ADD COLUMN IF NOT EXISTS is_premium BOOLEAN NOT NULL DEFAULT FALSE;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS elo INTEGER NOT NULL DEFAULT 1200;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT '';
		ALTER TABLE users ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
		ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url TEXT NOT NULL DEFAULT '';
		ALTER TABLE users ADD COLUMN IF NOT EXISTS country TEXT NOT NULL DEFAULT '';
		ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login TIMESTAMP NOT NULL DEFAULT NOW();
		ALTER TABLE games ADD COLUMN IF NOT EXISTS assessments TEXT NOT NULL DEFAULT '[]';
		ALTER TABLE games ADD COLUMN IF NOT EXISTS white_think_time INTEGER NOT NULL DEFAULT 1000;
		ALTER TABLE games ADD COLUMN IF NOT EXISTS black_think_time INTEGER NOT NULL DEFAULT 1000;
	`)
	return err
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) Ping() error {
	return s.db.Ping()
}

func (s *PostgresStore) CreateUser(username, passwordHash string) (*User, error) {
	now := time.Now()
	u := &User{
		Username:     username,
		PasswordHash: passwordHash,
		CreatedAt:    now,
		LastLogin:    now,
		Elo:          1200,
	}
	err := s.db.QueryRow(`
		INSERT INTO users (username, password_hash, created_at, last_login, elo)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, username, passwordHash, now, now, u.Elo).Scan(&u.ID)
	if err != nil {
		return nil, err
	}
	u.UserID = u.ID
	return u, nil
}

func (s *PostgresStore) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, display_name, avatar_url, country,
		       is_premium, elo, bio, last_login, created_at
		FROM users
		WHERE username = $1
	`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName,
		&u.AvatarURL, &u.Country, &u.IsPremium, &u.Elo, &u.Bio,
		&u.LastLogin, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.UserID = u.ID
	return u, nil
}

func (s *PostgresStore) GetUserByID(id int64) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, display_name, avatar_url, country,
		       is_premium, elo, bio, last_login, created_at
		FROM users
		WHERE id = $1
	`, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName,
		&u.AvatarURL, &u.Country, &u.IsPremium, &u.Elo, &u.Bio,
		&u.LastLogin, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.UserID = u.ID
	return u, nil
}

func (s *PostgresStore) UpdateUserProfile(id int64, displayName, bio, avatarURL, country string) error {
	_, err := s.db.Exec(`
		UPDATE users SET display_name = $1, bio = $2, avatar_url = $3, country = $4
		WHERE id = $5
	`, displayName, bio, avatarURL, country, id)
	return err
}

func (s *PostgresStore) GetUserStats(id int64) (*UserStats, error) {
	stats := &UserStats{}

	// Total games played (where the user is owner)
	err := s.db.QueryRow(`SELECT COUNT(*) FROM games WHERE user_id = $1`, id).Scan(&stats.GamesPlayed)
	if err != nil {
		return nil, err
	}

	// Wins: games where the user's side checkmated the opponent
	// For simplicity, count games where status is 'checkmate' and the user was likely the winner.
	// A more rigorous approach would track which color the user played.
	s.db.QueryRow(`
		SELECT COUNT(*) FROM games
		WHERE user_id = $1 AND status = 'checkmate'
	`, id).Scan(&stats.Wins)

	// Draws
	s.db.QueryRow(`
		SELECT COUNT(*) FROM games
		WHERE user_id = $1 AND status IN ('stalemate', 'draw50', 'draw_repetition', 'draw_insufficient')
	`, id).Scan(&stats.Draws)

	stats.Losses = stats.GamesPlayed - stats.Wins - stats.Draws
	if stats.Losses < 0 {
		stats.Losses = 0
	}

	return stats, nil
}

func (s *PostgresStore) SaveGame(g *GameRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO games (id, user_id, session_id, fen, history, history_san, engine_white, engine_black, white_think_time, black_think_time, status, assessments, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT(id) DO UPDATE SET
			user_id=EXCLUDED.user_id,
			session_id=EXCLUDED.session_id,
			fen=EXCLUDED.fen,
			history=EXCLUDED.history,
			history_san=EXCLUDED.history_san,
			engine_white=EXCLUDED.engine_white,
			engine_black=EXCLUDED.engine_black,
			white_think_time=EXCLUDED.white_think_time,
			black_think_time=EXCLUDED.black_think_time,
			status=EXCLUDED.status,
			assessments=EXCLUDED.assessments,
			updated_at=EXCLUDED.updated_at
	`, g.ID, g.UserID, g.SessionID, g.FEN, g.History, g.HistorySAN, g.EngineWhite, g.EngineBlack, g.WhiteThinkTime, g.BlackThinkTime, g.Status, g.Assessments, g.CreatedAt, g.UpdatedAt)
	return err
}

func (s *PostgresStore) ListGames(userID int64, sessionID string) ([]GameRecord, error) {
	query := `
		SELECT id, user_id, session_id, fen, history, history_san, engine_white, engine_black, white_think_time, black_think_time, status, assessments, created_at, updated_at
		FROM games
		WHERE (user_id > 0 AND user_id = $1) OR (user_id = 0 AND session_id = $2)
		ORDER BY updated_at DESC
	`
	rows, err := s.db.Query(query, userID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []GameRecord
	for rows.Next() {
		var g GameRecord
		if err := rows.Scan(&g.ID, &g.UserID, &g.SessionID, &g.FEN, &g.History, &g.HistorySAN, &g.EngineWhite, &g.EngineBlack, &g.WhiteThinkTime, &g.BlackThinkTime, &g.Status, &g.Assessments, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, g)
	}
	return records, nil
}

func (s *PostgresStore) GetGame(id string) (*GameRecord, error) {
	g := &GameRecord{}
	err := s.db.QueryRow(`
		SELECT id, user_id, session_id, fen, history, history_san, engine_white, engine_black, white_think_time, black_think_time, status, assessments, created_at, updated_at
		FROM games
		WHERE id = $1
	`, id).Scan(&g.ID, &g.UserID, &g.SessionID, &g.FEN, &g.History, &g.HistorySAN, &g.EngineWhite, &g.EngineBlack, &g.WhiteThinkTime, &g.BlackThinkTime, &g.Status, &g.Assessments, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (s *PostgresStore) DeleteGame(id string, userID int64) error {
	_, err := s.db.Exec("DELETE FROM games WHERE id = $1 AND user_id = $2", id, userID)
	return err
}
