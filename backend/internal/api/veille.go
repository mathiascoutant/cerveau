package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// La veille : trois agents — mails pro, Slack, WhatsApp — qui relèvent leur
// messagerie à intervalle régulier et préviennent dans Cerveau HQ quand une
// information compte.
//
// Une relève ne coûte presque rien : on lit ce qui est arrivé, on ne garde que
// ce qui s'adresse vraiment à Mathias (le même tri que les urgences de l'app :
// adressé à lui, mention, message privé — des règles, pas de modèle), et on
// écarte ce que l'agent a déjà regardé. Ce n'est que s'il reste du neuf que
// l'agent se met au travail — et le bâtiment le montre — pour juger, avec
// Claude (par l'atelier), ce qui mérite une notification.
//
// La toute première relève d'un agent ne signale rien : elle note ce qui est
// déjà là, pour ne pas réveiller Mathias avec trois jours de messages.

const (
	veilleDefautMinutes = 1
	// Slack limite les lectures d'historique par minute, pour tout le jeton :
	// relever chaque minute le faisait refuser (« ratelimited »), et Raoul
	// avec. Slack est donc relevé moins souvent que les mails et WhatsApp.
	veilleSlackMinutes = 3
	veilleTimeout      = 3 * time.Minute
	veilleMaxMessages  = 25
)

// veilleSources : ce que chaque agent relève.
var veilleSources = []struct {
	Source string
	Label  string
}{
	{"mail", "mails pro"},
	{"slack", "Slack"},
	{"whatsapp", "WhatsApp"},
}

// StartVeille lance la veille en tâche de fond pour les comptes configurés
// (VEILLE_COMPTES). Sans compte, ou sans équipe, elle ne fait rien.
func (s *Server) StartVeille(ctx context.Context) {
	if s.equipe == nil || len(s.cfg.VeilleComptes) == 0 {
		return
	}
	every := time.Duration(s.cfg.VeilleMinutes) * time.Minute
	if every <= 0 {
		every = veilleDefautMinutes * time.Minute
	}
	slog.Info("veille active", "comptes", len(s.cfg.VeilleComptes), "intervalle", every)
	go func() {
		// Le temps que le serveur et WhatsApp soient prêts.
		if !sleepOrDone(ctx, time.Minute) {
			return
		}
		for {
			s.veilleTour(ctx)
			if !sleepOrDone(ctx, every) {
				return
			}
		}
	}()
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) veilleTour(ctx context.Context) {
	slackEvery := time.Duration(max(s.cfg.VeilleMinutesSlack, veilleSlackMinutes)) * time.Minute
	slackDue := time.Since(s.veilleSlackAt) >= slackEvery-10*time.Second
	if slackDue {
		s.veilleSlackAt = time.Now()
	}
	for _, email := range s.cfg.VeilleComptes {
		user, err := s.store.UserByEmail(ctx, email)
		if err != nil {
			slog.Warn("veille : compte introuvable", "email", email)
			continue
		}
		src := s.sources(ctx, user)
		for _, v := range veilleSources {
			connected := (v.Source == "mail" && src.Mail) || (v.Source == "slack" && src.Slack) || (v.Source == "whatsapp" && src.WhatsApp)
			agent, ok := equipe.VeilleDe(s.equipe.Roster(), v.Source)
			if !connected || !ok || (v.Source == "slack" && !slackDue) {
				continue
			}
			vctx, cancel := context.WithTimeout(ctx, veilleTimeout)
			if err := s.veiller(vctx, user, agent, v.Source, v.Label); err != nil {
				slog.Warn("veille : relève en échec", "agent", agent.ID, "err", err)
			}
			cancel()
			// Après le jugement : un mail rangé quitte la réception.
			if v.Source == "mail" && s.trieur != nil {
				tctx, cancel := context.WithTimeout(ctx, triTimeout)
				if err := s.trier(tctx, user, agent); err != nil {
					slog.Warn("tri : relève en échec", "agent", agent.ID, "err", err)
				}
				cancel()
			}
		}
	}
}

// veilleItems : ce qui est arrivé dans la messagerie et s'adresse à Mathias.
func (s *Server) veilleItems(ctx context.Context, user *store.User, source string) ([]triage.Item, error) {
	tb := s.toolbox(user)
	switch source {
	case "mail":
		mails, moi, err := tb.unreadMail(ctx, urgentMailDepth)
		if err != nil {
			return nil, err
		}
		return triage.Mails(triageMails(mails), moi), nil
	case "slack":
		threads, err := tb.slackActivity(ctx, urgentSlackDepth)
		if err != nil {
			return nil, err
		}
		return triage.Slack(triageConversations(threads)), nil
	case "whatsapp":
		threads, err := tb.whatsAppThreads(ctx, urgentWhatsAppDepth)
		if err != nil {
			return nil, err
		}
		return triage.WhatsApp(triageWhatsApp(threads)), nil
	}
	return nil, fmt.Errorf("messagerie inconnue : %s", source)
}

