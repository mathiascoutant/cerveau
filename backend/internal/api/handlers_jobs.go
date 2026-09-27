package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/tuleap"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// L'onglet Jobs.
//
// Deux cartes : CSP, qui ouvre les tickets Tuleap, et Flight Schedule, qui
// n'est pas encore branché et le dit. Le serveur décrit les deux, pour que
// l'app n'ait pas à deviner si CSP est utilisable : il ne l'est que si
// l'instance est configurée ET que l'utilisateur a saisi sa clé.

// Durée de vie du cache des tickets. Une minute : assez pour que l'ouverture
// de la vue, le retour arrière et la réouverture ne repartent pas trois fois
// vers Tuleap, assez court pour que le rafraîchissement manuel ait un sens.
const cspCacheTTL = time.Minute

type jobCard struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Enabled : la carte s'ouvre. Sinon Reason dit pourquoi.
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
	// Configured : le serveur connaît la source (Tuleap configuré) ; il peut
	// manquer la clé de l'utilisateur.
	Configured bool `json:"configured"`
	// Connected : une clé est utilisable — la sienne, ou celle du serveur.
	Connected bool `json:"connected"`
	// ServerKey : c'est la clé posée sur le serveur qui sert, pas la sienne.
	ServerKey bool `json:"server_key,omitempty"`
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	csp := jobCard{
		Key:         "csp",
		Title:       "CSP",
		Description: "Accès à mes tickets CSP Tuleap",
		Configured:  s.cfg.TuleapEnabled(),
	}
	if creds, err := s.tuleapCreds(r.Context(), user); err == nil {
		csp.Connected = true
		csp.ServerKey = creds.Name == serverKeyLabel
	}
	switch {
	case !csp.Configured:
		csp.Reason = "Tuleap n'est pas configuré sur le serveur (TULEAP_URL)."
	case !csp.Connected:
		csp.Reason = "Saisis ta clé d'accès Tuleap dans l'onglet Accès."
	default:
		csp.Enabled = true
	}
	flights := jobCard{
		Key:         "flight_schedule",
		Title:       "Flight Schedule",
		Description: "Accès au planning des vols",
		Reason:      "Bientôt disponible.",
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"jobs": []jobCard{csp, flights}})
}

type cspListResponse struct {
	Tickets     []tuleap.Ticket `json:"tickets"`
	GeneratedAt time.Time       `json:"generated_at"`
	// Scope : ce qui a été demandé à Tuleap, pour que l'écran puisse dire d'où
	// vient la liste (tracker, sélection).
	Scope cspScope `json:"scope"`
	// Cached : la liste vient du cache, pas d'un appel à l'instant.
	Cached bool `json:"cached"`
}

type cspScope struct {
	BaseURL   string `json:"base_url"`
	TrackerID int    `json:"tracker_id"`
	// Mode : « pxfeed » (les cycles CSP, comme PXFeed-UI) ou « query ».
	Mode      string `json:"mode"`
	ReleaseID int    `json:"release_id,omitempty"`
	// Source : l'adresse Tuleap qui a rendu les artefacts, en mode pxfeed.
	Source       string           `json:"source,omitempty"`
	Airlines     []string         `json:"airlines,omitempty"`
	Stats        *tuleap.CSPStats `json:"stats,omitempty"`
	Query        string           `json:"query,omitempty"`
	ExpertQuery  string           `json:"expert_query,omitempty"`
	AssignedToMe bool             `json:"assigned_to_me"`
	User         string           `json:"user,omitempty"`
}

