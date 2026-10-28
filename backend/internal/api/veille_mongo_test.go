package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// Une tâche redite à la casse, aux espaces ou au point près reste la même.
func TestCleTache(t *testing.T) {
	if cleTache("Répondre à  Satish.") != cleTache("répondre à Satish") {
		t.Fatal("même tâche attendue")
	}
	if cleTache("Vérifier les images Azul 1226") == cleTache("Répondre à Satish") || cleTache("  ") != "" {
		t.Fatal("deux tâches distinctes ; un titre vide n'en fait pas une")
	}
}

// Le jugement rend ses tâches avec chaque sujet ; la consigne reste un
// format valide malgré les %% qu'elle contient.
func TestJugementTaches(t *testing.T) {
	var j jugement
	raw := `{"sujets":[{"sujet":"","messages":[1],"titre":"Azul 1226 : grille V1.0 et images livrées","resume":"r","a_faire":"Vérifier les images puis répondre.","taches":[{"titre":"Vérifier les images livrées pour Azul 1226","echeance":""},{"titre":"Répondre à Satish","echeance":"2026-10-29"}],"importance":"haute"}]}`
	if err := json.Unmarshal([]byte(raw), &j); err != nil || len(j.Sujets) != 1 || len(j.Sujets[0].Taches) != 2 || j.Sujets[0].Taches[1].Echeance != "2026-10-29" {
		t.Fatalf("tâches du jugement mal lues : %+v %v", j, err)
	}
	if c := fmt.Sprintf(veilleConsigne, "mails pro"); strings.Contains(c, "%!") || !strings.Contains(c, `"taches"`) {
		t.Fatal("consigne mal formée")
	}
}

// Les tâches d'un sujet se relisent par lui, cochées comprises ; les autres
// tâches n'en font pas partie.
func TestTodosDesSujets(t *testing.T) {
	st := runsStore(t)
	ctx := context.Background()
	user, sujet := bson.NewObjectID(), bson.NewObjectID()
	for _, titre := range []string{"Vérifier les images", "Répondre à Satish"} {
		if _, err := st.SaveTodo(ctx, store.Todo{UserID: user, Title: titre, Sujet: &sujet}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SaveTodo(ctx, store.Todo{UserID: user, Title: "Dictée à la main"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.TodosDesSujets(ctx, user, []bson.ObjectID{sujet})
	if err != nil || len(got[sujet]) != 2 {
		t.Fatalf("deux tâches attendues pour le sujet : %+v %v", got, err)
	}
	// Et elles sont bien dans la liste « toutes ».
	all, err := st.Todos(ctx, user, store.TodoQuery{Undated: true})
	if err != nil || len(all) != 3 {
		t.Fatalf("liste complète : %d %v", len(all), err)
	}
}

// Une tâche de la veille devient une carte « à faire » sans agent, qui dit
// d'où elle vient ; ce qui tombe demain est urgent, un sujet important haut.
func TestCarteDeTache(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	maintenant := time.Date(2026, 10, 28, 10, 0, 0, 0, paris)
	sujet := bson.NewObjectID()
	n := store.Notification{AFaire: "Corriger les Series name.", Resume: "Xavier confirme.", Importance: "haute", Lieu: "#condor"}
	premier := &triage.Item{Source: "slack", De: "Xavier", Titre: "#condor"}

	c := carteDeTache("Faire renseigner le Series name", nil, sujet, n, premier, maintenant)
	if c.Statut != store.CarteAFaire || c.Agent != "" || c.Priorite != store.PrioriteHaute || c.Echeance != nil || c.Sujet == nil || *c.Sujet != sujet {
		t.Fatalf("carte mal posée : %+v", c)
	}
	if c.Description != "Corriger les Series name.\n\nXavier confirme.\n\nVient de : #condor · Xavier" {
		t.Fatalf("description : %q", c.Description)
	}

	demain := time.Date(2026, 10, 29, 9, 0, 0, 0, paris)
	n.Importance = "moyenne"
	c = carteDeTache("Répondre à Xavier", &demain, sujet, n, premier, maintenant)
	if c.Priorite != store.PrioriteUrgente || c.Echeance == nil || c.Echeance.Format(time.RFC3339) != "2026-10-29T00:00:00Z" {
		t.Fatalf("échéance de demain, urgente : %+v %v", c, c.Echeance)
	}
	loin := time.Date(2026, 11, 4, 0, 0, 0, 0, paris)
	if c = carteDeTache("Relancer", &loin, sujet, n, nil, maintenant); c.Priorite != store.PrioriteNormale || strings.Contains(c.Description, "Vient de") {
		t.Fatalf("échéance lointaine, normale : %+v", c)
	}
}

// Ce que Mathias écrit lui-même met le sujet à jour en silence : il reste lu
// et ne remonte pas. Un message d'un autre le rend de nouveau non lu.
func TestSujetMisAJourEnSilence(t *testing.T) {
	st := runsStore(t)
	ctx := context.Background()
	user := bson.NewObjectID()
	n, err := st.InsertNotification(ctx, store.Notification{UserID: user, Agent: "veille_slack", Source: "slack", Titre: "Accès PXPlane", AFaire: "Envoyer le lien", Importance: "moyenne"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarquerNotificationsLues(ctx, user, &n.ID); err != nil {
		t.Fatal(err)
	}
	avant, _, _ := st.Notifications(ctx, user)
	n.Resume, n.AFaire = "Tu as envoyé le lien, c'est réglé.", ""
	if err := st.MettreAJourSujet(ctx, n.ID, n, true); err != nil {
		t.Fatal(err)
	}
	list, nonLues, _ := st.Notifications(ctx, user)
	if nonLues != 0 || list[0].LueAt == nil || !list[0].MisAJour.Equal(avant[0].MisAJour) || list[0].Resume != n.Resume {
		t.Fatalf("en silence : non lues %d, lue %v, maj %v → %v, résumé %q", nonLues, list[0].LueAt, avant[0].MisAJour, list[0].MisAJour, list[0].Resume)
	}
	if list[0].DernierPoint().Before(time.Now().Add(-time.Minute)) {
		t.Fatal("le point du sujet doit avancer, même en silence")
	}
	if err := st.MettreAJourSujet(ctx, n.ID, n, false); err != nil {
		t.Fatal(err)
	}
	if _, nonLues, _ = st.Notifications(ctx, user); nonLues != 1 {
		t.Fatalf("un message d'un autre rend le sujet non lu : %d", nonLues)
	}
}
