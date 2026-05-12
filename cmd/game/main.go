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

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
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

	// Health check and API server
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		})
		mux.HandleFunc("/api/state", s.handleState)
		mux.HandleFunc("/api/games", s.handleListGames)
		http.ListenAndServe(":8080", mux)
	}()

	slog.Info("Game Service starting (Command Processor)...")
	go s.listenToEngineResults(ctx)
	s.Run(ctx)
}

func (s *GameService) handleState(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}

	rec, err := s.db.GetGame(gameID)
	if err != nil {
		http.Error(w, "game not found", 404)
		return
	}

	writeJSON(w, rec)
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

func (s *GameService) listenToEngineResults(ctx context.Context) {
	pubsub := s.bus.Rdb().Subscribe(ctx, eventbus.ChannelEngineResults)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if msg == nil {
				return
			}
			var resp eventbus.EngineResponse
			if err := json.Unmarshal([]byte(msg.Payload), &resp); err != nil {
				continue
			}

			if resp.Context != "move" {
				continue
			}

			// Automated engine move command
			cmdPayload, _ := json.Marshal(eventbus.MakeMoveCmd{Move: resp.BestMove})
			cmd := eventbus.Command{
				Type:    eventbus.CmdMakeMove,
				GameID:  resp.GameID,
				UserID:  0, // System/Engine user
				Payload: cmdPayload,
			}
			s.bus.SendCommand(ctx, cmd)
		}
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
	case eventbus.CmdHint:
		s.handleHint(ctx, cmd)
	case eventbus.CmdAssess:
		s.handleAssess(ctx, cmd)
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

	white := cmd.UserID
	rec := &db.GameRecord{
		ID:             id,
		WhiteUserID:    &white,
		BlackUserID:    nil,
		FEN:            gm.Board.FEN(),
		History:        "[]",
		HistorySAN:     "[]",
		EngineWhite:    false,
		EngineBlack:    true,
		WhiteThinkTime: 1000,
		BlackThinkTime: 1000,
		TimeControl:    "engine",
		Rated:          false,
		Status:         "ongoing",
		Result:         "*",
		Assessments:    "[]",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := s.db.SaveGame(rec); err != nil {
		slog.Error("failed to create game", "error", err)
		return
	}

	slog.Info("new game created", "game_id", id, "user_id", cmd.UserID)

	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:   "GameStarted",
		GameID: id,
	})
}

func (s *GameService) handleHint(ctx context.Context, cmd eventbus.Command) {
	rec, err := s.db.GetGame(cmd.GameID)
	if err != nil {
		return
	}
	gm := game.NewGame()
	var history []string
	json.Unmarshal([]byte(rec.History), &history)
	gm.Load(rec.FEN, history, rec.EngineWhite, rec.EngineBlack)

	req := eventbus.EngineRequest{
		GameID:   cmd.GameID,
		FEN:      gm.Board.FEN(),
		History:  game.CopyHistory(gm.HistoryHash()),
		MoveTime: 1 * time.Second,
		Context:  "hint",
	}
	s.bus.SendEngineRequest(ctx, req)
}

func (s *GameService) handleAssess(ctx context.Context, cmd eventbus.Command) {
	// Not implemented
}

func (s *GameService) handleMakeMove(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.MakeMoveCmd
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return
	}

	rec, err := s.db.GetGame(cmd.GameID)
	if err != nil {
		slog.Error("game not found", "game_id", cmd.GameID)
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
	var history, historySAN []string
	json.Unmarshal([]byte(rec.History), &history)
	json.Unmarshal([]byte(rec.HistorySAN), &historySAN)
	gm.Load(rec.FEN, history, rec.EngineWhite, rec.EngineBlack)
	gm.HistorySAN = historySAN

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

	rec.FEN = gm.Board.FEN()
	hJSON, _ := json.Marshal(gm.History)
	rec.History = string(hJSON)
	hsJSON, _ := json.Marshal(gm.HistorySAN)
	rec.HistorySAN = string(hsJSON)
	rec.Status = string(gm.Status())
	rec.UpdatedAt = time.Now()

	if err := s.db.SaveGame(rec); err != nil {
		slog.Error("failed to save game", "error", err)
		return
	}

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

	if gm.Status() != game.StatusOngoing {
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
	rec, err := s.db.GetGame(cmd.GameID)
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
	s.db.SaveGame(rec)

	s.emitGameFinished(ctx, cmd.GameID, nil)
}

func (s *GameService) emitGameFinished(ctx context.Context, gameID string, gm *game.Game) {
	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:   eventbus.EvtGameFinished,
		GameID: gameID,
	})
}
