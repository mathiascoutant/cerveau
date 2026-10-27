package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/config"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Les schémas des outils de Cerveau passent tels quels à l'API.
func TestSchemaOutil(t *testing.T) {
	got := schemaOutil(map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"tache": map[string]any{"type": "string"}},
		"required":             []any{"tache"},
		"additionalProperties": false,
	})
	if len(got.Required) != 1 || got.Required[0] != "tache" {
		t.Errorf("required : %v", got.Required)
	}
	if got.ExtraFields["additionalProperties"] != false || got.Properties == nil {
		t.Errorf("schéma incomplet : %+v", got)
	}
}

// Le bâtiment montre à qui Hugo confie et qui il attend.
func TestLabelPilote(t *testing.T) {
	nomsAgents.Store("ingenieur", "Léo")
	noms := &sync.Map{}
	if got := labelPilote("confier_a_un_agent", `{"agent":"ingenieur"}`, noms); got != "Confie à Léo" {
		t.Errorf("confier : %q", got)
	}
	retenirNom("confier_a_un_agent", `{"tache":"abc","agent":"Léo","statut":"en_cours"}`, noms)
	if got := labelPilote("suivre_tache", `{"tache":"abc"}`, noms); got != "Attend Léo" {
		t.Errorf("suivre : %q", got)
	}
}

// fausseAPI rejoue des réponses de l'API Claude, dans l'ordre, et garde les
// requêtes reçues.
type fausseAPI struct {
	mu       sync.Mutex
	reponses []string
	recues   []map[string]any
	entetes  []http.Header
}

func (f *fausseAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recues = append(f.recues, req)
	f.entetes = append(f.entetes, r.Header.Clone())
	if len(f.reponses) == 0 {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"plus rien"}}`, http.StatusInternalServerError)
		return
	}
	rep := f.reponses[0]
	f.reponses = f.reponses[1:]
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, rep)
}

func message(stop string, content ...string) string {
	return `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"` + stop +
		`","usage":{"input_tokens":1,"output_tokens":1},"content":[` + strings.Join(content, ",") + `]}`
}

func serveurPiloteAPI(t *testing.T, f *fausseAPI) (*Server, *store.User, equipe.Membre, *liveRun) {
	t.Helper()
	st := runsStore(t)
	api := httptest.NewServer(f)
	t.Cleanup(api.Close)
	s := &Server{
		cfg:    config.Config{DefaultTimezone: "Europe/Paris"},
		store:  st,
		engine: assistant.New("cle", "", ""),
		equipe: equipe.New(assistant.New("cle", "", ""), nil, nil, equipe.Config{}),
		runs:   newOrchestre(st, 2, 10),
		pilotage: &piloteAPI{
			client: anthropic.NewClient(option.WithAPIKey("cle"), option.WithBaseURL(api.URL), option.WithMaxRetries(0)),
			modele: DefaultPiloteAPIModele,
			effort: DefaultPiloteAPIEffort,
		},
	}
	user := &store.User{ID: bson.NewObjectID(), Name: "Mathias"}
	moi, _ := s.equipe.Membre(equipe.IDPilote)
	lr := &liveRun{o: s.runs, run: store.AgentRun{ID: bson.NewObjectID(), UserID: user.ID, Agent: moi.ID}}
	return s, user, moi, lr
}

// Hugo appelle ses outils, plusieurs dans un même tour, puis répond. Ses
// étapes se voient dans le bâtiment ; la requête porte le modèle, l'effort,
// le cache et le repli des garde-fous.
func TestPiloterParAPI(t *testing.T) {
	f := &fausseAPI{reponses: []string{
		message("tool_use",
			`{"type":"tool_use","id":"tu_1","name":"suivre_tache","input":{"tache":"`+bson.NewObjectID().Hex()+`"}}`,
			`{"type":"tool_use","id":"tu_2","name":"suivre_tache","input":{"tache":"pas-un-id"}}`),
		message("end_turn", `{"type":"text","text":"Rien à suivre pour l'instant."}`),
	}}
	s, user, moi, lr := serveurPiloteAPI(t, f)

	res, err := s.piloterParAPI(t.Context(), user, moi, "Où en est l'équipe ?", lr)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "Rien à suivre pour l'instant." || res.Agent != moi.ID {
		t.Errorf("réponse : %+v", res)
	}
	if len(f.recues) != 2 {
		t.Fatalf("%d appels à l'API, attendu 2", len(f.recues))
	}
	premier := f.recues[0]
	if premier["model"] != DefaultPiloteAPIModele || premier["fallbacks"] != "default" {
		t.Errorf("modèle ou repli : %v %v", premier["model"], premier["fallbacks"])
	}
	if eff := premier["output_config"].(map[string]any)["effort"]; eff != "high" {
		t.Errorf("effort %v, attendu high", eff)
	}
	if premier["cache_control"] == nil || len(premier["system"].([]any)) != 2 {
		t.Error("cache absent ou consigne mal découpée")
	}
	if !strings.Contains(f.entetes[0].Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("en-tête beta : %q", f.entetes[0].Get("anthropic-beta"))
	}
	var noms []string
	for _, tool := range premier["tools"].([]any) {
		noms = append(noms, tool.(map[string]any)["name"].(string))
	}
	if !strings.Contains(strings.Join(noms, ","), "confier_a_un_agent") {
		t.Errorf("outils : %v", noms)
	}
	// Les deux résultats reviennent ensemble, dans l'ordre des appels, en
	// erreur : ni l'une ni l'autre tâche ne lui appartient.
	msgs := f.recues[1]["messages"].([]any)
	retour := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	if len(retour) != 2 || retour[0].(map[string]any)["tool_use_id"] != "tu_1" || retour[1].(map[string]any)["is_error"] != true {
		t.Errorf("résultats des outils : %v", retour)
	}
	lr.mu.Lock()
	etapes := lr.etapes
	lr.mu.Unlock()
	if len(etapes) != 2 || etapes[0].Libelle != "Suit une tâche" || etapes[0].Ms == 0 {
		t.Errorf("étapes : %+v", etapes)
	}
}

// L'API échoue avant toute délégation : la demande peut repartir à
// l'atelier.
func TestPiloterParAPIRepli(t *testing.T) {
	s, user, moi, lr := serveurPiloteAPI(t, &fausseAPI{})
	_, err := s.piloterParAPI(t.Context(), user, moi, "Corrige le bug", lr)
	if !errors.Is(err, errPiloteRepli) {
		t.Fatalf("erreur %v, attendu un repli", err)
	}
}
