package api

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/atelier"
	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/cryptoutil"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/projets"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/stt"
	"github.com/mathiascoutant/cerveau/backend/internal/tri"
	"github.com/mathiascoutant/cerveau/backend/internal/tts"
)

type Server struct {
	cfg    config.Config
	store  *store.Store
	cipher *cryptoutil.Cipher
	engine *assistant.Engine
	stt    *stt.Client
	tts    *tts.Client
	// voixHQ : les voix gratuites de Cerveau HQ (Microsoft Edge, par le
	// service deploy/voix). VOIX=appareil vaut pour l'app ; à l'ordinateur,
	// l'équipe garde une voix humaine.
	voixHQ *tts.Edge
	// elevenHQ : ElevenLabs pour HQ, même quand l'app reste aux voix du
	// téléphone. Seul Hugo s'en sert, avec voixHQ en relais quand le crédit
	// gratuit est épuisé.
	elevenHQ *tts.Client
	// wa tient les sessions WhatsApp : contrairement aux autres sources, on ne
	// l'interroge pas à la demande — c'est une connexion permanente qui reçoit
	// les messages au fil de l'eau, et le serveur la porte de bout en bout.
	wa *whatsapp.Manager

	pending *pendingOAuth
	speech  *speechTickets
	confirm *confirmations
	// csp : cache court des tickets Tuleap, par utilisateur (voir handlers_jobs.go).
	csp *cspCache

	// projets et equipe : l'espace perso technique. Nil tant qu'aucun
	// registre de projets n'est configuré — et alors ni écran Projets, ni
	// passage de parole.
	projets *projets.Service
	equipe  *equipe.Equipe
	// runs : le travail réel des agents — suivi, file, limites. Voir
	// hq_runs.go.
	runs *orchestre
	// atelier : Claude Code sur le VPS, pour les tâches confiées aux agents.
	// Nil : ces tâches passent par le moteur habituel.
	atelier *atelier.Client
	// trieur : Claude par l'API, qui range les mails pro qui arrivent.
	trieur *tri.Trieur
	// direct : Hugo répond lui-même, sans l'atelier, à ce qui ne demande pas
	// l'équipe (pilote_rapide.go). Nil sans clé Claude.
	direct *piloteDirect
	// triDossiers : les comptes dont les dossiers du tri ont été vérifiés
	// depuis le lancement.
	triDossiers sync.Map
	// veilleSlackAt : la dernière relève Slack de la veille (voir veille.go).
	veilleSlackAt time.Time
}

// WithPerso branche le suivi des projets et l'équipe technique qui va avec.
func (s *Server) WithPerso(p *projets.Service) *Server {
	if p == nil {
		return s
	}
	s.projets = p
	s.equipe = equipe.New(s.engine, p, s.store, equipe.Config{
		Voix:              equipe.Voix{Raoul: s.tts.VoiceID(), Ingenieur: s.cfg.VoixIngenieur, Responsable: s.cfg.VoixResponsable, Pilote: s.cfg.VoixPilote},
		Modele:            s.cfg.EquipeModele,
		EffortIngenieur:   s.cfg.EffortIngenieur,
		EffortResponsable: s.cfg.EffortResponsable,
	})
	return s
}

func NewServer(cfg config.Config, st *store.Store, cipher *cryptoutil.Cipher, wa *whatsapp.Manager) *Server {
	return &Server{
		cfg:    cfg,
		store:  st,
		cipher: cipher,
		wa:     wa,
		engine: assistant.New(cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.OpenAIEffort).
			WithFast(cfg.OpenAIFastModel, cfg.OpenAIFastEffort).
			WithDeep(cfg.OpenAIDeepModel, cfg.OpenAIDeepEffort),
		csp:    newCSPCache(),
		stt:    stt.New(cfg.STTBaseURL, cfg.STTAPIKey, cfg.STTModel),
		tts:    tts.New(elevenLabsKey(cfg), cfg.ElevenLabsVoiceID, cfg.ElevenLabsModel, cfg.ElevenLabsLanguage),
		voixHQ: tts.NewEdge(cfg.VoixGratuiteURL, ""),
		// multilingual v2 : le plus naturel en français, celui que Mathias a
		// écouté pour choisir la voix de Hugo.
		elevenHQ: tts.New(cfg.ElevenLabsAPIKey, "", "eleven_multilingual_v2", cfg.ElevenLabsLanguage),

		pending: newPendingOAuth(),
		speech:  newSpeechTickets(),
		confirm: newConfirmations(),
		runs:    newOrchestre(st, cfg.AgentsConcurrence, cfg.AgentsBudgetJour),
		atelier: newAtelier(cfg),
		trieur:  tri.New(cfg.AnthropicAPIKey, cfg.TriModele),
		direct:  newPiloteDirect(cfg.AnthropicAPIKey, cfg.PiloteModele),
	}
}

