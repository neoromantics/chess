# chess

A small but complete chess engine and analysis GUI in a single Go binary.

- **UCI engine** that plugs into Cute Chess, Arena, and other GUIs
- **Self-contained web GUI** served by the same binary — no Node, no build step, no assets directory
- **Move-generation correctness** validated against four standard perft positions (startpos, Kiwipete, Pos3, Pos4)
- **Packageable as a macOS `.app`** that opens in your browser and quits when you close the tab

## Features

**Engine**

- 0x88 board representation; legal-move generation with castling, en passant, and promotion
- Negamax + alpha-beta with iterative deepening, quiescence, MVV-LVA move ordering
- Material evaluation with piece-square tables (side-to-move POV)
- Repetition-aware search: treats positions reached three times in the live game (or twice along the search line) as 0, so the engine plays for or against the draw on cp grounds
- Time management: `wtime`/`btime`/`winc`/`binc`/`movestogo`, `movetime`, `depth`, `infinite`
- ~3-4M nodes/sec on a modern laptop

**GUI** (`./chess` from a terminal, or `Chess.app`)

- Three modes — human vs human, human vs engine, engine vs engine — with **live switching** between them mid-game
- **Per-side think time** so two engines can play at different strengths
- **Pause** for engine-vs-engine games
- **Hint** — engine suggests a move, highlighted but not played
- **Move assessment** — click any move to grade it `Best / Brilliant / Excellent / Good / Inaccuracy / Mistake / Blunder` with a centipawn-loss number; defaults to your last move (skips the engine's reply); annotates the move list with `?`/`?!`/`!`/`!!`
- **Touch-move** opt-in tournament rule: clicking a piece commits you to moving it; clicking an immobile piece loses on the spot
- **Board editor** — paint pieces from a palette; Select tool to pick up and move; Delete tool; set side-to-move and castling rights; load any FEN
- **Save / Load** game state as JSON, including the *actual* starting position so edited puzzles round-trip
- **Undo** the last half-move
- **Replay export** — generates a self-contained HTML player you can email to a friend (browser **File → Save Page As → HTML Only**, or `?download=1` on the URL); auto-plays with adjustable speed, prev/next/scrubbable
- **Move history in standard algebraic notation** (`Nf3`, `Bxc4`, `O-O-O`, `e8=Q+`, `Qh4#`) with the long-algebraic move ID as a tooltip
- **Move sounds** (capture / check distinguished); mute toggle persisted to localStorage
- **Draw detection**: stalemate, threefold repetition, insufficient material (KvK, K+minor vs K, K+B vs K+B same color), 50-move rule

## Quick start

Requires Go 1.22+ (uses `http.ServeMux` method-prefixed patterns).

```sh
go build -o chess .

./chess                  # auto: GUI when stdin is a terminal, UCI when piped
./chess -uci             # force UCI on stdio
./chess -gui -no-open    # GUI without auto-opening the browser
./chess -addr :9000      # custom listen address
```

Or with [`just`](https://github.com/casey/just):

```sh
just build       # go build -o chess
just test        # go test ./...
just perft       # move-gen validation
just gui         # ./chess -gui
just check       # gofmt + vet + test
just app         # build a macOS .app
```

### UCI session

```
$ ./chess -uci
uci
id name chessgo
id author anonymous
uciok
position startpos
go movetime 1000
info depth 6 score cp 0 nodes 842162 nps 4088165 time 206 pv b1c3 d7d5 g1f3 ...
bestmove b1c3
quit
```

## macOS app

```sh
just app                        # build/Chess.app  (host arch)
just app-universal              # both Intel and Apple Silicon
open build/Chess.app            # launch
```

The first launch may trigger Gatekeeper because the bundle is unsigned —
right-click → Open → Open, or:

```sh
xattr -dr com.apple.quarantine build/Chess.app
```

When you double-click `Chess.app` it picks up port 8080 (or a free one if
that's taken), starts the GUI, opens your default browser, and waits.
**Closing the browser tab quits the app** — the page sends a 5-second
heartbeat over `/api/ping` and the app exits after 30 seconds without one.

The bundle runs as an "agent" (`LSUIElement` in `Info.plist`), so it has
no Dock icon and no menu bar — the browser tab *is* the UI. To force-quit
without using the browser, run `pkill Chess` in a terminal.

### Uninstalling

Drag `Chess.app` to the Trash. That's it.

The app writes nothing outside its own bundle — no `~/Library` entries,
no caches, no databases, no log files. The only persistent state is the
browser's `localStorage` for the Sound mute preference, scoped to
`localhost:<port>`; since the port can vary between launches it does not
linger meaningfully and is cleared by clearing site data for `localhost`.

## Project layout

| File | Role |
|------|------|
| `main.go` | CLI entry: TTY/app detection, listener fallback, `-gui`/`-uci`/`-shutdown-on-idle` |
| `board.go` | 0x88 board, FEN parse/print, Unicode display |
| `move.go` | `Move`, `Make`/`Unmake`, castling-rights table, long-algebraic parser |
| `movegen.go` | Pseudo-legal generation, attack detection, legal filter |
| `eval.go` | Material + piece-square tables |
| `search.go` | Negamax + alpha-beta + iterative deepening + quiescence + MVV-LVA + repetition awareness |
| `uci.go` | UCI command loop and time control |
| `san.go` | `MoveToSAN` — standard algebraic notation |
| `game.go` | Pure game state and rules (Game, GameStatus, status transitions, undo, touch-move, replay frames) |
| `gui.go` | HTTP-only shim over Game; `http.ServeMux` routes the JSON API |
| `gui.html` | Embedded UI (`//go:embed`) |
| `replay.html` | Embedded shareable replay player |
| `*_test.go` | perft (move-gen), game logic, SAN, repetition |
| `scripts/build-app.sh` | Produces the macOS bundle |

## API

The GUI server speaks JSON over HTTP. All endpoints are local and
unauthenticated — they're meant to drive the embedded UI.

| Method | Path | Purpose |
|---|---|---|
| GET  | `/`                 | Embedded HTML UI |
| GET  | `/api/state`        | Current game snapshot (FEN, legal moves, status, SAN history…) |
| POST | `/api/move`         | `{move: "e2e4"}` — apply a human move |
| POST | `/api/engine_step`  | `{movetime: ms}` — engine plays one move |
| POST | `/api/hint`         | `{movetime}` — engine suggests a move; not applied |
| POST | `/api/assess`       | `{movetime, index?}` — grade a move (default = last human) |
| POST | `/api/touch`        | `{square: "e2"}` — touch-move commit; may declare loss |
| POST | `/api/touch_move`   | `{enabled}` — toggle the tournament rule |
| POST | `/api/set_players`  | `{engine_white, engine_black}` — live switch H↔E |
| POST | `/api/new`          | `{engine_white?, engine_black?}` — fresh game |
| POST | `/api/load`         | `{start_fen?, moves[], engine_white?, engine_black?}` |
| GET  | `/api/save`         | Download game JSON |
| POST | `/api/undo`         | Step back one half-move |
| GET  | `/api/replay.html`  | Self-contained replay player (`?download=1` to attach) |
| POST | `/api/ping`         | Heartbeat for `-shutdown-on-idle` |

## Tests

```sh
just test                # all
just perft               # move-gen vs four standard positions
go test -run SAN ./...
go test -run Repetition ./...
```

## License

MIT.
