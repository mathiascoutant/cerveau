// Package fuzzy rapproche un nom prononcé d'un nom écrit.
//
// Tout ce que Raoul reçoit passe d'abord par une reconnaissance vocale
// française, qui découpe et orthographie selon ce qu'elle croit entendre. Un
// canal nommé « dubaiairwing » lui revient en « dubai R wing » : trois mots au
// lieu d'un, et une lettre à la place d'une syllabe. Une comparaison exacte, ou
// même une inclusion de chaîne, répond « je ne trouve pas » — alors que le nom
// est juste, il est seulement mal écrit.
//
// Chaque chaîne est d'abord lue de deux façons — telle quelle, et en
// prononçant les lettres isolées : « dubai R wing » se relit « dubai air wing »
// parce qu'en français la lettre R se prononce « air ». C'est la passe qui
// compte le plus ici, parce qu'elle reconstitue le nom écrit au lieu de
// l'approcher : « dubai R wing » redevient exactement « dubaiairwing ».
//
// Puis trois rapprochements successifs, du plus sûr au plus tolérant :
//
//   - la forme serrée : minuscules, sans accents, sans ponctuation ni espaces.
//     Elle réunit déjà « Dubaï Air Wing », « dubai-airwing » et « #DubaiAirwing » ;
//   - la clé sonore : les graphies françaises qui se prononcent pareil sont
//     ramenées à la même écriture (« ai » et « e », « eau » et « o », le h muet) ;
//   - la distance d'édition, pour ce qui reste : une syllabe avalée, un pluriel
//     de trop, une consonne doublée.
//
// Rien ici ne tranche : le paquet donne un score, l'appelant décide — et, quand
// deux candidats se valent, demande lequel plutôt que de choisir.
package fuzzy

import (
	"strings"
	"unicode"
)

// Match est le score au-delà duquel deux noms désignent probablement la même
// chose. Réglé à la main sur des cas réels : « dubai R wing » contre
// « dubaiairwing » vaut 0,85 ; deux canaux sans rapport tombent sous 0,5.
const Match = 0.72

// Close : deux candidats séparés par moins que ça se valent. L'écart sert à
// décider s'il faut demander lequel plutôt que de prendre le premier.
const Close = 0.06

// Score dit à quel point deux noms désignent la même chose, de 0 à 1.
//
// Les deux chaînes sont confrontées dans toutes leurs lectures : une lettre
// isolée d'un côté peut correspondre à la syllabe qu'elle nomme de l'autre, et
// on ne sait pas d'avance de quel côté la dictée a découpé.
func Score(query, candidate string) float64 {
	best := 0.0
	for _, q := range Spellings(query) {
		for _, c := range Spellings(candidate) {
			if s := compare(q, c); s > best {
				best = s
			}
		}
	}
	return best
}

// Spellings rend les lectures d'un nom : la forme serrée, et la même où chaque
// lettre isolée est remplacée par le son de son nom.
//
// Les deux sont gardées, jamais l'une à la place de l'autre : un canal peut
// très bien s'appeler « plan-b », et lire son B « bé » serait exactement
// l'erreur inverse de celle qu'on corrige.
func Spellings(s string) []string {
	tight := Tight(s)
	spelled := Spelled(s)
	if spelled == "" || spelled == tight {
		return []string{tight}
	}
	return []string{tight, spelled}
}

// compare note deux formes déjà serrées.
func compare(q, c string) float64 {
	if q == "" || c == "" {
		return 0
	}
	if q == c {
		return 1
	}
	// L'inclusion ne vaut que si la partie commune est assez longue pour
	// désigner quelque chose : « a » est contenu dans tout.
	if min(len(q), len(c)) >= 3 && (strings.Contains(c, q) || strings.Contains(q, c)) {
		return 0.9
	}

	qs, cs := Sound(q), Sound(c)
	if qs == cs {
		return 0.85
	}
	if min(len(qs), len(cs)) >= 4 && (strings.Contains(cs, qs) || strings.Contains(qs, cs)) {
		return 0.8
	}

	// La meilleure des deux distances : l'écriture et le son se trompent
	// rarement au même endroit.
	return max(similarity(q, c), similarity(qs, cs))
}

// Normalize aplatit une chaîne dictée : minuscules, sans accents, la
// ponctuation ramenée à des espaces.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if folded, ok := accents[r]; ok {
			return folded
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// Tight enlève jusqu'aux espaces. C'est la forme qui réunit « air wing » et
// « airwing » — la reconnaissance vocale n'a aucun moyen de savoir si un nom
// s'écrit en un mot ou en trois.
func Tight(s string) string {
	return strings.ReplaceAll(Normalize(s), " ", "")
}

