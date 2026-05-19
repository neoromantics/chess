---
name: chess-precommit
description: Run the local CI gate (gofmt, golangci-lint, go test, wire-contract check, sqlc-up-to-date) before committing to the chess repo. Use whenever you are about to create a commit, or whenever the user asks to "check before commit" / "run CI locally" / "pre-commit check". Surfaces failures with the exact follow-up command.
---

# chess-precommit

CI for this repo gates on five things. Run them in this order and STOP on the first failure — fix it before proceeding. Do **not** skip any: every one of these has caught a real production-breaking regression on this codebase.

## Step 0 — backend-only build prerequisite

The gateway uses `go:embed all:dist`. If `cmd/gateway/dist/index.html` doesn't exist, every Go build fails with `pattern dist: no matching files found`. Before running any Go command:

```bash
mkdir -p cmd/gateway/dist && [ -f cmd/gateway/dist/index.html ] || touch cmd/gateway/dist/index.html
```

CI does the same thing in the backend job. If frontend changed in the same commit, run `cd frontend && npm run build` instead so the embed is real, not a stub.

## Step 1 — gofmt

```bash
gofmt -l .
```

Output **must be empty**. Any path printed = unformatted file. Fix with `gofmt -w <path>`.

## Step 2 — golangci-lint

```bash
golangci-lint run --config infra/.golangci.yml
```

Config lives at `infra/.golangci.yml`, NOT repo root. Fix all reported issues — do not silence with `//nolint` unless the user explicitly says to.

## Step 3 — tests

```bash
go test ./pkg/... ./cmd/...
```

CI runs both subtrees. A pass in `./pkg/...` alone is not sufficient. If a test in `cmd/game/testhelpers_test.go` or `cmd/gateway/admin_handlers_test.go` fails after a sqlc regen, the `panicStore` / `gameStore` stubs likely need their method signatures updated to match the new generated interface.

## Step 4 — wire-contract drift

```bash
./infra/check-wire-contract.sh
```

This walks `pkg/eventbus/eventbus.go` and `pkg/wire/CONTRACT.md` and fails if a Command/Event type exists in code but not in the doc, or vice versa. **The contract is normative** — if the doc is stale, update the doc; don't suppress the check.

## Step 5 — sqlc up to date

If `pkg/db/queries/queries.sql` was modified, regenerate:

```bash
sqlc generate -f infra/sqlc.yaml
```

Then check `git status pkg/db/gen/` — if anything changed, the previous commit had stale generated code. Stage the regenerated files alongside the query edit.

## Reporting

When all five pass, say one short line: `precommit: green`. When any fail, surface the **exact command that failed** and the first ~10 lines of output. Don't paraphrase the error.

## When NOT to run

- Doc-only commits (`*.md` only — but still run `./infra/check-wire-contract.sh` if `CONTRACT.md` changed)
- Frontend-only commits — replace steps 1-3 with `cd frontend && npm run build` and `npm run typecheck` if it exists
- The user says "skip checks" or "I'll commit as-is"
