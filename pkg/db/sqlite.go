package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func OpenSQLite(path string) (Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite: %w", err)
	}

	s := &SQLiteStore{db: db}
	if err := s.init(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return s, nil
}

func (s *SQLiteStore) init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '',
			country TEXT NOT NULL DEFAULT '',
			is_premium BOOLEAN NOT NULL DEFAULT FALSE,
			elo INTEGER NOT NULL DEFAULT 1200,
			bio TEXT NOT NULL DEFAULT '',
			last_login DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME NOT NULL
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
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
	`)
	if err != nil {
		return err
	}

	// Migrations for existing tables (SQLite doesn't support ADD COLUMN IF NOT EXISTS)
	s.db.Exec("ALTER TABLE users ADD COLUMN is_premium BOOLEAN NOT NULL DEFAULT FALSE")
	s.db.Exec("ALTER TABLE users ADD COLUMN elo INTEGER NOT NULL DEFAULT 1200")
	s.db.Exec("ALTER TABLE users ADD COLUMN bio TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE users ADD COLUMN avatar_url TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE users ADD COLUMN country TEXT NOT NULL DEFAULT ''")
	s.db.Exec("ALTER TABLE users ADD COLUMN last_login DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP")
	s.db.Exec("ALTER TABLE games ADD COLUMN assessments TEXT NOT NULL DEFAULT '[]'")

	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) Ping() error {
	return s.db.Ping()
}

func (s *SQLiteStore) CreateUser(username, passwordHash string) (*User, error) {
	now := time.Now()
	res, err := s.db.Exec(`
		INSERT INTO users (username, password_hash, created_at, last_login, elo)
		VALUES (?, ?, ?, ?, ?)
	`, username, passwordHash, now, now, 1200)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{
		ID:           id,
		UserID:       id,
		Username:     username,
		PasswordHash: passwordHash,
		LastLogin:    now,
		CreatedAt:    now,
		Elo:          1200,
	}, nil
}

func (s *SQLiteStore) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, display_name, avatar_url, country,
		       is_premium, elo, bio, last_login, created_at
		FROM users
		WHERE username = ?
	`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName,
		&u.AvatarURL, &u.Country, &u.IsPremium, &u.Elo, &u.Bio,
		&u.LastLogin, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.UserID = u.ID
	return u, nil
}

func (s *SQLiteStore) GetUserByID(id int64) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`
		SELECT id, username, password_hash, display_name, avatar_url, country,
		       is_premium, elo, bio, last_login, created_at
		FROM users
		WHERE id = ?
	`, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName,
		&u.AvatarURL, &u.Country, &u.IsPremium, &u.Elo, &u.Bio,
		&u.LastLogin, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.UserID = u.ID
	return u, nil
}

func (s *SQLiteStore) UpdateUserProfile(id int64, displayName, bio, avatarURL, country string) error {
	_, err := s.db.Exec(`
		UPDATE users SET display_name = ?, bio = ?, avatar_url = ?, country = ?
		WHERE id = ?
	`, displayName, bio, avatarURL, country, id)
	return err
}

func (s *SQLiteStore) GetUserStats(id int64) (*UserStats, error) {
	stats := &UserStats{}

	err := s.db.QueryRow(`SELECT COUNT(*) FROM games WHERE user_id = ?`, id).Scan(&stats.GamesPlayed)
	if err != nil {
		return nil, err
	}

	s.db.QueryRow(`
		SELECT COUNT(*) FROM games
		WHERE user_id = ? AND status = 'checkmate'
	`, id).Scan(&stats.Wins)

	s.db.QueryRow(`
		SELECT COUNT(*) FROM games
		WHERE user_id = ? AND status IN ('stalemate', 'draw50', 'draw_repetition', 'draw_insufficient')
	`, id).Scan(&stats.Draws)

	stats.Losses = stats.GamesPlayed - stats.Wins - stats.Draws
	if stats.Losses < 0 {
		stats.Losses = 0
	}

	return stats, nil
}

func (s *SQLiteStore) SaveGame(g *GameRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO games (id, user_id, session_id, fen, history, history_san, engine_white, engine_black, status, assessments, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			user_id=excluded.user_id,
			session_id=excluded.session_id,
			fen=excluded.fen,
			history=excluded.history,
			history_san=excluded.history_san,
			engine_white=excluded.engine_white,
			engine_black=excluded.engine_black,
			status=excluded.status,
			assessments=excluded.assessments,
			updated_at=excluded.updated_at
	`, g.ID, g.UserID, g.SessionID, g.FEN, g.History, g.HistorySAN, g.EngineWhite, g.EngineBlack, g.Status, g.Assessments, g.CreatedAt, g.UpdatedAt)
	return err
}

func (s *SQLiteStore) ListGames(userID int64, sessionID string) ([]GameRecord, error) {
	query := `
		SELECT id, user_id, session_id, fen, history, history_san, engine_white, engine_black, status, assessments, created_at, updated_at
		FROM games
		WHERE (user_id > 0 AND user_id = ?) OR (user_id = 0 AND session_id = ?)
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

func (s *SQLiteStore) GetGame(id string) (*GameRecord, error) {
	g := &GameRecord{}
	err := s.db.QueryRow(`
		SELECT id, user_id, session_id, fen, history, history_san, engine_white, engine_black, status, assessments, created_at, updated_at
		FROM games
		WHERE id = ?
	`, id).Scan(&g.ID, &g.UserID, &g.SessionID, &g.FEN, &g.History, &g.HistorySAN, &g.EngineWhite, &g.EngineBlack, &g.Status, &g.Assessments, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (s *SQLiteStore) DeleteGame(id string, userID int64) error {
	_, err := s.db.Exec("DELETE FROM games WHERE id = ? AND user_id = ?", id, userID)
	return err
}
