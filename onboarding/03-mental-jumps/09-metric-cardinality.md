# 3.9 Metric cardinality

Prometheus counters look like one metric, but in reality they're a *family* — one time-series per unique combination of label values. A counter labeled by `route` looks innocent until you let `route` be the *raw URL* including IDs: `/api/games/12345/move`, `/api/games/12346/move`, ... and now you have a million series, your Prometheus dies, your Grafana dashboards take 60 seconds to render, and your retention drops to two days.

The rule: **labels must have bounded cardinality**. For HTTP routes, that means templated paths (`/api/games/{id}/move`) not resolved ones. Go 1.22's enhanced `ServeMux` does this for us: when you register `mux.HandleFunc("POST /api/games/{id}/move", ...)`, the matched request has `r.Pattern == "POST /api/games/{id}/move"`. We label by `r.Pattern`, and any request that arrives without a Pattern goes into a fixed `<unknown>` bucket so cardinality stays bounded. A spike on the `<unknown>` panel = somebody registered a handler without a `Method /path` declaration.

See `pkg/metrics/metrics.go:HTTPMiddleware`. The same discipline applies to every label everywhere: `time_control` is fine (10 values), `user_id` would be a disaster.

Reference: [`../../docs/invariants.md`](../../docs/invariants.md) (the route-registration invariant).

---

← [`08-cswsh.md`](08-cswsh.md) · Next: [`10-schema-migrations.md`](10-schema-migrations.md)
