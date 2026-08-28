package triage

import (
	"testing"
	"time"
)

const moi = "mathias@neurorun.fr"

func mail(objet, de string, pour, copie []string) Mail {
	return Mail{
		De:      "Quelqu'un",
		Adresse: de,
		Objet:   objet,
		Date:    time.Now(),
		Pour:    pour,
		Copie:   copie,
	}
}

func TestMailsGardeLeDestinataireNomme(t *testing.T) {
	got := Mails([]Mail{
		mail("Le devis de jeudi", "olivier@cabinet.fr", []string{"Mathias <" + moi + ">"}, nil),
	}, moi)
	if len(got) != 1 {
		t.Fatalf("un mail adressé nommément doit remonter, obtenu %d entrées", len(got))
	}
	if got[0].Motif != ReasonDirect {
		t.Errorf("motif = %q, attendu %q", got[0].Motif, ReasonDirect)
	}
}

func TestMailsEcarteLaSimpleCopie(t *testing.T) {
	got := Mails([]Mail{
		mail("Compte rendu", "olivier@cabinet.fr", []string{"equipe@cabinet.fr"}, []string{moi}),
	}, moi)
	if len(got) != 0 {
		t.Fatalf("un mail où l'utilisateur n'est qu'en copie ne doit pas remonter : %+v", got)
	}
}

func TestMailsEcarteLesRobots(t *testing.T) {
	cas := []struct {
		nom   string
		objet string
		de    string
	}{
		{"boîte sans réponse", "Ton relevé est disponible", "no-reply@banque.fr"},
		{"notifications", "Olivier a commenté", "notifications@github.com"},
		{"alerte de connexion", "Tentative de connexion à ton compte", "securite@service.fr"},
		{"code de vérification", "Votre code de vérification est 448192", "hello@service.fr"},
		{"réinitialisation", "Password reset requested", "support@service.fr"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			got := Mails([]Mail{mail(c.objet, c.de, []string{moi}, nil)}, moi)
			if len(got) != 0 {
				t.Fatalf("%s aurait dû être écarté : %+v", c.nom, got)
			}
		})
	}
}

func TestMailsEcarteLaDiffusion(t *testing.T) {
	m := mail("La newsletter du mois", "julie@studio.fr", []string{moi}, nil)
	m.Diffusion = true
	if got := Mails([]Mail{m}, moi); len(got) != 0 {
		t.Fatalf("un message de liste ne doit pas remonter : %+v", got)
	}
}

func TestMailsEcarteLEnvoiEnMasse(t *testing.T) {
	pour := make([]string, 0, massMailing+1)
	pour = append(pour, moi)
	for i := 0; i < massMailing; i++ {
		pour = append(pour, "collegue@cabinet.fr")
	}
	if got := Mails([]Mail{mail("Invitation", "olivier@cabinet.fr", pour, nil)}, moi); len(got) != 0 {
		t.Fatalf("être un destinataire parmi %d n'est pas être visé : %+v", len(pour), got)
	}
}

func TestSlackNeGardeQueCeQuiSAdresseAToi(t *testing.T) {
	got := Slack([]Conversation{
		{Canal: "DM Olivier", Type: "dm", NonLus: 2, Extraits: []string{"Olivier : tu peux relire ?"}},
		{Canal: "#général", Type: "canal", Mentions: 1, Extraits: []string{"Julie : @mathias tu en penses quoi ?"}},
		{Canal: "#random", Type: "canal", NonLus: 12, Extraits: []string{"Paul : bon week-end"}},
	})
	if len(got) != 2 {
		t.Fatalf("attendu 2 entrées (DM + mention), obtenu %d : %+v", len(got), got)
	}
	if got[0].Motif != ReasonDM {
		t.Errorf("le message privé doit porter le motif %q, obtenu %q", ReasonDM, got[0].Motif)
	}
	if got[1].Motif != ReasonMention {
		t.Errorf("la mention doit porter le motif %q, obtenu %q", ReasonMention, got[1].Motif)
	}
	if got[1].De != "Julie" || got[1].Apercu != "@mathias tu en penses quoi ?" {
		t.Errorf("auteur et extrait mal séparés : de=%q aperçu=%q", got[1].De, got[1].Apercu)
	}
}

