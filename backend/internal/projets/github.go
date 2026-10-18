package projets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// GitHub, en lecture seule : les derniers commits, les exécutions de CI, et le
// code. Un jeton par propriétaire, parce qu'un jeton fin de GitHub ne vaut que
// pour un propriétaire — ses dépôts à lui d'un côté, ceux de l'organisation
// VisionShowDev de l'autre. Sans jeton, les dépôts publics restent lisibles ;
// les privés non, et l'outil le dit au lieu de prétendre qu'ils sont vides.

const githubAPI = "https://api.github.com"

type GitHub struct {
	tokens map[string]string // propriétaire en minuscules → jeton ; "" : défaut
	http   *http.Client
	base   string

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at   time.Time
	data []byte
}

// NewGitHub prend le jeton par défaut et les jetons par propriétaire.
func NewGitHub(defaultToken string, byOwner map[string]string) *GitHub {
	tokens := map[string]string{}
	if defaultToken != "" {
		tokens[""] = defaultToken
	}
	for owner, tok := range byOwner {
		if tok != "" {
			tokens[ownerKey(owner)] = tok
		}
	}
	return &GitHub{tokens: tokens, http: &http.Client{Timeout: 15 * time.Second}, base: githubAPI, cache: map[string]cached{}}
}

// Authentifie dit si au moins un jeton est configuré.
func (g *GitHub) Authentifie() bool { return len(g.tokens) > 0 }

func (g *GitHub) token(repo string) string {
	owner, _, _ := strings.Cut(repo, "/")
	if t, ok := g.tokens[ownerKey(owner)]; ok {
		return t
	}
	return g.tokens[""]
}

// ownerKey ramène un propriétaire à la forme d'un nom de variable : un nom
// d'organisation avec un tiret (« my-org ») se règle par GITHUB_TOKEN_MY_ORG.
func ownerKey(owner string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, owner)
}

// get lit une ressource de l'API, avec un cache court : le point sur les
// projets interroge les mêmes dépôts à chaque ouverture de l'écran.
func (g *GitHub) get(ctx context.Context, repo, p string, query url.Values, ttl time.Duration, accept string, into any) error {
	u := g.base + p
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	key := accept + " " + u
	if ttl > 0 {
		g.mu.Lock()
		if c, ok := g.cache[key]; ok && time.Since(c.at) < ttl {
			g.mu.Unlock()
			return json.Unmarshal(c.data, into)
		}
		g.mu.Unlock()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cerveau")
	if tok := g.token(repo); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub injoignable : %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		if g.token(repo) == "" {
			return fmt.Errorf("%s introuvable : dépôt privé et aucun jeton GitHub pour %s", repo, ownerOf(repo))
		}
		return fmt.Errorf("%s : introuvable (ou le jeton n'y a pas accès)", path.Clean(p))
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("jeton GitHub refusé (expiré ou révoqué)")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return errors.New("quota de l'API GitHub épuisé pour l'heure")
		}
		return fmt.Errorf("GitHub refuse l'accès (%d)", resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("GitHub : statut %d", resp.StatusCode)
	}
	if ttl > 0 {
		g.mu.Lock()
		g.cache[key] = cached{at: time.Now(), data: body}
		g.mu.Unlock()
	}
	return json.Unmarshal(body, into)
}

func ownerOf(repo string) string {
	owner, _, _ := strings.Cut(repo, "/")
	return owner
}

// Commit : un commit, réduit à ce qui se lit.
type Commit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Auteur  string `json:"auteur,omitempty"`
	Date    string `json:"date"`
}

// Execution : une exécution de workflow GitHub Actions.
type Execution struct {
	Nom        string `json:"nom"`
	Statut     string `json:"statut"`               // queued, in_progress, completed
	Conclusion string `json:"conclusion,omitempty"` // success, failure, cancelled…
	Branche    string `json:"branche,omitempty"`
	SHA        string `json:"sha"`
	Evenement  string `json:"evenement,omitempty"`
	Date       string `json:"date"`
	URL        string `json:"lien,omitempty"`
}

// EtatDepot : où en est un dépôt.
type EtatDepot struct {
	Depot      string      `json:"depot"`
	Prive      bool        `json:"prive,omitempty"`
	Branche    string      `json:"branche"`
	PousseLe   string      `json:"pousse_le,omitempty"`
	Commits    []Commit    `json:"commits,omitempty"`
	Executions []Execution `json:"executions,omitempty"`
	// Avertissements : ce qui n'a pas pu être lu (les Actions, par exemple,
	// quand le jeton n'a pas ce droit).
	Avertissements []string `json:"avertissements,omitempty"`
}

