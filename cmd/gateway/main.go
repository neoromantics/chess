package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
)

type Gateway struct {
	userSvcURL *url.URL
	gameSvcURL *url.URL
	bus        *eventbus.Client
	hub        *Hub
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	userSvcURLStr := os.Getenv("USER_SERVICE_URL")
	if userSvcURLStr == "" {
		userSvcURLStr = "http://user-service:8081"
	}
	userSvcURL, err := url.Parse(userSvcURLStr)
	if err != nil {
		log.Fatalf("invalid user service URL: %v", err)
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
	hub := NewHub(bus)

	gw := &Gateway{
		userSvcURL: userSvcURL,
		gameSvcURL: gameSvcURL,
		bus:        bus,
		hub:        hub,
	}

	// Start WebSocket Hub
	go hub.Run(context.Background())

	mux := http.NewServeMux()

	// 1. Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("Gateway OK"))
	})

	// 2. Reverse proxy to User Service. /api/users/ (plural) is the
	// search endpoint used by the invite autocomplete; the singular
	// /api/user/ is self-service (me/profile/password/stats).
	userProxy := httputil.NewSingleHostReverseProxy(userSvcURL)
	mux.HandleFunc("/api/auth/", userProxy.ServeHTTP)
	mux.HandleFunc("/api/user/", userProxy.ServeHTTP)
	mux.HandleFunc("/api/users/", userProxy.ServeHTTP)

	// 3. Reverse proxy to Game Service. Wrapped in injectAuthedUser so
	// downstream services don't have to re-validate the JWT — they
	// trust the gateway-injected ?user_id=X. Without this, /api/games
	// returns 400 because game-service requires user_id and the
	// frontend has no business knowing it (and shouldn't be trusted to
	// send it — that would let any caller list any user's games).
	gameProxy := httputil.NewSingleHostReverseProxy(gameSvcURL)
	mux.Handle("/api/state", gw.injectAuthedUser(gameProxy))
	mux.Handle("/api/games", gw.injectAuthedUser(gameProxy))

	// All single-game mutations now route through game-service over
	// sync HTTP. Gateway only authenticates + injects user_id; game-
	// service holds the per-game lock and owns the snapshot synthesis.
	// See CLAUDE.md ("Streams vs HTTP") for why these aren't Commands.
	mux.Handle("POST /api/move", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/new", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/set_players", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/undo", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/touch", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/touch_move", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/load", gw.injectAuthedUser(gameProxy))
	mux.Handle("DELETE /api/games/delete", gw.injectAuthedUser(gameProxy))
	mux.Handle("GET /api/save", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/hint", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/assess", gw.injectAuthedUser(gameProxy))

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
	// caller without re-validating JWTs.
	mux.Handle("/api/invites/", gw.injectAuthedUser(gameProxy))

	// 5c. Replay. Gateway owns the embedded replay.html template;
	// game-service supplies the per-ply JSON frames. We substitute the
	// placeholder and serve a single HTML doc — same UX as the old
	// monolith's handleReplay, just split across two services.
	mux.HandleFunc("GET /api/replay.html", gw.handleReplay)

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
	log.Fatal(http.ListenAndServe(":"+port, metrics.HTTPMiddleware("gateway", auth.Middleware(mux))))
}

func (gw *Gateway) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}

	gameID := uuid.New().String()

	cmd := eventbus.Command{
		Type:   eventbus.CmdNewGame,
		GameID: gameID,
		UserID: user.UserID,
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
// user's ID is appended to the proxied request as ?user_id=N. This is
// the gateway's job, not the frontend's: the SPA sends a JWT cookie,
// the gateway resolves the user once, and downstream services trust the
// query param. Doing this client-side would let any caller list anyone
// else's games.
func (gw *Gateway) injectAuthedUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.GetUser(r.Context())
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		q := r.URL.Query()
		q.Set("user_id", strconv.FormatInt(user.UserID, 10))
		r.URL.RawQuery = q.Encode()
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
		Rating      int    `json:"rating"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if req.TimeControl == "" {
		http.Error(w, "missing time_control", 400)
		return
	}
	payload, _ := json.Marshal(eventbus.JoinQueueCmd{TimeControl: req.TimeControl, Rating: req.Rating})
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
// "is userID a participant on gameID?". We piggy-back on /api/state's
// existing ownership check (which returns 404 for non-participants) so
// we don't need a new dedicated authz endpoint.
func (gw *Gateway) userMayWatchGame(ctx context.Context, userID int64, gameID string) bool {
	u := *gw.gameSvcURL
	u.Path = "/api/state"
	q := u.Query()
	q.Set("game_id", gameID)
	q.Set("user_id", strconv.FormatInt(userID, 10))
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
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

	// Pull the JSON frames from game-service.
	dataURL := *gw.gameSvcURL
	dataURL.Path = "/api/replay"
	q := dataURL.Query()
	q.Set("game_id", gameID)
	dataURL.RawQuery = q.Encode()
	resp, err := http.Get(dataURL.String())
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
