# Onboarding — neoromantics Chess

Welcome. This is the bridge between what you learned in your CS degree and what you'll actually be doing in this codebase. It assumes you're comfortable with algorithms, data structures, basic OS concepts, SQL, Git, and at least one mainstream language. It does **not** assume you've shipped to a Kubernetes cluster, debugged a distributed lock, or argued with a Prometheus dashboard at 11 pm. Those gaps are what this doc fills.

Read in order the first time through. Then keep [`../CLAUDE.md`](../CLAUDE.md) and [`../docs/`](../docs/) open as day-to-day references — those are the operator's manual; this is the *map*.

## Reading order

1. [`01-intro.md`](01-intro.md) — what you're working on
2. [`02-system-shape.md`](02-system-shape.md) — the architecture in pictures
3. [`03-mental-jumps/`](03-mental-jumps/) — eleven "CS to production" jumps, one file each
4. [`04-move-trace.md`](04-move-trace.md) — a single move, end to end, touching everything above
5. [`05-doing-work.md`](05-doing-work.md) — dev loop, where to find what, tests, commits, deploys
6. [`06-gotchas.md`](06-gotchas.md) — things that bit us once; recognize the signature so they don't bite again
7. [`07-reading-list.md`](07-reading-list.md) — what to read in this repo, then externally
8. [`08-way-we-work.md`](08-way-we-work.md) — cultural notes that save cycles
9. [`09-deep-dives/`](09-deep-dives/) — optional Redis + WebSocket internals; skim if §3 felt sufficient
10. [`10-cloud-tradeoffs.md`](10-cloud-tradeoffs.md) — what "deploy to AWS" would actually buy
11. [`11-production-grade.md`](11-production-grade.md) — honest assessment of how production-grade this is

## How this relates to the public docs

`../docs/` is the **reference** layer — canonical patterns, full invariant list, command tables. This onboarding layer is the **pedagogical** layer — the *why*, the analogies, the failure scenarios that motivate each pattern. Where they overlap, this layer keeps the teaching and links to `../docs/` for the canonical form.

This whole directory is gitignored — it lives locally as personal context, not in the public repo.
