---
name: update-chess-docs
description: After shipping a feature on the chess platform, refresh the right files under docs/, pkg/wire/CONTRACT.md (if the wire surface moved), the critical-invariants subset in CLAUDE.md (if a new invariant landed), and the user's memory. Trigger when the user says "update docs", "update memories and docs", "update CLAUDE.md", "refresh docs", or "document this". Do NOT trigger on a routine fix that doesn't change behavior visible from outside the changed file.
---

# update-chess-docs

The user reads docs to onboard their future self and collaborators. Stale docs hurt more than missing ones because they encode a wrong mental model. The bar: someone re-reading the docs after your change should still have a correct picture of the system.

The repo's docs are organized topically:

```
CLAUDE.md                                 — slim always-loaded index + critical invariants subset
README.md                                 — public landing page
docs/architecture/overview.md             — services, responsibilities
docs/architecture/redis-patterns.md       — locks, leader election, streams vs pub/sub
docs/architecture/wire.md                 — wire surface summary
docs/invariants.md                        — full invariants list
docs/operations/{commands,deployment,debugging,database}.md
docs/roadmap.md                           — shipped + queued + deferred
pkg/wire/CONTRACT.md                      — normative wire surface
```

## What to touch

Walk this list in order. Skip any that genuinely don't apply — but **read each file before deciding** it doesn't apply. A feature that adds a new `/api/*` route ALWAYS touches `pkg/wire/CONTRACT.md`, no exceptions.

### 1. Service-shape changes → `docs/architecture/overview.md`

If the change altered which service owns what (a new endpoint moved to a different binary, a goroutine moved, a stream consumer added), update the relevant service paragraph. Keep paragraphs cohesive — don't just append; rewrite for flow.

### 2. New invariant introduced → `docs/invariants.md` AND `CLAUDE.md`

Did the change introduce a new "you must do X or Y breaks" rule? Phrase it as a *rule + reason*, not a description. ("Gateway pulls the matchmaker rating from the DB, never from the request body" — followed by why.) Match the existing voice.

- Always add to `docs/invariants.md` (the full normative list).
- ALSO add to `CLAUDE.md`'s "Critical invariants" subset *if* the invariant is one Claude should know without needing to follow a link — i.e., violating it silently corrupts data or opens a security hole.

### 3. Wire surface moved → `pkg/wire/CONTRACT.md`

Update whenever you:
- Add/remove an HTTP route (any `/api/*`)
- Add/rename a Redis Stream, Pub/Sub channel, Command, or Event
- Change a payload shape (JSON field added / removed / renamed)
- Change auth requirements on an existing route

The CI gate `infra/check-wire-contract.sh` catches Command/Event drift but **not** payload-shape drift — humans catch that. If the SPA contract changed, list both the backend constant AND the frontend listener.

If the high-level Redis-channel table in `docs/architecture/wire.md` is now stale (e.g., a new stream was added), update that summary too.

### 4. Roadmap moved → `docs/roadmap.md`

- Mark queued items shipped with the `✅` legend.
- If a previously-blocked item is now unblocked, update its status line.
- Don't add aspirational items unless the user asked you to.
- This file absorbs what used to be CLAUDE.md's "Already shipped" list — the roadmap is the single source of truth for what's done and what's not.

### 5. Memory under `/Users/taiyanliu/.claude/projects/-Users-taiyanliu-Desktop-code-chess/memory/`

Read `MEMORY.md` first — it's the index of what already exists. Then:

- **Feedback you got this session that wasn't already in memory:** save it. Use the structured body (`**Why:** ...` + `**How to apply:** ...`).
- **Project-level facts that just changed:** if a memory says "X is the current approach" and you replaced X with Y, update or delete that memory. Stale memories actively hurt — they make future-you confidently wrong.
- **Don't save:** what the code now does, recent commits, who touched what. Those are derivable. Save *why* a choice was made or *what surprised the user*.

## What NOT to do

- Don't create new doc files at the repo root. Edit the ones in `docs/`. If a genuinely new topic needs its own file, place it inside the matching `docs/<area>/` folder.
- Don't dump the full diff into docs. Docs are for the mental model; the diff is for `git log`.
- Don't add planning/decision/analysis files. The user's instruction is explicit: "work from conversation context, not intermediate files."
- Don't reference the current task ("added for the studies feature") in docs — docs outlive tasks. Phrase it as how-the-system-now-works.

## Reporting

When done, list the files changed in one line each: `docs/architecture/overview.md — added paragraph for X`. Don't paste the diff.
