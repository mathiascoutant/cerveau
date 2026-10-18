package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var equipeTest = []Collegue{
	{ID: "responsable_info", Nom: "Claire", Role: "Responsable informatique", Quand: "la vue d'ensemble"},
	{ID: "ingenieur", Nom: "Léo", Role: "Ingénieur logiciel", Quand: "ce qui demande de creuser"},
}

func toolNames(src Sources) map[string]map[string]any {
	raw, _ := json.Marshal(toolDefinitions(src))
	var tools []struct {
		Name       string `json:"name"`
		Parameters struct {
			Properties map[string]any `json:"properties"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal(raw, &tools)
	out := map[string]map[string]any{}
	for _, t := range tools {
		out[t.Name] = t.Parameters.Properties
	}
	return out
}

// Le passage de parole n'existe que s'il y a une équipe ; le point sur les
// projets, que si des projets sont suivis.
func TestOutilsPersoSelonLaConfiguration(t *testing.T) {
	bare := toolNames(Sources{Mail: true})
	if _, ok := bare["passer_la_parole"]; ok {
		t.Error("passer_la_parole exposé sans équipe")
	}
	if _, ok := bare["etat_projets"]; ok {
		t.Error("etat_projets exposé sans projets suivis")
	}
	full := toolNames(Sources{Mail: true, Projets: true, Equipe: equipeTest})
	if _, ok := full["passer_la_parole"]; !ok {
		t.Error("passer_la_parole manquant avec une équipe")
	}
	if _, ok := full["etat_projets"]; !ok {
		t.Error("etat_projets manquant avec des projets suivis")
	}
}

// Le paramètre espace n'apparaît que lorsqu'il y a deux boîtes à départager.
func TestParametreEspaceSeulementAvecDeuxBoites(t *testing.T) {
	if _, ok := toolNames(Sources{Mail: true})["mails_non_lus"]["espace"]; ok {
		t.Error("espace proposé avec une seule boîte")
	}
	if _, ok := toolNames(Sources{Mail: true, MailPerso: true})["mails_non_lus"]["espace"]; !ok {
		t.Error("espace absent alors que deux boîtes sont branchées")
	}
	// Une boîte perso seule suffit à exposer les outils de mail.
	if _, ok := toolNames(Sources{MailPerso: true})["lire_mail"]; !ok {
		t.Error("les outils de mail doivent exister avec la seule boîte perso")
	}
}

func TestHandoff(t *testing.T) {
	out, err := handoff(equipeTest, `{"a":"ingenieur"}`)
	if err != nil || out.Handoff != "ingenieur" || !strings.Contains(out.Payload, "Léo") {
		t.Fatalf("passage de parole : %+v %v", out, err)
	}
	if _, err := handoff(equipeTest, `{"a":"comptable"}`); err == nil {
		t.Fatal("un collègue inconnu aurait dû être refusé")
	}
}

// spacedFake : une boîte à outils qui sait dire dans quel espace elle lit.
type spacedFake struct {
	Toolbox
	space string
}

func (f spacedFake) InSpace(space string) Toolbox { return spacedFake{space: space} }

func TestMailToolboxChoisitLEspace(t *testing.T) {
	pro := spacedFake{space: "pro"}
	both := Sources{Mail: true, MailPerso: true}
	if got := mailToolbox(pro, both, "mails_non_lus", `{"espace":"perso"}`).(spacedFake); got.space != "perso" {
		t.Error("espace=perso doit lire la boîte perso")
	}
	if got := mailToolbox(pro, both, "mails_non_lus", `{}`).(spacedFake); got.space != "pro" {
		t.Error("sans précision, la boîte pro reste la boîte par défaut")
	}
	if got := mailToolbox(pro, Sources{MailPerso: true}, "lire_mail", `{}`).(spacedFake); got.space != "perso" {
		t.Error("seule la boîte perso est branchée : c'est elle qu'on lit")
	}
	if got := mailToolbox(pro, both, "slack_non_lus", `{"espace":"perso"}`).(spacedFake); got.space != "pro" {
		t.Error("l'espace ne concerne que les outils de mail")
	}
}

// La consigne ne nomme que ce qui est branché, y compris dans la section
// sur les espaces.
func TestReglesDesEspacesNeNommentQueCeQuiEstBranche(t *testing.T) {
	rules := spaceRules(Sources{Mail: true, Projets: true})
	if strings.Contains(rules, "Slack") || strings.Contains(rules, "WhatsApp") {
		t.Errorf("source non branchée nommée :\n%s", rules)
	}
	if !strings.Contains(rules, "côté pro") || !strings.Contains(rules, "ses projets à lui") {
		t.Errorf("règles incomplètes :\n%s", rules)
	}
	two := spaceRules(Sources{Mail: true, MailPerso: true})
	if !strings.Contains(two, "DEUX boîtes mail") {
		t.Errorf("les deux boîtes doivent être distinguées :\n%s", two)
	}
	prompt := systemPrompt(time.Now(), "Europe/Paris", "Mathias", "m@x.fr", Sources{Mail: true, Projets: true, Equipe: equipeTest}, nil, nil)
	if !strings.Contains(prompt, "passer_la_parole") || !strings.Contains(prompt, "[Claire, responsable informatique]") {
		t.Error("la consigne doit expliquer le passage de parole")
	}
}

type briefFake struct{}

func (briefFake) ProjectsBrief(context.Context) (string, error) { return `{"projets":[]}`, nil }

func TestRaoulToolEtatProjets(t *testing.T) {
	e := New("cle-factice", "", "")
	out, err := e.raoulTool(context.Background(), nil, Request{Perso: briefFake{}}, time.Now(), time.UTC, "etat_projets", `{}`)
	if err != nil || !strings.Contains(out.Payload, "projets") {
		t.Fatalf("etat_projets : %+v %v", out, err)
	}
	if _, err := e.raoulTool(context.Background(), nil, Request{}, time.Now(), time.UTC, "etat_projets", `{}`); err == nil {
		t.Fatal("sans projets suivis, l'outil doit échouer proprement")
	}
}
