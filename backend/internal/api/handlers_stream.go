package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// La réponse en flux : POST /assistant/stream.
//
// Même entrée que /assistant/ask, mais la sortie est un flux d'événements
// (Server-Sent Events) : l'app voit les outils démarrer, le texte arriver mot
// à mot, puis la fin avec ses mesures. Une connexion coupée par le client —
// il a appuyé sur « stop » — annule le contexte, donc le modèle et les outils
// s'arrêtent avec elle : c'est ce qui rend l'interruption réelle et non
// cosmétique.
//
// Le format est volontairement plat : une ligne « data: {json} » par événement,
// et rien d'autre. Un ping toutes les quinze secondes empêche un proxy de
// fermer une connexion qu'un outil lent fait paraître muette.

const streamPing = 15 * time.Second

func (s *Server) handleAskStream(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		httpx.Error(w, http.StatusBadRequest, "aucune demande")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "flux non supporté par ce serveur")
		return
	}

	user := userFrom(r.Context())
	tz := req.Timezone
	if tz == "" {
		tz = user.Timezone
	}
	if tz == "" {
		tz = s.cfg.DefaultTimezone
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
	defer cancel()

	// Les événements du moteur arrivent sur la goroutine du moteur ; le ping
	// sur un minuteur. Un seul écrivain sur la réponse : tout passe par ce
	// canal, et l'écriture se fait ici.
	events := make(chan assistant.Event, 64)
	write := func(ev any) bool {
		raw, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: " + string(raw) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	history := s.history(ctx, user)
	request := s.request(ctx, user, req.Text, now, tz, history)

	done := make(chan struct{})
	var (
		result assistant.Result
		askErr error
	)
	go func() {
		defer close(done)
		result, askErr = s.engine.AskStream(ctx, s.toolbox(user), request, func(ev assistant.Event) {
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		})
	}()

	ping := time.NewTicker(streamPing)
	defer ping.Stop()
	alive := true
loop:
	for {
		select {
		case ev := <-events:
			if ev.Type == "done" {
				// La fin porte la voix : elle se fabrique dès maintenant, avant
				// même que le texte ait fini de traverser le réseau.
				if ev.Result != nil {
					ev.Result.Actions = nonNilActions(ev.Result.Actions)
				}
				if !write(doneEvent{Event: ev, SpeechURL: s.prepareSpeech(user.ID, result.Reply)}) {
					alive = false
				}
				continue
			}
			if !write(ev) {
				alive = false
				cancel()
			}
		case <-ping.C:
			if !write(map[string]string{"type": "ping"}) {
				alive = false
				cancel()
			}
		case <-done:
			break loop
		}
	}
	// Ce qui restait dans le canal après la fin : le « done » peut arriver
	// juste avant la fermeture.
	for {
		select {
		case ev := <-events:
			if alive && ev.Type == "done" {
				write(doneEvent{Event: ev, SpeechURL: s.prepareSpeech(user.ID, result.Reply)})
			}
			continue
		default:
		}
		break
	}

	if askErr != nil {
		if alive {
			write(map[string]string{"type": "error", "message": "Raoul n'a pas pu répondre : " + askErr.Error()})
		}
		return
	}
	// Une réponse interrompue n'est pas mémorisée : elle n'a jamais été
	// entendue, et la retrouver dans l'historique fausserait le fil.
	if alive {
		_ = s.store.SaveInteraction(context.WithoutCancel(r.Context()), store.Interaction{
			UserID:     user.ID,
			Transcript: req.Text,
			Reply:      result.Reply,
			Actions:    result.Actions,
		})
	}
}

// doneEvent : l'événement de fin, augmenté de l'adresse de la voix.
type doneEvent struct {
	assistant.Event
	SpeechURL string `json:"speech_url,omitempty"`
}

func nonNilActions(a []store.Action) []store.Action {
	if a == nil {
		return []store.Action{}
	}
	return a
}
