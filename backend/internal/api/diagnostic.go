package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// Le diagnostic : de la veille à Hugo.
//
// Un agent de veille lit un message qui dit que quelque chose ne va pas. Si —
// et seulement si — c'est à Mathias qu'on s'adresse, il le passe à Hugo, qui
// s'assure d'être sur le bon problème, fait enquêter l'équipe, et rend sur le
// sujet son diagnostic : le problème, la cause, ce qu'il faut faire pour
// corriger (une commande, un process à revoir), et ce qui l'a laissé passer.
//
// Le filtre est la partie difficile, et il est voulu sévère. Mathias est
// destinataire de beaucoup de mails où l'on demande à quelqu'un d'autre de
// régler un problème : mobiliser l'équipe pour ceux-là, c'est du bruit et de
// la facture. Trois verrous se suivent, et chacun peut dire non :
//
//  1. la veille dit à qui l'on demande, et recopie la phrase qui le montre ;
//  2. le serveur vérifie que cette phrase est bien dans le message, écrite
//     par quelqu'un d'autre que Mathias — un modèle qui invente sa preuve est
//     arrêté ici, sans modèle ;
//  3. Hugo relit le message entier, avec pour seule question « est-ce bien à
//     Mathias qu'on parle ? », et la consigne de dire non dans le doute.
//
// Un problème écarté n'est pas perdu : il reste sur le sujet, avec la raison,
// et un bouton pour demander quand même le diagnostic. Se tromper dans ce
// sens-là coûte un clic ; dans l'autre, une enquête pour rien.

const (
	// diagnosticsJourDefaut : enquêtes lancées d'elles-mêmes par jour.
	diagnosticsJourDefaut = 5
	// diagnosticDuree : ce que dure au plus une enquête de Hugo.
	diagnosticDuree = 45 * time.Minute
	// controleDuree : ce que dure au plus le contrôle d'une remontée.
	controleDuree = 2 * time.Minute
	// diagnosticMessages : ce que l'enquête reçoit des messages, en signes.
	diagnosticMessages = 7000
)

// problemeJuge : ce que la veille dit d'un problème, dans son jugement.
type problemeJuge struct {
	Quoi   string `json:"quoi"`
	Pour   string `json:"pour"` // mathias, autre, incertain
	AQui   string `json:"a_qui"`
	Preuve string `json:"preuve"`
}

// UnmarshalJSON tolère ce qu'un modèle met parfois à la place de l'objet —
// une simple phrase, ou rien. Un problème mal formé ne doit jamais faire
// perdre le jugement entier de la relève.
func (p *problemeJuge) UnmarshalJSON(b []byte) error {
	type brut problemeJuge
	var v brut
	if err := json.Unmarshal(b, &v); err == nil {
		*p = problemeJuge(v)
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		// Sans « pour », personne n'est désigné : ce sera « incertain ».
		p.Quoi = s
	}
	return nil
}

// remontee : un problème que la veille vient de voir dans un sujet.
type remontee struct {
	sujet bson.ObjectID
	n     store.Notification
	p     problemeJuge
	// items : les messages des autres dans ce sujet (pas ceux de Mathias).
	items []triage.Item
	// deja : ce que le sujet portait déjà comme diagnostic.
	deja *store.Diagnostic
}

// remonter passe au contrôle les problèmes d'une relève, hors de celle-ci :
// la veille n'attend pas Hugo pour rendre la main.
func (s *Server) remonter(user *store.User, agent equipe.Membre, source string, rs []remontee) {
	if len(rs) == 0 {
		return
	}
	go func() {
		for _, r := range rs {
			ctx, cancel := context.WithTimeout(context.Background(), controleDuree)
			s.remonterUn(ctx, user, agent, source, r)
			cancel()
		}
	}()
}

