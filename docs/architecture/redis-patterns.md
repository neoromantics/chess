# Redis patterns

Three different Redis features, three different jobs. All happen to live inside Redis, but they solve different problems and have different failure modes. The biggest mistake people make is treating them as variations of "Redis storage."

| Primitive | What it is | Persistence | Delivery | When to use |
|---|---|---|---|---|
| GET/SET, HSET | Networked key-value store | Persistent (with AOF/RDB) | Pull-based | State, cache, locks, counters |
| Pub/Sub | Broadcast channel | None (ephemeral) | At-most-once, to all subscribers | Live updates where stale = worthless |
| Streams + groups | Append-only durable log | Persistent | At-least-once, partitioned across group | Cross-service intent, work queues, durable events |

## The Streams-vs-HTTP rule

Redis Streams are used for **(1) CPU-asymmetric workloads** (engine search dispatch + result delivery on `engine:requests` / `engine:results`) and **(2) cross-service intent** (matchmaker pairing on `game:commands`). Everything else — single-game mutations, invites, profile changes — uses synchronous HTTP through the gateway. **Do not put a user-initiated chess action behind a Stream**; the SPA expects each button to round-trip a new `StateJSON`. See `cmd/game/handlers.go` for the pattern.

## Distributed locks (the per-game lock)

You learned about mutexes that protect a critical section inside one process. Across pods, you need a *distributed* lock — something all replicas can see and respect. The mental model is the same as a `sync.Mutex`, with two important twists: the lock lives in a *different* process (Redis) from the holder, and that other process has no idea when the holder dies.

### Why we need them

Say you make a move and immediately click "offer draw" on the next button. Two HTTP requests fly out within 30ms. With three game-service pods behind a load balancer, the first lands on pod A, the second on pod B. Every game-mutating handler follows the same three-step pattern: read game state → mutate in memory → write back. Without a lock:

```
T+0ms   Pod A: READ → {5 moves, no draw}
T+2ms   Pod B: READ → {5 moves, no draw}      ← reads BEFORE A's write
T+5ms   Pod A: MUTATE → {6 moves, no draw}
T+7ms   Pod B: MUTATE → {5 moves, draw=true}  ← built on stale read
T+10ms  Pod A: WRITE → DB now has {6 moves}
T+12ms  Pod B: WRITE → DB now has {5 moves, draw=true}  ← overwrites move 6
```

Your move just disappeared. Both pods returned HTTP 200; neither knew. This is a **read-modify-write race**, and it bites any system with concurrent writes to shared state.

### The pattern

We use the standard Redis idiom: `SET key value NX PX <ms>` to acquire (atomic "set if not exists" with a TTL so a crashed holder eventually releases), and a tiny Lua script to release only if the token still matches yours.

```go
// every code path that reads-then-writes a game's row holds this lock
unlock, err := acquireGameLock(ctx, redis, gameID, 5*time.Second)
if err != nil { return err }
defer unlock()
// ... read game, mutate, write back ...
```

Mapping back to a `sync.Mutex`: the **key** (`game:lock:42`) is the mutex — existence means held, absence means free. The **value** is a 128-bit random token that uniquely identifies *this specific acquire*. One key per lockable thing — `game:lock:42` and `game:lock:43` are completely independent.

### Why the TTL

In an in-process mutex you get cleanup for free: if the holder thread crashes, the OS reclaims the process's memory and the mutex evaporates. In a distributed lock, the lock lives in Redis (a different process), so when the holder pod dies, the key *just sits there forever* — Redis has no idea the holder is dead. Without a TTL, one OOM-kill on one pod = a forever-stuck game. The TTL is the substitute for OS process cleanup.

### Why the Lua release isn't just `DEL`

The TTL fixes one problem and creates another. If the holder hangs for 6 seconds while the TTL is 5, the TTL fires; another caller acquires the freshly-vacated lock with a new random token. The original holder finally wakes up and calls `DEL` — and that `DEL` deletes the *successor's* lock, not its own. Now two writers think they hold the lock and corruption is back.

Concretely (using `"alice"` and `"bob"` as the random tokens for readability):

```
T+0    Pod A: SET game:lock:42 alice EX 5     Redis: {game:lock:42 = alice}
T+5s   TTL fires → key auto-deleted           Redis: {}
T+5.1s Pod B: SET game:lock:42 bob EX 5       Redis: {game:lock:42 = bob}
T+5.7s Pod A finishes, runs release:
       Naive "DEL":                            → deletes bob's lock ❌
       Lua "if value == alice then DEL":       → "bob" != "alice", skip ✓
```

