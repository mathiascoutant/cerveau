package gandi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
)

// Le fil complet d'une conversation, reconstitué depuis la boîte.
//
// Le corps d'un mail recopie souvent l'historique, mais pas toujours, et
// jamais au-delà de ce que l'expéditeur a gardé : une réponse tapée sur un
// téléphone coupe la citation, et quelqu'un qu'on ajoute en copie au troisième
// message ne voit que ce qu'on veut bien lui recopier. Or « est-ce que ça me
// concerne, où on en est » ne se tranche qu'avec le début — qui a lancé le
// sujet, ce qu'il demandait, à qui — et avec la fin : ce qui a été répondu
// APRÈS le mail qu'on lit, y compris par l'utilisateur lui-même.
//
// Les mails, eux, se tiennent par leurs en-têtes. References énumère les
// messages auxquels celui-ci fait suite, du premier au dernier ; In-Reply-To
// nomme son parent. Une seule recherche par boîte suffit à retrouver toute la
// conversation : les ancêtres par leur Message-ID, et tout le reste — les
// réponses, les réponses aux réponses, les branches parallèles — parce que
// chacun de ces messages cite le premier dans ses propres References. On la
// fait dans la réception ET dans les envoyés : c'est là que sont ses réponses.

// Bornes de la reconstitution. Douze messages avant couvrent un fil de projet
// — au-delà, le début n'apporte rien que le premier message ne dise déjà — et
// six après suffisent à dire où l'affaire en est.
const (
	maxBefore = 12
	maxAfter  = 6
	// maxThreadFetch : corps rapatriés par boîte. Au-delà, on lit d'abord
	// les enveloppes pour choisir lesquels valent le détour.
	maxThreadFetch = 16
	// maxThreadTotal : longueur cumulée des extraits du fil. Un long fil ne
	// doit pas noyer le mail qu'on lit sous ses prédécesseurs.
	maxThreadTotal = 24000
	// middleRunes : longueur d'un message du milieu quand le fil déborde.
	// Le premier (le sujet) et les derniers (l'état) restent entiers.
	middleRunes = 700
)

// filiation : ce qui rattache un message aux autres, tel que ses en-têtes le
// disent. Les identifiants sont normalisés (sans chevrons, en minuscules).
type filiation struct {
	messageID  string
	inReplyTo  []string
	references []string
}

// msgIDRE extrait les identifiants d'un en-tête. Une expression plutôt qu'un
// analyseur strict : References est l'en-tête le plus maltraité qui soit
// (commentaires, virgules, identifiants sans domaine), et un identifiant
// perdu coupe le fil là où il passait.
var msgIDRE = regexp.MustCompile(`<([^<>\s]+)>`)

// readFiliation lit les en-têtes de filiation d'un message brut.
func readFiliation(raw []byte) filiation {
	if len(raw) == 0 {
		return filiation{}
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return filiation{}
	}
	f := filiation{
		inReplyTo:  msgIDs(h.Get("In-Reply-To")),
		references: msgIDs(h.Get("References")),
	}
	if ids := msgIDs(h.Get("Message-Id")); len(ids) > 0 {
		f.messageID = ids[0]
	}
	return f
}

