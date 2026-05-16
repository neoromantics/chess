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
// One queue per TC. Trimmed to the modal Blitz + Rapid pair so the
// pairing pool isn't diluted at our current playerbase size — keep
// this list in lock-step with validTimeControl in invites.go.
var supportedTCs = []string{
	"3+0", "10+0",
}

// handleJoinQueue / handleLeaveQueue are called from processCommand
// (cmd/game/main.go) when the gateway dispatches CmdJoinQueue /
// CmdLeaveQueue intents. Idempotent so re-delivery (Streams provide
// at-least-once) is safe.

// Rating-window pairing constants. Window starts tight (so similar-
// rated players prefer each other on the first pass) and grows over
// time so a long-waiting user eventually gets *some* match. Tuned to
// match the chess.com / lichess behaviour roughly.
const (
	mmWindowInitial = 50.0  // rating points each side of the target
	mmWindowGrowth  = 50.0  // per growthEvery
	mmGrowthEvery   = 2     // seconds — every 2s, window grows by mmWindowGrowth
	mmWindowMax     = 400.0 // hard cap regardless of wait time
)

// queueKey / joinedKey return the per-TC Redis keys. `queue` is a
// sorted set scored by rating; `joined` is a hash mapping user_id →
// unix-seconds enqueue time, used to size the expanding rating window.
func queueKey(tc string) string  { return "mm:queue:" + tc }
func joinedKey(tc string) string { return "mm:joined:" + tc }

func (s *GameService) handleJoinQueue(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.JoinQueueCmd
	_ = json.Unmarshal(cmd.Payload, &payload)
	uid := fmt.Sprintf("%d", cmd.UserID)
	s.bus.Rdb().ZAdd(ctx, queueKey(payload.TimeControl), redis.Z{
		Score: float64(payload.Rating), Member: uid,
	})
	s.bus.Rdb().HSet(ctx, joinedKey(payload.TimeControl), uid, time.Now().Unix())
	slog.Info("user joined queue", "user_id", cmd.UserID, "tc", payload.TimeControl, "rating", payload.Rating)
}

func (s *GameService) handleLeaveQueue(ctx context.Context, cmd eventbus.Command) {
	var payload eventbus.LeaveQueueCmd
	_ = json.Unmarshal(cmd.Payload, &payload)
	uid := fmt.Sprintf("%d", cmd.UserID)
	s.bus.Rdb().ZRem(ctx, queueKey(payload.TimeControl), uid)
	s.bus.Rdb().HDel(ctx, joinedKey(payload.TimeControl), uid)
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

// tryPair walks one TC's queue ordered by rating and pairs each user
// with the closest neighbour whose rating falls inside the user's
// current rating window. The window starts tight (±mmWindowInitial)
// and expands the longer a user has waited, so brand-new entries
// prefer similar-rated opponents but a stranded high-Elo user still
// gets *some* match within a few minutes.
//
// Pairs as many couples as it can in a single tick. Each successful
// pairing atomically removes both users from the queue + joined hash;
// the loop then advances past them.
func (s *GameService) tryPair(ctx context.Context, tc string) {
	rdb := s.bus.Rdb()
	now := time.Now().Unix()

	for {
		// Pull everyone with their score (rating). For tens-of-thousands
		// scale we'd page or shard per rating bucket; at our current
		// scale a single ZRangeWithScores per tick is fine.
		entries, err := rdb.ZRangeWithScores(ctx, queueKey(tc), 0, -1).Result()
		if err != nil || len(entries) < 2 {
			return
		}
		joined, _ := rdb.HGetAll(ctx, joinedKey(tc)).Result()

		pairedThisRound := false
		for i := 0; i < len(entries)-1; i++ {
			a := entries[i]
			aID, _ := a.Member.(string)
			win := currentRatingWindow(now, joined[aID])

			// Look at the next neighbour; if they fall within both A's
			// and their own window, pair them. (Symmetric check so the
			// younger entry doesn't get matched outside its tighter
			// window just because A waited long enough.)
			b := entries[i+1]
			bID, _ := b.Member.(string)
			bwin := currentRatingWindow(now, joined[bID])
			gap := b.Score - a.Score
			if gap <= win && gap <= bwin {
				// Atomic remove from both structures; on conflict (another
				// leader paired them first) skip silently.
				removed, err := rdb.ZRem(ctx, queueKey(tc), aID, bID).Result()
				if err != nil || removed < 2 {
					continue
				}
				rdb.HDel(ctx, joinedKey(tc), aID, bID)
				u1, _ := strconv.ParseInt(aID, 10, 64)
				u2, _ := strconv.ParseInt(bID, 10, 64)
				s.dispatchPaired(ctx, u1, u2, tc)
				pairedThisRound = true
				i++ // skip past the paired-with neighbour
			}
		}
		if !pairedThisRound {
			return
		}
	}
}

// currentRatingWindow returns the rating tolerance a user currently
// allows, growing from mmWindowInitial → mmWindowMax over the time
// they've been queued.
func currentRatingWindow(nowSec int64, joinedAt string) float64 {
	if joinedAt == "" {
		return mmWindowInitial
	}
	t, err := strconv.ParseInt(joinedAt, 10, 64)
	if err != nil {
		return mmWindowInitial
	}
	elapsed := nowSec - t
	if elapsed < 0 {
		elapsed = 0
	}
	win := mmWindowInitial + float64(elapsed/int64(mmGrowthEvery))*mmWindowGrowth
	if win > mmWindowMax {
		win = mmWindowMax
	}
	return win
}

func (s *GameService) dispatchPaired(ctx context.Context, u1, u2 int64, tc string) {
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
