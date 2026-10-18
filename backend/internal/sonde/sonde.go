// Package sonde est le regard de Cerveau sur le VPS.
//
// Cerveau tourne dans un conteneur : il ne voit ni le disque de la machine, ni
// les autres conteneurs, ni les mises à jour en attente, ni les bases des
// autres projets. Lui ouvrir tout ça directement — socket Docker monté, système
// de fichiers de l'hôte, fichiers .env des autres projets — reviendrait à
// donner les clés de toute la machine à un service exposé sur Internet.
//
// D'où une sonde : un petit programme installé sur l'hôte, qui ne sait faire
// que des lectures, chacune nommée et bornée. Cerveau l'appelle en SSH avec une
// clé dédiée, que le serveur SSH attache à la sonde et à rien d'autre
// (command= dans authorized_keys) : même volée, cette clé ne donne pas un
// shell, elle donne un relevé de disque. Les secrets des autres projets ne
// quittent jamais l'hôte — la sonde lit leur URI Mongo, se connecte, et ne
// rend que le diagnostic.
//
// Ce fichier porte ce que les deux côtés partagent : la forme des demandes,
// celle des réponses, et les relevés.
package sonde

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version de la sonde. Cerveau la lit à chaque réponse : une sonde plus vieille
// que le serveur qui l'interroge se signale au lieu de répondre à côté.
const Version = "1"

// Les commandes que la sonde connaît. Il n'y en a pas d'autre, et c'est le
// principe : une commande inconnue est refusée, pas interprétée.
const (
	CmdSysteme    = "systeme"
	CmdConteneurs = "conteneurs"
	CmdJournaux   = "journaux"
	CmdStats      = "stats"
	CmdMongo      = "mongo"
	CmdExpliquer  = "expliquer"
	CmdSites      = "sites"
)

// Requete est une demande de Cerveau à la sonde.
type Requete struct {
	Commande string            `json:"c"`
	Args     map[string]string `json:"a,omitempty"`
}

// Encode rend la demande sous la forme d'un seul mot, sans espace ni
// guillemet : elle traverse SSH comme une commande et n'a aucune chance d'être
// réinterprétée par un shell en chemin.
func (r Requete) Encode() (string, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode lit une demande telle que SSH la transmet : soit le mot encodé seul,
// soit précédé du nom du programme (« cerveau-sonde <mot> »), selon que la clé
// est attachée à la sonde par command= ou non.
func Decode(line string) (Requete, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return Requete{}, errors.New("demande vide")
	}
	word := fields[len(fields)-1]
	raw, err := base64.RawURLEncoding.DecodeString(word)
	if err != nil {
		return Requete{}, fmt.Errorf("demande illisible : %w", err)
	}
	var r Requete
	if err := json.Unmarshal(raw, &r); err != nil {
		return Requete{}, fmt.Errorf("demande illisible : %w", err)
	}
	return r, nil
}

// Reponse est l'enveloppe de toute réponse.
type Reponse struct {
	Version string          `json:"version"`
	Erreur  string          `json:"erreur,omitempty"`
	Donnees json.RawMessage `json:"donnees,omitempty"`
}

// Systeme : l'état de la machine.
type Systeme struct {
	Hote string `json:"hote"`
	// OS : le nom complet (« Ubuntu 25.04 »), Version et Nom de code
	// (« 25.04 », « plucky »).
	OS      string `json:"os"`
	Version string `json:"version"`
	NomCode string `json:"nom_de_code,omitempty"`
	// FinSupport : la fin du support standard de cette version, telle que la
	// machine elle-même la connaît (distro-info). Vide si inconnue.
	FinSupport  string `json:"fin_support,omitempty"`
	HorsSupport bool   `json:"hors_support,omitempty"`
	LTS         bool   `json:"lts,omitempty"`
	// Noyau : celui qui tourne. NoyauInstalle : le plus récent présent dans
	// /boot — s'ils diffèrent, un redémarrage est en retard.
	Noyau         string    `json:"noyau"`
	NoyauInstalle string    `json:"noyau_installe,omitempty"`
	AllumeDepuisS int64     `json:"allume_depuis_s"`
	Charge        []float64 `json:"charge"`
	Processeurs   int       `json:"processeurs"`
	Memoire       Memoire   `json:"memoire"`
	Disques       []Disque  `json:"disques"`
	// MisesAJour : paquets en attente (-1 si inconnu), dont de sécurité.
	MisesAJour         int      `json:"mises_a_jour"`
	MisesAJourSecurite int      `json:"mises_a_jour_securite"`
	Paquets            []string `json:"paquets,omitempty"`
	// IndexAPT : dernière mise à jour de la liste des paquets. Vieille de
	// plusieurs jours, elle rend le compte des mises à jour trompeur.
	IndexAPT          string   `json:"index_apt,omitempty"`
	RedemarrageRequis bool     `json:"redemarrage_requis,omitempty"`
	RaisonRedemarrage []string `json:"raison_redemarrage,omitempty"`
	Docker            string   `json:"docker,omitempty"`
	// Avertissements : ce qui n'a pas pu être relevé, dit plutôt que tu.
	Avertissements []string `json:"avertissements,omitempty"`
}

type Memoire struct {
	TotalMo      int64 `json:"total_mo"`
	DisponibleMo int64 `json:"disponible_mo"`
	SwapMo       int64 `json:"swap_mo"`
	SwapLibreMo  int64 `json:"swap_libre_mo"`
}

