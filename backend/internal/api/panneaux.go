package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/atelier"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Les panneaux des projets : ce que Mathias veut voir d'un projet dans HQ.
//
// Il le demande (« ajoute sur mon dashboard de pxsky les jobs en cours, avec
// leur progression, leur worker, leur tag ») ; l'ingénieur écrit un script qui
// va chercher les données et les imprime dans le format ci-dessous, le teste,
// et l'enregistre avec ecrire_panneau. HQ le relance dans l'atelier — là où
// sont les accès, pxsky-lire compris — tant qu'on regarde le panneau, et le
// dessine avec ses propres composants : tableau, chiffres, barres, liste,
// texte. Le code de HQ ne change pas, rien n'est à déployer : le panneau
// apparaît dès qu'il est enregistré.

const (
	panneauRafraichirMin    = 10
	panneauRafraichirDefaut = 30
	panneauDelai            = 50 * time.Second
	panneauScriptMax        = 60_000
	panneauBlocsMax         = 12
	panneauLignesMax        = 500
)

// -- Le format que les scripts impriment ------------------------------------------

var (
	typesBloc      = map[string]bool{"chiffres": true, "tableau": true, "barres": true, "liste": true, "texte": true}
	formatsColonne = map[string]bool{"": true, "texte": true, "nombre": true, "progression": true, "etiquette": true, "etat": true, "date": true, "duree": true, "lien": true, "code": true}
	tons           = map[string]bool{"": true, "ok": true, "info": true, "attention": true, "danger": true, "neutre": true}
)

type colonnePanneau struct {
	Cle    string `json:"cle"`
	Label  string `json:"label"`
	Format string `json:"format,omitempty"`
}

type blocPanneau struct {
	Type  string `json:"type"`
	Titre string `json:"titre,omitempty"`
	Ton   string `json:"ton,omitempty"`
	// chiffres, barres, liste
	Elements []map[string]any `json:"elements,omitempty"`
	Unite    string           `json:"unite,omitempty"`
	// tableau
	Colonnes []colonnePanneau `json:"colonnes,omitempty"`
	Lignes   []map[string]any `json:"lignes,omitempty"`
	// Vide : ce qu'on lit quand il n'y a rien à montrer.
	Vide string `json:"vide,omitempty"`
	// texte
	Texte string `json:"texte,omitempty"`
}

type donneesPanneau struct {
	Resume string        `json:"resume,omitempty"`
	Ton    string        `json:"ton,omitempty"`
	Blocs  []blocPanneau `json:"blocs"`
}

// lireSortie vérifie ce qu'a imprimé un script. Les erreurs disent quoi
// corriger : c'est l'ingénieur qui les lit, au moment d'enregistrer.
func lireSortie(sortie string) (donneesPanneau, error) {
	sortie = strings.TrimSpace(sortie)
	if sortie == "" {
		return donneesPanneau{}, errors.New("le script n'a rien imprimé")
	}
	var d donneesPanneau
	if err := json.NewDecoder(strings.NewReader(sortie)).Decode(&d); err != nil {
		// Des messages de mise au point avant le JSON : la dernière ligne
		// peut suffire.
		if i := strings.LastIndex(sortie, "\n"); i >= 0 && json.Unmarshal([]byte(sortie[i+1:]), &d) == nil && len(d.Blocs) > 0 {
			return d, validerDonnees(d)
		}
		return donneesPanneau{}, fmt.Errorf("sortie illisible, un seul objet JSON attendu : %v", err)
	}
	return d, validerDonnees(d)
}

