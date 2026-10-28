package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Hugo pilote l'équipe par l'API Claude.
//
// Dans l'atelier, chaque demande à Hugo lançait Claude Code sur le VPS :
// démarrage, mise à jour des dépôts, puis un suivi des agents par le serveur
// MCP, relu toutes les deux secondes. Or Hugo ne code pas : il lit, découpe,
// confie et rassemble. Ici, il réfléchit avec le même modèle et le même effort,
// mais directement dans Cerveau : ses outils de délégation sont des appels de
// fonction, sans aller-retour, et il ne prend pas de place à l'équipe pendant
// qu'il l'attend. Ce qui touche au code ou aux serveurs, il le confie à
// l'agent compétent, qui travaille toujours dans l'atelier.

const (
	// DefaultPiloteAPIModele et DefaultPiloteAPIEffort : ceux de l'atelier.
	DefaultPiloteAPIModele = "claude-opus-5-5"
	DefaultPiloteAPIEffort = "high"
	// piloteAPITours : au-delà, il conclut avec ce qu'il a.
	piloteAPITours = 40
)

type piloteAPI struct {
	client anthropic.Client
	modele string
	effort anthropic.BetaOutputConfigEffort
}

func newPiloteAPI(cfg config.Config) *piloteAPI {
	if cfg.AnthropicAPIKey == "" || !cfg.PiloteAPI {
		return nil
	}
	modele, effort := cfg.PiloteAPIModele, cfg.PiloteAPIEffort
	if modele == "" {
		modele = DefaultPiloteAPIModele
	}
	if effort == "" {
		effort = DefaultPiloteAPIEffort
	}
	return &piloteAPI{
		client: anthropic.NewClient(option.WithAPIKey(cfg.AnthropicAPIKey)),
		modele: modele,
		effort: anthropic.BetaOutputConfigEffort(effort),
	}
}

// errPiloteRepli : l'API n'a rien pu faire avant la moindre délégation ;
// la demande peut repartir à l'atelier sans rien faire deux fois.
var errPiloteRepli = errors.New("pilotage par l'API indisponible")

// piloterParAPI fait travailler Hugo sur une demande, jusqu'à sa réponse.
func (s *Server) piloterParAPI(ctx context.Context, user *store.User, moi equipe.Membre, demande string, lr *liveRun) (assistant.Result, error) {
	p := s.pilotage
	lr.mu.Lock()
	caller := runSnapshot{Run: lr.run}
	lr.mu.Unlock()

	specs := append(assistant.ReadToolSpecs(s.sources(ctx, user)), s.outilsProjets(moi)...)
	specs = append(specs, s.outilsPanneaux(moi)...)
	specs = append(specs, s.piloteTools(moi, suiviMaxAttenteAPI)...)
	tools := make([]anthropic.BetaToolUnionParam, 0, len(specs))
	for _, t := range specs {
		tools = append(tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schemaOutil(t.Schema),
		}})
	}
	adaptive := anthropic.BetaThinkingConfigAdaptiveParam{}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(p.modele),
		MaxTokens: 16000,
		System: []anthropic.BetaTextBlockParam{
			// Ce qui ne change pas d'une demande à l'autre d'abord : le
			// cache sert aussi d'une demande à la suivante.
			{Text: s.consignePiloteAPI(ctx, moi), CacheControl: anthropic.NewBetaCacheControlEphemeralParam()},
			{Text: s.contextePiloteAPI(ctx, user, moi)},
		},
		Thinking:     anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &adaptive},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: p.effort},
		Tools:        tools,
		// Chaque tour relit le précédent depuis le cache.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		// Un refus des garde-fous est repris par un autre modèle, dans le
		// même appel.
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(demande))},
	}

	noms := &sync.Map{} // tâche confiée → nom de l'agent, pour les étapes
	delegue := false
	for range piloteAPITours {
		resp, err := p.client.Beta.Messages.New(ctx, params)
		if err != nil {
			if !delegue && ctx.Err() == nil {
				return assistant.Result{}, fmt.Errorf("%w : %v", errPiloteRepli, err)
			}
			return assistant.Result{}, err
		}
		params.Messages = append(params.Messages, resp.ToParam())

		var texte strings.Builder
		var appels []anthropic.BetaToolUseBlock
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.BetaTextBlock:
				texte.WriteString(b.Text)
			case anthropic.BetaToolUseBlock:
				appels = append(appels, b)
			}
		}
		switch resp.StopReason {
		case anthropic.BetaStopReasonToolUse:
		case anthropic.BetaStopReasonRefusal:
			if !delegue {
				return assistant.Result{}, fmt.Errorf("%w : demande refusée par le modèle", errPiloteRepli)
			}
			return assistant.Result{}, errors.New("le modèle a refusé de continuer")
		default:
			reply := strings.TrimSpace(texte.String())
			if reply == "" {
				return assistant.Result{}, fmt.Errorf("réponse vide (%s)", resp.StopReason)
			}
			return assistant.Result{Reply: reply, Agent: moi.ID}, nil
		}

		// Les appels d'un même tour partent ensemble : Hugo confie ou suit
		// plusieurs tâches à la fois. Leurs réponses reviennent ensemble.
		resultats := make([]anthropic.BetaContentBlockParamUnion, len(appels))
		var wg sync.WaitGroup
		for i, a := range appels {
			if a.Name == "confier_a_un_agent" {
				delegue = true
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				args := a.JSON.Input.Raw()
				label := labelPilote(a.Name, args, noms)
				debut := time.Now()
				lr.Etape(assistant.Event{Type: "status", Tool: a.Name, Label: label})
				out, err := s.outilPiloteAPI(ctx, user, caller, moi, a.Name, args)
				fin := assistant.Event{Type: "status", Tool: a.Name, Label: label, Ms: max(1, time.Since(debut).Milliseconds())}
				if err != nil {
					fin.Err = err.Error()
					resultats[i] = anthropic.NewBetaToolResultBlock(a.ID, err.Error(), true)
				} else {
					retenirNom(a.Name, out, noms)
					resultats[i] = anthropic.NewBetaToolResultBlock(a.ID, out, false)
				}
				lr.Etape(fin)
			}()
		}
		wg.Wait()
		params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(resultats...))
	}
	return assistant.Result{}, fmt.Errorf("pas de conclusion après %d tours", piloteAPITours)
}

