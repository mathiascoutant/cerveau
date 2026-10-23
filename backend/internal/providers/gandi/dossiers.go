package gandi

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Les dossiers de la boîte.
//
// Un mail reçu n'est pas toujours dans la réception : le tri d'Inès range les
// cycles CSP dans « CSP », Mathias range à la main dans Thunderbird. Pour Raoul,
// « reçu » veut donc dire : la réception ET chaque dossier où l'on range des
// mails reçus. Ce qui n'en est pas — envoyés, brouillons, corbeille,
// indésirables — se reconnaît à l'attribut que le serveur pose dessus
// (RFC 6154), et à défaut à son nom.

// NonLusAussi : les dossiers dont les non-lus comptent comme ceux de la
// réception — « CSP », où le tri range les cycles de contenu : du travail qui
// attend. Les autres dossiers (« Autres », ceux qu'un filtre de Thunderbird
// alimente) ont été mis de côté exprès ; on les parcourt et on y cherche,
// mais leurs non-lus ne remontent pas.
var NonLusAussi = []string{"CSP"}

// Rôles spéciaux d'un dossier, tels qu'on les dit.
const (
	RoleReception   = "réception"
	RoleEnvoyes     = "envoyés"
	RoleBrouillons  = "brouillons"
	RoleCorbeille   = "corbeille"
	RoleIndesirable = "indésirables"
	RoleArchives    = "archives"
)

var nomsSpeciaux = map[string][]string{
	RoleBrouillons:  {"Drafts", "Brouillons"},
	RoleCorbeille:   {"Trash", "Corbeille", "Deleted Messages", "Deleted Items", "Éléments supprimés", "Elements supprimes", "Bin"},
	RoleIndesirable: {"Junk", "Spam", "Indésirables", "Courrier indésirable", "Junk E-mail", "Junk Email"},
	RoleArchives:    {"Archive", "Archives"},
}

// Dossier : un dossier de la boîte, avec ce qu'il contient.
type Dossier struct {
	// Nom : le nom tel qu'on le dit (« CSP », « Clients/Azul ») — c'est celui
	// qu'on repasse pour le parcourir.
	Nom string
	// Role : réception, envoyés, brouillons, corbeille… vide pour un dossier
	// de rangement ordinaire.
	Role     string
	Messages uint32
	NonLus   uint32

	serveur string
}

// boite : un dossier tel que le serveur le nomme, et tel qu'on le dit.
type boite struct {
	serveur, label, role string
}

// recu : un dossier où sont des mails reçus.
func (b boite) recu() bool {
	return b.role == "" || b.role == RoleReception || b.role == RoleArchives
}

