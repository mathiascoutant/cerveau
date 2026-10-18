package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Le moteur en flux.
//
// Une réponse de Raoul se fabrique en plusieurs temps : le modèle choisit ses
// outils, les outils travaillent, le modèle rédige. Attendre la fin pour
// afficher quoi que ce soit fait passer trois secondes de travail pour dix
// secondes de silence. D'où ce fichier : chaque étape émet un événement — un
// outil qui démarre, un fragment de texte, la fin avec ses mesures — et l'app
// affiche au fur et à mesure.
//
// Les mesures ne sont pas décoratives. Sans temps avant le premier mot, sans
// compte de tokens par appel, « Raoul est lent » n'a pas de cause, donc pas de
// remède. Elles descendent dans les journaux et jusqu'à l'app, où l'on peut les
// lire sous chaque réponse.

// Event est ce que le moteur émet pendant qu'il travaille.
type Event struct {
	// Type : « status » (un outil démarre ou finit), « delta » (du texte),
	// « reset » (le texte émis jusqu'ici n'était pas la réponse : le modèle a
	// enchaîné sur des outils), « interlocuteur » (la parole passe à un
	// autre agent), « done ».
	Type string `json:"type"`
	// Pour status : l'outil, son libellé lisible, et sa durée quand il finit.
	Tool  string `json:"tool,omitempty"`
	Label string `json:"label,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
	Err   string `json:"err,omitempty"`
	// Pour delta.
	Text string `json:"text,omitempty"`
	// Pour done.
	Result  *Result  `json:"result,omitempty"`
	Metrics *Metrics `json:"metrics,omitempty"`
	// Pour interlocuteur : qui prend la parole (« raoul » pour revenir à
	// lui). Émis par le serveur entre deux agents, jamais par la boucle.
	Agent string `json:"agent,omitempty"`
}

// Emitter reçoit les événements. Appelé depuis la goroutine du moteur, jamais
// en parallèle.
type Emitter func(Event)

// Metrics : ce que la réponse a coûté.
type Metrics struct {
	// Agent : qui a répondu — vide pour Raoul.
	Agent  string `json:"agent,omitempty"`
	Tier   string `json:"tier"`
	Model  string `json:"model"`
	Reason string `json:"reason,omitempty"`
	// ModelCalls : nombre d'allers-retours avec le modèle (un par tour
	// d'outils, plus le dernier).
	ModelCalls   int   `json:"model_calls"`
	InputTokens  int64 `json:"input_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// FirstTokenMs : délai avant le premier fragment de la réponse finale.
	FirstTokenMs int64 `json:"first_token_ms"`
	// FirstEventMs : délai avant le premier signe de vie (outil ou texte).
	FirstEventMs int64        `json:"first_event_ms"`
	TotalMs      int64        `json:"total_ms"`
	Tools        []ToolTiming `json:"tools,omitempty"`
}

type ToolTiming struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
	Err  bool   `json:"err,omitempty"`
}

// Délai accordé à un outil. Au-delà, on rend la main au modèle avec l'erreur :
// un IMAP qui ne répond pas ne doit pas geler toute la conversation.
const toolTimeout = 40 * time.Second

// Budget d'historique. Quatorze tours restent la fenêtre, mais bornée en
// caractères : les réponses longues d'hier ne doivent pas coûter à chaque
// question d'aujourd'hui.
const (
	historyMaxTurns  = 14
	historyMaxRunes  = 9000
	historyReplyCap  = 700
	historyPromptCap = 1200
)

