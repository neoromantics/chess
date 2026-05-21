# 3.11 Why we read kubectl logs, not Sentry or Datadog

Production observability here is `kubectl logs` (raw text) plus Prometheus/Grafana (numbers). No APM, no log-aggregation SaaS. That means:

- **Log a sentence, not a fragment.** `slog.Info("matchmaker paired", "white", w, "black", b, "tc", tc)` becomes a JSON line a human can grep. Decisions should be logged at INFO; routine successes should not be.
- **Log on direction changes.** When the matchmaker switches a player from "queued for human" to "engine fallback," log it. When the engine returns a result, log the move and the eval. When auth rejects something, log why.
- **Errors get the cause.** `slog.Error("xadd failed", "err", err, "stream", "engine:requests")` — never bare `log.Println(err)`.

The user (your collaborator) reads logs on the VM. They will not see your `fmt.Println` debugging in production. Use `slog`.

Reference: [`../../docs/operations/debugging.md`](../../docs/operations/debugging.md).

---

← [`10-schema-migrations.md`](10-schema-migrations.md) · Back to [`README.md`](README.md)
