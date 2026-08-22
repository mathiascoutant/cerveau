package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// Nombre d'entrées rendues à l'app. L'écran d'accueil montre une petite liste,
// pas une boîte de réception : au-delà d'une dizaine, on ne la lit plus, on la
// balaie — et un écran qu'on balaie ne sert plus à décider quoi faire.
const urgentLimit = 8

// Profondeur d'inspection. On regarde large avant de filtrer serré : le tri
// écarte l'essentiel, donc se limiter à quinze mails en entrée reviendrait
// souvent à n'en garder aucun.
const (
	urgentMailDepth  = 40
	urgentSlackDepth = 30
)

type urgentItem struct {
	Source string `json:"source"` // "mail" | "slack"
	Titre  string `json:"titre"`
	De     string `json:"de,omitempty"`
	Apercu string `json:"apercu,omitempty"`
	// Quand est déjà mis en mots dans le fuseau de l'utilisateur : l'app
	// affiche, elle ne recalcule pas.
	Quand  string `json:"quand,omitempty"`
	Motif  string `json:"motif"` // "direct" | "dm" | "mention"
	Compte int    `json:"compte,omitempty"`
}

type urgentResponse struct {
	Items []urgentItem `json:"items"`
	// Sources réellement interrogées : sans elles, une liste vide serait
	// ambiguë — « rien d'urgent » et « rien de branché » se ressemblent trop.
	Sources     []string  `json:"sources"`
	Unavailable []string  `json:"unavailable,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

// handleUrgent rend ce qui n'a pas encore été traité et qui s'adresse
// personnellement à l'utilisateur : mails dont il est le destinataire nommé,
// messages privés et mentions Slack.
//
// Aucun appel au modèle ici. Les règles sont dans internal/triage, elles sont
// déterministes et testées : l'écran d'accueil doit s'afficher tout de suite,
// et « suis-je dans le champ À ? » ne demande pas de savoir raisonner.
func (s *Server) handleUrgent(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	tb := s.toolbox(user)
	src := s.sources(ctx, user)

	out := urgentResponse{Items: []urgentItem{}, Sources: []string{}, GeneratedAt: time.Now()}

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

	for _, item := range triage.Merge(urgentLimit, fromMail, fromChat) {
		out.Items = append(out.Items, urgentItem{
			Source: item.Source,
			Titre:  item.Titre,
			De:     item.De,
			Apercu: item.Apercu,
			Quand:  tb.when(item.Quand),
			Motif:  string(item.Motif),
			Compte: item.Compte,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
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
