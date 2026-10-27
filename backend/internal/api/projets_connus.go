package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Les projets que connaît Hugo, et comment l'équipe apprend les nouveaux.
//
// Le catalogue réunit les projets du registre (ceux qui tournent sur le VPS)
// et ceux que Mathias a donnés depuis HQ ou en le disant à Hugo. Donner un
// projet lance une étude : Hugo le fait lire par l'équipe, chacun range ce
// qu'il a compris dans les fiches partagées (noter_fiche) et pose à Mathias
// ce que le code ne dit pas (poser_question). Toutes les tâches de l'atelier
// reçoivent ensuite la liste des projets, leurs dépôts clonés et de quoi lire
// leurs fiches : n'importe quel agent peut y travailler sans tout redécouvrir.

// États d'un projet dans le catalogue.
const (
	projetAEtudier = "a_etudier" // donné, pas encore étudié
	projetEnEtude  = "en_etude"  // l'équipe l'étudie en ce moment
	projetQuestion = "questions" // des questions attendent Mathias
	projetConnu    = "connu"
)

type projetVue struct {
	ID          string `json:"id"`
	Nom         string `json:"nom"`
	Description string `json:"description,omitempty"`
	// Origine : « registre » (il tourne sur le VPS) ou « ajoute ».
	Origine   string                 `json:"origine"`
	Depots    []string               `json:"depots"`
	Liens     []string               `json:"liens,omitempty"`
	Notes     string                 `json:"notes,omitempty"`
	Sujets    []string               `json:"sujets"`
	Questions []store.QuestionProjet `json:"questions"`
	Ouvertes  int                    `json:"questions_ouvertes"`
	Etat      string                 `json:"etat"`
	Etude     *hqTache               `json:"etude,omitempty"`
}

// catalogue rend les projets connus, ceux du registre d'abord.
func (s *Server) catalogue(ctx context.Context) ([]projetVue, error) {
	if s.store == nil {
		return nil, errors.New("pas de base")
	}
	ajoutes, err := s.store.ProjetsConnus(ctx)
	if err != nil {
		return nil, err
	}
	fiches, err := s.store.Connaissances(ctx, store.ConnaissanceQuery{Equipe: equipe.EquipeInformatique, Limit: 500})
	if err != nil {
		return nil, err
	}
	sujets := map[string][]string{}
	for _, f := range fiches {
		sujets[f.Projet] = append(sujets[f.Projet], f.Sujet)
	}
	extra := map[string]store.ProjetConnu{}
	for _, p := range ajoutes {
		extra[p.Slug] = p
	}

	var out []projetVue
	if s.projets != nil {
		for _, p := range s.projets.Registre.Projets {
			v := projetVue{ID: p.ID, Nom: p.Nom, Description: p.Description, Origine: "registre"}
			for _, c := range p.Composants {
				v.Depots = appendUnique(v.Depots, c.Depot)
			}
			if e, ok := extra[p.ID]; ok {
				s.completerVue(ctx, &v, e)
				delete(extra, p.ID)
			}
			out = append(out, v)
		}
	}
	for _, p := range ajoutes {
		if _, ok := extra[p.Slug]; !ok {
			continue
		}
		v := projetVue{ID: p.Slug, Nom: p.Nom, Description: p.Description, Origine: "ajoute"}
		s.completerVue(ctx, &v, p)
		out = append(out, v)
	}
	for i := range out {
		v := &out[i]
		v.Sujets = sujets[v.ID]
		sort.Strings(v.Sujets)
		if v.Sujets == nil {
			v.Sujets = []string{}
		}
		if v.Depots == nil {
			v.Depots = []string{}
		}
		if v.Questions == nil {
			v.Questions = []store.QuestionProjet{}
		}
		for _, q := range v.Questions {
			if q.Repondue == nil {
				v.Ouvertes++
			}
		}
		switch {
		case v.Etude != nil && (v.Etude.Statut == store.RunEnCours || v.Etude.Statut == store.RunEnAttente):
			v.Etat = projetEnEtude
		case v.Ouvertes > 0:
			v.Etat = projetQuestion
		case len(v.Sujets) > 0:
			v.Etat = projetConnu
		default:
			v.Etat = projetAEtudier
		}
	}
	return out, nil
}

