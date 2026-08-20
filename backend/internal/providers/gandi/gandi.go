// Package gandi lit la boîte mail Gandi en IMAP.
//
// Gandi ne propose pas d'OAuth pour le mail : on utilise IMAP avec un mot de
// passe d'application (Gandi Admin > Boîte mail > Mots de passe d'application).
package gandi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const DefaultHost = "mail.gandi.net:993"

type Credentials struct {
	Email    string
	Password string
	Host     string
}

func (c Credentials) host() string {
	if c.Host == "" {
		return DefaultHost
	}
	return c.Host
}

// Message est la vue simplifiée d'un mail non lu qu'on donne à l'assistant.
type Message struct {
	Subject string    `json:"subject"`
	From    string    `json:"from"`
	Date    time.Time `json:"date"`
	Snippet string    `json:"snippet,omitempty"`
}

// TestConnection vérifie les identifiants sans rien lire d'autre.
func TestConnection(ctx context.Context, creds Credentials) error {
	c, err := dial(ctx, creds)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("impossible d'ouvrir INBOX : %w", err)
	}
	return nil
}

// Unread renvoie les mails non lus de INBOX, du plus récent au plus ancien.
func Unread(ctx context.Context, creds Credentials, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 15
	}
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("impossible d'ouvrir INBOX : %w", err)
	}

	criteria := &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}
	found, err := c.Search(criteria, &imap.SearchOptions{ReturnAll: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("recherche des non lus : %w", err)
	}
	nums := found.AllSeqNums()
	if len(nums) == 0 {
		return nil, nil
	}
	// Les plus récents sont en fin de liste : on ne récupère que la queue.
	if len(nums) > limit {
		nums = nums[len(nums)-limit:]
	}

	msgs, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{
		Envelope: true,
		Flags:    true,
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("lecture des mails : %w", err)
	}

	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Envelope == nil {
			continue
		}
		out = append(out, Message{
			Subject: strings.TrimSpace(m.Envelope.Subject),
			From:    formatAddresses(m.Envelope.From),
			Date:    m.Envelope.Date,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

// UnreadCount ne compte que, sans rapatrier les enveloppes.
func UnreadCount(ctx context.Context, creds Credentials) (int, error) {
	c, err := dial(ctx, creds)
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return 0, err
	}
	found, err := c.Search(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}},
		&imap.SearchOptions{ReturnAll: true}).Wait()
	if err != nil {
		return 0, err
	}
	return len(found.AllSeqNums()), nil
}

func dial(ctx context.Context, creds Credentials) (*imapclient.Client, error) {
	dialer := &imapclient.Options{}
	c, err := imapclient.DialTLS(creds.host(), dialer)
	if err != nil {
		return nil, fmt.Errorf("connexion à %s impossible : %w", creds.host(), err)
	}
	if err := c.Login(creds.Email, creds.Password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("identifiants Gandi refusés : %w", err)
	}
	return c, nil
}

func formatAddresses(addrs []imap.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Name != "" {
			parts = append(parts, a.Name)
			continue
		}
		parts = append(parts, a.Addr())
	}
	return strings.Join(parts, ", ")
}
