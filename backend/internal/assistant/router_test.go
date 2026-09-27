package assistant

import (
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		text string
		prev *Turn
		want Tier
	}{
		// Rapide : ce qui ne touche aucune donnée.
		{"Bonjour Raoul", nil, TierFast},
		{"merci !", nil, TierFast},
		{"Explique-moi ce qu'est une API.", nil, TierFast},
		{"C'est quoi le protocole IMAP ?", nil, TierFast},
		{"ça va ?", nil, TierFast},

		// Outils : dès qu'une source est en jeu, même de loin.
		{"C'est quoi le dernier mail que j'ai envoyé ?", nil, TierTools},
		{"Qu'est-ce que Hebat m'a dit par mail ?", nil, TierTools},
		{"Qu'est-ce que Xavier a demandé aujourd'hui sur Slack ?", nil, TierTools},
		{"Qui a demandé à Xavier de faire la mise à jour du CSP ?", nil, TierTools},
		{"Est-ce que Thomas a répondu à Marie concernant le problème ?", nil, TierTools},
		{"Explique-moi le mail de Cyril", nil, TierTools},
		{"Je peux aller faire du sport à 10h demain ?", nil, TierTools},
		{"Note que je dois relancer Olivier", nil, TierTools},
		// Une suite sans mot de source, mais dans un fil outillé.
		{"et lui, il a répondu quoi ?", &Turn{User: "lis le mail de Cyril"}, TierTools},
		// Une question inconnue va aux outils, jamais au rapide.
		{"Tu penses quoi de la proposition de Paul ?", nil, TierTools},

		// Fort : croiser ou reconstituer.
		{"Compare le ticket CSP 101 avec ce que Xavier a dit sur Slack", nil, TierDeep},
		{"Qui a demandé quoi à qui dans cette conversation ?", nil, TierDeep},
		{"Fais-moi une synthèse des mails de Hebat et des messages Slack du canal projet", nil, TierDeep},
	}
	for _, c := range cases {
		got, reason := Classify(c.text, c.prev)
		if got != c.want {
			t.Errorf("%q → %s (%s), attendu %s", c.text, got, reason, c.want)
		}
	}
}

func TestDecideFallsBackWithoutOptionalModels(t *testing.T) {
	m := Models{Tools: "gpt-5.4-mini", ToolsEffort: "low", FastEffort: "none"}
	if r := m.Decide("Bonjour", nil); r.Tier != TierFast || r.Model != "gpt-5.4-mini" {
		t.Errorf("sans modèle rapide, l'étage rapide tourne sur le principal : %+v", r)
	}
	if r := m.Decide("Compare le ticket CSP et le mail de Cyril", nil); r.Tier != TierTools {
		t.Errorf("sans modèle fort, on reste sur les outils : %+v", r)
	}
	full := Models{Fast: "gpt-5.4-nano", Tools: "gpt-5.4-mini", Deep: "gpt-5.4", Deep2: "medium"}
	if r := full.Decide("Compare le ticket CSP et le mail de Cyril", nil); r.Tier != TierDeep || r.Model != "gpt-5.4" {
		t.Errorf("avec modèle fort : %+v", r)
	}
}

// L'historique est borné : les tours les plus récents survivent, les réponses
// longues sont coupées, et le budget total tient.
func TestTrimHistory(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'a'
	}
	var h []Turn
	for i := 0; i < 20; i++ {
		h = append(h, Turn{User: "question", Assistant: string(long)})
	}
	got := trimHistory(h)
	if len(got) == 0 || len(got) > historyMaxTurns {
		t.Fatalf("%d tours gardés", len(got))
	}
	total := 0
	for _, turn := range got {
		if n := len([]rune(turn.Assistant)); n > historyReplyCap+1 {
			t.Errorf("réponse non coupée : %d runes", n)
		}
		total += len([]rune(turn.User)) + len([]rune(turn.Assistant))
	}
	if total > historyMaxRunes {
		t.Errorf("budget dépassé : %d runes", total)
	}
	if got[len(got)-1].User != "question" {
		t.Error("le dernier tour doit être le plus récent")
	}
}

// Le préfixe de la consigne doit être identique d'une minute à l'autre : c'est
// la condition du cache de prompts. Seule la fin — le contexte du moment —
// peut changer.
func TestSystemPromptKeepsStablePrefix(t *testing.T) {
	src := Sources{Mail: true, Slack: true, CSP: true}
	a := systemPrompt(time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC), "Europe/Paris", "Mathias", "m@x.fr", src, nil)
	b := systemPrompt(time.Date(2026, 9, 28, 17, 45, 0, 0, time.UTC), "Europe/Paris", "Mathias", "m@x.fr", src, nil)
	cut := strings.Index(a, "CONTEXTE DU MOMENT")
	if cut < 0 {
		t.Fatal("section du contexte du moment absente")
	}
	if cut < len(a)*3/4 {
		t.Errorf("le contexte du moment doit être en fin de consigne (position %d sur %d)", cut, len(a))
	}
	if a[:cut] != b[:cut] {
		t.Error("le préfixe de la consigne change avec l'heure : le cache de prompts ne servira jamais")
	}
	for _, want := range []string{"chercher_slack", "mails_envoyes", "tickets_csp", "QUI A ÉCRIT, QUI EST CITÉ"} {
		if !strings.Contains(a, want) {
			t.Errorf("consigne sans %q", want)
		}
	}
	if strings.Contains(fastPrompt(time.Now(), "Europe/Paris", "Mathias"), "chercher_slack") {
		t.Error("la consigne rapide ne doit mentionner aucun outil")
	}
}
