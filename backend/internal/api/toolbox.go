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

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/echeance"
	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
	"github.com/mathiascoutant/cerveau/backend/internal/triage"
)

// userToolbox donne à l'assistant l'accès aux données d'UN utilisateur.
//
// space dit de quelle boîte mail il s'agit. Tout le reste — Slack, WhatsApp,
// Tuleap, l'agenda, la liste à faire — ne dépend pas de l'espace : ces
// sources n'existent qu'en un exemplaire. La boîte mail est la seule qu'on
// peut brancher deux fois, une pro et une perso.
type userToolbox struct {
	srv   *Server
	user  *store.User
	space string
	// people : l'annuaire de son entourage, chargé au premier besoin et
	// partagé par les deux espaces de la même requête.
	people *peopleCache
	// seen : les échanges lus pendant la requête — voir echanges.go.
	seen *readLog
}

// toolbox rend la boîte à outils pro, celle de toujours.
func (s *Server) toolbox(user *store.User) *userToolbox {
	return &userToolbox{srv: s, user: user, space: store.SpacePro, people: &peopleCache{}, seen: &readLog{}}
}

// InSpace rend la même boîte à outils, lisant la boîte mail de l'autre espace.
// L'assistant s'en sert quand le modèle demande explicitement « perso ».
func (t *userToolbox) InSpace(space string) assistant.Toolbox {
	return &userToolbox{srv: t.srv, user: t.user, space: store.NormalizeSpace(space), people: t.people, seen: t.seen}
}

func (t *userToolbox) mailCreds(ctx context.Context) (gandi.Credentials, error) {
	return t.srv.gandiCredsIn(ctx, t.user, t.space)
}

func (t *userToolbox) mailMissing() error {
	return fmt.Errorf("la boîte mail %s n'est pas connectée", t.space)
}

func (t *userToolbox) markMailError(ctx context.Context, err error) {
	t.srv.store.MarkConnectionErrorIn(ctx, t.user.ID, store.ProviderGandi, t.space, err.Error())
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

// unreadMail rend les mails non lus tels que le fournisseur les a lus —
// destinataires et horodatages intacts — ainsi que l'adresse de l'utilisateur.
//
// UnreadEmails en fait une vue pour le modèle, le tri des urgences a besoin de
// la version complète : « suis-je le destinataire ? » ne se répond pas sur un
// résumé où le champ « À » a déjà été jeté.
func (t *userToolbox) unreadMail(ctx context.Context, limit int) ([]gandi.Message, string, error) {
	creds, err := t.mailCreds(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", t.mailMissing()
		}
		return nil, "", err
	}
	mails, err := gandi.Unread(ctx, creds, limit)
	if err != nil {
		t.markMailError(ctx, err)
		return nil, "", err
	}
	return mails, creds.Email, nil
}

func (t *userToolbox) UnreadEmails(ctx context.Context, limit int) ([]assistant.EmailView, error) {
	mails, moi, err := t.unreadMail(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.EmailView, 0, len(mails))
	for _, m := range mails {
		out = append(out, assistant.EmailView{
			De:       m.From,
			Identite: t.mailAuthor(ctx, m.From, m.FromAddr, moi, m.Bulk),
			Objet:    m.Subject,
			Recu:     t.when(m.Date),
			// Sa place parmi les destinataires est tranchée ici, pas par le
			// modèle : « suis-je dans ce champ À ? » est une comparaison de
			// chaînes, et une comparaison de chaînes ne se confie pas à
			// quelque chose qui devine.
			PourToi: string(triage.Addressing(toTriageMail(m), moi)),
		})
	}
	return out, nil
}

