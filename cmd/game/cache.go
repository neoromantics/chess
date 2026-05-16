package main

// Redis-backed hot cache for the games table.
//
// Why: every /api/state and every read-modify-write inside
// withLockedMutation called s.db.GetGame which round-tripped to
// Postgres (~5ms typical). At 1000 concurrent games each producing
// ~3 reads/sec (state poll + move + WS reconnect retry), that's
// already ~3000 PG SELECTs/sec on hot paths — and the connection
// pool is what tips over first under load. PGBouncer fixed the
// connection ceiling; this commit fixes the latency floor and load
// floor by keeping the working set in Redis.
//
// Storage: one Redis STRING per game holding the JSON-serialized
// GameRecord. We tried a hash with one field per column; that
// burned a per-field encode/decode cycle on every read and made
// schema changes painful (every new field needed a marshal helper).
// JSON is one round-trip in each direction and reuses the same
// struct definition the DB layer already maintains.
//
// Consistency: every writer takes the per-game lock (cmd/game/lock.go)
// before reading the row. Because reads and writes both go through
// this cache and writes are write-through (Redis + PG together,
// inside the lock), there is no cache-coherence problem to solve.
// Reads outside the lock can see stale values for at most one
// in-flight writer's window — which is exactly the same staleness
// you'd get from any read-modify-write race anyway, and the lock
// prevents two writers from both succeeding on stale data.
//
// Failure modes:
//   - Redis down → reads fall through to PG; writes still hit PG.
//     The cache-write best-effort log line surfaces the outage but
//     doesn't block the request.
//   - Redis evicts the key (LRU under memory pressure) → next read
//     repopulates from PG. Cold-start latency, no incorrectness.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/neoromantics/chess/pkg/db"
	"github.com/redis/go-redis/v9"
)

// gameCacheTTL is conservative — most games are short-lived. We're
// happy to repopulate from PG when a row goes cold.
const gameCacheTTL = 1 * time.Hour

// gameCacheKey returns the Redis key for a game row.
func gameCacheKey(id string) string { return "game:state:" + id }

// getGameCached is the read path: try Redis first, fall back to PG on
// miss or Redis error, repopulate the cache on PG hit. Always returns
// a value safe to mutate — never the in-memory cached object.
func (s *GameService) getGameCached(ctx context.Context, id string) (*db.GameRecord, error) {
	if rec, ok, err := s.tryReadCache(ctx, id); ok && err == nil {
		return rec, nil
	} else if err != nil {
		slog.Warn("game cache read error; falling through to PG", "game_id", id, "error", err)
	}

	rec, err := s.db.GetGame(id)
	if err != nil {
		return nil, err
	}
	// Best effort: warming the cache failing must not fail the read.
	if cacheErr := s.writeCache(ctx, rec); cacheErr != nil {
		slog.Warn("game cache warm failed", "game_id", id, "error", cacheErr)
	}
	return rec, nil
}

// saveGameCached is the write path: write PG first (durable), then
// write Redis. Order matters — PG is canonical, Redis is acceleration.
// If PG fails the write fails; if Redis fails we log and continue
// (the next read will repopulate from PG).
func (s *GameService) saveGameCached(ctx context.Context, rec *db.GameRecord) error {
	if err := s.db.SaveGame(rec); err != nil {
		return err
	}
	if cacheErr := s.writeCache(ctx, rec); cacheErr != nil {
		slog.Warn("game cache write failed", "game_id", rec.ID, "error", cacheErr)
	}
	return nil
}

// invalidateGameCache deletes the cached row. Used by paths that
// invalidate without writing fresh state (currently just delete).
func (s *GameService) invalidateGameCache(ctx context.Context, id string) {
	if err := s.bus.Rdb().Del(ctx, gameCacheKey(id)).Err(); err != nil {
		slog.Warn("game cache invalidate failed", "game_id", id, "error", err)
	}
}

// tryReadCache attempts a single GET. (false, nil) means clean miss
// — caller should fall through to PG. (rec, true, nil) is a hit.
// Errors are returned but treated as miss + log by the caller.
func (s *GameService) tryReadCache(ctx context.Context, id string) (*db.GameRecord, bool, error) {
	data, err := s.bus.Rdb().Get(ctx, gameCacheKey(id)).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	var rec db.GameRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		// Cached payload is corrupt (schema drift, partial write,
		// truncation); treat as miss and let PG repopulate.
		return nil, false, err
	}
	return &rec, true, nil
}

// writeCache stores the GameRecord as a single JSON blob with TTL.
func (s *GameService) writeCache(ctx context.Context, rec *db.GameRecord) error {
	if rec == nil || rec.ID == "" {
		return errors.New("nil record")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.bus.Rdb().Set(ctx, gameCacheKey(rec.ID), data, gameCacheTTL).Err()
}
