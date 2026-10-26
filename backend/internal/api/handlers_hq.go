package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/atelier"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Cerveau HQ : le bâtiment où l'on voit travailler les agents.
//
// Ces routes ne fabriquent rien. Les agents sont ceux de l'équipe telle que le
// serveur la connaît — mêmes identifiants, mêmes outils, même exécution — et
// leur état vient de leurs exécutions réelles. Raoul n'y figure pas : c'est
// l'application elle-même, celle qui passe la parole, pas un agent du
// bâtiment.

// États affichés d'un agent.
const (
	hqDisponible = "disponible"
	hqEnCours    = "en_cours"
	hqEnAttente  = "en_attente"
	hqErreur     = "erreur"
	hqTermine    = "termine"
	hqInactif    = "inactif"
)

// Combien de temps un agent garde l'état de sa dernière exécution avant de
// redevenir disponible : assez pour qu'on voie qu'il a fini, ou échoué.
const (
	hqTermineFor = 10 * time.Minute
	hqErreurFor  = 30 * time.Minute
)

type hqTache struct {
	ID      string          `json:"id"`
	Agent   string          `json:"agent"`
	Source  string          `json:"source"`
	Demande string          `json:"demande"`
	Statut  string          `json:"statut"`
	Etape   string          `json:"etape,omitempty"`
	Etapes  []store.RunStep `json:"etapes,omitempty"`
	Reponse string          `json:"reponse,omitempty"`
	Erreur  string          `json:"erreur,omitempty"`
	// Affectation : pourquoi cet agent, quand on n'a nommé personne.
	Affectation string     `json:"affectation,omitempty"`
	Parent      string     `json:"parent,omitempty"`
	Creee       time.Time  `json:"creee"`
	Debut       *time.Time `json:"debut,omitempty"`
	Fin         *time.Time `json:"fin,omitempty"`
	Duree       int64      `json:"duree_ms,omitempty"`
	Annuler     bool       `json:"annulable,omitempty"`
}

func tacheView(r store.AgentRun, etape string, etapes []store.RunStep) hqTache {
	t := hqTache{
		ID: r.ID.Hex(), Agent: r.Agent, Source: r.Source, Demande: r.Demande, Statut: r.Statut,
		Etape: etape, Etapes: r.Etapes, Reponse: r.Reponse, Erreur: r.Erreur, Affectation: r.Affectation,
		Creee: r.CreatedAt, Debut: r.StartedAt, Fin: r.FinishedAt,
	}
	if len(etapes) > 0 {
		t.Etapes = etapes
	}
	if r.Parent != nil {
		t.Parent = r.Parent.Hex()
	}
	if r.StartedAt != nil && r.FinishedAt != nil {
		t.Duree = r.FinishedAt.Sub(*r.StartedAt).Milliseconds()
	}
	t.Annuler = r.Source == store.RunSourceHQ && (r.Statut == store.RunEnAttente || r.Statut == store.RunEnCours)
	return t
}

type hqAgent struct {
	ID           string `json:"id"`
	Nom          string `json:"nom"`
	Role         string `json:"role"`
	Equipe       string `json:"equipe,omitempty"`
	Superieur    string `json:"superieur,omitempty"`
	Espace       string `json:"espace,omitempty"`
	Couleur      string `json:"couleur"`
	Presentation string `json:"presentation"`
	// Voix : il parle — HQ peut faire entendre ses réponses.
	Voix      bool           `json:"voix,omitempty"`
	Actif     bool           `json:"actif"`
	Statut    string         `json:"statut"`
	Depuis    *time.Time     `json:"depuis,omitempty"`
	Tache     *hqTache       `json:"tache,omitempty"`
	EnAttente int            `json:"en_attente,omitempty"`
	Outils    []equipe.Outil `json:"outils"`
}

