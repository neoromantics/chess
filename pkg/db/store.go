package db

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"display_name"`
	AvatarURL    string    `json:"avatar_url"`
	Country      string    `json:"country"`
	IsPremium    bool      `json:"is_premium"`
	Bio          string    `json:"bio"`
	LastLogin    time.Time `json:"last_login"`
	CreatedAt    time.Time `json:"created_at"`

	// Glicko-2 rating system (added in migration 000003). Float32 in the
	// DB because RD and volatility are sub-integer. Wins/Losses/Draws are
	// authoritative counters maintained by the rating updater; the older
	// COUNT()-based stats are kept as a sanity check for now.
	Rating      float32 `json:"rating"`
	RD          float32 `json:"rd"`
	Volatility  float32 `json:"volatility"`
	GamesPlayed int     `json:"games_played"`
	Wins        int     `json:"wins"`
	Losses      int     `json:"losses"`
	Draws       int     `json:"draws"`
}

// UserSummary is the public view of another user (no PII, no hash).
// Returned by username search for invites and shown in profile cards.
type UserSummary struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Country     string `json:"country"`
	Rating      int    `json:"rating"`
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
// WhiteUserID/BlackUserID are the new platform-level owners; UserID has
// been dropped.
//
// WhiteThinkTime / BlackThinkTime are the *engine search budget* in ms
// for engine-to-move searches on each side. They're meaningful only for
// engine and engine-vs-engine games; PvP rows carry a placeholder 1000
// that the clock subsystem ignores (PvP uses the time_control + clock
// hash for the actual bank). Don't conflate this with the player's
// remaining clock — that's WhiteClockMS / BlackClockMS in the wire
// snapshot, sourced from cmd/game/clocks.go.
type GameRecord struct {
	ID             string    `json:"id"`
	WhiteUserID    *int64    `json:"white_user_id,omitempty"` // nil when engine plays white
	BlackUserID    *int64    `json:"black_user_id,omitempty"` // nil when engine plays black
	FEN            string    `json:"fen"`
	StartFEN       string    `json:"start_fen"`   // empty = standard start; non-empty = board-editor setup
	History        string    `json:"history"`     // JSON string
	HistorySAN     string    `json:"history_san"` // JSON string
	EngineWhite    bool      `json:"engine_white"`
	EngineBlack    bool      `json:"engine_black"`
	WhiteThinkTime int       `json:"white_think_time"` // engine search budget, ms; ignored for PvP
	BlackThinkTime int       `json:"black_think_time"` // engine search budget, ms; ignored for PvP
	TimeControl    string    `json:"time_control"`     // "engine" | "1+0" | "3+2" | "10+5" | "corr-1d"
	Rated          bool      `json:"rated"`
	Status         string    `json:"status"`
	Result         string    `json:"result"` // "*" | "1-0" | "0-1" | "1/2-1/2"
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	IsPublic       bool      `json:"is_public"` // spectator mode: any user (and /watch/{id}) can read
}

// Invite represents a direct user-to-user challenge.
type Invite struct {
	ID          uuid.UUID  `json:"id"`
	FromUserID  int64      `json:"from_user_id"`
	ToUserID    int64      `json:"to_user_id"`
	TimeControl string     `json:"time_control"`
	Rated       bool       `json:"rated"`
	Status      string     `json:"status"` // pending|accepted|declined|expired|cancelled
	GameID      *string    `json:"game_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
}

// RatingUpdate captures the Glicko-2 delta to apply after a rated game.
// All four scalar fields move atomically; wins/losses/draws are +1/0
// counters chosen by the caller based on the game result.
type RatingUpdate struct {
	UserID     int64
	Rating     float32
	RD         float32
	Volatility float32
	Wins       int32 // 0 or 1
	Losses     int32 // 0 or 1
	Draws      int32 // 0 or 1
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
	SearchUsersByPrefix(prefix string) ([]UserSummary, error)
	UpdateUserProfile(id int64, displayName, bio, avatarURL, country string) error
	UpdateLastLogin(id int64) error
	UpdatePassword(id int64, passwordHash string) error
	UpdateUserRating(u RatingUpdate) error
	GetUserStats(id int64) (*UserStats, error)

	// Game management
	SaveGame(g *GameRecord) error
	// ListGames returns the user's games newer-than-cursor, ordered by
	// updated_at DESC, capped at limit. Pass a zero cursor for the first
	// page (interpreted as "now"); pass the last returned UpdatedAt for
	// the next page. limit <= 0 falls back to a sensible default.
	ListGames(userID int64, cursor time.Time, limit int) ([]GameRecord, error)
	GetGame(id string) (*GameRecord, error)
	// DeleteGame returns the number of rows removed; callers should treat 0
	// as a missing record (or one that was already deleted).
	DeleteGame(id string) (int64, error)
	// SetGameVisibility toggles the spectator-mode flag. Handler is
	// responsible for enforcing that the caller is a participant before
	// invoking this — the query itself scopes by row id only.
	SetGameVisibility(id string, isPublic bool) (int64, error)

	// Invites — the durable side of the per-user notification system.
	// Realtime push is via Redis user.evt.{id}; these endpoints serve the
	// reconnect backlog and post-action audit trail.
	CreateInvite(id uuid.UUID, fromUserID, toUserID int64, timeControl string, rated bool, expiresAt time.Time) (*Invite, error)
	GetInvite(id uuid.UUID) (*Invite, error)
	ListPendingInvitesForUser(userID int64) ([]Invite, error)
	ListPendingInvitesFromUser(userID int64) ([]Invite, error)
	// AcceptInviteWithGame atomically upserts the new game row AND flips
	// the invite to accepted in a single transaction. Returns the number
	// of invite rows updated (0 means the invite was no longer pending —
	// e.g. expired, declined, or already accepted — and the transaction
	// is rolled back so no orphan game is left behind). The earlier bare
	// AcceptInvite + SaveGame pair had a failure window where the invite
	// was marked accepted but the game row was missing, and was removed.
	AcceptInviteWithGame(inviteID uuid.UUID, toUserID int64, game *GameRecord) (int64, error)
	DeclineInvite(inviteID uuid.UUID, toUserID int64) (int64, error)
	CancelInvite(inviteID uuid.UUID, fromUserID int64) (int64, error)
	// ExpireStaleInvites flips pending → expired for invites past their
	// TTL, returning the affected rows so the sweeper can broadcast a
	// per-invite event. Called only by the leader-elected sweeper.
	ExpireStaleInvites() ([]Invite, error)
}
