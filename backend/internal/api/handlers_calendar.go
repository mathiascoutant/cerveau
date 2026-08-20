package api

import (
	"net/http"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

type calendarSyncRequest struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Events []struct {
		ID       string    `json:"id"`
		Calendar string    `json:"calendar"`
		Title    string    `json:"title"`
		Location string    `json:"location"`
		Start    time.Time `json:"start"`
		End      time.Time `json:"end"`
		AllDay   bool      `json:"all_day"`
	} `json:"events"`
}

// handleCalendarSync reçoit le miroir du calendrier iOS.
//
// Le backend ne parle pas directement au calendrier : c'est l'app qui lit
// EventKit et pousse la fenêtre courante. Ça évite tout OAuth calendrier et
// couvre d'un coup tous les comptes agrégés sur le téléphone (iCloud, Google,
// Exchange…).
func (s *Server) handleCalendarSync(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req calendarSyncRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if req.From.IsZero() || req.To.IsZero() || !req.To.After(req.From) {
		httpx.Error(w, http.StatusBadRequest, "fenêtre de synchronisation invalide")
		return
	}

	events := make([]store.CalendarEvent, 0, len(req.Events))
	for _, e := range req.Events {
		if e.ID == "" || e.Start.IsZero() {
			continue
		}
		end := e.End
		if end.IsZero() || end.Before(e.Start) {
			end = e.Start.Add(time.Hour)
		}
		events = append(events, store.CalendarEvent{
			ExternalID: e.ID,
			Calendar:   e.Calendar,
			Title:      e.Title,
			Location:   e.Location,
			Start:      e.Start,
			End:        end,
			AllDay:     e.AllDay,
		})
	}

	if err := s.store.ReplaceCalendarWindow(r.Context(), user.ID, req.From, req.To, events); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "synchronisation impossible")
		return
	}
	if err := s.store.UpsertConnection(r.Context(), store.Connection{
		UserID: user.ID, Provider: store.ProviderCalendar,
		Status: "connected", Label: "Calendrier iOS",
	}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"synced": len(events)})
}
