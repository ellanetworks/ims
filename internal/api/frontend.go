package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

func Frontend(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !exists(fsys, "index.html") {
			http.NotFound(w, r)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)

			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		if !exists(fsys, r.URL.Path) {
			http.ServeFileFS(w, r, fsys, "index.html")
			return
		}

		files.ServeHTTP(w, r)
	})
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, strings.TrimPrefix(path.Clean("/"+name), "/"))
	return err == nil
}
