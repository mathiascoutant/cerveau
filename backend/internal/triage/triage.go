// Package triage sépare, dans ce qui n'a pas encore été traité, ce qui réclame
// vraiment quelqu'un de ce qui ne réclame personne.
//
// Le tri se veut sévère. Une liste d'urgences qui remonte tout n'est plus une
// liste d'urgences, c'est la boîte de réception avec une autre étiquette : on
// la regarde deux jours, puis on cesse de la regarder. Deux règles la tiennent :
//
//   - un mail ne compte que si l'utilisateur en est le destinataire nommé, et
//     s'il est écrit par quelqu'un. Une copie n'est pas une demande, et une
//     alerte de connexion n'attend pas de réponse ;
//   - un message Slack ne compte que s'il lui est adressé — message privé ou
//     mention. L'activité d'un canal où personne ne le cite peut se lire plus
//     tard, par définition.
//
// Le prix de ces règles est assumé : on préfère taire un message qui comptait
// que noyer les cinq qui comptent sous quarante qui ne comptent pas.
package triage

import (
	"sort"
	"strings"
	"time"
)

// Reason dit pourquoi un élément est remonté. L'app l'affiche : savoir qu'on
// est cité nommément, ou qu'on est le seul destinataire, change l'ordre dans
// lequel on ouvre les choses.
type Reason string

const (
	// ReasonDirect : mail dont l'utilisateur est le destinataire principal.
	ReasonDirect Reason = "direct"
	// ReasonDM : message privé Slack.
	ReasonDM Reason = "dm"
	// ReasonMention : l'utilisateur est cité nommément dans une conversation.
	ReasonMention Reason = "mention"
)

// Item est une chose à traiter, quelle que soit sa source.
type Item struct {
	Source string
	Titre  string
	De     string
	Apercu string
	Quand  time.Time
	Motif  Reason
	// Compte : nombre de messages derrière l'entrée (Slack regroupe par
	// conversation). Zéro quand la notion n'a pas de sens.
	Compte int
}

// Mail est ce que le tri a besoin de savoir d'un mail. Volontairement plus
// pauvre que le message IMAP : ce paquet ne doit dépendre d'aucun fournisseur,
// c'est ce qui le rend testable sans boîte mail.
type Mail struct {
	De      string
	Adresse string
	Objet   string
	Date    time.Time
	Pour    []string
	Copie   []string
	// Diffusion : le message porte les en-têtes d'une liste ou d'un envoi
	// automatique (List-Id, List-Unsubscribe, Precedence, Auto-Submitted).
	Diffusion bool
}

// Conversation est ce que le tri a besoin de savoir d'une conversation Slack.
type Conversation struct {
	Canal    string
	Type     string // "dm" ou "canal"
	NonLus   int
	Mentions int
	Dernier  time.Time
	Extraits []string
}

// Au-delà de ce nombre de destinataires, être dans le champ « À » ne veut plus
// dire qu'on est visé : c'est une diffusion qui n'a pas pris la peine du champ
// « Copie ». Le seuil est haut à dessein — un fil de projet à huit personnes
// reste un fil de projet.
const massMailing = 10

// Mails garde les mails qui réclament l'utilisateur nommément, du plus récent
// au plus ancien. `moi` est sa propre adresse.
func Mails(mails []Mail, moi string) []Item {
	out := make([]Item, 0, len(mails))
	for _, m := range mails {
		if !urgentMail(m, moi) {
			continue
		}
		out = append(out, Item{
			Source: "mail",
			Titre:  fallback(strings.TrimSpace(m.Objet), "(sans objet)"),
			De:     displayName(m.De, m.Adresse),
			Quand:  m.Date,
			Motif:  ReasonDirect,
		})
	}
	return out
}

// Slack garde les conversations qui s'adressent à l'utilisateur : ses messages
// privés, et les canaux où il est cité.
func Slack(convs []Conversation) []Item {
	out := make([]Item, 0, len(convs))
	for _, c := range convs {
		motif, ok := urgentSlack(c)
		if !ok {
			continue
		}
		compte := c.NonLus
		if compte == 0 {
			compte = c.Mentions
		}
		out = append(out, Item{
			Source: "slack",
			Titre:  c.Canal,
			De:     speaker(c.Extraits),
			Apercu: excerpt(c.Extraits),
			Quand:  c.Dernier,
			Motif:  motif,
			Compte: compte,
		})
	}
	return out
}

// urgentMail : destinataire nommé, et écrit par quelqu'un.
func urgentMail(m Mail, moi string) bool {
	if m.Diffusion || robot(m.Adresse) || automatique(m.Objet) {
		return false
	}
	return addressedTo(m, moi)
}

