package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// spa sert une application monopage construite : les fichiers qui existent
// tels quels, index.html pour tout le reste. Les ressources versionnées
// (assets/) se gardent en cache, index.html jamais — sinon un déploiement ne se
// verrait qu'au prochain vidage du cache.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean("/" + r.URL.Path)
		if info, err := os.Stat(filepath.Join(dir, clean)); err == nil && !info.IsDir() {
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}