func (s *Server) handleCSPTickets(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if !s.cfg.TuleapEnabled() {
		httpx.Error(w, http.StatusNotImplemented, "Tuleap n'est pas configuré sur le serveur")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	refresh := r.URL.Query().Get("refresh") != ""
	list, creds, cached, err := s.cspList(ctx, user, refresh)
	if err != nil {
		httpx.Error(w, statusFor(err), err.Error())
		return
	}
	scope := cspScope{
		BaseURL: s.cfg.TuleapBaseURL, TrackerID: s.trackerID(),
		AssignedToMe: s.cfg.TuleapCSPAssignedToMe, User: creds.Name,
	}
	if s.cfg.TuleapGenericQuery() {
		scope.Mode = "query"
		scope.Query, scope.ExpertQuery = s.cfg.TuleapCSPQuery, s.cfg.TuleapCSPExpertQuery
	} else {
		scope.Mode = "pxfeed"
		scope.ReleaseID = s.releaseID()
		scope.Source = list.source
		scope.Airlines = s.cfg.TuleapCSPAirlines
		scope.Stats = list.stats
	}
	httpx.JSON(w, http.StatusOK, cspListResponse{
		Tickets:     list.tickets,
		GeneratedAt: time.Now(),
		Cached:      cached,
		Scope:       scope,
	})
}

func (s *Server) trackerID() int {
	if s.cfg.TuleapCSPTrackerID > 0 {
		return s.cfg.TuleapCSPTrackerID
	}
	return tuleap.DefaultCSPTrackerID
}

func (s *Server) releaseID() int {
	if s.cfg.TuleapCSPReleaseID > 0 {
		return s.cfg.TuleapCSPReleaseID
	}
	return tuleap.DefaultCSPReleaseID
}

func (s *Server) handleCSPTicket(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, err := strconv.Atoi(strings.TrimSpace(chi.URLParam(r, "id")))
	if err != nil || id <= 0 {
		httpx.Error(w, http.StatusBadRequest, "identifiant de ticket invalide")
		return
	}
	creds, err := s.tuleapCreds(r.Context(), user)
	if err != nil {
		httpx.Error(w, statusFor(err), err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ticket, comments, err := tuleap.New(s.cfg.TuleapBaseURL, creds.AccessKey).Ticket(ctx, id)
	if err != nil {
		httpx.Error(w, statusFor(err), err.Error())
		return
	}
	if comments == nil {
		comments = []tuleap.Commentaire{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ticket": ticket, "comments": comments})
}

// cspList est ce que le cache garde : la liste, et d'où elle vient.
type cspList struct {
	tickets []tuleap.Ticket
	source  string
	stats   *tuleap.CSPStats
}

// cspTickets rend les tickets du périmètre, depuis le cache quand il est frais.
func (s *Server) cspTickets(ctx context.Context, user *store.User, refresh bool) ([]tuleap.Ticket, store.TuleapCredentials, bool, error) {
	list, creds, cached, err := s.cspList(ctx, user, refresh)
	return list.tickets, creds, cached, err
}

// cspList lit Tuleap selon le mode configuré : la sélection « cycles CSP »
// de PXFeed par défaut, une requête explicite sinon.
func (s *Server) cspList(ctx context.Context, user *store.User, refresh bool) (cspList, store.TuleapCredentials, bool, error) {
	creds, err := s.tuleapCreds(ctx, user)
	if err != nil {
		return cspList{}, creds, false, err
	}
	if !refresh {
		if list, ok := s.csp.get(user.ID); ok {
			return list, creds, true, nil
		}
	}
	client := tuleap.New(s.cfg.TuleapBaseURL, creds.AccessKey)
	var list cspList
	if s.cfg.TuleapGenericQuery() {
		tickets, err := client.Tickets(ctx, tuleap.Scope{
			TrackerID:    s.trackerID(),
			Query:        s.cfg.TuleapCSPQuery,
			ExpertQuery:  s.cfg.TuleapCSPExpertQuery,
			AssignedToMe: s.cfg.TuleapCSPAssignedToMe,
			Limit:        200,
		})
		if err != nil {
			s.noteTuleapFailure(ctx, user, err)
			return cspList{}, creds, false, err
		}
		list.tickets = tickets
	} else {
		res, err := client.Cycles(ctx, tuleap.CSPScope{
			TrackerID:  s.trackerID(),
			ReleaseID:  s.releaseID(),
			PlanningID: s.cfg.TuleapCSPPlanningID,
			ProjectID:  s.cfg.TuleapCSPProjectID,
			Airlines:   s.cfg.TuleapCSPAirlines,
		})
		if err != nil {
			s.noteTuleapFailure(ctx, user, err)
			return cspList{}, creds, false, err
		}
		list.tickets, list.source = res.Tickets, res.Source
		stats := res.Stats
		list.stats = &stats
		if s.cfg.TuleapCSPAssignedToMe {
			list.tickets = assignedTo(list.tickets, creds)
		}
	}
	if list.tickets == nil {
		list.tickets = []tuleap.Ticket{}
	}
	s.csp.put(user.ID, list)
	return list, creds, false, nil
}

// assignedTo ne garde que les tickets dont le responsable est l'utilisateur de
// la clé — par son nom, la liste des cycles ne portant pas les identifiants.
func assignedTo(tickets []tuleap.Ticket, creds store.TuleapCredentials) []tuleap.Ticket {
	name := strings.ToLower(strings.TrimSpace(creds.Name))
	if name == "" || name == serverKeyLabel {
		return tickets
	}
	out := tickets[:0:0]
	for _, t := range tickets {
		if strings.Contains(strings.ToLower(t.Responsable), name) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) noteTuleapFailure(ctx context.Context, user *store.User, err error) {
	var apiErr *tuleap.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden) {
		s.store.MarkConnectionError(ctx, user.ID, store.ProviderTuleap, err.Error())
	}
}

func statusFor(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusPreconditionFailed
	}
	var apiErr *tuleap.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusUnauthorized
		case http.StatusNotFound:
			return http.StatusNotFound
		}
	}
	return http.StatusBadGateway
}

// --- cache --------------------------------------------------------------------

type cspCache struct {
	mu    sync.Mutex
	items map[string]cspEntry
}

type cspEntry struct {
	list cspList
	at   time.Time
}

func newCSPCache() *cspCache {
	return &cspCache{items: map[string]cspEntry{}}
}

func (c *cspCache) get(userID bson.ObjectID) (cspList, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[userID.Hex()]
	if !ok || time.Since(e.at) > cspCacheTTL {
		return cspList{}, false
	}
	return e.list, true
}

func (c *cspCache) put(userID bson.ObjectID, list cspList) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[userID.Hex()] = cspEntry{list: list, at: time.Now()}
}

func (c *cspCache) drop(userID bson.ObjectID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, userID.Hex())
}
