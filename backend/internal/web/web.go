// Package web serves the SvelteKit frontend.
//
// The built UI is embedded via embed.FS. During development the dist/
// directory contains only a .gitkeep placeholder, so Handler returns a
// friendly placeholder response. After `make build` (or `make ui-build`),
// dist/ is populated with the SvelteKit static output and is served as a
// regular file server.
//
// During development prefer `make dev`, which runs the Vite dev server with
// HMR alongside the backend and proxies /ws, /api, and /healthz to :8080.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var distFS embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return placeholderHandler()
	}
	return http.FileServer(http.FS(sub))
}

func placeholderHandler() http.Handler {
	const body = "Frequency Bridge backend is running.\n\n" +
		"No UI is embedded in this binary yet.\n" +
		"  - For development:        make dev   (then open the Vite URL)\n" +
		"  - To embed the built UI:  make build (then run ./bin/freqbridge)\n"
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	})
}
