// Package tri décide dans quel dossier ranger un mail pro qui arrive.
//
// Deux dossiers, voulus par Mathias, et la boîte de réception pour le reste :
//
//	CSP    — les cycles de contenu des fournisseurs (CSP) des compagnies :
//	         « Condor 1126 - PxCom », « Azul - 1126 PXCom Updates »… ;
//	Autres — les mails de marques et de services qui ne sont pas du travail :
//	         Facebook, Volotea, promotions, newsletters.
//
// Le jugement est confié à Claude par l'API, avec un modèle léger (Claude
// Haiku 5.5 par défaut) : ranger un mail selon des règles nettes et des
// exemples ne demande pas un gros modèle, et il en passe des dizaines par jour.
package tri

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Les dossiers. Reception veut dire : on n'y touche pas.
const (
	CSP                   = "CSP"
	Autres                = "Autres"
	ProcessFlightSchedule = "ProcessFlightSchedule"
	Reception             = "Reception"
)

// Dossiers : ceux que le tri peut remplir, créés dès son démarrage.
var Dossiers = []string{CSP, Autres, ProcessFlightSchedule}

// reglesFixes : les règles voulues par Mathias, qui passent avant le
// jugement de Claude — un envoi automatique se reconnaît à son expéditeur
// (et, pour une adresse qui envoie aussi autre chose, à son objet), sans
// hésitation ni coût.
var reglesFixes = []struct {
	adresse, objet, dossier string
}{
	{"pxalert@pxcom.aero", "", Autres},
	{"processflightschedule@pxcom.aero", "", ProcessFlightSchedule},
	// contact@ reçoit aussi de vrais messages : seules les inscriptions.
	{"contact@pxcom.aero", "new user registration", Autres},
}

// Regle rend le dossier imposé à un mail par son expéditeur et son objet,
// s'il y en a un.
func Regle(adresse, objet string) (string, bool) {
	adresse = strings.ToLower(strings.TrimSpace(adresse))
	objet = strings.ToLower(objet)
	for _, r := range reglesFixes {
		if r.adresse == adresse && strings.Contains(objet, r.objet) {
			return r.dossier, true
		}
	}
	return "", false
}

// ModeleDefaut : le modèle du tri quand TRI_MODELE n'en donne pas.
const ModeleDefaut = "claude-haiku-5-5"

// Mail : ce que le tri voit d'un mail.
type Mail struct {
	// Adresse : celle de l'expéditeur, pour les règles fixes.
	Adresse       string
	De            string
	Objet         string
	A             []string
	PiecesJointes []string
	// Masse : le mail porte les en-têtes d'un envoi de masse (liste de
	// diffusion, désinscription…).
	Masse bool
	Texte string
}

// Decision : où ranger un mail, et pourquoi en quelques mots.
type Decision struct {
	Dossier string
	Raison  string
}

// Trieur interroge Claude.
type Trieur struct {
	client anthropic.Client
	modele string
}

// New rend nil quand aucune clé n'est donnée : pas de tri.
func New(cle, modele string) *Trieur {
	if strings.TrimSpace(cle) == "" {
		return nil
	}
	if strings.TrimSpace(modele) == "" {
		modele = ModeleDefaut
	}
	return &Trieur{client: anthropic.NewClient(option.WithAPIKey(cle)), modele: modele}
}

// Modele : le modèle qui trie.
func (t *Trieur) Modele() string { return t.modele }

