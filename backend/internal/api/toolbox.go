package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// userToolbox donne à l'assistant l'accès aux données d'UN utilisateur.
type userToolbox struct {
	srv  *Server
	user *store.User
}

func (s *Server) toolbox(user *store.User) *userToolbox {
	return &userToolbox{srv: s, user: user}
}

func (t *userToolbox) CalendarEvents(ctx context.Context, start, end time.Time) ([]assistant.EventView, error) {
	events, err := t.srv.store.EventsBetween(ctx, t.user.ID, start, end)
	if err != nil {
		return nil, err
	}
	loc := t.location()
	out := make([]assistant.EventView, 0, len(events))
	for _, e := range events {
		out = append(out, assistant.EventView{
			Titre:   e.Title,
			Debut:   e.Start.In(loc).Format(time.RFC3339),
			Fin:     e.End.In(loc).Format(time.RFC3339),
			Lieu:    e.Location,
			Journee: e.AllDay,
		})
	}
	return out, nil
}

func (t *userToolbox) UnreadEmails(ctx context.Context, limit int) ([]assistant.EmailView, error) {
	creds, err := t.srv.gandiCreds(ctx, t.user)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("la boîte mail Gandi n'est pas connectée")
		}
		return nil, err
	}
	mails, err := gandi.Unread(ctx, creds, limit)
	if err != nil {
		t.srv.store.MarkConnectionError(ctx, t.user.ID, store.ProviderGandi, err.Error())
		return nil, err
	}
	out := make([]assistant.EmailView, 0, len(mails))
	for _, m := range mails {
		out = append(out, assistant.EmailView{
			De:    m.From,
			Objet: m.Subject,
			Recu:  t.when(m.Date),
		})
	}
	return out, nil
}

// ReadEmail rapatrie le corps d'un mail. Séparé de UnreadEmails à dessein :
// descendre un corps coûte un aller-retour IMAP de plus, et la plupart des
// questions n'en ont pas besoin.
func (t *userToolbox) ReadEmail(ctx context.Context, query string, unreadOnly bool) (assistant.EmailContentView, error) {
	creds, err := t.srv.gandiCreds(ctx, t.user)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return assistant.EmailContentView{}, errors.New("la boîte mail Gandi n'est pas connectée")
		}
		return assistant.EmailContentView{}, err
	}
	msg, err := gandi.Read(ctx, creds, query, unreadOnly)
	if err != nil {
		var amb *gandi.AmbiguousSenderError
		if errors.As(err, &amb) {
			return assistant.EmailContentView{}, &assistant.AmbiguousError{
				Quoi: "expéditeur", Recherche: amb.Query, Choix: amb.Senders,
			}
		}
		return assistant.EmailContentView{}, err
	}
	return assistant.EmailContentView{
		De:      msg.From,
		Objet:   msg.Subject,
		Recu:    t.when(msg.Date),
		Contenu: msg.Body,
		Tronque: strings.HasSuffix(msg.Body, "…"),
	}, nil
}

func (t *userToolbox) UnreadSlack(ctx context.Context, limit int) ([]assistant.SlackView, error) {
	creds, err := t.srv.slackCreds(ctx, t.user)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("Slack n'est pas connecté")
		}
		return nil, err
	}
	threads, warnings, err := slack.New(creds.UserToken).RecentActivity(ctx, limit, slack.DefaultChannelWindow)
	if err != nil {
		t.srv.store.MarkConnectionError(ctx, t.user.ID, store.ProviderSlack, err.Error())
		return nil, err
	}
	// Une conversation illisible (scope manquant) ne doit pas se traduire par un
	// silencieux « aucun message » : on la remonte au modèle.
	for _, w := range warnings {
		slog.Warn("slack : conversation ignorée", "detail", w)
	}

	out := make([]assistant.SlackView, 0, len(threads))
	for _, th := range threads {
		out = append(out, assistant.SlackView{
			Canal:           th.Channel,
			Type:            th.Kind,
			NonLus:          th.Unread,
			MessagesRecents: th.Recent,
			Mentions:        th.Mentions,
			Dernier:         t.when(th.Latest),
			Extraits:        th.Messages,
		})
	}
	return out, nil
}