// veilleCle : un message (ou, pour une conversation, son dernier état) vu
// une fois. Un nouveau message dans la même conversation change la clé.
func veilleCle(it triage.Item) string {
	h := sha256.Sum256([]byte(strings.Join([]string{string(it.Source), it.Adresse, it.Titre, it.De, it.Quand.UTC().Format(time.RFC3339), it.Apercu}, "\x1f")))
	return hex.EncodeToString(h[:12])
}

// veilleSujetDuree : combien de temps un sujet reste ouvert aux nouveaux
// messages de sa discussion. Au-delà, une reprise est un nouveau sujet.
const veilleSujetDuree = 3 * 24 * time.Hour

const veilleConsigne = `Tu fais la veille %s de Mathias. Voici les NOUVEAUX messages qui lui sont adressés depuis ta dernière relève, et les sujets que tu lui as déjà signalés ces derniers jours.
Signale seulement ce qui demande vraiment son attention : une question ou une demande qui attend sa réponse, une décision à prendre, un problème, une échéance proche, ou un message d'une personne importante pour lui (direction, client).
Un message privé (motif : message privé) est TOUJOURS à signaler : quelqu'un lui écrit directement et attend sa réponse.
Ignore les remerciements, les « ok », les informations de routine, les notifications automatiques et ce qui ne lui demande rien.

Une affaire = UN sujet, un seul bloc que tu fais vivre : tu ne signales jamais deux fois la même discussion. Un nouveau message qui continue un sujet déjà ouvert (S1, S2…) s'y rattache — champ "sujet" — même s'il ne demande rien de plus : il fait avancer l'affaire. Le titre et le résumé que tu rends alors REMPLACENT les anciens : ils disent où en est TOUTE l'affaire, ce qu'on savait déjà compris.

Réponds UNIQUEMENT par un objet JSON : {"sujets": [{"sujet": "S1" ou "" pour un nouveau sujet, "messages": [numéros], "titre": "<l'affaire en clair, 8 mots au plus, sans « Re: » ni nom d'expéditeur recopié de l'objet>", "resume": "<l'ESSENTIEL du contenu, deux phrases au plus>", "a_faire": "<ce qu'on attend de Mathias, une phrase ; vide si rien>", "taches": [{"titre": "<une action à l'infinitif>", "echeance": "AAAA-MM-JJ ou vide"}], "echanges": [{"qui": "<prénom>", "a_qui": "<prénom, ou vide>", "quoi": "<ce qu'il a dit, une phrase courte>"}], "importance": "haute" ou "moyenne"}]}.
"resume" : va droit au fond — ce qui est dit, demandé, décidé, livré, les chiffres, les dates, les noms de fichiers. Mathias voit déjà le titre et le lieu : ne redis jamais qui écrit, que c'est une réponse, ni l'objet du mail. Bon : « La v7.2 corrige les deux doublons ; Jonata attend la validation pour lancer l'import vendredi. » Mauvais : « Olivier répond sur le sujet Condor 1126 ; c'est une réponse à un échange en cours. » Si tu n'as vraiment que l'objet, dis en quelques mots ce qu'il annonce — jamais que le contenu manque.
Une réponse se résume PAR RAPPORT à ce à quoi elle répond (« il répond à ce message de… », les messages d'avant) : la question, la demande, la proposition, et ce qu'on y répond. Bon : « Il répond oui à ta demande : il est dispo samedi matin. » ; « Elle refuse le devis que tu lui as envoyé : trop cher de 15 %%, elle attend une nouvelle offre. » Ce que Mathias a écrit lui-même est marqué « toi ». Si c'est lui qui a écrit en dernier, il a déjà répondu : rien n'est à faire (a_faire vide), dis seulement où en est l'échange.
Une entrée « de toi » marquée SUIVI n'est pas un message à signaler : c'est Mathias qui a agi dans la discussion d'un sujet ouvert (envoyé le fichier, répondu, proposé un appel…). Rattache-la à ce sujet et mets-le à jour : ce qu'il a fait, et "a_faire" vide si c'est réglé.
"taches" : ce qui entre dans sa liste à faire — une tâche par action distincte que le sujet attend de lui, à l'infinitif, assez précise pour se comprendre seule dans la liste (quoi, pour quel client ou quel dossier, avec qui). Bon, pour un mail qui livre des fichiers et attend un retour : « Vérifier les images livrées pour Azul 1226 » et « Répondre à Satish sur la livraison Azul 1226 ». "echeance" seulement si le message fixe un jour (calcule-le d'après la date du jour donnée en tête), sinon vide. Aucune tâche quand "a_faire" est vide, ni pour une action déjà dans les « tâches inscrites » d'un sujet ouvert, même dite autrement.
"echanges" : qui a dit quoi dans les NOUVEAUX messages, une ligne par prise de parole qui compte, dans l'ordre — par exemple Adélaïde dit à Sarah que le chiffre est faux, puis Sarah lui répond qu'elle a raison. L'essentiel, pas une recopie. Mathias s'écrit « toi ».
Regroupe dans un même sujet les messages qui parlent de la même chose. Liste vide si rien n'est important et que rien ne continue un sujet ouvert. En français.`

