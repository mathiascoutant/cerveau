package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/tts"
)

// Un peu plus long qu'une réponse de Raoul : au-delà, ce n'est plus une phrase
// qu'on lui fait dire, c'est un texte qu'on lui fait facturer.
const maxSpeechChars = 4000

// Garde-fou mémoire : une réponse de Raoul pèse une centaine de kilooctets en
// mp3 128 kbps, soit quelques minutes de parole avant d'atteindre cette borne.
const maxSpeechBytes = 8 << 20

// speechTickets relie une URL jetable au texte à prononcer.
//
// Le lecteur audio du téléphone ne sait que faire un GET : il ne peut pas
// poster la réponse de Raoul, et faire passer celle-ci en paramètre d'URL
// reviendrait à écrire le contenu de ses mails dans les journaux d'accès.
// L'app poste donc le texte, reçoit un ticket aléatoire, et le lecteur va
// chercher le son à cette adresse.
//
// Le ticket n'est PAS à usage unique, et c'est délibéré : AVPlayer, côté iOS,
// ouvre plusieurs requêtes sur la même URL — une pour sonder le fichier, une
// ou plusieurs pour le lire par plages d'octets. Un ticket consommé au premier
// appel faisait échouer toutes les suivantes, et Raoul retombait silencieusement
// sur la voix système. Il reste borné dans le temps, et le son fabriqué est
// gardé avec lui pour ne pas resynthétiser à chaque plage demandée.
type speechTickets struct {
	mu    sync.Mutex
	items map[string]*speechTicket
}

type speechTicket struct {
	userID  bson.ObjectID
	text    string
	expires time.Time

	// audio : rempli à la première lecture, réutilisé par les suivantes.
	audio []byte
}

func newSpeechTickets() *speechTickets {
	return &speechTickets{items: map[string]*speechTicket{}}
}

func (t *speechTickets) issue(userID bson.ObjectID, text string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)

	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, v := range t.items { // purge opportuniste
		if now.After(v.expires) {
			delete(t.items, k)
		}
	}
	// Deux minutes : le temps que le lecteur charge et joue, pas davantage.
	// C'est aussi la durée pendant laquelle le son reste en mémoire.
	t.items[id] = &speechTicket{userID: userID, text: text, expires: now.Add(2 * time.Minute)}
	return id, nil
}

// lookup rend le ticket sans le retirer : plusieurs requêtes du même lecteur
// doivent aboutir.
func (t *speechTickets) lookup(id string) (*speechTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.items[id]
	if !ok || time.Now().After(entry.expires) {
		delete(t.items, id)
		return nil, false
	}
	return entry, true
}

// cache retient le son fabriqué pour la durée restante du ticket.
func (t *speechTickets) cache(id string, audio []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry, ok := t.items[id]; ok {
		entry.audio = audio
	}
}

// handleSpeak prépare la lecture d'un texte et renvoie l'URL à jouer.
func (s *Server) handleSpeak(w http.ResponseWriter, r *http.Request) {
	if !s.tts.Enabled() {
		// 501 et non 500 : l'app sait alors retomber sur la voix système
		// sans afficher d'erreur à l'utilisateur.
		httpx.Error(w, http.StatusNotImplemented, tts.ErrDisabled.Error())
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		httpx.Error(w, http.StatusBadRequest, "aucun texte à prononcer")
		return
	}
	if len(text) > maxSpeechChars {
		text = text[:maxSpeechChars]
	}

	user := userFrom(r.Context())
	ticket, err := s.speech.issue(user.ID, text)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "impossible de préparer la lecture")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"url":        "/api/v1/speech/" + ticket,
		"expires_in": 120,
	})
}

// handleSpeechStream renvoie l'audio. Pas de vérification de token ici : le
// ticket EST l'autorisation — aléatoire, périmé en deux minutes. C'est ce qui
// permet au lecteur audio natif d'aller le chercher sans savoir poser d'en-tête.
//
// Le son est fabriqué en entier avant d'être servi, et non relayé au fil de sa
// génération. On perd la demi-seconde d'avance que donnait la diffusion
// continue, mais on rend un vrai fichier : taille connue, plages d'octets
// acceptées, rejouable. AVPlayer refuse de lire un flux qui n'a ni longueur ni
// support des Range, et abandonne sans bruit — c'est exactement ce qui faisait
// retomber Raoul sur la voix du téléphone.
func (s *Server) handleSpeechStream(w http.ResponseWriter, r *http.Request) {
	ticket := chi.URLParam(r, "ticket")
	entry, ok := s.speech.lookup(ticket)
	if !ok {
		httpx.Error(w, http.StatusNotFound, "lecture expirée")
		return
	}

	audio := entry.audio
	if audio == nil {
		body, err := s.tts.Speak(r.Context(), entry.text)
		if err != nil {
			slog.Warn("synthèse vocale en échec", "err", err)
			httpx.Error(w, http.StatusBadGateway, "synthèse vocale indisponible")
			return
		}
		defer body.Close()

		audio, err = io.ReadAll(io.LimitReader(body, maxSpeechBytes))
		if err != nil {
			slog.Warn("lecture du flux ElevenLabs", "err", err)
			httpx.Error(w, http.StatusBadGateway, "synthèse vocale interrompue")
			return
		}
		s.speech.cache(ticket, audio)
	}

	w.Header().Set("Content-Type", tts.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	// ServeContent pose Content-Length, Accept-Ranges, et répond aux requêtes
	// partielles — tout ce dont AVPlayer a besoin.
	http.ServeContent(w, r, "raoul.mp3", time.Time{}, bytes.NewReader(audio))
}
