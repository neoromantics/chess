package db

import (
	"time"
)

type User struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"` // alias used by auth
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"display_name"`
	AvatarURL    string    `json:"avatar_url"`
	Country      string    `json:"country"`
	IsPremium    bool      `json:"is_premium"`
	Elo          int       `json:"elo"`
	Bio          string    `json:"bio"`
	LastLogin    time.Time `json:"last_login"`
	CreatedAt    time.Time `json:"created_at"`
}

// UserStats holds aggregated game statistics for a user.
type UserStats struct {
	GamesPlayed   int `json:"games_played"`
	Wins          int `json:"wins"`
	Losses        int `json:"losses"`
	Draws         int `json:"draws"`
	CurrentStreak int `json:"current_streak"`
}

// GameRecord represents a persistent game state in the database.
// Every game has a non-zero UserID — anonymous play is no longer supported.
type GameRecord struct {
	ID             string    `json:"id"`
	UserID         int64     `json:"user_id"`
	FEN            string    `json:"fen"`
	History        string    `json:"history"`     // JSON string
	HistorySAN     string    `json:"history_san"` // JSON string
	EngineWhite    bool      `json:"engine_white"`
	EngineBlack    bool      `json:"engine_black"`
	WhiteThinkTime int       `json:"white_think_time"` // in ms
	BlackThinkTime int       `json:"black_think_time"` // in ms
	Status         string    `json:"status"`
	Assessments    string    `json:"assessments"` // JSON string
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Store is the interface for all persistent storage operations.
// The production implementation is PostgresStore (sqlc-backed); the
// interface exists so handlers stay decoupled from the storage driver.
type Store interface {
	Close() error
	Ping() error

	// User management
	CreateUser(username, passwordHash string) (*User, error)
	GetUserByUsername(username string) (*User, error)
	GetUserByID(id int64) (*User, error)
	UpdateUserProfile(id int64, displayName, bio, avatarURL, country string) error
	UpdateLastLogin(id int64) error
	UpdatePassword(id int64, passwordHash string) error
	GetUserStats(id int64) (*UserStats, error)

	// Game management
	SaveGame(g *GameRecord) error
	ListGames(userID int64) ([]GameRecord, error)
	GetGame(id string) (*GameRecord, error)
	// DeleteGame returns the number of rows removed; callers should treat 0
	// as a missing record (or one that was already deleted).
	DeleteGame(id string) (int64, error)
}
