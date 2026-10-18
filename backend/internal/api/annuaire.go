package api

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// L'annuaire de son entourage.
//
// Ce que Raoul sait des gens ne vaut que s'il le reconnaît au moment où ça
// compte : quand un message arrive. La mémoire dit « Cyril est son patron » ;
// l'annuaire dit que cyril.martin@pxcom.aero et l'auteur U07ABC sur Slack sont
// ce Cyril-là. Chaque message descend donc avec l'identité de son auteur,
// établie ici — par adresse ou identifiant d'abord, par le nom seulement quand
// la fiche n'a encore ni l'un ni l'autre.

type annuaire struct {
	people  []store.Fact
	byAddr  map[string]int
	bySlack map[string]int
}

func newAnnuaire(people []store.Fact) *annuaire {
	a := &annuaire{people: people, byAddr: map[string]int{}, bySlack: map[string]int{}}
	for i, p := range people {
		for _, addr := range p.Adresses {
			a.byAddr[strings.ToLower(strings.TrimSpace(addr))] = i
		}
		for _, id := range p.SlackIDs {
			a.bySlack[strings.ToUpper(strings.TrimSpace(id))] = i
		}
	}
	return a
}

// peopleCache charge l'annuaire une fois par requête, au premier besoin : un
// point sur vingt messages ne doit pas relire la base vingt fois.
type peopleCache struct {
	once sync.Once
	dir  *annuaire
}

// knownPeople : un annuaire déjà en main, sans lecture de la base.
func knownPeople(people []store.Fact) *peopleCache {
	c := &peopleCache{dir: newAnnuaire(people)}
	c.once.Do(func() {})
	return c
}

func (t *userToolbox) annuaire(ctx context.Context) *annuaire {
	load := func() *annuaire {
		people, err := t.srv.store.People(ctx, t.user.ID)
		if err != nil {
			// Sans annuaire, les messages descendent sans identité : Raoul
			// répond comme avant d'en avoir un, il ne tombe pas en panne.
			slog.Warn("lecture de l'annuaire impossible", "err", err)
		}
		return newAnnuaire(people)
	}
	if t.people == nil {
		return load()
	}
	t.people.once.Do(func() { t.people.dir = load() })
	return t.people.dir
}

// forMail : l'expéditeur d'un mail. Une fiche qui a déjà ses adresses ne se
// reconnaît plus au nom : si celle du message n'en fait pas partie, c'est un
// homonyme — deux Cyril, et un seul est son patron.
func (a *annuaire) forMail(name, addr string) assistant.Identite {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if i, ok := a.byAddr[addr]; ok && addr != "" {
		return identity(a.people[i], false)
	}
	if strings.TrimSpace(name) == "" || strings.EqualFold(strings.TrimSpace(name), addr) {
		name = nameFromAddress(addr)
	}
	if p, ok := a.byName(name, func(p store.Fact) bool { return addr == "" || len(p.Adresses) == 0 }); ok {
		return identity(p, true)
	}
	return assistant.Identite{}
}

// forSlack : l'auteur d'un message Slack, par son identifiant, à défaut par
// son nom affiché.
func (a *annuaire) forSlack(id, name string) assistant.Identite {
	id = strings.ToUpper(strings.TrimSpace(id))
	if i, ok := a.bySlack[id]; ok && id != "" {
		return identity(a.people[i], false)
	}
	if p, ok := a.byName(name, func(p store.Fact) bool { return id == "" || len(p.SlackIDs) == 0 }); ok {
		return identity(p, true)
	}
	return assistant.Identite{}
}

// forName : une personne dont on n'a que le nom (WhatsApp).
func (a *annuaire) forName(name string) assistant.Identite {
	if p, ok := a.byName(name, func(store.Fact) bool { return true }); ok {
		return identity(p, true)
	}
	return assistant.Identite{}
}

