// Package slack lit les conversations non lues via l'API Slack, avec un token
// utilisateur (xoxp-…). Un bot token ne convient pas : seul un token utilisateur
// expose « ce que MOI je n'ai pas lu » (last_read / unread_count_display).
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
	"time"
)

const apiBase = "https://slack.com/api/"

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
	// cache des noms d'utilisateurs, pour éviter un users.info par message
	userNames map[string]string
}

func New(token string) *Client {
	return &Client{
		token:     token,
		http:      &http.Client{Timeout: 20 * time.Second},
		userNames: map[string]string{},
	}
}

// Unread est un fil de discussion contenant des messages non lus.
type Unread struct {
	Channel  string    `json:"channel"`
	IsDM     bool      `json:"is_dm"`
	Count    int       `json:"unread_count"`
	Latest   time.Time `json:"latest"`
	Messages []string  `json:"messages"`
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

// UnreadMessages parcourt les conversations de l'utilisateur et renvoie celles
// qui ont des messages non lus, la plus récente en premier.
func (c *Client) UnreadMessages(ctx context.Context, maxThreads int) ([]Unread, error) {
	if maxThreads <= 0 {
		maxThreads = 10
	}

	var convs struct {
		apiResponse
		Channels []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			IsIM   bool   `json:"is_im"`
			IsMPIM bool   `json:"is_mpim"`
			User   string `json:"user"`
		} `json:"channels"`
	}
	err := c.call(ctx, "users.conversations", url.Values{
		"types":            {"public_channel,private_channel,im,mpim"},
		"exclude_archived": {"true"},
		"limit":            {"200"},
	}, &convs)
	if err != nil {
		return nil, err
	}

	var out []Unread
	for _, conv := range convs.Channels {
		var info struct {
			apiResponse
			Channel struct {
				LastRead    string `json:"last_read"`
				UnreadCount int    `json:"unread_count_display"`
			} `json:"channel"`
		}
		if err := c.call(ctx, "conversations.info", url.Values{"channel": {conv.ID}}, &info); err != nil {
			continue // un canal illisible ne doit pas casser tout le bilan
		}
		if info.Channel.UnreadCount == 0 {
			continue
		}

		label := "#" + conv.Name
		if conv.IsIM {
			label = "DM " + c.userName(ctx, conv.User)
		} else if conv.IsMPIM {
			label = "Groupe " + conv.Name
		}

		u := Unread{Channel: label, IsDM: conv.IsIM || conv.IsMPIM, Count: info.Channel.UnreadCount}

		var hist struct {
			apiResponse
			Messages []struct {
				User string `json:"user"`
				Text string `json:"text"`
				TS   string `json:"ts"`
			} `json:"messages"`
		}
		params := url.Values{"channel": {conv.ID}, "limit": {"5"}}
		if info.Channel.LastRead != "" {
			params.Set("oldest", info.Channel.LastRead)
		}
		if err := c.call(ctx, "conversations.history", params, &hist); err == nil {
			for _, m := range hist.Messages {
				if strings.TrimSpace(m.Text) == "" {
					continue
				}
				u.Messages = append(u.Messages, fmt.Sprintf("%s : %s", c.userName(ctx, m.User), truncate(m.Text, 180)))
				if ts := parseSlackTS(m.TS); ts.After(u.Latest) {
					u.Latest = ts
				}
			}
		}
		out = append(out, u)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Latest.After(out[j].Latest) })
	if len(out) > maxThreads {
		out = out[:maxThreads]
	}
	return out, nil
}

func (c *Client) userName(ctx context.Context, id string) string {
	if id == "" {
		return "quelqu'un"
	}
	if n, ok := c.userNames[id]; ok {
		return n
	}
	var res struct {
		apiResponse
		User struct {
			RealName string `json:"real_name"`
			Name     string `json:"name"`
		} `json:"user"`
	}
	name := id
	if err := c.call(ctx, "users.info", url.Values{"user": {id}}, &res); err == nil {
		if res.User.RealName != "" {
			name = res.User.RealName
		} else if res.User.Name != "" {
			name = res.User.Name
		}
	}
	c.userNames[id] = name
	return name
}

type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

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
	// Toutes les réponses embarquent apiResponse : on relit ok/error par assertion.
	if r, ok := out.(interface{ status() (bool, string) }); ok {
		if ok2, e := r.status(); !ok2 {
			return fmt.Errorf("slack %s : %s", method, e)
		}
	}
	return nil
}

func (a apiResponse) status() (bool, string) { return a.OK, a.Error }

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