func TestMergeRangeDuPlusRecentAuPlusAncien(t *testing.T) {
	now := time.Now()
	got := Merge(10,
		[]Item{{Titre: "vieux", Quand: now.Add(-3 * time.Hour)}, {Titre: "indaté"}},
		[]Item{{Titre: "récent", Quand: now}},
	)
	ordre := []string{got[0].Titre, got[1].Titre, got[2].Titre}
	attendu := []string{"récent", "vieux", "indaté"}
	for i := range attendu {
		if ordre[i] != attendu[i] {
			t.Fatalf("ordre = %v, attendu %v", ordre, attendu)
		}
	}
}

func TestMergeTronque(t *testing.T) {
	items := make([]Item, 12)
	if got := Merge(5, items); len(got) != 5 {
		t.Fatalf("limite non appliquée : %d entrées", len(got))
	}
}

// Une réponse à un mail qu'il a envoyé le vise, même quand il n'est qu'en
// copie. C'est le seul cas où la copie passe le tri : on ne répond pas à
// quelqu'un pour information.
func TestReplyToYouPassesEvenInCopy(t *testing.T) {
	const moi = "mathias@pxcom.aero"

	copie := Mail{
		De: "Cyril", Adresse: "cyril@daw.ae", Objet: "Re: les deux boxes",
		Pour: []string{"equipe@daw.ae"}, Copie: []string{"Mathias <" + moi + ">"},
		Date: time.Now(),
	}
	if got := Mails([]Mail{copie}, moi); len(got) != 0 {
		t.Fatalf("une simple copie ne doit pas remonter : %+v", got)
	}

	copie.RepondAToi = true
	got := Mails([]Mail{copie}, moi)
	if len(got) != 1 {
		t.Fatalf("une réponse à son propre mail doit remonter, obtenu %d entrées", len(got))
	}
	if got[0].Motif != ReasonReply {
		t.Errorf("motif %q, attendu %q", got[0].Motif, ReasonReply)
	}
}

// Mais une réponse robotisée reste une réponse robotisée : un accusé
// automatique cite bien son mail, il n'attend rien pour autant.
func TestReplyFromRobotStillFiltered(t *testing.T) {
	m := Mail{
		De: "Support", Adresse: "no-reply@outil.io", Objet: "Re: ticket 42",
		Pour: []string{"mathias@pxcom.aero"}, RepondAToi: true, Date: time.Now(),
	}
	if got := Mails([]Mail{m}, "mathias@pxcom.aero"); len(got) != 0 {
		t.Errorf("un robot qui répond n'attend toujours rien : %+v", got)
	}
}

// L'adressage est ce qu'on donne au modèle à la place des listes d'adresses.
// Il doit nommer la place exacte, pas approximativement.
func TestAddressing(t *testing.T) {
	const moi = "mathias@pxcom.aero"
	moiAddr := "Mathias COUTANT <" + moi + ">"

	cases := []struct {
		nom  string
		mail Mail
		want Adressage
	}{
		{"seul destinataire", Mail{Pour: []string{moiAddr}}, AdressageDirect},
		{"avec d'autres", Mail{Pour: []string{moiAddr, "cyril@daw.ae"}}, AdressageAvecAutres},
		{"en copie", Mail{Pour: []string{"cyril@daw.ae"}, Copie: []string{moiAddr}}, AdressageCopie},
		{"absent des champs", Mail{Pour: []string{"equipe@daw.ae"}}, AdressageAbsent},
		{"diffusion", Mail{Pour: []string{moiAddr}, Diffusion: true}, AdressageDiffusion},
		{"réponse", Mail{Copie: []string{moiAddr}, RepondAToi: true}, AdressageReponse},
		// La réponse prime même sur la diffusion : un fil de liste où l'on
		// répond à son message reste une réponse à son message.
		{"réponse dans une liste", Mail{Diffusion: true, RepondAToi: true}, AdressageReponse},
	}
	for _, c := range cases {
		if got := Addressing(c.mail, moi); got != c.want {
			t.Errorf("%s : %q, attendu %q", c.nom, got, c.want)
		}
	}

	// Un champ « À » démesuré ne vise personne, même si on y figure.
	large := Mail{Pour: make([]string, massMailing+1)}
	large.Pour[0] = moiAddr
	if got := Addressing(large, moi); got != AdressageDiffusion {
		t.Errorf("envoi de masse : %q", got)
	}

	// Sans adresse de référence, on ne prétend rien.
	if got := Addressing(Mail{Pour: []string{moiAddr}}, ""); got != "" {
		t.Errorf("sans adresse connue, l'adressage doit rester vide, obtenu %q", got)
	}
}
