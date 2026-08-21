package slack

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
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
		names := make([]string, 0, 8)
		for _, conv := range convs {
			names = append(names, c.label(ctx, conv))
			if len(names) == 8 {
				break
			}
		}
		return "", nil, fmt.Errorf("aucune conversation ne correspond à %q. Quelques-unes existantes : %s",
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

// matchConversation cherche d'abord une correspondance exacte, puis partielle.
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
// Un nom exact l'emporte tout de suite. À défaut, on rassemble TOUTES les
// correspondances partielles : s'il y en a plusieurs, le troisième retour les
// remonte pour qu'on demande laquelle plutôt que de trancher à l'aveugle.
func matchConversation(ctx context.Context, c *Client, convs []conversation, query string) (conversation, []string, bool) {
	want := normalizeName(query)
	if want == "" {
		return conversation{}, nil, false
	}

	var partials []conversation
	seen := map[string]bool{}

	for _, conv := range convs {
		candidates := []string{normalizeName(conv.Name)}
		if conv.IsIM {
			candidates = append(candidates, normalizeName(c.userName(ctx, conv.User)))
		}
		for _, cand := range candidates {
			if cand == "" {
				continue
			}
			if cand == want {
				return conv, nil, true
			}
			if !seen[conv.ID] && (strings.Contains(cand, want) || strings.Contains(want, cand)) {
				seen[conv.ID] = true
				partials = append(partials, conv)
			}
		}
	}

	switch len(partials) {
	case 0:
		return conversation{}, nil, false
	case 1:
		return partials[0], nil, true
	}

	choices := make([]string, 0, maxAmbiguousChoices)
	for _, conv := range partials {
		if len(choices) == maxAmbiguousChoices {
			break
		}
		choices = append(choices, c.label(ctx, conv))
	}
	return conversation{}, choices, false
}

func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "#")
	s = strings.TrimPrefix(s, "dm ")
	s = strings.TrimPrefix(s, "groupe ")
	s = strings.TrimPrefix(s, "canal ")
	s = strings.TrimPrefix(s, "le ")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	return strings.Join(strings.Fields(s), " ")
}
