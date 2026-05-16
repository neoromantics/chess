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

// tryReadCache attempts a single HGETALL. (false, nil) means clean miss
// — caller should fall through to PG. (rec, true, nil) is a hit.
// Errors are returned but treated as miss + log by the caller.
func (s *GameService) tryReadCache(ctx context.Context, id string) (*db.GameRecord, bool, error) {
	res, err := s.bus.Rdb().HGetAll(ctx, gameCacheKey(id)).Result()
	if err != nil {
		return nil, false, err
	}
	if len(res) == 0 {
		return nil, false, nil
	}
	rec, err := unmarshalGameHash(res)
	if err != nil {
		// Cached payload is corrupt (schema drift, partial write...);
		// treat as miss and let PG repopulate the entry.
		return nil, false, err
	}
	return rec, true, nil
}

// writeCache HSETs every GameRecord field, refreshes TTL.
func (s *GameService) writeCache(ctx context.Context, rec *db.GameRecord) error {
	if rec == nil || rec.ID == "" {
		return errors.New("nil record")
	}
	fields := marshalGameHash(rec)
	pipe := s.bus.Rdb().TxPipeline()
	pipe.Del(ctx, gameCacheKey(rec.ID)) // wipe stale fields a previous schema may have written
	pipe.HSet(ctx, gameCacheKey(rec.ID), fields)
	pipe.Expire(ctx, gameCacheKey(rec.ID), gameCacheTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// marshalGameHash flattens a GameRecord into Redis hash fields. Each
// field is stored as a string; pointer fields use sentinel keys so we
// can disambiguate "nil" from "0". Versioning prefix `v1:` lets us
// add fields later without breaking old cached payloads (we'd just
// mismatch the prefix and treat as miss).
func marshalGameHash(rec *db.GameRecord) map[string]interface{} {
	m := map[string]interface{}{
		"v":                "1",
		"id":               rec.ID,
		"fen":              rec.FEN,
		"start_fen":        rec.StartFEN,
		"history":          rec.History,
		"history_san":      rec.HistorySAN,
		"engine_white":     boolToStr(rec.EngineWhite),
		"engine_black":     boolToStr(rec.EngineBlack),
		"white_think_time": intToStr(rec.WhiteThinkTime),
		"black_think_time": intToStr(rec.BlackThinkTime),
		"time_control":     rec.TimeControl,
		"rated":            boolToStr(rec.Rated),
		"status":           rec.Status,
		"result":           rec.Result,
		"created_at":       rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":       rec.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if rec.WhiteUserID != nil {
		m["white_user_id"] = intToStr(int(*rec.WhiteUserID))
	}
	if rec.BlackUserID != nil {
		m["black_user_id"] = intToStr(int(*rec.BlackUserID))
	}
	return m
}

func unmarshalGameHash(m map[string]string) (*db.GameRecord, error) {
	if m["v"] != "1" {
		return nil, errors.New("unknown cache version")
	}
	rec := &db.GameRecord{
		ID:             m["id"],
		FEN:            m["fen"],
		StartFEN:       m["start_fen"],
		History:        m["history"],
		HistorySAN:     m["history_san"],
		EngineWhite:    m["engine_white"] == "1",
		EngineBlack:    m["engine_black"] == "1",
		WhiteThinkTime: strToInt(m["white_think_time"]),
		BlackThinkTime: strToInt(m["black_think_time"]),
		TimeControl:    m["time_control"],
		Rated:          m["rated"] == "1",
		Status:         m["status"],
		Result:         m["result"],
	}
	if v, ok := m["white_user_id"]; ok && v != "" {
		i := int64(strToInt(v))
		rec.WhiteUserID = &i
	}
	if v, ok := m["black_user_id"]; ok && v != "" {
		i := int64(strToInt(v))
		rec.BlackUserID = &i
	}
	if t, err := time.Parse(time.RFC3339Nano, m["created_at"]); err == nil {
		rec.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, m["updated_at"]); err == nil {
		rec.UpdatedAt = t
	}
	return rec, nil
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
func intToStr(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
func strToInt(s string) int {
	var i int
	_ = json.Unmarshal([]byte(s), &i)
	return i
}

// silence linter when redis is imported only via s.bus.Rdb() in the
// helpers above — we don't construct a Client directly here.
var _ = redis.Nil