func (s *Server) remonterUn(ctx context.Context, user *store.User, agent equipe.Membre, source string, r remontee) {
	// Une enquête par sujet : celle qui tourne ou qui a abouti n'est pas
	// refaite parce qu'un nouveau message redit le problème.
	if r.deja != nil && (r.deja.Etat == store.DiagnosticEnCours || r.deja.Etat == store.DiagnosticPret) {
		return
	}
	v := s.trancher(ctx, source, r, s.cfg.DiagnosticAuto)
	ecarter := func(raison string) {
		d := store.Diagnostic{Etat: store.DiagnosticEcarte, AQui: v.AQui, Preuve: v.Preuve, Raison: raison}
		if err := s.store.PoserDiagnostic(ctx, r.sujet, v.Quoi, d); err != nil {
			slog.Warn("diagnostic : problème écarté non noté", "err", err)
		}
		slog.Info("diagnostic : problème non remonté", "sujet", r.n.Titre, "verrou", v.Verrou, "raison", raison)
	}
	if !v.Remonter {
		ecarter(v.Raison)
		return
	}
	// Le plafond du jour : Hugo travaille par l'API, à l'usage.
	plafond := s.cfg.DiagnosticsJour
	if plafond <= 0 {
		plafond = diagnosticsJourDefaut
	}
	if n, err := s.store.DiagnosticsDepuis(ctx, user.ID, time.Now().Add(-24*time.Hour)); err == nil && int(n) >= plafond {
		ecarter(fmt.Sprintf("C'est bien pour toi, mais %d diagnostics ont déjà été lancés aujourd'hui.", n))
		return
	}
	r.n.ID = r.sujet
	d := store.Diagnostic{Preuve: v.Preuve, Raison: v.Raison}
	if _, err := s.lancerDiagnostic(ctx, user, agent, r.n, v.Quoi, d, dossierMessages(r.items)); err != nil && !errors.Is(err, errDiagnosticDeja) {
		slog.Warn("diagnostic : enquête non lancée", "err", err)
	}
}

// verdict : ce que les trois verrous disent d'un problème.
type verdict struct {
	// Remonter : il s'adresse à Mathias, Hugo peut enquêter.
	Remonter bool
	// Verrou : celui qui a dit non — « veille », « preuve », « adressage »,
	// « desactive », « controle ». Vide quand tout est passé.
	Verrou string
	Quoi   string
	AQui   string
	Preuve string
	Raison string
}

// trancher fait passer un problème par les trois verrous, sans rien écrire
// ni lancer. auto : les diagnostics automatiques sont permis (sinon on
// s'arrête avant le contrôle, qui passe par l'API).
func (s *Server) trancher(ctx context.Context, source string, r remontee, auto bool) verdict {
	v := verdict{Quoi: truncateRunes(strings.TrimSpace(r.p.Quoi), 400)}
	non := func(verrou, aQui, preuve, raison string) verdict {
		v.Verrou, v.AQui, v.Preuve, v.Raison = verrou, strings.TrimSpace(aQui), preuve, raison
		return v
	}
	autres := texteDesAutres(r.items)

	// 1. Ce que la veille en dit.
	switch strings.ToLower(strings.TrimSpace(r.p.Pour)) {
	case "mathias":
	case "autre":
		preuve, _ := citation(r.p.Preuve, autres)
		raison := "On le demande à quelqu'un d'autre."
		if qui := strings.TrimSpace(r.p.AQui); qui != "" {
			raison = "On le demande à " + qui + ", pas à toi."
		}
		return non("veille", r.p.AQui, preuve, raison)
	default:
		return non("veille", "", "", "Rien dans le message n'établit que c'est à toi qu'on s'adresse.")
	}
	// 2. La phrase qui le montre doit exister, mot pour mot, chez un autre.
	preuve, ok := citation(r.p.Preuve, autres)
	if !ok {
		return non("preuve", "", "", "La veille n'a pas su citer la phrase qui s'adresse à toi.")
	}
	// Un mail où il n'est qu'en copie ou en diffusion ne passe déjà pas le
	// tri ; s'il arrivait ici, il ne remonterait pas.
	if source == "mail" && !adresseDirecte(r.items) {
		return non("adressage", "", preuve, "Tu n'es pas destinataire en titre de ce mail.")
	}
	if !auto {
		return non("desactive", "", preuve, "Les diagnostics automatiques sont désactivés.")
	}
	// 3. Hugo relit, avec une seule question.
	c, err := s.controler(ctx, r, autres)
	if err != nil {
		slog.Warn("diagnostic : contrôle en échec", "err", err)
		return non("controle", "", preuve, "Le contrôle n'a pas pu se faire ; dans le doute, rien n'a été lancé.")
	}
	if !c.PourMathias {
		raison := strings.TrimSpace(c.Raison)
		if raison == "" {
			raison = "Hugo n'a pas pu établir que c'est à toi qu'on s'adresse."
		}
		p, _ := citation(c.Preuve, autres)
		return non("controle", c.AQui, p, raison)
	}
	preuve, ok = citation(c.Preuve, autres)
	if !ok {
		return non("controle", "", "", "Hugo n'a pas su citer la phrase qui s'adresse à toi.")
	}
	if p := strings.TrimSpace(c.Probleme); p != "" {
		v.Quoi = truncateRunes(p, 400)
	}
	v.Remonter, v.Preuve, v.Raison = true, preuve, strings.TrimSpace(c.Raison)
	return v
}

