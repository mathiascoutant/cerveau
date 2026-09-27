package tuleap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Les cycles CSP, tels que PXFeed les sélectionne.
//
// Ce fichier transpose `csp/lib/tuleap-csp-cycles.js` et
// `csp/lib/csp-ticket-parse.js` du serveur PXFeed, règle pour règle, pour que
// l'onglet Jobs de Raoul montre la même liste que PXFeed-UI :
//
//  1. les artefacts viennent du contenu de la release (milestone) du planning,
//     avec repli sur le tracker entier ;
//  2. un artefact est un cycle si son TITRE se lit comme un cycle CSP —
//     « [Azul] CSP cycle November follow-up (2026-11) », « [RAM] Process
//     November 2026 », ou « Process Mai 2026 » quand le champ Projet donne la
//     compagnie. Un titre illisible fait relire la fiche complète avant de
//     renoncer ;
//  3. un statut clos (done, closed, terminé, archivé) ou de déploiement
//     (« deploy ») écarte le cycle ;
//  4. tri : chemin de cycle décroissant, puis compagnie.
//
// Un seul écart, assumé : PXFeed ne garde que les compagnies qui ont un feed
// servi par un serveur PXFeed avec l'UI activée. Raoul n'a pas cette
// configuration ; CSPScope.Airlines en tient lieu, vide = toutes.

// Valeurs de PXFeed (K_TRACKER_ID, K_RELEASE_ID, K_PLANNING_ID, K_PROJECT_ID).
const (
	DefaultCSPTrackerID  = 427
	DefaultCSPReleaseID  = 12051
	DefaultCSPPlanningID = 69
	DefaultCSPProjectID  = 138
)

// Nombre de fiches complètes relues en parallèle quand le titre ne suffit pas
// (TULEAP_ENRICH_CONCURRENCY chez PXFeed).
const enrichConcurrency = 10

// CSPScope délimite la sélection façon PXFeed.
type CSPScope struct {
	TrackerID, ReleaseID, PlanningID, ProjectID int
	// Airlines : compagnies gardées, comparées comme PXFeed compare une
	// compagnie à ses feeds (clé normalisée, inclusion dans les deux sens).
	// Vide : toutes.
	Airlines []string
}

// CSPStats dit ce que la sélection a fait, comme filterStats chez PXFeed.
type CSPStats struct {
	Total          int `json:"total"`
	TitreReconnu   int `json:"titre_reconnu"`
	StatutExclu    int `json:"statut_exclu"`
	CompagnieExclu int `json:"compagnie_exclue"`
	Retenus        int `json:"retenus"`
}

// CSPResult est la liste et d'où elle vient. Chaque ticket porte Compagnie,
// Cycle et Label, remplis par le parsing du titre.
type CSPResult struct {
	Tickets []Ticket
	// Source : l'adresse qui a rendu les artefacts (release ou tracker).
	Source string
	Stats  CSPStats
}

// Cycles rend les cycles CSP ouverts, dans l'ordre de PXFeed-UI.
func (c *Client) Cycles(ctx context.Context, scope CSPScope) (CSPResult, error) {
	if scope.TrackerID <= 0 {
		scope.TrackerID = DefaultCSPTrackerID
	}
	if scope.ReleaseID <= 0 {
		scope.ReleaseID = DefaultCSPReleaseID
	}
	if scope.PlanningID <= 0 {
		scope.PlanningID = DefaultCSPPlanningID
	}
	if scope.ProjectID <= 0 {
		scope.ProjectID = DefaultCSPProjectID
	}

	raw, source, err := c.linkedArtifacts(ctx, scope)
	if err != nil {
		return CSPResult{}, err
	}
	res := CSPResult{Source: source, Stats: CSPStats{Total: len(raw)}}

	enriched := c.enrich(ctx, raw)
	for _, e := range enriched {
		if e.parsed == nil {
			continue
		}
		res.Stats.TitreReconnu++
		status := statusLabel(e.artifact)
		if IsExcludedCSPStatus(status) {
			res.Stats.StatutExclu++
			continue
		}
		if len(scope.Airlines) > 0 && !airlineAllowed(e.parsed.airline, scope.Airlines) {
			res.Stats.CompagnieExclu++
			continue
		}
		t := c.flatten(e.artifact)
		if t.Statut == "" {
			t.Statut = status
		}
		if t.Statut == "" {
			t.Statut = "Unknown"
		}
		t.Titre = e.parsed.raw
		t.Ferme = false
		t.Compagnie = e.parsed.airline
		t.Cycle = e.parsed.cycle
		t.Label = e.parsed.airline + " " + e.parsed.cycle
		res.Tickets = append(res.Tickets, t)
		res.Stats.Retenus++
	}

	sort.SliceStable(res.Tickets, func(i, j int) bool {
		if res.Tickets[i].Cycle != res.Tickets[j].Cycle {
			return res.Tickets[i].Cycle > res.Tickets[j].Cycle
		}
		return res.Tickets[i].Compagnie < res.Tickets[j].Compagnie
	})
	return res, nil
}

