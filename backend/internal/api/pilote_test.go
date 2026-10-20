package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

func serveurPilote() *Server {
	return &Server{
		equipe: equipe.New(assistant.New("cle", "", ""), nil, nil, equipe.Config{}),
		runs:   newOrchestre(nil, 2, 10),
	}
}

// Le responsable connaît toute l'équipe, sauf lui-même et Raoul, et sait
// comment faire circuler l'information.
func TestConsignePilote(t *testing.T) {
	s := serveurPilote()
	moi, _ := s.equipe.Membre(equipe.IDPilote)
	c := s.consignePilote(moi)
	for _, want := range []string{`"ingenieur"`, `"responsable_info"`, `"veille_mail"`, `"veille_slack"`, `"veille_whatsapp"`, "confier_a_un_agent", "suivre_tache", "documentation"} {
		if !strings.Contains(c, want) {
			t.Errorf("consigne : %q manquant", want)
		}
	}
	if strings.Contains(c, `"raoul"`) || strings.Contains(c, `"responsable")`) {
		t.Error("ni Raoul ni lui-même parmi ceux à qui il délègue")
	}
	tools := s.piloteTools(moi)
	enum := tools[0].Schema["properties"].(map[string]any)["agent"].(map[string]any)["enum"].([]any)
	for _, id := range enum {
		if id == equipe.IDPilote || id == equipe.IDRaoul {
			t.Errorf("il ne peut pas se confier une tâche, ni à Raoul : %v", enum)
		}
	}
}

// Sans tâche du responsable en cours, pas d'outil de délégation : un autre
// agent de l'atelier ne peut pas déléguer.
func TestPiloteReconnuParSaTache(t *testing.T) {
	s := serveurPilote()
	user := &store.User{}
	r := httptest.NewRequest("POST", "/mcp", nil)
	if _, _, ok := s.pilote(r, user); ok {
		t.Fatal("sans en-tête, pas de responsable")
	}
	r.Header.Set("X-Cerveau-Run", "pas-un-identifiant")
	if _, _, ok := s.pilote(r, user); ok {
		t.Fatal("en-tête invalide, pas de responsable")
	}
}
