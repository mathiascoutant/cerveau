package equipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// L'affectation : à qui confier une tâche quand on ne nomme personne.
//
// On choisit sur les compétences, et seulement sur elles : ce que chaque
// agent sait faire (son rôle, sa présentation, ce pour quoi on le sollicite,
// ses outils). Sa disponibilité ne compte pas — l'orchestrateur met la tâche
// en file s'il est occupé ; mieux vaut attendre la bonne personne que d'avoir
// vite la mauvaise.
//
// Le modèle rapide lit les fiches et choisit, avec une raison en une phrase.
// S'il ne répond pas, ou répond un agent qui n'existe pas, un classement par
// mots-clés prend le relais : la tâche est toujours affectée, et on dit
// comment.

// Affectation : l'agent choisi, et pourquoi.
type Affectation struct {
	Agent  string `json:"agent"`
	Raison string `json:"raison"`
	// Par : « modele » quand le modèle a choisi, « mots-cles » pour le
	// classement de secours, « seul » s'il n'y avait qu'un agent.
	Par string `json:"par"`
}

// ErrPersonne : aucun agent à qui confier une tâche.
var ErrPersonne = errors.New("aucun agent dans l'équipe")

// Classifier : un appel court à un modèle, sans outils.
type Classifier func(ctx context.Context, instructions, input string) (string, error)

const affectationTimeout = 15 * time.Second

const affectationConsigne = `Tu répartis les tâches d'une équipe d'agents.
Choisis l'agent dont les COMPÉTENCES conviennent le mieux à la tâche, d'après sa fiche. Ne tiens pas compte de sa disponibilité.
Réponds UNIQUEMENT par un objet JSON, sans texte autour : {"agent": "<identifiant>", "raison": "<une phrase courte en français, qui dit quelle compétence de l'agent correspond à la tâche>"}.
L'identifiant doit être exactement l'un de ceux de la liste.`

// Candidats rend les agents à qui l'on peut confier une tâche : toute
// l'équipe, sauf Raoul — c'est l'application, pas un agent du bâtiment.
func (e *Equipe) Candidats() []Membre {
	var out []Membre
	for _, m := range e.roster {
		if m.ID != IDRaoul {
			out = append(out, m)
		}
	}
	return out
}

// Affecter choisit l'agent le plus compétent pour une tâche.
func (e *Equipe) Affecter(ctx context.Context, demande string) (Affectation, error) {
	cands := e.Candidats()
	switch len(cands) {
	case 0:
		return Affectation{}, ErrPersonne
	case 1:
		return Affectation{Agent: cands[0].ID, Raison: cands[0].Nom + " est le seul agent de l'équipe.", Par: "seul"}, nil
	}
	if classify := e.classifier(); classify != nil {
		cctx, cancel := context.WithTimeout(ctx, affectationTimeout)
		defer cancel()
		if out, err := classify(cctx, affectationConsigne, e.fiches(cands, demande)); err == nil {
			if a, ok := lireAffectation(out, cands); ok {
				a.Par = "modele"
				return a, nil
			}
		}
	}
	return parMotsCles(cands, demande), nil
}

func (e *Equipe) classifier() Classifier {
	if e.classify != nil {
		return e.classify
	}
	if e.engine != nil {
		return e.engine.Classify
	}
	return nil
}

// fiches : ce que le modèle lit pour choisir.
func (e *Equipe) fiches(cands []Membre, demande string) string {
	var b strings.Builder
	b.WriteString("AGENTS :\n")
	for _, m := range cands {
		fmt.Fprintf(&b, "- identifiant : %s\n  nom : %s\n  rôle : %s\n  présentation : %s\n", m.ID, m.Nom, m.Role, m.Presentation)
		if m.Quand != "" {
			fmt.Fprintf(&b, "  à solliciter pour : %s\n", m.Quand)
		}
		if outils := e.Outils(m); len(outils) > 0 {
			labels := make([]string, 0, len(outils))
			for _, o := range outils {
				labels = append(labels, o.Libelle)
			}
			fmt.Fprintf(&b, "  outils : %s\n", strings.Join(labels, ", "))
		}
	}
	fmt.Fprintf(&b, "\nTÂCHE :\n%s\n", demande)
	return b.String()
}

