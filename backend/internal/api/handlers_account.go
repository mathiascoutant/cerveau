package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	// Device : le nom de l'appareil, pour s'y retrouver dans ses sessions.
	Device string `json:"device"`
}

type accountResponse struct {
	Token    string `json:"token"`
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Timezone string `json:"timezone"`
}

const minPasswordLen = 8

// handleLogin : adresse + mot de passe → token de session pour cet appareil.
// Tout ce qui est branché sur le compte (mails, Slack, WhatsApp, mémoire) est
// rattaché à l'utilisateur, pas au téléphone : se connecter sur un nouvel
// appareil le retrouve tel quel.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	user, err := s.store.Authenticate(r.Context(), req.Email, req.Password)
	if errors.Is(err, store.ErrBadCredentials) {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "connexion impossible")
		return
	}
	s.issueSession(w, r, user, req.Device)
}

// handleSignup n'existe que si ALLOW_SIGNUP est activé. Par défaut le serveur
// est fermé : chaque compte consomme la clé OpenAI et ElevenLabs du serveur, et
// une inscription ouverte à qui connaît l'adresse, c'est une facture ouverte.
// Les comptes se créent alors avec `go run ./cmd/account`.
func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowSignup {
		httpx.Error(w, http.StatusForbidden, "les inscriptions sont fermées sur ce serveur")
		return
	}
	var req credentialsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	email := store.NormalizeEmail(req.Email)
	if !strings.Contains(email, "@") || !strings.Contains(email, ".") {
		httpx.Error(w, http.StatusBadRequest, "adresse mail invalide")
		return
	}
	if len(req.Password) < minPasswordLen {
		httpx.Error(w, http.StatusBadRequest, "mot de passe trop court (8 caractères minimum)")
		return
	}
	tz := req.Timezone
	if tz == "" {
		tz = s.cfg.DefaultTimezone
	}
	user, err := s.store.CreateAccount(r.Context(), email, req.Password, strings.TrimSpace(req.Name), tz)
	if errors.Is(err, store.ErrEmailTaken) {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "création du compte impossible")
		return
	}
	s.issueSession(w, r, user, req.Device)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user *store.User, device string) {
	token, err := s.store.OpenSession(r.Context(), user.ID, strings.TrimSpace(device))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "ouverture de session impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, accountResponse{
		Token:    token,
		Email:    user.Email,
		Name:     user.Name,
		Timezone: user.Timezone,
	})
}

// handleLogout ferme la session de cet appareil seulement.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.store.CloseSession(r.Context(), bearer(r)); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "déconnexion impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
