package main

// Move assessment. The 0.x platform ran an analyze-as-you-play loop
// that fired an engine search per ply alongside the real move; that
// tripled engine load on every game even for users who never opened
// the assessment panel.
//
// This version is on-demand: the user clicks "Analyze" once the game
// is in a state worth reviewing (typically terminal). We replay the
// game from StartFEN ply by ply and dispatch a shallow engine search
// on each pre-move position PLUS one extra at the position AFTER the
// last move (the "terminal anchor"). Per-ply verdicts are streamed
// back over the per-game WS channel as the results arrive.
//
// Why N+1 searches (not N): centipawn-loss classification needs both
// the best-from-pre score AND the played-move's resulting score. Each
// pre-move search returns the former; the latter is just `-score` of
// the search at the post-move position. So:
//
//   cp_loss(k) = best_score(k) + best_score(k+1)
//
// (both scored from each position's side-to-move POV; negamax sign-flip
// makes them additive.) That means search results for *consecutive*
// plies pair up. Position N (after the last move) has no ply to assess
// for itself, but is needed to give ply N-1 its cp_loss anchor.
//
// Multi-replica safety: results land on the engine-results consumer
// group, which round-robins across game-service pods. Two adjacent
// scores may land on two different pods, so the "do I have both
// neighbours yet?" check goes through a shared Redis hash, and a
// SETNX-emitted sentinel dedupes the per-ply Assessment event.
//
// Why CP-loss buckets here mirror lichess's: those thresholds are
// well-calibrated against millions of human games. Tightening past
// them gives more "blunder" calls than humans intuit; loosening
// hides genuine mistakes. The buckets aren't science but they're
// the best-known prior in chess UX.

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

// assessScoresKey holds per-position search results until the
// neighbour-pairing classifier consumes them. TTL covers analysis
// of a long (>200-ply) game plus headroom for slow engine bursts.
const assessScoresKey = "analyze:scores:"
const assessEmittedKey = "analyze:emitted:"
const assessKeyTTL = 30 * time.Minute

// cpLossCap clamps absurdly large losses (most commonly: best move was
// mate-in-N and played throws it away). Without a cap the SPA would
// have to render a "you lost 29894 cp" tooltip. The bucketing already
// pins anything >= cpLossBlunder as "blunder", so the exact value
// past the cap doesn't change the verdict.
const cpLossCap = 1500

// Classification thresholds in centipawns. Calibrated against
// lichess's published buckets (which were fit to millions of human
// games). Best is the "matched engine pick" case and short-circuits
// before any CP arithmetic.
const (
	cpLossGreat      = 30  // < 0.30 pawn loss
	cpLossGood       = 80  // < 0.80 pawn loss
	cpLossInaccuracy = 150 // < 1.50 pawn loss
	cpLossMistake    = 300 // < 3.00 pawn loss
	// blunder = cp_loss >= cpLossMistake
)

// PlyAssessment is the per-ply record streamed back over the per-game
// WS channel. Adding a field is wire-safe (the SPA's Assessment type
// keeps unknown fields); renaming is a break.
type PlyAssessment struct {
	Ply    int    `json:"ply"`    // 0-indexed position in the History array
	Played string `json:"played"` // UCI of the move actually played
	Best   string `json:"best"`   // UCI of the engine's recommended move
	Score  int    `json:"score"`  // engine score for the pre-move position (mover POV, cp)
	Depth  int    `json:"depth"`
	CPLoss int    `json:"cp_loss"` // 0 for best/only; capped at cpLossCap
	// Class is one of: "best", "only", "great", "good", "inaccuracy",
	// "mistake", "blunder". The SPA renders a marker + color per class
	// (SidePanel.vue moveMarker / .assess-* CSS).
	Class string `json:"class"`
}

