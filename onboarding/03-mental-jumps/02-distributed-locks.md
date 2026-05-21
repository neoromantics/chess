# 3.2 Distributed locks (Redis `SETNX`)

You learned about mutexes that protect a critical section inside one process. Across pods, you need a *distributed* lock — something all replicas can see and respect. The mental model is the same as a `sync.Mutex`, with two important twists: the lock lives in a *different* process (Redis) from the holder, and that other process has no idea when the holder dies.

## Why we need them

Say you make a move and immediately click "offer draw" on the next button. Two HTTP requests fly out within 30ms. With three game-service pods behind a load balancer, the first lands on pod A, the second on pod B. Every game-mutating handler follows the same three-step pattern: read game state → mutate in memory → write back. Without a lock:

```
T+0ms   Pod A: READ → {5 moves, no draw}
T+2ms   Pod B: READ → {5 moves, no draw}      ← reads BEFORE A's write
T+5ms   Pod A: MUTATE → {6 moves, no draw}
T+7ms   Pod B: MUTATE → {5 moves, draw=true}  ← built on stale read
T+10ms  Pod A: WRITE → DB now has {6 moves}
T+12ms  Pod B: WRITE → DB now has {5 moves, draw=true}  ← overwrites move 6
```

Your move just disappeared. Both pods returned HTTP 200; neither knew. This is a **read-modify-write race**, and it bites any system with concurrent writes to shared state — databases, files, registers, distributed game state. The fix: serialize the reads and writes so one mutation always sees the other's writes.

## The pattern

We use the standard Redis idiom: `SET key value NX PX <ms>` to acquire (atomic "set if not exists" with a TTL so a crashed holder eventually releases), and a tiny Lua script to release only if the token still matches yours.

```go
// every code path that reads-then-writes a game's row holds this lock
unlock, err := acquireGameLock(ctx, redis, gameID, 5*time.Second)
if err != nil { return err }
defer unlock()
// ... read game, mutate, write back ...
```

Mapping back to a `sync.Mutex`: the **key** (`game:lock:42`) is the mutex — existence means held, absence means free. The **value** is a 128-bit random token that uniquely identifies *this specific acquire*. One key per lockable thing — `game:lock:42` and `game:lock:43` are completely independent.

## Why the TTL

In an in-process mutex you get cleanup for free: if the holder thread crashes, the OS reclaims the process's memory and the mutex evaporates. In a distributed lock, the lock lives in Redis (a different process), so when the holder pod dies, the key *just sits there forever* — Redis has no idea the holder is dead. Without a TTL, one OOM-kill on one pod = a forever-stuck game. The TTL is the substitute for OS process cleanup: "if nobody refreshes this within X seconds, assume the holder is dead and free the lock automatically."

## Why the Lua release isn't just `DEL`

The TTL fixes one problem and creates another. Imagine the holder hangs for 6 seconds while the TTL is 5. The TTL fires; another caller acquires the freshly-vacated lock with a new random token. The original holder finally wakes up and calls `DEL` — and that `DEL` deletes the *successor's* lock, not its own. Now two writers hold the "lock" in their minds and corruption is back.

Concretely (using `"alice"` and `"bob"` as the random tokens for readability):

```
T+0    Pod A: SET game:lock:42 alice EX 5     Redis: {game:lock:42 = alice}
T+5s   TTL fires → key auto-deleted           Redis: {}
T+5.1s Pod B: SET game:lock:42 bob EX 5       Redis: {game:lock:42 = bob}
T+5.7s Pod A finishes, runs release:
       Naive "DEL":                            → deletes bob's lock ❌
       Lua "if value == alice then DEL":       → "bob" != "alice", skip ✓
```

The Lua script is the **compare-and-swap** pattern from CPU atomics (think `CMPXCHG`), lifted to a network service: `GET → COMPARE → DELETE` in one indivisible operation. It has to run inside Redis (as a script, not as separate GET/DEL calls from Go) because Redis is single-threaded for command execution — between the script's GET and DEL, nothing else can interleave. Two separate round trips from your Go code would let another client sneak in between.

The token must be **per-acquire random**, not per-pod (e.g. hostname). Otherwise two acquires from the same pod would have the same token, and Pod A's release after a TTL rollover could accidentally match Pod A's own *later* lock and delete it.

## API note

When another writer holds the lock, `acquireGameLock` returns immediately rather than blocking. The handler translates that to HTTP 409 Conflict and the SPA retries 50ms later. Block-and-wait would tie up a goroutine for unbounded time and the user's browser would time out anyway — fail-fast and let the client decide.

## Reference

- Canonical pattern doc: [`../../docs/architecture/redis-patterns.md`](../../docs/architecture/redis-patterns.md) (Distributed locks section)
- Code: `cmd/game/lock.go`
- Background reading: Martin Kleppmann's critique of Redlock and the responses to it. For our scale (one Redis, low contention), `SETNX` is fine.

---

← [`README.md`](README.md) · Next: [`03-leader-election.md`](03-leader-election.md)
