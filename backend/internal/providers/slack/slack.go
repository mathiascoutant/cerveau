// Package slack lit l'activité Slack avec un token utilisateur (xoxp-…).
//
// Attention à une limite de l'API : `conversations.info` ne renvoie
// `unread_count_display` et `last_read` QUE pour les messages directs. Pour les
// canaux, Slack n'expose pas l'état de lecture par utilisateur. On distingue
// donc deux notions :
//
//   - les DM et DM de groupe : vrai « non lu », fiable ;
//   - les canaux : activité récente sur une fenêtre de temps, qui est la
//     meilleure approximation disponible de « ce que tu n'as peut-être pas vu ».
//
// Cette distinction est remontée telle quelle à l'assistant, pour qu'il ne
// présente pas une activité récente comme un message non lu.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const apiBase = "https://slack.com/api/"

// Fenêtre par défaut pour l'activité des canaux.
const DefaultChannelWindow = 18 * time.Hour

// Nombre de conversations inspectées en parallèle. Slack limite à environ 50
// requêtes par minute sur ces méthodes : rester modeste évite les 429.
const concurrency = 5

// Scopes utilisateur à demander lors de la création de l'app Slack.
var RequiredScopes = []string{
	"channels:history", "channels:read",
	"groups:history", "groups:read",
	"im:history", "im:read",
	"mpim:history", "mpim:read",
	"users:read",
}

type Client struct {
	token string
	http  *http.Client

	mu           sync.Mutex
	userNames    map[string]string
	channelNames map[string]string
	self         string
	selfResolved bool
}

func New(token string) *Client {
	return &Client{
		token:        token,
		http:         &http.Client{Timeout: 20 * time.Second},
		userNames:    map[string]string{},
		channelNames: map[string]string{},
	}
}

// Activity décrit une conversation qui mérite l'attention de l'utilisateur.
type Activity struct {
	Channel string `json:"canal"`
	// "dm" (message direct ou de groupe) ou "canal".
	Kind string `json:"type"`
	// Unread n'est renseigné que pour les DM : c'est un vrai compteur de non-lus.
	Unread int `json:"non_lus,omitempty"`
	// Recent est le nombre de messages récents dans un canal, sur la fenêtre
	// demandée. Ce n'est PAS un compteur de non-lus.
	Recent int `json:"messages_recents,omitempty"`
	// Mentions : nombre de fois où l'utilisateur est cité nommément.
	Mentions int       `json:"mentions,omitempty"`
	Latest   time.Time `json:"-"`
	Messages []string  `json:"extraits,omitempty"`
}

// TestConnection valide le token et renvoie le nom du workspace.
func (c *Client) TestConnection(ctx context.Context) (team string, user string, err error) {
	var res struct {
		apiResponse
		Team string `json:"team"`
		User string `json:"user"`
	}
	if err := c.call(ctx, "auth.test", nil, &res); err != nil {
		return "", "", err
	}
	return res.Team, res.User, nil
}

type conversation struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	IsIM   bool   `json:"is_im"`
	IsMPIM bool   `json:"is_mpim"`
	User   string `json:"user"`
}

// RecentActivity renvoie les DM non lus et les canaux actifs récemment.
//
// Le second retour liste les avertissements (conversations illisibles, scope
// manquant…). Les taire donnerait un « aucun message » trompeur, alors que le
// problème est un droit absent.
func (c *Client) RecentActivity(ctx context.Context, maxThreads int, window time.Duration) ([]Activity, []string, error) {
	if maxThreads <= 0 {
		maxThreads = 10
	}
	if window <= 0 {
		window = DefaultChannelWindow
	}

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
		return nil, nil, err
	}

	type outcome struct {
		activity *Activity
		warning  string
	}

	// Chemin privilégié : l'état de lecture réel, tel que l'affiche le client
	// Slack. S'il est indisponible, on retombe sur l'activité récente.
	counts, countsErr := c.clientCounts(ctx)

	results := make([]outcome, len(convs.Channels))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, conv := range convs.Channels {
		wg.Add(1)
		go func(i int, conv conversation) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			var (
				act *Activity
				err error
			)
			if state, ok := counts[conv.ID]; ok {
				act, err = c.inspectWithReadState(ctx, conv, state)
			} else {
				act, err = c.inspect(ctx, conv, window)
			}
			if err != nil {
				results[i] = outcome{warning: fmt.Sprintf("%s : %v", c.label(ctx, conv), err)}
				return
			}
			results[i] = outcome{activity: act}
		}(i, conv)
	}
	wg.Wait()

	var out []Activity
	var warnings []string
	for _, r := range results {
		if r.warning != "" {
			warnings = append(warnings, r.warning)
		}
		if r.activity != nil {
			out = append(out, *r.activity)
		}
	}

	// Les DM d'abord — un message direct engage davantage qu'un canal actif —
	// puis du plus récent au plus ancien.
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Kind == "dm") != (out[j].Kind == "dm") {
			return out[i].Kind == "dm"
		}
		return out[i].Latest.After(out[j].Latest)
	})
	if len(out) > maxThreads {
		out = out[:maxThreads]
	}
	if len(warnings) > 3 {
		warnings = append(warnings[:3], fmt.Sprintf("… et %d autres conversations illisibles", len(warnings)-3))
	}
	if countsErr != nil {
		warnings = append(warnings, "état de lecture des canaux indisponible ("+countsErr.Error()+
			") : les canaux sont remontés en activité récente, pas en non-lus")
	}
	return out, warnings, nil
}

