package equipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/projets"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Les outils de l'équipe. Tous lisent ; aucun n'écrit ailleurs que dans la
// mémoire de l'équipe. Comme pour Raoul, un outil dont la source n'est pas
// configurée n'existe pas : sans sonde, pas de journaux — plutôt qu'un outil
// qui échoue et un ingénieur qui raconte l'échec.

var toolLabels = map[string]string{
	"etat_projets":          "Point des projets",
	"verifier_url":          "Appel du site",
	"vps_systeme":           "État du VPS",
	"vps_conteneurs":        "Conteneurs",
	"vps_consommation":      "Consommation",
	"vps_sites":             "Sites nginx",
	"journaux":              "Journaux",
	"base_etat":             "Base de données",
	"base_expliquer":        "Plan de requête",
	"github_depot":          "GitHub",
	"code_arborescence":     "Code",
	"code_lire":             "Lecture du code",
	"code_chercher":         "Recherche dans le code",
	"relire_notes":          "Mémoire de l'équipe",
	"noter":                 "Mémoire de l'équipe",
	"oublier_note":          "Mémoire de l'équipe",
	"confier_a_l_ingenieur": "Enquête de l'ingénieur",
	"rendre_la_parole":      "Passage de parole",
}

// Outil : un outil d'un membre, tel que le bâtiment l'affiche.
type Outil struct {
	Nom     string `json:"nom"`
	Libelle string `json:"libelle"`
}

// Outils rend les outils réellement disponibles pour un membre, avec ce que le
// serveur sait interroger en ce moment — ceux qu'il a en main, pas une liste
// de principe.
func (e *Equipe) Outils(m Membre) []Outil {
	if m.ID == IDRaoul {
		return nil
	}
	raw, err := json.Marshal(e.toolsFor(m, e.capacites(), false))
	if err != nil {
		return nil
	}
	var defs []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &defs)
	out := make([]Outil, 0, len(defs))
	for _, d := range defs {
		if d.Name != "" {
			out = append(out, Outil{Nom: d.Name, Libelle: labelOf(d.Name)})
		}
	}
	return out
}

// Connecteur : une source que l'équipe interroge, et si elle répond.
type Connecteur struct {
	Nom    string `json:"nom"`
	Actif  bool   `json:"actif"`
	Detail string `json:"detail"`
}

// Connecteurs rend les sources de l'équipe et leur état.
func (e *Equipe) Connecteurs() []Connecteur {
	c := e.capacites()
	return []Connecteur{
		{Nom: "Registre des projets", Actif: c.projets, Detail: "ce qui tourne, ce qui est déployé, la CI"},
		{Nom: "Sonde du VPS", Actif: c.sonde, Detail: "système, conteneurs, journaux, bases MongoDB"},
		{Nom: "GitHub", Actif: c.github, Detail: "commits, CI, code des dépôts"},
		{Nom: "Mémoire de l'équipe", Actif: e.memoire != nil, Detail: "les fiches que l'équipe a écrites"},
	}
}

