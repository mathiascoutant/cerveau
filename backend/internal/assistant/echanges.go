package assistant

import (
	"context"
	"fmt"
	"strings"
)

// Ce que Raoul a déjà lu pour lui.
//
// Un collègue qui vient de lire un mail ne le relit pas quand on lui en
// reparle : il s'en souvient, et vérifie seulement si quelque chose de neuf est
// arrivé depuis. Chaque lecture est donc gardée — le dernier message vu, et ce
// que Raoul en a dit —, et l'outil memoire_echanges rend ce souvenir avec une
// vérification à l'instant de ce qui a bougé depuis. Relire tout le fil pour
// redire la même chose, c'était payer trois secondes pour rien.

// EchangeView : une conversation déjà lue, ce qu'il en a retenu, et ce qui est
// arrivé depuis.
type EchangeView struct {
	// Source : mail, slack ou whatsapp.
	Source       string   `json:"source"`
	Sujet        string   `json:"sujet"`
	Participants []string `json:"participants,omitempty"`
	// Le dernier message vu à la lecture.
	DernierDe      string `json:"dernier_de,omitempty"`
	DernierQuand   string `json:"dernier_quand,omitempty"`
	DernierDeToi   bool   `json:"dernier_de_toi,omitempty"`
	DernierExtrait string `json:"dernier_extrait,omitempty"`
	PourToi        string `json:"pour_toi,omitempty"`
	// Compris : ce qu'il en a dit la dernière fois, ou l'analyse d'un débrief.
	Compris   string `json:"compris,omitempty"`
	ComprisLe string `json:"compris_le,omitempty"`
	LuLe      string `json:"lu_le"`
	// Depuis : ce qui est arrivé depuis cette lecture, vérifié à l'instant —
	// « rien de nouveau », « 2 nouveaux messages », ou pourquoi on n'a pas
	// pu vérifier. Vide dans la consigne, où rien n'est vérifié.
	Depuis   string        `json:"depuis,omitempty"`
	Nouveaux []NouveauView `json:"nouveaux,omitempty"`
	// De quoi rouvrir : le dernier mail par son identifiant, la conversation
	// par son nom.
	MessageID    string `json:"message_id,omitempty"`
	Conversation string `json:"conversation,omitempty"`
}

// NouveauView : un message arrivé depuis la dernière lecture.
type NouveauView struct {
	De        string `json:"de"`
	DeToi     bool   `json:"de_toi,omitempty"`
	Quand     string `json:"quand"`
	Objet     string `json:"objet,omitempty"`
	Extrait   string `json:"extrait,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// ExchangeMemory : la boîte à outils qui retient ce que Raoul comprend d'un
// échange. Facultative — le débrief s'en sert quand elle est là.
type ExchangeMemory interface {
	NoteUnderstanding(ctx context.Context, analysis string)
}

// recentBlock : ce que Raoul a lu récemment, pour la consigne. Une ligne par
// échange, de quoi savoir ce qu'il a déjà en tête — pas de quoi le réciter.
func recentBlock(list []EchangeView) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("CE QUE TU AS LU POUR LUI RÉCEMMENT, le plus récent d'abord :\n")
	for _, e := range list {
		label := e.Source
		switch e.Source {
		case "mail":
			label = "mail « " + e.Sujet + " »"
		case "slack":
			label = "Slack " + e.Sujet
		case "whatsapp":
			label = "WhatsApp « " + e.Sujet + " »"
		}
		line := "- " + label
		if e.DernierDe != "" {
			who := e.DernierDe
			if e.DernierDeToi {
				who = "lui"
			}
			line += fmt.Sprintf(" — dernier message : %s, %s", who, e.DernierQuand)
		}
		if e.PourToi != "" {
			line += " ; " + e.PourToi
		}
		line += ". Lu " + e.LuLe + "."
		if e.Compris != "" {
			line += " Tu en avais dit : « " + truncateWords(e.Compris, 160) + " »"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

func truncateWords(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// echangeRules : se souvenir avant de relire.
func echangeRules(src Sources) string {
	if !src.Mail && !src.MailPerso && !src.Slack && !src.WhatsApp {
		return ""
	}
	var reopen []string
	if src.Mail || src.MailPerso {
		reopen = append(reopen, "lire_mail avec son message_id")
	}
	if src.Slack {
		reopen = append(reopen, "lire_canal_slack")
	}
	if src.WhatsApp {
		reopen = append(reopen, "lire_conversation_whatsapp")
	}
	return `CE QUE TU AS DÉJÀ LU, TU T'EN SOUVIENS

Chaque mail, canal ou conversation que tu ouvres pour lui est gardé : le dernier message vu, et ce que tu lui en as dit. Quand il te reparle de quelque chose que tu as déjà lu — « Cyril, c'était quoi son dernier mail ? », « et le fil sur la maquette, ça a bougé ? », « rappelle-moi ce que disait Paul » —, tu appelles memoire_echanges AVANT de rouvrir quoi que ce soit. L'outil te rend ce que tu en savais et vérifie à l'instant ce qui est arrivé depuis :
- depuis dit « rien de nouveau » : tu réponds de mémoire, sans rien rouvrir. Une clause suffit à le signaler (« rien de neuf depuis »).
- des messages nouveaux : tu le dis d'abord — c'est l'information —, puis tu n'ouvres QUE le nouveau (` + strings.Join(reopen, ", ") + `), pas tout le reste.
- vérification impossible : tu réponds de mémoire en précisant que tu n'as pas pu vérifier s'il y a du neuf.
compris est ce que tu lui en avais dit : sers-t'en pour répondre, ne le récite pas mot pour mot. S'il est daté d'avant le dernier message, il ne dit rien de ce message-là.

S'il te demande explicitement de relire, de rouvrir ou de vérifier en détail, tu relis.

`
}
