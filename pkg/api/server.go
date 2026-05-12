package api

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/taiyanliu/chess/pkg/auth"
	"github.com/taiyanliu/chess/pkg/db"
	"github.com/taiyanliu/chess/pkg/game"
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
		panic(fmt.Sprintf("failed to read index.html: %v", err))
	}
	replayHTML, err = frontendDist.ReadFile("dist/replay.html")
	if err != nil {
		panic(fmt.Sprintf("failed to read replay.html: %v", err))
	}
	sub, err := fs.Sub(frontendDist, "dist")
	if err != nil {
		panic(err)
	}
	assetsFS = http.FS(sub)
}

const replayPlaceholder = "REPLAY_DATA_PLACEHOLDER"

type gameEntry struct {
	game       *game.Game
	thinking   atomic.Bool
	stopSearch atomic.Bool
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

	mu    sync.Mutex
	games map[string]*gameEntry

	lastPing time.Time
}

func NewServer(database db.Store) *Server {
	s := &Server{
		db:       database,
		hub:      NewHub(),
		games:    make(map[string]*gameEntry),
		lastPing: time.Now(),
	}
	go s.hub.Run()
	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /api/auth/signup", s.handleSignup)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/user/me", s.handleMe)
	s.mux.HandleFunc("POST /api/games/new", s.handleCreateGame)
	s.mux.HandleFunc("GET /api/games", s.handleListGames)
	s.mux.HandleFunc("DELETE /api/games/delete", s.handleDeleteGame)
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("POST /api/move", s.handleMove)
	s.mux.HandleFunc("POST /api/new", s.handleNew)
	s.mux.HandleFunc("POST /api/hint", s.handleHint)
	s.mux.HandleFunc("POST /api/assess", s.handleAssess)
	s.mux.HandleFunc("POST /api/set_players", s.handleSetPlayers)
	s.mux.HandleFunc("POST /api/touch", s.handleTouch)
	s.mux.HandleFunc("POST /api/touch_move", s.handleTouchMove)
	s.mux.HandleFunc("POST /api/undo", s.handleUndo)
	s.mux.HandleFunc("POST /api/ping", s.handlePing)
	s.mux.HandleFunc("GET /api/save", s.handleSave)
	s.mux.HandleFunc("POST /api/load", s.handleLoad)
	s.mux.HandleFunc("GET /api/replay.html", s.handleReplay)
	s.mux.HandleFunc("GET /ws", s.handleWS)
	s.mux.Handle("GET /assets/", http.FileServer(assetsFS))
	// Catch-all route for SPA navigation (Dashboard, GameView, etc.)
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /{path...}", s.handleIndex)
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
	handler = auth.Middleware(handler)
	handler.ServeHTTP(w, r)
}

func (s *Server) StartIdleShutdown(d time.Duration) {
	go func() {
		for {
			time.Sleep(5 * time.Second)
			s.mu.Lock()
			idle := time.Since(s.lastPing)
			s.mu.Unlock()
			if idle > d {
				// os.Exit(0)
			}
		}
	}()
}
