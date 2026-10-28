package gandi

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Le tri des mails : les nouveaux mails de la réception, et leur rangement
// dans un dossier de la boîte — celui que Thunderbird montre.
//
// On suit les arrivées par UID, pas par le drapeau « non lu » : un mail lu sur
// le téléphone avant la relève doit être trié quand même. L'UID ne vaut que
// pour une UIDVALIDITY donnée ; si le serveur la change, les anciens UID ne
// veulent plus rien dire et on repart de la fin de la boîte.

// maxArriveeOctets : au-delà, on ne rapatrie pas le mail entier pour en lire
// le texte — l'objet, l'expéditeur et le nom des pièces jointes suffisent à le
// trier, et un mail de 20 Mo ne vaut pas d'être téléchargé chaque minute.
const maxArriveeOctets = 3 << 20

// Arrivee : un mail arrivé dans la réception, avec de quoi le trier.
type Arrivee struct {
	UID uint32
	Message
	// PiecesJointes : le nom des fichiers joints (« planning 1126.xlsx »).
	PiecesJointes []string
}

// Releve : où en est la réception.
type Releve struct {
	UIDValidity uint32
	// DernierUID : le plus grand UID existant dans la boîte (UIDNEXT - 1).
	DernierUID uint32
}

// Arrivees rend les mails de la réception dont l'UID suit `apres`, du plus
// ancien au plus récent, au plus `limit`. Si `validity` n'est pas celle de la
// boîte (ou vaut 0, première relève), rien n'est rendu : à l'appelant de noter
// la relève et de repartir de là.
//
// La boîte est ouverte en lecture seule et tout est lu en PEEK : trier un mail
// ne doit pas le marquer comme lu.
func Arrivees(ctx context.Context, creds Credentials, validity, apres uint32, limit int) (Releve, []Arrivee, error) {
	c, err := dial(ctx, creds)
	if err != nil {
		return Releve{}, nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	sel, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return Releve{}, nil, fmt.Errorf("impossible d'ouvrir INBOX : %w", err)
	}
	rel := Releve{UIDValidity: sel.UIDValidity}
	if sel.UIDNext > 0 {
		rel.DernierUID = uint32(sel.UIDNext) - 1
	}
	if validity == 0 || validity != sel.UIDValidity || sel.NumMessages == 0 || rel.DernierUID <= apres {
		return rel, nil, nil
	}

	var set imap.UIDSet
	set.AddRange(imap.UID(apres+1), 0) // apres+1:*
	headers := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: bulkHeaders,
		Peek:         true,
	}
	msgs, err := c.Fetch(set, &imap.FetchOptions{
		UID:           true,
		Envelope:      true,
		RFC822Size:    true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imap.FetchItemBodySection{headers},
	}).Collect()
	if err != nil {
		return rel, nil, fmt.Errorf("lecture des arrivées : %w", err)
	}

	var out []Arrivee
	for _, m := range msgs {
		// « n:* » rend toujours au moins le dernier message, même si son UID
		// est plus petit que n.
		if uint32(m.UID) <= apres || m.Envelope == nil {
			continue
		}
		a := Arrivee{
			UID: uint32(m.UID),
			Message: Message{
				Subject:  strings.TrimSpace(m.Envelope.Subject),
				From:     formatAddresses(m.Envelope.From),
				FromAddr: firstAddress(m.Envelope.From),
				Date:     m.Envelope.Date,
				To:       addressList(m.Envelope.To),
				Cc:       addressList(m.Envelope.Cc),
				Bulk:     isBulk(m.FindBodySection(headers)),
				// Pour retrouver le mail une fois rangé : son UID change de
				// dossier, pas son Message-ID.
				MessageID: normalizeMessageID(m.Envelope.MessageID),
			},
			PiecesJointes: piecesJointes(m.BodyStructure),
		}
		if m.RFC822Size > 0 && m.RFC822Size <= maxArriveeOctets {
			if raw, ok := fetchRawUID(c, m.UID); ok {
				a.Body, _ = extractParts(raw)
			}
		}
		out = append(out, a)
	}
	// Les plus anciens d'abord : si la relève est bornée, on avance dans
	// l'ordre et la suivante reprend où celle-ci s'est arrêtée.
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return rel, out, nil
}