// completerVue ajoute à un projet ce que la base en sait : dépôts et liens
// donnés par Mathias, ses notes, les questions, l'étude.
func (s *Server) completerVue(ctx context.Context, v *projetVue, p store.ProjetConnu) {
	if p.Description != "" {
		v.Description = p.Description
	}
	for _, d := range p.Depots {
		v.Depots = appendUnique(v.Depots, d)
	}
	v.Liens, v.Notes, v.Questions = p.Liens, p.Notes, p.Questions
	if p.Etude == nil {
		return
	}
	if snap, ok := s.runs.liveRunByID(*p.Etude); ok {
		t := tacheView(snap.Run, snap.Etape, snap.Etapes)
		v.Etude = &t
	} else if run, err := s.store.RunByIDAny(ctx, *p.Etude); err == nil {
		t := tacheView(run, "", nil)
		v.Etude = &t
	}
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return list
		}
	}
	return append(list, v)
}

// projetDuCatalogue rend un projet par son identifiant.
func (s *Server) projetDuCatalogue(ctx context.Context, id string) (projetVue, bool) {
	list, err := s.catalogue(ctx)
	if err != nil {
		return projetVue{}, false
	}
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return projetVue{}, false
}

// depotsConnus : tous les dépôts du catalogue, que l'atelier clone pour
// chaque tâche.
func (s *Server) depotsConnus(ctx context.Context) []string {
	list, err := s.catalogue(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range list {
		for _, d := range p.Depots {
			out = appendUnique(out, d)
		}
	}
	return out
}

var (
	depotGitHubRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	depotSCPRE    = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9.-]+:[A-Za-z0-9_./-]+$`)
)

// normaliserDepot accepte « propriétaire/dépôt », une adresse GitHub, ou une
// adresse git complète (ssh://, https://, git@hôte:chemin) ; rend la forme que
// l'atelier sait cloner.
func normaliserDepot(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "" || strings.HasPrefix(d, "-") || strings.ContainsAny(d, " \t\n\"'`$;|&") {
		return "", fmt.Errorf("dépôt invalide : %q", d)
	}
	if depotGitHubRE.MatchString(d) {
		return strings.TrimSuffix(d, ".git"), nil
	}
	if depotSCPRE.MatchString(d) {
		return d, nil
	}
	u, err := url.Parse(d)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return "", fmt.Errorf("dépôt invalide : %q (attendu : propriétaire/dépôt, ou une adresse git)", d)
	}
	if strings.EqualFold(u.Host, "github.com") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), nil
		}
	}
	u.User = nil
	if u.Scheme == "ssh" {
		u.User = url.User("git")
	}
	return u.String(), nil
}

// slugProjet : l'identifiant d'un projet, tiré de son nom.
func slugProjet(nom string) string {
	return strings.ReplaceAll(fuzzy.Normalize(nom), " ", "-")
}

// projetDonne : ce que Mathias (ou Hugo) dit d'un projet en le donnant.
type projetDonne struct {
	Nom         string   `json:"nom"`
	Description string   `json:"description"`
	Depots      []string `json:"depots"`
	Liens       []string `json:"liens"`
	Notes       string   `json:"notes"`
}

func (d *projetDonne) nettoyer() error {
	d.Nom = strings.TrimSpace(d.Nom)
	d.Description = strings.TrimSpace(d.Description)
	d.Notes = strings.TrimSpace(d.Notes)
	if len([]rune(d.Description))+len([]rune(d.Notes)) > 8000 {
		return errors.New("description et notes trop longues (8000 caractères au plus)")
	}
	var depots []string
	for _, raw := range d.Depots {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		n, err := normaliserDepot(raw)
		if err != nil {
			return err
		}
		depots = appendUnique(depots, n)
	}
	d.Depots = depots
	var liens []string
	for _, raw := range d.Liens {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("lien invalide : %q", raw)
		}
		liens = appendUnique(liens, raw)
	}
	d.Liens = liens
	return nil
}

