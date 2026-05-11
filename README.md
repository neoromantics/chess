# Chess Engine & GUI

A modern chess engine and web-based GUI written in Go and Vue 3 (TypeScript).

## Features
- **Engine**: Bitboard-based (0x88) chess engine with iterative deepening, alpha-beta pruning, quiescence search, and tapered evaluation.
- **Web UI**: Responsive Vue 3 Single Page Application with TypeScript.
- **Native Mac UI**: Standalone macOS window mode using `webview_go`.
- **Analysis Tool**: Move assessment and hints powered by the engine.
- **Board Editor**: Full-featured position setup tool.
- **Replay**: Self-contained, portable replay viewer.

## Project Structure
```
.
├── cmd/chess/          # Main entry point (main.go)
├── pkg/
│   ├── core/           # Engine internals (board, search, eval, etc.)
│   ├── game/           # Game session & history management
│   ├── api/            # Web server & HTTP handlers (embeds frontend/dist)
│   └── uci/            # UCI protocol implementation
├── frontend/           # Vue 3 TypeScript project
├── scripts/            # Build and utility scripts
└── Justfile            # Command runner
```

## Development

### One Button Dev Workflow
To run both the backend and frontend with Hot Module Replacement (HMR):
```bash
just dev
```
This starts the Go API server on port 8080 and the Vite dev server on port 5173. Visit `http://localhost:5173` to play.

### Local Build
To build a production binary with the frontend embedded:
```bash
just build
./chess -gui
```

### macOS App
To create a native macOS application bundle (`build/Chess.app`):
```bash
just app
```

## License
MIT
