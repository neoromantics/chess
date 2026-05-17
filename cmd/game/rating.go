package main

// Glicko-2 rating updater. Consumes the durable game:events stream;
// on every GameFinished event with rec.Rated==true, applies a
// Glicko-2 update to both participants. One-game-per-rating-period
// for simplicity — Glickman's paper recommends batching into periods
// of 10–15 games for stable RD evolution, but a global periodic
// flush adds operational complexity we don't need at this scale.
//
// Multi-replica safety: consumer-group reader on rating-updater-group.
// Streams' at-most-once-per-group delivery means each event is
// processed exactly once across all game-service replicas. The
// rating update itself is two PG UPDATEs — idempotent per
// consumer-group ack, and the only operation that touches the row
// for that game's finalization.
//
// Numerical correctness: pkg/rating/glicko2.go is verified against
// the paper's worked example in glicko2_test.go (TestUpdateAgainstPaperExample).

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
	"github.com/neoromantics/chess/pkg/rating"
	"github.com/redis/go-redis/v9"
)

// runRatingUpdater consumes game:events forever, applying Glicko-2
// on every GameFinished. Started as a goroutine in main().
func (s *GameService) runRatingUpdater(ctx context.Context) {
	hostname, _ := os.Hostname()
	s.bus.Consume(ctx, eventbus.StreamGameEvents, "rating-updater-group", hostname, s.processRatingEvent)
}

func (s *GameService) processRatingEvent(ctx context.Context, msg redis.XMessage) {
	data, ok := msg.Values["data"].(string)
	if !ok {
		return
	}
	var evt eventbus.Event
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return
	}
	if evt.Type != eventbus.EvtGameFinished {
		return
	}
	// Bookkeep finalization. Emit sites send the full snapshot as
	// payload (which carries both result + rated), so we can label the
	// counter without a DB roundtrip. Falls back to unknown/false if
	// the payload is missing the fields — shouldn't happen but the
	// metric should never crash the consumer.
	var meta struct {
		Result string `json:"result"`
		Rated  bool   `json:"rated"`
	}
	_ = json.Unmarshal(evt.Payload, &meta)
	if meta.Result == "" {
		meta.Result = "unknown"
	}
	metrics.GamesFinishedTotal.WithLabelValues(meta.Result, strconv.FormatBool(meta.Rated)).Inc()

	s.applyRatingUpdate(ctx, evt)
}

// applyRatingUpdate computes new Glicko-2 numbers for both sides of a
// rated PvP game and writes them back. Skips unrated games, engine
// games, and games missing a player.
func (s *GameService) applyRatingUpdate(ctx context.Context, evt eventbus.Event) {
	rec, err := s.db.GetGame(evt.GameID)
	if err != nil || rec == nil || !rec.Rated || rec.WhiteUserID == nil || rec.BlackUserID == nil {
		return
	}
	// Timed scope covers the two PG UPDATEs + the Glicko math. The
	// math is microseconds; the UPDATEs are the real cost — if this
	// histogram's p95 ever climbs, PG is the suspect.
	t0 := time.Now()
	defer func() { metrics.RatingUpdateDuration.Observe(time.Since(t0).Seconds()) }()
	// Only finalize once per game. If the row's Result is still "*"
	// (open) we shouldn't be here — the only emitter is GameFinished,
	// so this is defensive against duplicate stream delivery races.
	if rec.Result == "" || rec.Result == "*" {
		return
	}
	w, err1 := s.db.GetUserByID(*rec.WhiteUserID)
	b, err2 := s.db.GetUserByID(*rec.BlackUserID)
	if err1 != nil || err2 != nil || w == nil || b == nil {
		return
	}

	wRes, bRes := rating.Result(0.5), rating.Result(0.5)
	switch rec.Result {
	case "1-0":
		wRes, bRes = 1.0, 0.0
	case "0-1":
		wRes, bRes = 0.0, 1.0
	}

	wPrior := rating.Player{Rating: float64(w.Rating), RD: float64(w.RD), Volatility: float64(w.Volatility)}
	bPrior := rating.Player{Rating: float64(b.Rating), RD: float64(b.RD), Volatility: float64(b.Volatility)}

	wNew := rating.Update(wPrior, []rating.Opponent{{P: bPrior, Score: wRes}})
	bNew := rating.Update(bPrior, []rating.Opponent{{P: wPrior, Score: bRes}})

	if err := s.db.UpdateUserRating(db.RatingUpdate{
		UserID: w.ID,
		Rating: float32(wNew.Rating), RD: float32(wNew.RD), Volatility: float32(wNew.Volatility),
		Wins:   boolToInt32(rec.Result == "1-0"),
		Losses: boolToInt32(rec.Result == "0-1"),
		Draws:  boolToInt32(rec.Result == "1/2-1/2"),
	}); err != nil {
		slog.Error("rating update (white) failed", "user_id", w.ID, "game_id", evt.GameID, "error", err)
	}
	if err := s.db.UpdateUserRating(db.RatingUpdate{
		UserID: b.ID,
		Rating: float32(bNew.Rating), RD: float32(bNew.RD), Volatility: float32(bNew.Volatility),
		Wins:   boolToInt32(rec.Result == "0-1"),
		Losses: boolToInt32(rec.Result == "1-0"),
		Draws:  boolToInt32(rec.Result == "1/2-1/2"),
	}); err != nil {
		slog.Error("rating update (black) failed", "user_id", b.ID, "game_id", evt.GameID, "error", err)
	}

	slog.Info("ratings updated",
		"game_id", evt.GameID,
		"white_id", w.ID, "white_rating", wNew.Rating,
		"black_id", b.ID, "black_rating", bNew.Rating)

	// Push a per-user notification so the SPA can refresh the rating
	// chip without a manual reload.
	push := func(uid int64, newRating float64) {
		payload, _ := json.Marshal(map[string]any{
			"user_id": uid, "rating": newRating, "game_id": evt.GameID,
		})
		s.bus.PublishUserEvent(ctx, uid, eventbus.Event{
			Type: "rating_updated", GameID: evt.GameID, Payload: payload,
		})
	}
	push(w.ID, wNew.Rating)
	push(b.ID, bNew.Rating)
}

func boolToInt32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