func msgIDs(v string) []string {
	var out []string
	for _, m := range msgIDRE.FindAllStringSubmatch(v, -1) {
		if id := normalizeMessageID(m[1]); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// filiationOf rassemble ce que l'on sait d'un message : ses en-têtes bruts
// quand on les a lus, son enveloppe sinon.
func filiationOf(msg Message, raw filiation) filiation {
	f := raw
	if f.messageID == "" {
		f.messageID = msg.MessageID
	}
	if len(f.inReplyTo) == 0 {
		for _, id := range msg.InReplyTo {
			if id = normalizeMessageID(id); id != "" {
				f.inReplyTo = append(f.inReplyTo, id)
			}
		}
	}
	return f
}

// parents : le message auquel celui-ci répond. In-Reply-To le nomme ; à
// défaut, c'est le dernier de References.
func (f filiation) parents() []string {
	if len(f.inReplyTo) > 0 {
		return f.inReplyTo
	}
	if n := len(f.references); n > 0 {
		return f.references[n-1:]
	}
	return nil
}

// root : le premier message de la conversation, celui qui a posé le sujet.
// Vide quand ce message-ci ouvre la conversation.
func (f filiation) root() string {
	if len(f.references) > 0 {
		return f.references[0]
	}
	if len(f.inReplyTo) > 0 {
		return f.inReplyTo[0]
	}
	return ""
}

// ancestors : les identifiants des messages qui précèdent, du parent au
// premier. Le premier reste dans la liste même quand on la coupe : c'est lui
// qui dit de quoi il retourne.
func (f filiation) ancestors() []string {
	seen := map[string]bool{"": true, f.messageID: true}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range f.parents() {
		add(id)
	}
	for i := len(f.references) - 1; i >= 0; i-- {
		add(f.references[i])
	}
	if len(out) > maxBefore {
		root := f.root()
		out = out[:maxBefore-1]
		if root != "" && !slices.Contains(out, root) {
			out = append(out, root)
		}
	}
	return out
}

// threadCriteria : « tout message de cette conversation », en une recherche.
func threadCriteria(f filiation) *imap.SearchCriteria {
	var leaves []imap.SearchCriteria
	header := func(key, id string) {
		// L'en-tête se cherche avec ses chevrons : c'est la forme sous
		// laquelle il a été écrit. La recherche IMAP ignore la casse.
		leaves = append(leaves, imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: "<" + id + ">"}},
		})
	}
	for _, id := range f.ancestors() {
		header("Message-Id", id)
	}
	if root := f.root(); root != "" {
		header("References", root)
	}
	if f.messageID != "" {
		header("References", f.messageID)
		header("In-Reply-To", f.messageID)
	}
	return anyOf(leaves)
}

// anyOf combine des critères en OU. IMAP ne connaît que le OU à deux branches :
// on construit un arbre équilibré plutôt qu'un peigne, certains serveurs
// limitant la profondeur d'imbrication.
func anyOf(list []imap.SearchCriteria) *imap.SearchCriteria {
	switch len(list) {
	case 0:
		return nil
	case 1:
		return &list[0]
	}
	mid := len(list) / 2
	left, right := anyOf(list[:mid]), anyOf(list[mid:])
	return &imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{*left, *right}}}
}

// conversation retrouve, dans la réception et dans les envoyés, les autres
// messages de la conversation. Elle change de boîte sélectionnée : à appeler
// une fois la lecture du message lui-même terminée.
//
// Rend ce qui précède et ce qui a suivi, chacun dans l'ordre chronologique,
// et dit si ce mail répond à un message de l'utilisateur.
func conversation(c *imapclient.Client, f filiation, date time.Time, me string) (before, after []ThreadMessage, answersMe bool) {
	crit := threadCriteria(f)
	if crit == nil {
		return nil, nil, false
	}
	ancestors := f.ancestors()
	parents := f.parents()

	type box struct {
		name string
		sent bool
	}
	boxes := []box{{"INBOX", false}}
	if sent := sentMailbox(c); sent != "" {
		boxes = append(boxes, box{sent, true})
	}

	seen := map[string]bool{}
	if f.messageID != "" {
		seen[f.messageID] = true
	}
	sentSearched := false
	for _, b := range boxes {
		if _, err := c.Select(b.name, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			continue
		}
		res, err := c.Search(crit, &imap.SearchOptions{ReturnAll: true}).Wait()
		if err != nil {
			continue
		}
		if b.sent {
			sentSearched = true
		}
		for _, m := range fetchThread(c, res.AllSeqNums(), b.sent, me, ancestors) {
			if m.MessageID != "" {
				if seen[m.MessageID] {
					continue
				}
				seen[m.MessageID] = true
			}
			if m.Mine && slices.Contains(parents, m.MessageID) {
				answersMe = true
			}
			// Un ancêtre précède toujours, quelle que soit l'heure que son
			// expéditeur a mise à son horloge ; le reste se range par date.
			if slices.Contains(ancestors, m.MessageID) || (!date.IsZero() && m.Date.Before(date)) {
				before = append(before, m)
			} else {
				after = append(after, m)
			}
		}
	}
	// La recherche groupée a échoué dans les envoyés (serveur capricieux sur
	// les OU) : la question « répond-il à un des miens » vaut bien une
	// recherche ciblée de rattrapage.
	if !sentSearched && !answersMe {
		answersMe = answersSent(c, parents)
	}

	sortChrono(before)
	sortChrono(after)
	return capBefore(before, ancestors), lastN(after, maxAfter), answersMe
}

