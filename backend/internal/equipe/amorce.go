package equipe

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// L'amorce : le savoir de départ de l'équipe.
//
// Un dossier de documents Markdown, posé sur le VPS à côté du registre — pas
// dans le dépôt, qui est public, pour la même raison que le registre. Chaque
// document est une fiche, avec un court en-tête :
//
//	---
//	projet: visionshow
//	sujet: Vue d'ensemble
//	---
//	Le texte de la fiche…
//
// Le dossier est relu à chaque démarrage. Une fiche d'amorce qui change est
// mise à jour ; une fiche que l'équipe a réécrite en travaillant ne l'est plus
// jamais — ce qu'elle a appris vaut mieux que le document de départ.

// Amorceur : ce que l'amorce écrit. *store.Store le satisfait.
type Amorceur interface {
	Amorcer(ctx context.Context, c store.Connaissance) (bool, error)
}

// Amorcer lit le dossier et range ses fiches. Rend le nombre de fiches
// créées ou mises à jour.
func Amorcer(ctx context.Context, dst Amorceur, dir string) (int, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return 0, err
	}
	sort.Strings(paths)
	changed := 0
	for _, path := range paths {
		fiche, err := lireFiche(path)
		if err != nil {
			return changed, fmt.Errorf("%s : %w", filepath.Base(path), err)
		}
		ok, err := dst.Amorcer(ctx, fiche)
		if err != nil {
			return changed, fmt.Errorf("%s : %w", filepath.Base(path), err)
		}
		if ok {
			changed++
		}
	}
	return changed, nil
}

func lireFiche(path string) (store.Connaissance, error) {
	f, err := os.Open(path)
	if err != nil {
		return store.Connaissance{}, err
	}
	defer f.Close()

	c := store.Connaissance{Equipe: EquipeInformatique}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var body []string
	inHeader, headerDone := false, false
	line := 0
	for sc.Scan() {
		text := sc.Text()
		line++
		if line == 1 && strings.TrimSpace(text) == "---" {
			inHeader = true
			continue
		}
		if inHeader && !headerDone {
			if strings.TrimSpace(text) == "---" {
				headerDone = true
				continue
			}
			key, value, ok := strings.Cut(text, ":")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "projet":
				c.Projet = value
			case "sujet":
				c.Sujet = value
			case "equipe", "équipe":
				c.Equipe = value
			}
			continue
		}
		body = append(body, text)
	}
	if err := sc.Err(); err != nil {
		return store.Connaissance{}, err
	}
	c.Contenu = strings.TrimSpace(strings.Join(body, "\n"))
	if c.Projet == "" || c.Sujet == "" {
		return store.Connaissance{}, fmt.Errorf("l'en-tête doit donner « projet » et « sujet »")
	}
	if c.Contenu == "" {
		return store.Connaissance{}, fmt.Errorf("fiche vide")
	}
	return c, nil
}
