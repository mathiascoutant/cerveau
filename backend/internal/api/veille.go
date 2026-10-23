package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
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

Réponds UNIQUEMENT par un objet JSON : {"sujets": [{"sujet": "S1" ou "" pour un nouveau sujet, "messages": [numéros], "titre": "<l'affaire en clair, 8 mots au plus, sans « Re: » ni nom d'expéditeur recopié de l'objet>", "resume": "<l'ESSENTIEL du contenu, deux phrases au plus>", "a_faire": "<ce qu'on attend de Mathias, une phrase ; vide si rien>", "echanges": [{"qui": "<prénom>", "a_qui": "<prénom, ou vide>", "quoi": "<ce qu'il a dit, une phrase courte>"}], "importance": "haute" ou "moyenne"}]}.
"resume" : va droit au fond — ce qui est dit, demandé, décidé, livré, les chiffres, les dates, les noms de fichiers. Mathias voit déjà le titre et le lieu : ne redis jamais qui écrit, que c'est une réponse, ni l'objet du mail. Bon : « La v7.2 corrige les deux doublons ; Jonata attend la validation pour lancer l'import vendredi. » Mauvais : « Olivier répond sur le sujet Condor 1126 ; c'est une réponse à un échange en cours. » Si tu n'as vraiment que l'objet, dis en quelques mots ce qu'il annonce — jamais que le contenu manque.
Une réponse se résume PAR RAPPORT à ce à quoi elle répond (« il répond à ce message de… », les messages d'avant) : la question, la demande, la proposition, et ce qu'on y répond. Bon : « Il répond oui à ta demande : il est dispo samedi matin. » ; « Elle refuse le devis que tu lui as envoyé : trop cher de 15 %%, elle attend une nouvelle offre. » Ce que Mathias a écrit lui-même est marqué « toi ».
"echanges" : qui a dit quoi dans les NOUVEAUX messages, une ligne par prise de parole qui compte, dans l'ordre — par exemple Adélaïde dit à Sarah que le chiffre est faux, puis Sarah lui répond qu'elle a raison. L'essentiel, pas une recopie. Mathias s'écrit « toi ».
Regroupe dans un même sujet les messages qui parlent de la même chose. Liste vide si rien n'est important et que rien ne continue un sujet ouvert. En français.`

type jugement struct {
	Sujets []struct {
		Sujet      string   `json:"sujet"`
		Messages   []int    `json:"messages"`
		Titre      string   `json:"titre"`
		Resume     string   `json:"resume"`
		AFaire     string   `json:"a_faire"`
		Echanges   []parole `json:"echanges"`
		Importance string   `json:"importance"`
	} `json:"sujets"`
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
	if len(neufs) == 0 {
		return nil
	}
	if len(neufs) > veilleMaxMessages {
		neufs = neufs[:veilleMaxMessages]
	}

	// Un mail ne se juge pas sur son objet : on en lit le contenu, et ce
	// qui le précède dans le fil.
	if source == "mail" {
		s.lireMailsNeufs(ctx, user, neufs)
	}

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

	tb := s.toolbox(user)
	dir := tb.annuaire(ctx)
	var b strings.Builder
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
	couverts := map[int]bool{}
	// noter range un sujet : il fait avancer un sujet ouvert (même numéro
	// rendu par le jugement, ou même discussion), ou il en ouvre un.
	noter := func(cible int, n store.Notification) {
		if cible >= 0 && cible < len(ouverts) {
			if err := s.store.MettreAJourSujet(ctx, ouverts[cible].ID, n); err != nil {
				slog.Warn("veille : sujet non mis à jour", "err", err)
				return
			}
			titres = append(titres, n.Titre+" (mis à jour)")
			return
		}
		ins, err := s.store.InsertNotification(ctx, n)
		if err != nil {
			slog.Warn("veille : notification non enregistrée", "err", err)
			return
		}
		ouverts = append(ouverts, ins)
		if n.Fil != "" {
			parFil[n.Fil] = len(ouverts) - 1
		}
		titres = append(titres, n.Titre)
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
		var quand time.Time
		for _, n := range suj.Messages {
			if n >= 1 && n <= len(neufs) {
				couverts[n-1] = true
				it := neufs[n-1]
				ref := origin(it) + " · " + it.De
				if it.Titre != "" {
					ref += " · " + it.Titre
				}
				refs = append(refs, ref)
				if fil == "" {
					fil, lieu = filDe(it), lieuDe(it)
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
		noter(cible, store.Notification{
			UserID: user.ID, Agent: agent.ID, Source: source, Titre: titre,
			Resume: strings.TrimSpace(suj.Resume), AFaire: strings.TrimSpace(suj.AFaire), Importance: importance,
			Messages: refs, Run: run.ID().Hex(), Fil: fil, Lieu: lieu, Echanges: echanges,
		})
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
		noter(cible, n)
	}
	reply := "Rien d'important."
	if len(titres) > 0 {
		reply = fmt.Sprintf("%d sujet%s : %s.", len(titres), plural(len(titres), "s", ""), strings.Join(titres, " ; "))
	}
	run.Fin(assistant.Result{Reply: reply}, nil)
	return nil
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