The Lua script is the **compare-and-swap** pattern from CPU atomics (think `CMPXCHG`), lifted to a network service: `GET → COMPARE → DELETE` in one indivisible operation. It has to run inside Redis (as a script, not as separate GET/DEL calls from Go) because Redis is single-threaded for command execution — between the script's GET and DEL, nothing else can interleave.

The token must be **per-acquire random**, not per-pod. Otherwise two acquires from the same pod would have the same token, and Pod A's release after a TTL rollover could accidentally match Pod A's own *later* lock and delete it.

See `cmd/game/lock.go`. The paper to read for depth is Martin Kleppmann's critique of Redlock and the responses to it. For our scale (one Redis, low contention), `SETNX` is fine.

## Leader election

Some jobs should run *exactly once* in the cluster, not once per pod. Examples here: the matchmaker pairing loop (otherwise two pods pair the same player twice), the invite expiry sweeper, the clock flag-fall sweeper. These aren't request-driven; they're *time-driven* goroutines that tick on a ticker. Naive startup would run the tick on every replica.

We use the same `SETNX`+TTL primitive as the per-game lock, in a totally different shape. Every pod runs an acquisition loop on boot:

```go
// cmd/game/matchmaker.go (simplified)
func (s *GameService) runPairingLoop(ctx context.Context) {
    hostname, _ := os.Hostname()
    for ctx.Err() == nil {
        got, _ := s.bus.Rdb().SetNX(ctx, "mm:leader", hostname, 15*time.Second).Result()
        if !got {
            time.Sleep(5 * time.Second)   // lost the race, spin
            continue
        }
        s.holdAndPair(ctx, hostname)      // won — run the loop until lease lost
    }
}
```

`SetNX` is atomic: only one pod's call returns `true`. The losers sleep and retry. The winner enters `holdAndPair`, which is the actual matchmaking ticker — and the place where the mental model gets interesting:

```go
func (s *GameService) holdAndPair(ctx context.Context, hostname string) {
    pair  := time.NewTicker(2 * time.Second)
    renew := time.NewTicker(5 * time.Second)
    defer pair.Stop()
    defer renew.Stop()
    for {
        select {
        case <-ctx.Done():
            return                                    // shutdown
        case <-renew.C:
            if v, _ := s.bus.Rdb().Get(ctx, "mm:leader").Result(); v != hostname {
                return                                // we lost the lease — bail
            }
            s.bus.Rdb().Expire(ctx, "mm:leader", 15*time.Second)
        case <-pair.C:
            for _, tc := range supportedTCs {
                s.tryPair(ctx, tc)                    // ← the actual work
            }
        }
    }
}
```

**Where the mental model gets weird.** A `sync.Mutex`'s critical section is a few lines of code you hold the lock for — short, scoped, "acquire → work → release." Here the "critical section" is the *entire body of `holdAndPair`* — an open-ended `for { select { … } }` loop that runs for hours. You can't release-at-end because there is no end. Instead the leader keeps proving it's alive by refreshing the TTL every 5s, and any pod that goes silent loses the lease automatically when the TTL fires.

The two patterns share an implementation but solve different shapes of problem:

| | Per-game lock (`game:lock:42`) | Leader election (`mm:leader`) |
|---|---|---|
| Competitors are... | HTTP request goroutines | Pods |
| Held for | ~ms (one request) | Hours (pod lifetime) |
| Acquire pattern | Fail-fast (409 if busy) | Retry loop with sleep |
| Release | Explicit Lua DEL at end of request | Just stop refreshing; TTL expires on death |
| TTL purpose | Safety net for stuck handlers | Failover detection window |

This is a primitive form of leader election. Real systems use Raft (etcd, Consul, Postgres replicas) — provable single-leader-at-a-time under network partitions, plus a consistent log of operations. For our scale, Redis-with-TTL is fine: the worst-case dual-leader window is "matchmaking creates a duplicate pairing for a few hundred ms," and the per-game lock above catches the worst of it anyway. Raft buys you correctness when dual-leader is *unrecoverable* (two Postgres replicas accepting writes, two systems both authorizing a payment). That's not our cost function.

## Streams vs Pub/Sub

### Plain GET/SET (and HSET hashes)

This is what most people think "Redis" means: a giant networked hashmap. Persistent until deleted or expired (and survives Redis restart if AOF/RDB is on).

```
SET game:state:42 "{fen:...,moves:[...],clocks:[...]}"
HSET game:state:42 fen "rnbqkbnr/..." white_clock 180
HINCRBY game:state:42 white_clock -2     # atomic field-level update
```

