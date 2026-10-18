package api

import (
	"context"
	"errors"
	"strings"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/tuleap"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// Les outils de recherche : mails envoyés, mails par personne, Slack par
// auteur, tickets CSP. Ce fichier ne fait que traduire — les règles vivent
// dans les fournisseurs, et ce qui descend au modèle est mis en mots ici.

func (t *userToolbox) SentEmails(ctx context.Context, query string, limit int, withBody bool) ([]assistant.MailHitView, error) {
	creds, err := t.mailCreds(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, t.mailMissing()
		}
		return nil, err
	}
	found, err := gandi.Sent(ctx, creds, query, "", limit, withBody)
	if err != nil {
		t.markMailError(ctx, err)
		return nil, err
	}
	out := make([]assistant.MailHitView, 0, len(found))
	for _, f := range found {
		out = append(out, t.mailHit(f, creds.Email))
	}
	if withBody && len(found) > 0 && found[0].Body != "" {
		out[0].Fil = t.threadViews(ctx, found[0].Thread, creds.Email)
		out[0].Suite = t.threadViews(ctx, found[0].Followups, creds.Email)
		t.noteMail(ctx, found[0].Message, assistant.EmailContentView{PourToi: "envoyé par toi"})
	}
	return out, nil
}

func (t *userToolbox) SearchEmails(ctx context.Context, q assistant.MailSearch) ([]assistant.MailHitView, error) {
	creds, err := t.mailCreds(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, t.mailMissing()
		}
		return nil, err
	}
	var folders []string
	switch strings.ToLower(strings.TrimSpace(q.Folder)) {
	case "envoye", "envoyé", "envoyes", "envoyés", "sent":
		folders = []string{gandi.FolderSent}
	case "tous", "all", "both":
		folders = []string{gandi.FolderInbox, gandi.FolderSent}
	default:
		folders = []string{gandi.FolderInbox}
	}
	found, err := gandi.Search(ctx, creds, gandi.SearchQuery{
		Sender: q.Person, Subject: q.Subject, Since: q.Since, Folders: folders, Limit: q.Limit,
	})
	if err != nil {
		t.markMailError(ctx, err)
		return nil, err
	}
	out := make([]assistant.MailHitView, 0, len(found))
	for _, f := range found {
		out = append(out, t.mailHit(f, creds.Email))
	}
	return out, nil
}

func (t *userToolbox) ReadEmailByID(ctx context.Context, messageID string) (assistant.EmailContentView, error) {
	creds, err := t.mailCreds(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return assistant.EmailContentView{}, t.mailMissing()
		}
		return assistant.EmailContentView{}, err
	}
	f, err := gandi.ReadByID(ctx, creds, messageID)
	if err != nil {
		return assistant.EmailContentView{}, err
	}
	view := t.mailContent(ctx, f.Message, creds.Email, f.Folder)
	t.noteMail(ctx, f.Message, view)
	return view, nil
}

func (t *userToolbox) mailHit(f gandi.Found, mine string) assistant.MailHitView {
	hit := assistant.MailHitView{
		MessageID: f.MessageID,
		Dossier:   f.Folder,
		De:        f.From,
		Adresse:   f.FromAddr,
		Pour:      f.To,
		Copie:     f.Cc,
		Objet:     f.Subject,
		Quand:     t.when(f.Date),
		Lu:        f.Seen,
		Brouillon: f.Draft,
		Contenu:   f.Body,
	}
	if f.Folder == gandi.FolderSent || writtenBy(f.FromAddr, mine) {
		hit.DeToi = true
	} else {
		hit.PourToi = string(triage.Addressing(toTriageMail(f.Message), mine))
		hit.ReponseATonMail = f.AnswersYou
	}
	return hit
}