// ReadEmail rapatrie le corps d'un mail. Séparé de UnreadEmails à dessein :
// descendre un corps coûte un aller-retour IMAP de plus, et la plupart des
// questions n'en ont pas besoin.
func (t *userToolbox) ReadEmail(ctx context.Context, query string, unreadOnly bool) (assistant.EmailContentView, error) {
	creds, err := t.mailCreds(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return assistant.EmailContentView{}, t.mailMissing()
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
	view := t.mailContent(ctx, msg, creds.Email, gandi.FolderInbox)
	t.noteMail(ctx, msg, view)
	return view, nil
}

// mailContent met un mail lu en mots pour le modèle, avec toute sa
// conversation : ce qui le précède, ce qui l'a suivi, et sa place à lui
// dedans. Commun à la lecture par expéditeur et à la lecture par identifiant —
// le même mail ne doit pas se raconter différemment selon la façon dont on
// l'a ouvert.
func (t *userToolbox) mailContent(ctx context.Context, msg gandi.Message, email, folder string) assistant.EmailContentView {
	view := assistant.EmailContentView{
		De:            msg.From,
		Adresse:       msg.FromAddr,
		Pour:          msg.To,
		Copie:         msg.Cc,
		Personnes:     t.knownRecipients(ctx, msg.To, msg.Cc),
		Objet:         msg.Subject,
		Recu:          t.when(msg.Date),
		Contenu:       msg.Body,
		Tronque:       strings.HasSuffix(msg.Body, "…"),
		Destinataires: len(msg.To) + len(msg.Cc),
		MessageID:     msg.MessageID,
	}
	if folder == gandi.FolderSent || writtenBy(msg.FromAddr, email) {
		// Un mail de sa boîte d'envoi : c'est lui qui l'a écrit. Le champ
		// pour_toi n'a pas de sens, et « de » le désigne.
		view.PourToi = "envoyé par toi"
	} else {
		view.Identite = t.mailAuthor(ctx, msg.From, msg.FromAddr, email, msg.Bulk)
		view.PourToi = string(triage.Addressing(toTriageMail(msg), email))
		view.ReponseATonMail = msg.AnswersYou
		view.TeNomme = namesYou(msg.Body, t.srv.userName(t.user))
	}
	// L'échéance se lit dans le corps, donc elle n'existe qu'ici : la liste des
	// non-lus ne descend que les enveloppes, et rapatrier quinze corps pour y
	// chercher une date coûterait quinze allers-retours IMAP.
	if d := echeance.Trouver(msg.Body, msg.Date, t.location()); d != nil {
		view.Echeance = assistant.Deadline(d.Quand, time.Now(), t.location(), d.Heure)
		view.EcheanceDite = d.Extrait
	}
	// L'adresse de sa propre boîte sert à reconnaître ses envois dans le fil :
	// « j'ai dit quoi dans le mail d'avant » n'a de réponse que si on sait
	// lesquels de ces messages sont les siens.
	view.Fil = t.threadViews(ctx, msg.Thread, email)
	view.Suite = t.threadViews(ctx, msg.Followups, email)
	for _, m := range view.Suite {
		if m.DeToi {
			view.DejaRepondu = true
		}
	}
	return view
}

func (t *userToolbox) threadViews(ctx context.Context, list []gandi.ThreadMessage, email string) []assistant.ThreadView {
	out := make([]assistant.ThreadView, 0, len(list))
	for _, m := range list {
		// Recu : l'horodatage IMAP quand on l'a, sinon l'en-tête recopié tel
		// quel — jamais une date reconstruite.
		recu := m.Sent
		if !m.Date.IsZero() {
			recu = t.when(m.Date)
		}
		mine := m.Mine || writtenBy(m.From, email)
		var who assistant.Identite
		if !mine {
			// Un message cité n'a que sa ligne « De : » : on y cherche une
			// adresse, et à défaut on s'en tient au nom.
			name, addr := m.From, m.FromAddr
			if addr == "" {
				name, addr = splitAddress(quotedSender(m.From))
			}
			who = t.annuaire(ctx).forMail(name, addr)
		}
		out = append(out, assistant.ThreadView{
			De:        m.From,
			Identite:  who,
			DeToi:     mine,
			Recu:      recu,
			Pour:      m.To,
			Copie:     m.Cc,
			Objet:     m.Subject,
			Extrait:   m.Excerpt,
			MessageID: m.MessageID,
		})
	}
	return out
}

// quotedSender isole l'expéditeur d'une ligne de citation : « Le 3 mars 2026 à
// 10:02, Paul Durand <paul@x.fr> a écrit » devient « Paul Durand <paul@x.fr> ».
// Une ligne « De : » d'Outlook est déjà l'expéditeur seul.
func quotedSender(line string) string {
	line = strings.TrimSpace(line)
	low := strings.ToLower(line)
	for _, suffix := range []string{" a écrit", " wrote"} {
		if i := strings.LastIndex(low, suffix); i > 0 {
			line = line[:i]
			if j := strings.LastIndex(line, ", "); j >= 0 {
				line = line[j+2:]
			}
			break
		}
	}
	return strings.TrimSpace(line)
}

// namesYou dit si le corps d'un mail l'interpelle par son prénom. En copie ou
// parmi dix destinataires, c'est ce qui change une information en demande :
// « Mathias, tu peux regarder ? » ne s'adresse qu'à lui, quels que soient les
// champs À et Cc.
func namesYou(body, name string) bool {
	first := strings.Fields(name)
	if len(first) == 0 || len([]rune(first[0])) < 3 {
		return false
	}
	want := fuzzy.Normalize(first[0])
	for _, word := range strings.Fields(fuzzy.Normalize(body)) {
		if word == want {
			return true
		}
	}
	return false
}

// slackActivity rend l'activité brute, avec l'horodatage réel du dernier
// message. UnreadSlack le met en mots pour le modèle ; le tri des urgences a
// besoin de l'instant, pas de « il y a deux heures », pour ordonner la liste.
func (t *userToolbox) slackActivity(ctx context.Context, limit int) ([]slack.Activity, error) {
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
	return threads, nil
}

func (t *userToolbox) UnreadSlack(ctx context.Context, limit int) ([]assistant.SlackView, error) {
	threads, err := t.slackActivity(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.SlackView, 0, len(threads))
	for _, th := range threads {
		view := assistant.SlackView{
			Canal:           th.Channel,
			Type:            th.Kind,
			NonLus:          th.Unread,
			MessagesRecents: th.Recent,
			Mentions:        th.Mentions,
			Nomme:           th.Nomme,
			MentionsGroupe:  th.MentionsGroupe,
			Dernier:         t.when(th.Latest),
			Extraits:        th.Messages,
		}
		for _, it := range th.Interpellations {
			view.Interpellations = append(view.Interpellations, t.slackInterpellation(ctx, it.Message, it.Contexte))
		}
		out = append(out, view)
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
	client := slack.New(creds.UserToken)
	label, messages, err := client.ReadConversation(ctx, name, limit)
	if err != nil {
		var amb *slack.AmbiguousConversationError
		if errors.As(err, &amb) {
			return assistant.SlackChannelView{}, &assistant.AmbiguousError{
				Quoi: "conversation", Recherche: amb.Query, Choix: amb.Choices,
			}
		}
		var unsure *slack.UnconfirmedConversationError
		if errors.As(err, &unsure) {
			if !t.srv.confirm.askedOnce(t.user.ID, "slack:"+unsure.Found) {
				return assistant.SlackChannelView{}, &assistant.ConfirmError{
					Quoi: "canal", Recherche: unsure.Query, Trouve: unsure.Found,
				}
			}
			// Déjà demandé une fois : on relit sous le libellé que le client a
			// lui-même trouvé, le seul que son contrôle d'exactitude accepte.
			// Un message privé n'a pas de nom côté Slack, seulement ce libellé
			// — « DM Xavier » — et personne ne dit « DM Xavier » à voix haute :
			// sans ce rattrapage, la question était strictement sans issue.
			label, messages, err = client.ReadConversation(ctx, unsure.Found, limit)
			if err != nil {
				return assistant.SlackChannelView{}, err
			}
		} else {
			return assistant.SlackChannelView{}, err
		}
	}
	out := assistant.SlackChannelView{Canal: label}
	for _, m := range messages {
		out.Messages = append(out.Messages, t.slackMessage(ctx, m))
	}
	t.noteSlack(ctx, label, messages)
	return out, nil
}

func (t *userToolbox) slackMessage(ctx context.Context, m slack.Message) assistant.SlackMessageView {
	v := assistant.SlackMessageView{
		ID: m.ID, Canal: m.Canal, Auteur: m.Auteur, AuteurID: m.AuteurID,
		Identite: t.slackAuthor(ctx, m.AuteurID, m.Auteur, m.DeToi),
		Texte:    m.Texte, Quand: t.when(m.Quand), Fichiers: m.Fichiers, DeToi: m.DeToi,
		TeCite: m.TeCite, TeVise: m.TeVise, FilDe: m.FilDe, Lien: m.Lien,
	}
	// Les mentions descendent résolues : c'est avec elles, et pas avec les
	// prénoms du texte, que le modèle dit qui est interpellé.
	for _, mention := range m.Mentions {
		v.Mentions = append(v.Mentions, assistant.MentionView{ID: mention.ID, Nom: mention.Nom})
	}
	for _, r := range m.Fil {
		v.Fil = append(v.Fil, t.slackMessage(ctx, r))
	}
	return v
}

// slackInterpellation : un message qui le vise, avec ce qui le précède.
func (t *userToolbox) slackInterpellation(ctx context.Context, m slack.Message, before []slack.Message) assistant.SlackMessageView {
	v := t.slackMessage(ctx, m)
	for _, b := range before {
		c := t.slackMessage(ctx, b)
		c.Fil = nil
		v.ContexteAvant = append(v.ContexteAvant, c)
	}
	return v
}

// whatsAppThreads rend les conversations non lues telles que le journal les
// tient. UnreadWhatsApp les met en mots pour le modèle ; le tri des urgences a
// besoin des instants, pas de « il y a deux heures ».
func (t *userToolbox) whatsAppThreads(ctx context.Context, limit int) ([]store.WhatsAppThread, error) {
	if _, err := t.srv.whatsappCreds(ctx, t.user); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("WhatsApp n'est pas connecté")
		}
		return nil, err
	}
	return t.srv.store.UnreadWhatsApp(ctx, t.user.ID, limit)
}

