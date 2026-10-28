package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/tri"
)

// Le process CSP d'une airline, depuis le mail qui apporte son Excel.
//
// Quand l'agent mail pro range un mail de cycle dans « CSP » et qu'il porte
// un Excel, il propose à Mathias de lancer le process dans pxsky : quelle
// airline (son projet pxsky), quel cycle, avec quel fichier — et si ce
// fichier a déjà été importé. Il le déduit des imports passés de pxsky, où
// chaque airline a ses habitudes de nom (« EY_1226_PXCom » pour Etihad,
// « Metadata_Azul_2612 » pour Azul : le même cycle 2026-12, écrit à l'envers).
//
// Rien ne part sans le clic de Mathias. Au clic, l'Excel est relu dans sa
// boîte et passe à l'atelier, où pxsky-lancer-import-csp fait l'appel que
// fait l'écran « Import CSP » de pxsky. HQ suit ensuite l'import : Excel,
// import, build du cycle.

var (
	cycleCSP  = regexp.MustCompile(`^20\d\d-(0[1-9]|1[0-2])$`)
	projetCSP = regexp.MustCompile(`^[a-f0-9]{24}$`)
	excelCSP  = regexp.MustCompile(`(?i)\.xlsx?$`)
)

const (
	cspDelai        = 3 * time.Minute
	cspSuiviFrais   = 15 * time.Second
	cspImportsVus   = 6 // imports passés montrés par airline
	cspChercherJour = 10
)

// importPxsky : un import CSP de pxsky, tel que le rend son API.
type importPxsky struct {
	ID       string `json:"id"`
	Projet   string `json:"project"`
	Airline  string `json:"airline"`
	Fichier  string `json:"filename"`
	Cycle    string `json:"cycle"`
	Statut   string `json:"status"`
	Version  int    `json:"version"`
	CreeLe   string `json:"created_at"`
	Etapes   []etapeImport
	Recap    []string
	Erreur   string
	Messages []string
}

type etapeImport struct {
	Nom     string `json:"nom"`
	Statut  string `json:"statut"`
	Message string `json:"message,omitempty"`
}

// etatImport : où en est un import lancé depuis HQ.
type etatImport struct {
	Statut string        `json:"statut"`
	Etapes []etapeImport `json:"etapes"`
	Recap  []string      `json:"recap,omitempty"`
}

// scriptAtelier fait tourner un petit script dans l'atelier (le mode des
// panneaux) et rend ce qu'il imprime.
func (s *Server) scriptAtelier(ctx context.Context, nom, script string) (string, error) {
	run := fmt.Sprintf("%s%d", nom, time.Now().UnixNano())
	var err error
	var out struct {
		Code   int
		Sortie string
		Stderr string
	}
	switch {
	case s.atelier != nil:
		d, e := s.atelier.Panneau(ctx, run, script)
		out.Code, out.Sortie, out.Stderr, err = d.Code, d.Sortie, d.Stderr, e
	case s.cfg.PanneauxLocal:
		d, e := panneauLocal(ctx, run, script)
		out.Code, out.Sortie, out.Stderr, err = d.Code, d.Sortie, d.Stderr, e
	default:
		return "", errors.New("l'atelier n'est pas configuré")
	}
	if err != nil {
		return "", err
	}
	if out.Code != 0 {
		return "", fmt.Errorf("%s", clipTo(strings.TrimSpace(out.Stderr+" "+out.Sortie), 600))
	}
	var e struct {
		Erreur string `json:"erreur"`
	}
	if json.Unmarshal([]byte(out.Sortie), &e) == nil && e.Erreur != "" {
		return "", errors.New("pxsky : " + e.Erreur)
	}
	return out.Sortie, nil
}

// scriptImports : les imports CSP récents de pxsky, réduits à l'essentiel.
const scriptImports = `import json, subprocess
r = subprocess.run(["sudo", "-n", "/usr/local/bin/pxsky-lire", "contentmanager/csp-excel/jobs"], capture_output=True, text=True, timeout=45)
d = json.loads(r.stdout or "{}")
if "erreur" in d:
    print(json.dumps(d)); raise SystemExit
out = []
for j in d.get("jobs") or []:
    out.append({"id": j.get("id"), "project": j.get("project"), "airline": (j.get("config") or {}).get("name") or "",
                "filename": j.get("filename"), "cycle": j.get("cycle"), "status": j.get("status"),
                "version": j.get("version") or 0, "created_at": j.get("created_at")})
print(json.dumps(out))
`

