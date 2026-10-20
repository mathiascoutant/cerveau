package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
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
// l'agent se met au travail — et le bâtiment le montre — pour juger, avec le
// modèle rapide, ce qui mérite une notification.
//
// La toute première relève d'un agent ne signale rien : elle note ce qui est
// déjà là, pour ne pas réveiller Mathias avec trois jours de messages.

const (
	veilleDefautMinutes = 10
	veilleTimeout       = 3 * time.Minute
	veilleMaxMessages   = 25
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
			if !connected || !ok {
				continue
			}
			vctx, cancel := context.WithTimeout(ctx, veilleTimeout)
			if err := s.veiller(vctx, user, agent, v.Source, v.Label); err != nil {
				slog.Warn("veille : relève en échec", "agent", agent.ID, "err", err)
			}
			cancel()
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

const veilleConsigne = `Tu fais la veille %s de Mathias. Voici les NOUVEAUX messages qui lui sont adressés depuis ta dernière relève.
Signale seulement ce qui demande vraiment son attention : une question ou une demande qui attend sa réponse, une décision à prendre, un problème, une échéance proche, ou un message d'une personne importante pour lui (direction, client).
Ignore les remerciements, les « ok », les informations de routine, les notifications automatiques et ce qui ne lui demande rien.
Réponds UNIQUEMENT par un objet JSON : {"importants": [{"messages": [numéros], "titre": "<qui et quoi, 8 mots au plus>", "resume": "<une phrase : ce qu'il se passe et ce qu'on attend de lui>", "importance": "haute" ou "moyenne"}]}.
Regroupe dans une même entrée les messages qui parlent de la même chose. Liste vide si rien n'est important. En français.`

type jugement struct {
	Importants []struct {
		Messages   []int  `json:"messages"`
		Titre      string `json:"titre"`
		Resume     string `json:"resume"`
		Importance string `json:"importance"`
	} `json:"importants"`
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

	// Il y a du neuf : l'agent se met au travail, et le bâtiment le voit.
	demande := fmt.Sprintf("Relève %s : %d nouveau%s message%s à juger", label, len(neufs), plural(len(neufs), "x", ""), plural(len(neufs), "s", ""))
	run := s.runs.begin(ctx, user.ID, agent.ID, store.RunSourceVeille, demande, nil)
	run.Etape(assistant.Event{Type: "status", Tool: "veille", Label: "Lecture des nouveaux messages"})
	started := time.Now()

	tb := s.toolbox(user)
	dir := tb.annuaire(ctx)
	var b strings.Builder
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
		if it.Titre != "" {
			fmt.Fprintf(&b, "    sujet : %s\n", it.Titre)
		}
		if it.Apercu != "" {
			fmt.Fprintf(&b, "    extrait : %s\n", truncateRunes(it.Apercu, 600))
		}
	}
	out, err := s.engine.Classify(ctx, fmt.Sprintf(veilleConsigne, label), b.String())
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
	for _, imp := range j.Importants {
		titre := strings.TrimSpace(imp.Titre)
		if titre == "" {
			continue
		}
		importance := imp.Importance
		if importance != "haute" {
			importance = "moyenne"
		}
		var refs []string
		for _, n := range imp.Messages {
			if n >= 1 && n <= len(neufs) {
				it := neufs[n-1]
				ref := origin(it) + " · " + it.De
				if it.Titre != "" {
					ref += " · " + it.Titre
				}
				refs = append(refs, ref)
			}
		}
		if _, err := s.store.InsertNotification(ctx, store.Notification{
			UserID: user.ID, Agent: agent.ID, Source: source, Titre: titre,
			Resume: strings.TrimSpace(imp.Resume), Importance: importance, Messages: refs, Run: run.ID().Hex(),
		}); err != nil {
			slog.Warn("veille : notification non enregistrée", "err", err)
			continue
		}
		titres = append(titres, titre)
	}
	reply := "Rien d'important."
	if len(titres) > 0 {
		reply = fmt.Sprintf("%d information%s importante%s : %s.", len(titres), plural(len(titres), "s", ""), plural(len(titres), "s", ""), strings.Join(titres, " ; "))
	}
	run.Fin(assistant.Result{Reply: reply}, nil)
	return nil
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
