package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// L'orchestration du travail des agents.
//
// Afficher cent agents ne veut pas dire en faire travailler cent à la fois.
// Les tâches lancées depuis Cerveau HQ passent par une file : un agent ne
// traite qu'une tâche à la fois, et le serveur n'en exécute qu'un nombre borné
// en même temps (AGENTS_CONCURRENCE). Au-delà, la tâche attend — et le
// personnage l'affiche comme en attente, parce que c'est vrai. Un budget
// quotidien (AGENTS_BUDGET_JOUR) borne ce qu'on peut lancer : chaque tâche
// coûte des appels au modèle.
//
// Les conversations ne passent pas par la file — quelqu'un attend la réponse —
// mais elles sont suivies de la même façon, et la délégation d'une enquête à
// l'ingénieur aussi : tout travail réel d'un agent se voit dans le bâtiment,
// et rien d'autre.

const (
	defaultConcurrence = 2
	defaultBudgetJour  = 100
	// hqRunTimeout : une tâche lancée depuis le bâtiment a le temps d'une
	// enquête, pas plus — comme une demande en conversation.
	hqRunTimeout = conversationTimeout
)

type orchestre struct {
	store  *store.Store
	global chan struct{}
	budget int

	mu    sync.Mutex
	slots map[string]chan struct{} // un par (utilisateur, agent)
	live  map[bson.ObjectID]*liveRun
}

func newOrchestre(st *store.Store, concurrence, budget int) *orchestre {
	if concurrence <= 0 {
		concurrence = defaultConcurrence
	}
	if budget <= 0 {
		budget = defaultBudgetJour
	}
	return &orchestre{
		store:  st,
		global: make(chan struct{}, concurrence),
		budget: budget,
		slots:  map[string]chan struct{}{},
		live:   map[bson.ObjectID]*liveRun{},
	}
}

// liveRun : une exécution en cours ou en attente, telle que le serveur la vit.
type liveRun struct {
	o *orchestre

	mu     sync.Mutex
	run    store.AgentRun
	etapes []store.RunStep
	etape  string // libellé de l'outil en cours
	cancel context.CancelFunc
	done   bool
}

func (o *orchestre) slot(user bson.ObjectID, agent string) chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := user.Hex() + "/" + agent
	ch, ok := o.slots[key]
	if !ok {
		ch = make(chan struct{}, 1)
		o.slots[key] = ch
	}
	return ch
}

// begin ouvre une exécution qui commence tout de suite (conversation,
// délégation). Une base indisponible ne bloque pas le travail : l'exécution
// est seulement suivie en mémoire.
func (o *orchestre) begin(ctx context.Context, user bson.ObjectID, agent, source, demande string, parent *bson.ObjectID) *liveRun {
	now := time.Now()
	run := store.AgentRun{
		UserID: user, Agent: agent, Source: source, Demande: strings.TrimSpace(demande),
		Statut: store.RunEnCours, Parent: parent, CreatedAt: now, StartedAt: &now,
	}
	if saved, err := o.store.InsertRun(context.WithoutCancel(ctx), run); err == nil {
		run = saved
	} else {
		slog.Warn("exécution non enregistrée", "agent", agent, "err", err)
		run.ID = bson.NewObjectID()
	}
	lr := &liveRun{o: o, run: run}
	o.mu.Lock()
	o.live[run.ID] = lr
	o.mu.Unlock()
	return lr
}

