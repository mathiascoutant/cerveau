// Package assistant orchestre Raoul : il donne au modèle un accès outillé au
// calendrier, aux mails Gandi, à Slack et à WhatsApp, puis le laisse décider
// quoi consulter avant de répondre — et, le cas échéant, de poser l'événement
// dans le calendrier.
package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

const maxIterations = 8

// Toolbox expose à l'assistant les données de l'utilisateur courant.
// L'implémentation vit dans le package api (elle a le contexte utilisateur).
type Toolbox interface {
	CalendarEvents(ctx context.Context, start, end time.Time) ([]EventView, error)
	UnreadEmails(ctx context.Context, limit int) ([]EmailView, error)
	UnreadSlack(ctx context.Context, limit int) ([]SlackView, error)
	UnreadWhatsApp(ctx context.Context, limit int) ([]WhatsAppView, error)
	CreateEvent(ctx context.Context, draft EventDraft) (store.Action, error)
}

type EventView struct {
	Titre   string `json:"titre"`
	Debut   string `json:"debut"`
	Fin     string `json:"fin"`
	Lieu    string `json:"lieu,omitempty"`
	Journee bool   `json:"journee_entiere,omitempty"`
}

type EmailView struct {
	De    string `json:"de"`
	Objet string `json:"objet"`
	Recu  string `json:"recu"`
}

// SlackView distingue deux réalités que l'API Slack ne traite pas pareil :
// les DM ont un vrai compteur de non-lus, les canaux non — pour eux on ne
// dispose que de l'activité récente.
type SlackView struct {
	Canal string `json:"canal"`
	Type  string `json:"type"` // "dm" ou "canal"
	// NonLus : uniquement pour les DM.
	NonLus int `json:"non_lus,omitempty"`
	// MessagesRecents : repli quand l'état de lecture est indisponible.
	MessagesRecents int `json:"messages_recents,omitempty"`
	// Mentions : nombre de fois où l'utilisateur est cité nommément.
	Mentions int      `json:"mentions,omitempty"`
	Extraits []string `json:"extraits,omitempty"`
}

type WhatsAppView struct {
	De      string `json:"de"`
	Message string `json:"message"`
	Recu    string `json:"recu"`
}

type EventDraft struct {
	Titre string
	Debut time.Time
	Fin   time.Time
	Lieu  string
	Note  string
}

// Request est une demande adressée à Raoul.
type Request struct {
	Text     string
	Now      time.Time
	Timezone string
	UserName string
	// History : tours précédents (les plus récents en dernier), pour le contexte.
	History []Turn
}

type Turn struct {
	User      string
	Assistant string
}

// Result est ce que le backend renvoie à l'app.
type Result struct {
	Reply   string         `json:"reply"`
	Actions []store.Action `json:"actions,omitempty"`
	Steps   []string       `json:"steps,omitempty"`
}

type Engine struct {
	client openai.Client
	model  shared.ResponsesModel
	effort shared.ReasoningEffort
}

// New construit le moteur. `effort` pilote le budget de raisonnement :
// « low » est le bon réglage pour du vocal, où la latence compte autant que
// la finesse de l'analyse.
func New(apiKey, model, effort string) *Engine {
	if model == "" {
		model = string(shared.ChatModelGPT5_4Mini)
	}
	if effort == "" {
		effort = string(shared.ReasoningEffortLow)
	}
	return &Engine{
		client: openai.NewClient(option.WithAPIKey(apiKey)),
		model:  shared.ResponsesModel(model),
		effort: shared.ReasoningEffort(effort),
	}
}

