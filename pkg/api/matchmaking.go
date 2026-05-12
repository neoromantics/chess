package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/leader"
)

// Matchmaking constants
const (
	MatchmakingQueueKeyPrefix  = "mm:queue:" // mm:queue:{time_control}
	MatchmakingPairingInterval = 2 * time.Second
	InitialRatingWindow        = 50.0
	MaxRatingWindow            = 400.0
	RatingWindowExpansion      = 50.0
)

type matchmakingJoinRequest struct {
	TimeControl string `json:"time_control"`
}

func (s *Server) handleMatchmakingJoin(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Fetch latest user data from DB (rating might have changed)
	dbUser, err := s.db.GetUserByID(user.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	var req matchmakingJoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Validate time control
	if !isValidTimeControl(req.TimeControl) {
		http.Error(w, "invalid time control", http.StatusBadRequest)
		return
	}

	// Add to Redis sorted set: key = mm:queue:{tc}, member = user_id, score = rating
	ctx := r.Context()
	queueKey := MatchmakingQueueKeyPrefix + req.TimeControl
	err = s.bus.SetSortedSet(ctx, queueKey, strconv.FormatInt(dbUser.ID, 10), float64(dbUser.Rating))
	if err != nil {
		slog.Error("failed to join matchmaking queue", "error", err, "user_id", dbUser.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("user joined matchmaking queue", "user_id", dbUser.ID, "time_control", req.TimeControl, "rating", dbUser.Rating)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleMatchmakingLeave(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req matchmakingJoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	queueKey := MatchmakingQueueKeyPrefix + req.TimeControl
	err := s.bus.RemoveFromSortedSet(ctx, queueKey, strconv.FormatInt(user.UserID, 10))
	if err != nil {
		slog.Error("failed to leave matchmaking queue", "error", err, "user_id", user.UserID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("user left matchmaking queue", "user_id", user.UserID, "time_control", req.TimeControl)
	w.WriteHeader(http.StatusNoContent)
}

func isValidTimeControl(tc string) bool {
	switch tc {
	case "1+0", "3+0", "3+2", "5+0", "5+3", "10+0", "10+5", "30+0":
		return true
	}
	return false
}

// startMatchmaker starts the leader-elected matchmaking pairing loop.
func (s *Server) startMatchmaker(ctx context.Context) {
	l := leader.NewElection(s.bus.Rdb(), "matchmaker", leader.WithLeaseTTL(10*time.Second))

	go l.Run(ctx, func(leaderCtx context.Context) {
		ticker := time.NewTicker(MatchmakingPairingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-ticker.C:
				s.runMatchmakingSweep(leaderCtx)
			}
		}
	})
}

func (s *Server) runMatchmakingSweep(ctx context.Context) {
	// For each time control queue
	timeControls := []string{"1+0", "3+0", "3+2", "5+0", "5+3", "10+0", "10+5", "30+0"}
	for _, tc := range timeControls {
		s.pairQueue(ctx, tc)
	}
}

func (s *Server) pairQueue(ctx context.Context, tc string) {
	queueKey := MatchmakingQueueKeyPrefix + tc

	// Get all players in the queue
	members, err := s.bus.GetSortedSetRangeWithScores(ctx, queueKey, 0, -1)
	if err != nil || len(members) < 2 {
		return
	}

	paired := make(map[string]bool)

	for i := 0; i < len(members)-1; i++ {
		m1 := members[i]
		if paired[m1.Member] {
			continue
		}

		for j := i + 1; j < len(members); j++ {
			m2 := members[j]
			if paired[m2.Member] {
				continue
			}

			diff := math.Abs(m1.Score - m2.Score)
			if diff <= InitialRatingWindow {
				// Match found!
				s.createMatch(ctx, m1.Member, m2.Member, tc)
				paired[m1.Member] = true
				paired[m2.Member] = true
				break
			}
		}
	}
}

func (s *Server) createMatch(ctx context.Context, id1, id2 string, tc string) {
	uid1, _ := strconv.ParseInt(id1, 10, 64)
	uid2, _ := strconv.ParseInt(id2, 10, 64)

	// Atomic ZREM both from queue
	queueKey := MatchmakingQueueKeyPrefix + tc
	err := s.bus.RemoveFromSortedSet(ctx, queueKey, id1, id2)
	if err != nil {
		slog.Error("failed to remove users from mm queue", "error", err, "u1", uid1, "u2", uid2)
		return
	}

	// Create a new game
	gameID := uuid.New().String()

	// Randomize who is white
	whiteID, blackID := uid1, uid2
	if time.Now().UnixNano()%2 == 0 {
		whiteID, blackID = uid2, uid1
	}

	record := &db.GameRecord{
		ID:          gameID,
		WhiteUserID: &whiteID,
		BlackUserID: &blackID,
		FEN:         "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1",
		History:     "[]",
		HistorySAN:  "[]",
		TimeControl: tc,
		Rated:       true,
		Status:      "ongoing",
		Result:      "*",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	err = s.db.SaveGame(record)
	if err != nil {
		slog.Error("failed to save matched game", "error", err)
		return
	}

	// Initialize clock in Redis
	s.bus.SetClock(ctx, gameID, bus.GameClock{
		WhiteMS:       600000,
		BlackMS:       600000,
		TurnStartedAt: time.Now().UnixMilli(),
	})

	// Notify users via user.evt.{id}
	s.hub.PublishUser(ctx, whiteID, "match_found", map[string]any{
		"game_id":  gameID,
		"opponent": id2,
		"color":    "white",
	})
	s.hub.PublishUser(ctx, blackID, "match_found", map[string]any{
		"game_id":  gameID,
		"opponent": id1,
		"color":    "black",
	})

	slog.Info("match found and game created", "game_id", gameID, "white", whiteID, "black", blackID, "tc", tc)
}
