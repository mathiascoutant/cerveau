package gandi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/mail"
)

// Les pièces jointes d'un mail reçu.
//
// La lecture des mails les ignore : on ne lit pas un Excel à voix haute. Mais
// le process CSP d'une airline part de l'Excel qu'un CSP envoie par mail ; il
// faut alors le rapatrier tel quel, pour pxsky. Le mail est retrouvé par son
// Message-ID, qui ne change pas quand le tri le range dans un dossier.

// maxPieceOctets : au-delà, le mail n'est pas rapatrié.
const maxPieceOctets = 40 << 20

// ErrPieceIntrouvable : le mail ou le fichier n'est plus dans la boîte.
var ErrPieceIntrouvable = errors.New("pièce jointe introuvable")

// PieceJointe rend le contenu du fichier `nom` joint au mail `messageID`,
// cherché dans la réception et les dossiers de mails reçus. Tout est lu en
// PEEK : le mail garde son état lu ou non lu.
func PieceJointe(ctx context.Context, creds Credentials, messageID, nom string) ([]byte, error) {
	id := normalizeMessageID(messageID)
	if id == "" || strings.TrimSpace(nom) == "" {
		return nil, ErrPieceIntrouvable
	}
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	for _, b := range boitesRecues(c) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, err := c.Select(b.serveur, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			continue
		}
		res, err := c.Search(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: "Message-Id", Value: "<" + id + ">"}},
		}, &imap.SearchOptions{ReturnAll: true}).Wait()
		if err != nil || len(res.AllSeqNums()) == 0 {
			continue
		}
		seq := res.AllSeqNums()[0]
		taille, err := c.Fetch(imap.SeqSetNum(seq), &imap.FetchOptions{RFC822Size: true}).Collect()
		if err == nil && len(taille) > 0 && taille[0].RFC822Size > maxPieceOctets {
			return nil, fmt.Errorf("mail trop lourd (%d Mo) pour être rapatrié", taille[0].RFC822Size>>20)
		}
		raw, ok := fetchRaw(c, seq)
		if !ok {
			return nil, fmt.Errorf("lecture du mail impossible dans %s", b.label)
		}
		return extrairePiece(raw, nom)
	}
	return nil, ErrPieceIntrouvable
}

// extrairePiece tire d'un mail brut le fichier joint qui porte ce nom.
func extrairePiece(raw []byte, nom string) ([]byte, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("mail illisible : %w", err)
	}
	defer mr.Close()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, ErrPieceIntrouvable
		}
		if err != nil {
			return nil, fmt.Errorf("mail illisible : %w", err)
		}
		var fichier string
		switch h := part.Header.(type) {
		case *mail.AttachmentHeader:
			fichier, _ = h.Filename()
		case *mail.InlineHeader:
			// Certains clients joignent un fichier « inline » : son nom est
			// alors dans le Content-Type.
			_, params, _ := h.ContentType()
			fichier = params["name"]
		}
		if strings.TrimSpace(fichier) == strings.TrimSpace(nom) {
			return io.ReadAll(io.LimitReader(part.Body, maxPieceOctets))
		}
	}
}

// AvecPieces rend les mails d'un dossier de mails reçus (« CSP »), arrivés
// depuis `depuis` et qui portent au moins une pièce jointe, les plus récents
// d'abord, au plus `limit`. Leur texte est lu quand le mail n'est pas trop
// lourd ; rien n'est marqué lu.
func AvecPieces(ctx context.Context, creds Credentials, dossier string, depuis time.Time, limit int) ([]Arrivee, error) {
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	serveur := ""
	for _, b := range boitesRecues(c) {
		if strings.EqualFold(b.label, dossier) || strings.EqualFold(b.serveur, dossier) {
			serveur = b.serveur
			break
		}
	}
	if serveur == "" {
		return nil, fmt.Errorf("dossier %s introuvable", dossier)
	}
	if _, err := c.Select(serveur, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("impossible d'ouvrir %s : %w", dossier, err)
	}
	res, err := c.Search(&imap.SearchCriteria{Since: depuis}, &imap.SearchOptions{ReturnAll: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("recherche dans %s : %w", dossier, err)
	}
	nums := res.AllSeqNums()
	if len(nums) == 0 {
		return nil, nil
	}
	msgs, err := c.Fetch(imap.SeqSetNum(nums...), &imap.FetchOptions{
		UID: true, Envelope: true, RFC822Size: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("lecture de %s : %w", dossier, err)
	}
	var out []Arrivee
	for i := len(msgs) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		m := msgs[i]
		pieces := piecesJointes(m.BodyStructure)
		if m.Envelope == nil || len(pieces) == 0 {
			continue
		}
		a := Arrivee{
			UID: uint32(m.UID),
			Message: Message{
				Subject: strings.TrimSpace(m.Envelope.Subject), From: formatAddresses(m.Envelope.From),
				FromAddr: firstAddress(m.Envelope.From), Date: m.Envelope.Date,
				MessageID: normalizeMessageID(m.Envelope.MessageID),
			},
			PiecesJointes: pieces,
		}
		if m.RFC822Size > 0 && m.RFC822Size <= maxArriveeOctets {
			if raw, ok := fetchRawUID(c, m.UID); ok {
				a.Body, _ = extractParts(raw)
			}
		}
		out = append(out, a)
	}
	return out, nil
}
