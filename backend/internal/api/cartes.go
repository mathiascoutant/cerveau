package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Le tableau des tâches dans HQ : les cartes de chaque projet, et leur lien
// avec le travail des agents.
//
// Confier une carte, c'est lancer une tâche pour son agent, avec ce qu'il faut
// savoir du projet. La carte suit alors cette exécution : elle reste « en
// cours » pendant qu'il travaille, passe « à vérifier » avec sa réponse, ou
// revient « à faire » si elle échoue. Le suivi se fait à la lecture du
// tableau, que HQ relève en continu : rien à brancher dans l'orchestre.

type carteVue struct {
	store.Carte
	// Tache : l'exécution en cours, avec son étape.
	Tache *hqTache `json:"tache,omitempty"`
}

var (
	statutsCarte   = map[string]bool{store.CarteAFaire: true, store.CarteEnCours: true, store.CarteAVerifier: true, store.CarteTerminee: true}
	prioritesCarte = map[string]bool{store.PrioriteBasse: true, store.PrioriteNormale: true, store.PrioriteHaute: true, store.PrioriteUrgente: true}
)

// suivreCarte met une carte en cours à jour d'après son exécution.
func (s *Server) suivreCarte(ctx context.Context, c store.Carte) carteVue {
	v := carteVue{Carte: c}
	if c.Run == nil || c.Statut != store.CarteEnCours {
		return v
	}
	if snap, ok := s.runs.liveRunByID(*c.Run); ok {
		t := tacheView(snap.Run, snap.Etape, snap.Etapes)
		v.Tache = &t
		return v
	}
	run, err := s.store.RunByIDAny(ctx, *c.Run)
	if err != nil {
		return v
	}
	set := bson.M{}
	switch run.Statut {
	case store.RunTermine:
		set = bson.M{"statut": store.CarteAVerifier, "compte_rendu": run.Reponse, "erreur": ""}
	case store.RunErreur, store.RunAnnule:
		msg := run.Erreur
		if msg == "" {
			msg = "tâche annulée"
		}
		set = bson.M{"statut": store.CarteAFaire, "erreur": msg}
	default:
		t := tacheView(run, "", nil)
		v.Tache = &t
		return v
	}
	if updated, err := s.store.ModifierCarte(ctx, c.ID, set); err == nil {
		v.Carte = updated
	}
	return v
}

func (s *Server) handleHQCartes(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Cartes(r.Context(), strings.TrimSpace(r.URL.Query().Get("projet")))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture du tableau impossible")
		return
	}
	out := make([]carteVue, 0, len(list))
	for _, c := range list {
		out = append(out, s.suivreCarte(r.Context(), c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"cartes": out})
}

// carteDonnee : ce qu'on écrit sur une carte. Les champs absents ne changent
// pas ; une échéance vide l'efface.
type carteDonnee struct {
	Projet      *string  `json:"projet"`
	Titre       *string  `json:"titre"`
	Description *string  `json:"description"`
	Statut      *string  `json:"statut"`
	Agent       *string  `json:"agent"`
	Priorite    *string  `json:"priorite"`
	Echeance    *string  `json:"echeance"`
	Ordre       *float64 `json:"ordre"`
}

// valider traduit la saisie en champs à écrire.
func (s *Server) validerCarte(ctx context.Context, d carteDonnee) (bson.M, []string, error) {
	set := bson.M{}
	var unset []string
	if d.Titre != nil {
		t := strings.TrimSpace(*d.Titre)
		if t == "" || len([]rune(t)) > 200 {
			return nil, nil, errors.New("un titre de 1 à 200 caractères")
		}
		set["titre"] = t
	}
	if d.Description != nil {
		if len([]rune(*d.Description)) > 8000 {
			return nil, nil, errors.New("description trop longue (8000 caractères au plus)")
		}
		set["description"] = strings.TrimSpace(*d.Description)
	}
	if d.Projet != nil {
		p := strings.ToLower(strings.TrimSpace(*d.Projet))
		if p != "" {
			if _, ok := s.projetDuCatalogue(ctx, p); !ok {
				return nil, nil, fmt.Errorf("projet inconnu : %s", p)
			}
		}
		set["projet"] = p
	}
	if d.Statut != nil {
		if !statutsCarte[*d.Statut] {
			return nil, nil, fmt.Errorf("colonne inconnue : %s", *d.Statut)
		}
		set["statut"] = *d.Statut
	}
	if d.Agent != nil {
		a := strings.TrimSpace(*d.Agent)
		if a != "" {
			if _, ok := s.hqMember(a); !ok {
				return nil, nil, fmt.Errorf("agent inconnu : %s", a)
			}
		}
		set["agent"] = a
	}
	if d.Priorite != nil {
		if !prioritesCarte[*d.Priorite] {
			return nil, nil, fmt.Errorf("priorité inconnue : %s", *d.Priorite)
		}
		set["priorite"] = *d.Priorite
	}
	if d.Echeance != nil {
		if strings.TrimSpace(*d.Echeance) == "" {
			unset = append(unset, "echeance")
		} else {
			t, err := time.Parse("2006-01-02", strings.TrimSpace(*d.Echeance))
			if err != nil {
				return nil, nil, errors.New("échéance attendue au format AAAA-MM-JJ")
			}
			set["echeance"] = t
		}
	}
	if d.Ordre != nil {
		set["ordre"] = *d.Ordre
	}
	return set, unset, nil
}