const consigne = `Tu tries les mails qui arrivent dans la boîte pro de Mathias, chez PxCom (pxcom.aero). PxCom fait le divertissement à bord (IFE) des compagnies aériennes. Ses fournisseurs de contenu, les CSP (Content Service Providers), lui livrent les contenus des compagnies — images, vidéos, mp3 — par cycles mensuels.

Range chaque mail dans UN dossier :

« CSP » — le mail parle d'un cycle de contenu CSP.
- Signe le plus sûr : l'objet nomme une compagnie aérienne et un code de cycle à 4 chiffres (mois puis année : 1126 = novembre 2026), souvent suivi de PxCom ou du fournisseur. Exemples réels : « Condor 1126 - PxCom », « Azul - 1126 PXCom Updates », « Etihad 1126 - HiFly IFE ». La compagnie, le cycle et le fournisseur changent, la forme reste proche ; les réponses et transferts (« RE: », « TR: », « Fwd: ») de ces fils comptent aussi.
- En cas de doute : un fichier Excel joint (.xlsx, .xls, .csv) et un texte qui parle de livraison de contenu (delivery, assets, médias, films, audio, metadata, upload, cycle…) font pencher vers CSP.
- Un mail d'une compagnie aérienne n'est PAS un mail CSP s'il ne parle pas de contenu : une promotion de vol, une réservation, une carte d'embarquement vont ailleurs.

« Autres » — un mail de marque ou de service qui n'est pas du travail : réseaux sociaux (Facebook, LinkedIn, Instagram…), compagnies aériennes ou voyages côté client (Volotea, offres, réservations), promotions, newsletters commerciales, publicités.

« Reception » — tout le reste : les mails de collègues, de clients, de partenaires, les outils de travail (Tuleap, Gitea, GitHub, Slack, Jira…), les factures et l'administratif, et tout mail dont tu n'es pas sûr. Dans le doute entre deux dossiers, choisis « Reception » : un mail de travail rangé à tort est pire qu'un mail laissé dans la réception.

Pour chaque mail numéroté, rends son numéro, le dossier et une raison de quelques mots.`

var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"tri": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mail":    map[string]any{"type": "integer"},
					"dossier": map[string]any{"type": "string", "enum": []string{CSP, Autres, Reception}},
					"raison":  map[string]any{"type": "string"},
				},
				"required":             []string{"mail", "dossier", "raison"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"tri"},
	"additionalProperties": false,
}

// maxTexte : ce qu'on montre du corps. Le début suffit à dire de quoi parle
// un mail, et garde la requête petite.
const maxTexte = 1500

// Classer rend une décision par mail, dans l'ordre. Un mail que la réponse
// oublie reste dans la réception.
func (t *Trieur) Classer(ctx context.Context, mails []Mail) ([]Decision, error) {
	out := make([]Decision, len(mails))
	for i := range out {
		out[i] = Decision{Dossier: Reception}
	}
	if len(mails) == 0 {
		return out, nil
	}
	// Les règles fixes d'abord ; seul le reste va à Claude.
	var reste []int
	for i, m := range mails {
		if d, ok := Regle(m.Adresse, m.Objet); ok {
			out[i] = Decision{Dossier: d, Raison: "règle fixe : " + m.Adresse}
			continue
		}
		reste = append(reste, i)
	}
	if len(reste) == 0 {
		return out, nil
	}
	var b strings.Builder
	for n, i := range reste {
		m := mails[i]
		fmt.Fprintf(&b, "[%d]\nDe : %s\nObjet : %s\n", n+1, m.De, m.Objet)
		if len(m.A) > 0 {
			fmt.Fprintf(&b, "À : %s\n", strings.Join(m.A, ", "))
		}
		if len(m.PiecesJointes) > 0 {
			fmt.Fprintf(&b, "Pièces jointes : %s\n", strings.Join(m.PiecesJointes, ", "))
		}
		if m.Masse {
			b.WriteString("En-têtes d'envoi de masse : oui\n")
		}
		if texte := strings.TrimSpace(m.Texte); texte != "" {
			fmt.Fprintf(&b, "Texte : %s\n", tronquer(texte, maxTexte))
		}
		b.WriteString("\n")
	}

	resp, err := t.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(t.modele),
		MaxTokens: 4096,
		System:    []anthropic.TextBlockParam{{Text: consigne}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(b.String())),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("tri par Claude : %w", err)
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return nil, errors.New("tri par Claude : demande refusée")
	}
	var texte string
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			texte += tb.Text
		}
	}
	var rep struct {
		Tri []struct {
			Mail    int    `json:"mail"`
			Dossier string `json:"dossier"`
			Raison  string `json:"raison"`
		} `json:"tri"`
	}
	if err := json.Unmarshal([]byte(texte), &rep); err != nil {
		return nil, fmt.Errorf("tri par Claude : réponse illisible : %w", err)
	}
	for _, d := range rep.Tri {
		if d.Mail < 1 || d.Mail > len(reste) {
			continue
		}
		switch d.Dossier {
		case CSP, Autres, Reception:
			out[reste[d.Mail-1]] = Decision{Dossier: d.Dossier, Raison: strings.TrimSpace(d.Raison)}
		}
	}
	return out, nil
}

func tronquer(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
