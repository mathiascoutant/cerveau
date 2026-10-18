package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/gandi"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/slack"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// La mémoire des échanges, côté serveur.
//
// Chaque lecture — un mail, un canal Slack, une conversation WhatsApp — est
// notée au passage, sans que le modèle ait à y penser : le dernier message vu,
// à qui il s'adressait. Ce que Raoul en a compris s'y ajoute en fin de tour :
// l'analyse d'un débrief quand il y en a eu un, sinon ce qu'il a répondu, à
// condition que la demande ne portait que sur cet échange-là. Une réponse qui
// fait le point sur cinq mails ne résume aucun des cinq.

const (
	// recentWindow : ce qui descend dans la consigne. Au-delà de trois jours,
	// un échange se retrouve par memoire_echanges, il n'encombre plus.
	recentWindow = 72 * time.Hour
	recentLimit  = 6
	// recallLimit : échanges rendus par memoire_echanges, chacun vérifié.
	recallLimit = 3
	// freshTimeout : la vérification de ce qui a bougé ne doit pas faire
	// attendre plus qu'une relecture.
	freshTimeout = 8 * time.Second
)

// readLog : les échanges lus pendant une demande.
type readLog struct {
	mu         sync.Mutex
	keys       []echangeKey
	understood map[echangeKey]bool
}

type echangeKey struct{ source, cle string }

func (t *userToolbox) noteExchange(ctx context.Context, e store.Echange) {
	if t.srv.store == nil || strings.TrimSpace(e.Cle) == "" {
		return
	}
	e.UserID = t.user.ID
	if err := t.srv.store.NoterEchange(ctx, e); err != nil {
		slog.Warn("échange non noté", "source", e.Source, "err", err)
		return
	}
	if t.seen == nil {
		return
	}
	t.seen.mu.Lock()
	defer t.seen.mu.Unlock()
	key := echangeKey{e.Source, e.Cle}
	for _, k := range t.seen.keys {
		if k == key {
			return
		}
	}
	t.seen.keys = append(t.seen.keys, key)
}

// mailRoot : la clé d'un fil de mails, le premier message de la conversation.
func mailRoot(msg gandi.Message) string {
	if len(msg.References) > 0 {
		return msg.References[0]
	}
	for _, id := range msg.InReplyTo {
		if id = strings.Trim(strings.ToLower(strings.TrimSpace(id)), "<>"); id != "" {
			return id
		}
	}
	return msg.MessageID
}

// noteMail note la lecture d'un mail. Le dernier message retenu est le plus
// récent de la conversation — la suite comprise : c'est contre lui qu'on
// vérifiera plus tard s'il y a du neuf.
func (t *userToolbox) noteMail(ctx context.Context, msg gandi.Message, view assistant.EmailContentView) {
	e := store.Echange{
		Source: "mail", Espace: t.space, Cle: mailRoot(msg), Sujet: gandi.CleanSubject(msg.Subject),
		DernierDe: msg.From, DernierAdresse: msg.FromAddr, DernierQuand: msg.Date, DernierRef: msg.MessageID,
		DernierExtrait: msg.Body, DernierDeToi: view.PourToi == "envoyé par toi", PourToi: view.PourToi,
	}
	var people []string
	add := func(name string, mine bool) {
		name = strings.TrimSpace(name)
		if name == "" || mine || len(people) >= 8 {
			return
		}
		for _, p := range people {
			if strings.EqualFold(p, name) {
				return
			}
		}
		people = append(people, name)
	}
	add(msg.From, e.DernierDeToi)
	for _, m := range msg.Thread {
		if m.MessageID != "" {
			add(m.From, m.Mine)
		}
	}
	for _, m := range msg.Followups {
		add(m.From, m.Mine)
		if m.Date.After(e.DernierQuand) {
			e.DernierDe, e.DernierAdresse, e.DernierQuand, e.DernierRef = m.From, m.FromAddr, m.Date, m.MessageID
			e.DernierExtrait, e.DernierDeToi, e.PourToi = m.Excerpt, m.Mine, ""
		}
	}
	e.Participants = people
	t.noteExchange(ctx, e)
}

// noteSlack note la lecture d'une conversation Slack. Le dernier message
// retenu est le dernier du premier niveau : c'est l'historique du canal qui
// dira plus tard ce qui est arrivé depuis.
func (t *userToolbox) noteSlack(ctx context.Context, label string, messages []slack.Message) {
	if len(messages) == 0 {
		return
	}
	last := messages[len(messages)-1]
	e := store.Echange{
		Source: "slack", Cle: last.CanalID, Sujet: label,
		DernierDe: last.Auteur, DernierQuand: last.Quand, DernierRef: last.ID,
		DernierExtrait: last.Texte, DernierDeToi: last.DeToi,
	}
	if last.TeCite {
		e.PourToi = "il y est cité"
	}
	seen := map[string]bool{}
	for _, m := range messages {
		if !m.DeToi && m.Auteur != "" && !seen[m.Auteur] && len(e.Participants) < 8 {
			seen[m.Auteur] = true
			e.Participants = append(e.Participants, m.Auteur)
		}
	}
	t.noteExchange(ctx, e)
}

