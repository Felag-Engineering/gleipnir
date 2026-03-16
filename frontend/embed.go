package frontend

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// NewSPAHandler creates an http.Handler that serves the embedded frontend
// assets with SPA fallback (unknown paths serve index.html).
func NewSPAHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("frontend: failed to sub dist FS: " + err.Error())
	}
	return newSPAHandler(sub)
}

func newSPAHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean the path — strip leading slash for fs operations.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Check if the file exists in the embedded FS.
		f, err := fsys.Open(path)
		if err != nil {
			// File doesn't exist — serve index.html for SPA client-side routing.
			serveIndexHTML(w, r, fsys)
			return
		}
		f.Close()

		// Vite builds hashed filenames under assets/; these are safe to cache forever.
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}

		fileServer.ServeHTTP(w, r)
	})
}

func serveIndexHTML(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data) //nolint:errcheck
}
