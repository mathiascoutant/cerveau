package api

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Ces tests parlent à un vrai MongoDB. Lancer avec
// MONGO_TEST_URI=mongodb://127.0.0.1:27017 — sans, ils s'effacent.
func runsStore(t *testing.T) *store.Store {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI non défini")
	}
	st, err := store.Connect(context.Background(), uri, "cerveau_test_"+bson.NewObjectID().Hex())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DropForTests(context.Background()) })
	return st
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("délai dépassé : %s", what)
}

// Un agent traite une tâche à la fois ; la suivante attend, et le dit.
// Une tâche en attente s'annule ; la tâche finie garde sa réponse et ses étapes.
func TestOrchestreFileParAgent(t *testing.T) {
	st := runsStore(t)
	o := newOrchestre(st, 4, 10)
	user := bson.NewObjectID()
	release := make(chan struct{})
	var running atomic.Int32

	exec := func(ctx context.Context, lr *liveRun) (assistant.Result, error) {
		running.Add(1)
		defer running.Add(-1)
		lr.Etape(assistant.Event{Type: "status", Tool: "journaux", Label: "Journaux"})
		select {
		case <-release:
		case <-ctx.Done():
			return assistant.Result{}, ctx.Err()
		}
		lr.Etape(assistant.Event{Type: "status", Tool: "journaux", Label: "Journaux", Ms: 12})
		return assistant.Result{Reply: "Tout va bien."}, nil
	}

	first, err := o.enqueue(user, "ingenieur", "regarde le VPS", exec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := o.enqueue(user, "ingenieur", "et la base ?", exec)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "la première tâche démarre", func() bool { return running.Load() == 1 })

	live := o.active(user)["ingenieur"]
	statuts := map[string]string{}
	for _, s := range live {
		statuts[s.Run.ID.Hex()] = s.Run.Statut
		if s.Run.ID == first.ID && s.Etape != "Journaux" {
			t.Errorf("l'étape en cours doit être visible, obtenu %q", s.Etape)
		}
	}
	if statuts[first.ID.Hex()] != store.RunEnCours || statuts[second.ID.Hex()] != store.RunEnAttente {
		t.Fatalf("statuts : %+v", statuts)
	}

	if !o.cancelRun(user, second.ID) {
		t.Fatal("une tâche en attente doit s'annuler")
	}
	if o.cancelRun(bson.NewObjectID(), first.ID) {
		t.Fatal("un autre utilisateur ne peut pas annuler la tâche")
	}
	close(release)
	waitFor(t, "les deux tâches se terminent", func() bool { return len(o.active(user)) == 0 })

	done, err := st.RunByID(context.Background(), user, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Statut != store.RunTermine || done.Reponse != "Tout va bien." || len(done.Etapes) != 1 || done.Etapes[0].Ms != 12 {
		t.Errorf("tâche terminée : %+v", done)
	}
	cancelled, _ := st.RunByID(context.Background(), user, second.ID)
	if cancelled.Statut != store.RunAnnule {
		t.Errorf("tâche annulée : %+v", cancelled)
	}
	latest, err := st.LatestRuns(context.Background(), user)
	if err != nil || latest["ingenieur"].ID != second.ID {
		t.Errorf("dernière exécution : %+v, %v", latest["ingenieur"], err)
	}
}

// La limite du serveur borne les exécutions simultanées, tous agents
// confondus ; le budget refuse la tâche de trop.
func TestOrchestreLimiteEtBudget(t *testing.T) {
	st := runsStore(t)
	o := newOrchestre(st, 1, 2)
	user := bson.NewObjectID()
	release := make(chan struct{})
	var running, peak atomic.Int32
	exec := func(ctx context.Context, lr *liveRun) (assistant.Result, error) {
		n := running.Add(1)
		if n > peak.Load() {
			peak.Store(n)
		}
		<-release
		running.Add(-1)
		return assistant.Result{Reply: "ok"}, nil
	}
	if _, err := o.enqueue(user, "ingenieur", "a", exec); err != nil {
		t.Fatal(err)
	}
	if _, err := o.enqueue(user, "responsable_info", "b", exec); err != nil {
		t.Fatal(err)
	}
	if _, err := o.enqueue(user, "ingenieur", "c", exec); err != errBudget {
		t.Fatalf("la troisième tâche dépasse le budget : %v", err)
	}
	waitFor(t, "une tâche démarre", func() bool { return running.Load() == 1 })
	enCours, enAttente := o.load()
	if enCours != 1 || enAttente != 1 {
		t.Errorf("charge : %d en cours, %d en attente", enCours, enAttente)
	}
	close(release)
	waitFor(t, "tout se termine", func() bool { enCours, enAttente := o.load(); return enCours+enAttente == 0 })
	if peak.Load() != 1 {
		t.Errorf("au plus une exécution simultanée, vu %d", peak.Load())
	}
}

// Les exécutions restées ouvertes au redémarrage sont closes en erreur.
func TestAbandonStaleRuns(t *testing.T) {
	st := runsStore(t)
	user := bson.NewObjectID()
	run, err := st.InsertRun(context.Background(), store.AgentRun{UserID: user, Agent: "ingenieur", Source: store.RunSourceHQ, Statut: store.RunEnCours, Demande: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.AbandonStaleRuns(context.Background()); err != nil || n != 1 {
		t.Fatalf("abandon : %d, %v", n, err)
	}
	got, _ := st.RunByID(context.Background(), user, run.ID)
	if got.Statut != store.RunErreur || got.FinishedAt == nil {
		t.Errorf("exécution abandonnée : %+v", got)
	}
}
