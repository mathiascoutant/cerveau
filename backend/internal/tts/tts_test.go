package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// La requête part telle quelle chez ElevenLabs : une erreur de forme ne se voit
// qu'au moment où Raoul devrait parler. On la vérifie ici.
func TestSpeakRequest(t *testing.T) {
	var gotPath, gotKey, gotAccept string
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotKey = r.Header.Get("xi-api-key")
		gotAccept = r.Header.Get("Accept")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", ContentType)
		_, _ = w.Write([]byte("des octets mp3"))
	}))
	defer srv.Close()

	c := New("clé", "", "")
	c.baseURL = srv.URL

	stream, err := c.Speak(context.Background(), "Trois mails, dont deux qui comptent.")
	if err != nil {
		t.Fatalf("Speak : %v", err)
	}
	defer stream.Close()

	audio, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("lecture du flux : %v", err)
	}
	if string(audio) != "des octets mp3" {
		t.Errorf("flux audio inattendu : %q", audio)
	}

	if !strings.Contains(gotPath, "/text-to-speech/"+DefaultVoiceID+"/stream") {
		t.Errorf("chemin appelé : %s", gotPath)
	}
	if !strings.Contains(gotPath, "output_format="+outputFormat) {
		t.Errorf("format de sortie absent : %s", gotPath)
	}
	if gotKey != "clé" {
		t.Errorf("clé transmise : %q", gotKey)
	}
	if gotAccept != ContentType {
		t.Errorf("Accept : %q", gotAccept)
	}
	if body["text"] != "Trois mails, dont deux qui comptent." {
		t.Errorf("texte transmis : %v", body["text"])
	}
	if body["model_id"] != DefaultModel {
		t.Errorf("modèle transmis : %v", body["model_id"])
	}
	if _, ok := body["voice_settings"].(map[string]any); !ok {
		t.Errorf("voice_settings absent du corps : %v", body)
	}
}

// Une erreur ElevenLabs (quota, voix inconnue) doit remonter, pas produire un
// flux vide que le téléphone jouerait comme un silence.
func TestSpeakPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"quota exceeded"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New("clé", "", "")
	c.baseURL = srv.URL

	if _, err := c.Speak(context.Background(), "bonjour"); err == nil {
		t.Fatal("erreur attendue")
	}
}

func TestSpeakDisabled(t *testing.T) {
	if _, err := New("", "", "").Speak(context.Background(), "bonjour"); err != ErrDisabled {
		t.Fatalf("attendu ErrDisabled, obtenu %v", err)
	}
}
