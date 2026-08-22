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

// Les réponses de mail préparées par Raoul. Elles ne partent jamais d'ici :
// l'app les affiche, l'utilisateur les copie dans son client mail.

func (s *Server) handleListDrafts(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	drafts, err := s.store.EmailDrafts(r.Context(), user.ID, r.URL.Query().Get("q"), 50)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des réponses impossible")
		return
	}
	if drafts == nil {
		drafts = []store.EmailDraft{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"drafts": drafts})
}

// handleUpdateDraft permet la retouche au clavier, quand dicter la correction
// est plus long que la taper.
func (s *Server) handleUpdateDraft(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, ok := draftID(w, r)
	if !ok {
		return
	}

	var req struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		httpx.Error(w, http.StatusBadRequest, "le corps du mail est vide")
		return
	}

	updated, err := s.store.UpdateEmailDraft(r.Context(), user.ID, id, req.Subject, req.Body)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "cette réponse n'existe plus")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "modification impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteDraft(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, ok := draftID(w, r)
	if !ok {
		return
	}

	if err := s.store.DeleteEmailDraft(r.Context(), user.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "cette réponse n'existe plus")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "suppression impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"deleted": id.Hex()})
}

func draftID(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bool) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return bson.ObjectID{}, false
	}
	return id, true
}
