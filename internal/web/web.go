// Package web serves the embedded single-page console.
//
// The console is one set of static files for the whole instance; only the API
// is per project. The page reads its project from /p/<id>/ in the URL, so the
// asset paths stay absolute and the HTML is served unchanged for any project.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var files embed.FS

func sub() fs.FS {
	s, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return s
}

// assetCache is safe because Vite puts a content hash in every asset filename:
// the URL changes whenever the bytes do. Without it a refresh revalidates every
// chunk (the xterm bundle is ~330 KB), which is exactly the reload the console
// should not pay for.
const assetCache = "public, max-age=31536000, immutable"

// Static serves /static/, at the instance root, for every project.
func Static() http.Handler {
	serve := http.FileServer(http.FS(sub()))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "assets/") {
			w.Header().Set("Cache-Control", assetCache)
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		serve.ServeHTTP(w, r)
	}))
}

// Index serves the console shell at the root of a project. The HTML must be
// revalidated: it names the hashed assets of the running build.
func Index() http.HandlerFunc {
	index, err := files.ReadFile("static/index.html")
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}
}

// Register mounts the whole console on a mux: index at /, assets below
// /static/. Used when a server and the page live in the same namespace.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", Index())
	mux.Handle("GET /static/", Static())
}