// addressedTo dit si l'utilisateur est destinataire principal.
//
// La copie ne compte pas : mettre quelqu'un en copie, c'est précisément dire
// « pour information ». Un champ « À » vide (envoi masqué) ne compte pas non
// plus — personne n'a été nommé, donc personne n'a été visé.
func addressedTo(m Mail, moi string) bool {
	moi = strings.ToLower(strings.TrimSpace(moi))
	if moi == "" {
		// Sans adresse de référence on ne sait rien trancher. Plutôt que de
		// tout jeter, on laisse passer : le reste du tri a déjà écarté les
		// robots, et une liste un peu large vaut mieux qu'une liste vide.
		return true
	}
	if len(m.Pour) > massMailing {
		return false
	}
	for _, addr := range m.Pour {
		if strings.Contains(strings.ToLower(addr), moi) {
			return true
		}
	}
	return false
}

// urgentSlack : un message privé ou une mention, jamais l'activité d'un canal.
func urgentSlack(c Conversation) (Reason, bool) {
	switch {
	case c.Mentions > 0:
		return ReasonMention, true
	case c.Type == "dm" && (c.NonLus > 0 || len(c.Extraits) > 0):
		return ReasonDM, true
	default:
		return "", false
	}
}

// Boîtes d'expédition qui n'attendent pas de réponse. On ne teste que la partie
// locale de l'adresse : « no-reply@ » chez n'importe qui veut dire la même
// chose, et « notifications@github.com » n'a pas besoin d'une liste de domaines
// pour se reconnaître.
var robotBoxes = []string{
	"no-reply", "noreply", "no_reply", "donotreply", "do-not-reply",
	"ne-pas-repondre", "nepasrepondre", "ne_pas_repondre",
	"mailer-daemon", "postmaster", "bounce", "bounces",
	"notification", "notifications", "notify", "alerte", "alerts", "alert",
	"newsletter", "mailing", "marketing", "no-responder",
}

func robot(addr string) bool {
	local, _, found := strings.Cut(strings.ToLower(strings.TrimSpace(addr)), "@")
	if !found {
		return false
	}
	for _, box := range robotBoxes {
		if strings.Contains(local, box) {
			return true
		}
	}
	return false
}

// Objets de messages produits par une machine : sécurité, vérification,
// facturation automatique. Ils sont adressés à l'utilisateur et lui seul, donc
// la règle du destinataire ne les attrape pas — d'où cette liste, qui vise ce
// que l'utilisateur a nommé le premier : « tentative de connexion, on s'en fout ».
var machineSubjects = []string{
	"tentative de connexion", "nouvelle connexion", "connexion détectée",
	"connexion inhabituelle", "activité inhabituelle", "alerte de sécurité",
	"sign-in attempt", "new sign-in", "new sign in", "new login",
	"unusual activity", "security alert", "suspicious",
	"code de vérification", "code de verification", "votre code",
	"verification code", "confirmation code", "one-time", "otp ",
	"vérifie ton adresse", "vérifiez votre adresse", "verify your email",
	"confirm your email", "confirme ton adresse",
	"réinitialisation de mot de passe", "reinitialisation de mot de passe",
	"password reset", "reset your password", "changement de mot de passe",
	"se désabonner", "unsubscribe", "vous recevez cet e-mail",
}

func automatique(objet string) bool {
	objet = strings.ToLower(objet)
	for _, s := range machineSubjects {
		if strings.Contains(objet, s) {
			return true
		}
	}
	return false
}

// displayName préfère le nom affiché à l'adresse : « Olivier Dupont » se lit,
// « o.dupont@cabinet-machin.fr » se déchiffre.
func displayName(from, addr string) string {
	from = strings.TrimSpace(from)
	if from != "" {
		return from
	}
	return strings.TrimSpace(addr)
}

// speaker rend l'auteur du premier extrait Slack. Les extraits arrivent sous la
// forme « Prénom : message » ; le canal dit où, l'auteur dit qui.
func speaker(extraits []string) string {
	if len(extraits) == 0 {
		return ""
	}
	if who, _, found := strings.Cut(extraits[0], " : "); found {
		return strings.TrimSpace(who)
	}
	return ""
}

// excerpt rend le premier message sans son auteur : il est déjà affiché à part.
func excerpt(extraits []string) string {
	if len(extraits) == 0 {
		return ""
	}
	if _, texte, found := strings.Cut(extraits[0], " : "); found {
		return strings.TrimSpace(texte)
	}
	return strings.TrimSpace(extraits[0])
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Merge fusionne les sources et range du plus récent au plus ancien, puis
// tronque à `limit`.
//
// L'ordre est chronologique et pas hiérarchique : une fois le tri passé, tout
// ce qui reste s'adresse à l'utilisateur, et rien ne dit qu'une mention de ce
// matin passe avant un mail d'hier. Le motif est affiché sur chaque entrée,
// c'est lui qui laisse arbitrer — pas un classement décidé ici.
func Merge(limit int, lists ...[]Item) []Item {
	var all []Item
	for _, l := range lists {
		all = append(all, l...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		// Une entrée sans horodatage part à la fin : elle n'est pas plus
		// ancienne que les autres, elle est simplement indatable.
		if all[i].Quand.IsZero() != all[j].Quand.IsZero() {
			return all[j].Quand.IsZero()
		}
		return all[i].Quand.After(all[j].Quand)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}
