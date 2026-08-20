package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// handleWhatsAppVerify répond au challenge de vérification du webhook Meta.
func (s *Server) handleWhatsAppVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("hub.mode") == "subscribe" && q.Get("hub.verify_token") == s.cfg.WhatsAppVerifyToken && s.cfg.WhatsAppVerifyToken != "" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(q.Get("hub.challenge")))
		return
	}
	http.Error(w, "verify token invalide", http.StatusForbidden)
}

// handleWhatsAppEvent enregistre les messages entrants. C'est notre seule source
// WhatsApp : l'API officielle ne permet pas de relire l'historique.
func (s *Server) handleWhatsAppEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps illisible")
		return
	}
	defer r.Body.Close()

	if !whatsapp.VerifySignature(s.cfg.WhatsAppAppSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		slog.Warn("webhook whatsapp : signature invalide")
		http.Error(w, "signature invalide", http.StatusForbidden)
		return
	}

	messages, err := whatsapp.ParseWebhook(body)
	if err != nil {
		// Meta renvoie aussi des événements de statut : on acquitte quand même,
		// sinon Meta désactive le webhook après trop d'erreurs.
		slog.Warn("webhook whatsapp : payload non exploitable", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	for _, m := range messages {
		user, err := s.store.UserByWhatsAppPhoneNumberID(r.Context(), m.PhoneNumberID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				slog.Error("webhook whatsapp : résolution du compte", "err", err)
			}
			continue
		}
		err = s.store.SaveWhatsAppMessage(r.Context(), store.WhatsAppMessage{
			UserID:    user.ID,
			MessageID: m.MessageID,
			From:      m.From,
			FromName:  m.FromName,
			Body:      m.Body,
			Type:      m.Type,
			Timestamp: m.Timestamp,
			Read:      false,
		})
		if err != nil {
			slog.Error("webhook whatsapp : enregistrement", "err", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleWhatsAppMessages(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	msgs, err := s.store.UnreadWhatsApp(r.Context(), user.ID, 100)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture impossible")
		return
	}
	if msgs == nil {
		msgs = []store.WhatsAppMessage{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) handleWhatsAppMarkRead(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if err := s.store.MarkWhatsAppRead(r.Context(), user.ID, req.IDs); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "mise à jour impossible")
		return
	}

	// Best effort : poser aussi la coche bleue côté WhatsApp.
	if creds, err := s.whatsappCreds(r.Context(), user); err == nil {
		go func(ids []string, c store.WhatsAppCredentials) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for _, id := range ids {
				if err := whatsapp.MarkRead(ctx, c.PhoneNumberID, c.AccessToken, id); err != nil {
					slog.Debug("whatsapp mark read", "err", err)
				}
			}
		}(req.IDs, creds)
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}
