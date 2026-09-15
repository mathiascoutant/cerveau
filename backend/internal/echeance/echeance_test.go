package echeance

import (
	"testing"
	"time"
)

var paris = mustLoad()

func mustLoad() *time.Location {
	l, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		panic(err)
	}
	return l
}

// Mardi 8 septembre 2026, 10h00.
func recu() time.Time { return time.Date(2026, 9, 8, 10, 0, 0, 0, paris) }

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, paris)
}

func TestTrouveLesFormes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Time
		hour bool
	}{
		{"jour de semaine", "Peux-tu me répondre avant vendredi ?", day(2026, 9, 11), false},
		{"jour de semaine prochain", "Il me le faut d'ici lundi prochain.", day(2026, 9, 21), false},
		{"date chiffrée", "Réponse attendue avant le 15/09.", day(2026, 9, 15), false},
		{"date en lettres", "Au plus tard le 3 octobre, merci.", day(2026, 10, 3), false},
		{"date avec année", "Échéance : 12 janvier 2027 pour le rendu.", day(2027, 1, 12), false},
		{"demain", "J'ai besoin de ça avant demain.", day(2026, 9, 9), false},
		{"durée en jours", "Merci de confirmer sous 3 jours.", day(2026, 9, 11), false},
		{"fin de semaine", "On boucle avant la fin de la semaine.", day(2026, 9, 11), false},
		{"fin du mois", "Facture à régler avant la fin du mois.", day(2026, 9, 30), false},
		{"anglais", "Please confirm before September 15.", time.Time{}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Trouver(c.body, recu(), paris)
			if c.want.IsZero() {
				if got != nil {
					t.Fatalf("attendu aucune échéance, obtenu %v", got.Quand)
				}
				return
			}
			if got == nil {
				t.Fatalf("aucune échéance trouvée dans %q", c.body)
			}
			if !got.Quand.Equal(c.want) {
				t.Errorf("échéance %v, attendu %v", got.Quand, c.want)
			}
			if got.Heure != c.hour {
				t.Errorf("heure précisée = %v, attendu %v", got.Heure, c.hour)
			}
		})
	}
}

// Une heure seule se rapporte au jour du message, et elle se signale comme
// heure : « avant 18h » et « avant vendredi » ne s'annoncent pas pareil.
func TestHeureSeule(t *testing.T) {
	got := Trouver("Il me faut ta réponse avant 18h.", recu(), paris)
	if got == nil {
		t.Fatal("aucune échéance trouvée")
	}
	want := time.Date(2026, 9, 8, 18, 0, 0, 0, paris)
	if !got.Quand.Equal(want) {
		t.Errorf("échéance %v, attendu %v", got.Quand, want)
	}
	if !got.Heure {
		t.Error("l'heure devrait être signalée comme précisée")
	}
}

// LE CŒUR DU PAQUET. Une date sans marqueur d'échéance n'est pas une échéance.
// Sans cette règle, tout mail qui évoque un jour en fabrique une, et une urgence
// inventée fait cesser de croire celles qui existent.
func TestIgnoreLesDatesSansMarqueur(t *testing.T) {
	quiet := []string{
		"On s'est vus vendredi, c'était bien.",
		"La réunion de lundi a été annulée.",
		"Je t'ai envoyé ça le 3 septembre.",
		"Bonne journée, à bientôt.",
		"Rendez-vous pris pour information.",
		"Ça marche jusqu'à présent.",
	}
	for _, body := range quiet {
		if got := Trouver(body, recu(), paris); got != nil {
			t.Errorf("%q : échéance inventée (%v, extrait %q)", body, got.Quand, got.Extrait)
		}
	}
}

// Le point de référence est la date du MESSAGE, pas l'instant présent. C'est
// toute la raison d'être du paquet : « avant vendredi » dans un mail vieux de
// dix jours désigne un vendredi déjà passé.
func TestRelatifAuMessagePasAMaintenant(t *testing.T) {
	vieux := time.Date(2026, 8, 25, 9, 0, 0, 0, paris) // mardi 25 août
	got := Trouver("Réponse attendue avant vendredi.", vieux, paris)
	if got == nil {
		t.Fatal("aucune échéance trouvée")
	}
	if want := day(2026, 8, 28); !got.Quand.Equal(want) {
		t.Errorf("échéance %v, attendu %v — elle doit partir du message", got.Quand, want)
	}
}

// Le jour même compte : « avant vendredi » écrit un vendredi matin vise ce
// vendredi-là. Le renvoyer à la semaine suivante ferait rater la demande.
func TestJourMemeCompte(t *testing.T) {
	vendredi := time.Date(2026, 9, 11, 8, 0, 0, 0, paris)
	got := Trouver("Il me le faut avant vendredi soir.", vendredi, paris)
	if got == nil {
		t.Fatal("aucune échéance trouvée")
	}
	if want := day(2026, 9, 11); !got.Quand.Equal(want) {
		t.Errorf("échéance %v, attendu %v", got.Quand, want)
	}
}

// Une date sans année qui semble passée vise l'année suivante : « avant le
// 3 janvier » reçu le 20 décembre ne parle pas du mois de janvier écoulé.
func TestAnneeImpliciteVersLAvant(t *testing.T) {
	decembre := time.Date(2026, 12, 20, 9, 0, 0, 0, paris)
	got := Trouver("Merci de régler avant le 3 janvier.", decembre, paris)
	if got == nil {
		t.Fatal("aucune échéance trouvée")
	}
	if want := day(2027, 1, 3); !got.Quand.Equal(want) {
		t.Errorf("échéance %v, attendu %v", got.Quand, want)
	}
}

// Deux dates : la première porte la demande, la seconde nuance.
func TestPremiereEnOrdreDeLecture(t *testing.T) {
	body := "Il me faut ton retour avant jeudi. Au plus tard le 30 septembre si tu es pris."
	got := Trouver(body, recu(), paris)
	if got == nil {
		t.Fatal("aucune échéance trouvée")
	}
	if want := day(2026, 9, 10); !got.Quand.Equal(want) {
		t.Errorf("échéance %v, attendu %v", got.Quand, want)
	}
}

// L'extrait rend les mots tels qu'ils étaient écrits : c'est ce qui permet de
// dire d'où sort la date quand elle surprend.
func TestExtraitCiteLeTexte(t *testing.T) {
	got := Trouver("Merci de répondre AVANT VENDREDI.", recu(), paris)
	if got == nil {
		t.Fatal("aucune échéance trouvée")
	}
	if got.Extrait != "AVANT VENDREDI" {
		t.Errorf("extrait %q, attendu %q", got.Extrait, "AVANT VENDREDI")
	}
}

func TestSansDate(t *testing.T) {
	if got := Trouver("Merci pour ton retour, c'est parfait.", recu(), paris); got != nil {
		t.Errorf("échéance inventée : %v", got.Quand)
	}
	if got := Trouver("avant vendredi", time.Time{}, paris); got != nil {
		t.Error("sans date de réception, rien ne doit être résolu")
	}
}
