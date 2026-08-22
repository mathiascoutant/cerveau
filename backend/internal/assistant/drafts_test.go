package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// stubToolbox ne sert qu'aux outils de brouillon : le reste du contrat n'est
// pas exercé ici.
type stubToolbox struct {
	Toolbox
	drafts  []EmailDraftView
	updated EmailReplyDraft
}

func (s *stubToolbox) PrepareEmailReply(_ context.Context, d EmailReplyDraft) (EmailDraftView, store.Action, error) {
	s.updated = d
	return EmailDraftView{ID: "abc", Destinataire: d.Destinataire, Corps: d.Corps}, store.Action{Type: "email_draft"}, nil
}

func (s *stubToolbox) FindEmailDrafts(context.Context, string) ([]EmailDraftView, error) {
	return s.drafts, nil
}

func runDraftTool(t *testing.T, tb Toolbox, name, args string) (string, *store.Action, error) {
	t.Helper()
	e := &Engine{}
	return e.runTool(context.Background(), tb, time.UTC, name, args)
}

// Un seul brouillon : on rend le texte complet, il n'y a rien à demander.
func TestChercherBrouillonSingleReturnsBody(t *testing.T) {
	tb := &stubToolbox{drafts: []EmailDraftView{
		{ID: "1", Destinataire: "Cyril", Objet: "Re: devis", Corps: "Bonjour Cyril, c'est bon pour moi."},
	}}

	out, _, err := runDraftTool(t, tb, "chercher_brouillon", `{"recherche":"Cyril"}`)
	if err != nil {
		t.Fatalf("chercher_brouillon : %v", err)
	}
	if !strings.Contains(out, "c'est bon pour moi") {
		t.Errorf("le corps du brouillon devrait être rendu tel quel : %s", out)
	}
}

// Plusieurs brouillons : les corps ne descendent pas, et le modèle reçoit
// l'ordre de demander. Relire le mauvais mail est pire que poser une question.
func TestChercherBrouillonAmbiguousWithholdsBodies(t *testing.T) {
	tb := &stubToolbox{drafts: []EmailDraftView{
		{ID: "1", Destinataire: "Cyril", Objet: "Re: devis", Corps: "PREMIER CORPS"},
		{ID: "2", Destinataire: "Cyril", Objet: "Re: planning", Corps: "SECOND CORPS"},
	}}

	out, _, err := runDraftTool(t, tb, "chercher_brouillon", `{"recherche":"Cyril"}`)
	if err != nil {
		t.Fatalf("chercher_brouillon : %v", err)
	}
	for _, body := range []string{"PREMIER CORPS", "SECOND CORPS"} {
		if strings.Contains(out, body) {
			t.Errorf("un corps a fuité alors que le choix n'est pas tranché : %s", out)
		}
	}
	for _, subject := range []string{"Re: devis", "Re: planning"} {
		if !strings.Contains(out, subject) {
			t.Errorf("l'objet %q devrait être proposé au choix : %s", subject, out)
		}
	}
	if !strings.Contains(out, "Ne choisis pas") {
		t.Errorf("le modèle devrait recevoir la consigne de demander : %s", out)
	}
}

func TestChercherBrouillonEmpty(t *testing.T) {
	out, _, err := runDraftTool(t, &stubToolbox{}, "chercher_brouillon", `{"recherche":"Cyril"}`)
	if err != nil {
		t.Fatalf("chercher_brouillon : %v", err)
	}
	if !strings.Contains(out, "Aucune réponse") {
		t.Errorf("un résultat vide doit se dire clairement : %s", out)
	}
}

// Le modèle doit rédiger le mail, pas décrire ce qu'il écrirait. Un corps vide
// est une erreur qui lui revient, pas un brouillon vide qu'on enregistre.
func TestPreparerReponseRejectsEmptyBody(t *testing.T) {
	_, _, err := runDraftTool(t, &stubToolbox{}, "preparer_reponse_mail",
		`{"destinataire":"Cyril","corps":"   "}`)
	if err == nil {
		t.Fatal("un corps vide devrait être refusé")
	}
	if !strings.Contains(err.Error(), "rédiger") {
		t.Errorf("l'erreur devrait dire quoi faire, obtenu : %v", err)
	}
}

// Une modification partielle écraserait le mail par son seul fragment modifié.
func TestModifierBrouillonRequiresFullBody(t *testing.T) {
	if _, _, err := runDraftTool(t, &stubToolbox{}, "modifier_brouillon", `{"id":"1","corps":""}`); err == nil {
		t.Fatal("un corps vide devrait être refusé")
	}
	if _, _, err := runDraftTool(t, &stubToolbox{}, "modifier_brouillon", `{"corps":"texte"}`); err == nil {
		t.Fatal("un identifiant manquant devrait être refusé")
	}
}
