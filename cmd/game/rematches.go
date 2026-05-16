package main

// Rematch flow. PvP only — engine-only games already reset in place
// via /api/new since there's no opponent to consult. The server holds
// a single ephemeral key per finished game:
//   rematch-offer:{game_id}  string user_id of the offerer, TTL = 5 min
//
// Lifecycle:
//   - POST /api/rematch_offer   → SETNX the key on a finished PvP row.
//                                 409 if an offer is already pending.
//                                 Broadcasts RematchOffered{from_user_id}
//                                 on the per-game channel so the opposite
//                                 SPA shows Accept/Decline.
//   - POST /api/rematch_decline → opposite participant. DELs the key and
//                                 emits RematchDeclined{by_user_id}.
//   - POST /api/rematch_accept  → opposite participant. Creates a NEW
//                                 game row (same time-control + rated
//                                 flag, *swapped colors* per chess
//                                 convention), wires its clock, then
//                                 emits RematchAccepted{new_game_id, …}
//                                 on the OLD game's channel. GameView
//                                 navigates participants to the new
//                                 room and toasts for any spectators.
//
// Why a new row (not a /api/new reset on the same row): the finished
// game has to stay readable from /match's "Past games" list and from
// any replay link the players already shared. Stamping a fresh game
// over the same UUID would erase that history.
//
// Bot games: isBotMatch rows look like PvP to the SPA, so the same
// "Rematch" button fires /api/rematch_offer. The maybeScheduleBot…
// helper at the bottom of this file mirrors draws.go and auto-
// accepts/declines after the bot reaction delay so the offer doesn't
// sit unresolved.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
)

const (
	rematchOfferKey = "rematch-offer:"
	// 5 min — players take longer to decide on a rematch than on a
	// draw mid-clock (no time pressure post-finish), but stale offers
	// shouldn't outlive the visit. If both have closed the tab, the
	// next opener gets a clean slate.
	rematchOfferTTL = 5 * time.Minute
)

// requireRematchAccess validates that the caller participates in a
// FINISHED PvP-shaped game. Engine-only rows (one user_id nil) are
// rejected — for those, /api/new is the correct surface.
func (s *GameService) requireRematchAccess(w http.ResponseWriter, r *http.Request) (*db.GameRecord, int64, bool) {
	rec, uid, ok := s.requireGameAccess(w, r)
	if !ok {
		return nil, 0, false
	}
	if rec.WhiteUserID == nil || rec.BlackUserID == nil {
		http.Error(w, "rematches require a human-vs-human game", http.StatusBadRequest)
		return nil, 0, false
	}
	if rec.Status == "ongoing" {
		http.Error(w, "game is still in progress", http.StatusConflict)
		return nil, 0, false
	}
	return rec, uid, true
}

func (s *GameService) handleRematchOffer(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireRematchAccess(w, r)
	if !ok {
		return
	}
	set, err := s.bus.Rdb().SetNX(r.Context(), rematchOfferKey+rec.ID, strconv.FormatInt(uid, 10), rematchOfferTTL).Result()
	if err != nil {
		http.Error(w, "redis error", http.StatusInternalServerError)
		return
	}
	if !set {
		http.Error(w, "a rematch offer is already pending", http.StatusConflict)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"from_user_id": uid,
		"game_id":      rec.ID,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtRematchOffered, GameID: rec.ID, Payload: payload,
	})
	slog.Info("rematch offered", "game_id", rec.ID, "from", uid)
	// Bot-fallback: same delayed accept/decline pattern as draws/takebacks.
	// TODO(matchmaker-engine-fallback): remove with the bot pool.
	s.maybeScheduleBotRematchResponse(rec, uid)
	w.WriteHeader(http.StatusAccepted)
}

