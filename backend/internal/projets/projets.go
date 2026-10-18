// Package projets surveille ses projets perso : ce qui tourne sur le VPS, ce
// qui répond, ce qui est déployé, ce que dit la CI, comment vont les bases.
//
// Il croise trois regards. La sonde voit l'intérieur de la machine — système,
// conteneurs, journaux, bases. Les appels HTTP voient ce que voit un
// utilisateur — le site répond-il, en combien de temps, avec quel certificat.
// GitHub voit d'où ça vient — le dernier commit, la dernière CI. Aucun des
// trois ne suffit seul : « le site est lent » peut venir du disque, du code
// déployé hier ou d'une base à l'autre bout du monde.
package projets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/registre"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
)

// Service rassemble le registre, la sonde et GitHub.
type Service struct {
	Registre *registre.Registre
	Sonde    *sonde.Client
	GitHub   *GitHub

	mu      sync.Mutex
	etat    *Etat
	etatAt  time.Time
	pending chan struct{}
}

// Durée pendant laquelle un point reste valable. Courte : on ouvre l'écran
// pour savoir ce qui se passe maintenant.
const etatTTL = 45 * time.Second

// New rend nil quand aucun registre n'est configuré : pas de projets suivis,
// pas d'espace perso technique.
func New(reg *registre.Registre, probe *sonde.Client, gh *GitHub) *Service {
	if reg == nil {
		return nil
	}
	return &Service{Registre: reg, Sonde: probe, GitHub: gh}
}

// Capacites dit ce que le serveur sait réellement interroger.
type Capacites struct {
	Sonde  bool `json:"sonde"`
	GitHub bool `json:"github"`
	// GitHubAuthentifie : un jeton est posé. Sans lui, les dépôts privés et
	// la recherche dans le code sont hors de portée.
	GitHubAuthentifie bool `json:"github_authentifie"`
}

func (s *Service) Capacites() Capacites {
	return Capacites{
		Sonde:             s.Sonde != nil,
		GitHub:            s.GitHub != nil && len(s.Registre.Depots()) > 0,
		GitHubAuthentifie: s.GitHub != nil && s.GitHub.Authentifie(),
	}
}

// --- Le point d'ensemble ----------------------------------------------------

// Etat : le point d'ensemble, tel que l'écran Projets l'affiche et que
// l'équipe le lit.
type Etat struct {
	GenereLe  string       `json:"genere_le"`
	VPS       *EtatVPS     `json:"vps,omitempty"`
	Projets   []EtatProjet `json:"projets"`
	Alertes   []Alerte     `json:"alertes"`
	Capacites Capacites    `json:"capacites"`
	// Erreurs : les regards qui n'ont rien rendu (sonde injoignable…).
	Erreurs []string `json:"erreurs,omitempty"`
}

// EtatVPS : la machine, en résumé.
type EtatVPS struct {
	Nom string `json:"nom"`
	sonde.Systeme
	Conteneurs []sonde.Conteneur `json:"conteneurs,omitempty"`
}

type EtatProjet struct {
	ID          string          `json:"id"`
	Nom         string          `json:"nom"`
	Description string          `json:"description,omitempty"`
	Etat        string          `json:"etat"` // ok, attention, panne, inconnu
	Composants  []EtatComposant `json:"composants"`
	Bases       []EtatBase      `json:"bases,omitempty"`
}

type EtatComposant struct {
	ID        string           `json:"id"`
	Nom       string           `json:"nom"`
	Type      string           `json:"type"`
	Env       string           `json:"env,omitempty"`
	Etat      string           `json:"etat"`
	HTTP      *VerifURL        `json:"http,omitempty"`
	Conteneur *sonde.Conteneur `json:"conteneur,omitempty"`
	Depot     *ResumeDepot     `json:"depot,omitempty"`
}

// ResumeDepot : d'où vient un composant, et si ce qui tourne est à jour.
type ResumeDepot struct {
	Depot         string      `json:"depot"`
	Branche       string      `json:"branche"`
	DernierCommit *Commit     `json:"dernier_commit,omitempty"`
	CI            []Execution `json:"ci,omitempty"`
	// Deploiement : « a_jour », « en_retard » ou « inconnu », avec ce qui le
	// fonde — on ne tranche pas sans dire sur quoi.
	Deploiement string `json:"deploiement"`
	Explication string `json:"explication,omitempty"`
	Erreur      string `json:"erreur,omitempty"`
}

type EtatBase struct {
	ID  string `json:"id"`
	Nom string `json:"nom"`
	sonde.Mongo
}