type Disque struct {
	Point   string  `json:"point"`
	TotalGo float64 `json:"total_go"`
	LibreGo float64 `json:"libre_go"`
	Pct     int     `json:"utilise_pct"`
}

// Conteneur : un conteneur Docker de la machine.
type Conteneur struct {
	Nom    string `json:"nom"`
	Image  string `json:"image"`
	Etat   string `json:"etat"`   // running, exited, restarting…
	Statut string `json:"statut"` // « Up 3 hours », tel que Docker le dit
	Sante  string `json:"sante,omitempty"`
	// DemarreLe : le dernier démarrage — pour un service déployé par image,
	// c'est en pratique la date du dernier déploiement.
	DemarreLe    string `json:"demarre_le,omitempty"`
	Redemarrages int    `json:"redemarrages,omitempty"`
	Ports        []Port `json:"ports,omitempty"`
	// Revision : le commit dont l'image a été construite (étiquette OCI posée
	// par la CI). C'est ce qui permet de dire si le dernier push est déployé.
	Revision    string `json:"revision,omitempty"`
	VersionOCI  string `json:"version,omitempty"`
	ImageCreeLe string `json:"image_cree_le,omitempty"`
	Projet      string `json:"projet_compose,omitempty"`
}

type Port struct {
	IP    string `json:"ip,omitempty"`
	Hote  int    `json:"hote,omitempty"`
	Prive int    `json:"prive"`
	// Public : publié sur toutes les interfaces. Docker écrit ses propres
	// règles de pare-feu : un tel port est joignable depuis Internet, avec ou
	// sans ufw.
	Public bool `json:"public,omitempty"`
}

// Journaux : la fin des journaux d'un conteneur.
type Journaux struct {
	Conteneur string   `json:"conteneur"`
	Lignes    []string `json:"lignes"`
	Filtre    string   `json:"filtre,omitempty"`
	// Parcourues : lignes lues avant filtrage.
	Parcourues int `json:"parcourues"`
}

// Stat : la consommation d'un conteneur en marche.
type Stat struct {
	Nom       string  `json:"nom"`
	CPU       float64 `json:"cpu_pct"`
	MemoireMo float64 `json:"memoire_mo"`
	LimiteMo  float64 `json:"limite_mo,omitempty"`
}

// Mongo : le diagnostic d'une base.
type Mongo struct {
	Base string `json:"base"`
	// NomBase : le nom de la base examinée dans le cluster.
	NomBase string `json:"nom_base,omitempty"`
	// Hotes : les serveurs du cluster, sans identifiants.
	Hotes  []string `json:"hotes,omitempty"`
	Etat   string   `json:"etat"` // ok, erreur
	Erreur string   `json:"erreur,omitempty"`
	// PingMs : un aller-retour, connexion déjà ouverte — le prix de chaque
	// requête. ConnexionMs : l'ouverture de la connexion, authentification
	// comprise — le prix d'un démarrage ou d'une connexion perdue.
	PingMs      int64        `json:"ping_ms,omitempty"`
	ConnexionMs int64        `json:"connexion_ms,omitempty"`
	Version     string       `json:"version,omitempty"`
	DonneesMo   float64      `json:"donnees_mo,omitempty"`
	StockageMo  float64      `json:"stockage_mo,omitempty"`
	IndexMo     float64      `json:"index_mo,omitempty"`
	Connexions  *Connexions  `json:"connexions,omitempty"`
	Collections []Collection `json:"collections,omitempty"`
}

type Connexions struct {
	Actuelles   int `json:"actuelles"`
	Disponibles int `json:"disponibles"`
}

type Collection struct {
	Nom       string   `json:"nom"`
	Documents int64    `json:"documents"`
	Index     []string `json:"index,omitempty"`
}

// Explication : le plan d'exécution d'une recherche, pour savoir si elle passe
// par un index ou parcourt toute la collection.
type Explication struct {
	Base       string `json:"base"`
	Collection string `json:"collection"`
	Filtre     string `json:"filtre"`
	// Etapes : le plan gagnant, de la racine à la feuille (« FETCH > IXSCAN »).
	Etapes              string `json:"etapes"`
	Index               string `json:"index,omitempty"`
	ParcoursComplet     bool   `json:"parcours_complet,omitempty"`
	Rendus              int64  `json:"rendus"`
	DocumentsExamines   int64  `json:"documents_examines"`
	ClesExaminees       int64  `json:"cles_examinees"`
	DureeMs             int64  `json:"duree_ms"`
	DocumentsCollection int64  `json:"documents_collection,omitempty"`
}

// Site : un vhost nginx et ce vers quoi il renvoie.
type Site struct {
	Fichier string   `json:"fichier"`
	Noms    []string `json:"noms"`
	Vers    []string `json:"vers,omitempty"`
	TLS     bool     `json:"tls,omitempty"`
}

// DaysToHours traduit une durée en jours (« 7d », « 2j ») en heures, la seule
// unité longue que time.ParseDuration connaisse. Le modèle dit « depuis 7d »
// aussi naturellement que « depuis 30m » ; refuser l'un et pas l'autre faisait
// échouer la lecture des journaux pour une affaire d'unité.
func DaysToHours(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	unit := s[len(s)-1]
	if unit != 'd' && unit != 'j' {
		return s
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 {
		return s
	}
	return strconv.Itoa(n*24) + "h"
}
