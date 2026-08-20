package slack

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// Slack ne transporte pas le texte affiché : il transporte du mrkdwn, où toute
// entité est encodée entre chevrons. Une mention devient « <@U5G2I82BU> », un
// canal « <#C024BE7LR|general> », un lien « <https://x.fr|le devis> ». Rendre ce
// texte brut à l'assistant lui fait lire des identifiants à voix haute, alors
// que l'utilisateur, lui, a vu un prénom dans son client Slack.
var entityRE = regexp.MustCompile(`<[^<>]*>`)

// renderText transforme le mrkdwn Slack en texte lisible par un humain.
func (c *Client) renderText(ctx context.Context, s string) string {
	s = entityRE.ReplaceAllStringFunc(s, func(m string) string {
		return c.renderEntity(ctx, m[1:len(m)-1])
	})
	// Après coup seulement : un « < » littéral du message arrive encodé en
	// « &lt; », donc le désencoder avant aurait fabriqué de fausses entités.
	return unescapeMrkdwn(s)
}

func (c *Client) renderEntity(ctx context.Context, body string) string {
	head, label, hasLabel := strings.Cut(body, "|")
	label = strings.TrimSpace(label)

	switch {
	case strings.HasPrefix(head, "@"):
		// Mention d'une personne. Slack joint parfois le libellé affiché, mais
		// le plus souvent il n'y a que l'identifiant : c'est le cas à résoudre.
		if hasLabel && label != "" {
			return "@" + firstName(strings.TrimPrefix(label, "@"))
		}
		return "@" + c.userName(ctx, head[1:])

	case strings.HasPrefix(head, "#"):
		if hasLabel && label != "" {
			return "#" + strings.TrimPrefix(label, "#")
		}
		return "#" + c.channelName(ctx, head[1:])

	case strings.HasPrefix(head, "!"):
		return renderSpecial(strings.TrimPrefix(head, "!"), label)

	case hasLabel && label != "":
		// Lien avec libellé : « <https://x.fr|le devis> » se lit « le devis ».
		return label

	case strings.HasPrefix(head, "mailto:"):
		return strings.TrimPrefix(head, "mailto:")
	}
	return head
}

// renderSpecial couvre les entités « <!… > » : mentions collectives, groupes
// d'utilisateurs et dates formatées par Slack.
func renderSpecial(head, label string) string {
	switch {
	case head == "here" || strings.HasPrefix(head, "here|"):
		return "@ici"
	case head == "channel" || strings.HasPrefix(head, "channel|"):
		return "@canal"
	case head == "everyone" || strings.HasPrefix(head, "everyone|"):
		return "@tout le monde"
	case strings.HasPrefix(head, "subteam^"):
		if label != "" {
			return "@" + strings.TrimPrefix(label, "@")
		}
		return "un groupe"
	case strings.HasPrefix(head, "date^"):
		// Slack fournit déjà la date rendue en libellé de repli.
		if label != "" {
			return label
		}
		return ""
	}
	if label != "" {
		return label
	}
	return "@" + head
}

// unescapeMrkdwn annule l'échappement que Slack applique à ces trois
// caractères, et à eux seuls — le reste du HTML n'est pas échappé.
func unescapeMrkdwn(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

// channelName résout un identifiant de canal en son nom. Le cas est rare :
// Slack accompagne presque toujours « <#C…> » du nom du canal.
func (c *Client) channelName(ctx context.Context, id string) string {
	if id == "" {
		return "un canal"
	}
	c.mu.Lock()
	if n, ok := c.channelNames[id]; ok {
		c.mu.Unlock()
		return n
	}
	c.mu.Unlock()

	var res struct {
		apiResponse
		Channel struct {
			Name string `json:"name"`
		} `json:"channel"`
	}
	if err := c.call(ctx, "conversations.info", url.Values{"channel": {id}}, &res); err != nil ||
		strings.TrimSpace(res.Channel.Name) == "" {
		// Pas de mise en cache : l'échec peut être passager (429, scope
		// temporairement absent), et un nom vaut mieux au prochain passage.
		return "un canal"
	}
	c.mu.Lock()
	c.channelNames[id] = res.Channel.Name
	c.mu.Unlock()
	return res.Channel.Name
}
