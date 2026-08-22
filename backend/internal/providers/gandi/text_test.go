package gandi

import "testing"

func TestHtmlToText(t *testing.T) {
	html := `<html><head><style>p{color:red}</style><script>var a=1;</script></head>
<body><p>Bonjour Mathias,</p><p>Le devis est <b>pr&ecirc;t</b>&nbsp;: 1&nbsp;200&nbsp;&euro;.</p>
<div>Bonne journ&eacute;e</div></body></html>`

	got := collapse(htmlToText(html))
	// Une ligne vide est conservée entre blocs : c'est le rythme de lecture.
	want := "Bonjour Mathias,\nLe devis est prêt : 1 200 €.\n\nBonne journée"
	if got != want {
		t.Errorf("htmlToText :\n  obtenu %q\n  attendu %q", got, want)
	}
}

// Le style et le script ne doivent jamais finir dans ce qui est lu à voix haute.
func TestHtmlToTextDropsCode(t *testing.T) {
	got := htmlToText(`<style>.a{x:1}</style><script>alert("bim")</script><p>Salut</p>`)
	for _, forbidden := range []string{"x:1", "alert", "bim"} {
		if contains(got, forbidden) {
			t.Errorf("htmlToText a laissé passer %q dans %q", forbidden, got)
		}
	}
}

func TestStripQuotedReply(t *testing.T) {
	cases := map[string]string{
		"Oui c'est bon pour moi.\n\nLe 3 mars 2026, Olivier a écrit :\n> Tu confirmes ?": "Oui c'est bon pour moi.",
		"Ça marche.\n> ancien message\n> suite":                                          "Ça marche.",
		"On 3 March, Olivier wrote:\n> hello":                                            "",
		"Pas de citation ici.":                                                           "Pas de citation ici.",
	}
	for in, want := range cases {
		if got := stripQuotedReply(in); got != want {
			t.Errorf("stripQuotedReply(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestCollapseGardeLesParagraphes(t *testing.T) {
	// Quatre lignes vides d'affilée se réduisent à une seule.
	got := collapse("Ligne  un\r\n\n\n\nLigne   deux\t\tsuite\n\n")
	want := "Ligne un\n\nLigne deux suite"
	if got != want {
		t.Errorf("collapse = %q, attendu %q", got, want)
	}
}

func TestTruncateRunes(t *testing.T) {
	// Découpe sur les runes, pas les octets : « é » fait deux octets.
	if got := truncateRunes("ééééé", 3); got != "ééé…" {
		t.Errorf("truncateRunes = %q, attendu %q", got, "ééé…")
	}
	if got := truncateRunes("court", 50); got != "court" {
		t.Errorf("truncateRunes ne doit pas toucher un texte court, obtenu %q", got)
	}
}

func TestNormalizeQuery(t *testing.T) {
	cases := map[string]string{
		"Olivier Dupont":    "olivier dupont",
		"le DEVIS, urgent!": "le devis urgent",
		"réunion":           "reunion",
		"  ":                "",
	}
	for in, want := range cases {
		if got := normalizeQuery(in); got != want {
			t.Errorf("normalizeQuery(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}

// L'historique cité ne doit pas être jeté : c'est lui qui dit ce qui a déjà été
// échangé, donc ce à quoi on répond.
func TestSplitQuotedReplyKeepsHistory(t *testing.T) {
	in := "Oui c'est bon pour moi.\n\nLe 3 mars 2026, Cyril a écrit :\n> Tu confirmes pour jeudi ?\n> Cyril"

	body, quoted := splitQuotedReply(in)
	if body != "Oui c'est bon pour moi." {
		t.Errorf("corps = %q", body)
	}
	if !contains(quoted, "Tu confirmes pour jeudi ?") {
		t.Errorf("l'historique devrait être conservé, obtenu %q", quoted)
	}
	// Le prénom en signature du fil est ce qui permet d'écrire « Bonjour Cyril »
	// quand l'en-tête ne porte qu'une adresse.
	if !contains(quoted, "Cyril") {
		t.Errorf("la signature citée devrait survivre, obtenu %q", quoted)
	}
	// Les chevrons gênent la lecture et n'apportent rien au modèle.
	if contains(quoted, ">") {
		t.Errorf("les chevrons de citation devraient être retirés : %q", quoted)
	}
}

// Sans en-tête « a écrit : », les lignes citées se trient quand même.
func TestSplitQuotedReplyWithoutHeader(t *testing.T) {
	body, quoted := splitQuotedReply("Ça marche.\n> ancien message\n> suite")
	if body != "Ça marche." {
		t.Errorf("corps = %q", body)
	}
	if !contains(quoted, "ancien message") || !contains(quoted, "suite") {
		t.Errorf("historique = %q", quoted)
	}
}

// Deux mails du même fil ne se ressemblent que par leur objet dépouillé.
func TestBaseSubject(t *testing.T) {
	cases := map[string]string{
		"Re: Devis":            "devis",
		"RE: Re: Fwd: Devis":   "devis",
		"TR: Réunion d'équipe": "reunion d equipe",
		"Devis":                "devis",
		"Re[2]: Devis":         "devis",
		"":                     "",
	}
	for in, want := range cases {
		if got := baseSubject(in); got != want {
			t.Errorf("baseSubject(%q) = %q, attendu %q", in, got, want)
		}
	}

	// Le rapprochement doit marcher dans les deux sens.
	if baseSubject("Re: Devis piscine") != baseSubject("Devis piscine") {
		t.Error("une réponse et son mail d'origine doivent tomber dans le même fil")
	}
	// Mais deux sujets distincts ne doivent pas fusionner.
	if baseSubject("Re: Devis") == baseSubject("Re: Planning") {
		t.Error("deux sujets différents ne doivent pas se confondre")
	}
}
