package gandi

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

const mailAvecExcel = "Message-ID: <azul@csp.fr>\r\n" +
	"Date: Fri, 23 Oct 2026 10:00:00 +0200\r\n" +
	"From: Anna <anna@csp.fr>\r\nTo: " + me + "\r\n" +
	"Subject: Azul - 1126 PXCom Updates\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
	"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHere is the content delivery for cycle 1126.\r\n" +
	"--b\r\nContent-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet\r\n" +
	"Content-Disposition: attachment; filename=\"Azul 1126.xlsx\"\r\nContent-Transfer-Encoding: base64\r\n\r\nUEsDBA==\r\n" +
	"--b--\r\n"

func TestArriveesPuisRanger(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	creds := startServer(t, []testMail{
		{box: "INBOX", id: "ancien@x.fr", from: "Paul <paul@x.fr>", to: me, subject: "Déjà là", body: "ancien", at: t0},
	})
	ctx := context.Background()

	// Première relève : on note la fin de la boîte, sans rien rendre.
	rel, list, err := Arrivees(ctx, creds, 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 || rel.UIDValidity == 0 || rel.DernierUID != 1 {
		t.Fatalf("première relève : %+v, %d mails", rel, len(list))
	}

	c, err := dial(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	cmd := c.Append("INBOX", int64(len(mailAvecExcel)), nil)
	_, _ = cmd.Write([]byte(mailAvecExcel))
	_ = cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	_ = c.Logout().Wait()

	// Seul le nouveau mail revient, avec son Excel et son texte.
	_, list, err = Arrivees(ctx, creds, rel.UIDValidity, rel.DernierUID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("attendu 1 arrivée, eu %d", len(list))
	}
	a := list[0]
	if a.Subject != "Azul - 1126 PXCom Updates" || !slices.Contains(a.PiecesJointes, "Azul 1126.xlsx") || !strings.Contains(a.Body, "content delivery") {
		t.Fatalf("arrivée mal lue : %+v", a)
	}

	// Rangé dans un dossier CSP qui n'existait pas : créé, abonné, non lu.
	if err := Ranger(ctx, creds, rel.UIDValidity, map[string][]uint32{"CSP": {a.UID}}); err != nil {
		t.Fatal(err)
	}
	c, err = dial(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Logout().Wait() }()
	subs, err := c.List("", "*", &imap.ListOptions{SelectSubscribed: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(subs, func(d *imap.ListData) bool { return d.Mailbox == "CSP" }) {
		t.Fatal("le dossier CSP n'est pas abonné")
	}
	sel, err := c.Select("CSP", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || sel.NumMessages != 1 {
		t.Fatalf("CSP : %v, %+v", err, sel)
	}
	msgs, err := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
	if err != nil || len(msgs) != 1 || slices.Contains(msgs[0].Flags, imap.FlagSeen) {
		t.Fatalf("le mail rangé ne doit pas être marqué lu : %v %+v", err, msgs)
	}
	inbox, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || inbox.NumMessages != 1 {
		t.Fatalf("la réception doit garder l'ancien mail seulement : %v, %+v", err, inbox)
	}
}

// Une boîte rangée : la réception, CSP, Autres, la corbeille. Raoul doit y
// voir clair — lister, parcourir, chercher, lire, et compter les non-lus de
// CSP sans ceux d'Autres.
func TestDossiers(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	creds := startServer(t, []testMail{
		{box: "INBOX", id: "cyril@x.fr", from: "Cyril <cyril@pxcom.aero>", to: me, subject: "Point de la semaine", body: "On se voit lundi ?", at: t0},
	})
	ctx := context.Background()
	c, err := dial(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	for _, box := range []string{"CSP", "Autres", "Trash", "Trash/2025"} {
		if err := c.Create(box, nil).Wait(); err != nil {
			t.Fatal(err)
		}
	}
	add := func(box string, m testMail) {
		raw := m.raw()
		cmd := c.Append(box, int64(len(raw)), &imap.AppendOptions{Time: m.at})
		_, _ = cmd.Write(raw)
		_ = cmd.Close()
		if _, err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	add("CSP", testMail{id: "condor@csp.fr", from: "Jonata Rosa <jonata@csp.fr>", to: me, subject: "Condor 1126 - PxCom", body: "Metadata v7 attached.", at: t0.Add(time.Hour)})
	add("Autres", testMail{id: "fb@facebook.com", from: "Facebook <notify@facebook.com>", to: me, subject: "You have 10 notifications", body: "…", at: t0.Add(2 * time.Hour)})
	_ = c.Logout().Wait()

	list, err := Dossiers(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, d := range list {
		roles[d.Nom] = d.Role
	}
	if list[0].Nom != "Réception" || roles["CSP"] != "" || roles["Trash"] != RoleCorbeille || roles["Trash/2025"] != RoleCorbeille || roles["Sent"] != RoleEnvoyes {
		t.Fatalf("dossiers mal reconnus : %+v", list)
	}

	// Parcourir CSP, avec un nom dit en minuscules.
	found, err := Parcourir(ctx, creds, "csp", 10, false)
	if err != nil || len(found) != 1 || found[0].Folder != "CSP" || found[0].Subject != "Condor 1126 - PxCom" {
		t.Fatalf("parcourir CSP : %v %+v", err, found)
	}
	if _, err := Parcourir(ctx, creds, "Projets", 10, false); err == nil || !strings.Contains(err.Error(), "CSP") {
		t.Fatalf("un dossier inconnu doit citer les dossiers existants : %v", err)
	}

	// Chercher dans « reçu » trouve le mail rangé dans CSP.
	hits, err := Search(ctx, creds, SearchQuery{Sender: "jonata", Folders: []string{FolderInbox}})
	if err != nil || len(hits) != 1 || hits[0].Folder != "CSP" {
		t.Fatalf("recherche dans les dossiers reçus : %v %+v", err, hits)
	}

	// « Lis-moi le mail de Jonata » : pas dans la réception, trouvé dans CSP.
	msg, err := Read(ctx, creds, "Jonata", false)
	if err != nil || msg.Dossier != "CSP" || !strings.Contains(msg.Body, "Metadata v7") {
		t.Fatalf("lecture d'un mail rangé : %v %+v", err, msg)
	}

	// Les non-lus : la réception et CSP, pas Autres.
	unread, err := Unread(ctx, creds, 10)
	if err != nil {
		t.Fatal(err)
	}
	var objets []string
	for _, m := range unread {
		objets = append(objets, m.Dossier+":"+m.Subject)
	}
	if len(unread) != 2 || !slices.Contains(objets, "CSP:Condor 1126 - PxCom") || !slices.Contains(objets, ":Point de la semaine") {
		t.Fatalf("non-lus attendus : réception et CSP, eu %v", objets)
	}
}