// outilPiloteAPI exécute un outil de Hugo : déléguer, les projets, ou ce
// que Raoul sait lire.
func (s *Server) outilPiloteAPI(ctx context.Context, user *store.User, caller runSnapshot, moi equipe.Membre, name, args string) (string, error) {
	if args == "" || args == "null" {
		args = "{}"
	}
	switch {
	case name == "confier_a_un_agent" || name == "suivre_tache":
		return s.piloteCall(ctx, user, caller, moi, name, args, suiviMaxAttenteAPI)
	case estOutilProjets(name):
		return s.appelOutilProjets(ctx, moi, name, args)
	case estOutilPanneaux(name):
		return s.appelOutilPanneaux(ctx, caller, moi, name, args)
	}
	ctx, cancel := context.WithTimeout(ctx, mcpToolTimeout)
	defer cancel()
	return s.engine.RunReadTool(ctx, s.toolbox(user), s.sources(ctx, user), s.location(user), name, args)
}

// schemaOutil : le schéma d'un outil de Cerveau, au format de l'API.
func schemaOutil(schema map[string]any) anthropic.BetaToolInputSchemaParam {
	out := anthropic.BetaToolInputSchemaParam{Properties: schema["properties"]}
	switch req := schema["required"].(type) {
	case []string:
		out.Required = req
	case []any:
		for _, r := range req {
			if s, ok := r.(string); ok {
				out.Required = append(out.Required, s)
			}
		}
	}
	for k, v := range schema {
		if k != "type" && k != "properties" && k != "required" {
			if out.ExtraFields == nil {
				out.ExtraFields = map[string]any{}
			}
			out.ExtraFields[k] = v
		}
	}
	return out
}

