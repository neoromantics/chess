package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/leader"
	"github.com/neoromantics/chess/pkg/rating"
)

// startRatingUpdater starts the leader-elected rating update loop.
func (s *Server) startRatingUpdater(ctx context.Context) {
	l := leader.NewElection(s.bus.Rdb(), "rating-updater", leader.WithLeaseTTL(10*time.Second))

	go l.Run(ctx, func(leaderCtx context.Context) {
		// Subscribe to game.finished events only when leader
		s.bus.Subscribe(leaderCtx, bus.GameFinishedEventChannel, func(payload []byte) {
			var evt bus.GameFinishedEvent
			if err := json.Unmarshal(payload, &evt); err != nil {
				slog.Error("failed to unmarshal game finished event", "error", err)
				return
			}
			s.handleRatingUpdate(leaderCtx, evt)
		})

		// Keep alive until leaderCtx is cancelled
		<-leaderCtx.Done()
	})
}

func (s *Server) handleRatingUpdate(ctx context.Context, evt bus.GameFinishedEvent) {
	// We need to fetch the full game record to get player IDs and 'rated' flag
	record, err := s.db.GetGame(evt.GameID)
	if err != nil {
		slog.Error("failed to fetch game for rating update", "error", err, "game_id", evt.GameID)
		return
	}

	if !record.Rated || record.WhiteUserID == nil || record.BlackUserID == nil {
		return
	}

	// Fetch players
	white, err := s.db.GetUserByID(*record.WhiteUserID)
	if err != nil {
		return
	}
	black, err := s.db.GetUserByID(*record.BlackUserID)
	if err != nil {
		return
	}

	// Map result to score
	var whiteScore, blackScore rating.Result
	switch record.Result {
	case "1-0":
		whiteScore, blackScore = 1.0, 0.0
	case "0-1":
		whiteScore, blackScore = 0.0, 1.0
	case "1/2-1/2":
		whiteScore, blackScore = 0.5, 0.5
	default:
		return // Game not finished with a decisive result
	}

	// Update Glicko-2
	pWhite := rating.Player{Rating: float64(white.Rating), RD: float64(white.RD), Volatility: float64(white.Volatility)}
	pBlack := rating.Player{Rating: float64(black.Rating), RD: float64(black.RD), Volatility: float64(black.Volatility)}

	newWhite := rating.Update(pWhite, []rating.Opponent{{P: pBlack, Score: whiteScore}})
	newBlack := rating.Update(pBlack, []rating.Opponent{{P: pWhite, Score: blackScore}})

	// Save updates
	err = s.db.UpdateUserRating(db.RatingUpdate{
		UserID:     white.ID,
		Rating:     float32(newWhite.Rating),
		RD:         float32(newWhite.RD),
		Volatility: float32(newWhite.Volatility),
		Wins:       i32(whiteScore == 1.0),
		Losses:     i32(whiteScore == 0.0),
		Draws:      i32(whiteScore == 0.5),
	})
	if err != nil {
		slog.Error("failed to update white rating", "error", err, "user_id", white.ID)
	}

	err = s.db.UpdateUserRating(db.RatingUpdate{
		UserID:     black.ID,
		Rating:     float32(newBlack.Rating),
		RD:         float32(newBlack.RD),
		Volatility: float32(newBlack.Volatility),
		Wins:       i32(blackScore == 1.0),
		Losses:     i32(blackScore == 0.0),
		Draws:      i32(blackScore == 0.5),
	})
	if err != nil {
		slog.Error("failed to update black rating", "error", err, "user_id", black.ID)
	}

	// Notify users
	s.hub.PublishUser(ctx, white.ID, "rating_updated", map[string]any{
		"rating": int(newWhite.Rating),
		"rd":     int(newWhite.RD),
	})
	s.hub.PublishUser(ctx, black.ID, "rating_updated", map[string]any{
		"rating": int(newBlack.Rating),
		"rd":     int(newBlack.RD),
	})

	slog.Info("ratings updated", "game_id", evt.GameID, "white", white.ID, "black", black.ID, "result", record.Result)
}

func i32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
