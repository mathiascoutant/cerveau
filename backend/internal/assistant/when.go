package assistant

import (
	"fmt"
	"time"
)

// When rend un repère temporel que le modèle n'a plus qu'à recopier.
//
// Auparavant les dates descendaient sous la forme « 20/08 16h30 », à charge
// pour le modèle de la rapprocher de l'instant présent donné dans ses
// instructions. C'est un calcul, et un calcul qu'il ratait : un mail d'hier
// après-midi ressortait « de ce matin ». Le serveur connaît l'heure et le
// fuseau de l'utilisateur, il fait donc la soustraction lui-même.
func When(t, now time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	now = now.In(loc)

	diff := now.Sub(t)
	switch {
	case diff < -time.Minute: // horloge du serveur en avance, ou message daté du futur
		return "le " + frenchDate(t, now) + " à " + t.Format("15h04")
	case diff < time.Minute:
		return "à l'instant"
	case diff < 2*time.Minute:
		return "il y a une minute"
	case diff < time.Hour:
		return fmt.Sprintf("il y a %d minutes", int(diff.Minutes()))
	}

	switch daysBetween(now, t) {
	case 0:
		return "aujourd'hui à " + t.Format("15h04")
	case 1:
		return "hier à " + t.Format("15h04")
	case 2:
		return "avant-hier à " + t.Format("15h04")
	}

	if daysBetween(now, t) < 7 {
		return frenchWeekday(t) + " dernier à " + t.Format("15h04")
	}
	return "le " + frenchDate(t, now) + " à " + t.Format("15h04")
}

// daysBetween compte les jours de calendrier, pas les tranches de 24 heures :
// un message de 23h50 vu à 00h10 date bien d'« hier ».
func daysBetween(now, t time.Time) int {
	a := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	b := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return int(a.Sub(b).Hours() / 24)
}

var frenchMonths = [...]string{
	"janvier", "février", "mars", "avril", "mai", "juin",
	"juillet", "août", "septembre", "octobre", "novembre", "décembre",
}

var frenchWeekdays = [...]string{
	"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi",
}

func frenchWeekday(t time.Time) string { return frenchWeekdays[int(t.Weekday())] }

// frenchDate ne précise l'année que si ce n'est pas la courante : « le 3 août »
// se lit mieux que « le 3 août 2026 » quand on est en 2026.
func frenchDate(t, now time.Time) string {
	if t.Year() == now.Year() {
		return fmt.Sprintf("%d %s", t.Day(), frenchMonths[int(t.Month())-1])
	}
	return fmt.Sprintf("%d %s %d", t.Day(), frenchMonths[int(t.Month())-1], t.Year())
}