// hqAgents rend les agents de l'utilisateur avec leur état réel.
func (s *Server) hqAgents(ctx context.Context, user *store.User) ([]hqAgent, error) {
	if s.equipe == nil {
		return []hqAgent{}, nil
	}
	latest, err := s.store.LatestRuns(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	active := s.runs.active(user.ID)
	var out []hqAgent
	for _, m := range s.equipe.Roster() {
		if m.ID == equipe.IDRaoul {
			continue
		}
		a := hqAgent{
			ID: m.ID, Nom: m.Nom, Role: m.Role, Equipe: m.Equipe, Superieur: m.Superieur, Espace: m.Espace,
			Couleur: m.Couleur, Presentation: m.Presentation, Voix: m.Voix != "",
			Actif: true, Statut: hqDisponible,
			Outils: s.equipe.Outils(m),
		}
		if a.Outils == nil {
			a.Outils = []equipe.Outil{}
		}
		live := active[m.ID]
		sort.Slice(live, func(i, j int) bool { return live[i].Run.CreatedAt.Before(live[j].Run.CreatedAt) })
		var running, queued *runSnapshot
		for i := range live {
			switch live[i].Run.Statut {
			case store.RunEnCours:
				if running == nil {
					running = &live[i]
				}
			case store.RunEnAttente:
				a.EnAttente++
				if queued == nil {
					queued = &live[i]
				}
			}
		}
		switch {
		case running != nil:
			t := tacheView(running.Run, running.Etape, running.Etapes)
			a.Statut, a.Tache, a.Depuis = hqEnCours, &t, running.Run.StartedAt
		case queued != nil:
			t := tacheView(queued.Run, "", nil)
			a.Statut, a.Tache, a.Depuis = hqEnAttente, &t, &queued.Run.CreatedAt
		default:
			if last, ok := latest[m.ID]; ok && last.FinishedAt != nil {
				t := tacheView(last, "", nil)
				a.Tache = &t
				since := time.Since(*last.FinishedAt)
				switch {
				case last.Statut == store.RunErreur && since < hqErreurFor:
					a.Statut, a.Depuis = hqErreur, last.FinishedAt
				case last.Statut == store.RunTermine && since < hqTermineFor:
					a.Statut, a.Depuis = hqTermine, last.FinishedAt
				}
			}
		}
		out = append(out, a)
	}
	if out == nil {
		out = []hqAgent{}
	}
	return out, nil
}

func (s *Server) handleHQAgents(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	agents, err := s.hqAgents(r.Context(), user)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des agents impossible")
		return
	}
	enCours, enAttente := s.runs.load()
	used, _ := s.store.CountRunsSince(r.Context(), user.ID, store.RunSourceHQ, time.Now().Add(-24*time.Hour))
	out := map[string]any{
		"agents": agents,
		"orchestration": map[string]any{
			"max_simultanees": cap(s.runs.global),
			"en_cours":        enCours,
			"en_attente":      enAttente,
			"budget_jour":     s.runs.budget,
			"utilise_jour":    used,
		},
		"maj": time.Now(),
	}
	if s.equipe == nil {
		out["message"] = persoDisabled
		out["connecteurs"] = []equipe.Connecteur{}
	} else {
		out["connecteurs"] = s.equipe.Connecteurs()
	}
	httpx.JSON(w, http.StatusOK, out)
}

// hqAgent rend un agent par son identifiant, Raoul exclu.
func (s *Server) hqMember(id string) (equipe.Membre, bool) {
	if s.equipe == nil || id == equipe.IDRaoul {
		return equipe.Membre{}, false
	}
	return s.equipe.Membre(id)
}

func (s *Server) handleHQAgent(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id := chi.URLParam(r, "id")
	if _, ok := s.hqMember(id); !ok {
		httpx.Error(w, http.StatusNotFound, "agent inconnu")
		return
	}
	agents, err := s.hqAgents(r.Context(), user)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture de l'agent impossible")
		return
	}
	var agent *hqAgent
	for i := range agents {
		if agents[i].ID == id {
			agent = &agents[i]
		}
	}
	if agent == nil {
		httpx.Error(w, http.StatusNotFound, "agent inconnu")
		return
	}
	runs, err := s.store.RunsOfAgent(r.Context(), user.ID, id, 40)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des tâches impossible")
		return
	}
	taches := make([]hqTache, 0, len(runs))
	for _, run := range runs {
		if snap, ok := s.runs.liveRunByID(run.ID); ok {
			taches = append(taches, tacheView(snap.Run, snap.Etape, snap.Etapes))
			continue
		}
		taches = append(taches, tacheView(run, "", nil))
	}
	history, err := s.store.InteractionsOfAgent(r.Context(), user.ID, id, 30)
	if err != nil {
		history = nil
	}
	if history == nil {
		history = []store.Interaction{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"agent":      agent,
		"taches":     taches,
		"historique": history,
	})
}

// lireDemande lit et valide le texte d'une tâche.
func lireDemande(w http.ResponseWriter, r *http.Request, vide string) (string, bool) {
	var req struct {
		Demande string `json:"demande"`
	}
	if err := httpx.Decode(r, &req); err != nil || strings.TrimSpace(req.Demande) == "" {
		httpx.Error(w, http.StatusBadRequest, vide)
		return "", false
	}
	demande := strings.TrimSpace(req.Demande)
	if len([]rune(demande)) > 4000 {
		httpx.Error(w, http.StatusBadRequest, "demande trop longue (4000 caractères au plus)")
		return "", false
	}
	return demande, true
}

// handleHQLaunch confie une tâche à un agent, par le moteur de l'équipe — la
// même exécution que lorsqu'on lui parle, avec ses outils et sa mémoire.
func (s *Server) handleHQLaunch(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	m, ok := s.hqMember(chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "agent inconnu")
		return
	}
	demande, ok := lireDemande(w, r, "écris ce que l'agent doit faire")
	if !ok {
		return
	}
	run, err := s.launchHQ(user, m, demande, "")
	if err != nil {
		s.launchError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, tacheView(run, "", nil))
}

