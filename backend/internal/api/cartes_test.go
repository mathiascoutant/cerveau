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
	"github.com/mathiascoutant/cerveau/backend/internal/projets"
	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Une carte se crée, se déplace, et suit le travail de son agent : en cours
// tant qu'il travaille, « à vérifier » avec sa réponse, « à faire » s'il
// échoue.
func TestTableauDesTaches(t *testing.T) {
	st := runsStore(t)
	ctx := context.Background()
	reg := &registre.Registre{Projets: []registre.Projet{{ID: "neurorun", Nom: "Neurorun"}}}
	srv := NewServer(config.Config{}, st, nil, nil).WithPerso(projets.New(reg, nil, nil))
	user := &store.User{ID: bson.NewObjectID()}

	call := func(method, path, body string, params map[string]string, h http.HandlerFunc) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rc := chi.NewRouteContext()
		for k, v := range params {
			rc.URLParams.Add(k, v)
		}
		c := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
		c = context.WithValue(c, userCtxKey, user)
		rec := httptest.NewRecorder()
		h(rec, req.WithContext(c))
		return rec
	}
	var out struct{ Carte carteVue }

	rec := call(http.MethodPost, "/", `{"projet":"neurorun","titre":"Corriger la connexion Garmin","agent":"ingenieur","priorite":"haute","echeance":"2026-11-02"}`, nil, srv.handleHQCreerCarte)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusCreated || out.Carte.Statut != store.CarteAFaire || out.Carte.Echeance == nil || out.Carte.Priorite != "haute" {
		t.Fatalf("création : %d %s", rec.Code, rec.Body)
	}
	id := out.Carte.ID.Hex()

	for _, bad := range []string{`{"titre":""}`, `{"titre":"x","projet":"inconnu"}`, `{"titre":"x","agent":"personne"}`, `{"titre":"x","priorite":"vite"}`, `{"titre":"x","echeance":"demain"}`} {
		if rec := call(http.MethodPost, "/", bad, nil, srv.handleHQCreerCarte); rec.Code != http.StatusBadRequest {
			t.Errorf("%s accepté : %d", bad, rec.Code)
		}
	}

	// Déplacée à la main, échéance effacée.
	rec = call(http.MethodPatch, "/", `{"statut":"termine","echeance":""}`, map[string]string{"id": id}, srv.handleHQModifierCarte)
	out.Carte = carteVue{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Carte.Statut != store.CarteTerminee || out.Carte.TermineeLe == nil || out.Carte.Echeance != nil {
		t.Fatalf("déplacement : %s", rec.Body)
	}

	// Confiée à l'agent : elle suit son exécution.
	run, _ := st.InsertRun(ctx, store.AgentRun{UserID: user.ID, Agent: equipe.IDIngenieur, Demande: "x", Statut: store.RunEnCours})
	_, _ = st.ModifierCarte(ctx, out.Carte.ID, bson.M{"statut": store.CarteEnCours, "run": run.ID})
	_ = st.FinishRun(ctx, run.ID, store.RunTermine, "C'est corrigé sur la branche agent/garmin.", "", nil)
	var list struct{ Cartes []carteVue }
	_ = json.Unmarshal(call(http.MethodGet, "/?projet=neurorun", "", nil, srv.handleHQCartes).Body.Bytes(), &list)
	if len(list.Cartes) != 1 || list.Cartes[0].Statut != store.CarteAVerifier || !strings.Contains(list.Cartes[0].CompteRendu, "agent/garmin") {
		t.Fatalf("la carte devait passer à vérifier : %+v", list.Cartes)
	}

	run2, _ := st.InsertRun(ctx, store.AgentRun{UserID: user.ID, Agent: equipe.IDIngenieur, Demande: "x", Statut: store.RunEnCours})
	_, _ = st.ModifierCarte(ctx, out.Carte.ID, bson.M{"statut": store.CarteEnCours, "run": run2.ID})
	_ = st.FinishRun(ctx, run2.ID, store.RunErreur, "", "dépôt injoignable", nil)
	_ = json.Unmarshal(call(http.MethodGet, "/", "", nil, srv.handleHQCartes).Body.Bytes(), &list)
	if list.Cartes[0].Statut != store.CarteAFaire || list.Cartes[0].Erreur != "dépôt injoignable" {
		t.Fatalf("la carte devait revenir à faire : %+v", list.Cartes[0])
	}

	// La demande faite à l'agent porte le projet, l'échéance et l'attendu.
	c, _ := st.Carte(ctx, out.Carte.ID)
	if d := srv.demandeDeCarte(ctx, c, "regarde la CI"); !strings.Contains(d, "projet « Neurorun »") || !strings.Contains(d, "regarde la CI") || !strings.Contains(d, "compte rendu") {
		t.Errorf("demande incomplète :\n%s", d)
	}

	if rec := call(http.MethodDelete, "/", "", map[string]string{"id": id}, srv.handleHQSupprimerCarte); rec.Code != http.StatusOK {
		t.Errorf("suppression : %d", rec.Code)
	}
}
