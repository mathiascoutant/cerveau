package slack

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
)

// Conversation est une conversation accessible à l'utilisateur.
type Conversation struct {
	ID    string `json:"-"`
	Nom   string `json:"nom"`
	Type  string `json:"type"` // "canal" | "dm" | "groupe"
	Prive bool   `json:"prive,omitempty"`
}

// Message est un message lu à la demande.
//
// Quand est un instant, pas une chaîne : la mise en forme demande le fuseau de
// l'utilisateur, que ce paquet ne connaît pas. Elle se faisait ici en heure
// locale du serveur — donc en UTC sur le VPS, et tous les horaires Slack
// arrivaient décalés de deux heures l'été.
type Message struct {
	Auteur string    `json:"auteur"`
	Texte  string    `json:"texte"`
	Quand  time.Time `json:"quand"`
}

// ListConversations énumère ce à quoi l'utilisateur a accès.
func (c *Client) ListConversations(ctx context.Context) ([]Conversation, error) {
	convs, err := c.rawConversations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Conversation, 0, len(convs))
	for _, conv := range convs {
		out = append(out, Conversation{
			ID:   conv.ID,
			Nom:  strings.TrimPrefix(c.label(ctx, conv), "#"),
			Type: kindOf(conv),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nom < out[j].Nom })
	return out, nil
}

// ReadConversation lit les derniers messages d'une conversation désignée par
// son nom. Le modèle reçoit un nom prononcé à l'oral (« le canal projet »,
// « dièse dev »), donc la résolution doit tolérer les approximations.
func (c *Client) ReadConversation(ctx context.Context, query string, limit int) (string, []Message, error) {
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	convs, err := c.rawConversations(ctx)
	if err != nil {
		return "", nil, err
	}

	target, ambiguous, ok := matchConversation(ctx, c, convs, query)
	if len(ambiguous) > 0 {
		return "", nil, &AmbiguousConversationError{Query: query, Choices: ambiguous}
	}
	if !ok {
		// Les plus proches, pas les premières venues : quand la dictée a
		// déformé le nom, la bonne est presque toujours dans ces six-là, et le
		// modèle n'a plus qu'à rappeler l'outil avec l'orthographe exacte.
		near := rank(ctx, c, convs, query)
		names := make([]string, 0, 6)
		for _, r := range near {
			names = append(names, c.label(ctx, r.conv))
			if len(names) == 6 {
				break
			}
		}
		for _, conv := range convs {
			if len(names) == 6 {
				break
			}
			if label := c.label(ctx, conv); !slices.Contains(names, label) {
				names = append(names, label)
			}
		}
		return "", nil, fmt.Errorf("aucune conversation ne correspond à %q. Les plus proches : %s. "+
			"Si l'une d'elles est la bonne, rappelle l'outil avec son nom exact au lieu de dire que tu n'as pas trouvé.",
			query, strings.Join(names, ", "))
	}

	var hist struct {
		apiResponse
		Messages []struct {
			User    string `json:"user"`
			Text    string `json:"text"`
			Subtype string `json:"subtype"`
			TS      string `json:"ts"`
		} `json:"messages"`
	}
	err = c.call(ctx, "conversations.history", url.Values{
		"channel": {target.ID},
		"limit":   {fmt.Sprintf("%d", limit)},
	}, &hist)
	if err != nil {
		return "", nil, err
	}

	label := c.label(ctx, target)
	out := make([]Message, 0, len(hist.Messages))
	for _, m := range hist.Messages {
		if m.Subtype != "" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		author := "un bot"
		if m.User != "" {
			author = c.userName(ctx, m.User)
		}
		out = append(out, Message{
			Auteur: author,
			Texte:  truncate(c.renderText(ctx, m.Text), 400),
			Quand:  parseSlackTS(m.TS),
		})
	}
	return label, out, nil
}

func (c *Client) rawConversations(ctx context.Context) ([]conversation, error) {
	var convs struct {
		apiResponse
		Channels []conversation `json:"channels"`
	}
	err := c.call(ctx, "users.conversations", url.Values{
		"types":            {"public_channel,private_channel,im,mpim"},
		"exclude_archived": {"true"},
		"limit":            {"200"},
	}, &convs)
	if err != nil {
		return nil, err
	}
	return convs.Channels, nil
}

func kindOf(conv conversation) string {
	switch {
	case conv.IsIM:
		return "dm"
	case conv.IsMPIM:
		return "groupe"
	default:
		return "canal"
	}
}