func (t *userToolbox) SearchSlack(ctx context.Context, q assistant.SlackSearch) (assistant.SlackSearchView, error) {
	creds, err := t.srv.slackCreds(ctx, t.user)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return assistant.SlackSearchView{}, errors.New("Slack n'est pas connecté")
		}
		return assistant.SlackSearchView{}, err
	}
	client := slack.New(creds.UserToken)
	res, err := client.Search(ctx, slack.SearchQuery{
		Author: q.Author, Mentions: q.Mentions, Text: q.Text, Channel: q.Channel, Since: q.Since, Limit: q.Limit,
	})
	if err != nil {
		var ambUser *slack.AmbiguousUserError
		if errors.As(err, &ambUser) {
			return assistant.SlackSearchView{}, &assistant.AmbiguousError{
				Quoi: "personne", Recherche: ambUser.Query, Choix: ambUser.Choices,
			}
		}
		var ambConv *slack.AmbiguousConversationError
		if errors.As(err, &ambConv) {
			return assistant.SlackSearchView{}, &assistant.AmbiguousError{
				Quoi: "conversation", Recherche: ambConv.Query, Choix: ambConv.Choices,
			}
		}
		t.srv.store.MarkConnectionError(ctx, t.user.ID, store.ProviderSlack, err.Error())
		return assistant.SlackSearchView{}, err
	}
	view := assistant.SlackSearchView{
		Auteur:         res.Author,
		AuteurID:       res.AuthorID,
		Mentionne:      res.Mentioned,
		Canaux:         res.Channels,
		Depuis:         t.when(res.Since),
		Messages:       []assistant.SlackMessageView{},
		Avertissements: res.Warnings,
	}
	// Un message qui le vise ne se comprend qu'avec ce qui le précède : on
	// remonte le contexte des quatre plus récents.
	withContext := map[int]bool{}
	for i := len(res.Messages) - 1; i >= 0 && len(withContext) < 4; i-- {
		if v := res.Messages[i].TeVise; v == slack.VisePerso || v == slack.ViseNomme {
			withContext[i] = true
		}
	}
	for i, m := range res.Messages {
		if withContext[i] {
			before := client.ContextBefore(ctx, m.CanalID, m.Canal, m.ID, m.FilDe)
			view.Messages = append(view.Messages, t.slackInterpellation(ctx, m, before))
			continue
		}
		view.Messages = append(view.Messages, t.slackMessage(ctx, m))
	}
	return view, nil
}

func (t *userToolbox) CSPTickets(ctx context.Context, query string, includeClosed bool, limit int) ([]assistant.TicketView, error) {
	if !t.srv.cfg.TuleapEnabled() {
		return nil, errors.New("Tuleap n'est pas configuré sur le serveur")
	}
	tickets, _, _, err := t.srv.cspTickets(ctx, t.user, false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("la clé Tuleap n'est pas renseignée")
		}
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	want := strings.ToLower(strings.TrimSpace(query))
	out := make([]assistant.TicketView, 0, len(tickets))
	for _, tk := range tickets {
		if !includeClosed && tk.Ferme {
			continue
		}
		if want != "" && !ticketMatches(tk, want) {
			continue
		}
		out = append(out, assistant.TicketView{
			ID:          tk.ID,
			Ref:         tk.Ref,
			Compagnie:   tk.Compagnie,
			Cycle:       tk.Cycle,
			Titre:       tk.Titre,
			Statut:      tk.Statut,
			Priorite:    tk.Priorite,
			Responsable: tk.Responsable,
			Auteur:      tk.Auteur,
			Modifie:     t.when(tk.Modifie),
			URL:         tk.URL,
			Ferme:       tk.Ferme,
			Description: truncateText(tk.Description, 400),
		})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func ticketMatches(tk tuleap.Ticket, want string) bool {
	hay := strings.ToLower(strings.Join([]string{
		tk.Ref, tk.Titre, tk.Statut, tk.Priorite, tk.Responsable, tk.Auteur, tk.Description,
		tk.Compagnie, tk.Cycle, tk.Label,
	}, " "))
	for _, w := range strings.Fields(want) {
		w = strings.TrimPrefix(w, "#")
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func truncateText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
