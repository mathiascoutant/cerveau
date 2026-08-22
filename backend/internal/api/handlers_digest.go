package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Une synthèse coûte un appel au modèle : on la réutilise tant qu'elle est
// fraîche, plutôt que d'en produire une à chaque ouverture de l'onglet.
const digestMaxAge = 20 * time.Minute

type digestResponse struct {
	Summary     string                   `json:"summary"`
	GeneratedAt time.Time                `json:"generated_at"`
	Stale       bool                     `json:"stale"`
	Emails      []assistant.EmailView    `json:"emails"`
	Slack       []assistant.SlackView    `json:"slack"`
	WhatsApp    []assistant.WhatsAppView `json:"whatsapp"`
	Events      []assistant.EventView    `json:"events"`
	Unavailable []string                 `json:"unavailable,omitempty"`
	// Sources : les comptes branchés. L'app s'en sert pour n'afficher que ce
	// qui existe — une tuile « 0 WhatsApp » chez quelqu'un qui n'a pas
	// WhatsApp est la version silencieuse du même bavardage.
	Sources []string `json:"sources"`
}

// handleHistory renvoie le fil des échanges avec Raoul, du plus récent au plus
// ancien.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	items, err := s.store.RecentInteractions(r.Context(), user.ID, 50)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture de l'historique impossible")
		return
	}
	if items == nil {
		items = []store.Interaction{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"interactions": items})
}

// handleDigest rassemble l'état des sources et, si besoin, régénère la synthèse.
//
// ?refresh=1 force la régénération même si le cache est frais.
func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()

	tb := s.toolbox(user)
	out := digestResponse{
		Emails:   []assistant.EmailView{},
		Slack:    []assistant.SlackView{},
		WhatsApp: []assistant.WhatsAppView{},
		Events:   []assistant.EventView{},
	}

	// Les sources distantes sont indépendantes : les interroger en séquence
	// tripleraient l'attente à l'ouverture de l'onglet. Celles qui ne sont pas
	// branchées ne sont pas interrogées du tout, et surtout pas rangées parmi
	// les indisponibles : « indisponible » veut dire « en panne », pas
	// « jamais configuré ». Signaler un compte qu'on n'a pas revient à
	// réclamer, chaque matin, un service dont on ne veut pas.
	src := s.sources(ctx, user)
	out.Sources = []string{store.ProviderCalendar}
	if src.Mail {
		out.Sources = append(out.Sources, store.ProviderGandi)
	}
	if src.Slack {
		out.Sources = append(out.Sources, store.ProviderSlack)
	}
	if src.WhatsApp {
		out.Sources = append(out.Sources, store.ProviderWhatsApp)
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	unavailable := func(label string) {
		mu.Lock()
		out.Unavailable = append(out.Unavailable, label)
		mu.Unlock()
	}

	if src.Mail {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mails, err := tb.UnreadEmails(ctx, 25)
			if err != nil {
				unavailable("mails")
				return
			}
			mu.Lock()
			out.Emails = mails
			mu.Unlock()
		}()
	}
	if src.Slack {
		wg.Add(1)
		go func() {
			defer wg.Done()
			threads, err := tb.UnreadSlack(ctx, 15)
			if err != nil {
				unavailable("slack")
				return
			}
			mu.Lock()
			out.Slack = threads
			mu.Unlock()
		}()
	}
	if src.WhatsApp {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs, err := tb.UnreadWhatsApp(ctx, 20)
			if err != nil {
				unavailable("whatsapp")
				return
			}
			mu.Lock()
			out.WhatsApp = msgs
			mu.Unlock()
		}()
	}
	wg.Wait()

	loc := tb.location()
	now := time.Now().In(loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if events, err := tb.CalendarEvents(ctx, dayStart, dayStart.Add(24*time.Hour)); err == nil {
		out.Events = events
	}

	cached, err := s.store.LatestDigest(ctx, user.ID)
	fresh := err == nil && time.Since(cached.GeneratedAt) < digestMaxAge
	if fresh && r.URL.Query().Get("refresh") == "" {
		out.Summary = cached.Summary
		out.GeneratedAt = cached.GeneratedAt
		out.Stale = false
		httpx.JSON(w, http.StatusOK, out)
		return
	}

	name := user.Name
	if name == "" {
		name = s.cfg.DefaultUserName
	}
	summary, genErr := s.engine.Summarize(ctx, assistant.DigestInput{
		Emails:    out.Emails,
		Slack:     out.Slack,
		WhatsApp:  out.WhatsApp,
		Events:    out.Events,
		Manquants: out.Unavailable,
	}, now, loc.String(), name)

	if genErr != nil {
		// Une synthèse qui échoue ne doit pas vider l'écran : on rend les
		// données brutes et la dernière synthèse connue, signalée comme datée.
		if cached != nil {
			out.Summary = cached.Summary
			out.GeneratedAt = cached.GeneratedAt
			out.Stale = true
		}
		httpx.JSON(w, http.StatusOK, out)
		return
	}

	out.Summary = summary
	out.GeneratedAt = time.Now()
	_ = s.store.SaveDigest(ctx, user.ID, summary)
	httpx.JSON(w, http.StatusOK, out)
}