// scoreEntry is what we stash per position in the shared scores hash.
// Keep it small — JSON-encoded, one per ply, lives for assessKeyTTL.
type scoreEntry struct {
	Best  string `json:"best"`
	Score int    `json:"score"`
	Depth int    `json:"depth"`
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

// dispatchAssessmentJobs replays a game ply by ply, firing a shallow
// engine request per pre-move position AND one extra at the position
// AFTER the last move (so ply N-1 has a neighbour to compute cp_loss
// against). Caller supplies the history as a []string so this works
// for both the durable PG-backed record and the temp Redis-only one.
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

	// Wipe any stale state from a prior /analyze on the same game.
	// Without this the second call would re-emit classifications for
	// plies that hadn't changed.
	rdb := s.bus.Rdb()
	_ = rdb.Del(ctx, assessScoresKey+gameID).Err()
	// Per-ply emitted sentinels we Del lazily — they have their own TTL
	// and the ply-suffix means a new analyze for the same game keeps
	// them disjoint as long as len(history) hasn't shrunk. For shrinking
	// histories (undo + re-analyze) we want a positive reset too:
	emittedPattern := assessEmittedKey + gameID + ":"
	if keys, err := rdb.Keys(ctx, emittedPattern+"*").Result(); err == nil && len(keys) > 0 {
		_ = rdb.Del(ctx, keys...).Err()
	}

	// Walk every move, snapshotting the pre-move FEN at each step,
	// then one final dispatch at the post-last-move position as the
	// terminal anchor. The anchor uses empty Metadata["played"] to
	// signal "don't emit a PlyAssessment for this — you're only here
	// so ply N-1 can compute cp_loss".
	gm := game.NewGame()
	if err := gm.Load(startFEN, nil, false, false); err != nil {
		return fmt.Errorf("game load: %w", err)
	}

	dispatchOne := func(ply int, played string) error {
		preFEN := gm.Board.FEN()
		md := map[string]string{
			"assess":  "1",
			"ply":     strconv.Itoa(ply),
			"played":  played, // "" on the terminal anchor
			"start":   startFEN,
			"pre_fen": preFEN,
			"total":   strconv.Itoa(len(history)),
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
			return fmt.Errorf("dispatch ply %d: %w", ply, err)
		}
		return nil
	}

	for ply, uci := range history {
		if err := dispatchOne(ply, uci); err != nil {
			slog.Error("assess dispatch failed", "game_id", gameID, "ply", ply, "error", err)
			return err
		}
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

	// Terminal anchor at position N. If the position is checkmate or
	// stalemate the engine will return its terminal-position score
	// (negamax: -MateScore+ply for mate, 0 for stalemate) — which is
	// exactly what we want for cp_loss arithmetic: a played move that
	// delivers mate gives the opponent a "very negative" search result,
	// which negates to "very positive" played-side score, so cp_loss
	// ≈ 0. A played move that walks into mate gives the inverse.
	if err := dispatchOne(len(history), ""); err != nil {
		slog.Error("assess terminal anchor dispatch failed", "game_id", gameID, "error", err)
		return err
	}
	return nil
}

// applyAssessmentResult handles one EngineResponse with Context=="assess".
// Stashes the score, then for each ply whose neighbour score is now
// known, classifies and emits a PlyAssessment.
func (s *GameService) applyAssessmentResult(ctx context.Context, resp eventbus.EngineResponse) {
	plyStr := resp.Metadata["ply"]
	totalStr := resp.Metadata["total"]
	if plyStr == "" || totalStr == "" {
		slog.Warn("assess result missing metadata", "game_id", resp.GameID, "metadata", resp.Metadata)
		return
	}
	ply, err := strconv.Atoi(plyStr)
	if err != nil {
		slog.Warn("assess result bad ply", "ply", plyStr, "error", err)
		return
	}
	total, err := strconv.Atoi(totalStr)
	if err != nil {
		slog.Warn("assess result bad total", "total", totalStr, "error", err)
		return
	}

	rdb := s.bus.Rdb()
	scoresKey := assessScoresKey + resp.GameID

	entry := scoreEntry{Best: resp.BestMove, Score: resp.Score, Depth: resp.Depth}
	encoded, _ := json.Marshal(entry)
	if err := rdb.HSet(ctx, scoresKey, plyStr, string(encoded)).Err(); err != nil {
		slog.Error("assess store score failed", "game_id", resp.GameID, "ply", ply, "error", err)
		return
	}
	_ = rdb.Expire(ctx, scoresKey, assessKeyTTL).Err()

	// Try to classify any ply whose neighbour is now known. The new
	// result completes at most two pairs: (ply-1, ply) and (ply, ply+1).
	if ply-1 >= 0 && ply-1 < total {
		s.maybeEmitAssessment(ctx, resp.GameID, ply-1, total)
	}
	if ply < total {
		s.maybeEmitAssessment(ctx, resp.GameID, ply, total)
	}
}

// maybeEmitAssessment checks whether scores for both ply k and ply k+1
// are present and, if so, classifies ply k and emits Assessment exactly
// once (SETNX dedupe protects against double-emit when two adjacent
// results land on two pods at near-identical times).
func (s *GameService) maybeEmitAssessment(ctx context.Context, gameID string, ply, total int) {
	if ply < 0 || ply >= total {
		return
	}
	rdb := s.bus.Rdb()
	scoresKey := assessScoresKey + gameID
	cur, err := rdb.HGet(ctx, scoresKey, strconv.Itoa(ply)).Result()
	if err != nil {
		return // not yet stored
	}
	next, err := rdb.HGet(ctx, scoresKey, strconv.Itoa(ply+1)).Result()
	if err != nil {
		return // anchor or neighbour not yet stored
	}
	var curEntry, nextEntry scoreEntry
	if err := json.Unmarshal([]byte(cur), &curEntry); err != nil {
		return
	}
	if err := json.Unmarshal([]byte(next), &nextEntry); err != nil {
		return
	}

	// Multi-replica dedupe: only the pod whose SETNX wins emits.
	emittedKey := assessEmittedKey + gameID + ":" + strconv.Itoa(ply)
	set, err := rdb.SetNX(ctx, emittedKey, "1", assessKeyTTL).Result()
	if err != nil || !set {
		return
	}

	// We need the played move + pre-move FEN to finish classification;
	// both came in on the ply-k result. The current/next-result fan-in
	// is best-effort, so re-derive what we can from what's persisted.
	// The cleanest source for "played" is the metadata of the original
	// ply-k result, which we don't have here — but the game's history
	// has the same UCI at index ply. Fetch via getGameCached. This
	// extra read is fine: classification fires ~N times per analyze.
	played, oneLegal := s.lookupPlayedAndOneLegal(ctx, gameID, ply)
	if played == "" {
		// Game gone, or temp game — caller-context-specific recovery
		// gets complex. Drop the emit; the SPA will leave that ply
		// undecorated, which beats emitting wrong data.
		slog.Warn("assess emit: no played move", "game_id", gameID, "ply", ply)
		_ = rdb.Del(ctx, emittedKey).Err()
		return
	}

	class, cpLoss := classifyPly(played, curEntry, nextEntry, oneLegal)
	pa := PlyAssessment{
		Ply:    ply,
		Played: played,
		Best:   curEntry.Best,
		Score:  curEntry.Score,
		Depth:  curEntry.Depth,
		CPLoss: cpLoss,
		Class:  class,
	}
	payload, _ := json.Marshal(pa)
	if _, err := s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtAssessment, GameID: gameID, Payload: payload,
	}); err != nil {
		slog.Error("assess emit failed", "game_id", gameID, "ply", ply, "error", err)
	}
}

