package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
// fait parler aussi (Will, Andrew en relais) : sa présentation sans texte,
// sinon le texte donné — une de ses réponses, ou ce qu'on veut lui faire
// dire. La veille, qui ne parle pas, n'a pas de voix à prêter.
func TestVoixMembre(t *testing.T) {
	srv := NewServer(config.Config{ElevenLabsAPIKey: "test", VoixPilote: "voix-de-hugo", VoixGratuiteURL: "http://voix.invalid"}, nil, nil, nil)
	srv.equipe = equipe.New(nil, nil, nil, equipe.Config{Voix: equipe.Voix{Pilote: srv.cfg.VoixPilote}})
	// Pas d'appel à ElevenLabs depuis les tests : le relais se teste à part.
	srv.elevenHQ = tts.New("", "", "", "")
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
	if e := ticket(call(equipe.IDPilote, `{"texte":"Tout est en ordre."}`)); e.text != "Tout est en ordre." {
		t.Errorf("Hugo devait dire le texte donné : %q", e.text)
	}
	if rec := call(equipe.IDVeilleMail, ""); rec.Code != http.StatusNotFound {
		t.Errorf("la veille n'a pas de voix : statut %d", rec.Code)
	}

	// VOIX=appareil : l'app, sans corps, reçoit son 501 et parle avec
	// l'iPhone ; HQ garde une voix.
	srv.tts = tts.New("", "", "", "")
	if rec := call(equipe.IDPilote, ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("l'app attend un 501 sans voix serveur : statut %d", rec.Code)
	}
	if e := ticket(call(equipe.IDPilote, `{"texte":"Je m'en occupe."}`)); e.text != "Je m'en occupe." {
		t.Errorf("HQ doit garder une voix : %q", e.text)
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

// Hugo parle dans HQ avec Will (ElevenLabs) ; quand ElevenLabs refuse —
// crédit gratuit épuisé —, Andrew (Microsoft) prend le relais. Les autres
// gardent leur voix Microsoft.
func TestVoixDeHQ(t *testing.T) {
	srv := NewServer(config.Config{ElevenLabsAPIKey: "cle", VoixGratuiteURL: "http://voix.invalid"}, nil, nil, nil)
	srv.equipe = equipe.New(nil, nil, nil, equipe.Config{})
	hugo, _ := srv.equipe.Membre(equipe.IDPilote)
	leo, _ := srv.equipe.Membre(equipe.IDIngenieur)

	relais, ok := srv.voixDeHQ(hugo).(voixEnRelais)
	if !ok || relais.premiere.voix != equipe.VoixPilote || relais.relais.voix != equipe.VoixGratuitePilote {
		t.Fatalf("Hugo : Will puis Andrew attendus, eu %+v", srv.voixDeHQ(hugo))
	}
	if v, ok := srv.voixDeHQ(leo).(voixFixe); !ok || v.voix != equipe.VoixGratuiteIngenieur {
		t.Errorf("Léo garde sa voix Microsoft : %+v", srv.voixDeHQ(leo))
	}

	will := &fausseVoix{err: errors.New("quota_exceeded")}
	andrew := &fausseVoix{}
	v := voixEnRelais{premiere: voixFixe{will, "will"}, relais: voixFixe{andrew, "andrew"}}
	rc, err := v.Speak(context.Background(), "Salut !")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if will.voix != "will" || andrew.voix != "andrew" {
		t.Errorf("le relais n'a pas pris Andrew : will=%q andrew=%q", will.voix, andrew.voix)
	}
	will.err, andrew.voix = nil, ""
	if rc, err := v.Speak(context.Background(), "Salut !"); err != nil || andrew.voix != "" {
		t.Errorf("Will répond : pas de relais (%v, %q)", err, andrew.voix)
	} else {
		rc.Close()
	}
}

type fausseVoix struct {
	err  error
	voix string
}

func (f *fausseVoix) Enabled() bool { return true }
func (f *fausseVoix) Speak(ctx context.Context, text string) (io.ReadCloser, error) {
	return f.SpeakWith(ctx, "", text)
}
func (f *fausseVoix) SpeakWith(_ context.Context, voix, _ string) (io.ReadCloser, error) {
	f.voix = voix
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader("mp3")), nil
}
