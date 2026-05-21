# 9.3 The structure IS the index

This is the framing shift that distinguishes Redis from "SQL but faster," and it's the one most worth internalizing.

In Postgres, the table and an index are separate physical structures. Data lives in the heap; an index is one access path. The query planner picks an index at query time. You can add new indexes after the fact without changing any write code. A query the planner can't index just falls back to sequential scan — slower, but it still answers.

In Redis, **the structure IS the data IS the index.** A ZSET keyed by rating answers a whole family of related queries off the same sort key — top-N, range, rank, score lookup — all O(log N) or O(1). What it cannot answer is anything about a *different dimension*: querying the same members by registration date is *impossible from this ZSET*, because that information isn't in it.

So in Redis you pre-build a structure *per access dimension*, and to support a new access pattern you add a new structure and a new write path that keeps it in sync. The matchmaker is the canonical example: `mm:queue:{tc}` is a ZSET indexed by rating (so `ZRANGEBYSCORE` does rating-window pairing in O(log N + M)), and `mm:joined:{tc}` is a Hash indexed by user (so we can compute per-user wait time and trigger the 10s engine fallback). Two structures, two access patterns, both updated together on every join/leave:

```go
// cmd/game/matchmaker.go — every join writes both
s.bus.Rdb().ZAdd(ctx, queueKey(tc), redis.Z{Score: float64(rating), Member: uidStr})
s.bus.Rdb().HSet(ctx, joinedKey(tc), uid, time.Now().Unix())
```

The costs: memory duplication, write-path complexity, no foreign keys, no `ON DELETE CASCADE` — application code owns consistency. The rewards: predictable latency (no query-planner surprises), atomic operations against the structure (no row locks, no MVCC), and microsecond reads. You take this trade for **known, hot, repeated** access patterns. Ad-hoc queries you didn't anticipate go to Postgres.

A related framing trap: Redis isn't only "SQL questions made faster." A large slice of *why we have Redis at all* is things SQL is bad at — distributed locks, leader election, Pub/Sub fan-out, durable inter-service queues, TTL on everything. Those aren't SQL-shaped problems made fast; they're a different category of problem that happens to fit in a key-value server.

---

← [`02-redis-data-structures.md`](02-redis-data-structures.md) · Next: [`04-pubsub-relay.md`](04-pubsub-relay.md)