func (e *Engine) Ask(ctx context.Context, tb Toolbox, req Request) (Result, error) {
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

	var result Result

	// API Responses et non Chat Completions : sur les modèles à raisonnement,
	// OpenAI refuse la combinaison outils + reasoning_effort sur /v1/chat/completions.
	items := make([]responses.ResponseInputItemUnionParam, 0, len(req.History)*2+2)
	for _, t := range req.History {
		if t.User == "" {
			continue
		}
		items = append(items, responses.ResponseInputItemParamOfMessage(t.User, responses.EasyInputMessageRoleUser))
		if t.Assistant != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(t.Assistant, responses.EasyInputMessageRoleAssistant))
		}
	}
	items = append(items, responses.ResponseInputItemParamOfMessage(req.Text, responses.EasyInputMessageRoleUser))

	params := responses.ResponseNewParams{
		Model:        e.model,
		Instructions: openai.String(systemPrompt(now, loc.String(), req.UserName)),
		Tools:        toolDefinitions(),
		Reasoning:    shared.ReasoningParam{Effort: e.effort},
		// Rien ne doit être conservé côté OpenAI : les objets de mails, les
		// extraits Slack et l'agenda ne sortent que le temps de la requête.
		Store: openai.Bool(false),
	}

	for range maxIterations {
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}

		resp, err := e.client.Responses.New(ctx, params)
		if err != nil {
			return result, fmt.Errorf("appel du modèle : %w", err)
		}

		if txt := strings.TrimSpace(resp.OutputText()); txt != "" {
			result.Reply = txt
		}

		calls := make([]responses.ResponseFunctionToolCall, 0, 4)
		for _, item := range resp.Output {
			if call, ok := item.AsAny().(responses.ResponseFunctionToolCall); ok {
				calls = append(calls, call)
			}
		}
		if len(calls) == 0 {
			return result, nil
		}

		for _, call := range calls {
			payload, action, err := e.runTool(ctx, tb, loc, call.Name, call.Arguments)
			if action != nil {
				result.Actions = append(result.Actions, *action)
			}
			result.Steps = append(result.Steps, call.Name)
			if err != nil {
				slog.Warn("outil en échec", "outil", call.Name, "err", err)
				payload = "Erreur : " + err.Error()
			}
			// L'appel doit être rejoué dans l'entrée avant son résultat, sinon
			// le modèle ne sait pas à quoi le rattacher.
			items = append(items,
				responses.ResponseInputItemParamOfFunctionCall(call.Arguments, call.CallID, call.Name),
				responses.ResponseInputItemParamOfFunctionCallOutput(call.CallID, payload),
			)
		}
	}

	if result.Reply == "" {
		result.Reply = "Je n'ai pas réussi à conclure, désolé. Tu peux reformuler ?"
	}
	return result, nil
}

func (e *Engine) runTool(ctx context.Context, tb Toolbox, loc *time.Location, name, rawInput string) (string, *store.Action, error) {
	switch name {
	case "consulter_calendrier":
		var in struct {
			Debut string `json:"debut"`
			Fin   string `json:"fin"`
		}
		if err := json.Unmarshal([]byte(rawInput), &in); err != nil {
			return "", nil, err
		}
		start, err := parseTime(in.Debut, loc)
		if err != nil {
			return "", nil, fmt.Errorf("date de début invalide : %w", err)
		}
		end, err := parseTime(in.Fin, loc)
		if err != nil {
			return "", nil, fmt.Errorf("date de fin invalide : %w", err)
		}
		events, err := tb.CalendarEvents(ctx, start, end)
		if err != nil {
			return "", nil, err
		}
		if len(events) == 0 {
			return "Aucun événement sur ce créneau : le calendrier est libre.", nil, nil
		}
		return encode(events), nil, nil

	case "mails_non_lus":
		limit := limitOf(rawInput, 15)
		mails, err := tb.UnreadEmails(ctx, limit)
		if err != nil {
			return "", nil, err
		}
		if len(mails) == 0 {
			return "Aucun mail non lu.", nil, nil
		}
		return encode(mails), nil, nil

	case "slack_non_lus":
		limit := limitOf(rawInput, 10)
		threads, err := tb.UnreadSlack(ctx, limit)
		if err != nil {
			return "", nil, err
		}
		if len(threads) == 0 {
			return "Aucun message Slack non lu.", nil, nil
		}
		return encode(threads), nil, nil

	case "whatsapp_non_lus":
		limit := limitOf(rawInput, 15)
		msgs, err := tb.UnreadWhatsApp(ctx, limit)
		if err != nil {
			return "", nil, err
		}
		if len(msgs) == 0 {
			return "Aucun message WhatsApp non lu.", nil, nil
		}
		return encode(msgs), nil, nil

	case "creer_evenement":
		var in struct {
			Titre string `json:"titre"`
			Debut string `json:"debut"`
			Fin   string `json:"fin"`
			Lieu  string `json:"lieu"`
			Note  string `json:"note"`
		}
		if err := json.Unmarshal([]byte(rawInput), &in); err != nil {
			return "", nil, err
		}
		start, err := parseTime(in.Debut, loc)
		if err != nil {
			return "", nil, fmt.Errorf("date de début invalide : %w", err)
		}
		end, err := parseTime(in.Fin, loc)
		if err != nil {
			end = start.Add(time.Hour)
		}
		action, err := tb.CreateEvent(ctx, EventDraft{
			Titre: in.Titre, Debut: start, Fin: end, Lieu: in.Lieu, Note: in.Note,
		})
		if err != nil {
			return "", nil, err
		}
		msg := fmt.Sprintf("Événement « %s » ajouté au calendrier le %s de %s à %s.",
			in.Titre,
			start.Format("02/01/2006"),
			start.Format("15h04"),
			end.Format("15h04"))
		return msg, &action, nil
	}
	return "", nil, fmt.Errorf("outil inconnu : %s", name)
}

