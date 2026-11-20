// Package webui serves the buildless administration console embedded in Meloport.
package webui

import (
	"embed"
	"errors"
	"io"
	"net/http"
	"os"
)

//go:embed assets/index.html assets/app.css assets/app.js
var assets embed.FS

// Handler exposes only the console entry point and its explicit static assets.
// API routes belong to the host's separate /api/ mux.
func Handler() http.Handler {
	return HandlerFromDirectory("")
}

// ValidateDirectory checks the explicitly selected live asset directory before
// the service starts. The handler still opens each asset through os.Root so a
// symlink or replacement cannot escape this directory at request time.
func ValidateDirectory(directory string) error {
	if directory == "" {
		return nil
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return errors.New("web asset directory is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("web asset path must be a directory")
	}
	return nil
}

// HandlerFromDirectory serves the embedded production assets when directory is
// empty. In explicit development mode it reads only the three named assets from
// directory on every request, so frontend edits take effect after a refresh.
func HandlerFromDirectory(directory string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		file, contentType := "", ""
		switch r.URL.Path {
		case "/":
			file, contentType = "index.html", "text/html; charset=utf-8"
		case "/assets/app.css":
			file, contentType = "app.css", "text/css; charset=utf-8"
		case "/assets/app.js":
			file, contentType = "app.js", "text/javascript; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		var body []byte
		if directory == "" {
			var err error
			body, err = assets.ReadFile("assets/" + file)
			if err != nil {
				http.Error(w, "asset unavailable", http.StatusInternalServerError)
				return
			}
		} else {
			root, err := os.OpenRoot(directory)
			if err != nil {
				http.Error(w, "asset directory unavailable", http.StatusInternalServerError)
				return
			}
			defer root.Close()
			info, err := root.Lstat(file)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 4*1024*1024 {
				http.Error(w, "invalid web asset", http.StatusInternalServerError)
				return
			}
			asset, err := root.Open(file)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer asset.Close()
			body, err = io.ReadAll(io.LimitReader(asset, 4*1024*1024+1))
			if err != nil || len(body) > 4*1024*1024 {
				http.Error(w, "invalid web asset", http.StatusInternalServerError)
				return
			}
		}
		if len(body) == 0 && file == "index.html" && directory == "" {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		if directory == "" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
}
