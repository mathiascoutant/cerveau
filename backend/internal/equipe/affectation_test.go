package equipe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
)

func equipeAvec(classify Classifier) *Equipe {
	e := New(assistant.New("cle", "", ""), nil, nil, Config{})
	e.classify = classify
	return e
}

func TestAffecterSuitLeModele(t *testing.T) {
	var lu string
	e := equipeAvec(func(_ context.Context, _, input string) (string, error) {
		lu = input
		return "```json\n{\"agent\": \"ingenieur\", \"raison\": \"Il connaît le VPS.\"}\n```", nil
	})
	a, err := e.Affecter(context.Background(), "le site ne répond plus")
	if err != nil || a.Agent != IDIngenieur || a.Par != "modele" || a.Raison != "Il connaît le VPS." {
		t.Fatalf("affectation : %+v, %v", a, err)
	}
	// Le modèle choisit sur les fiches : rôles et compétences, pas Raoul.
	if !strings.Contains(lu, "à solliciter pour") || !strings.Contains(lu, IDResponsable) || strings.Contains(lu, "identifiant : "+IDRaoul) {
		t.Errorf("fiches lues par le modèle :\n%s", lu)
	}
}

func TestAffecterSansModeleUtilisableClasseParMotsCles(t *testing.T) {
	cas := []struct {
		reponse string
		err     error
	}{
		{`{"agent": "raoul", "raison": "x"}`, nil}, // Raoul n'est pas un agent du bâtiment
		{`{"agent": "inconnu"}`, nil},
		{"je ne sais pas", nil},
		{"", errors.New("hors ligne")},
	}
	for _, c := range cas {
		e := equipeAvec(func(context.Context, string, string) (string, error) { return c.reponse, c.err })
		a, err := e.Affecter(context.Background(), "le déploiement a raté, regarde le code et la base de données")
		if err != nil || a.Agent != IDIngenieur || a.Par != "mots-cles" {
			t.Errorf("réponse %q : %+v, %v", c.reponse, a, err)
		}
	}
}

func TestParMotsCles(t *testing.T) {
	cands := equipeAvec(nil).Candidats()
	cas := map[string]string{
		"Il y a une panne sur le VPS, trouve pourquoi":                          IDIngenieur,
		"Une lenteur sur la base de données de neurorun":                        IDIngenieur,
		"Quelles sont les priorités de mes projets perso, et dans quel ordre ?": IDResponsable,
		"Fais-moi une vue d'ensemble de l'infrastructure":                       IDResponsable,
		// Rien en commun avec personne : celle qui répartit le travail.
		"Organise un anniversaire surprise": IDResponsable,
	}
	for demande, attendu := range cas {
		if a := parMotsCles(cands, demande); a.Agent != attendu || a.Raison == "" {
			t.Errorf("%q → %+v, attendu %s", demande, a, attendu)
		}
	}
}

func TestAffecterUnSeulAgent(t *testing.T) {
	e := equipeAvec(func(context.Context, string, string) (string, error) {
		t.Fatal("pas besoin du modèle pour un seul agent")
		return "", nil
	})
	e.roster = []Membre{{ID: IDRaoul}, {ID: IDIngenieur, Nom: "Léo"}}
	if a, err := e.Affecter(context.Background(), "x"); err != nil || a.Agent != IDIngenieur || a.Par != "seul" {
		t.Fatalf("%+v, %v", a, err)
	}
	e.roster = []Membre{{ID: IDRaoul}}
	if _, err := e.Affecter(context.Background(), "x"); !errors.Is(err, ErrPersonne) {
		t.Fatalf("sans agent : %v", err)
	}
}
