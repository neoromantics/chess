package api

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/game"
)

//go:embed all:dist
var frontendDist embed.FS

var (
	indexHTML  []byte
	replayHTML []byte
	assetsFS   http.FileSystem
)

func init() {
	var err error
	indexHTML, err = frontendDist.ReadFile("dist/index.html")
	if err != nil {
		// Fallback for development if assets aren't built yet
		indexHTML = []byte("<html><body>Frontend not built. Run 'just build'</body></html>")
	}
	replayHTML, err = frontendDist.ReadFile("dist/replay.html")
	if err != nil {
		replayHTML = []byte("<html><body>Replay not built.</body></html>")
	}
	sub, err := fs.Sub(frontendDist, "dist")
	if err != nil {
		slog.Error("failed to create assets sub-filesystem", "error", err)
	} else {
		assetsFS = http.FS(sub)
	}
}

const replayPlaceholder = "REPLAY_DATA_PLACEHOLDER"

// maxRequestBody is the maximum allowed request body size (1 MB).
const maxRequestBody int64 = 1 << 20

type gameEntry struct {
	game       *game.Game
	stopSearch atomic.Bool
	eventFired atomic.Bool
	lastUsed   time.Time
	id         string
	userID     int64
	sessionID  string
	createdAt  time.Time

	whiteThinkTime time.Duration
	blackThinkTime time.Duration
}

type Server struct {
	mux *http.ServeMux
	db  db.Store
	hub *Hub
	bus *bus.Client

	mu    sync.Mutex
	games map[string]*gameEntry

	lastPing time.Time

	// Rate limiters for expensive endpoints
	engineLimiter *RateLimiter // /api/hint, /api/assess
	gameLimiter   *RateLimiter // /api/games/new
}

func NewServer(database db.Store, eventBus *bus.Client) *Server {
	s := &Server{
		db:            database,
		hub:           NewHub(),
		bus:           eventBus,
		games:         make(map[string]*gameEntry),
		lastPing:      time.Now(),
		engineLimiter: NewRateLimiter(10, 10, time.Minute), // 10 engine requests per minute per IP
		gameLimiter:   NewRateLimiter(5, 5, time.Minute),   // 5 new games per minute per IP
	}
	go s.hub.Run()
	go s.listenToGameUpdates()
	s.listenToEngine()
	s.StartClockTicker()
	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := s.mux
	// Apply middleware chain (innermost first)
	var h http.Handler = handler
	h = RecoveryMiddleware(h)
	h = LoggerMiddleware(h)
	h = SecurityHeadersMiddleware(h)
	h = CORSMiddleware(h)
	h = BodyLimitMiddleware(maxRequestBody)(h)
	h.ServeHTTP(w, r)
}

func (s *Server) StartClockTicker() {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("clock ticker panic recovered", "error", err)
				// Restart the ticker after recovery
				s.StartClockTicker()
			}
		}()

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.mu.Lock()
			now := time.Now()
			for id, entry := range s.games {
				gm := entry.game
				if gm.Status() == game.StatusOngoing {
					elapsed := now.Sub(gm.LastTick).Milliseconds()
					if gm.Board.SideToMove == core.White {
						gm.WhiteTime -= elapsed
					} else {
						gm.BlackTime -= elapsed
					}
					gm.LastTick = now

					// Detect timeout
					if gm.WhiteTime <= 0 || gm.BlackTime <= 0 {
						if gm.WhiteTime < 0 {
							gm.WhiteTime = 0
						}
						if gm.BlackTime < 0 {
							gm.BlackTime = 0
						}
						slog.Info("game timeout detected", "game_id", id)
						// syncGameToDB will broadcast the terminal state
						s.mu.Unlock()
						s.syncGameToDB(entry, nil)
						s.mu.Lock()
					} else {
						// Periodic sync/broadcast to keep clients updated
						s.hub.BroadcastEvent(id, "state", s.snapshotLocked(entry))
					}
				}
			}
			s.mu.Unlock()
		}
	}()
}

func (s *Server) StartCacheManager() {
	go func() {
		for {
			time.Sleep(1 * time.Minute)

			// 1. Manage In-Memory Game Cache
			s.mu.Lock()
			now := time.Now()
			for id, entry := range s.games {
				if now.Sub(entry.lastUsed) > 10*time.Minute {
					slog.Info("evicting game from memory cache", "game_id", id)
					delete(s.games, id)
				}
			}

			// 2. Optional: Idle Server Shutdown
			// idle := time.Since(s.lastPing)
			s.mu.Unlock()

			// if idle > d {
			// 	slog.Info("server idle timeout triggered")
			// 	os.Exit(0)
			// }
		}
	}()
}

// Shutdown gracefully shuts down the server, closing WebSocket connections
// and flushing active game states to the database.
func (s *Server) Shutdown(ctx context.Context) {
	slog.Info("starting graceful shutdown")

	// Save all active games to database
	s.mu.Lock()
	for id, entry := range s.games {
		slog.Info("saving game before shutdown", "game_id", id)
		entry.stopSearch.Store(true)
	}
	entries := make([]*gameEntry, 0, len(s.games))
	for _, e := range s.games {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	for _, entry := range entries {
		s.syncGameToDB(entry, nil)
	}

	slog.Info("graceful shutdown complete")
}

// HealthStatus represents the response from the health check endpoint.
type HealthStatus struct {
	Status string `json:"status"`
	Time   string `json:"time"`
	Redis  string `json:"redis"`
	DB     string `json:"db"`
}

// CheckHealth returns the health status including dependency checks.
func (s *Server) CheckHealth() HealthStatus {
	h := HealthStatus{
		Status: "ok",
		Time:   time.Now().Format(time.RFC3339),
		Redis:  "ok",
		DB:     "ok",
	}

	// Check Redis
	if err := s.bus.Ping(context.Background()); err != nil {
		h.Redis = "error: " + err.Error()
		h.Status = "degraded"
	}

	// Check Database
	if err := s.db.Ping(); err != nil {
		h.DB = "error: " + err.Error()
		h.Status = "degraded"
	}

	return h
}

func (s *Server) listenToGameUpdates() {
	err := s.bus.Subscribe(context.Background(), bus.GameUpdatedChannel, func(payload []byte) {
		var event bus.GameUpdatedEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return
		}

		s.mu.Lock()
		_, exists := s.games[event.GameID]
		s.mu.Unlock()

		if exists {
			s.refreshGameFromDB(event.GameID)
		}
	})
	if err != nil {
		slog.Error("failed to subscribe to game updates", "error", err)
	}
}

func (s *Server) refreshGameFromDB(id string) {
	record, err := s.db.GetGame(id)
	if err != nil {
		return
	}

	s.mu.Lock()
	entry, exists := s.games[id]
	if !exists {
		s.mu.Unlock()
		return
	}

	var history, historySAN []string
	json.Unmarshal([]byte(record.History), &history)
	json.Unmarshal([]byte(record.HistorySAN), &historySAN)

	// Preserve the engine configurations, but update the FEN and history
	entry.game.Load(record.FEN, history, record.EngineWhite, record.EngineBlack)
	entry.game.HistorySAN = historySAN
	entry.whiteThinkTime = time.Duration(record.WhiteThinkTime) * time.Millisecond
	entry.blackThinkTime = time.Duration(record.BlackThinkTime) * time.Millisecond

	snapshot := s.snapshotLocked(entry)
	s.mu.Unlock()

	s.hub.BroadcastState(id, snapshot)
}
