// Package echeance retrouve, dans le corps d'un message, la date à laquelle on
// attend quelque chose — et la résout en date réelle.
//
// C'est le même geste que celui du champ « reçu » : une soustraction que le
// serveur fait pour que le modèle n'ait pas à la faire. Sauf qu'ici elle est
// plus traître, parce que le point de référence n'est PAS l'instant présent.
// « Avant vendredi » dans un mail reçu mardi dernier ne désigne pas le vendredi
// qui vient. Le modèle, à qui on donne « reçu mardi dernier » d'un côté et
// « avant vendredi » de l'autre, recolle les deux à sa manière — et il annonce
// une échéance fausse avec le même aplomb qu'une vraie.
//
// LE TRI EST SÉVÈRE, et c'est le cœur de ce paquet. Une date n'est retenue que
// si elle est portée par un marqueur d'échéance explicite — « avant »,
// « d'ici », « au plus tard », « sous ». Sans cette règle, « on s'est vus
// vendredi » devient une échéance, et une échéance inventée est bien pire que
// pas d'échéance du tout : elle crée une urgence qui n'existe pas, et on cesse
// de croire celles qui existent.
//
// Ne dépend d'aucun fournisseur : il prend du texte et une date, ce qui le rend
// testable sans boîte mail.
package echeance

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Echeance est une date attendue, telle qu'on l'a retrouvée.
type Echeance struct {
	// Quand : la date résolue, à minuit dans le fuseau de l'utilisateur quand
	// aucune heure n'a été donnée.
	Quand time.Time
	// Heure : une heure précise figurait dans le texte. « avant vendredi » et
	// « avant vendredi 18h » ne s'annoncent pas pareil, et une heure inventée
	// dans une échéance fait rater un rendez-vous auquel personne ne s'est
	// engagé.
	Heure bool
	// Extrait : les mots qui la portaient, tels qu'écrits. C'est ce qui permet
	// de dire d'où sort la date quand elle surprend.
	Extrait string
}

// Les marqueurs. Un mot de cette liste doit précéder la date, sinon on ne
// retient rien.
//
// « pour » et « jusqu'à » ne figurent qu'en version longue — « pour le »,
// « jusqu'au » — parce que « pour information » et « jusqu'à présent » sont des
// tournures trop courantes pour qu'on prenne le risque.
const markers = `(?:avant|d'ici|au plus tard le|au plus tard|dernier d[ée]lai|` +
	`d[ée]lai|[ée]ch[ée]ance|deadline|jusqu'au|pour le|pour|sous|` +
	`no later than|before|by|due)`

// sep sépare le marqueur de la date. Les deux-points comptent : « Échéance :
// 12 janvier » est une des façons les plus courantes de l'écrire, et exiger une
// espace laissait passer exactement les mails qui annoncent leur date le plus
// clairement.
const sep = `(?:\s*[:,]\s*|\s+)`

var (
	// Date chiffrée : 12/09, 12-09-2026, 12.09.26.
	reNumeric = regexp.MustCompile(markers + sep + `(?:le\s+)?(\d{1,2})[/.-](\d{1,2})(?:[/.-](\d{2,4}))?`)

	// Date en toutes lettres : le 12 septembre, 3 mars 2027.
	reTextual = regexp.MustCompile(markers + sep + `(?:le\s+)?(\d{1,2})(?:er)?\s+` +
		`(janvier|f[ée]vrier|mars|avril|mai|juin|juillet|ao[uû]t|septembre|octobre|novembre|d[ée]cembre)` +
		`(?:\s+(\d{4}))?`)

	// Jour de la semaine : avant vendredi, d'ici lundi prochain.
	reWeekday = regexp.MustCompile(markers + sep + `(?:le\s+)?` +
		`(lundi|mardi|mercredi|jeudi|vendredi|samedi|dimanche)(\s+prochain)?`)

	// Jour relatif : avant demain, d'ici ce soir.
	reRelative = regexp.MustCompile(markers + sep + `(aujourd'hui|ce soir|demain|apr[èe]s-demain)`)

	// Durée : sous 48h, d'ici trois jours, sous 2 semaines.
	reDuration = regexp.MustCompile(`(?:sous|d'ici|dans|within)\s+(\d{1,3})\s*` +
		`(h\b|heures?|jours?|semaines?|hours?|days?|weeks?)`)

	// Fin de période : avant la fin de la semaine, d'ici fin du mois.
	rePeriod = regexp.MustCompile(markers + sep + `(?:la\s+|le\s+)?fin\s+(?:de\s+|du\s+|de la\s+)?(semaine|mois|journ[ée]e)`)

	// Heure seule : avant 18h, avant 9h30. Se rapporte au jour du message.
	reClock = regexp.MustCompile(markers + sep + `(\d{1,2})\s*h(?:\s*(\d{2}))?`)
)