// ajouterProjet enregistre un projet donné.
func (s *Server) ajouterProjet(ctx context.Context, d projetDonne) (store.ProjetConnu, error) {
	if err := d.nettoyer(); err != nil {
		return store.ProjetConnu{}, err
	}
	slug := slugProjet(d.Nom)
	if slug == "" {
		return store.ProjetConnu{}, errors.New("donne un nom au projet")
	}
	if slug == "general" {
		return store.ProjetConnu{}, store.ErrProjetExiste
	}
	if s.projets != nil {
		for _, p := range s.projets.Registre.Projets {
			if p.ID == slug {
				return store.ProjetConnu{}, store.ErrProjetExiste
			}
		}
	}
	return s.store.CreerProjetConnu(ctx, store.ProjetConnu{
		Slug: slug, Nom: d.Nom, Description: d.Description, Depots: d.Depots, Liens: d.Liens, Notes: d.Notes,
	})
}

// lancerEtude confie à Hugo l'étude d'un projet : il la répartit dans
// l'équipe, qui range ce qu'elle comprend et pose ses questions.
func (s *Server) lancerEtude(ctx context.Context, user *store.User, p projetVue, precision string) (store.AgentRun, error) {
	hugo, ok := s.hqMember(equipe.IDPilote)
	if !ok {
		return store.AgentRun{}, errors.New("Hugo n'est pas dans l'équipe")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Fais connaître à l'équipe le projet « %s » (identifiant « %s »), pour que chacun puisse ensuite y travailler.\n", p.Nom, p.ID)
	if p.Description != "" {
		fmt.Fprintf(&b, "Ce qu'en dit Mathias : %s\n", p.Description)
	}
	if p.Notes != "" {
		fmt.Fprintf(&b, "Ses notes : %s\n", p.Notes)
	}
	if len(p.Depots) > 0 {
		b.WriteString("Dépôts (clonés dans ~/depots/<nom du dépôt>) : " + strings.Join(p.Depots, ", ") + "\n")
	} else {
		b.WriteString("Aucun dépôt donné : appuie-toi sur ce qu'en dit Mathias, les mails et Slack, et demande-lui où est le code.\n")
	}
	if len(p.Liens) > 0 {
		b.WriteString("Liens : " + strings.Join(p.Liens, ", ") + "\n")
	}
	if len(p.Sujets) > 0 {
		b.WriteString("Fiches déjà écrites : " + strings.Join(p.Sujets, ", ") + ". Complète-les plutôt que de repartir de zéro.\n")
	}
	if precision = strings.TrimSpace(precision); precision != "" {
		fmt.Fprintf(&b, "Ce que Mathias demande en plus : %s\n", precision)
	}
	run, err := s.launchRun(user, hugo, strings.TrimSpace(b.String()),
		runOpts{Affectation: "Étude du projet " + p.Nom, SansReponseDirecte: true},
		s.consignePilote(hugo)+"\n\n"+consigneEtude(p.ID))
	if err != nil {
		return store.AgentRun{}, err
	}
	if err := s.store.MarquerEtude(ctx, p.ID, p.Nom, run.ID); err != nil {
		slog.Warn("étude d'un projet non retenue", "projet", p.ID, "err", err)
	}
	return run, nil
}

// consigneEtude : comment Hugo fait étudier un projet.
func consigneEtude(projet string) string {
	return fmt.Sprintf(`ÉTUDE D'UN PROJET. Le but : que toute l'équipe connaisse le projet %q assez pour y travailler demain sans tout redécouvrir.
1. Regarde d'abord ce que l'équipe en sait : projets_connus, puis fiches_projet.
2. Confie la lecture du code à l'ingénieur, dépôt par dépôt. Demande-lui de ranger ce qu'il comprend avec noter_fiche, une fiche par sujet, chacune complète et précise : « Vue d'ensemble » (à quoi sert le projet, pour qui, ses morceaux et comment ils se parlent), « Architecture », « Lancer et tester », « Déploiement et environnements », « Points fragiles ».
3. Si le projet se discute ailleurs — mails pro, Slack, WhatsApp —, demande à la veille concernée ce qui s'en dit, et fais noter l'essentiel.
4. Ce que ni le code ni les échanges ne disent — le but, les utilisateurs, ce qui est en production ou abandonné, les priorités, les conventions —, pose-le à Mathias avec poser_question : une question par appel, précise, cinq au plus pour tout le projet. Chacun de l'équipe peut en poser.
5. Ta réponse finale à Mathias, à la place de la règle 5 : en deux phrases, ce que l'équipe a compris du projet, puis combien de questions l'attendent dans HQ.`, projet)
}

// consigneProjets : ce que toute tâche de l'atelier sait des projets.
func (s *Server) consigneProjets(ctx context.Context) string {
	list, err := s.catalogue(ctx)
	if err != nil || len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("PROJETS QUE CONNAÎT L'ÉQUIPE (identifiant entre parenthèses) :\n")
	for _, p := range list {
		fmt.Fprintf(&b, "- %s (%s)", p.Nom, p.ID)
		if p.Description != "" {
			fmt.Fprintf(&b, " : %s", clipTo(p.Description, 200))
		}
		if len(p.Depots) > 0 {
			b.WriteString(". Dépôts : " + strings.Join(p.Depots, ", "))
		}
		if len(p.Sujets) > 0 {
			b.WriteString(". Fiches : " + strings.Join(p.Sujets, ", "))
		} else {
			b.WriteString(". Pas encore de fiche")
		}
		b.WriteString(".\n")
	}
	b.WriteString(`Avant de travailler sur un projet, lis ses fiches avec fiches_projet : l'équipe y a rangé ce qu'elle sait.
Quand tu comprends quelque chose qui vaudra encore dans un mois, range-le avec noter_fiche — une fiche par sujet, réécrite en entier (l'ancien savoir plus le nouveau).
Si un point compte pour ta tâche et ne se devine ni du code ni des fiches, pose-le à Mathias avec poser_question, puis avance avec l'hypothèse la plus prudente.`)
	return b.String()
}

// reponsesEnFiche : les réponses de Mathias rejoignent les fiches du projet,
// où toute l'équipe les lit.
func (s *Server) reponsesEnFiche(ctx context.Context, p store.ProjetConnu) error {
	var b strings.Builder
	for _, q := range p.Questions {
		if q.Repondue == nil {
			continue
		}
		fmt.Fprintf(&b, "Question (%s) : %s\nRéponse de Mathias : %s\n\n", q.Auteur, q.Question, q.Reponse)
	}
	if b.Len() == 0 {
		return nil
	}
	_, err := s.store.Noter(ctx, store.Connaissance{
		Equipe: equipe.EquipeInformatique, Projet: p.Slug, Sujet: "Réponses de Mathias",
		Contenu: strings.TrimSpace(b.String()), Origine: store.OrigineApp, Auteur: "mathias",
	})
	return err
}

// --- L'API de HQ ---------------------------------------------------------------

func (s *Server) handleHQProjets(w http.ResponseWriter, r *http.Request) {
	list, err := s.catalogue(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des projets impossible")
		return
	}
	if list == nil {
		list = []projetVue{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"projets": list})
}

func (s *Server) handleHQProjet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projetDuCatalogue(r.Context(), chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "projet inconnu")
		return
	}
	fiches, err := s.store.Connaissances(r.Context(), store.ConnaissanceQuery{Equipe: equipe.EquipeInformatique, Projet: p.ID, Limit: 100})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des fiches impossible")
		return
	}
	if fiches == nil {
		fiches = []store.Connaissance{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"projet": p, "fiches": fiches})
}

