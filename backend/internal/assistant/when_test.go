package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestWhen(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("fuseau : %v", err)
	}
	// Vendredi 21 août 2026, 20h04.
	now := time.Date(2026, 8, 21, 20, 4, 0, 0, paris)

	cases := []struct {
		nom  string
		at   time.Time
		want string
	}{
		{"à l'instant", now.Add(-20 * time.Second), "à l'instant"},
		{"une minute", now.Add(-90 * time.Second), "il y a une minute"},
		{"quelques minutes", now.Add(-25 * time.Minute), "il y a 25 minutes"},
		{"plus tôt aujourd'hui", time.Date(2026, 8, 21, 9, 12, 0, 0, paris), "aujourd'hui à 09h12"},
		// Le cas qui a mis Raoul en défaut : un mail d'hier après-midi.
		{"hier après-midi", time.Date(2026, 8, 20, 16, 30, 0, 0, paris), "hier à 16h30"},
		{"avant-hier", time.Date(2026, 8, 19, 8, 5, 0, 0, paris), "avant-hier à 08h05"},
		{"cette semaine", time.Date(2026, 8, 17, 14, 0, 0, 0, paris), "lundi dernier à 14h00"},
		{"plus ancien", time.Date(2026, 8, 3, 11, 20, 0, 0, paris), "le 3 août à 11h20"},
		{"année précédente", time.Date(2025, 12, 24, 18, 0, 0, 0, paris), "le 24 décembre 2025 à 18h00"},
	}

	for _, c := range cases {
		if got := When(c.at, now, paris); got != c.want {
			t.Errorf("%s : %q, attendu %q", c.nom, got, c.want)
		}
	}
}

// Un horodatage arrive en UTC (Slack, IMAP) : c'est le fuseau de l'utilisateur
// qui décide du jour, sinon un message de 23h à Paris passe pour la veille.
func TestWhenUsesUserTimezone(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, paris)

	// 20 août 22h30 UTC = 21 août 00h30 à Paris.
	utc := time.Date(2026, 8, 20, 22, 30, 0, 0, time.UTC)
	if got := When(utc, now, paris); got != "aujourd'hui à 00h30" {
		t.Errorf("%q, attendu \"aujourd'hui à 00h30\"", got)
	}
}

// Minuit ne doit pas transformer « hier soir » en « il y a quelques heures ».
func TestWhenCountsCalendarDays(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 8, 21, 0, 10, 0, 0, paris)
	at := time.Date(2026, 8, 20, 23, 50, 0, 0, paris)

	if got := When(at, now, paris); got != "il y a 20 minutes" {
		t.Errorf("%q, attendu \"il y a 20 minutes\"", got)
	}

	at = time.Date(2026, 8, 20, 19, 0, 0, 0, paris)
	if got := When(at, now, paris); got != "hier à 19h00" {
		t.Errorf("%q, attendu \"hier à 19h00\"", got)
	}
}

func TestWhenZero(t *testing.T) {
	if got := When(time.Time{}, time.Now(), time.UTC); got != "" {
		t.Errorf("un horodatage absent doit rendre une chaîne vide, obtenu %q", got)
	}
}

// Une ambiguïté n'est pas une panne : le modèle doit recevoir une consigne de
// question, jamais un « Erreur : ».
func TestAmbiguousInstruction(t *testing.T) {
	err := &AmbiguousError{
		Quoi:      "expéditeur",
		Recherche: "cyril",
		Choix:     []string{"Cyril Martin (cyril@orange.fr)", "Cyril Dubois (c.dubois@compta.fr)"},
	}

	got := err.instruction()
	for _, want := range []string{"cyril", "Cyril Martin", "Cyril Dubois", "demande à l'utilisateur"} {
		if !strings.Contains(got, want) {
			t.Errorf("la consigne devrait contenir %q :\n%s", want, got)
		}
	}
	if strings.Contains(got, "Erreur") {
		t.Errorf("la consigne ne doit pas se présenter comme une erreur :\n%s", got)
	}
}