var months = map[string]time.Month{
	"janvier": time.January, "fevrier": time.February, "février": time.February,
	"mars": time.March, "avril": time.April, "mai": time.May, "juin": time.June,
	"juillet": time.July, "aout": time.August, "août": time.August,
	"septembre": time.September, "octobre": time.October,
	"novembre": time.November, "decembre": time.December, "décembre": time.December,
}

var weekdays = map[string]time.Weekday{
	"lundi": time.Monday, "mardi": time.Tuesday, "mercredi": time.Wednesday,
	"jeudi": time.Thursday, "vendredi": time.Friday,
	"samedi": time.Saturday, "dimanche": time.Sunday,
}

// Trouver cherche l'échéance d'un message, résolue par rapport à sa date de
// réception.
//
// Rend nil quand il n'y a rien de sûr, et c'est le cas le plus fréquent : la
// plupart des mails n'ont pas d'échéance, et en inventer une à chacun ferait de
// ce champ un bruit qu'on apprend à ignorer.
func Trouver(body string, recu time.Time, loc *time.Location) *Echeance {
	if loc == nil {
		loc = time.UTC
	}
	if recu.IsZero() {
		return nil
	}
	recu = recu.In(loc)

	// ToLower préserve la longueur en octets des lettres françaises, donc les
	// positions rendues par les expressions valent aussi dans le texte
	// d'origine — c'est lui qu'on cite dans Extrait.
	low := strings.ToLower(body)

	found := collect(low, body, recu, loc)
	if len(found) == 0 {
		return nil
	}

	// La première en ordre de lecture, parmi celles qui ne précèdent pas le
	// message lui-même. Une date antérieure à l'envoi n'est pas une échéance :
	// c'est presque toujours une analyse qui a mal tourné, et un rappel de
	// rendez-vous passé dans le pire des cas.
	//
	// L'ordre de lecture plutôt que la plus proche : quand un mail cite deux
	// dates, c'est la première qui porte la demande, la seconde nuance.
	day := time.Date(recu.Year(), recu.Month(), recu.Day(), 0, 0, 0, 0, loc)
	for _, e := range found {
		if !e.Quand.Before(day) {
			out := e
			return &out
		}
	}
	return nil
}

// hit est une échéance candidate et l'endroit où elle a été lue.
type hit struct {
	Echeance
	at int
}