// toolLabels : ce que l'app affiche pendant qu'un outil travaille. Des mots
// d'utilisateur, pas des noms de fonctions.
var toolLabels = map[string]string{
	"consulter_calendrier":       "Agenda",
	"creer_evenement":            "Agenda",
	"mails_non_lus":              "Mails non lus",
	"lire_mail":                  "Lecture du mail",
	"mails_envoyes":              "Mails envoyés",
	"chercher_mails":             "Recherche dans les mails",
	"preparer_reponse_mail":      "Rédaction de la réponse",
	"chercher_brouillon":         "Réponses préparées",
	"modifier_brouillon":         "Réponses préparées",
	"slack_non_lus":              "Slack non lu",
	"lire_canal_slack":           "Lecture Slack",
	"chercher_slack":             "Recherche Slack",
	"whatsapp_non_lus":           "WhatsApp non lu",
	"lire_conversation_whatsapp": "Lecture WhatsApp",
	"tickets_csp":                "Tickets CSP",
	"debriefer":                  "Analyse approfondie",
	"point_urgences":             "Point des urgences",
	"ouvrir_urgence":             "Ouverture de l'urgence",
	"urgence_traitee":            "Urgence traitée",
	"chercher_historique":        "Mémoire",
	"retenir":                    "Mémoire",
	"oublier":                    "Mémoire",
	"ajouter_tache":              "Liste à faire",
	"mes_taches":                 "Liste à faire",
	"terminer_tache":             "Liste à faire",
	"reprogrammer_tache":         "Liste à faire",
	"supprimer_tache":            "Liste à faire",
	"lancer_navigation":          "Navigation",
	"etat_projets":               "Projets perso",
	"passer_la_parole":           "Passage de parole",
}

func labelOf(tool string) string {
	if l, ok := toolLabels[tool]; ok {
		return l
	}
	return tool
}

// Spec décrit un agent le temps d'un tour : qui il est (sa consigne), ce qu'il
// sait faire (ses outils et la façon de les exécuter), et sur quel modèle.
//
// Raoul et les membres de l'équipe tournent dans la même boucle — modèle,
// outils, modèle — et ne diffèrent que par là. Une seule boucle, c'est un seul
// endroit où se règlent le flux, les mesures, les délais et l'interruption :
// deux boucles finiraient par diverger sur exactement ces détails-là.
type Spec struct {
	// Agent : qui répond. Vide pour Raoul, qui est l'interlocuteur par défaut.
	Agent        string
	Route        Route
	Instructions string
	Tools        []responses.ToolUnionParam
	Exec         ToolExec
	// Label : le libellé affiché pendant qu'un outil travaille. Défaut : ceux
	// de Raoul.
	Label func(tool string) string
	// MaxIterations : tours d'outils avant d'abandonner. Défaut : maxIterations.
	MaxIterations int
	// ToolTimeout : délai propre à un outil. Défaut : toolTimeout pour tous.
	ToolTimeout func(tool string) time.Duration
	CacheKey    string
}

// ToolExec exécute un appel d'outil. Une erreur portant instruction() n'est
// pas une panne : c'est une question à poser (voir AmbiguousError).
type ToolExec func(ctx context.Context, name, args string) (ToolOutput, error)

// ToolOutput est ce qu'un outil rend à la boucle.
type ToolOutput struct {
	Payload string
	Action  *store.Action
	// Handoff : l'outil passe la parole à cet interlocuteur. Le tour s'arrête
	// là, sans réponse de l'agent courant : c'est le suivant qui parlera.
	Handoff string
}

