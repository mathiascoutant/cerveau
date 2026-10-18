package gandi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestReadFiliation(t *testing.T) {
	raw := []byte("Message-ID: <C1@X.fr>\r\n" +
		"In-Reply-To: <m1@pxcom.aero>\r\n" +
		"References: <root@x.fr> (commentaire)\r\n <m1@pxcom.aero>,<m1@pxcom.aero>\r\n" +
		"Subject: Re: Projet\r\n\r\ncorps\r\n")
	f := readFiliation(raw)
	if f.messageID != "c1@x.fr" {
		t.Errorf("message-id = %q", f.messageID)
	}
	if strings.Join(f.inReplyTo, ",") != "m1@pxcom.aero" {
		t.Errorf("in-reply-to = %v", f.inReplyTo)
	}
	// Repliée sur deux lignes, avec un commentaire et un doublon : la lignée
	// doit sortir propre, dans l'ordre.
	if strings.Join(f.references, ",") != "root@x.fr,m1@pxcom.aero" {
		t.Errorf("references = %v", f.references)
	}
	if f.root() != "root@x.fr" {
		t.Errorf("root = %q", f.root())
	}
}

// Une lignée trop longue est coupée, mais jamais avant d'avoir gardé le premier
// message : c'est lui qui dit de quoi il s'agit.
func TestAncestorsKeepParentAndRoot(t *testing.T) {
	f := filiation{messageID: "self"}
	for i := 0; i < 20; i++ {
		f.references = append(f.references, fmt.Sprintf("r%d", i))
	}
	got := f.ancestors()
	if len(got) != maxBefore {
		t.Fatalf("%d ancêtres, attendu %d", len(got), maxBefore)
	}
	if got[0] != "r19" {
		t.Errorf("le parent doit venir en premier : %v", got)
	}
	if got[len(got)-1] != "r0" {
		t.Errorf("le premier message doit rester : %v", got)
	}
}

// L'historique cité complète la boîte sans la doubler : ce qui n'existe que
// recopié passe devant (c'est le plus ancien), ce que la boîte a déjà disparaît.
func TestMergeThreadDedupesQuotedCopies(t *testing.T) {
	found := []ThreadMessage{
		{From: "Mathias", Excerpt: "Je m'en occupe d'ici jeudi, je vous envoie la maquette.", Mine: true, MessageID: "m1"},
	}
	quoted := []QuotedMessage{
		// Du plus récent au plus ancien, comme dans un corps de mail.
		{From: "Le 3 mars, Mathias a écrit", Body: "Je m’en occupe d’ici jeudi, je vous envoie la maquette.\nLe 2 mars, Paul a écrit :\nIl faut livrer"},
		{From: "Le 1 mars, Sophie a écrit", Body: "Lancement du projet : on part sur la version courte."},
	}
	got := mergeThread(quoted, found)
	if len(got) != 2 {
		t.Fatalf("fil = %+v", got)
	}
	if got[0].From != "Le 1 mars, Sophie a écrit" || got[1].MessageID != "m1" {
		t.Errorf("ordre ou dédoublonnage faux : %+v", got)
	}
}

func TestTrimThreadKeepsEnds(t *testing.T) {
	long := strings.Repeat("mot ", 2000)
	list := []ThreadMessage{{Excerpt: long}, {Excerpt: long}, {Excerpt: long}, {Excerpt: long}, {Excerpt: long}}
	got := trimThread(list)
	if len([]rune(got[0].Excerpt)) != len([]rune(long)) || len([]rune(got[4].Excerpt)) != len([]rune(long)) {
		t.Error("le premier et le dernier message doivent rester entiers")
	}
	if len([]rune(got[1].Excerpt)) > middleRunes+1 {
		t.Errorf("un message du milieu aurait dû être raccourci (%d)", len([]rune(got[1].Excerpt)))
	}
}

// --- Bout en bout, sur un serveur IMAP en mémoire --------------------------

const me = "mathias@pxcom.aero"

type testMail struct {
	box, id, from, to, cc, subject, inReplyTo, references, body string
	at                                                          time.Time
}

func (m testMail) raw() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", m.id)
	fmt.Fprintf(&b, "Date: %s\r\n", m.at.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\n", m.from, m.to)
	if m.cc != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", m.cc)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", m.subject)
	if m.inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", m.inReplyTo)
	}
	if m.references != "" {
		fmt.Fprintf(&b, "References: %s\r\n", m.references)
	}
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(m.body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}

