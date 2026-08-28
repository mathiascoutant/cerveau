package gandi

import (
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Savoir si un mail répond à un message qu'on a envoyé.
//
// C'est la question qui manquait le plus à Raoul, et elle ne se répond pas dans
// la boîte de réception : les messages envoyés n'y sont pas. Il faut aller lire
// la boîte d'envoi, y relever les Message-ID, et les rapprocher de l'en-tête
// In-Reply-To des mails reçus.
//
// Pourquoi les en-têtes et pas l'objet : un « Re: » se tape à la main, se
// traduit, se perd quand quelqu'un répond en changeant le sujet. Message-ID et
// In-Reply-To sont posés par les clients mail eux-mêmes et ne mentent pas. Le
// rapprochement est donc exact — soit ce mail répond à l'un des siens, soit il
// n'y répond pas.

// Profondeur de la boîte d'envoi. Cent messages couvrent plusieurs semaines
// d'échanges, et une réponse qui arrive après ce délai n'a plus rien d'une
// conversation en cours.
const sentDepth = 100

// Noms de boîte d'envoi, quand le serveur ne déclare pas d'attribut \Sent.
// L'ordre compte peu, la casse non plus : la comparaison est insensible.
var sentNames = []string{
	"Sent", "Sent Items", "Sent Messages", "Sent Mail",
	"Envoyés", "Envoyes", "Éléments envoyés", "Elements envoyes",
	"INBOX.Sent", "INBOX.Envoyés", "INBOX.Envoyes",
}

// sentMessageIDs relève les Message-ID des derniers messages envoyés.
//
// Best-effort de bout en bout : pas de boîte d'envoi, pas de droit de lecture,
// serveur qui refuse le LIST — on rend une carte vide, et les mails reçus
// redeviennent de simples mails reçus. Aucune erreur ne remonte : cette
// information enrichit le tri, elle ne le conditionne pas.
//
// ATTENTION : la fonction change de boîte sélectionnée. Elle doit être appelée
// avant de sélectionner INBOX, jamais au milieu d'une lecture.
func sentMessageIDs(c *imapclient.Client) map[string]bool {
	name := sentMailbox(c)
	if name == "" {
		return nil
	}
	sel, err := c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || sel.NumMessages == 0 {
		return nil
	}

	from := uint32(1)
	if sel.NumMessages > sentDepth {
		from = sel.NumMessages - sentDepth + 1
	}
	seq := imap.SeqSet{}
	seq.AddRange(from, sel.NumMessages)

	msgs, err := c.Fetch(seq, &imap.FetchOptions{Envelope: true}).Collect()
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Envelope == nil {
			continue
		}
		if id := normalizeMessageID(m.Envelope.MessageID); id != "" {
			out[id] = true
		}
	}
	return out
}

// sentMailbox retrouve la boîte d'envoi.
//
// L'attribut \Sent (RFC 6154) est la bonne façon de la désigner : il survit au
// renommage et à la langue de l'interface. Les noms connus ne servent que de
// repli, pour les serveurs qui ne l'annoncent pas.
func sentMailbox(c *imapclient.Client) string {
	boxes, err := c.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		// Serveur sans l'extension : le LIST étendu est refusé en bloc, on
		// redemande la version simple et on s'en remet aux noms connus.
		if boxes, err = c.List("", "*", nil).Collect(); err != nil {
			return ""
		}
	}

	for _, box := range boxes {
		if slices.Contains(box.Attrs, imap.MailboxAttrSent) {
			return box.Mailbox
		}
	}
	for _, want := range sentNames {
		for _, box := range boxes {
			if strings.EqualFold(box.Mailbox, want) {
				return box.Mailbox
			}
		}
	}
	return ""
}

// answersSent dit si l'un des messages cités se trouve dans la boîte d'envoi.
//
// Variante ciblée de sentMessageIDs, pour la lecture d'UN mail : une recherche
// par en-tête coûte un aller-retour et rend un booléen, là où relever cent
// Message-ID pour n'en comparer qu'un serait payer la liste entière pour une
// seule ligne. Sur un chemin vocal, cette différence s'entend.
//
// Comme sa sœur, elle change de boîte sélectionnée : à n'appeler qu'une fois la
// lecture d'INBOX terminée.
func answersSent(c *imapclient.Client, inReplyTo []string) bool {
	if len(inReplyTo) == 0 {
		return false
	}
	name := sentMailbox(c)
	if name == "" {
		return false
	}
	if _, err := c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return false
	}

	for _, id := range inReplyTo {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		// L'en-tête se cherche avec ses chevrons : c'est la forme sous laquelle
		// il a été écrit dans le message.
		if !strings.HasPrefix(id, "<") {
			id = "<" + id + ">"
		}
		found, err := c.Search(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Message-Id", Value: id}},
		}, &imap.SearchOptions{ReturnAll: true}).Wait()
		if err == nil && len(found.AllSeqNums()) > 0 {
			return true
		}
	}
	return false
}

// answersYou dit si l'un des messages cités est de l'utilisateur.
func answersYou(inReplyTo []string, sent map[string]bool) bool {
	if len(sent) == 0 {
		return false
	}
	for _, id := range inReplyTo {
		if sent[normalizeMessageID(id)] {
			return true
		}
	}
	return false
}

// normalizeMessageID enlève les chevrons et la casse. Les clients mail ne sont
// pas d'accord sur les premiers ; la RFC dit la partie domaine insensible à la
// seconde, et personne ne s'amuse à changer la casse d'un identifiant.
func normalizeMessageID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return strings.ToLower(id)
}
