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

// Message est la vue d'un mail donnée à l'assistant. Body n'est rempli que par
// Read : la liste des non-lus ne rapatrie que les enveloppes, c'est ce qui la
// garde rapide.
type Message struct {
	Subject string `json:"subject"`
	From    string `json:"from"`
	// FromAddr est l'adresse brute. Elle ne sert pas à l'affichage mais à
	// distinguer deux expéditeurs qui portent le même prénom : « Cyril » et
	// « Cyril » ne se séparent que par leur adresse.
	FromAddr string    `json:"-"`
	Date     time.Time `json:"date"`
	Body     string    `json:"body,omitempty"`
	// To et Cc disent à qui le mail était adressé. Répondre sans le savoir,
	// c'est écrire « Bonjour Cyril » à un message envoyé à cinq personnes.
	To []string `json:"to,omitempty"`
	Cc []string `json:"cc,omitempty"`
	// InReplyTo : le Message-ID du message auquel celui-ci répond, tel que
	// l'enveloppe IMAP le donne. C'est le seul lien de filiation fiable entre
	// deux mails — un « Re: » dans l'objet se tape à la main.
	InReplyTo []string `json:"-"`
	// AnswersYou : ce mail répond à un message que l'utilisateur a envoyé.
	// Établi en rapprochant InReplyTo des Message-ID de sa boîte d'envoi.
	AnswersYou bool `json:"-"`
	// Bulk dit que le message porte les en-têtes d'un envoi de masse ou
	// automatique. C'est un fait posé par l'expéditeur lui-même dans le
	// message, pas une devinette sur son contenu — d'où sa présence ici, à
	// côté des autres champs lus tels quels.
	Bulk bool `json:"-"`
	// Thread : les messages antérieurs de la conversation, du plus récent au
	// plus ancien. Ils viennent d'abord du fil que le mail recopie lui-même,
	// et à défaut d'une recherche dans la boîte.
	Thread []ThreadMessage `json:"-"`
}

// ThreadMessage est un message antérieur de la conversation, rendu court : il
// sert à situer l'échange, pas à être lu.
type ThreadMessage struct {
	From string
	// Date n'est renseignée que pour les messages retrouvés en IMAP, où le
	// serveur donne un vrai horodatage.
	Date time.Time
	// Sent est l'en-tête de date recopié par le client mail, tel quel. On ne
	// cherche pas à l'analyser : « Wednesday, August 19, 2026 3:10 PM » et
	// « mercredi 19 août 2026 15:10 » dépendent de la langue d'Outlook, et une
	// date mal interprétée est pire qu'une date brute.
	Sent    string
	To      string
	Subject string
	Excerpt string
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

	// Avant INBOX : Select change de boîte, donc on relève les envois d'abord.
	// Au pire on n'a rien, et un mail qui répond à l'utilisateur redevient un
	// mail ordinaire — c'est une dégradation, pas une panne.
	sent := sentMessageIDs(c)

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

	// PEEK : la boîte est déjà ouverte en lecture seule, mais un serveur qui
	// l'ignorerait poserait \Seen sur des mails que personne n'a lus. Deux
	// verrous valent mieux qu'un quand la conséquence est de faire disparaître
	// des non-lus du vrai client mail.
	headers := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: bulkHeaders,
		Peek:         true,
	}
	msgs, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{
		Envelope:    true,
		Flags:       true,
		BodySection: []*imap.FetchItemBodySection{headers},
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
			Subject:    strings.TrimSpace(m.Envelope.Subject),
			From:       formatAddresses(m.Envelope.From),
			FromAddr:   firstAddress(m.Envelope.From),
			Date:       m.Envelope.Date,
			To:         addressList(m.Envelope.To),
			Cc:         addressList(m.Envelope.Cc),
			InReplyTo:  m.Envelope.InReplyTo,
			AnswersYou: answersYou(m.Envelope.InReplyTo, sent),
			Bulk:       isBulk(m.FindBodySection(headers)),
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

// firstAddress rend l'adresse du premier expéditeur, sans le nom affiché.
func firstAddress(addrs []imap.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0].Addr()
}

// addressList rend chaque destinataire séparément, nom et adresse ensemble :
// c'est ce qui permet de reconnaître l'utilisateur parmi eux, et de choisir le
// bon prénom pour la salutation.
func addressList(addrs []imap.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		addr := a.Addr()
		switch {
		case a.Name == "" && addr == "":
			continue
		case a.Name == "" || strings.EqualFold(a.Name, addr):
			out = append(out, addr)
		default:
			out = append(out, a.Name+" <"+addr+">")
		}
	}
	return out
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