func (s *Server) importsPxsky(ctx context.Context) ([]importPxsky, error) {
	out, err := s.scriptAtelier(ctx, "imports", scriptImports)
	if err != nil {
		return nil, err
	}
	var list []importPxsky
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("imports pxsky illisibles : %w", err)
	}
	return list, nil
}

// -- Proposer ----------------------------------------------------------------------

var propositionCSPSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"lancer": map[string]any{"type": "boolean"},
		"projet": map[string]any{"type": "string"},
		"cycle":  map[string]any{"type": "string"},
		"raison": map[string]any{"type": "string"},
	},
	"required":             []string{"lancer", "projet", "cycle", "raison"},
	"additionalProperties": false,
}

// consigneCSP : ce que le modèle sait pour reconnaître airline et cycle.
func consigneCSP(imports []importPxsky, maintenant time.Time) string {
	type airline struct {
		nom, projet string
		vus         []importPxsky
	}
	par := map[string]*airline{}
	var ordre []string
	for _, j := range imports {
		if j.Projet == "" {
			continue
		}
		a, ok := par[j.Projet]
		if !ok {
			a = &airline{nom: j.Airline, projet: j.Projet}
			par[j.Projet] = a
			ordre = append(ordre, j.Projet)
		}
		if len(a.vus) < cspImportsVus {
			a.vus = append(a.vus, j)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `Tu prépares le lancement du process CSP d'une airline dans pxsky, l'outil de PxCom (divertissement à bord des avions).
Un CSP (fournisseur de contenu) envoie par mail l'Excel des métadonnées d'un cycle ; on l'importe dans pxsky pour l'airline et le cycle.
Nous sommes le %s.

Les airlines connues de pxsky, avec leur identifiant de projet et leurs derniers imports (fichier → cycle) :
`, maintenant.Format("02/01/2006"))
	for _, id := range ordre {
		a := par[id]
		fmt.Fprintf(&b, "- %s (projet %s) :", a.nom, a.projet)
		for _, j := range a.vus {
			fmt.Fprintf(&b, " %s → %s ;", j.Fichier, j.Cycle)
		}
		b.WriteString("\n")
	}
	b.WriteString(`
Règles :
- lancer = true seulement si la pièce jointe est l'Excel des métadonnées d'un cycle (comme les fichiers ci-dessus) d'une airline de la liste. Une facture, un planning, un rapport, une autre airline : lancer = false.
- projet : l'identifiant exact de l'airline, recopié de la liste.
- cycle : AAAA-MM. Le code à quatre chiffres d'un nom de fichier change d'ordre selon l'airline (« EY_1226 » = 2026-12, « Metadata_Azul_2612 » = 2026-12) : déduis-le comme les imports passés de la même airline, et vérifie-le avec l'objet et le texte du mail.
- raison : une phrase courte, en français, qui dit d'où viennent l'airline et le cycle.
Si lancer = false, projet et cycle sont vides et raison dit pourquoi.`)
	return b.String()
}

type mailCSP struct {
	MessageID, Objet, De, Texte string
	Recu                        time.Time
	Pieces                      []string
}

// proposerCSP regarde les mails de cycle rangés dans « CSP » et propose un
// process pour chaque Excel. imports peut être nil : il est alors relu.
func (s *Server) proposerCSP(ctx context.Context, user *store.User, mails []mailCSP) (int, error) {
	if s.direct == nil {
		return 0, errors.New("pas de clé Claude : les process CSP ne sont pas proposés")
	}
	var aVoir []mailCSP
	for _, m := range mails {
		for _, f := range m.Pieces {
			if excelCSP.MatchString(f) && m.MessageID != "" && !s.store.PropositionCSPExiste(ctx, user.ID, m.MessageID, f) {
				aVoir = append(aVoir, mailCSP{MessageID: m.MessageID, Objet: m.Objet, De: m.De, Texte: m.Texte, Recu: m.Recu, Pieces: []string{f}})
			}
		}
	}
	if len(aVoir) == 0 {
		return 0, nil
	}
	imports, err := s.importsPxsky(ctx)
	if err != nil {
		return 0, err
	}
	consigne := consigneCSP(imports, time.Now().In(s.location(user)))
	connus := map[string]string{}
	for _, j := range imports {
		connus[j.Projet] = j.Airline
	}
	n := 0
	for _, m := range aVoir {
		f := m.Pieces[0]
		var rep struct {
			Lancer bool   `json:"lancer"`
			Projet string `json:"projet"`
			Cycle  string `json:"cycle"`
			Raison string `json:"raison"`
		}
		entree := fmt.Sprintf("Mail de %s, reçu le %s\nObjet : %s\nPièce jointe : %s\n\n%s",
			m.De, m.Recu.Format("02/01/2006"), m.Objet, f, clipTo(m.Texte, 2000))
		if err := s.direct.jsonOutil(ctx, consigne, entree, propositionCSPSchema, &rep); err != nil {
			slog.Warn("csp : proposition non jugée", "fichier", f, "err", err)
			continue
		}
		if !rep.Lancer {
			slog.Info("csp : pas de process pour cette pièce jointe", "fichier", f, "raison", rep.Raison)
			continue
		}
		airline, ok := connus[rep.Projet]
		if !ok || !cycleCSP.MatchString(rep.Cycle) {
			slog.Warn("csp : proposition incohérente écartée", "fichier", f, "projet", rep.Projet, "cycle", rep.Cycle)
			continue
		}
		p := store.PropositionCSP{
			UserID: user.ID, MessageID: m.MessageID, Objet: m.Objet, De: m.De, Recu: m.Recu, Fichier: f,
			Airline: airline, Projet: rep.Projet, Cycle: rep.Cycle, Raison: clipTo(rep.Raison, 300),
		}
		p.Deja = dejaImporte(imports, p.Projet, p.Cycle, f)
		if _, nouvelle, err := s.store.ProposerCSP(ctx, p); err != nil {
			slog.Warn("csp : proposition non enregistrée", "err", err)
		} else if nouvelle {
			n++
			slog.Info("csp : process proposé", "airline", airline, "cycle", p.Cycle, "fichier", f)
		}
	}
	return n, nil
}

// dejaImporte : le dernier import de ce fichier pour ce projet et ce cycle.
func dejaImporte(imports []importPxsky, projet, cycle, fichier string) *store.ImportCSP {
	for _, j := range imports { // les plus récents d'abord
		if j.Projet == projet && j.Cycle == cycle && strings.EqualFold(strings.TrimSpace(j.Fichier), strings.TrimSpace(fichier)) {
			le, _ := time.Parse(time.RFC3339, j.CreeLe)
			return &store.ImportCSP{Job: j.ID, Statut: j.Statut, Le: le, Version: j.Version}
		}
	}
	return nil
}

// mailsDuTri : les mails que le tri vient de ranger dans « CSP ».
func mailsDuTri(arrivees []gandi.Arrivee, decisions []tri.Decision) []mailCSP {
	var out []mailCSP
	for i, d := range decisions {
		if d.Dossier != tri.CSP || i >= len(arrivees) {
			continue
		}
		a := arrivees[i]
		out = append(out, mailCSP{MessageID: a.MessageID, Objet: a.Subject, De: a.From, Texte: a.Body, Recu: a.Date, Pieces: a.PiecesJointes})
	}
	return out
}

// proposerApresTri : en arrière-plan, après le rangement — le tri ne
// l'attend pas.
func (s *Server) proposerApresTri(user *store.User, mails []mailCSP) {
	if len(mails) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cspDelai)
		defer cancel()
		if _, err := s.proposerCSP(ctx, user, mails); err != nil {
			slog.Warn("csp : propositions après le tri", "err", err)
		}
	}()
}

