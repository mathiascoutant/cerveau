package assistant

// Les urgences telles que l'assistant les manipule.
//
// Elles ne sont pas produites ici : la liste vient du serveur, qui la fabrique
// une fois (tri déterministe puis synthèse) et la sert à l'app comme au modèle.
// Ce fichier ne décrit que la forme sous laquelle elle redescend, et elle est
// pensée pour une conversation — pas pour un écran.
//
// D'où le rang : à l'oral, on ne désigne pas une tâche par son identifiant, on
// dit « le premier ». Et d'où le contenu complet dans le détail : quand il
// entre dans une ligne, la question n'est plus « qu'est-ce qui m'attend » mais
// « qu'est-ce que je dois savoir », et ça ne se répond qu'avec le message.

// UrgentEntryView est une ligne de la liste à traiter.
type UrgentEntryView struct {
	// Rang : sa place dans la liste, à partir de 1. C'est par là qu'il la
	// désigne — « on part sur le premier ».
	Rang int    `json:"rang"`
	ID   string `json:"id"`
	// Action : ce qu'il y a à faire, tel qu'affiché à l'écran. Recopie-le, ne
	// le reformule pas : c'est le texte qu'il a sous les yeux.
	Action   string       `json:"action"`
	Urgence  string       `json:"urgence"`
	Pourquoi string       `json:"pourquoi"`
	Sources  []SourceView `json:"sources"`
}

// UrgentListView est la liste entière, dans l'ordre où l'app l'affiche.
type UrgentListView struct {
	Taches []UrgentEntryView `json:"taches"`
	// Sources : les comptes réellement interrogés. Une liste vide ne veut pas
	// dire la même chose selon qu'on a regardé trois messageries ou aucune.
	Sources []string `json:"sources_consultees,omitempty"`
	// Indisponibles : ce qui n'a pas répondu. À signaler en une demi-clause,
	// jamais à taire : une liste amputée qui se présente comme complète est
	// pire qu'une liste qui s'excuse.
	Indisponibles []string `json:"sources_indisponibles,omitempty"`
}

// UrgentDetailView est une urgence ouverte : la ligne, et le message derrière.
//
// Les trois contenus s'excluent — une tâche vient d'un mail, d'un canal Slack ou
// d'une conversation WhatsApp. Celui qui est renseigné dit d'où elle sort.
type UrgentDetailView struct {
	ID       string `json:"id"`
	Rang     int    `json:"rang"`
	Total    int    `json:"total"`
	Action   string `json:"action"`
	Urgence  string `json:"urgence"`
	Pourquoi string `json:"pourquoi"`
	// Sources : les messages qui ont produit la tâche. Quand il y en a
	// plusieurs, le sujet traîne depuis un moment — c'est une information en
	// soi, et elle mérite d'être dite.
	Sources []SourceView `json:"sources"`

	// Mail : le message entier avec son fil, quand la tâche sort d'un mail.
	Mail *EmailContentView `json:"mail,omitempty"`
	// Conversation : le fil Slack, quand elle sort d'un canal ou d'un DM.
	Conversation *SlackChannelView `json:"conversation_slack,omitempty"`
	// WhatsApp : le fil du groupe ou du tête-à-tête.
	WhatsApp *WhatsAppChatView `json:"conversation_whatsapp,omitempty"`

	// Note : pourquoi le contenu manque, quand il manque. La ligne reste
	// sélectionnée et son « pourquoi » reste vrai : on répond avec ça, sans
	// prétendre avoir lu ce qu'on n'a pas lu.
	Note string `json:"note,omitempty"`
}

// UrgentDoneView est le résultat d'une ligne écartée.
type UrgentDoneView struct {
	Traitee string `json:"traitee"`
	// Restantes : combien il en reste après celle-ci. Zéro est la bonne
	// nouvelle qu'on annonce, pas un compteur qu'on récite.
	Restantes int `json:"restantes"`
	// Suivante : l'action d'après, s'il y en a une. Elle est là pour qu'on
	// puisse proposer d'enchaîner sans repasser par la liste.
	Suivante string `json:"suivante,omitempty"`
}
