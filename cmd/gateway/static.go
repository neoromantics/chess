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

var (
	indexHTML []byte
	assetsFS  http.FileSystem
)

func init() {
	var err error
	indexHTML, err = frontendDist.ReadFile("dist/index.html")
	if err != nil {
		indexHTML = []byte("<html><body>Frontend not built. Run 'just build'</body></html>")
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