// AmbiguousConversationError signale que plusieurs conversations répondent au
// même nom — deux Cyril en message direct, par exemple. En choisir une au
// hasard donnerait une réponse fausse avec l'aplomb d'une vraie.
type AmbiguousConversationError struct {
	Query   string
	Choices []string
}

func (e *AmbiguousConversationError) Error() string {
	return fmt.Sprintf("plusieurs conversations correspondent à %q : %s",
		e.Query, strings.Join(e.Choices, " ; "))
}

// Nombre de conversations proposées au choix : au-delà, la question devient
// une liste qu'on ne peut pas écouter.
const maxAmbiguousChoices = 5

// matchConversation résout un nom prononcé à l'oral.
//
// La comparaison est volontairement tolérante : ce qui arrive ici n'est pas un
// nom de canal, c'est ce qu'une reconnaissance vocale française a cru entendre.
// « dubaiairwing » revient en « dubai R wing », « #dev-back » en « dev bac ».
// Une égalité de chaînes, ou même une inclusion, répond « je ne trouve pas » sur
// un nom pourtant juste — c'est le paquet fuzzy qui rattrape l'écart.
//
// Ce qui ne change pas : on ne tranche pas entre deux candidats qui se valent.
// Lire le mauvais canal donne une réponse fausse énoncée avec l'aplomb d'une
// vraie, et personne ne va vérifier.
func matchConversation(ctx context.Context, c *Client, convs []conversation, query string) (conversation, []string, bool) {
	ranked := rank(ctx, c, convs, query)
	if len(ranked) == 0 || ranked[0].score < fuzzy.Match {
		return conversation{}, nil, false
	}
	// Un candidat nettement devant emporte la décision.
	if len(ranked) == 1 || ranked[0].score-ranked[1].score > fuzzy.Close {
		return ranked[0].conv, nil, true
	}

	choices := make([]string, 0, maxAmbiguousChoices)
	for _, r := range ranked {
		if r.score < fuzzy.Match || ranked[0].score-r.score > fuzzy.Close {
			break
		}
		if len(choices) == maxAmbiguousChoices {
			break
		}
		choices = append(choices, c.label(ctx, r.conv))
	}
	if len(choices) < 2 {
		return ranked[0].conv, nil, true
	}
	return conversation{}, choices, false
}

type scored struct {
	conv  conversation
	score float64
}

// rank note toutes les conversations, de la plus proche à la plus lointaine.
// Sert deux fois : à choisir, et à proposer les plus proches quand rien
// n'atteint le seuil — un « je ne trouve pas » suivi de six noms au hasard
// n'aide personne à se rattraper.
func rank(ctx context.Context, c *Client, convs []conversation, query string) []scored {
	wanted := spokenVariants(query)
	if len(wanted) == 0 {
		return nil
	}

	out := make([]scored, 0, len(convs))
	for _, conv := range convs {
		names := []string{conv.Name}
		if conv.IsIM {
			names = append(names, c.userName(ctx, conv.User))
		}
		best := 0.0
		for _, want := range wanted {
			for _, name := range names {
				if s := fuzzy.Score(want, name); s > best {
					best = s
				}
			}
		}
		if best > 0 {
			out = append(out, scored{conv: conv, score: best})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// mots par lesquels on désigne une conversation à l'oral sans qu'ils fassent
// partie de son nom.
var leadIns = []string{
	"le canal ", "la conversation ", "le groupe ", "la discussion ",
	"canal ", "conversation ", "groupe ", "discussion ", "diese ", "dm ",
	"le ", "la ", "les ",
}

// spokenVariants rend les lectures possibles d'un nom dicté : tel quel, et
// débarrassé de ce qui l'annonce. Les deux sont essayées plutôt qu'une seule,
// parce qu'un canal peut très bien s'appeler « les-devs » — lui retirer son
// « les » serait exactement l'erreur inverse.
func spokenVariants(query string) []string {
	base := fuzzy.Normalize(query)
	if base == "" {
		return nil
	}
	stripped := base
	for changed := true; changed; {
		changed = false
		for _, lead := range leadIns {
			if after, ok := strings.CutPrefix(stripped, lead); ok && strings.TrimSpace(after) != "" {
				stripped = after
				changed = true
			}
		}
	}
	if stripped == base {
		return []string{base}
	}
	return []string{base, stripped}
}
