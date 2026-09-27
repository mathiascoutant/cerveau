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

// Recherche dans la boîte, envoyés compris.
//
// La lecture des non-lus répond à « qu'est-ce qui m'attend ». Elle ne répond pas
// à « qu'est-ce que Hebat m'a dit », ni à « c'est quoi le dernier mail que j'ai
// envoyé » : le premier demande de chercher parmi les mails lus, le second de
// lire la boîte d'envoi, qui n'est pas INBOX. Ce fichier tient ces deux gestes,
// avec une règle qui vaut pour tout ce qu'il rend : chaque message dit d'où il
// vient (Folder), qui l'a écrit, à qui, et porte son Message-ID — l'identifiant
// que les clients mail posent eux-mêmes, et le seul qui permette de rouvrir
// exactement ce message-là plutôt qu'un homonyme.

// Dossiers d'origine d'un message retrouvé.
const (
	FolderInbox = "reçu"
	FolderSent  = "envoyé"
)

// Profondeur d'une recherche : enveloppes inspectées par dossier. Cent cinquante
// messages couvrent plusieurs semaines sans transformer chaque question en
// téléchargement de la boîte entière.
const searchScan = 150

// SearchQuery cadre une recherche.
type SearchQuery struct {
	// Sender : personne cherchée, telle qu'elle a été dite (« Hebat »). Comparée
	// au nom affiché et à l'adresse. Dans la boîte d'envoi, c'est le
	// destinataire qui est comparé — « ce que j'ai répondu à Hebat ».
	Sender string
	// Subject : fragment d'objet, facultatif.
	Subject string
	// Since : ne garder que ce qui est postérieur. Zéro = pas de borne.
	Since time.Time
	// Folders : FolderInbox, FolderSent, ou les deux. Vide = INBOX seule.
	Folders []string
	Limit   int
}

// Found est un message retrouvé, avec ce qu'il faut pour le situer sans l'ouvrir.
type Found struct {
	Message
	// Folder : d'où il vient. Un mail de la boîte d'envoi est un mail que
	// l'utilisateur a écrit — le champ De y porte sa propre adresse.
	Folder string
	// MessageID normalisé (sans chevrons), pour ReadByID.
	MessageID string
	// Seen : lu ou non, tel que le serveur le tient.
	Seen bool
	// Draft : porte le drapeau \Draft. Un brouillon n'est pas un mail envoyé,
	// même s'il traîne dans la boîte d'envoi de certains clients.
	Draft bool

	seq uint32
}

// Search cherche par personne et par objet, dans les dossiers demandés, du plus
// récent au plus ancien.
func Search(ctx context.Context, creds Credentials, q SearchQuery) ([]Found, error) {
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	if q.Limit <= 0 {
		q.Limit = 10
	}
	if len(q.Folders) == 0 {
		q.Folders = []string{FolderInbox}
	}

	var out []Found
	for _, folder := range q.Folders {
		name := "INBOX"
		if folder == FolderSent {
			name = sentMailbox(c)
			if name == "" {
				continue
			}
		}
		found, err := scanFolder(c, name, folder, q)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Sent rend les derniers messages envoyés, du plus récent au plus ancien.
//
// `to` filtre sur le destinataire (nom ou adresse), `subject` sur l'objet. Le
// corps du plus récent est rapatrié si withBody est vrai : « c'est quoi le
// dernier mail que j'ai envoyé » attend le contenu, pas l'enveloppe.
func Sent(ctx context.Context, creds Credentials, to, subject string, limit int, withBody bool) ([]Found, error) {
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	name := sentMailbox(c)
	if name == "" {
		return nil, fmt.Errorf("aucune boîte d'envoi trouvée sur ce compte")
	}
	if limit <= 0 {
		limit = 5
	}
	found, err := scanFolder(c, name, FolderSent, SearchQuery{Sender: to, Subject: subject, Limit: limit})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Date.After(found[j].Date) })
	if len(found) > limit {
		found = found[:limit]
	}
	if withBody && len(found) > 0 {
		// La boîte d'envoi est encore sélectionnée : scanFolder ne change pas
		// de boîte après son Fetch.
		if body, ok := fetchBody(c, found[0].seq); ok {
			found[0].Body = body
		}
	}
	return found, nil
}

