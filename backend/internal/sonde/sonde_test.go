package sonde

import "testing"

// Une demande doit traverser SSH telle quelle, qu'elle arrive seule (clé
// attachée à la sonde par command=) ou précédée du nom du programme (essai
// depuis un poste de développement).
func TestRequeteAllerRetour(t *testing.T) {
	req := Requete{Commande: CmdJournaux, Args: map[string]string{"conteneur": "back-api-1", "filtre": "error|panic", "lignes": "50"}}
	word, err := req.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{word, "cerveau-sonde " + word, "/usr/local/bin/cerveau-sonde -registre /x.json " + word} {
		got, err := Decode(line)
		if err != nil {
			t.Fatalf("%q : %v", line, err)
		}
		if got.Commande != CmdJournaux || got.Args["filtre"] != "error|panic" || got.Args["lignes"] != "50" {
			t.Fatalf("%q : demande déformée : %+v", line, got)
		}
	}
}

// Le mot encodé ne contient rien qu'un shell pourrait réinterpréter.
func TestRequeteSansCaractereSpecial(t *testing.T) {
	word, err := Requete{Commande: CmdExpliquer, Args: map[string]string{"filtre": `{"email": "a b"; rm -rf /}`}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range word {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			t.Fatalf("caractère %q dans la demande encodée", r)
		}
	}
}

func TestDecodeRefuseLeBruit(t *testing.T) {
	for _, line := range []string{"", "   ", "ls -la", "cerveau-sonde %%%"} {
		if _, err := Decode(line); err == nil {
			t.Errorf("%q aurait dû être refusé", line)
		}
	}
}

func TestDaysToHours(t *testing.T) {
	for in, want := range map[string]string{"7d": "168h", "2j": "48h", "30m": "30m", "2h": "2h", "": "", "d": "d", "xd": "xd", "2026-10-18T10:00:00Z": "2026-10-18T10:00:00Z"} {
		if got := DaysToHours(in); got != want {
			t.Errorf("DaysToHours(%q) = %q, attendu %q", in, got, want)
		}
	}
}
