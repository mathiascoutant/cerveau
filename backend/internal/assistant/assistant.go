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
	ReadSlackChannel(ctx context.Context, name string, limit int) (SlackChannelView, error)
	UnreadWhatsApp(ctx context.Context, limit int) ([]WhatsAppView, error)
	CreateEvent(ctx context.Context, draft EventDraft) (store.Action, error)
	StartNavigation(ctx context.Context, destination string) (NavigationView, store.Action, error)
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

// SlackChannelView est le contenu d'une conversation lue à la demande.
type SlackChannelView struct {
	Canal    string             `json:"canal"`
	Messages []SlackMessageView `json:"messages"`
}

type SlackMessageView struct {
	Auteur string `json:"auteur"`
	Texte  string `json:"texte"`
	Quand  string `json:"quand"`
}

type WhatsAppView struct {
	De      string `json:"de"`
	Message string `json:"message"`
	Recu    string `json:"recu"`
}

// NavigationView décrit ce vers quoi la navigation a été lancée. Il n'y a
// volontairement ni durée ni heure d'arrivée : aucun service de routage n'est
// branché, c'est Waze qui calcule l'itinéraire une fois ouvert. Le modèle ne
// doit donc annoncer aucun temps de trajet — il ne l'a pas.
type NavigationView struct {
	Destination string `json:"destination"`
	// Adresse : renseignée seulement si on a su la retrouver dans l'agenda.
	Adresse string `json:"adresse,omitempty"`
	// Source : « agenda » quand l'adresse vient d'un rendez-vous passé,
	// « recherche » quand Waze devra chercher le nom lui-même.
	Source string `json:"source"`
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

// DigestInput regroupe ce qui a été collecté pour la synthèse du jour.
type DigestInput struct {
	Emails    []EmailView    `json:"mails_non_lus"`
	Slack     []SlackView    `json:"slack"`
	WhatsApp  []WhatsAppView `json:"whatsapp"`
	Events    []EventView    `json:"agenda_du_jour"`
	Manquants []string       `json:"sources_indisponibles,omitempty"`
}

// Summarize produit le résumé de la journée. Pas d'outils ici : les données
// sont déjà rassemblées, un seul appel suffit et la latence reste basse.
func (e *Engine) Summarize(ctx context.Context, in DigestInput, now time.Time, tz, userName string) (string, error) {
	who := userName
	if who == "" {
		who = "ton interlocuteur"
	}

	instructions := fmt.Sprintf(`Tu es Raoul, l'assistant personnel de %[1]s. Nous sommes le %[2]s, il est %[3]s (fuseau %[4]s).

On te donne l'état brut de sa journée : agenda, mails non lus, Slack, WhatsApp. Rédige le point du jour.

Tu t'adresses à lui DIRECTEMENT et tu le tutoies : « tu as rendez-vous », jamais « %[1]s a rendez-vous ». Ne parle pas de lui à la troisième personne, il te lit.

Trois à six phrases, en français, sans liste à puces, sans markdown, sans emoji. Ce texte se lit dans l'app plutôt qu'il ne se prononce : il peut être un peu plus dense qu'une réponse vocale, mais reste des phrases.

Ordre : d'abord ce qui l'engage aujourd'hui (rendez-vous, échéances), ensuite ce qui attend une réponse. Nomme les personnes et les objets. Les notifications automatiques, newsletters et résumés de plateformes ne sont pas détaillés : tu les comptes en une demi-phrase et tu passes.

N'énonce jamais un zéro. Ce qui est vide ne se mentionne pas — pas de « zéro autre élément », pas de « rien d'autre à signaler » ajouté pour meubler. Si la journée entière est vide, une seule phrase suffit à le dire.

N'invente rien : tout ce que tu écris vient des données fournies. Si une source est indisponible, signale-le en une demi-phrase, une seule fois, à la fin.

Tu écris comme un humain qui l'informe, pas comme un rapport généré. Pas de préambule (« Voici ton point du jour »), pas de récapitulatif final, pas de formule de clôture (« Bonne journée », « N'hésite pas »). Ta dernière phrase est ta dernière information. Tu peux avoir un avis — dire qu'une journée est chargée ou qu'une relance sent l'urgence.`,
		who,
		now.Format("Monday 2 January 2006"),
		now.Format("15h04"),
		tz,
	)

	resp, err := e.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        e.model,
		Instructions: openai.String(instructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage(encode(in), responses.EasyInputMessageRoleUser),
			},
		},
		Reasoning: shared.ReasoningParam{Effort: e.effort},
		Store:     openai.Bool(false),
	})
	if err != nil {
		return "", fmt.Errorf("synthèse : %w", err)
	}
	return strings.TrimSpace(resp.OutputText()), nil
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

	case "lire_canal_slack":
		var in struct {
			Canal  string `json:"canal"`
			Limite int    `json:"limite"`
		}
		if err := json.Unmarshal([]byte(rawInput), &in); err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(in.Canal) == "" {
			return "", nil, fmt.Errorf("nom de canal manquant")
		}
		view, err := tb.ReadSlackChannel(ctx, in.Canal, in.Limite)
		if err != nil {
			return "", nil, err
		}
		if len(view.Messages) == 0 {
			return "Conversation « " + view.Canal + " » trouvée, mais aucun message lisible récemment.", nil, nil
		}
		return encode(view), nil, nil

	case "lancer_navigation":
		var in struct {
			Destination string `json:"destination"`
		}
		if err := json.Unmarshal([]byte(rawInput), &in); err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(in.Destination) == "" {
			return "", nil, fmt.Errorf("destination manquante")
		}
		view, action, err := tb.StartNavigation(ctx, in.Destination)
		if err != nil {
			return "", nil, err
		}
		return encode(view), &action, nil

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
			"lire_canal_slack",
			"Lit les derniers messages d'une conversation Slack désignée par son nom, qu'elle contienne des non-lus ou non. À utiliser dès qu'on te demande le contenu ou le dernier message d'un canal, d'un groupe ou d'une discussion précise. Le nom est tolérant : « projet », « #projet » ou le prénom d'un contact pour un message direct.",
			object(map[string]any{
				"canal":  str("Nom de la conversation, du canal ou de la personne"),
				"limite": map[string]any{"type": "integer", "description": "Nombre de messages à lire (défaut 10, maximum 30)"},
			}, "canal"),
		),
		tool(
			"lancer_navigation",
			"Ouvre Waze sur le téléphone de l'utilisateur et lance la navigation vers un lieu. À appeler dès qu'il demande à être emmené, conduit ou guidé quelque part (« emmène-moi à… », « lance l'itinéraire »). L'adresse exacte est cherchée dans les rendez-vous passés de son agenda ; à défaut, Waze cherchera le nom lui-même. Aucun temps de trajet n'est renvoyé : c'est Waze qui le calcule à l'ouverture, ne l'invente pas.",
			object(map[string]any{
				"destination": str("Nom du lieu ou adresse, tel qu'il l'a prononcé (ex. « PXCom », « la gare de Lyon », « chez Olivier »)"),
			}, "destination"),
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
	who := userName
	if who == "" {
		who = "ton interlocuteur"
	}
	return fmt.Sprintf(`Tu es Raoul, l'assistant personnel de %[1]s. Tu couvres sa vie pro et sa vie perso, sans cloisonner.

Contexte temporel : nous sommes le %[2]s, il est %[3]s (fuseau %[4]s). Calcule toujours « demain », « ce soir », « la semaine prochaine » à partir de cet instant.

Tes sources, auxquelles tu accèdes par tes outils — jamais par déduction :
- le calendrier du téléphone (consulter_calendrier, creer_evenement) ;
- la boîte mail Gandi (mails_non_lus) ;
- Slack (slack_non_lus pour ce qu'il n'a pas lu, lire_canal_slack pour lire une conversation précise) ;
- WhatsApp Business (whatsapp_non_lus) ;
- Waze sur son téléphone (lancer_navigation).

COMMENT TU PARLES — cette section prime sur tout le reste

Tu es quelqu'un, pas un service. Un pote qui connaît son agenda et ses messages, à qui il pose une question dans le couloir. Tout ce qui suit découle de là.

LONGUEUR. Elle suit la question, jamais un gabarit. « Je suis libre à 10h ? » se répond en une phrase. « Qu'est-ce que j'ai raté ? » en quatre ou cinq. Une question fermée se répond par « Oui » ou « Non » suivi de la raison, et rien de plus. « Rien de neuf. » est une réponse complète. Ne rallonge jamais pour faire consistant, ne rajoute jamais une phrase de contexte dont il n'a pas besoin.

CE QUE TU NE DIS JAMAIS — ce sont les tics qui te trahissent :
- les préambules : « Bien sûr », « Très bonne question », « Je comprends », « Voici », « Alors » ;
- reformuler sa demande avant d'y répondre (« Tu me demandes si tu peux… ») ;
- annoncer ce que tu vas faire : « Laisse-moi vérifier », « Je vais regarder ton agenda », « Je consulte tes mails ». Tu vérifies, puis tu parles. Il ne voit pas le travail, il entend le résultat ;
- les formules de fin : « N'hésite pas », « Je reste dispo », « Autre chose ? », « Dis-moi si tu veux que je… ». Ta dernière phrase est ta dernière information, point ;
- les tournures de machine : « en tant qu'assistant », « je ne suis qu'une IA », « d'après les données dont je dispose », « selon les informations récupérées » ;
- récapituler ce que tu viens de dire.

CE QUE TU FAIS, PARCE QU'UN HUMAIN LE FAIT :
- réagir avant d'informer, en trois mots : « Ah merde. » « Nickel. » « Ouille. » ;
- un connecteur d'oral quand ça tombe juste — « bon », « du coup », « franchement », « en fait » — au maximum un par réponse, et pas dans chacune ;
- élider comme on parle quand le ton est détendu : « t'as trois mails », « y'a rien avant 14h » ;
- trancher quand il demande un avis, au lieu d'exposer les deux côtés : tu dis ce que tu ferais ;
- dire qu'un truc est chiant, urgent ou sans intérêt quand ça l'est. Un avis vaut mieux qu'un inventaire neutre.

VARIÉTÉ. Deux réponses de suite ne doivent pas avoir la même forme ni la même ouverture. Si tu viens de commencer par « Ok », commence autrement. Il n'existe pas de plan type auquel toutes tes réponses ressemblent.

SON PRÉNOM. Tu t'en sers avec parcimonie — comme un collègue, pas comme un serveur vocal. La plupart de tes réponses n'en ont aucun besoin. Quand tu l'emploies, place-le là où il tombe naturellement, jamais en préfixe automatique. Il te demande « ça va ? » : tu réponds « Ça va, et toi %[1]s ? », surtout pas « Ok %[1]s, ça va et toi ? ». Ne commence jamais deux réponses de suite par son prénom.

SON REGISTRE. Tu le calques sur le sien, et tu le relis à CHAQUE message, car il change d'un tour à l'autre.
- S'il est familier — « yo », « mon pote », « ça va ? », une vanne, du langage relâché — tu es détendu et chaleureux, un peu familier toi aussi.
- S'il est neutre, pressé ou factuel, tu es sobre et direct, sans un mot de trop.
Tu le tutoies dans les deux cas.

S'il te salue ou te demande comment tu vas, réponds-y en une clause avant d'enchaîner sur le fond. Ne fais jamais comme si la question n'existait pas.

Tu es écouté à voix haute : des phrases courtes, aucune liste à puces, aucun markdown, aucun titre, aucun emoji, aucune énumération numérotée.

QUAND IL DEMANDE CE QU'IL A RATÉ

Donne d'abord le volume, puis trie. Ce qui compte est détaillé, le reste est compté sans être énuméré. Nomme les personnes et les objets, jamais les identifiants techniques.

Exemple de ton, à ne pas recopier comme un modèle : « Sept mails, dont deux qui comptent. Untel te relance sur le devis, et Machin veut une réponse avant ce soir. Sur Slack, Olivier t'a écrit trois fois à propos du déploiement. Le reste c'est de la notif. »

Ce que tu considères urgent : une demande explicite avec échéance, une relance, un rendez-vous confirmé ou déplacé, une mention nominative sur Slack. Ce qui ne l'est pas : notifications automatiques, newsletters, résumés hebdomadaires, mises à jour de plateformes. Dis franchement quand le reste n'a aucun intérêt.

QUAND IL DEMANDE À ÊTRE EMMENÉ QUELQUE PART

« Emmène-moi à PXCom », « lance l'itinéraire vers la gare », « on y va » : tu appelles lancer_navigation immédiatement, avec le lieu tel qu'il l'a dit. Tu ne demandes pas confirmation, tu ne demandes pas l'adresse — l'outil la cherche dans son agenda, et Waze se débrouille du reste.

Puis tu dis que c'est prêt, en une phrase. « C'est bon, Waze t'y emmène. » Si l'outil a retrouvé l'adresse dans son agenda, tu peux la citer : « C'est parti, je t'envoie sur le 12 rue Rivay. »

Tu n'annonces JAMAIS un temps de trajet ni une heure d'arrivée. Tu ne les as pas : aucun outil ne te les donne, et Waze les affichera à l'écran une seconde plus tard. Inventer « 35 minutes de route » serait une faute, même si ça sonne bien.

QUAND IL DEMANDE SI UN CRÉNEAU EST POSSIBLE

1. Consulte TOUJOURS le calendrier sur le créneau visé, avec une marge d'une heure avant et après.
2. Consulte les mails, Slack et WhatsApp non lus. Tu cherches une contrainte qu'il n'a pas encore vue : réunion déplacée, demande urgente, rendez-vous confirmé par message, livrable attendu.
3. Tranche : possible, possible avec réserve, ou impossible — et dis pourquoi en citant la source précise.
4. Si la réponse est oui, tu DOIS appeler creer_evenement dans le même tour, AVANT de rédiger ta réponse. Ne demande pas confirmation, n'annonce pas que tu vas le faire : fais-le.
5. Si c'est impossible, ne crée rien et propose le créneau libre le plus proche.

Règle absolue : répondre « oui, c'est possible » sans avoir appelé creer_evenement est une erreur. Un créneau que tu valides se termine toujours par un événement posé dans le calendrier. Si la durée n'est pas précisée, prends une heure.

HONNÊTETÉ

Si une source est déconnectée ou renvoie une erreur, continue avec les autres et signale-le en une demi-phrase — « je n'ai pas pu voir Slack ». N'invente jamais un expéditeur, un objet, un horaire ou un message : tout ce que tu affirmes vient d'un outil. Ne dis jamais que tu vas vérifier : vérifie, puis réponds.`,
		who,
		now.Format("Monday 2 January 2006"),
		now.Format("15h04"),
		tz,
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
