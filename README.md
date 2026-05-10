# chess

A self-contained chess engine + web GUI in Go.

- UCI engine: plug into Cute Chess, Arena, or any UCI GUI.
- Embedded web UI: click-to-move board with Hint, board editor, save/load,
  per-move engine assessment, and an opt-in tournament-style touch-move rule.
- Move generation validated against four standard perft positions.

## Quick start

```
go build -o chess
./chess                    # auto: GUI in a terminal (opens browser), UCI when piped
./chess -gui               # force GUI (opens browser; add -no-open to suppress)
./chess -uci               # force UCI on stdio
./chess -addr localhost:9000
```

Auto-detect uses stdin: a terminal launches the GUI; a pipe (Cute Chess,
CI scripts) speaks UCI on stdio.

Or with `just`:

```
just build
just gui
```

## UCI session

```
$ ./chess
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

## GUI features

- Three modes: human-vs-engine, human-vs-human, engine-vs-engine.
- **Hint** — engine suggests a move (highlighted green) without playing it.
- **Assess my moves** — after each user move, the engine grades it
  Best / Brilliant / Excellent / Good / Inaccuracy / Mistake / Blunder
  with a centipawn-loss number. Always assesses *your* last move (skips
  the engine's reply).
- **Touch-move** — opt-in tournament rule: once you click a piece you
  must move it; clicking a piece with no legal moves loses on the spot.
- **Edit Position** — paint pieces from a palette, set side-to-move and
  castling rights, then Apply to load any FEN. Useful for puzzle setups.
- **Save / Load** — download/upload game JSON; or paste any FEN.

## Project layout

See [CLAUDE.md](./CLAUDE.md) for the file map and engine notes.

## Tests

```
just test       # all tests
just perft      # move-gen validation against startpos, Kiwipete, Pos3, Pos4
```
