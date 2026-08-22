package gandi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"

	// Enregistre les jeux de caractères hérités (ISO-8859-1, Windows-1252…).
	// Sans ça, un mail français un peu ancien revient en mojibake.
	_ "github.com/emersion/go-message/charset"
)

// Longueur maximale du corps rendu à l'assistant. Un mail se lit à voix haute :
// au-delà ce n'est plus une lecture, c'est une punition — le modèle résumera.
const maxBodyRunes = 4000

// Nombre de mails récents inspectés quand on cherche par expéditeur ou objet.
const searchDepth = 40

// Messages antérieurs du fil rapatriés en plus du mail demandé. Trois suffisent
// à savoir de quoi on parle et comment les gens s'appellent ; au-delà on paie
// des allers-retours IMAP pour du contexte que personne ne relira.
const maxThreadMessages = 3

// Longueur d'un message antérieur. Il situe la conversation, il ne se lit pas.
const maxThreadRunes = 600

// Longueur de l'historique cité par le mail lui-même. Plus généreux que les
// messages du fil : c'est souvent là que tient toute la conversation.
const maxQuotedRunes = 2000

// Read renvoie un mail avec son contenu.
//
// `query` est ce que l'utilisateur a dit : un expéditeur (« le mail d'Olivier »),
// un bout d'objet (« celui sur le devis »), ou rien pour prendre le plus récent.
//
// La boîte est ouverte en lecture seule et le corps récupéré en PEEK : consulter
// un mail par Raoul ne doit pas le marquer comme lu dans le vrai client.
func Read(ctx context.Context, creds Credentials, query string, unreadOnly bool) (Message, error) {
	c, err := dial(ctx, creds)
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	sel, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return Message{}, fmt.Errorf("impossible d'ouvrir INBOX : %w", err)
	}
	if sel.NumMessages == 0 {
		return Message{}, fmt.Errorf("la boîte de réception est vide")
	}

	nums, err := candidates(c, sel.NumMessages, unreadOnly)
	if err != nil {
		return Message{}, err
	}
	if len(nums) == 0 {
		return Message{}, fmt.Errorf("aucun mail non lu")
	}

	chosen, all, err := pick(c, nums, query)
	if err != nil {
		return Message{}, err
	}

	// PEEK : sans lui, le serveur poserait le drapeau \Seen.
	section := &imap.FetchItemBodySection{Peek: true}
	fetched, err := c.Fetch(imap.SeqSetNum(chosen.seqNum), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return Message{}, fmt.Errorf("lecture du mail : %w", err)
	}
	if len(fetched) == 0 {
		return Message{}, fmt.Errorf("mail introuvable")
	}

	msg := chosen.msg
	msg.Body, msg.Quoted = extractParts(fetched[0].FindBodySection(section))
	msg.Thread = readThread(c, all, chosen)
	return msg, nil
}

// readThread rapatrie les messages précédents de la même conversation.
//
// IMAP ne donne pas de fil : on rapproche les messages par leur objet dépouillé
// de ses « Re: ». C'est imparfait — deux « Facture » sans lien se retrouvent
// ensemble — mais l'inverse coûte plus cher : répondre sans savoir ce qui a été
// dit produit un mail poli et hors sujet.
//
// Les enveloppes sont déjà en main, seuls les corps se paient : on en prend
// trois au maximum, et un échec ici ne fait pas échouer la lecture du mail.
func readThread(c *imapclient.Client, all []candidate, chosen candidate) []ThreadMessage {
	subject := baseSubject(chosen.msg.Subject)
	if subject == "" {
		return nil
	}

	siblings := make([]candidate, 0, maxThreadMessages)
	for _, cand := range all {
		if cand.seqNum == chosen.seqNum || baseSubject(cand.msg.Subject) != subject {
			continue
		}
		siblings = append(siblings, cand)
		if len(siblings) == maxThreadMessages {
			break
		}
	}
	if len(siblings) == 0 {
		return nil
	}

	nums := make([]uint32, 0, len(siblings))
	for _, cand := range siblings {
		nums = append(nums, cand.seqNum)
	}
	section := &imap.FetchItemBodySection{Peek: true}
	fetched, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil
	}

	bodies := make(map[uint32]string, len(fetched))
	for _, f := range fetched {
		body, _ := extractParts(f.FindBodySection(section))
		bodies[f.SeqNum] = truncateRunes(body, maxThreadRunes)
	}

	out := make([]ThreadMessage, 0, len(siblings))
	for _, cand := range siblings {
		excerpt := bodies[cand.seqNum]
		if strings.TrimSpace(excerpt) == "" {
			continue
		}
		out = append(out, ThreadMessage{
			From:    describeSender(cand.msg),
			Date:    cand.msg.Date,
			Excerpt: excerpt,
		})
	}
	return out
}

