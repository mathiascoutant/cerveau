package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// Le son fabriqué est gardé avec le ticket : une plage d'octets demandée après
// coup ne doit pas relancer — ni refacturer — une synthèse.
func TestSpeechTicketCachesAudio(t *testing.T) {
	tickets := newSpeechTickets()
	id, err := tickets.issue(bson.NewObjectID(), "bonjour")
	if err != nil {
		t.Fatalf("issue : %v", err)
	}

	tickets.cache(id, []byte("des octets mp3"))

	entry, ok := tickets.lookup(id)
	if !ok {
		t.Fatal("ticket introuvable")
	}
	if string(entry.audio) != "des octets mp3" {
		t.Errorf("audio en cache : %q", entry.audio)
	}
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
	srv := NewServer(config.Config{}, nil, nil)

	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/speech/inconnu", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("statut %d, attendu 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "expirée") {
		t.Errorf("corps inattendu : %s", rec.Body.String())
	}
}
