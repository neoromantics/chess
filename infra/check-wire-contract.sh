#!/usr/bin/env bash
# Verifies that every WebSocket event type documented in
# pkg/wire/CONTRACT.md is referenced by both backend (Go) and frontend
# (TS/Vue) code. Catches the silent class of bug where one side renames
# or removes an event and the other side keeps listening for / emitting
# the now-dead name.
#
# Run from repo root:  ./infra/check-wire-contract.sh
# Exit 0 = all events linked from both sides; nonzero = drift.
#
# Heuristic: a "reference" is any literal string match like "EvtName"
# (backend constant declarations resolve to literals; frontend handlers
# always pass the literal to userEventsStore.on(...) or compare against
# data.type). False positives are rare because the strings are
# distinctive — false negatives only happen if a side constructs the
# event name via concatenation, which we don't do.

set -euo pipefail

contract="pkg/wire/CONTRACT.md"
if [[ ! -f "$contract" ]]; then
  echo "check-wire-contract: $contract not found (run from repo root)" >&2
  exit 2
fi

# Extract the WS event names from Section 3's tables. Each row is
#   | `EventName` | ...
# (the backticked first column is the wire string; the rest of the
# row is human prose). We skip the literal `type` header row.
#
# Rows annotated with the HTML comment `<!-- spa-ignored -->` opt out
# of the frontend-reference check: the event still reaches the SPA
# over the per-game pub/sub but no Pinia store / view actively
# listens for it (the StateUpdated companion carries the relevant
# state). Use sparingly — usually you want the SPA to act on what the
# backend emits.
section3=$(awk '/^## Section 3/,/^## Section 4/' "$contract")
events=$(echo "$section3" \
  | grep -oE '^\| `[a-zA-Z_]+`' \
  | sed -E 's/^\| `(.*)`$/\1/' \
  | grep -v '^type$' \
  | sort -u)

spa_ignored=$(echo "$section3" \
  | grep -E '<!-- spa-ignored -->' \
  | grep -oE '^\| `[a-zA-Z_]+`' \
  | sed -E 's/^\| `(.*)`$/\1/' \
  | sort -u)

if [[ -z "$events" ]]; then
  echo "check-wire-contract: extracted zero events — CONTRACT.md format changed?" >&2
  exit 2
fi

fail=0
for evt in $events; do
  # Backend: any Go file under cmd/ or pkg/ contains the literal.
  # The constant declarations in pkg/eventbus/eventbus.go are the
  # canonical place; usage sites reference the constant, not the
  # literal, so finding it in the const block is enough.
  if ! grep -rqF "\"$evt\"" --include='*.go' cmd/ pkg/; then
    echo "MISSING backend reference: $evt" >&2
    fail=1
  fi
  # Frontend: any TS or Vue file under frontend/src/ contains either
  # 'evt' or "evt" as a literal — covers both quote styles. Skip
  # events explicitly flagged spa-ignored in CONTRACT.md.
  if echo "$spa_ignored" | grep -qx "$evt"; then
    continue
  fi
  if ! grep -rqE "['\"]${evt}['\"]" --include='*.ts' --include='*.vue' frontend/src/; then
    echo "MISSING frontend reference: $evt" >&2
    fail=1
  fi
done

if (( fail )); then
  echo "" >&2
  echo "check-wire-contract: drift detected. Either:" >&2
  echo "  - remove the event from pkg/wire/CONTRACT.md if it's truly dead, or" >&2
  echo "  - add the missing reference on the side that lost it." >&2
  exit 1
fi

echo "check-wire-contract: $(echo "$events" | wc -l | tr -d ' ') events linked from both sides ✓"
