package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// La liste à faire, côté app. Raoul l'alimente à la voix ; ces routes servent
// à la lire, à cocher d'un doigt, et à y verser une ligne de la liste
// « à traiter » sans avoir à la lui dicter.

// handleListTodos rend tout ce qui reste à faire, plus ce qui vient d'être
// coché. Faire disparaître une tâche à l'instant où on la coche prive du seul
// retour qui compte — voir la ligne se barrer — et rend le geste irrattrapable
// quand on s'est trompé de ligne.
func (s *Server) handleListTodos(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	todos, err := s.store.Todos(r.Context(), user.ID, store.TodoQuery{
		Undated:   true,
		DoneSince: time.Now().Add(-todoDoneWindow),
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture de la liste impossible")
		return
	}
	if todos == nil {
		todos = []store.Todo{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"todos": todos})
}

type todoRequest struct {
	Title string `json:"title"`
	Note  string `json:"note"`
	// Due : jour retenu, « 2026-08-27 » ou avec l'heure. Vide = pas de date.
	Due    string `json:"due"`
	Source *struct {
		Origine string `json:"origine"`
		De      string `json:"de"`
		Titre   string `json:"titre"`
	} `json:"source"`
}

func (s *Server) handleCreateTodo(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	var req todoRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		httpx.Error(w, http.StatusBadRequest, "titre manquant")
		return
	}

	loc := s.location(user)
	due, timed, err := assistant.ParseDue(req.Due, loc)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	todo := store.Todo{
		UserID: user.ID,
		Title:  strings.TrimSpace(req.Title),
		Note:   strings.TrimSpace(req.Note),
		Timed:  timed,
	}
	if !due.IsZero() {
		todo.Due = &due
	}
	if req.Source != nil {
		todo.Source = &store.TodoSource{
			Origine: req.Source.Origine,
			De:      req.Source.De,
			Titre:   req.Source.Titre,
		}
	}

	saved, err := s.store.SaveTodo(r.Context(), todo)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

// handleUpdateTodo coche, décoche ou reprogramme.
//
// Les deux champs sont des pointeurs : absents, ils ne touchent à rien. C'est
// ce qui permet de cocher une tâche sans renvoyer sa date, et de la déplacer
// sans dire si elle est faite.
func (s *Server) handleUpdateTodo(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req struct {
		Done *bool `json:"done"`
		// Due : chaîne vide pour retirer la date, absent pour ne pas y toucher.
		Due *string `json:"due"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if req.Done == nil && req.Due == nil {
		httpx.Error(w, http.StatusBadRequest, "rien à modifier")
		return
	}

	var (
		updated store.Todo
		err     error
	)
	if req.Due != nil {
		due, timed, perr := assistant.ParseDue(*req.Due, s.location(user))
		if perr != nil {
			httpx.Error(w, http.StatusBadRequest, perr.Error())
			return
		}
		var when *time.Time
		if !due.IsZero() {
			when = &due
		}
		updated, err = s.store.RescheduleTodo(r.Context(), user.ID, id, when, timed)
	}
	if err == nil && req.Done != nil {
		updated, err = s.store.SetTodoDone(r.Context(), user.ID, id, *req.Done)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "cette tâche n'existe plus")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "modification impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := s.store.DeleteTodo(r.Context(), user.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "cette tâche n'existe plus")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "suppression impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"deleted": id.Hex()})
}

func pathID(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bool) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return bson.ObjectID{}, false
	}
	return id, true
}

// location : le fuseau de l'utilisateur, celui du serveur à défaut. Un jour
// retenu est un jour vécu quelque part — « jeudi » commence à minuit chez lui,
// pas à minuit UTC.
func (s *Server) location(user *store.User) *time.Location {
	tz := user.Timezone
	if tz == "" {
		tz = s.cfg.DefaultTimezone
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}