// ReadByID ouvre exactement le message dont on a le Message-ID, où qu'il soit.
//
// C'est ce qui rend une recherche rigoureuse : Search rend des identifiants,
// et l'ouverture se fait sur l'identifiant — jamais sur un nom que deux
// personnes peuvent porter.
func ReadByID(ctx context.Context, creds Credentials, messageID string) (Found, error) {
	id := normalizeMessageID(messageID)
	if id == "" {
		return Found{}, fmt.Errorf("identifiant de message vide")
	}
	c, err := dial(ctx, creds)
	if err != nil {
		return Found{}, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	folders := []struct{ box, label string }{{"INBOX", FolderInbox}}
	if sent := sentMailbox(c); sent != "" {
		folders = append(folders, struct{ box, label string }{sent, FolderSent})
	}
	for _, f := range folders {
		if _, err := c.Select(f.box, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			continue
		}
		res, err := c.Search(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Message-Id", Value: "<" + id + ">"}},
		}, &imap.SearchOptions{ReturnAll: true}).Wait()
		if err != nil {
			continue
		}
		nums := res.AllSeqNums()
		if len(nums) == 0 {
			continue
		}
		list, err := fetchEnvelopes(c, nums, f.label)
		if err != nil || len(list) == 0 {
			continue
		}
		found := list[0]
		if body, ok := fetchBody(c, found.seq); ok {
			found.Body = body
		}
		return found, nil
	}
	return Found{}, fmt.Errorf("aucun message ne porte l'identifiant %q", messageID)
}

// scanFolder inspecte les dernières enveloppes d'une boîte et garde celles qui
// répondent à la recherche. La boîte reste sélectionnée au retour.
func scanFolder(c *imapclient.Client, box, label string, q SearchQuery) ([]Found, error) {
	sel, err := c.Select(box, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("impossible d'ouvrir %s : %w", box, err)
	}
	if sel.NumMessages == 0 {
		return nil, nil
	}
	first := uint32(1)
	if sel.NumMessages > searchScan {
		first = sel.NumMessages - searchScan + 1
	}
	nums := make([]uint32, 0, sel.NumMessages-first+1)
	for n := first; n <= sel.NumMessages; n++ {
		nums = append(nums, n)
	}
	list, err := fetchEnvelopes(c, nums, label)
	if err != nil {
		return nil, err
	}

	who := normalizeQuery(q.Sender)
	subj := normalizeQuery(q.Subject)
	var out []Found
	for _, f := range list {
		if !q.Since.IsZero() && f.Date.Before(q.Since) {
			continue
		}
		if who != "" && !matchesPerson(f, who) {
			continue
		}
		if subj != "" && !strings.Contains(normalizeQuery(f.Subject), subj) {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// matchesPerson dit si la personne cherchée est l'expéditeur (boîte de
// réception) ou l'un des destinataires (boîte d'envoi) du message.
func matchesPerson(f Found, want string) bool {
	if f.Folder == FolderSent {
		for _, to := range append(append([]string{}, f.To...), f.Cc...) {
			if strings.Contains(normalizeQuery(to), want) {
				return true
			}
		}
		return false
	}
	return strings.Contains(normalizeQuery(f.From), want) ||
		strings.Contains(normalizeQuery(f.FromAddr), want)
}

func fetchEnvelopes(c *imapclient.Client, nums []uint32, label string) ([]Found, error) {
	fetched, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{Envelope: true, Flags: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("lecture des en-têtes : %w", err)
	}
	out := make([]Found, 0, len(fetched))
	for _, m := range fetched {
		if m.Envelope == nil {
			continue
		}
		f := Found{
			Message: Message{
				Subject:   strings.TrimSpace(m.Envelope.Subject),
				From:      formatAddresses(m.Envelope.From),
				FromAddr:  firstAddress(m.Envelope.From),
				Date:      m.Envelope.Date,
				To:        addressList(m.Envelope.To),
				Cc:        addressList(m.Envelope.Cc),
				InReplyTo: m.Envelope.InReplyTo,
			},
			Folder:    label,
			MessageID: normalizeMessageID(m.Envelope.MessageID),
			seq:       m.SeqNum,
		}
		for _, flag := range m.Flags {
			switch flag {
			case imap.FlagSeen:
				f.Seen = true
			case imap.FlagDraft:
				f.Draft = true
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// fetchBody rapatrie le corps d'un message de la boîte sélectionnée, en PEEK.
func fetchBody(c *imapclient.Client, seq uint32) (string, bool) {
	section := &imap.FetchItemBodySection{Peek: true}
	fetched, err := c.Fetch(imap.SeqSetNum(seq), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil || len(fetched) == 0 {
		return "", false
	}
	body, _ := extractParts(fetched[0].FindBodySection(section))
	return body, true
}
