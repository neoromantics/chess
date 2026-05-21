# 8. The way we work

A few cultural notes that will save you cycles:

- **Investigate first, then ship.** When asked "where can we improve this?", produce a prioritized list with no code. Wait for explicit greenlight before shipping. Ship items one at a time with build + test + format gates between, not as one giant PR. The `investigate` Claude Code skill codifies this.
- **Don't add abstractions you don't need.** Three similar lines is better than a premature framework. A bug fix doesn't need surrounding cleanup. A one-shot operation doesn't need a helper.
- **Don't write defensive code for impossible cases.** Trust internal callers. Validate at system boundaries (user input, external APIs), not at every layer.
- **Default to no comments.** The code says *what*. Comments say *why*, when the why is non-obvious — a hidden constraint, a workaround for a specific bug, surprising behavior. If removing the comment wouldn't confuse a future reader, don't write it.
- **Ask before doing irreversible things.** Reading and searching are free. Deleting, force-pushing, sending messages, modifying shared infrastructure all need confirmation.
- **Update docs in the same commit as the work.** If you ship a new endpoint, update [`../pkg/wire/CONTRACT.md`](../pkg/wire/CONTRACT.md). If you ship a new invariant, update [`../docs/invariants.md`](../docs/invariants.md). Stale docs send the next person down a wrong path. The `update-chess-docs` skill walks the surfaces.

---

← [`07-reading-list.md`](07-reading-list.md) · Next: [`09-deep-dives/`](09-deep-dives/)