// Le nom français des lettres, tel qu'un nom de canal l'écrit quand il l'a
// avalé. C'est la table qui fait tout le travail sur le cas d'origine : la
// dictée entend la syllabe « air » de « dubaiairwing » comme la lettre R, et
// écrit « dubai R wing ». En sens inverse, R redevient « air » et le nom se
// reconstitue au caractère près.
//
// Les voyelles y figurent pour elles-mêmes : elles ne changent rien, mais leur
// absence ferait un trou dans la lecture d'un nom épelé en entier.
var letterSounds = map[string]string{
	"a": "a", "b": "be", "c": "ce", "d": "de", "e": "e", "f": "effe",
	"g": "ge", "h": "ache", "i": "i", "j": "ji", "k": "ka", "l": "elle",
	"m": "emme", "n": "enne", "o": "o", "p": "pe", "q": "ku", "r": "air",
	"s": "esse", "t": "te", "u": "u", "v": "ve", "w": "doubleve",
	"x": "ixe", "y": "igrec", "z": "zede",
}

// Spelled relit une chaîne en prononçant ses lettres isolées.
//
// « dubai R wing » → « dubaiairwing ». Une lettre seule au milieu d'un nom
// dicté n'est presque jamais une lettre : c'est une syllabe que la
// reconnaissance vocale a prise pour le nom d'une lettre, parce qu'elles se
// prononcent pareil et qu'elle n'a aucun moyen de les distinguer.
//
// Rend une chaîne vide quand il n'y a aucune lettre isolée : il n'y a alors
// rien à relire autrement.
func Spelled(s string) string {
	fields := strings.Fields(Normalize(s))
	found := false
	for i, f := range fields {
		if len([]rune(f)) != 1 {
			continue
		}
		if sound, ok := letterSounds[f]; ok {
			fields[i] = sound
			found = true
		}
	}
	if !found {
		return ""
	}
	return strings.Join(fields, "")
}

// paires de graphies qui se prononcent pareil en français, appliquées dans
// l'ordre : les plus longues d'abord, sinon « eau » se ferait manger par « au ».
var sounds = [...][2]string{
	{"eau", "o"}, {"au", "o"},
	{"ain", "in"}, {"ein", "in"},
	{"ai", "e"}, {"ei", "e"},
	{"ou", "u"}, {"oi", "wa"},
	{"qu", "k"}, {"ph", "f"}, {"ck", "k"},
}

// Sound ramène une chaîne à peu près à ce qu'elle donne à l'oreille.
//
// Ce n'est pas un Soundex : on ne cherche pas à classer des patronymes, mais à
// écrire pareil ce qu'une dictée française peut orthographier de deux façons.
// D'où le petit nombre de règles — chacune corrige une confusion vue, aucune
// n'est là par symétrie.
func Sound(s string) string {
	s = Tight(s)
	if s == "" {
		return ""
	}
	for _, pair := range sounds {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	s = strings.ReplaceAll(s, "y", "i")
	// Le h ne s'entend pas — et il disparaît d'autant plus dans une dictée.
	// Après ph, sans quoi « photo » deviendrait « poto ».
	s = strings.ReplaceAll(s, "h", "")

	// Consonnes et voyelles doublées : « airwing » entendu deux fois de suite
	// se recolle en « eerwing », qui doit valoir « erwing ».
	var b strings.Builder
	var prev rune
	for _, r := range s {
		if r != prev {
			b.WriteRune(r)
		}
		prev = r
	}
	s = b.String()

	// Marques muettes de fin : le pluriel et le e final ne s'entendent pas.
	if len(s) > 3 && (strings.HasSuffix(s, "s") || strings.HasSuffix(s, "e")) {
		s = s[:len(s)-1]
	}
	return s
}

// similarity : 1 quand les chaînes sont identiques, 0 quand tout diffère.
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	longest := max(len([]rune(a)), len([]rune(b)))
	if longest == 0 {
		return 0
	}
	return 1 - float64(distance(a, b))/float64(longest)
}

// distance est la distance de Levenshtein, en runes — un accent mal placé ne
// doit pas compter pour deux erreurs.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	// Deux lignes suffisent : on ne relit jamais la ligne d'avant l'avant.
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

var accents = map[rune]rune{
	'à': 'a', 'â': 'a', 'ä': 'a', 'á': 'a', 'ã': 'a', 'å': 'a',
	'ç': 'c',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ñ': 'n', 'ý': 'y', 'ÿ': 'y',
	'œ': 'e', 'æ': 'e',
}