// classifyPly computes the cp_loss verdict for one move given the
// search results at the pre-move and post-played-move positions.
// Pure function — exported (lowercase but available within package)
// for unit testing.
func classifyPly(played string, pre, post scoreEntry, oneLegal bool) (class string, cpLoss int) {
	if oneLegal {
		return "only", 0
	}
	if played == pre.Best {
		return "best", 0
	}
	// cp_loss in mover POV: pre.Score is mover's POV, post.Score is the
	// opponent's POV — additive thanks to negamax sign convention.
	cpLoss = pre.Score + post.Score
	if cpLoss < 0 {
		// Shallow-search noise: played sometimes "scores better" than
		// best by a few cp due to TT-cutoff timing. Floor at 0.
		cpLoss = 0
	}
	if cpLoss > cpLossCap {
		cpLoss = cpLossCap
	}
	switch {
	case cpLoss < cpLossGreat:
		return "great", cpLoss
	case cpLoss < cpLossGood:
		return "good", cpLoss
	case cpLoss < cpLossInaccuracy:
		return "inaccuracy", cpLoss
	case cpLoss < cpLossMistake:
		return "mistake", cpLoss
	default:
		return "blunder", cpLoss
	}
}

// lookupPlayedAndOneLegal fetches the played UCI at the given ply and
// whether the pre-move position had a single legal move. Works for
// both durable and temp games — temp games are stored in a separate
// Redis namespace and we fall back to it if PG lookup misses.
func (s *GameService) lookupPlayedAndOneLegal(ctx context.Context, gameID string, ply int) (played string, oneLegal bool) {
	history, startFEN, ok := s.loadHistoryAny(ctx, gameID)
	if !ok || ply < 0 || ply >= len(history) {
		return "", false
	}
	played = history[ply]
	if startFEN == "" {
		startFEN = core.StartFEN
	}
	gm := game.NewGame()
	if err := gm.Load(startFEN, history[:ply], false, false); err != nil {
		return played, false
	}
	if len(gm.Board.GenerateLegalMoves()) == 1 {
		oneLegal = true
	}
	return played, oneLegal
}

// loadHistoryAny tries the durable cache first, then the temp store.
// Returning (nil, "", false) means the game is gone or never existed.
// Durable rec.History is JSON-encoded; tempGameRec.History is already
// a parsed []string — handle both shapes.
func (s *GameService) loadHistoryAny(ctx context.Context, gameID string) ([]string, string, bool) {
	if rec, err := s.getGameCached(ctx, gameID); err == nil && rec != nil {
		var history []string
		_ = json.Unmarshal([]byte(rec.History), &history)
		return history, rec.StartFEN, true
	}
	if trec, err := newTempStore(s.bus.Rdb()).get(ctx, gameID); err == nil && trec != nil {
		return trec.History, trec.StartFEN, true
	}
	return nil, "", false
}
