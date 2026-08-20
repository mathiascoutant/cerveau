package api

import (
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Les lieux arrivent d'une reconnaissance vocale : ni casse, ni accents, ni
// articles ne sont fiables. Le carnet d'adresses, lui, vient de l'agenda et
// porte des intitulés de rendez-vous.
func TestMatchPlace(t *testing.T) {
	// Ordre du plus récent au plus ancien, comme KnownPlaces les renvoie.
	places := []store.KnownPlace{
		{Titre: "Point hebdo PXCom", Adresse: "12 rue Rivay, Levallois-Perret"},
		{Titre: "Déjeuner Olivier", Adresse: "3 place Gambetta, Paris"},
		{Titre: "Comité", Adresse: "Gare de Lyon, Paris"},
		{Titre: "Vieux rendez-vous PXCom", Adresse: "1 avenue ancienne, Paris"},
	}

	cases := map[string]string{
		"PXCom":           "12 rue Rivay, Levallois-Perret",
		"pxcom":           "12 rue Rivay, Levallois-Perret",
		"à PXCom":         "12 rue Rivay, Levallois-Perret",
		"la gare de Lyon": "Gare de Lyon, Paris",
		"chez Olivier":    "3 place Gambetta, Paris",
		"12 rue Rivay":    "12 rue Rivay, Levallois-Perret",
		"rue Rivay":       "12 rue Rivay, Levallois-Perret",
		"Lévallois":       "12 rue Rivay, Levallois-Perret",
		"Point hebdo":     "12 rue Rivay, Levallois-Perret",
	}
	for query, want := range cases {
		got, ok := matchPlace(places, query)
		if !ok {
			t.Errorf("matchPlace(%q) : rien trouvé, attendu %q", query, want)
			continue
		}
		if got != want {
			t.Errorf("matchPlace(%q) = %q, attendu %q", query, got, want)
		}
	}

	// Un lieu jamais vu doit laisser Waze chercher, pas renvoyer n'importe quoi.
	for _, unknown := range []string{"Décathlon Vélizy", "", "   "} {
		if addr, ok := matchPlace(places, unknown); ok {
			t.Errorf("matchPlace(%q) a trouvé %q, attendu aucun résultat", unknown, addr)
		}
	}
}

func TestNormalizePlace(t *testing.T) {
	cases := map[string]string{
		"À la Gare de Lyon": "gare de lyon",
		"chez  Olivier ":    "olivier",
		"PX-Com":            "px com",
		"aux Halles":        "halles",
		"12, rue Rivay":     "12 rue rivay",
		"Levallois-Perret":  "levallois perret",
	}
	for in, want := range cases {
		if got := normalizePlace(in); got != want {
			t.Errorf("normalizePlace(%q) = %q, attendu %q", in, got, want)
		}
	}
}
