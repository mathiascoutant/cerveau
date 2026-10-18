package slack

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
)

// Comment un message le vise.
//
// Sur Slack, on est cité de quatre façons qui n'engagent pas pareil : une
// mention nominative (@Mathias) s'adresse à lui seul ; son prénom écrit en
// toutes lettres (« Mathias, tu as fait ça ? ») aussi, même sans @ ; une
// mention de canal (@ici, @canal) s'adresse à tout le monde présent ; une
// mention de groupe (@devs) à une équipe dont il fait peut-être partie. Le
// reste est l'activité du canal, qui se lit plus tard. Les confondre, c'est
// remonter comme urgent chaque « bonjour à tous », ou rater la seule question
// qui lui était posée parce qu'elle n'avait pas d'arobase.
const (
	VisePerso  = "toi"
	ViseNomme  = "prenom"
	ViseGroupe = "groupe"
	ViseEquipe = "equipe"
)

// tokenRE : les entités du mrkdwn brut — mentions, liens, canaux. On les
// retire avant de chercher le prénom : un lien qui contient « mathias » n'est
// pas une interpellation.
var tokenRE = regexp.MustCompile(`<[^>]*>`)

// targeting dit comment un message brut le vise, du plus au moins personnel.
// Vide quand il ne le vise pas, ou quand c'est lui qui l'a écrit.
func (c *Client) targeting(ctx context.Context, raw, authorID string) string {
	self := c.selfUser(ctx)
	if self == "" || authorID == self {
		return ""
	}
	if strings.Contains(raw, "<@"+self+">") || strings.Contains(raw, "<@"+self+"|") {
		return VisePerso
	}
	if first := c.selfFirstName(ctx); first != "" && hasWord(tokenRE.ReplaceAllString(raw, " "), first) {
		return ViseNomme
	}
	if strings.Contains(raw, "<!here") || strings.Contains(raw, "<!channel") || strings.Contains(raw, "<!everyone") {
		return ViseGroupe
	}
	if strings.Contains(raw, "<!subteam^") {
		return ViseEquipe
	}
	return ""
}

// selfFirstName : son prénom, tel que Slack le connaît. Vide quand il est trop
// court pour être cherché sans se confondre avec un mot (« Al »).
func (c *Client) selfFirstName(ctx context.Context) string {
	self := c.selfUser(ctx)
	if self == "" {
		return ""
	}
	name := c.userName(ctx, self)
	if name == "quelqu'un" || utf8.RuneCountInString(name) < 3 {
		return ""
	}
	return name
}

// hasWord : le mot apparaît tel quel, à la casse et aux accents près.
func hasWord(text, word string) bool {
	want := fuzzy.Normalize(word)
	return slices.Contains(strings.Fields(fuzzy.Normalize(text)), want)
}

// Interpellation : un message qui le vise personnellement, avec ce qui le
// précède dans la conversation — là où se trouve le « ça » de « tu as fait
// ça ? ».
type Interpellation struct {
	Message  Message
	Contexte []Message
}

// contextDepth : messages remontés avant une interpellation. Une demande se
// pose rarement plus de cinq messages après ce qu'elle désigne ; au-delà, on
// ne remonte plus le contexte, on relit le canal.
const contextDepth = 6

// ContextBefore rend les messages qui précèdent un message dans sa
// conversation — dans son fil s'il en fait partie, message d'origine compris —,
// du plus ancien au plus récent. Un échec rend une liste vide : le contexte
// éclaire, il ne conditionne pas.
func (c *Client) ContextBefore(ctx context.Context, channelID, label, ts, threadTS string) []Message {
	if channelID == "" || ts == "" {
		return nil
	}
	var resp struct {
		apiResponse
		Messages []rawMessage `json:"messages"`
	}
	var raws []rawMessage
	if threadTS != "" && threadTS != ts {
		err := c.call(ctx, "conversations.replies", url.Values{
			"channel": {channelID}, "ts": {threadTS}, "latest": {ts}, "inclusive": {"false"},
			"limit": {fmt.Sprintf("%d", maxRepliesRead+1)},
		}, &resp)
		if err != nil {
			return nil
		}
		// Les réponses d'un fil arrivent dans l'ordre ; on garde les
		// dernières avant le message, l'origine du fil toujours en tête.
		for _, m := range resp.Messages {
			if m.TS < ts {
				raws = append(raws, m)
			}
		}
		if len(raws) > contextDepth {
			raws = append(raws[:1], raws[len(raws)-contextDepth+1:]...)
		}
	} else {
		err := c.call(ctx, "conversations.history", url.Values{
			"channel": {channelID}, "latest": {ts}, "inclusive": {"false"},
			"limit": {fmt.Sprintf("%d", contextDepth)},
		}, &resp)
		if err != nil {
			return nil
		}
		raws = resp.Messages
		slices.Reverse(raws)
	}
	out := make([]Message, 0, len(raws))
	for _, m := range raws {
		if msg, ok := c.toMessage(ctx, m, maxReplyTextLen, channelID, label); ok {
			out = append(out, msg)
		}
	}
	return out
}
