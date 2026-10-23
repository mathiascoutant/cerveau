package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// Les deux espaces, et les collègues.
//
// Sa vie est rangée en deux côtés qui ne se mélangent pas : le PRO — la boîte
// mail Gandi, Slack, WhatsApp, les tickets CSP, et tout ce qui en découle — et
// le PERSO — ses projets à lui (VisionShow, Neurorun, Cerveau), le VPS qui les
// héberge, et l'équipe technique qui les suit. « Mes mails côté pro » est une
// consigne de périmètre, pas une précision de politesse : le jour où une boîte
// perso est branchée, la confondre avec la pro serait lui lire le courrier de
// quelqu'un d'autre.
//
// Les collègues sont l'autre nouveauté. Raoul reste l'interlocuteur par
// défaut, celui qui couvre tout ; mais pour la technique de ses projets, il a
// une équipe — un ingénieur logiciel, une responsable informatique — et il
// passe la parole plutôt que de jouer à l'ingénieur.

// Collegue est quelqu'un de l'équipe à qui Raoul peut passer la parole.
type Collegue struct {
	ID   string
	Nom  string
	Role string
	// Quand : ce pour quoi on le fait venir, en une phrase. C'est ce qui
	// apprend à Raoul à qui passer quoi.
	Quand string
}

// PersoAccess : le point sur ses projets perso, tel que Raoul peut le donner
// sans passer la parole. Implémenté côté serveur, qui sait interroger le VPS.
type PersoAccess interface {
	ProjectsBrief(ctx context.Context) (string, error)
}

// SpacedToolbox : une boîte à outils capable de lire la boîte mail de l'autre
// espace. Interface à part, et facultative : les sources qui n'existent qu'en
// un exemplaire n'ont pas à savoir que les espaces existent.
type SpacedToolbox interface {
	InSpace(space string) Toolbox
}

// mailTools : les outils qui lisent une boîte mail, et donc les seuls à qui
// l'espace importe.
var mailTools = map[string]bool{
	"mails_non_lus":         true,
	"lire_mail":             true,
	"mails_envoyes":         true,
	"chercher_mails":        true,
	"dossiers_mail":         true,
	"parcourir_dossier":     true,
	"preparer_reponse_mail": true,
}

// mailToolbox rend la boîte à outils de l'espace visé par un appel.
//
// Le modèle ne reçoit le paramètre espace que si deux boîtes existent. Sans
// lui, c'est l'unique boîte branchée qui sert — la pro si elle est là, la
// perso sinon : demander « laquelle ? » quand il n'y en a qu'une serait une
// question à laquelle il n'y a rien à répondre.
func mailToolbox(tb Toolbox, src Sources, name, args string) Toolbox {
	if !mailTools[name] && !(name == "debriefer" && strings.Contains(args, `"mail"`)) {
		return tb
	}
	var in struct {
		Espace string `json:"espace"`
	}
	_ = json.Unmarshal([]byte(args), &in)
	space := strings.ToLower(strings.TrimSpace(in.Espace))
	if space == "" && !src.Mail && src.MailPerso {
		space = "perso"
	}
	if space != "perso" {
		return tb
	}
	if spaced, ok := tb.(SpacedToolbox); ok {
		return spaced.InSpace("perso")
	}
	return tb
}