func toolDefinitions() []responses.ToolUnionParam {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	object := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	tool := func(name, description string, parameters map[string]any) responses.ToolUnionParam {
		t := responses.ToolParamOfFunction(name, parameters, false)
		t.OfFunction.Description = openai.String(description)
		return t
	}

	return []responses.ToolUnionParam{
		tool(
			"consulter_calendrier",
			"Liste les événements déjà présents dans le calendrier de l'utilisateur sur une période. À utiliser systématiquement avant de proposer ou de créer un créneau.",
			object(map[string]any{
				"debut": str("Début de la période, ISO 8601 (ex. 2026-08-21T08:00:00+02:00)"),
				"fin":   str("Fin de la période, ISO 8601"),
			}, "debut", "fin"),
		),
		tool(
			"mails_non_lus",
			"Récupère les mails non lus de la boîte Gandi (expéditeur, objet, date). Sert à repérer une urgence ou une contrainte non encore vue.",
			object(map[string]any{
				"limite": map[string]any{"type": "integer", "description": "Nombre maximum de mails (défaut 15)"},
			}),
		),
		tool(
			"slack_non_lus",
			"État de Slack : conversations avec des messages non lus, DM comme canaux, avec un extrait. Le champ mentions indique que l'utilisateur y est cité nommément, ce qui est plus urgent qu'un simple non-lu. Si une entrée porte messages_recents au lieu de non_lus, c'est que l'état de lecture était indisponible pour cette conversation : parle alors d'activité récente, pas de non-lus.",
			object(map[string]any{
				"limite": map[string]any{"type": "integer", "description": "Nombre maximum de conversations (défaut 10)"},
			}),
		),
		tool(
			"whatsapp_non_lus",
			"Récupère les messages WhatsApp Business non lus reçus par l'utilisateur.",
			object(map[string]any{
				"limite": map[string]any{"type": "integer", "description": "Nombre maximum de messages (défaut 15)"},
			}),
		),
		tool(
			"creer_evenement",
			"Crée un événement dans le calendrier de l'utilisateur. À n'appeler qu'après avoir vérifié que le créneau est libre et qu'aucun message non lu ne s'y oppose.",
			object(map[string]any{
				"titre": str("Titre de l'événement (ex. « Sport »)"),
				"debut": str("Début, ISO 8601"),
				"fin":   str("Fin, ISO 8601"),
				"lieu":  str("Lieu, facultatif"),
				"note":  str("Note libre, facultative"),
			}, "titre", "debut", "fin"),
		),
	}
}

func systemPrompt(now time.Time, tz, userName string) string {
	who := "l'utilisateur"
	if userName != "" {
		who = userName
	}
	return fmt.Sprintf(`Tu es Raoul, l'assistant vocal personnel de %s. Tu couvres à la fois sa vie pro et sa vie perso, sans cloisonner.

Contexte temporel : nous sommes le %s, il est %s (fuseau %s). Calcule toujours « demain », « ce soir », « la semaine prochaine » à partir de cet instant.

Tes sources :
- le calendrier du téléphone (outil consulter_calendrier) ;
- la boîte mail Gandi (outil mails_non_lus) ;
- Slack (outil slack_non_lus) ;
- WhatsApp Business (outil whatsapp_non_lus).

Méthode quand on te demande si un créneau est possible :
1. Consulte TOUJOURS le calendrier sur le créneau visé, en prenant une marge d'une heure avant et après.
2. Consulte les mails, Slack et WhatsApp non lus. Tu cherches une contrainte que %s n'a pas encore vue : réunion déplacée, demande urgente, rendez-vous confirmé par message, livrable attendu.
3. Tranche : possible, possible avec réserve, ou impossible — et dis pourquoi en citant la source précise (« un mail de X ce matin », « 3 messages Slack non lus dans #projet »).
4. Si la réponse est oui, tu DOIS appeler creer_evenement dans le même tour, AVANT de rédiger ta réponse. Ne demande pas confirmation, n'annonce pas que tu vas le faire : fais-le.
5. Si c'est impossible, ne crée rien et propose le créneau libre le plus proche.

Règle absolue : répondre « oui, c'est possible » sans avoir appelé creer_evenement est une erreur. Un créneau que tu valides se termine toujours par un événement posé dans le calendrier. Si la durée n'est pas précisée, prends une heure.

Style : tu es écouté à voix haute. Réponds en français, à l'oral, en 2 à 4 phrases maximum. Pas de liste à puces, pas de markdown, pas d'emoji. Va droit au but, ton direct et cordial, tutoie %s.

Si une source est déconnectée ou renvoie une erreur, continue avec les autres et signale-le en une demi-phrase (« je n'ai pas pu voir Slack »). Ne réponds jamais que tu vas vérifier : vérifie, puis réponds.`,
		who,
		now.Format("Monday 2 January 2006"),
		now.Format("15h04"),
		tz,
		who,
		who,
	)
}

func parseTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("format de date non reconnu : %q", s)
}

func limitOf(rawInput string, def int) int {
	var in struct {
		Limite int `json:"limite"`
	}
	if err := json.Unmarshal([]byte(rawInput), &in); err == nil && in.Limite > 0 {
		return in.Limite
	}
	return def
}

func encode(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
}
