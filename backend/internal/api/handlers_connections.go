package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	conns, err := s.store.Connections(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des connexions impossible")
		return
	}
	if conns == nil {
		conns = []store.Connection{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"connections": conns})
}

// handleConnectGandi valide les identifiants IMAP avant de les stocker chiffrés.
func (s *Server) handleConnectGandi(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Host     string `json:"host"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "email et mot de passe d'application requis")
		return
	}
	if req.Host == "" {
		req.Host = gandi.DefaultHost
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	creds := gandi.Credentials{Email: req.Email, Password: req.Password, Host: req.Host}
	if err := gandi.TestConnection(ctx, creds); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	secret, err := s.cipher.SealJSON(store.GandiCredentials{
		Email: req.Email, Password: req.Password, Host: req.Host,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "chiffrement du secret impossible")
		return
	}
	if err := s.store.UpsertConnection(r.Context(), store.Connection{
		UserID: user.ID, Provider: store.ProviderGandi,
		Status: "connected", Label: req.Email, Secret: secret,
	}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"provider": store.ProviderGandi, "status": "connected", "label": req.Email})
}

func (s *Server) handleConnectSlack(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req struct {
		UserToken string `json:"user_token"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	req.UserToken = strings.TrimSpace(req.UserToken)
	if !strings.HasPrefix(req.UserToken, "xoxp-") {
		httpx.Error(w, http.StatusBadRequest, "il faut un token utilisateur Slack (xoxp-…), pas un bot token")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	team, _, err := slack.New(req.UserToken).TestConnection(ctx)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	secret, err := s.cipher.SealJSON(store.SlackCredentials{UserToken: req.UserToken})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "chiffrement du secret impossible")
		return
	}
	if err := s.store.UpsertConnection(r.Context(), store.Connection{
		UserID: user.ID, Provider: store.ProviderSlack,
		Status: "connected", Label: team, Secret: secret,
	}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"provider": store.ProviderSlack, "status": "connected", "label": team})
}

func (s *Server) handleConnectWhatsApp(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req struct {
		PhoneNumberID string `json:"phone_number_id"`
		AccessToken   string `json:"access_token"`
		WABAID        string `json:"waba_id"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	req.PhoneNumberID = strings.TrimSpace(req.PhoneNumberID)
	if req.PhoneNumberID == "" || req.AccessToken == "" {
		httpx.Error(w, http.StatusBadRequest, "phone_number_id et access_token requis")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	display, err := whatsapp.TestConnection(ctx, req.PhoneNumberID, req.AccessToken)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	secret, err := s.cipher.SealJSON(store.WhatsAppCredentials{
		PhoneNumberID: req.PhoneNumberID, AccessToken: req.AccessToken, WABAID: req.WABAID,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "chiffrement du secret impossible")
		return
	}
	// Le label porte le phone_number_id : c'est la clé qui permet au webhook Meta
	// (non authentifié côté utilisateur) de retrouver le bon compte.
	if err := s.store.UpsertConnection(r.Context(), store.Connection{
		UserID: user.ID, Provider: store.ProviderWhatsApp,
		Status: "connected", Label: req.PhoneNumberID, Secret: secret,
	}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"provider": store.ProviderWhatsApp, "status": "connected", "label": display,
	})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	provider := chi.URLParam(r, "provider")
	if err := s.store.DeleteConnection(r.Context(), user.ID, provider); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "déconnexion impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"provider": provider, "status": "disconnected"})
}

// --- lecture des credentials déchiffrés --------------------------------------

func (s *Server) gandiCreds(ctx context.Context, user *store.User) (gandi.Credentials, error) {
	conn, err := s.store.Connection(ctx, user.ID, store.ProviderGandi)
	if err != nil {
		return gandi.Credentials{}, err
	}
	var c store.GandiCredentials
	if err := s.cipher.OpenJSON(conn.Secret, &c); err != nil {
		return gandi.Credentials{}, errors.New("secret Gandi illisible, reconnecte la boîte mail")
	}
	return gandi.Credentials{Email: c.Email, Password: c.Password, Host: c.Host}, nil
}

func (s *Server) slackCreds(ctx context.Context, user *store.User) (store.SlackCredentials, error) {
	conn, err := s.store.Connection(ctx, user.ID, store.ProviderSlack)
	if err != nil {
		return store.SlackCredentials{}, err
	}
	var c store.SlackCredentials
	if err := s.cipher.OpenJSON(conn.Secret, &c); err != nil {
		return store.SlackCredentials{}, errors.New("secret Slack illisible, reconnecte Slack")
	}
	return c, nil
}

func (s *Server) whatsappCreds(ctx context.Context, user *store.User) (store.WhatsAppCredentials, error) {
	conn, err := s.store.Connection(ctx, user.ID, store.ProviderWhatsApp)
	if err != nil {
		return store.WhatsAppCredentials{}, err
	}
	var c store.WhatsAppCredentials
	if err := s.cipher.OpenJSON(conn.Secret, &c); err != nil {
		return store.WhatsAppCredentials{}, errors.New("secret WhatsApp illisible, reconnecte WhatsApp")
	}
	return c, nil
}
