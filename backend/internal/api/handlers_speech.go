package api

import (
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

// speechTickets relie une URL jetable au texte à prononcer.
//
// Le lecteur audio du téléphone ne sait que faire un GET : il ne peut pas
// poster la réponse de Raoul, et faire passer celle-ci en paramètre d'URL
// reviendrait à écrire le contenu de ses mails dans les journaux d'accès.
// L'app poste donc le texte, reçoit un ticket aléatoire à usage unique, et le
// lecteur va chercher le son à cette adresse.
type speechTickets struct {
	mu    sync.Mutex
	items map[string]speechTicket
}

type speechTicket struct {
	userID  bson.ObjectID
	text    string
	expires time.Time
}

func newSpeechTickets() *speechTickets {
	return &speechTickets{items: map[string]speechTicket{}}
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
	// Deux minutes : le lecteur enchaîne dans la seconde, un ticket qui traîne
	// est un ticket qui ne servira plus.
	t.items[id] = speechTicket{userID: userID, text: text, expires: now.Add(2 * time.Minute)}
	return id, nil
}

func (t *speechTickets) consume(id string) (speechTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.items[id]
	if !ok || time.Now().After(entry.expires) {
		delete(t.items, id)
		return speechTicket{}, false
	}
	delete(t.items, id) // usage unique
	return entry, true
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

// handleSpeechStream diffuse l'audio. Pas de vérification de token ici : le
// ticket EST l'autorisation — aléatoire, à usage unique, périmé en deux
// minutes. C'est ce qui permet au lecteur audio natif d'aller le chercher
// sans savoir poser d'en-tête.
func (s *Server) handleSpeechStream(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.speech.consume(chi.URLParam(r, "ticket"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "lecture expirée")
		return
	}

	body, err := s.tts.Speak(r.Context(), entry.text)
	if err != nil {
		slog.Warn("synthèse vocale en échec", "err", err)
		httpx.Error(w, http.StatusBadGateway, "synthèse vocale indisponible")
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", tts.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	// Recopie par petits blocs, vidés au fur et à mesure : le téléphone doit
	// pouvoir commencer à jouer pendant qu'ElevenLabs fabrique la suite.
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 8<<10)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return // le téléphone a coupé (arrêt de la lecture)
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				slog.Warn("flux audio interrompu", "err", readErr)
			}
			return
		}
	}
}
