package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func paris(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("fuseau : %v", err)
	}
	return loc
}

// Une échéance se lit dans les mots où on la dit. Le modèle recopie ce champ,
// donc c'est ici, et nulle part dans ses instructions, que « demain » se
// calcule.
func TestDue(t *testing.T) {
	loc := paris(t)
	// Mardi 25 août 2026, 14h00.
	now := time.Date(2026, 8, 25, 14, 0, 0, 0, loc)

	cases := []struct {
		nom   string
		at    time.Time
		timed bool
		want  string
	}{
		{"aujourd'hui", time.Date(2026, 8, 25, 0, 0, 0, 0, loc), false, "aujourd'hui"},
		{"avec heure", time.Date(2026, 8, 25, 9, 30, 0, 0, loc), true, "aujourd'hui à 09h30"},
		{"demain", time.Date(2026, 8, 26, 0, 0, 0, 0, loc), false, "demain"},
		{"après-demain", time.Date(2026, 8, 27, 0, 0, 0, 0, loc), false, "après-demain"},
		{"cette semaine", time.Date(2026, 8, 28, 0, 0, 0, 0, loc), false, "vendredi"},
		{"plus loin", time.Date(2026, 9, 10, 0, 0, 0, 0, loc), false, "le 10 septembre"},
		{"hier", time.Date(2026, 8, 24, 0, 0, 0, 0, loc), false, "hier"},
		{"la semaine passée", time.Date(2026, 8, 21, 0, 0, 0, 0, loc), false, "vendredi dernier"},
	}
	for _, c := range cases {
		if got := Due(c.at, now, loc, c.timed); got != c.want {
			t.Errorf("%s : Due = %q, attendu %q", c.nom, got, c.want)
		}
	}

	if got := Due(time.Time{}, now, loc, false); got != "" {
		t.Errorf("une tâche sans jour ne doit rien rendre, obtenu %q", got)
	}
}

// « Ce que j'ai à faire aujourd'hui » doit remonter ce qui traîne depuis mardi.
// Une fenêtre bornée en bas au matin ferait disparaître le retard le lendemain
// du jour où il aurait dû être fait — exactement quand il devient un problème.
func TestTodoScopeTodayKeepsOverdue(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 25, 14, 0, 0, 0, loc)

	from, to, undated := TodoScope("aujourd_hui", now)
	if !from.IsZero() {
		t.Errorf("la portée du jour ne doit pas avoir de borne basse, obtenu %s", from)
	}
	if want := time.Date(2026, 8, 26, 0, 0, 0, 0, loc); !to.Equal(want) {
		t.Errorf("borne haute %s, attendu %s", to, want)
	}
	if undated {
		t.Error("une tâche sans jour n'est pas une tâche du jour")
	}
}

func TestTodoScopeWindows(t *testing.T) {
	loc := paris(t)
	now := time.Date(2026, 8, 25, 14, 0, 0, 0, loc)
	day := func(n int) time.Time { return time.Date(2026, 8, 25+n, 0, 0, 0, 0, loc) }

	from, to, _ := TodoScope("demain", now)
	if !from.Equal(day(1)) || !to.Equal(day(2)) {
		t.Errorf("demain : %s → %s", from, to)
	}
	from, to, _ = TodoScope("en_retard", now)
	if !from.IsZero() || !to.Equal(day(0)) {
		t.Errorf("en retard : %s → %s", from, to)
	}
	_, to, _ = TodoScope("semaine", now)
	if !to.Equal(day(7)) {
		t.Errorf("semaine : borne haute %s", to)
	}
	from, to, undated := TodoScope("toutes", now)
	if !from.IsZero() || !to.IsZero() || !undated {
		t.Error("« toutes » ne borne rien et rend aussi ce qui n'a pas de jour")
	}
}

// Un jour seul et un jour avec heure ne disent pas la même chose : « jeudi » ne
// doit pas devenir « jeudi à 00h00 » dans la liste.
func TestParseDue(t *testing.T) {
	loc := paris(t)

	day, timed, err := ParseDue("2026-08-27", loc)
	if err != nil {
		t.Fatalf("date seule : %v", err)
	}
	if timed {
		t.Error("une date sans heure ne doit pas être marquée horaire")
	}
	if day.Hour() != 0 || day.Location() != loc {
		t.Errorf("date lue %s, attendue à minuit à Paris", day)
	}

	at, timed, err := ParseDue("2026-08-27T09:30:00+02:00", loc)
	if err != nil {
		t.Fatalf("date et heure : %v", err)
	}
	if !timed {
		t.Error("une heure donnée doit être marquée horaire")
	}
	if at.In(loc).Hour() != 9 {
		t.Errorf("heure lue %s", at.In(loc))
	}

	// Vide est valide : c'est la tâche dont il ne sait pas encore quand il la fera.
	if zero, _, err := ParseDue("  ", loc); err != nil || !zero.IsZero() {
		t.Errorf("échéance vide : %v / %s", err, zero)
	}
	if _, _, err := ParseDue("jeudi prochain", loc); err == nil {
		t.Error("une échéance non convertie doit être refusée, pas devinée")
	}
}

// stubTodos n'exerce que les outils de liste.
type stubTodos struct {
	Toolbox
	added TodoDraft
	list  TodoListView
}

func (s *stubTodos) AddTodo(_ context.Context, d TodoDraft) (TodoView, store.Action, error) {
	s.added = d
	return TodoView{ID: "1", Titre: d.Titre, Echeance: "jeudi"}, store.Action{Type: "todo"}, nil
}

func (s *stubTodos) Todos(context.Context, string) (TodoListView, error) { return s.list, nil }

// L'échéance descend telle quelle jusqu'au store : le modèle donne une date,
// pas une intention.
func TestAjouterTachePassesDue(t *testing.T) {
	tb := &stubTodos{}
	e := &Engine{}
	loc := paris(t)

	out, action, err := e.runTool(context.Background(), tb, loc,
		"ajouter_tache", `{"titre":"Configurer deux boxes pour DAW","echeance":"2026-08-27","de":"Cyril","origine":"mail"}`)
	if err != nil {
		t.Fatalf("ajouter_tache : %v", err)
	}
	if tb.added.Titre != "Configurer deux boxes pour DAW" {
		t.Errorf("titre transmis : %q", tb.added.Titre)
	}
	if tb.added.Echeance.Day() != 27 || tb.added.AvecHeure {
		t.Errorf("échéance transmise : %s (heure : %v)", tb.added.Echeance, tb.added.AvecHeure)
	}
	if action == nil || action.Type != "todo" {
		t.Error("l'app doit être prévenue pour rafraîchir sa liste")
	}
	if !strings.Contains(out, "jeudi") {
		t.Errorf("l'échéance doit redescendre en mots : %s", out)
	}
}

// Une liste vide n'est pas une erreur, et elle doit dire de quelle période elle
// parle : « rien » ne se répond pas pareil pour aujourd'hui et pour la semaine.
func TestMesTachesEmptyNamesScope(t *testing.T) {
	tb := &stubTodos{list: TodoListView{Portee: "demain"}}
	e := &Engine{}

	out, _, err := e.runTool(context.Background(), tb, time.UTC, "mes_taches", `{"quand":"demain"}`)
	if err != nil {
		t.Fatalf("mes_taches : %v", err)
	}
	if !strings.Contains(out, "demain") {
		t.Errorf("la portée doit être nommée : %s", out)
	}
}
