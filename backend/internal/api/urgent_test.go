package api

import (
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

func TestFingerprintStableEtSensible(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 0, 0, 0, time.UTC)
	base := []triage.Item{
		{Source: "mail", De: "Olivier", Titre: "Le devis", Quand: now},
		{Source: "slack", De: "Julie", Titre: "#projet", Quand: now.Add(-time.Hour)},
	}

	// Même contenu, même empreinte : c'est ce qui évite de rappeler le modèle
	// à chaque ouverture de l'app.
	same := []triage.Item{
		{Source: "mail", De: "Olivier", Titre: "Le devis", Quand: now},
		{Source: "slack", De: "Julie", Titre: "#projet", Quand: now.Add(-time.Hour)},
	}
	if fingerprint(base) != fingerprint(same) {
		t.Fatal("deux listes identiques doivent donner la même empreinte")
	}

	// Les compteurs bougent sans que rien de neuf ne soit arrivé : ils ne
	// doivent pas déclencher de régénération.
	noisy := []triage.Item{
		{Source: "mail", De: "Olivier", Titre: "Le devis", Quand: now, Motif: triage.ReasonDirect},
		{Source: "slack", De: "Julie", Titre: "#projet", Quand: now.Add(-time.Hour), Compte: 7},
	}
	if fingerprint(base) != fingerprint(noisy) {
		t.Error("un compteur qui change ne doit pas changer l'empreinte")
	}

	// Un message de plus est un vrai changement.
	extra := append(append([]triage.Item{}, base...),
		triage.Item{Source: "mail", De: "Paul", Titre: "Relance", Quand: now})
	if fingerprint(base) == fingerprint(extra) {
		t.Error("un message supplémentaire doit changer l'empreinte")
	}
}
