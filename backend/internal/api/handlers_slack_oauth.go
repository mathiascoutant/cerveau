package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html"
	"net/http"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// pendingOAuth retient les demandes d'autorisation en cours.
//
// Le paramètre `state` d'OAuth sert à deux choses : empêcher un tiers de forger
// un retour de callback, et retrouver à quel utilisateur rattacher le token.
// On garde ça en mémoire — une autorisation qui traîne plus de dix minutes n'a
// aucune raison d'être honorée.
type pendingOAuth struct {
	mu    sync.Mutex
	items map[string]pendingEntry
}

type pendingEntry struct {
	userID  bson.ObjectID
	expires time.Time
}

func newPendingOAuth() *pendingOAuth {
	return &pendingOAuth{items: map[string]pendingEntry{}}
}

func (p *pendingOAuth) issue(userID bson.ObjectID) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := hex.EncodeToString(buf)

	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, v := range p.items { // purge opportuniste
		if now.After(v.expires) {
			delete(p.items, k)
		}
	}
	p.items[state] = pendingEntry{userID: userID, expires: now.Add(10 * time.Minute)}
	return state, nil
}

func (p *pendingOAuth) consume(state string) (bson.ObjectID, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.items[state]
	if !ok || time.Now().After(entry.expires) {
		delete(p.items, state)
		return bson.ObjectID{}, false
	}
	delete(p.items, state) // usage unique
	return entry.userID, true
}

// handleSlackOAuthStart renvoie l'URL de consentement à ouvrir dans le navigateur.
func (s *Server) handleSlackOAuthStart(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.SlackOAuthEnabled() {
		httpx.Error(w, http.StatusNotImplemented,
			"OAuth Slack non configuré côté serveur (SLACK_CLIENT_ID, SLACK_CLIENT_SECRET, PUBLIC_BASE_URL)")
		return
	}
	user := userFrom(r.Context())

	state, err := s.pending.issue(user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "impossible de préparer l'autorisation")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]string{
		"url": slack.AuthorizeURL(s.cfg.SlackClientID, s.cfg.SlackRedirectURI(), state),
	})
}

// handleSlackOAuthCallback reçoit le retour de Slack, échange le code et
// enregistre le token chiffré. Pas d'authentification par token d'appareil ici :
// c'est le navigateur qui arrive, l'identité vient du `state`.
func (s *Server) handleSlackOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if slackErr := q.Get("error"); slackErr != "" {
		renderOAuthPage(w, http.StatusOK, "Autorisation refusée", "Slack a renvoyé : "+slackErr)
		return
	}

	userID, ok := s.pending.consume(q.Get("state"))
	if !ok {
		renderOAuthPage(w, http.StatusBadRequest, "Lien expiré",
			"Cette autorisation a déjà servi ou date de plus de dix minutes. Relance la connexion depuis l'app.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	res, err := slack.ExchangeCode(ctx, s.cfg.SlackClientID, s.cfg.SlackClientSecret,
		s.cfg.SlackRedirectURI(), q.Get("code"))
	if err != nil {
		renderOAuthPage(w, http.StatusBadGateway, "Échec de la connexion", err.Error())
		return
	}

	secret, err := s.cipher.SealJSON(store.SlackCredentials{UserToken: res.UserToken})
	if err != nil {
		renderOAuthPage(w, http.StatusInternalServerError, "Erreur interne", "chiffrement du secret impossible")
		return
	}
	if err := s.store.UpsertConnection(ctx, store.Connection{
		UserID: userID, Provider: store.ProviderSlack,
		Status: "connected", Label: res.TeamName, Secret: secret,
	}); err != nil {
		renderOAuthPage(w, http.StatusInternalServerError, "Erreur interne", "enregistrement impossible")
		return
	}

	renderOAuthPage(w, http.StatusOK, "Slack connecté",
		"Espace de travail « "+res.TeamName+" ». Tu peux fermer cet onglet et revenir dans Raoul.")
}

func renderOAuthPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="fr"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Raoul — Slack</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
background:#0B0E14;color:#EEF2F8;font:16px/1.5 -apple-system,BlinkMacSystemFont,sans-serif}
main{max-width:32rem;padding:2rem;text-align:center}
h1{font-size:1.5rem;margin:0 0 .75rem}p{color:#8C97AC;margin:0}
</style></head><body><main><h1>` + html.EscapeString(title) + `</h1><p>` +
		html.EscapeString(message) + `</p></main></body></html>`))
}