func validerDonnees(d donneesPanneau) error {
	if len(d.Blocs) == 0 {
		return errors.New("aucun bloc : « blocs » doit contenir au moins un bloc")
	}
	if len(d.Blocs) > panneauBlocsMax {
		return fmt.Errorf("%d blocs : %d au plus", len(d.Blocs), panneauBlocsMax)
	}
	if !tons[d.Ton] {
		return fmt.Errorf("ton inconnu : %q (ok, info, attention, danger, neutre)", d.Ton)
	}
	for i, b := range d.Blocs {
		n := fmt.Sprintf("bloc %d (%s)", i+1, b.Type)
		if !typesBloc[b.Type] {
			return fmt.Errorf("bloc %d : type inconnu %q (chiffres, tableau, barres, liste, texte)", i+1, b.Type)
		}
		if !tons[b.Ton] {
			return fmt.Errorf("%s : ton inconnu %q", n, b.Ton)
		}
		for _, e := range b.Elements {
			if t, _ := e["ton"].(string); !tons[t] {
				return fmt.Errorf("%s : ton inconnu %q", n, t)
			}
		}
		switch b.Type {
		case "tableau":
			if len(b.Colonnes) == 0 {
				return fmt.Errorf("%s : « colonnes » manque", n)
			}
			for _, c := range b.Colonnes {
				if c.Cle == "" {
					return fmt.Errorf("%s : une colonne sans « cle »", n)
				}
				if !formatsColonne[c.Format] {
					return fmt.Errorf("%s : format de colonne inconnu %q (texte, nombre, progression, etiquette, etat, date, duree, lien, code)", n, c.Format)
				}
			}
			if len(b.Lignes) > panneauLignesMax {
				return fmt.Errorf("%s : %d lignes, %d au plus — filtre ou résume", n, len(b.Lignes), panneauLignesMax)
			}
		case "chiffres", "barres", "liste":
			if len(b.Elements) > panneauLignesMax {
				return fmt.Errorf("%s : %d éléments, %d au plus", n, len(b.Elements), panneauLignesMax)
			}
		case "texte":
			if strings.TrimSpace(b.Texte) == "" {
				return fmt.Errorf("%s : « texte » vide", n)
			}
		}
	}
	return nil
}

// -- Les relevés ------------------------------------------------------------------

// releve : le dernier passage d'un panneau. On garde les dernières bonnes
// données : un relevé en échec les laisse affichées, avec l'erreur.
type releve struct {
	mu      sync.Mutex
	version time.Time
	at      time.Time
	donnees *donneesPanneau
	bonneAt time.Time
	erreur  string
	ms      int64
}

type releveVue struct {
	Donnees *donneesPanneau `json:"donnees,omitempty"`
	// Maj : quand les données affichées ont été relevées.
	Maj    *time.Time `json:"maj,omitempty"`
	Erreur string     `json:"erreur,omitempty"`
	Ms     int64      `json:"ms,omitempty"`
}

type releves struct {
	mu sync.Mutex
	m  map[bson.ObjectID]*releve
}

func (rs *releves) de(id bson.ObjectID) *releve {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.m == nil {
		rs.m = map[bson.ObjectID]*releve{}
	}
	r, ok := rs.m[id]
	if !ok {
		r = &releve{}
		rs.m[id] = r
	}
	return r
}

func (rs *releves) oublier(id bson.ObjectID) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.m, id)
}

// releverPanneau rend les données d'un panneau : celles du dernier relevé
// s'il est assez frais, sinon un nouveau passage du script. Deux écrans qui
// regardent le même panneau ne le font pas tourner deux fois.
func (s *Server) releverPanneau(ctx context.Context, p store.Panneau, forcer bool) releveVue {
	r := s.panneauxReleves.de(p.ID)
	r.mu.Lock()
	defer r.mu.Unlock()
	frais := time.Duration(max(p.Rafraichir, panneauRafraichirMin)) * time.Second
	if forcer || !r.version.Equal(p.UpdatedAt) || time.Since(r.at) >= frais-2*time.Second {
		if !r.version.Equal(p.UpdatedAt) {
			r.donnees, r.bonneAt = nil, time.Time{}
		}
		d, ms, err := s.lancerPanneau(ctx, p.ID.Hex(), p.Script)
		r.version, r.at, r.ms = p.UpdatedAt, time.Now(), ms
		if err != nil {
			r.erreur = err.Error()
		} else {
			r.donnees, r.bonneAt, r.erreur = &d, r.at, ""
		}
	}
	v := releveVue{Donnees: r.donnees, Erreur: r.erreur, Ms: r.ms}
	if !r.bonneAt.IsZero() {
		at := r.bonneAt
		v.Maj = &at
	}
	return v
}

