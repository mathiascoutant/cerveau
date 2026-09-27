package tuleap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Les titres que PXFeed reconnaît, et ceux qu'il écarte — mêmes cas que sa
// documentation et son code.
func TestParseCSPTitle(t *testing.T) {
	cases := []struct {
		title, project string
		airline, cycle string
	}{
		{"[Azul] CSP cycle November follow-up (2026-11)", "", "Azul", "2026-11"},
		{"[RAM] Process November 2026", "", "RAM", "2026-11"},
		{"[Hifly] Process Décembre 2026", "", "Hifly", "2026-12"},
		{"Process Mai 2026", "Etihad", "Etihad", "2026-05"},
		{"  [Canada-01] CSP cycle Q4 (2026-10)  ", "", "Canada-01", "2026-10"},
	}
	for _, c := range cases {
		got := ParseCSPTitle(c.title, c.project)
		if got == nil {
			t.Errorf("%q : non reconnu", c.title)
			continue
		}
		if got.airline != c.airline || got.cycle != c.cycle {
			t.Errorf("%q → %s %s, attendu %s %s", c.title, got.airline, got.cycle, c.airline, c.cycle)
		}
	}
	for _, bad := range []string{
		"Process Mai 2026",               // sans projet, pas de compagnie
		"[RAM] Process Brumaire 2026",    // mois inconnu
		"Bug écran figé au démarrage",    // pas un cycle
		"[Azul] CSP cycle November 2026", // sans (AAAA-MM)
		"",
	} {
		if got := ParseCSPTitle(bad, ""); got != nil {
			t.Errorf("%q ne devrait pas être un cycle, obtenu %+v", bad, got)
		}
	}
}

func TestIsExcludedCSPStatus(t *testing.T) {
	for _, s := range []string{"Done", "Closed", "Clos", "Terminé", "Archivé", "Deployed", "To deploy", "deploy"} {
		if !IsExcludedCSPStatus(s) {
			t.Errorf("%q devrait être exclu", s)
		}
	}
	// « En déploiement » passe : PXFeed cherche « deploy » sans accent, on ne fait pas mieux que lui.
	for _, s := range []string{"", "New", "En cours", "In progress", "To do", "Validation", "En déploiement"} {
		if IsExcludedCSPStatus(s) {
			t.Errorf("%q ne devrait pas être exclu", s)
		}
	}
}

func TestAirlineAllowed(t *testing.T) {
	allowed := []string{"ram", "etihad", "azul", "hifly", "canada-01"}
	for _, a := range []string{"RAM", "Azul", "Canada-01", "Hi Fly", "Royal Air Maroc"} {
		if a == "Royal Air Maroc" {
			continue
		}
		if !airlineAllowed(a, allowed) {
			t.Errorf("%q devrait être gardée", a)
		}
	}
	if airlineAllowed("Disney", allowed) {
		t.Error("Disney n'est pas dans la liste")
	}
	if normalizeAirlineKey("Canada-01") != "canada01" || normalizeAirlineKey("Étihad") != "etihad" {
		t.Errorf("normalisation : %q %q", normalizeAirlineKey("Canada-01"), normalizeAirlineKey("Étihad"))
	}
}

