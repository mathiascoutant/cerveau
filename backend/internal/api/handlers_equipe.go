package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// L'espace perso, côté app : l'écran Projets et l'écran Équipe.

const persoDisabled = "aucun projet perso n'est suivi sur ce serveur (PROJETS_REGISTRE)"

// handleProjets rend le point d'ensemble sur ses projets. ?refresh=1 refait
// les relevés au lieu de prendre ceux de moins d'une minute.
func (s *Server) handleProjets(w http.ResponseWriter, r *http.Request) {
	if s.projets == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	etat, err := s.projets.Etat(ctx, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "relevé impossible : "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, etat)
}

// handleEquipe rend l'équipe et qui a la parole.
func (s *Server) handleEquipe(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	user := userFrom(r.Context())
	agent := s.interlocutor(r.Context(), user)
	if agent == "" {
		agent = equipe.IDRaoul
	}
	// L'écran Équipe de l'app sert à parler aux membres : la veille, qui ne
	// parle pas, n'y figure pas (elle est dans Cerveau HQ).
	var membres []equipe.Membre
	for _, m := range s.equipe.Roster() {
		if m.Veille == "" && !m.Pilote {
			membres = append(membres, m)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"membres":       membres,
		"interlocuteur": agent,
		"capacites":     s.projets.Capacites(),
	})
}

// handleInterlocuteur change d'interlocuteur depuis l'app — l'écran Équipe,
// ou le bouton qui rend la parole à Raoul.
func (s *Server) handleInterlocuteur(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	var req struct {
		Agent string `json:"agent"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	m, ok := s.equipe.Membre(strings.TrimSpace(req.Agent))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "membre de l'équipe inconnu")
		return
	}
	next := m.ID
	if next == equipe.IDRaoul {
		next = ""
	}
	user := userFrom(r.Context())
	if err := s.store.PasserLaParole(r.Context(), user.ID, next); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "changement d'interlocuteur impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"interlocuteur": m.ID})
}

// handleVoixMembre fait entendre la voix d'un membre : une phrase de
// présentation, pour savoir qui on entendra.
func (s *Server) handleVoixMembre(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	if !s.tts.Enabled() {
		httpx.Error(w, http.StatusNotImplemented, "synthèse vocale non configurée")
		return
	}
	m, ok := s.equipe.Membre(chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "membre inconnu")
		return
	}
	user := userFrom(r.Context())
	text := m.Bonjour
	if text == "" {
		text = "Salut, c'est " + m.Nom + ", " + strings.ToLower(m.Role) + "."
	}
	url := s.prepareSpeechAs(user.ID, text, s.voiceOf(m.ID))
	if url == "" {
		httpx.Error(w, http.StatusInternalServerError, "impossible de préparer la lecture")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"url": url, "texte": text})
}

// handleConnaissances rend ce que l'équipe sait, projet par projet.
func (s *Server) handleConnaissances(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	list, err := s.store.Connaissances(r.Context(), store.ConnaissanceQuery{
		Equipe:    equipe.EquipeInformatique,
		Projet:    r.URL.Query().Get("projet"),
		Recherche: r.URL.Query().Get("q"),
		Limit:     300,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture impossible")
		return
	}
	if list == nil {
		list = []store.Connaissance{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"connaissances": list})
}

// handleOublierConnaissance retire une fiche, au doigt.
func (s *Server) handleOublierConnaissance(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	if err := s.store.SupprimerConnaissance(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "fiche introuvable")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "suppression impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"deleted": id.Hex()})
}