// lancerPanneau fait tourner un script et vérifie ce qu'il imprime.
func (s *Server) lancerPanneau(ctx context.Context, nom, script string) (donneesPanneau, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, panneauDelai)
	defer cancel()
	run := fmt.Sprintf("panneau%s%d", nom, time.Now().UnixNano())
	var out atelier.Donnees
	var err error
	switch {
	case s.atelier != nil:
		out, err = s.atelier.Panneau(ctx, run, script)
	case s.cfg.PanneauxLocal:
		out, err = panneauLocal(ctx, run, script)
	default:
		err = errors.New("l'atelier n'est pas configuré : les panneaux ne peuvent pas tourner")
	}
	if err != nil {
		return donneesPanneau{}, 0, err
	}
	if out.Code != 0 {
		msg := strings.TrimSpace(out.Stderr)
		if msg == "" {
			msg = clipTo(strings.TrimSpace(out.Sortie), 500)
		}
		return donneesPanneau{}, out.Ms, fmt.Errorf("le script s'est arrêté (code %d) : %s", out.Code, clipTo(msg, 1500))
	}
	d, err := lireSortie(out.Sortie)
	return d, out.Ms, err
}

// panneauLocal : en développement, sans atelier (PANNEAUX_LOCAL=oui).
func panneauLocal(ctx context.Context, run, script string) (atelier.Donnees, error) {
	dir, err := os.MkdirTemp("", "cerveau-panneau-")
	if err != nil {
		return atelier.Donnees{}, err
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, run+".py")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		return atelier.Donnees{}, err
	}
	cmd := exec.CommandContext(ctx, "python3", "-I", f)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	debut := time.Now()
	err = cmd.Run()
	out := atelier.Donnees{Ms: time.Since(debut).Milliseconds(), Sortie: stdout.String(), Stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		out.Code = exit.ExitCode()
	case err != nil:
		return atelier.Donnees{}, err
	}
	return out, nil
}

// -- Ce que les agents en savent ---------------------------------------------------

// consignePanneaux : ce que sait un agent de l'atelier des panneaux de HQ.
// Le responsable ne les écrit pas : il les confie à l'ingénieur.
func consignePanneaux(m equipe.Membre) string {
	if m.Pilote || m.Veille != "" {
		return ""
	}
	return `PANNEAUX DE HQ. Quand Mathias veut voir quelque chose d'un projet dans HQ (« ajoute sur mon dashboard… », « un panneau avec… », « un suivi de… »), ne touche pas au code de HQ et ne fais ni branche ni commit : écris un panneau. Il apparaît aussitôt sur le tableau de bord du projet.
Un panneau est un script Python 3 (bibliothèque standard seulement) que HQ relance dans l'atelier toutes les rafraichir_s secondes tant qu'on le regarde. Il imprime UN objet JSON :
{"resume": "12 jobs en cours", "ton": "ok", "blocs": [ … ]}
Les blocs (chacun peut avoir un "titre") :
- {"type":"chiffres","elements":[{"label":"En cours","valeur":12,"detail":"3 en file","ton":"info"}]}
- {"type":"tableau","colonnes":[{"cle":"job","label":"Job"},{"cle":"progress","label":"Progression","format":"progression"},{"cle":"worker","label":"Worker","format":"code"},{"cle":"tag","label":"Tag","format":"etiquette"},{"cle":"statut","label":"État","format":"etat"},{"cle":"debut","label":"Démarré","format":"date"}],"lignes":[{"job":"…","progress":42}],"vide":"Aucun job en cours"}
  Formats de colonne : texte (défaut), nombre, progression (0 à 100), etiquette, etat (running, failed… : la couleur suit), date (ISO 8601 ou secondes epoch), duree (secondes), lien, code. Une cellule peut aussi être {"texte":"…","ton":"danger","url":"…"}.
- {"type":"barres","unite":"Go","elements":[{"label":"…","valeur":7,"max":10,"ton":"attention"}]}
- {"type":"liste","elements":[{"titre":"…","detail":"…","meta":"…","ton":"danger","url":"…"}]}
- {"type":"texte","texte":"…","ton":"attention"}
Tons : ok, info, attention, danger, neutre. 500 lignes au plus par bloc, 12 blocs au plus.
Règles : lecture seule ; 30 secondes au plus ; aucun secret dans le script ni dans sa sortie ; si une source échoue, imprime quand même un JSON avec un bloc texte qui le dit. Pour pxsky : subprocess.run(["sudo", "pxsky-lire", "<chemin>", "clé=valeur", …]) et lis son JSON ; filtre côté API quand c'est possible, les listes complètes pèsent lourd.
Pour aller vite : 1) panneaux_projet et les fiches du projet (l'API y est peut-être déjà décrite) ; 2) une seule lecture de la source pour voir la forme des données ; 3) écris panneau.py dans ton dossier, lance python3 -I panneau.py et corrige jusqu'à un JSON propre ; 4) enregistre-le avec ecrire_panneau, qui le relance et le vérifie. Pour modifier un panneau, donne son identifiant à ecrire_panneau. Range dans une fiche ce que tu as appris de la source s'il vaut encore dans un mois.
Quand ta tâche était un panneau, ta réponse dit ce qu'il montre, en une phrase.`
}

