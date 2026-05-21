# docs/

Topic-organized documentation for this repo. Pick a starting point based on what you're trying to do.

## Architecture

- **[architecture/overview.md](architecture/overview.md)** — the three Go services, why three (not six), responsibilities of each, Postgres + Redis roles.
- **[architecture/redis-patterns.md](architecture/redis-patterns.md)** — distributed locks, leader election, Streams vs Pub/Sub. The three Redis primitives we use and the rules for picking the right one.
- **[architecture/wire.md](architecture/wire.md)** — high-level summary of HTTP routes, Redis channels, and WebSocket events. The normative source is [`pkg/wire/CONTRACT.md`](../pkg/wire/CONTRACT.md).

## Invariants

- **[invariants.md](invariants.md)** — the full list of rules every change must respect. Critical-subset lives inline in `CLAUDE.md` for Claude's always-on context.

## Operations

- **[operations/commands.md](operations/commands.md)** — dev loop: gofmt, lint, test, build, sqlc, frontend.
- **[operations/deployment.md](operations/deployment.md)** — k3s, secrets, manifests, rotation.
- **[operations/debugging.md](operations/debugging.md)** — `kubectl` cheatsheet and a failure-mode index.
- **[operations/database.md](operations/database.md)** — schema policy and the sqlc workflow.

## Roadmap

- **[roadmap.md](roadmap.md)** — shipped log, queued work, deliberately-deferred items, parking lot.
