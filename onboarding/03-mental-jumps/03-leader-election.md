# 3.3 Leader election

Some jobs should run *exactly once* in the cluster, not once per pod. Examples here: the matchmaker pairing loop (otherwise two pods pair the same player twice), the invite expiry sweeper, the clock flag-fall sweeper. These aren't request-driven; they're *time-driven* goroutines that tick on a ticker. With three game-service replicas, naive startup would run the tick three times in parallel and create duplicate pairings / double-cancel invites / triple-award flag-falls.

We do leader election with the same `SETNX`+TTL primitive as the per-game lock, used in a totally different shape. Every pod runs an acquisition loop on boot:

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

**Where the mental model gets weird.** A `sync.Mutex`'s critical section is a few lines of code you hold the lock for — short, scoped, "acquire → work → release." Here the "critical section" is the *entire body of `holdAndPair`* — an open-ended `for { select { … } }` loop that runs for hours. You can't release-at-end because there is no end. Instead the leader keeps proving it's alive by refreshing the TTL every 5s (`Expire`), and any pod that goes silent loses the lease automatically when the TTL fires.

So the two patterns share an implementation but solve different shapes of problem:

| Per-game lock (`game:lock:42`) | Leader election (`mm:leader`) |
|---|---|
| Competitors are *HTTP request goroutines* | Competitors are *pods* |
| Held for ~ms (one request) | Held for hours (pod lifetime) |
| Acquire pattern: fail-fast (409 if busy) | Retry loop with sleep |
| Release: explicit Lua DEL at end of request | Just stop refreshing; TTL expires on death |
| TTL purpose: safety net for stuck handlers | TTL purpose: failover detection window |

This is a primitive form of leader election. Real systems use Raft (etcd, Consul, Postgres replicas) — provable single-leader-at-a-time under network partitions, plus a consistent log of operations the new leader picks up where the old one left off. For our scale, Redis-with-TTL is fine: the worst-case dual-leader window is "matchmaking creates a duplicate pairing for a few hundred ms," and the per-game lock from [§3.2](02-distributed-locks.md) catches the worst of it anyway. Raft buys you correctness in the cases where dual-leader is *unrecoverable* (two Postgres replicas accepting writes, two systems both authorizing a payment). That's not our cost function.

Reference: [`../../docs/architecture/redis-patterns.md`](../../docs/architecture/redis-patterns.md) (Leader election section).

---

← [`02-distributed-locks.md`](02-distributed-locks.md) · Next: [`04-streams-vs-pubsub.md`](04-streams-vs-pubsub.md)