// AskStream répond à une demande en émettant ses étapes au fur et à mesure.
func (e *Engine) AskStream(ctx context.Context, tb Toolbox, req Request, emit Emitter) (Result, error) {
	loc := time.UTC
	if req.Timezone != "" {
		if l, err := time.LoadLocation(req.Timezone); err == nil {
			loc = l
		}
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(loc)

	history := trimHistory(req.History)
	var previous *Turn
	if len(history) > 0 {
		previous = &history[len(history)-1]
	}
	route := e.models().Decide(req.Text, previous)

	spec := Spec{
		Route:    route,
		CacheKey: req.CacheKey,
		Exec: func(ctx context.Context, name, args string) (ToolOutput, error) {
			return e.raoulTool(ctx, tb, req, now, loc, name, args)
		},
	}
	if route.Tier == TierFast {
		spec.Instructions = fastPrompt(now, loc.String(), req.UserName)
	} else {
		spec.Instructions = systemPrompt(now, loc.String(), req.UserName, req.UserEmail, req.Sources, req.Facts, req.Echanges)
		spec.Tools = toolDefinitions(req.Sources)
	}
	return e.Run(ctx, spec, history, req.Text, emit)
}

// raoulTool aiguille un appel d'outil de Raoul. Le débrief et le passage de
// parole ont leur propre chemin ; tout le reste passe par runTool, avec la
// boîte mail de l'espace demandé.
func (e *Engine) raoulTool(ctx context.Context, tb Toolbox, req Request, now time.Time, loc *time.Location, name, args string) (ToolOutput, error) {
	switch name {
	case "debriefer":
		payload, err := e.debrief(ctx, mailToolbox(tb, req.Sources, name, args), req, now, loc, args)
		return ToolOutput{Payload: payload}, err
	case "passer_la_parole":
		return handoff(req.Sources.Equipe, args)
	case "etat_projets":
		if req.Perso == nil {
			return ToolOutput{}, errors.New("aucun projet perso n'est suivi sur ce serveur")
		}
		brief, err := req.Perso.ProjectsBrief(ctx)
		return ToolOutput{Payload: brief}, err
	}
	payload, action, err := e.runTool(ctx, mailToolbox(tb, req.Sources, name, args), loc, name, args)
	return ToolOutput{Payload: payload, Action: action}, err
}

// Run fait tourner un agent sur une demande, en émettant ses étapes. C'est la
// boucle de Raoul comme celle de l'équipe : AskStream lui prépare la consigne
// et les outils de Raoul, l'équipe les siens.
func (e *Engine) Run(ctx context.Context, spec Spec, history []Turn, text string, emit Emitter) (Result, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	started := time.Now()
	history = trimHistory(history)
	metrics := &Metrics{
		Agent: spec.Agent, Tier: string(spec.Route.Tier), Model: string(spec.Route.Model), Reason: spec.Route.Reason,
	}

	items := make([]responses.ResponseInputItemUnionParam, 0, len(history)*2+2)
	for _, t := range history {
		if t.User == "" {
			continue
		}
		items = append(items, responses.ResponseInputItemParamOfMessage(t.User, responses.EasyInputMessageRoleUser))
		if t.Assistant != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(t.Assistant, responses.EasyInputMessageRoleAssistant))
		}
	}
	items = append(items, responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleUser))

	params := responses.ResponseNewParams{
		Model:        spec.Route.Model,
		Instructions: openai.String(spec.Instructions),
		// Rien ne doit être conservé côté OpenAI : les objets de mails, les
		// extraits Slack et l'agenda ne sortent que le temps de la requête.
		Store: openai.Bool(false),
	}
	if spec.Route.Effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: spec.Route.Effort}
	}
	if spec.CacheKey != "" {
		// Une même clé par utilisateur : les requêtes d'un même compte
		// partagent le même préfixe (consigne + outils), et l'API le garde en
		// cache si elles arrivent sur la même machine.
		params.PromptCacheKey = openai.String(spec.CacheKey)
	}
	if len(spec.Tools) > 0 {
		params.Tools = spec.Tools
	}
	limit := spec.MaxIterations
	if limit <= 0 {
		limit = maxIterations
	}

	result := Result{Agent: spec.Agent}
	var (
		firstAny  time.Time
		firstText time.Time
	)
	markEvent := func() {
		if firstAny.IsZero() {
			firstAny = time.Now()
		}
	}

	for iteration := 0; iteration < limit; iteration++ {
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}
		metrics.ModelCalls++

		stream := e.client.Responses.NewStreaming(ctx, params)
		var (
			final   *responses.Response
			text    strings.Builder
			emitted bool
		)
		for stream.Next() {
			ev := stream.Current()
			switch ev.Type {
			case "response.output_text.delta":
				if ev.Delta == "" {
					continue
				}
				markEvent()
				if firstText.IsZero() {
					firstText = time.Now()
				}
				text.WriteString(ev.Delta)
				emitted = true
				emit(Event{Type: "delta", Text: ev.Delta})
			case "response.completed":
				resp := ev.Response
				final = &resp
			case "response.failed", "error":
				msg := ev.Code
				if msg == "" {
					msg = "réponse en échec"
				}
				_ = stream.Close()
				return result, fmt.Errorf("appel du modèle : %s", msg)
			}
		}
		if err := stream.Err(); err != nil {
			return result, fmt.Errorf("appel du modèle : %w", err)
		}
		if final == nil {
			return result, errors.New("appel du modèle : flux terminé sans réponse")
		}
		metrics.InputTokens += final.Usage.InputTokens
		metrics.CachedTokens += final.Usage.InputTokensDetails.CachedTokens
		metrics.OutputTokens += final.Usage.OutputTokens

		reply := strings.TrimSpace(text.String())
		if reply == "" {
			reply = strings.TrimSpace(final.OutputText())
		}
		if reply != "" {
			result.Reply = reply
		}

		calls := make([]responses.ResponseFunctionToolCall, 0, 4)
		for _, item := range final.Output {
			if call, ok := item.AsAny().(responses.ResponseFunctionToolCall); ok {
				calls = append(calls, call)
			}
		}
		if len(calls) == 0 {
			break
		}
		// Le modèle a parlé puis appelé des outils : ce qu'il a dit n'était
		// pas la réponse. L'app efface et repart de la suite.
		if emitted {
			emit(Event{Type: "reset"})
			firstText = time.Time{}
		}

		outputs := e.runCalls(ctx, spec, calls, emit, markEvent, metrics)
		for i, call := range calls {
			if outputs[i].action != nil {
				result.Actions = append(result.Actions, *outputs[i].action)
			}
			if outputs[i].handoff != "" {
				result.Handoff = outputs[i].handoff
			}
			result.Steps = append(result.Steps, call.Name)
			// L'appel doit être rejoué dans l'entrée avant son résultat, sinon
			// le modèle ne sait pas à quoi le rattacher.
			items = append(items,
				responses.ResponseInputItemParamOfFunctionCall(call.Arguments, call.CallID, call.Name),
				responses.ResponseInputItemParamOfFunctionCallOutput(call.CallID, outputs[i].payload),
			)
		}
		// La parole passe : l'agent courant ne conclut pas, c'est le suivant
		// qui répondra à la même demande. Ce qu'il aurait dit par-dessus
		// (« je te le passe ») ferait parler deux voix pour une seule réponse.
		if result.Handoff != "" {
			result.Reply = ""
			break
		}
	}

	if result.Actions == nil {
		result.Actions = []store.Action{}
	}
	metrics.TotalMs = time.Since(started).Milliseconds()
	if !firstAny.IsZero() {
		metrics.FirstEventMs = firstAny.Sub(started).Milliseconds()
	}
	if !firstText.IsZero() {
		metrics.FirstTokenMs = firstText.Sub(started).Milliseconds()
	} else {
		metrics.FirstTokenMs = metrics.TotalMs
	}
	agent := spec.Agent
	if agent == "" {
		agent = "raoul"
	}
	slog.Info("raoul",
		"agent", agent, "tier", metrics.Tier, "model", metrics.Model, "reason", metrics.Reason,
		"calls", metrics.ModelCalls, "in", metrics.InputTokens, "cached", metrics.CachedTokens,
		"out", metrics.OutputTokens, "ttft_ms", metrics.FirstTokenMs, "total_ms", metrics.TotalMs,
		"tools", len(metrics.Tools), "handoff", result.Handoff)
	result.Metrics = metrics
	if result.Handoff != "" {
		// Pas de « done » : la demande n'est pas finie, elle change de main.
		// C'est l'appelant qui annonce le nouvel interlocuteur et le fait
		// répondre — son « done » à lui fermera le tour.
		return result, nil
	}
	if result.Reply == "" {
		result.Reply = "Je n'ai pas réussi à conclure, désolé. Tu peux reformuler ?"
	}
	emit(Event{Type: "done", Result: &result, Metrics: metrics})
	return result, nil
}

