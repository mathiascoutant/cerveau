package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// La preuve doit se trouver dans le message : un modèle qui l'invente, ou
// qui la tire de ce que Mathias a écrit lui-même, ne fait rien remonter.
func TestCitation(t *testing.T) {
	texte := "Bonjour Mathias,\nL'import du cycle 1226 est en échec depuis ce matin, peux-tu regarder ?\nMerci, Johanna"
	cas := []struct {
		nom, preuve string
		ok          bool
	}{
		{"mot pour mot", "L'import du cycle 1226 est en échec depuis ce matin, peux-tu regarder ?", true},
		{"casse, accents et guillemets", "« bonjour mathias, l'import du cycle 1226 est en echec »", true},
		{"abrégée", "Bonjour Mathias […] peux-tu regarder ?", true},
		{"abrégée avec trois points", "L'import du cycle 1226... peux-tu regarder", true},
		{"inventée", "Mathias, peux-tu corriger le bug de connexion ?", false},
		{"un morceau inventé", "Bonjour Mathias … corrige vite le serveur", false},
		{"trop courte", "Merci", false},
		{"vide", "  ", false},
	}
	for _, c := range cas {
		if _, ok := citation(c.preuve, texte); ok != c.ok {
			t.Errorf("%s : citation(%q) = %v, attendu %v", c.nom, c.preuve, ok, c.ok)
		}
	}
}

// Ce qui est cité plus haut dans le fil, et ce que Mathias a écrit, ne
// comptent pas : seul le nouveau message d'un autre peut s'adresser à lui.
func TestTexteDesAutres(t *testing.T) {
	mail := triage.Item{Source: "mail", Apercu: "Olivier, peux-tu relancer l'import ?" +
		"\n    plus tôt dans le fil :\n      Cyril : Mathias, tu peux regarder ce bug ?" +
		"\n    il répond à ce message de toi (Mathias) : Mathias, je regarde demain"}
	got := texteDesAutres([]triage.Item{mail})
	if !strings.Contains(got, "Olivier, peux-tu relancer") {
		t.Fatalf("le corps du mail manque : %q", got)
	}
	if strings.Contains(got, "tu peux regarder ce bug") || strings.Contains(got, "je regarde demain") {
		t.Fatalf("le fil cité ne doit pas servir de preuve : %q", got)
	}

	conv := triage.Item{Source: "slack", Apercu: "derniers messages de la conversation :" +
		"\n      Johanna : @Mathias l'export plante encore" +
		"\n      toi (Mathias) : Mathias s'en occupe, promis" +
		"\n      (le dernier message est de toi : tu as déjà répondu)"}
	got = texteDesAutres([]triage.Item{conv})
	if !strings.Contains(got, "l'export plante encore") {
		t.Fatalf("le message de Johanna manque : %q", got)
	}
	if strings.Contains(got, "promis") || strings.Contains(got, "déjà répondu") {
		t.Fatalf("les lignes de Mathias ne doivent pas servir de preuve : %q", got)
	}
	// Sa propre phrase ne prouve pas qu'on lui parle.
	if _, ok := citation("Mathias s'en occupe, promis", got); ok {
		t.Fatal("une phrase de Mathias a été acceptée comme preuve")
	}
}

// Un « probleme » mal formé ne doit jamais faire perdre le jugement entier.
func TestProblemeJugeTolerant(t *testing.T) {
	cas := map[string]problemeJuge{
		`{"sujets":[{"titre":"A","probleme":{"quoi":"import en échec","pour":"mathias","a_qui":"Mathias","preuve":"peux-tu regarder"}}]}`: {Quoi: "import en échec", Pour: "mathias", AQui: "Mathias", Preuve: "peux-tu regarder"},
		`{"sujets":[{"titre":"A","probleme":"import en échec"}]}`:                                                                         {Quoi: "import en échec"},
		`{"sujets":[{"titre":"A","probleme":null}]}`:                                                                                      {},
		`{"sujets":[{"titre":"A","probleme":""}]}`:                                                                                        {},
		`{"sujets":[{"titre":"A","probleme":[]}]}`:                                                                                        {},
		`{"sujets":[{"titre":"A"}]}`:                                                                                                      {},
	}
	for brut, attendu := range cas {
		var j jugement
		if err := json.Unmarshal([]byte(brut), &j); err != nil {
			t.Errorf("%s : jugement perdu : %v", brut, err)
			continue
		}
		if len(j.Sujets) != 1 || j.Sujets[0].Titre != "A" || j.Sujets[0].Probleme != attendu {
			t.Errorf("%s : %+v, attendu %+v", brut, j.Sujets, attendu)
		}
	}
}

