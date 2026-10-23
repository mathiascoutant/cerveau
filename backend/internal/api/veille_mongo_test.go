package api

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// La veille ne juge que le neuf, démarre sans rien signaler, et ses
// notifications se marquent lues une à une ou toutes ensemble.
func TestVeilleVusEtNotifications(t *testing.T) {
	st := runsStore(t)
	ctx := context.Background()
	user := bson.NewObjectID()
	agent := "veille_slack"

	if ok, err := st.VeilleDemarree(ctx, user, agent); err != nil || ok {
		t.Fatalf("veille démarrée trop tôt : %v %v", ok, err)
	}
	if err := st.VeilleMarquerVus(ctx, user, agent, []string{"a", "b", "depart"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.VeilleDemarree(ctx, user, agent); !ok {
		t.Fatal("la première relève démarre la veille")
	}
	vus, err := st.VeilleDejaVus(ctx, user, agent, []string{"a", "c"})
	if err != nil || !vus["a"] || vus["c"] {
		t.Fatalf("déjà vus : %v %v", vus, err)
	}
	// Un autre agent a sa propre mémoire.
	if vus, _ := st.VeilleDejaVus(ctx, user, "veille_mail", []string{"a"}); vus["a"] {
		t.Fatal("les agents ne partagent pas ce qu'ils ont vu")
	}

	for _, titre := range []string{"Xavier attend ta réponse", "Le client relance le devis"} {
		if _, err := st.InsertNotification(ctx, store.Notification{UserID: user, Agent: agent, Source: "slack", Titre: titre, Importance: "haute"}); err != nil {
			t.Fatal(err)
		}
	}
	list, nonLues, err := st.Notifications(ctx, user)
	if err != nil || len(list) != 2 || nonLues != 2 {
		t.Fatalf("notifications : %d, non lues %d, %v", len(list), nonLues, err)
	}
	id := list[0].ID
	if err := st.MarquerNotificationsLues(ctx, user, &id); err != nil {
		t.Fatal(err)
	}
	if _, nonLues, _ = st.Notifications(ctx, user); nonLues != 1 {
		t.Fatalf("une lue, reste %d", nonLues)
	}
	if err := st.MarquerNotificationsLues(ctx, user, nil); err != nil {
		t.Fatal(err)
	}
	if _, nonLues, _ = st.Notifications(ctx, user); nonLues != 0 {
		t.Fatalf("toutes lues, reste %d", nonLues)
	}
}

// Un nouveau message dans la même conversation est du neuf ; le même état,
// non.
func TestVeilleCle(t *testing.T) {
	now := time.Now()
	a := triage.Item{Source: "slack", De: "Xavier", Titre: "#pxsky", Apercu: "tu as vu ?", Quand: now}
	b := a
	if veilleCle(a) != veilleCle(b) {
		t.Fatal("même message, même clé")
	}
	b.Quand = now.Add(time.Minute)
	b.Apercu = "alors ?"
	if veilleCle(a) == veilleCle(b) {
		t.Fatal("nouveau message, nouvelle clé")
	}
}

// Un message privé non retenu par le jugement est signalé quand même ; une
// mention dans un canal reste au jugement.
func TestPrivesOublies(t *testing.T) {
	neufs := []triage.Item{
		{Source: "slack", De: "Xavier", Motif: triage.ReasonDM},
		{Source: "slack", De: "Cyril", Motif: triage.ReasonMention},
		{Source: "slack", De: "Inès", Motif: triage.ReasonDM},
	}
	got := privesOublies(neufs, map[int]bool{2: true})
	if len(got) != 1 || got[0].De != "Xavier" {
		t.Fatalf("seul le message privé de Xavier, non retenu, doit ressortir : %+v", got)
	}
}

// Les messages d'une même discussion se rangent sous un même fil : la réponse
// à un mail rejoint son sujet, un nouveau message Slack aussi.
func TestFilDe(t *testing.T) {
	a := triage.Item{Source: "mail", Titre: "APEX - WestIQ - PXCom"}
	b := triage.Item{Source: "mail", Titre: "RE: TR: APEX - WestIQ - PXCom"}
	if filDe(a) != filDe(b) || lieuDe(b) != "APEX - WestIQ - PXCom" {
		t.Fatalf("même fil de mail attendu : %q %q", filDe(a), filDe(b))
	}
	s1 := triage.Item{Source: "slack", Titre: "#apex", Apercu: "un"}
	s2 := triage.Item{Source: "slack", Titre: "#APEX", Apercu: "deux"}
	if filDe(s1) != filDe(s2) || filDe(s1) == filDe(a) {
		t.Fatal("même canal, même fil ; une autre messagerie, un autre fil")
	}
}
