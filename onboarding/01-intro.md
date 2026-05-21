# 1. What you're working on

`chess` is a multiplayer chess platform — sign up, get a Glicko-2 rating, play other humans through matchmaking, play the engine, analyze your games, watch other people's games live. It runs at `https://vcm-50800.vm.duke.edu` on a Kubernetes cluster (k3s, single VM at Duke).

Three programs in Go, one Postgres, one Redis, one Vue 3 single-page app. Everything talks over the network — no shared memory, no shared disk, no in-process state that matters across restarts. You will find this annoying at first and liberating later.

The frontend is *embedded inside the gateway binary* via Go's `//go:embed`. That means one Docker image carries the SPA, the auth surface, the game logic, and the routing layer. The engine search is the only thing that lives in its own image, because CPU search has a fundamentally different scaling profile from web traffic.

---

Next: [`02-system-shape.md`](02-system-shape.md) — the architecture in pictures.
