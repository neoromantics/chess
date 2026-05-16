package main

// Bot side of the draw/takeback negotiation flow.
//
// TODO(matchmaker-engine-fallback): remove this whole file along with
// cmd/game/bots.go and the is_bot DB column once organic pairing volume
// makes the engine-fallback matchmaker unnecessary.
//
// Trigger model: handleDrawOffer / handleTakebackOffer call into
// maybeScheduleBot*Response after the offer SETNX succeeds. If the
// opponent is a seeded bot, a goroutine sleeps for botResponseDelay()
// (0.5–10s), re-reads the offer to confirm it's still ours, then either
// accepts or declines. The accept/decline paths inline what the HTTP
// handlers do — same lock, same event fan-out — so the SPA on the
// human's side sees the bot's reply via the normal WS event stream.
//
// Why we don't reuse handleDrawAccept / handleTakebackAccept directly:
// those are HTTP handlers expecting an *http.Request with the bot's
// user_id injected as a query param. Synthesizing one would mean
// faking r.Context() lifetime and writeJSON to a discarded recorder.
// Inlining the 20-odd lines per side is simpler and obviously
// deletable along with the bot pool.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/game"
)

// maybeScheduleBotDrawResponse fires a delayed bot reply (accept or
// decline) if the offer's recipient is a seeded bot. No-op for human-vs-
// human games. Called from handleDrawOffer right after the SETNX
// succeeds.
func (s *GameService) maybeScheduleBotDrawResponse(rec *db.GameRecord, offererUID int64) {
	if !isBotMatch(rec) {
		return
	}
	botUID, ok := opponentUIDOnRec(rec, offererUID)
	if !ok || !isBotUserID(botUID) {
		return
	}
	delay := botResponseDelay()
	accept := botShouldAcceptOffer()
	gameID := rec.ID
	slog.Info("bot draw response scheduled",
		"game_id", gameID, "bot_id", botUID, "accept", accept, "delay", delay)

	go func() {
		t := time.NewTimer(delay)
		defer t.Stop()
		<-t.C
		ctx := context.Background()
		// Re-check the offer: the human may have rescinded by playing a
		// move, or the offer TTL may have expired (drawOfferRedisTTL
		// caps at remaining clock for clocked games). Either way, if
		// the offer is no longer pending or no longer ours, drop.
		cur, exists := s.loadDrawOfferer(ctx, gameID)
		if !exists || cur != offererUID {
			return
		}
		if accept {
			s.botAcceptDraw(ctx, gameID, botUID)
		} else {
			s.botDeclineDraw(ctx, gameID, botUID)
		}
	}()
}

// botAcceptDraw mirrors handleDrawAccept's mutation path. Lock + re-
// read + set status + save + delete offer + tear down clock + emit
// the StateUpdated / DrawAccepted / GameFinished events.
func (s *GameService) botAcceptDraw(ctx context.Context, gameID string, botUID int64) {
	lock, err := acquireGameLock(ctx, s.bus.Rdb(), gameID, gameLockTTL)
	if err != nil || lock == nil {
		slog.Warn("bot draw accept: lock unavailable", "game_id", gameID)
		return
	}
	defer lock.release(context.Background())

	offerer, exists := s.loadDrawOfferer(ctx, gameID)
	if !exists {
		return
	}
	rec, err := s.getGameCached(ctx, gameID)
	if err != nil || rec == nil {
		return
	}
	if rec.Status != "ongoing" {
		_ = s.bus.Rdb().Del(ctx, drawOfferKey+gameID).Err()
		return
	}
	rec.Status = "draw_agreement"
	rec.Result = "1/2-1/2"
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(ctx, rec); err != nil {
		slog.Error("bot draw accept: save failed", "game_id", gameID, "error", err)
		return
	}
	_ = s.bus.Rdb().Del(ctx, drawOfferKey+gameID).Err()
	deleteClock(ctx, s.bus.Rdb(), gameID)

	snapshot := s.snapshotFromRecord(ctx, rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: gameID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtDrawAccepted, GameID: gameID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtGameFinished, GameID: gameID, Payload: snapPayload,
	})
	slog.Info("bot draw accepted", "game_id", gameID, "bot_id", botUID, "offerer", offerer)
}

