package main

// Server-authoritative clocks for PvP. Engine games carry no clock
// (TimeControl="engine"); their think-time is per-search, not
// per-side-bank.
//
// Storage:
//   clock:{game_id}            Redis hash with the bank state
//   clock:fallschedule         Redis sorted-set keyed by deadline (unix
//                              ms) of the current mover's flag-fall;
//                              the sweeper reads this to find games
//                              that ran out.
//
// Invariants:
//   - The clock is the truth for time. Backend deducts on every move;
//     the SPA only extrapolates locally for smoothness between snapshots.
//   - Per-game lock guards every read-modify-write on the clock hash.
//     Clock mutations always happen alongside game-state mutations
//     under the same lock.
//   - "now" is always time.Now().UnixMilli(). All clock values are ms.
//   - Initial clock is parsed from the time_control string ("15+10"
//     means 15 minutes initial + 10 second increment per move).
//   - White's clock starts ticking the moment the game is created. The
//     first MakeMove deducts elapsed wall time from white's bank.

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"encoding/json"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/redis/go-redis/v9"
)

const (
	clockKey           = "clock:"
	clockFallScheduleZ = "clock:fallschedule"
	clockSweepInterval = 500 * time.Millisecond
)

// clockState is the in-memory view of a single game's clock. Mover is
// "w" or "b"; TurnStartedMS is the unix-ms timestamp at which the
// current mover's turn began (i.e., when their bank started draining).
type clockState struct {
	GameID        string
	WhiteMS       int64
	BlackMS       int64
	IncMS         int64
	InitialMS     int64
	Mover         string
	TurnStartedMS int64
}

