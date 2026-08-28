package fuzzy

import (
	"sort"
	"strings"
)

// Scored est un candidat noté, désigné par son rang dans la liste d'origine.
// L'appelant garde ses propres objets : ce paquet ne connaît que des noms.
type Scored struct {
	Index int
	Score float64
}

// Rank note tous les candidats, du plus proche au plus lointain.
//
// Chaque candidat porte plusieurs noms parce qu'une même chose s'appelle
// souvent de plusieurs façons : un groupe WhatsApp a son sujet, un contact a
// son nom de répertoire et son nom de profil, et on peut le désigner par
// n'importe lequel.
//
// Sert deux fois : à choisir, et à proposer les plus proches quand rien
// n'atteint le seuil — un « je ne trouve pas » suivi de six noms au hasard
// n'aide personne à se rattraper.
func Rank(query string, names [][]string) []Scored {
	wanted := Variants(query)
	if len(wanted) == 0 {
		return nil
	}

	out := make([]Scored, 0, len(names))
	for i, candidate := range names {
		best := 0.0
		for _, want := range wanted {
			for _, name := range candidate {
				if s := Score(want, name); s > best {
					best = s
				}
			}
		}
		if best > 0 {
			out = append(out, Scored{Index: i, Score: best})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Resolve désigne le candidat visé par un nom dicté.
//
// Rend son indice, ou -1 avec les indices de ceux qui se valent. Ne trancher
// pas entre deux candidats équivalents est le cœur de la fonction : lire la
// mauvaise conversation donne une réponse fausse énoncée avec l'aplomb d'une
// vraie, et personne ne va vérifier.
func Resolve(query string, names [][]string) (winner int, tied []int) {
	ranked := Rank(query, names)
	if len(ranked) == 0 || ranked[0].Score < Match {
		return -1, nil
	}
	// Un candidat nettement devant emporte la décision.
	if len(ranked) == 1 || ranked[0].Score-ranked[1].Score > Close {
		return ranked[0].Index, nil
	}

	for _, r := range ranked {
		if r.Score < Match || ranked[0].Score-r.Score > Close {
			break
		}
		if len(tied) == maxTied {
			break
		}
		tied = append(tied, r.Index)
	}
	if len(tied) < 2 {
		return ranked[0].Index, nil
	}
	return -1, tied
}

// Exact dit si le nom prononcé EST celui du candidat, et pas seulement un nom
// qui lui ressemble.
//
// La nuance décide de qui tranche. « azul » désigne très probablement le groupe
// « PXCom- Azul technique », mais « très probablement » ne suffit pas quand la
// réponse sera écoutée sans être vérifiée : l'appelant demande confirmation.
// Alors que dire « PXCom Azul technique », ou « le groupe Azul » quand le
// groupe s'appelle « Azul », ne laisse aucun doute à lever.
//
// La comparaison se fait sur la forme serrée : la ponctuation, les accents, les
// espaces et les majuscules ne distinguent rien à l'oral.
func Exact(query string, names ...string) bool {
	forms := append([]string{Normalize(query)}, Variants(query)...)
	for _, form := range forms {
		tight := Tight(form)
		if tight == "" {
			continue
		}
		for _, name := range names {
			if tight == Tight(name) {
				return true
			}
		}
	}
	return false
}

// Nombre de candidats proposés au choix : au-delà, la question devient une
// liste qu'on ne peut pas écouter.
const maxTied = 5

// mots par lesquels on annonce une conversation à l'oral sans qu'ils fassent
// partie de son nom.
var leadIns = []string{
	"le canal ", "la conversation ", "le groupe ", "la discussion ",
	"le message de ", "les messages de ", "la conv ",
	"canal ", "conversation ", "groupe ", "discussion ", "conv ", "diese ", "dm ",
	"le ", "la ", "les ",
}

// Variants rend les lectures possibles d'un nom dicté : tel quel, et débarrassé
// de ce qui l'annonce. Les deux sont essayées plutôt qu'une seule, parce qu'une
// conversation peut très bien s'appeler « les-devs » — lui retirer son « les »
// serait exactement l'erreur inverse.
func Variants(query string) []string {
	base := Normalize(query)
	if base == "" {
		return nil
	}
	stripped := base
	for changed := true; changed; {
		changed = false
		for _, lead := range leadIns {
			if after, ok := strings.CutPrefix(stripped, lead); ok && strings.TrimSpace(after) != "" {
				stripped = after
				changed = true
			}
		}
	}
	if stripped == base {
		return []string{base}
	}
	return []string{base, stripped}
}
