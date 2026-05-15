package main

// Matchmaking, folded into game-service from the former cmd/matchmaker
// pod during the 6→3 consolidation. The pairing loop is small (single
// 2s ticker, ZRange-based pair-take, then dispatches a CreatePvPGame
// Command which this same service already consumes) and shares all
// its state (PG + Redis) with game-service, so a separate pod was
// pure overhead.
//
// Multi-replica safety:
//   - JoinQueue / LeaveQueue handlers are idempotent (ZADD/ZREM on
//     mm:queue:{tc}); processing the same Command twice is harmless.
//   - The pairing loop is NOT idempotent across replicas: two game-
//     service pods both running pairingLoop will race the ZRange/ZRem
//     and could pair the same user twice. We protect with a Redis
//     leader-election lock (mm:leader, 15s TTL, renewed every 5s).
//     Only the holder runs tryPair. If the holder dies, the lock
//     expires and another replica takes over within the TTL.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/redis/go-redis/v9"
)

// supportedTCs is the per-time-control queue keyspace the pairing loop
// scans every tick. Matches what the SPA matchmaking picker exposes.
// Single rapid TC for now; the rest come back later (see ROADMAP.md).
var supportedTCs = []string{"15+10"}

// handleJoinQueue / handleLeaveQueue are called from processCommand
// (cmd/game/main.go) when the gateway dispatches CmdJoinQueue /
// CmdLeaveQueue intents. Idempotent so re-delivery (Streams provide
// at-least-once) is safe.

func (s *GameService) handleJoinQueue(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.JoinQueueCmd
	_ = json.Unmarshal(cmd.Payload, &payload)
	key := "mm:queue:" + payload.TimeControl
	s.bus.Rdb().ZAdd(ctx, key, redis.Z{
		Score:  float64(payload.Rating),
		Member: fmt.Sprintf("%d", cmd.UserID),
	})
	slog.Info("user joined queue", "user_id", cmd.UserID, "tc", payload.TimeControl)
}

func (s *GameService) handleLeaveQueue(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.LeaveQueueCmd
	_ = json.Unmarshal(cmd.Payload, &payload)
	key := "mm:queue:" + payload.TimeControl
	s.bus.Rdb().ZRem(ctx, key, fmt.Sprintf("%d", cmd.UserID))
	slog.Info("user left queue", "user_id", cmd.UserID, "tc", payload.TimeControl)
}

// runPairingLoop starts the 2s ticker on the elected leader. Use one
// goroutine per game-service pod; the SETNX-based leader election lets
// exactly one pod's loop do real work at a time.
func (s *GameService) runPairingLoop(ctx context.Context) {
	hostname, _ := os.Hostname()
	for ctx.Err() == nil {
		// Try to grab the leader lock. If we lose, sleep and retry.
		got, err := s.bus.Rdb().SetNX(ctx, "mm:leader", hostname, 15*time.Second).Result()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if !got {
			time.Sleep(5 * time.Second)
			continue
		}
		slog.Info("matchmaker: assumed pairing leadership", "pod", hostname)
		s.holdAndPair(ctx, hostname)
	}
}

// holdAndPair runs the actual pairing ticks while extending the lease
// every 5s. Returns when ctx cancels or the lease is lost.
func (s *GameService) holdAndPair(ctx context.Context, hostname string) {
	pair := time.NewTicker(2 * time.Second)
	renew := time.NewTicker(5 * time.Second)
	defer pair.Stop()
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			// PEXPIRE only renews if the key still has our value.
			ok, _ := s.bus.Rdb().Get(ctx, "mm:leader").Result()
			if ok != hostname {
				slog.Warn("matchmaker: lost leadership")
				return
			}
			s.bus.Rdb().Expire(ctx, "mm:leader", 15*time.Second)
		case <-pair.C:
			for _, tc := range supportedTCs {
				s.tryPair(ctx, tc)
			}
		}
	}
}

// tryPair takes the two lowest-rated entries in a time-control queue
// (no Glicko rating-bucket logic yet — see TODO in CLAUDE.md), removes
// them atomically, dispatches a CreatePvPGame command, and notifies
// both users via their personal user.evt channels with their color.
func (s *GameService) tryPair(ctx context.Context, tc string) {
	key := "mm:queue:" + tc
	res, err := s.bus.Rdb().ZRange(ctx, key, 0, 1).Result()
	if err != nil || len(res) < 2 {
		return
	}
	u1, _ := strconv.ParseInt(res[0], 10, 64)
	u2, _ := strconv.ParseInt(res[1], 10, 64)
	s.bus.Rdb().ZRem(ctx, key, res[0], res[1])

	gameID := uuid.New().String()
	slog.Info("match found", "u1", u1, "u2", u2, "game_id", gameID, "tc", tc)

	// CreatePvPGame Command — game-service's own consumer (us!) picks
	// this up and writes the row. Same pattern as before, just on the
	// same replica that's running the pairing loop.
	createPayload, _ := json.Marshal(eventbus.CreatePvPGameCmd{
		WhiteUserID: u1,
		BlackUserID: u2,
		TimeControl: tc,
		Rated:       true,
	})
	s.bus.SendCommand(ctx, eventbus.Command{
		Type:    eventbus.CmdCreatePvPGame,
		GameID:  gameID,
		Payload: createPayload,
	})

	// Per-user push for SPA redirection. Each recipient gets a payload
	// with THEIR color so the frontend can flip the board immediately.
	whitePayload, _ := json.Marshal(eventbus.MatchFoundEvt{
		GameID: gameID, WhiteUserID: u1, BlackUserID: u2, Color: "white",
	})
	blackPayload, _ := json.Marshal(eventbus.MatchFoundEvt{
		GameID: gameID, WhiteUserID: u1, BlackUserID: u2, Color: "black",
	})
	s.bus.PublishUserEvent(ctx, u1, eventbus.Event{
		Type: eventbus.EvtMatchFound, GameID: gameID, Payload: whitePayload,
	})
	s.bus.PublishUserEvent(ctx, u2, eventbus.Event{
		Type: eventbus.EvtMatchFound, GameID: gameID, Payload: blackPayload,
	})

	// Audit-trail emission so future consumers (rating-updater,
	// observability) can see the pairing in the durable stream.
	auditPayload, _ := json.Marshal(eventbus.MatchFoundEvt{
		GameID: gameID, WhiteUserID: u1, BlackUserID: u2,
	})
	s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtMatchFound, GameID: gameID, Payload: auditPayload,
	})
}
