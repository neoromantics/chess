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
	`)
	return err
}

func (d *DB) Close() error {
	return d.db.Close()
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
