package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/tts"
)

// L'app fait entendre la présentation de Hugo avec ElevenLabs ; Cerveau HQ le
// fait parler avec sa voix gratuite (Rémy) : sa présentation sans texte,
// sinon le texte donné — une de ses réponses, ou ce qu'on veut lui faire
// dire. La veille, qui ne parle pas, n'a pas de voix à prêter.
func TestVoixMembre(t *testing.T) {
	srv := NewServer(config.Config{ElevenLabsAPIKey: "test", VoixPilote: "voix-de-hugo", VoixGratuiteURL: "http://voix.invalid"}, nil, nil, nil)
	srv.equipe = equipe.New(nil, nil, nil, equipe.Config{Voix: equipe.Voix{Pilote: srv.cfg.VoixPilote}})
	user := &store.User{ID: bson.NewObjectID()}

	call := func(agent, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/equipe/"+agent+"/voix", strings.NewReader(body))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", agent)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
		ctx = context.WithValue(ctx, userCtxKey, user)
		rec := httptest.NewRecorder()
		srv.handleVoixMembre(rec, req.WithContext(ctx))
		return rec
	}
	ticket := func(rec *httptest.ResponseRecorder) *speechTicket {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("statut %d : %s", rec.Code, rec.Body)
		}
		var out struct{ URL string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		entry, ok := srv.speech.lookup(strings.TrimPrefix(out.URL, "/api/v1/speech/"))
		if !ok {
			t.Fatalf("pas de ticket pour %q", out.URL)
		}
		return entry
	}

	if e := ticket(call(equipe.IDPilote, "")); !strings.HasPrefix(e.text, "Salut, c'est Hugo") || e.voice != "voix-de-hugo" {
		t.Errorf("présentation de Hugo inattendue : %q avec %q", e.text, e.voice)
	}
	if e := ticket(call(equipe.IDPilote, `{"texte":"Tout est en ordre."}`)); e.text != "Tout est en ordre." || e.voice != equipe.VoixGratuitePilote {
		t.Errorf("Hugo devait dire le texte donné avec Rémy : %q avec %q", e.text, e.voice)
	}
	if rec := call(equipe.IDVeilleMail, ""); rec.Code != http.StatusNotFound {
		t.Errorf("la veille n'a pas de voix : statut %d", rec.Code)
	}

	// VOIX=appareil : l'app, sans corps, reçoit son 501 et parle avec
	// l'iPhone ; HQ garde la voix de Rémy.
	srv.tts = tts.New("", "", "", "")
	if rec := call(equipe.IDPilote, ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("l'app attend un 501 sans voix serveur : statut %d", rec.Code)
	}
	if e := ticket(call(equipe.IDPilote, `{"texte":"Je m'en occupe."}`)); e.voice != equipe.VoixGratuitePilote {
		t.Errorf("HQ doit garder la voix de Rémy : %q", e.voice)
	}

	// Sans le service des voix gratuites, HQ reçoit le texte et le lit dans
	// le navigateur.
	srv.voixHQ = tts.NewEdge("", "")
	rec := call(equipe.IDPilote, `{"texte":""}`)
	var out struct{ URL, Texte string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.URL != "" || !strings.HasPrefix(out.Texte, "Salut, c'est Hugo") {
		t.Errorf("HQ attend le texte seul : %d %+v", rec.Code, out)
	}
}