// linkedArtifacts essaie, dans l'ordre de PXFeed, les adresses qui rendent le
// contenu de la release, puis se rabat sur le tracker entier.
func (c *Client) linkedArtifacts(ctx context.Context, scope CSPScope) ([]artifact, string, error) {
	candidates := []string{
		fmt.Sprintf("/api/planning_milestones/%d/content", scope.ReleaseID),
		fmt.Sprintf("/api/plannings/%d/milestones/%d/content", scope.PlanningID, scope.ReleaseID),
		fmt.Sprintf("/api/projects/%d/planning_backlogs/%d/milestones/%d/content", scope.ProjectID, scope.PlanningID, scope.ReleaseID),
		fmt.Sprintf("/api/artifacts/%d/linked_artifacts", scope.ReleaseID),
		fmt.Sprintf("/api/artifacts/%d/children", scope.ReleaseID),
	}
	var lastErr error
	for _, path := range candidates {
		var body json.RawMessage
		if err := c.get(ctx, path, url.Values{"limit": {"500"}, "offset": {"0"}}, &body); err != nil {
			lastErr = err
			continue
		}
		if list := normalizeArtifactList(body); len(list) > 0 {
			return list, path, nil
		}
	}

	// Repli : le tracker, page par page — PXFeed demande 500 d'un coup, mais
	// une instance qui plafonne la page ne doit pas tronquer la liste en
	// silence.
	path := fmt.Sprintf("/api/trackers/%d/artifacts", scope.TrackerID)
	var all []artifact
	for offset := 0; offset < 500; offset += 100 {
		var body json.RawMessage
		total, err := c.getPaged(ctx, path, url.Values{
			"limit": {"100"}, "offset": {strconv.Itoa(offset)},
		}, &body)
		if err != nil {
			lastErr = err
			break
		}
		page := normalizeArtifactList(body)
		all = append(all, page...)
		if len(page) < 100 || (total > 0 && offset+100 >= total) {
			break
		}
	}
	if len(all) > 0 {
		return all, path, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("aucun artefact CSP sur Tuleap")
	}
	return nil, "", lastErr
}

// normalizeArtifactList lit une liste d'artefacts quelle que soit son
// enveloppe : tableau nu, { collection }, { artifacts }, { content } — et
// déballe { artifact: {…} } quand une entrée l'emballe.
func normalizeArtifactList(body json.RawMessage) []artifact {
	var entries []json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil {
		var wrapped struct {
			Collection []json.RawMessage `json:"collection"`
			Artifacts  []json.RawMessage `json:"artifacts"`
			Content    []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(body, &wrapped) != nil {
			return nil
		}
		switch {
		case len(wrapped.Collection) > 0:
			entries = wrapped.Collection
		case len(wrapped.Artifacts) > 0:
			entries = wrapped.Artifacts
		case len(wrapped.Content) > 0:
			entries = wrapped.Content
		}
	}
	out := make([]artifact, 0, len(entries))
	for _, raw := range entries {
		var a artifact
		if json.Unmarshal(raw, &a) != nil {
			continue
		}
		var wrap struct {
			Artifact json.RawMessage `json:"artifact"`
		}
		if json.Unmarshal(raw, &wrap) == nil && len(wrap.Artifact) > 0 {
			var inner artifact
			if json.Unmarshal(wrap.Artifact, &inner) == nil && inner.ID > 0 {
				a = inner
			}
		}
		if a.ID > 0 {
			out = append(out, a)
		}
	}
	return out
}

type enrichedArtifact struct {
	artifact artifact
	parsed   *parsedTitle
}

// enrich lit le titre, et relit la fiche complète quand il ne suffit pas —
// le contenu d'une release est parfois rendu sans ses champs.
func (c *Client) enrich(ctx context.Context, raw []artifact) []enrichedArtifact {
	out := make([]enrichedArtifact, len(raw))
	sem := make(chan struct{}, enrichConcurrency)
	var wg sync.WaitGroup
	for i, a := range raw {
		wg.Add(1)
		go func(i int, a artifact) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			parsed := ParseCSPTitle(a.Title, projectLabel(a))
			if parsed == nil && a.ID > 0 {
				var detail artifact
				if err := c.get(ctx, fmt.Sprintf("/api/artifacts/%d", a.ID), nil, &detail); err == nil && detail.ID > 0 {
					a = detail
					parsed = ParseCSPTitle(a.Title, projectLabel(a))
				}
			}
			out[i] = enrichedArtifact{artifact: a, parsed: parsed}
		}(i, a)
	}
	wg.Wait()
	return out
}

