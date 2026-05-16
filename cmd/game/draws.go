package main

// Draw flow. PvP only — there's no opponent to negotiate with in an
// engine game. The server holds a single ephemeral key per game:
//   draw-offer:{game_id}  string user_id of the offerer, TTL = 60s
//                         (capped to remaining clock for clocked games).
//
// Lifecycle:
//   - POST /api/draw_offer → SETNX the key; 409 if an offer is already
//     pending. Broadcasts DrawOffered { from_user_id } so the opposite
//     side's SPA shows Accept / Decline buttons.
//   - POST /api/draw_accept → only the OPPOSITE participant can accept.
//     Sets rec.Status="draw_agreement", Result="1/2-1/2", clears clock,
//     emits StateUpdated + GameFinished + DrawAccepted.
//   - POST /api/draw_decline → opposite participant. Just DELs the
//     key and emits DrawDeclined { by_user_id }.
//
// Authorization mirrors handleHTTPMove: requireGameAccess proves the
// caller participates; we then check side-specifics inline.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
)

const (
	drawOfferKey = "draw-offer:"
	drawOfferTTL = 60 * time.Second
)

// requireDrawAccess validates that the caller is a participant of an
// ongoing PvP game. Engine games can't draw — there's no opposite
// side to negotiate with.
func (s *GameService) requireDrawAccess(w http.ResponseWriter, r *http.Request) (*db.GameRecord, int64, bool) {
	rec, uid, ok := s.requireGameAccess(w, r)
	if !ok {
		return nil, 0, false
	}
	if rec.WhiteUserID == nil || rec.BlackUserID == nil {
		http.Error(w, "draws require a human-vs-human game", http.StatusBadRequest)
		return nil, 0, false
	}
	if rec.Status != "ongoing" {
		http.Error(w, "game is already finished", http.StatusConflict)
		return nil, 0, false
	}
	return rec, uid, true
}

// drawOfferRedisTTL returns how long a fresh offer should live. For
// clocked games we cap at the offerer's remaining bank so a stale
// offer can't outlive the game; otherwise default to 60s which
// matches typical online-chess UX.
func (s *GameService) drawOfferRedisTTL(ctx context.Context, gameID string) time.Duration {
	c, err := loadClock(ctx, s.bus.Rdb(), gameID)
	if err != nil || c == nil {
		return drawOfferTTL
	}
	min := c.WhiteMS
	if c.BlackMS < min {
		min = c.BlackMS
	}
	if min <= 0 {
		return drawOfferTTL
	}
	d := time.Duration(min) * time.Millisecond
	if d > drawOfferTTL {
		return drawOfferTTL
	}
	if d < 5*time.Second {
		return 5 * time.Second
	}
	return d
}

func (s *GameService) handleDrawOffer(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireDrawAccess(w, r)
	if !ok {
		return
	}
	ttl := s.drawOfferRedisTTL(r.Context(), rec.ID)
	set, err := s.bus.Rdb().SetNX(r.Context(), drawOfferKey+rec.ID, strconv.FormatInt(uid, 10), ttl).Result()
	if err != nil {
		http.Error(w, "redis error", http.StatusInternalServerError)
		return
	}
	if !set {
		http.Error(w, "a draw offer is already pending", http.StatusConflict)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"from_user_id": uid,
		"game_id":      rec.ID,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type:    eventbus.EvtDrawOffered,
		GameID:  rec.ID,
		Payload: payload,
	})
	slog.Info("draw offered", "game_id", rec.ID, "from", uid)
	// Bot-fallback: if the opposite side is a seeded bot, kick off a
	// delayed accept/decline so the offer doesn't sit unresolved.
	// TODO(matchmaker-engine-fallback): remove with the bot pool.
	s.maybeScheduleBotDrawResponse(rec, uid)
	w.WriteHeader(http.StatusAccepted)
}

func (s *GameService) handleDrawDecline(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireDrawAccess(w, r)
	if !ok {
		return
	}
	offerer, exists := s.loadDrawOfferer(r.Context(), rec.ID)
	if !exists {
		http.Error(w, "no pending draw offer", http.StatusNotFound)
		return
	}
	if offerer == uid {
		http.Error(w, "cannot decline your own offer (let it expire instead)", http.StatusBadRequest)
		return
	}
	_ = s.bus.Rdb().Del(r.Context(), drawOfferKey+rec.ID).Err()
	payload, _ := json.Marshal(map[string]any{
		"by_user_id": uid,
		"game_id":    rec.ID,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type:    eventbus.EvtDrawDeclined,
		GameID:  rec.ID,
		Payload: payload,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *GameService) handleDrawAccept(w http.ResponseWriter, r *http.Request) {
	rec, uid, ok := s.requireDrawAccess(w, r)
	if !ok {
		return
	}
	offerer, exists := s.loadDrawOfferer(r.Context(), rec.ID)
	if !exists {
		http.Error(w, "no pending draw offer", http.StatusNotFound)
		return
	}
	if offerer == uid {
		http.Error(w, "cannot accept your own offer", http.StatusBadRequest)
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

	// Re-check after lock — the offer may have expired or been resolved
	// while we waited.
	if _, exists := s.loadDrawOfferer(r.Context(), rec.ID); !exists {
		http.Error(w, "draw offer expired before accept landed", http.StatusConflict)
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
	rec.Status = "draw_agreement"
	rec.Result = "1/2-1/2"
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(r.Context(), rec); err != nil {
		slog.Error("draw accept save failed", "game_id", rec.ID, "error", err)
		http.Error(w, "save failed", http.StatusInternalServerError)
		return
	}
	_ = s.bus.Rdb().Del(r.Context(), drawOfferKey+rec.ID).Err()
	deleteClock(r.Context(), s.bus.Rdb(), rec.ID)

	snapshot := s.snapshotFromRecord(r.Context(), rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: rec.ID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtDrawAccepted, GameID: rec.ID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(r.Context(), eventbus.Event{
		Type: eventbus.EvtGameFinished, GameID: rec.ID, Payload: snapPayload,
	})
	slog.Info("draw accepted", "game_id", rec.ID, "by", uid, "offerer", offerer)
	writeJSON(w, snapshot)
}

func (s *GameService) loadDrawOfferer(ctx context.Context, gameID string) (int64, bool) {
	v, err := s.bus.Rdb().Get(ctx, drawOfferKey+gameID).Result()
	if err != nil {
		return 0, false
	}
	uid, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return uid, true
}
