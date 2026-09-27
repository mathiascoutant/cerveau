package slack

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
)

// Recherche transversale : par personne, par mots, par période, dans toutes
// les conversations accessibles.
//
// La lecture d'un canal répond à « qu'est-ce qui se dit sur #projet ». Elle ne
// répond pas à « qu'est-ce que Xavier a demandé aujourd'hui » : Xavier écrit
// dans six canaux, et la question porte sur LUI, pas sur un endroit. Ce fichier
// parcourt donc les conversations sur une fenêtre de temps et ne garde que ce
// qui correspond — avec une règle absolue : l'auteur d'un message est celui que
// porte le champ `user` de l'API, comparé à un identifiant. Jamais un prénom
// deviné dans le texte. Un message qui CITE Xavier n'est pas un message DE
// Xavier, et c'est précisément la distinction que cette recherche doit tenir.

// Fenêtre par défaut d'une recherche sans date : une semaine.
const DefaultSearchWindow = 7 * 24 * time.Hour

// Bornes de la recherche. Une recherche parcourt jusqu'à cent messages par
// conversation et ouvre les fils récents pour ne pas rater une réponse donnée
// dans un fil — c'est souvent là qu'on demande quelque chose à quelqu'un.
const (
	searchPerChannel = 100
	searchThreads    = 12
	maxSearchResults = 60
)

// SearchQuery cadre une recherche.
type SearchQuery struct {
	// Author : la personne dont on veut LES messages, telle qu'elle a été
	// dite. Résolue contre l'annuaire ; ambiguë, la recherche le dit.
	Author string
	// Mentions : ne garder que les messages où cette personne est citée par
	// une vraie mention. Répond à « qui a demandé quelque chose à Xavier ».
	Mentions string
	// Text : mots à retrouver dans le texte rendu, insensible à la casse.
	Text string
	// Channel : limiter à une conversation, nom tel qu'il a été dit. Vide =
	// toutes.
	Channel string
	// Since : ne garder que ce qui est postérieur. Zéro = DefaultSearchWindow.
	Since time.Time
	Limit int
}

// SearchResult est ce qu'une recherche rend, avec de quoi dire ce qui a été
// réellement parcouru : un « rien trouvé » ne vaut que si l'on sait où l'on a
// cherché.
type SearchResult struct {
	// Author : la personne résolue (« Xavier Martin »), quand on cherchait par
	// auteur. C'est ce qu'il faut annoncer si le prénom dicté était ambigu.
	Author   string
	AuthorID string
	// Mentioned : idem pour la personne citée.
	Mentioned   string
	MentionedID string
	Messages    []Message
	// Channels : combien de conversations ont été parcourues.
	Channels int
	Since    time.Time
	Warnings []string
}

