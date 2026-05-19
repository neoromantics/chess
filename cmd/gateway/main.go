package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
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
	// upstream is the bounded HTTP client used for every explicit
	// gateway→game-service call (can-watch preflight, replay frame
	// fetch, anon-game upgrade). Shares its Transport with the reverse
	// proxy so connection pooling is unified. See the construction in
	// main() for the timeout rationale.
	upstream *http.Client
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

	// Bounded outbound HTTP transport. Shared by the reverse proxy and
	// every explicit gateway→game-service call. Before this, an upstream
	// hang let gateway goroutines pile up forever: http.DefaultTransport
	// has no DialContext deadline and no ResponseHeaderTimeout, so a
	// dead game-service pod would silently sink WS-upgrade preflights,
	// replay frame fetches, and proxied API calls without timing out.
	//
	// Why these numbers:
	//   - DialContext.Timeout=3s: in-cluster Service DNS resolution +
	//     TCP handshake is sub-100ms on a healthy network; 3s is
	//     generous head-room for transient blips.
	//   - ResponseHeaderTimeout=30s: caps the slowest legitimate path.
	//     /analyze dispatches engine jobs async and returns "ok" in
	//     <200ms; every other endpoint is sub-second. 30s is the wall.
	//   - Client.Timeout=30s: belt-and-suspenders for the explicit
	//     gw.upstream.Do() callers. Reverse proxy ignores this (uses
	//     the Transport directly), so /analyze's async-pattern dispatch
	//     still works.
	upstreamTransport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	upstreamClient := &http.Client{
		Transport: upstreamTransport,
		Timeout:   30 * time.Second,
	}

	gw := &Gateway{
		gameSvcURL: gameSvcURL,
		bus:        bus,
		db:         store,
		hub:        hub,
		upstream:   upstreamClient,
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

	// Per-IP rate limiters protecting the auth surface. Numbers chosen
	// for "stops abuse, doesn't block legitimate users":
	//   - authLim:   5 burst @ 1/5s sustained = 12/min. A user typing
	//                their password wrong once or twice never trips it;
	//                a brute-forcer hits the wall in seconds. Each call
	//                costs a cost-14 bcrypt verify, so the cap doubles
	//                as a CPU-DoS guard.
	//   - signupLim: 3 burst @ 1/30s sustained = 2/min. Sign-up is
	//                rarer than login by an order of magnitude and
	//                writes a new row, so it deserves the tightest cap.
	//   - probeLim:  10 burst @ 1/2s sustained = 30/min. Used for the
	//                check-username preflight: the Signup form fires on
	//                each debounced keystroke and a normal user trying
	//                out a few candidate names should never trip it,
	//                while an enumeration script gets visibly throttled.
	authLim := NewLimiter(0.2, 5)
	signupLim := NewLimiter(1.0/30.0, 3)
	probeLim := NewLimiter(0.5, 10)
	stopJanitor := make(chan struct{})
	defer close(stopJanitor)
	go authLim.RunJanitor(stopJanitor, 15*time.Minute, 5*time.Minute)
	go signupLim.RunJanitor(stopJanitor, 15*time.Minute, 5*time.Minute)
	go probeLim.RunJanitor(stopJanitor, 15*time.Minute, 5*time.Minute)

	// Per-route body caps. Auth bodies are tiny JSON objects; PGN
	// upload (handled downstream) can be much larger and is capped at
	// the leaf endpoint, not here.
	const (
		authBodyMax    = 4 << 10  // 4 KB — usernames + passwords
		profileBodyMax = 16 << 10 // 16 KB — bio is capped at 500 chars
	)

	mux := http.NewServeMux()

	// 1. Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("Gateway OK"))
	})

	// 2. Auth + profile + user-search handled directly. Used to live in
	// the chess-user-service pod; absorbed here to eliminate one
	// cross-service network hop and one wire-protocol surface.
	// Auth surface — every entry that costs a bcrypt verify (login,
	// change-password) or admits a new row (signup) is rate-limited per
	// IP and body-capped. Without the limit, the cost-14 bcrypt is a
	// CPU-DoS vector; without the cap, a 1GB JSON POST can OOM the
	// gateway during decode. See cmd/gateway/middleware.go for the
	// fail-safe IP attribution behind Traefik.
	mux.HandleFunc("POST /api/auth/signup",
		maxBody(authBodyMax)(rateLimit(signupLim, 30)(gw.handleSignup)))
	mux.HandleFunc("POST /api/auth/login",
		maxBody(authBodyMax)(rateLimit(authLim, 5)(gw.handleLogin)))
	mux.HandleFunc("POST /api/auth/logout", gw.handleLogout)
	mux.HandleFunc("GET /api/user/me", gw.handleMe)
	mux.HandleFunc("GET /api/user/profile", gw.handleGetProfile)
	mux.HandleFunc("PUT /api/user/profile",
		maxBody(profileBodyMax)(gw.handleUpdateProfile))
	mux.HandleFunc("POST /api/user/password",
		maxBody(authBodyMax)(rateLimit(authLim, 5)(gw.handleChangePassword)))
	mux.HandleFunc("GET /api/user/stats", gw.handleUserStats)
	mux.HandleFunc("GET /api/users/search", gw.handleUserSearch)
	// Live preflight for the Signup form; debounced from the client
	// side. Returns {available, reason}; auth-free since it gates a
	// flow that's itself auth-free. Rate-limited on the same bucket as
	// signup so an attacker can't enumerate usernames any faster than
	// they could brute-force signup itself. See cmd/gateway/user_handlers.go.
	mux.HandleFunc("GET /api/auth/check-username",
		rateLimit(probeLim, 2)(gw.handleCheckUsername))

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
	// Reuse the same bounded Transport the explicit-call client uses
	// so the connection pool is shared. ErrorHandler turns upstream
	// timeouts into a clean 504 instead of the default behaviour
	// (which leaks the raw "context deadline exceeded" string).
	gameProxy.Transport = upstreamTransport
	gameProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Caller went away — no point answering. http.ErrAbortHandler
		// would also fit here, but a quiet return is enough.
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.Warn("gateway proxy: upstream error",
			"method", r.Method, "path", r.URL.Path, "error", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	mux.Handle("GET /api/games", gw.injectAuthedUser(gameProxy))

	// Per-game endpoints are RESTfully nested under /api/games/{id}/<verb>.
	// Gateway only authenticates + injects user_id; game-service holds the
	// per-game lock and owns the snapshot synthesis. See CLAUDE.md ("Streams
	// vs HTTP") for why these aren't Commands.
	//
	// /api/games/{id}/state is auth-optional so anonymous spectator links
	// work for public games. game-service's handleState uses the
	// spectator-aware userMayRead predicate — private games still 404 for
	// non-owners.
	mux.Handle("GET /api/games/{id}/state", gw.injectAuthedUserOptional(gameProxy))
	mux.Handle("GET /api/games/{id}/pgn", gw.injectAuthedUser(gameProxy))
	// Replay frames: same payload handleReplay fetches server-side for
	// the embedded /api/replay.html page; the SPA also hits this
	// directly for the move-list scrub + fork-from-ply flow. Mirrors
	// /state's auth-optional shape so spectators on public games can
	// scrub a finished game without signing in.
	mux.Handle("GET /api/games/{id}/replay", gw.injectAuthedUserOptional(gameProxy))
	mux.Handle("DELETE /api/games/{id}", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/visibility", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/move", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/resign", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/new", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/set_players", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/set_position", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/load_pgn", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/analyze", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/undo", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/hint", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/draw_offer", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/draw_accept", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/draw_decline", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/takeback_offer", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/takeback_accept", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/takeback_decline", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/rematch_offer", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/rematch_accept", gw.injectAuthedUser(gameProxy))
	mux.Handle("POST /api/games/{id}/rematch_decline", gw.injectAuthedUser(gameProxy))

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

	// Studies. Same shape as invites: per-user durable rows on
	// game-service. Prefix-mounted because the surface is a small CRUD
	// (POST/GET list/GET one/PATCH/DELETE); game-service's mux enforces
	// the method on each leaf. The trailing-slashless POST/GET list
	// also lives here — ServeMux treats "/api/studies" as a child of
	// the "/api/studies/" pattern when the literal path is non-empty.
	// Per-id GETs are auth-optional so anonymous callers can open a
	// shared public study (game-service's userMayReadStudy predicate
	// gates the actual read). Everything else — list, create, patch,
	// visibility toggle, delete — stays auth-required via the prefix
	// mount below; Go 1.22's enhanced mux uses longest-match so these
	// specific method+pattern entries win over the prefix.
	mux.Handle("GET /api/studies/{id}", gw.injectAuthedUserOptional(gameProxy))
	mux.Handle("GET /api/studies/{id}/positions", gw.injectAuthedUserOptional(gameProxy))
	mux.Handle("/api/studies", gw.injectAuthedUser(gameProxy))
	mux.Handle("/api/studies/", gw.injectAuthedUser(gameProxy))

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
// "may userID read gameID?". Hits /api/games/{id}/can_watch — a bare
// ownership check, no snapshot synthesis. The game-service handler is
// spectator-aware: signed-in participants pass on every game; anyone
// passes on public games. Pass userID=0 for anonymous viewers.
//
// Returns (may, isPlayer). isPlayer surfaces the participant-vs-spectator
// distinction via the X-Is-Player response header so the hub can keep
// players out of the viewer count without a second round-trip.
func (gw *Gateway) userMayWatchGame(ctx context.Context, userID int64, gameID string) (bool, bool) {
	u := *gw.gameSvcURL
	u.Path = "/api/games/" + gameID + "/can_watch"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if userID > 0 {
		req.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
	}
	resp, err := gw.upstream.Do(req)
	if err != nil {
		slog.Warn("ws authz preflight failed", "error", err)
		return false, false
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	return true, resp.Header.Get("X-Is-Player") == "1"
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
	// matching game-service endpoint. Durable IDs go to
	// /api/games/{id}/replay, temp IDs (always prefixed "temp-") go to
	// /api/temp/{id}/replay.
	dataURL := *gw.gameSvcURL
	if strings.HasPrefix(gameID, "temp-") {
		dataURL.Path = "/api/temp/" + gameID + "/replay"
	} else {
		dataURL.Path = "/api/games/" + gameID + "/replay"
	}
	// Forward the caller's identity so game-service can enforce the same
	// userMayRead check it does on /api/state. Without this, a private
	// game's replay was reachable to anyone who knew the UUID — the
	// spectator-mode flag (commit 2b6fb78) silently stopped applying
	// because the conditional auth in handleReplayData never fired.
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, dataURL.String(), nil)
	if user, ok := auth.GetUser(r.Context()); ok {
		req.Header.Set("X-User-ID", strconv.FormatInt(user.UserID, 10))
	}
	resp, err := gw.upstream.Do(req)
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
	// viteSingleFile rewrites `<link rel="icon" href="/favicon.svg">`
	// to a relative path during build; the relative path then 404s
	// because /api/replay.html resolves "./favicon.svg" against /api/.
	// Restore the absolute path here so the favicon loads from the
	// gateway's static root like every other public/ asset. Bounded
	// 1-occurrence Replace so a future template adding its own
	// "./favicon.svg" string won't double-rewrite.
	html = bytes.Replace(html, []byte(`href="./favicon.svg"`), []byte(`href="/favicon.svg"`), 1)
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
