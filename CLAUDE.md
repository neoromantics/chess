# CLAUDE.md - Development Guide

## Build & Run
- **Frontend Assets**: `just frontend` (builds Vue + copies to Go package)
- **Build Binary**: `just build` (produces `./chess`)
- **One-Button Dev**: `just dev` (Go API on :8080 + Vite on :5173 with HMR)
- **Run GUI**: `just gui` (builds + runs embedded web UI)
- **Run UCI**: `just run` (starts engine in terminal)
- **Tests**: `just test` (all), `just perft` (movegen only)

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
