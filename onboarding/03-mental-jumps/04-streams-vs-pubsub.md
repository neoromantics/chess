# 3.4 Redis Streams vs Pub/Sub vs plain key/value

Three different Redis features, three different jobs.

- **Plain GET/SET (and HSET hashes):** key-value cache. Fast lookup. Used for game state cache (`game:state:{id}`), clocks (`clock:{id}`), locks, sessions, rate-limiter buckets.
- **Pub/Sub:** *ephemeral* fan-out. Publisher sends, all current subscribers receive, nothing is stored. If you reconnect 1 second later, you missed it. We use this for the "live move arrived, push it to the browser" path — if a browser reconnects mid-game, it just re-fetches state via `GET /api/state`.
- **Streams + consumer groups:** *durable* queue with at-least-once delivery, replay, and acknowledgment. Producers append; consumers read; unacknowledged messages can be re-claimed by another consumer if the original one dies. This is what Kafka does. We use it for engine search dispatch (`engine:requests`, `engine:results`) and for cross-service command intent (`game:commands`).

**The rule:** ephemeral live updates → Pub/Sub. Anything where losing the message is unacceptable → Stream. Don't mix them. The wire contract spells out which channel each event flows over ([`../../pkg/wire/CONTRACT.md`](../../pkg/wire/CONTRACT.md)).

We learned this the hard way: `engine:results` started life as Pub/Sub. A game-service restart during a search dropped the engine's reply on the floor, and the SPA hung forever waiting for a move that had been computed and discarded. Promoted to a Stream in commit `fa76c2f`.

## Streams in one analogy

If Kafka means nothing to you: a stream is a **ticket rail in a busy kitchen**. Orders come in on paper tickets clipped to the rail. Cooks (workers in the same *consumer group*) each grab the next available ticket, make the dish, and pull their ticket off the rail when the plate goes out. Tickets stay on the rail until somebody pulls them off — even if every cook stepped out for a smoke break. If a cook drops dead halfway through a pizza, another cook can take their abandoned ticket (`XCLAIM`) and finish the order. Worst case the customer gets two pizzas instead of zero — that's "at-least-once." Compare to Pub/Sub, where the waiter just yells the order across the kitchen: heard right now or lost forever.

## The three primitives composed

Here is how the rule plays out in three concrete flows. Every flow ends with Pub/Sub for "tell the browser," but they differ in what carries the durable middle.

**Flow 1: a PvP move** (see [`../04-move-trace.md`](../04-move-trace.md) for the full trace).
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
- `engine:requests`, `engine:results` — **STREAMS**. Two reasons: work distribution across N workers (each search goes to exactly one consumer in the group), and durability across pod restarts (an in-flight search survives an engine-worker OOM via `XCLAIM`).
- `game.evt.{id}` — **PUB/SUB**, same as Flow 1.

**Flow 3: a game invite.**
```
INSERT INTO invites RETURNING id   ──Postgres──► durable truth
PUBLISH user.evt.7                 ──Pub/Sub──►  live ping (may be missed)
```
- **POSTGRES** for durability — invites must survive even if the recipient is offline for two weeks.
- **PUB/SUB** for the optional live ping. If they're offline, the message vanishes — fine, the SPA fetches pending invites from Postgres on next page load. *No Stream needed because Postgres already provides the durability.*

## The decision tree

When designing a new flow, walk these in order:

1. *Does this need to survive a pod restart?* No → Pub/Sub. Done. Yes → continue.
2. *Will the recipient re-fetch via REST if they miss the live alert?* Yes → Postgres + Pub/Sub. No → continue.
3. *Is this CPU-bound async work or cross-service command intent?* Yes → Stream + Pub/Sub at the end for the live UI nudge. No → step back; you almost certainly want one of the simpler options above.

The most common mistakes are using Stream where Pub/Sub would do (overkill — every message persisted, every consumer must ack) and using Pub/Sub where a Stream is needed (silent message loss during restarts, exactly the `engine:results` regression above). The decision tree catches both. **Streams earn their keep specifically when the cost of losing a message is higher than the cost of running it twice** — narrower than people think.

## Reference

- Canonical pattern doc: [`../../docs/architecture/redis-patterns.md`](../../docs/architecture/redis-patterns.md) (Streams vs Pub/Sub section + decision tree)
- Channel inventory: [`../../docs/architecture/wire.md`](../../docs/architecture/wire.md)

---

← [`03-leader-election.md`](03-leader-election.md) · Next: [`05-trust-boundary.md`](05-trust-boundary.md)