// parseTimeControl turns "M+S" (minutes initial + seconds increment)
// into ms pairs. Engine / unknown TCs return ok=false so the caller
// skips clock initialization entirely.
func parseTimeControl(tc string) (initialMS, incMS int64, ok bool) {
	tc = strings.TrimSpace(tc)
	if tc == "" || tc == "engine" {
		return 0, 0, false
	}
	parts := strings.SplitN(tc, "+", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	initialMin, err := strconv.Atoi(parts[0])
	if err != nil || initialMin < 0 {
		return 0, 0, false
	}
	incSec, err := strconv.Atoi(parts[1])
	if err != nil || incSec < 0 {
		return 0, 0, false
	}
	return int64(initialMin) * 60 * 1000, int64(incSec) * 1000, true
}

// initClock writes a fresh clock hash for a brand-new PvP game. White
// is the initial mover; their turn starts immediately. No-ops for
// engine games (returns nil silently — the caller can ignore).
func initClock(ctx context.Context, rdb *redis.Client, rec *db.GameRecord) error {
	initial, inc, ok := parseTimeControl(rec.TimeControl)
	if !ok {
		return nil
	}
	now := time.Now().UnixMilli()
	c := &clockState{
		GameID:        rec.ID,
		WhiteMS:       initial,
		BlackMS:       initial,
		IncMS:         inc,
		InitialMS:     initial,
		Mover:         "w",
		TurnStartedMS: now,
	}
	if err := c.save(ctx, rdb); err != nil {
		return err
	}
	c.scheduleFlag(ctx, rdb)
	return nil
}

func loadClock(ctx context.Context, rdb *redis.Client, gameID string) (*clockState, error) {
	m, err := rdb.HGetAll(ctx, clockKey+gameID).Result()
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	parseI := func(k string) int64 {
		v, _ := strconv.ParseInt(m[k], 10, 64)
		return v
	}
	return &clockState{
		GameID:        gameID,
		WhiteMS:       parseI("white_ms"),
		BlackMS:       parseI("black_ms"),
		IncMS:         parseI("inc_ms"),
		InitialMS:     parseI("initial_ms"),
		Mover:         m["mover"],
		TurnStartedMS: parseI("turn_started_ms"),
	}, nil
}

func (c *clockState) save(ctx context.Context, rdb *redis.Client) error {
	return rdb.HSet(ctx, clockKey+c.GameID, map[string]any{
		"white_ms":        c.WhiteMS,
		"black_ms":        c.BlackMS,
		"inc_ms":          c.IncMS,
		"initial_ms":      c.InitialMS,
		"mover":           c.Mover,
		"turn_started_ms": c.TurnStartedMS,
	}).Err()
}

// scheduleFlag pushes the current mover's deadline into the sorted
// set so the sweeper can find them. Idempotent (ZADD overwrites the
// score for an existing member).
func (c *clockState) scheduleFlag(ctx context.Context, rdb *redis.Client) {
	if c.Mover == "" || c.TurnStartedMS == 0 {
		return
	}
	moverMS := c.WhiteMS
	if c.Mover == "b" {
		moverMS = c.BlackMS
	}
	deadline := c.TurnStartedMS + moverMS
	rdb.ZAdd(ctx, clockFallScheduleZ, redis.Z{Score: float64(deadline), Member: c.GameID})
}

func unscheduleFlag(ctx context.Context, rdb *redis.Client, gameID string) {
	rdb.ZRem(ctx, clockFallScheduleZ, gameID)
}

// deleteClock tears down both the hash and the schedule entry. Called
// when a game finishes (resign, checkmate, draw, timeout) so memory
// doesn't pile up and the sweeper doesn't repeatedly check finished
// rows.
func deleteClock(ctx context.Context, rdb *redis.Client, gameID string) {
	rdb.Del(ctx, clockKey+gameID)
	unscheduleFlag(ctx, rdb, gameID)
}

// currentTimes returns the snapshot-time view of both banks: the
// non-mover's bank as stored, the mover's bank reduced by elapsed wall
// time. Clamps at 0 — the bank never goes negative on the wire even if
// the sweeper hasn't fired yet.
func (c *clockState) currentTimes() (whiteMS, blackMS int64) {
	whiteMS = c.WhiteMS
	blackMS = c.BlackMS
	if c.Mover == "" || c.TurnStartedMS == 0 {
		return
	}
	elapsed := time.Now().UnixMilli() - c.TurnStartedMS
	if elapsed < 0 {
		elapsed = 0
	}
	if c.Mover == "w" {
		whiteMS -= elapsed
		if whiteMS < 0 {
			whiteMS = 0
		}
	} else {
		blackMS -= elapsed
		if blackMS < 0 {
			blackMS = 0
		}
	}
	return
}

// applyMove deducts the elapsed time from the moving side's bank,
// adds the increment, swaps the mover, restarts turn_started. Returns
// (flagged=true, loserSide=mover-before-swap) if elapsed > remaining
// — caller should finalize the game with a timeout result and NOT
// persist the post-increment bank (the clock is already 0).
func (c *clockState) applyMove(now int64) (flagged bool, loser string) {
	if c.Mover == "" || c.TurnStartedMS == 0 {
		return false, ""
	}
	elapsed := now - c.TurnStartedMS
	if elapsed < 0 {
		elapsed = 0
	}
	remaining := c.WhiteMS
	if c.Mover == "b" {
		remaining = c.BlackMS
	}
	if elapsed >= remaining {
		// Flag fell mid-turn. The opponent wins on time; the moving
		// side's bank lands at 0.
		loser = c.Mover
		if c.Mover == "w" {
			c.WhiteMS = 0
		} else {
			c.BlackMS = 0
		}
		c.TurnStartedMS = 0
		c.Mover = ""
		return true, loser
	}
	// Deduct + add increment + swap.
	if c.Mover == "w" {
		c.WhiteMS = remaining - elapsed + c.IncMS
		c.Mover = "b"
	} else {
		c.BlackMS = remaining - elapsed + c.IncMS
		c.Mover = "w"
	}
	c.TurnStartedMS = now
	return false, ""
}

// runClockFallSweeper polls the schedule sorted-set for games whose
// mover's deadline has passed and finalizes them as timeouts. Runs in
// a single goroutine on every game-service replica — the per-game
// lock makes the work idempotent across pods so leader election is
// unnecessary at this scale (the same flag fall claimed by two pods
// produces identical writes; the second pod no-ops on Status check).
func (s *GameService) runClockFallSweeper(ctx context.Context) {
	t := time.NewTicker(clockSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.processFlagFalls(ctx)
		}
	}
}