// inspectWithReadState exploite l'état de lecture réel : ce qui remonte est
// vraiment non lu, canaux compris.
func (c *Client) inspectWithReadState(ctx context.Context, conv conversation, state readState) (*Activity, error) {
	if !state.HasUnreads && state.MentionCount == 0 {
		return nil, nil
	}

	kind := "canal"
	if conv.IsIM || conv.IsMPIM {
		kind = "dm"
	}
	act := &Activity{Channel: c.label(ctx, conv), Kind: kind}

	params := url.Values{"channel": {conv.ID}, "limit": {"5"}}
	if state.LastRead != "" {
		params.Set("oldest", state.LastRead)
	}
	if err := c.fillMessages(ctx, params, act); err != nil {
		return nil, err
	}
	if len(act.Messages) == 0 && state.MentionCount == 0 {
		return nil, nil
	}

	act.Unread = len(act.Messages)
	if state.MentionCount > 0 {
		act.Mentions = state.MentionCount
	}
	return act, nil
}

// inspect renvoie nil si la conversation n'a rien à signaler.
func (c *Client) inspect(ctx context.Context, conv conversation, window time.Duration) (*Activity, error) {
	isDM := conv.IsIM || conv.IsMPIM

	if isDM {
		var info struct {
			apiResponse
			Channel struct {
				LastRead    string `json:"last_read"`
				UnreadCount int    `json:"unread_count_display"`
			} `json:"channel"`
		}
		if err := c.call(ctx, "conversations.info", url.Values{"channel": {conv.ID}}, &info); err != nil {
			return nil, err
		}
		if info.Channel.UnreadCount == 0 {
			return nil, nil
		}
		act := &Activity{
			Channel: c.label(ctx, conv),
			Kind:    "dm",
			Unread:  info.Channel.UnreadCount,
		}
		params := url.Values{"channel": {conv.ID}, "limit": {"5"}}
		if info.Channel.LastRead != "" {
			params.Set("oldest", info.Channel.LastRead)
		}
		c.fillMessages(ctx, params, act)
		return act, nil
	}

	// Canaux : Slack n'expose pas l'état de lecture, on regarde l'activité
	// récente sur la fenêtre demandée.
	oldest := time.Now().Add(-window)
	params := url.Values{
		"channel": {conv.ID},
		"limit":   {"5"},
		"oldest":  {fmt.Sprintf("%d", oldest.Unix())},
	}
	act := &Activity{Channel: c.label(ctx, conv), Kind: "canal"}
	if err := c.fillMessages(ctx, params, act); err != nil {
		return nil, err
	}
	if len(act.Messages) == 0 {
		return nil, nil
	}
	act.Recent = len(act.Messages)
	return act, nil
}

func (c *Client) fillMessages(ctx context.Context, params url.Values, act *Activity) error {
	var hist struct {
		apiResponse
		Messages []struct {
			User    string `json:"user"`
			BotID   string `json:"bot_id"`
			Text    string `json:"text"`
			Subtype string `json:"subtype"`
			TS      string `json:"ts"`
		} `json:"messages"`
	}
	if err := c.call(ctx, "conversations.history", params, &hist); err != nil {
		return err
	}
	for _, m := range hist.Messages {
		// Les entrées/sorties de canal et autres événements système ne sont pas
		// des messages à lire.
		if m.Subtype != "" || strings.TrimSpace(m.Text) == "" {
			continue
		}
		author := "un bot"
		if m.User != "" {
			author = c.userName(ctx, m.User)
		}
		act.Messages = append(act.Messages, fmt.Sprintf("%s : %s", author, truncate(c.renderText(ctx, m.Text), 180)))
		if ts := parseSlackTS(m.TS); ts.After(act.Latest) {
			act.Latest = ts
		}
	}
	return nil
}

func (c *Client) label(ctx context.Context, conv conversation) string {
	switch {
	case conv.IsIM:
		return "DM " + c.userName(ctx, conv.User)
	case conv.IsMPIM:
		return "Groupe " + c.groupLabel(ctx, conv.Name)
	default:
		return "#" + conv.Name
	}
}

