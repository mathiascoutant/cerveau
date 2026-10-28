package api

import (
	"strings"
	"testing"
)

func TestLireSortie(t *testing.T) {
	ok := []string{
		`{"resume":"2 jobs","ton":"ok","blocs":[{"type":"chiffres","elements":[{"label":"En cours","valeur":2,"ton":"info"}]}]}`,
		`{"blocs":[{"type":"tableau","colonnes":[{"cle":"job","label":"Job"},{"cle":"p","label":"Progression","format":"progression"}],"lignes":[{"job":"a","p":40}]}]}`,
		"lecture de pxsky…\n" + `{"blocs":[{"type":"texte","texte":"pxsky injoignable","ton":"danger"}]}`,
	}
	for _, s := range ok {
		if _, err := lireSortie(s); err != nil {
			t.Errorf("%s : %v", s, err)
		}
	}
	ko := map[string]string{
		"":                           "rien imprimé",
		`pas du json`:                "illisible",
		`{"blocs":[]}`:               "aucun bloc",
		`{"blocs":[{"type":"gif"}]}`: "type inconnu",
		`{"blocs":[{"type":"tableau","lignes":[]}]}`:                                   "colonnes",
		`{"blocs":[{"type":"tableau","colonnes":[{"cle":"a","format":"camembert"}]}]}`: "format de colonne",
		`{"blocs":[{"type":"chiffres","elements":[{"label":"x","ton":"rouge"}]}]}`:     "ton inconnu",
		`{"blocs":[{"type":"texte","texte":"  "}]}`:                                    "vide",
	}
	for s, attendu := range ko {
		_, err := lireSortie(s)
		if err == nil || !strings.Contains(err.Error(), attendu) {
			t.Errorf("%q : erreur %v, attendu %q", s, err, attendu)
		}
	}
}