// projectLabel : le champ « Projet » de l'artefact, quand il désigne une
// compagnie. « Aucun » vaut absent.
func projectLabel(a artifact) string {
	for _, v := range a.Values {
		name := strings.ToLower(strings.TrimSpace(v.Label))
		if name != "projet" && name != "project" {
			continue
		}
		label := stripHTML(renderValue(v))
		if label == "" || strings.EqualFold(label, "aucun") || strings.EqualFold(label, "aucune") {
			continue
		}
		return label
	}
	return ""
}

// statusLabel reprend extractStatusLabel : le statut porté par l'artefact,
// sinon un champ dont le nom contient « status ».
func statusLabel(a artifact) string {
	if s := strings.TrimSpace(a.Status); s != "" {
		return s
	}
	for _, v := range a.Values {
		if strings.Contains(strings.ToLower(v.Label), "status") {
			if s := renderValue(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// --- le parsing des titres (csp-ticket-parse.js) -----------------------------

var (
	cspTitleRE       = regexp.MustCompile(`(?i)^\[([^\]]+)\]\s+CSP cycle\s+.+\((\d{4}-\d{2})\)\s*$`)
	bracketProcessRE = regexp.MustCompile(`(?i)^\[([^\]]+)\]\s+Process\s+([A-Za-zÀ-ÿ]+)\s+(\d{4})\s*$`)
	plainProcessRE   = regexp.MustCompile(`(?i)^Process\s+([A-Za-zÀ-ÿ]+)\s+(\d{4})\s*$`)
)

var monthToMM = map[string]string{
	"january": "01", "february": "02", "march": "03", "april": "04", "may": "05", "june": "06",
	"july": "07", "august": "08", "september": "09", "october": "10", "november": "11", "december": "12",
	"janvier": "01", "fevrier": "02", "février": "02", "mars": "03", "avril": "04", "mai": "05", "juin": "06",
	"juillet": "07", "aout": "08", "août": "08", "septembre": "09", "octobre": "10", "novembre": "11",
	"decembre": "12", "décembre": "12",
}

type parsedTitle struct {
	airline string
	cycle   string
	raw     string
}

// ParseCSPTitle lit un titre de ticket comme PXFeed : compagnie et chemin de
// cycle, ou rien.
func ParseCSPTitle(title, project string) *parsedTitle {
	raw := strings.TrimSpace(title)
	if raw == "" {
		return nil
	}
	if m := cspTitleRE.FindStringSubmatch(raw); m != nil {
		return &parsedTitle{airline: strings.TrimSpace(m[1]), cycle: m[2], raw: raw}
	}
	if m := bracketProcessRE.FindStringSubmatch(raw); m != nil {
		cycle := cyclePathFromMonthYear(m[2], m[3])
		if cycle == "" {
			return nil
		}
		return &parsedTitle{airline: strings.TrimSpace(m[1]), cycle: cycle, raw: raw}
	}
	if m := plainProcessRE.FindStringSubmatch(raw); m != nil && strings.TrimSpace(project) != "" {
		cycle := cyclePathFromMonthYear(m[1], m[2])
		if cycle == "" {
			return nil
		}
		return &parsedTitle{airline: strings.TrimSpace(project), cycle: cycle, raw: raw}
	}
	return nil
}

func cyclePathFromMonthYear(month, year string) string {
	mm, ok := monthToMM[strings.ToLower(strings.TrimSpace(month))]
	if !ok {
		return ""
	}
	y := strings.TrimSpace(year)
	if len(y) != 4 {
		return ""
	}
	return y + "-" + mm
}

// IsExcludedCSPStatus : les statuts que PXFeed-UI cache — clos, et en
// déploiement.
func IsExcludedCSPStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	if s == "" {
		return false
	}
	for _, w := range []string{"done", "clos", "closed", "termin", "archiv", "deploy"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// normalizeAirlineKey reprend normalizeAirlineKey : minuscules, sans accents,
// sans rien d'autre que lettres et chiffres.
func normalizeAirlineKey(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	plain, _, err := transform.String(t, strings.ToLower(s))
	if err != nil {
		plain = strings.ToLower(s)
	}
	var b strings.Builder
	for _, r := range plain {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// airlineAllowed compare comme feedsMatchingAirlineLabel : égalité ou
// inclusion dans un sens ou l'autre, sur les clés normalisées.
func airlineAllowed(airline string, allowed []string) bool {
	target := normalizeAirlineKey(airline)
	if target == "" {
		return false
	}
	for _, a := range allowed {
		key := normalizeAirlineKey(a)
		if key == "" {
			continue
		}
		if key == target || strings.Contains(key, target) || strings.Contains(target, key) {
			return true
		}
	}
	return false
}
