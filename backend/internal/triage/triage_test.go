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
