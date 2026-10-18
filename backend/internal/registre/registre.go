// Package registre lit la description de ses projets perso : ce qui tourne,
// où, d'où ça vient, et où sont leurs bases.
//
// Le fichier vit sur le VPS, pas dans ce dépôt. Le dépôt est public, et une
// carte de l'infrastructure — conteneurs, ports, emplacement des fichiers de
// secrets — n'a rien à faire dans un dépôt public. Cerveau le lit monté en
// lecture seule dans son conteneur ; la sonde le lit à son emplacement sur
// l'hôte. Le même fichier sert aux deux, pour qu'ils ne divergent jamais.
//
// Il ne contient aucun secret : pour une base, il dit dans quel fichier .env
// et sous quelle variable se trouve l'URI, jamais l'URI elle-même.
package registre

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Registre est le contenu du fichier.
type Registre struct {
	VPS      VPS      `json:"vps"`
	Projets  []Projet `json:"projets"`
	Bases    []Base   `json:"bases,omitempty"`
	Domaines []string `json:"domaines,omitempty"`
}

type VPS struct {
	Nom string `json:"nom"`
}

// Projet : un de ses projets perso.
type Projet struct {
	ID          string      `json:"id"`
	Nom         string      `json:"nom"`
	Description string      `json:"description,omitempty"`
	Composants  []Composant `json:"composants"`
	// Bases : les identifiants des bases du projet (voir Registre.Bases).
	Bases []string `json:"bases,omitempty"`
}

// Composant : un morceau d'un projet — un back, un site, une app mobile.
type Composant struct {
	ID   string `json:"id"`
	Nom  string `json:"nom"`
	Type string `json:"type"` // back, front, app
	Env  string `json:"env,omitempty"`
	// URL : ce qu'on appelle pour savoir s'il répond. Pour un back, son point
	// de santé ; pour un site, sa page d'accueil. Vide pour une app mobile.
	URL string `json:"url,omitempty"`
	// Conteneur : son nom Docker sur le VPS, quand il y tourne.
	Conteneur string `json:"conteneur,omitempty"`
	// Depot : « propriétaire/nom » sur GitHub ; Branche : celle qui part en
	// production (défaut : la branche par défaut du dépôt).
	Depot   string `json:"depot,omitempty"`
	Branche string `json:"branche,omitempty"`
	// Base : l'identifiant de la base qu'il utilise.
	Base string `json:"base,omitempty"`
}

// Base : une base MongoDB, désignée par l'endroit où son URI est rangée.
type Base struct {
	ID  string `json:"id"`
	Nom string `json:"nom"`
	// FichierEnv et Variable : où la sonde trouve l'URI sur l'hôte.
	FichierEnv string `json:"fichier_env"`
	Variable   string `json:"variable"`
	// NomBase : la base à examiner dans le cluster. VariableBase : la
	// variable du même fichier qui la nomme (MONGO_DB…), quand le projet la
	// règle à part de l'URI. Aucune des deux : celle de l'URI.
	NomBase      string `json:"base,omitempty"`
	VariableBase string `json:"variable_base,omitempty"`
}

// Charger lit et valide le registre.
func Charger(path string) (*Registre, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registre
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("registre %s illisible : %w", path, err)
	}
	if err := r.valider(); err != nil {
		return nil, fmt.Errorf("registre %s : %w", path, err)
	}
	return &r, nil
}

func (r *Registre) valider() error {
	ids := map[string]bool{}
	bases := map[string]bool{}
	for _, b := range r.Bases {
		if b.ID == "" || b.FichierEnv == "" || b.Variable == "" {
			return fmt.Errorf("base %q incomplète (id, fichier_env et variable sont requis)", b.ID)
		}
		bases[b.ID] = true
	}
	for _, p := range r.Projets {
		if p.ID == "" || p.Nom == "" {
			return fmt.Errorf("projet sans id ou sans nom")
		}
		if ids[p.ID] {
			return fmt.Errorf("projet %q déclaré deux fois", p.ID)
		}
		ids[p.ID] = true
		for _, id := range p.Bases {
			if !bases[id] {
				return fmt.Errorf("projet %q : base %q inconnue", p.ID, id)
			}
		}
		for _, c := range p.Composants {
			if c.ID == "" {
				return fmt.Errorf("projet %q : composant sans id", p.ID)
			}
			if c.URL != "" {
				u, err := url.Parse(c.URL)
				if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
					return fmt.Errorf("projet %q, composant %q : URL invalide", p.ID, c.ID)
				}
			}
			if c.Base != "" && !bases[c.Base] {
				return fmt.Errorf("projet %q, composant %q : base %q inconnue", p.ID, c.ID, c.Base)
			}
		}
	}
	return nil
}

// Base rend la base d'identifiant id.
func (r *Registre) Base(id string) (Base, bool) {
	for _, b := range r.Bases {
		if b.ID == id {
			return b, true
		}
	}
	return Base{}, false
}

// Depots rend les dépôts GitHub déclarés, sans doublon, dans l'ordre.
func (r *Registre) Depots() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range r.Projets {
		for _, c := range p.Composants {
			if c.Depot != "" && !seen[strings.ToLower(c.Depot)] {
				seen[strings.ToLower(c.Depot)] = true
				out = append(out, c.Depot)
			}
		}
	}
	return out
}

// HoteAutorise dit si l'ingénieur peut aller appeler cet hôte : un domaine
// déclaré, un de ses sous-domaines, ou l'hôte d'une URL de composant. Rien
// d'autre — un outil qui appelle n'importe quelle adresse depuis le VPS, c'est
// une porte vers tout ce qui n'écoute qu'en local.
func (r *Registre) HoteAutorise(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	match := func(domain string) bool {
		domain = strings.ToLower(strings.TrimSpace(domain))
		return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
	}
	for _, d := range r.Domaines {
		if match(d) {
			return true
		}
	}
	for _, p := range r.Projets {
		for _, c := range p.Composants {
			if u, err := url.Parse(c.URL); err == nil && strings.EqualFold(u.Hostname(), host) {
				return true
			}
		}
	}
	return false
}
