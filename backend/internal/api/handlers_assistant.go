package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

type askRequest struct {
	Text     string    `json:"text"`
	Now      time.Time `json:"now"`
	Timezone string    `json:"timezone"`
}

type askResponse struct {
	Transcript string         `json:"transcript"`
	Reply      string         `json:"reply"`
	Actions    []store.Action `json:"actions"`
	Steps      []string       `json:"steps,omitempty"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
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
	s.respondToPrompt(w, r, req.Text, req)
}

// handleVoice est le chemin de secours quand la reconnaissance vocale native
// n'est pas disponible : l'app envoie l'audio brut, on transcrit ici.
func (s *Server) handleVoice(w http.ResponseWriter, r *http.Request) {
	if !s.stt.Enabled() {
		httpx.Error(w, http.StatusNotImplemented,
			"transcription serveur désactivée : l'app doit utiliser la reconnaissance vocale native")
		return
	}
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		httpx.Error(w, http.StatusBadRequest, "envoi audio invalide")
		return
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "fichier audio manquant (champ « audio »)")
		return
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	transcript, err := s.stt.Transcribe(ctx, file, header.Filename, "fr")
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		httpx.JSON(w, http.StatusOK, askResponse{Reply: "Je n'ai rien entendu."})
		return
	}

	req := askRequest{
		Text:     transcript,
		Timezone: r.FormValue("timezone"),
	}
	s.respondToPrompt(w, r, transcript, req)
}

func (s *Server) respondToPrompt(w http.ResponseWriter, r *http.Request, transcript string, req askRequest) {
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

	// Contexte conversationnel. L'app garde désormais la conversation ouverte
	// après le premier « OK Raoul » : les tours s'enchaînent sans réveil, et
	// deux échanges de mémoire ne suffisaient plus à suivre un fil — ni même à
	// se souvenir de quel Cyril on vient de parler.
	//
	// Ce qui déborde de cette fenêtre n'est pas perdu : l'outil
	// chercher_historique va le rechercher en base quand il renvoie à une
	// conversation plus ancienne.
	var history []assistant.Turn
	if past, err := s.store.RecentInteractions(r.Context(), user.ID, 14); err == nil {
		for i := len(past) - 1; i >= 0; i-- {
			history = append(history, assistant.Turn{
				User:      past[i].Transcript,
				Assistant: past[i].Reply,
			})
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
	defer cancel()

	name := user.Name
	if name == "" {
		name = s.cfg.DefaultUserName
	}

	// Son adresse vient de la connexion Gandi : c'est elle qui lui permet de se
	// reconnaître parmi les destinataires d'un mail, et de signer une réponse.
	var email string
	if creds, err := s.gandiCreds(ctx, user); err == nil {
		email = creds.Email
	}

	result, err := s.engine.Ask(ctx, s.toolbox(user), assistant.Request{
		Text:      transcript,
		Now:       now,
		Timezone:  tz,
		UserName:  name,
		UserEmail: email,
		History:   history,
		Sources:   s.sources(ctx, user),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "Raoul n'a pas pu répondre : "+err.Error())
		return
	}

	_ = s.store.SaveInteraction(r.Context(), store.Interaction{
		UserID:     user.ID,
		Transcript: transcript,
		Reply:      result.Reply,
		Actions:    result.Actions,
	})

	if result.Actions == nil {
		result.Actions = []store.Action{}
	}
	httpx.JSON(w, http.StatusOK, askResponse{
		Transcript: transcript,
		Reply:      result.Reply,
		Actions:    result.Actions,
		Steps:      result.Steps,
	})
}

// sources dit quels comptes sont branchés, pour que le modèle n'en reçoive ni
// les outils ni le nom. Un compte jamais configuré ne doit pas se transformer
// en outil qui échoue : le modèle rapporterait l'échec à voix haute, et
// « je n'ai rien trouvé sur WhatsApp » à quelqu'un qui n'a pas WhatsApp est
// une réponse à une question que personne n'a posée.
func (s *Server) sources(ctx context.Context, user *store.User) assistant.Sources {
	connected := func(provider string) bool {
		conn, err := s.store.Connection(ctx, user.ID, provider)
		return err == nil && conn.Status != "disconnected"
	}
	return assistant.Sources{
		Mail:     connected(store.ProviderGandi),
		Slack:    connected(store.ProviderSlack),
		WhatsApp: connected(store.ProviderWhatsApp),
	}
}
