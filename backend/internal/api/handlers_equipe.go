package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/tts"
)

// L'espace perso, côté app : l'écran Projets et l'écran Équipe.

const persoDisabled = "aucun projet perso n'est suivi sur ce serveur (PROJETS_REGISTRE)"

// handleProjets rend le point d'ensemble sur ses projets. ?refresh=1 refait
// les relevés au lieu de prendre ceux de moins d'une minute.
func (s *Server) handleProjets(w http.ResponseWriter, r *http.Request) {
	if s.projets == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	etat, err := s.projets.Etat(ctx, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "relevé impossible : "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, etat)
}

// handleEquipe rend l'équipe et qui a la parole.
func (s *Server) handleEquipe(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	user := userFrom(r.Context())
	agent := s.interlocutor(r.Context(), user)
	if agent == "" {
		agent = equipe.IDRaoul
	}
	// L'écran Équipe de l'app sert à parler aux membres : la veille, qui ne
	// parle pas, n'y figure pas (elle est dans Cerveau HQ).
	var membres []equipe.Membre
	for _, m := range s.equipe.Roster() {
		if m.Veille == "" && !m.Pilote {
			membres = append(membres, m)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"membres":       membres,
		"interlocuteur": agent,
		"capacites":     s.projets.Capacites(),
	})
}

// handleInterlocuteur change d'interlocuteur depuis l'app — l'écran Équipe,
// ou le bouton qui rend la parole à Raoul.
func (s *Server) handleInterlocuteur(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	var req struct {
		Agent string `json:"agent"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	m, ok := s.equipe.Membre(strings.TrimSpace(req.Agent))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "membre de l'équipe inconnu")
		return
	}
	next := m.ID
	if next == equipe.IDRaoul {
		next = ""
	}
	user := userFrom(r.Context())
	if err := s.store.PasserLaParole(r.Context(), user.ID, next); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "changement d'interlocuteur impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"interlocuteur": m.ID})
}

// handleVoixMembre fait entendre la voix d'un membre : une phrase de
// présentation, pour savoir qui on entendra — ou, si le corps porte un texte,
// ce texte-là : Cerveau HQ fait ainsi lire à Hugo sa réponse, ou ce qu'on veut
// lui faire dire.
func (s *Server) handleVoixMembre(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	m, ok := s.equipe.Membre(chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "membre inconnu")
		return
	}
	if m.Voix == "" {
		httpx.Error(w, http.StatusNotFound, m.Nom+" n'a pas de voix")
		return
	}
	// Un corps vide est permis : c'est la présentation qu'on veut entendre.
	var req struct {
		Texte string `json:"texte"`
	}
	err := httpx.Decode(r, &req)
	sansCorps := errors.Is(err, io.EOF)
	if err != nil && !sansCorps {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	// L'app de l'iPhone n'envoie pas de corps : elle parle avec ElevenLabs,
	// et sans lui (VOIX=appareil, ou quota épuisé) elle attend un 501 pour
	// prendre la voix du téléphone. Cerveau HQ en envoie un, et parle avec les
	// voix gratuites de Microsoft — Hugo d'abord avec sa voix ElevenLabs ;
	// sans elles, il lit lui-même le texte rendu avec une voix du navigateur.
	var synth synthetiseur = s.tts
	voix := s.voiceOf(m.ID)
	if !sansCorps {
		synth, voix = s.voixDeHQ(m), ""
	}
	if !synth.Enabled() && sansCorps {
		httpx.Error(w, http.StatusNotImplemented, "synthèse vocale non configurée")
		return
	}
	user := userFrom(r.Context())
	text := strings.TrimSpace(req.Texte)
	if text == "" {
		text = m.Bonjour
	}
	if text == "" {
		text = "Salut, c'est " + m.Nom + ", " + strings.ToLower(m.Role) + "."
	}
	if !synth.Enabled() {
		httpx.JSON(w, http.StatusOK, map[string]any{"texte": text})
		return
	}
	url := s.prepareSpeechWith(synth, user.ID, text, voix)
	if url == "" {
		httpx.Error(w, http.StatusInternalServerError, "impossible de préparer la lecture")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"url": url, "texte": text})
}

// voixDeHQ : la voix d'un membre dans HQ. Hugo parle avec sa voix
// ElevenLabs, la plus naturelle, et passe à sa voix Microsoft quand le crédit
// gratuit est épuisé ou qu'ElevenLabs refuse ; les autres, avec leur voix
// Microsoft.
func (s *Server) voixDeHQ(m equipe.Membre) synthetiseur {
	var gratuite synthetiseur = s.voixHQ
	if m.VoixGratuite == "" {
		gratuite = tts.NewEdge("", "")
	}
	if !m.Pilote || s.elevenHQ == nil {
		return voixFixe{gratuite, m.VoixGratuite}
	}
	return voixEnRelais{
		premiere: voixFixe{s.elevenHQ, s.voiceOf(m.ID)},
		relais:   voixFixe{gratuite, m.VoixGratuite},
	}
}

// voixFixe : un synthétiseur et la voix qu'il prend, quelle que soit celle
// qu'on lui demande.
type voixFixe struct {
	synth synthetiseur
	voix  string
}

func (v voixFixe) Enabled() bool { return v.synth.Enabled() }
func (v voixFixe) Speak(ctx context.Context, text string) (io.ReadCloser, error) {
	return v.synth.SpeakWith(ctx, v.voix, text)
}
func (v voixFixe) SpeakWith(ctx context.Context, _, text string) (io.ReadCloser, error) {
	return v.Speak(ctx, text)
}

// voixEnRelais : la première voix, et la seconde quand la première se tait.
type voixEnRelais struct{ premiere, relais voixFixe }

func (v voixEnRelais) Enabled() bool { return v.premiere.Enabled() || v.relais.Enabled() }
func (v voixEnRelais) Speak(ctx context.Context, text string) (io.ReadCloser, error) {
	if v.premiere.Enabled() {
		rc, err := v.premiere.Speak(ctx, text)
		if err == nil {
			return rc, nil
		}
		slog.Warn("voix de HQ : relais", "err", err)
	}
	return v.relais.Speak(ctx, text)
}
func (v voixEnRelais) SpeakWith(ctx context.Context, _, text string) (io.ReadCloser, error) {
	return v.Speak(ctx, text)
}

// handleConnaissances rend ce que l'équipe sait, projet par projet.
func (s *Server) handleConnaissances(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	list, err := s.store.Connaissances(r.Context(), store.ConnaissanceQuery{
		Equipe:    equipe.EquipeInformatique,
		Projet:    r.URL.Query().Get("projet"),
		Recherche: r.URL.Query().Get("q"),
		Limit:     300,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture impossible")
		return
	}
	if list == nil {
		list = []store.Connaissance{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"connaissances": list})
}

// handleOublierConnaissance retire une fiche, au doigt.
func (s *Server) handleOublierConnaissance(w http.ResponseWriter, r *http.Request) {
	if s.equipe == nil {
		httpx.Error(w, http.StatusNotImplemented, persoDisabled)
		return
	}
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	if err := s.store.SupprimerConnaissance(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "fiche introuvable")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "suppression impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"deleted": id.Hex()})
}