// boites liste les dossiers sélectionnables, la réception d'abord.
func boites(c *imapclient.Client) ([]boite, error) {
	list, err := c.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
	if err != nil {
		// Serveur sans l'extension SPECIAL-USE : la liste simple, et les noms.
		if list, err = c.List("", "*", nil).Collect(); err != nil {
			return nil, fmt.Errorf("liste des dossiers : %w", err)
		}
	}
	out := []boite{{serveur: "INBOX", label: FolderInbox, role: RoleReception}}
	for _, d := range list {
		if strings.EqualFold(d.Mailbox, "INBOX") ||
			slices.Contains(d.Attrs, imap.MailboxAttrNoSelect) || slices.Contains(d.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		out = append(out, boite{serveur: d.Mailbox, label: nomDit(d.Mailbox, d.Delim), role: role(d)})
	}
	sort.SliceStable(out[1:], func(i, j int) bool { return out[1+i].label < out[1+j].label })
	return out, nil
}

// nomDit : le nom sans le préfixe « INBOX. » de certains serveurs, avec des
// « / » pour les sous-dossiers.
func nomDit(mailbox string, delim rune) string {
	nom := mailbox
	if delim != 0 {
		if rest, ok := strings.CutPrefix(nom, "INBOX"+string(delim)); ok && rest != "" {
			nom = rest
		}
		if delim != '/' {
			nom = strings.ReplaceAll(nom, string(delim), "/")
		}
	}
	return nom
}

func role(d *imap.ListData) string {
	for _, a := range d.Attrs {
		switch a {
		case imap.MailboxAttrSent:
			return RoleEnvoyes
		case imap.MailboxAttrDrafts:
			return RoleBrouillons
		case imap.MailboxAttrTrash:
			return RoleCorbeille
		case imap.MailboxAttrJunk:
			return RoleIndesirable
		case imap.MailboxAttrArchive:
			return RoleArchives
		case imap.MailboxAttrAll, imap.MailboxAttrFlagged:
			// Dossiers virtuels (Gmail) : des copies de mails rangés ailleurs.
			return "virtuel"
		}
	}
	// Par le nom : celui du dossier, ou celui de son parent — « Trash/2025 »
	// est encore la corbeille, « Archives/2024 » encore des archives.
	path := strings.Split(nomDit(d.Mailbox, d.Delim), "/")
	for _, seg := range []string{path[len(path)-1], path[0]} {
		for _, n := range sentNames {
			if strings.EqualFold(nomDit(n, '.'), seg) {
				return RoleEnvoyes
			}
		}
		for r, noms := range nomsSpeciaux {
			for _, n := range noms {
				if strings.EqualFold(n, seg) {
					return r
				}
			}
		}
	}
	return ""
}

// boitesRecues : la réception, puis les dossiers de mails reçus.
func boitesRecues(c *imapclient.Client) []boite {
	all, err := boites(c)
	if err != nil {
		// Sans la liste, on garde au moins la réception : c'est une
		// dégradation, pas une panne.
		return []boite{{serveur: "INBOX", label: FolderInbox, role: RoleReception}}
	}
	var out []boite
	for _, b := range all {
		if b.recu() {
			out = append(out, b)
		}
	}
	return out
}

// trouverBoite retrouve le dossier désigné, tel qu'il a été dit (« csp »,
// « le dossier Autres », « réception »). Rend aussi les noms existants, pour
// dire lesquels on a quand rien ne correspond.
func trouverBoite(c *imapclient.Client, nom string) (boite, []string, error) {
	all, err := boites(c)
	if err != nil {
		return boite{}, nil, err
	}
	want := normalizeQuery(nom)
	for _, prefix := range []string{"le dossier ", "dossier ", "les ", "la ", "le "} {
		want = strings.TrimPrefix(want, prefix)
	}
	noms := make([]string, 0, len(all))
	for _, b := range all {
		noms = append(noms, b.label)
	}
	switch want {
	case "", "inbox", "recu", "recus", "reception", "boite de reception":
		return all[0], noms, nil
	}
	// Le nom exact, puis le rôle (« corbeille », « envoyés »), puis le dernier
	// segment d'un sous-dossier.
	for _, b := range all {
		if normalizeQuery(b.label) == want || normalizeQuery(b.serveur) == want {
			return b, noms, nil
		}
	}
	for _, b := range all {
		if b.role != "" && normalizeQuery(b.role) == want {
			return b, noms, nil
		}
	}
	for _, b := range all {
		if i := strings.LastIndex(b.label, "/"); i >= 0 && normalizeQuery(b.label[i+1:]) == want {
			return b, noms, nil
		}
	}
	return boite{}, noms, fmt.Errorf("aucun dossier « %s » — dossiers existants : %s", nom, strings.Join(noms, ", "))
}

// Dossiers rend les dossiers de la boîte, avec le nombre de messages et de
// non-lus de chacun. La réception d'abord.
func Dossiers(ctx context.Context, creds Credentials) ([]Dossier, error) {
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	all, err := boites(c)
	if err != nil {
		return nil, err
	}
	out := make([]Dossier, 0, len(all))
	for _, b := range all {
		if b.role == "virtuel" {
			continue
		}
		d := Dossier{Nom: b.label, Role: b.role, serveur: b.serveur}
		if b.serveur == "INBOX" {
			d.Nom = "Réception"
		}
		st, err := c.Status(b.serveur, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
		if err == nil {
			if st.NumMessages != nil {
				d.Messages = *st.NumMessages
			}
			if st.NumUnseen != nil {
				d.NonLus = *st.NumUnseen
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// Parcourir rend les derniers mails d'un dossier, du plus récent au plus
// ancien — ou seulement ses non-lus. Chacun porte son Message-ID, pour être
// ouvert ensuite par ReadByID. Rien n'est marqué comme lu.
func Parcourir(ctx context.Context, creds Credentials, dossier string, limit int, nonLus bool) ([]Found, error) {
	if limit <= 0 {
		limit = 15
	}
	c, err := dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	b, _, err := trouverBoite(c, dossier)
	if err != nil {
		return nil, err
	}
	sel, err := c.Select(b.serveur, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("impossible d'ouvrir %s : %w", b.label, err)
	}
	if sel.NumMessages == 0 {
		return nil, nil
	}
	var nums []uint32
	if nonLus {
		found, err := c.Search(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}, &imap.SearchOptions{ReturnAll: true}).Wait()
		if err != nil {
			return nil, fmt.Errorf("recherche des non lus : %w", err)
		}
		nums = found.AllSeqNums()
	} else {
		first := uint32(1)
		if sel.NumMessages > uint32(limit) {
			first = sel.NumMessages - uint32(limit) + 1
		}
		for n := first; n <= sel.NumMessages; n++ {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		return nil, nil
	}
	if len(nums) > limit {
		nums = nums[len(nums)-limit:]
	}
	label := b.label
	if b.role == RoleEnvoyes {
		label = FolderSent
	}
	list, err := fetchEnvelopes(c, nums, label)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Date.After(list[j].Date) })
	return list, nil
}

func nonLusSuivis(label string) bool {
	return slices.ContainsFunc(NonLusAussi, func(n string) bool { return strings.EqualFold(n, label) })
}
