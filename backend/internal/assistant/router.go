package assistant

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/openai/openai-go/v3/shared"
)

// Le routage : quel modèle pour quelle question.
//
// Trois étages, et une règle de prudence qui vaut plus que les trois : dans le
// doute, on prend l'étage du milieu. Une question simple servie par le modèle
// à outils coûte quelques centimes de trop ; une question qui demandait un mail
// et qu'on a envoyée au modèle sans outils produit une réponse inventée avec
// l'aplomb d'une vraie. Le premier écart se mesure, le second se paie.
//
//   - Fast : conversation pure. Salutations, remerciements, connaissances
//     générales, résumé ou reformulation du texte fourni. Aucun outil n'est
//     donné au modèle, et la consigne se réduit au ton. C'est le seul étage où
//     l'on économise vraiment — et le classement n'y envoie que ce qu'il
//     reconnaît formellement.
//   - Tools : tout le reste. Le modèle principal, avec ses outils et sa
//     consigne complète.
//   - Deep : la demande croise plusieurs sources ou exige une reconstitution
//     (« compare le ticket et ce que Xavier a dit », « qui a demandé quoi à
//     qui »). Le modèle fort, avec un budget de raisonnement, outils compris.
//
// Rien ici n'appelle un modèle : classer coûterait un aller-retour de plus,
// sur toutes les questions, pour n'en accélérer qu'une partie.

type Tier string

const (
	TierFast  Tier = "fast"
	TierTools Tier = "tools"
	TierDeep  Tier = "deep"
)

// Route est la décision de routage, avec sa raison — c'est ce qui la rend
// lisible dans les journaux et discutable quand elle se trompe.
type Route struct {
	Tier   Tier
	Model  shared.ResponsesModel
	Effort shared.ReasoningEffort
	Reason string
}

// Models regroupe les trois modèles et leurs efforts. Tout vient de la
// configuration : changer de modèle ne demande pas de recompiler.
type Models struct {
	Fast, Tools, Deep              shared.ResponsesModel
	FastEffort, ToolsEffort, Deep2 shared.ReasoningEffort
}

// greetingRE : ce qu'on dit en arrivant ou en partant. Rien là-dedans ne
// demande une source.
var greetingRE = regexp.MustCompile(`^(?:(?:bon(?:jour|soir)|salut|hello|hey|yo|coucou|merci(?:\s+beaucoup)?|super|parfait|ok(?:ay)?|top|ça\s+va\s*\??|ca\s+va\s*\??|tu\s+vas\s+bien\s*\??|à\s+plus|a\s+plus|bonne\s+(?:journée|soirée|nuit)|à\s+demain|a\s+demain|de\s+rien|nickel|génial|cool|d'accord|bien\s+reçu|c'est\s+noté)[\s,!.?]*(?:raoul[\s,!.?]*)?)+$`)

// generalRE : des demandes de connaissance ou de traitement du texte fourni,
// qui ne portent sur aucune de ses données.
var generalRE = regexp.MustCompile(`^(?:explique(?:-moi)?|c'est\s+quoi|qu'est-ce\s+que\s+c'est|qu'est-ce\s+qu'un|qu'est-ce\s+qu'une|définis|definis|traduis|reformule|corrige|résume\s+(?:ce|le)\s+texte|resume\s+(?:ce|le)\s+texte|raconte(?:-moi)?\s+une\s+blague|quelle\s+est\s+la\s+différence\s+entre|comment\s+(?:fonctionne|marche)\s+(?:un|une|le|la|les)\s)`)

// sourceWords : la présence d'un de ces mots interdit l'étage rapide, même si
// la phrase ressemble à une question générale. « Explique-moi le mail de
// Cyril » n'est pas une question de culture générale.
var sourceWords = []string{
	"mail", "mél", "courriel", "message", "slack", "whatsapp", "canal", "channel", "fil ", "thread",
	"ticket", "csp", "tuleap", "agenda", "calendrier", "rendez", "réunion", "reunion", "créneau", "creneau",
	"tâche", "tache", "todo", "brouillon", "réponse", "reponse", "urgence", "urgent", "envoyé", "envoye",
	"reçu", "recu", "répondu", "repondu", "demandé", "demande", "écrit", "ecrit", "dit ", "a dit", "hier", "aujourd", "demain",
	"semaine", "matin", "soir", "dernier", "dernière", "derniere", "qui a", "qu'est-ce que", "planning", "vol", "flight",
	"note", "retiens", "rappelle", "souviens", "oublie", "emmène", "emmene", "itinéraire", "itineraire",
}

