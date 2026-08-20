package api

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// StartNavigation renvoie l'action que l'app exécutera : ouvrir Waze. Le
// serveur ne calcule aucun itinéraire — il n'a pas de service de routage, et
// Waze fait ce travail bien mieux une fois lancé.
//
// Le seul travail utile ici est de transformer un nom prononcé à l'oral en
// adresse exacte quand c'est possible, en fouillant les rendez-vous passés de
// l'agenda. « PXCom » devient « 12 rue Rivay, Levallois-Perret », et Waze part
// sur une adresse plutôt que sur une recherche approximative.
func (t *userToolbox) StartNavigation(ctx context.Context, destination string) (assistant.NavigationView, store.Action, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return assistant.NavigationView{}, store.Action{}, errors.New("destination manquante")
	}

	view := assistant.NavigationView{Destination: destination, Source: "recherche"}

	places, err := t.srv.store.KnownPlaces(ctx, t.user.ID)
	if err == nil {
		if addr, ok := matchPlace(places, destination); ok {
			view.Adresse = addr
			view.Source = "agenda"
		}
	}

	// L'app choisit : l'adresse si on l'a, le nom brut sinon.
	return view, store.Action{
		Type: "navigate",
		Payload: map[string]any{
			"label":   destination,
			"address": view.Adresse,
		},
	}, nil
}

// matchPlace retrouve l'adresse d'un lieu à partir de son nom parlé.
//
// L'appariement est volontairement tolérant : la demande arrive d'une
// reconnaissance vocale, donc sans casse fiable, sans accents garantis, et
// souvent précédée d'un article (« à la gare de Lyon »). On regarde d'abord le
// titre du rendez-vous, qui porte presque toujours le nom de l'entreprise,
// puis l'adresse elle-même.
func matchPlace(places []store.KnownPlace, query string) (string, bool) {
	want := normalizePlace(query)
	if want == "" {
		return "", false
	}

	var partial string
	for _, p := range places {
		for _, cand := range []string{normalizePlace(p.Titre), normalizePlace(p.Adresse)} {
			if cand == "" {
				continue
			}
			if cand == want {
				return p.Adresse, true
			}
			// `places` est trié du plus récent au plus ancien : la première
			// correspondance partielle est la plus fraîche, on garde celle-là.
			if partial == "" && strings.Contains(cand, want) {
				partial = p.Adresse
			}
		}
	}
	return partial, partial != ""
}

// Mots vides qui précèdent un lieu à l'oral et n'aident pas à l'identifier.
var placeFillers = []string{"a ", "au ", "aux ", "chez ", "le ", "la ", "les ", "l ", "vers ", "jusqu a "}

func normalizePlace(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if folded, ok := accentFolding[r]; ok {
			return folded
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	s = strings.Join(strings.Fields(s), " ")

	// Les articles s'empilent : « à la gare » en porte deux.
	for changed := true; changed; {
		changed = false
		for _, f := range placeFillers {
			if strings.HasPrefix(s, f) {
				s, changed = strings.TrimPrefix(s, f), true
				break
			}
		}
	}
	return s
}

// accentFolding réduit les voyelles accentuées à leur forme nue : la dictée
// écrit « Levallois » là où l'agenda porte « Lévallois », et l'inverse.
var accentFolding = map[rune]rune{
	'à': 'a', 'â': 'a', 'ä': 'a', 'á': 'a', 'ã': 'a', 'å': 'a',
	'ç': 'c',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
}
