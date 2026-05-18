package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
)

type GameService struct {
	db  db.Store
	bus *eventbus.Client
}

func main() {
	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	store, err := db.OpenPostgres(dbURL)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer store.Close()

	bus := eventbus.NewClient(redisAddr)
	defer bus.Close()
	s := &GameService{db: store, bus: bus}

	// rootCtx cancellation propagates to every background goroutine
	// (Run/listenToEngineResults/sweepers/rating-updater) so they exit
	// promptly on SIGTERM. The HTTP server uses its own Shutdown call
	// to drain in-flight requests with a separate timeout.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Health check and API server. All /api/invites/* routes assume the
	// gateway has already JWT-validated the caller and injected
	// ?user_id=N — no service downstream re-checks the token.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("GET /api/games", s.handleListGames)
	// Per-game endpoints are nested under /api/games/{id}/<verb>. The
	// id arrives via PathValue("id") and is read by handlers through
	// gameIDFrom(r), which falls back to the legacy ?game_id= query
	// param so any straggler caller still works during the rollout.
	mux.HandleFunc("GET /api/games/{id}/state", s.handleState)
	mux.HandleFunc("GET /api/games/{id}/can_watch", s.handleCanWatch)
	mux.HandleFunc("GET /api/games/{id}/replay", s.handleReplayData)
	mux.HandleFunc("POST /api/games/{id}/visibility", s.handleSetVisibility)

	// Sync game-mutation HTTP. See cmd/game/handlers.go for the
	// shared lock + ownership pattern.
	mux.HandleFunc("POST /api/games/{id}/move", s.handleHTTPMove)
	mux.HandleFunc("POST /api/games/{id}/resign", s.handleHTTPResign)
	mux.HandleFunc("POST /api/games/{id}/new", s.handleHTTPNew)
	mux.HandleFunc("POST /api/games/{id}/set_players", s.handleHTTPSetPlayers)
	mux.HandleFunc("POST /api/games/{id}/set_position", s.handleHTTPSetPosition)
	mux.HandleFunc("GET /api/games/{id}/pgn", s.handleHTTPDownloadPGN)
	mux.HandleFunc("POST /api/games/{id}/load_pgn", s.handleHTTPLoadPGN)
	mux.HandleFunc("POST /api/games/{id}/analyze", s.handleHTTPAnalyze)
	mux.HandleFunc("POST /api/games/{id}/undo", s.handleHTTPUndo)
	mux.HandleFunc("DELETE /api/games/{id}", s.handleHTTPDelete)
	mux.HandleFunc("POST /api/games/{id}/hint", s.handleHTTPHint)
	mux.HandleFunc("POST /api/games/{id}/draw_offer", s.handleDrawOffer)
	mux.HandleFunc("POST /api/games/{id}/draw_accept", s.handleDrawAccept)
	mux.HandleFunc("POST /api/games/{id}/draw_decline", s.handleDrawDecline)
	mux.HandleFunc("POST /api/games/{id}/takeback_offer", s.handleTakebackOffer)
	mux.HandleFunc("POST /api/games/{id}/takeback_accept", s.handleTakebackAccept)
	mux.HandleFunc("POST /api/games/{id}/takeback_decline", s.handleTakebackDecline)
	mux.HandleFunc("POST /api/games/{id}/rematch_offer", s.handleRematchOffer)
	mux.HandleFunc("POST /api/games/{id}/rematch_accept", s.handleRematchAccept)
	mux.HandleFunc("POST /api/games/{id}/rematch_decline", s.handleRematchDecline)

	mux.HandleFunc("POST /api/invites/send", s.handleSendInvite)
	mux.HandleFunc("GET /api/invites/pending", s.handleListPendingInvites)
	mux.HandleFunc("POST /api/invites/{id}/accept", s.handleAcceptInvite)
	mux.HandleFunc("POST /api/invites/{id}/decline", s.handleDeclineInvite)
	mux.HandleFunc("POST /api/invites/{id}/cancel", s.handleCancelInvite)

	// Studies. Saved positions + exploration trees, per-user. See
	// cmd/game/studies.go. Ownership is enforced inside each handler
	// (existence-leak rule: non-owner gets 404, not 403).
	mux.HandleFunc("POST /api/studies", s.handleCreateStudy)
	mux.HandleFunc("GET /api/studies", s.handleListStudies)
	mux.HandleFunc("GET /api/studies/{id}", s.handleGetStudy)
	mux.HandleFunc("PATCH /api/studies/{id}", s.handleUpdateStudy)
	mux.HandleFunc("DELETE /api/studies/{id}", s.handleDeleteStudy)

	// Temp game (anonymous, Redis-only, 10-min sliding TTL). See cmd/game/temp.go.
	// The whole /api/temp namespace IS the anonymous-games surface, so
	// per-game endpoints nest under /api/temp/{id}/<verb> rather than
	// /api/temp/games/{id}/<verb> — the "games" word would be redundant.
	mux.HandleFunc("POST /api/temp/session", s.handleTempSession)
	mux.HandleFunc("GET /api/temp/{id}/state", s.handleTempState)
	mux.HandleFunc("POST /api/temp/{id}/move", s.handleTempMove)
	mux.HandleFunc("POST /api/temp/{id}/new", s.handleTempNew)
	mux.HandleFunc("POST /api/temp/{id}/resign", s.handleTempResign)
	mux.HandleFunc("POST /api/temp/{id}/undo", s.handleTempUndo)
	mux.HandleFunc("POST /api/temp/{id}/hint", s.handleTempHint)
	mux.HandleFunc("POST /api/temp/{id}/set_players", s.handleTempSetPlayers)
	mux.HandleFunc("POST /api/temp/{id}/set_position", s.handleTempSetPosition)
	mux.HandleFunc("GET /api/temp/{id}/pgn", s.handleTempDownloadPGN)
	mux.HandleFunc("GET /api/temp/{id}/replay", s.handleTempReplayData)
	mux.HandleFunc("POST /api/temp/{id}/load_pgn", s.handleTempLoadPGN)
	mux.HandleFunc("POST /api/temp/{id}/analyze", s.handleTempAnalyze)
	// Internal-only — gateway calls this from handleSignup when
	// the signup request carried a chess-anon cookie. Not in the
	// public proxy table.
	mux.HandleFunc("POST /api/temp/upgrade", s.handleTempUpgrade)

	mux.Handle("/metrics", metrics.Handler())

	srv := &http.Server{
		Addr:    ":8080",
		Handler: metrics.HTTPMiddleware("game-service", mux),
	}

	var wg sync.WaitGroup
	startBg := func(name string, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(rootCtx)
			slog.Info("background goroutine exited", "name", name)
		}()
	}

	slog.Info("Game Service starting (Command Processor)...")
	// Seed the bot pool used by the engine-fallback matchmaker. Runs
	// once at boot, idempotent across replicas (ON CONFLICT on the
	// username unique). See cmd/game/bots.go.
	// TODO(matchmaker-engine-fallback): remove with the fallback.
	seedBots(store)
	startBg("engine-results", s.listenToEngineResults)
	startBg("invite-sweeper", s.runInviteSweeper)
	// Matchmaker absorbed from the former cmd/matchmaker pod. The
	// pairing loop is Redis-leader-elected so multiple game-service
	// replicas don't race the queue. See cmd/game/matchmaker.go.
	startBg("pairing", s.runPairingLoop)
	// Clock flag-fall sweeper. Per-game lock makes the work idempotent
	// across replicas — no leader election needed at this scale. See
	// cmd/game/clocks.go.
	startBg("clock-fall", s.runClockFallSweeper)
	// Glicko-2 rating updater. Consumes game:events (rating-updater-group)
	// and applies a one-game-per-period update on every rated GameFinished.
	// pkg/rating is numerically verified against the paper's worked
	// example — see pkg/rating/glicko2_test.go.
	startBg("rating-updater", s.runRatingUpdater)
	startBg("command-processor", s.Run)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("game-service HTTP server failed: %v", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	slog.Info("shutdown signal received; draining", "signal", sig.String())

	// 1) Stop accepting new HTTP connections and let in-flight requests
	//    finish. 15s matches the readiness-probe grace under our Traefik
	//    config — long enough for any single mutation, short enough that
	//    the kube terminationGracePeriod (30s default) absorbs both this
	//    drain and the goroutine wait below.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("HTTP shutdown error", "error", err)
	}

	// 2) Cancel rootCtx so every background consumer / sweeper exits.
	//    Consume() returns at the next read deadline (≤5s); sweepers'
	//    tickers fall through their select.
	rootCancel()
	wg.Wait()
	slog.Info("clean shutdown complete")
}
