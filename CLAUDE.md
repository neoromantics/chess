# CLAUDE.md - Development Guide

## Build & Run
- **Start All (Docker)**: `just up`
- **Stop All**: `just down`
- **View Logs**: `just logs`
- **One-Button Dev**: `just dev` (Host-native for rapid coding)
- **Run Tests**: `just test`
- **Clean All**: `just clean`

## Architecture
- **cmd/chess**: Entry point; handles CLI flags and GUI/UCI mode selection.
- **pkg/core**: Pure chess logic (Board, MoveGen, Search, Eval). No external dependencies.
- **pkg/game**: Game session management, history, and state transitions.
- **pkg/api**: HTTP handlers and static asset embedding for the Web/Native GUI.
- **pkg/uci**: Standard Chess Engine protocol implementation.
- **frontend**: Vue 3 SPA using TypeScript, Vite, and SFCs.

## Style Guidelines
- **Go**: Idiomatic Go, capitalized exports for cross-package use, `gofmt` compliant.
- **Vue/TS**: Composition API (`<script setup lang="ts">`), explicit interfaces in `types.ts`, scoped styles.
- **Modularity**: Pure logic in `pkg/core`. Keep `pkg/api` as a thin wrapper for JSON conversion.

## Commands Reference
- Format: `just fmt`
- Lint: `just check`
- Benchmark: `just bench depth=8`
