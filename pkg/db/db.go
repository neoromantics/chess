package db

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type DB struct {
	db *sql.DB
}

func Open() (*DB, error) {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "chess.db"
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	d := &DB{db: db}
	if err := d.init(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return d, nil
}

func (d *DB) init() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS games (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			fen TEXT NOT NULL,
			history TEXT NOT NULL,
			history_san TEXT NOT NULL,
			engine_white BOOLEAN NOT NULL,
			engine_black BOOLEAN NOT NULL,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id)
		);
	`)
	return err
}

func (d *DB) Close() error {
	return d.db.Close()
}

// ... CreateUser and GetUserByUsername ...

type GameRecord struct {
	ID          string    `json:"id"`
	UserID      int64     `json:"user_id"`
	FEN         string    `json:"fen"`
	History     string    `json:"history"`     // JSON string
	HistorySAN  string    `json:"history_san"` // JSON string
	EngineWhite bool      `json:"engine_white"`
	EngineBlack bool      `json:"engine_black"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (d *DB) SaveGame(g *GameRecord) error {
	_, err := d.db.Exec(`
		INSERT INTO games (id, user_id, fen, history, history_san, engine_white, engine_black, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			fen=excluded.fen,
			history=excluded.history,
			history_san=excluded.history_san,
			status=excluded.status,
			updated_at=excluded.updated_at
	`, g.ID, g.UserID, g.FEN, g.History, g.HistorySAN, g.EngineWhite, g.EngineBlack, g.Status, g.CreatedAt, g.UpdatedAt)
	return err
}

func (d *DB) ListGames(userID int64) ([]GameRecord, error) {
	rows, err := d.db.Query(`
		SELECT id, user_id, fen, history, history_san, engine_white, engine_black, status, created_at, updated_at
		FROM games
		WHERE user_id = ?
		ORDER BY updated_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []GameRecord
	for rows.Next() {
		var g GameRecord
		if err := rows.Scan(&g.ID, &g.UserID, &g.FEN, &g.History, &g.HistorySAN, &g.EngineWhite, &g.EngineBlack, &g.Status, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, g)
	}
	return records, nil
}

func (d *DB) GetGame(id string) (*GameRecord, error) {
	g := &GameRecord{}
	err := d.db.QueryRow(`
		SELECT id, user_id, fen, history, history_san, engine_white, engine_black, status, created_at, updated_at
		FROM games
		WHERE id = ?
	`, id).Scan(&g.ID, &g.UserID, &g.FEN, &g.History, &g.HistorySAN, &g.EngineWhite, &g.EngineBlack, &g.Status, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (d *DB) DeleteGame(id string, userID int64) error {
	_, err := d.db.Exec("DELETE FROM games WHERE id = ? AND user_id = ?", id, userID)
	return err
}

func (d *DB) CreateUser(username, passwordHash string) (*User, error) {
	now := time.Now()
	res, err := d.db.Exec(`
		INSERT INTO users (username, password_hash, created_at)
		VALUES (?, ?, ?)
	`, username, passwordHash, now)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &User{
		ID:           id,
		Username:     username,
		PasswordHash: passwordHash,
		CreatedAt:    now,
	}, nil
}

func (d *DB) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := d.db.QueryRow(`
		SELECT id, username, password_hash, created_at
		FROM users
		WHERE username = ?
	`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}