// jsonOutil : une réponse JSON du modèle rapide, selon un schéma.
func (p *piloteDirect) jsonOutil(ctx context.Context, system, entree string, schema map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(p.modele),
		MaxTokens: 1024,
		System:    []anthropic.TextBlockParam{{Text: system}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortMedium,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(entree))},
	})
	if err != nil {
		return err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return errors.New("refus du modèle")
	}
	var texte strings.Builder
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			texte.WriteString(tb.Text)
		}
	}
	return json.Unmarshal([]byte(texte.String()), out)
}

// -- Suivre ------------------------------------------------------------------------

type suiviCSP struct {
	mu   sync.Mutex
	at   time.Time
	cle  string
	etat map[string]etatImport
}

// etatsImports : où en sont ces imports dans pxsky, relu au plus toutes les
// quinze secondes.
func (s *Server) etatsImports(ctx context.Context, jobs []string) map[string]etatImport {
	sort.Strings(jobs)
	cle := strings.Join(jobs, ",")
	s.cspSuivi.mu.Lock()
	defer s.cspSuivi.mu.Unlock()
	if cle == s.cspSuivi.cle && time.Since(s.cspSuivi.at) < cspSuiviFrais {
		return s.cspSuivi.etat
	}
	var ids []string
	for _, j := range jobs {
		if projetCSP.MatchString(j) {
			ids = append(ids, `"`+j+`"`)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	script := `import json, subprocess
out = {}
for i in [` + strings.Join(ids, ",") + `]:
    r = subprocess.run(["sudo", "-n", "/usr/local/bin/pxsky-lire", "contentmanager/csp-excel/jobs/" + i], capture_output=True, text=True, timeout=25)
    try:
        j = json.loads(r.stdout)
    except ValueError:
        continue
    j = j.get("job", j) if isinstance(j, dict) else {}
    steps = j.get("steps") or {}
    etapes = sorted(({"nom": k, "statut": (v or {}).get("status") or "", "message": (v or {}).get("message") or "", "debut": (v or {}).get("started_at") or "~"} for k, v in steps.items()), key=lambda e: e["debut"])
    for e in etapes: e.pop("debut")
    out[i] = {"statut": j.get("status") or "", "etapes": etapes, "recap": [str(x) for x in (j.get("recap") or [])][:5]}
print(json.dumps(out))
`
	raw, err := s.scriptAtelier(ctx, "suivicsp", script)
	if err != nil {
		slog.Warn("csp : suivi des imports", "err", err)
		return s.cspSuivi.etat
	}
	var etat map[string]etatImport
	if json.Unmarshal([]byte(raw), &etat) != nil {
		return s.cspSuivi.etat
	}
	s.cspSuivi.at, s.cspSuivi.cle, s.cspSuivi.etat = time.Now(), cle, etat
	return etat
}

// -- L'API de HQ -------------------------------------------------------------------

type propositionVue struct {
	store.PropositionCSP
	Etat *etatImport `json:"etat,omitempty"`
}

func (s *Server) handleHQPropositionsCSP(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	list, err := s.store.PropositionsCSP(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des process CSP impossible")
		return
	}
	var jobs []string
	for _, p := range list {
		// On suit un import une journée : au-delà, il est fini depuis longtemps.
		if p.Job != "" && p.LanceeLe != nil && time.Since(*p.LanceeLe) < 24*time.Hour {
			jobs = append(jobs, p.Job)
		}
	}
	etats := map[string]etatImport{}
	if len(jobs) > 0 {
		etats = s.etatsImports(r.Context(), jobs)
	}
	out := make([]propositionVue, 0, len(list))
	for _, p := range list {
		v := propositionVue{PropositionCSP: p}
		if e, ok := etats[p.Job]; ok {
			v.Etat = &e
		}
		out = append(out, v)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"propositions": out})
}

func (s *Server) propositionDeLaRequete(w http.ResponseWriter, r *http.Request) (store.PropositionCSP, bool) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return store.PropositionCSP{}, false
	}
	p, err := s.store.PropositionCSP(r.Context(), userFrom(r.Context()).ID, id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "proposition introuvable")
		return store.PropositionCSP{}, false
	}
	return p, true
}

