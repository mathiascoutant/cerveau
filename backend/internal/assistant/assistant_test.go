package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// L'authentification OpenAI est vérifiée avant le corps de la requête : une 401
// ne prouve donc rien sur la validité du payload. Ce test sérialise réellement
// les définitions d'outils et vérifie leur forme.
func TestToolDefinitionsSerialization(t *testing.T) {
	raw, err := json.Marshal(toolDefinitions(Sources{Mail: true, Slack: true, WhatsApp: true}))
	if err != nil {
		t.Fatalf("sérialisation des outils : %v", err)
	}

	// Forme attendue par l'API Responses : les champs sont à plat sur l'outil,
	// contrairement à Chat Completions qui les imbrique sous "function".
	var tools []struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatalf("désérialisation : %v\npayload: %s", err, raw)
	}

	want := map[string][]string{
		"consulter_calendrier":  {"debut", "fin"},
		"mails_non_lus":         nil,
		"lire_mail":             nil,
		"slack_non_lus":         nil,
		"whatsapp_non_lus":      nil,
		"lire_canal_slack":      {"canal"},
		"lancer_navigation":     {"destination"},
		"creer_evenement":       {"titre", "debut", "fin"},
		"preparer_reponse_mail": {"destinataire", "corps"},
		"chercher_brouillon":    nil,
		"modifier_brouillon":    {"id", "corps"},
		"chercher_historique":   nil,
	}
	if len(tools) != len(want) {
		t.Fatalf("attendu %d outils, obtenu %d", len(want), len(tools))
	}

	for _, tool := range tools {
		if tool.Type != "function" {
			t.Errorf("%s : type %q, attendu \"function\"", tool.Name, tool.Type)
		}
		required, known := want[tool.Name]
		if !known {
			t.Errorf("outil inattendu : %s", tool.Name)
			continue
		}
		if tool.Description == "" {
			t.Errorf("%s : description vide", tool.Name)
		}
		if tool.Parameters.Type != "object" {
			t.Errorf("%s : schéma de type %q, attendu \"object\"", tool.Name, tool.Parameters.Type)
		}
		if len(tool.Parameters.Properties) == 0 {
			t.Errorf("%s : aucune propriété déclarée", tool.Name)
		}
		for _, field := range required {
			if _, ok := tool.Parameters.Properties[field]; !ok {
				t.Errorf("%s : champ requis %q absent des propriétés", tool.Name, field)
			}
		}
		if len(tool.Parameters.Required) != len(required) {
			t.Errorf("%s : %d champs requis, attendu %d",
				tool.Name, len(tool.Parameters.Required), len(required))
		}
	}
}

func TestParseTime(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("fuseau introuvable : %v", err)
	}

	cases := []struct {
		in       string
		wantHour int
	}{
		{"2026-08-21T10:00:00+02:00", 10},
		{"2026-08-21T10:00:00", 10},
		{"2026-08-21T10:00", 10},
		{"2026-08-21 10:00", 10},
		{"2026-08-21", 0},
	}
	for _, c := range cases {
		got, err := parseTime(c.in, paris)
		if err != nil {
			t.Errorf("parseTime(%q) : %v", c.in, err)
			continue
		}
		if h := got.In(paris).Hour(); h != c.wantHour {
			t.Errorf("parseTime(%q) : heure %d, attendu %d", c.in, h, c.wantHour)
		}
	}

	if _, err := parseTime("demain matin", paris); err == nil {
		t.Error("parseTime devrait rejeter une date non ISO — le modèle doit renvoyer de l'ISO 8601")
	}
}

func TestLimitOf(t *testing.T) {
	if got := limitOf(`{"limite": 5}`, 15); got != 5 {
		t.Errorf("limite explicite : %d, attendu 5", got)
	}
	// Les outils sans argument reçoivent « {} » : le défaut doit s'appliquer.
	if got := limitOf(`{}`, 15); got != 15 {
		t.Errorf("limite par défaut : %d, attendu 15", got)
	}
	if got := limitOf(``, 15); got != 15 {
		t.Errorf("arguments vides : %d, attendu 15", got)
	}
	if got := limitOf(`{"limite": -3}`, 15); got != 15 {
		t.Errorf("limite négative : %d, attendu le défaut 15", got)
	}
}

func TestSystemPromptCarriesTemporalContext(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 8, 20, 19, 30, 0, 0, paris)
	prompt := systemPrompt(now, "Europe/Paris", "Mathias", "mathias@exemple.fr", Sources{Mail: true, Slack: true, WhatsApp: true})

	for _, needle := range []string{"Mathias", "mathias@exemple.fr", "20 August 2026", "19h30", "Europe/Paris"} {
		if !strings.Contains(prompt, needle) {
			t.Errorf("le prompt système ne contient pas %q", needle)
		}
	}
}

// Une source débranchée ne doit pas devenir un outil qui échoue : le modèle
// rapporterait l'échec à voix haute. Sans outil, elle n'existe pas pour lui.
func TestToolDefinitionsHideDisconnectedSources(t *testing.T) {
	names := func(src Sources) map[string]bool {
		raw, err := json.Marshal(toolDefinitions(src))
		if err != nil {
			t.Fatalf("sérialisation : %v", err)
		}
		var tools []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &tools); err != nil {
			t.Fatalf("désérialisation : %v", err)
		}
		out := map[string]bool{}
		for _, tool := range tools {
			out[tool.Name] = true
		}
		return out
	}

	only := names(Sources{Mail: true})
	for _, absent := range []string{"whatsapp_non_lus", "slack_non_lus", "lire_canal_slack"} {
		if only[absent] {
			t.Errorf("%s exposé alors que la source n'est pas branchée", absent)
		}
	}
	for _, present := range []string{"mails_non_lus", "lire_mail", "preparer_reponse_mail"} {
		if !only[present] {
			t.Errorf("%s manquant alors que la boîte mail est branchée", present)
		}
	}

	// Le calendrier et la mémoire ne dépendent d'aucune connexion externe.
	bare := names(Sources{})
	for _, always := range []string{"consulter_calendrier", "creer_evenement", "chercher_historique", "chercher_brouillon"} {
		if !bare[always] {
			t.Errorf("%s devrait rester disponible sans aucune source branchée", always)
		}
	}
	if bare["preparer_reponse_mail"] {
		t.Error("preparer_reponse_mail exposé sans boîte mail branchée")
	}
}

// Le prompt ne doit pas nommer un service débranché : le nommer suffit à ce
// que le modèle en parle, ne serait-ce que pour dire qu'il n'y a rien dessus.
func TestSystemPromptOmitsDisconnectedSources(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 8, 20, 19, 30, 0, 0, paris)

	prompt := systemPrompt(now, "Europe/Paris", "Mathias", "mathias@exemple.fr", Sources{Mail: true})
	if strings.Contains(prompt, "WhatsApp") {
		t.Error("le prompt cite WhatsApp alors que le compte n'est pas branché")
	}
	if strings.Contains(prompt, "Slack") {
		t.Error("le prompt cite Slack alors que le compte n'est pas branché")
	}
	if !strings.Contains(prompt, "Gandi") {
		t.Error("le prompt devrait citer la boîte mail, qui est branchée")
	}
}