// piecesJointes : les noms de fichiers portés par les parties du message.
func piecesJointes(bs imap.BodyStructure) []string {
	if bs == nil {
		return nil
	}
	var out []string
	bs.Walk(func(_ []int, part imap.BodyStructure) bool {
		if single, ok := part.(*imap.BodyStructureSinglePart); ok {
			if name := strings.TrimSpace(single.Filename()); name != "" {
				out = append(out, name)
			}
		}
		return true
	})
	return out
}

func fetchRawUID(c *imapclient.Client, uid imap.UID) ([]byte, bool) {
	section := &imap.FetchItemBodySection{Peek: true}
	fetched, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil || len(fetched) == 0 {
		return nil, false
	}
	return fetched[0].FindBodySection(section), true
}

// Ranger déplace des mails de la réception dans des dossiers (nom du dossier
// → UID), en créant les dossiers qui manquent. Un dossier créé est aussi
// « abonné » : Thunderbird, par défaut, ne montre que ceux-là.
//
// Le déplacement garde les drapeaux : un mail non lu reste non lu dans son
// dossier. Si l'UIDVALIDITY a changé depuis la relève, rien n'est déplacé —
// les UID désigneraient d'autres mails.
func Ranger(ctx context.Context, creds Credentials, validity uint32, parDossier map[string][]uint32) error {
	c, err := dial(ctx, creds)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	boxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		return fmt.Errorf("liste des dossiers : %w", err)
	}
	noms := map[string]string{} // en minuscules → nom exact sur le serveur
	for _, b := range boxes {
		noms[strings.ToLower(b.Mailbox)] = b.Mailbox
	}

	sel, err := c.Select("INBOX", nil).Wait()
	if err != nil {
		return fmt.Errorf("impossible d'ouvrir INBOX : %w", err)
	}
	if sel.UIDValidity != validity {
		return fmt.Errorf("la boîte de réception a changé (UIDVALIDITY), tri reporté")
	}

	for dossier, uids := range parDossier {
		if len(uids) == 0 {
			continue
		}
		nom, err := assurerDossier(c, noms, dossier)
		if err != nil {
			return err
		}
		var set imap.UIDSet
		for _, u := range uids {
			set.AddNum(imap.UID(u))
		}
		if _, err := c.Move(set, nom).Wait(); err != nil {
			return fmt.Errorf("déplacement vers %s : %w", nom, err)
		}
	}
	return nil
}

// assurerDossier rend le nom exact du dossier sur le serveur, en le créant —
// et en l'abonnant, pour que Thunderbird le montre — s'il n'existe pas.
func assurerDossier(c *imapclient.Client, noms map[string]string, dossier string) (string, error) {
	if nom, ok := noms[strings.ToLower(dossier)]; ok {
		return nom, nil
	}
	if err := c.Create(dossier, nil).Wait(); err != nil {
		return "", fmt.Errorf("création du dossier %s : %w", dossier, err)
	}
	_ = c.Subscribe(dossier).Wait()
	noms[strings.ToLower(dossier)] = dossier
	return dossier, nil
}

// CreerDossiers crée les dossiers qui manquent, vides : ils sont là dans
// Thunderbird avant même le premier mail à y ranger.
func CreerDossiers(ctx context.Context, creds Credentials, dossiers []string) error {
	c, err := dial(ctx, creds)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()
	boxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		return fmt.Errorf("liste des dossiers : %w", err)
	}
	noms := map[string]string{}
	for _, b := range boxes {
		noms[strings.ToLower(b.Mailbox)] = b.Mailbox
	}
	for _, d := range dossiers {
		if _, err := assurerDossier(c, noms, d); err != nil {
			return err
		}
	}
	return nil
}