type jugement struct {
	Sujets []struct {
		Sujet      string       `json:"sujet"`
		Messages   []int        `json:"messages"`
		Titre      string       `json:"titre"`
		Resume     string       `json:"resume"`
		AFaire     string       `json:"a_faire"`
		Taches     []tacheJugee `json:"taches"`
		Echanges   []parole     `json:"echanges"`
		Importance string       `json:"importance"`
	} `json:"sujets"`
}

// tacheJugee : une action que le jugement inscrit dans la liste à faire.
type tacheJugee struct {
	Titre    string `json:"titre"`
	Echeance string `json:"echeance"`
}

type parole struct {
	Qui  string `json:"qui"`
	AQui string `json:"a_qui"`
	Quoi string `json:"quoi"`
}

// filDe : la clé de la discussion d'un message. Un mail se range sous son
// objet nu, une conversation Slack ou WhatsApp sous son nom.
func filDe(it triage.Item) string {
	if it.Source == "mail" {
		return "mail:" + strings.ToLower(gandi.CleanSubject(it.Titre))
	}
	return it.Source + ":" + strings.ToLower(strings.TrimSpace(it.Titre))
}

// lieuDe : où se tient la discussion, tel qu'on le dit.
func lieuDe(it triage.Item) string {
	if it.Source == "mail" {
		return gandi.CleanSubject(it.Titre)
	}
	return strings.TrimSpace(it.Titre)
}

