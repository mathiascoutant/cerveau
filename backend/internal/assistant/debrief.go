package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Le débrief est le seul endroit où Raoul doit vraiment COMPRENDRE plutôt que
// relayer.
//
// Le modèle de la boucle vocale est choisi pour sa vitesse : il enchaîne les
// outils et répond en une seconde, et c'est ce qu'on lui demande neuf fois sur
// dix. Mais « fais-moi un débrief du groupe Azul » n'est pas une question de
// vitesse. C'est quatre-vingts messages où les réponses arrivent vingt lignes
// après les questions, où « ok pour moi » engage quelqu'un sur un sujet lancé
// la veille, où la moitié du sens tient à qui parle — et c'est là que le petit
// modèle, avec un raisonnement au minimum, attribuait des décisions au mauvais
// sujet et ratait ce qu'on attendait de lui.
//
// D'où un outil à part : il lit large, puis confie la lecture à un modèle plus
// fort avec un vrai budget de raisonnement et une consigne écrite pour ce seul
// travail. La boucle vocale ne fait plus que restituer. On paie une seconde ou
// deux de plus, sur la seule question où elles s'entendent moins qu'une erreur.

const (
	// DefaultDeepModel : le modèle du débrief. Un cran au-dessus de celui de la
	// boucle vocale, qui reste réglé pour la latence.
	DefaultDeepModel = string(shared.ChatModelGPT5_4)
	// DefaultDeepEffort : « medium » laisse au modèle le temps de recoller les
	// réponses à leurs questions sans le faire réfléchir une demi-minute.
	DefaultDeepEffort = string(shared.ReasoningEffortMedium)

	// debriefDepth : messages lus pour un débrief de conversation. Le maximum
	// des outils de lecture — un débrief qui commence au milieu d'un sujet en
	// invente le début.
	debriefDepth = 100
)

// WithDeep règle le modèle et l'effort du débrief. Vides, les valeurs par
// défaut s'appliquent.
func (e *Engine) WithDeep(model, effort string) *Engine {
	if model == "" {
		model = DefaultDeepModel
	}
	if effort == "" {
		effort = DefaultDeepEffort
	}
	e.deepModel = shared.ResponsesModel(model)
	e.deepEffort = shared.ReasoningEffort(effort)
	return e
}

type debriefInput struct {
	Source   string `json:"source"`
	Cible    string `json:"cible"`
	Question string `json:"question"`
}

// debrief lit la source demandée puis la fait analyser. Les erreurs des outils
// de lecture remontent telles quelles : une conversation à confirmer ou un
// expéditeur ambigu se traitent exactement comme pour une lecture simple.
func (e *Engine) debrief(ctx context.Context, tb Toolbox, req Request, now time.Time, loc *time.Location, raw string) (string, error) {
	var in debriefInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return "", err
	}

	var data, label string
	switch strings.ToLower(strings.TrimSpace(in.Source)) {
	case "mail":
		view, err := tb.ReadEmail(ctx, in.Cible, false)
		if err != nil {
			return "", err
		}
		data, label = encode(view), "le mail « "+view.Objet+" » de "+view.De
	case "slack":
		if strings.TrimSpace(in.Cible) == "" {
			return "", fmt.Errorf("nom de conversation Slack manquant")
		}
		view, err := tb.ReadSlackChannel(ctx, in.Cible, debriefDepth)
		if err != nil {
			return "", err
		}
		if len(view.Messages) == 0 {
			return "Conversation « " + view.Canal + " » trouvée, mais aucun message lisible récemment.", nil
		}
		data, label = encode(view), "la conversation Slack « "+view.Canal+" »"
	case "whatsapp":
		if strings.TrimSpace(in.Cible) == "" {
			return "", fmt.Errorf("nom de conversation WhatsApp manquant")
		}
		view, err := tb.ReadWhatsAppChat(ctx, in.Cible, debriefDepth, false)
		if err != nil {
			return "", err
		}
		if len(view.Messages) == 0 {
			return "Conversation « " + view.Conversation + " » trouvée, mais aucun message archivé.", nil
		}
		data, label = encode(view), "la conversation WhatsApp « "+view.Conversation+" »"
	default:
		return "", fmt.Errorf("source inconnue %q : mail, slack ou whatsapp", in.Source)
	}

	analysis, err := e.analyze(ctx, debriefPrompt(now, loc.String(), req.UserName, req.Facts, label, in.Question), data)
	if err != nil {
		return "", err
	}
	return "ANALYSE DE " + strings.ToUpper(label) + " — faite pour toi par une lecture approfondie. " +
		"Restitue L'ESSENTIEL à l'oral, avec tes mots et dans ton ton ; garde LES DÉTAILS pour ses questions suivantes. " +
		"N'ajoute rien qui n'y figure pas.\n\n" + analysis, nil
}

func (e *Engine) analyze(ctx context.Context, instructions, data string) (string, error) {
	model, effort := e.deepModel, e.deepEffort
	if model == "" {
		model, effort = e.model, e.effort
	}
	resp, err := e.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        model,
		Instructions: openai.String(instructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage(data, responses.EasyInputMessageRoleUser),
			},
		},
		Reasoning: shared.ReasoningParam{Effort: effort},
		Store:     openai.Bool(false),
	})
	if err != nil {
		return "", fmt.Errorf("analyse : %w", err)
	}
	out := strings.TrimSpace(resp.OutputText())
	if out == "" {
		return "", fmt.Errorf("analyse vide")
	}
	return out, nil
}

