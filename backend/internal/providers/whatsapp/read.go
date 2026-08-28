package whatsapp

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
)

// MaxRead : profondeur maximale d'une lecture de conversation. Haut à dessein —
// comprendre un fil demande parfois d'y remonter loin, et c'est le modèle qui
// décide de la profondeur selon ce qu'il cherche.
const MaxRead = 100

// MatchChat désigne la conversation visée par un nom prononcé à l'oral.
//
// Rend la conversation trouvée, ou la liste de celles qui se valent quand
// aucune ne se détache. Un nom de groupe WhatsApp n'a rien d'un identifiant :
// « PXCom- Azul technique », « Famille ❤️ », « Appart 3ème » — la dictée en
// rend ce qu'elle peut, et l'utilisateur ne dit de toute façon que le mot qui
// lui vient, « azul ».
func MatchChat(chats []Chat, query string) (Chat, []string, bool) {
	winner, tied := fuzzy.Resolve(query, chatNames(chats))
	if winner >= 0 {
		return chats[winner], nil, true
	}
	choices := make([]string, 0, len(tied))
	for _, i := range tied {
		choices = append(choices, chats[i].Label())
	}
	return Chat{}, choices, false
}

// Nearest rend les conversations les plus proches d'un nom qui n'a rien donné.
// Un « je ne trouve pas » sans rien d'autre laisse l'utilisateur répéter le
// même mot ; avec les noms voisins, il corrige d'un mot.
func Nearest(chats []Chat, query string, n int) []string {
	out := make([]string, 0, n)
	for _, r := range fuzzy.Rank(query, chatNames(chats)) {
		if len(out) == n {
			break
		}
		out = append(out, chats[r.Index].Label())
	}
	for _, c := range chats {
		if len(out) == n {
			break
		}
		if label := c.Label(); !slices.Contains(out, label) {
			out = append(out, label)
		}
	}
	return out
}

// NeedsConfirmation dit si le nom prononcé n'est pas celui de la conversation.
//
// « azul » désigne sans doute « PXCom- Azul technique », mais « sans doute » ne
// suffit pas quand la réponse sera écoutée sans être vérifiée : on ne lit sans
// demander que si le nom prononcé EST le nom de la conversation, à la
// ponctuation et aux accents près. Dans tous les autres cas, Raoul nomme ce
// qu'il a trouvé et attend le feu vert — une question de deux secondes contre
// un compte rendu du mauvais groupe.
func NeedsConfirmation(query string, chat Chat) bool {
	return !fuzzy.Exact(query, chat.names()...)
}

// Label est le nom à prononcer. Un groupe est annoncé comme tel : « le groupe
// PXCom- Azul technique » se comprend, « PXCom- Azul technique » tout court
// laisse croire à un contact.
func (c Chat) Label() string {
	if c.Name == "" {
		return c.JID
	}
	if c.IsGroup {
		return "groupe " + c.Name
	}
	return c.Name
}

func (c Chat) names() []string {
	names := []string{c.Name}
	if c.IsGroup && c.Name != "" {
		// Les groupes se désignent souvent par leur nom précédé du mot
		// « groupe » : la forme complète doit valoir comme nom exact.
		names = append(names, "groupe "+c.Name)
	}
	return names
}

func chatNames(chats []Chat) [][]string {
	out := make([][]string, 0, len(chats))
	for _, c := range chats {
		out = append(out, c.names())
	}
	return out
}

// AmbiguousChatError signale que plusieurs conversations répondent au même nom.
type AmbiguousChatError struct {
	Query   string
	Choices []string
}

func (e *AmbiguousChatError) Error() string {
	return fmt.Sprintf("plusieurs conversations correspondent à %q : %s",
		e.Query, strings.Join(e.Choices, " ; "))
}

// UnknownChatError : rien ne correspond, mais voici ce qui s'en approche.
type UnknownChatError struct {
	Query   string
	Nearest []string
}

func (e *UnknownChatError) Error() string {
	if len(e.Nearest) == 0 {
		return fmt.Sprintf("aucune conversation ne correspond à %q", e.Query)
	}
	return fmt.Sprintf("aucune conversation ne correspond à %q. Les plus proches : %s",
		e.Query, strings.Join(e.Nearest, ", "))
}
