package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

type sessionRequest struct {
	DeviceID string `json:"device_id"`
	Timezone string `json:"timezone"`
}

type sessionResponse struct {
	Token    string `json:"token"`
	Timezone string `json:"timezone"`
	Name     string `json:"name,omitempty"`
	New      bool   `json:"new"`
}

// handleSession remplace le couple login/register : l'app envoie l'identifiant
// unique généré à l'installation, le serveur crée le compte s'il n'existe pas.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	if len(req.DeviceID) < 8 {
		httpx.Error(w, http.StatusBadRequest, "device_id manquant ou trop court")
		return
	}
	if req.Timezone == "" {
		req.Timezone = s.cfg.DefaultTimezone
	}

	user, err := s.store.EnsureUser(r.Context(), req.DeviceID, req.Timezone)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "impossible d'initialiser l'appareil")
		return
	}

	httpx.JSON(w, http.StatusOK, sessionResponse{
		Token:    user.Token,
		Timezone: user.Timezone,
		Name:     user.Name,
		New:      time.Since(user.CreatedAt) < 5*time.Second,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	// L'app a besoin de savoir quelle voix va parler pour l'afficher dans
	// l'écran Accès. C'est de la configuration serveur, elle voyage ici plutôt
	// que dans /status, qui interroge les quatre sources et met des secondes.
	voice := map[string]any{"engine": "device"}
	if s.tts.Enabled() {
		voice = map[string]any{
			"engine":   "elevenlabs",
			"voice_id": s.tts.VoiceID(),
			"model":    s.tts.Model(),
		}
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"name":     user.Name,
		"timezone": user.Timezone,
		"since":    user.CreatedAt,
		"voice":    voice,
	})
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if err := s.store.SetUserName(r.Context(), user.ID, strings.TrimSpace(req.Name)); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "impossible d'enregistrer le nom")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"name": req.Name})
}

type sourceStatus struct {
	Provider  string `json:"provider"`
	Connected bool   `json:"connected"`
	Unread    int    `json:"unread"`
	Error     string `json:"error,omitempty"`
}

// handleStatus donne à l'écran d'accueil un bilan rapide des quatre sources.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	tb := s.toolbox(user)
	out := []sourceStatus{}

	// Mails
	mailStatus := sourceStatus{Provider: store.ProviderGandi}
	if _, err := s.gandiCreds(ctx, user); err == nil {
		mailStatus.Connected = true
		if mails, err := tb.UnreadEmails(ctx, 50); err != nil {
			mailStatus.Error = err.Error()
		} else {
			mailStatus.Unread = len(mails)
		}
	}
	out = append(out, mailStatus)

	// Slack
	slackStatus := sourceStatus{Provider: store.ProviderSlack}
	if _, err := s.slackCreds(ctx, user); err == nil {
		slackStatus.Connected = true
		if threads, err := tb.UnreadSlack(ctx, 30); err != nil {
			slackStatus.Error = err.Error()
		} else {
			for _, t := range threads {
				slackStatus.Unread += t.NonLus
			}
		}
	}
	out = append(out, slackStatus)

	// WhatsApp
	waStatus := sourceStatus{Provider: store.ProviderWhatsApp}
	if _, err := s.whatsappCreds(ctx, user); err == nil {
		waStatus.Connected = true
		if msgs, err := s.store.UnreadWhatsApp(ctx, user.ID, 200); err != nil {
			waStatus.Error = err.Error()
		} else {
			waStatus.Unread = len(msgs)
		}
	}
	out = append(out, waStatus)

	// Calendrier : synchronisé par l'app, donc « connecté » dès qu'il y a des données.
	calStatus := sourceStatus{Provider: store.ProviderCalendar}
	if conn, err := s.store.Connection(ctx, user.ID, store.ProviderCalendar); err == nil {
		calStatus.Connected = conn.Status == "connected"
	}
	now := time.Now()
	if events, err := s.store.EventsBetween(ctx, user.ID, now, now.Add(7*24*time.Hour)); err == nil {
		calStatus.Unread = len(events) // ici : nombre d'événements à venir sur 7 jours
	}
	out = append(out, calStatus)

	httpx.JSON(w, http.StatusOK, map[string]any{"sources": out})
}