func (s *GameService) handleRematchDecline(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireRematchAccess(w, r)
	if !ok {
		return
	}
	offerer, exists := s.loadRematchOfferer(r.Context(), rec.ID)
	if !exists {
		http.Error(w, "no pending rematch offer", http.StatusNotFound)
		return
	}
	if offerer == uid {
		http.Error(w, "cannot decline your own offer (let it expire instead)", http.StatusBadRequest)
		return
	}
	_ = s.bus.Rdb().Del(r.Context(), rematchOfferKey+rec.ID).Err()
	payload, _ := json.Marshal(map[string]any{
		"by_user_id": uid,
		"game_id":    rec.ID,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtRematchDeclined, GameID: rec.ID, Payload: payload,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *GameService) handleRematchAccept(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireRematchAccess(w, r)
	if !ok {
		return
	}
	offerer, exists := s.loadRematchOfferer(r.Context(), rec.ID)
	if !exists {
		http.Error(w, "no pending rematch offer", http.StatusNotFound)
		return
	}
	if offerer == uid {
		http.Error(w, "cannot accept your own offer", http.StatusBadRequest)
		return
	}

	newRec, err := s.createRematchRow(r.Context(), rec)
	if err != nil {
		slog.Error("rematch create failed", "game_id", rec.ID, "error", err)
		http.Error(w, "rematch failed", http.StatusInternalServerError)
		return
	}
	_ = s.bus.Rdb().Del(r.Context(), rematchOfferKey+rec.ID).Err()

	// Announce on the OLD game's channel: GameView already subscribes
	// there and can route participants while leaving spectators with a
	// passive toast. Carrying both user_ids in the payload lets the SPA
	// gate the redirect on "am I one of them?" without a second fetch.
	acceptPayload, _ := json.Marshal(map[string]any{
		"new_game_id":   newRec.ID,
		"white_user_id": *newRec.WhiteUserID,
		"black_user_id": *newRec.BlackUserID,
		"by_user_id":    uid,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtRematchAccepted, GameID: rec.ID, Payload: acceptPayload,
	})

	// And announce on the NEW game's channel so anyone who navigates
	// before the snapshot fetch returns sees the StartedEvt the
	// matchmaker would otherwise produce.
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtGameStarted, GameID: newRec.ID,
	})

	slog.Info("rematch accepted",
		"old_game_id", rec.ID, "new_game_id", newRec.ID,
		"by", uid, "offerer", offerer)

	// Return the new game's id so the accepter can router.push without
	// waiting on the RematchAccepted WS event (which arrives moments
	// later for both sides). snapshotFromRecord doesn't fill ID for
	// durable rows, so returning a small wrapper object keeps the wire
	// honest without retrofitting the stateJSON shape.
	writeJSON(w, map[string]any{"game_id": newRec.ID})
}

// createRematchRow forges a fresh GameRecord for the rematch. Colors
// swap (chess convention so each side has a turn with white), but
// engine-flag assignments swap with them so a masked bot game keeps
// the human-vs-engine wiring intact on the opposite side.
func (s *GameService) createRematchRow(ctx context.Context, prev *db.GameRecord) (*db.GameRecord, error) {
	newID := uuid.New().String()
	gm := game.NewGame()

	// Swap player IDs: whoever was white last game is black this game.
	newWhite := *prev.BlackUserID
	newBlack := *prev.WhiteUserID
	// Swap engine flags alongside the IDs so a bot match stays a bot
	// match on the opposite side. For real PvP both flags are false
	// and the swap is a no-op.
	newEngineWhite := prev.EngineBlack
	newEngineBlack := prev.EngineWhite

	newRec := &db.GameRecord{
		ID:             newID,
		WhiteUserID:    &newWhite,
		BlackUserID:    &newBlack,
		FEN:            gm.Board.FEN(),
		History:        "[]",
		HistorySAN:     "[]",
		EngineWhite:    newEngineWhite,
		EngineBlack:    newEngineBlack,
		WhiteThinkTime: prev.WhiteThinkTime,
		BlackThinkTime: prev.BlackThinkTime,
		TimeControl:    prev.TimeControl,
		Rated:          prev.Rated,
		Status:         "ongoing",
		Result:         "*",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := s.saveGameCached(ctx, newRec); err != nil {
		return nil, err
	}
	if err := initClock(ctx, s.bus.Rdb(), newRec); err != nil {
		slog.Error("rematch clock init failed", "game_id", newID, "error", err)
	}
	// If a bot drew white in the rematch, kick off the engine search now
	// so the user opens to a "bot is thinking" board instead of waiting
	// on the human-to-engine handoff that would otherwise trigger only
	// after the first user move.
	if newEngineWhite {
		s.triggerEngineForMove(newRec, gm)
	}
	return newRec, nil
}

func (s *GameService) loadRematchOfferer(ctx context.Context, gameID string) (int64, bool) {
	v, err := s.bus.Rdb().Get(ctx, rematchOfferKey+gameID).Result()
	if err != nil {
		return 0, false
	}
	uid, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return uid, true
}
