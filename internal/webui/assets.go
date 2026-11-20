// Package webui serves the buildless administration console embedded in Meloport.
package webui

import (
	"embed"
	"net/http"
)

//go:embed assets/index.html assets/app.css assets/app.js
var assets embed.FS

// Handler exposes only the console entry point and its explicit static assets.
// API routes belong to the host's separate /api/ mux.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		file, contentType := "", ""
		switch r.URL.Path {
		case "/":
			file, contentType = "assets/index.html", "text/html; charset=utf-8"
		case "/assets/app.css":
			file, contentType = "assets/app.css", "text/css; charset=utf-8"
		case "/assets/app.js":
			file, contentType = "assets/app.js", "text/javascript; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		body, err := assets.ReadFile(file)
		if err != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
}