// Depot relève un dépôt : sa branche de production, ses derniers commits et
// ses dernières exécutions de CI.
func (g *GitHub) Depot(ctx context.Context, repo, branch string, refresh bool) (EtatDepot, error) {
	ttl := 2 * time.Minute
	if refresh {
		ttl = 0
	}
	out := EtatDepot{Depot: repo}
	var meta struct {
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
		PushedAt      string `json:"pushed_at"`
	}
	if err := g.get(ctx, repo, "/repos/"+repo, nil, ttl, "", &meta); err != nil {
		return out, err
	}
	out.Prive = meta.Private
	out.PousseLe = meta.PushedAt
	if branch == "" {
		branch = meta.DefaultBranch
	}
	out.Branche = branch

	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := g.get(ctx, repo, "/repos/"+repo+"/commits", url.Values{"sha": {branch}, "per_page": {"15"}}, ttl, "", &commits); err != nil {
		out.Avertissements = append(out.Avertissements, "commits illisibles : "+err.Error())
	}
	for _, c := range commits {
		out.Commits = append(out.Commits, Commit{
			SHA: c.SHA, Message: firstLine(c.Commit.Message), Auteur: c.Commit.Author.Name, Date: c.Commit.Author.Date,
		})
	}

	var runs struct {
		Runs []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
			Event      string `json:"event"`
			CreatedAt  string `json:"created_at"`
			HTMLURL    string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := g.get(ctx, repo, "/repos/"+repo+"/actions/runs", url.Values{"branch": {branch}, "per_page": {"10"}}, ttl, "", &runs); err != nil {
		out.Avertissements = append(out.Avertissements, "exécutions de CI illisibles : "+err.Error())
	}
	for _, r := range runs.Runs {
		out.Executions = append(out.Executions, Execution{
			Nom: r.Name, Statut: r.Status, Conclusion: r.Conclusion, Branche: r.HeadBranch,
			SHA: r.HeadSHA, Evenement: r.Event, Date: r.CreatedAt, URL: r.HTMLURL,
		})
	}
	return out, nil
}

// Verifier dit, pour chaque dépôt, s'il est lisible avec les jetons posés.
// Le serveur s'en sert au démarrage : un jeton expiré ou limité aux mauvais
// dépôts se voit dans les journaux, pas seulement le jour où l'ingénieur en a
// besoin.
func (g *GitHub) Verifier(ctx context.Context, repos []string) (readable []string, failed map[string]string) {
	failed = map[string]string{}
	for _, repo := range repos {
		var meta struct {
			FullName string `json:"full_name"`
		}
		if err := g.get(ctx, repo, "/repos/"+repo, nil, 10*time.Minute, "", &meta); err != nil {
			failed[repo] = err.Error()
			continue
		}
		readable = append(readable, repo)
	}
	return readable, failed
}

// DernieresExecutions rend, pour chaque workflow, sa dernière exécution :
// « la CI » d'un dépôt, ce sont souvent plusieurs workflows (build, qualité),
// et l'échec de l'un ne dit rien de l'autre.
func (d EtatDepot) DernieresExecutions() []Execution {
	seen := map[string]bool{}
	var out []Execution
	for _, e := range d.Executions {
		if seen[e.Nom] {
			continue
		}
		seen[e.Nom] = true
		out = append(out, e)
	}
	return out
}

// --- Le code ------------------------------------------------------------------

// Entree : un fichier ou un dossier de l'arborescence.
type Entree struct {
	Chemin string `json:"chemin"`
	Type   string `json:"type"` // fichier, dossier
	Taille int    `json:"taille,omitempty"`
}

// Arbre liste ce que contient un dossier du dépôt, sur deux niveaux. Les
// dossiers de dépendances et de build sont écartés : node_modules n'apprend
// rien sur un projet.
func (g *GitHub) Arbre(ctx context.Context, repo, branch, dir string) ([]Entree, bool, error) {
	if branch == "" {
		b, err := g.defaultBranch(ctx, repo)
		if err != nil {
			return nil, false, err
		}
		branch = b
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int    `json:"size"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := g.get(ctx, repo, "/repos/"+repo+"/git/trees/"+url.PathEscape(branch), url.Values{"recursive": {"1"}}, 5*time.Minute, "", &tree); err != nil {
		return nil, false, err
	}
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	prefix := ""
	if dir != "" && dir != "." {
		prefix = dir + "/"
	}
	var out []Entree
	for _, e := range tree.Tree {
		if !strings.HasPrefix(e.Path, prefix) || e.Path == dir {
			continue
		}
		rest := strings.TrimPrefix(e.Path, prefix)
		if strings.Count(rest, "/") > 1 || ignoredPath(e.Path) {
			continue
		}
		kind := "fichier"
		if e.Type == "tree" {
			kind = "dossier"
		}
		out = append(out, Entree{Chemin: e.Path, Type: kind, Taille: e.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Chemin < out[j].Chemin })
	if len(out) > 400 {
		out = out[:400]
	}
	return out, tree.Truncated, nil
}

func ignoredPath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "node_modules", "node_modules.nosync", ".next", ".expo", "dist", "build", "coverage", "vendor", ".git", "Pods":
			return true
		}
	}
	return false
}

// Fichier : un extrait de fichier, lignes numérotées.
type Fichier struct {
	Depot   string `json:"depot"`
	Chemin  string `json:"chemin"`
	Lignes  int    `json:"lignes_total"`
	De      int    `json:"de"`
	A       int    `json:"a"`
	Contenu string `json:"contenu"`
}

// Lire rend les lignes [de, a] d'un fichier (toutes, jusqu'à 400, par défaut).
func (g *GitHub) Lire(ctx context.Context, repo, branch, file string, from, to int) (Fichier, error) {
	q := url.Values{}
	if branch != "" {
		q.Set("ref", branch)
	}
	var content struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	file = strings.Trim(strings.TrimSpace(file), "/")
	if file == "" || strings.Contains(file, "..") {
		return Fichier{}, errors.New("chemin de fichier invalide")
	}
	if err := g.get(ctx, repo, "/repos/"+repo+"/contents/"+escapePath(file), q, 2*time.Minute, "", &content); err != nil {
		return Fichier{}, err
	}
	if content.Type != "file" {
		return Fichier{}, fmt.Errorf("%s n'est pas un fichier", file)
	}
	if content.Encoding != "base64" {
		return Fichier{}, fmt.Errorf("%s est trop gros pour être lu par l'API (%d octets)", file, content.Size)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return Fichier{}, err
	}
	if isBinary(raw) {
		return Fichier{}, fmt.Errorf("%s est un fichier binaire", file)
	}
	lines := strings.Split(string(raw), "\n")
	if from < 1 {
		from = 1
	}
	if to <= 0 || to > len(lines) {
		to = len(lines)
	}
	if to-from > 399 {
		to = from + 399
	}
	if from > len(lines) {
		return Fichier{}, fmt.Errorf("%s n'a que %d lignes", file, len(lines))
	}
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return Fichier{Depot: repo, Chemin: file, Lignes: len(lines), De: from, A: to, Contenu: b.String()}, nil
}

// Trouve : un fichier où la recherche de code a trouvé les mots.
type Trouve struct {
	Chemin    string   `json:"chemin"`
	Fragments []string `json:"fragments,omitempty"`
}

// Chercher cherche des mots dans le code d'un dépôt. L'API de recherche exige
// un jeton, même pour un dépôt public.
func (g *GitHub) Chercher(ctx context.Context, repo, words string) ([]Trouve, error) {
	if g.token(repo) == "" {
		return nil, errors.New("la recherche dans le code demande un jeton GitHub ; à défaut, parcours l'arborescence et lis les fichiers")
	}
	words = strings.TrimSpace(words)
	if words == "" {
		return nil, errors.New("rien à chercher")
	}
	var res struct {
		Items []struct {
			Path        string `json:"path"`
			TextMatches []struct {
				Fragment string `json:"fragment"`
			} `json:"text_matches"`
		} `json:"items"`
	}
	q := url.Values{"q": {words + " repo:" + repo}, "per_page": {"20"}}
	if err := g.get(ctx, repo, "/search/code", q, time.Minute, "application/vnd.github.text-match+json", &res); err != nil {
		return nil, err
	}
	var out []Trouve
	for _, it := range res.Items {
		t := Trouve{Chemin: it.Path}
		for i, m := range it.TextMatches {
			if i >= 3 {
				break
			}
			t.Fragments = append(t.Fragments, strings.TrimSpace(m.Fragment))
		}
		out = append(out, t)
	}
	return out, nil
}

func (g *GitHub) defaultBranch(ctx context.Context, repo string) (string, error) {
	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.get(ctx, repo, "/repos/"+repo, nil, 10*time.Minute, "", &meta); err != nil {
		return "", err
	}
	return meta.DefaultBranch, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// isBinary : un octet nul dans les premiers kilo-octets, c'est une image ou un
// exécutable, pas du code à lire.
func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}