// labelPilote : l'étape telle qu'on la lit dans le bâtiment (« Confie à
// Léo », « Attend Léo »).
func labelPilote(name, args string, noms *sync.Map) string {
	var in struct {
		Agent string `json:"agent"`
		Tache string `json:"tache"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	switch name {
	case "confier_a_un_agent":
		if nom := nomAgent(in.Agent); nom != "" {
			return "Confie à " + nom
		}
		return "Confie une tâche"
	case "suivre_tache":
		if nom, ok := noms.Load(in.Tache); ok {
			return "Attend " + nom.(string)
		}
		return "Suit une tâche"
	}
	return assistant.ToolLabel(name)
}

var nomsAgents sync.Map // identifiant → nom, rempli par retenirNom

func nomAgent(id string) string {
	if nom, ok := nomsAgents.Load(id); ok {
		return nom.(string)
	}
	return ""
}

// retenirNom : la réponse de confier_a_un_agent donne la tâche et le nom de
// l'agent ; suivre_tache s'en sert pour son libellé.
func retenirNom(name, out string, noms *sync.Map) {
	if name != "confier_a_un_agent" {
		return
	}
	var r struct {
		Tache string `json:"tache"`
		Agent string `json:"agent"`
	}
	if json.Unmarshal([]byte(out), &r) == nil && r.Tache != "" {
		noms.Store(r.Tache, r.Agent)
	}
}

// consignePiloteAPI : ce que Hugo sait de son rôle et de son équipe — la
// partie qui ne change pas d'une demande à l'autre.
func (s *Server) consignePiloteAPI(ctx context.Context, moi equipe.Membre) string {
	for _, m := range s.equipe.Candidats() {
		nomsAgents.Store(m.ID, m.Nom)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `Tu es %s, %s de l'équipe d'agents de Mathias. %s
Mathias te parle depuis Cerveau HQ. Tu pilotes l'équipe : tu n'as ni terminal ni dépôts. Tout ce qui demande de lire ou de modifier du code, de toucher à un dépôt (branches, fusions, push), d'interroger un serveur ou une application, tu le confies à l'agent compétent : lui travaille sur le VPS, avec les dépôts et Claude Code. Tes outils de lecture — mails, Slack, WhatsApp, agenda, tâches, fiches des projets — te servent à vérifier rapidement un point ou à donner du contexte à une consigne.

Ton équipe :
`, moi.Nom, strings.ToLower(moi.Role), moi.Presentation)
	b.WriteString(s.equipeDuPilote(moi))
	fmt.Fprintf(&b, `
Comment tu travailles :
1. Découpe la demande et confie chaque partie à l'agent compétent avec confier_a_un_agent : son identifiant, et une consigne complète et autonome (le contexte, y compris ce que Mathias a dit dans vos derniers échanges quand ça compte ; ce que tu attends ; sous quelle forme). Les parties indépendantes partent dans le même tour.
2. Dans le tour suivant, appelle suivre_tache pour chaque tâche confiée, toutes ensemble. Rappelle-le tant qu'une tâche n'est pas finie. Ne conclus pas avant d'avoir toutes les réponses.
3. Fais circuler l'information. Si un agent dit qu'il lui manque quelque chose — une documentation, un échange, une décision —, cherche-le avec tes outils de lecture ou demande-le à l'agent qui peut le trouver (la veille des mails pour un document reçu par mail, celle de Slack ou de WhatsApp pour une discussion), puis renvoie une tâche au premier agent avec ce qu'il lui manquait.
4. Tu ne fais pas toi-même le travail d'un spécialiste. %d délégations au plus par demande.
5. N'invente jamais un état ou un résultat : tu ne sais que ce que tes outils et ton équipe t'ont dit.
6. Quand Mathias veut voir quelque chose d'un projet dans HQ (« ajoute sur mon dashboard… », un suivi, des chiffres, une liste qui se met à jour), confie-le à l'ingénieur tout de suite, sans autre étape : le projet (son identifiant) et sa demande telle quelle, en lui disant que c'est un panneau de HQ. Il l'écrit, le teste et l'enregistre ; le panneau apparaît aussitôt sur le tableau de bord du projet, sans toucher au code de HQ ni rien déployer. panneaux_projet te dit ce qui y est déjà.

Ta réponse finale va à Mathias, et elle peut être lue à voix haute :
- Tu le tutoies, simplement, comme un collègue. Commence par la réponse elle-même (« Oui, … », « Non, … », le chiffre, le nom), en trois phrases au plus, puis qui a fait quoi, en quelques mots.
- Ni titres, ni listes, ni markdown. Pas de chemins de fichiers, de numéros de ligne ou de commit, sauf s'il les demande.
- Pour une modification de code : ce qui a été fait, le lien de la branche ou de la demande de fusion donné par l'agent, et ce qu'il doit vérifier.
- Ne dis pas ce que tu n'as pas fait et n'ajoute pas de remarque qu'on ne t'a pas demandée.`, piloteMaxDelegations)
	if projets := s.consigneProjets(ctx); projets != "" {
		b.WriteString("\n\n" + projets)
	}
	return b.String()
}

// contextePiloteAPI : ce qui change à chaque demande — qui travaille en ce
// moment, vos derniers échanges, la date.
func (s *Server) contextePiloteAPI(ctx context.Context, user *store.User, moi equipe.Membre) string {
	var b strings.Builder
	if agents, err := s.hqAgents(ctx, user); err == nil {
		var occupes []string
		for _, a := range agents {
			if a.ID != moi.ID && a.Statut == hqEnCours && a.Tache != nil {
				occupes = append(occupes, fmt.Sprintf("- %s : %s", a.Nom, clipTo(a.Tache.Demande, 160)))
			}
		}
		if len(occupes) > 0 {
			b.WriteString("En ce moment dans l'équipe :\n" + strings.Join(occupes, "\n") + "\n\n")
		}
	}
	if hist := s.history(ctx, user, moi.ID); len(hist) > 0 {
		b.WriteString("Vos derniers échanges (les plus anciens d'abord) :\n")
		for _, t := range hist[max(0, len(hist)-6):] {
			fmt.Fprintf(&b, "- Lui : %s\n  Toi : %s\n", clipTo(t.User, 400), clipTo(t.Assistant, 600))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Nous sommes le %s.", time.Now().In(s.location(user)).Format("02/01/2006 15:04"))
	return b.String()
}