func (s *Server) outilsPanneaux(moi equipe.Membre) []assistant.ToolSpec {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	tools := []assistant.ToolSpec{{
		Name:        "panneaux_projet",
		Description: "Les panneaux du tableau de bord d'un projet dans HQ : identifiant, titre, ce que Mathias a demandé, rythme, et leur script.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"projet": str("L'identifiant du projet."),
		}, "required": []any{"projet"}},
	}}
	if moi.Pilote || moi.Veille != "" {
		return tools
	}
	return append(tools,
		assistant.ToolSpec{
			Name: "ecrire_panneau",
			Description: "Enregistre un panneau sur le tableau de bord d'un projet dans HQ, ou remplace un panneau existant. Le script est lancé une fois " +
				"et sa sortie vérifiée : en cas d'erreur, rien n'est enregistré et l'erreur dit quoi corriger. Une fois enregistré, il apparaît aussitôt dans HQ.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"projet":       str("L'identifiant du projet."),
				"titre":        str("Le titre du panneau, court (« Jobs en cours »)."),
				"script":       str("Le script Python 3 complet, qui imprime le JSON du panneau."),
				"rafraichir_s": map[string]any{"type": "integer", "description": fmt.Sprintf("Toutes les combien de secondes le relancer (%d au moins, %d par défaut).", panneauRafraichirMin, panneauRafraichirDefaut)},
				"largeur":      map[string]any{"type": "string", "enum": []any{store.PanneauNormal, store.PanneauLarge}, "description": "« large » pour un tableau de plusieurs colonnes : toute la largeur."},
				"panneau":      str("L'identifiant du panneau à remplacer. Absent : un nouveau panneau."),
				"demande":      str("Ce que Mathias a demandé, en une phrase."),
			}, "required": []any{"projet", "titre", "script"}},
		},
		assistant.ToolSpec{
			Name:        "retirer_panneau",
			Description: "Retire un panneau du tableau de bord d'un projet.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"panneau": str("L'identifiant du panneau."),
			}, "required": []any{"panneau"}},
		},
	)
}

func estOutilPanneaux(name string) bool {
	return name == "panneaux_projet" || name == "ecrire_panneau" || name == "retirer_panneau"
}

