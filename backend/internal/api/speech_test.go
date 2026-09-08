package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/config"
)

// AVPlayer ouvre plusieurs requêtes sur la même URL (sondage, puis plages
// d'octets). Le ticket doit donc rester valable tant qu'il n'a pas expiré.
func TestSpeechTicketReusableUntilExpiry(t *testing.T) {
	tickets := newSpeechTickets()
	user := bson.NewObjectID()

	id, err := tickets.issue(user, "Trois mails, dont deux qui comptent.")
	if err != nil {
		t.Fatalf("issue : %v", err)
	}

	entry, ok := tickets.lookup(id)
	if !ok {
		t.Fatal("le ticket devrait être valide")
	}
	if entry.userID != user || entry.text != "Trois mails, dont deux qui comptent." {
		t.Errorf("contenu du ticket inattendu : %+v", entry)
	}

	if _, ok := tickets.lookup(id); !ok {
		t.Error("le ticket devrait survivre à une deuxième requête")
	}
}

// Le son fabriqué est gardé avec le ticket : les plages d'octets demandées
// ensuite ne doivent relancer — ni refacturer — aucune synthèse.
func TestSpeechTicketSynthesizesOnce(t *testing.T) {
	tickets := newSpeechTickets()
	id, err := tickets.issue(bson.NewObjectID(), "bonjour")
	if err != nil {
		t.Fatalf("issue : %v", err)
	}
	entry, ok := tickets.lookup(id)
	if !ok {
		t.Fatal("ticket introuvable")
	}

	synth := &countingSynth{audio: "des octets mp3"}
	for range 3 { // le lecteur sonde, puis lit par plages
		entry.synthesize(synth)
	}
	<-entry.ready

	if entry.err != nil {
		t.Fatalf("synthèse : %v", entry.err)
	}
	if string(entry.audio) != "des octets mp3" {
		t.Errorf("audio : %q", entry.audio)
	}
	if n := synth.calls.Load(); n != 1 {
		t.Errorf("%d appels à ElevenLabs, attendu 1", n)
	}
}

// Une connexion refermée par le lecteur ne doit pas emporter la synthèse avec
// elle : c'est ce qui obligeait la requête suivante à tout recommencer.
func TestSpeechSynthesisSurvivesClientCancel(t *testing.T) {
	tickets := newSpeechTickets()
	id, err := tickets.issue(bson.NewObjectID(), "bonjour")
	if err != nil {
		t.Fatalf("issue : %v", err)
	}
	entry, _ := tickets.lookup(id)

	ctx, cancel := context.WithCancel(context.Background())
	synth := &countingSynth{audio: "des octets mp3", waitFor: ctx}
	entry.synthesize(synth)
	cancel() // le lecteur raccroche pendant la synthèse

	select {
	case <-entry.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("la synthèse ne s'est jamais terminée")
	}
	if entry.err != nil {
		t.Fatalf("la synthèse a été annulée avec la requête : %v", entry.err)
	}
	if string(entry.audio) != "des octets mp3" {
		t.Errorf("audio : %q", entry.audio)
	}
}

// countingSynth compte les allers-retours et, si waitFor est posé, ne rend la
// main qu'une fois ce contexte terminé — de quoi simuler une synthèse encore
// en cours quand le lecteur referme sa connexion.
type countingSynth struct {
	audio   string
	calls   atomic.Int32
	waitFor context.Context
}

func (c *countingSynth) Speak(ctx context.Context, text string) (io.ReadCloser, error) {
	c.calls.Add(1)
	if c.waitFor != nil {
		<-c.waitFor.Done()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(c.audio)), nil
}

func TestSpeechTicketExpires(t *testing.T) {
	tickets := newSpeechTickets()
	id, err := tickets.issue(bson.NewObjectID(), "bonjour")
	if err != nil {
		t.Fatalf("issue : %v", err)
	}

	tickets.mu.Lock()
	tickets.items[id].expires = time.Now().Add(-time.Second)
	tickets.mu.Unlock()

	if _, ok := tickets.lookup(id); ok {
		t.Error("un ticket périmé ne doit pas être accepté")
	}
}

func TestSpeechTicketUnknown(t *testing.T) {
	if _, ok := newSpeechTickets().lookup("inconnu"); ok {
		t.Error("un ticket inconnu ne doit pas être accepté")
	}
}

func TestSpeechStreamRouting(t *testing.T) {
	srv := NewServer(config.Config{}, nil, nil, nil)

	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/speech/inconnu", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("statut %d, attendu 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "expirée") {
		t.Errorf("corps inattendu : %s", rec.Body.String())
	}
}
