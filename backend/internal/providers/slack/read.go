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
	// Fichiers : les pièces jointes, par leur nom. « Il a envoyé le devis »
	// ne se comprend pas si le devis n'apparaît nulle part.
	Fichiers []string `json:"fichiers,omitempty"`
	// DeToi : c'est lui qui l'a écrit. Sans cette marque, son propre message
	// arrive signé de son prénom comme celui de n'importe qui, et « dites-moi
	// si c'est bloquant, pour @Xavier » devient une question que Xavier lui
	// pose — c'est l'erreur exacte qui a motivé le champ.
	DeToi bool `json:"de_toi,omitempty"`
	// Fil : les réponses données dans le fil de ce message. Sur Slack, la
	// discussion se tient souvent là, et l'historique du canal n'en montre que
	// la question — lire le canal sans ses fils, c'est lire la moitié des
	// échanges et en tirer des conclusions sur l'autre moitié.
	Fil []Message `json:"fil,omitempty"`
}

// Longueurs retenues à la lecture. Assez pour qu'un message argumenté arrive
// entier : couper au milieu d'une explication, c'est garder la question et
// perdre la réponse.
const (
	maxReadText     = 3000
	maxThreadsRead  = 10
	maxRepliesRead  = 30
	maxReplyTextLen = 1500
)

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
	if limit <= 0 {
		limit = 15
	}
	// Plafond haut à dessein : comprendre un fil demande parfois de remonter
	// loin, et un extrait tronqué au mauvais endroit fait dire n'importe quoi
	// sur ce qui s'y joue. C'est le modèle qui décide de la profondeur, selon
	// ce qu'il cherche.
	if limit > MaxRead {
		limit = MaxRead
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
		names := make([]string, 0, 6)
		for _, r := range fuzzy.Rank(query, candidateNames(ctx, c, convs)) {
			names = append(names, c.label(ctx, convs[r.Index]))
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

	label := c.label(ctx, target)
	// Le nom prononcé n'est pas celui du canal : on a très probablement raison,
	// mais « très probablement » ne se dit pas à quelqu'un qui écoute sans
	// vérifier. On rend la main pour qu'il confirme.
	if !fuzzy.Exact(query, target.Name, label, strings.TrimPrefix(label, "#")) {
		return "", nil, &UnconfirmedConversationError{Query: query, Found: label}
	}

	var hist struct {
		apiResponse
		Messages []rawMessage `json:"messages"`
	}
	err = c.call(ctx, "conversations.history", url.Values{
		"channel": {target.ID},
		"limit":   {fmt.Sprintf("%d", limit)},
	}, &hist)
	if err != nil {
		return "", nil, err
	}

	// Slack rend l'historique du plus récent au plus ancien. On le remet dans
	// l'ordre où il s'est écrit : lu à l'envers, une réponse précède sa
	// question, et le modèle rattache chaque message au mauvais voisin.
	//
	// Les fils à ouvrir se choisissent avant : les plus récents, puisque c'est
	// là que se joue ce qu'on demande.
	withThread := make(map[string]bool, maxThreadsRead)
	for _, m := range hist.Messages {
		if m.ReplyCount > 0 && len(withThread) < maxThreadsRead {
			withThread[m.TS] = true
		}
	}
	slices.Reverse(hist.Messages)

	out := make([]Message, 0, len(hist.Messages))
	for _, m := range hist.Messages {
		msg, ok := c.toMessage(ctx, m, maxReadText)
		if !ok {
			continue
		}
		if withThread[m.TS] {
			msg.Fil = c.threadReplies(ctx, target.ID, m.TS)
		}
		out = append(out, msg)
	}
	return label, out, nil
}

type rawMessage struct {
	User       string `json:"user"`
	Text       string `json:"text"`
	Subtype    string `json:"subtype"`
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts"`
	ReplyCount int    `json:"reply_count"`
	Files      []struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"files"`
}

func (c *Client) toMessage(ctx context.Context, m rawMessage, maxLen int) (Message, bool) {
	// file_share et thread_broadcast portent un vrai message ; les autres
	// sous-types (arrivées, départs, changements de sujet) ne sont que du bruit.
	if m.Subtype != "" && m.Subtype != "file_share" && m.Subtype != "thread_broadcast" {
		return Message{}, false
	}
	var files []string
	for _, f := range m.Files {
		name := f.Title
		if name == "" {
			name = f.Name
		}
		if name != "" {
			files = append(files, name)
		}
	}
	if strings.TrimSpace(m.Text) == "" && len(files) == 0 {
		return Message{}, false
	}
	author := "un bot"
	mine := m.User != "" && m.User == c.selfUser(ctx)
	switch {
	case mine:
		author = "toi"
	case m.User != "":
		author = c.userName(ctx, m.User)
	}
	return Message{
		DeToi:    mine,
		Auteur:   author,
		Texte:    truncate(c.renderText(ctx, m.Text), maxLen),
		Quand:    parseSlackTS(m.TS),
		Fichiers: files,
	}, true
}

// threadReplies lit les réponses d'un fil, sans le message d'origine (déjà
// présent dans l'historique). Un fil illisible n'empêche pas de rendre le
// canal : on le laisse vide plutôt que d'échouer.
func (c *Client) threadReplies(ctx context.Context, channel, ts string) []Message {
	var resp struct {
		apiResponse
		Messages []rawMessage `json:"messages"`
	}
	err := c.call(ctx, "conversations.replies", url.Values{
		"channel": {channel},
		"ts":      {ts},
		"limit":   {fmt.Sprintf("%d", maxRepliesRead+1)},
	}, &resp)
	if err != nil {
		return nil
	}
	out := make([]Message, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		if m.TS == ts {
			continue
		}
		if msg, ok := c.toMessage(ctx, m, maxReplyTextLen); ok {
			out = append(out, msg)
		}
	}
	return out
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

// MaxRead : profondeur maximale d'une lecture de conversation.
const MaxRead = 100

// UnconfirmedConversationError : le nom prononcé désigne probablement cette
// conversation, mais ne lui est pas identique. Voir fuzzy.Exact — c'est
// l'appelant qui demande confirmation, pas ce paquet qui devine.
type UnconfirmedConversationError struct {
	Query string
	Found string
}

func (e *UnconfirmedConversationError) Error() string {
	return fmt.Sprintf("%q désigne probablement %s, à confirmer", e.Query, e.Found)
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

// candidateNames rend, pour chaque conversation, les noms sous lesquels on peut
// la désigner à l'oral. Un message direct s'appelle par le prénom de la
// personne, jamais par l'identifiant interne du canal.
func candidateNames(ctx context.Context, c *Client, convs []conversation) [][]string {
	out := make([][]string, 0, len(convs))
	for _, conv := range convs {
		names := []string{conv.Name}
		if conv.IsIM {
			names = append(names, c.userName(ctx, conv.User))
		}
		out = append(out, names)
	}
	return out
}

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
	winner, tied := fuzzy.Resolve(query, candidateNames(ctx, c, convs))
	if winner >= 0 {
		return convs[winner], nil, true
	}
	choices := make([]string, 0, len(tied))
	for _, i := range tied {
		choices = append(choices, c.label(ctx, convs[i]))
	}
	return conversation{}, choices, false
}