// Search parcourt les conversations et rend les messages qui correspondent, du
// plus ancien au plus récent.
func (c *Client) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	res := SearchResult{Since: q.Since}
	if q.Limit <= 0 {
		q.Limit = 25
	}
	if q.Limit > maxSearchResults {
		q.Limit = maxSearchResults
	}
	if res.Since.IsZero() {
		res.Since = time.Now().Add(-DefaultSearchWindow)
	}

	if strings.TrimSpace(q.Author) != "" {
		id, name, err := c.FindUser(ctx, q.Author)
		if err != nil {
			return res, err
		}
		res.AuthorID, res.Author = id, name
	}
	if strings.TrimSpace(q.Mentions) != "" {
		id, name, err := c.FindUser(ctx, q.Mentions)
		if err != nil {
			return res, err
		}
		res.MentionedID, res.Mentioned = id, name
	}

	convs, err := c.rawConversations(ctx)
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(q.Channel) != "" {
		target, ambiguous, ok := matchConversation(ctx, c, convs, q.Channel)
		if len(ambiguous) > 0 {
			return res, &AmbiguousConversationError{Query: q.Channel, Choices: ambiguous}
		}
		if !ok {
			return res, fmt.Errorf("aucune conversation ne correspond à %q", q.Channel)
		}
		convs = []conversation{target}
	}
	res.Channels = len(convs)

	words := strings.Fields(strings.ToLower(strings.TrimSpace(q.Text)))
	keep := func(m Message) bool {
		if res.AuthorID != "" && m.AuteurID != res.AuthorID {
			return false
		}
		if res.MentionedID != "" {
			cited := false
			for _, mention := range m.Mentions {
				if mention.ID == res.MentionedID {
					cited = true
					break
				}
			}
			if !cited {
				return false
			}
		}
		if len(words) > 0 {
			text := strings.ToLower(m.Texte)
			for _, w := range words {
				if !strings.Contains(text, w) {
					return false
				}
			}
		}
		return true
	}

	type outcome struct {
		messages []Message
		warning  string
	}
	results := make([]outcome, len(convs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	oldest := fmt.Sprintf("%d", res.Since.Unix())

	for i, conv := range convs {
		wg.Add(1)
		go func(i int, conv conversation) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var hist struct {
				apiResponse
				Messages []rawMessage `json:"messages"`
			}
			err := c.call(ctx, "conversations.history", url.Values{
				"channel": {conv.ID},
				"oldest":  {oldest},
				"limit":   {fmt.Sprintf("%d", searchPerChannel)},
			}, &hist)
			if err != nil {
				results[i] = outcome{warning: fmt.Sprintf("%s : %v", c.label(ctx, conv), err)}
				return
			}
			label := c.label(ctx, conv)
			var found []Message
			threads := 0
			for _, m := range hist.Messages {
				msg, ok := c.toMessage(ctx, m, maxReadText, conv.ID, label)
				if ok && keep(msg) {
					found = append(found, msg)
				}
				// Les fils : une demande faite à quelqu'un se fait souvent en
				// réponse, et l'historique du canal n'en montre que la racine.
				if m.ReplyCount > 0 && threads < searchThreads {
					threads++
					for _, reply := range c.threadReplies(ctx, conv.ID, label, m.TS) {
						if keep(reply) {
							found = append(found, reply)
						}
					}
				}
			}
			results[i] = outcome{messages: found}
		}(i, conv)
	}
	wg.Wait()

	for _, r := range results {
		if r.warning != "" {
			res.Warnings = append(res.Warnings, r.warning)
		}
		res.Messages = append(res.Messages, r.messages...)
	}
	// Du plus ancien au plus récent : un échange se relit dans l'ordre où il
	// s'est déroulé. Puis on garde les derniers, qui sont ceux que la question
	// vise presque toujours.
	sort.SliceStable(res.Messages, func(i, j int) bool { return res.Messages[i].Quand.Before(res.Messages[j].Quand) })
	if len(res.Messages) > q.Limit {
		res.Messages = res.Messages[len(res.Messages)-q.Limit:]
	}
	if len(res.Warnings) > 3 {
		res.Warnings = append(res.Warnings[:3], fmt.Sprintf("… et %d autres conversations illisibles", len(res.Warnings)-3))
	}
	return res, nil
}

// --- annuaire ----------------------------------------------------------------

type directoryUser struct {
	ID       string
	RealName string
	Display  string
	Handle   string
	Deleted  bool
	IsBot    bool
}

// AmbiguousUserError : plusieurs membres répondent au nom dicté.
type AmbiguousUserError struct {
	Query   string
	Choices []string
}

func (e *AmbiguousUserError) Error() string {
	return fmt.Sprintf("plusieurs personnes correspondent à %q : %s", e.Query, strings.Join(e.Choices, " ; "))
}

