package assistant

import (
	"fmt"
	"strings"
)

// Ce que Raoul sait de lui en permanence, par opposition à ce qu'il va chercher.
//
// Tout le reste de ses sources se consulte : un outil part, revient, et le
// modèle raisonne sur ce qu'il a rapporté. La mémoire ne marche pas comme ça.
// Elle n'est pas une source de plus, elle est le fond sur lequel les autres se
// lisent — savoir que Cyril est le contact technique du dossier boxes change la
// lecture du mail AVANT qu'il soit ouvert, pas après.
//
// C'est pourquoi elle est descendue dans la consigne système à chaque tour au
// lieu d'être un outil « chercher_ce_que_je_sais ». Un outil suppose qu'on ait
// eu l'idée de l'appeler, et on n'a cette idée que lorsqu'on sait déjà qu'on
// ignore quelque chose. Or, le cas qui coûte cher est exactement l'inverse : le
// modèle lit « Cyril » sans savoir qu'il y a quelque chose à savoir, et répond
// avec aplomb à côté. Une mémoire qu'on doit penser à consulter ne sert qu'aux
// questions qu'on se pose ; celle-ci sert à celles qu'on ne se pose pas.

// FactView est une chose retenue, telle qu'elle est écrite dans la consigne.
type FactView struct {
	Kind    string `json:"categorie"`
	Subject string `json:"sujet,omitempty"`
	Content string `json:"contenu"`
}

// Les catégories, côté modèle. Elles doublent celles du store plutôt que de
// l'importer : le paquet assistant ne connaît pas MongoDB, et c'est ce qui rend
// le prompt testable sans base.
const (
	FactPerson     = "personne"
	FactProject    = "projet"
	FactPreference = "preference"
	FactOther      = "fait"
)

// factSections décide de l'ordre et des intitulés. L'ordre est celui de
// l'utilité : on reconnaît un expéditeur bien plus souvent qu'on ne se rappelle
// une préférence.
var factSections = []struct {
	kind  string
	title string
}{
	{FactPerson, "Les gens"},
	{FactProject, "Ses dossiers"},
	{FactPreference, "Ses habitudes"},
	{FactOther, "Divers"},
}

// memoryBlock met la mémoire en mots pour la consigne système.
//
// En phrases et non en JSON, à la différence de tout ce qui remonte des outils.
// La différence est voulue : ce qui arrive par un outil est une donnée que le
// modèle examine, ce qui est écrit ici est quelque chose qu'il sait. Une
// structure balisée invite à la citer telle quelle — et une mémoire récitée est
// exactement le défaut qu'on cherche à éviter.
//
// Rend la chaîne vide quand il n'y a rien : une section « ce que tu sais de
// lui » suivie du néant apprend au modèle qu'il ne sait rien, ce qui est pire
// que de ne pas poser la question.
func memoryBlock(facts []FactView) string {
	groups := make(map[string][]FactView, len(factSections))
	for _, f := range facts {
		if strings.TrimSpace(f.Content) == "" {
			continue
		}
		kind := f.Kind
		if _, known := groups[kind]; !known {
			switch kind {
			case FactPerson, FactProject, FactPreference:
			default:
				kind = FactOther
			}
		}
		groups[kind] = append(groups[kind], f)
	}

	var b strings.Builder
	for _, section := range factSections {
		list := groups[section.kind]
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s :\n", section.title)
		for _, f := range list {
			if s := strings.TrimSpace(f.Subject); s != "" {
				fmt.Fprintf(&b, "- %s — %s\n", s, strings.TrimSpace(f.Content))
				continue
			}
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(f.Content))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}

	return `CE QUE TU SAIS DÉJÀ DE LUI

Ce n'est pas une source que tu consultes, c'est ce que tu sais. Tu ne l'annonces pas, tu ne le récites pas, tu ne dis jamais « d'après ce que je sais de toi » : tu t'en sers, comme on se sert de ce qu'on sait d'un collègue sans le lui rappeler. Quand il te demande frontalement ce que tu retiens sur quelqu'un ou sur un sujet, alors seulement tu le dis.

C'est aussi ce qui décide comment tu lis le reste. Un mail d'une personne que tu connais ne se résume pas comme un mail d'un inconnu : tu sais déjà ce qu'il fait là et pourquoi ça compte. Sers-t'en pour trancher ce qui est urgent, pour situer une demande dans le bon dossier, et pour écrire dans le registre qui convient à la personne.

Ce qui suit peut être daté ou faux — il change d'avis, les gens changent de poste. Ce qu'un outil te dit maintenant l'emporte toujours sur ce que tu croyais savoir.

` + b.String()
}

