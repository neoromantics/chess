package api

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neoromantics/chess/pkg/bus"
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
}

func NewServer(database db.Store, eventBus *bus.Client) *Server {
	s := &Server{
		db:       database,
		hub:      NewHub(),
		bus:      eventBus,
		games:    make(map[string]*gameEntry),
		lastPing: time.Now(),
	}
	go s.hub.Run()
	s.listenToEngine()
	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
	if r.Method == "OPTIONS" {
		return
	}
	handler := RecoveryMiddleware(s.mux)
	handler = LoggerMiddleware(handler)
	handler = SecurityHeadersMiddleware(handler)
	handler.ServeHTTP(w, r)
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