// groupLabel rend prononçable le nom interne d'un DM de groupe. Slack le
// nomme « mpdm-mathias--olivier--jean-1 » : c'est un identifiant, pas quelque
// chose qu'on annonce à voix haute.
func (c *Client) groupLabel(ctx context.Context, raw string) string {
	if !strings.HasPrefix(raw, "mpdm-") {
		return raw
	}
	body := strings.TrimSuffix(strings.TrimPrefix(raw, "mpdm-"), "-1")
	self := c.selfHandle(ctx)

	names := make([]string, 0, 4)
	for _, handle := range strings.Split(body, "--") {
		// L'utilisateur fait partie du groupe, mais se citer lui-même dans le
		// nom de la conversation n'apprend rien.
		if handle == "" || (self != "" && handle == self) {
			continue
		}
		names = append(names, firstName(handle))
	}
	switch len(names) {
	case 0:
		return raw
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " et " + names[len(names)-1]
	}
}

// selfHandle renvoie le pseudo Slack de l'utilisateur courant, résolu une fois
// par client. Une chaîne vide en cas d'échec : c'est un confort d'affichage,
// pas une donnée dont dépend une réponse.
func (c *Client) selfHandle(ctx context.Context) string {
	c.mu.Lock()
	if c.selfResolved {
		defer c.mu.Unlock()
		return c.self
	}
	c.mu.Unlock()

	_, user, err := c.TestConnection(ctx)
	if err != nil {
		return ""
	}
	c.mu.Lock()
	c.self, c.selfResolved = user, true
	c.mu.Unlock()
	return user
}

func (c *Client) userName(ctx context.Context, id string) string {
	if id == "" {
		return "quelqu'un"
	}
	c.mu.Lock()
	if n, ok := c.userNames[id]; ok {
		c.mu.Unlock()
		return n
	}
	c.mu.Unlock()

	var res struct {
		apiResponse
		User struct {
			RealName string `json:"real_name"`
			Name     string `json:"name"`
			Profile  struct {
				RealNameNormalized string `json:"real_name_normalized"`
				RealName           string `json:"real_name"`
				DisplayName        string `json:"display_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	var name string
	if err := c.call(ctx, "users.info", url.Values{"user": {id}}, &res); err == nil {
		// Du plus complet au moins complet : `real_name` au premier niveau est
		// souvent vide, et `name` n'est que le pseudo — d'où des prénoms seuls
		// là où le mail affiche « Prénom Nom ».
		for _, candidate := range []string{
			res.User.Profile.RealNameNormalized,
			res.User.Profile.RealName,
			res.User.RealName,
			res.User.Profile.DisplayName,
			res.User.Name,
		} {
			if strings.TrimSpace(candidate) != "" {
				name = candidate
				break
			}
		}
	}
	// Un identifiant brut ne doit jamais ressortir : « U5G2I82BU t'a écrit »
	// n'a aucun sens à l'oral. Et l'échec n'est pas mis en cache, il peut être
	// passager — au prochain passage on retentera d'avoir le prénom.
	if strings.TrimSpace(name) == "" {
		return "quelqu'un"
	}
	name = firstName(name)
	c.mu.Lock()
	c.userNames[id] = name
	c.mu.Unlock()
	return name
}

// firstName ne garde que le prénom.
//
// Sur Slack on parle de collègues qu'on nomme par leur prénom : « Olivier t'a
// écrit » sonne juste, « Olivier Dupont t'a écrit » sonne comme un rapport. Les
// mails gardent le nom complet, où l'expéditeur peut être un inconnu.
func firstName(full string) string {
	full = strings.TrimSpace(full)
	if full == "" {
		return full
	}
	if fields := strings.Fields(full); len(fields) > 1 {
		return fields[0]
	}
	// Pseudo du type « olivier.dupont » ou « olivier_dupont ».
	for _, sep := range []string{".", "_", "-"} {
		if head, _, found := strings.Cut(full, sep); found && head != "" {
			full = head
			break
		}
	}
	// Un pseudo tout en minuscules se prononce mieux avec une capitale.
	r := []rune(full)
	return string(unicode.ToUpper(r[0])) + string(r[1:])
}

type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (a apiResponse) status() (bool, string) { return a.OK, a.Error }

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	endpoint := apiBase + method + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s : %w", method, err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("slack %s : réponse illisible : %w", method, err)
	}
	if r, ok := out.(interface{ status() (bool, string) }); ok {
		if ok2, e := r.status(); !ok2 {
			return fmt.Errorf("slack %s : %s", method, e)
		}
	}
	return nil
}

func parseSlackTS(ts string) time.Time {
	sec, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
