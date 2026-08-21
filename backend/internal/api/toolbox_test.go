package api

import (
	"strings"
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Les horodatages Slack et IMAP arrivent en UTC. C'est le fuseau de
// l'utilisateur qui doit décider du jour : formatés en heure du serveur, tous
// les horaires étaient décalés de deux heures l'été.
func TestToolboxWhenUsesUserTimezone(t *testing.T) {
	tb := &userToolbox{
		srv:  &Server{cfg: config.Config{DefaultTimezone: "Europe/Paris"}},
		user: &store.User{Timezone: "Europe/Paris"},
	}

	// 22h30 UTC = 00h30 le lendemain à Paris.
	got := tb.when(time.Date(2026, 8, 20, 22, 30, 0, 0, time.UTC))
	if !strings.Contains(got, "00h30") {
		t.Errorf("%q : l'heure devrait être celle de Paris", got)
	}
}

// Sans fuseau sur l'utilisateur, celui du serveur prend le relais plutôt que
// de retomber silencieusement sur UTC.
func TestToolboxLocationFallsBackToConfig(t *testing.T) {
	tb := &userToolbox{
		srv:  &Server{cfg: config.Config{DefaultTimezone: "Europe/Paris"}},
		user: &store.User{},
	}
	if name := tb.location().String(); name != "Europe/Paris" {
		t.Errorf("fuseau %q, attendu Europe/Paris", name)
	}
}

func TestToolboxWhenEmptyTimestamp(t *testing.T) {
	tb := &userToolbox{
		srv:  &Server{cfg: config.Config{DefaultTimezone: "Europe/Paris"}},
		user: &store.User{},
	}
	if got := tb.when(time.Time{}); got != "" {
		t.Errorf("un horodatage absent doit rendre une chaîne vide, obtenu %q", got)
	}
}
