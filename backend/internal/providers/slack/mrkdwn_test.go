package slack

import "context"
import "testing"

// Le rendu qui n'a pas besoin du réseau : tout ce que Slack accompagne déjà
// d'un libellé, plus l'échappement. Le cas « <@U…> » nu passe par users.info et
// se teste au niveau de l'intégration.
func TestRenderTextSansRéseau(t *testing.T) {
	c := New("xoxp-test")
	ctx := context.Background()

	cases := map[string]string{
		"Salut <@U5G2I82BU|olivier.dupont>, tu peux voir ?": "Salut @Olivier, tu peux voir ?",
		"le point est dans <#C024BE7LR|general>":            "le point est dans #general",
		"<!here> réunion dans 5 min":                        "@ici réunion dans 5 min",
		"<!channel|@channel> demain":                        "@canal demain",
		"<!everyone> bonne année":                           "@tout le monde bonne année",
		"cc <!subteam^S0614TZR7|@marketing>":                "cc @marketing",
		"le devis est <https://x.fr/devis|ici>":             "le devis est ici",
		"voir <https://x.fr/devis>":                         "voir https://x.fr/devis",
		"écris à <mailto:a@b.fr|a@b.fr>":                    "écris à a@b.fr",
		"écris à <mailto:a@b.fr>":                           "écris à a@b.fr",
		"Dupont &amp; fils, 3 &lt; 5":                       "Dupont & fils, 3 < 5",
		"rendu le <!date^1392734382^{date_short}|18/02>":    "rendu le 18/02",
		"aucune entité ici":                                 "aucune entité ici",
		"":                                                  "",
	}
	for in, want := range cases {
		if got := c.renderText(ctx, in); got != want {
			t.Errorf("renderText(%q)\n  = %q\n  attendu %q", in, got, want)
		}
	}
}

func TestGroupLabel(t *testing.T) {
	c := New("xoxp-test")
	c.self, c.selfResolved = "mathias", true
	ctx := context.Background()

	cases := map[string]string{
		"mpdm-mathias--olivier--jean-1":        "Olivier et Jean",
		"mpdm-mathias--olivier.dupont-1":       "Olivier",
		"mpdm-olivier--jean--marie--mathias-1": "Olivier, Jean et Marie",
		"mpdm-mathias-1":                       "mpdm-mathias-1",
		"pas-un-mpdm":                          "pas-un-mpdm",
	}
	for in, want := range cases {
		if got := c.groupLabel(ctx, in); got != want {
			t.Errorf("groupLabel(%q) = %q, attendu %q", in, got, want)
		}
	}
}
