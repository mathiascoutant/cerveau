package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// La liste à faire vue par l'assistant.
//
// Elle ne se déduit d'aucune source : c'est la seule chose de Raoul que
// l'utilisateur écrit lui-même, en dictant. D'où la règle qui traverse ce
// fichier — on ne devine rien à sa place, ni le jour d'une tâche, ni laquelle
// il désigne quand deux se ressemblent.

// Combien de tâches cochées restent visibles. Assez pour que la liste garde une
// trace de la journée, pas assez pour qu'elle devienne un journal.
const todoDoneWindow = 12 * time.Hour

func (t *userToolbox) AddTodo(ctx context.Context, draft assistant.TodoDraft) (assistant.TodoView, store.Action, error) {
	title := strings.TrimSpace(draft.Titre)
	if title == "" {
		return assistant.TodoView{}, store.Action{}, errors.New("titre de tâche manquant")
	}

	todo := store.Todo{
		UserID: t.user.ID,
		Title:  title,
		Note:   strings.TrimSpace(draft.Note),
		Timed:  draft.AvecHeure,
	}
	if !draft.Echeance.IsZero() {
		due := draft.Echeance.In(t.location())
		todo.Due = &due
	}
	if draft.Origine != "" || draft.De != "" || draft.Sujet != "" {
		todo.Source = &store.TodoSource{
			Origine: strings.TrimSpace(draft.Origine),
			De:      strings.TrimSpace(draft.De),
			Titre:   strings.TrimSpace(draft.Sujet),
		}
	}

	saved, err := t.srv.store.SaveTodo(ctx, todo)
	if err != nil {
		return assistant.TodoView{}, store.Action{}, fmt.Errorf("enregistrement de la tâche : %w", err)
	}
	view := t.todoView(saved)
	return view, todoAction(view, "added"), nil
}

func (t *userToolbox) Todos(ctx context.Context, scope string) (assistant.TodoListView, error) {
	now := time.Now().In(t.location())
	from, to, undated := assistant.TodoScope(scope, now)

	todos, err := t.srv.store.Todos(ctx, t.user.ID, store.TodoQuery{
		From: from, To: to, Undated: undated,
	})
	if err != nil {
		return assistant.TodoListView{}, err
	}
	out := assistant.TodoListView{Portee: assistant.ScopeLabel(scope), Taches: []assistant.TodoView{}}
	for _, todo := range todos {
		out.Taches = append(out.Taches, t.todoView(todo))
	}
	return out, nil
}

func (t *userToolbox) CompleteTodo(ctx context.Context, query string) (assistant.TodoView, store.Action, error) {
	todo, err := t.findTodo(ctx, query)
	if err != nil {
		return assistant.TodoView{}, store.Action{}, err
	}
	done, err := t.srv.store.SetTodoDone(ctx, t.user.ID, todo.ID, true)
	if err != nil {
		return assistant.TodoView{}, store.Action{}, err
	}
	view := t.todoView(done)
	return view, todoAction(view, "done"), nil
}

func (t *userToolbox) RescheduleTodo(ctx context.Context, query string, due time.Time, timed bool) (assistant.TodoView, store.Action, error) {
	todo, err := t.findTodo(ctx, query)
	if err != nil {
		return assistant.TodoView{}, store.Action{}, err
	}
	var when *time.Time
	if !due.IsZero() {
		local := due.In(t.location())
		when = &local
	}
	moved, err := t.srv.store.RescheduleTodo(ctx, t.user.ID, todo.ID, when, timed)
	if err != nil {
		return assistant.TodoView{}, store.Action{}, err
	}
	view := t.todoView(moved)
	return view, todoAction(view, "moved"), nil
}

func (t *userToolbox) DropTodo(ctx context.Context, query string) (assistant.TodoView, store.Action, error) {
	todo, err := t.findTodo(ctx, query)
	if err != nil {
		return assistant.TodoView{}, store.Action{}, err
	}
	if err := t.srv.store.DeleteTodo(ctx, t.user.ID, todo.ID); err != nil {
		return assistant.TodoView{}, store.Action{}, err
	}
	view := t.todoView(todo)
	return view, todoAction(view, "dropped"), nil
}

// findTodo retrouve LA tâche que désignent quelques mots dictés.
//
// Deux tâches qui correspondent ne se départagent pas au hasard : cocher la
// mauvaise est une erreur silencieuse — la vraie reste à faire et il la croit
// réglée. On remonte donc les candidates et Raoul pose la question, exactement
// comme pour deux personnes qui portent le même prénom.
func (t *userToolbox) findTodo(ctx context.Context, query string) (store.Todo, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return store.Todo{}, errors.New("dis-moi de quelle tâche il s'agit")
	}
	todos, err := t.srv.store.Todos(ctx, t.user.ID, store.TodoQuery{Search: query, Undated: true})
	if err != nil {
		return store.Todo{}, err
	}
	switch len(todos) {
	case 0:
		return store.Todo{}, fmt.Errorf("aucune tâche de sa liste ne correspond à « %s »", query)
	case 1:
		return todos[0], nil
	}

	// Un titre qui correspond exactement tranche sans qu'on ait à demander.
	var exact []store.Todo
	for _, todo := range todos {
		if strings.EqualFold(strings.TrimSpace(todo.Title), query) {
			exact = append(exact, todo)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}

	choices := make([]string, 0, len(todos))
	for _, todo := range todos {
		choices = append(choices, todo.Title)
	}
	return store.Todo{}, &assistant.AmbiguousError{Quoi: "tâche", Recherche: query, Choix: choices}
}

func (t *userToolbox) todoView(todo store.Todo) assistant.TodoView {
	view := assistant.TodoView{
		ID:    todo.ID.Hex(),
		Titre: todo.Title,
		Note:  todo.Note,
		Faite: todo.Done,
	}
	if todo.Due != nil {
		now := time.Now().In(t.location())
		view.Echeance = assistant.Due(*todo.Due, now, t.location(), todo.Timed)
		view.EnRetard = !todo.Done && todo.Due.Before(startOfDay(now))
	}
	if todo.Source != nil {
		view.Source = sourceLabel(*todo.Source)
	}
	return view
}

// sourceLabel met la provenance en une ligne : « mail de Cyril », « daw ».
func sourceLabel(src store.TodoSource) string {
	switch {
	case src.Origine != "" && src.De != "":
		return src.Origine + " de " + src.De
	case src.Origine != "":
		return src.Origine
	case src.De != "":
		return src.De
	}
	return src.Titre
}

// todoAction prévient l'app qu'une tâche a bougé. Elle n'écrit rien sur le
// téléphone : elle sert à rafraîchir la section « À faire » et à laisser une
// trace dans l'échange.
func todoAction(view assistant.TodoView, state string) store.Action {
	return store.Action{
		Type: "todo",
		Payload: map[string]any{
			"id":    view.ID,
			"title": view.Titre,
			"when":  view.Echeance,
			"state": state,
		},
	}
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