// adresseDirecte : au moins un des mails nomme Mathias dans son champ « À »,
// ou répond à un message qu'il a envoyé.
func adresseDirecte(items []triage.Item) bool {
	for _, it := range items {
		switch it.Adressage {
		case triage.AdressageCopie, triage.AdressageDiffusion, triage.AdressageAbsent:
		default:
			return true
		}
	}
	return false
}

// Les marques que la veille pose dans l'aperçu d'un mail pour séparer le
// message de ce qui le précède dans le fil (veille.go, lireMailsNeufs).
var marquesDuFil = []string{"\n    plus tôt dans le fil :", "\n    il répond à ce message de"}

const prefixeToi = "toi (Mathias) :"

// texteDesAutres rend ce que les AUTRES ont écrit dans ces messages : le
// corps des nouveaux mails sans le fil qui les précède, les conversations
// sans les lignes de Mathias. C'est là, et seulement là, que doit se trouver
// la phrase qui s'adresse à lui : sa propre question, citée plus haut dans le
// fil, ne prouve pas qu'on lui parle.
func texteDesAutres(items []triage.Item) string {
	var b strings.Builder
	for _, it := range items {
		apercu := it.Apercu
		if it.Source == "mail" {
			for _, m := range marquesDuFil {
				if i := strings.Index(apercu, m); i >= 0 {
					apercu = apercu[:i]
				}
			}
			b.WriteString(apercu + "\n")
			continue
		}
		for _, ligne := range strings.Split(apercu, "\n") {
			l := strings.TrimSpace(ligne)
			if strings.HasPrefix(l, prefixeToi) || strings.HasPrefix(l, "(le dernier message est de toi") {
				continue
			}
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

var coupeCitation = regexp.MustCompile(`\[\s*(?:…|\.\.\.)\s*\]|…|\.\.\.`)

// citation vérifie qu'une phrase donnée comme preuve se trouve bien dans le
// texte — à la casse, aux accents et à la ponctuation près, et par morceaux si
// elle est abrégée (« … »). Rend la phrase nettoyée, et faux si elle n'y est
// pas ou si elle est trop courte pour prouver quoi que ce soit.
func citation(preuve, texte string) (string, bool) {
	preuve = strings.TrimSpace(strings.Trim(strings.TrimSpace(preuve), "«»\"“”'"))
	if preuve == "" {
		return "", false
	}
	hay := fuzzy.Normalize(texte)
	total := 0
	for _, part := range coupeCitation.Split(preuve, -1) {
		p := fuzzy.Normalize(part)
		if p == "" {
			continue
		}
		if !strings.Contains(hay, p) {
			return "", false
		}
		total += len(p)
	}
	if total < 10 {
		return "", false
	}
	return truncateRunes(preuve, 300), true
}

// dossierMessages : les messages tels que la veille les a lus, pour le
// contrôle et pour l'enquête.
func dossierMessages(items []triage.Item) string {
	var b strings.Builder
	for i, it := range items {
		fmt.Fprintf(&b, "[%d] %s · de %s · %s · %s\n", i+1, origin(it), it.De, it.Quand.Format("02/01/2006 15:04"), adresseA(it))
		if it.Titre != "" {
			fmt.Fprintf(&b, "    sujet : %s\n", it.Titre)
		}
		if it.Apercu != "" {
			fmt.Fprintf(&b, "    contenu : %s\n", it.Apercu)
		}
	}
	return truncateRunes(strings.TrimSpace(b.String()), diagnosticMessages)
}

// adresseA dit, pour le modèle, à qui un message est écrit. Pour un mail :
// la place de Mathias parmi les destinataires, et les autres destinataires
// quand il n'est pas seul — c'est souvent à l'un d'eux qu'on demande.
func adresseA(it triage.Item) string {
	if it.Source != "mail" || it.Adressage == "" {
		return motif(it.Motif)
	}
	out := string(it.Adressage)
	if len(it.Pour) > 1 {
		pour := it.Pour
		if len(pour) > 8 {
			pour = pour[:8]
		}
		out += " (champ « À » : " + strings.Join(pour, ", ") + ")"
	}
	return out
}

// -- Le contrôle ----------------------------------------------------------------

const controleConsigne = `Tu es Hugo, responsable de l'équipe d'agents de Mathias. Un agent de veille pense qu'un message signale à Mathias un problème qui le concerne. Avant de mobiliser l'équipe pour un diagnostic, tu vérifies UNE seule chose : est-ce bien à MATHIAS qu'on s'adresse pour ce problème ?

OUI seulement si le texte établit l'un de ces cas :
a. on l'interpelle lui, nommément, pour ce problème : « Mathias, … », « @Mathias », ou « peux-tu / tu peux / pourrais-tu… » dans un message écrit à lui seul ou en réponse à un message de lui ;
b. le problème porte sur quelque chose qu'il a lui-même fait, envoyé, livré, ou dont le fil dit qu'il a la charge, et on attend qu'il réagisse ;
c. le message lui est écrit à lui seul (seul destinataire, message privé) et lui signale le problème ;
d. c'est une notification de l'outil de tickets (Tuleap), le ticket lui est ASSIGNÉ (« Assigned to … Mathias Coutant ») et il y a quelque chose à corriger : le ticket passe « To correct », ou son bilan relève des anomalies. La preuve est alors la ligne qui le lui assigne.

NON dans tous les autres cas, et notamment :
- on demande à une AUTRE personne de s'en occuper (« Olivier, peux-tu regarder ? »), même si Mathias est destinataire, en copie, ou cité pour information (« cc Mathias », « Mathias est au courant », « comme disait Mathias ») ;
- le message s'adresse à un groupe sans le désigner (« quelqu'un peut regarder ? », « l'équipe », « bonjour à tous ») ;
- quelqu'un d'autre a déjà pris le problème en charge dans le fil, ou il est déjà réglé ;
- c'est une alerte automatique ou une notification d'outil, hors le cas d : un ticket assigné à quelqu'un d'autre, ou qui change seulement d'état sans rien à corriger (Testing, Ready, Done), c'est non ;
- ce n'est pas un problème mais une question, une demande ordinaire, une relance ou une information ;
- tu ne peux pas trancher.
Être dans les destinataires ne suffit JAMAIS. Dans le doute : NON. Un diagnostic lancé à tort dérange Mathias et coûte ; un problème non remonté reste visible dans sa veille, où il peut le demander d'un clic.

Ce que Mathias a écrit lui-même est marqué « toi (Mathias) ». La phrase qui prouve qu'on s'adresse à lui doit venir d'un AUTRE que lui, dans les nouveaux messages — pas de ce qui est cité plus haut dans le fil.

Réponds UNIQUEMENT par un objet JSON : {"pour_mathias": true ou false, "a_qui": "<le prénom de la personne à qui on demande de s'en occuper ; « Mathias » si c'est lui ; vide si personne n'est désigné>", "probleme": "<ce qui ne va pas, précisément : quoi, où, depuis quand — une ou deux phrases>", "preuve": "<la phrase du message, recopiée MOT POUR MOT, qui montre à qui on s'adresse>", "raison": "<pourquoi oui ou non, une phrase, en tutoyant Mathias : « Johanna te demande de… », « C'est à Olivier qu'on demande… »>"}. En français.`

type controle struct {
	PourMathias bool   `json:"pour_mathias"`
	AQui        string `json:"a_qui"`
	Probleme    string `json:"probleme"`
	Preuve      string `json:"preuve"`
	Raison      string `json:"raison"`
}

// controler fait relire la remontée par Hugo — son modèle et son effort
// quand il pilote par l'API, sinon le jugement de l'atelier.
func (s *Server) controler(ctx context.Context, r remontee, autres string) (controle, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "LE SUJET : %s", r.n.Titre)
	if r.n.Lieu != "" {
		fmt.Fprintf(&b, " · %s", r.n.Lieu)
	}
	fmt.Fprintf(&b, "\nCe que la veille en a compris : %s\n", r.n.Resume)
	fmt.Fprintf(&b, "Le problème, selon elle : %s\nLa phrase qu'elle donne comme preuve : « %s »\n\n", strings.TrimSpace(r.p.Quoi), strings.TrimSpace(r.p.Preuve))
	b.WriteString("LES NOUVEAUX MESSAGES :\n" + dossierMessages(r.items))
	b.WriteString("\n\nCE QUE LES AUTRES Y ONT ÉCRIT (c'est là que doit se trouver ta preuve) :\n" + truncateRunes(strings.TrimSpace(autres), 4000))

	var out string
	var err error
	switch {
	case s.pilotage != nil:
		out, err = s.pilotage.juger(ctx, controleConsigne, b.String())
	case s.atelier != nil:
		out, err = s.atelier.Classify(ctx, controleConsigne, b.String())
	case s.engine != nil:
		out, err = s.engine.Classify(ctx, controleConsigne, b.String())
	default:
		err = errors.New("aucun modèle pour le contrôle")
	}
	if err != nil {
		return controle{}, err
	}
	var c controle
	if err := json.Unmarshal([]byte(objetJSON(out)), &c); err != nil {
		return controle{}, fmt.Errorf("contrôle illisible : %w", err)
	}
	return c, nil
}

// objetJSON : l'objet JSON d'une réponse, sans ce qui l'entoure.
func objetJSON(out string) string {
	if i, k := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && k > i {
		return out[i : k+1]
	}
	return out
}

// -- L'enquête de Hugo ------------------------------------------------------------

var errDiagnosticDeja = errors.New("un diagnostic est déjà en cours sur ce sujet")

// lancerDiagnostic confie le problème à Hugo. d porte ce que le contrôle a
// établi (la preuve, la raison) ; messages, ce que la veille a lu.
func (s *Server) lancerDiagnostic(ctx context.Context, user *store.User, agent equipe.Membre, n store.Notification, probleme string, d store.Diagnostic, messages string) (store.AgentRun, error) {
	hugo, ok := s.hqMember(equipe.IDPilote)
	if !ok {
		return store.AgentRun{}, errors.New("pas de responsable d'équipe à qui confier le diagnostic")
	}
	d.Quand = time.Now()
	pris, err := s.store.ReserverDiagnostic(ctx, n.ID, probleme, d, d.Demande)
	if err != nil {
		return store.AgentRun{}, err
	}
	if !pris {
		return store.AgentRun{}, errDiagnosticDeja
	}
	demande := demandeDiagnostic(agent, n, probleme, d, messages)
	run, err := s.launchRun(user, hugo, demande, runOpts{
		Source: store.RunSourceDiagnostic, Affectation: "Diagnostic : " + n.Titre, Enquete: true, Limit: diagnosticDuree,
		Apres: func(res assistant.Result, err error) {
			s.conclureDiagnostic(user, hugo, n, probleme, d, res.Reply, err)
		},
	}, s.consignePilote(hugo))
	if err != nil {
		echec := d
		echec.Etat, echec.Erreur = store.DiagnosticEchec, "L'enquête n'a pas pu être lancée : "+err.Error()
		_ = s.store.PoserDiagnostic(context.WithoutCancel(ctx), n.ID, probleme, echec)
		return store.AgentRun{}, err
	}
	if err := s.store.NoterRunDiagnostic(context.WithoutCancel(ctx), n.ID, run.ID.Hex()); err != nil {
		slog.Warn("diagnostic : tâche non rattachée au sujet", "err", err)
	}
	slog.Info("diagnostic lancé", "sujet", n.Titre, "run", run.ID.Hex(), "demande", d.Demande)
	return run, nil
}

// demandeDiagnostic : ce que Hugo reçoit. La méthode est dans la demande, pas
// seulement dans sa consigne : elle vaut aussi quand il travaille dans
// l'atelier, et elle est sous ses yeux à chaque tour.
func demandeDiagnostic(agent equipe.Membre, n store.Notification, probleme string, d store.Diagnostic, messages string) string {
	var b strings.Builder
	qui := "la veille"
	if agent.Nom != "" {
		qui = fmt.Sprintf("%s (%s)", agent.Nom, strings.ToLower(agent.Role))
	}
	if d.Demande {
		fmt.Fprintf(&b, "DIAGNOSTIC — Mathias te demande, depuis HQ, d'enquêter sur un sujet de sa veille (%s).\n", qui)
	} else {
		fmt.Fprintf(&b, "DIAGNOSTIC — %s te remonte un problème adressé à Mathias.\n", qui)
	}
	b.WriteString("Ce n'est pas Mathias qui te parle en direct. Il lira ton diagnostic dans HQ, sur ce sujet.\n\n")
	fmt.Fprintf(&b, "LE SUJET : %s\n", n.Titre)
	if n.Lieu != "" {
		ou := "la conversation"
		if n.Source == "mail" {
			ou = "le mail, objet"
		}
		fmt.Fprintf(&b, "Où : %s « %s » (%s)\n", ou, n.Lieu, n.Source)
	}
	if n.Resume != "" {
		fmt.Fprintf(&b, "Ce que la veille en a compris : %s\n", n.Resume)
	}
	if n.AFaire != "" {
		fmt.Fprintf(&b, "Ce qu'on attend de Mathias : %s\n", n.AFaire)
	}
	if probleme != "" {
		fmt.Fprintf(&b, "LE PROBLÈME, tel qu'il est compris pour l'instant : %s\n", probleme)
	}
	if d.Preuve != "" {
		fmt.Fprintf(&b, "Pourquoi c'est à lui : « %s »", d.Preuve)
		if d.Raison != "" {
			fmt.Fprintf(&b, " — %s", d.Raison)
		}
		b.WriteString("\n")
	}
	if messages = strings.TrimSpace(messages); messages != "" {
		b.WriteString("\nLES MESSAGES, tels que la veille les a lus :\n" + messages + "\n")
	} else if len(n.Echanges) > 0 {
		b.WriteString("\nQUI A DIT QUOI :\n")
		for _, e := range n.Echanges {
			fmt.Fprintf(&b, "- %s", e.Qui)
			if e.AQui != "" {
				fmt.Fprintf(&b, " à %s", e.AQui)
			}
			fmt.Fprintf(&b, " : %s\n", e.Quoi)
		}
	}
	b.WriteString(`
TA MÉTHODE, dans cet ordre :
1. Comprends avant d'enquêter. Relis la discussion entière avec tes outils de lecture (le mail et ses réponses, la conversation Slack ou WhatsApp), puis les fiches : le projet « metier » pour le vocabulaire et les process de Mathias, et le projet concerné. Reformule le problème avec des faits : quoi exactement, où (quel projet, quel client ou airline, quel cycle, quel environnement), depuis quand, qui le constate, ce qui était attendu à la place.
2. Assure-toi que c'est le BON problème. Ce que dit le message n'est qu'un symptôme rapporté : fais-le constater. Confie la vérification à l'agent compétent — l'ingénieur pour le code, les serveurs, les applications, les logs, les données ; la veille pour retrouver un échange ou un document — avec une consigne complète : le symptôme tel qu'il est rapporté, ce qu'il doit observer LUI-MÊME (reproduire, lire les logs, regarder l'état réel), et la question précise à trancher. En LECTURE SEULE : personne ne corrige, ne relance, ne déploie, ne pousse, n'envoie quoi que ce soit.
3. Remonte à la cause. Un constat sans cause n'est pas un diagnostic : si la première enquête ne l'établit pas, renvoie une tâche plus précise avec ce que tu as appris. Sépare toujours ce qui a été constaté de ce qui est supposé. Si le problème rapporté n'existe pas, ou n'est pas celui qu'on croit, dis-le : c'est un diagnostic.
4. Dis ce qu'il faut faire pour corriger, concrètement : la commande exacte à lancer (telle que l'ingénieur l'a établie, jamais inventée), ou le changement à faire dans un process — qui, quoi, à quelle étape. Et cherche le trou dans la raquette : ce qui a laissé passer le problème (un contrôle absent, une étape faite à la main, une alerte qui manque, une règle que personne n'a écrite), et ce qu'il faudrait changer pour qu'il ne revienne pas.
5. Retiens ce que l'affaire t'a appris du métier de Mathias : range-le avec noter_fiche dans le projet « metier » (un mot du métier, un process, qui fait quoi, cette cause de panne). Ce que seul Mathias peut dire et qui t'a manqué, pose-le avec poser_question sur « metier ».

TA RÉPONSE FINALE est UNIQUEMENT cet objet JSON, sans texte autour ni markdown — cette forme remplace toutes tes règles de forme habituelles :
{"probleme": "<le problème, reformulé précisément, deux phrases au plus>", "cause": "<la cause, ou « Non établie : … » avec ce qui manque>", "constate": "<ce qui a été vérifié, par qui, et ce qui reste supposé>", "corrections": [{"quoi": "<une chose à faire, à l'infinitif, en clair>", "commande": "<la commande exacte s'il y en a une, sinon vide>"}], "trou": "<ce qui a laissé passer le problème et ce qu'il faudrait changer pour qu'il ne revienne pas ; vide s'il n'y en a pas>", "confiance": "haute" ou "moyenne" ou "faible", "manque": "<ce qu'il te manque pour être sûr, ou ce que Mathias doit confirmer ; vide sinon>"}
Tu tutoies Mathias. Des phrases simples, sans jargon inutile. N'invente rien : tu ne sais que ce que tes outils et ton équipe t'ont dit.`)
	return b.String()
}

// diagnosticRendu : la réponse finale de Hugo.
type diagnosticRendu struct {
	Probleme    string `json:"probleme"`
	Cause       string `json:"cause"`
	Constate    string `json:"constate"`
	Corrections []struct {
		Quoi     string `json:"quoi"`
		Commande string `json:"commande"`
	} `json:"corrections"`
	Trou      string `json:"trou"`
	Confiance string `json:"confiance"`
	Manque    string `json:"manque"`
}

// lireDiagnostic range la réponse de Hugo dans le diagnostic. Une réponse
// qui n'a pas la forme attendue est gardée telle quelle : mieux vaut un
// diagnostic mal mis en page qu'un diagnostic perdu.
func lireDiagnostic(reply string, d *store.Diagnostic) {
	reply = strings.TrimSpace(reply)
	var r diagnosticRendu
	if err := json.Unmarshal([]byte(objetJSON(reply)), &r); err != nil || (strings.TrimSpace(r.Cause) == "" && strings.TrimSpace(r.Probleme) == "") {
		d.Texte = truncateRunes(reply, 6000)
		return
	}
	d.Compris = strings.TrimSpace(r.Probleme)
	d.Cause = strings.TrimSpace(r.Cause)
	d.Constate = strings.TrimSpace(r.Constate)
	d.Trou = strings.TrimSpace(r.Trou)
	d.Manque = strings.TrimSpace(r.Manque)
	switch c := strings.ToLower(strings.TrimSpace(r.Confiance)); c {
	case "haute", "moyenne", "faible":
		d.Confiance = c
	}
	d.Corrections = nil
	for _, c := range r.Corrections {
		quoi, cmd := strings.TrimSpace(c.Quoi), strings.TrimSpace(c.Commande)
		if quoi == "" && cmd == "" {
			continue
		}
		d.Corrections = append(d.Corrections, store.Correction{Quoi: quoi, Commande: cmd})
	}
}

// diagnosticEnClair : le diagnostic en quelques phrases, pour la mémoire des
// échanges de Hugo — Mathias peut ensuite lui en reparler.
func diagnosticEnClair(d store.Diagnostic) string {
	if d.Texte != "" {
		return d.Texte
	}
	var parts []string
	add := func(label, v string) {
		if v != "" {
			parts = append(parts, label+v)
		}
	}
	add("Problème : ", d.Compris)
	add("Cause : ", d.Cause)
	add("Vérifié : ", d.Constate)
	for _, c := range d.Corrections {
		ligne := "À faire : " + c.Quoi
		if c.Commande != "" {
			ligne += " — " + c.Commande
		}
		parts = append(parts, ligne)
	}
	add("Trou dans la raquette : ", d.Trou)
	add("Il manque : ", d.Manque)
	return strings.Join(parts, "\n")
}

// conclureDiagnostic pose sur le sujet ce que l'enquête a donné.
func (s *Server) conclureDiagnostic(user *store.User, hugo equipe.Membre, n store.Notification, probleme string, d store.Diagnostic, reply string, runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// La tâche rattachée au sujet pendant l'enquête y reste.
	if cur, err := s.store.NotificationByID(ctx, user.ID, n.ID); err == nil && cur.Diagnostic != nil {
		d.Run = cur.Diagnostic.Run
	}
	d.Quand = time.Now()
	switch {
	case errors.Is(runErr, context.Canceled):
		d.Etat, d.Erreur = store.DiagnosticEchec, "L'enquête a été annulée."
	case errors.Is(runErr, context.DeadlineExceeded):
		d.Etat, d.Erreur = store.DiagnosticEchec, "L'enquête a dépassé le temps prévu."
	case runErr != nil:
		d.Etat, d.Erreur = store.DiagnosticEchec, "L'enquête s'est arrêtée : "+truncateRunes(runErr.Error(), 300)
	case strings.TrimSpace(reply) == "":
		d.Etat, d.Erreur = store.DiagnosticEchec, "Hugo n'a rien rendu."
	default:
		d.Etat = store.DiagnosticPret
		lireDiagnostic(reply, &d)
	}
	if err := s.store.PoserDiagnostic(ctx, n.ID, probleme, d); err != nil {
		slog.Warn("diagnostic non enregistré", "err", err)
		return
	}
	if d.Etat == store.DiagnosticPret {
		_ = s.store.SaveInteraction(ctx, store.Interaction{
			UserID: user.ID, Agent: hugo.ID,
			Transcript: "(Diagnostic remonté par la veille, sujet « " + n.Titre + " »)",
			Reply:      truncateRunes(diagnosticEnClair(d), 1500),
		})
	}
}

// reprendreDiagnostics remet d'aplomb les diagnostics restés « en cours »
// alors que leur tâche ne tourne plus — un redémarrage du serveur pendant
// l'enquête. Appelé à la lecture des sujets : rien ne reste bloqué à l'écran.
func (s *Server) reprendreDiagnostics(ctx context.Context, user *store.User, list []store.Notification) {
	hugo, _ := s.hqMember(equipe.IDPilote)
	for i := range list {
		d := list[i].Diagnostic
		if d == nil || d.Etat != store.DiagnosticEnCours {
			continue
		}
		if d.Run == "" {
			// La tâche se rattache dans la seconde qui suit le lancement.
			if time.Since(d.Quand) < 2*time.Minute {
				continue
			}
			s.conclureDiagnostic(user, hugo, list[i], "", *d, "", errors.New("elle n'a jamais démarré"))
		} else {
			id, err := bson.ObjectIDFromHex(d.Run)
			if err != nil {
				continue
			}
			if _, ok := s.runs.liveRunByID(id); ok {
				continue
			}
			run, err := s.store.RunByID(ctx, user.ID, id)
			switch {
			case err != nil:
				continue
			case run.Statut == store.RunTermine:
				s.conclureDiagnostic(user, hugo, list[i], "", *d, run.Reponse, nil)
			case run.Statut == store.RunAnnule:
				s.conclureDiagnostic(user, hugo, list[i], "", *d, "", context.Canceled)
			case run.Statut == store.RunErreur:
				s.conclureDiagnostic(user, hugo, list[i], "", *d, "", errors.New(run.Erreur))
			default:
				s.conclureDiagnostic(user, hugo, list[i], "", *d, "", errors.New("le serveur a redémarré pendant l'enquête"))
			}
		}
		if cur, err := s.store.NotificationByID(ctx, user.ID, list[i].ID); err == nil {
			list[i] = cur
		}
	}
}

// handleHQDiagnostiquer : Mathias demande lui-même le diagnostic d'un sujet —
// un problème que la veille a écarté, ou un sujet où elle n'en a pas vu.
func (s *Server) handleHQDiagnostiquer(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if s.equipe == nil {
		httpx.Error(w, http.StatusServiceUnavailable, persoDisabled)
		return
	}
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
		return
	}
	n, err := s.store.NotificationByID(r.Context(), user.ID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "sujet introuvable")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "lecture du sujet impossible")
		return
	}
	agent, _ := s.hqMember(n.Agent)
	d := store.Diagnostic{Demande: true}
	if n.Diagnostic != nil {
		d.Preuve = n.Diagnostic.Preuve
	}
	if _, err := s.lancerDiagnostic(r.Context(), user, agent, n, n.Probleme, d, ""); err != nil {
		switch {
		case errors.Is(err, errDiagnosticDeja):
			httpx.Error(w, http.StatusConflict, "Hugo enquête déjà sur ce sujet")
		case errors.Is(err, errBudget):
			httpx.Error(w, http.StatusTooManyRequests, err.Error())
		default:
			httpx.Error(w, http.StatusInternalServerError, "le diagnostic n'a pas pu être lancé")
		}
		return
	}
	out, err := s.store.NotificationByID(r.Context(), user.ID, id)
	if err != nil {
		out = n
	}
	httpx.JSON(w, http.StatusAccepted, out)
}
