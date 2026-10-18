package assistant

import "strings"

// Son entourage professionnel : qui sont les gens qui lui écrivent.
//
// La mémoire dit « Cyril est son patron ». Elle ne dit pas que
// cyril.martin@pxcom.aero, l'auteur U07ABC sur Slack et « Cyril M. » sur
// WhatsApp sont ce Cyril-là — et un modèle qui fait ce rapprochement lui-même,
// à partir d'un prénom, prend tôt ou tard un homonyme pour le patron. Le
// serveur le fait donc pour lui : chaque message descend avec l'identité de
// son auteur telle que l'annuaire la connaît, ou la mention qu'il est inconnu.

// Les niveaux d'importance d'une personne, du plus au moins pressant. Ils
// doublent ceux du store plutôt que de l'importer, comme les catégories de
// la mémoire.
const (
	ImportanceCritique = "critique"
	ImportanceHaute    = "haute"
	ImportanceNormale  = "normale"
	ImportanceBasse    = "basse"
)

var importanceLevels = []string{ImportanceCritique, ImportanceHaute, ImportanceNormale, ImportanceBasse}

// Identite : ce que Raoul sait de l'auteur d'un message. Posée par le serveur
// d'après l'annuaire — le modèle ne la devine pas.
type Identite struct {
	// Qui : sa place pour lui (« son patron, CEO de PXCom »).
	Qui string `json:"qui,omitempty"`
	// Importance : critique, haute, normale ou basse.
	Importance string `json:"importance,omitempty"`
	// QuiAuNom : reconnu à son nom seulement, l'annuaire n'ayant encore ni son
	// adresse ni son identifiant. Très probablement la bonne personne.
	QuiAuNom bool `json:"qui_au_nom,omitempty"`
	// Inconnu : une vraie personne que l'annuaire ne connaît pas — ni une
	// machine, ni lui-même.
	Inconnu bool `json:"inconnu,omitempty"`
}

// PersonView : un participant connu d'un échange, autre que l'auteur.
type PersonView struct {
	Nom        string `json:"nom"`
	Qui        string `json:"qui"`
	Importance string `json:"importance,omitempty"`
}

// entourageRules : reconnaître les gens, demander qui sont ceux qu'on ne
// connaît pas, et laisser leur place peser sur ce qui presse. Seules les
// messageries branchées sont nommées.
func entourageRules(src Sources) string {
	var kinds []string
	if src.Mail {
		kinds = append(kinds, "mail")
	}
	if src.Slack {
		kinds = append(kinds, "message Slack")
	}
	if src.WhatsApp {
		kinds = append(kinds, "message WhatsApp")
	}
	if len(kinds) == 0 {
		return ""
	}
	each := kinds[0]
	if len(kinds) > 1 {
		each = strings.Join(kinds[:len(kinds)-1], ", ") + " ou " + kinds[len(kinds)-1]
	}

	var who []string
	if src.Mail {
		who = append(who, "l'expéditeur du mail que tu lis")
	}
	if src.Slack {
		who = append(who, "la personne qui l'interpelle sur Slack")
	}
	if src.WhatsApp {
		who = append(who, "celle qui lui écrit sur WhatsApp")
	}

	var ids []string
	if src.Mail {
		ids = append(ids, "son adresse")
	}
	if src.Slack {
		ids = append(ids, "son slack_id sur Slack")
	}
	exact := ""
	if len(ids) > 0 {
		exact = "- qui_au_nom : reconnu au nom seulement. Quand tu lis un message d'une telle personne, appelle retenir avec son étiquette (le sujet de sa fiche) et " + strings.Join(ids, " — ou ") + ", sans contenu : la reconnaissance devient exacte, et un homonyme ne pourra plus se faire passer pour elle.\n"
	}

	return `SON ENTOURAGE — QUI LUI ÉCRIT

Chaque ` + each + ` descend avec ce que tu sais de son auteur, établi par le serveur d'après son adresse ou son identifiant :
- qui : sa place pour lui — « son patron, CEO », « client chez Orange », « dev de l'équipe ». Tu t'en sers sans l'annoncer comme une fiche : « ton patron te demande… », pas « Cyril, qui est ton patron d'après ma mémoire… ».
- importance : critique, haute, normale ou basse. Elle pèse sur ce qui presse. Une demande d'une personne critique (son patron, la direction, un client majeur) passe devant le reste et ne se range jamais dans « ça peut attendre » sans que tu le dises explicitement. Une personne basse ne fait jamais l'ouverture d'un point.
` + exact + `- inconnu : une vraie personne que tu ne connais pas encore.

QUAND TU NE SAIS PAS QUI C'EST, TU DEMANDES — une fois. Si l'auteur du message dont il te parle est inconnu (` + strings.Join(who, ", ") + `), tu termines ta réponse par une question courte : « Au fait, Paul Durand, c'est qui pour toi ? ». Une seule question de ce genre par réponse, sur la personne qui compte pour ce qu'il demande — jamais sur quelqu'un qui est seulement en copie, jamais sur une machine, jamais au milieu d'un point rapide sur dix messages. Ce n'est pas un interrogatoire : c'est ce qu'un collègue arrivé depuis peu demanderait.

QUAND IL TE RÉPOND — « c'est mon patron », « un client, pas important », « le chef de projet côté Airbus » —, tu appelles retenir dans le même tour : categorie personne, sujet = son prénom (prénom et nom s'il y a un homonyme), role en quelques mots, importance, et ce qui permet de la reconnaître tel que l'outil te l'a donné. Son patron et la direction sont critiques ; un client ou un chef de projet qui le sollicite directement, haut ; un collègue, normal ; un fournisseur ou un contact lointain, bas. Puis tu reprends là où vous en étiez.

S'il ne sait pas ou s'en moque, tu retiens quand même — role « à préciser », importance basse — pour ne plus jamais reposer la question.

CE QUE TU FAIS DE CE SAVOIR. Tu l'aiguilles comme le ferait quelqu'un qui connaît la maison : qui attend quoi de lui, qui pèse, qui peut attendre. Quand deux demandes se concurrencent, c'est la place des gens qui tranche, et tu le dis : « je ferais d'abord celle de Cyril, c'est ton patron ».

`
}
