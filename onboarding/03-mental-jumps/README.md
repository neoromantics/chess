# 3. The mental jumps from CS to production

This is the part you can't get from a textbook. Each file is something that bit us in production at least once. They're roughly ordered by how foundational the concept is — start at 01 and go through.

1. [`01-multi-replica.md`](01-multi-replica.md) — "works locally" is not enough
2. [`02-distributed-locks.md`](02-distributed-locks.md) — Redis SETNX as cross-pod mutex
3. [`03-leader-election.md`](03-leader-election.md) — running goroutines exactly-once
4. [`04-streams-vs-pubsub.md`](04-streams-vs-pubsub.md) — picking the right Redis primitive
5. [`05-trust-boundary.md`](05-trust-boundary.md) — JWT, `X-User-ID`, and why downstream services never re-validate
6. [`06-passwords.md`](06-passwords.md) — why bcrypt, not SHA-256
7. [`07-websocket-hijack.md`](07-websocket-hijack.md) — the middleware-composition gotcha that silently kills WebSockets
8. [`08-cswsh.md`](08-cswsh.md) — same-origin and Cross-Site WebSocket Hijacking
9. [`09-metric-cardinality.md`](09-metric-cardinality.md) — why labels matter
10. [`10-schema-migrations.md`](10-schema-migrations.md) — schema as code, not commands
11. [`11-ops-logging.md`](11-ops-logging.md) — why we read kubectl logs, not Sentry or Datadog

Where these overlap with the public reference docs ([`../../docs/`](../../docs/)), this layer keeps the teaching narrative and links out for the canonical form.

---

← [`../README.md`](../README.md)
