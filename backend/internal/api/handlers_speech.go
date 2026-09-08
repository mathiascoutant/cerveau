package api

import (
	"bytes"
	"context"
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

// Temps laissé à une synthèse. Il est volontairement détaché de la requête qui
// la déclenche : voir speechTicket.synthesize.
const speechSynthesisTimeout = 60 * time.Second

// speechSynth est ce dont un ticket a besoin pour fabriquer son son.
// *tts.Client le satisfait ; les tests en posent un faux.
type speechSynth interface {
	Speak(ctx context.Context, text string) (io.ReadCloser, error)
}

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

	// start garantit une synthèse et une seule, quel que soit le nombre de
	// requêtes que le lecteur ouvre sur le ticket.
	start sync.Once
	// ready est fermé quand audio et err sont posés ; les lire avant est une
	// course, les lire après ne demande aucun verrou.
	ready chan struct{}
	audio []byte
	err   error
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
	t.items[id] = &speechTicket{
		userID:  userID,
		text:    text,
		expires: now.Add(2 * time.Minute),
		ready:   make(chan struct{}),
	}
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

// synthesize lance la fabrication du son si elle n'a pas déjà commencé.
//
// Deux choix tiennent cette fonction, et ce sont eux qui décident du délai
// avant que Raoul ouvre la bouche.
//
// Elle part d'un contexte à elle, pas de celui de la requête. AVPlayer ouvre,
// sonde et referme : la première connexion meurt souvent avant la fin de la
// synthèse. Adossée à la requête, celle-ci était annulée avec elle, rien
// n'était gardé, et la connexion suivante repartait de zéro — deux allers-
// retours ElevenLabs pour une phrase, facturés et attendus deux fois.
//
// Et elle ne se déclenche qu'une fois : les plages d'octets demandées ensuite
// attendent le même son au lieu d'en commander chacune un nouveau.
func (t *speechTicket) synthesize(synth speechSynth) {
	t.start.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), speechSynthesisTimeout)
			defer cancel()

			body, err := synth.Speak(ctx, t.text)
			if err != nil {
				t.err = err
				close(t.ready)
				return
			}
			defer body.Close()
			t.audio, t.err = io.ReadAll(io.LimitReader(body, maxSpeechBytes))
			close(t.ready)
		}()
	})
}

// prepareSpeech émet un ticket et met la synthèse en route sans attendre.
//
// C'est là que se joue le silence entre la fin de la réflexion et la première
// syllabe : le son se fabrique pendant que la réponse voyage jusqu'au
// téléphone et que celui-ci reprend la session audio, au lieu d'attendre que
// le lecteur réclame le premier octet. Rend "" quand il n'y a rien à dire ou
// pas de voix distante — l'app parle alors avec celle du téléphone.
func (s *Server) prepareSpeech(userID bson.ObjectID, text string) string {
	if !s.tts.Enabled() {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > maxSpeechChars {
		text = text[:maxSpeechChars]
	}

	id, err := s.speech.issue(userID, text)
	if err != nil {
		slog.Warn("émission du ticket de lecture", "err", err)
		return ""
	}
	if entry, ok := s.speech.lookup(id); ok {
		entry.synthesize(s.tts)
	}
	return "/api/v1/speech/" + id
}

// handleSpeak prépare la lecture d'un texte et renvoie l'URL à jouer.
//
// L'app n'a plus à passer par ici pour lire une réponse de Raoul — celle-ci
// arrive avec son ticket déjà émis. La route reste pour tout le reste : une
// phrase fabriquée côté app, un écran de diagnostic, un texte relu.
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
	if strings.TrimSpace(req.Text) == "" {
		httpx.Error(w, http.StatusBadRequest, "aucun texte à prononcer")
		return
	}

	user := userFrom(r.Context())
	url := s.prepareSpeech(user.ID, req.Text)
	if url == "" {
		httpx.Error(w, http.StatusInternalServerError, "impossible de préparer la lecture")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"url":        url,
		"expires_in": 120,
	})
}

// handleSpeechStream renvoie l'audio. Pas de vérification de token ici : le
// ticket EST l'autorisation — aléatoire, périmé en deux minutes. C'est ce qui
// permet au lecteur audio natif d'aller le chercher sans savoir poser d'en-tête.
//
// Le son est servi en entier, et non relayé au fil de sa génération. On perd la
// demi-seconde d'avance que donnait la diffusion continue — reprise ailleurs,
// en démarrant la synthèse dès que la réponse est écrite — mais on rend un vrai
// fichier : taille connue, plages d'octets acceptées, rejouable. AVPlayer refuse
// de lire un flux qui n'a ni longueur ni support des Range, et abandonne sans
// bruit — c'est exactement ce qui faisait retomber Raoul sur la voix du
// téléphone.
func (s *Server) handleSpeechStream(w http.ResponseWriter, r *http.Request) {
	ticket := chi.URLParam(r, "ticket")
	entry, ok := s.speech.lookup(ticket)
	if !ok {
		httpx.Error(w, http.StatusNotFound, "lecture expirée")
		return
	}

	// Presque toujours déjà en route. Le rappel couvre le ticket dont
	// l'émission n'aurait pas démarré la synthèse.
	entry.synthesize(s.tts)

	select {
	case <-entry.ready:
	case <-r.Context().Done():
		// Le lecteur a raccroché. La synthèse continue sans lui : c'est elle
		// que trouvera la connexion suivante, déjà faite.
		return
	}

	if entry.err != nil {
		slog.Warn("synthèse vocale en échec", "err", entry.err)
		httpx.Error(w, http.StatusBadGateway, "synthèse vocale indisponible")
		return
	}

	w.Header().Set("Content-Type", tts.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	// ServeContent pose Content-Length, Accept-Ranges, et répond aux requêtes
	// partielles — tout ce dont AVPlayer a besoin.
	http.ServeContent(w, r, "raoul.mp3", time.Time{}, bytes.NewReader(entry.audio))
}