// candidates renvoie les numéros de séquence à inspecter, du plus ancien au
// plus récent — l'ordre dans lequel IMAP les rend.
func candidates(c *imapclient.Client, total uint32, unreadOnly bool) ([]uint32, error) {
	if unreadOnly {
		found, err := c.Search(
			&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}},
			&imap.SearchOptions{ReturnAll: true},
		).Wait()
		if err != nil {
			return nil, fmt.Errorf("recherche des non lus : %w", err)
		}
		nums := found.AllSeqNums()
		if len(nums) > searchDepth {
			nums = nums[len(nums)-searchDepth:]
		}
		return nums, nil
	}

	// Les plus récents portent les plus grands numéros de séquence.
	first := uint32(1)
	if total > searchDepth {
		first = total - searchDepth + 1
	}
	nums := make([]uint32, 0, total-first+1)
	for n := first; n <= total; n++ {
		nums = append(nums, n)
	}
	return nums, nil
}

// AmbiguousSenderError signale que la recherche désigne plusieurs personnes.
//
// « le dernier mail de Cyril » n'a pas de réponse quand deux Cyril écrivent :
// en choisir un au hasard donne une réponse fausse avec l'aplomb d'une vraie.
// On remonte donc les candidats pour que Raoul demande lequel.
type AmbiguousSenderError struct {
	Query   string
	Senders []string
}

func (e *AmbiguousSenderError) Error() string {
	return fmt.Sprintf("plusieurs expéditeurs correspondent à %q : %s",
		e.Query, strings.Join(e.Senders, " ; "))
}

// Nombre d'expéditeurs proposés au choix. Au-delà, la question devient une
// liste qu'on ne peut pas écouter.
const maxAmbiguousSenders = 5

// pick choisit le mail visé parmi les candidats.
//
// L'appariement se fait ici plutôt qu'en SEARCH IMAP : la demande vient d'une
// dictée vocale, donc sans accents fiables ni casse, et SEARCH impose un jeu de
// caractères que tous les serveurs ne gèrent pas pareil.
//
// L'expéditeur prime sur l'objet : « le mail de Cyril » ne doit pas rendre un
// mail de quelqu'un d'autre dont l'objet contient « Cyril ».
// candidate est un mail de la fenêtre inspectée, gardé avec son numéro de
// séquence pour pouvoir en rapatrier le corps ensuite.
type candidate struct {
	msg    Message
	seqNum uint32
}

