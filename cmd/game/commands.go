package main

// Command stream consumer. Run() loops on the game:commands stream
// and dispatches each Command to the matching handler:
//
//   CmdMakeMove        → handleMakeMove (move pipeline + lock + write)
//   CmdNewGame         → handleNewGame  (engine-side game creation)
//   CmdCreatePvPGame   → handleCreatePvPGame (PvP game creation)
//   CmdJoinQueue       → handleJoinQueue (matchmaker.go)
//   CmdLeaveQueue      → handleLeaveQueue (matchmaker.go)
//
// Pulled out of main.go to keep that file boot-only; emitGameFinished
// is here too because it's only used by these handlers.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
	"github.com/redis/go-redis/v9"
)

func (s *GameService) Run(ctx context.Context) {
	hostname, _ := os.Hostname()
	s.bus.Consume(ctx, eventbus.StreamGameCommands, "game-service-group", hostname, s.processCommand)
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
	case eventbus.CmdNewGame:
		s.handleNewGame(ctx, cmd)
	case eventbus.CmdCreatePvPGame:
		s.handleCreatePvPGame(ctx, cmd)
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

	// Drop paths must clear the thinking sentinel; the success path
	// either clears it (if no engine-to-move next) or transfers it to
	// the new search (triggerEngineForMove re-SETs it with a fresh
	// TTL after the successful move). A blanket defer-Del at the top
	// would race the new sentinel: triggerEngineForMove sets it just
	// before this function returns, and the defer would immediately
	// wipe it. So we Del explicitly at each abort path instead.
	clearThinking := func() {
		_ = s.bus.Rdb().Del(context.Background(), "game:thinking:"+cmd.GameID).Err()
	}

	rec, err := s.getGameCached(ctx, cmd.GameID)
	if err != nil {
		slog.Error("game not found", "game_id", cmd.GameID)
		clearThinking()
		return
	}

	// Drop late engine replies for games that ended in the meantime
	// (resign / timeout). Without this, an engine search dispatched a
	// second before the user resigned would land here, get applied to
	// the row, and unwind the resign — the SPA snapshot would flip back
	// to "ongoing" with the engine's reply on the board.
	if rec.Status != "" && rec.Status != "ongoing" {
		slog.Info("dropping move for finished game", "game_id", cmd.GameID, "status", rec.Status)
		clearThinking()
		return
	}

	if cmd.UserID != 0 {
		if (rec.WhiteUserID != nil && *rec.WhiteUserID == cmd.UserID) ||
			(rec.BlackUserID != nil && *rec.BlackUserID == cmd.UserID) {
			// Valid
		} else {
			slog.Warn("unauthorized move attempt", "game_id", cmd.GameID, "user_id", cmd.UserID)
			clearThinking()
			return
		}
	}

	gm := game.NewGame()
	var history []string
	json.Unmarshal([]byte(rec.History), &history)
	gm.Load(rec.StartFEN, history, rec.EngineWhite, rec.EngineBlack)

	// Engine-toggle late-reply guard. A search dispatched while a side
	// was configured as engine can land here AFTER the user flipped that
	// side back to human via /api/set_players. Without this check, the
	// engine's bestmove still gets applied — the user's piece moves on
	// its own and they're rightly confused. UserID==0 marks the system
	// (engine) dispatch path; for those, re-check that the side-to-move
	// is still engine in the *fresh* rec we just loaded inside the lock.
	if cmd.UserID == 0 {
		stm := gm.Board.SideToMove
		stillEngine := (stm == core.White && rec.EngineWhite) ||
			(stm == core.Black && rec.EngineBlack)
		if !stillEngine {
			slog.Info("dropping engine reply: side no longer configured as engine",
				"game_id", cmd.GameID, "side", stm)
			clearThinking()
			return
		}
	}

	m, err := gm.Board.ParseUCIMove(payload.Move)
	if err != nil {
		slog.Error("invalid move format", "move", payload.Move)
		clearThinking()
		return
	}

	matched, ok := game.MatchMove(gm.Board.GenerateLegalMoves(), m)
	if !ok {
		slog.Warn("illegal move", "game_id", cmd.GameID, "move", payload.Move)
		clearThinking()
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
		clearThinking()
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

	if rec.Status != "ongoing" {
		// Position-derived terminal (checkmate / stalemate / draw) OR
		// clock-flagged terminal handled above. Either way the row is
		// final; emit GameFinished and tear down the clock.
		deleteClock(ctx, s.bus.Rdb(), rec.ID)
		s.emitGameFinished(ctx, cmd.GameID, gm)
		clearThinking()
	} else if gm.EngineToMove() {
		// Chain into the next engine search. triggerEngineForMove
		// reads rec.{White,Black}ThinkTime so engine-vs-engine
		// respects per-side think-time settings, and SETs the
		// thinking sentinel so the SPA's spinner survives the
		// transition between consecutive engine moves.
		s.triggerEngineForMove(rec, gm)
	} else {
		// Non-engine to move next: clear the sentinel so the SPA's
		// spinner falls.
		clearThinking()
	}
}

func (s *GameService) emitGameFinished(ctx context.Context, gameID string, gm *game.Game) {
	s.bus.EmitEvent(ctx, eventbus.Event{
		Type:   eventbus.EvtGameFinished,
		GameID: gameID,
	})
}