// Alerte : quelque chose qui mérite d'être dit sans qu'on le demande.
type Alerte struct {
	Niveau string `json:"niveau"` // critique, attention, info
	Texte  string `json:"texte"`
	Projet string `json:"projet,omitempty"`
}

// Etat rend le point d'ensemble, depuis le cache s'il est frais. Deux
// demandes simultanées n'en lancent qu'un : l'écran et l'ingénieur qui
// regardent en même temps ne doivent pas doubler les appels au VPS.
func (s *Service) Etat(ctx context.Context, refresh bool) (*Etat, error) {
	for {
		s.mu.Lock()
		if !refresh && s.etat != nil && time.Since(s.etatAt) < etatTTL {
			e := s.etat
			s.mu.Unlock()
			return e, nil
		}
		if s.pending != nil {
			wait := s.pending
			s.mu.Unlock()
			select {
			case <-wait:
				refresh = false
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.pending = make(chan struct{})
		s.mu.Unlock()
		break
	}

	// Le relevé ne s'arrête pas si l'appelant s'en va : il servira au suivant.
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
	e := s.build(bctx, refresh)
	cancel()

	s.mu.Lock()
	s.etat, s.etatAt = e, time.Now()
	close(s.pending)
	s.pending = nil
	s.mu.Unlock()
	return e, nil
}

func (s *Service) build(ctx context.Context, refresh bool) *Etat {
	reg := s.Registre
	e := &Etat{GenereLe: time.Now().UTC().Format(time.RFC3339), Capacites: s.Capacites()}

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		system     *sonde.Systeme
		containers map[string]sonde.Conteneur
		all        []sonde.Conteneur
		checks     = map[string]VerifURL{}
		repos      = map[string]EtatDepot{}
		repoErrs   = map[string]string{}
		bases      = map[string]sonde.Mongo{}
	)
	fail := func(msg string) {
		mu.Lock()
		e.Erreurs = append(e.Erreurs, msg)
		mu.Unlock()
	}

	if s.Sonde != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			sys, err := s.Sonde.Systeme(ctx)
			if err != nil {
				fail("système du VPS : " + err.Error())
				return
			}
			mu.Lock()
			system = &sys
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			list, err := s.Sonde.Conteneurs(ctx)
			if err != nil {
				fail("conteneurs : " + err.Error())
				return
			}
			byName := map[string]sonde.Conteneur{}
			for _, c := range list {
				byName[c.Nom] = c
			}
			mu.Lock()
			containers, all = byName, list
			mu.Unlock()
		}()
		for _, b := range reg.Bases {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				m, err := s.Sonde.Mongo(ctx, id)
				if err != nil {
					m = sonde.Mongo{Base: id, Etat: "erreur", Erreur: err.Error()}
				}
				mu.Lock()
				bases[id] = m
				mu.Unlock()
			}(b.ID)
		}
	}

	for _, p := range reg.Projets {
		for _, c := range p.Composants {
			if c.URL != "" {
				wg.Add(1)
				go func(u string) {
					defer wg.Done()
					v := Verifier(ctx, u)
					mu.Lock()
					checks[u] = v
					mu.Unlock()
				}(c.URL)
			}
		}
	}

	if s.GitHub != nil {
		branches := map[string]string{}
		for _, p := range reg.Projets {
			for _, c := range p.Composants {
				if c.Depot != "" {
					branches[c.Depot] = c.Branche
				}
			}
		}
		for repo, branch := range branches {
			wg.Add(1)
			go func(repo, branch string) {
				defer wg.Done()
				d, err := s.GitHub.Depot(ctx, repo, branch, refresh)
				mu.Lock()
				if err != nil {
					repoErrs[repo] = err.Error()
				} else {
					repos[repo] = d
				}
				mu.Unlock()
			}(repo, branch)
		}
	}
	wg.Wait()

	if system != nil {
		e.VPS = &EtatVPS{Nom: reg.VPS.Nom, Systeme: *system, Conteneurs: all}
		if e.VPS.Nom == "" {
			e.VPS.Nom = system.Hote
		}
	}

	for _, p := range reg.Projets {
		ep := EtatProjet{ID: p.ID, Nom: p.Nom, Description: p.Description}
		for _, c := range p.Composants {
			ec := EtatComposant{ID: c.ID, Nom: c.Nom, Type: c.Type, Env: c.Env}
			if c.URL != "" {
				v := checks[c.URL]
				ec.HTTP = &v
			}
			if c.Conteneur != "" && containers != nil {
				if ct, ok := containers[c.Conteneur]; ok {
					ec.Conteneur = &ct
				} else {
					ec.Conteneur = &sonde.Conteneur{Nom: c.Conteneur, Etat: "absent"}
				}
			}
			if c.Depot != "" && s.GitHub != nil {
				ec.Depot = summarizeRepo(c, repos, repoErrs, ec.Conteneur)
			}
			ec.Etat = componentState(ec)
			ep.Composants = append(ep.Composants, ec)
		}
		for _, id := range p.Bases {
			b, _ := reg.Base(id)
			eb := EtatBase{ID: id, Nom: b.Nom}
			if m, ok := bases[id]; ok {
				eb.Mongo = m
			} else {
				eb.Mongo = sonde.Mongo{Base: id, Etat: "inconnu"}
			}
			// Le détail des collections est pour l'ingénieur, pas pour le
			// point d'ensemble.
			eb.Collections = nil
			ep.Bases = append(ep.Bases, eb)
		}
		ep.Etat = projectState(ep)
		e.Projets = append(e.Projets, ep)
	}
	e.Alertes = alerts(e)
	sort.Strings(e.Erreurs)
	return e
}

