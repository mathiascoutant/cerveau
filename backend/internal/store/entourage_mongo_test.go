package store

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Ces tests parlent à un vrai MongoDB : les opérateurs de mise à jour
// ($addToSet, filtres conditionnels) ne se vérifient pas sur le papier.
// Lancer avec MONGO_TEST_URI=mongodb://127.0.0.1:27017 — sans, ils s'effacent.
func testStore(t *testing.T) *Store {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI non défini")
	}
	ctx := context.Background()
	name := "cerveau_test_" + bson.NewObjectID().Hex()
	s, err := Connect(ctx, uri, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.db.Drop(context.Background())
		_ = s.Close(context.Background())
	})
	return s
}

// Réapprendre quelqu'un complète sa fiche sans rien perdre : le rôle reste
// quand on ne le redonne pas, les adresses s'ajoutent.
func TestSaveFactMergesPersonIdentity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := bson.NewObjectID()

	first, err := s.SaveFact(ctx, Fact{
		UserID: user, Kind: FactPerson, Subject: "Cyril", Content: "Son patron",
		Role: "son patron, CEO", Importance: "élevée", Adresses: []string{"Cyril.Martin@PXCOM.aero"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Importance != ImportanceHaute || first.Adresses[0] != "cyril.martin@pxcom.aero" {
		t.Fatalf("première fiche : %+v", first)
	}

	second, err := s.SaveFact(ctx, Fact{
		UserID: user, Kind: FactPerson, Subject: "cyril", Content: "Son patron, porte le dossier boxes",
		Adresses: []string{"cyril@perso.fr", "cyril.martin@pxcom.aero"}, SlackIDs: []string{"u07cyril"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatal("la même personne a ouvert une deuxième fiche")
	}
	if second.Role != "son patron, CEO" || second.Importance != ImportanceHaute {
		t.Errorf("rôle ou importance perdus : %+v", second)
	}
	if len(second.Adresses) != 2 || len(second.SlackIDs) != 1 || second.SlackIDs[0] != "U07CYRIL" {
		t.Errorf("identités mal fusionnées : %+v", second)
	}

	got, err := s.FactBySubject(ctx, user, FactPerson, "CYRIL")
	if err != nil || got.ID != first.ID {
		t.Fatalf("FactBySubject : %+v, %v", got, err)
	}

	if _, err := s.SaveFact(ctx, Fact{UserID: user, Kind: FactProject, Subject: "DAW", Content: "Boxes"}); err != nil {
		t.Fatal(err)
	}
	people, err := s.People(ctx, user)
	if err != nil || len(people) != 1 {
		t.Fatalf("People : %d, %v", len(people), err)
	}

	updated, err := s.UpdatePerson(ctx, user, first.ID, "", "critique", []string{"cyril@pxcom.aero"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != "" || updated.Importance != ImportanceCritique || len(updated.Adresses) != 1 {
		t.Errorf("UpdatePerson : %+v", updated)
	}
	if _, err := s.UpdatePerson(ctx, user, bson.NewObjectID(), "x", "basse", nil); err != ErrNotFound {
		t.Errorf("personne inconnue : %v", err)
	}
}

// Le dernier message d'un échange ne recule jamais, et relire sans comprendre
// n'efface pas ce qu'on avait compris.
func TestNoterEchangeKeepsLatestAndUnderstanding(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := bson.NewObjectID()
	t0 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

	base := Echange{UserID: user, Source: "mail", Espace: "pro", Cle: "root@x.fr", Sujet: "Projet Atlas"}
	e := base
	e.Participants = []string{"Paul Durand"}
	e.DernierDe, e.DernierAdresse, e.DernierQuand, e.DernierRef = "Cyril Martin", "Cyril@X.fr", t0.Add(2*time.Hour), "c1@x.fr"
	if err := s.NoterEchange(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.NoterCompris(ctx, user, "mail", "root@x.fr", "Cyril demande le budget."); err != nil {
		t.Fatal(err)
	}

	// Une lecture plus ancienne (un mail du milieu du fil) ne remplace pas le
	// dernier message, mais ajoute son auteur aux participants.
	older := base
	older.Participants = []string{"Paul Durand", "Sophie"}
	older.DernierDe, older.DernierQuand, older.DernierRef = "Paul Durand", t0, "root@x.fr"
	if err := s.NoterEchange(ctx, older); err != nil {
		t.Fatal(err)
	}

	list, err := s.EchangesRecents(ctx, user, 5)
	if err != nil || len(list) != 1 {
		t.Fatalf("échanges : %d, %v", len(list), err)
	}
	got := list[0]
	if got.DernierRef != "c1@x.fr" || got.DernierAdresse != "cyril@x.fr" {
		t.Errorf("le dernier message a reculé : %+v", got)
	}
	if got.Resume != "Cyril demande le budget." || got.ResumeLe.IsZero() {
		t.Errorf("compréhension perdue : %+v", got)
	}
	if len(got.Participants) != 2 {
		t.Errorf("participants : %v", got.Participants)
	}

	newer := base
	newer.DernierDe, newer.DernierQuand, newer.DernierRef = "Paul Durand", t0.Add(3*time.Hour), "p2@x.fr"
	if err := s.NoterEchange(ctx, newer); err != nil {
		t.Fatal(err)
	}
	found, err := s.ChercherEchanges(ctx, user, "paul", 3)
	if err != nil || len(found) != 1 || found[0].DernierRef != "p2@x.fr" {
		t.Fatalf("recherche par personne : %+v, %v", found, err)
	}
	if none, _ := s.ChercherEchanges(ctx, user, "facture", 3); len(none) != 0 {
		t.Errorf("recherche sans rapport : %+v", none)
	}
}
