package slack

import (
	"context"
	"testing"
)

func channels(names ...string) []conversation {
	out := make([]conversation, 0, len(names))
	for _, n := range names {
		out = append(out, conversation{ID: "C" + n, Name: n})
	}
	return out
}

// Le cas réel : « dis-moi le dernier message du canal dubaiairwing » revient de
// la dictée en « dubai R wing ». Le canal existe, il doit être trouvé.
func TestMatchConversationSurvivesDictation(t *testing.T) {
	convs := channels("dubaiairwing", "recrutement", "general", "dev-backend")
	c := &Client{}

	cases := []string{
		"dubai R wing",
		"dubai air wing",
		"le canal dubai airwing",
		"Dubaï Airwing",
		"#dubaiairwing",
	}
	for _, query := range cases {
		got, ambiguous, ok := matchConversation(context.Background(), c, convs, query)
		if !ok || got.Name != "dubaiairwing" {
			t.Errorf("%q → %q (ambigu : %v)", query, got.Name, ambiguous)
		}
	}
}

// Et il l'emporte franchement, même entouré de canaux qui partagent la moitié
// de son nom : la lettre épelée reconstitue le nom exact, les voisins n'en
// approchent que des morceaux.
func TestMatchConversationSpelledLetterBeatsNeighbours(t *testing.T) {
	convs := channels("dubai-ops", "airwing-archive", "dubaiairwing", "dubai-travel")
	got, ambiguous, ok := matchConversation(context.Background(), &Client{}, convs, "dubai R wing")
	if !ok {
		t.Fatalf("non résolu (ambigu : %v)", ambiguous)
	}
	if got.Name != "dubaiairwing" {
		t.Errorf("canal choisi : %q", got.Name)
	}
}

// Un nom qui ne désigne rien ne doit pas se faire attribuer le canal le moins
// éloigné : mieux vaut la liste des noms proches qu'une lecture du mauvais canal.
func TestMatchConversationRejectsUnrelated(t *testing.T) {
	convs := channels("dubaiairwing", "recrutement")
	if _, _, ok := matchConversation(context.Background(), &Client{}, convs, "comptabilité"); ok {
		t.Error("« comptabilité » ne désigne aucun de ces canaux")
	}
}

// Deux canaux qui se valent : on demande lequel, on ne tranche pas.
func TestMatchConversationAsksWhenTied(t *testing.T) {
	convs := channels("projet-alpha", "projet-beta")
	_, ambiguous, ok := matchConversation(context.Background(), &Client{}, convs, "projet")
	if ok {
		t.Fatal("deux canaux également proches ne se départagent pas tout seuls")
	}
	if len(ambiguous) != 2 {
		t.Errorf("choix proposés : %v", ambiguous)
	}
}

// Mais un nom exact l'emporte sur ses voisins, même s'ils se ressemblent.
func TestMatchConversationExactWins(t *testing.T) {
	convs := channels("projet", "projet-alpha", "projet-beta")
	got, _, ok := matchConversation(context.Background(), &Client{}, convs, "projet")
	if !ok || got.Name != "projet" {
		t.Errorf("obtenu %q (%v)", got.Name, ok)
	}
}

// « les-devs » ne doit pas perdre son article : la variante nettoyée est
// essayée EN PLUS du nom entier, jamais à sa place.
func TestSpokenVariantsKeepsOriginal(t *testing.T) {
	convs := channels("les-devs", "devops")
	got, _, ok := matchConversation(context.Background(), &Client{}, convs, "les devs")
	if !ok || got.Name != "les-devs" {
		t.Errorf("obtenu %q (%v)", got.Name, ok)
	}
}
