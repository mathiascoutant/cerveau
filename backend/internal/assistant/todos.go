package assistant

import "time"

// TodoDraft est une chose à faire telle que le modèle la dicte.
//
// Echeance à zéro veut dire « pas encore de jour ». C'est un état légitime — il
// arrive qu'on ne sache pas quand on fera quelque chose — mais ce n'est pas
// l'état par défaut : la consigne demande à Raoul de poser la question avant
// d'appeler l'outil, parce qu'une liste dont rien n'est daté ne se relit jamais.
type TodoDraft struct {
	Titre    string
	Echeance time.Time
	// AvecHeure : une heure précise a été donnée, pas seulement un jour.
	AvecHeure bool
	Note      string
	Origine   string
	De        string
	Sujet     string
}

// TodoView est une tâche telle qu'elle redescend au modèle. L'échéance y est
// déjà mise en mots (« demain », « jeudi ») : comme pour les dates de messages,
// le modèle recopie, il ne calcule pas.
type TodoView struct {
	ID       string `json:"id"`
	Titre    string `json:"titre"`
	Echeance string `json:"echeance,omitempty"`
	// EnRetard : l'échéance est passée et la tâche n'est pas cochée.
	EnRetard bool   `json:"en_retard,omitempty"`
	Note     string `json:"note,omitempty"`
	Source   string `json:"source,omitempty"`
	Faite    bool   `json:"faite,omitempty"`
}

// TodoListView est le résultat de mes_taches. Portee reprend ce qui a été
// demandé : sans elle, une liste vide ne dirait pas de quelle période elle
// parle.
type TodoListView struct {
	Portee string     `json:"portee"`
	Taches []TodoView `json:"taches"`
}

// Due met une échéance en mots, dans le fuseau de l'utilisateur et par rapport
// à maintenant.
//
// Le pendant de When, tourné vers l'avenir. Une tâche se lit « demain » ou
// « jeudi », jamais « dans 2 jours » : c'est ainsi qu'on en parle, et c'est
// aussi ce qui évite au modèle de refaire une soustraction qu'il rate.
func Due(t, now time.Time, loc *time.Location, timed bool) string {
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	now = now.In(loc)

	var day string
	switch d := daysBetween(now, t); {
	case d == 0:
		day = "aujourd'hui"
	case d == 1:
		day = "hier"
	case d == 2:
		day = "avant-hier"
	case d == -1:
		day = "demain"
	case d == -2:
		day = "après-demain"
	case d > 2 && d < 7:
		day = frenchWeekday(t) + " dernier"
	case d < -2 && d > -7:
		day = frenchWeekday(t)
	default:
		day = "le " + frenchDate(t, now)
	}
	if timed {
		return day + " à " + t.Format("15h04")
	}
	return day
}

// TodoScope traduit une portée parlée en fenêtre de dates.
//
// From et To sont des bornes sur l'échéance, To exclusive. Une borne basse
// laissée à zéro est volontaire : « ce que j'ai à faire aujourd'hui » doit
// remonter ce qui traînait depuis mardi, sinon le retard disparaît de la liste
// le lendemain du jour où il aurait dû être fait.
func TodoScope(scope string, now time.Time) (from, to time.Time, undated bool) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := func(n int) time.Time { return today.AddDate(0, 0, n) }

	switch scope {
	case "demain":
		return day(1), day(2), false
	case "en_retard":
		return time.Time{}, today, false
	case "semaine":
		return time.Time{}, day(7), false
	case "toutes", "tout":
		return time.Time{}, time.Time{}, true
	default: // aujourd'hui, et tout ce qui aurait dû être fait avant
		return time.Time{}, day(1), false
	}
}

// ScopeLabel nomme la portée pour le modèle, dans les mots où il la rendra.
func ScopeLabel(scope string) string {
	switch scope {
	case "demain":
		return "demain"
	case "en_retard":
		return "en retard"
	case "semaine":
		return "les sept prochains jours, retard compris"
	case "toutes", "tout":
		return "toute la liste"
	default:
		return "aujourd'hui, retard compris"
	}
}
