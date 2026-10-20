package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// L'atelier ne reçoit que des outils de lecture : jamais de brouillon, de
// tâche, d'événement, de mémoire écrite ni de passage de parole.
func TestOutilsPretesEnLectureSeule(t *testing.T) {
	src := Sources{Mail: true, MailPerso: true, Slack: true, WhatsApp: true, CSP: true}
	specs := ReadToolSpecs(src)
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		if s.Description == "" || s.Schema["type"] != "object" {
			t.Errorf("%s : description ou schéma manquant", s.Name)
		}
	}
	for _, want := range []string{"chercher_mails", "lire_mail", "chercher_slack", "lire_conversation_whatsapp", "consulter_calendrier"} {
		if !names[want] {
			t.Errorf("outil de lecture absent : %s", want)
		}
	}
	for _, interdit := range []string{"preparer_reponse_mail", "modifier_brouillon", "creer_evenement", "ajouter_tache", "supprimer_tache", "retenir", "oublier", "urgence_traitee", "lancer_navigation", "passer_la_parole", "debriefer"} {
		if names[interdit] {
			t.Errorf("outil d'écriture prêté : %s", interdit)
		}
	}
	// Sans Slack, pas d'outil Slack.
	for _, s := range ReadToolSpecs(Sources{Mail: true}) {
		if strings.Contains(s.Name, "slack") {
			t.Errorf("outil Slack sans Slack : %s", s.Name)
		}
	}
	e := New("cle", "", "")
	if _, err := e.RunReadTool(context.Background(), nil, src, time.UTC, "ajouter_tache", `{"titre":"x"}`); err == nil {
		t.Error("un outil d'écriture doit être refusé")
	}
}
