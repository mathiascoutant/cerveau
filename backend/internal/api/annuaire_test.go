package api

import (
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/config"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func testPeople() *annuaire {
	return newAnnuaire([]store.Fact{
		{Kind: store.FactPerson, Subject: "Cyril", Content: "Son patron", Role: "son patron, CEO", Importance: "critique",
			Adresses: []string{"cyril.martin@pxcom.aero"}, SlackIDs: []string{"U07CYRIL"}},
		{Kind: store.FactPerson, Subject: "Paul Durand", Content: "Chef de projet côté client. Exigeant.", Importance: "haute"},
		{Kind: store.FactPerson, Subject: "Sophie", Content: "Collègue", Role: "dev de l'équipe"},
		{Kind: store.FactPerson, Subject: "Sophie B", Content: "Comptable"},
	})
}

// L'adresse fait foi ; un homonyme dont l'adresse n'est pas la bonne n'est pas
// le patron, même s'il porte le même prénom.
func TestAnnuaireMailByAddressNotByHomonym(t *testing.T) {
	dir := testPeople()
	who := dir.forMail("Cyril Martin", "Cyril.Martin@pxcom.aero")
	if who.Qui != "son patron, CEO" || who.Importance != "critique" || who.QuiAuNom {
		t.Errorf("le patron n'est pas reconnu par son adresse : %+v", who)
	}
	if other := dir.forMail("Cyril Dupont", "cyril@orange.fr"); other.Qui != "" {
		t.Errorf("un autre Cyril pris pour le patron : %+v", other)
	}
}

// Sans adresse connue, le nom suffit — s'il ne désigne qu'une fiche.
func TestAnnuaireMailByName(t *testing.T) {
	dir := testPeople()
	who := dir.forMail("DURAND Paul", "p.durand@client.fr")
	if who.Qui != "Chef de projet côté client" || !who.QuiAuNom || who.Importance != "haute" {
		t.Errorf("Paul Durand mal reconnu : %+v", who)
	}
	// « Sophie » correspond à deux fiches : n'en reconnaître aucune.
	if amb := dir.forMail("Sophie Bernard", "sophie@x.fr"); amb.Qui != "" {
		t.Errorf("une Sophie ambiguë a été reconnue : %+v", amb)
	}
	// Sans nom affiché, l'adresse se lit comme un nom.
	if who := dir.forMail("", "paul.durand@client.fr"); who.Qui == "" {
		t.Error("le nom tiré de l'adresse n'a pas servi")
	}
}

func TestAnnuaireSlack(t *testing.T) {
	dir := testPeople()
	if who := dir.forSlack("u07cyril", "cyril"); who.Qui != "son patron, CEO" || who.QuiAuNom {
		t.Errorf("identifiant Slack non reconnu : %+v", who)
	}
	if who := dir.forSlack("U09AUTRE", "Cyril"); who.Qui != "" {
		t.Errorf("un autre Cyril sur Slack pris pour le patron : %+v", who)
	}
}

func TestSplitAddressAndQuotedSender(t *testing.T) {
	name, addr := splitAddress(`"Paul Durand" <Paul@X.fr>`)
	if name != "Paul Durand" || addr != "paul@x.fr" {
		t.Errorf("splitAddress = %q, %q", name, addr)
	}
	if got := quotedSender("Le 3 mars 2026 à 10:02, Paul Durand <paul@x.fr> a écrit"); got != "Paul Durand <paul@x.fr>" {
		t.Errorf("quotedSender = %q", got)
	}
}

func TestNamesYou(t *testing.T) {
	if !namesYou("Bonjour à tous,\nMathias, tu peux regarder ?", "Mathias Coutant") {
		t.Error("l'interpellation par le prénom n'est pas vue")
	}
	if namesYou("Bonjour à tous, voici le compte rendu.", "Mathias") {
		t.Error("interpellation inventée")
	}
}

func TestElevenLabsKeyRespectsVoix(t *testing.T) {
	if got := elevenLabsKey(config.Config{ElevenLabsAPIKey: "k", Voix: "appareil"}); got != "" {
		t.Error("VOIX=appareil doit couper ElevenLabs")
	}
	if got := elevenLabsKey(config.Config{ElevenLabsAPIKey: "k"}); got != "k" {
		t.Error("sans VOIX, la clé doit passer")
	}
}
