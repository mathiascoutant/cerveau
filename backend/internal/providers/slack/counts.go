package slack

import (
	"context"
	"net/url"
)

// readState est l'état de lecture d'une conversation, tel que le connaît le
// client Slack officiel.
type readState struct {
	HasUnreads   bool
	MentionCount int
	LastRead     string
}

// clientCounts interroge `client.counts`, la méthode qu'utilise l'application
// Slack elle-même pour afficher ses pastilles de non-lus.
//
// Elle n'est PAS dans la référence publique : l'API Web documentée ne permet
// pas de lire l'état de lecture d'un canal (seul `conversations.mark` existe,
// et il écrit). Elle accepte un token utilisateur ordinaire et ne demande pas
// de scope supplémentaire, mais rien ne garantit sa stabilité — d'où le repli
// systématique sur l'activité récente en cas d'échec.
func (c *Client) clientCounts(ctx context.Context) (map[string]readState, error) {
	var res struct {
		apiResponse
		Channels []countEntry `json:"channels"`
		Groups   []countEntry `json:"groups"`
		MPIMs    []countEntry `json:"mpims"`
		IMs      []countEntry `json:"ims"`
	}
	err := c.call(ctx, "client.counts", url.Values{
		"thread_counts_by_channel": {"false"},
	}, &res)
	if err != nil {
		return nil, err
	}

	out := make(map[string]readState, 64)
	for _, group := range [][]countEntry{res.Channels, res.Groups, res.MPIMs, res.IMs} {
		for _, e := range group {
			out[e.ID] = readState{
				HasUnreads:   e.HasUnreads,
				MentionCount: e.MentionCount,
				LastRead:     e.LastRead,
			}
		}
	}
	return out, nil
}

type countEntry struct {
	ID           string `json:"id"`
	HasUnreads   bool   `json:"has_unreads"`
	MentionCount int    `json:"mention_count"`
	LastRead     string `json:"last_read"`
}