// summarizeRepo rapproche la CI de ce qui tourne.
//
// Le verdict le plus sûr vient de l'étiquette de révision de l'image : elle
// porte le commit exact dont elle a été construite. Sans elle, on compare les
// dates — le conteneur a-t-il redémarré après le dernier build réussi ? — et
// on le dit, parce que c'est une présomption, pas une preuve.
func summarizeRepo(c registre.Composant, repos map[string]EtatDepot, errs map[string]string, ct *sonde.Conteneur) *ResumeDepot {
	r := &ResumeDepot{Depot: c.Depot, Branche: c.Branche, Deploiement: "inconnu"}
	d, ok := repos[c.Depot]
	if !ok {
		r.Erreur = errs[c.Depot]
		return r
	}
	r.Branche = d.Branche
	if len(d.Commits) > 0 {
		head := d.Commits[0]
		head.SHA = short(head.SHA)
		r.DernierCommit = &head
	}
	r.CI = d.DernieresExecutions()
	if c.Type == "app" || ct == nil || ct.Etat == "absent" {
		return r
	}

	if ct.Revision != "" && len(d.Commits) > 0 {
		for i, commit := range d.Commits {
			if strings.HasPrefix(commit.SHA, ct.Revision) || strings.HasPrefix(ct.Revision, commit.SHA) {
				if i == 0 {
					r.Deploiement = "a_jour"
					r.Explication = "l'image en service a été construite depuis le dernier commit (" + short(commit.SHA) + ")"
				} else {
					r.Deploiement = "en_retard"
					r.Explication = fmt.Sprintf("l'image en service date du commit %s, %d commit(s) derrière la branche", short(commit.SHA), i)
				}
				return r
			}
		}
		r.Deploiement = "en_retard"
		r.Explication = "l'image en service vient du commit " + short(ct.Revision) + ", absent des derniers commits de la branche"
		return r
	}

	// Pas d'étiquette : on compare les dates.
	started, err := time.Parse(time.RFC3339, ct.DemarreLe)
	if err != nil || len(d.Commits) == 0 {
		return r
	}
	headDate, err := time.Parse(time.RFC3339, d.Commits[0].Date)
	if err != nil {
		return r
	}
	if !started.Before(headDate) {
		r.Deploiement = "a_jour"
		r.Explication = "le conteneur a redémarré après le dernier commit (présomption : l'image ne dit pas de quel commit elle vient)"
		return r
	}
	r.Deploiement = "en_retard"
	r.Explication = "le dernier commit est postérieur au dernier démarrage du conteneur (présomption : l'image ne dit pas de quel commit elle vient)"
	return r
}

func componentState(c EtatComposant) string {
	switch {
	case c.Conteneur != nil && (c.Conteneur.Etat == "absent" || (c.Conteneur.Etat != "running" && c.Conteneur.Etat != "")):
		return "panne"
	case c.HTTP != nil && !c.HTTP.Repond():
		return "panne"
	case c.HTTP != nil && c.HTTP.TotalMs > 2500:
		return "attention"
	case c.Conteneur != nil && c.Conteneur.Sante == "unhealthy":
		return "attention"
	case c.Depot != nil && c.Depot.Deploiement == "en_retard":
		return "attention"
	case c.HTTP == nil && c.Conteneur == nil:
		return "inconnu"
	}
	return "ok"
}

