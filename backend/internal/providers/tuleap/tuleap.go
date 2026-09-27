// Package tuleap lit les artefacts d'un tracker Tuleap par son API REST.
//
// C'est la source des tickets CSP de l'onglet Jobs. L'accès se fait avec une
// clé d'accès personnelle (Tuleap › Mon compte › Clés d'accès), envoyée dans
// l'en-tête X-Auth-AccessKey : les tickets remontés sont exactement ceux que
// l'utilisateur a le droit de voir, ni plus ni moins.
//
// Le périmètre — quel tracker, quels tickets — est de la configuration, pas du
// code : voir Scope. Il reproduit la sélection de PXFeed-UI dès qu'on lui
// donne la même requête, sans figer une règle métier dans un fichier Go.
package tuleap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client parle à une instance Tuleap au nom d'un utilisateur.
type Client struct {
	base string
	key  string
	http *http.Client
}

func New(baseURL, accessKey string) *Client {
	return &Client{
		base: strings.TrimSuffix(strings.TrimSpace(baseURL), "/"),
		key:  strings.TrimSpace(accessKey),
		http: &http.Client{Timeout: 25 * time.Second},
	}
}

// Scope délimite les tickets à remonter.
type Scope struct {
	// TrackerID : le tracker CSP.
	TrackerID int
	// Query : la sélection côté serveur, au format JSON de Tuleap
	// (« {"status":"open"} », ou des critères à opérateurs). Vide = tous.
	Query string
	// ExpertQuery : la sélection en syntaxe TQL (« status = 'Open' AND
	// assigned_to = MYSELF() »). Prime sur Query quand elle est renseignée.
	ExpertQuery string
	// AssignedToMe : ne garder, après coup, que les tickets assignés à
	// l'utilisateur de la clé.
	AssignedToMe bool
	// Limit : nombre maximal de tickets rapatriés.
	Limit int
}

// Ticket est un artefact mis à plat pour l'affichage. Les champs nommés sont
// ceux que tout tracker de tickets possède ; Champs garde le reste, par
// libellé, pour la vue de détail.
type Ticket struct {
	ID          int               `json:"id"`
	Ref         string            `json:"ref"`
	Titre       string            `json:"titre"`
	Statut      string            `json:"statut"`
	Priorite    string            `json:"priorite,omitempty"`
	Responsable string            `json:"responsable,omitempty"`
	Auteur      string            `json:"auteur,omitempty"`
	Cree        time.Time         `json:"cree"`
	Modifie     time.Time         `json:"modifie"`
	URL         string            `json:"url"`
	Tracker     string            `json:"tracker,omitempty"`
	Description string            `json:"description,omitempty"`
	Champs      map[string]string `json:"champs,omitempty"`
	// Ferme : le statut appartient au groupe des statuts clos, tel que Tuleap
	// le sait par la sémantique du tracker.
	Ferme bool `json:"ferme,omitempty"`

	// Lecture « cycle CSP » (voir csp.go) : la compagnie et le chemin de cycle
	// tirés du titre, et le libellé que PXFeed-UI affiche (« Azul 2026-11 »).
	Compagnie string `json:"compagnie,omitempty"`
	Cycle     string `json:"cycle,omitempty"`
	Label     string `json:"label,omitempty"`
}

// Commentaire est une entrée de l'historique d'un ticket qui porte un texte.
type Commentaire struct {
	Auteur string    `json:"auteur"`
	Quand  time.Time `json:"quand"`
	Texte  string    `json:"texte"`
}

// Me vérifie la clé et rend l'utilisateur qu'elle représente.
func (c *Client) Me(ctx context.Context) (id int, name string, err error) {
	var me struct {
		ID          int    `json:"id"`
		Username    string `json:"username"`
		RealName    string `json:"real_name"`
		DisplayName string `json:"display_name"`
	}
	if err := c.get(ctx, "/api/users/self", nil, &me); err != nil {
		return 0, "", err
	}
	name = me.DisplayName
	if name == "" {
		name = me.RealName
	}
	if name == "" {
		name = me.Username
	}
	return me.ID, name, nil
}

