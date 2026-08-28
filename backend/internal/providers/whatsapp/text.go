package whatsapp

import (
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// Longueur d'un extrait conservé. Au-delà, on garde le début : ce qui compte
// pour décider s'il faut ouvrir la conversation tient dans les premières lignes.
const maxBody = 400

// content lit un message chiffré et rend ce qu'il faut en garder : son texte,
// sa nature, et les personnes qu'il cite.
//
// Un message WhatsApp n'a pas de champ « texte » : selon qu'il porte une photo,
// une réponse, un document ou un simple mot, le contenu est ailleurs dans
// l'arbre. On ne descend que ce qui se lit à voix haute — le reste devient une
// mention de sa nature, parce qu'« il t'a envoyé un vocal » est une information,
// même quand on ne sait pas ce qu'il dit.
func content(msg *waE2E.Message) (body, kind string, ctx *waE2E.ContextInfo) {
	if msg == nil {
		return "", "", nil
	}
	switch {
	case msg.GetConversation() != "":
		return clean(msg.GetConversation()), "texte", nil

	case msg.GetExtendedTextMessage() != nil:
		m := msg.GetExtendedTextMessage()
		return clean(m.GetText()), "texte", m.GetContextInfo()

	case msg.GetImageMessage() != nil:
		m := msg.GetImageMessage()
		return withCaption("photo", m.GetCaption()), "image", m.GetContextInfo()

	case msg.GetVideoMessage() != nil:
		m := msg.GetVideoMessage()
		return withCaption("vidéo", m.GetCaption()), "vidéo", m.GetContextInfo()

	case msg.GetAudioMessage() != nil:
		m := msg.GetAudioMessage()
		label := "audio"
		if m.GetPTT() {
			label = "message vocal"
		}
		return "[" + label + "]", "vocal", m.GetContextInfo()

	case msg.GetDocumentMessage() != nil:
		m := msg.GetDocumentMessage()
		return withCaption("document "+m.GetFileName(), m.GetCaption()), "document", m.GetContextInfo()

	case msg.GetStickerMessage() != nil:
		return "[sticker]", "sticker", msg.GetStickerMessage().GetContextInfo()

	case msg.GetLocationMessage() != nil:
		m := msg.GetLocationMessage()
		return withCaption("position", m.GetName()), "position", m.GetContextInfo()

	case msg.GetLiveLocationMessage() != nil:
		return "[position en direct]", "position", msg.GetLiveLocationMessage().GetContextInfo()

	case msg.GetContactMessage() != nil:
		m := msg.GetContactMessage()
		return withCaption("contact", m.GetDisplayName()), "contact", m.GetContextInfo()

	case msg.GetPollCreationMessageV3() != nil:
		m := msg.GetPollCreationMessageV3()
		return withCaption("sondage", m.GetName()), "sondage", m.GetContextInfo()

	case msg.GetEventMessage() != nil:
		m := msg.GetEventMessage()
		return withCaption("événement", m.GetName()), "événement", m.GetContextInfo()
	}
	return "", "", nil
}

// withCaption assemble la nature du message et la légende qui l'accompagne :
// « [photo] Regarde ce qu'on a trouvé ». Sans la légende, une photo commentée
// perdrait le seul texte qu'elle portait.
func withCaption(label, caption string) string {
	caption = clean(caption)
	if caption == "" {
		return "[" + label + "]"
	}
	return "[" + label + "] " + caption
}

func clean(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= maxBody {
		return s
	}
	return string([]rune(s)[:maxBody]) + "…"
}

// mentions dit si l'utilisateur est interpellé par ce message.
//
// Deux façons de l'être, et elles se valent : être cité nommément (@Mathias,
// qui met son identifiant dans mentionedJid), ou recevoir une réponse à l'un de
// ses propres messages. La seconde compte autant que la première — dans un
// groupe de trente personnes, quelqu'un qui répond à ce qu'on a écrit s'adresse
// à nous, même sans écrire notre nom.
func mentions(ctx *waE2E.ContextInfo, me ...types.JID) bool {
	if ctx == nil {
		return false
	}
	for _, raw := range ctx.GetMentionedJID() {
		jid, err := types.ParseJID(raw)
		if err != nil {
			continue
		}
		if sameUser(jid, me...) {
			return true
		}
	}
	if raw := ctx.GetParticipant(); raw != "" {
		if jid, err := types.ParseJID(raw); err == nil && sameUser(jid, me...) {
			return true
		}
	}
	return false
}

// sameUser compare deux identités WhatsApp sans tenir compte de l'appareil.
//
// Un même compte porte plusieurs adresses : le numéro de téléphone
// (…@s.whatsapp.net) et l'identifiant masqué (…@lid) que les groupes utilisent
// désormais à sa place. Les deux nous désignent, et un message qui cite l'une
// ne cite pas l'autre : il faut donc comparer contre toutes celles qu'on se
// connaît, sinon les mentions en groupe passent inaperçues.
func sameUser(jid types.JID, candidates ...types.JID) bool {
	for _, c := range candidates {
		if c.IsEmpty() {
			continue
		}
		if jid.ToNonAD() == c.ToNonAD() {
			return true
		}
	}
	return false
}
