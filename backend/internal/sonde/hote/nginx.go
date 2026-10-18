package hote

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
)

var nginxSites = "/etc/nginx/sites-enabled"

// Sites relève les vhosts nginx : quels noms de domaine, vers quels services.
// C'est la carte qui relie une URL publique au conteneur qui la sert — sans
// elle, « le site est lent » ne dit pas quel back regarder.
func Sites() ([]sonde.Site, error) {
	entries, err := os.ReadDir(nginxSites)
	if err != nil {
		return nil, err
	}
	var out []sonde.Site
	for _, e := range entries {
		path := filepath.Join(nginxSites, e.Name())
		site, err := parseSite(path)
		if err != nil || len(site.Noms) == 0 {
			continue
		}
		out = append(out, site)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fichier < out[j].Fichier })
	return out, nil
}

func parseSite(path string) (sonde.Site, error) {
	f, err := os.Open(path)
	if err != nil {
		return sonde.Site{}, err
	}
	defer f.Close()
	site := sonde.Site{Fichier: filepath.Base(path)}
	names := map[string]bool{}
	targets := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		line = strings.TrimSuffix(line, ";")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "server_name":
			for _, n := range fields[1:] {
				if n != "_" && !names[n] {
					names[n] = true
					site.Noms = append(site.Noms, n)
				}
			}
		case "proxy_pass":
			if !targets[fields[1]] {
				targets[fields[1]] = true
				site.Vers = append(site.Vers, fields[1])
			}
		case "listen":
			if strings.Contains(line, "443") || strings.Contains(line, "ssl") {
				site.TLS = true
			}
		}
	}
	return site, sc.Err()
}
