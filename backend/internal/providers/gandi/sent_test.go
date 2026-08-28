package gandi

import "testing"

// Les clients mail ne sont pas d'accord sur les chevrons : Outlook les met,
// certains scripts non, et une comparaison littérale raterait un fil sur deux.
func TestNormalizeMessageID(t *testing.T) {
	cases := [][2]string{
		{"<CAF=abc123@mail.gmail.com>", "caf=abc123@mail.gmail.com"},
		{"CAF=abc123@mail.gmail.com", "caf=abc123@mail.gmail.com"},
		{"  <AM0PR07MB1234@eurprd07.prod.outlook.com> ", "am0pr07mb1234@eurprd07.prod.outlook.com"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeMessageID(c[0]); got != c[1] {
			t.Errorf("normalizeMessageID(%q) = %q, attendu %q", c[0], got, c[1])
		}
	}
}

func TestAnswersYou(t *testing.T) {
	sent := map[string]bool{"moi-1@pxcom.aero": true, "moi-2@pxcom.aero": true}

	if !answersYou([]string{"<MOI-1@pxcom.aero>"}, sent) {
		t.Error("un mail qui cite l'un de ses envois doit être reconnu, chevrons et casse compris")
	}
	if answersYou([]string{"<autre@ailleurs.fr>"}, sent) {
		t.Error("un mail qui répond à quelqu'un d'autre ne le vise pas")
	}
	if answersYou(nil, sent) {
		t.Error("un mail sans filiation ne répond à rien")
	}
	// Boîte d'envoi illisible : on ne prétend pas, on ne marque rien.
	if answersYou([]string{"<moi-1@pxcom.aero>"}, nil) {
		t.Error("sans boîte d'envoi lue, aucune filiation ne doit être affirmée")
	}
}