func (s *Server) veiller(ctx context.Context, user *store.User, agent equipe.Membre, source, label string) error {
	items, err := s.veilleItems(ctx, user, source)
	if err != nil {
		return err
	}
	cles := make([]string, len(items))
	for i, it := range items {
		cles[i] = veilleCle(it)
	}
	demarree, err := s.store.VeilleDemarree(ctx, user.ID, agent.ID)
	if err != nil {
		return err
	}
	if !demarree {
		// Première relève : on note l'existant, sans rien signaler. Une clé
		// témoin marque la veille comme démarrée même sans message.
		return s.store.VeilleMarquerVus(ctx, user.ID, agent.ID, append(cles, "depart"))
	}
	vus, err := s.store.VeilleDejaVus(ctx, user.ID, agent.ID, cles)
	if err != nil {
		return err
	}
	var neufs []triage.Item
	var nouvellesCles []string
	for i, it := range items {
		if !vus[cles[i]] {
			neufs = append(neufs, it)
			nouvellesCles = append(nouvellesCles, cles[i])
		}
	}
	// Les sujets ouverts où Mathias a agi depuis : le suivi (Slack, WhatsApp).
	var suivis []triage.Item
	var clesSuivis []string
	if source != "mail" {
		candidats := s.sujetsSuivis(ctx, user, agent.ID, source)
		ks := make([]string, len(candidats))
		for i, it := range candidats {
			ks[i] = veilleCle(it)
		}
		dejaVus, err := s.store.VeilleDejaVus(ctx, user.ID, agent.ID, ks)
		if err == nil {
			for i, it := range candidats {
				if !dejaVus[ks[i]] {
					suivis = append(suivis, it)
					clesSuivis = append(clesSuivis, ks[i])
				}
			}
		}
	}
	if len(neufs) == 0 && len(suivis) == 0 {
		return nil
	}
	if len(neufs) > veilleMaxMessages {
		neufs = neufs[:veilleMaxMessages]
	}

	// Un mail ne se juge pas sur son objet : on en lit le contenu, et ce
	// qui le précède dans le fil.
	if source == "mail" {
		s.lireMailsNeufs(ctx, user, neufs)
	} else {
		s.lireConversationsNeuves(ctx, user, source, neufs)
	}
	// Les suivis viennent après les messages neufs : à partir de cet
	// indice, ce sont les messages de Mathias lui-même.
	debutSuivis := len(neufs)
	neufs = append(neufs, suivis...)
	nouvellesCles = append(nouvellesCles, clesSuivis...)

	// Il y a du neuf : l'agent se met au travail, et le bâtiment le voit.
	demande := fmt.Sprintf("Relève %s : %d nouveau%s message%s à juger", label, len(neufs), plural(len(neufs), "x", ""), plural(len(neufs), "s", ""))
	run := s.runs.begin(ctx, user.ID, agent.ID, store.RunSourceVeille, demande, nil)
	run.Etape(assistant.Event{Type: "status", Tool: "veille", Label: "Lecture des nouveaux messages"})
	started := time.Now()

	// Les sujets déjà signalés : un message qui continue l'un d'eux le fait
	// avancer au lieu d'en ouvrir un autre.
	ouverts, err := s.store.SujetsOuverts(ctx, user.ID, agent.ID, time.Now().Add(-veilleSujetDuree))
	if err != nil {
		slog.Warn("veille : sujets ouverts illisibles", "err", err)
	}
	parFil := map[string]int{}
	for i := len(ouverts) - 1; i >= 0; i-- {
		if ouverts[i].Fil != "" {
			parFil[ouverts[i].Fil] = i
		}
	}

	// Les tâches déjà tirées des sujets ouverts : le jugement ne les redonne
	// pas, et le code les écarte s'il le fait quand même.
	ids := make([]bson.ObjectID, len(ouverts))
	for i, o := range ouverts {
		ids[i] = o.ID
	}
	inscrites, err := s.store.TodosDesSujets(ctx, user.ID, ids)
	if err != nil {
		slog.Warn("veille : tâches des sujets illisibles", "err", err)
		inscrites = map[bson.ObjectID][]store.Todo{}
	}

	tb := s.toolbox(user)
	dir := tb.annuaire(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "AUJOURD'HUI : %s\n\n", time.Now().In(tb.location()).Format("Monday 2006-01-02"))
	if len(ouverts) > 0 {
		b.WriteString("SUJETS DÉJÀ OUVERTS :\n")
		for i, o := range ouverts {
			fmt.Fprintf(&b, "[S%d] %s", i+1, o.Titre)
			if o.Lieu != "" {
				fmt.Fprintf(&b, " · %s", o.Lieu)
			}
			fmt.Fprintf(&b, "\n    où on en est : %s\n", o.Resume)
			if n := len(o.Echanges); n > 0 {
				var lignes []string
				for _, e := range o.Echanges[max(0, n-4):] {
					lignes = append(lignes, e.Qui+" : "+e.Quoi)
				}
				fmt.Fprintf(&b, "    derniers échanges : %s\n", strings.Join(lignes, " ; "))
			}
			if ts := inscrites[o.ID]; len(ts) > 0 {
				var lignes []string
				for _, t := range ts {
					lignes = append(lignes, t.Title)
				}
				fmt.Fprintf(&b, "    tâches inscrites : %s\n", strings.Join(lignes, " ; "))
			}
		}
		b.WriteString("\nNOUVEAUX MESSAGES :\n")
	}
	for i, it := range neufs {
		id := itemIdentity(dir, it)
		fmt.Fprintf(&b, "[%d] %s · de %s", i+1, origin(it), it.De)
		if id.Qui != "" {
			fmt.Fprintf(&b, " (%s)", id.Qui)
		}
		if id.Importance != "" {
			fmt.Fprintf(&b, " · importance de la personne : %s", id.Importance)
		}
		fmt.Fprintf(&b, " · %s · %s\n", tb.when(it.Quand), motif(it.Motif))
		if i, ok := parFil[filDe(it)]; ok {
			fmt.Fprintf(&b, "    même discussion que le sujet S%d\n", i+1)
		}
		if it.Titre != "" {
			fmt.Fprintf(&b, "    sujet : %s\n", it.Titre)
		}
		if it.Apercu != "" {
			fmt.Fprintf(&b, "    contenu : %s\n", truncateRunes(it.Apercu, 1800))
		}
	}
	// Le jugement passe par Claude (l'atelier, sur l'abonnement) ; le modèle
	// rapide du moteur ne sert que si l'atelier n'est pas configuré.
	judge := s.engine.Classify
	if s.atelier != nil {
		judge = s.atelier.Classify
	}
	out, err := judge(ctx, fmt.Sprintf(veilleConsigne, label), b.String())
	run.Etape(assistant.Event{Type: "status", Tool: "veille", Ms: max(1, time.Since(started).Milliseconds())})
	if err != nil {
		run.Fin(assistant.Result{}, err)
		return err
	}
	var j jugement
	if i, k := strings.Index(out, "{"), strings.LastIndex(out, "}"); i >= 0 && k > i {
		out = out[i : k+1]
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		run.Fin(assistant.Result{}, fmt.Errorf("jugement illisible : %w", err))
		return err
	}
	// Ce qui a été jugé est vu, important ou non.
	if err := s.store.VeilleMarquerVus(ctx, user.ID, agent.ID, nouvellesCles); err != nil {
		slog.Warn("veille : messages vus non notés", "err", err)
	}

	var titres []string
	nbTaches := 0
	couverts := map[int]bool{}
	// noter range un sujet : il fait avancer un sujet ouvert (même numéro
	// rendu par le jugement, ou même discussion), ou il en ouvre un. Il rend
	// l'identifiant du sujet, faux si rien n'a pu être écrit.
	noter := func(cible int, n store.Notification, discret bool) (bson.ObjectID, bool) {
		if cible >= 0 && cible < len(ouverts) {
			if err := s.store.MettreAJourSujet(ctx, ouverts[cible].ID, n, discret); err != nil {
				slog.Warn("veille : sujet non mis à jour", "err", err)
				return bson.ObjectID{}, false
			}
			if discret {
				titres = append(titres, n.Titre+" (mis à jour en silence : c'est toi qui as écrit)")
			} else {
				titres = append(titres, n.Titre+" (mis à jour)")
			}
			return ouverts[cible].ID, true
		}
		if discret {
			// Pas de nouveau sujet fait des seuls messages de Mathias.
			return bson.ObjectID{}, false
		}
		ins, err := s.store.InsertNotification(ctx, n)
		if err != nil {
			slog.Warn("veille : notification non enregistrée", "err", err)
			return bson.ObjectID{}, false
		}
		ouverts = append(ouverts, ins)
		if n.Fil != "" {
			parFil[n.Fil] = len(ouverts) - 1
		}
		titres = append(titres, n.Titre)
		return ins.ID, true
	}

	for _, suj := range j.Sujets {
		titre := strings.TrimSpace(suj.Titre)
		if titre == "" {
			continue
		}
		importance := suj.Importance
		if importance != "haute" {
			importance = "moyenne"
		}
		var refs []string
		var fil, lieu string
		var premier *triage.Item
		var quand time.Time
		// Discret : le sujet n'avance que par ce que Mathias a écrit.
		discret := len(suj.Messages) > 0
		for _, n := range suj.Messages {
			if n >= 1 && n <= len(neufs) {
				if n-1 < debutSuivis {
					discret = false
				}
				couverts[n-1] = true
				it := neufs[n-1]
				ref := origin(it) + " · " + it.De
				if it.Titre != "" {
					ref += " · " + it.Titre
				}
				refs = append(refs, ref)
				if fil == "" {
					fil, lieu = filDe(it), lieuDe(it)
					premier = &neufs[n-1]
				}
				if it.Quand.After(quand) {
					quand = it.Quand
				}
			}
		}
		if quand.IsZero() {
			quand = time.Now()
		}
		var echanges []store.PriseDeParole
		for _, p := range suj.Echanges {
			if quoi := strings.TrimSpace(p.Quoi); quoi != "" {
				echanges = append(echanges, store.PriseDeParole{Qui: strings.TrimSpace(p.Qui), AQui: strings.TrimSpace(p.AQui), Quoi: quoi, Quand: quand})
			}
		}
		cible := -1
		if k, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(suj.Sujet)), "S")); err == nil && k >= 1 && k <= len(ouverts) {
			cible = k - 1
		} else if k, ok := parFil[fil]; ok {
			cible = k
		}
		n := store.Notification{
			UserID: user.ID, Agent: agent.ID, Source: source, Titre: titre,
			Resume: strings.TrimSpace(suj.Resume), AFaire: strings.TrimSpace(suj.AFaire), Importance: importance,
			Messages: refs, Run: run.ID().Hex(), Fil: fil, Lieu: lieu, Echanges: echanges,
		}
		id, ok := noter(cible, n, discret)
		// Ce que le sujet attend de Mathias entre dans sa liste à faire.
		if ok && n.AFaire != "" && len(suj.Taches) > 0 {
			nbTaches += s.inscrireTaches(ctx, tb, id, n, premier, suj.Taches, inscrites[id])
		}
	}
	// Un message privé non lu est toujours signalé, même si le jugement l'a
	// laissé de côté : quelqu'un attend une réponse de Mathias.
	for _, it := range privesOublies(neufs, couverts) {
		apercu := strings.TrimSpace(it.Apercu)
		if apercu == "" {
			apercu = "Nouveau message privé non lu."
		}
		n := store.Notification{
			UserID: user.ID, Agent: agent.ID, Source: source, Titre: "Message privé de " + it.De,
			Resume: truncateRunes(apercu, 220), Importance: "moyenne", Messages: []string{origin(it) + " · " + it.De},
			Run: run.ID().Hex(), Fil: filDe(it), Lieu: lieuDe(it),
			Echanges: []store.PriseDeParole{{Qui: it.De, AQui: "toi", Quoi: truncateRunes(apercu, 220), Quand: it.Quand}},
		}
		cible := -1
		if k, ok := parFil[n.Fil]; ok {
			// La discussion a déjà son sujet : il garde son titre et son
			// résumé, le message s'ajoute à ses échanges.
			cible = k
			n.Titre, n.Resume, n.AFaire, n.Importance = ouverts[k].Titre, ouverts[k].Resume, ouverts[k].AFaire, ouverts[k].Importance
		}
		noter(cible, n, false)
	}
	reply := "Rien d'important."
	if len(titres) > 0 {
		reply = fmt.Sprintf("%d sujet%s : %s.", len(titres), plural(len(titres), "s", ""), strings.Join(titres, " ; "))
	}
	if nbTaches > 0 {
		reply += fmt.Sprintf(" %d tâche%s inscrite%s.", nbTaches, plural(nbTaches, "s", ""), plural(nbTaches, "s", ""))
	}
	run.Fin(assistant.Result{Reply: reply}, nil)
	return nil
}

