package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// fauxClaude répond à /v1/messages avec ce texte, comme le ferait l'API.
func fauxClaude(t *testing.T, texte string) *piloteDirect {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": DefaultPiloteModele,
			"stop_reason": "end_turn",
			"content":     []map[string]any{{"type": "text", "text": texte}},
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	return &piloteDirect{
		client: anthropic.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL)),
		modele: DefaultPiloteModele,
	}
}

func TestPiloteDirectRepond(t *testing.T) {
	p := fauxClaude(t, `{"repondre":true,"reponse":" Oui, ça va bien, merci ! "}`)
	reply, ok, err := p.repondre(context.Background(), "consigne", "ça va ?")
	if err != nil || !ok || reply != "Oui, ça va bien, merci !" {
		t.Fatalf("réponse directe attendue : %q %v %v", reply, ok, err)
	}
}

// Ce qui demande l'équipe part à l'atelier.
func TestPiloteDirectPasseLaMain(t *testing.T) {
	p := fauxClaude(t, `{"repondre":false,"reponse":""}`)
	if _, ok, err := p.repondre(context.Background(), "consigne", "où en est la CI de neurorun ?"); ok || err != nil {
		t.Fatalf("la demande devait partir à l'atelier : %v %v", ok, err)
	}
	// Une réponse vide ne vaut pas réponse, même si le modèle dit l'inverse.
	p = fauxClaude(t, `{"repondre":true,"reponse":"  "}`)
	if _, ok, _ := p.repondre(context.Background(), "consigne", "ça va ?"); ok {
		t.Fatal("une réponse vide ne doit pas être rendue")
	}
}