func projectState(p EtatProjet) string {
	worst := "inconnu"
	rank := map[string]int{"inconnu": 0, "ok": 1, "attention": 2, "panne": 3}
	for _, c := range p.Composants {
		if rank[c.Etat] > rank[worst] {
			worst = c.Etat
		}
	}
	for _, b := range p.Bases {
		state := "ok"
		if b.Etat == "erreur" {
			state = "panne"
		} else if b.Etat != "ok" {
			continue
		}
		if rank[state] > rank[worst] {
			worst = state
		}
	}
	return worst
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// --- Les alertes ------------------------------------------------------------

func alerts(e *Etat) []Alerte {
	var out []Alerte
	add := func(level, project, format string, args ...any) {
		out = append(out, Alerte{Niveau: level, Projet: project, Texte: fmt.Sprintf(format, args...)})
	}

	if v := e.VPS; v != nil {
		if v.HorsSupport {
			add("critique", "", "%s n'est plus maintenu depuis le %s : plus aucun correctif de sécurité. Il faut passer à une version supportée.", v.OS, frenchDate(v.FinSupport))
		} else if v.FinSupport != "" {
			if end, err := time.Parse("2006-01-02", v.FinSupport); err == nil && time.Until(end) < 60*24*time.Hour {
				add("attention", "", "%s arrive en fin de support le %s.", v.OS, frenchDate(v.FinSupport))
			}
		}
		for _, d := range v.Disques {
			switch {
			case d.Pct >= 90:
				add("critique", "", "Disque %s plein à %d %% (%.1f Go libres).", d.Point, d.Pct, d.LibreGo)
			case d.Pct >= 80:
				add("attention", "", "Disque %s rempli à %d %% (%.1f Go libres).", d.Point, d.Pct, d.LibreGo)
			}
		}
		if v.Memoire.TotalMo > 0 && v.Memoire.DisponibleMo*10 < v.Memoire.TotalMo {
			add("attention", "", "Mémoire presque épuisée : %d Mo disponibles sur %d.", v.Memoire.DisponibleMo, v.Memoire.TotalMo)
		}
		if v.RedemarrageRequis {
			add("attention", "", "Le VPS attend un redémarrage pour finir d'appliquer des mises à jour.")
		}
		if v.NoyauInstalle != "" && v.Noyau != "" && v.NoyauInstalle != v.Noyau {
			add("attention", "", "Un noyau plus récent est installé (%s) mais le VPS tourne encore sur %s : il faut redémarrer.", v.NoyauInstalle, v.Noyau)
		}
		if v.MisesAJourSecurite > 0 {
			add("attention", "", "%d mise(s) à jour de sécurité en attente sur le VPS.", v.MisesAJourSecurite)
		} else if v.MisesAJour > 0 {
			add("info", "", "%d paquet(s) à mettre à jour sur le VPS.", v.MisesAJour)
		}
		if t, err := time.Parse(time.RFC3339, v.IndexAPT); err == nil && time.Since(t) > 7*24*time.Hour {
			add("info", "", "La liste des paquets n'a pas été rafraîchie depuis le %s : le compte des mises à jour est peut-être faux.", t.Format("02/01"))
		}
		var public []string
		for _, c := range v.Conteneurs {
			for _, p := range c.Ports {
				if p.Public && p.IP != "::" {
					public = append(public, fmt.Sprintf("%d (%s)", p.Hote, c.Nom))
				}
			}
		}
		if len(public) > 0 {
			add("attention", "", "Ports de conteneurs ouverts sur Internet, nginx contourné : %s. À publier sur 127.0.0.1.", strings.Join(public, ", "))
		}
		for _, c := range v.Conteneurs {
			if c.Redemarrages >= 3 {
				add("attention", c.Projet, "Le conteneur %s a redémarré %d fois : il plante en boucle ?", c.Nom, c.Redemarrages)
			}
		}
	}

	certs := map[string]bool{}
	for _, p := range e.Projets {
		for _, c := range p.Composants {
			label := p.Nom + " · " + c.Nom
			if c.Conteneur != nil && c.Conteneur.Etat == "absent" {
				add("critique", p.ID, "%s : le conteneur %s n'existe pas sur le VPS.", label, c.Conteneur.Nom)
			} else if c.Conteneur != nil && c.Conteneur.Etat != "" && c.Conteneur.Etat != "running" {
				add("critique", p.ID, "%s : le conteneur %s est %s.", label, c.Conteneur.Nom, c.Conteneur.Etat)
			}
			if h := c.HTTP; h != nil {
				switch {
				case h.Erreur != "":
					add("critique", p.ID, "%s ne répond pas : %s.", label, h.Erreur)
				case h.Statut >= 400:
					add("critique", p.ID, "%s répond en erreur %d.", label, h.Statut)
				case h.TotalMs > 2500:
					add("attention", p.ID, "%s répond lentement (%.1f s).", label, float64(h.TotalMs)/1000)
				}
				if host := hostOf(h.URL); h.CertificatExpire != "" && !certs[host] {
					certs[host] = true
					switch {
					case h.CertificatJours < 3:
						add("critique", p.ID, "Le certificat de %s expire dans %d jour(s).", host, h.CertificatJours)
					case h.CertificatJours < 14:
						add("attention", p.ID, "Le certificat de %s expire dans %d jours : le renouvellement automatique ne passe peut-être plus.", host, h.CertificatJours)
					}
				}
			}
			if d := c.Depot; d != nil {
				for _, run := range d.CI {
					if run.Statut == "completed" && run.Conclusion == "failure" {
						add("attention", p.ID, "%s : la dernière exécution de « %s » a échoué.", label, run.Nom)
					}
				}
				if d.Deploiement == "en_retard" {
					add("attention", p.ID, "%s : ce qui tourne n'est pas le dernier commit — %s.", label, d.Explication)
				}
			}
		}
		for _, b := range p.Bases {
			if b.Etat == "erreur" {
				add("critique", p.ID, "%s : la base %s est injoignable (%s).", p.Nom, b.Nom, b.Erreur)
			}
		}
	}

	rank := map[string]int{"critique": 0, "attention": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Niveau] < rank[out[j].Niveau] })
	if out == nil {
		out = []Alerte{}
	}
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}

