package api

import (
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/tuleap"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func testToolbox() *userToolbox {
	return &userToolbox{
		srv:  &Server{cfg: config.Config{DefaultTimezone: "Europe/Paris"}},
		user: &store.User{Timezone: "Europe/Paris"},
	}
}

// Un mail de la boîte d'envoi est marqué de_toi et ne porte pas de « pour_toi » ;
// un mail reçu porte sa place parmi les destinataires. C'est la distinction
// qui empêche de présenter ce qu'il a écrit comme ce qu'on lui a dit.
func TestMailHitDistinguishesSentFromReceived(t *testing.T) {
	tb := testToolbox()
	const me = "mathias@pxcom.aero"

	sent := tb.mailHit(gandi.Found{
		Message: gandi.Message{
			From: "Mathias", FromAddr: me, To: []string{"Hebat <hebat@x.fr>"},
			Subject: "Planning", Date: time.Now().Add(-time.Hour),
		},
		Folder: gandi.FolderSent, MessageID: "abc@pxcom",
	}, me)
	if !sent.DeToi || sent.Dossier != "envoyé" || sent.PourToi != "" {
		t.Errorf("mail envoyé mal marqué : %+v", sent)
	}
	if sent.MessageID != "abc@pxcom" {
		t.Errorf("message_id perdu : %+v", sent)
	}

	received := tb.mailHit(gandi.Found{
		Message: gandi.Message{
			From: "Hebat", FromAddr: "hebat@x.fr", To: []string{"Mathias <" + me + ">"},
			Subject: "Re: Planning", Date: time.Now().Add(-30 * time.Minute), AnswersYou: true,
		},
		Folder: gandi.FolderInbox, MessageID: "def@x", Seen: true,
	}, me)
	if received.DeToi || received.Dossier != "reçu" || received.PourToi == "" || !received.ReponseATonMail {
		t.Errorf("mail reçu mal marqué : %+v", received)
	}
	if !received.Lu {
		t.Error("le drapeau lu doit être conservé")
	}

	draft := tb.mailHit(gandi.Found{
		Message: gandi.Message{From: "Mathias", FromAddr: me, Subject: "Brouillon"},
		Folder:  gandi.FolderSent, Draft: true,
	}, me)
	if !draft.Brouillon {
		t.Error("un brouillon doit être signalé comme tel")
	}
}

// Ce qui fait foi sur Slack descend tel quel : l'identifiant de l'auteur, les
// mentions résolues, le parent du fil et le lien.
func TestSlackMessageViewCarriesAttribution(t *testing.T) {
	tb := testToolbox()
	view := tb.slackMessage(slack.Message{
		ID: "1727000000.000100", Canal: "#csp", Auteur: "Thomas", AuteurID: "UTHOMAS",
		Texte: "@Xavier, peux-tu regarder le problème CSP ?", Quand: time.Now().Add(-5 * time.Minute),
		Mentions: []slack.Mention{{ID: "UXAVIER", Nom: "Xavier"}},
		Lien:     "https://acme.slack.com/archives/C1/p1727000000000100",
		Fil: []slack.Message{{
			ID: "1727000050.000200", Auteur: "Xavier", AuteurID: "UXAVIER", Texte: "Je regarde",
			FilDe: "1727000000.000100", Quand: time.Now(),
		}},
	})
	if view.AuteurID != "UTHOMAS" || view.Auteur != "Thomas" {
		t.Errorf("auteur : %+v", view)
	}
	if len(view.Mentions) != 1 || view.Mentions[0].ID != "UXAVIER" {
		t.Errorf("mentions : %+v", view.Mentions)
	}
	if view.Lien == "" || view.ID == "" || view.Canal != "#csp" {
		t.Errorf("identité du message : %+v", view)
	}
	if len(view.Fil) != 1 || view.Fil[0].FilDe != "1727000000.000100" || view.Fil[0].AuteurID != "UXAVIER" {
		t.Errorf("fil : %+v", view.Fil)
	}
}

func TestTicketMatches(t *testing.T) {
	tk := tuleap.Ticket{Ref: "csp #101", Titre: "Écran figé au démarrage", Statut: "En cours", Responsable: "Mathias"}
	for _, q := range []string{"101", "#101", "écran figé", "en cours mathias"} {
		if !ticketMatches(tk, q) {
			t.Errorf("%q devrait correspondre", q)
		}
	}
	if ticketMatches(tk, "firmware") {
		t.Error("« firmware » ne devrait pas correspondre")
	}
}