// noteWhatsApp note la lecture d'une conversation WhatsApp.
func (t *userToolbox) noteWhatsApp(ctx context.Context, chat whatsapp.Chat, msgs []store.WhatsAppMessage) {
	if len(msgs) == 0 {
		return
	}
	last := msgs[len(msgs)-1]
	e := store.Echange{
		Source: "whatsapp", Cle: chat.JID, Sujet: chat.Name,
		DernierDe: last.Sender, DernierQuand: last.Timestamp, DernierRef: last.MessageID,
		DernierExtrait: last.Body, DernierDeToi: last.FromMe,
	}
	if last.Mentioned {
		e.PourToi = "il y est cité"
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		if !m.FromMe && m.Sender != "" && !seen[m.Sender] && len(e.Participants) < 8 {
			seen[m.Sender] = true
			e.Participants = append(e.Participants, m.Sender)
		}
	}
	t.noteExchange(ctx, e)
}

// NoteUnderstanding : l'analyse d'un débrief devient ce que Raoul a compris
// de l'échange qu'il vient de lire.
func (t *userToolbox) NoteUnderstanding(ctx context.Context, analysis string) {
	if t.seen == nil || t.srv.store == nil {
		return
	}
	t.seen.mu.Lock()
	if len(t.seen.keys) == 0 {
		t.seen.mu.Unlock()
		return
	}
	key := t.seen.keys[len(t.seen.keys)-1]
	if t.seen.understood == nil {
		t.seen.understood = map[echangeKey]bool{}
	}
	t.seen.understood[key] = true
	t.seen.mu.Unlock()
	if err := t.srv.store.NoterCompris(ctx, t.user.ID, key.source, key.cle, analysis); err != nil {
		slog.Warn("compréhension non notée", "err", err)
	}
}

// closeTurn : en fin de tour, ce que Raoul a répondu devient ce qu'il a
// compris de l'échange — s'il n'en a lu qu'un, et qu'aucun débrief n'en a
// déjà laissé une analyse plus fouillée.
func (t *userToolbox) closeTurn(ctx context.Context, answer string) {
	if t.seen == nil || t.srv.store == nil {
		return
	}
	t.seen.mu.Lock()
	keys := append([]echangeKey(nil), t.seen.keys...)
	done := len(keys) == 1 && t.seen.understood[keys[0]]
	t.seen.mu.Unlock()
	if len(keys) != 1 || done || len([]rune(strings.TrimSpace(answer))) < 60 {
		return
	}
	if err := t.srv.store.NoterCompris(ctx, t.user.ID, keys[0].source, keys[0].cle, answer); err != nil {
		slog.Warn("compréhension non notée", "err", err)
	}
}

// RecallExchanges rend ce qu'il a déjà lu sur une personne, un sujet ou une
// conversation, chaque échange vérifié à l'instant : ce qui est arrivé depuis.
func (t *userToolbox) RecallExchanges(ctx context.Context, query string) ([]assistant.EchangeView, error) {
	found, err := t.srv.store.ChercherEchanges(ctx, t.user.ID, query, recallLimit)
	if err != nil {
		return nil, err
	}
	// Quand la question porte sur quelqu'un que l'annuaire connaît, un mail de
	// lui dans une autre conversation compte aussi : « son dernier mail » a
	// peut-être changé de fil.
	people := t.addressesOf(ctx, query)
	out := make([]assistant.EchangeView, len(found))
	var wg sync.WaitGroup
	for i, e := range found {
		out[i] = t.echangeView(e)
		wg.Add(1)
		go func(i int, e store.Echange) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, freshTimeout)
			defer cancel()
			out[i].Depuis, out[i].Nouveaux = t.whatsNew(ctx, e, people)
		}(i, e)
	}
	wg.Wait()
	return out, nil
}

func (t *userToolbox) echangeView(e store.Echange) assistant.EchangeView {
	v := assistant.EchangeView{
		Source: e.Source, Sujet: e.Sujet, Participants: e.Participants,
		DernierDe: e.DernierDe, DernierDeToi: e.DernierDeToi, DernierExtrait: e.DernierExtrait,
		PourToi: e.PourToi, Compris: e.Resume, LuLe: t.when(e.VuLe),
	}
	if !e.DernierQuand.IsZero() {
		v.DernierQuand = t.when(e.DernierQuand)
	}
	if !e.ResumeLe.IsZero() {
		v.ComprisLe = t.when(e.ResumeLe)
	}
	if e.Source == "mail" {
		v.MessageID = e.DernierRef
	} else {
		v.Conversation = e.Sujet
	}
	return v
}