// fetchThread rapatrie enveloppes et corps (en PEEK : rien n'est marqué lu)
// des messages trouvés dans la boîte sélectionnée.
func fetchThread(c *imapclient.Client, nums []uint32, sentBox bool, me string, ancestors []string) []ThreadMessage {
	if len(nums) == 0 {
		return nil
	}
	if len(nums) > maxThreadFetch {
		nums = chooseThread(c, nums, ancestors)
	}
	section := &imap.FetchItemBodySection{Peek: true}
	fetched, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil
	}
	me = strings.ToLower(strings.TrimSpace(me))
	out := make([]ThreadMessage, 0, len(fetched))
	for _, f := range fetched {
		if f.Envelope == nil {
			continue
		}
		body, _ := extractParts(f.FindBodySection(section))
		addr := strings.ToLower(firstAddress(f.Envelope.From))
		out = append(out, ThreadMessage{
			From:      formatAddresses(f.Envelope.From),
			FromAddr:  addr,
			Date:      f.Envelope.Date,
			To:        strings.Join(addressList(f.Envelope.To), ", "),
			Cc:        strings.Join(addressList(f.Envelope.Cc), ", "),
			Subject:   strings.TrimSpace(f.Envelope.Subject),
			Excerpt:   truncateRunes(body, maxThreadRunes),
			MessageID: normalizeMessageID(f.Envelope.MessageID),
			Mine:      sentBox || (me != "" && addr == me),
		})
	}
	return out
}

// chooseThread choisit, dans une longue conversation, les messages dont on
// lira le corps : les ancêtres, le plus ancien (le sujet), et les plus
// récents (l'état de l'affaire). Les enveloppes seules coûtent peu.
func chooseThread(c *imapclient.Client, nums []uint32, ancestors []string) []uint32 {
	// Les numéros de séquence suivent l'ordre d'arrivée : la queue de la
	// liste est la partie la plus récente.
	if len(nums) > 200 {
		nums = nums[len(nums)-200:]
	}
	fetched, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{Envelope: true}).Collect()
	if err != nil || len(fetched) == 0 {
		return nums[len(nums)-maxThreadFetch:]
	}
	sort.SliceStable(fetched, func(i, j int) bool {
		return envelopeDate(fetched[i]).Before(envelopeDate(fetched[j]))
	})
	keep := map[uint32]bool{fetched[0].SeqNum: true}
	for _, f := range fetched {
		if f.Envelope != nil && slices.Contains(ancestors, normalizeMessageID(f.Envelope.MessageID)) {
			keep[f.SeqNum] = true
		}
	}
	for i := len(fetched) - 1; i >= 0 && len(keep) < maxThreadFetch; i-- {
		keep[fetched[i].SeqNum] = true
	}
	out := make([]uint32, 0, len(keep))
	for n := range keep {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func envelopeDate(f *imapclient.FetchMessageBuffer) time.Time {
	if f.Envelope == nil {
		return time.Time{}
	}
	return f.Envelope.Date
}

func sortChrono(list []ThreadMessage) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Date.Before(list[j].Date) })
}