// inscrireTaches met dans la liste à faire les tâches qu'un sujet attend de
// Mathias, sans redonner celles qui en sont déjà sorties (même cochées). Une
// tâche garde le sujet d'où elle vient, et le message qui l'a fait naître :
// « mail de Satish » se relit sans rouvrir la boîte. Chacune est aussi posée
// en carte « à faire » sur le tableau de HQ, là où Mathias suit son travail.
// Rend combien sont inscrites.
func (s *Server) inscrireTaches(ctx context.Context, tb *userToolbox, sujet bson.ObjectID, n store.Notification, premier *triage.Item, taches []tacheJugee, deja []store.Todo) int {
	vues := map[string]bool{}
	for _, t := range deja {
		vues[cleTache(t.Title)] = true
	}
	var src *store.TodoSource
	if premier != nil {
		src = &store.TodoSource{Origine: origin(*premier), De: premier.De, Titre: n.Lieu}
	}
	note := n.AFaire
	if n.Resume != "" {
		note += " " + n.Resume
	}
	inscrites := 0
	for _, t := range taches {
		titre := strings.TrimSpace(t.Titre)
		cle := cleTache(titre)
		if cle == "" || vues[cle] {
			continue
		}
		vues[cle] = true
		todo := store.Todo{
			UserID: tb.user.ID, Title: titre, Note: truncateRunes(strings.TrimSpace(note), 400),
			Source: src, Sujet: &sujet,
		}
		// Une échéance illisible laisse la tâche sans jour plutôt que de la
		// perdre.
		if due, timed, err := assistant.ParseDue(t.Echeance, tb.location()); err == nil && !due.IsZero() {
			todo.Due, todo.Timed = &due, timed
		}
		if _, err := s.store.SaveTodo(ctx, todo); err != nil {
			slog.Warn("veille : tâche non inscrite", "titre", titre, "err", err)
			continue
		}
		inscrites++
		if _, err := s.store.CreerCarte(ctx, carteDeTache(titre, todo.Due, sujet, n, premier, time.Now().In(tb.location()))); err != nil {
			slog.Warn("veille : carte non posée", "titre", titre, "err", err)
		}
	}
	return inscrites
}