var months = []string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}

func frenchDate(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return fmt.Sprintf("%d %s %d", t.Day(), months[t.Month()-1], t.Year())
}

// --- Pour Raoul ---------------------------------------------------------------

// Brief rend le point d'ensemble sous la forme que Raoul lit : les états et
// les alertes, sans les détails techniques qu'il ne prononcera pas.
func (s *Service) Brief(ctx context.Context) (string, error) {
	e, err := s.Etat(ctx, false)
	if err != nil {
		return "", err
	}
	type composant struct {
		Nom         string `json:"nom"`
		Etat        string `json:"etat"`
		Reponse     string `json:"reponse,omitempty"`
		Deploiement string `json:"deploiement,omitempty"`
		CI          string `json:"ci,omitempty"`
	}
	type projet struct {
		Nom        string      `json:"nom"`
		Etat       string      `json:"etat"`
		Composants []composant `json:"composants"`
		Bases      []string    `json:"bases,omitempty"`
	}
	brief := struct {
		VPS     map[string]any `json:"vps,omitempty"`
		Projets []projet       `json:"projets"`
		Alertes []Alerte       `json:"alertes"`
		Erreurs []string       `json:"releves_impossibles,omitempty"`
	}{Alertes: e.Alertes, Erreurs: e.Erreurs}
	if v := e.VPS; v != nil {
		vps := map[string]any{"systeme": v.OS, "hors_support": v.HorsSupport, "mises_a_jour": v.MisesAJour}
		if len(v.Disques) > 0 {
			vps["disque_utilise_pct"] = v.Disques[0].Pct
			vps["disque_libre_go"] = v.Disques[0].LibreGo
		}
		if v.Memoire.TotalMo > 0 {
			vps["memoire_disponible_pct"] = v.Memoire.DisponibleMo * 100 / v.Memoire.TotalMo
		}
		brief.VPS = vps
	}
	for _, p := range e.Projets {
		bp := projet{Nom: p.Nom, Etat: p.Etat}
		for _, c := range p.Composants {
			bc := composant{Nom: c.Nom, Etat: c.Etat}
			if c.HTTP != nil {
				if c.HTTP.Repond() {
					bc.Reponse = fmt.Sprintf("répond en %d ms", c.HTTP.TotalMs)
				} else if c.HTTP.Erreur != "" {
					bc.Reponse = c.HTTP.Erreur
				} else {
					bc.Reponse = fmt.Sprintf("statut %d", c.HTTP.Statut)
				}
			}
			if c.Depot != nil {
				bc.Deploiement = c.Depot.Deploiement
				for _, run := range c.Depot.CI {
					if run.Conclusion != "" {
						bc.CI += run.Nom + " : " + run.Conclusion + " ; "
					}
				}
				bc.CI = strings.TrimSuffix(bc.CI, " ; ")
			}
			bp.Composants = append(bp.Composants, bc)
		}
		for _, b := range p.Bases {
			bp.Bases = append(bp.Bases, b.Nom+" : "+b.Etat)
		}
		brief.Projets = append(brief.Projets, bp)
	}
	raw, err := json.Marshal(brief)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ErrSansSonde : l'outil demandé passe par la sonde, qui n'est pas configurée.
var ErrSansSonde = errors.New("la sonde du VPS n'est pas configurée sur ce serveur")
