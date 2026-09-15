package store

import "testing"

// Le sujet porte l'identité de la fiche, et il la porte à l'orthographe près :
// la dictée ne garantit ni la casse ni les accents, et « Cyril » prononcé deux
// fois ne doit pas ouvrir deux fiches.
func TestFactKeyFoldsSubject(t *testing.T) {
	same := []struct{ a, b string }{
		{"Cyril", "cyril"},
		{"Cyril", "  CYRIL  "},
		{"Réunions", "reunions"},
		{"les réunions", "Les Reunions"},
	}
	for _, c := range same {
		if factKey(FactPerson, c.a, "") != factKey(FactPerson, c.b, "") {
			t.Errorf("%q et %q devraient désigner la même fiche", c.a, c.b)
		}
	}

	if factKey(FactPerson, "Cyril", "") == factKey(FactPerson, "Olivier", "") {
		t.Error("deux personnes distinctes partagent une clé")
	}
}

// La catégorie fait partie de la clé : un projet nommé comme quelqu'un — « Azul
// » le dossier et « Azul » la personne — reste deux fiches. Les confondre
// écraserait l'une avec l'autre sans que rien ne le signale.
func TestFactKeySeparatesKinds(t *testing.T) {
	if factKey(FactPerson, "Azul", "") == factKey(FactProject, "Azul", "") {
		t.Error("la personne et le projet Azul partagent une clé")
	}
}

// Un fait sans sujet se dédoublonne sur son propre texte. C'est un pis-aller,
// mais il empêche qu'une phrase dite deux fois soit retenue deux fois.
func TestFactKeyFallsBackToContent(t *testing.T) {
	a := factKey(FactOther, "", "Permis A2 passé en 2024")
	b := factKey(FactOther, "", "permis a2 passé en 2024")
	if a != b {
		t.Error("un même fait sans sujet produit deux clés")
	}
	if a == factKey(FactOther, "", "Permis B passé en 2018") {
		t.Error("deux faits distincts partagent une clé")
	}
}
