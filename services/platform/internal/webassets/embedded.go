//go:build embedui

package webassets

import (
	"bytes"
	"crypto/rand"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed dist/*
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "dist")
	server := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(name, "api/") {
			http.NotFound(w, r)
			return
		}
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(root, name); err != nil {
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			name = "index.html"
		}
		if name == "index.html" {
			body, err := fs.ReadFile(root, name)
			if err != nil {
				http.Error(w, "Dashboard unavailable", http.StatusInternalServerError)
				return
			}
			nonce := rand.Text()
			policy := w.Header().Get("Content-Security-Policy")
			w.Header().Set("Content-Security-Policy", strings.Replace(policy, "style-src 'self'", "style-src 'self' 'nonce-"+nonce+"'", 1))
			w.Header().Set("Cache-Control", "no-store")
			body = bytes.Replace(body, []byte("<head>"), []byte("<head><meta name=\"style-nonce\" content=\""+nonce+"\">"), 1)
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public,max-age=31536000,immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		server.ServeHTTP(w, r)
	})
}
