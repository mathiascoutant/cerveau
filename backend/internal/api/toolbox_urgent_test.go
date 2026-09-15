package api

import (
	"errors"
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
)

func tasks() []assistant.TaskView {
	return []assistant.TaskView{
		{
			ID:     "aaaa000000000001",
			Action: "Répondre au mail de Cyril sur les boxes",
			Sources: []assistant.SourceView{
				{Origine: "mail", De: "Cyril Martin", Titre: "Les deux boxes DAW"},
			},
		},
		{
			ID:     "aaaa000000000002",
			Action: "Envoyer le devis à Westent",
			Sources: []assistant.SourceView{
				{Origine: "mail", De: "Olivier Westent", Titre: "Relance devis"},
			},
		},
		{
			ID:     "aaaa000000000003",
			Action: "Trancher sur la date de la démo",
			Sources: []assistant.SourceView{
				{Origine: "#projet", De: "Julie", Titre: "#projet"},
			},
		},
	}
}

// Le rang est la façon la plus courante de désigner une ligne à l'oral, et la
// seule qui ne puisse pas viser deux choses à la fois.
func TestPickUrgentParRang(t *testing.T) {
	cases := map[string]int{
		"le premier":       0,
		"on part sur le 1": 0,
		"la deuxième":      1,
		"3":                2,
		"le dernier":       2,
	}
	for ref, want := range cases {
		got, err := pickUrgent(tasks(), ref)
		if err != nil {
			t.Fatalf("%q : %v", ref, err)
		}
		if got != want {
			t.Errorf("%q : ligne %d, attendu %d", ref, got, want)
		}
	}
}

// Un chiffre dans un sujet n'est pas un rang. Le confondre ouvrirait la
// mauvaise ligne sans que rien ne le signale.
func TestPickUrgentIgnoreLesChiffresDuSujet(t *testing.T) {
	list := []assistant.TaskView{
		{Action: "Relire le contrat", Sources: []assistant.SourceView{{De: "Paul", Titre: "Contrat"}}},
		{Action: "Payer la facture 2024", Sources: []assistant.SourceView{{De: "Compta", Titre: "Facture 2024"}}},
	}
	got, err := pickUrgent(list, "la facture 2024")
	if err != nil {
		t.Fatalf("inattendu : %v", err)
	}
	if got != 1 {
		t.Errorf("ligne %d, attendu 1", got)
	}
}

// « le mail de Cyril » doit tomber sur Cyril, pas sur le premier venu.
func TestPickUrgentParNomEtParSujet(t *testing.T) {
	cases := map[string]int{
		"le mail de Cyril": 0,
		"Westent":          1,
		"le devis":         1,
		"la démo":          2,
	}
	for ref, want := range cases {
		got, err := pickUrgent(tasks(), ref)
		if err != nil {
			t.Fatalf("%q : %v", ref, err)
		}
		if got != want {
			t.Errorf("%q : ligne %d, attendu %d", ref, got, want)
		}
	}
}

// L'identifiant exact est ce que l'app repasse quand le geste vient de l'écran.
func TestPickUrgentParIdentifiant(t *testing.T) {
	got, err := pickUrgent(tasks(), "aaaa000000000002")
	if err != nil {
		t.Fatalf("inattendu : %v", err)
	}
	if got != 1 {
		t.Errorf("ligne %d, attendu 1", got)
	}
}

// Ne rien trouver doit se dire, jamais se deviner : ouvrir la mauvaise ligne
// produit un compte rendu faux que personne n'ira vérifier.
func TestPickUrgentNeDevinePas(t *testing.T) {
	if _, err := pickUrgent(tasks(), "le dossier assurance"); err == nil {
		t.Fatal("une recherche sans correspondance doit échouer")
	}
	if _, err := pickUrgent(tasks(), "le huitième"); err == nil {
		t.Fatal("un rang hors liste doit échouer")
	}
	if _, err := pickUrgent(nil, "le premier"); err == nil {
		t.Fatal("une liste vide doit échouer")
	}
	// Sans précision et avec plusieurs candidats, on demande au lieu de choisir.
	if _, err := pickUrgent(tasks(), ""); err == nil {
		t.Fatal("une référence vide doit échouer quand la liste en compte plusieurs")
	}
	// Une seule ligne : « c'est bon » ne peut désigner qu'elle.
	if got, err := pickUrgent(tasks()[:1], ""); err != nil || got != 0 {
		t.Fatalf("ligne unique : %d, %v", got, err)
	}
}

// Deux Cyril dans la liste : on pose la question, on ne tranche pas.
func TestPickUrgentRemonteLAmbiguite(t *testing.T) {
	list := []assistant.TaskView{
		{Action: "Répondre à Cyril Martin", Sources: []assistant.SourceView{{De: "Cyril Martin", Titre: "Boxes"}}},
		{Action: "Répondre à Cyril Dubois", Sources: []assistant.SourceView{{De: "Cyril Dubois", Titre: "Contrat"}}},
	}
	_, err := pickUrgent(list, "Cyril")
	var amb *assistant.AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("attendu une ambiguïté, obtenu %v", err)
	}
	if len(amb.Choix) != 2 {
		t.Errorf("%d choix proposés, attendu 2", len(amb.Choix))
	}
}

// L'identifiant tient aux sources, pas au libellé : le modèle reformule son
// action d'une génération à l'autre sans que le sujet ait bougé, et une ligne
// écartée doit rester écartée.
func TestTaskKeySuitLesSourcesPasLeLibelle(t *testing.T) {
	sources := []assistant.SourceView{
		{Origine: "mail", De: "Cyril", Titre: "Les deux boxes", Quand: "il y a 20 min"},
	}
	a := assistant.TaskView{Action: "Répondre au mail de Cyril", Sources: sources}
	b := assistant.TaskView{Action: "Répondre à Cyril sur les boxes", Sources: sources}
	if taskKey(a) != taskKey(b) {
		t.Fatal("un libellé reformulé ne doit pas changer l'identifiant")
	}

	// La date, elle, ne compte pas : « il y a 20 min » devient « il y a une
	// heure » à la lecture suivante, et la tâche n'a pas changé pour autant.
	c := assistant.TaskView{
		Action:  a.Action,
		Sources: []assistant.SourceView{{Origine: "mail", De: "Cyril", Titre: "Les deux boxes", Quand: "il y a 2 h"}},
	}
	if taskKey(a) != taskKey(c) {
		t.Fatal("l'heure relative ne doit pas entrer dans l'identifiant")
	}

	// Un autre sujet est une autre tâche.
	d := assistant.TaskView{
		Action:  a.Action,
		Sources: []assistant.SourceView{{Origine: "mail", De: "Cyril", Titre: "Le contrat"}},
	}
	if taskKey(a) == taskKey(d) {
		t.Fatal("deux sujets distincts ne doivent pas partager un identifiant")
	}
}

func TestWhatsAppOrigin(t *testing.T) {
	if name, ok := whatsAppOrigin("Azul technique (WhatsApp)"); !ok || name != "Azul technique" {
		t.Errorf("nom %q, reconnu %v", name, ok)
	}
	if _, ok := whatsAppOrigin("#projet"); ok {
		t.Error("un canal Slack ne doit pas passer pour une conversation WhatsApp")
	}
}
