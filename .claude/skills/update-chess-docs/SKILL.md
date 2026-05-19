---
name: update-chess-docs
description: After shipping a feature on the chess platform, refresh CLAUDE.md (service paragraphs + invariants + "Already shipped" list), pkg/wire/CONTRACT.md (if the wire surface moved), ROADMAP.md (if status changed), and the user's relevant memory entries. Trigger when the user says "update docs", "update memories and docs", "update CLAUDE.md", "refresh docs", or "document this". Do NOT trigger on a routine fix that doesn't change behavior visible from outside the changed file.
---

# update-chess-docs

The user reads docs to onboard their future self and collaborators. Stale docs hurt more than missing ones because they encode a wrong mental model. The bar: someone re-reading `CLAUDE.md` after your change should still have a correct picture of the system.

## What to touch

Walk this list in order. Skip any that genuinely don't apply — but **read each file before deciding** it doesn't apply. A feature that adds a new `/api/*` route ALWAYS touches CONTRACT.md, no exceptions.

### 1. `CLAUDE.md` — three spots that drift fastest

a) **Service responsibilities paragraph** (the bullet for the service that changed). Add a sentence mentioning the new behavior. Keep the paragraph cohesive — don't just append.

b) **Key invariants section.** Did the change introduce a new "you must do X or Y breaks" rule? Add it. Phrase it as a *rule + reason*, not a description. ("Gateway pulls the matchmaker rating from the DB, never from the request body" — followed by why.) Match the existing voice.

c) **"Already shipped" list** under "Things explicitly left as follow-ups." One short bullet per shipped feature. Cross-link to the file/function where the entry point lives so future-you can grep.

### 2. `pkg/wire/CONTRACT.md` — normative wire surface

Update whenever you:
- Add/remove an HTTP route (any `/api/*`)
- Add/rename a Redis Stream, Pub/Sub channel, Command, or Event
- Change a payload shape (JSON field added / removed / renamed)
- Change auth requirements on an existing route

The CI gate `infra/check-wire-contract.sh` catches Command/Event drift but **not** payload-shape drift — humans catch that. If the SPA contract changed, list both the backend constant AND the frontend listener.

### 3. `ROADMAP.md` — only if a roadmap item shipped or unblocked

- Move shipped items from open lists to the "Already shipped" section in CLAUDE.md (NOT in ROADMAP.md — ROADMAP is forward-looking).
- If a previously-blocked item is now unblocked, update its status line.
- Don't add aspirational items here unless the user asked you to.

### 4. Memory under `/Users/taiyanliu/.claude/projects/-Users-taiyanliu-Desktop-code-chess/memory/`

Read `MEMORY.md` first — it's the index of what already exists. Then:

- **Feedback you got this session that wasn't already in memory:** save it. Use the structured body (`**Why:** ...` + `**How to apply:** ...`).
- **Project-level facts that just changed:** if a memory says "X is the current approach" and you replaced X with Y, update or delete that memory. Stale memories actively hurt — they make future-you confidently wrong.
- **Don't save:** what the code now does, recent commits, who touched what. Those are derivable. Save *why* a choice was made or *what surprised the user*.

## What NOT to do

- Don't create new doc files. Edit existing ones. `CLAUDE.md`, `CONTRACT.md`, `ROADMAP.md`, and the memory dir are the four surfaces.
- Don't dump the full diff into docs. Docs are for the mental model; the diff is for `git log`.
- Don't add planning/decision/analysis files. The user's instruction is explicit: "work from conversation context, not intermediate files."
- Don't reference the current task ("added for the studies feature") in docs — docs outlive tasks. Phrase it as how-the-system-now-works.

## Reporting

When done, list the files changed in one line each: `CLAUDE.md — added "Already shipped" bullet for X`. Don't paste the diff.