// deepMarkers : ce qui signale une analyse à plusieurs sources ou une
// reconstitution de rôles. Un seul marqueur ne suffit pas toujours ; c'est la
// combinaison avec plusieurs sources qui fait basculer.
var deepMarkers = []string{
	"compare", "croise", "recoupe", "confronte", "analyse", "synthèse", "synthese", "débrief", "debrief",
	"qui a demandé quoi", "qui a dit quoi", "qui a fait quoi", "reconstitue", "chronologie", "où en est", "ou en est",
	"vue d'ensemble", "bilan", "point complet", "tout ce qui", "toutes les", "tous les",
}

// sourceFamilies : les familles de sources qu'une question peut croiser.
var sourceFamilies = [][]string{
	{"mail", "mél", "courriel", "boîte", "boite"},
	{"slack", "canal", "channel"},
	{"whatsapp", "groupe"},
	{"ticket", "csp", "tuleap"},
	{"agenda", "calendrier", "rendez", "réunion", "reunion", "créneau", "creneau"},
}

// Classify décide de l'étage à partir du texte de la demande et du dernier
// tour. Le dernier tour compte : « et lui, il a répondu quoi ? » n'a aucun mot
// de source, mais suit une question sur un mail.
func Classify(text string, previous *Turn) (Tier, string) {
	norm := normalizeRoute(text)
	if norm == "" {
		return TierTools, "vide"
	}

	families := countFamilies(norm)
	hasDeep := containsAny(norm, deepMarkers)
	switch {
	case families >= 2 && hasDeep:
		return TierDeep, "plusieurs sources à croiser"
	case families >= 2 && len(norm) > 80:
		return TierDeep, "plusieurs sources dans une demande longue"
	case strings.Contains(norm, "qui a demandé quoi") || strings.Contains(norm, "qui a dit quoi") ||
		strings.Contains(norm, "qui a fait quoi") || strings.Contains(norm, "reconstitue"):
		return TierDeep, "reconstitution des rôles"
	}

	if greetingRE.MatchString(norm) {
		return TierFast, "salutation"
	}
	if containsAny(norm, sourceWords) {
		return TierTools, "mots de source"
	}
	// Un tour précédent qui a touché une source rend la suite dépendante des
	// outils : on ne rétrograde pas au milieu d'un fil.
	if previous != nil && containsAny(normalizeRoute(previous.User), sourceWords) {
		return TierTools, "suite d'une question outillée"
	}
	if generalRE.MatchString(norm) {
		return TierFast, "connaissance générale"
	}
	// Une question très courte sans aucun mot de source, posée hors contexte :
	// « ça va ? », « t'es là ? ». Tout le reste va aux outils.
	if len([]rune(norm)) <= 12 && previous == nil {
		return TierFast, "très courte, hors contexte"
	}
	return TierTools, "défaut"
}

// Decide applique la classification aux modèles configurés.
func (m Models) Decide(text string, previous *Turn) Route {
	tier, reason := Classify(text, previous)
	switch tier {
	case TierFast:
		if m.Fast != "" {
			return Route{Tier: TierFast, Model: m.Fast, Effort: m.FastEffort, Reason: reason}
		}
		// Pas de modèle rapide configuré : l'étage garde son sens (pas
		// d'outils, consigne courte) mais sur le modèle principal.
		return Route{Tier: TierFast, Model: m.Tools, Effort: m.FastEffort, Reason: reason + " (modèle principal)"}
	case TierDeep:
		if m.Deep != "" {
			return Route{Tier: TierDeep, Model: m.Deep, Effort: m.Deep2, Reason: reason}
		}
		return Route{Tier: TierTools, Model: m.Tools, Effort: m.ToolsEffort, Reason: reason + " (pas de modèle fort configuré)"}
	}
	return Route{Tier: TierTools, Model: m.Tools, Effort: m.ToolsEffort, Reason: reason}
}

func normalizeRoute(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func countFamilies(s string) int {
	n := 0
	for _, family := range sourceFamilies {
		if containsAny(s, family) {
			n++
		}
	}
	return n
}