func labelOf(tool string) string {
	if l, ok := toolLabels[tool]; ok {
		return l
	}
	return tool
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func enum(values []string, desc string) map[string]any {
	if len(values) == 0 {
		return str(desc)
	}
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

func object(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func tool(name, description string, parameters map[string]any) responses.ToolUnionParam {
	t := responses.ToolParamOfFunction(name, parameters, false)
	t.OfFunction.Description = openai.String(description)
	return t
}

// toolsFor rend les outils d'un membre. Sans passage, il n'a pas de quoi
// rendre la parole : c'est lui qui répond.
func (e *Equipe) toolsFor(m Membre, caps capacites, sansPassage bool) []responses.ToolUnionParam {
	var depots, bases, projectIDs []string
	if e.projets != nil {
		depots = e.projets.Registre.Depots()
		for _, b := range e.projets.Registre.Bases {
			bases = append(bases, b.ID)
		}
		for _, p := range e.projets.Registre.Projets {
			projectIDs = append(projectIDs, p.ID)
		}
	}
	projectIDs = append(projectIDs, "general")

	var tools []responses.ToolUnionParam
	if caps.projets {
		tools = append(tools, tool(
			"etat_projets",
			"Le point d'ensemble sur ses projets perso : chaque composant (back, site, app), s'il répond et en combien de temps, son conteneur, la version déployée comparée au dernier commit, la dernière CI, les bases MongoDB, l'état du VPS et les alertes calculées. C'est le premier regard sur tout problème.",
			object(map[string]any{
				"rafraichir": map[string]any{"type": "boolean", "description": "Refaire les relevés au lieu de prendre ceux de moins d'une minute."},
			}),
		))
	}
	if m.ID == IDIngenieur && caps.projets {
		tools = append(tools, tool(
			"verifier_url",
			"Appelle une adresse de ses projets en GET et décompose le temps : DNS, connexion, TLS, attente du premier octet (le temps de l'application), total ; avec le statut, le certificat et le début de la réponse. Seuls ses domaines sont autorisés. Appelle-le plusieurs fois pour distinguer une lenteur constante d'un pic.",
			object(map[string]any{"url": str("L'adresse complète, https://… (un point de santé, une page, une route d'API en GET)")}, "url"),
		))
	}
	if m.ID == IDIngenieur && caps.sonde {
		tools = append(tools,
			tool("vps_systeme", "L'état du VPS : système et fin de support, noyau, depuis quand il tourne, charge, mémoire, disque, mises à jour en attente (dont sécurité), redémarrage requis, version de Docker.", object(map[string]any{})),
			tool("vps_conteneurs", "Tous les conteneurs Docker du VPS, arrêtés compris : état, santé, dernier démarrage (souvent la date du dernier déploiement), redémarrages, ports publiés (et s'ils sont exposés sur Internet), commit et version de l'image quand la CI les a étiquetés.", object(map[string]any{})),
			tool("vps_consommation", "Processeur et mémoire de chaque conteneur en marche, à l'instant.", object(map[string]any{})),
			tool("vps_sites", "Les sites nginx du VPS : quels domaines, vers quels ports locaux. C'est la carte qui relie une URL au conteneur qui la sert.", object(map[string]any{})),
			tool(
				"journaux",
				"La fin des journaux d'un conteneur, horodatés. Filtre côté serveur par expression régulière (insensible à la casse) pour ne remonter que ce qui compte : « error|panic|fatal », « login|connexion », « timeout|deadline », un identifiant… Sans filtre, les dernières lignes.",
				object(map[string]any{
					"conteneur": str("Le nom du conteneur (voir vps_conteneurs ou les projets déclarés)."),
					"lignes":    integer("Nombre de lignes à rendre (défaut 80, maximum 500)."),
					"filtre":    str("Expression régulière, facultative."),
					"depuis":    str("Ne lire que depuis : une durée (« 30m », « 2h », « 24h ») ou une date RFC 3339. Facultatif."),
				}, "conteneur"),
			),
			tool(
				"base_etat",
				"Examine une base MongoDB d'un projet, sur place depuis le VPS : temps de connexion et de ping (la latence entre le VPS et la base), version, tailles, connexions si l'offre le permet, et chaque collection avec son nombre de documents et ses index.",
				object(map[string]any{"base": enum(bases, "L'identifiant de la base.")}, "base"),
			),
			tool(
				"base_expliquer",
				"Le plan d'exécution d'une recherche dans une collection : passe-t-elle par un index (IXSCAN) ou parcourt-elle toute la collection (COLLSCAN), combien de documents examinés pour combien rendus, en combien de temps. C'est la preuve d'un index manquant. Ne lit rien d'autre que ce plan.",
				object(map[string]any{
					"base":       enum(bases, "L'identifiant de la base."),
					"collection": str("La collection (ex. « users »)."),
					"filtre":     str("Le filtre de la recherche en JSON étendu MongoDB, tel que le code le construit (ex. {\"email\": \"x@y.fr\"}). Une valeur fictive du bon type suffit."),
				}, "base", "collection", "filtre"),
			),
		)
	}
	if caps.github {
		tools = append(tools, tool(
			"github_depot",
			"Un dépôt GitHub : sa branche de production, les derniers commits (message, auteur, date) et les dernières exécutions de CI de chaque workflow (succès, échec, en cours), avec leurs liens.",
			object(map[string]any{
				"depot":   enum(depots, "Le dépôt, « propriétaire/nom »."),
				"branche": str("Une autre branche que celle de production, facultatif."),
			}, "depot"),
		))
	}
	if m.ID == IDIngenieur && caps.code {
		tools = append(tools,
			tool(
				"code_arborescence",
				"Ce que contient un dossier d'un dépôt, sur deux niveaux (les dossiers de dépendances et de build sont écartés). Vide pour la racine.",
				object(map[string]any{
					"depot":   enum(depots, "Le dépôt."),
					"dossier": str("Le dossier, depuis la racine (ex. « handlers »). Vide pour la racine."),
					"branche": str("Facultatif : défaut, la branche par défaut."),
				}, "depot"),
			),
			tool(
				"code_lire",
				"Lit un fichier d'un dépôt, lignes numérotées (400 lignes au plus par appel : utilise de et a pour lire la suite).",
				object(map[string]any{
					"depot":   enum(depots, "Le dépôt."),
					"chemin":  str("Le chemin du fichier depuis la racine."),
					"de":      integer("Première ligne (défaut 1)."),
					"a":       integer("Dernière ligne (défaut : 400 lignes plus loin)."),
					"branche": str("Facultatif : défaut, la branche par défaut."),
				}, "depot", "chemin"),
			),
		)
		if caps.recherche {
			tools = append(tools, tool(
				"code_chercher",
				"Cherche des mots dans le code d'un dépôt (branche par défaut) et rend les fichiers qui les contiennent, avec des extraits. Pour trouver où une route est déclarée, où une fonction est appelée, où une variable d'environnement est lue.",
				object(map[string]any{
					"depot": enum(depots, "Le dépôt."),
					"mots":  str("Les mots ou l'identifiant à chercher (ex. « HandleLogin », « MONGO_URI »)."),
				}, "depot", "mots"),
			))
		}
	}

	tools = append(tools,
		tool(
			"relire_notes",
			"Cherche dans tout ce que l'équipe a noté sur ses projets — au-delà des fiches que tu as déjà sous les yeux. Par projet, par mots, ou les deux.",
			object(map[string]any{
				"projet":    enum(projectIDs, "Le projet, ou « general » pour ce qui les traverse tous. Facultatif."),
				"recherche": str("Mots du sujet (ex. « connexion », « déploiement »). Facultatif."),
			}),
		),
		tool(
			"noter",
			"Retient durablement ce que tu as compris d'un de ses projets. À appeler DE TA PROPRE INITIATIVE, dans le même tour et sans le commenter, dès que tu as appris quelque chose qui vaudra encore dans un mois. Une fiche par sujet : réécrire un sujet existant remplace la fiche — écris-la donc EN ENTIER, l'ancien savoir plus le nouveau.",
			object(map[string]any{
				"projet":  enum(projectIDs, "Le projet concerné, ou « general »."),
				"sujet":   str("L'étiquette de la fiche, quelques mots (« Connexion », « Déploiement », « Base de prod »). Réutilise celle d'une fiche existante pour la compléter ; « Vue d'ensemble » est la fiche de fond du projet."),
				"contenu": str("Ce qu'il y a à savoir, en phrases complètes et précises : mécanismes, fichiers, réglages, causes, décisions. Pas de chiffres du moment."),
			}, "projet", "sujet", "contenu"),
		),
		tool(
			"oublier_note",
			"Retire une fiche devenue fausse ou inutile.",
			object(map[string]any{
				"projet": enum(projectIDs, "Le projet de la fiche."),
				"sujet":  str("Son sujet exact."),
			}, "projet", "sujet"),
		),
	)

	if m.ID == IDResponsable {
		ing, _ := e.Membre(IDIngenieur)
		tools = append(tools, tool(
			"confier_a_l_ingenieur",
			fmt.Sprintf("Confie une enquête à %s, l'ingénieur logiciel : il fouille avec ses outils (journaux, bases, code, CI, VPS) et te rend son rapport. À appeler pour tout ce qui demande de creuser. Écris une mission claire : ce qu'on cherche, ce qu'on sait déjà, ce que tu attends en retour. Compte une à deux minutes.", ing.Nom),
			object(map[string]any{"mission": str("La mission, en phrases.")}, "mission"),
		))
	}

	var targets []string
	var who []string
	for _, c := range Collegues(e.roster, m.ID) {
		targets = append(targets, c.ID)
		who = append(who, fmt.Sprintf("« %s » : %s, %s", c.ID, c.Nom, strings.ToLower(c.Role)))
	}
	if sansPassage {
		return tools
	}
	tools = append(tools, tool(
		"rendre_la_parole",
		"Rend la parole à Raoul quand il a fini avec toi ou parle d'autre chose que ton métier, ou la passe à un collègue qu'il demande. Le tour s'arrête là : c'est l'autre qui répond. Tu ne dis rien avant. Interlocuteurs : "+strings.Join(who, " ; ")+".",
		object(map[string]any{"a": enum(targets, "Qui reprend la parole.")}, "a"),
	))
	return tools
}

// exec exécute un outil de l'équipe.
func (e *Equipe) exec(ctx context.Context, m Membre, d Demande, emit assistant.Emitter, name, args string) (assistant.ToolOutput, error) {
	var in map[string]any
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return assistant.ToolOutput{}, fmt.Errorf("arguments illisibles : %w", err)
		}
	}
	s := func(key string) string {
		v, _ := in[key].(string)
		return strings.TrimSpace(v)
	}
	n := func(key string) int {
		switch v := in[key].(type) {
		case float64:
			return int(v)
		case string:
			var i int
			_, _ = fmt.Sscanf(v, "%d", &i)
			return i
		}
		return 0
	}
	b := func(key string) bool {
		v, _ := in[key].(bool)
		return v
	}
	ok := func(v any, err error) (assistant.ToolOutput, error) {
		if err != nil {
			return assistant.ToolOutput{}, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return assistant.ToolOutput{}, err
		}
		return assistant.ToolOutput{Payload: string(raw)}, nil
	}
	p := e.projets

	switch name {
	case "etat_projets":
		if p == nil {
			return assistant.ToolOutput{}, errors.New("aucun projet n'est suivi sur ce serveur")
		}
		return ok(p.Etat(ctx, b("rafraichir")))

	case "verifier_url":
		if p == nil {
			return assistant.ToolOutput{}, errors.New("aucun projet n'est suivi sur ce serveur")
		}
		raw := s("url")
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return assistant.ToolOutput{}, errors.New("adresse invalide : il faut une URL complète en http(s)")
		}
		if !p.Registre.HoteAutorise(u.Hostname()) {
			return assistant.ToolOutput{}, fmt.Errorf("%s n'est pas un domaine de ses projets : seuls ceux-là sont autorisés", u.Hostname())
		}
		return ok(projets.Verifier(ctx, raw), nil)

	case "vps_systeme", "vps_conteneurs", "vps_consommation", "vps_sites", "journaux", "base_etat", "base_expliquer":
		if p == nil || p.Sonde == nil {
			return assistant.ToolOutput{}, projets.ErrSansSonde
		}
		switch name {
		case "vps_systeme":
			return ok(p.Sonde.Systeme(ctx))
		case "vps_conteneurs":
			return ok(p.Sonde.Conteneurs(ctx))
		case "vps_consommation":
			return ok(p.Sonde.Stats(ctx))
		case "vps_sites":
			return ok(p.Sonde.Sites(ctx))
		case "journaux":
			j, err := p.Sonde.Journaux(ctx, s("conteneur"), n("lignes"), s("filtre"), s("depuis"))
			if err == nil && len(j.Lignes) == 0 {
				return assistant.ToolOutput{Payload: fmt.Sprintf("Aucune ligne ne correspond (%d lignes parcourues). Dis-le tel quel, et élargis le filtre ou la période si ça a un sens.", j.Parcourues)}, nil
			}
			return ok(j, err)
		case "base_etat":
			return ok(p.Sonde.Mongo(ctx, s("base")))
		default:
			return ok(p.Sonde.Expliquer(ctx, s("base"), s("collection"), s("filtre")))
		}

	case "github_depot", "code_arborescence", "code_lire", "code_chercher":
		if p == nil || p.GitHub == nil {
			return assistant.ToolOutput{}, errors.New("GitHub n'est pas configuré sur ce serveur")
		}
		repo := s("depot")
		if repo == "" {
			return assistant.ToolOutput{}, errors.New("dépôt manquant")
		}
		switch name {
		case "github_depot":
			return ok(p.GitHub.Depot(ctx, repo, s("branche"), true))
		case "code_arborescence":
			entries, truncated, err := p.GitHub.Arbre(ctx, repo, s("branche"), s("dossier"))
			if err != nil {
				return assistant.ToolOutput{}, err
			}
			out := map[string]any{"depot": repo, "dossier": s("dossier"), "entrees": entries}
			if truncated {
				out["avertissement"] = "dépôt très gros : l'arborescence rendue par GitHub est incomplète"
			}
			return ok(out, nil)
		case "code_lire":
			return ok(p.GitHub.Lire(ctx, repo, s("branche"), s("chemin"), n("de"), n("a")))
		default:
			found, err := p.GitHub.Chercher(ctx, repo, s("mots"))
			if err == nil && len(found) == 0 {
				return assistant.ToolOutput{Payload: "Aucun fichier ne contient ces mots (l'index de recherche de GitHub peut avoir quelques minutes de retard sur le dernier push)."}, nil
			}
			return ok(found, err)
		}

	case "relire_notes":
		// Huit fiches au plus : chacune peut faire plusieurs milliers de
		// caractères, et une recherche trop large se resserre mieux qu'elle
		// ne se lit.
		notes, err := e.memoire.Connaissances(ctx, store.ConnaissanceQuery{
			Equipe: m.equipe(), Projet: s("projet"), Recherche: s("recherche"), Limit: 8,
		})
		if err != nil {
			return assistant.ToolOutput{}, err
		}
		if len(notes) == 0 {
			return assistant.ToolOutput{Payload: "Rien de noté là-dessus."}, nil
		}
		type view struct {
			Projet  string `json:"projet"`
			Sujet   string `json:"sujet"`
			Contenu string `json:"contenu"`
			Note    string `json:"note_le"`
		}
		out := make([]view, 0, len(notes))
		for _, c := range notes {
			out = append(out, view{c.Projet, c.Sujet, c.Contenu, c.UpdatedAt.Format("02/01/2006")})
		}
		return ok(out, nil)

	case "noter":
		saved, err := e.memoire.Noter(ctx, store.Connaissance{
			Equipe: m.equipe(), Projet: s("projet"), Sujet: s("sujet"), Contenu: s("contenu"),
			Origine: store.OrigineEquipe, Auteur: m.ID,
		})
		if err != nil {
			return assistant.ToolOutput{}, err
		}
		// Sec, comme le « retenir » de Raoul : il n'y a rien à relire à voix
		// haute dans une fiche qu'on vient d'écrire.
		return assistant.ToolOutput{Payload: "Noté : " + saved.Projet + " · " + saved.Sujet}, nil

	case "oublier_note":
		gone, err := e.memoire.OublierConnaissance(ctx, m.equipe(), s("projet"), s("sujet"))
		if err != nil {
			return assistant.ToolOutput{}, err
		}
		if !gone {
			return assistant.ToolOutput{Payload: "Aucune fiche à ce nom."}, nil
		}
		return assistant.ToolOutput{Payload: "Fiche retirée."}, nil

	case "confier_a_l_ingenieur":
		if m.ID != IDResponsable {
			return assistant.ToolOutput{}, errors.New("seule la responsable confie des missions")
		}
		return e.delegate(ctx, m, d, emit, s("mission"))

	case "rendre_la_parole":
		target := s("a")
		for _, c := range Collegues(e.roster, m.ID) {
			if c.ID == target {
				return assistant.ToolOutput{Payload: "C'est " + c.Nom + " qui reprend.", Handoff: c.ID}, nil
			}
		}
		return assistant.ToolOutput{}, fmt.Errorf("interlocuteur inconnu : %q", target)
	}
	return assistant.ToolOutput{}, fmt.Errorf("outil inconnu : %s", name)
}

