package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// Profondeur d'inspection. On regarde large avant de filtrer serré : le tri
// écarte l'essentiel, donc se limiter à quinze mails en entrée reviendrait
// souvent à n'en garder aucun.
const (
	urgentMailDepth     = 40
	urgentSlackDepth    = 30
	urgentWhatsAppDepth = 25
	// Messages descendus au modèle. Au-delà il ne synthétise plus, il résume.
	urgentMessages = 18
)

// Filet de sécurité du cache. L'empreinte des messages suffit d'ordinaire à
// décider ; cette durée rattrape le cas où rien n'a bougé mais où le temps, lui,
// a passé — « répondre avant ce soir » ne veut plus dire la même chose demain.
const tasksMaxAge = 6 * time.Hour

type urgentResponse struct {
	Taches []assistant.TaskView `json:"taches"`
	// Sources réellement interrogées : sans elles, une liste vide serait
	// ambiguë — « rien à traiter » et « rien de branché » se ressemblent trop.
	Sources     []string  `json:"sources"`
	Unavailable []string  `json:"unavailable,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

// handleUrgent rend ce qu'il reste à faire, pas ce qu'il reste à lire.
func (s *Server) handleUrgent(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	out, err := s.urgentList(ctx, user, r.URL.Query().Get("refresh") != "")
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// handleUrgentDone retire une urgence de la liste.
//
// Le geste existe à l'écran comme à la voix, et il doit valoir la même chose
// des deux côtés : c'est la même empreinte qui part en base, donc la ligne
// écartée d'un balayage ne revient pas parce que Raoul a régénéré la liste.
func (s *Server) handleUrgentDone(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		httpx.Error(w, http.StatusBadRequest, "identifiant manquant")
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if err := s.store.DismissUrgent(r.Context(), user.ID, id, body.Action); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "impossible d'enregistrer")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"id": id})
}

// urgentList construit ce qu'il reste à faire.
//
// Trois étages, et la séparation est volontaire :
//
//   - le tri (internal/triage) écarte tout ce qui ne s'adresse pas
//     personnellement à l'utilisateur. Déterministe, testé, sans modèle : « suis-je
//     dans le champ À ? » ne demande pas de savoir raisonner, et un filtre qui
//     change d'avis d'un appel à l'autre n'est pas un filtre ;
//   - la synthèse (assistant.Tasks) fait ce qu'aucune règle ne sait faire :
//     reconnaître que trois messages parlent du même sujet, écarter ce qui
//     n'attend aucune action, et nommer ce qui reste en six mots ;
//   - le tamis final retire ce qu'il a déjà déclaré traité. Il vient APRÈS le
//     modèle et pas avant, parce qu'une tâche n'existe qu'une fois nommée :
//     c'est le regroupement par sujet qui décide de ce qu'on écarte, et trois
//     mails sur le même devis se traitent d'un seul « c'est fait ».
//
// L'appel au modèle est mis en cache sur l'empreinte des messages : tant que
// rien de neuf n'est arrivé, la liste ne peut pas avoir changé.
func (s *Server) urgentList(ctx context.Context, user *store.User, refresh bool) (urgentResponse, error) {
	tb := s.toolbox(user)
	src := s.sources(ctx, user)

	out := urgentResponse{Taches: []assistant.TaskView{}, Sources: []string{}, GeneratedAt: time.Now()}

	var (
		mu           sync.Mutex
		wg           sync.WaitGroup
		fromMail     []triage.Item
		fromChat     []triage.Item
		fromWhatsApp []triage.Item
	)
	unavailable := func(label string) {
		mu.Lock()
		out.Unavailable = append(out.Unavailable, label)
		mu.Unlock()
	}

	if src.Mail {
		out.Sources = append(out.Sources, "mail")
		wg.Add(1)
		go func() {
			defer wg.Done()
			mails, moi, err := tb.unreadMail(ctx, urgentMailDepth)
			if err != nil {
				unavailable("mails")
				return
			}
			mu.Lock()
			fromMail = triage.Mails(triageMails(mails), moi)
			mu.Unlock()
		}()
	}
	if src.Slack {
		out.Sources = append(out.Sources, "slack")
		wg.Add(1)
		go func() {
			defer wg.Done()
			threads, err := tb.slackActivity(ctx, urgentSlackDepth)
			if err != nil {
				unavailable("slack")
				return
			}
			mu.Lock()
			fromChat = triage.Slack(triageConversations(threads))
			mu.Unlock()
		}()
	}
	if src.WhatsApp {
		out.Sources = append(out.Sources, "whatsapp")
		wg.Add(1)
		go func() {
			defer wg.Done()
			threads, err := tb.whatsAppThreads(ctx, urgentWhatsAppDepth)
			if err != nil {
				unavailable("whatsapp")
				return
			}
			mu.Lock()
			fromWhatsApp = triage.WhatsApp(triageWhatsApp(threads))
			mu.Unlock()
		}()
	}
	wg.Wait()

	retained := triage.Merge(urgentMessages, fromMail, fromChat, fromWhatsApp)
	if len(retained) == 0 {
		return out, nil
	}

	messages := make([]assistant.MessageView, 0, len(retained))
	for _, item := range retained {
		messages = append(messages, assistant.MessageView{
			Origine: origin(item),
			De:      item.De,
			Titre:   item.Titre,
			Extrait: item.Apercu,
			Quand:   tb.when(item.Quand),
			Motif:   motif(item.Motif),
			Fil:     item.Fil,
		})
	}

	print := fingerprint(retained)
	cached, err := s.store.LatestTasks(ctx, user.ID)
	fresh := err == nil && cached.Fingerprint == print && time.Since(cached.GeneratedAt) < tasksMaxAge
	if fresh && !refresh {
		if err := json.Unmarshal([]byte(cached.Payload), &out.Taches); err == nil {
			out.GeneratedAt = cached.GeneratedAt
			return s.sift(ctx, user, out), nil
		}
		// Cache illisible (format d'une version précédente) : on régénère.
	}

	name := user.Name
	if name == "" {
		name = s.cfg.DefaultUserName
	}
	tasks, genErr := s.engine.Tasks(ctx, messages, time.Now().In(tb.location()), tb.location().String(), name)
	if genErr != nil {
		// Une synthèse qui échoue ne doit pas vider l'écran : on rend la
		// dernière liste connue plutôt qu'un « rien à traiter » mensonger.
		if cached != nil {
			if err := json.Unmarshal([]byte(cached.Payload), &out.Taches); err == nil {
				out.GeneratedAt = cached.GeneratedAt
				return s.sift(ctx, user, out), nil
			}
		}
		return out, errors.New("liste à traiter indisponible")
	}

	if tasks != nil {
		out.Taches = tasks
	}
	// L'identifiant est posé avant la mise en cache : une tâche relue du cache
	// doit porter le même que le jour où elle a été produite, sinon l'app
	// perdrait la sélection en cours à chaque relecture.
	for i := range out.Taches {
		out.Taches[i].ID = taskKey(out.Taches[i])
	}
	if payload, err := json.Marshal(out.Taches); err == nil {
		_ = s.store.SaveTasks(ctx, user.ID, string(payload), print)
	}
	return s.sift(ctx, user, out), nil
}

// sift retire les tâches déjà déclarées traitées, et pose l'identifiant de
// celles qui n'en auraient pas (listes mises en cache avant son existence).
//
// Une erreur de lecture ne fait rien échouer : au pire une ligne réapparaît, ce
// qui se corrige d'un mot, alors qu'un écran vide sur une panne de base se
// lirait comme « tu n'as rien à faire ».
func (s *Server) sift(ctx context.Context, user *store.User, out urgentResponse) urgentResponse {
	done, err := s.store.DismissedUrgents(ctx, user.ID)
	if err != nil {
		slog.Warn("urgences : traitées illisibles", "err", err)
		done = nil
	}
	kept := make([]assistant.TaskView, 0, len(out.Taches))
	for _, t := range out.Taches {
		if t.ID == "" {
			t.ID = taskKey(t)
		}
		if done[t.ID] {
			continue
		}
		kept = append(kept, t)
	}
	out.Taches = kept
	return out
}

// taskKey identifie une tâche par ses sources, jamais par son libellé.
//
// Le modèle reformule « Répondre au mail de Westent » en « Répondre à Westent
// sur le devis » d'une génération à l'autre sans que rien n'ait bougé ; les
// sources, elles, lui sont données et il les recopie. Une tâche écartée reste
// donc écartée même si la phrase qui la nomme a changé.
//
// Sans source — le cas ne devrait pas exister, le schéma l'exige — on retombe
// sur le libellé : un identifiant fragile vaut mieux que pas d'identifiant.
func taskKey(t assistant.TaskView) string {
	h := sha256.New()
	if len(t.Sources) == 0 {
		h.Write([]byte(t.Action))
	}
	for _, src := range t.Sources {
		h.Write([]byte(src.Origine))
		h.Write([]byte{0})
		h.Write([]byte(src.De))
		h.Write([]byte{0})
		h.Write([]byte(src.Titre))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// motif met en mots la raison pour laquelle le tri a retenu un message.
//
// Le modèle ne peut pas la retrouver : il reçoit un expéditeur, un objet et une
// date, jamais les champs À et Copie ni les en-têtes de filiation. Or c'est la
// raison qui décide de l'urgence — une réponse à un mail qu'on a soi-même
// envoyé n'est pas du même ordre qu'un mail où l'on figure parmi six.
func motif(r triage.Reason) string {
	switch r {
	case triage.ReasonReply:
		return "réponse à un mail que tu as envoyé"
	case triage.ReasonDM:
		return "message privé"
	case triage.ReasonMention:
		return "tu es cité nommément"
	case triage.ReasonDirect:
		return "tu es destinataire"
	}
	return ""
}

// origin nomme la provenance d'un message pour le modèle. Un mail dit « mail »,
// une conversation Slack dit son canal — c'est ce qui permet à la source
// affichée dans l'app de ressembler à ce que l'utilisateur voit dans Slack.
func origin(item triage.Item) string {
	switch item.Source {
	case "mail":
		return "mail"
	case "whatsapp":
		// Le service est nommé, contrairement à Slack : un groupe WhatsApp et
		// un canal Slack portent des noms de même allure, et « Azul » tout seul
		// ne dit pas où aller regarder.
		return item.Titre + " (WhatsApp)"
	}
	return item.Titre
}

// fingerprint résume les messages retenus en une empreinte stable.
//
// Elle ne dépend que de ce qui identifie un message — sa provenance, son
// expéditeur, son titre, son instant. Ni l'ordre d'arrivée des sources ni les
// compteurs n'y entrent : ils bougent sans que rien de neuf ne soit arrivé, et
// feraient rappeler le modèle pour rien.
//
// Le motif non plus, et c'est délibéré malgré son influence sur le texte
// produit : il dépend d'une lecture de la boîte d'envoi qui a le droit
// d'échouer sans bruit. Un mail passerait de « réponse à ton mail » à « tu es
// destinataire » et retour au gré d'un incident IMAP, régénérant la liste
// entière pour une nuance de formulation. Quand le motif fait vraiment entrer
// un message dans la liste, c'est la liste qui change, donc l'empreinte aussi.
func fingerprint(items []triage.Item) string {
	h := sha256.New()
	for _, item := range items {
		h.Write([]byte(item.Source))
		h.Write([]byte{0})
		h.Write([]byte(item.De))
		h.Write([]byte{0})
		h.Write([]byte(item.Titre))
		h.Write([]byte{0})
		h.Write([]byte(item.Quand.UTC().Format(time.RFC3339)))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// triageMails traduit les messages IMAP dans le vocabulaire du tri. La
// traduction se fait ici et pas dans internal/triage : ce paquet ne connaît
// aucun fournisseur, c'est ce qui permet de le tester sans boîte mail.
func triageMails(mails []gandi.Message) []triage.Mail {
	out := make([]triage.Mail, 0, len(mails))
	for _, m := range mails {
		out = append(out, toTriageMail(m))
	}
	return out
}

// toTriageMail est la seule traduction d'un message IMAP vers le vocabulaire du
// tri. Elle sert au filtrage des urgences comme au calcul de l'adressage rendu
// au modèle : les deux doivent répondre pareil à « ce mail me vise-t-il ? »,
// sinon Raoul annonce une urgence qu'il décrit ensuite comme une simple copie.
func toTriageMail(m gandi.Message) triage.Mail {
	return triage.Mail{
		De:         m.From,
		Adresse:    m.FromAddr,
		Objet:      m.Subject,
		Date:       m.Date,
		Pour:       m.To,
		Copie:      m.Cc,
		Diffusion:  m.Bulk,
		DansUnFil:  len(m.InReplyTo) > 0,
		RepondAToi: m.AnswersYou,
	}
}

// triageWhatsApp traduit les conversations WhatsApp dans le vocabulaire du tri.
// Un tête-à-tête vaut un message privé : c'est la même chose sous un autre nom.
func triageWhatsApp(threads []store.WhatsAppThread) []triage.Conversation {
	out := make([]triage.Conversation, 0, len(threads))
	for _, th := range threads {
		kind := "dm"
		if th.Chat.IsGroup {
			kind = "groupe"
		}
		conv := triage.Conversation{
			Canal:    th.Chat.Name,
			Type:     kind,
			NonLus:   th.Unread,
			Mentions: th.Mentions,
			Dernier:  th.Chat.LastAt,
		}
		for _, m := range th.Latest {
			conv.Extraits = append(conv.Extraits, m.Sender+" : "+m.Body)
		}
		out = append(out, conv)
	}
	return out
}

func triageConversations(threads []slack.Activity) []triage.Conversation {
	out := make([]triage.Conversation, 0, len(threads))
	for _, th := range threads {
		out = append(out, triage.Conversation{
			Canal:    th.Channel,
			Type:     th.Kind,
			NonLus:   th.Unread,
			Mentions: th.Mentions,
			Dernier:  th.Latest,
			Extraits: th.Messages,
		})
	}
	return out
}
