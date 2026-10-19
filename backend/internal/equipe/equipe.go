package equipe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/shared"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/projets"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Memoire : le savoir de l'équipe. *store.Store le satisfait ; les tests en
// posent un faux.
type Memoire interface {
	Noter(ctx context.Context, c store.Connaissance) (store.Connaissance, error)
	Connaissances(ctx context.Context, q store.ConnaissanceQuery) ([]store.Connaissance, error)
	OublierConnaissance(ctx context.Context, equipe, projet, sujet string) (bool, error)
}

// Equipe fait travailler les membres.
type Equipe struct {
	engine  *assistant.Engine
	projets *projets.Service
	memoire Memoire
	roster  []Membre
	routes  map[string]assistant.Route
	// classify : remplace le modèle pour choisir à qui confier une tâche
	// (tests). Nil : le modèle rapide du moteur.
	classify Classifier
}

// Config règle l'équipe.
type Config struct {
	Voix Voix
	// Modele : celui de l'équipe — le modèle fort. Un diagnostic est le
	// contraire d'une question vocale rapide : il croise des journaux, du
	// code et des mesures, et une erreur coûte plus cher qu'une seconde.
	Modele string
	// EffortIngenieur et EffortResponsable : le budget de raisonnement de
	// chacun. L'ingénieur enquête, la responsable synthétise.
	EffortIngenieur   string
	EffortResponsable string
}

// New prépare l'équipe. projets peut être nil : l'équipe parle alors sans
// outils de surveillance, avec sa seule mémoire — mais Raoul ne lui passe la
// parole que si des projets sont suivis.
func New(engine *assistant.Engine, p *projets.Service, memoire Memoire, cfg Config) *Equipe {
	if cfg.Modele == "" {
		cfg.Modele = assistant.DefaultDeepModel
	}
	if cfg.EffortIngenieur == "" {
		cfg.EffortIngenieur = assistant.DefaultDeepEffort
	}
	if cfg.EffortResponsable == "" {
		cfg.EffortResponsable = string(shared.ReasoningEffortLow)
	}
	model := shared.ResponsesModel(cfg.Modele)
	return &Equipe{
		engine:  engine,
		projets: p,
		memoire: memoire,
		roster:  Roster(cfg.Voix),
		routes: map[string]assistant.Route{
			IDIngenieur:   {Tier: assistant.TierDeep, Model: model, Effort: shared.ReasoningEffort(cfg.EffortIngenieur), Reason: "ingénieur"},
			IDResponsable: {Tier: assistant.TierDeep, Model: model, Effort: shared.ReasoningEffort(cfg.EffortResponsable), Reason: "responsable"},
		},
	}
}

// Roster rend les membres, Raoul compris.
func (e *Equipe) Roster() []Membre { return e.roster }

// Membre rend un membre par son identifiant.
func (e *Equipe) Membre(id string) (Membre, bool) {
	for _, m := range e.roster {
		if m.ID == id {
			return m, true
		}
	}
	return Membre{}, false
}

// ColleguesDeRaoul : ceux à qui Raoul peut passer la parole.
func (e *Equipe) ColleguesDeRaoul() []assistant.Collegue {
	return Collegues(e.roster, IDRaoul)
}

// Demande : une question adressée à un membre.
type Demande struct {
	Text     string
	History  []assistant.Turn
	Now      time.Time
	Timezone string
	UserName string
	CacheKey string
	// SansPassage : le membre répond lui-même, sans pouvoir passer la
	// parole — il vient de la recevoir, et la repasser ferait boucler.
	SansPassage bool
	// Suivi : qui veut savoir quand un membre travaille pour un autre — la
	// responsable qui confie une enquête à l'ingénieur. Nil : personne.
	Suivi Suivi
}

// Suivi est prévenu quand un membre se met au travail pour un collègue. Le
// bâtiment de Cerveau HQ s'en sert : l'ingénieur qui enquête pour la
// responsable est au travail, et doit l'être aussi à l'écran.
type Suivi interface {
	Debut(agent, source, demande string) Travail
}

