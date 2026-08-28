package fuzzy

import "testing"

// Le cas qui a motivé le paquet : la dictée française entend « ai » comme le nom
// de la lettre R, et découpe un mot en trois. Le canal existait, Raoul répondait
// qu'il ne le trouvait pas.
func TestScoreRecognizesDictatedChannel(t *testing.T) {
	cases := []struct {
		query string
		name  string
	}{
		{"dubai R wing", "dubaiairwing"},
		{"dubai air wing", "dubaiairwing"},
		{"Dubaï Airwing", "dubai-airwing"},
		{"le canal dubai airwing", "dubaiairwing"}, // la variante nettoyée est essayée à part
		{"dev back", "dev-backend"},
		{"projet", "projet-2026"},
	}
	for _, c := range cases {
		if got := Score(c.query, c.name); got < Match {
			t.Errorf("Score(%q, %q) = %.2f, sous le seuil %.2f", c.query, c.name, got, Match)
		}
	}
}

// La tolérance ne doit pas devenir de la complaisance : deux noms sans rapport
// restent sans rapport, sinon Raoul lirait n'importe quel canal avec aplomb.
func TestScoreRejectsUnrelated(t *testing.T) {
	cases := [][2]string{
		{"dubai airwing", "recrutement"},
		{"compta", "dev-backend"},
		{"olivier", "general"},
	}
	for _, c := range cases {
		if got := Score(c[0], c[1]); got >= Match {
			t.Errorf("Score(%q, %q) = %.2f : trop permissif", c[0], c[1], got)
		}
	}
}

// Une lettre isolée n'est pas rapprochée du nom, elle le reconstitue : R
// redevient « air », « dubai R wing » redevient « dubaiairwing ». Le score vaut
// donc 1, comme si le nom avait été dicté juste — ce qui, à l'oreille, est le
// cas.
func TestScoreRebuildsSpelledLetters(t *testing.T) {
	if got := Score("dubai R wing", "dubaiairwing"); got != 1 {
		t.Errorf("Score = %.2f, attendu 1 : la lettre R doit rendre sa syllabe", got)
	}
	if got := Spelled("dubai R wing"); got != "dubaiairwing" {
		t.Errorf("Spelled = %q, attendu \"dubaiairwing\"", got)
	}
}

// La lecture épelée s'ajoute à la lecture normale, elle ne la remplace pas :
// un canal qui s'appelle vraiment « plan-b » ne doit pas devenir « planbe ».
func TestSpellingsKeepsLiteralReading(t *testing.T) {
	if got := Score("plan B", "plan-b"); got != 1 {
		t.Errorf("Score(« plan B », « plan-b ») = %.2f, attendu 1", got)
	}
	if got := Score("plan B", "plan-be"); got != 1 {
		t.Errorf("Score(« plan B », « plan-be ») = %.2f, attendu 1", got)
	}
}

// Les autres lettres qui portent une syllabe, dans les deux sens de découpage.
func TestSpelledLettersBothWays(t *testing.T) {
	cases := [][2]string{
		{"dev K", "devka"}, // K entendu là où le nom écrit « ka »
		{"canal H bergement", "achebergement"},
		{"team S", "teamesse"},
		{"devka", "dev K"}, // et le nom écrit avec la lettre, dicté en syllabes
	}
	for _, c := range cases {
		if got := Score(c[0], c[1]); got < Match {
			t.Errorf("Score(%q, %q) = %.2f, sous le seuil", c[0], c[1], got)
		}
	}
}

// La clé sonore réunit les graphies qu'une dictée peut choisir indifféremment.
func TestSound(t *testing.T) {
	same := [][2]string{
		{"dubai R wing", "dubai airwing"},
		{"photo", "foto"},
		{"quai", "ke"},
		{"bureau", "buro"},
		{"devs", "dev"},
	}
	for _, c := range same {
		if a, b := Sound(c[0]), Sound(c[1]); a != b {
			t.Errorf("Sound(%q)=%q ≠ Sound(%q)=%q", c[0], a, c[1], b)
		}
	}
}

func TestTightFlattensPunctuationAndAccents(t *testing.T) {
	if got := Tight(" #Dubaï-Air_Wing "); got != "dubaiairwing" {
		t.Errorf("Tight = %q", got)
	}
}

// Exact sépare « il a dit le nom » de « il a dit quelque chose qui y
// ressemble ». C'est cette limite qui décide si on lit tout de suite ou si on
// demande confirmation : « azul » ressemble, « PXCom- Azul technique » est le
// nom.
func TestExactSeparatesNameFromResemblance(t *testing.T) {
	const group = "PXCom- Azul technique"

	saidExactly := []string{
		"PXCom- Azul technique",
		"pxcom azul technique",
		"PXCOM AZUL TECHNIQUE",
		"le groupe PXCom Azul technique",
	}
	for _, said := range saidExactly {
		if !Exact(said, group) {
			t.Errorf("%q est le nom du groupe", said)
		}
	}

	onlyResembles := []string{"azul", "azul technique", "le groupe azul", "pxcom"}
	for _, said := range onlyResembles {
		if Exact(said, group) {
			t.Errorf("%q n'est qu'une approximation du nom", said)
		}
	}
}