// delegate fait enquêter l'ingénieur pour la responsable.
//
// Il travaille dans la même boucle que lorsqu'il parle à Mathias, avec ses
// outils, mais il ne parle pas : ses étapes remontent dans l'app, préfixées de
// son nom — on le voit travailler pour elle —, et son texte revient à la
// responsable comme résultat d'outil. C'est elle qui parle.
func (e *Equipe) delegate(ctx context.Context, from Membre, d Demande, emit assistant.Emitter, mission string) (assistant.ToolOutput, error) {
	if strings.TrimSpace(mission) == "" {
		return assistant.ToolOutput{}, errors.New("mission vide : écris ce que l'ingénieur doit chercher")
	}
	ing, _ := e.Membre(IDIngenieur)
	forward := func(ev assistant.Event) {
		if ev.Type != "status" || emit == nil {
			return
		}
		ev.Label = ing.Nom + " · " + ev.Label
		ev.Tool = IDIngenieur + ":" + ev.Tool
		emit(ev)
	}
	sub := d
	sub.Text = missionPrompt(from, mission)
	// L'enquête part sans l'historique de la conversation : la mission dit ce
	// qu'il faut savoir, et l'ingénieur ne doit pas répondre à une question
	// qu'on ne lui a pas posée.
	sub.History = nil
	var travail Travail
	if d.Suivi != nil {
		travail = d.Suivi.Debut(IDIngenieur, "delegation", mission)
		inner := forward
		forward = func(ev assistant.Event) {
			travail.Etape(ev)
			inner(ev)
		}
	}
	res, err := e.run(ctx, ing, sub, forward)
	if travail != nil {
		travail.Fin(res, err)
	}
	if err != nil {
		return assistant.ToolOutput{}, fmt.Errorf("l'enquête de %s n'a pas abouti : %w", ing.Nom, err)
	}
	if res.Handoff != "" || strings.TrimSpace(res.Reply) == "" {
		return assistant.ToolOutput{Payload: ing.Nom + " n'a pas rendu de rapport exploitable. Dis-le, et propose de lui passer la parole."}, nil
	}
	return assistant.ToolOutput{Payload: "RAPPORT DE " + strings.ToUpper(ing.Nom) + " — à restituer en responsable, pas à relire :\n\n" + res.Reply}, nil
}

// toolTimeout : la délégation prend le temps d'une enquête entière.
func toolTimeout(name string) time.Duration {
	switch name {
	case "confier_a_l_ingenieur":
		return 4 * time.Minute
	case "etat_projets":
		return 45 * time.Second
	case "base_etat", "base_expliquer", "journaux", "vps_consommation", "vps_systeme":
		return 50 * time.Second
	}
	return 0
}

func (m Membre) equipe() string {
	if m.Equipe == "" {
		return EquipeInformatique
	}
	return m.Equipe
}
