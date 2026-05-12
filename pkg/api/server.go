package api

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/db"
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

// Server is now entirely stateless regarding active games.

type Server struct {
	podID string
	mux   *http.ServeMux
	db    db.Store
	hub   *Hub
	bus   *bus.Client

	// Rate limiters for expensive endpoints
	engineLimiter *RateLimiter // /api/hint, /api/assess
	gameLimiter   *RateLimiter // /api/games/new
}

func NewServer(database db.Store, eventBus *bus.Client) *Server {
	podID, _ := os.Hostname()
	if podID == "" {
		podID = "api-pod"
	}

	s := &Server{
		podID:         podID,
		db:            database,
		hub:           NewHub(eventBus),
		bus:           eventBus,
		engineLimiter: NewRateLimiter(10, 10, time.Minute), // 10 engine requests per minute per IP
		gameLimiter:   NewRateLimiter(5, 5, time.Minute),   // 5 new games per minute per IP
	}
	// Pod lifetime is the server lifetime.
	go s.hub.Run(context.Background())
	s.listenToEngine()
	s.StartClockTicker()

	// Leader-elected workers.
	s.startInviteSweeper(context.Background())
	s.startMatchmaker(context.Background())
	s.startRatingUpdater(context.Background())
	s.startClockManager(context.Background())

	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := s.mux
	// Middleware chain — listed outermost-to-innermost (last wraps first).
	// auth.Middleware populates request context with the current user
	// (from JWT cookie) so every handler can call auth.GetUser.
	var h http.Handler = handler
	h = auth.Middleware(h)
	h = RecoveryMiddleware(h)
	h = LoggerMiddleware(h)
	h = MetricsMiddleware(h)
	h = SecurityHeadersMiddleware(h)
	h = CORSMiddleware(h)
	h = BodyLimitMiddleware(maxRequestBody)(h)
	h.ServeHTTP(w, r)
}

func (s *Server) StartClockTicker() {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			// In a million-dollar platform, this would be optimized with a Redis set of active game IDs.
			// For now, we'll implement the logic in syncGameToDB when a move is played.
			// TODO: Periodic broadcast for spectators.
		}
	}()
}

// Shutdown gracefully shuts down the server. Since we are stateless,
// no active games need to be flushed to the database.
func (s *Server) Shutdown(ctx context.Context) {
	slog.Info("starting graceful shutdown")
	// Any pending requests will finish up to ctx timeout
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