// capBefore garde au plus maxBefore messages antérieurs : le premier, la
// lignée directe, puis les plus récents. L'ordre chronologique est conservé.
func capBefore(list []ThreadMessage, ancestors []string) []ThreadMessage {
	if len(list) <= maxBefore {
		return list
	}
	keep := make([]bool, len(list))
	kept := 1
	keep[0] = true
	for i, m := range list {
		if kept < maxBefore && !keep[i] && slices.Contains(ancestors, m.MessageID) {
			keep[i] = true
			kept++
		}
	}
	for i := len(list) - 1; i >= 0 && kept < maxBefore; i-- {
		if !keep[i] {
			keep[i] = true
			kept++
		}
	}
	out := make([]ThreadMessage, 0, maxBefore)
	for i, m := range list {
		if keep[i] {
			out = append(out, m)
		}
	}
	return out
}

func lastN(list []ThreadMessage, n int) []ThreadMessage {
	if len(list) > n {
		return list[len(list)-n:]
	}
	return list
}

// mergeThread assemble le fil antérieur, dans l'ordre chronologique.
//
// Les messages retrouvés dans la boîte font foi : vrai horodatage, expéditeur
// et destinataires exacts. L'historique recopié dans le corps complète ce
// qu'elle n'a pas — typiquement le début d'un échange auquel l'utilisateur a
// été ajouté en cours de route, et qui n'est donc jamais arrivé chez lui. Ces
// messages-là sont les plus anciens : ils passent devant. Un message cité qui
// est aussi dans la boîte est écarté, pour ne pas compter deux fois la même
// réponse.
func mergeThread(quoted []QuotedMessage, found []ThreadMessage) []ThreadMessage {
	var older []ThreadMessage
	// L'historique cité va du plus récent au plus ancien : on le remet à
	// l'endroit.
	for i := len(quoted) - 1; i >= 0; i-- {
		q := quoted[i]
		if slices.ContainsFunc(found, func(m ThreadMessage) bool { return sameText(q.Body, m.Excerpt) }) {
			continue
		}
		older = append(older, ThreadMessage{
			From: q.From, Sent: q.Sent, To: q.To, Cc: q.Cc, Subject: q.Subject, Excerpt: q.Body,
		})
	}
	return trimThread(append(older, found...))
}

// sameText dit si deux textes sont le même message, l'un retrouvé dans la
// boîte, l'autre recopié dans une citation. Les deux ne sont jamais identiques
// au caractère près — la version citée vient souvent du HTML, l'autre du texte
// brut — mais leur début, une fois la ponctuation et la casse aplanies, l'est.
func sameText(a, b string) bool {
	ka, kb := []rune(normalizeQuery(a)), []rune(normalizeQuery(b))
	n := min(len(ka), len(kb), 60)
	if n < 20 {
		return n > 0 && len(ka) == len(kb) && string(ka) == string(kb)
	}
	if string(ka[:n]) == string(kb[:n]) {
		return true
	}
	// Une formule d'appel ajoutée ou retirée décale le début : on cherche
	// alors le début de l'un dans les premières lignes de l'autre.
	head := func(r []rune, size int) string { return string(r[:min(len(r), size)]) }
	return strings.Contains(head(kb, 240), head(ka, 40)) || strings.Contains(head(ka, 240), head(kb, 40))
}

// trimThread tient le fil dans maxThreadTotal : le premier message et les deux
// derniers restent entiers, ceux du milieu sont raccourcis.
func trimThread(list []ThreadMessage) []ThreadMessage {
	total := 0
	for _, m := range list {
		total += len([]rune(m.Excerpt))
	}
	if total <= maxThreadTotal {
		return list
	}
	for i := 1; i < len(list)-2 && total > maxThreadTotal; i++ {
		before := len([]rune(list[i].Excerpt))
		list[i].Excerpt = truncateRunes(list[i].Excerpt, middleRunes)
		total -= before - len([]rune(list[i].Excerpt))
	}
	return list
}

