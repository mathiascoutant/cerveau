package slack

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Conversation est une conversation accessible à l'utilisateur.
type Conversation struct {
	ID    string `json:"-"`
	Nom   string `json:"nom"`
	Type  string `json:"type"` // "canal" | "dm" | "groupe"
	Prive bool   `json:"prive,omitempty"`
}

// Message est un message lu à la demande.
type Message struct {
	Auteur string `json:"auteur"`
	Texte  string `json:"texte"`
	Quand  string `json:"quand"`
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

	target, ok := matchConversation(ctx, c, convs, query)
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
			Quand:  parseSlackTS(m.TS).Local().Format("02/01 15h04"),
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
func matchConversation(ctx context.Context, c *Client, convs []conversation, query string) (conversation, bool) {
	want := normalizeName(query)
	if want == "" {
		return conversation{}, false
	}

	var partial conversation
	var hasPartial bool

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
				return conv, true
			}
			if !hasPartial && (strings.Contains(cand, want) || strings.Contains(want, cand)) {
				partial, hasPartial = conv, true
			}
		}
	}
	return partial, hasPartial
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
