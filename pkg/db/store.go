package db

import (
	"time"
)
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	IsPremium    bool      `json:"is_premium"`
	Elo          int       `json:"elo"`
	Bio          string    `json:"bio"`
	CreatedAt    time.Time `json:"created_at"`
}

// GameRecord represents a persistent game state in the database.
type GameRecord struct {
	ID          string    `json:"id"`
	UserID      int64     `json:"user_id"`
	SessionID   string    `json:"session_id"`
	FEN         string    `json:"fen"`
	History     string    `json:"history"`     // JSON string
	HistorySAN  string    `json:"history_san"` // JSON string
	EngineWhite bool      `json:"engine_white"`
	EngineBlack bool      `json:"engine_black"`
	Status      string    `json:"status"`
	Assessments string    `json:"assessments"` // JSON string
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Store is the interface for all persistent storage operations.
// This allows us to swap SQLite for PostgreSQL/Redis without changing the API logic.
type Store interface {
	Close() error
	
	// User management
	CreateUser(username, passwordHash string) (*User, error)
	GetUserByUsername(username string) (*User, error)
	
	// Game management
	SaveGame(g *GameRecord) error
	ListGames(userID int64, sessionID string) ([]GameRecord, error)
	GetGame(id string) (*GameRecord, error)
	DeleteGame(id string, userID int64) error
}