// memoryRules dit quand écrire dans la mémoire. Toujours présent, même quand
// elle est vide : c'est justement quand il ne sait encore rien qu'il a le plus
// à apprendre.
const memoryRules = `CE QUE TU RETIENS

Tu appelles retenir quand il t'apprend quelque chose qui vaudra encore dans un mois. Tu le fais dans le même tour, sans le commenter, sans demander l'autorisation, et sans le lui dire — « c'est noté » après chaque phrase transforme une conversation en dictée. Il parle, tu retiens, tu réponds à ce qu'il a dit.

CE QUI SE RETIENT — quatre catégories, et le choix de la catégorie compte autant que le contenu :
- personne : qui est quelqu'un de son entourage, ce qu'ils font ensemble, comment ils se parlent. « Cyril, c'est le technique chez Orange » ;
- projet : un dossier en cours, ce qu'il recouvre, avec qui. « DAW, c'est le déploiement des boxes pour PXCom » ;
- preference : une manière de faire qu'il attend de toi ou des autres. « Jamais de réunion avant 10h », « mes mails, tu les fais courts » ;
- fait : ce qui ne rentre dans aucune des trois, et rien d'autre. Ce n'est pas le tiroir par défaut.

LE SUJET EST L'ÉTIQUETTE SOUS LAQUELLE ÇA SE RANGE, deux ou trois mots — un prénom, un nom de dossier, le thème de l'habitude. C'est lui qui fait qu'apprendre autre chose sur Cyril complète la fiche Cyril au lieu d'en ouvrir une deuxième. Quand tu réapprends sur un sujet déjà connu, tu réécris la fiche ENTIÈRE, l'ancien savoir plus le nouveau : ce que tu envoies remplace ce qui était là, et un contenu partiel efface le reste.

CE QUI NE SE RETIENT PAS, et c'est la moitié du travail :
- ce qui a une échéance. « Relancer Olivier jeudi » est une tâche, elle va dans ajouter_tache. Ici on ne met que ce qui n'expire pas ;
- ce qu'un outil sait déjà te dire. Un rendez-vous est dans le calendrier, un mail est dans sa boîte. Recopier ici ce qui est déjà ailleurs crée une deuxième vérité qui se périme sans prévenir ;
- ce qu'il dit en passant sans que ça l'engage — une humeur, une hésitation, un « faudrait que » ;
- ce que tu déduis. Tu retiens ce qu'il t'a dit, pas ce que tu as conclu d'un mail. Une mémoire remplie de tes propres inférences devient un endroit où tes erreurs se figent en certitudes.

Dans le doute, tu ne retiens pas. Une mémoire courte et juste vaut mieux qu'une mémoire longue où il faut vérifier chaque ligne.

QUAND IL TE CORRIGE — « non, Cyril c'est plus chez Orange », « j'ai changé d'avis là-dessus » — tu réécris la fiche dans le même tour. Une correction qu'on ne retient pas est une erreur qu'il devra refaire à chaque fois, et c'est ce qui le fera cesser de te parler.

QUAND IL TE DEMANDE D'OUBLIER — « oublie ce que je t'ai dit sur Untel », « efface ça » — tu appelles oublier avec le sujet, et tu confirmes en trois mots.

`

// MemoryLimit borne ce qui descend dans la consigne. Même valeur d'esprit que
// la borne du store, appliquée une seconde fois ici parce que le paquet ne
// contrôle pas ce qu'on lui passe.
const MemoryLimit = 60