// carteDeTache : la carte du tableau pour une tâche que la veille tire d'un
// sujet. Elle n'a pas d'agent : c'est à Mathias de la faire, ou de la confier.
// Ce qui tombe aujourd'hui ou demain est urgent ; un sujet important rend la
// carte haute.
func carteDeTache(titre string, due *time.Time, sujet bson.ObjectID, n store.Notification, premier *triage.Item, maintenant time.Time) store.Carte {
	var desc []string
	if n.AFaire != "" {
		desc = append(desc, n.AFaire)
	}
	if n.Resume != "" {
		desc = append(desc, n.Resume)
	}
	if premier != nil {
		de := origin(*premier) + " · " + premier.De
		if n.Lieu != "" && n.Lieu != origin(*premier) {
			de += " · " + n.Lieu
		}
		desc = append(desc, "Vient de : "+de)
	}
	c := store.Carte{
		Titre: truncateRunes(titre, 199), Description: truncateRunes(strings.Join(desc, "\n\n"), 7999),
		Statut: store.CarteAFaire, Priorite: store.PrioriteNormale, Sujet: &sujet,
	}
	if n.Importance == "haute" {
		c.Priorite = store.PrioriteHaute
	}
	if due != nil {
		// Le tableau garde le jour seul, à minuit UTC.
		l := due.In(maintenant.Location())
		jour := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
		c.Echeance = &jour
		demain := time.Date(maintenant.Year(), maintenant.Month(), maintenant.Day()+1, 0, 0, 0, 0, time.UTC)
		if !jour.After(demain) {
			c.Priorite = store.PrioriteUrgente
		}
	}
	return c
}

