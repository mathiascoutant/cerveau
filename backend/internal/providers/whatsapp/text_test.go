package whatsapp

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

func TestContentReadsPlainAndExtendedText(t *testing.T) {
	plain := &waE2E.Message{Conversation: proto.String("  on se voit à 14h  ")}
	body, kind, _ := content(plain)
	if body != "on se voit à 14h" || kind != "texte" {
		t.Errorf("texte simple : %q / %q", body, kind)
	}

	extended := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("je relance Cyril demain"),
	}}
	if body, kind, _ := content(extended); body != "je relance Cyril demain" || kind != "texte" {
		t.Errorf("texte étendu : %q / %q", body, kind)
	}
}

// Une photo légendée ne doit pas perdre sa légende : c'est le seul texte
// qu'elle portait, et souvent tout ce qui compte.
func TestContentKeepsCaption(t *testing.T) {
	msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("le devis signé"),
	}}
	body, kind, _ := content(msg)
	if body != "[photo] le devis signé" {
		t.Errorf("légende : %q", body)
	}
	if kind != "image" {
		t.Errorf("nature : %q", kind)
	}
}

// Un vocal n'a pas de texte, mais « il t'a envoyé un vocal » est une
// information : le message ne doit pas disparaître de l'archive.
func TestContentNamesVoiceNote(t *testing.T) {
	msg := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}}
	if body, kind, _ := content(msg); body != "[message vocal]" || kind != "vocal" {
		t.Errorf("vocal : %q / %q", body, kind)
	}
}

// Un message de service (clé de chiffrement, protocole) n'a rien à archiver.
func TestContentIgnoresProtocolMessages(t *testing.T) {
	if body, _, _ := content(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}); body != "" {
		t.Errorf("message de protocole : %q", body)
	}
	if body, _, _ := content(nil); body != "" {
		t.Errorf("message vide : %q", body)
	}
}

// Les mentions comptent sous les deux adresses du compte : les groupes récents
// citent l'identifiant masqué, les anciens le numéro. N'en connaître qu'une
// revient à ne plus voir aucune mention.
func TestMentionsAcceptsBothIdentities(t *testing.T) {
	phone := types.JID{User: "33612345678", Server: types.DefaultUserServer}
	lid := types.JID{User: "180992345", Server: types.HiddenUserServer}

	byLID := &waE2E.ContextInfo{MentionedJID: []string{lid.String()}}
	if !mentions(byLID, phone, lid) {
		t.Error("mention par identifiant masqué non vue")
	}
	byPhone := &waE2E.ContextInfo{MentionedJID: []string{phone.String()}}
	if !mentions(byPhone, phone, lid) {
		t.Error("mention par numéro non vue")
	}
	someoneElse := &waE2E.ContextInfo{MentionedJID: []string{"33699999999@s.whatsapp.net"}}
	if mentions(someoneElse, phone, lid) {
		t.Error("quelqu'un d'autre cité ne doit pas compter")
	}
}

// Répondre à l'un de ses messages, c'est s'adresser à lui — même sans écrire
// son nom, et c'est fréquent dans un groupe de trente personnes.
func TestMentionsCountsRepliesToYou(t *testing.T) {
	me := types.JID{User: "33612345678", Server: types.DefaultUserServer}
	reply := &waE2E.ContextInfo{Participant: proto.String(me.String())}
	if !mentions(reply, me) {
		t.Error("une réponse à son message s'adresse à lui")
	}
}

func TestMentionsIgnoresNilContext(t *testing.T) {
	if mentions(nil, types.JID{User: "1", Server: types.DefaultUserServer}) {
		t.Error("pas de contexte, pas de mention")
	}
}