// Travail : une exécution en cours, qu'on informe de ses étapes et de sa fin.
type Travail interface {
	Etape(ev assistant.Event)
	Fin(res assistant.Result, err error)
}

// Ask fait répondre un membre de l'équipe, en flux.
func (e *Equipe) Ask(ctx context.Context, agent string, d Demande, emit assistant.Emitter) (assistant.Result, error) {
	m, ok := e.Membre(agent)
	if !ok || m.ID == IDRaoul {
		return assistant.Result{}, fmt.Errorf("%q n'est pas un membre de l'équipe", agent)
	}
	return e.run(ctx, m, d, emit)
}

func (e *Equipe) run(ctx context.Context, m Membre, d Demande, emit assistant.Emitter) (assistant.Result, error) {
	loc := time.UTC
	if d.Timezone != "" {
		if l, err := time.LoadLocation(d.Timezone); err == nil {
			loc = l
		}
	}
	now := d.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(loc)

	caps := e.capacites()
	c := contexte{
		moi: m, roster: e.roster, userName: d.UserName, now: now, tz: loc.String(), capacites: caps,
	}
	if e.projets != nil {
		c.registre = e.projets.Registre
	}
	c.fiches, c.index = e.savoir(ctx, m)

	var instructions string
	switch m.ID {
	case IDIngenieur:
		instructions = consigneIngenieur(c)
	case IDResponsable:
		instructions = consigneResponsable(c)
	default:
		return assistant.Result{}, errors.New("ce membre n'a pas de consigne")
	}

	cacheKey := d.CacheKey
	if cacheKey != "" {
		cacheKey += ":" + m.ID
	}
	spec := assistant.Spec{
		Agent:         m.ID,
		Route:         e.routes[m.ID],
		Instructions:  instructions,
		Tools:         e.toolsFor(m, caps, d.SansPassage),
		Label:         labelOf,
		MaxIterations: 14,
		ToolTimeout:   toolTimeout,
		// Deux minutes et demie d'enquête, puis il conclut : la demande
		// entière n'a que cinq minutes, et la responsable qui a délégué doit
		// encore restituer.
		Budget:   150 * time.Second,
		CacheKey: cacheKey,
		Exec: func(ctx context.Context, name, args string) (assistant.ToolOutput, error) {
			return e.exec(ctx, m, d, emit, name, args)
		},
	}
	return e.engine.Run(ctx, spec, d.History, d.Text, emit)
}

func (e *Equipe) capacites() capacites {
	if e.projets == nil {
		return capacites{}
	}
	pc := e.projets.Capacites()
	return capacites{
		projets:   true,
		sonde:     pc.Sonde,
		github:    pc.GitHub,
		code:      pc.GitHub,
		recherche: pc.GitHubAuthentifie,
	}
}

// savoir rend les fiches de fond (« Vue d'ensemble » de chaque projet, et du
// général) en entier, et le titre des autres.
func (e *Equipe) savoir(ctx context.Context, m Membre) (fiches, index []store.Connaissance) {
	if e.memoire == nil {
		return nil, nil
	}
	all, err := e.memoire.Connaissances(ctx, store.ConnaissanceQuery{Equipe: m.equipe(), Limit: 200})
	if err != nil {
		slog.Warn("mémoire de l'équipe illisible", "err", err)
		return nil, nil
	}
	budget := 24000
	for _, c := range all {
		if isOverview(c.Sujet) && budget > 0 {
			fiches = append(fiches, c)
			budget -= len(c.Contenu)
			continue
		}
		if len(index) < 120 {
			index = append(index, c)
		}
	}
	return fiches, index
}

func isOverview(subject string) bool {
	s := strings.ToLower(strings.TrimSpace(subject))
	return s == "vue d'ensemble" || s == "vue d’ensemble" || s == "vue ensemble"
}