// handoff traite passer_la_parole : vérifie que le collègue existe, puis rend
// la main. Le tour de Raoul s'arrête là, et c'est le collègue qui répond à la
// même demande, avec sa voix.
func handoff(equipe []Collegue, args string) (ToolOutput, error) {
	var in struct {
		A string `json:"a"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return ToolOutput{}, err
	}
	want := strings.TrimSpace(in.A)
	for _, c := range equipe {
		if c.ID == want {
			return ToolOutput{
				Payload: fmt.Sprintf("C'est %s (%s) qui répond maintenant. Tu n'ajoutes rien.", c.Nom, strings.ToLower(c.Role)),
				Handoff: c.ID,
			}, nil
		}
	}
	ids := make([]string, 0, len(equipe))
	for _, c := range equipe {
		ids = append(ids, c.ID)
	}
	return ToolOutput{}, fmt.Errorf("personne de ce nom dans l'équipe : choisis parmi %s", strings.Join(ids, ", "))
}

// spaceProperty ajoute le paramètre espace aux outils de mail, quand il y a
// vraiment deux boîtes à départager.
func spaceProperty(src Sources, props map[string]any) map[string]any {
	if !(src.Mail && src.MailPerso) {
		return props
	}
	props["espace"] = map[string]any{
		"type":        "string",
		"enum":        []string{"pro", "perso"},
		"description": "Quelle boîte : « pro » (Gandi, le travail) ou « perso ». Il le dit souvent (« côté pro », « mes mails perso ») ; s'il ne dit rien, regarde les deux en appelant l'outil une fois pour chacune.",
	}
	return props
}

// persoTools : ce que Raoul a sous la main côté perso — le point sur les
// projets, et le passage de parole à l'équipe.
func persoTools(src Sources) []responses.ToolUnionParam {
	tool := func(name, description string, parameters map[string]any) responses.ToolUnionParam {
		t := responses.ToolParamOfFunction(name, parameters, false)
		t.OfFunction.Description = openai.String(description)
		return t
	}
	var tools []responses.ToolUnionParam
	if src.Projets {
		tools = append(tools, tool(
			"etat_projets",
			"Le point sur ses projets PERSO, tels que le serveur les surveille en ce moment : chaque projet et ses composants (backs, sites, apps), s'ils répondent, la version déployée, le dernier déploiement et l'état de la CI GitHub, les bases MongoDB, et l'état du VPS (disque, mémoire, mises à jour, système). À appeler pour une question rapide — « mes backs tournent ? », « le VPS va bien ? », « VisionShow est à jour ? ». Pour un diagnostic, une panne à comprendre ou une question technique qui demande de creuser, ce n'est pas toi : passe la parole à l'ingénieur.",
			map[string]any{"type": "object", "properties": map[string]any{}},
		))
	}
	if len(src.Equipe) > 0 {
		ids := make([]string, 0, len(src.Equipe))
		var who []string
		for _, c := range src.Equipe {
			ids = append(ids, c.ID)
			who = append(who, fmt.Sprintf("« %s » : %s, %s — %s", c.ID, c.Nom, strings.ToLower(c.Role), c.Quand))
		}
		tools = append(tools, tool(
			"passer_la_parole",
			"Passe la conversation à un collègue de l'équipe : c'est lui qui répond à la demande en cours, avec sa voix, et qui garde la parole pour la suite jusqu'à ce qu'il te la rende. À appeler dès qu'il demande à parler à quelqu'un (« passe-moi l'ingénieur », « je peux avoir ma responsable informatique ? ») ET de ta propre initiative quand la demande relève de leur métier. Tu n'annonces rien avant : l'app montre le changement. Collègues : "+strings.Join(who, " ; ")+".",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string", "enum": ids, "description": "Le collègue qui prend la parole."},
				},
				"required": []string{"a"},
			},
		))
	}
	return tools
}

// spaceRules : la séparation pro / perso, dite au modèle. Comme le reste de la
// consigne, elle ne nomme que ce qui est branché.
func spaceRules(src Sources) string {
	var pro []string
	if src.Mail {
		pro = append(pro, "sa boîte mail pro (Gandi)")
	}
	if src.Slack {
		pro = append(pro, "Slack")
	}
	if src.WhatsApp {
		pro = append(pro, "WhatsApp")
	}
	if src.CSP {
		pro = append(pro, "ses tickets CSP")
	}
	var perso []string
	if src.MailPerso {
		perso = append(perso, "sa boîte mail perso")
	}
	if src.Projets {
		perso = append(perso, "ses projets à lui et le VPS qui les héberge")
	}
	if len(src.Equipe) > 0 {
		perso = append(perso, "l'équipe technique qui suit ces projets")
	}
	if len(pro) == 0 && len(perso) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("SES DEUX ESPACES : LE PRO ET LE PERSO\n\n")
	b.WriteString("Sa vie est rangée en deux côtés qui ne se mélangent pas.")
	if len(pro) > 0 {
		fmt.Fprintf(&b, " Le PRO, c'est son travail : %s, et ce qui en découle — ce qu'il a à traiter, les réponses de mail préparées.", strings.Join(pro, ", "))
	}
	if len(perso) > 0 {
		fmt.Fprintf(&b, " Le PERSO, c'est %s.", strings.Join(perso, ", "))
	}
	b.WriteString(" L'agenda et sa liste à faire sont à cheval sur les deux.\n\n")
	b.WriteString("Quand il précise « côté pro » ou « côté perso », tu ne regardes QUE ce côté-là, et tu ne débordes pas sur l'autre. Quand il ne précise rien, la question dit souvent d'elle-même où elle se range : ses tickets sont pro, ses projets sont perso.")
	if src.Mail && src.MailPerso {
		b.WriteString(" Il a DEUX boîtes mail, une pro et une perso, qui ne sont pas la même : « mes mails » sans précision se regarde des deux côtés (un appel par boîte), et tu dis de laquelle vient chaque chose. Un mail perso ne se présente jamais comme un mail de travail, ni l'inverse.")
	} else if src.MailPerso && !src.Mail {
		b.WriteString(" Sa seule boîte mail branchée est la perso : c'est d'elle qu'il parle quand il dit « mes mails ».")
	}
	b.WriteString("\n\n")
	return b.String()
}

// teamRules : quand Raoul passe la parole, et ce qu'il garde pour lui.
func teamRules(src Sources) string {
	if len(src.Equipe) == 0 && !src.Projets {
		return ""
	}
	var b strings.Builder
	b.WriteString("SES PROJETS PERSO ET L'ÉQUIPE TECHNIQUE\n\n")
	if src.Projets {
		b.WriteString("Ses projets perso tournent sur son VPS. Pour une question d'état rapide — « mes backs tournent ? », « le VPS a de la place ? », « VisionShow est à jour ? » — tu appelles etat_projets et tu réponds toi-même, en deux phrases : ce qui va, et surtout ce qui ne va pas. Les identifiants de commit, les ports et les noms de conteneurs ne se lisent pas à voix haute.\n\n")
	}
	if len(src.Equipe) > 0 {
		b.WriteString("Pour tout ce qui demande de creuser — comprendre une panne, une lenteur, un déploiement raté, une question sur le code, une base de données, l'infrastructure — tu n'es pas l'ingénieur et tu ne fais pas semblant : tu passes la parole avec passer_la_parole. Même chose dès qu'il demande à parler à quelqu'un de l'équipe. Tu ne dis rien avant de passer la parole, pas même « je te le passe » : l'app montre qui parle, et c'est le collègue qui répond, tout de suite, à ce qu'il vient de demander.\n\n")
		first := src.Equipe[0]
		fmt.Fprintf(&b, "Dans l'historique, les réponses précédées d'un nom entre crochets — « [%s, %s] » — sont celles d'un collègue, pas les tiennes. Tu peux t'en servir, tu ne te les attribues pas.\n\n",
			first.Nom, strings.ToLower(first.Role))
	}
	return b.String()
}