// FindUser résout un nom dicté en membre de l'espace de travail.
//
// Rend l'identifiant et le nom complet. Deux membres qui se valent ne sont pas
// départagés : « Xavier » quand il y a deux Xavier n'a pas de réponse, et
// attribuer les messages du mauvais serait pire que demander lequel.
func (c *Client) FindUser(ctx context.Context, query string) (id, name string, err error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", "", fmt.Errorf("nom de personne vide")
	}
	if strings.EqualFold(query, "moi") || strings.EqualFold(query, "toi") || strings.EqualFold(query, "me") {
		self := c.selfUser(ctx)
		if self == "" {
			return "", "", fmt.Errorf("impossible de résoudre l'utilisateur courant")
		}
		return self, "toi", nil
	}
	users, err := c.loadDirectory(ctx)
	if err != nil {
		return "", "", err
	}
	names := make([][]string, 0, len(users))
	for _, u := range users {
		candidates := []string{u.RealName}
		if u.Display != "" {
			candidates = append(candidates, u.Display)
		}
		if u.Handle != "" {
			candidates = append(candidates, u.Handle)
		}
		if fields := strings.Fields(u.RealName); len(fields) > 1 {
			// Le prénom seul : c'est presque toujours ainsi qu'on désigne un
			// collègue, et « Xavier » doit trouver « Xavier Martin ».
			candidates = append(candidates, fields[0])
		}
		names = append(names, candidates)
	}
	winner, tied := fuzzy.Resolve(query, names)
	if winner >= 0 {
		u := users[winner]
		return u.ID, displayName(u), nil
	}
	if len(tied) > 0 {
		choices := make([]string, 0, len(tied))
		for _, i := range tied {
			choices = append(choices, displayName(users[i]))
		}
		return "", "", &AmbiguousUserError{Query: query, Choices: choices}
	}
	closest := make([]string, 0, 4)
	for _, r := range fuzzy.Rank(query, names) {
		closest = append(closest, displayName(users[r.Index]))
		if len(closest) == 4 {
			break
		}
	}
	if len(closest) > 0 {
		return "", "", fmt.Errorf("aucun membre ne s'appelle %q. Les plus proches : %s", query, strings.Join(closest, ", "))
	}
	return "", "", fmt.Errorf("aucun membre ne s'appelle %q", query)
}

func displayName(u directoryUser) string {
	if strings.TrimSpace(u.RealName) != "" {
		return u.RealName
	}
	if u.Display != "" {
		return u.Display
	}
	return u.Handle
}

// loadDirectory charge les membres, une fois par client. users.list est
// paginé : on suit le curseur jusqu'au bout, bornés à quelques milliers de
// membres — au-delà, on cherche dans ce qu'on a.
func (c *Client) loadDirectory(ctx context.Context) ([]directoryUser, error) {
	c.mu.Lock()
	if c.directoryLoaded {
		defer c.mu.Unlock()
		return c.directory, nil
	}
	c.mu.Unlock()

	var out []directoryUser
	cursor := ""
	for page := 0; page < 10; page++ {
		var res struct {
			apiResponse
			Members []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				RealName string `json:"real_name"`
				Deleted  bool   `json:"deleted"`
				IsBot    bool   `json:"is_bot"`
				Profile  struct {
					RealName    string `json:"real_name"`
					DisplayName string `json:"display_name"`
				} `json:"profile"`
			} `json:"members"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		params := url.Values{"limit": {"500"}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		if err := c.call(ctx, "users.list", params, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Members {
			if m.Deleted || m.IsBot || m.ID == "USLACKBOT" {
				continue
			}
			real := m.RealName
			if real == "" {
				real = m.Profile.RealName
			}
			out = append(out, directoryUser{
				ID: m.ID, RealName: real, Display: m.Profile.DisplayName, Handle: m.Name,
			})
		}
		cursor = res.Meta.NextCursor
		if cursor == "" {
			break
		}
	}

	c.mu.Lock()
	c.directory, c.directoryLoaded = out, true
	for _, u := range out {
		if _, known := c.userNames[u.ID]; !known && strings.TrimSpace(u.RealName) != "" {
			c.userNames[u.ID] = firstName(u.RealName)
		}
	}
	c.mu.Unlock()
	return out, nil
}