type callOutput struct {
	payload string
	action  *store.Action
	handoff string
}

// runCalls exécute les appels d'un même tour. Plusieurs appels sont
// indépendants par construction — le modèle les a émis ensemble — donc ils
// courent en parallèle : chercher un mail et lire un canal ne s'attendent pas.
func (e *Engine) runCalls(ctx context.Context, spec Spec, calls []responses.ResponseFunctionToolCall,
	emit Emitter, markEvent func(), metrics *Metrics) []callOutput {

	outputs := make([]callOutput, len(calls))
	timings := make([]ToolTiming, len(calls))
	label := spec.Label
	if label == nil {
		label = labelOf
	}

	var emitMu sync.Mutex
	safeEmit := func(ev Event) {
		emitMu.Lock()
		defer emitMu.Unlock()
		markEvent()
		emit(ev)
	}

	run := func(i int, call responses.ResponseFunctionToolCall) {
		started := time.Now()
		safeEmit(Event{Type: "status", Tool: call.Name, Label: label(call.Name)})

		timeout := toolTimeout
		if spec.ToolTimeout != nil {
			if d := spec.ToolTimeout(call.Name); d > 0 {
				timeout = d
			}
		}
		tctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		out, err := spec.Exec(tctx, call.Name, call.Arguments)
		payload := out.Payload
		failed := false
		if err != nil {
			// Ce n'est pas toujours une panne : certains outils font leur
			// travail et rendent la main pour qu'on lève un doute. Ils
			// portent alors la question à poser, pas un message d'erreur.
			var ask interface{ instruction() string }
			if errors.As(err, &ask) {
				payload = ask.instruction()
			} else {
				failed = true
				slog.Warn("outil en échec", "agent", spec.Agent, "outil", call.Name, "err", err)
				if errors.Is(err, context.DeadlineExceeded) {
					payload = "Erreur : la source n'a pas répondu à temps. Dis-le en une clause, n'invente pas ce qu'elle aurait rendu."
				} else {
					payload = "Erreur : " + err.Error()
				}
			}
		}
		ms := time.Since(started).Milliseconds()
		if ms < 1 {
			// La fin d'un outil se reconnaît à sa durée : un outil instantané
			// (un passage de parole) passerait sinon pour un outil qui démarre.
			ms = 1
		}
		outputs[i] = callOutput{payload: payload, action: out.Action, handoff: out.Handoff}
		timings[i] = ToolTiming{Name: call.Name, Ms: ms, Err: failed}
		ev := Event{Type: "status", Tool: call.Name, Label: label(call.Name), Ms: ms}
		if failed {
			ev.Err = err.Error()
		}
		safeEmit(ev)
	}

	if len(calls) == 1 {
		run(0, calls[0])
	} else {
		var wg sync.WaitGroup
		for i, call := range calls {
			wg.Add(1)
			go func(i int, call responses.ResponseFunctionToolCall) {
				defer wg.Done()
				run(i, call)
			}(i, call)
		}
		wg.Wait()
	}
	metrics.Tools = append(metrics.Tools, timings...)
	return outputs
}