// whatsNew vérifie ce qui est arrivé dans un échange depuis sa dernière
// lecture. Une vérification impossible se dit comme telle : « rien de
// nouveau » ne se prononce que sur une vérification faite.
func (t *userToolbox) whatsNew(ctx context.Context, e store.Echange, people []string) (string, []assistant.NouveauView) {
	if e.DernierQuand.IsZero() {
		return "", nil
	}
	var news []assistant.NouveauView
	var err error
	switch e.Source {
	case "mail":
		news, err = t.newMail(ctx, e, people)
	case "slack":
		news, err = t.newSlack(ctx, e)
	case "whatsapp":
		news, err = t.newWhatsApp(ctx, e)
	default:
		return "", nil
	}
	if err != nil {
		return "vérification impossible : " + err.Error(), nil
	}
	switch len(news) {
	case 0:
		return "rien de nouveau depuis", nil
	case 1:
		return "1 nouveau message depuis", news
	}
	return fmt.Sprintf("%d nouveaux messages depuis", len(news)), news
}

func (t *userToolbox) newMail(ctx context.Context, e store.Echange, people []string) ([]assistant.NouveauView, error) {
	space := e.Espace
	if space == "" {
		space = store.SpacePro
	}
	creds, err := t.srv.gandiCredsIn(ctx, t.user, space)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("la boîte mail %s n'est plus connectée", space)
		}
		return nil, err
	}
	// Quand le dernier mot n'était pas le sien, un nouveau mail de la même
	// personne compte aussi : « son dernier mail » a peut-être changé.
	from := append([]string{}, people...)
	if !e.DernierDeToi {
		from = append(from, e.DernierAdresse)
	}
	found, err := gandi.Nouveaux(ctx, creds, e.Cle, from, e.DernierQuand)
	if err != nil {
		return nil, err
	}
	var out []assistant.NouveauView
	for _, f := range found {
		if f.MessageID == e.DernierRef {
			continue
		}
		out = append(out, assistant.NouveauView{
			De: f.From, DeToi: f.Folder == gandi.FolderSent, Quand: t.when(f.Date),
			Objet: f.Subject, MessageID: f.MessageID,
		})
	}
	return out, nil
}

func (t *userToolbox) newSlack(ctx context.Context, e store.Echange) ([]assistant.NouveauView, error) {
	creds, err := t.srv.slackCreds(ctx, t.user)
	if err != nil {
		return nil, errors.New("Slack n'est plus connecté")
	}
	msgs, err := slack.New(creds.UserToken).NewSince(ctx, e.Cle, e.DernierRef)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.NouveauView, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, assistant.NouveauView{
			De: m.Auteur, DeToi: m.DeToi, Quand: t.when(m.Quand), Extrait: truncateRunes(m.Texte, 300),
		})
	}
	return out, nil
}

func (t *userToolbox) newWhatsApp(ctx context.Context, e store.Echange) ([]assistant.NouveauView, error) {
	msgs, err := t.srv.store.WhatsAppMessages(ctx, t.user.ID, e.Cle, e.DernierQuand, 20)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.NouveauView, 0, len(msgs))
	for _, m := range msgs {
		if m.MessageID == e.DernierRef {
			continue
		}
		out = append(out, assistant.NouveauView{
			De: m.Sender, DeToi: m.FromMe, Quand: t.when(m.Timestamp), Extrait: truncateRunes(m.Body, 300),
		})
	}
	return out, nil
}

// addressesOf : les adresses connues de la personne que désigne une recherche,
// quand l'annuaire la reconnaît sans hésiter.
func (t *userToolbox) addressesOf(ctx context.Context, query string) []string {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	p, ok := t.annuaire(ctx).byName(query, func(store.Fact) bool { return true })
	if !ok {
		return nil
	}
	return p.Adresses
}

// recentExchanges : ce que Raoul a lu ces derniers jours, pour la consigne.
func (s *Server) recentExchanges(ctx context.Context, user *store.User) []assistant.EchangeView {
	list, err := s.store.EchangesRecents(ctx, user.ID, recentLimit)
	if err != nil {
		slog.Warn("lecture des échanges impossible", "err", err)
		return nil
	}
	tb := s.toolbox(user)
	out := make([]assistant.EchangeView, 0, len(list))
	for _, e := range list {
		if time.Since(e.VuLe) > recentWindow {
			continue
		}
		out = append(out, tb.echangeView(e))
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
