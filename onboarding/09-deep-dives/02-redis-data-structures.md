# 9.2 Redis data structures: the full menu

"In-memory hashmap" undersells it. Redis is really a *library of hand-picked data structures*, each with atomic operations implemented in C — that's most of why people reach for it over a SQL database.

| Type | Shape | Where it lands |
|---|---|---|
| **String** | `key → bytes` | Counters (`INCR`), locks (`SET NX PX`), JSON blob caches |
| **Hash** | `key → {field: value, …}` | A record where fields update independently. We use it for `clock:{id}` so a clock tick doesn't rewrite the whole blob |
| **List** | doubly-linked, push/pop both ends, blocking variants (`BLPOP`) | Queues/stacks. We use Streams instead |
| **Set** | unordered unique members + set algebra (`SUNION`, `SINTER`, `SDIFF`) | Membership tests across populations |
| **Sorted Set (ZSet)** | unique members + `float64` scores, sorted by score, range queries are O(log N + M) | Priority queues, leaderboards, time-window indexes. We use it for matchmaking (by rating) and flag-fall (by deadline) |
| **Stream** | append-only log + offsets + consumer groups; unacked messages can be `XCLAIM`ed by another consumer | Kafka-lite. Engine work, game-commands |
| **Pub/Sub** | ephemeral fan-out channels (not stored) | Live WebSocket updates |
| **HyperLogLog** | probabilistic cardinality, ~12 KB regardless of input size, ~0.81% error | "Unique daily active users" without storing user IDs |
| **Bitmap / Bitfield** | bit-addressable string | "Has user N done X today?" at one bit per user |
| **Geo** | lat/lng members + radius queries (built on ZSets via geohashes) | "Players within 50 km" |

---

← [`01-what-lives-in-redis.md`](01-what-lives-in-redis.md) · Next: [`03-structure-is-index.md`](03-structure-is-index.md)
