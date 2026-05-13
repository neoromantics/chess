package main

// Glicko-2 rating updater, folded in from the former cmd/rating-updater
// pod during the 6→3 consolidation. Consumes the durable game:events
// stream and on every GameFinished event with rec.Rated==true, applies
// a Glicko-2 update to both participants.
//
// Multi-replica safety: this is a consumer-group reader on
// rating-updater-group. Streams' at-most-once-per-group delivery means
// each event is processed exactly once across all game-service
// replicas. The rating update itself is two PG UPDATEs — idempotent
// per consumer-group ack.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/rating"
	"github.com/redis/go-redis/v9"
)

// runRatingUpdater consumes game:events forever, applying Glicko-2
// on every GameFinished. Started as a goroutine in main().
func (s *GameService) runRatingUpdater(ctx context.Context) {
	hostname, _ := os.Hostname()
	for ctx.Err() == nil {
		msgs, err := s.bus.ReadEvents(ctx, "rating-updater-group", hostname, 5*time.Second)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("rating-updater: read events error", "error", err)
			}
			time.Sleep(1 * time.Second)
			continue
		}
		for _, msg := range msgs {
			s.processRatingEvent(ctx, msg)
			s.bus.Ack(ctx, eventbus.StreamGameEvents, "rating-updater-group", msg.ID)
		}
	}
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
	s.applyRatingUpdate(ctx, evt)
}

// applyRatingUpdate computes new Glicko-2 numbers for both sides of a
// rated PvP game and writes them back. Skips unrated games, engine
// games, and games missing a player (shouldn't happen but defensive).
//
// One-game-per-rating-period: each game becomes its own rating period
// for simplicity. The Glicko-2 paper recommends batching games into
// periods of ~10-15 games each for stable RD evolution; we accept the
// noise tradeoff for simpler bookkeeping. Verify against the paper's
// worked example before claiming rating accuracy (see CLAUDE.md TODO).
func (s *GameService) applyRatingUpdate(ctx context.Context, evt eventbus.Event) {
	rec, err := s.db.GetGame(evt.GameID)
	if err != nil || !rec.Rated || rec.WhiteUserID == nil || rec.BlackUserID == nil {
		return
	}
	w, err1 := s.db.GetUserByID(*rec.WhiteUserID)
	b, err2 := s.db.GetUserByID(*rec.BlackUserID)
	if err1 != nil || err2 != nil {
		return
	}

	wRes, bRes := rating.Result(0.5), rating.Result(0.5)
	switch rec.Result {
	case "1-0":
		wRes, bRes = 1.0, 0.0
	case "0-1":
		wRes, bRes = 0.0, 1.0
	}

	wNew := rating.Update(rating.Player{
		Rating: float64(w.Rating), RD: float64(w.RD), Volatility: float64(w.Volatility),
	}, []rating.Opponent{{
		P: rating.Player{
			Rating: float64(b.Rating), RD: float64(b.RD), Volatility: float64(b.Volatility),
		},
		Score: wRes,
	}})
	bNew := rating.Update(rating.Player{
		Rating: float64(b.Rating), RD: float64(b.RD), Volatility: float64(b.Volatility),
	}, []rating.Opponent{{
		P: rating.Player{
			Rating: float64(w.Rating), RD: float64(w.RD), Volatility: float64(w.Volatility),
		},
		Score: bRes,
	}})

	_ = s.db.UpdateUserRating(db.RatingUpdate{
		UserID: w.ID,
		Rating: float32(wNew.Rating), RD: float32(wNew.RD), Volatility: float32(wNew.Volatility),
		Wins:   boolToInt32(rec.Result == "1-0"),
		Losses: boolToInt32(rec.Result == "0-1"),
		Draws:  boolToInt32(rec.Result == "1/2-1/2"),
	})
	_ = s.db.UpdateUserRating(db.RatingUpdate{
		UserID: b.ID,
		Rating: float32(bNew.Rating), RD: float32(bNew.RD), Volatility: float32(bNew.Volatility),
		Wins:   boolToInt32(rec.Result == "0-1"),
		Losses: boolToInt32(rec.Result == "1-0"),
		Draws:  boolToInt32(rec.Result == "1/2-1/2"),
	})

	slog.Info("ratings updated",
		"game_id", evt.GameID,
		"white_rating", wNew.Rating,
		"black_rating", bNew.Rating)
}

func boolToInt32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