func TestAdresseDirecte(t *testing.T) {
	if adresseDirecte([]triage.Item{{Adressage: triage.AdressageCopie}, {Adressage: triage.AdressageDiffusion}}) {
		t.Error("en copie ou en diffusion : il n'est pas destinataire en titre")
	}
	if !adresseDirecte([]triage.Item{{Adressage: triage.AdressageCopie}, {Adressage: triage.AdressageAvecAutres}}) {
		t.Error("destinataire avec d'autres : c'est le texte qui tranchera")
	}
	if !adresseDirecte([]triage.Item{{Adressage: triage.AdressageReponse}}) {
		t.Error("on répond à son message")
	}
}

// Le modèle sait qui d'autre est destinataire : c'est souvent à l'un d'eux
// qu'on demande.
func TestAdresseA(t *testing.T) {
	it := triage.Item{Source: "mail", Motif: triage.ReasonDirect, Adressage: triage.AdressageAvecAutres,
		Pour: []string{"moi@exemple.fr", "Olivier <olivier@exemple.fr>"}}
	got := adresseA(it)
	if !strings.Contains(got, "avec d'autres") || !strings.Contains(got, "Olivier") {
		t.Errorf("adresseA = %q", got)
	}
	seul := triage.Item{Source: "mail", Adressage: triage.AdressageDirect, Pour: []string{"moi@exemple.fr"}}
	if got := adresseA(seul); got != string(triage.AdressageDirect) {
		t.Errorf("seul destinataire : %q", got)
	}
	if got := adresseA(triage.Item{Source: "slack", Motif: triage.ReasonMention}); got != motif(triage.ReasonMention) {
		t.Errorf("Slack garde son motif : %q", got)
	}
}

func TestLireDiagnostic(t *testing.T) {
	var d store.Diagnostic
	lireDiagnostic("Voici :\n```json\n"+`{"probleme":"L'import Azul 2612 s'arrête à 40 %.","cause":"Deux lignes en doublon dans l'Excel.","constate":"Léo a lu le journal du job.","corrections":[{"quoi":"Relancer l'import en mode check","commande":"pxsky-lancer-import-csp --mode check"},{"quoi":"","commande":""}],"trou":"Aucun contrôle des doublons avant l'import.","confiance":"Haute","manque":""}`+"\n```", &d)
	if d.Texte != "" || d.Cause == "" || d.Compris == "" || d.Confiance != "haute" {
		t.Fatalf("diagnostic mal lu : %+v", d)
	}
	if len(d.Corrections) != 1 || d.Corrections[0].Commande != "pxsky-lancer-import-csp --mode check" {
		t.Fatalf("corrections : %+v", d.Corrections)
	}
	if !strings.Contains(diagnosticEnClair(d), "Trou dans la raquette") {
		t.Errorf("en clair : %q", diagnosticEnClair(d))
	}

	// Une réponse sans la forme attendue est gardée telle quelle.
	var brut store.Diagnostic
	lireDiagnostic("Je n'ai pas réussi à joindre Léo, voici ce que je sais.", &brut)
	if brut.Texte == "" || brut.Cause != "" {
		t.Fatalf("réponse libre perdue : %+v", brut)
	}
}

// La demande dit à Hugo que ce n'est pas Mathias qui parle, et porte la
// méthode et la preuve.
func TestDemandeDiagnostic(t *testing.T) {
	n := store.Notification{Titre: "Import Azul en échec", Lieu: "Azul - 1226 PXCom Updates", Source: "mail", Resume: "L'import s'arrête."}
	d := store.Diagnostic{Preuve: "Mathias, peux-tu regarder ?", Raison: "Johanna te le demande."}
	got := demandeDiagnostic(equipeMembreTest("Inès", "Veille des mails pro"), n, "L'import s'arrête à 40 %.", d, "[1] mail · de Johanna")
	for _, attendu := range []string{"DIAGNOSTIC — Inès", "Mathias, peux-tu regarder ?", "LECTURE SEULE", "trou dans la raquette", "« metier »", `"corrections"`} {
		if !strings.Contains(got, attendu) {
			t.Errorf("la demande ne contient pas %q", attendu)
		}
	}
	d.Demande = true
	if got := demandeDiagnostic(equipeMembreTest("Inès", "Veille des mails pro"), n, "", d, ""); !strings.Contains(got, "Mathias te demande, depuis HQ") {
		t.Errorf("demande depuis HQ : %q", got[:120])
	}
}

func equipeMembreTest(nom, role string) equipe.Membre { return equipe.Membre{Nom: nom, Role: role} }
