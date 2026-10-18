package slack

import (
	"context"
	"testing"
)

// Les quatre façons d'être visé, et celles qui n'en sont pas.
func TestTargeting(t *testing.T) {
	c := attributionClient()
	ctx := context.Background()
	cases := []struct {
		author, text, want string
	}{
		{"UTHOMAS", "<@UME> tu peux valider ?", VisePerso},
		{"UTHOMAS", "<@UME|mathias> tu peux valider ?", VisePerso},
		{"USOPHIE", "Mathias, est-ce que tu as fait ça ?", ViseNomme},
		{"USOPHIE", "MATHIAS tu as vu ?", ViseNomme},
		{"UPAUL", "<!channel> la prod est en maintenance ce soir", ViseGroupe},
		{"UPAUL", "<!here> qui est dispo ?", ViseGroupe},
		{"UPAUL", "<!subteam^S123|@devs> revue à 14h", ViseEquipe},
		// Le prénom dans un lien n'est pas une interpellation.
		{"UPAUL", "voir <https://wiki.acme.fr/mathias/notes|les notes>", ""},
		// Un mot qui contient le prénom non plus.
		{"UPAUL", "les mathiasiens sont arrivés", ""},
		// Son propre message ne le vise jamais.
		{"UME", "Mathias ici, je regarde <!channel>", ""},
		{"UPAUL", "Rien à voir avec personne", ""},
	}
	for _, tc := range cases {
		if got := c.targeting(ctx, tc.text, tc.author); got != tc.want {
			t.Errorf("%q de %s : %q, attendu %q", tc.text, tc.author, got, tc.want)
		}
	}
}
