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
