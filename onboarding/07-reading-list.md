# 7. The reading list

Read in this order:

1. **[`../CLAUDE.md`](../CLAUDE.md)** at the repo root. Slim always-loaded index for Claude Code. Treat it as authoritative for the critical-invariants subset.
2. **[`../docs/invariants.md`](../docs/invariants.md)**. The full normative list.
3. **[`../docs/architecture/`](../docs/architecture/)**. Overview, Redis patterns, wire surface.
4. **[`../pkg/wire/CONTRACT.md`](../pkg/wire/CONTRACT.md)**. The wire protocol. Every endpoint, every event, every payload shape. The frontend and backend both reference it.
5. **[`../docs/roadmap.md`](../docs/roadmap.md)**. Shipped, queued, deferred. Look here before proposing a feature — it might already be on the list with a reason it hasn't shipped yet.
6. **[`../README.md`](../README.md)**. The public-facing summary.
7. **`../infra/deploy.yaml`**. The whole Kubernetes deployment in one file. Reading this teaches you what env vars each service needs, how the HPAs are configured, where secrets come from.

External material that will pay off here:

- *Designing Data-Intensive Applications* (Kleppmann). The book to read on distributed systems if you only read one. Especially chapters 5 (replication), 7 (transactions), 9 (consistency and consensus).
- The Redis docs on Streams + consumer groups (the official docs are unusually good).
- Go's `net/http` source — specifically the `ResponseWriter`, `Hijacker`, `Flusher` interfaces. Read `httptest.ResponseRecorder` for a reference wrapper.
- Glicko-2 paper (Glickman). Short, readable, and the comments in `pkg/rating/glicko2.go` reference its equation numbers.

---

← [`06-gotchas.md`](06-gotchas.md) · Next: [`08-way-we-work.md`](08-way-we-work.md)