// startServer lance un serveur IMAP en mémoire garni des mails donnés et
// branche le paquet dessus, sans TLS.
func startServer(t *testing.T, mails []testMail) Credentials {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(me, "pass")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	if err := user.Create("Sent", &imap.CreateOptions{SpecialUse: []imap.MailboxAttr{imap.MailboxAttrSent}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range mails {
		raw := m.raw()
		if _, err := user.Append(m.box, bytes.NewReader(raw), &imap.AppendOptions{Time: m.at}); err != nil {
			t.Fatalf("append %s : %v", m.id, err)
		}
	}
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	previous := connect
	connect = func(host string) (*imapclient.Client, error) { return imapclient.DialInsecure(host, nil) }
	t.Cleanup(func() { connect = previous })

	return Credentials{Email: me, Password: "pass", Host: ln.Addr().String()}
}

// Le scénario de la vraie vie : Paul lance un sujet à Cyril, Mathias en copie ;
// Mathias répond ; Cyril répond à Mathias ; Paul tranche ; Mathias remercie.
// Un mail sans rapport porte le même objet.
func projectThread(t0 time.Time) []testMail {
	return []testMail{
		{box: "INBOX", id: "root@x.fr", from: "Paul Durand <paul@x.fr>", to: "Cyril Martin <cyril@x.fr>", cc: "Mathias <" + me + ">",
			subject: "Projet Atlas", body: "Il faut livrer la maquette du tableau de bord avant la revue.", at: t0},
		{box: "Sent", id: "m1@pxcom.aero", from: "Mathias <" + me + ">", to: "Paul Durand <paul@x.fr>", cc: "Cyril Martin <cyril@x.fr>",
			subject: "Re: Projet Atlas", inReplyTo: "root@x.fr", references: "<root@x.fr>",
			body: "Je m'en occupe d'ici jeudi, je vous envoie la maquette.\n\nLe 2 mars, Paul Durand a écrit :\n> Il faut livrer la maquette du tableau de bord avant la revue.",
			at:   t0.Add(time.Hour)},
		{box: "INBOX", id: "c1@x.fr", from: "Cyril Martin <cyril@x.fr>", to: "Mathias <" + me + ">", cc: "Paul Durand <paul@x.fr>",
			subject: "Re: Projet Atlas", inReplyTo: "m1@pxcom.aero", references: "<root@x.fr> <m1@pxcom.aero>",
			body: "Parfait. Et pour le budget, on en est où ?\n\nLe 2 mars, Mathias a écrit :\n> Je m'en occupe d'ici jeudi, je vous envoie la maquette.",
			at:   t0.Add(2 * time.Hour)},
		{box: "INBOX", id: "p2@x.fr", from: "Paul Durand <paul@x.fr>", to: "Cyril Martin <cyril@x.fr>", cc: "Mathias <" + me + ">",
			subject: "Re: Projet Atlas", inReplyTo: "c1@x.fr", references: "<root@x.fr> <m1@pxcom.aero> <c1@x.fr>",
			body: "Le budget est validé, on peut lancer.", at: t0.Add(3 * time.Hour)},
		{box: "Sent", id: "m2@pxcom.aero", from: "Mathias <" + me + ">", to: "Paul Durand <paul@x.fr>",
			subject: "Re: Projet Atlas", inReplyTo: "p2@x.fr", references: "<root@x.fr> <m1@pxcom.aero> <c1@x.fr> <p2@x.fr>",
			body: "Merci, je lance la maquette demain.", at: t0.Add(4 * time.Hour)},
		{box: "INBOX", id: "autre@y.fr", from: "Lucie <lucie@y.fr>", to: "Mathias <" + me + ">",
			subject: "Projet Atlas", body: "Rien à voir : la salle est réservée.", at: t0.Add(5 * time.Hour)},
	}
}

func TestReadRebuildsWholeConversation(t *testing.T) {
	t0 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	creds := startServer(t, projectThread(t0))

	msg, err := Read(context.Background(), creds, "cyril", false)
	if err != nil {
		t.Fatal(err)
	}
	if msg.MessageID != "c1@x.fr" {
		t.Fatalf("mail lu = %q (%s)", msg.MessageID, msg.Subject)
	}
	if !msg.AnswersYou {
		t.Error("Cyril répond au mail de Mathias : AnswersYou attendu")
	}
	// Avant : le lancement par Paul, puis la réponse de Mathias — une seule
	// fois, bien que le corps de Cyril la recopie.
	if len(msg.Thread) != 2 {
		t.Fatalf("fil antérieur = %+v", msg.Thread)
	}
	if msg.Thread[0].MessageID != "root@x.fr" || msg.Thread[0].Mine {
		t.Errorf("le fil doit commencer par le lancement de Paul : %+v", msg.Thread[0])
	}
	if msg.Thread[1].MessageID != "m1@pxcom.aero" || !msg.Thread[1].Mine {
		t.Errorf("la réponse de Mathias doit être marquée sienne : %+v", msg.Thread[1])
	}
	// Après : Paul tranche, Mathias remercie. Le mail sans rapport n'y est pas.
	if len(msg.Followups) != 2 || msg.Followups[0].MessageID != "p2@x.fr" || msg.Followups[1].MessageID != "m2@pxcom.aero" {
		t.Fatalf("suite = %+v", msg.Followups)
	}
	if !msg.Followups[1].Mine {
		t.Error("le remerciement de Mathias doit être marqué sien")
	}
	for _, m := range append(msg.Thread, msg.Followups...) {
		if m.MessageID == "autre@y.fr" {
			t.Error("un mail de même objet mais sans filiation s'est glissé dans le fil")
		}
	}
}

// Rouvrir son propre mail par identifiant montre ce qu'on lui a répondu.
func TestReadByIDShowsRepliesToOwnMail(t *testing.T) {
	t0 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	creds := startServer(t, projectThread(t0))

	f, err := ReadByID(context.Background(), creds, "<M1@pxcom.aero>")
	if err != nil {
		t.Fatal(err)
	}
	if f.Folder != FolderSent {
		t.Errorf("dossier = %q", f.Folder)
	}
	if len(f.Thread) != 1 || f.Thread[0].MessageID != "root@x.fr" {
		t.Errorf("fil antérieur = %+v", f.Thread)
	}
	var ids []string
	for _, m := range f.Followups {
		ids = append(ids, m.MessageID)
	}
	if strings.Join(ids, ",") != "c1@x.fr,p2@x.fr,m2@pxcom.aero" {
		t.Errorf("suite = %v", ids)
	}
}

// Un mail sans aucune filiation ne ramène rien d'autre que lui-même.
func TestReadStandaloneMailHasNoThread(t *testing.T) {
	t0 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	creds := startServer(t, projectThread(t0))

	msg, err := Read(context.Background(), creds, "lucie", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Thread) != 0 || len(msg.Followups) != 0 || msg.AnswersYou {
		t.Errorf("mail isolé : fil %+v, suite %+v, answers %v", msg.Thread, msg.Followups, msg.AnswersYou)
	}
}

// Ce qui est arrivé depuis la dernière lecture : la suite du fil, dans les
// deux boîtes, et rien d'une autre conversation.
func TestNouveauxSinceLastRead(t *testing.T) {
	t0 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	creds := startServer(t, projectThread(t0))

	got, err := Nouveaux(context.Background(), creds, "<root@x.fr>", []string{"cyril@x.fr"}, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range got {
		ids = append(ids, f.MessageID)
	}
	if strings.Join(ids, ",") != "p2@x.fr,m2@pxcom.aero" {
		t.Errorf("nouveautés = %v", ids)
	}
	if len(got) == 2 && got[1].Folder != FolderSent {
		t.Errorf("la réponse de Mathias vient des envoyés : %+v", got[1])
	}

	none, err := Nouveaux(context.Background(), creds, "root@x.fr", []string{"Cyril@X.fr", ""}, t0.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("rien de neuf attendu, obtenu %+v", none)
	}
}

// Le dernier mail envoyé arrive avec ce à quoi il répondait : sans le fil,
// « merci, je lance la maquette demain » ne dit pas de quoi il s'agit.
func TestSentCarriesThread(t *testing.T) {
	t0 := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	creds := startServer(t, projectThread(t0))

	found, err := Sent(context.Background(), creds, "", "", 5, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].MessageID != "m2@pxcom.aero" || found[0].Body == "" {
		t.Fatalf("dernier envoi = %+v", found)
	}
	if len(found[0].Thread) != 4 || found[0].Thread[0].MessageID != "root@x.fr" {
		t.Errorf("fil du dernier envoi = %+v", found[0].Thread)
	}
}
