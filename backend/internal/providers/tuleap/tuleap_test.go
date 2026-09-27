package tuleap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Un serveur Tuleap de poche : deux artefacts sur le tracker 42, dont un
// assigné à l'utilisateur de la clé, avec les formats de valeurs que l'API
// réelle renvoie (chaîne, liste de libellés, liste d'utilisateurs, texte HTML).
func fakeTuleap(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-AccessKey") != "tlp-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Access key invalid"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/users/self":
			_, _ = w.Write([]byte(`{"id":7,"username":"coutant","real_name":"Mathias Coutant","display_name":"Mathias Coutant (coutant)"}`))
		case "/api/trackers/42/artifacts":
			if r.URL.Query().Get("query") != `{"status":"open"}` {
				t.Errorf("requête inattendue : %s", r.URL.RawQuery)
			}
			w.Header().Set("X-PAGINATION-SIZE", "2")
			_, _ = w.Write([]byte(`[
			  {"id":101,"title":"Écran figé au démarrage","status":"En cours","xref":"csp #101","html_url":"/plugins/tracker/?aid=101",
			   "submitted_on":"2026-09-20T10:00:00+02:00","last_modified_date":"2026-09-26T09:30:00+02:00",
			   "submitted_by_details":{"id":3,"display_name":"Thomas Durand"},
			   "assignees":[{"id":7,"display_name":"Mathias Coutant"}],
			   "tracker":{"label":"CSP"},
			   "values":[
			     {"field_id":1,"label":"Priority","type":"sb","values":[{"label":"High"}]},
			     {"field_id":2,"label":"Description","type":"text","value":"<p>Le CSP <b>plante</b> au boot.</p>","format":"html"},
			     {"field_id":3,"label":"Compagnie","type":"string","value":"Azul"},
			     {"field_id":4,"label":"Artifact ID","type":"aid","value":101}
			   ]},
			  {"id":102,"title":"Mise à jour firmware","status":"Closed","xref":"csp #102","html_url":"/plugins/tracker/?aid=102",
			   "submitted_on":"2026-09-01T10:00:00+02:00","last_modified_date":"2026-09-10T09:30:00+02:00",
			   "submitted_by_details":{"id":7,"display_name":"Mathias Coutant"},
			   "assignees":[{"id":3,"display_name":"Thomas Durand"}],
			   "tracker":{"label":"CSP"},
			   "values":[{"field_id":1,"label":"Priority","type":"sb","values":[{"label":"Low"}]}]}
			]`))
		case "/api/artifacts/101":
			_, _ = w.Write([]byte(`{"id":101,"title":"Écran figé au démarrage","status":"En cours","xref":"csp #101","html_url":"/plugins/tracker/?aid=101",
			   "submitted_on":"2026-09-20T10:00:00+02:00","last_modified_date":"2026-09-26T09:30:00+02:00",
			   "submitted_by_details":{"id":3,"display_name":"Thomas Durand"},"assignees":[],"tracker":{"label":"CSP"},"values":[]}`))
		case "/api/artifacts/101/changesets":
			_, _ = w.Write([]byte(`[{"submitted_by_details":{"display_name":"Thomas Durand"},"submitted_on":"2026-09-21T08:00:00+02:00","last_comment":{"body":"<p>Reproduit sur le banc.</p>","format":"html"}},
			  {"submitted_by_details":{"display_name":"Bot"},"submitted_on":"2026-09-21T09:00:00+02:00","last_comment":{"body":"","format":"text"}}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"no route"}}`))
		}
	}))
}

func TestTicketsFlattenAndFilter(t *testing.T) {
	srv := fakeTuleap(t)
	defer srv.Close()

	c := New(srv.URL, "tlp-secret")
	tickets, err := c.Tickets(context.Background(), Scope{TrackerID: 42, Query: `{"status":"open"}`})
	if err != nil {
		t.Fatalf("tickets : %v", err)
	}
	if len(tickets) != 2 {
		t.Fatalf("attendu 2 tickets, obtenu %d", len(tickets))
	}
	first := tickets[0]
	if first.ID != 101 || first.Titre != "Écran figé au démarrage" || first.Statut != "En cours" {
		t.Errorf("ticket 101 mal lu : %+v", first)
	}
	if first.Priorite != "High" {
		t.Errorf("priorité : %q", first.Priorite)
	}
	if first.Responsable != "Mathias Coutant" || first.Auteur != "Thomas Durand" {
		t.Errorf("responsable=%q auteur=%q", first.Responsable, first.Auteur)
	}
	if first.Description != "Le CSP plante au boot." {
		t.Errorf("description : %q", first.Description)
	}
	if first.Champs["Compagnie"] != "Azul" {
		t.Errorf("champs libres : %+v", first.Champs)
	}
	if _, leaked := first.Champs["Artifact ID"]; leaked {
		t.Error("un champ technique ne doit pas remonter dans les champs libres")
	}
	if first.URL != srv.URL+"/plugins/tracker/?aid=101" {
		t.Errorf("url : %q", first.URL)
	}
	if first.Ferme || !tickets[1].Ferme {
		t.Errorf("statut clos : 101=%v 102=%v", first.Ferme, tickets[1].Ferme)
	}

	mine, err := c.Tickets(context.Background(), Scope{TrackerID: 42, Query: `{"status":"open"}`, AssignedToMe: true})
	if err != nil {
		t.Fatalf("assignés : %v", err)
	}
	if len(mine) != 1 || mine[0].ID != 101 {
		t.Errorf("filtre « assigné à moi » : %+v", mine)
	}
}

func TestTicketDetailWithComments(t *testing.T) {
	srv := fakeTuleap(t)
	defer srv.Close()

	c := New(srv.URL, "tlp-secret")
	ticket, comments, err := c.Ticket(context.Background(), 101)
	if err != nil {
		t.Fatalf("détail : %v", err)
	}
	if ticket.ID != 101 || ticket.Ref != "csp #101" {
		t.Errorf("détail mal lu : %+v", ticket)
	}
	if len(comments) != 1 || comments[0].Texte != "Reproduit sur le banc." || comments[0].Auteur != "Thomas Durand" {
		t.Errorf("commentaires : %+v", comments)
	}
}

func TestRejectedKeyIsExplicit(t *testing.T) {
	srv := fakeTuleap(t)
	defer srv.Close()

	_, _, err := New(srv.URL, "mauvaise").Me(context.Background())
	if err == nil {
		t.Fatal("une clé refusée doit échouer")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != http.StatusUnauthorized {
		t.Errorf("erreur attendue 401, obtenu %v", err)
	}
}