func (t *userToolbox) UnreadWhatsApp(ctx context.Context, limit int) ([]assistant.WhatsAppView, error) {
	threads, err := t.whatsAppThreads(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.WhatsAppView, 0, len(threads))
	for _, th := range threads {
		view := assistant.WhatsAppView{
			Conversation: th.Chat.Name,
			Type:         chatKind(th.Chat.IsGroup),
			NonLus:       th.Unread,
			Mentions:     th.Mentions,
			Dernier:      t.when(th.Chat.LastAt),
		}
		for _, m := range th.Latest {
			view.Extraits = append(view.Extraits, m.Sender+" : "+m.Body)
		}
		out = append(out, view)
	}
	return out, nil
}

// ReadWhatsAppChat ouvre une conversation à la demande, non-lus ou pas.
//
// C'est l'outil qui répond à « quoi de neuf dans le groupe Azul » — et, quand
// le nom prononcé n'est pas le nom exact du groupe, celui qui refuse de lire
// tant qu'on n'a pas confirmé lequel.
func (t *userToolbox) ReadWhatsAppChat(ctx context.Context, name string, limit int, sinceMine bool) (assistant.WhatsAppChatView, error) {
	if _, err := t.srv.whatsappCreds(ctx, t.user); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return assistant.WhatsAppChatView{}, errors.New("WhatsApp n'est pas connecté")
		}
		return assistant.WhatsAppChatView{}, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > whatsapp.MaxRead {
		limit = whatsapp.MaxRead
	}

	stored, err := t.srv.store.WhatsAppChats(ctx, t.user.ID)
	if err != nil {
		return assistant.WhatsAppChatView{}, err
	}
	chats := make([]whatsapp.Chat, 0, len(stored))
	for _, c := range stored {
		chats = append(chats, whatsapp.Chat{
			JID: c.JID, Name: c.Name, IsGroup: c.IsGroup,
			LastAt: c.LastAt, LastReadAt: c.LastReadAt,
		})
	}

	chat, tied, ok := whatsapp.MatchChat(chats, name)
	if len(tied) > 0 {
		return assistant.WhatsAppChatView{}, &assistant.AmbiguousError{
			Quoi: "conversation", Recherche: name, Choix: tied,
		}
	}
	if !ok {
		return assistant.WhatsAppChatView{}, &whatsapp.UnknownChatError{
			Query: name, Nearest: whatsapp.Nearest(chats, name, 6),
		}
	}
	// La question ne se pose qu'une fois par conversation : la cible est le JID,
	// pas le nom prononcé, qui change à chaque tour (« ce groupe », « oui »)
	// pendant que la conversation visée reste la même. Voir confirmations.go.
	if whatsapp.NeedsConfirmation(name, chat) &&
		!t.srv.confirm.askedOnce(t.user.ID, "whatsapp:"+chat.JID) {
		return assistant.WhatsAppChatView{}, &assistant.ConfirmError{
			Quoi: chatWord(chat.IsGroup), Recherche: name, Trouve: chat.Name,
		}
	}

	var since time.Time
	if sinceMine {
		mine, err := t.srv.store.LastOwnWhatsAppMessage(ctx, t.user.ID, chat.JID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return assistant.WhatsAppChatView{}, err
		}
		if mine != nil {
			since = mine.Timestamp
		}
	}

	msgs, err := t.srv.store.WhatsAppMessages(ctx, t.user.ID, chat.JID, since, int64(limit))
	if err != nil {
		return assistant.WhatsAppChatView{}, err
	}
	out := assistant.WhatsAppChatView{
		Conversation:         chat.Name,
		Type:                 chatKind(chat.IsGroup),
		DepuisTonMessage:     sinceMine && !since.IsZero(),
		PlusAncienDisponible: len(msgs) == limit,
	}
	for _, m := range msgs {
		// Le non-lu se compte sur ce qui est rendu, et il se compte ici : le
		// modèle n'a pas les horodatages bruts, seulement « hier à 16h30 ».
		if !m.FromMe && !chat.LastReadAt.IsZero() && m.Timestamp.After(chat.LastReadAt) {
			out.NonLus++
		}
		view := assistant.WhatsAppMessageView{
			Auteur: m.Sender,
			Texte:  m.Body,
			Quand:  t.when(m.Timestamp),
			DeToi:  m.FromMe,
			TeCite: m.Mentioned,
		}
		if !m.FromMe {
			view.Identite = t.annuaire(ctx).forName(m.Sender)
		}
		out.Messages = append(out.Messages, view)
	}
	t.noteWhatsApp(ctx, chat, msgs)
	return out, nil
}

