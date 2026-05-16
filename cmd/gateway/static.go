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
		// Vite hashes every file in /assets/ (e.g. main-DZo9wGhC.js), so
		// the URL changes on every build — safe to cache aggressively
		// in the browser AND in Traefik. Everything else (favicon,
		// public/ files) keeps the default no-policy behaviour, which
		// the browser interprets as "cache but revalidate". The
		// hashed-assets carve-out is the bulk of bytes anyway.
		if isHashedAsset(r.URL.Path) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServer(assetsFS).ServeHTTP(w, r)
		return
	}

	// index.html is the SPA shell and references the hashed assets by
	// name; if a browser caches it, a deploy can ship a new asset hash
	// but the browser keeps loading the old bundle until its
	// heuristic cache expires (often hours). `no-store` forces a
	// revalidation on every visit — the response is small, and this is
	// the canonical Vite SPA cache policy.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html")
	w.Write(indexHTML)
}

// isHashedAsset reports whether a URL path is a Vite hashed-asset
// output and therefore safe for year-long immutable caching. We match
// the conventional /assets/<name>-<hash>.<ext> shape rather than a
// blanket "any extension" rule so that other public/ files (favicon,
// robots.txt, future static drops) keep the conservative default.
func isHashedAsset(p string) bool {
	const prefix = "/assets/"
	if len(p) <= len(prefix) || p[:len(prefix)] != prefix {
		return false
	}
	return true
}
