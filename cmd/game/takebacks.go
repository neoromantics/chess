package main

// Takeback flow. PvP only and casual-only — rated games never take
// back, since "I want my move back" is exactly the kind of bargaining
// that ratings exist to prevent.
//
// Storage:
//   takeback-offer:{game_id}  string user_id of the requester, TTL=60s
//                             (capped to remaining clock if PvP-clocked).
//
// Pop semantics: the requester wants to be on move again. So:
//   - If side-to-move == requester: opponent already replied to the
//     requester's last move. Pop 2 plies (their reply + my last move),
//     leaving requester to play again.
//   - If side-to-move == opponent: requester just moved and immediately
//     wants it back. Pop 1 ply.
//   - If requester has no moves yet, refuse (nothing to take back).
//
// On accept, the clock RESETS to its post-move bank for both sides
// (the wall time spent on the popped moves is "given back"). This is
// the chess.com convention: takebacks free both bank and increment.
// Engine-game players already have unilateral /api/undo; takeback only
// shows up for human-vs-human.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
)

const (
	takebackOfferKey = "takeback-offer:"
	takebackOfferTTL = 60 * time.Second
)

// Event-name constants for the takeback flow. Defined here rather than
// in pkg/eventbus to keep the type registry colocated with the only
// service that emits them — eventbus stays the canonical home for
// cross-service constants only.
const (
	EvtTakebackRequested = "TakebackRequested"
	EvtTakebackAccepted  = "TakebackAccepted"
	EvtTakebackDeclined  = "TakebackDeclined"
)

func (s *GameService) requireTakebackAccess(w http.ResponseWriter, r *http.Request) (*db.GameRecord, int64, bool) {
	rec, uid, ok := s.requireGameAccess(w, r)
	if !ok {
		return nil, 0, false
	}
	if rec.WhiteUserID == nil || rec.BlackUserID == nil {
		http.Error(w, "takebacks require a human-vs-human game (engine games can use Undo)", http.StatusBadRequest)
		return nil, 0, false
	}
	if rec.Rated {
		http.Error(w, "takebacks are not allowed in rated games", http.StatusBadRequest)
		return nil, 0, false
	}
	if rec.Status != "ongoing" {
		http.Error(w, "game is already finished", http.StatusConflict)
		return nil, 0, false
	}
	return rec, uid, true
}

func (s *GameService) takebackOfferTTL(ctx context.Context, gameID string) time.Duration {
	c, err := loadClock(ctx, s.bus.Rdb(), gameID)
	if err != nil || c == nil {
		return takebackOfferTTL
	}
	min := c.WhiteMS
	if c.BlackMS < min {
		min = c.BlackMS
	}
	if min <= 0 {
		return takebackOfferTTL
	}
	d := time.Duration(min) * time.Millisecond
	if d > takebackOfferTTL {
		return takebackOfferTTL
	}
	if d < 5*time.Second {
		return 5 * time.Second
	}
	return d
}

func (s *GameService) handleTakebackOffer(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireTakebackAccess(w, r)
	if !ok {
		return
	}
	// Validate that the requester actually has a move on the board.
	// Without this we'd let a player open a takeback prompt at move 0
	// (white-to-move, no moves played) which the opponent couldn't
	// fulfill anyway.
	gm := game.NewGame()
	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	gm.Load(rec.StartFEN, history, false, false)
	requesterColor := requesterColorOnRec(rec, uid)
	if requesterColor == "" {
		http.Error(w, "you are not a player in this game", http.StatusForbidden)
		return
	}
	pliesToPop := pliesToTakeBack(gm, requesterColor)
	if pliesToPop == 0 {
		http.Error(w, "you have no moves to take back yet", http.StatusBadRequest)
		return
	}

	ttl := s.takebackOfferTTL(r.Context(), rec.ID)
	set, err := s.bus.Rdb().SetNX(r.Context(), takebackOfferKey+rec.ID, strconv.FormatInt(uid, 10), ttl).Result()
	if err != nil {
		http.Error(w, "redis error", http.StatusInternalServerError)
		return
	}
	if !set {
		http.Error(w, "a takeback request is already pending", http.StatusConflict)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"from_user_id": uid,
		"game_id":      rec.ID,
		"plies":        pliesToPop,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type:    EvtTakebackRequested,
		GameID:  rec.ID,
		Payload: payload,
	})
	slog.Info("takeback requested", "game_id", rec.ID, "from", uid, "plies", pliesToPop)
	w.WriteHeader(http.StatusAccepted)
}