func collect(low, raw string, recu time.Time, loc *time.Location) []Echeance {
	var hits []hit

	add := func(at int, quoted string, when time.Time, timed bool) {
		hits = append(hits, hit{
			Echeance: Echeance{Quand: when, Heure: timed, Extrait: excerpt(raw, quoted, at)},
			at:       at,
		})
	}

	for _, m := range reNumeric.FindAllStringSubmatchIndex(low, -1) {
		day := atoi(low, m, 2)
		month := atoi(low, m, 4)
		year := atoi(low, m, 6)
		if day < 1 || day > 31 || month < 1 || month > 12 {
			continue
		}
		add(m[0], low[m[0]:m[1]], resolveDate(day, time.Month(month), year, recu, loc), false)
	}

	for _, m := range reTextual.FindAllStringSubmatchIndex(low, -1) {
		day := atoi(low, m, 2)
		month, ok := months[low[m[4]:m[5]]]
		if !ok || day < 1 || day > 31 {
			continue
		}
		add(m[0], low[m[0]:m[1]], resolveDate(day, month, atoi(low, m, 6), recu, loc), false)
	}

	for _, m := range reWeekday.FindAllStringSubmatchIndex(low, -1) {
		wd, ok := weekdays[low[m[2]:m[3]]]
		if !ok {
			continue
		}
		next := m[4] >= 0 // « prochain » : la semaine d'après
		add(m[0], low[m[0]:m[1]], resolveWeekday(wd, next, recu, loc), false)
	}

	for _, m := range reRelative.FindAllStringSubmatchIndex(low, -1) {
		day := midnight(recu, loc)
		timed := false
		switch low[m[2]:m[3]] {
		case "demain":
			day = day.AddDate(0, 0, 1)
		case "après-demain", "apres-demain":
			day = day.AddDate(0, 0, 2)
		case "ce soir":
			day = day.Add(20 * time.Hour)
			timed = true
		}
		add(m[0], low[m[0]:m[1]], day, timed)
	}

	for _, m := range reDuration.FindAllStringSubmatchIndex(low, -1) {
		n := atoi(low, m, 2)
		if n <= 0 {
			continue
		}
		unit := strings.TrimSpace(low[m[4]:m[5]])
		switch {
		case strings.HasPrefix(unit, "h"), strings.HasPrefix(unit, "hour"):
			add(m[0], low[m[0]:m[1]], recu.Add(time.Duration(n)*time.Hour), true)
		case strings.HasPrefix(unit, "jour"), strings.HasPrefix(unit, "day"):
			add(m[0], low[m[0]:m[1]], midnight(recu, loc).AddDate(0, 0, n), false)
		case strings.HasPrefix(unit, "semaine"), strings.HasPrefix(unit, "week"):
			add(m[0], low[m[0]:m[1]], midnight(recu, loc).AddDate(0, 0, 7*n), false)
		}
	}

	for _, m := range rePeriod.FindAllStringSubmatchIndex(low, -1) {
		switch low[m[2]:m[3]] {
		case "semaine":
			add(m[0], low[m[0]:m[1]], resolveWeekday(time.Friday, false, recu, loc), false)
		case "mois":
			first := time.Date(recu.Year(), recu.Month(), 1, 0, 0, 0, 0, loc)
			add(m[0], low[m[0]:m[1]], first.AddDate(0, 1, -1), false)
		case "journée", "journee":
			add(m[0], low[m[0]:m[1]], midnight(recu, loc), false)
		}
	}

	for _, m := range reClock.FindAllStringSubmatchIndex(low, -1) {
		h := atoi(low, m, 2)
		if h > 23 {
			continue
		}
		add(m[0], low[m[0]:m[1]], midnight(recu, loc).Add(
			time.Duration(h)*time.Hour+time.Duration(atoi(low, m, 4))*time.Minute), true)
	}

	// Ordre de lecture. Deux expressions peuvent couvrir le même endroit — « avant
	// vendredi 18h » est vu par reWeekday et par reClock ; la plus à gauche
	// gagne, et c'est bien le jour qui porte l'échéance.
	out := make([]Echeance, 0, len(hits))
	for i := range hits {
		for j := i; j > 0 && hits[j].at < hits[j-1].at; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	for _, h := range hits {
		out = append(out, h.Echeance)
	}
	return out
}

// resolveDate choisit l'année quand elle n'est pas écrite : celle qui place la
// date devant le message, pas derrière. Un « avant le 3 janvier » reçu le
// 20 décembre vise l'année suivante.
func resolveDate(day int, month time.Month, year int, recu time.Time, loc *time.Location) time.Time {
	if year > 0 {
		if year < 100 {
			year += 2000
		}
		return time.Date(year, month, day, 0, 0, 0, 0, loc)
	}
	t := time.Date(recu.Year(), month, day, 0, 0, 0, 0, loc)
	if t.Before(midnight(recu, loc)) {
		t = t.AddDate(1, 0, 0)
	}
	return t
}

// resolveWeekday avance jusqu'au jour nommé. Le jour même compte : « avant
// vendredi » écrit un vendredi matin veut dire ce vendredi-là, pas le suivant.
func resolveWeekday(wd time.Weekday, next bool, recu time.Time, loc *time.Location) time.Time {
	t := midnight(recu, loc)
	delta := (int(wd) - int(t.Weekday()) + 7) % 7
	t = t.AddDate(0, 0, delta)
	if next {
		t = t.AddDate(0, 0, 7)
	}
	return t
}

func midnight(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// atoi lit le groupe n d'une correspondance, 0 s'il est absent.
func atoi(s string, m []int, n int) int {
	if n+1 >= len(m) || m[n] < 0 {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(s[m[n]:m[n+1]]))
	if err != nil {
		return 0
	}
	return v
}

// excerpt rend les mots tels qu'ils étaient écrits, casse d'origine comprise.
func excerpt(raw, quoted string, at int) string {
	if at < 0 || at+len(quoted) > len(raw) {
		return quoted
	}
	return strings.Join(strings.Fields(raw[at:at+len(quoted)]), " ")
}
