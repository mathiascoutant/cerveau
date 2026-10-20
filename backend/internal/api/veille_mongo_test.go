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