func chatKind(isGroup bool) string {
	if isGroup {
		return "groupe"
	}
	return "prive"
}

// chatWord : le mot par lequel on annonce la conversation dans la question de
// confirmation. « Tu parles du groupe Azul ? » se comprend, « Tu parles de la
// conversation Azul ? » sonne comme une machine.
func chatWord(isGroup bool) string {
	if isGroup {
		return "groupe"
	}
	return "la conversation avec"
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

// writtenBy dit si un message du fil vient de l'utilisateur lui-même.
//
// L'en-tête recopié mêle nom affiché et adresse (« Mathias COUTANT <m@x.fr> ») :
// on cherche l'adresse dedans, elle seule est fiable. Sans ce marquage, « j'ai
// dit quoi dans le mail d'avant » n'a pas de réponse — ses propres envois ne
// sont nulle part ailleurs, la boîte de réception ne les contient pas.
func writtenBy(from, email string) bool {
	if strings.TrimSpace(email) == "" {
		return false
	}
	return strings.Contains(strings.ToLower(from), strings.ToLower(email))
}

// --- Réponses de mail -------------------------------------------------------

// PrepareEmailReply range la réponse rédigée par le modèle, sans rien envoyer.
//
// La séparation est le cœur de la fonctionnalité : Raoul écrit, l'utilisateur
// expédie. Un mail parti par erreur ne se rattrape pas, et un assistant qui
// tient la plume n'a pas besoin de tenir aussi le bouton « envoyer ».
func (t *userToolbox) PrepareEmailReply(ctx context.Context, draft assistant.EmailReplyDraft) (assistant.EmailDraftView, store.Action, error) {
	body := strings.TrimSpace(draft.Corps)
	if body == "" {
		return assistant.EmailDraftView{}, store.Action{}, errors.New("le corps du mail est vide")
	}
	to := strings.TrimSpace(draft.Destinataire)
	if to == "" {
		to = strings.TrimSpace(draft.Adresse)
	}
	if to == "" {
		return assistant.EmailDraftView{}, store.Action{}, errors.New("destinataire manquant")
	}

	subject := strings.TrimSpace(draft.Objet)
	if subject == "" {
		subject = replySubject(draft.MailSource)
	}

	saved, err := t.srv.store.SaveEmailDraft(ctx, store.EmailDraft{
		UserID:        t.user.ID,
		To:            to,
		ToAddr:        strings.TrimSpace(draft.Adresse),
		Subject:       subject,
		Body:          body,
		Language:      normalizeLang(draft.Langue),
		SourceSubject: strings.TrimSpace(draft.MailSource),
	})
	if err != nil {
		return assistant.EmailDraftView{}, store.Action{}, fmt.Errorf("enregistrement de la réponse : %w", err)
	}
	return t.draftView(saved), draftAction(saved), nil
}

func (t *userToolbox) FindEmailDrafts(ctx context.Context, query string) ([]assistant.EmailDraftView, error) {
	drafts, err := t.srv.store.EmailDrafts(ctx, t.user.ID, query, 10)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.EmailDraftView, 0, len(drafts))
	for _, d := range drafts {
		out = append(out, t.draftView(d))
	}
	return out, nil
}