// cleTache : un titre de tâche ramené à ce qui le distingue, pour ne pas
// inscrire deux fois la même à la casse ou aux espaces près.
func cleTache(titre string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimRight(strings.TrimSpace(titre), ".!"))), " ")
}

// veilleMailsLus : les nouveaux mails dont la veille lit le contenu à chaque
// relève. Au-delà, ils sont jugés sur leur objet.
const veilleMailsLus = 8

// lireMailsNeufs met le contenu des nouveaux mails — et les derniers messages
// de leur fil — dans leur aperçu, pour que le jugement dise l'essentiel au
// lieu de deviner d'après l'objet. Le mail est lu sans être marqué comme lu.
func (s *Server) lireMailsNeufs(ctx context.Context, user *store.User, neufs []triage.Item) {
	creds, err := s.gandiCreds(ctx, user)
	if err != nil {
		return
	}
	lus := 0
	for i := range neufs {
		if neufs[i].Ref == "" || lus >= veilleMailsLus {
			continue
		}
		lus++
		f, err := gandi.ReadByID(ctx, creds, neufs[i].Ref)
		if err != nil {
			slog.Warn("veille : mail illisible", "err", err)
			continue
		}
		var b strings.Builder
		b.WriteString(strings.TrimSpace(f.Body))
		// Une réponse ne se comprend qu'avec ce à quoi elle répond : le
		// message juste avant est donné plus largement, et ce que Mathias a
		// écrit lui-même est marqué « toi » — « il répond oui à ta question »
		// n'existe que si l'on sait qui a posé la question.
		if n := len(f.Thread); n > 0 {
			if n > 1 {
				b.WriteString("\n    plus tôt dans le fil :")
				for _, t := range f.Thread[max(0, n-3) : n-1] {
					fmt.Fprintf(&b, "\n      %s : %s", auteurDuFil(t), truncateRunes(strings.TrimSpace(t.Excerpt), 250))
				}
			}
			t := f.Thread[n-1]
			fmt.Fprintf(&b, "\n    il répond à ce message de %s : %s", auteurDuFil(t), truncateRunes(strings.TrimSpace(t.Excerpt), 700))
		}
		if apercu := strings.TrimSpace(b.String()); apercu != "" {
			neufs[i].Apercu = apercu
		}
	}
}

// veilleConversationsLues : les conversations Slack ou WhatsApp que la veille
// relit à chaque relève, et combien de leurs derniers messages.
const (
	veilleConversationsLues = 6
	veilleMessagesLus       = 12
)

// lireConversationsNeuves remplace l'extrait d'une conversation par ses
// derniers messages, ceux de Mathias marqués « toi ». Un « Sur la clé secrète
// d'Olivier » ne veut rien dire seul ; à la suite de la question qu'il
// répond, si — et on voit aussi quand Mathias a déjà répondu.
func (s *Server) lireConversationsNeuves(ctx context.Context, user *store.User, source string, neufs []triage.Item) {
	lues := 0
	for i := range neufs {
		if lues >= veilleConversationsLues || strings.TrimSpace(neufs[i].Titre) == "" {
			continue
		}
		lues++
		lignes, err := s.lireConversation(ctx, user, source, neufs[i].Titre)
		if err != nil {
			slog.Warn("veille : conversation illisible", "source", source, "conversation", neufs[i].Titre, "err", err)
			continue
		}
		if len(lignes) > 0 {
			neufs[i].Apercu = formatConversation(lignes)
		}
	}
}

// ligneConv : un message d'une conversation Slack ou WhatsApp.
type ligneConv struct {
	qui, texte string
	toi        bool
	quand      time.Time
}

