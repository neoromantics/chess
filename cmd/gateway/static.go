package main

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
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
	// If it's a request for a static asset, serve it from the assets filesystem
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		http.FileServer(assetsFS).ServeHTTP(w, r)
		return
	}

	// For all other paths (SPA routing), serve index.html
	if path.Ext(r.URL.Path) != "" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write(indexHTML)
}