func (t *userToolbox) UpdateEmailDraft(ctx context.Context, id, subject, body string) (assistant.EmailDraftView, store.Action, error) {
	oid, err := bson.ObjectIDFromHex(strings.TrimSpace(id))
	if err != nil {
		return assistant.EmailDraftView{}, store.Action{}, errors.New("identifiant de brouillon invalide : reprends celui rendu par chercher_brouillon")
	}
	updated, err := t.srv.store.UpdateEmailDraft(ctx, t.user.ID, oid, subject, strings.TrimSpace(body))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return assistant.EmailDraftView{}, store.Action{}, errors.New("cette réponse préparée n'existe plus")
		}
		return assistant.EmailDraftView{}, store.Action{}, err
	}
	return t.draftView(updated), draftAction(updated), nil
}

// SearchHistory est la mémoire longue : ce que la fenêtre de contexte a laissé
// tomber reste retrouvable par son contenu.
func (t *userToolbox) SearchHistory(ctx context.Context, query string, since time.Time) ([]assistant.MemoryView, error) {
	past, err := t.srv.store.SearchInteractions(ctx, t.user.ID, query, since, 12)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.MemoryView, 0, len(past))
	// Du plus ancien au plus récent : un fil se relit dans le sens où il s'est
	// déroulé, sinon le modèle prend la fin pour le début.
	for i := len(past) - 1; i >= 0; i-- {
		out = append(out, assistant.MemoryView{
			Quand:   t.when(past[i].CreatedAt),
			Demande: past[i].Transcript,
			Reponse: past[i].Reply,
		})
	}
	return out, nil
}