The textbook analogy: a thread-safe `map[string][]byte` that lives on another machine.

**In our codebase:** `game:state:{id}` (game row cache), `clock:{id}` (clock state, very frequent updates), `game:lock:{id}` and `mm:leader` (the locks above), sessions, rate-limiter buckets.

### Pub/Sub

Not storage. An ephemeral fan-out channel. A publisher sends a message to a channel name. Redis fans it out to every currently-connected subscriber in real time. **Then it forgets.** If no one is subscribed when you publish, the message goes to `/dev/null`.

Textbook analogy: a Go channel. Or a radio broadcast — you're tuned in or you're not.

**In our codebase:**
- `game.evt.{id}` — "a move happened in game 42, push it to all WebSocket clients watching this game."
- `user.evt.{id}` — "invite accepted, push it to all of user 7's open browser tabs."

Why this is fine for these use cases: if a browser misses a move event during a brief WebSocket reconnect, no big deal — the SPA calls `GET /api/state` and re-syncs.

### Streams + consumer groups

The heaviest primitive. An append-only log with consumer groups, acknowledgments, and replay.

Think of it as a ticket rail in a busy kitchen. Orders come in on paper tickets clipped to the rail. Cooks (workers in the same consumer group) each grab the next available ticket, make the dish, and pull their ticket off the rail when the plate goes out. Tickets stay on the rail until somebody pulls them off — even if every cook stepped out. If a cook drops dead halfway through a pizza, another cook can take their abandoned ticket and finish the order. Worst case the customer gets two pizzas — that's "at-least-once."

Key properties:
- **Persistent.** Messages live in the stream until you trim them. You can replay from any point.
- **Consumer groups partition work.** Multiple workers join group `engine-workers`; each message goes to *exactly one* worker.
- **At-least-once delivery.** After reading, the worker must acknowledge to mark it done. If the worker crashes before acking, another worker can reclaim it and retry.

**In our codebase:** `engine:requests`, `engine:results`, `game:commands`.

### The rule: ephemeral → Pub/Sub, can't-lose → Stream

We learned this the hard way: `engine:results` started life as Pub/Sub. A game-service restart during a search dropped the engine's reply on the floor, and the SPA hung forever waiting for a move that had been computed and discarded. Promoted to a Stream in commit `fa76c2f`.

### How the three primitives compose

Three concrete flows. Every flow ends with Pub/Sub for "tell the browser," but they differ in what carries the durable middle.

**Flow 1: a PvP move** (the canonical case).
- `game:lock:{id}`, `game:state:{id}` — **KEY-VALUE**. State + lock; nothing to hand off across services.
- `game.evt.{id}` — **PUB/SUB**. Reconnecting clients re-sync via `GET /api/state`.
- *No Stream.* The whole flow is one sync HTTP request completing in ~10ms.

**Flow 2: an engine hint.**
```
game-service                       engine-worker pod (1 of N)        game-service
  XADD engine:requests  ──Stream──► XREADGROUP, search 5s,
                                    XADD engine:results   ──Stream──► XREADGROUP, apply,
                                                                       PUBLISH game.evt → browser
```
- `engine:requests`, `engine:results` — **STREAMS**. Two reasons: work distribution across N workers (each search goes to exactly one consumer in the group), and durability across pod restarts.
- `game.evt.{id}` — **PUB/SUB**, same as Flow 1.

**Flow 3: a game invite.**
```
INSERT INTO invites RETURNING id   ──Postgres──► durable truth
PUBLISH user.evt.7                 ──Pub/Sub──►  live ping (may be missed)
```
- **POSTGRES** for durability — invites must survive even if the recipient is offline for two weeks.
- **PUB/SUB** for the optional live ping. If they're offline, the message vanishes — fine, the SPA fetches pending invites from Postgres on next page load. No Stream needed because Postgres already provides the durability.

### Decision tree for new flows

1. *Does this need to survive a pod restart?* No → Pub/Sub. Yes → continue.
2. *Will the recipient re-fetch via REST if they miss the live alert?* Yes → Postgres + Pub/Sub. No → continue.
3. *Is this CPU-bound async work or cross-service command intent?* Yes → Stream + Pub/Sub at the end for the live UI nudge. No → step back; you almost certainly want one of the simpler options.

Most common mistakes:
- Stream where Pub/Sub would do (overkill — every message persisted, every consumer must ack).
- Pub/Sub where a Stream is needed (silent message loss during restarts, exactly the `engine:results` regression above).

**Streams earn their keep specifically when the cost of losing a message is higher than the cost of running it twice** — narrower than people think.
