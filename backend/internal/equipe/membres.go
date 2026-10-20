// Package equipe est l'équipe de Raoul : des collègues spécialisés à qui il
// passe la parole, et qui ont chacun leur métier, leurs outils, leur mémoire et
// leur voix.
//
// La hiérarchie est celle d'une petite entreprise. Raoul est au sommet : c'est
// lui qu'on appelle, il couvre tout et oriente. En dessous, des responsables —
// aujourd'hui l'informatique — qui voient large et délèguent. En dessous
// encore, ceux qui font : l'ingénieur logiciel, qui fouille le code, le VPS,
// les bases et la CI. D'autres équipes viendront (communication, logistique) ;
// tout ce qui les distingue tient dans une fiche de membre et une consigne.
package equipe

import "github.com/mathiascoutant/cerveau/backend/internal/assistant"

// Identifiants des membres. Ils servent de clé partout — dans l'historique,
// dans l'app, dans le passage de parole — et ne changent donc jamais, même si
// les prénoms changent.
const (
	IDRaoul       = "raoul"
	IDResponsable = "responsable_info"
	IDIngenieur   = "ingenieur"
	// La veille : un agent par messagerie.
	IDVeilleMail     = "veille_mail"
	IDVeilleSlack    = "veille_slack"
	IDVeilleWhatsApp = "veille_whatsapp"
)

// EquipeVeille : les agents qui surveillent les messageries.
const EquipeVeille = "veille"

// EquipeInformatique : l'équipe technique des projets perso.
const EquipeInformatique = "informatique"

// Membre est la fiche d'un membre de l'équipe.
type Membre struct {
	ID     string `json:"id"`
	Nom    string `json:"nom"`
	Role   string `json:"role"`
	Equipe string `json:"equipe,omitempty"`
	// Superieur : à qui il rend compte. Vide pour Raoul.
	Superieur string `json:"superieur,omitempty"`
	// Espace : où il travaille — « perso » pour l'équipe technique.
	Espace string `json:"espace,omitempty"`
	// Couleur : sa teinte dans l'app, pour savoir d'un coup d'œil qui parle.
	Couleur string `json:"couleur"`
	// Voix : l'identifiant de sa voix ElevenLabs. Non exposé : l'app reçoit
	// le son, pas le réglage.
	Voix string `json:"-"`
	// Presentation : ce qu'il fait, en une phrase — pour l'app et pour Raoul.
	Presentation string `json:"presentation"`
	// Quand : ce pour quoi on le fait venir. C'est ce qui apprend à Raoul à
	// qui passer quoi.
	Quand string `json:"-"`
	// Veille : la messagerie que surveille un agent de veille (« mail »,
	// « slack », « whatsapp ») ; vide pour les autres.
	Veille string `json:"veille,omitempty"`
	// Bonjour : ce qu'il dit pour se présenter, dans sa voix et à la première
	// personne — l'écran Équipe le fait entendre.
	Bonjour string `json:"-"`
}

// Les voix par défaut. Distinctes à l'oreille, c'est tout leur rôle : on doit
// savoir qui parle sans regarder l'écran.
//
// Ce sont des voix « premade » : un compte ElevenLabs gratuit refuse les voix
// de la bibliothèque (402), y compris les voix françaises qu'il a ajoutées. Le
// français est lu avec une légère couleur d'accent, comme pour Raoul. Avec un
// compte payant, ELEVENLABS_VOICE_INGENIEUR et ELEVENLABS_VOICE_RESPONSABLE
// prennent n'importe quelle voix — David Vautrin (bts16wA7hWMfnlEIHuRo) est
// française et irait très bien à l'ingénieur.
const (
	VoixIngenieur   = "iP95p4xoKVk53GoZ742B" // Chris, simple et direct
	VoixResponsable = "XrExE9yKIg1WjnnlVkGX" // Matilda, posée et professionnelle
)

// Voix : les voix configurées, vides pour garder les défauts.
type Voix struct {
	Raoul       string
	Ingenieur   string
	Responsable string
}

