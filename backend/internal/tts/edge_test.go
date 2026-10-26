package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEdgeSpeakWith(t *testing.T) {
	var recu map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/parler" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&recu)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("mp3"))
	}))
	defer srv.Close()

	e := NewEdge(srv.URL+"/", "")
	body, err := e.Speak(context.Background(), "Salut")
	if err != nil {
		t.Fatal(err)
	}
	audio, _ := io.ReadAll(body)
	body.Close()
	if string(audio) != "mp3" || recu["texte"] != "Salut" || recu["voix"] != DefaultEdgeVoice {
		t.Fatalf("requête ou son inattendus : %q %v", audio, recu)
	}
	if _, err := e.SpeakWith(context.Background(), "fr-FR-HenriNeural", "Salut"); err != nil || recu["voix"] != "fr-FR-HenriNeural" {
		t.Fatalf("la voix demandée doit passer : %v %v", err, recu)
	}
}

func TestEdgeDisabled(t *testing.T) {
	if NewEdge("", "").Enabled() {
		t.Fatal("sans service, la voix gratuite est éteinte")
	}
	if _, err := NewEdge("", "").Speak(context.Background(), "x"); err != ErrDisabled {
		t.Fatalf("ErrDisabled attendu : %v", err)
	}
}
