package handler

import (
	"net/http"
	"path"
	"path/filepath"
	"strings"
)

// demoMediaExtensions is the allowlist of media types the demo route serves.
// Restricting by extension keeps the route from serving anything else that
// happens to live in the demo directory.
var demoMediaExtensions = map[string]bool{
	".mp4":  true,
	".webm": true,
	".m4a":  true,
	".mp3":  true,
	".wav":  true,
	".ogg":  true,
}

// demoMediaHandler serves bundled demo media from dir. The filename comes from
// the path and is reduced to its base, so it can never escape dir; only
// allowlisted media extensions are served, and a missing or rejected file
// answers 404. http.ServeFile sets the content type and supports range
// requests so the browser can seek the clip.
func demoMediaHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name == "" || path.Base(name) != name || !demoMediaExtensions[strings.ToLower(filepath.Ext(name))] {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	}
}