// Roster rend les membres, du sommet vers le terrain.
func Roster(v Voix) []Membre {
	pick := func(set, def string) string {
		if set != "" {
			return set
		}
		return def
	}
	return []Membre{
		{
			ID: IDRaoul, Nom: "Raoul", Role: "Assistant personnel",
			Couleur:      "#FFB547",
			Voix:         v.Raoul,
			Presentation: "Il couvre tout, pro comme perso, et passe la parole à l'équipe quand il faut un spécialiste.",
			Bonjour:      "Salut, c'est Raoul. Je m'occupe de tout, ton agenda, tes mails, tes messages, et quand il faut un spécialiste pour tes projets, je te passe l'équipe.",
		},
		{
			ID: IDResponsable, Nom: "Claire", Role: "Responsable informatique",
			Equipe: EquipeInformatique, Superieur: IDRaoul, Espace: "perso",
			Couleur:      "#F39BD0",
			Voix:         pick(v.Responsable, VoixResponsable),
			Presentation: "Elle a la vue d'ensemble de tes projets perso, arbitre les priorités et confie les enquêtes à l'ingénieur.",
			Quand:        "la vue d'ensemble de ses projets perso, les priorités, ce qu'il faudrait faire et dans quel ordre, l'état général de l'infrastructure",
			Bonjour:      "Bonjour, c'est Claire, ta responsable informatique. Je garde la vue d'ensemble sur tes projets, je te dis ce qui presse et dans quel ordre, et je confie les enquêtes à Léo.",
		},
		{
			ID: IDIngenieur, Nom: "Léo", Role: "Ingénieur logiciel",
			Equipe: EquipeInformatique, Superieur: IDResponsable, Espace: "perso",
			Couleur:      "#6EE7B7",
			Voix:         pick(v.Ingenieur, VoixIngenieur),
			Presentation: "Il connaît le code, le VPS, les bases et la CI de tes projets, et diagnostique ce qui ne va pas.",
			Quand:        "tout ce qui demande de creuser : une panne, une lenteur, un déploiement raté, une question sur le code, une base de données, le VPS",
			Bonjour:      "Salut, c'est Léo, ton ingénieur logiciel. Je connais le code, le VPS, les bases et la CI de tes projets : quand quelque chose cloche, c'est moi qui regarde.",
		},
		{
			ID: IDVeilleMail, Nom: "Inès", Role: "Veille des mails pro",
			Equipe: EquipeVeille, Superieur: IDRaoul, Espace: "pro", Veille: "mail",
			Couleur:      "#93C5FD",
			Presentation: "Elle relève tes mails pro chaque minute et te prévient dès qu'un message demande ton attention.",
			Quand:        "ce qui se passe dans ses mails pro : qui lui a écrit, ce qu'on attend de lui, un mail précis à retrouver",
		},
		{
			ID: IDVeilleSlack, Nom: "Sacha", Role: "Veille Slack",
			Equipe: EquipeVeille, Superieur: IDRaoul, Espace: "pro", Veille: "slack",
			Couleur:      "#C4B5FD",
			Presentation: "Sacha suit tes canaux et messages privés Slack toutes les trois minutes et te prévient dès qu'on te mentionne ou qu'une info compte.",
			Quand:        "ce qui se passe sur Slack : les mentions, les messages privés, ce qui s'est dit dans un canal",
		},
		{
			ID: IDVeilleWhatsApp, Nom: "Nora", Role: "Veille WhatsApp",
			Equipe: EquipeVeille, Superieur: IDRaoul, Espace: "pro", Veille: "whatsapp",
			Couleur:      "#A3E635",
			Presentation: "Elle lit tes conversations WhatsApp et te prévient dès qu'un message important t'attend.",
			Quand:        "ce qui se passe sur WhatsApp : qui lui a écrit, une conversation à résumer",
		},
	}
}

// VeilleDe rend l'agent de veille d'une messagerie.
func VeilleDe(roster []Membre, source string) (Membre, bool) {
	for _, m := range roster {
		if m.Veille == source {
			return m, true
		}
	}
	return Membre{}, false
}

// Collegues rend, pour un membre, ceux à qui il peut passer la parole : tous
// les autres, Raoul compris pour l'équipe — on revient toujours au standard.
func Collegues(roster []Membre, from string) []assistant.Collegue {
	var out []assistant.Collegue
	for _, m := range roster {
		// Les agents de veille surveillent ; on ne leur passe pas la parole.
		if m.ID == from || m.Veille != "" {
			continue
		}
		quand := m.Quand
		if m.ID == IDRaoul {
			quand = "tout ce qui sort de l'informatique — son agenda, ses mails, ses messages, sa liste à faire — ou quand il dit qu'il a fini avec l'équipe"
		}
		out = append(out, assistant.Collegue{ID: m.ID, Nom: m.Nom, Role: m.Role, Quand: quand})
	}
	return out
}
