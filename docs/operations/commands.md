# Dev commands

There is **no Justfile** despite some lingering doc references. Direct commands only:

| What | Command |
|---|---|
| Format check (CI gate) | `gofmt -l .` — must be empty |
| Lint | `golangci-lint run --config infra/.golangci.yml` |
| Tests | `go test -v ./pkg/... ./cmd/...` (CI runs both) |
| Run one test | `go test -run TestSingleLeader ./pkg/leader/...` |
| Build everything | `go build ./...` |
| Build a single service | `go build -o /tmp/gateway ./cmd/gateway` |
| Regenerate sqlc | `sqlc generate -f infra/sqlc.yaml` (config is **not** at repo root) |
| Frontend build | `cd frontend && npm run build` (runs both `build:main` and `build:replay`) |
| UCI smoke (matches CI) | `printf 'uci\nposition startpos\ngo depth 4\nquit\n' \| ./chess-worker -uci` |

## Backend-only build prerequisite

The gateway's `go:embed all:dist` requires `cmd/gateway/dist/*` to exist; backend-only builds need a dummy:

```bash
mkdir -p cmd/gateway/dist && touch cmd/gateway/dist/index.html
```

CI does this in the backend job to avoid an npm round-trip when only Go changed.

## Pre-commit gate

For any commit, run the full local CI gate before pushing. The five gates are:

1. `gofmt -l .` (must be empty)
2. `golangci-lint run --config infra/.golangci.yml`
3. `go test ./pkg/... ./cmd/...`
4. `./infra/check-wire-contract.sh`
5. `sqlc generate -f infra/sqlc.yaml` (only if `pkg/db/queries/queries.sql` changed; then check `git status pkg/db/gen/`)

The `chess-precommit` Claude Code skill walks these in order with stop-on-first-fail. See `.claude/skills/chess-precommit/SKILL.md`.
