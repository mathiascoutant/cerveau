package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// Profondeur d'inspection. On regarde large avant de filtrer serré : le tri
// écarte l'essentiel, donc se limiter à quinze mails en entrée reviendrait
// souvent à n'en garder aucun.
const (
	urgentMailDepth  = 40
	urgentSlackDepth = 30
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
//
// Deux étages, et la séparation est volontaire :
//
//   - le tri (internal/triage) écarte tout ce qui ne s'adresse pas
//     personnellement à l'utilisateur. Déterministe, testé, sans modèle : « suis-je
//     dans le champ À ? » ne demande pas de savoir raisonner, et un filtre qui
//     change d'avis d'un appel à l'autre n'est pas un filtre ;
//   - la synthèse (assistant.Tasks) fait ce qu'aucune règle ne sait faire :
//     reconnaître que trois messages parlent du même sujet, écarter ce qui
//     n'attend aucune action, et nommer ce qui reste en six mots.
//
// L'appel au modèle est mis en cache sur l'empreinte des messages : tant que
// rien de neuf n'est arrivé, la liste ne peut pas avoir changé.
func (s *Server) handleUrgent(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	tb := s.toolbox(user)
	src := s.sources(ctx, user)

	out := urgentResponse{Taches: []assistant.TaskView{}, Sources: []string{}, GeneratedAt: time.Now()}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		fromMail []triage.Item
		fromChat []triage.Item
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
	wg.Wait()

	retained := triage.Merge(urgentMessages, fromMail, fromChat)
	if len(retained) == 0 {
		httpx.JSON(w, http.StatusOK, out)
		return
	}

	messages := make([]assistant.MessageView, 0, len(retained))
	for _, item := range retained {
		messages = append(messages, assistant.MessageView{
			Origine: origin(item),
			De:      item.De,
			Titre:   item.Titre,
			Extrait: item.Apercu,
			Quand:   tb.when(item.Quand),
		})
	}

	print := fingerprint(retained)
	cached, err := s.store.LatestTasks(ctx, user.ID)
	fresh := err == nil && cached.Fingerprint == print && time.Since(cached.GeneratedAt) < tasksMaxAge
	if fresh && r.URL.Query().Get("refresh") == "" {
		if err := json.Unmarshal([]byte(cached.Payload), &out.Taches); err == nil {
			out.GeneratedAt = cached.GeneratedAt
			httpx.JSON(w, http.StatusOK, out)
			return
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
				httpx.JSON(w, http.StatusOK, out)
				return
			}
		}
		httpx.Error(w, http.StatusBadGateway, "liste à traiter indisponible")
		return
	}

	if tasks != nil {
		out.Taches = tasks
	}
	if payload, err := json.Marshal(out.Taches); err == nil {
		_ = s.store.SaveTasks(ctx, user.ID, string(payload), print)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// origin nomme la provenance d'un message pour le modèle. Un mail dit « mail »,
// une conversation Slack dit son canal — c'est ce qui permet à la source
// affichée dans l'app de ressembler à ce que l'utilisateur voit dans Slack.
func origin(item triage.Item) string {
	if item.Source == "mail" {
		return "mail"
	}
	return item.Titre
}

// fingerprint résume les messages retenus en une empreinte stable.
//
// Elle ne dépend que de ce qui identifie un message — sa provenance, son
// expéditeur, son titre, son instant. Ni l'ordre d'arrivée des sources ni les
// compteurs n'y entrent : ils bougent sans que rien de neuf ne soit arrivé, et
// feraient rappeler le modèle pour rien.
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
		out = append(out, triage.Mail{
			De:        m.From,
			Adresse:   m.FromAddr,
			Objet:     m.Subject,
			Date:      m.Date,
			Pour:      m.To,
			Copie:     m.Cc,
			Diffusion: m.Bulk,
		})
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