// botDeclineDraw mirrors handleDrawDecline.
func (s *GameService) botDeclineDraw(ctx context.Context, gameID string, botUID int64) {
	if err := s.bus.Rdb().Del(ctx, drawOfferKey+gameID).Err(); err != nil {
		slog.Warn("bot draw decline: del offer key failed", "game_id", gameID, "error", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"by_user_id": botUID,
		"game_id":    gameID,
	})
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type:    eventbus.EvtDrawDeclined,
		GameID:  gameID,
		Payload: payload,
	})
	slog.Info("bot draw declined", "game_id", gameID, "bot_id", botUID)
}

// maybeScheduleBotTakebackResponse fires a delayed bot reply if the
// takeback recipient is a seeded bot. Called from handleTakebackOffer.
func (s *GameService) maybeScheduleBotTakebackResponse(rec *db.GameRecord, requesterUID int64) {
	if !isBotMatch(rec) {
		return
	}
	botUID, ok := opponentUIDOnRec(rec, requesterUID)
	if !ok || !isBotUserID(botUID) {
		return
	}
	delay := botResponseDelay()
	accept := botShouldAcceptOffer()
	gameID := rec.ID
	slog.Info("bot takeback response scheduled",
		"game_id", gameID, "bot_id", botUID, "accept", accept, "delay", delay)

	go func() {
		t := time.NewTimer(delay)
		defer t.Stop()
		<-t.C
		ctx := context.Background()
		cur, exists := s.loadTakebackRequester(ctx, gameID)
		if !exists || cur != requesterUID {
			return
		}
		if accept {
			s.botAcceptTakeback(ctx, gameID, botUID)
		} else {
			s.botDeclineTakeback(ctx, gameID, botUID)
		}
	}()
}

// botAcceptTakeback mirrors handleTakebackAccept's mutation path. Pops
// the right number of plies, rewrites history/FEN, rebases the clock,
// emits StateUpdated + TakebackAccepted.
func (s *GameService) botAcceptTakeback(ctx context.Context, gameID string, botUID int64) {
	lock, err := acquireGameLock(ctx, s.bus.Rdb(), gameID, gameLockTTL)
	if err != nil || lock == nil {
		slog.Warn("bot takeback accept: lock unavailable", "game_id", gameID)
		return
	}
	defer lock.release(context.Background())

	requester, exists := s.loadTakebackRequester(ctx, gameID)
	if !exists {
		return
	}
	rec, err := s.getGameCached(ctx, gameID)
	if err != nil || rec == nil {
		return
	}
	if rec.Status != "ongoing" {
		_ = s.bus.Rdb().Del(ctx, takebackOfferKey+gameID).Err()
		return
	}

	gm := game.NewGame()
	var history []string
	_ = json.Unmarshal([]byte(rec.History), &history)
	gm.Load(rec.StartFEN, history, false, false)
	requesterColor := requesterColorOnRec(rec, requester)
	pliesToPop := pliesToTakeBack(gm, requesterColor)
	if pliesToPop == 0 {
		_ = s.bus.Rdb().Del(ctx, takebackOfferKey+gameID).Err()
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

	if c, _ := loadClock(ctx, s.bus.Rdb(), gameID); c != nil {
		c.Mover = "w"
		if gm.Board.SideToMove == core.Black {
			c.Mover = "b"
		}
		c.TurnStartedMS = time.Now().UnixMilli()
		_ = c.save(ctx, s.bus.Rdb())
		c.scheduleFlag(ctx, s.bus.Rdb())
	}

	if err := s.saveGameCached(ctx, rec); err != nil {
		slog.Error("bot takeback accept: save failed", "game_id", gameID, "error", err)
		return
	}
	_ = s.bus.Rdb().Del(ctx, takebackOfferKey+gameID).Err()

	snapshot := s.snapshotFromRecord(ctx, rec)
	snapPayload, _ := json.Marshal(snapshot)
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: gameID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: EvtTakebackAccepted, GameID: gameID, Payload: snapPayload,
	})
	slog.Info("bot takeback accepted",
		"game_id", gameID, "bot_id", botUID, "requester", requester, "plies_popped", pliesToPop)
}

// botDeclineTakeback mirrors handleTakebackDecline.
func (s *GameService) botDeclineTakeback(ctx context.Context, gameID string, botUID int64) {
	if err := s.bus.Rdb().Del(ctx, takebackOfferKey+gameID).Err(); err != nil {
		slog.Warn("bot takeback decline: del offer key failed", "game_id", gameID, "error", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"by_user_id": botUID,
		"game_id":    gameID,
	})
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type:    EvtTakebackDeclined,
		GameID:  gameID,
		Payload: payload,
	})
	slog.Info("bot takeback declined", "game_id", gameID, "bot_id", botUID)
}