// lireAffectation lit la réponse du modèle ; faux si elle n'est pas
// exploitable ou nomme un agent inconnu.
func lireAffectation(out string, cands []Membre) (Affectation, bool) {
	out = strings.TrimSpace(out)
	if i, j := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && j > i {
		out = out[i : j+1]
	}
	var a Affectation
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		return Affectation{}, false
	}
	a.Agent = strings.TrimSpace(a.Agent)
	for _, m := range cands {
		if m.ID == a.Agent {
			a.Raison = strings.TrimSpace(a.Raison)
			if a.Raison == "" {
				a.Raison = "Ses compétences correspondent à la tâche."
			}
			return a, true
		}
	}
	return Affectation{}, false
}

// -- Classement de secours -------------------------------------------------------

var motsVides = map[string]bool{
	"les": true, "des": true, "une": true, "est": true, "sur": true, "par": true, "pas": true, "son": true, "ses": true,
	"aux": true, "tes": true, "ton": true, "toi": true, "moi": true, "oui": true, "non": true, "the": true, "and": true,
	"avec": true, "dans": true, "pour": true, "sans": true, "sous": true, "vers": true, "chez": true, "entre": true,
	"est-ce": true, "quel": true, "quelle": true, "quels": true, "quelles": true, "comment": true, "pourquoi": true,
	"peux": true, "peut": true, "veux": true, "voudrais": true, "faire": true, "fais": true, "dire": true, "dis-moi": true,
	"tout": true, "tous": true, "toute": true, "toutes": true, "cette": true, "ceux": true, "celle": true, "cela": true,
	"mais": true, "donc": true, "alors": true, "aussi": true, "plus": true, "moins": true, "très": true, "tres": true,
	"mon": true, "mes": true, "notre": true, "nos": true, "votre": true, "vos": true, "leur": true, "leurs": true,
	"elle": true, "il": true, "nous": true, "vous": true, "ils": true, "elles": true, "qui": true, "que": true, "quoi": true,
	"stp": true, "merci": true, "bien": true, "voir": true, "regarde": true, "regarder": true, "moment": true,
}

// racine : minuscules sans accents, coupée à six lettres — « déploiement »,
// « déploie », « déployer » se retrouvent.
func racine(mot string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(mot)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if r := []rune(s); len(r) > 6 {
		s = string(r[:6])
	}
	return s
}

func mots(texte string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(texte, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' }) {
		w = strings.Trim(w, "-")
		if len([]rune(w)) < 3 || motsVides[strings.ToLower(w)] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// parMotsCles : l'agent dont la fiche partage le plus de mots avec la tâche.
// Ce pour quoi on le sollicite compte double. Sans aucun mot commun, la tâche
// va à celui à qui les autres rendent compte : c'est lui qui répartit.
func parMotsCles(cands []Membre, demande string) Affectation {
	tache := map[string]string{}
	for _, w := range mots(demande) {
		tache[racine(w)] = strings.ToLower(w)
	}
	type score struct {
		m       Membre
		points  int
		communs []string
	}
	var scores []score
	for _, m := range cands {
		s := score{m: m}
		vus := map[string]bool{}
		poids := []struct {
			texte string
			fois  int
		}{{m.Quand, 2}, {m.Presentation, 1}, {m.Role, 1}}
		for _, p := range poids {
			for _, w := range mots(p.texte) {
				r := racine(w)
				if mot, ok := tache[r]; ok {
					s.points += p.fois
					if !vus[r] {
						vus[r] = true
						s.communs = append(s.communs, mot)
					}
				}
			}
		}
		scores = append(scores, s)
	}
	sort.SliceStable(scores, func(i, j int) bool { return scores[i].points > scores[j].points })
	best := scores[0]
	if best.points > 0 {
		return Affectation{
			Agent:  best.m.ID,
			Raison: fmt.Sprintf("Sa fiche couvre : %s.", strings.Join(best.communs, ", ")),
			Par:    "mots-cles",
		}
	}
	chef := cands[0]
	for _, m := range cands {
		for _, autre := range cands {
			if autre.Superieur == m.ID {
				chef = m
			}
		}
	}
	return Affectation{
		Agent:  chef.ID,
		Raison: fmt.Sprintf("Aucune compétence ne se détache : %s répartit le travail de l'équipe.", chef.Nom),
		Par:    "mots-cles",
	}
}
