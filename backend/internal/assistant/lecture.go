package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Les outils de lecture de Raoul, prêtés à d'autres que lui.
//
// L'atelier (Claude Code sur le VPS) fait les tâches des agents ; pour qu'il
// puisse, comme Raoul, chercher dans les mails, Slack, WhatsApp ou l'agenda,
// Cerveau lui prête ces outils-là — et seulement eux. Rien qui écrive, rien
// qui prépare un brouillon, rien qui touche à l'app : la liste est fermée.

var outilsDeLecture = map[string]bool{
	"consulter_calendrier":       true,
	"mes_taches":                 true,
	"chercher_historique":        true,
	"chercher_brouillon":         true,
	"mails_non_lus":              true,
	"lire_mail":                  true,
	"mails_envoyes":              true,
	"chercher_mails":             true,
	"dossiers_mail":              true,
	"parcourir_dossier":          true,
	"slack_non_lus":              true,
	"lire_canal_slack":           true,
	"chercher_slack":             true,
	"whatsapp_non_lus":           true,
	"lire_conversation_whatsapp": true,
	"tickets_csp":                true,
	"memoire_echanges":           true,
	"point_urgences":             true,
}

// ToolSpec : un outil tel qu'un autre client le décrit (MCP, par exemple).
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"inputSchema"`
}

// ReadToolSpecs rend les outils de lecture disponibles avec ces sources.
func ReadToolSpecs(src Sources) []ToolSpec {
	var out []ToolSpec
	for _, t := range toolDefinitions(src) {
		f := t.OfFunction
		if f == nil || !outilsDeLecture[f.Name] {
			continue
		}
		schema := map[string]any(f.Parameters)
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, ToolSpec{Name: f.Name, Description: f.Description.Value, Schema: schema})
	}
	return out
}

// RunReadTool exécute un outil de lecture. Un outil hors de la liste est
// refusé. Quand Raoul aurait demandé une précision (« lequel des deux
// Xavier ? »), la question est rendue comme texte : l'appelant la lit.
func (e *Engine) RunReadTool(ctx context.Context, tb Toolbox, src Sources, loc *time.Location, name, args string) (string, error) {
	if !outilsDeLecture[name] {
		return "", fmt.Errorf("outil non disponible : %s", name)
	}
	if args == "" {
		args = "{}"
	}
	out, _, err := e.runTool(ctx, mailToolbox(tb, src, name, args), loc, name, args)
	if err != nil {
		var ask interface{ instruction() string }
		if errors.As(err, &ask) {
			return ask.instruction(), nil
		}
		return "", err
	}
	return out, nil
}

// ToolLabel : le libellé lisible d'un outil de Raoul (« Lecture des mails »).
func ToolLabel(name string) string { return labelOf(name) }