func newAtelier(cfg config.Config) *atelier.Client {
	c, err := atelier.New(sonde.Config{Cible: cfg.AtelierSSH, Cle: cfg.AtelierCle, CleHote: cfg.AtelierCleHote, KnownHosts: cfg.AtelierKnownHosts})
	if err != nil {
		slog.Error("atelier mal configuré : les tâches restent au moteur habituel", "err", err)
		return nil
	}
	if c != nil {
		slog.Info("atelier actif : les tâches des agents passent par Claude Code")
	}
	return c
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	// Deux minutes pour tout, sauf pour les questions à l'assistant : une
	// enquête de l'ingénieur dure plus longtemps, et la couper au milieu
	// jetterait tout le travail fait.
	r.Use(func(next http.Handler) http.Handler {
		short := middleware.Timeout(120 * time.Second)(next)
		long := middleware.Timeout(conversationTimeout + 30*time.Second)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/api/v1/assistant/stream" || req.URL.Path == "/api/v1/assistant/ask" {
				long.ServeHTTP(w, req)
				return
			}
			short.ServeHTTP(w, req)
		})
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Cerveau HQ, le bâtiment des agents : servi par le même serveur que
	// l'API, pour qu'il n'y ait ni CORS ni second domaine à gérer.
	if s.cfg.HQDir != "" {
		r.Handle("/hq", http.RedirectHandler("/hq/", http.StatusMovedPermanently))
		r.Handle("/hq/*", http.StripPrefix("/hq", spa(s.cfg.HQDir)))
	}

	// Le serveur MCP de l'atelier : les outils de lecture de Raoul, avec son
	// propre jeton (voir mcp.go).
	if s.cfg.AtelierMCPToken != "" && s.cfg.AtelierMCPEmail != "" {
		r.Post("/mcp", s.handleMCP)
		r.Get("/mcp", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })
		r.Delete("/mcp", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })
	}

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
			r.Post("/auth/claim", s.handleClaim)
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
			r.Put("/connections/tuleap", s.handleConnectTuleap)
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

			// Son entourage pro : les gens, leur place, ce que Raoul a lu.
			r.Get("/entourage", s.handleEntourage)
			r.Post("/entourage", s.handleSavePerson)
			r.Patch("/entourage/{id}", s.handleUpdatePerson)

			r.Get("/drafts", s.handleListDrafts)
			r.Patch("/drafts/{id}", s.handleUpdateDraft)
			r.Delete("/drafts/{id}", s.handleDeleteDraft)

			r.Get("/jobs", s.handleJobs)
			r.Get("/jobs/csp/tickets", s.handleCSPTickets)
			r.Get("/jobs/csp/tickets/{id}", s.handleCSPTicket)

			// L'espace perso : ses projets, et l'équipe qui les suit.
			r.Get("/perso/projets", s.handleProjets)
			r.Get("/equipe", s.handleEquipe)

			// Cerveau HQ : les agents réels, leur état et leurs tâches.
			r.Get("/hq/agents", s.handleHQAgents)
			r.Get("/hq/agents/{id}", s.handleHQAgent)
			r.Post("/hq/agents/{id}/taches", s.handleHQLaunch)
			r.Post("/hq/taches", s.handleHQAssign)
			r.Get("/hq/notifications", s.handleHQNotifications)
			r.Post("/hq/notifications/lues", s.handleHQNotificationLue)
			r.Post("/hq/notifications/{id}/lue", s.handleHQNotificationLue)
			r.Get("/hq/projets", s.handleHQProjets)
			r.Post("/hq/projets", s.handleHQAjouterProjet)
			r.Get("/hq/projets/{id}", s.handleHQProjet)
			r.Patch("/hq/projets/{id}", s.handleHQCompleterProjet)
			r.Delete("/hq/projets/{id}", s.handleHQOublierProjet)
			r.Post("/hq/projets/{id}/etudier", s.handleHQEtudierProjet)
			r.Post("/hq/projets/{id}/questions/{qid}", s.handleHQRepondreQuestion)
			r.Get("/hq/cartes", s.handleHQCartes)
			r.Post("/hq/cartes", s.handleHQCreerCarte)
			r.Patch("/hq/cartes/{id}", s.handleHQModifierCarte)
			r.Delete("/hq/cartes/{id}", s.handleHQSupprimerCarte)
			r.Post("/hq/cartes/{id}/lancer", s.handleHQLancerCarte)
			r.Get("/hq/taches/{id}", s.handleHQTask)
			r.Post("/hq/taches/{id}/annuler", s.handleHQCancel)
			r.Put("/equipe/interlocuteur", s.handleInterlocuteur)
			r.Post("/equipe/{id}/voix", s.handleVoixMembre)
			r.Get("/equipe/connaissances", s.handleConnaissances)
			r.Delete("/equipe/connaissances/{id}", s.handleOublierConnaissance)

			r.Post("/assistant/ask", s.handleAsk)
			r.Post("/assistant/stream", s.handleAskStream)
			r.Post("/assistant/voice", s.handleVoice)
			r.Post("/assistant/speech", s.handleSpeak)
		})
	})

	return r
}

// elevenLabsKey : la clé ElevenLabs, sauf quand la configuration demande les
// voix gratuites du téléphone (VOIX=appareil). Sans clé, le serveur ne prépare
// plus aucun son et l'app parle avec les voix de l'iPhone.
func elevenLabsKey(cfg config.Config) string {
	if cfg.Voix == "appareil" {
		return ""
	}
	return cfg.ElevenLabsAPIKey
}