// trimHistory garde les derniers tours dans un budget de caractères. Les
// réponses longues sont coupées : ce qui compte pour la suite tient dans leurs
// premières lignes, et un mail relu hier n'a pas à voyager entier aujourd'hui.
func trimHistory(history []Turn) []Turn {
	if len(history) > historyMaxTurns {
		history = history[len(history)-historyMaxTurns:]
	}
	out := make([]Turn, 0, len(history))
	budget := historyMaxRunes
	// Du plus récent au plus ancien : ce sont les derniers tours qui doivent
	// survivre au budget.
	for i := len(history) - 1; i >= 0; i-- {
		t := Turn{
			User:      truncateRunes(history[i].User, historyPromptCap),
			Assistant: truncateRunes(history[i].Assistant, historyReplyCap),
		}
		cost := len([]rune(t.User)) + len([]rune(t.Assistant))
		if cost > budget {
			break
		}
		budget -= cost
		out = append(out, t)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// fastPrompt : la consigne de l'étage rapide. Le ton, l'identité, et rien de
// ce qui parle des sources — sans outils, la moindre allusion inviterait le
// modèle à raconter ce qu'il ne peut pas voir.
func fastPrompt(now time.Time, tz, userName string) string {
	who := userName
	if who == "" {
		who = "ton interlocuteur"
	}
	return fmt.Sprintf(`Tu es Raoul, l'assistant personnel de %[1]s. Tu le tutoies, tu réponds court et direct, en français, sans préambule, sans formule de fin, sans markdown ni emoji. Tu es écouté à voix haute : des phrases courtes.

Cette demande ne porte sur aucune de ses données : réponds avec ce que tu sais, ou traite le texte qu'il te donne. Si, contre toute attente, elle demandait de consulter ses mails, ses messages, ses tickets ou son agenda, dis en une phrase que tu ne les as pas sous les yeux pour cette question et invite-le à la reposer en nommant la source — n'invente jamais un contenu.

Nous sommes le %[2]s, il est %[3]s (fuseau %[4]s).`,
		who, now.Format("Monday 2 January 2006"), now.Format("15h04"), tz)
}
