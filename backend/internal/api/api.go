package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/cryptoutil"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/stt"
)

type Server struct {
	cfg    config.Config
	store  *store.Store
	cipher *cryptoutil.Cipher
	engine *assistant.Engine
	stt    *stt.Client

	pending *pendingOAuth
}

func NewServer(cfg config.Config, st *store.Store, cipher *cryptoutil.Cipher) *Server {
	return &Server{
		cfg:    cfg,
		store:  st,
		cipher: cipher,
		engine: assistant.New(cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.OpenAIEffort),
		stt:    stt.New(cfg.STTBaseURL, cfg.STTAPIKey, cfg.STTModel),

		pending: newPendingOAuth(),
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(120 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Retour d'autorisation Slack : c'est le navigateur qui arrive, l'identité
	// vient du paramètre state, pas d'un token d'appareil.
	r.Get("/oauth/slack/callback", s.handleSlackOAuthCallback)

	// Webhook Meta : pas de token utilisateur, authentifié par signature HMAC.
	r.Route("/webhooks/whatsapp", func(r chi.Router) {
		r.Get("/", s.handleWhatsAppVerify)
		r.Post("/", s.handleWhatsAppEvent)
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Pas de login : l'app poste son identifiant d'appareil et reçoit un token.
		r.Post("/session", s.handleSession)

		r.Group(func(r chi.Router) {
			r.Use(s.requireUser)

			r.Get("/me", s.handleMe)
			r.Patch("/me", s.handleUpdateMe)
			r.Get("/status", s.handleStatus)
			r.Get("/history", s.handleHistory)
			r.Get("/digest", s.handleDigest)

			r.Get("/connections", s.handleListConnections)
			r.Put("/connections/gandi", s.handleConnectGandi)
			r.Put("/connections/slack", s.handleConnectSlack)
			r.Post("/connections/slack/oauth", s.handleSlackOAuthStart)
			r.Put("/connections/whatsapp", s.handleConnectWhatsApp)
			r.Delete("/connections/{provider}", s.handleDisconnect)

			r.Post("/calendar/sync", s.handleCalendarSync)

			r.Get("/whatsapp/messages", s.handleWhatsAppMessages)
			r.Post("/whatsapp/read", s.handleWhatsAppMarkRead)

			r.Post("/assistant/ask", s.handleAsk)
			r.Post("/assistant/voice", s.handleVoice)
		})
	})

	return r
}