func (s *GameService) processFlagFalls(ctx context.Context) {
	now := time.Now().UnixMilli()
	ids, err := s.bus.Rdb().ZRangeByScore(ctx, clockFallScheduleZ, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now, 10), Count: 32,
	}).Result()
	if err != nil {
		slog.Warn("clock sweeper read failed", "error", err)
		return
	}
	for _, id := range ids {
		s.flagGame(ctx, id)
	}
}

// flagGame finalizes a single game whose clock ran out. Takes the
// per-game lock so it never races a concurrent move handler; if the
// game has already ended for any reason (resign, checkmate, etc.),
// just clean up the clock and return.
func (s *GameService) flagGame(ctx context.Context, gameID string) {
	lock, err := acquireGameLock(ctx, s.bus.Rdb(), gameID, gameLockTTL)
	if err != nil {
		slog.Warn("flagGame lock failed", "game_id", gameID, "error", err)
		return
	}
	if lock == nil {
		// Someone else is mid-mutation; they'll either finish the game
		// themselves (we'll see clean state on the next sweep) or clear
		// the schedule entry by calling deleteClock.
		return
	}
	defer lock.release(context.Background())

	rec, err := s.getGameCached(ctx, gameID)
	if err != nil || rec == nil {
		// Game is gone (or never existed); drop the schedule entry.
		deleteClock(ctx, s.bus.Rdb(), gameID)
		return
	}
	if rec.Status != "" && rec.Status != "ongoing" {
		// Already finished — no-op the timeout but still clear clock.
		deleteClock(ctx, s.bus.Rdb(), gameID)
		return
	}
	c, err := loadClock(ctx, s.bus.Rdb(), gameID)
	if err != nil || c == nil {
		// Clock vanished but schedule entry survived — clean up.
		unscheduleFlag(ctx, s.bus.Rdb(), gameID)
		return
	}
	now := time.Now().UnixMilli()
	if c.Mover == "" || c.TurnStartedMS == 0 {
		// Idle clock somehow scheduled — shouldn't happen, but be safe.
		unscheduleFlag(ctx, s.bus.Rdb(), gameID)
		return
	}
	elapsed := now - c.TurnStartedMS
	remaining := c.WhiteMS
	if c.Mover == "b" {
		remaining = c.BlackMS
	}
	if elapsed < remaining {
		// Sweeper fired early (clock-drift edge case). Reschedule.
		c.scheduleFlag(ctx, s.bus.Rdb())
		return
	}
	// Flag fell. Mover loses on time.
	loser := c.Mover
	if loser == "w" {
		c.WhiteMS = 0
		rec.Result = "0-1"
	} else {
		c.BlackMS = 0
		rec.Result = "1-0"
	}
	c.TurnStartedMS = 0
	c.Mover = ""
	rec.Status = "timeout"
	rec.UpdatedAt = time.Now()
	if err := s.saveGameCached(ctx, rec); err != nil {
		slog.Error("flagGame save failed", "game_id", gameID, "error", err)
		return
	}
	_ = c.save(ctx, s.bus.Rdb())
	deleteClock(ctx, s.bus.Rdb(), gameID)

	// Broadcast so both clients flip to "Time out" without polling.
	snap := s.snapshotFromRecord(ctx, rec)
	snapPayload, _ := json.Marshal(snap)
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtStateUpdated, GameID: rec.ID, Payload: snapPayload,
	})
	_, _ = s.bus.EmitEvent(ctx, eventbus.Event{
		Type: eventbus.EvtGameFinished, GameID: rec.ID, Payload: snapPayload,
	})
	slog.Info("game flagged on time", "game_id", gameID, "loser", loser)
}
