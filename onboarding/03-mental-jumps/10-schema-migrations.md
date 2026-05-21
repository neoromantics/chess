# 3.10 Schema migrations as code, not commands

In some shops, migrations are sequential SQL files run by a tool. Here, the schema is a single idempotent file (`pkg/db/schema.sql`) that *every service applies on boot*, guarded by a Postgres advisory lock so multiple replicas racing to boot serialize their apply.

`CREATE TABLE IF NOT EXISTS …` is idempotent. `ALTER TABLE … ADD COLUMN IF NOT EXISTS …` is idempotent. Dropping a column is also idempotent if you write `DROP COLUMN IF EXISTS` and grep first to make sure nothing reads or writes it anywhere.

The advantage: no separate migration tool, no migration version table to drift, no "did the migration run on staging?" questions. The constraint: you have to think in terms of *additive, idempotent* schema changes. Renames become "add new column, dual-write, backfill, drop old column" sequences, not in-place renames.

Reference: [`../../docs/operations/database.md`](../../docs/operations/database.md).

---

← [`09-metric-cardinality.md`](09-metric-cardinality.md) · Next: [`11-ops-logging.md`](11-ops-logging.md)