func (t *userToolbox) draftView(d store.EmailDraft) assistant.EmailDraftView {
	return assistant.EmailDraftView{
		ID:           d.ID.Hex(),
		Destinataire: d.To,
		Adresse:      d.ToAddr,
		Objet:        d.Subject,
		Corps:        d.Body,
		Langue:       d.Language,
		MailSource:   d.SourceSubject,
		Modifie:      t.when(d.UpdatedAt),
	}
}

// draftAction prévient l'app qu'une réponse est prête. Elle n'écrit rien sur le
// téléphone, contrairement à create_event : elle sert à l'annoncer et à
// rafraîchir l'onglet Réponses.
func draftAction(d store.EmailDraft) store.Action {
	return store.Action{
		Type: "email_draft",
		Payload: map[string]any{
			"id":      d.ID.Hex(),
			"to":      d.To,
			"subject": d.Subject,
		},
	}
}

// replySubject fabrique un objet quand le modèle n'en a pas donné. « Re: » n'est
// pas traduit : c'est la convention de tous les clients mail, quelle que soit
// la langue du message.
func replySubject(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "Re:"
	}
	if strings.HasPrefix(strings.ToLower(source), "re:") {
		return source
	}
	return "Re: " + source
}

// normalizeLang ramène « Anglais », « EN-US » ou « english » à « en ». Le champ
// n'est qu'indicatif : il sert à l'affichage et à la voix, pas au routage.
func normalizeLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch {
	case lang == "":
		return ""
	case strings.HasPrefix(lang, "fr"), strings.HasPrefix(lang, "fran"):
		return "fr"
	case strings.HasPrefix(lang, "en"), strings.HasPrefix(lang, "ang"):
		return "en"
	}
	if len(lang) > 2 {
		lang = lang[:2]
	}
	return lang
}

// when met en mots un horodatage, dans le fuseau de l'utilisateur et par
// rapport à maintenant. Tout ce qui descend une date au modèle passe par ici :
// le modèle recopie, il ne calcule pas.
func (t *userToolbox) when(ts time.Time) string {
	return assistant.When(ts, time.Now(), t.location())
}

func (t *userToolbox) location() *time.Location {
	return t.srv.location(t.user)
}
