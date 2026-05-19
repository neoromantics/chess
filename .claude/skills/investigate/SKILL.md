---
name: investigate
description: When the user says "investigate X", "look into X", "what's going on with X", "audit X", or otherwise asks for analysis without explicitly authorizing code changes — produce a prioritized written punch-list and STOP. Do not write or edit code. Wait for explicit approval ("Yes" / "Sure" / "Go" / "Ship it" / "Do it" / "do the next thing") before implementing any item, and pause after each item is shipped before moving to the next. This codifies the user's repeated guidance about investigation-then-ship workflow.
---

# investigate

The user has corrected this pattern multiple times — investigation and implementation are **separate phases**. Mixing them ("I'll just fix this one while I'm in here") loses the user's chance to redirect priorities and skip items they don't care about.

## Phase 1 — investigate (read-only)

Allowed tools: Read, Grep/Bash-for-grep, Glob, WebFetch, the Explore agent, git read commands. **Not allowed: Edit, Write, NotebookEdit, any state-changing Bash command.**

Output a **prioritized list** of what you found. Each item:

- **Title** — one short line, what the issue is.
- **Where** — `path/to/file.go:LINE` so the user can navigate.
- **Why it matters** — one sentence on impact. Be specific ("breaks reconnect when the gateway pod restarts mid-game") not vague ("could cause issues").
- **Effort** — rough size: trivial / small / medium / large. Lets the user pick high-value-low-effort items first.

Order the list by impact, not by file. Group only when items are genuinely the same root cause.

Format the list with numbered items so the user can refer back: "ship item 3."

End with a single line: **"Pick which to ship, or say 'all' to walk the list."** Do not implement anything yet.

## Phase 2 — ship one item

Trigger: explicit approval ("Yes" / "Sure" / "Go" / "Ship it" / "Do it" / "do the next thing" / "do #3").

For the chosen item only:
1. Make the change.
2. Run the local CI gate (use the `chess-precommit` skill).
3. **Do not commit** unless the user explicitly asked for a commit. The user's standing rule: never commit without an explicit ask.
4. Report what changed in one or two sentences. Note the file:line.

Then **STOP**. Do not continue to the next item even if the previous one was trivial.

## Phase 3 — pause for the next signal

Wait for the next "Yes" / "do the next thing" / "continue". If the user comes back with a new direction or a different item number, drop the list order and follow them.

If the user says "all", walk the list in order but still pause briefly between items — they may want to interject. A 1-2 sentence "moving to #2: ..." update before each item is enough.

## When NOT to use this skill

- The user already authorized a specific fix in plain language ("fix the off-by-one in handleHTTPMove"). That's an instruction, not an investigation request. Just do it.
- The user asks a factual question ("does the gateway verify JWT on /api/state?"). Answer it directly — no punch-list needed.
- The investigation is one-step and the answer fits in two sentences. Don't ceremony-up a trivial question.

## Anti-patterns to avoid

- **Don't ship while listing.** ("Found 4 issues; fixed #1 while writing this up.") The user wants the choice.
- **Don't pad the list.** Three real issues is better than five with two filler items.
- **Don't refactor as you investigate.** Renaming a variable, deleting an unused import — those are items 4 and 5 on the list, not silent side-effects of reading.
- **Don't bury the recommendation.** If you have a strong opinion on which to ship first, say so in one line after the list.