func (s *Server) handleHQCreerCarte(w http.ResponseWriter, r *http.Request) {
	var d carteDonnee
	if err := httpx.Decode(r, &d); err != nil || d.Titre == nil {
		httpx.Error(w, http.StatusBadRequest, "donne au moins un titre")
		return
	}
	set, _, err := s.validerCarte(r.Context(), d)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	// La saisie validée, rangée dans une carte neuve.
	raw, _ := bson.Marshal(set)
	var c store.Carte
	_ = bson.Unmarshal(raw, &c)
	if c.Statut == "" {
		c.Statut = store.CarteAFaire
	}
	if c.Priorite == "" {
		c.Priorite = store.PrioriteNormale
	}
	created, err := s.store.CreerCarte(r.Context(), c)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"carte": carteVue{Carte: created}})
}

func (s *Server) handleHQModifierCarte(w http.ResponseWriter, r *http.Request) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	var d carteDonnee
	if err := httpx.Decode(r, &d); err != nil {
		httpx.Error(w, http.StatusBadRequest, "requête illisible")
		return
	}
	set, unset, err := s.validerCarte(r.Context(), d)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.store.ModifierCarte(r.Context(), id, set, unset...)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "carte introuvable")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"carte": s.suivreCarte(r.Context(), updated)})
}

func (s *Server) handleHQSupprimerCarte(w http.ResponseWriter, r *http.Request) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	if err := s.store.SupprimerCarte(r.Context(), id); err != nil {
		httpx.Error(w, http.StatusNotFound, "carte introuvable")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"supprimee": id.Hex()})
}

// handleHQLancerCarte confie une carte à son agent.
func (s *Server) handleHQLancerCarte(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	c, err := s.store.Carte(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "carte introuvable")
		return
	}
	if c.Statut == store.CarteEnCours && c.Run != nil {
		if _, live := s.runs.liveRunByID(*c.Run); live {
			httpx.Error(w, http.StatusConflict, "cette tâche est déjà en cours")
			return
		}
	}
	m, ok := s.hqMember(c.Agent)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "choisis d'abord l'agent qui s'en charge")
		return
	}
	var req struct {
		Precision string `json:"precision"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	run, err := s.launchHQ(user, m, s.demandeDeCarte(r.Context(), c, req.Precision), "Tâche du tableau : "+c.Titre)
	if err != nil {
		s.launchError(w, err)
		return
	}
	updated, err := s.store.ModifierCarte(r.Context(), c.ID, bson.M{"statut": store.CarteEnCours, "run": run.ID, "erreur": ""})
	if err != nil {
		slog.Warn("carte lancée mais non mise à jour", "carte", c.ID.Hex(), "err", err)
		updated = c
	}
	t := tacheView(run, "", nil)
	httpx.JSON(w, http.StatusAccepted, map[string]any{"carte": carteVue{Carte: updated, Tache: &t}})
}

// demandeDeCarte : ce que l'agent reçoit — la tâche, son projet, son
// échéance, et la forme de compte rendu attendue.
func (s *Server) demandeDeCarte(ctx context.Context, c store.Carte, precision string) string {
	var b strings.Builder
	if p, ok := s.projetDuCatalogue(ctx, c.Projet); ok {
		fmt.Fprintf(&b, "Tâche du projet « %s » (identifiant « %s ») : %s\n", p.Nom, p.ID, c.Titre)
		if len(p.Depots) > 0 {
			fmt.Fprintf(&b, "Dépôts du projet : %s. Lis d'abord ses fiches (fiches_projet).\n", strings.Join(p.Depots, ", "))
		}
	} else {
		fmt.Fprintf(&b, "Tâche : %s\n", c.Titre)
	}
	if c.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", c.Description)
	}
	if precision = strings.TrimSpace(precision); precision != "" {
		fmt.Fprintf(&b, "\nPrécision de Mathias : %s\n", precision)
	}
	if c.CompteRendu != "" {
		fmt.Fprintf(&b, "\nCe qui a déjà été fait (ton compte rendu précédent) : %s\n", clipTo(c.CompteRendu, 1500))
	}
	if c.Echeance != nil {
		fmt.Fprintf(&b, "\nÉchéance : %s. Priorité : %s.\n", c.Echeance.Format("02/01/2006"), c.Priorite)
	}
	b.WriteString("\nTa réponse sert de compte rendu sur la carte du tableau : ce qui est fait, ce qui reste, et ce que Mathias doit vérifier.")
	return b.String()
}