// debriefPrompt : la consigne du lecteur approfondi. Elle est écrite contre les
// erreurs qu'on a réellement vues, pas pour faire joli — chaque paragraphe
// répond à une façon précise dont un débrief se trompe.
func debriefPrompt(now time.Time, tz, userName string, facts []FactView, label, question string) string {
	who := userName
	if who == "" {
		who = "l'utilisateur"
	}
	focus := ""
	if q := strings.TrimSpace(question); q != "" {
		focus = fmt.Sprintf("\nSA QUESTION, MOT POUR MOT : « %s ». Ton analyse y répond d'abord ; le reste vient ensuite, et seulement s'il éclaire.\n", q)
	}
	memory := "Rien n'est encore retenu sur son entourage."
	if len(facts) > 0 {
		memory = "Ce que l'on sait déjà de son monde (sers-t'en pour savoir qui est qui, ne le recopie pas) :\n" + encode(facts)
	}

	return fmt.Sprintf(`Tu es le lecteur attentif derrière Raoul, l'assistant de %[1]s. On te confie %[2]s pour en faire le débrief. Nous sommes le %[3]s, il est %[4]s (fuseau %[5]s).
%[6]s
%[7]s

COMMENT LIRE — c'est là que se font les erreurs

1. QUI EST QUI. Les messages marqués de_toi, « toi » ou de son adresse sont les SIENS : ce qu'il a lui-même dit ou promis. Ne lui présente jamais ses propres messages comme ceux d'un autre. Repère les rôles (qui décide, qui demande, qui exécute) à partir de ce qu'ils écrivent et de ce qu'on sait déjà d'eux.

2. RECOLLE LES RÉPONSES À LEURS QUESTIONS. Dans un groupe, la réponse arrive souvent dix messages après la question, et « ok », « go », « pareil » ne veulent rien dire seuls. Les marques « ↪ en réponse à » et les champs reponses_dans_le_fil disent exactement à quoi un message répond : c'est la vérité, fie-t'y plutôt qu'à l'ordre d'affichage. Sans marque, rattache au sujet le plus plausible — et si deux lectures se valent, dis-le.

3. RECONSTITUE LE SUJET DEPUIS SON DÉBUT. Qui a lancé quoi, ce qui a été proposé, ce qui a été objecté, ce qui a été tranché et par qui, ce qui reste ouvert. Le dernier message n'est presque jamais le sujet. L'ordre chronologique : les messages peuvent arriver du plus récent au plus ancien, fie-toi au champ quand.

4. CE QUI L'ATTEND LUI. Une question qui lui est posée, une validation qu'on attend de lui, une promesse qu'il a faite et pas encore tenue, une échéance, quelqu'un qui relance. C'est la partie qu'il ne doit jamais rater.

5. LE NON-DIT. Une relance polie qui trahit de l'agacement, une tension entre deux personnes, un « on verra » qui veut dire non, une urgence qui ne dit pas son nom. Tu le signales quand c'est net — en disant que c'est ton interprétation.

6. LE JARGON ET LES LANGUES. Sigles, noms de projets, abréviations, messages en anglais : tu les comprends dans leur contexte et tu restitues en français. Si un terme t'échappe, dis-le plutôt que de l'inventer.

7. LES PIÈCES ET LE BRUIT. Un fichier, une photo, un vocal : c'est une information (« il a envoyé le devis »), même sans le contenu. Les messages automatiques, les « merci », les emojis seuls ne méritent pas une ligne.

8. EXACTITUDE. Chaque nom, chiffre, date et décision que tu écris est dans les messages. Pour les moments, recopie le champ quand tel quel (« hier à 16h30 »), ne recalcule rien. Ce qui n'est pas écrit n'est pas un fait — c'est une hypothèse, et tu le dis comme telle.

CE QUE TU RENDS — en français, sans markdown, sans emoji, sans puces décoratives

L'ESSENTIEL : trois à six phrases qu'on peut dire à voix haute telles quelles, en le tutoyant, comme un collègue qui a tout lu : de quoi ça parle, où ça en est, ce qu'on attend de lui. Commence par le plus important, jamais par « Dans cette conversation ».

LES DÉTAILS : les points qui pourraient lui servir s'il creuse — qui a dit quoi qui compte, les décisions et leurs auteurs, les questions ouvertes, les échéances, les pièces envoyées, tes doutes d'interprétation. Une ligne par point, en phrases courtes.`,
		who,
		label,
		now.Format("Monday 2 January 2006"),
		now.Format("15h04"),
		tz,
		focus,
		memory,
	)
}

// debriefRules : la consigne de la boucle vocale pour savoir QUAND déléguer.
const debriefRules = `QUAND IL DEMANDE DE COMPRENDRE, PAS DE LIRE

« Fais-moi un débrief du groupe Azul », « résume-moi le mail de Cyril », « explique-moi ce qui se passe sur le canal dev », « c'est quoi l'histoire avec le devis », « qu'est-ce qu'ils attendent de moi » : ce sont des demandes de compréhension, et elles passent par l'outil debriefer — pas par une lecture simple. Il lit la conversation en entier et la fait analyser à fond ; toi, tu restitues son ESSENTIEL avec ta voix, sans y ajouter ce qui n'y est pas, et tu gardes LES DÉTAILS pour ses questions suivantes, auxquelles tu réponds depuis l'analyse sans relire.

Les lectures simples (lire_mail, lire_canal_slack, lire_conversation_whatsapp) restent pour « lis-moi », « c'est quoi le dernier message », « il a répondu quoi » — quand il veut le texte ou un fait ponctuel, pas une synthèse.

`
