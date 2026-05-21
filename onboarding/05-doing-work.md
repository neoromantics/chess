# 5. How to actually do work

## 5.1 The dev loop

```bash
# Make sure cmd/gateway/dist/ exists so go:embed is happy on backend-only changes
mkdir -p cmd/gateway/dist && touch cmd/gateway/dist/index.html

# Quick build of everything
go build ./...

# Run a single service binary
go build -o /tmp/gateway ./cmd/gateway && /tmp/gateway

# Tests
go test -v ./pkg/... ./cmd/...

# A specific test
go test -run TestSingleLeader ./pkg/leader/...

# Format gate — CI fails if this prints anything
gofmt -l .

# Lint
golangci-lint run --config infra/.golangci.yml

# Frontend
cd frontend && npm run build

# UCI smoke — quick way to make sure the engine works at all
printf 'uci\nposition startpos\ngo depth 4\nquit\n' | ./chess-worker -uci
```

There is **no Justfile** despite docs that mention one — direct commands only. Canonical command reference: [`../docs/operations/commands.md`](../docs/operations/commands.md).

## 5.2 Where to find what

- `cmd/gateway/` — HTTP/WS edge, auth surface
- `cmd/game/` — game state machine, matchmaking, rating updates
- `cmd/engine-worker/` — CPU search + UCI CLI mode
- `pkg/core/` — **pure chess engine, zero dependencies.** This is the core IP. Do not introduce third-party packages here.
- `pkg/db/queries/queries.sql` — sqlc input. After editing, run `sqlc generate -f infra/sqlc.yaml`. Never hand-edit `pkg/db/gen/*`.
- `pkg/db/schema.sql` — single source of truth for the schema.
- `pkg/wire/CONTRACT.md` — *the* wire contract. Every endpoint, event, payload. Edit in the same commit as any new wire surface.
- `frontend/src/` — Vue 3 SPA. Components, stores (Pinia), API client.
- `infra/deploy.yaml` — single Kubernetes manifest with all three Deployments + ingress + PVCs.
- `CLAUDE.md` — slim always-loaded index for Claude Code (and humans).
- `docs/` — topic-organized depth (architecture, operations, invariants, roadmap).

## 5.3 Tests

`pkg/...` has the unit tests you'd expect — chess rules, PGN encode/decode, Glicko-2 math (numerically verified against the original paper).

`cmd/game/` is a more recent test suite. The pattern is composable in-memory stores: `panicStore` panics on any call (the default — failing loudly tells you what the test should be stubbing), then you compose a `gameStore` that overrides only the methods your test actually uses. `miniredis` stands in for Redis. See `cmd/game/testhelpers_test.go`, `cmd/game/lock_test.go`, `cmd/game/handle_move_test.go`. This is a useful pattern in general: test doubles that loudly fail on unexpected use catch more bugs than test doubles that silently return zero values.

## 5.4 Committing

- Create new commits, don't amend (failed pre-commit hooks mean the commit didn't happen — amending would modify the *previous* commit).
- Never `--no-verify` to skip hooks; never `--force` to main.
- Conventional commits: `feat(scope): summary`, `fix(scope): summary`, `refactor(scope): summary`, `docs: summary`. Bodies in HEREDOC so formatting survives.
- The `chess-precommit` Claude Code skill runs the full local CI gate (gofmt + golangci-lint + tests + wire-contract drift + sqlc). Run it before committing.

## 5.5 Deploying

CI is GitHub Actions. On push to main: build the unified Docker image, push to `ghcr.io/neoromantics/chess`, then a self-hosted runner on the VM does `kubectl apply -k infra/` + `kubectl rollout restart` on each Deployment.

**You do not run `kubectl` yourself.** That's your collaborator's role (operating the cluster). You commit code; CI ships it. If a deploy fails, the next message on your screen will be them showing you `kubectl logs` output.

Canonical deploy details: [`../docs/operations/deployment.md`](../docs/operations/deployment.md).

---

← [`04-move-trace.md`](04-move-trace.md) · Next: [`06-gotchas.md`](06-gotchas.md)