// Tickets rend les tickets du périmètre, du plus récemment modifié au plus
// ancien.
func (c *Client) Tickets(ctx context.Context, scope Scope) ([]Ticket, error) {
	if scope.TrackerID <= 0 {
		return nil, errors.New("tracker CSP non configuré (TULEAP_CSP_TRACKER_ID)")
	}
	limit := scope.Limit
	if limit <= 0 {
		limit = 100
	}

	var meID int
	if scope.AssignedToMe {
		id, _, err := c.Me(ctx)
		if err != nil {
			return nil, err
		}
		meID = id
	}

	var out []Ticket
	for offset := 0; offset < limit; offset += 50 {
		params := url.Values{
			"values": {"all"},
			"limit":  {"50"},
			"offset": {strconv.Itoa(offset)},
		}
		if scope.ExpertQuery != "" {
			params.Set("expert_query", scope.ExpertQuery)
		} else if scope.Query != "" {
			params.Set("query", scope.Query)
		}
		var raw []artifact
		total, err := c.getPaged(ctx, fmt.Sprintf("/api/trackers/%d/artifacts", scope.TrackerID), params, &raw)
		if err != nil {
			return nil, err
		}
		for _, a := range raw {
			t := c.flatten(a)
			if scope.AssignedToMe && !a.assignedTo(meID) {
				continue
			}
			out = append(out, t)
		}
		if len(raw) < 50 || (total > 0 && offset+50 >= total) {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modifie.After(out[j].Modifie) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Ticket rend un artefact et ses commentaires.
func (c *Client) Ticket(ctx context.Context, id int) (Ticket, []Commentaire, error) {
	var a artifact
	if err := c.get(ctx, fmt.Sprintf("/api/artifacts/%d", id), url.Values{"values_format": {"collection"}}, &a); err != nil {
		return Ticket{}, nil, err
	}
	t := c.flatten(a)

	var changesets []struct {
		SubmittedBy struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
			Username    string `json:"username"`
		} `json:"submitted_by_details"`
		SubmittedOn string `json:"submitted_on"`
		LastComment struct {
			Body   string `json:"body"`
			Format string `json:"format"`
		} `json:"last_comment"`
	}
	// L'historique est facultatif : un droit manquant ne doit pas priver de la
	// fiche elle-même.
	_ = c.get(ctx, fmt.Sprintf("/api/artifacts/%d/changesets", id), url.Values{
		"fields": {"comments"}, "limit": {"50"}, "order": {"desc"},
	}, &changesets)
	var comments []Commentaire
	for _, cs := range changesets {
		body := strings.TrimSpace(cs.LastComment.Body)
		if body == "" {
			continue
		}
		if cs.LastComment.Format == "html" {
			body = stripHTML(body)
		}
		who := cs.SubmittedBy.DisplayName
		if who == "" {
			who = cs.SubmittedBy.RealName
		}
		if who == "" {
			who = cs.SubmittedBy.Username
		}
		comments = append(comments, Commentaire{Auteur: who, Quand: parseDate(cs.SubmittedOn), Texte: body})
	}
	return t, comments, nil
}

// --- format Tuleap ------------------------------------------------------------

type artifact struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	XRef        string `json:"xref"`
	HTMLURL     string `json:"html_url"`
	SubmittedOn string `json:"submitted_on"`
	LastUpdate  string `json:"last_modified_date"`
	SubmittedBy struct {
		ID          int    `json:"id"`
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
		Username    string `json:"username"`
	} `json:"submitted_by_details"`
	Assignees []struct {
		ID          int    `json:"id"`
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
		Username    string `json:"username"`
	} `json:"assignees"`
	Tracker struct {
		Label string `json:"label"`
	} `json:"tracker"`
	Values []fieldValue `json:"values"`
}

// fieldValue est une valeur de champ telle que Tuleap la sérialise. Le format
// varie avec le type de champ : une chaîne, un nombre, une liste de libellés,
// une liste d'utilisateurs, une date… On lit tout en souplesse.
type fieldValue struct {
	FieldID int             `json:"field_id"`
	Label   string          `json:"label"`
	Type    string          `json:"type"`
	Value   json.RawMessage `json:"value"`
	Values  json.RawMessage `json:"values"`
	Format  string          `json:"format"`
}

func (a artifact) assignedTo(userID int) bool {
	for _, u := range a.Assignees {
		if u.ID == userID {
			return true
		}
	}
	return false
}

func (c *Client) flatten(a artifact) Ticket {
	t := Ticket{
		ID:      a.ID,
		Ref:     a.XRef,
		Titre:   strings.TrimSpace(a.Title),
		Statut:  strings.TrimSpace(a.Status),
		Cree:    parseDate(a.SubmittedOn),
		Modifie: parseDate(a.LastUpdate),
		Tracker: a.Tracker.Label,
		Champs:  map[string]string{},
	}
	if a.HTMLURL != "" {
		if strings.HasPrefix(a.HTMLURL, "http") {
			t.URL = a.HTMLURL
		} else {
			t.URL = c.base + a.HTMLURL
		}
	} else {
		t.URL = fmt.Sprintf("%s/plugins/tracker/?aid=%d", c.base, a.ID)
	}
	t.Auteur = personName(a.SubmittedBy.DisplayName, a.SubmittedBy.RealName, a.SubmittedBy.Username)
	names := make([]string, 0, len(a.Assignees))
	for _, u := range a.Assignees {
		if n := personName(u.DisplayName, u.RealName, u.Username); n != "" {
			names = append(names, n)
		}
	}
	t.Responsable = strings.Join(names, ", ")

	for _, v := range a.Values {
		label := strings.TrimSpace(v.Label)
		text := renderValue(v)
		key := strings.ToLower(label)
		switch {
		case text == "":
			continue
		case v.Type == "aid" || v.Type == "atid" || v.Type == "luby" || v.Type == "lud" || v.Type == "subby" || v.Type == "subon" || v.Type == "cross" || v.Type == "burndown":
			// Champs techniques déjà portés par les attributs de l'artefact.
			continue
		case t.Titre == "" && (v.Type == "string" && (strings.Contains(key, "title") || strings.Contains(key, "titre") || strings.Contains(key, "summary"))):
			t.Titre = text
		case t.Statut == "" && strings.Contains(key, "status"), t.Statut == "" && strings.Contains(key, "statut"):
			t.Statut = text
		case t.Priorite == "" && (strings.Contains(key, "priorit") || strings.Contains(key, "severity") || strings.Contains(key, "sévérité") || strings.Contains(key, "criticit")):
			t.Priorite = text
		case t.Description == "" && v.Type == "text" && (strings.Contains(key, "description") || strings.Contains(key, "détail") || strings.Contains(key, "detail")):
			t.Description = text
		case t.Responsable == "" && (strings.Contains(key, "assign") || strings.Contains(key, "responsable") || strings.Contains(key, "owner")):
			t.Responsable = text
		default:
			t.Champs[label] = text
		}
	}
	if t.Ref == "" {
		t.Ref = fmt.Sprintf("#%d", a.ID)
	}
	t.Ferme = closedStatus(t.Statut)
	return t
}

// renderValue met une valeur de champ en texte, quel que soit son type.
func renderValue(v fieldValue) string {
	if len(v.Values) > 0 && string(v.Values) != "null" {
		var items []json.RawMessage
		if err := json.Unmarshal(v.Values, &items); err == nil {
			parts := make([]string, 0, len(items))
			for _, it := range items {
				if s := renderScalar(it); s != "" {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, ", ")
		}
	}
	if len(v.Value) > 0 && string(v.Value) != "null" {
		s := renderScalar(v.Value)
		if v.Format == "html" {
			s = stripHTML(s)
		}
		return s
	}
	return ""
}

func renderScalar(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return "oui"
		}
		return "non"
	}
	var obj struct {
		Label       string `json:"label"`
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
		Username    string `json:"username"`
		Name        string `json:"name"`
		Filename    string `json:"filename"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, cand := range []string{obj.Label, obj.DisplayName, obj.RealName, obj.Username, obj.Name, obj.Filename} {
			if strings.TrimSpace(cand) != "" {
				return strings.TrimSpace(cand)
			}
		}
	}
	return ""
}

func personName(candidates ...string) string {
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			return strings.TrimSpace(c)
		}
	}
	return ""
}

// closedStatus reconnaît les statuts terminaux usuels d'un tracker de tickets.
func closedStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	for _, w := range []string{"closed", "clos", "done", "termin", "résolu", "resolu", "resolved", "fermé", "ferme", "rejected", "rejet", "cancel", "annul"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// stripHTML dégrade un texte HTML en texte brut, sans prétendre le rendre : de
// quoi lire un commentaire, pas de quoi l'afficher.
func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			b.WriteRune(' ')
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#39;", "'").Replace(b.String())
	return strings.Join(strings.Fields(out), " ")
}

// --- transport ----------------------------------------------------------------

// APIError garde le statut HTTP : l'appelant distingue une clé refusée (401)
// d'un tracker introuvable (404) d'une panne.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "clé d'accès Tuleap refusée : " + e.Message
	case http.StatusNotFound:
		return "introuvable sur Tuleap : " + e.Message
	}
	return fmt.Sprintf("Tuleap a répondu %d : %s", e.Status, e.Message)
}

func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	_, err := c.getPaged(ctx, path, params, out)
	return err
}

// getPaged fait un GET et rend, en plus, le total annoncé par l'en-tête
// X-PAGINATION-SIZE quand la ressource est une collection.
func (c *Client) getPaged(ctx context.Context, path string, params url.Values, out any) (int, error) {
	if c.base == "" {
		return 0, errors.New("adresse Tuleap non configurée (TULEAP_BASE_URL)")
	}
	if c.key == "" {
		return 0, errors.New("aucune clé d'accès Tuleap")
	}
	endpoint := c.base + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Auth-AccessKey", c.key)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("tuleap : %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("tuleap : lecture de la réponse : %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, &APIError{Status: resp.StatusCode, Message: apiMessage(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return 0, fmt.Errorf("tuleap : réponse illisible : %w", err)
	}
	total, _ := strconv.Atoi(resp.Header.Get("X-PAGINATION-SIZE"))
	return total, nil
}

func apiMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "sans détail"
	}
	return s
}