// completeThread pose sur msg le fil complet — ce qui précède, retrouvé dans la
// boîte et complété par l'historique cité ; ce qui a suivi ; et s'il répond à
// l'utilisateur. Change de boîte sélectionnée. Rend faux quand la boîte n'a
// rien donné, pour que l'appelant puisse tenter mieux que rien.
func completeThread(c *imapclient.Client, msg *Message, raw filiation, quoted []QuotedMessage, me string) bool {
	f := filiationOf(*msg, raw)
	msg.MessageID = f.messageID
	msg.References = f.references
	before, after, answersMe := conversation(c, f, msg.Date, me)
	msg.Thread = mergeThread(quoted, before)
	msg.Followups = trimThread(after)
	msg.AnswersYou = answersMe
	return len(before) > 0 || len(after) > 0
}

// looksLikeReply : l'objet porte un « Re: » ou un « Fwd: ». Seule condition à
// laquelle on se permet de rapprocher des mails par leur objet, faute de mieux.
func looksLikeReply(subject string) bool {
	return replyPrefixRE.MatchString(subject)
}

// Nouveaux rend ce qui est arrivé après `after` dans une conversation —
// désignée par le Message-ID de son premier message — ou de la part de l'un
// des expéditeurs donnés, réception et envois compris, du plus ancien au plus
// récent.
//
// Enveloppes seules : c'est la question « quelque chose de neuf depuis que je
// l'ai lu ? », pas une lecture. Elle coûte une recherche par boîte, là où tout
// relire coûterait le fil entier et le travail de le comprendre.
func Nouveaux(ctx context.Context, creds Credentials, root string, from []string, after time.Time) ([]Found, error) {
	root = normalizeMessageID(root)
	var senders []string
	for _, f := range from {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" && !slices.Contains(senders, f) {
			senders = append(senders, f)
		}
	}
	if root == "" && len(senders) == 0 {
		return nil, nil
	}
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	header := func(key, value string) imap.SearchCriteria {
		return imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: value}}}
	}
	var thread []imap.SearchCriteria
	if root != "" {
		thread = []imap.SearchCriteria{header("Message-Id", "<"+root+">"), header("References", "<"+root+">")}
	}
	inbox := append([]imap.SearchCriteria{}, thread...)
	for _, f := range senders {
		inbox = append(inbox, header("From", f))
	}

	type box struct {
		name, label string
		leaves      []imap.SearchCriteria
	}
	boxes := []box{{"INBOX", FolderInbox, inbox}}
	if sent := sentMailbox(c); sent != "" && len(thread) > 0 {
		boxes = append(boxes, box{sent, FolderSent, thread})
	}

	// SINCE ne connaît que le jour, dans le fuseau du serveur : on cherche
	// large, et l'heure exacte se compare sur l'enveloppe.
	since := after.Add(-24 * time.Hour)
	var out []Found
	for _, b := range boxes {
		crit := anyOf(b.leaves)
		if crit == nil {
			continue
		}
		withDate := *crit
		withDate.Since = since
		if _, err := c.Select(b.name, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return nil, fmt.Errorf("impossible d'ouvrir %s : %w", b.name, err)
		}
		res, err := c.Search(&withDate, &imap.SearchOptions{ReturnAll: true}).Wait()
		if err != nil {
			return nil, fmt.Errorf("recherche des nouveautés : %w", err)
		}
		nums := res.AllSeqNums()
		if len(nums) == 0 {
			continue
		}
		list, err := fetchEnvelopes(c, nums, b.label)
		if err != nil {
			return nil, err
		}
		for _, f := range list {
			if f.Date.After(after) && !f.Draft {
				out = append(out, f)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}
