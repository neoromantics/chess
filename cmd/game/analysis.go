package main

// Move assessment. The 0.x platform ran an analyze-as-you-play loop
// that fired an engine search per ply alongside the real move; that
// tripled engine load on every game even for users who never opened
// the assessment panel.
//
// This version is on-demand: the user clicks "Analyze" once the game
// is in a state worth reviewing (typically terminal). We replay the
// game from StartFEN ply by ply, dispatch a shallow engine search on
// each pre-move position, and stream the per-ply verdict back over
// the per-game WS channel. The classification is intentionally coarse
// — "best", "alt", "only" — until we add multipv-based centipawn
// loss in a follow-up. See ROADMAP.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
)

// assessMoveTime is the per-ply search budget. Short enough that a
// 60-ply game finishes analyzing in under a minute on a single
// engine-worker pod; the HPA scales out under burst.
const assessMoveTime = 200 * time.Millisecond

// PlyAssessment is the per-ply record streamed back over the per-game
// WS channel and (eventually) stored on rec.Assessments.
type PlyAssessment struct {
	Ply    int    `json:"ply"`    // 0-indexed position in the History array
	Played string `json:"played"` // UCI of the move actually played
	Best   string `json:"best"`   // UCI of the engine's recommended move
	Score  int    `json:"score"`  // engine score (centipawns, mover's POV)
	Depth  int    `json:"depth"`
	Class  string `json:"class"` // "best" | "alt" | "only"
}

func (s *GameService) handleHTTPAnalyze(w http.ResponseWriter, r *http.Request) {
	rec, _, ok := s.requireGameAccess(w, r)
	if !ok {
		return
	}
	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	if err := s.dispatchAssessmentJobs(r.Context(), rec.ID, rec.StartFEN, history, false); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "plies": len(history)})
}

// dispatchAssessmentJobs replays a game ply by ply and fires a shallow
// engine request per pre-move position. Caller supplies the history as
// a []string so this works for both the durable PG-backed record and
// the temp Redis-only record.
func (s *GameService) dispatchAssessmentJobs(ctx context.Context, gameID, startFEN string, history []string, isTemp bool) error {
	if len(history) == 0 {
		return fmt.Errorf("no moves to analyze")
	}
	if startFEN == "" {
		startFEN = core.StartFEN
	}
	if _, err := core.ParseFEN(startFEN); err != nil {
		return fmt.Errorf("bad start FEN: %w", err)
	}

	// Walk every move, snapshotting the pre-move FEN at each step. We
	// dispatch sequentially — engine-worker's HPA can take many
	// parallel requests, but firing 80 in one tight loop pegs Redis
	// briefly for no real win, and a slow-and-steady cadence makes
	// per-ply WS results arrive in order for the SPA.
	gm := game.NewGame()
	if err := gm.Load(startFEN, nil, false, false); err != nil {
		return fmt.Errorf("game load: %w", err)
	}

	for ply, uci := range history {
		preFEN := gm.Board.FEN()
		// Capture the played move's UCI for the result handler.
		md := map[string]string{
			"assess":  "1",
			"ply":     strconv.Itoa(ply),
			"played":  uci,
			"start":   startFEN,
			"pre_fen": preFEN,
		}
		if isTemp {
			md["temp"] = "1"
		}
		req := eventbus.EngineRequest{
			GameID:   gameID,
			FEN:      preFEN,
			History:  game.CopyHistory(gm.HistoryHash()),
			MoveTime: assessMoveTime,
			Context:  "assess",
			Metadata: md,
		}
		if _, err := s.bus.SendEngineRequest(ctx, req); err != nil {
			slog.Error("assess dispatch failed", "game_id", gameID, "ply", ply, "error", err)
			return fmt.Errorf("dispatch ply %d: %w", ply, err)
		}

		// Advance the board so the next pre-move FEN is correct.
		m, err := gm.Board.ParseUCIMove(uci)
		if err != nil {
			return fmt.Errorf("replay ply %d (%q): %w", ply, uci, err)
		}
		matched, mok := game.MatchMove(gm.Board.GenerateLegalMoves(), m)
		if !mok {
			return fmt.Errorf("replay ply %d: illegal move %q", ply, uci)
		}
		gm.PlayMove(matched)
	}
	return nil
}

// applyAssessmentResult handles one EngineResponse with Context=="assess".
// Classifies the played move against the engine's pick, then publishes
// a per-ply update on the per-game channel so the SPA can decorate
// the move list as results stream in.
func (s *GameService) applyAssessmentResult(ctx context.Context, resp eventbus.EngineResponse) {
	plyStr := resp.Metadata["ply"]
	played := resp.Metadata["played"]
	if plyStr == "" || played == "" {
		slog.Warn("assess result missing metadata", "game_id", resp.GameID, "metadata", resp.Metadata)
		return
	}
	ply, err := strconv.Atoi(plyStr)
	if err != nil {
		slog.Warn("assess result bad ply", "ply", plyStr, "error", err)
		return
	}

	class := "alt"
	if resp.BestMove == played {
		class = "best"
	}

	// Detect single-legal-move positions. Recompute by parsing the
	// pre-move FEN (we don't store it on the response). Cheap relative
	// to the search that just ran.
	preFEN := resp.Metadata["pre_fen"]
	if preFEN != "" {
		if b, err := core.ParseFEN(preFEN); err == nil {
			if len(b.GenerateLegalMoves()) == 1 {
				class = "only"
			}
		}
	}

	pa := PlyAssessment{
		Ply:    ply,
		Played: played,
		Best:   resp.BestMove,
		Score:  resp.Score,
		Depth:  resp.Depth,
		Class:  class,
	}
	payload, _ := json.Marshal(pa)
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtAssessment, GameID: resp.GameID, Payload: payload,
	})
}