// Le chemin de PXFeed de bout en bout : la release rend ses artefacts (sans
// champs), un titre illisible fait relire la fiche, les clos et déployés
// tombent, le tri met le cycle le plus récent en tête.
func TestCyclesFromReleaseContent(t *testing.T) {
	var detailCalls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-AccessKey") != "tlp-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/planning_milestones/12051/content":
			_, _ = w.Write([]byte(`[
			  {"id":1,"title":"[Azul] CSP cycle November follow-up (2026-11)","status":"En cours","html_url":"/plugins/tracker/?aid=1","last_modified_date":"2026-09-20T10:00:00+02:00"},
			  {"id":2,"title":"[RAM] Process December 2026","status":"New","html_url":"/plugins/tracker/?aid=2","last_modified_date":"2026-09-21T10:00:00+02:00"},
			  {"id":3,"title":"[Hifly] Process November 2026","status":"Deployed","html_url":"/plugins/tracker/?aid=3","last_modified_date":"2026-09-22T10:00:00+02:00"},
			  {"id":4,"title":"Process Mai 2026","status":"En cours","html_url":"/plugins/tracker/?aid=4","last_modified_date":"2026-09-23T10:00:00+02:00"},
			  {"id":5,"title":"[Etihad] CSP cycle Q4 (2026-11)","status":"Closed","html_url":"/plugins/tracker/?aid=5"},
			  {"id":6,"title":"Bug écran figé","status":"New","html_url":"/plugins/tracker/?aid=6"}
			]`))
		case "/api/artifacts/4":
			detailCalls = append(detailCalls, r.URL.Path)
			_, _ = w.Write([]byte(`{"id":4,"title":"Process Mai 2026","status":"En cours","html_url":"/plugins/tracker/?aid=4",
			  "assignees":[{"id":7,"display_name":"Mathias Coutant"}],
			  "values":[{"field_id":9,"label":"Projet","type":"sb","values":[{"label":"Canada-01"}]}]}`))
		case "/api/artifacts/6":
			detailCalls = append(detailCalls, r.URL.Path)
			_, _ = w.Write([]byte(`{"id":6,"title":"Bug écran figé","status":"New","values":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"no route"}}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tlp-secret")
	res, err := c.Cycles(context.Background(), CSPScope{})
	if err != nil {
		t.Fatalf("cycles : %v", err)
	}
	if !strings.Contains(res.Source, "/api/planning_milestones/12051/content") {
		t.Errorf("source : %q", res.Source)
	}
	want := []string{"RAM 2026-12", "Azul 2026-11", "Canada-01 2026-05"}
	if len(res.Tickets) != len(want) {
		t.Fatalf("attendu %v, obtenu %+v", want, res.Tickets)
	}
	for i, tk := range res.Tickets {
		if tk.Label != want[i] {
			t.Errorf("rang %d : %q, attendu %q", i, tk.Label, want[i])
		}
	}
	if res.Tickets[2].Responsable != "Mathias Coutant" || res.Tickets[2].Compagnie != "Canada-01" {
		t.Errorf("la fiche relue doit enrichir le ticket : %+v", res.Tickets[2])
	}
	if res.Tickets[0].URL != srv.URL+"/plugins/tracker/?aid=2" {
		t.Errorf("url : %q", res.Tickets[0].URL)
	}
	if res.Stats != (CSPStats{Total: 6, TitreReconnu: 5, StatutExclu: 2, Retenus: 3}) {
		t.Errorf("stats : %+v", res.Stats)
	}
	if len(detailCalls) != 2 {
		t.Errorf("seuls les titres illisibles font relire la fiche : %v", detailCalls)
	}

	only, err := c.Cycles(context.Background(), CSPScope{Airlines: []string{"azul", "ram"}})
	if err != nil {
		t.Fatalf("cycles filtrés : %v", err)
	}
	if len(only.Tickets) != 2 || only.Stats.CompagnieExclu != 1 {
		t.Errorf("filtre compagnies : %+v (%+v)", only.Tickets, only.Stats)
	}
}

// Sans release lisible, on retombe sur le tracker, page par page.
func TestCyclesFallBackToTracker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/trackers/427/artifacts":
			w.Header().Set("X-PAGINATION-SIZE", "1")
			_, _ = w.Write([]byte(`[{"id":9,"title":"[Azul] Process Juin 2026","status":"New","html_url":"/plugins/tracker/?aid=9"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"no route"}}`))
		}
	}))
	defer srv.Close()

	res, err := New(srv.URL, "k").Cycles(context.Background(), CSPScope{})
	if err != nil {
		t.Fatalf("repli : %v", err)
	}
	if len(res.Tickets) != 1 || res.Tickets[0].Label != "Azul 2026-06" || !strings.Contains(res.Source, "/api/trackers/427/artifacts") {
		t.Errorf("repli tracker : %+v (source %q)", res.Tickets, res.Source)
	}
}

// Les enveloppes que Tuleap peut rendre : tableau nu, collection, artefact
// emballé.
func TestNormalizeArtifactList(t *testing.T) {
	for _, body := range []string{
		`[{"id":1,"title":"a"}]`,
		`{"collection":[{"id":1,"title":"a"}]}`,
		`{"content":[{"artifact":{"id":1,"title":"a"}}]}`,
	} {
		got := normalizeArtifactList([]byte(body))
		if len(got) != 1 || got[0].ID != 1 || got[0].Title != "a" {
			t.Errorf("%s → %+v", body, got)
		}
	}
	if got := normalizeArtifactList([]byte(`{"error":{"message":"x"}}`)); len(got) != 0 {
		t.Errorf("une erreur n'est pas une liste : %+v", got)
	}
}