func (s *Server) handleHQAjouterProjet(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req struct {
		projetDonne
		Etudier *bool `json:"etudier"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "requête illisible")
		return
	}
	created, err := s.ajouterProjet(r.Context(), req.projetDonne)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrProjetExiste) {
			status = http.StatusConflict
		}
		httpx.Error(w, status, err.Error())
		return
	}
	out := map[string]any{}
	p, _ := s.projetDuCatalogue(r.Context(), created.Slug)
	if req.Etudier == nil || *req.Etudier {
		run, err := s.lancerEtude(r.Context(), user, p, "")
		if err != nil {
			slog.Warn("étude d'un nouveau projet non lancée", "projet", created.Slug, "err", err)
			out["erreur_etude"] = "le projet est enregistré, mais l'étude n'a pas pu partir : relance-la"
		} else {
			t := tacheView(run, "", nil)
			out["tache"] = t
			p.Etude, p.Etat = &t, projetEnEtude
		}
	}
	out["projet"] = p
	httpx.JSON(w, http.StatusCreated, out)
}

// handleHQCompleterProjet : ce que Mathias ajoute à un projet, du registre
// ou donné.
func (s *Server) handleHQCompleterProjet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projetDuCatalogue(r.Context(), chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "projet inconnu")
		return
	}
	var d projetDonne
	if err := httpx.Decode(r, &d); err != nil {
		httpx.Error(w, http.StatusBadRequest, "requête illisible")
		return
	}
	if err := d.nettoyer(); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.CompleterProjetConnu(r.Context(), p.ID, p.Nom, store.ProjetConnu{
		Description: d.Description, Depots: d.Depots, Liens: d.Liens, Notes: d.Notes,
	}); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	p, _ = s.projetDuCatalogue(r.Context(), p.ID)
	httpx.JSON(w, http.StatusOK, map[string]any{"projet": p})
}

func (s *Server) handleHQEtudierProjet(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	p, ok := s.projetDuCatalogue(r.Context(), chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "projet inconnu")
		return
	}
	if p.Etat == projetEnEtude {
		httpx.Error(w, http.StatusConflict, "l'équipe étudie déjà ce projet")
		return
	}
	var req struct {
		Precision string `json:"precision"`
	}
	_ = httpx.Decode(r, &req)
	run, err := s.lancerEtude(r.Context(), user, p, req.Precision)
	if err != nil {
		s.launchError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"tache": tacheView(run, "", nil)})
}

func (s *Server) handleHQRepondreQuestion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projetDuCatalogue(r.Context(), chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "projet inconnu")
		return
	}
	var req struct {
		Reponse string `json:"reponse"`
	}
	if err := httpx.Decode(r, &req); err != nil || strings.TrimSpace(req.Reponse) == "" {
		httpx.Error(w, http.StatusBadRequest, "écris ta réponse")
		return
	}
	updated, err := s.store.RepondreQuestion(r.Context(), p.ID, chi.URLParam(r, "qid"), req.Reponse)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "question introuvable")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "enregistrement impossible")
		return
	}
	if err := s.reponsesEnFiche(r.Context(), updated); err != nil {
		slog.Warn("réponses de Mathias non rangées en fiche", "projet", p.ID, "err", err)
	}
	p, _ = s.projetDuCatalogue(r.Context(), p.ID)
	httpx.JSON(w, http.StatusOK, map[string]any{"projet": p})
}

func (s *Server) handleHQOublierProjet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projetDuCatalogue(r.Context(), chi.URLParam(r, "id"))
	if !ok {
		httpx.Error(w, http.StatusNotFound, "projet inconnu")
		return
	}
	if p.Origine == "registre" {
		httpx.Error(w, http.StatusConflict, "ce projet vient du registre du VPS : il se retire là-bas")
		return
	}
	if err := s.store.SupprimerProjetConnu(r.Context(), p.ID); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "suppression impossible")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"oublie": p.ID})
}

// --- Les outils des agents dans l'atelier, prêtés par le serveur MCP -----------

// outilsProjets : lire et enrichir le savoir de l'équipe. Le responsable a en
// plus de quoi ajouter un projet qu'on lui donne en conversation.
func (s *Server) outilsProjets(moi equipe.Membre) []assistant.ToolSpec {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	obj := func(props map[string]any, required ...string) map[string]any {
		req := make([]any, 0, len(required))
		for _, r := range required {
			req = append(req, r)
		}
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	tools := []assistant.ToolSpec{
		{
			Name:        "projets_connus",
			Description: "Les projets que connaît l'équipe : identifiant, description, dépôts, fiches écrites, questions en attente de Mathias.",
			Schema:      obj(map[string]any{}),
		},
		{
			Name:        "fiches_projet",
			Description: "Lit ce que l'équipe a rangé sur un projet : ses fiches, en entier. Par projet, par mots, ou les deux.",
			Schema: obj(map[string]any{
				"projet":    str("L'identifiant du projet, ou « general » pour ce qui les traverse tous. Facultatif."),
				"recherche": str("Mots à retrouver (ex. « connexion », « déploiement »). Facultatif."),
			}),
		},
		{
			Name: "noter_fiche",
			Description: "Range durablement ce que tu as compris d'un projet, pour toute l'équipe. Une fiche par sujet : " +
				"réécrire un sujet existant remplace la fiche — écris-la donc EN ENTIER, l'ancien savoir plus le nouveau.",
			Schema: obj(map[string]any{
				"projet":  str("L'identifiant du projet, ou « general »."),
				"sujet":   str("L'étiquette de la fiche, quelques mots (« Vue d'ensemble », « Architecture », « Déploiement »). Reprends celle d'une fiche existante pour la compléter."),
				"contenu": str("Ce qu'il y a à savoir, en phrases complètes et précises : mécanismes, fichiers, réglages, décisions. Pas de chiffres du moment, aucun secret."),
			}, "projet", "sujet", "contenu"),
		},
		{
			Name: "poser_question",
			Description: "Pose une question à Mathias sur un projet, quand un point compte et ne se devine ni du code ni des fiches. " +
				"Elle l'attend dans Cerveau HQ ; sa réponse rejoindra les fiches du projet. N'attends pas la réponse : avance avec l'hypothèse la plus prudente.",
			Schema: obj(map[string]any{
				"projet":   str("L'identifiant du projet."),
				"question": str("Une seule question, précise, qu'on comprend sans contexte."),
			}, "projet", "question"),
		},
	}
	if moi.Pilote {
		tools = append(tools, assistant.ToolSpec{
			Name: "ajouter_projet",
			Description: "Ajoute au catalogue un projet que Mathias te donne en conversation, avec ce qu'il en dit et ses dépôts. " +
				"Ses dépôts seront clonés pour les tâches suivantes : confie ensuite son étude à l'équipe.",
			Schema: obj(map[string]any{
				"nom":         str("Le nom du projet."),
				"description": str("À quoi il sert, en une ou deux phrases, d'après Mathias."),
				"depots":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "« propriétaire/dépôt » pour GitHub, ou l'adresse git complète."},
				"liens":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Adresses utiles (site, documentation)."},
				"notes":       str("Le reste de ce qu'il en a dit."),
			}, "nom"),
		})
	}
	return tools
}

// estOutilProjets dit si un outil MCP est un de ceux-là.
func estOutilProjets(name string) bool {
	switch name {
	case "projets_connus", "fiches_projet", "noter_fiche", "poser_question", "ajouter_projet":
		return true
	}
	return false
}

// appelOutilProjets exécute un outil des projets pour l'agent qui travaille.
func (s *Server) appelOutilProjets(ctx context.Context, moi equipe.Membre, name, args string) (string, error) {
	var in struct {
		Projet    string `json:"projet"`
		Recherche string `json:"recherche"`
		Sujet     string `json:"sujet"`
		Contenu   string `json:"contenu"`
		Question  string `json:"question"`
		projetDonne
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("arguments illisibles : %w", err)
	}
	connu := func(id string) (projetVue, error) {
		id = strings.ToLower(strings.TrimSpace(id))
		if p, ok := s.projetDuCatalogue(ctx, id); ok {
			return p, nil
		}
		if id == "general" {
			return projetVue{ID: "general", Nom: "Général"}, nil
		}
		return projetVue{}, fmt.Errorf("projet inconnu : %q — appelle projets_connus pour les identifiants", id)
	}
	switch name {
	case "projets_connus":
		list, err := s.catalogue(ctx)
		if err != nil {
			return "", err
		}
		type ouverte struct{ Question, Auteur string }
		type vue struct {
			ID          string    `json:"id"`
			Nom         string    `json:"nom"`
			Description string    `json:"description,omitempty"`
			Depots      []string  `json:"depots,omitempty"`
			Fiches      []string  `json:"fiches"`
			Ouvertes    []ouverte `json:"questions_en_attente,omitempty"`
			Reponses    int       `json:"reponses_de_mathias,omitempty"`
			Etat        string    `json:"etat"`
		}
		out := make([]vue, 0, len(list))
		for _, p := range list {
			v := vue{ID: p.ID, Nom: p.Nom, Description: p.Description, Depots: p.Depots, Fiches: p.Sujets, Etat: p.Etat}
			for _, q := range p.Questions {
				if q.Repondue == nil {
					v.Ouvertes = append(v.Ouvertes, ouverte{q.Question, q.Auteur})
				} else {
					v.Reponses++
				}
			}
			out = append(out, v)
		}
		raw, _ := json.Marshal(out)
		return string(raw), nil

	case "fiches_projet":
		projet := ""
		if strings.TrimSpace(in.Projet) != "" {
			p, err := connu(in.Projet)
			if err != nil {
				return "", err
			}
			projet = p.ID
		}
		fiches, err := s.store.Connaissances(ctx, store.ConnaissanceQuery{
			Equipe: equipe.EquipeInformatique, Projet: projet, Recherche: in.Recherche, Limit: 12,
		})
		if err != nil {
			return "", err
		}
		if len(fiches) == 0 {
			return "Aucune fiche ne correspond.", nil
		}
		type vue struct {
			Projet  string `json:"projet"`
			Sujet   string `json:"sujet"`
			Contenu string `json:"contenu"`
			Auteur  string `json:"auteur,omitempty"`
			Maj     string `json:"maj"`
		}
		out := make([]vue, 0, len(fiches))
		for _, f := range fiches {
			out = append(out, vue{f.Projet, f.Sujet, f.Contenu, f.Auteur, f.UpdatedAt.Format("02/01/2006")})
		}
		raw, _ := json.Marshal(out)
		return string(raw), nil

	case "noter_fiche":
		p, err := connu(in.Projet)
		if err != nil {
			return "", err
		}
		saved, err := s.store.Noter(ctx, store.Connaissance{
			Equipe: equipe.EquipeInformatique, Projet: p.ID, Sujet: in.Sujet, Contenu: in.Contenu,
			Origine: store.OrigineEquipe, Auteur: moi.ID,
		})
		if err != nil {
			return "", err
		}
		return "Noté : " + saved.Projet + " · " + saved.Sujet, nil

	case "poser_question":
		p, err := connu(in.Projet)
		if err != nil {
			return "", err
		}
		if p.ID == "general" {
			return "", errors.New("une question porte sur un projet précis")
		}
		if len([]rune(in.Question)) > 1000 {
			return "", errors.New("question trop longue : une seule question, précise")
		}
		if p.Ouvertes >= 10 {
			return "", errors.New("dix questions attendent déjà Mathias sur ce projet : avance avec ce que tu as")
		}
		_, nouvelle, err := s.store.PoserQuestion(ctx, p.ID, p.Nom, moi.Nom, in.Question)
		if err != nil {
			return "", err
		}
		if !nouvelle {
			return "Cette question attend déjà Mathias.", nil
		}
		return "Question posée à Mathias ; elle l'attend dans HQ.", nil

	case "ajouter_projet":
		if !moi.Pilote {
			return "", errors.New("seul le responsable de l'équipe ajoute un projet")
		}
		created, err := s.ajouterProjet(ctx, in.projetDonne)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Projet ajouté : %s (identifiant %q). Ses dépôts seront clonés pour les tâches que tu confies maintenant.", created.Nom, created.Slug), nil
	}
	return "", fmt.Errorf("outil inconnu : %s", name)
}
