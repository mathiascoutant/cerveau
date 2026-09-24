package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/cryptoutil"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/stt"
	"github.com/mathiascoutant/cerveau/backend/internal/tts"
)

type Server struct {
	cfg    config.Config
	store  *store.Store
	cipher *cryptoutil.Cipher
	engine *assistant.Engine
	stt    *stt.Client
	tts    *tts.Client
	// wa tient les sessions WhatsApp : contrairement aux autres sources, on ne
	// l'interroge pas à la demande — c'est une connexion permanente qui reçoit
	// les messages au fil de l'eau, et le serveur la porte de bout en bout.
	wa *whatsapp.Manager

	pending *pendingOAuth
	speech  *speechTickets
	confirm *confirmations
}

func NewServer(cfg config.Config, st *store.Store, cipher *cryptoutil.Cipher, wa *whatsapp.Manager) *Server {
	return &Server{
		cfg:    cfg,
		store:  st,
		cipher: cipher,
		wa:     wa,
		engine: assistant.New(cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.OpenAIEffort).
			WithDeep(cfg.OpenAIDeepModel, cfg.OpenAIDeepEffort),
		stt: stt.New(cfg.STTBaseURL, cfg.STTAPIKey, cfg.STTModel),
		tts: tts.New(cfg.ElevenLabsAPIKey, cfg.ElevenLabsVoiceID, cfg.ElevenLabsModel, cfg.ElevenLabsLanguage),

		pending: newPendingOAuth(),
		speech:  newSpeechTickets(),
		confirm: newConfirmations(),
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

	r.Route("/api/v1", func(r chi.Router) {
		// Connexion par compte : le token revient pour cet appareil, et tout
		// ce qui est branché sur le compte le suit d'un téléphone à l'autre.
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/signup", s.handleSignup)

		// Héritage : l'app d'avant les comptes s'identifiait par son appareil.
		// Gardé pour qu'elle ne soit pas coupée avant sa mise à jour.
		r.Post("/session", s.handleSession)

		// Hors du groupe authentifié : le lecteur audio natif ne sait pas
		// poser d'en-tête Authorization. Le ticket, aléatoire et à usage
		// unique, tient lieu d'autorisation (voir handlers_speech.go).
		r.Get("/speech/{ticket}", s.handleSpeechStream)

		r.Group(func(r chi.Router) {
			r.Use(s.requireUser)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Patch("/me", s.handleUpdateMe)
			r.Get("/status", s.handleStatus)
			r.Get("/history", s.handleHistory)
			r.Get("/digest", s.handleDigest)
			r.Get("/urgent", s.handleUrgent)
			r.Post("/urgent/{id}/done", s.handleUrgentDone)

			r.Get("/connections", s.handleListConnections)
			r.Put("/connections/gandi", s.handleConnectGandi)
			r.Put("/connections/slack", s.handleConnectSlack)
			r.Post("/connections/slack/oauth", s.handleSlackOAuthStart)
			r.Post("/connections/whatsapp/pair", s.handleWhatsAppPair)
			r.Get("/connections/whatsapp/status", s.handleWhatsAppStatus)
			r.Delete("/connections/{provider}", s.handleDisconnect)

			r.Post("/calendar/sync", s.handleCalendarSync)

			r.Get("/whatsapp/conversations", s.handleWhatsAppChats)

			r.Get("/todos", s.handleListTodos)
			r.Post("/todos", s.handleCreateTodo)
			r.Patch("/todos/{id}", s.handleUpdateTodo)
			r.Delete("/todos/{id}", s.handleDeleteTodo)

			r.Get("/memory", s.handleListFacts)
			r.Post("/memory", s.handleSaveFact)
			r.Patch("/memory/{id}", s.handleUpdateFact)
			r.Delete("/memory/{id}", s.handleDeleteFact)

			r.Get("/drafts", s.handleListDrafts)
			r.Patch("/drafts/{id}", s.handleUpdateDraft)
			r.Delete("/drafts/{id}", s.handleDeleteDraft)

			r.Post("/assistant/ask", s.handleAsk)
			r.Post("/assistant/voice", s.handleVoice)
			r.Post("/assistant/speech", s.handleSpeak)
		})
	})

	return r
}