// handleHQLancerCSP : Mathias a cliqué « Lancer ». Le cycle peut avoir été
// corrigé ; le mode « check » ne fait que vérifier l'Excel.
func (s *Server) handleHQLancerCSP(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	p, ok := s.propositionDeLaRequete(w, r)
	if !ok {
		return
	}
	var req struct {
		Cycle string `json:"cycle"`
		Mode  string `json:"mode"`
	}
	_ = httpx.Decode(r, &req)
	cycle := strings.TrimSpace(req.Cycle)
	if cycle == "" {
		cycle = p.Cycle
	}
	if !cycleCSP.MatchString(cycle) {
		httpx.Error(w, http.StatusBadRequest, "cycle attendu au format AAAA-MM (ex. 2026-12)")
		return
	}
	mode := req.Mode
	if mode != "check" {
		mode = "full"
	}
	if s.atelier == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "l'atelier n'est pas configuré : pxsky est hors de portée")
		return
	}
	// Prise de la proposition : un second clic trouve « en cours » et s'arrête.
	p, err := s.store.ModifierPropositionCSP(r.Context(), user.ID, p.ID, bson.M{"statut": store.CSPLancement, "cycle": cycle, "mode": mode, "erreur": ""},
		store.CSPProposee, store.CSPErreur)
	if err != nil {
		httpx.Error(w, http.StatusConflict, "ce process est déjà lancé")
		return
	}
	echec := func(status int, msg string) {
		_, _ = s.store.ModifierPropositionCSP(context.WithoutCancel(r.Context()), user.ID, p.ID, bson.M{"statut": store.CSPErreur, "erreur": msg})
		httpx.Error(w, status, msg)
	}
	ctx, cancel := context.WithTimeout(r.Context(), cspDelai)
	defer cancel()
	creds, err := s.gandiCreds(ctx, user)
	if err != nil {
		echec(http.StatusServiceUnavailable, "boîte pro inaccessible : "+err.Error())
		return
	}
	excel, err := gandi.PieceJointe(ctx, creds, p.MessageID, p.Fichier)
	if err != nil {
		echec(http.StatusBadGateway, "l'Excel n'a pas pu être relu dans ta boîte : "+err.Error())
		return
	}
	d, err := s.atelier.ImportCSP(ctx, fmt.Sprintf("csp%s%d", p.ID.Hex(), time.Now().Unix()), p.Fichier, p.Projet, cycle, mode, excel)
	if err != nil {
		echec(http.StatusBadGateway, err.Error())
		return
	}
	var rep struct {
		JobID  string `json:"jobId"`
		Erreur string `json:"erreur"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(d.Sortie)), &rep)
	if d.Code != 0 || rep.JobID == "" {
		msg := rep.Erreur
		if msg == "" {
			msg = clipTo(strings.TrimSpace(d.Stderr+" "+d.Sortie), 600)
		}
		echec(http.StatusBadGateway, "pxsky a refusé l'import : "+msg)
		return
	}
	now := time.Now()
	p, err = s.store.ModifierPropositionCSP(context.WithoutCancel(r.Context()), user.ID, p.ID, bson.M{"statut": store.CSPLancee, "job": rep.JobID, "lancee_le": now})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "import lancé (job "+rep.JobID+") mais non noté")
		return
	}
	slog.Info("csp : process lancé", "airline", p.Airline, "cycle", cycle, "fichier", p.Fichier, "mode", mode, "job", rep.JobID)
	httpx.JSON(w, http.StatusAccepted, map[string]any{"proposition": propositionVue{PropositionCSP: p}})
}

func (s *Server) handleHQIgnorerCSP(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	p, ok := s.propositionDeLaRequete(w, r)
	if !ok {
		return
	}
	p, err := s.store.ModifierPropositionCSP(r.Context(), user.ID, p.ID, bson.M{"statut": store.CSPIgnoree}, store.CSPProposee, store.CSPErreur)
	if err != nil {
		httpx.Error(w, http.StatusConflict, "ce process n'est plus à décider")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"proposition": propositionVue{PropositionCSP: p}})
}

// handleHQChercherCSP : relire les mails des derniers jours du dossier CSP,
// pour les Excel arrivés avant que l'équipe sache les proposer.
func (s *Server) handleHQChercherCSP(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), cspDelai)
	defer cancel()
	creds, err := s.gandiCreds(ctx, user)
	if err != nil {
		httpx.Error(w, http.StatusServiceUnavailable, "boîte pro inaccessible")
		return
	}
	recus, err := gandi.AvecPieces(ctx, creds, tri.CSP, time.Now().AddDate(0, 0, -cspChercherJour), 30)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	var mails []mailCSP
	for _, a := range recus {
		mails = append(mails, mailCSP{MessageID: a.MessageID, Objet: a.Subject, De: a.From, Texte: a.Body, Recu: a.Date, Pieces: a.PiecesJointes})
	}
	n, err := s.proposerCSP(ctx, user, mails)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"nouvelles": n, "mails": len(mails)})
}