// appelOutilPanneaux exécute un outil des panneaux pour l'agent qui travaille.
func (s *Server) appelOutilPanneaux(ctx context.Context, caller runSnapshot, moi equipe.Membre, name, args string) (string, error) {
	var in struct {
		Projet     string `json:"projet"`
		Titre      string `json:"titre"`
		Script     string `json:"script"`
		Rafraichir int    `json:"rafraichir_s"`
		Largeur    string `json:"largeur"`
		Panneau    string `json:"panneau"`
		Demande    string `json:"demande"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("arguments illisibles : %w", err)
	}
	projet := func() (projetVue, error) {
		if p, ok := s.projetDuCatalogue(ctx, in.Projet); ok {
			return p, nil
		}
		return projetVue{}, fmt.Errorf("projet inconnu : %q — appelle projets_connus pour les identifiants", in.Projet)
	}
	switch name {
	case "panneaux_projet":
		p, err := projet()
		if err != nil {
			return "", err
		}
		list, err := s.store.Panneaux(ctx, p.ID)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "Aucun panneau sur ce projet pour l'instant.", nil
		}
		raw, _ := json.Marshal(list)
		return string(raw), nil

	case "ecrire_panneau":
		if moi.Pilote || moi.Veille != "" {
			return "", errors.New("les panneaux s'écrivent dans l'atelier : confie-le à l'ingénieur")
		}
		p, err := projet()
		if err != nil {
			return "", err
		}
		pan := store.Panneau{
			Projet: p.ID, Titre: clipTo(strings.TrimSpace(in.Titre), 80), Script: in.Script,
			Rafraichir: in.Rafraichir, Largeur: in.Largeur, Demande: clipTo(strings.TrimSpace(in.Demande), 500), Auteur: moi.ID,
		}
		if pan.Titre == "" || strings.TrimSpace(pan.Script) == "" {
			return "", errors.New("donne un titre et un script")
		}
		if len(pan.Script) > panneauScriptMax {
			return "", fmt.Errorf("script trop long (%d caractères, %d au plus)", len(pan.Script), panneauScriptMax)
		}
		if pan.Rafraichir == 0 {
			pan.Rafraichir = panneauRafraichirDefaut
		}
		pan.Rafraichir = min(max(pan.Rafraichir, panneauRafraichirMin), 3600)
		if pan.Largeur != store.PanneauLarge {
			pan.Largeur = store.PanneauNormal
		}
		if !caller.Run.ID.IsZero() {
			id := caller.Run.ID
			pan.Run = &id
		}
		if pan.Demande == "" {
			pan.Demande = clipTo(caller.Run.Demande, 500)
		}
		if strings.TrimSpace(in.Panneau) != "" {
			id, err := bson.ObjectIDFromHex(strings.TrimSpace(in.Panneau))
			if err != nil {
				return "", errors.New("identifiant de panneau invalide")
			}
			pan.ID = id
		}
		d, ms, err := s.lancerPanneau(ctx, "essai", pan.Script)
		if err != nil {
			return "", fmt.Errorf("panneau non enregistré — %v", err)
		}
		saved, err := s.store.EcrirePanneau(ctx, pan)
		if errors.Is(err, store.ErrNotFound) {
			return "", errors.New("aucun panneau de ce projet ne porte cet identifiant")
		}
		if err != nil {
			return "", err
		}
		// Le premier relevé est déjà fait : HQ l'affiche sans attendre.
		r := s.panneauxReleves.de(saved.ID)
		r.mu.Lock()
		r.version, r.at, r.bonneAt, r.donnees, r.erreur, r.ms = saved.UpdatedAt, time.Now(), time.Now(), &d, "", ms
		r.mu.Unlock()
		lignes := 0
		var types []string
		for _, b := range d.Blocs {
			types = append(types, b.Type)
			lignes += len(b.Lignes) + len(b.Elements)
		}
		return fmt.Sprintf("Panneau enregistré (identifiant %s) : il est dans HQ, sur le tableau de bord de %s. Relevé en %d ms : %s, %d lignes.",
			saved.ID.Hex(), p.Nom, ms, strings.Join(types, ", "), lignes), nil

	case "retirer_panneau":
		if moi.Pilote || moi.Veille != "" {
			return "", errors.New("confie-le à l'ingénieur")
		}
		id, err := bson.ObjectIDFromHex(strings.TrimSpace(in.Panneau))
		if err != nil {
			return "", errors.New("identifiant de panneau invalide")
		}
		if err := s.store.SupprimerPanneau(ctx, id); err != nil {
			return "", err
		}
		s.panneauxReleves.oublier(id)
		return "Panneau retiré.", nil
	}
	return "", fmt.Errorf("outil inconnu : %s", name)
}

// -- L'API de HQ -------------------------------------------------------------------

func sansScript(list []store.Panneau) []store.Panneau {
	for i := range list {
		list[i].Script = ""
	}
	return list
}

// handleHQPanneaux : tous les panneaux, ou ceux d'un projet (?projet=).
func (s *Server) handleHQPanneaux(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Panneaux(r.Context(), strings.TrimSpace(r.URL.Query().Get("projet")))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des panneaux impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"panneaux": sansScript(list)})
}

func (s *Server) panneauDeLaRequete(w http.ResponseWriter, r *http.Request) (store.Panneau, bool) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return store.Panneau{}, false
	}
	p, err := s.store.Panneau(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "panneau introuvable")
		} else {
			httpx.Error(w, http.StatusInternalServerError, "lecture du panneau impossible")
		}
		return store.Panneau{}, false
	}
	return p, true
}

// handleHQPanneau : un panneau, script compris.
func (s *Server) handleHQPanneau(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.panneauDeLaRequete(w, r); ok {
		httpx.JSON(w, http.StatusOK, map[string]any{"panneau": p})
	}
}

// handleHQDonneesPanneau : ce que montre un panneau en ce moment (?forcer=1
// pour un relevé neuf).
func (s *Server) handleHQDonneesPanneau(w http.ResponseWriter, r *http.Request) {
	p, ok := s.panneauDeLaRequete(w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, s.releverPanneau(r.Context(), p, r.URL.Query().Get("forcer") == "1"))
}

func (s *Server) handleHQModifierPanneau(w http.ResponseWriter, r *http.Request) {
	p, ok := s.panneauDeLaRequete(w, r)
	if !ok {
		return
	}
	var d struct {
		Titre   *string  `json:"titre"`
		Largeur *string  `json:"largeur"`
		Ordre   *float64 `json:"ordre"`
	}
	if err := httpx.Decode(r, &d); err != nil {
		httpx.Error(w, http.StatusBadRequest, "requête illisible")
		return
	}
	set := bson.M{}
	if d.Titre != nil {
		t := clipTo(strings.TrimSpace(*d.Titre), 80)
		if t == "" {
			httpx.Error(w, http.StatusBadRequest, "un titre, au moins")
			return
		}
		set["titre"] = t
	}
	if d.Largeur != nil {
		if *d.Largeur != store.PanneauNormal && *d.Largeur != store.PanneauLarge {
			httpx.Error(w, http.StatusBadRequest, "largeur : normal ou large")
			return
		}
		set["largeur"] = *d.Largeur
	}
	if d.Ordre != nil {
		set["ordre"] = *d.Ordre
	}
	if len(set) == 0 {
		httpx.JSON(w, http.StatusOK, map[string]any{"panneau": sansScript([]store.Panneau{p})[0]})
		return
	}
	out, err := s.store.ModifierPanneau(r.Context(), p.ID, set)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	// Le script n'a pas changé : le relevé en cours reste bon.
	rel := s.panneauxReleves.de(out.ID)
	rel.mu.Lock()
	if rel.version.Equal(p.UpdatedAt) {
		rel.version = out.UpdatedAt
	}
	rel.mu.Unlock()
	httpx.JSON(w, http.StatusOK, map[string]any{"panneau": sansScript([]store.Panneau{out})[0]})
}

func (s *Server) handleHQSupprimerPanneau(w http.ResponseWriter, r *http.Request) {
	p, ok := s.panneauDeLaRequete(w, r)
	if !ok {
		return
	}
	if err := s.store.SupprimerPanneau(r.Context(), p.ID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "suppression impossible")
		return
	}
	s.panneauxReleves.oublier(p.ID)
	httpx.JSON(w, http.StatusOK, map[string]string{"retire": p.ID.Hex()})
}

// handleHQDemanderPanneau : Mathias demande un panneau (ou un changement
// d'un panneau) depuis le projet. La demande va droit à l'ingénieur : sans
// passer par Hugo, elle démarre aussitôt.
func (s *Server) handleHQDemanderPanneau(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	p, ok := s.projetDuCatalogue(r.Context(), chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "projet inconnu")
		return
	}
	var req struct {
		Demande string `json:"demande"`
		Panneau string `json:"panneau"`
	}
	if err := httpx.Decode(r, &req); err != nil || strings.TrimSpace(req.Demande) == "" {
		httpx.Error(w, http.StatusBadRequest, "dis ce que tu veux voir")
		return
	}
	m, ok := s.hqMember(equipe.IDIngenieur)
	if !ok {
		httpx.Error(w, http.StatusServiceUnavailable, "l'ingénieur n'est pas dans l'équipe")
		return
	}
	demande := fmt.Sprintf("Panneau pour le tableau de bord du projet %s (identifiant %q) dans HQ. Mathias demande : %s",
		p.Nom, p.ID, strings.TrimSpace(req.Demande))
	affectation := "Panneau du projet " + p.Nom
	if id := strings.TrimSpace(req.Panneau); id != "" {
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "identifiant de panneau invalide")
			return
		}
		pan, err := s.store.Panneau(r.Context(), oid)
		if err != nil || pan.Projet != p.ID {
			httpx.Error(w, http.StatusNotFound, "panneau introuvable")
			return
		}
		demande += fmt.Sprintf("\nC'est un changement du panneau « %s » (identifiant %s) : relis son script avec panneaux_projet et remplace-le avec ecrire_panneau, en donnant son identifiant.", pan.Titre, pan.ID.Hex())
	}
	run, err := s.launchRun(user, m, demande, runOpts{Affectation: affectation}, "")
	if err != nil {
		s.launchError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"tache": tacheView(run, "", nil)})
}
