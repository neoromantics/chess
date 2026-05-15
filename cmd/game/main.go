package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
	"github.com/neoromantics/chess/pkg/metrics"
	"github.com/redis/go-redis/v9"
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
	s := &GameService{db: store, bus: bus}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Health check and API server. All /api/invites/* routes assume the
	// gateway has already JWT-validated the caller and injected
	// ?user_id=N — no service downstream re-checks the token.
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		})
		mux.HandleFunc("/api/state", s.handleState)
		mux.HandleFunc("/api/games", s.handleListGames)
		mux.HandleFunc("/api/replay", s.handleReplayData)

		// Sync game-mutation HTTP. See cmd/game/handlers.go for the
		// shared lock + ownership pattern. These are the contract the
		// monolith exposed; the SPA was written for them.
		mux.HandleFunc("POST /api/move", s.handleHTTPMove)
		mux.HandleFunc("POST /api/resign", s.handleHTTPResign)
		mux.HandleFunc("POST /api/new", s.handleHTTPNew)
		mux.HandleFunc("POST /api/set_players", s.handleHTTPSetPlayers)
		mux.HandleFunc("POST /api/undo", s.handleHTTPUndo)
		mux.HandleFunc("DELETE /api/games/delete", s.handleHTTPDelete)
		mux.HandleFunc("POST /api/hint", s.handleHTTPHint)
		mux.HandleFunc("POST /api/draw_offer", s.handleDrawOffer)
		mux.HandleFunc("POST /api/draw_accept", s.handleDrawAccept)
		mux.HandleFunc("POST /api/draw_decline", s.handleDrawDecline)
		mux.HandleFunc("POST /api/takeback_offer", s.handleTakebackOffer)
		mux.HandleFunc("POST /api/takeback_accept", s.handleTakebackAccept)
		mux.HandleFunc("POST /api/takeback_decline", s.handleTakebackDecline)

		mux.HandleFunc("POST /api/invites/send", s.handleSendInvite)
		mux.HandleFunc("GET /api/invites/pending", s.handleListPendingInvites)
		mux.HandleFunc("POST /api/invites/{id}/accept", s.handleAcceptInvite)
		mux.HandleFunc("POST /api/invites/{id}/decline", s.handleDeclineInvite)
		mux.HandleFunc("POST /api/invites/{id}/cancel", s.handleCancelInvite)

		// Temp game (anonymous, Redis-only, 10-min sliding TTL). See cmd/game/temp.go.
		mux.HandleFunc("POST /api/temp/session", s.handleTempSession)
		mux.HandleFunc("GET /api/temp/state", s.handleTempState)
		mux.HandleFunc("POST /api/temp/move", s.handleTempMove)
		mux.HandleFunc("POST /api/temp/new", s.handleTempNew)
		mux.HandleFunc("POST /api/temp/resign", s.handleTempResign)
		mux.HandleFunc("POST /api/temp/undo", s.handleTempUndo)
		mux.HandleFunc("POST /api/temp/hint", s.handleTempHint)
		mux.HandleFunc("POST /api/temp/set_players", s.handleTempSetPlayers)

		mux.Handle("/metrics", metrics.Handler())

		http.ListenAndServe(":8080", metrics.HTTPMiddleware("game-service", mux))
	}()

	slog.Info("Game Service starting (Command Processor)...")
	go s.listenToEngineResults(ctx)
	go s.runInviteSweeper(ctx)
	// Matchmaker absorbed from the former cmd/matchmaker pod. The
	// pairing loop is Redis-leader-elected so multiple game-service
	// replicas don't race the queue. See cmd/game/matchmaker.go.
	go s.runPairingLoop(ctx)
	// Clock flag-fall sweeper. Per-game lock makes the work idempotent
	// across replicas — no leader election needed at this scale. See
	// cmd/game/clocks.go.
	go s.runClockFallSweeper(ctx)
	// Rating updater paused with the rest of the Elo surface in the
	// 2026-05-15 cleanup. Stream events still emit GameFinished; nothing
	// consumes them today. See ROADMAP.md "Restore deleted features".
	s.Run(ctx)
}

// stateJSON is the contract the SPA expects from /api/state. The DB
// row stores history / history_san / assessments as JSON-encoded
// strings (so a single Postgres column can hold a JSON array); the
// frontend expects parsed arrays plus derived fields (turn, legal
// moves, in_check, last_move). We synthesize the full snapshot here.
//
// Keep this in sync with frontend/src/types.ts:StateJSON. Renaming a
// field is a wire-protocol break; adding a field is safe.
type stateJSON struct {
	// ID is set on temp-game snapshots so the landing endpoint can
	// return the snapshot directly and the SPA learns its game ID
	// without a second request. Durable-game snapshots leave it
	// empty (the SPA already knows the ID — it's in the URL).
	ID             string    `json:"id,omitempty"`
	FEN            string    `json:"fen"`
	Turn           string    `json:"turn"`
	EngineWhite    bool      `json:"engine_white"`
	EngineBlack    bool      `json:"engine_black"`
	EngineToMove   bool      `json:"engine_to_move"`
	Status         string    `json:"status"`
	Result         string    `json:"result"`
	InCheck        bool      `json:"in_check"`
	LegalMoves     []string  `json:"legal_moves"`
	History        []string  `json:"history"`
	HistorySAN     []string  `json:"history_san"`
	LastMove       *moveJSON `json:"last_move"`
	Thinking       bool      `json:"thinking"`
	WhiteThinkTime int       `json:"white_think_time"`
	BlackThinkTime int       `json:"black_think_time"`

	// Player metadata so the SPA can decide which color the caller is,
	// flip the board for the black player, and show "you vs opponent".
	// nil pointers mean that side is an engine, not a human.
	WhiteUserID *int64 `json:"white_user_id"`
	BlackUserID *int64 `json:"black_user_id"`
	TimeControl string `json:"time_control"`
	Rated       bool   `json:"rated"`

	// Server-authoritative clock state. Engine-only / pre-clocks games
	// leave ClockInitial=0; the SPA hides the clock UI when initial is 0.
	// WhiteClockMS/BlackClockMS are reduced for the mover at snapshot
	// time — the SPA extrapolates further locally for smoothness.
	WhiteClockMS  int64  `json:"white_clock_ms"`
	BlackClockMS  int64  `json:"black_clock_ms"`
	ClockInitial  int64  `json:"clock_initial_ms"`
	ClockInc      int64  `json:"clock_inc_ms"`
	ClockMover    string `json:"clock_mover"`     // "w", "b", or "" if not running
	ClockServerMS int64  `json:"clock_server_ms"` // unix ms at snapshot time, for SPA extrapolation
}

type moveJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *GameService) handleState(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}
	rec, err := s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}
	// Per-game authorization. Without this any signed-in user could
	// read any game's state by guessing UUIDs. Don't leak existence
	// to non-participants — return 404, not 403.
	if uid, ok := authedUserID(r); ok {
		if !userOwnsGame(uid, rec) {
			http.Error(w, "game not found", 404)
			return
		}
	}
	writeJSON(w, s.snapshotFromRecord(r.Context(), rec))
}

// snapshotFromRecord builds the wire-protocol StateJSON the SPA
// expects from a persisted GameRecord. The DB row's history /
// history_san / assessments columns hold JSON-encoded strings (one
// PG column per JSON array); we parse them, then rehydrate the game
// via game.Load so we can compute derived fields (turn, legal moves,
// last move, check status).
//
// thinking flag is sourced from a Redis sentinel key set by the engine
// trigger path; absent → false.
func (s *GameService) snapshotFromRecord(ctx context.Context, rec *db.GameRecord) stateJSON {
	var history, historySAN []string
	if rec.History != "" {
		_ = json.Unmarshal([]byte(rec.History), &history)
	}
	if rec.HistorySAN != "" {
		_ = json.Unmarshal([]byte(rec.HistorySAN), &historySAN)
	}
	if history == nil {
		history = []string{}
	}
	if historySAN == nil {
		historySAN = []string{}
	}

	// Replay from the standard starting position, NOT rec.FEN. rec.FEN
	// is the *current* (post-move) position, and the moves in history
	// are illegal there (replay bombs out on move 1). Loading from "" /
	// StartFEN replays cleanly so gm.History, gm.HistorySAN, gm.UndoStack,
	// and gm.LastMove are all correctly populated. This is what makes
	// undo work, the move list survive across mutations, and the SAN
	// column stay in sync with history.
	gm := game.NewGame()
	gm.Load("", history, rec.EngineWhite, rec.EngineBlack)
	_ = historySAN // SAN is recomputed by gm.Load; the saved column is just a wire shortcut

	turn := "w"
	if gm.Board.SideToMove == core.Black {
		turn = "b"
	}
	legal := gm.Board.GenerateLegalMoves()
	legalStrs := make([]string, len(legal))
	for i, m := range legal {
		legalStrs[i] = m.String()
	}
	var last *moveJSON
	if gm.LastMove != nil {
		last = &moveJSON{
			From: core.SquareName(gm.LastMove.From),
			To:   core.SquareName(gm.LastMove.To),
		}
	}

	thinking := false
	if v, err := s.bus.Rdb().Get(ctx, "game:thinking:"+rec.ID).Result(); err == nil && v == "1" {
		thinking = true
	}

	// rec.Status wins over gm.Status() whenever it's terminal. resign /
	// timeout / future agreed-draw don't change the *position* (gm.Status
	// would still report "ongoing"), but the row carries the real outcome.
	// Without this, resign silently has no effect — the SPA gets back
	// status="ongoing" on the very next snapshot and the user keeps
	// playing.
	status := string(gm.Status())
	if rec.Status != "" && rec.Status != "ongoing" {
		status = rec.Status
	}

	snap := stateJSON{
		FEN:            gm.Board.FEN(),
		Turn:           turn,
		EngineWhite:    gm.EngineWhite,
		EngineBlack:    gm.EngineBlack,
		EngineToMove:   gm.EngineToMove(),
		Status:         status,
		Result:         rec.Result,
		InCheck:        gm.Board.InCheck(gm.Board.SideToMove),
		LegalMoves:     legalStrs,
		History:        gm.History,
		HistorySAN:     gm.HistorySAN,
		LastMove:       last,
		Thinking:       thinking,
		WhiteThinkTime: rec.WhiteThinkTime,
		BlackThinkTime: rec.BlackThinkTime,
		WhiteUserID:    rec.WhiteUserID,
		BlackUserID:    rec.BlackUserID,
		TimeControl:    rec.TimeControl,
		Rated:          rec.Rated,
	}

	// Clock projection: read the live hash, deduct mid-turn elapsed for
	// the mover, surface ClockServerMS so the SPA's local tick lines up.
	// Errors here are non-fatal — clock UI just stays hidden / stale.
	if c, err := loadClock(ctx, s.bus.Rdb(), rec.ID); err == nil && c != nil {
		w, b := c.currentTimes()
		snap.WhiteClockMS = w
		snap.BlackClockMS = b
		snap.ClockInitial = c.InitialMS
		snap.ClockInc = c.IncMS
		// Stop the clock visually once the game ends — flagGame zeros
		// Mover, but resign / checkmate finalize the row without
		// touching the clock hash. Rely on rec.Status to decide.
		if rec.Status != "" && rec.Status != "ongoing" {
			snap.ClockMover = ""
		} else {
			snap.ClockMover = c.Mover
		}
		snap.ClockServerMS = time.Now().UnixMilli()
	}
	return snap
}

// handleReplayData returns the per-ply ReplayFrame slice for a single
// game so the gateway's replay.html template substitution has something
// to embed. Pure JSON; gateway is responsible for wrapping it in HTML.
func (s *GameService) handleReplayData(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}
	rec, err := s.getGameCached(r.Context(), gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}
	if uid, ok := authedUserID(r); ok {
		if !userOwnsGame(uid, rec) {
			http.Error(w, "game not found", 404)
			return
		}
	}
	var history []string
	if rec.History != "" {
		_ = json.Unmarshal([]byte(rec.History), &history)
	}
	gm := game.NewGame()
	gm.Load("", history, rec.EngineWhite, rec.EngineBlack)
	writeJSON(w, gm.ReplayData())
}

func (s *GameService) handleListGames(w http.ResponseWriter, r *http.Request) {
	userIDStr := r.URL.Query().Get("user_id")
	if userIDStr == "" {
		http.Error(w, "missing user_id", 400)
		return
	}
	userID, _ := strconv.ParseInt(userIDStr, 10, 64)

	games, err := s.db.ListGames(userID)
	if err != nil {
		http.Error(w, "failed to list games", 500)
		return
	}

	writeJSON(w, games)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// listenToEngineResults consumes the engine:results stream and converts
// each engine search outcome into a MakeMove Command on game:commands.
// The stream + consumer group pattern (vs the old Pub/Sub) means a
// game-service restart no longer loses results — they sit in the
// stream's pending entries list until acked.
func (s *GameService) listenToEngineResults(ctx context.Context) {
	hostname, _ := os.Hostname()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			msgs, err := s.bus.ReadEngineResults(ctx, "game-engine-results-group", hostname, 5*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("engine-results read error", "error", err)
				}
				time.Sleep(1 * time.Second)
				continue
			}
			for _, msg := range msgs {
				s.processEngineResult(ctx, msg)
				s.bus.Ack(ctx, eventbus.StreamEngineResults, "game-engine-results-group", msg.ID)
			}
		}
	}
}

func (s *GameService) processEngineResult(ctx context.Context, msg redis.XMessage) {
	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}
	var resp eventbus.EngineResponse
	if err := json.Unmarshal([]byte(data), &resp); err != nil {
		slog.Error("engine-result unmarshal failed", "error", err)
		return
	}
	if resp.Context != "move" {
		// hint / assess results need different handling (broadcast
		// to the WS hub rather than dispatching a Command). Not yet
		// implemented; ack and drop so they don't pile up.
		return
	}

	// Temp games short-circuit the Command pipeline. They have no PG
	// row and a different store; applyTempEngineMove does its own
	// lock + save + publish.
	if resp.Metadata["temp"] == "1" {
		s.applyTempEngineMove(ctx, resp)
		return
	}

	// Translate the engine's chosen move into a MakeMove Command so
	// the same code path (with its per-game lock) applies it. Clearing
	// the thinking sentinel happens inside handleMakeMove after the
	// save succeeds, so the SPA's spinner falls only when the move is
	// actually on the board.
	cmdPayload, _ := json.Marshal(eventbus.MakeMoveCmd{Move: resp.BestMove})
	cmd := eventbus.Command{
		Type:    eventbus.CmdMakeMove,
		GameID:  resp.GameID,
		UserID:  0, // System/Engine user
		Payload: cmdPayload,
	}
	if _, err := s.bus.SendCommand(ctx, cmd); err != nil {
		slog.Error("dispatch engine MakeMove failed", "game_id", resp.GameID, "error", err)
	}
}

func (s *GameService) Run(ctx context.Context) {
	hostname, _ := os.Hostname()
	for {
		select {
		case <-ctx.Done():
			return
		default:
			msgs, err := s.bus.ReadCommands(ctx, "game-service-group", hostname, 5*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("read commands error", "error", err)
				}
				time.Sleep(1 * time.Second)
				continue
			}

			for _, msg := range msgs {
				s.processCommand(ctx, msg)
				s.bus.Ack(ctx, eventbus.StreamGameCommands, "game-service-group", msg.ID)
			}
		}
	}
}

func (s *GameService) processCommand(ctx context.Context, msg redis.XMessage) {
	var cmd eventbus.Command
	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}
	if err := json.Unmarshal([]byte(data), &cmd); err != nil {
		slog.Error("failed to unmarshal command", "error", err)
		return
	}

	slog.Info("processing command", "id", msg.ID, "type", cmd.Type, "game_id", cmd.GameID)

	switch cmd.Type {
	case eventbus.CmdMakeMove:
		s.handleMakeMove(ctx, cmd)
	case eventbus.CmdResign:
		s.handleResign(ctx, cmd)
	case eventbus.CmdNewGame:
		s.handleNewGame(ctx, cmd)
	case eventbus.CmdCreatePvPGame:
		s.handleCreatePvPGame(ctx, cmd)
	case eventbus.CmdHint:
		s.handleHint(ctx, cmd)
	case eventbus.CmdJoinQueue:
		s.handleJoinQueue(ctx, cmd)
	case eventbus.CmdLeaveQueue:
		s.handleLeaveQueue(ctx, cmd)
	default:
		slog.Warn("unknown command type", "type", cmd.Type)
	}
}

func (s *GameService) handleNewGame(ctx context.Context, cmd eventbus.Command) {
	gm := game.NewGame()
	id := cmd.GameID
	if id == "" {
		return
	}

	// Defaults if the gateway didn't pass a payload (e.g. an older
	// caller). Engine on black, 1s think time — the historical default.
	engineWhite := false
	engineBlack := true
	thinkMS := 1000
	if len(cmd.Payload) > 0 {
		var p eventbus.NewGameCmd
		if err := json.Unmarshal(cmd.Payload, &p); err == nil {
			engineWhite = p.EngineWhite
			engineBlack = p.EngineBlack
			if p.ThinkTimeMS > 0 {
				thinkMS = p.ThinkTimeMS
			}
		}
	}

	white := cmd.UserID
	rec := &db.GameRecord{
		ID:             id,
		WhiteUserID:    &white,
		BlackUserID:    nil,
		FEN:            gm.Board.FEN(),
		History:        "[]",
		HistorySAN:     "[]",
		EngineWhite:    engineWhite,
		EngineBlack:    engineBlack,
		WhiteThinkTime: thinkMS,
		BlackThinkTime: thinkMS,
		TimeControl:    "engine",
		Rated:          false,
		Status:         "ongoing",
		Result:         "*",
		Assessments:    "[]",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := s.saveGameCached(ctx, rec); err != nil {
		slog.Error("failed to create game", "error", err)
		return
	}

	slog.Info("new game created", "game_id", id, "user_id", cmd.UserID)

	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:   "GameStarted",
		GameID: id,
	})
}

func (s *GameService) handleCreatePvPGame(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.CreatePvPGameCmd
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return
	}

	gm := game.NewGame()
	id := cmd.GameID

	white := payload.WhiteUserID
	black := payload.BlackUserID

	rec := &db.GameRecord{
		ID:             id,
		WhiteUserID:    &white,
		BlackUserID:    &black,
		FEN:            gm.Board.FEN(),
		History:        "[]",
		HistorySAN:     "[]",
		EngineWhite:    false,
		EngineBlack:    false,
		WhiteThinkTime: 1000,
		BlackThinkTime: 1000,
		TimeControl:    payload.TimeControl,
		Rated:          payload.Rated,
		Status:         "ongoing",
		Result:         "*",
		Assessments:    "[]",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := s.saveGameCached(ctx, rec); err != nil {
		slog.Error("failed to create pvp game", "error", err)
		return
	}
	if err := initClock(ctx, s.bus.Rdb(), rec); err != nil {
		slog.Error("clock init failed", "game_id", id, "error", err)
	}

	slog.Info("new pvp game created", "game_id", id, "white", white, "black", black)

	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:   "GameStarted",
		GameID: id,
	})
}

func (s *GameService) handleHint(ctx context.Context, cmd eventbus.Command) {
	rec, err := s.getGameCached(ctx, cmd.GameID)
	if err != nil {
		return
	}
	gm := game.NewGame()
	var history []string
	json.Unmarshal([]byte(rec.History), &history)
	gm.Load("", history, rec.EngineWhite, rec.EngineBlack)

	req := eventbus.EngineRequest{
		GameID:   cmd.GameID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: 1 * time.Second,
		Context:  "hint",
	}
	s.bus.SendEngineRequest(ctx, req)
}

func (s *GameService) handleMakeMove(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.MakeMoveCmd
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return
	}

	// Serialize per-game state mutations across all replicas. Without
	// this two pods consuming the same stream can race a single game.
	// 10s TTL covers the slowest expected mutation (engine search +
	// PG roundtrip); release happens on every exit path.
	lock, lockErr := acquireGameLock(ctx, s.bus.Rdb(), cmd.GameID, 10*time.Second)
	if lockErr != nil {
		slog.Error("lock acquire failed", "game_id", cmd.GameID, "error", lockErr)
		return
	}
	if lock == nil {
		// Another writer has it. Re-enqueue would risk reorderings; for
		// the consumer path we just drop and let the stream ack — the
		// other holder is processing this very command.
		slog.Warn("makemove: lock held by another writer", "game_id", cmd.GameID)
		return
	}
	defer lock.release(context.Background())

	rec, err := s.getGameCached(ctx, cmd.GameID)
	if err != nil {
		slog.Error("game not found", "game_id", cmd.GameID)
		return
	}

	// Drop late engine replies for games that ended in the meantime
	// (resign / timeout). Without this, an engine search dispatched a
	// second before the user resigned would land here, get applied to
	// the row, and unwind the resign — the SPA snapshot would flip back
	// to "ongoing" with the engine's reply on the board.
	if rec.Status != "" && rec.Status != "ongoing" {
		slog.Info("dropping move for finished game", "game_id", cmd.GameID, "status", rec.Status)
		return
	}

	if cmd.UserID != 0 {
		if (rec.WhiteUserID != nil && *rec.WhiteUserID == cmd.UserID) ||
			(rec.BlackUserID != nil && *rec.BlackUserID == cmd.UserID) {
			// Valid
		} else {
			slog.Warn("unauthorized move attempt", "game_id", cmd.GameID, "user_id", cmd.UserID)
			return
		}
	}

	gm := game.NewGame()
	var history []string
	json.Unmarshal([]byte(rec.History), &history)
	gm.Load("", history, rec.EngineWhite, rec.EngineBlack)

	m, err := gm.Board.ParseUCIMove(payload.Move)
	if err != nil {
		slog.Error("invalid move format", "move", payload.Move)
		return
	}

	matched, ok := game.MatchMove(gm.Board.GenerateLegalMoves(), m)
	if !ok {
		slog.Warn("illegal move", "game_id", cmd.GameID, "move", payload.Move)
		return
	}

	gm.PlayMove(matched)

	// Server-authoritative clock update. PvP only — engine games carry
	// no clock hash and loadClock returns nil silently.
	if c, _ := loadClock(ctx, s.bus.Rdb(), rec.ID); c != nil {
		flagged, loser := c.applyMove(time.Now().UnixMilli())
		if flagged {
			// The mover ran out exactly as their move landed. Treat as
			// a timeout (the move is moot — clock fell first). The
			// sweeper would catch this milliseconds later anyway; doing
			// it here avoids the brief "looks fine" snapshot in between.
			rec.Status = "timeout"
			if loser == "w" {
				rec.Result = "0-1"
			} else {
				rec.Result = "1-0"
			}
			_ = c.save(ctx, s.bus.Rdb())
			deleteClock(ctx, s.bus.Rdb(), rec.ID)
		} else {
			_ = c.save(ctx, s.bus.Rdb())
			c.scheduleFlag(ctx, s.bus.Rdb())
		}
	}

	rec.FEN = gm.Board.FEN()
	hJSON, _ := json.Marshal(gm.History)
	rec.History = string(hJSON)
	hsJSON, _ := json.Marshal(gm.HistorySAN)
	rec.HistorySAN = string(hsJSON)
	if rec.Status == "" || rec.Status == "ongoing" {
		// Don't overwrite a clock-flagged "timeout" status with whatever
		// gm reports (still ongoing because the position itself is
		// playable).
		rec.Status = string(gm.Status())
	}
	rec.UpdatedAt = time.Now()

	if err := s.saveGameCached(ctx, rec); err != nil {
		slog.Error("failed to save game", "error", err)
		return
	}
	// Engine response landed and the move is on the board — drop the
	// thinking sentinel so the SPA's spinner clears immediately rather
	// than waiting for the TTL.
	_ = s.bus.Rdb().Del(ctx, "game:thinking:"+rec.ID).Err()

	evtPayload := eventbus.MovePlayedEvt{
		Move:       matched.String(),
		SAN:        gm.HistorySAN[len(gm.HistorySAN)-1],
		FEN:        rec.FEN,
		History:    gm.History,
		HistorySAN: gm.HistorySAN,
		WhiteTime:  gm.WhiteTime,
		BlackTime:  gm.BlackTime,
	}
	pJSON, _ := json.Marshal(evtPayload)
	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:    eventbus.EvtMovePlayed,
		GameID:  cmd.GameID,
		Payload: pJSON,
	})

	if rec.Status != "ongoing" {
		// Position-derived terminal (checkmate / stalemate / draw) OR
		// clock-flagged terminal handled above. Either way the row is
		// final; emit GameFinished and tear down the clock.
		deleteClock(ctx, s.bus.Rdb(), rec.ID)
		s.emitGameFinished(ctx, cmd.GameID, gm)
	} else if gm.EngineToMove() {
		s.triggerEngine(ctx, cmd.GameID, gm)
	}
}

func (s *GameService) triggerEngine(ctx context.Context, gameID string, gm *game.Game) {
	req := eventbus.EngineRequest{
		GameID:   gameID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: 1 * time.Second,
		Context:  "move",
	}
	s.bus.SendEngineRequest(ctx, req)
	slog.Info("engine request dispatched", "game_id", gameID)
}

func (s *GameService) handleResign(ctx context.Context, cmd eventbus.Command) {
	rec, err := s.getGameCached(ctx, cmd.GameID)
	if err != nil {
		return
	}

	res := "*"
	if rec.WhiteUserID != nil && *rec.WhiteUserID == cmd.UserID {
		res = "0-1"
	} else if rec.BlackUserID != nil && *rec.BlackUserID == cmd.UserID {
		res = "1-0"
	} else {
		return
	}

	rec.Status = "resign"
	rec.Result = res
	rec.UpdatedAt = time.Now()
	_ = s.saveGameCached(ctx, rec)

	s.emitGameFinished(ctx, cmd.GameID, nil)
}

func (s *GameService) emitGameFinished(ctx context.Context, gameID string, gm *game.Game) {
	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:   eventbus.EvtGameFinished,
		GameID: gameID,
	})
}
