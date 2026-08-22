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

// MessageView est un message déjà retenu par le tri : il s'adresse
// personnellement à l'utilisateur. Ce qui reste à décider, c'est s'il appelle
// une action — et laquelle.
type MessageView struct {
	Origine string `json:"origine"` // "mail" ou le nom du canal Slack
	De      string `json:"de,omitempty"`
	Titre   string `json:"titre"`
	Extrait string `json:"extrait,omitempty"`
	Quand   string `json:"quand,omitempty"`
}

// SourceView est le message d'où sort une tâche, recopié pour l'affichage.
type SourceView struct {
	Origine string `json:"origine"`
	De      string `json:"de"`
	Titre   string `json:"titre"`
	Quand   string `json:"quand"`
}

// TaskView est une chose à faire, pas un message à lire.
type TaskView struct {
	// Action : un verbe et son objet, à l'infinitif. « Répondre au mail de
	// Westent », « Configurer deux boxes pour DAW ».
	Action string `json:"action"`
	// Urgence : "haute" ou "moyenne". Rien d'autre n'entre dans la liste.
	Urgence string `json:"urgence"`
	// Pourquoi : d'où ça vient et ce qui est attendu, en deux phrases.
	Pourquoi string       `json:"pourquoi"`
	Sources  []SourceView `json:"sources"`
}

type tasksResult struct {
	Taches []TaskView `json:"taches"`
}

// Schéma imposé au modèle. En sortie structurée stricte, chaque propriété doit
// être déclarée requise et l'objet fermé : c'est ce qui garantit que la réponse
// se décode sans repli ni tolérance.
var tasksSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"taches": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action":   map[string]any{"type": "string"},
					"urgence":  map[string]any{"type": "string", "enum": []string{"haute", "moyenne"}},
					"pourquoi": map[string]any{"type": "string"},
					"sources": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"origine": map[string]any{"type": "string"},
								"de":      map[string]any{"type": "string"},
								"titre":   map[string]any{"type": "string"},
								"quand":   map[string]any{"type": "string"},
							},
							"required":             []string{"origine", "de", "titre", "quand"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"action", "urgence", "pourquoi", "sources"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"taches"},
	"additionalProperties": false,
}

// Tasks transforme des messages en choses à faire.
//
// C'est la seule partie de la liste « à traiter » qui demande le modèle, et
// elle le demande pour ce qu'aucune règle ne sait faire : reconnaître que trois
// messages parlent du même sujet, et nommer l'action en six mots. Le filtrage,
// lui, reste déterministe en amont (internal/triage) — un tri qui change d'avis
// d'un appel à l'autre n'est pas un tri.
//
// Zéro tâche est un résultat normal et souhaitable : la consigne insiste
// là-dessus, parce qu'un modèle à qui on demande une liste a tendance à en
// fabriquer une.
func (e *Engine) Tasks(ctx context.Context, messages []MessageView, now time.Time, tz, userName string) ([]TaskView, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	who := userName
	if who == "" {
		who = "ton interlocuteur"
	}

	instructions := fmt.Sprintf(`Tu es Raoul, l'assistant personnel de %[1]s. Nous sommes le %[2]s, il est %[3]s (fuseau %[4]s).

On te donne des messages qui lui sont adressés personnellement et qu'il n'a pas encore traités. Ton travail : en tirer la liste de ce qu'il a À FAIRE. Pas la liste des messages — la liste des actions.

RÈGLE DE REGROUPEMENT. Un sujet = une ligne. Trois mails qui parlent du même devis ne font pas trois lignes, ils en font une, et ses sources contiennent les trois. C'est le point le plus important de la consigne : une liste qui répète le même sujet ne sert à rien.

RÈGLE D'EXCLUSION. N'inscris que ce qui attend une action de sa part. Ne produisent AUCUNE tâche : les notifications d'outils (tickets, CI, rapports, tableaux de bord, objets entre crochets type [tasks]), les récapitulatifs, les confirmations, les accusés de réception, les messages purement informatifs, et tout ce qui est déjà réglé dans le fil. Dans le doute, n'inscris rien. Une liste vide est une bonne réponse — meilleure qu'une liste qu'il apprendra à ignorer.

Ne retiens que l'urgent et l'important. Cinq lignes au maximum, et cinq c'est déjà beaucoup ; deux ou trois est le cas normal.

FORMAT DE « action ». Un verbe à l'infinitif et son objet, six à dix mots, sans point final, sans markdown, sans emoji. Nomme les personnes et les choses : « Répondre au mail de Westent », « Configurer deux boxes pour DAW ». Jamais de formulation vague (« Traiter les mails en attente », « Faire le point sur Slack ») : si tu ne peux pas nommer l'action, c'est qu'il n'y en a pas.

FORMAT DE « pourquoi ». Deux phrases maximum : d'où ça vient, qui le demande, et ce qui est attendu. Tu le tutoies, tu t'adresses à lui directement. C'est ce texte qu'il lit quand il touche la ligne, donc il doit lui éviter d'aller ouvrir le message.

FORMAT DE « sources ». Les messages qui ont produit la tâche, recopiés TELS QUELS depuis les données fournies — même origine, même expéditeur, même titre, même date. N'invente aucune source et n'en reformule aucune.

« urgence » vaut "haute" si ça se joue aujourd'hui ou si quelqu'un attend depuis plusieurs jours, "moyenne" sinon. Rien de moins urgent n'entre dans la liste.

N'invente rien. Tout ce que tu écris vient des messages fournis.`,
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
				responses.ResponseInputItemParamOfMessage(encode(messages), responses.EasyInputMessageRoleUser),
			},
		},
		Text: responses.ResponseTextConfigParam{
			// Strict : sans lui le schéma n'est qu'une suggestion, et une clé
			// manquante se paierait au décodage plutôt qu'à la génération.
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   "taches",
					Schema: tasksSchema,
					Strict: openai.Bool(true),
				},
			},
		},
		Reasoning: shared.ReasoningParam{Effort: e.effort},
		Store:     openai.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("liste à traiter : %w", err)
	}

	var out tasksResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.OutputText())), &out); err != nil {
		return nil, fmt.Errorf("liste à traiter : réponse illisible : %w", err)
	}
	return out.Taches, nil
}
