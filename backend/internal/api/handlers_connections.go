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
	"github.com/mathiascoutant/cerveau/backend/internal/providers/tuleap"
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

// handleConnectTuleap valide la clé d'accès contre l'instance avant de la
// ranger chiffrée. L'utilisateur qu'elle représente est relevé au passage :
// c'est lui qui sert au filtre « assigné à moi » et à l'étiquette de l'écran.
func (s *Server) handleConnectTuleap(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if !s.cfg.TuleapEnabled() {
		httpx.Error(w, http.StatusNotImplemented, "Tuleap n'est pas configuré sur le serveur (TULEAP_BASE_URL, TULEAP_CSP_TRACKER_ID)")
		return
	}
	var req struct {
		AccessKey string `json:"access_key"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	req.AccessKey = strings.TrimSpace(req.AccessKey)
	if req.AccessKey == "" {
		httpx.Error(w, http.StatusBadRequest, "clé d'accès Tuleap requise")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	id, name, err := tuleap.New(s.cfg.TuleapBaseURL, req.AccessKey).Me(ctx)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	secret, err := s.cipher.SealJSON(store.TuleapCredentials{AccessKey: req.AccessKey, UserID: id, Name: name})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "chiffrement du secret impossible")
		return
	}
	if err := s.store.UpsertConnection(r.Context(), store.Connection{
		UserID: user.ID, Provider: store.ProviderTuleap,
		Status: "connected", Label: name, Secret: secret,
	}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	s.csp.drop(user.ID)
	httpx.JSON(w, http.StatusOK, map[string]any{"provider": store.ProviderTuleap, "status": "connected", "label": name})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	provider := chi.URLParam(r, "provider")
	if provider == store.ProviderTuleap {
		s.csp.drop(user.ID)
	}

	// WhatsApp ne se débranche pas en supprimant une ligne : il faut délier
	// l'appareil côté WhatsApp, sinon il reste listé sur le téléphone et le
	// serveur continue de recevoir les messages.
	if provider == store.ProviderWhatsApp {
		if err := s.wa.Logout(r.Context(), user.ID.Hex()); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "déliaison impossible")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"provider": provider, "status": "disconnected"})
		return
	}

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

// tuleapCreds rend la clé personnelle de l'utilisateur, ou à défaut celle du
// serveur (TULEAP_ACCESS_KEY / TULEAP_KEY), comme PXFeed-UI. Sans l'une ni
// l'autre : ErrNotFound, et l'app dit de saisir une clé.
func (s *Server) tuleapCreds(ctx context.Context, user *store.User) (store.TuleapCredentials, error) {
	conn, err := s.store.Connection(ctx, user.ID, store.ProviderTuleap)
	if errors.Is(err, store.ErrNotFound) && s.cfg.TuleapAccessKey != "" {
		return store.TuleapCredentials{AccessKey: s.cfg.TuleapAccessKey, Name: serverKeyLabel}, nil
	}
	if err != nil {
		return store.TuleapCredentials{}, err
	}
	var c store.TuleapCredentials
	if err := s.cipher.OpenJSON(conn.Secret, &c); err != nil {
		return store.TuleapCredentials{}, errors.New("secret Tuleap illisible, ressaisis la clé d'accès")
	}
	return c, nil
}

// serverKeyLabel : le nom rendu quand c'est la clé du serveur qui sert.
const serverKeyLabel = "clé du serveur"

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