// pick rend le mail visé ET la fenêtre inspectée : les enveloppes coûtent un
// aller-retour, et c'est dans cette même liste qu'on retrouvera les autres
// messages du fil sans en payer un second.
func pick(c *imapclient.Client, nums []uint32, query string) (candidate, []candidate, error) {
	fetched, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{Envelope: true}).Collect()
	if err != nil {
		return candidate{}, nil, fmt.Errorf("lecture des en-têtes : %w", err)
	}

	list := make([]candidate, 0, len(fetched))
	for _, f := range fetched {
		if f.Envelope == nil {
			continue
		}
		list = append(list, candidate{
			msg: Message{
				Subject:  strings.TrimSpace(f.Envelope.Subject),
				From:     formatAddresses(f.Envelope.From),
				FromAddr: firstAddress(f.Envelope.From),
				Date:     f.Envelope.Date,
				To:       addressList(f.Envelope.To),
				Cc:       addressList(f.Envelope.Cc),
			},
			seqNum: f.SeqNum,
		})
	}
	if len(list) == 0 {
		return candidate{}, nil, fmt.Errorf("aucun mail lisible")
	}

	// Du plus récent au plus ancien : « mon dernier mail » est le premier, et
	// une recherche par nom doit trouver la correspondance la plus fraîche.
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}

	want := normalizeQuery(query)
	if want == "" {
		return list[0], list, nil
	}

	// Les expéditeurs distincts qui correspondent, dans l'ordre de fraîcheur.
	var senders []string
	seen := map[string]bool{}
	var first candidate
	for _, c := range list {
		if !strings.Contains(normalizeQuery(c.msg.From), want) &&
			!strings.Contains(normalizeQuery(c.msg.FromAddr), want) {
			continue
		}
		key := senderKey(c.msg)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(senders) == 0 {
			first = c
		}
		if len(senders) < maxAmbiguousSenders {
			senders = append(senders, describeSender(c.msg))
		}
	}

	switch {
	case len(senders) == 1:
		return first, list, nil
	case len(senders) > 1:
		return candidate{}, nil, &AmbiguousSenderError{Query: query, Senders: senders}
	}

	// Personne ne correspond : on retombe sur l'objet.
	for _, c := range list {
		if strings.Contains(normalizeQuery(c.msg.Subject), want) {
			return c, list, nil
		}
	}
	return candidate{}, nil, fmt.Errorf("aucun mail récent ne correspond à %q", query)
}

// senderKey identifie une personne. L'adresse fait foi quand elle existe : deux
// « Cyril » ne se distinguent que par elle.
func senderKey(m Message) string {
	if m.FromAddr != "" {
		return normalizeQuery(m.FromAddr)
	}
	return normalizeQuery(m.From)
}

// describeSender rend le candidat prononçable : « Cyril Martin (cyril@x.fr) ».
func describeSender(m Message) string {
	name := strings.TrimSpace(m.From)
	addr := strings.TrimSpace(m.FromAddr)
	switch {
	case name == "" && addr == "":
		return "expéditeur inconnu"
	case addr == "" || strings.EqualFold(name, addr):
		return name
	case name == "":
		return addr
	}
	return name + " (" + addr + ")"
}

// extractParts tire d'un mail brut ce qu'il dit, et ce qu'il cite.
//
// Ordre de préférence : text/plain, puis text/html dégradé en texte. Les pièces
// jointes sont ignorées — on ne les lit pas à voix haute.
func extractParts(raw []byte) (body, quoted string) {
	if len(raw) == 0 {
		return "", ""
	}
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		// Mail non conforme : mieux vaut rendre le brut tronqué que rien.
		return truncateRunes(collapse(string(raw)), maxBodyRunes), ""
	}
	defer mr.Close()

	var plain, html string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		header, ok := part.Header.(*mail.InlineHeader)
		if !ok {
			continue // pièce jointe
		}
		content, err := io.ReadAll(part.Body)
		if err != nil {
			continue
		}
		mediaType, _, _ := header.ContentType()
		switch mediaType {
		case "text/plain":
			if plain == "" {
				plain = string(content)
			}
		case "text/html":
			if html == "" {
				html = string(content)
			}
		}
	}

	text := plain
	if strings.TrimSpace(text) == "" {
		text = htmlToText(html)
	}
	body, quoted = splitQuotedReply(collapse(text))
	return truncateRunes(body, maxBodyRunes), truncateRunes(quoted, maxQuotedRunes)
}
