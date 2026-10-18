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

// Son entourage, ouvert à l'écran.
//
// Raoul apprend les gens à la voix — « Cyril, c'est mon patron » — et s'en sert
// pour trier ce qui presse. Ce qui pèse autant sur ce qu'il dit doit se voir
// et se corriger : un collègue rangé « critique » par erreur ferait passer
// chacun de ses messages devant le reste, et rien ne dirait pourquoi. L'écran
// montre donc chaque personne avec sa place et ce qui permet de la
// reconnaître, le reste de ce qu'il retient, et ce qu'il a lu récemment.

const entourageExchanges = 20

type entourageResponse struct {
	Personnes []store.Fact    `json:"personnes"`
	Autres    []store.Fact    `json:"autres"`
	Echanges  []store.Echange `json:"echanges"`
}

func (s *Server) handleEntourage(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	ctx := r.Context()

	people, err := s.store.People(ctx, user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture de l'entourage impossible")
		return
	}
	facts, err := s.store.Facts(ctx, user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture de la mémoire impossible")
		return
	}
	exchanges, err := s.store.EchangesRecents(ctx, user.ID, entourageExchanges)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des échanges impossible")
		return
	}
	out := entourageResponse{Personnes: people, Autres: []store.Fact{}, Echanges: exchanges}
	if out.Personnes == nil {
		out.Personnes = []store.Fact{}
	}
	if out.Echanges == nil {
		out.Echanges = []store.Echange{}
	}
	for _, f := range facts {
		if f.Kind != store.FactPerson {
			out.Autres = append(out.Autres, f)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

type personRequest struct {
	Sujet      *string  `json:"sujet"`
	Contenu    *string  `json:"contenu"`
	Role       string   `json:"role"`
	Importance string   `json:"importance"`
	Adresses   []string `json:"adresses"`
}

// handleSavePerson ajoute quelqu'un à la main. Le prénom porte l'identité,
// comme à la voix : réécrire « Cyril » complète la fiche Cyril.
func (s *Server) handleSavePerson(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req personRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	subject, content := deref(req.Sujet), deref(req.Contenu)
	if subject == "" {
		httpx.Error(w, http.StatusBadRequest, "il faut au moins un prénom")
		return
	}
	if content == "" {
		content = strings.TrimSpace(req.Role)
	}
	if content == "" {
		httpx.Error(w, http.StatusBadRequest, "dis au moins qui c'est")
		return
	}
	saved, err := s.store.SaveFact(r.Context(), store.Fact{
		UserID: user.ID, Kind: store.FactPerson, Subject: subject, Content: content,
		Role: req.Role, Importance: req.Importance, Adresses: req.Adresses, Origin: "app",
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "impossible d'enregistrer")
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

// handleUpdatePerson corrige la place de quelqu'un — rôle, importance,
// adresses — et, quand ils sont donnés, son prénom et ce qu'on sait de lui.
func (s *Server) handleUpdatePerson(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	var req personRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if req.Sujet != nil || req.Contenu != nil {
		current, err := s.store.FactByID(r.Context(), user.ID, id)
		if err != nil {
			s.personError(w, err)
			return
		}
		subject, content := current.Subject, current.Content
		if req.Sujet != nil {
			subject = strings.TrimSpace(*req.Sujet)
		}
		if req.Contenu != nil {
			content = strings.TrimSpace(*req.Contenu)
		}
		if _, err := s.store.UpdateFact(r.Context(), user.ID, id, subject, content); err != nil {
			s.personError(w, err)
			return
		}
	}
	updated, err := s.store.UpdatePerson(r.Context(), user.ID, id, req.Role, req.Importance, req.Adresses)
	if err != nil {
		s.personError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

func (s *Server) personError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "cette personne n'est plus dans l'entourage")
		return
	}
	httpx.Error(w, http.StatusInternalServerError, "correction impossible")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
