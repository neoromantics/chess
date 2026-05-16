package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
)

type Gateway struct {
	gameSvcURL *url.URL
	bus        *eventbus.Client
	db         db.Store
	hub        *Hub
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	gameSvcURLStr := os.Getenv("GAME_SERVICE_URL")
	if gameSvcURLStr == "" {
		gameSvcURLStr = "http://game-service:8080"
	}
	gameSvcURL, err := url.Parse(gameSvcURLStr)
	if err != nil {
		log.Fatalf("invalid game service URL: %v", err)
	}

	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	bus := eventbus.NewClient(redisAddr)
	defer bus.Close()
	hub := NewHub(bus)

	// Gateway absorbed user-service in the 6→3 pod consolidation. It
	// opens its own pool against Postgres for auth + profile reads.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required (gateway now owns the auth/user surface)")
	}
	store, err := db.OpenPostgres(dsn)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer store.Close()

	gw := &Gateway{
		gameSvcURL: gameSvcURL,
		bus:        bus,
		db:         store,
		hub:        hub,
	}

	// rootCtx cancellation propagates to the hub and its forward
	// goroutines so they exit promptly on SIGTERM. The HTTP server
	// uses its own Shutdown call for in-flight request draining.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Start WebSocket Hub
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		hub.Run(rootCtx)
	}()

	mux := http.NewServeMux()

	// 1. Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("Gateway OK"))
	})

	// 2. Auth + profile + user-search handled directly. Used to live in
	// the chess-user-service pod; absorbed here to eliminate one
	// cross-service network hop and one wire-protocol surface.
	mux.HandleFunc("POST /api/auth/signup", gw.handleSignup)
	mux.HandleFunc("POST /api/auth/login", gw.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", gw.handleLogout)
	mux.HandleFunc("GET /api/user/me", gw.handleMe)
	mux.HandleFunc("GET /api/user/profile", gw.handleGetProfile)
	mux.HandleFunc("PUT /api/user/profile", gw.handleUpdateProfile)
	mux.HandleFunc("POST /api/user/password", gw.handleChangePassword)
	mux.HandleFunc("GET /api/user/stats", gw.handleUserStats)
	mux.HandleFunc("GET /api/users/search", gw.handleUserSearch)
	// Live preflight for the Signup form; debounced from the client
	// side. Returns {available, reason}; auth-free since it gates a
	// flow that's itself auth-free. See cmd/gateway/user_handlers.go.
	mux.HandleFunc("GET /api/auth/check-username", gw.handleCheckUsername)

	// Admin dashboard (read-only). adminOnly inside each handler
	// 404s for callers without is_admin=TRUE on their users row.
	// Bootstrap the first admin via SQL (one-off):
	//   UPDATE users SET is_admin = TRUE WHERE username = '<you>';
	mux.HandleFunc("GET /api/admin/overview", gw.handleAdminOverview)
	mux.HandleFunc("GET /api/admin/signups", gw.handleAdminSignups)
	mux.HandleFunc("GET /api/admin/actions", gw.handleAdminActions)
	mux.HandleFunc("GET /api/admin/live_games", gw.handleAdminLiveGames)
	mux.HandleFunc("GET /api/admin/bots", gw.handleAdminBots)
	// DELETE /api/admin/users/{id} — body {"confirm_username": "..."}.
	// adminOnly + self-deletion guard + bot guard + typed-confirm.
	mux.HandleFunc("DELETE /api/admin/users/{id}", gw.handleAdminDeleteUser)

	// 3. Reverse proxy to Game Service. Wrapped in injectAuthedUser so
	// downstream services don't have to re-validate the JWT — they
	// trust the gateway-injected ?user_id=X. Without this, /api/games
	// returns 400 because game-service requires user_id and the
	// frontend has no business knowing it (and shouldn't be trusted to
	// send it — that would let any caller list any user's games).
	gameProxy := httputil.NewSingleHostReverseProxy(gameSvcURL)
	// /api/state is auth-optional so anonymous spectator links work for
	// public games. Game-service's handleState uses the spectator-aware
	// userMayRead predicate — private games still 404 for non-owners.
	mux.Handle("GET /api/state", gw.injectAuthedUserOptional(gameProxy))
	mux.Handle("GET /api/games", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/visibility", gw.injectAuthedUser(gameProxy))

	// All single-game mutations now route through game-service over
	// sync HTTP. Gateway only authenticates + injects user_id; game-
	// service holds the per-game lock and owns the snapshot synthesis.
	// See CLAUDE.md ("Streams vs HTTP") for why these aren't Commands.
	mux.Handle("POST /api/move", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/resign", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/new", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/set_players", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/set_position", gw.injectAuthedUser(gameProxy))
	mux.Handle("GET /api/pgn", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/load_pgn", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/analyze", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/undo", gw.injectAuthedUser(gameProxy))
	mux.Handle("DELETE /api/games/delete", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/hint", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/draw_offer", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/draw_accept", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/draw_decline", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/takeback_offer", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/takeback_accept", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/takeback_decline", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/rematch_offer", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/rematch_accept", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/rematch_decline", gw.injectAuthedUser(gameProxy))

	// 4. New game (intent dispatch). Async: gateway publishes a Command
	// onto the game:commands stream and returns the assigned game_id
	// immediately — game-service consumes the stream and writes the
	// row. The frontend SPA navigates to /game/{id} on response and
	// the GameView is allowed to load before the row exists because
	// /api/state on a missing row returns 404 and the view retries.
	mux.HandleFunc("POST /api/games/new", gw.handleCreateGame)

	// 5. Matchmaking (intent dispatch). Same Command pattern as
	// games/new: gateway translates HTTP → Command, matchmaker
	// service consumes, MatchFound event fans out via user.evt.
	mux.HandleFunc("POST /api/matchmaking/join", gw.handleJoinQueue)
	mux.HandleFunc("POST /api/matchmaking/leave", gw.handleLeaveQueue)

	// 5b. Invites (synchronous CRUD, not Command-based). Game-service
	// owns the durable PG row and the realtime user.evt fan-out.
	// injectAuthedUser appends ?user_id=N so game-service knows the
	// caller without re-validating JWTs. Prefix-mounted because the
	// subtree mixes GET (list) and POST (send/accept/decline/cancel);
	// game-service's mux enforces methods on each leaf route.
	mux.Handle("/api/invites/", gw.injectAuthedUser(gameProxy))

	// 5c. Replay. Gateway owns the embedded replay.html template;
	// game-service supplies the per-ply JSON frames. We substitute the
	// placeholder and serve a single HTML doc — same UX as the old
	// monolith's handleReplay, just split across two services.
	mux.HandleFunc("GET /api/replay.html", gw.handleReplay)

	// 5d. Anonymous temp games. The chess-anon HttpOnly cookie carries
	// an opaque UUID injected as an X-Anon-ID header on every proxied
	// /api/temp/* request. game-service trusts the injection (mirrors
	// the X-User-ID pattern). See cmd/game/temp.go for the storage model.
	mux.Handle("/api/temp/", gw.injectAnonID(gameProxy))

	// 6. WebSockets
	mux.HandleFunc("/ws", gw.handleWSGame)
	mux.HandleFunc("/ws/user", gw.handleWSUser)

	// 7. Static Assets & SPA Routing
	mux.HandleFunc("/", gw.handleIndex)

	// 8. Prometheus scrape endpoint. Lives outside auth.Middleware so
	// the in-cluster Prometheus can scrape without a JWT.
	mux.Handle("/metrics", metrics.Handler())

	log.Printf("Gateway Service starting on port %s...", port)
	// metrics.HTTPMiddleware wraps the auth layer so we record every
	// request including the unauthenticated ones (signup, login, /metrics
	// itself, the SPA index). auth.Middleware is the inner wrap because
	// the metrics labels don't care about who the user is.
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: metrics.HTTPMiddleware("gateway", auth.Middleware(mux)),
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("gateway HTTP server failed: %v", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	slog.Info("shutdown signal received; draining", "signal", sig.String())

	// 1) Stop accepting new HTTP connections; drain in-flight requests.
	//    WebSocket connections held open by Hijack will be closed by the
	//    server's connection-state callback when their TCP conn drops —
	//    Shutdown can't drain them, so 5s is plenty for HTTP requests.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("HTTP shutdown error", "error", err)
	}

	// 2) Cancel rootCtx so the hub's forward goroutines and per-PubSub
	//    readers exit cleanly.
	rootCancel()
	wg.Wait()
	slog.Info("clean shutdown complete")
}

func (gw *Gateway) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}

	// Dashboard sends { engine_white, engine_black, white_think_time,
	// black_think_time }. We don't yet support split per-side think times
	// from the dashboard form, so collapse to one. Body is optional —
	// missing fields fall back to the human-vs-engine defaults.
	var body struct {
		EngineWhite    bool `json:"engine_white"`
		EngineBlack    bool `json:"engine_black"`
		WhiteThinkTime int  `json:"white_think_time"`
		BlackThinkTime int  `json:"black_think_time"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	think := body.WhiteThinkTime
	if think == 0 {
		think = body.BlackThinkTime
	}

	gameID := uuid.New().String()
	payload, _ := json.Marshal(eventbus.NewGameCmd{
		EngineWhite: body.EngineWhite,
		EngineBlack: body.EngineBlack || (!body.EngineWhite && !body.EngineBlack), // default: engine plays black
		ThinkTimeMS: think,
	})
	cmd := eventbus.Command{
		Type:    eventbus.CmdNewGame,
		GameID:  gameID,
		UserID:  user.UserID,
		Payload: payload,
	}

	if _, err := gw.bus.SendCommand(r.Context(), cmd); err != nil {
		http.Error(w, "failed to dispatch game creation", 500)
		return
	}

	// game_id (not "id") matches the frontend contract in api.ts and the
	// rest of the per-game endpoints which all key on game_id query param.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"game_id": gameID})
}

// injectAuthedUser wraps a reverse-proxy handler so the authenticated
// user's ID is forwarded on the proxied request as an X-User-ID
// header. The SPA sends a JWT cookie, the gateway resolves the user
// once, and downstream services trust the gateway-set header. Doing
// this client-side would let any caller list anyone else's games.
func (gw *Gateway) injectAuthedUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.GetUser(r.Context())
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		r.Header.Set("X-User-ID", strconv.FormatInt(user.UserID, 10))
		next.ServeHTTP(w, r)
	})
}

// injectAuthedUserOptional is the spectator-aware variant of
// injectAuthedUser. If the caller is signed in, injects X-User-ID
// (so participant + private-game reads keep working); otherwise lets
// the request through with no identity attached. The downstream
// handler's userMayRead predicate decides whether to serve the
// snapshot (always for public, participant-only for private).
func (gw *Gateway) injectAuthedUserOptional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, ok := auth.GetUser(r.Context()); ok {
			r.Header.Set("X-User-ID", strconv.FormatInt(user.UserID, 10))
		}
		next.ServeHTTP(w, r)
	})
}

// anonCookieName is the HttpOnly cookie identifying an anonymous
// browser session. Value is an opaque UUID; the gateway is the only
// component that mints it. Game-service receives it as ?anon_id=...
// via injectAnonID, never as a cookie directly.
const anonCookieName = "chess-anon"

// readAnonCookie returns the cookie value, or "" if absent/empty.
func readAnonCookie(r *http.Request) string {
	c, err := r.Cookie(anonCookieName)
	if err != nil || c == nil {
		return ""
	}
	return c.Value
}

// setAnonCookie writes a fresh anon cookie. 7-day MaxAge is just a
// safety ceiling; the *real* lifetime is the temp-game Redis TTL
// (10 min sliding) — once the game expires, the cookie is just a
// pointer to nothing and a future /api/temp/session call mints a
// new game under the same cookie.
func setAnonCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     anonCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure is set by the reverse proxy (Traefik) terminating
		// TLS in production; cookie is only ever served over HTTPS
		// in that environment.
	})
}

// injectAnonID wraps a reverse-proxy handler so the caller's anon
// session ID lands on the proxied request as the X-Anon-ID header.
// Mints the cookie on first hit. Symmetric with injectAuthedUser but
// uses the cookie rather than the JWT context.
func (gw *Gateway) injectAnonID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anonID := readAnonCookie(r)
		if anonID == "" {
			anonID = uuid.New().String()
			setAnonCookie(w, anonID)
		}
		r.Header.Set("X-Anon-ID", anonID)
		next.ServeHTTP(w, r)
	})
}

// handleJoinQueue dispatches a JoinQueue command. The matchmaker pairing
// loop runs every 2s and emits a MatchFound event when two players in
// the same time-control bucket can be paired; the gateway's user-channel
// fan-out delivers it to the joined client's WebSocket, which navigates
// to the new game.
func (gw *Gateway) handleJoinQueue(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var req struct {
		TimeControl string `json:"time_control"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if req.TimeControl == "" {
		http.Error(w, "missing time_control", 400)
		return
	}
	// Pull the authoritative rating from the row — never trust a
	// client-supplied number, or a 1200 player could queue as "2400"
	// and farm wins from real high-Elo opponents.
	rating := 1500
	if dbUser, err := gw.db.GetUserByID(user.UserID); err == nil && dbUser != nil {
		rating = int(dbUser.Rating)
	}
	payload, _ := json.Marshal(eventbus.JoinQueueCmd{TimeControl: req.TimeControl, Rating: rating})
	cmd := eventbus.Command{
		Type:    eventbus.CmdJoinQueue,
		UserID:  user.UserID,
		Payload: payload,
	}
	if _, err := gw.bus.SendCommand(r.Context(), cmd); err != nil {
		http.Error(w, "failed to dispatch join queue", 500)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// userMayWatchGame is the WS upgrade pre-flight that asks game-service
// "may userID read gameID?". Hits /api/can_watch — a bare ownership
// check, no snapshot synthesis. The game-service handler is spectator-
// aware: signed-in participants pass on every game; anyone passes on
// public games. Pass userID=0 for anonymous viewers.
func (gw *Gateway) userMayWatchGame(ctx context.Context, userID int64, gameID string) bool {
	u := *gw.gameSvcURL
	u.Path = "/api/can_watch"
	q := u.Query()
	q.Set("game_id", gameID)
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if userID > 0 {
		req.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("ws authz preflight failed", "error", err)
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// handleReplay serves a self-contained replay HTML page for a given
// game. The replay.html in cmd/gateway/dist carries the literal token
// "REPLAY_DATA_PLACEHOLDER" inside a <script id="replay-data"> tag;
// Replay.vue's onMounted reads from that element. We substitute the
// token with the live ReplayFrame JSON fetched from game-service so
// the SPA boots with the data already inline (no second roundtrip
// after page load).
//
// Auth is intentionally NOT required here. The replay payload is just
// the move history of an already-existing game; lock this down later
// if/when we add private games or anti-scrape concerns.
func (gw *Gateway) handleReplay(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", http.StatusBadRequest)
		return
	}

	// Temp games live in a separate Redis namespace; route to the
	// matching game-service endpoint. Durable IDs go to /api/replay,
	// temp IDs (always prefixed "temp-") go to /api/temp/replay.
	dataURL := *gw.gameSvcURL
	if strings.HasPrefix(gameID, "temp-") {
		dataURL.Path = "/api/temp/replay"
	} else {
		dataURL.Path = "/api/replay"
	}
	q := dataURL.Query()
	q.Set("game_id", gameID)
	dataURL.RawQuery = q.Encode()
	// Forward the caller's identity so game-service can enforce the same
	// userMayRead check it does on /api/state. Without this, a private
	// game's replay was reachable to anyone who knew the UUID — the
	// spectator-mode flag (commit 2b6fb78) silently stopped applying
	// because the conditional auth in handleReplayData never fired.
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, dataURL.String(), nil)
	if user, ok := auth.GetUser(r.Context()); ok {
		req.Header.Set("X-User-ID", strconv.FormatInt(user.UserID, 10))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("replay: fetch frames failed", "error", err)
		http.Error(w, "replay unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "game not found", resp.StatusCode)
		return
	}
	frames, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "replay read failed", http.StatusBadGateway)
		return
	}

	// Substitute the placeholder. The template is small (~hundreds of
	// bytes after Vite's singlefile inline plugin) so a single
	// bytes.Replace is fine; if it ever grows, switch to text/template.
	html := bytes.Replace(replayHTML, []byte(replayDataPlaceholder), frames, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(html)
}

func (gw *Gateway) handleLeaveQueue(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	var req struct {
		TimeControl string `json:"time_control"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if req.TimeControl == "" {
		http.Error(w, "missing time_control", 400)
		return
	}
	payload, _ := json.Marshal(eventbus.LeaveQueueCmd{TimeControl: req.TimeControl})
	cmd := eventbus.Command{
		Type:    eventbus.CmdLeaveQueue,
		UserID:  user.UserID,
		Payload: payload,
	}
	if _, err := gw.bus.SendCommand(r.Context(), cmd); err != nil {
		http.Error(w, "failed to dispatch leave queue", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
