package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Ce que Raoul retient, ouvert à l'écran.
//
// La mémoire se remplit à la voix, mais elle ne peut pas rester invisible.
// Quelque chose qui apprend sans qu'on voie quoi finit par se tromper sans
// qu'on sache où : une réponse part de travers, et rien ne dit que la cause est
// une ligne retenue trois semaines plus tôt. L'écran est le seul endroit où
// cette ligne redevient un objet — on la lit, on la corrige, on la jette.
//
// C'est aussi ce qui donne le droit de retenir sans demander. Raoul ne
// s'interrompt pas pour faire valider chaque fiche, parce que tout est relisible
// et rattrapable à un endroit connu. Sans cet écran, il faudrait demander à
// chaque fois — et une conversation qu'on interrompt pour confirmer ne se tient
// plus.

func (s *Server) handleListFacts(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	facts, err := s.store.Facts(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture de la mémoire impossible")
		return
	}
	if facts == nil {
		facts = []store.Fact{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"facts": facts})
}

type factRequest struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Content string `json:"content"`
}

// handleSaveFact sert l'ajout comme la correction : le sujet porte l'identité,
// réécrire « Cyril » remplace la fiche Cyril. C'est la même règle qu'à la voix,
// et c'est ce qui évite qu'une correction tapée à l'écran vienne doubler ce que
// Raoul avait retenu en écoutant.
func (s *Server) handleSaveFact(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	var req factRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		httpx.Error(w, http.StatusBadRequest, "il n'y a rien à retenir")
		return
	}

	saved, err := s.store.SaveFact(r.Context(), store.Fact{
		UserID:  user.ID,
		Kind:    req.Kind,
		Subject: req.Subject,
		Content: req.Content,
		Origin:  "app",
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "impossible de retenir ça")
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

// handleUpdateFact corrige une fiche sans la déplacer.
//
// Distinct de handleSaveFact, qui range par sujet : une fiche sans sujet est
// rangée sous son propre texte, et le corriger via l'upsert en créerait une
// seconde à côté de l'ancienne. Ici la ligne qu'on a sous les yeux reste la
// même ligne, ce qui est la seule chose que l'écran promet.
func (s *Server) handleUpdateFact(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}

	var req factRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		httpx.Error(w, http.StatusBadRequest, "il n'y a rien à retenir")
		return
	}

	updated, err := s.store.UpdateFact(r.Context(), user.ID, id, req.Subject, req.Content)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "ce n'est plus retenu")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "correction impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteFact(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}

	if err := s.store.DeleteFact(r.Context(), user.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "ce n'est plus retenu")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "suppression impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"deleted": id.Hex()})
}
