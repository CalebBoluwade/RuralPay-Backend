package middleware

import (
	"net/http"
)

func StaticFileServer(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		fs.ServeHTTP(w, r)
	})
}