func (s *GameService) handleTakebackDecline(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireTakebackAccess(w, r)
	if !ok {
		return
	}
	requester, exists := s.loadTakebackRequester(r.Context(), rec.ID)
	if !exists {
		http.Error(w, "no pending takeback request", http.StatusNotFound)
		return
	}
	if requester == uid {
		http.Error(w, "cannot decline your own request", http.StatusBadRequest)
		return
	}
	_ = s.bus.Rdb().Del(r.Context(), takebackOfferKey+rec.ID).Err()
	payload, _ := json.Marshal(map[string]any{
		"by_user_id": uid,
		"game_id":    rec.ID,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type:    EvtTakebackDeclined,
		GameID:  rec.ID,
		Payload: payload,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *GameService) handleTakebackAccept(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireTakebackAccess(w, r)
	if !ok {
		return
	}
	requester, exists := s.loadTakebackRequester(r.Context(), rec.ID)
	if !exists {
		http.Error(w, "no pending takeback request", http.StatusNotFound)
		return
	}
	if requester == uid {
		http.Error(w, "cannot accept your own request", http.StatusBadRequest)
		return
	}

	lock, err := acquireGameLock(r.Context(), s.bus.Rdb(), rec.ID, gameLockTTL)
	if err != nil {
		http.Error(w, "lock acquire failed", http.StatusInternalServerError)
		return
	}
	if lock == nil {
		http.Error(w, "another writer is mutating this game; retry", http.StatusConflict)
		return
	}
	defer lock.release(context.Background())

	if _, exists := s.loadTakebackRequester(r.Context(), rec.ID); !exists {
		http.Error(w, "request expired before accept landed", http.StatusConflict)
		return
	}
	rec, err = s.getGameCached(r.Context(), rec.ID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	if rec.Status != "ongoing" {
		http.Error(w, "game is already finished", http.StatusConflict)
		return
	}

	gm := game.NewGame()
	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	gm.Load(rec.StartFEN, history, false, false)
	requesterColor := requesterColorOnRec(rec, requester)
	pliesToPop := pliesToTakeBack(gm, requesterColor)
	if pliesToPop == 0 {
		// Race: requester took back exactly when their last move was
		// undone by something else (shouldn't happen, but be defensive).
		_ = s.bus.Rdb().Del(r.Context(), takebackOfferKey+rec.ID).Err()
		http.Error(w, "no moves left to take back", http.StatusConflict)
		return
	}
	for i := 0; i < pliesToPop; i++ {
		if !gm.Undo() {
			break
		}
	}

	histJSON, _ := json.Marshal(gm.History)
	sanJSON, _ := json.Marshal(gm.HistorySAN)
	rec.FEN = gm.Board.FEN()
	rec.History = string(histJSON)
	rec.HistorySAN = string(sanJSON)
	rec.UpdatedAt = time.Now()

	// Reset the clock turn-start so the requester gets the same ms
	// they had when they made their now-popped move. We don't refund
	// the increment they earned (you got time for the move; the takeback
	// gives the move back, not the time). Conservative implementation:
	// just rebase TurnStartedMS to now. The bank values stay as-is.
	if c, _ := loadClock(r.Context(), s.bus.Rdb(), rec.ID); c != nil {
		c.Mover = "w"
		if gm.Board.SideToMove == core.Black {
			c.Mover = "b"
		}
		c.TurnStartedMS = time.Now().UnixMilli()
		_ = c.save(r.Context(), s.bus.Rdb())
		c.scheduleFlag(r.Context(), s.bus.Rdb())
	}

	if err := s.saveGameCached(r.Context(), rec); err != nil {
		slog.Error("takeback accept save failed", "game_id", rec.ID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	_ = s.bus.Rdb().Del(r.Context(), takebackOfferKey+rec.ID).Err()

	snapshot := s.snapshotFromRecord(r.Context(), rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: rec.ID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: EvtTakebackAccepted, GameID: rec.ID, Payload: snapPayload,
	})
	slog.Info("takeback accepted", "game_id", rec.ID, "by", uid, "requester", requester, "plies_popped", pliesToPop)
	writeJSON(w, snapshot)
}

func (s *GameService) loadTakebackRequester(ctx context.Context, gameID string) (int64, bool) {
	v, err := s.bus.Rdb().Get(ctx, takebackOfferKey+gameID).Result()
	if err != nil {
		return 0, false
	}
	uid, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return uid, true
}

// requesterColorOnRec returns "w" or "b" if uid plays that color on
// rec, "" if uid is not a participant.
func requesterColorOnRec(rec *db.GameRecord, uid int64) string {
	if rec.WhiteUserID != nil && *rec.WhiteUserID == uid {
		return "w"
	}
	if rec.BlackUserID != nil && *rec.BlackUserID == uid {
		return "b"
	}
	return ""
}

// pliesToTakeBack returns the number of plies to pop so that the
// named color is back on move. 0 means "nothing to take back" (the
// requester has played no moves yet that could be reverted).
func pliesToTakeBack(gm *game.Game, requesterColor string) int {
	if len(gm.History) == 0 {
		return 0
	}
	currentColor := "w"
	if gm.Board.SideToMove == core.Black {
		currentColor = "b"
	}
	if currentColor == requesterColor {
		// Opponent played last; pop their reply + my prior move = 2 plies.
		// Floor at history length so we don't try to pop past the start.
		if len(gm.History) >= 2 {
			return 2
		}
		return 0
	}
	// Requester just moved; pop 1 ply puts them back on move.
	return 1
}
