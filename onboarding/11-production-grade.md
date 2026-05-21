# 11. How production-grade is this, really?

The honest answer is two parts: **the code is genuinely production-grade; the infrastructure is hobby-grade.** Hold onto that distinction — it's the truest one-line description of the platform you'll get, and it matters for how you talk about the work (in interviews, in PRs, in postmortems you may eventually write).

## What's actually production-quality

The architectural primitives are textbook-right: per-game distributed locks with correct compare-and-swap release ([§3.2](03-mental-jumps/02-distributed-locks.md)), leader election ([§3.3](03-mental-jumps/03-leader-election.md)), durable Streams for at-least-once delivery ([§3.4](03-mental-jumps/04-streams-vs-pubsub.md)), a wire-contract CI gate against backend/frontend drift, structured slog + Prometheus business metrics ([§3.9](03-mental-jumps/09-metric-cardinality.md), [§3.11](03-mental-jumps/11-ops-logging.md)), Postgres write-through caching with "Postgres-first, then cache, then publish" ordering. Security posture is what a serious internal tool at a real company looks like: JWT trust boundary ([§3.5](03-mental-jumps/05-trust-boundary.md)), bcrypt ([§3.6](03-mental-jumps/06-passwords.md)), rate limiting on the auth surface, WS origin allow-list ([§3.8](03-mental-jumps/08-cswsh.md)), 404-not-403 for existence leaks, sqlc-only DB access so the SQL-injection surface is zero. The "we tried six services, every bug was wire-protocol drift, we consolidated to three" arc and the "we started with Pub/Sub for engine results, lost results during deploys, promoted to a Stream" arc ([§3.4](03-mental-jumps/04-streams-vs-pubsub.md)) are real production scars, healed correctly. This codebase is **better-engineered than ~70% of internal tools at mid-sized companies**, and that's an honest read, not a flattering one.

## What's not production-grade

- **Single-node k3s.** One Duke-issued VM. Node death = full platform outage. Every replica, every "HA" primitive, runs on the same kernel. The lock and leader-election plumbing is *correct* but its impressive shape is partly decorative until there's a second node.
- **No Redis HA, no Postgres HA.** Single instance of each, both SPOFs.
- **No off-cluster backups.** Postgres AOF + RDB live on a PVC; if the PVC corrupts, data is gone.
- **No DR plan.** No documented RPO/RTO. No runbook for "Postgres corrupt, restore from backup."
- **No staging.** Deploys land in prod.
- **No alerting / on-call.** Prometheus exists but isn't wired to PagerDuty/Opsgenie. You read `kubectl logs` when something feels wrong ([§3.11](03-mental-jumps/11-ops-logging.md)).
- **No frontend tests.** Go tests cover the backend; the Vue side relies on manual QA.
- **Bus factor of 1**, and the deployment is tied to a Duke VM that disappears with the operator's account.

None of this is hidden — the open items in [`../docs/roadmap.md`](../docs/roadmap.md) (Redis Sentinel, Postgres HA, read replicas) acknowledge most of these, deferred for the legitimate reason that they don't buy anything on a single-node cluster (see [`10-cloud-tradeoffs.md`](10-cloud-tradeoffs.md)).

## Scoring it honestly

| Dimension | 1–10 | Why |
|---|---|---|
| Code architecture | 8 | Distributed primitives correct; idiomatic Go; clean service boundaries. |
| Security posture | 7 | Strong on auth + injection; missing CSRF tokens on cookie routes, no MFA. |
| Observability | 7 | Prom + Grafana + business metrics + slog. Missing alerting + SLOs. |
| Documentation | 9 | This onboarding tree, `../CLAUDE.md`, `../docs/`, `../pkg/wire/CONTRACT.md` are exceptional for a solo project. |
| Data durability / HA | 3 | Single Redis, single Postgres, no backups, no DR. |
| Operational maturity | 4 | No staging, no on-call, manual deploys via `kubectl`. |
| Scalability *in theory* | 7 | HPA, KEDA, leader election, locks all present. |
| Scalability *in practice* | 4 | All replicas on one node — no real fault tolerance under node failure. |

## The framing to carry

This is **"engineered like a senior engineer's pet project, deployed like a senior engineer's pet project."** Both halves are honest. The architecture would impress a senior interviewer; the SPOF + missing-backups answers would get probed. The right framing isn't "production chess platform" — it's "production-quality chess platform on hobby-grade infrastructure, and here's exactly which pieces would change to be true production." Self-awareness reads as a senior signal, not a weakness.

## Cheapest wins to close the gap

Without buying more VMs, three moves would shift the platform from "production-quality code on hobby infra" to "production-quality code on small-but-defensible infra." All three are queued in [`../docs/roadmap.md`](../docs/roadmap.md) under "Production hardening":

1. **Automated `pg_dump` + Redis snapshot to off-VM storage.** Cron + scp/rclone to another machine or S3. ~half a day. Real durability win.
2. **Staging namespace within the same cluster.** `chess-staging` alongside `chess`. ~a few hours. Lets you smoke-test a deploy before pointing the prod ingress at it.
3. **Frontend Playwright smoke tests.** Login → start game → make move → resign. ~one day. Catches the class of regressions that Go tests can't.

The list of items that genuinely *need* more VMs (Sentinel, Patroni, multi-node KEDA spread) lives in [`10-cloud-tradeoffs.md`](10-cloud-tradeoffs.md) and in [`../docs/roadmap.md`](../docs/roadmap.md)'s "Deferred (deliberate)" section — those are correctly deferred, not gaps.

---

Welcome aboard.

← [`10-cloud-tradeoffs.md`](10-cloud-tradeoffs.md) · Back to [`README.md`](README.md)
