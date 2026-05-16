package main

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
)

//go:embed all:dist
var frontendDist embed.FS

// replayDataPlaceholder is the literal token inside dist/replay.html
// that handleReplay rewrites with the game's ReplayFrame JSON before
// serving. Kept in sync with frontend/replay.html.
const replayDataPlaceholder = "REPLAY_DATA_PLACEHOLDER"

var (
	indexHTML  []byte
	replayHTML []byte
	assetsFS   http.FileSystem
)

func init() {
	var err error
	indexHTML, err = frontendDist.ReadFile("dist/index.html")
	if err != nil {
		indexHTML = []byte("<html><body>Frontend not built.</body></html>")
	}

	replayHTML, err = frontendDist.ReadFile("dist/replay.html")
	if err != nil {
		// Stub keeps the route alive even if the replay build step is
		// skipped (e.g. a dummy frontend in a backend-only CI run).
		replayHTML = []byte("<html><body>Replay not built.</body></html>")
	}

	sub, err := fs.Sub(frontendDist, "dist")
	if err != nil {
		slog.Error("failed to create assets sub-filesystem", "error", err)
	} else {
		assetsFS = http.FS(sub)
	}
}

func (gw *Gateway) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Anything with a file extension is treated as a static asset and
	// served from the embedded dist filesystem. That covers /assets/* (the
	// hashed JS/CSS bundles), /favicon.svg, and any other public/ file
	// Vite drops at the dist root. SPA routes are extension-less so they
	// fall through to index.html below.
	if path.Ext(r.URL.Path) != "" {
		if assetsFS == nil {
			http.NotFound(w, r)
			return
		}
		http.FileServer(assetsFS).ServeHTTP(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write(indexHTML)
}
