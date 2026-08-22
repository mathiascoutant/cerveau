package gandi

import "testing"

func TestIsBulk(t *testing.T) {
	cas := []struct {
		nom     string
		headers string
		bulk    bool
	}{
		{"mail ordinaire", "From: olivier@cabinet.fr\r\nSubject: le devis\r\n", false},
		{"liste de diffusion", "List-Id: <news.studio.fr>\r\n", true},
		{"désabonnement", "List-Unsubscribe: <https://x.fr/u>\r\n", true},
		{"precedence bulk", "Precedence: bulk\r\n", true},
		{"precedence normale", "Precedence: normal\r\n", false},
		{"notification automatique", "Auto-Submitted: auto-generated\r\n", true},
		{"auto-submitted no", "Auto-Submitted: no\r\n", false},
		{"bloc vide", "", false},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			if got := isBulk([]byte(c.headers)); got != c.bulk {
				t.Errorf("isBulk = %v, attendu %v", got, c.bulk)
			}
		})
	}
}