// ReadSlackChannel lit une conversation à la demande, sans condition de
// non-lu : c'est ce qui permet de répondre à « le dernier message dans #X ».
func (t *userToolbox) ReadSlackChannel(ctx context.Context, name string, limit int) (assistant.SlackChannelView, error) {
	creds, err := t.srv.slackCreds(ctx, t.user)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return assistant.SlackChannelView{}, errors.New("Slack n'est pas connecté")
		}
		return assistant.SlackChannelView{}, err
	}
	label, messages, err := slack.New(creds.UserToken).ReadConversation(ctx, name, limit)
	if err != nil {
		var amb *slack.AmbiguousConversationError
		if errors.As(err, &amb) {
			return assistant.SlackChannelView{}, &assistant.AmbiguousError{
				Quoi: "conversation", Recherche: amb.Query, Choix: amb.Choices,
			}
		}
		return assistant.SlackChannelView{}, err
	}
	out := assistant.SlackChannelView{Canal: label}
	for _, m := range messages {
		out.Messages = append(out.Messages, assistant.SlackMessageView{
			Auteur: m.Auteur, Texte: m.Texte, Quand: t.when(m.Quand),
		})
	}
	return out, nil
}

func (t *userToolbox) UnreadWhatsApp(ctx context.Context, limit int) ([]assistant.WhatsAppView, error) {
	if _, err := t.srv.whatsappCreds(ctx, t.user); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("WhatsApp Business n'est pas connecté")
		}
		return nil, err
	}
	msgs, err := t.srv.store.UnreadWhatsApp(ctx, t.user.ID, int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]assistant.WhatsAppView, 0, len(msgs))
	for _, m := range msgs {
		from := m.FromName
		if from == "" {
			from = m.From
		}
		out = append(out, assistant.WhatsAppView{
			De:      from,
			Message: m.Body,
			Recu:    t.when(m.Timestamp),
		})
	}
	return out, nil
}

// CreateEvent enregistre l'événement côté serveur (pour que les vérifications
// suivantes le voient tout de suite) et renvoie l'action que l'app exécutera
// dans EventKit — seule l'app peut écrire dans le calendrier du téléphone.
func (t *userToolbox) CreateEvent(ctx context.Context, draft assistant.EventDraft) (store.Action, error) {
	if draft.Titre == "" {
		return store.Action{}, errors.New("titre d'événement manquant")
	}
	if !draft.Fin.After(draft.Debut) {
		draft.Fin = draft.Debut.Add(time.Hour)
	}

	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return store.Action{}, err
	}
	externalID := "cerveau:" + hex.EncodeToString(buf)

	err := t.srv.store.InsertEvent(ctx, store.CalendarEvent{
		UserID:     t.user.ID,
		ExternalID: externalID,
		Calendar:   "Cerveau",
		Title:      draft.Titre,
		Location:   draft.Lieu,
		Start:      draft.Debut,
		End:        draft.Fin,
	})
	if err != nil {
		return store.Action{}, fmt.Errorf("enregistrement de l'événement : %w", err)
	}

	return store.Action{
		Type: "create_event",
		Payload: map[string]any{
			"external_id": externalID,
			"title":       draft.Titre,
			"start":       draft.Debut.Format(time.RFC3339),
			"end":         draft.Fin.Format(time.RFC3339),
			"location":    draft.Lieu,
			"notes":       draft.Note,
		},
	}, nil
}

// when met en mots un horodatage, dans le fuseau de l'utilisateur et par
// rapport à maintenant. Tout ce qui descend une date au modèle passe par ici :
// le modèle recopie, il ne calcule pas.
func (t *userToolbox) when(ts time.Time) string {
	return assistant.When(ts, time.Now(), t.location())
}

func (t *userToolbox) location() *time.Location {
	tz := t.user.Timezone
	if tz == "" {
		tz = t.srv.cfg.DefaultTimezone
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}