// handleHQAssign crée une tâche sans nommer d'agent : l'équipe la confie à
// celui dont les compétences conviennent le mieux, et dit pourquoi.
func (s *Server) handleHQAssign(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if s.equipe == nil {
		httpx.Error(w, http.StatusServiceUnavailable, persoDisabled)
		return
	}
	demande, ok := lireDemande(w, r, "écris ce qu'il faut faire")
	if !ok {
		return
	}
	a, err := s.equipe.Affecter(r.Context(), demande)
	if err != nil {
		httpx.Error(w, http.StatusServiceUnavailable, "aucun agent à qui confier la tâche")
		return
	}
	m, ok := s.hqMember(a.Agent)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "agent choisi introuvable")
		return
	}
	slog.Info("tâche affectée", "agent", m.ID, "par", a.Par)
	run, err := s.launchHQ(user, m, demande, a.Raison)
	if err != nil {
		s.launchError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"tache":  tacheView(run, "", nil),
		"agent":  map[string]string{"id": m.ID, "nom": m.Nom, "role": m.Role},
		"raison": a.Raison,
		"par":    a.Par,
	})
}

func (s *Server) launchError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBudget) {
		httpx.Error(w, http.StatusTooManyRequests, err.Error())
		return
	}
	httpx.Error(w, http.StatusInternalServerError, "la tâche n'a pas pu être lancée")
}

// launchHQ met une tâche du bâtiment en file pour un agent.
func (s *Server) launchHQ(user *store.User, m equipe.Membre, demande, affectation string) (store.AgentRun, error) {
	consigne := ""
	if m.Pilote {
		consigne = s.consignePilote(m)
	}
	return s.launchRun(user, m, demande, runOpts{Affectation: affectation}, consigne)
}

// launchRun met une tâche en file pour un agent. Avec l'atelier, c'est Claude
// Code qui la fait, sur le VPS, avec les dépôts ; sans lui, le moteur de
// l'équipe, comme en conversation. consigne s'ajoute à ses instructions.
func (s *Server) launchRun(user *store.User, m equipe.Membre, demande string, opts runOpts, consigne string) (store.AgentRun, error) {
	tz := user.Timezone
	if tz == "" {
		tz = s.cfg.DefaultTimezone
	}
	if s.atelier != nil && opts.Limit == 0 {
		opts.Limit = time.Duration(max(s.cfg.AtelierDureeMax, 60)) * time.Minute
	}
	return s.runs.enqueueRun(user.ID, m.ID, demande, opts, func(ctx context.Context, lr *liveRun) (assistant.Result, error) {
		var res assistant.Result
		var err error
		if reply, ok := s.piloteRepond(ctx, user, m, demande, lr.Etape); ok {
			res = assistant.Result{Reply: reply, Agent: m.ID}
		} else if s.atelier != nil {
			res, err = s.atelier.Run(ctx, atelier.Tache{
				Run: lr.ID().Hex(), Agent: m.ID, Nom: m.Nom, Role: m.Role, Presentation: m.Presentation,
				Demande: demande, Consigne: consigne,
			}, lr.Etape)
		} else {
			res, err = s.equipe.Ask(ctx, m.ID, equipe.Demande{
				Text: demande, History: s.history(ctx, user, m.ID), Now: time.Now(), Timezone: tz,
				UserName: s.userName(user), CacheKey: "raoul:" + user.ID.Hex(),
				// Une tâche du bâtiment est pour cet agent-là : il ne passe
				// pas la parole, il fait le travail.
				SansPassage: true,
				Suivi:       s.runs.suiviPour(user.ID, lr),
			}, lr.Etape)
		}
		if err == nil && strings.TrimSpace(res.Reply) != "" && opts.Source == "" {
			// La tâche rejoint l'historique de l'agent, comme une question
			// posée en conversation : la suite peut s'y référer.
			_ = s.store.SaveInteraction(context.WithoutCancel(ctx), store.Interaction{
				UserID: user.ID, Transcript: demande, Reply: res.Reply, Agent: m.ID,
			})
		}
		return res, err
	})
}

func (s *Server) handleHQTask(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	if snap, ok := s.runs.liveRunByID(id); ok && snap.Run.UserID == user.ID {
		httpx.JSON(w, http.StatusOK, tacheView(snap.Run, snap.Etape, snap.Etapes))
		return
	}
	run, err := s.store.RunByID(r.Context(), user.ID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "tâche introuvable")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "lecture de la tâche impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, tacheView(run, "", nil))
}

func (s *Server) handleHQCancel(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	if !s.runs.cancelRun(user.ID, id) {
		httpx.Error(w, http.StatusConflict, "cette tâche n'est plus annulable")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"annulee": id.Hex()})
}
