# Database & schema

## Schema policy

- Schema lives in **one file**: `pkg/db/schema.sql`. There is no `migrations/` directory.
- `OpenPostgres` applies the schema on every service boot under a Postgres advisory lock (`schemaLockID`), so racing replicas serialize the apply safely. The schema is idempotent (`CREATE TABLE IF NOT EXISTS`).
- Editing the schema: prefer additive `ADD COLUMN IF NOT EXISTS`. Column drops or renames need to be deliberate and idempotent — the canonical pattern is `ALTER TABLE … DROP COLUMN IF EXISTS …;` appended after the `CREATE TABLE`, so the next boot's schema-apply removes the column on any cluster that still has it. Only do this when nothing reads or writes the column anywhere in the code (grep first).

## Connection pool

Per-pod sizing is env-tunable via `PG_MAX_OPEN_CONNS` (default 30) and `PG_MAX_IDLE_CONNS` (default 10). Per-session `statement_timeout = 5s` (override via `PG_STATEMENT_TIMEOUT_MS`) prevents a missing index from pinning a pool connection forever.

Cluster sizing context: see [`deployment.md`](deployment.md) — `max_connections=500` at the server end with HPA-bounded pod count keeping the live ceiling at ~420.

## sqlc workflow

- After editing `pkg/db/queries/queries.sql`, run `sqlc generate -f infra/sqlc.yaml` to regenerate `pkg/db/gen/*`. **Never hand-edit generated code.**
- The config file is at `infra/sqlc.yaml`, not the repo root.
- After regen, the pre-commit gate's Step 5 checks `git status pkg/db/gen/` — anything dirty there means a previous commit had stale generated code; stage the regenerated files alongside the query edit.
