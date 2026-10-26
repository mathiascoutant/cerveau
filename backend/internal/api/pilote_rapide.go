package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Hugo répond lui-même à ce qui ne demande pas l'équipe.
//
// Dans l'atelier, chaque demande lance Claude Code sur le VPS : une vingtaine
// de secondes avant le premier mot, même pour « ça va ? ». Avant d'y aller, un
// modèle rapide lit la demande avec ce que Hugo sait déjà — son équipe, qui
// travaille sur quoi, les derniers échanges — et répond si cela suffit. Tout
// ce qui demande de chercher, de lire ou de confier part à l'atelier, comme
// avant.

// DefaultPiloteModele : Haiku, le plus rapide ; la réponse directe n'est faite
// que de ce qu'on lui donne, sans outil. PILOTE_MODELE le remplace.
const DefaultPiloteModele = "claude-haiku-5-5"

// piloteDirectTimeout : au-delà, on renonce et l'atelier prend la demande.
const piloteDirectTimeout = 20 * time.Second

type piloteDirect struct {
	client anthropic.Client
	modele string
}

func newPiloteDirect(cle, modele string) *piloteDirect {
	if cle == "" {
		return nil
	}
	if modele == "" {
		modele = DefaultPiloteModele
	}
	return &piloteDirect{client: anthropic.NewClient(option.WithAPIKey(cle)), modele: modele}
}

var piloteDirectSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"repondre": map[string]any{"type": "boolean"},
		"reponse":  map[string]any{"type": "string"},
	},
	"required":             []string{"repondre", "reponse"},
	"additionalProperties": false,
}

// repondre rend la réponse de Hugo, ou false s'il faut l'atelier.
func (p *piloteDirect) repondre(ctx context.Context, contexte, demande string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, piloteDirectTimeout)
	defer cancel()
	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(p.modele),
		MaxTokens: 1024,
		System:    []anthropic.TextBlockParam{{Text: contexte}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow,
			Format: anthropic.JSONOutputFormatParam{Schema: piloteDirectSchema},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(demande)),
		},
	})
	if err != nil {
		return "", false, err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", false, nil
	}
	var texte string
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			texte += tb.Text
		}
	}
	var rep struct {
		Repondre bool   `json:"repondre"`
		Reponse  string `json:"reponse"`
	}
	if err := json.Unmarshal([]byte(texte), &rep); err != nil {
		return "", false, fmt.Errorf("réponse illisible : %w", err)
	}
	rep.Reponse = strings.TrimSpace(rep.Reponse)
	return rep.Reponse, rep.Repondre && rep.Reponse != "", nil
}

// consignePiloteDirect : ce que Hugo sait sans rien chercher.
func (s *Server) consignePiloteDirect(ctx context.Context, user *store.User, moi equipe.Membre) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tu es %s, %s de l'équipe d'agents de %s. %s\n\nTon équipe :\n",
		moi.Nom, strings.ToLower(moi.Role), s.userName(user), moi.Presentation)
	agents, _ := s.hqAgents(ctx, user)
	for _, a := range agents {
		if a.ID == moi.ID {
			continue
		}
		fmt.Fprintf(&b, "- %s, %s. %s", a.Nom, strings.ToLower(a.Role), a.Presentation)
		switch {
		case a.Statut == hqEnCours && a.Tache != nil:
			fmt.Fprintf(&b, " En ce moment : %s", clipTo(a.Tache.Demande, 160))
		case a.Statut == hqEnAttente:
			b.WriteString(" Une tâche l'attend.")
		default:
			b.WriteString(" Disponible.")
		}
		b.WriteString("\n")
	}
	if hist := s.history(ctx, user, moi.ID); len(hist) > 0 {
		b.WriteString("\nVos derniers échanges (les plus anciens d'abord) :\n")
		for _, t := range hist[max(0, len(hist)-6):] {
			fmt.Fprintf(&b, "- Lui : %s\n  Toi : %s\n", clipTo(t.User, 300), clipTo(t.Assistant, 500))
		}
	}
	fmt.Fprintf(&b, `
Nous sommes le %s.

Tu reçois un message de %s. Deux cas :
- Tu peux y répondre avec ce qui est écrit ici, sans rien chercher : une salutation, une question sur toi ou sur l'équipe, qui fait quoi, qui est occupé, une suite simple de vos derniers échanges. Alors repondre = true, et reponse = ta réponse : en français, chaleureuse et brève (trois phrases au plus), à l'oral — pas de liste, pas de markdown, elle peut être lue à voix haute.
- Il faut chercher, lire, vérifier ou faire travailler quelqu'un : un projet, du code, un serveur, des mails, Slack, WhatsApp, une fonctionnalité, un chiffre que tu n'as pas ici, une tâche à confier. Alors repondre = false et reponse = "" : la demande part à l'atelier, où tu pilotes l'équipe avec tes outils.
Dans le doute, repondre = false. N'invente jamais un état ou un résultat.`,
		time.Now().In(s.location(user)).Format("02/01/2006 15:04"), s.userName(user))
	return b.String()
}

func clipTo(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// piloteRepond : la réponse directe de Hugo, quand il en a une. L'étape se
// voit dans le bâtiment, qu'il réponde ou qu'il passe la main à l'atelier.
func (s *Server) piloteRepond(ctx context.Context, user *store.User, m equipe.Membre, demande string, etape assistant.Emitter) (string, bool) {
	if !m.Pilote || s.direct == nil {
		return "", false
	}
	const outil, libelle = "reponse_directe", "Lecture de la demande"
	debut := time.Now()
	etape(assistant.Event{Type: "status", Tool: outil, Label: libelle})
	reply, ok, err := s.direct.repondre(ctx, s.consignePiloteDirect(ctx, user, m), demande)
	fin := assistant.Event{Type: "status", Tool: outil, Label: libelle, Ms: max(1, time.Since(debut).Milliseconds())}
	if err != nil {
		slog.Warn("réponse directe du pilote", "err", err)
		fin.Err = err.Error()
	}
	etape(fin)
	return reply, ok && err == nil
}
