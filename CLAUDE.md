# Project guidance

A chess engine + GUI in Go (single `package main`). The engine speaks UCI on
stdio by default; with `-gui` it serves an embedded HTML/JS chessboard.

## Layout

| File             | Role                                                                   |
|------------------|------------------------------------------------------------------------|
| `main.go`        | CLI entry. `-gui [-addr]` selects GUI; otherwise UCI on stdio.          |
| `board.go`       | 0x88 board, `Color`/`PieceType`/`Piece`, FEN parse/print, Unicode display. |
| `move.go`        | `Move` type, `MakeMove` / `UnmakeMove`, long-algebraic parser, castling-rights table. |
| `movegen.go`     | Pseudo-legal generation, `SquareAttacked`, legal-move filter, castling guards. |
| `eval.go`        | Material + piece-square tables (side-to-move relative).                |
| `search.go`      | Negamax + alpha-beta + iterative deepening + quiescence + MVV-LVA + soft time bound. |
| `uci.go`         | UCI command loop and time management.                                   |
| `gui.go`         | HTTP handler, in-memory game, JSON API.                                 |
| `gui.html`       | Embedded UI (`//go:embed`).                                             |
| `perft_test.go`  | Move-gen validation against four standard perft positions.              |

## Conventions

- Run `gofmt` (the PostToolUse hook does this automatically on `.go` saves).
- One package, no premature splitting into `internal/`. Add subpackages only when there's a real consumer.
- Keep public surface small — only export what crosses a meaningful boundary.
- No comments unless they explain *why*. Doc comments only on exported identifiers when behavior isn't obvious from the name.
- No transposition table, bitboards, or `null-move pruning` until perft and time-control regressions are covered.
- Don't add abstractions for hypothetical future engine variants. The 0x88 representation is fine.

## Common tasks

```
just build      # go build -o chess
just test       # go test ./...
just perft      # go test -run Perft -v
just run        # ./chess  (UCI on stdio)
just gui        # ./chess -gui  (http://localhost:8080)
just check      # gofmt + vet + test
```

## Engine notes

- 0x88 board. `OnBoard(sq) == sq & 0x88 == 0`.
- Color in piece bit 3; Empty == 0.
- Castling-rights mask `castleClear[sq]` is OR'd off on every move from/to the corner squares.
- `genCastling` requires the king on the e-file and a real rook on the corner — robust to edited FENs with stale rights.
- Search returns scores in side-to-move POV. Mate scores: `MateScore - ply` (faster mates score higher).

## GUI API

Endpoints (all JSON):

| Method | Path                | Body / behavior                                                |
|--------|---------------------|----------------------------------------------------------------|
| GET    | `/`                 | Embedded HTML UI                                                |
| GET    | `/api/state`        | Current snapshot (FEN, legal moves, status, etc.)               |
| POST   | `/api/move`         | `{move:"e2e4"}` — apply human move                              |
| POST   | `/api/engine_step`  | `{movetime:1000}` — engine plays one move                        |
| POST   | `/api/hint`         | `{movetime}` — return suggested move; do NOT apply              |
| POST   | `/api/touch`        | `{square:"e2"}` — touch-move commit; may declare loss           |
| POST   | `/api/touch_move`   | `{enabled:bool}` — toggle tournament rule                        |
| POST   | `/api/assess`       | `{movetime}` — grade the user's *last* move (skips engine moves) |
| POST   | `/api/new`          | `{engine_white,engine_black}` — fresh game                       |
| GET    | `/api/save`         | Download game JSON                                               |
| POST   | `/api/load`         | `{start_fen?, moves[], engine_white?, engine_black?}`            |

## Don't

- Add features (analysis multipv, opening book, eval tweaks) without a perft + UCI-smoke pass first.
- Touch the search interface without re-running `go test` and a 1-second `go depth N` smoke.
- Edit `gui.html` and forget — it's `//go:embed`-ed; `go build` re-bundles.
- Reintroduce C++ tooling in `.claude/settings.json`.