// byName : la fiche dont l'étiquette se retrouve dans le nom affiché — tous ses
// mots (« Cyril » dans « Cyril Martin », « MARTIN Cyril »). Une seule fiche
// doit correspondre : deux candidates, c'est n'en reconnaître aucune.
func (a *annuaire) byName(name string, eligible func(store.Fact) bool) (store.Fact, bool) {
	words := strings.Fields(fuzzy.Normalize(name))
	if len(words) == 0 {
		return store.Fact{}, false
	}
	found := -1
	for i, p := range a.people {
		if !eligible(p) || !labelIn(p.Subject, words) {
			continue
		}
		if found >= 0 {
			return store.Fact{}, false
		}
		found = i
	}
	if found < 0 {
		return store.Fact{}, false
	}
	return a.people[found], true
}

func labelIn(label string, words []string) bool {
	parts := strings.Fields(fuzzy.Normalize(label))
	if len(parts) == 0 {
		return false
	}
	for _, w := range parts {
		match := func(x string) bool {
			// Une initiale (« Paul D. ») vaut pour le mot qu'elle commence.
			return x == w || (utf8.RuneCountInString(w) == 1 && strings.HasPrefix(x, w))
		}
		if !slices.ContainsFunc(words, match) {
			return false
		}
	}
	return true
}

// nameFromAddress : « cyril.martin@x.fr » s'entend « cyril martin ».
func nameFromAddress(addr string) string {
	local, _, _ := strings.Cut(addr, "@")
	return strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(local)
}

func identity(p store.Fact, byName bool) assistant.Identite {
	qui := strings.TrimSpace(p.Role)
	if qui == "" {
		qui = firstSentence(p.Content, 90)
	}
	return assistant.Identite{Qui: qui, Importance: p.Importance, QuiAuNom: byName}
}

// firstSentence : de quoi situer quelqu'un en une ligne, quand sa fiche n'a pas
// de rôle.
func firstSentence(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".;\n"); i > 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}

// mailAuthor : qui est l'expéditeur d'un mail pour lui. Inconnu ne se dit que
// d'une vraie personne : une machine ou lui-même ne se présentent pas.
func (t *userToolbox) mailAuthor(ctx context.Context, name, addr, me string, bulk bool) assistant.Identite {
	id := t.annuaire(ctx).forMail(name, addr)
	if id.Qui == "" && addr != "" && !bulk && !triage.Robot(addr) && !writtenBy(addr, me) {
		id.Inconnu = true
	}
	return id
}

// slackAuthor : qui est l'auteur d'un message Slack pour lui.
func (t *userToolbox) slackAuthor(ctx context.Context, id, name string, mine bool) assistant.Identite {
	if mine {
		return assistant.Identite{}
	}
	who := t.annuaire(ctx).forSlack(id, name)
	if who.Qui == "" && (id != "" || name != "") {
		who.Inconnu = true
	}
	return who
}

// knownRecipients : ceux des destinataires que l'annuaire connaît.
func (t *userToolbox) knownRecipients(ctx context.Context, lists ...[]string) []assistant.PersonView {
	dir := t.annuaire(ctx)
	var out []assistant.PersonView
	seen := map[string]bool{}
	for _, list := range lists {
		for _, entry := range list {
			name, addr := splitAddress(entry)
			who := dir.forMail(name, addr)
			if who.Qui == "" {
				continue
			}
			label := name
			if label == "" {
				label = addr
			}
			if seen[label] {
				continue
			}
			seen[label] = true
			out = append(out, assistant.PersonView{Nom: label, Qui: who.Qui, Importance: who.Importance})
		}
	}
	return out
}

// splitAddress sépare « Cyril Martin <cyril@x.fr> » en nom et adresse. Une
// entrée sans chevrons est une adresse nue, ou un nom sans adresse.
func splitAddress(s string) (name, addr string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return strings.Trim(strings.TrimSpace(s[:i]), `"`), strings.ToLower(strings.TrimSpace(s[i+1 : i+j]))
		}
	}
	if strings.Contains(s, "@") && !strings.Contains(s, " ") {
		return "", strings.ToLower(s)
	}
	return s, ""
}