// Etape suit les outils : un événement sans durée ouvre une étape, le même
// avec sa durée la ferme.
func (lr *liveRun) Etape(ev assistant.Event) {
	if ev.Type != "status" {
		return
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if ev.Ms == 0 && ev.Err == "" {
		lr.etape = ev.Label
		lr.etapes = append(lr.etapes, store.RunStep{Outil: ev.Tool, Libelle: ev.Label})
		return
	}
	for i := len(lr.etapes) - 1; i >= 0; i-- {
		if lr.etapes[i].Outil == ev.Tool && lr.etapes[i].Ms == 0 && lr.etapes[i].Erreur == "" {
			lr.etapes[i].Ms, lr.etapes[i].Erreur = ev.Ms, ev.Err
			break
		}
	}
	lr.etape = ""
}

// Fin clôt l'exécution avec ce qu'elle a vraiment produit.
func (lr *liveRun) Fin(res assistant.Result, err error) {
	lr.mu.Lock()
	if lr.done {
		lr.mu.Unlock()
		return
	}
	lr.done = true
	statut, reponse, erreur := store.RunTermine, strings.TrimSpace(res.Reply), ""
	switch {
	case errors.Is(err, context.Canceled):
		statut, erreur = store.RunAnnule, "annulée"
	case err != nil:
		statut, erreur = store.RunErreur, err.Error()
	case res.Handoff != "":
		reponse = "a passé la parole (" + res.Handoff + ")"
	}
	etapes := append([]store.RunStep(nil), lr.etapes...)
	id := lr.run.ID
	lr.run.Statut = statut
	lr.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lr.o.store.FinishRun(ctx, id, statut, reponse, erreur, etapes); err != nil {
		slog.Warn("fin d'exécution non enregistrée", "err", err)
	}
	lr.o.mu.Lock()
	delete(lr.o.live, id)
	lr.o.mu.Unlock()
}

// suivi : ce que l'équipe prévient quand la responsable confie une enquête.
type suivi struct {
	o      *orchestre
	user   bson.ObjectID
	parent *bson.ObjectID
}

func (s suivi) Debut(agent, source, demande string) equipe.Travail {
	return s.o.begin(context.Background(), s.user, agent, source, demande, s.parent)
}

func (o *orchestre) suiviPour(user bson.ObjectID, parent *liveRun) equipe.Suivi {
	var id *bson.ObjectID
	if parent != nil {
		pid := parent.run.ID
		id = &pid
	}
	return suivi{o: o, user: user, parent: id}
}

// errBudget : le budget quotidien des tâches lancées depuis le bâtiment est
// atteint.
var errBudget = errors.New("budget du jour atteint : plus de nouvelle tâche avant demain")

// enqueue accepte une tâche pour un agent et la met en file. exec la fait
// travailler une fois sa place obtenue.
func (o *orchestre) enqueue(user bson.ObjectID, agent, demande string, exec func(ctx context.Context, lr *liveRun) (assistant.Result, error)) (store.AgentRun, error) {
	ctx := context.Background()
	used, err := o.store.CountRunsSince(ctx, user, store.RunSourceHQ, time.Now().Add(-24*time.Hour))
	if err == nil && int(used) >= o.budget {
		return store.AgentRun{}, errBudget
	}
	run, err := o.store.InsertRun(ctx, store.AgentRun{
		UserID: user, Agent: agent, Source: store.RunSourceHQ, Demande: strings.TrimSpace(demande),
		Statut: store.RunEnAttente, CreatedAt: time.Now(),
	})
	if err != nil {
		return store.AgentRun{}, err
	}
	runCtx, cancel := context.WithTimeout(context.Background(), hqRunTimeout+10*time.Minute)
	lr := &liveRun{o: o, run: run, cancel: cancel}
	o.mu.Lock()
	o.live[run.ID] = lr
	o.mu.Unlock()

	go func() {
		defer cancel()
		agentSlot := o.slot(user, agent)
		// La place de l'agent d'abord, puis une place du serveur : un agent
		// occupé ne retient pas une exécution qu'un autre pourrait utiliser.
		select {
		case agentSlot <- struct{}{}:
		case <-runCtx.Done():
			lr.Fin(assistant.Result{}, context.Canceled)
			return
		}
		defer func() { <-agentSlot }()
		select {
		case o.global <- struct{}{}:
		case <-runCtx.Done():
			lr.Fin(assistant.Result{}, context.Canceled)
			return
		}
		defer func() { <-o.global }()

		lr.mu.Lock()
		now := time.Now()
		lr.run.Statut, lr.run.StartedAt = store.RunEnCours, &now
		lr.mu.Unlock()
		if err := o.store.StartRun(context.Background(), run.ID); err != nil {
			slog.Warn("début d'exécution non enregistré", "err", err)
		}
		workCtx, stop := context.WithTimeout(runCtx, hqRunTimeout)
		defer stop()
		res, err := exec(workCtx, lr)
		lr.Fin(res, err)
	}()
	return run, nil
}

// cancelRun annule une tâche en attente ou en cours, si elle appartient à
// l'utilisateur et a été lancée depuis le bâtiment.
func (o *orchestre) cancelRun(user, id bson.ObjectID) bool {
	o.mu.Lock()
	lr, ok := o.live[id]
	o.mu.Unlock()
	if !ok || lr.run.UserID != user || lr.cancel == nil {
		return false
	}
	lr.cancel()
	return true
}

// snapshot : l'état vivant d'une exécution.
type runSnapshot struct {
	Run    store.AgentRun
	Etape  string
	Etapes []store.RunStep
}

// active rend les exécutions vivantes d'un utilisateur, par agent.
func (o *orchestre) active(user bson.ObjectID) map[string][]runSnapshot {
	o.mu.Lock()
	runs := make([]*liveRun, 0, len(o.live))
	for _, lr := range o.live {
		runs = append(runs, lr)
	}
	o.mu.Unlock()
	out := map[string][]runSnapshot{}
	for _, lr := range runs {
		lr.mu.Lock()
		if lr.run.UserID == user && !lr.done {
			out[lr.run.Agent] = append(out[lr.run.Agent], runSnapshot{
				Run: lr.run, Etape: lr.etape, Etapes: append([]store.RunStep(nil), lr.etapes...),
			})
		}
		lr.mu.Unlock()
	}
	return out
}

// liveRunByID rend l'état vivant d'une exécution, s'il y en a un.
func (o *orchestre) liveRunByID(id bson.ObjectID) (runSnapshot, bool) {
	o.mu.Lock()
	lr, ok := o.live[id]
	o.mu.Unlock()
	if !ok {
		return runSnapshot{}, false
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return runSnapshot{Run: lr.run, Etape: lr.etape, Etapes: append([]store.RunStep(nil), lr.etapes...)}, true
}

// load : exécutions en cours et en attente, toutes origines.
func (o *orchestre) load() (enCours, enAttente int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, lr := range o.live {
		lr.mu.Lock()
		switch lr.run.Statut {
		case store.RunEnCours:
			enCours++
		case store.RunEnAttente:
			enAttente++
		}
		lr.mu.Unlock()
	}
	return enCours, enAttente
}
