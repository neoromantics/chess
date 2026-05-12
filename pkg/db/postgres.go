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
			is_premium BOOLEAN NOT NULL DEFAULT FALSE,
			elo INTEGER NOT NULL DEFAULT 1200,
			bio TEXT NOT NULL DEFAULT '',
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
			status TEXT NOT NULL,
			assessments TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);

		-- Migrations for existing tables
		ALTER TABLE users ADD COLUMN IF NOT EXISTS is_premium BOOLEAN NOT NULL DEFAULT FALSE;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS elo INTEGER NOT NULL DEFAULT 1200;
		ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT '';
		ALTER TABLE games ADD COLUMN IF NOT EXISTS assessments TEXT NOT NULL DEFAULT '[]';
	`)
	return err
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) CreateUser(username, passwordHash string) (*User, error) {
	now := time.Now()
	u := &User{
		Username:     username,
		PasswordHash: passwordHash,
		CreatedAt:    now,
		Elo:          1200,
	}
	err := s.db.QueryRow(`
		INSERT INTO users (username, password_hash, created_at, elo)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, username, passwordHash, now, u.Elo).Scan(&u.ID)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PostgresStore) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, is_premium, elo, bio, created_at
		FROM users
		WHERE username = $1
	`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsPremium, &u.Elo, &u.Bio, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *PostgresStore) SaveGame(g *GameRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO games (id, user_id, session_id, fen, history, history_san, engine_white, engine_black, status, assessments, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT(id) DO UPDATE SET
			user_id=EXCLUDED.user_id,
			session_id=EXCLUDED.session_id,
			fen=EXCLUDED.fen,
			history=EXCLUDED.history,
			history_san=EXCLUDED.history_san,
			engine_white=EXCLUDED.engine_white,
			engine_black=EXCLUDED.engine_black,
			status=EXCLUDED.status,
			assessments=EXCLUDED.assessments,
			updated_at=EXCLUDED.updated_at
	`, g.ID, g.UserID, g.SessionID, g.FEN, g.History, g.HistorySAN, g.EngineWhite, g.EngineBlack, g.Status, g.Assessments, g.CreatedAt, g.UpdatedAt)
	return err
}

func (s *PostgresStore) ListGames(userID int64, sessionID string) ([]GameRecord, error) {
	query := `
		SELECT id, user_id, session_id, fen, history, history_san, engine_white, engine_black, status, assessments, created_at, updated_at
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
		if err := rows.Scan(&g.ID, &g.UserID, &g.SessionID, &g.FEN, &g.History, &g.HistorySAN, &g.EngineWhite, &g.EngineBlack, &g.Status, &g.Assessments, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, g)
	}
	return records, nil
}

func (s *PostgresStore) GetGame(id string) (*GameRecord, error) {
	g := &GameRecord{}
	err := s.db.QueryRow(`
		SELECT id, user_id, session_id, fen, history, history_san, engine_white, engine_black, status, assessments, created_at, updated_at
		FROM games
		WHERE id = $1
	`, id).Scan(&g.ID, &g.UserID, &g.SessionID, &g.FEN, &g.History, &g.HistorySAN, &g.EngineWhite, &g.EngineBlack, &g.Status, &g.Assessments, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (s *PostgresStore) DeleteGame(id string, userID int64) error {
	_, err := s.db.Exec("DELETE FROM games WHERE id = $1 AND user_id = $2", id, userID)
	return err
}