// lireConversation lit les derniers messages d'une conversation, du plus
// ancien au plus récent. Lecture directe, pas par les outils de Raoul : eux
// notent ce qu'ils lisent dans sa mémoire des échanges, et la veille ne doit
// pas lui faire croire qu'il a déjà lu une conversation.
func (s *Server) lireConversation(ctx context.Context, user *store.User, source, titre string) ([]ligneConv, error) {
	var out []ligneConv
	switch source {
	case "slack":
		creds, err := s.slackCreds(ctx, user)
		if err != nil {
			return nil, err
		}
		_, msgs, err := slack.New(creds.UserToken).ReadConversation(ctx, titre, veilleMessagesLus)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			out = append(out, ligneConv{qui: m.Auteur, texte: m.Texte, toi: m.DeToi, quand: m.Quand})
			// Les réponses dans un fil : c'est souvent là qu'on répond.
			for _, r := range m.Fil {
				out = append(out, ligneConv{qui: r.Auteur, texte: "(dans le fil) " + r.Texte, toi: r.DeToi, quand: r.Quand})
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].quand.Before(out[j].quand) })
	case "whatsapp":
		stored, err := s.store.WhatsAppChats(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		chats := make([]whatsapp.Chat, 0, len(stored))
		for _, c := range stored {
			chats = append(chats, whatsapp.Chat{JID: c.JID, Name: c.Name, IsGroup: c.IsGroup, LastAt: c.LastAt, LastReadAt: c.LastReadAt})
		}
		chat, _, ok := whatsapp.MatchChat(chats, titre)
		if !ok {
			return nil, fmt.Errorf("conversation introuvable : %s", titre)
		}
		msgs, err := s.store.WhatsAppMessages(ctx, user.ID, chat.JID, time.Time{}, veilleMessagesLus)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			out = append(out, ligneConv{qui: m.Sender, texte: m.Body, toi: m.FromMe, quand: m.Timestamp})
		}
	}
	return out, nil
}

func formatConversation(lignes []ligneConv) string {
	var b strings.Builder
	b.WriteString("derniers messages de la conversation :")
	for _, l := range lignes {
		qui := l.qui
		if l.toi {
			qui = "toi (Mathias)"
		}
		fmt.Fprintf(&b, "\n      %s : %s", qui, truncateRunes(strings.TrimSpace(l.texte), 300))
	}
	if len(lignes) > 0 && lignes[len(lignes)-1].toi {
		b.WriteString("\n      (le dernier message est de toi : tu as déjà répondu)")
	}
	return b.String()
}

// veilleSuivisMax : les sujets ouverts dont la veille relit la discussion à
// chaque relève, pour voir si Mathias y a agi.
const veilleSuivisMax = 5

// sujetsSuivis : les sujets qui attendent quelque chose de Mathias et dans
// la discussion desquels il a écrit depuis. Personne ne lui écrit, donc rien
// de neuf ne remonte par le tri — c'est pourtant là qu'il envoie le fichier
// demandé ou répond à la question. Ces suivis passent au jugement comme un
// message, et le sujet se met à jour (souvent : réglé).
func (s *Server) sujetsSuivis(ctx context.Context, user *store.User, agent, source string) []triage.Item {
	ouverts, err := s.store.SujetsOuverts(ctx, user.ID, agent, time.Now().Add(-veilleSujetDuree))
	if err != nil {
		return nil
	}
	lus := map[string]bool{}
	var out []triage.Item
	for _, o := range ouverts {
		if o.AFaire == "" || o.Lieu == "" || lus[o.Lieu] || len(lus) >= veilleSuivisMax {
			continue
		}
		lus[o.Lieu] = true
		lignes, err := s.lireConversation(ctx, user, source, o.Lieu)
		if err != nil {
			slog.Warn("veille : suivi illisible", "source", source, "conversation", o.Lieu, "err", err)
			continue
		}
		var dernier time.Time
		for _, l := range lignes {
			if l.toi && l.quand.After(o.DernierPoint()) && l.quand.After(dernier) {
				dernier = l.quand
			}
		}
		if dernier.IsZero() {
			continue
		}
		out = append(out, triage.Item{
			Source: source, Titre: o.Lieu, De: "toi", Quand: dernier,
			Apercu: "SUIVI : tu as écrit dans cette discussion depuis le dernier point du sujet.\n    " + formatConversation(lignes),
		})
	}
	return out
}

func auteurDuFil(t gandi.ThreadMessage) string {
	if t.Mine {
		return "toi (Mathias)"
	}
	return t.From
}

// privesOublies : les messages privés que le jugement n'a pas retenus.
func privesOublies(neufs []triage.Item, couverts map[int]bool) []triage.Item {
	var out []triage.Item
	for i, it := range neufs {
		if !couverts[i] && it.Motif == triage.ReasonDM {
			out = append(out, it)
		}
	}
	return out
}

func plural(n int, many, one string) string {
	if n > 1 {
		return many
	}
	return one
}

// -- Notifications dans Cerveau HQ ------------------------------------------------

func (s *Server) handleHQNotifications(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	list, nonLues, err := s.store.Notifications(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture des notifications impossible")
		return
	}
	if list == nil {
		list = []store.Notification{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"notifications": list, "non_lues": nonLues})
}

func (s *Server) handleHQNotificationLue(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var id *bson.ObjectID
	if raw := chi.URLParam(r, "id"); raw != "" {
		oid, err := bson.ObjectIDFromHex(raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "identifiant invalide")
			return
		}
		id = &oid
	}
	if err := s.store.MarquerNotificationsLues(r.Context(), user.ID, id); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "notification non marquée")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
