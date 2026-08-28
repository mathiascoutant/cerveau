package whatsapp

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Nombre de messages retenus par conversation dans un lot d'historique. Le
// téléphone en pousse parfois des milliers d'un coup sur les conversations
// actives ; au-delà de ce seuil, ce qu'on garde en plus ne sert jamais à
// répondre « quoi de neuf depuis mon dernier message ».
const maxHistoryPerChat = 500

// Le temps qu'on s'accorde pour écrire dans le journal. whatsmeow appelle les
// gestionnaires sans contexte : sans limite, une base injoignable bloquerait la
// réception des messages suivants.
const journalTimeout = 30 * time.Second

// handle est le point d'entrée de tout ce que WhatsApp nous envoie.
//
// Seuls les événements qui changent ce que Raoul peut dire sont traités. Les
// autres — présence, accusés de réception des autres, appels — sont ignorés en
// silence : ce ne sont pas des erreurs, ce sont des choses dont on n'a rien à
// faire.
func (s *session) handle(evt any) {
	switch e := evt.(type) {
	case *events.Message:
		s.onMessage(e)
	case *events.HistorySync:
		s.onHistorySync(e)
	case *events.Receipt:
		s.onReceipt(e)
	case *events.MarkChatAsRead:
		if e.Action.GetRead() {
			s.markRead(e.JID, e.Timestamp)
		}
	case *events.Mute:
		s.write("sourdine", func(ctx context.Context) error {
			return s.mgr.journal.SetMuted(ctx, s.userID, e.JID.String(), e.Action.GetMuted())
		})
	case *events.Archive:
		s.write("archivage", func(ctx context.Context) error {
			return s.mgr.journal.SetArchived(ctx, s.userID, e.JID.String(), e.Action.GetArchived())
		})
	case *events.PairSuccess:
		s.onPaired(e.ID)
	case *events.Connected:
		s.onConnected()
	case *events.LoggedOut:
		s.onLoggedOut()
	case *events.Disconnected:
		s.setPhase(PhaseOffline, "")
	case *events.StreamReplaced:
		s.setPhase(PhaseOffline, "une autre session a pris la place de celle-ci")
	case *events.ClientOutdated:
		s.setPhase(PhaseOffline, "WhatsApp refuse cette version du client")
	case *events.PairError:
		s.fail("appairage refusé : " + e.Error.Error())
	}
}

func (s *session) onConnected() {
	s.mu.Lock()
	s.phase, s.code, s.lastErr = PhaseOnline, "", ""
	if s.client.Store != nil {
		s.pushName = s.client.Store.PushName
	}
	s.mu.Unlock()
	slog.Info("whatsapp : connecté", "user", s.userID)
}

func (s *session) onPaired(jid types.JID) {
	ctx, cancel := context.WithTimeout(context.Background(), journalTimeout)
	defer cancel()
	if err := s.mgr.journal.SaveDevice(ctx, s.userID, jid.String(), "+"+jid.User); err != nil {
		slog.Error("whatsapp : enregistrement de l'appareil lié", "err", err)
	}
	s.setPhase(PhaseOnline, "")
}

// onLoggedOut : l'appareil a été délié, presque toujours depuis le téléphone.
// On oublie le compte pour que l'app propose de le relier, au lieu d'afficher
// une connexion qui ne reviendra jamais.
func (s *session) onLoggedOut() {
	ctx, cancel := context.WithTimeout(context.Background(), journalTimeout)
	defer cancel()
	if err := s.mgr.journal.ForgetDevice(ctx, s.userID); err != nil {
		slog.Error("whatsapp : oubli de l'appareil délié", "err", err)
	}
	s.setPhase(PhaseOffline, "appareil délié depuis le téléphone")

	s.mgr.mu.Lock()
	delete(s.mgr.sessions, s.userID)
	s.mgr.mu.Unlock()
}

func (s *session) setPhase(phase, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.lastErr = phase, reason
	if phase != PhasePairing {
		s.code = ""
	}
}

// onMessage archive un message reçu ou envoyé.
//
// Les messages envoyés par l'utilisateur comptent autant que les autres : ce
// sont eux qui répondent à « depuis mon dernier message », et le fait qu'il ait
// écrit dans une conversation prouve qu'il l'a lue.
func (s *session) onMessage(e *events.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), journalTimeout)
	defer cancel()

	msg, ok := s.convert(ctx, e)
	if !ok {
		return
	}
	if err := s.mgr.journal.SaveMessages(ctx, s.userID, []Message{msg}); err != nil {
		slog.Error("whatsapp : archivage du message", "err", err)
		return
	}
	if err := s.mgr.journal.TouchChat(ctx, s.userID, msg.ChatJID, msg.ChatName, msg.IsGroup, msg.Timestamp); err != nil {
		slog.Error("whatsapp : mise à jour de la conversation", "err", err)
	}
	// Écrire dans une conversation, c'est l'avoir lue : sans ça, ses propres
	// réponses reviendraient comme des messages non lus jusqu'au prochain
	// accusé de lecture.
	if msg.FromMe {
		s.markRead(e.Info.Chat, msg.Timestamp)
	}
}

// convert traduit un message whatsmeow en entrée de journal. Rend false pour ce
// qui n'a rien à archiver : les messages de service (clé de chiffrement
// changée, message supprimé) et les protocoles internes.
func (s *session) convert(ctx context.Context, e *events.Message) (Message, bool) {
	body, kind, info := content(e.Message)
	if strings.TrimSpace(body) == "" {
		return Message{}, false
	}
	chatName, isGroup := s.chatLabel(ctx, e.Info)

	sender := "toi"
	if !e.Info.IsFromMe {
		sender = s.contactName(ctx, e.Info.Sender, e.Info.PushName)
	}
	return Message{
		ID:        e.Info.ID,
		ChatJID:   e.Info.Chat.String(),
		ChatName:  chatName,
		IsGroup:   isGroup,
		SenderJID: e.Info.Sender.String(),
		Sender:    sender,
		FromMe:    e.Info.IsFromMe,
		Body:      body,
		Kind:      kind,
		Mentioned: !e.Info.IsFromMe && mentions(info, s.identities()...),
		Timestamp: e.Info.Timestamp,
	}, true
}

// onHistorySync importe ce que le téléphone pousse à la liaison.
//
// C'est le seul passé auquel on aura jamais accès : WhatsApp ne stocke rien
// côté serveur, et un appareil lié ne reçoit que ce que le téléphone veut bien
// lui transférer. D'où l'importation intégrale, sans filtre — ce qui n'est pas
// pris ici ne se redemandera pas.
func (s *session) onHistorySync(e *events.HistorySync) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, conv := range e.Data.GetConversations() {
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		raw := conv.GetMessages()
		if len(raw) > maxHistoryPerChat {
			raw = raw[:maxHistoryPerChat]
		}

		msgs := make([]Message, 0, len(raw))
		var latest time.Time
		for _, item := range raw {
			parsed, err := s.client.ParseWebMessage(chatJID, item.GetMessage())
			if err != nil {
				continue
			}
			msg, ok := s.convert(ctx, parsed)
			if !ok {
				continue
			}
			msgs = append(msgs, msg)
			if msg.Timestamp.After(latest) {
				latest = msg.Timestamp
			}
		}

		chat := Chat{
			JID:      chatJID.String(),
			Name:     historyName(conv.GetName(), conv.GetDisplayName()),
			IsGroup:  chatJID.Server == types.GroupServer,
			Muted:    conv.GetMuteEndTime() > uint64(time.Now().Unix()),
			Archived: conv.GetArchived(),
			LastAt:   latest,
		}
		if chat.Name == "" && len(msgs) > 0 {
			chat.Name = msgs[0].ChatName
		}
		// Une conversation sans non-lus est une conversation lue jusqu'au
		// bout : c'est la seule occasion d'apprendre l'état de lecture du
		// passé, les accusés ne portent que sur ce qui arrive ensuite.
		if conv.GetUnreadCount() == 0 && !conv.GetMarkedAsUnread() && !latest.IsZero() {
			chat.LastReadAt = latest
		}

		if len(msgs) > 0 {
			if err := s.mgr.journal.SaveMessages(ctx, s.userID, msgs); err != nil {
				slog.Error("whatsapp : import d'historique", "err", err)
				continue
			}
		}
		if err := s.mgr.journal.SaveChats(ctx, s.userID, []Chat{chat}); err != nil {
			slog.Error("whatsapp : import de conversation", "err", err)
		}
	}
}

// onReceipt suit l'état de lecture depuis les autres appareils.
//
// C'est ce qui rend le « non lu » de WhatsApp fiable, là où Slack oblige à
// deviner : quand l'utilisateur ouvre une conversation sur son téléphone, ses
// appareils liés en sont informés. Les accusés des autres personnes, eux, ne
// disent que ce qu'elles ont lu de nos messages — ils ne nous apprennent rien.
func (s *session) onReceipt(e *events.Receipt) {
	if e.Type != types.ReceiptTypeRead && e.Type != types.ReceiptTypeReadSelf {
		return
	}
	if !e.MessageSource.IsFromMe {
		return
	}
	s.markRead(e.Chat, e.Timestamp)
}

func (s *session) markRead(chat types.JID, at time.Time) {
	s.write("état de lecture", func(ctx context.Context) error {
		return s.mgr.journal.MarkChatRead(ctx, s.userID, chat.String(), at)
	})
}

// write exécute une écriture de journal avec sa limite de temps. whatsmeow
// appelle les gestionnaires sans contexte : sans limite, une base injoignable
// bloquerait la réception des messages suivants.
func (s *session) write(what string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), journalTimeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		slog.Error("whatsapp : "+what, "user", s.userID, "err", err)
	}
}

// identities rend les adresses sous lesquelles l'utilisateur est désigné : son
// numéro et son identifiant masqué. Voir sameUser — les groupes récents citent
// le second, les anciens le premier.
func (s *session) identities() []types.JID {
	if s.client.Store == nil {
		return nil
	}
	out := make([]types.JID, 0, 2)
	if s.client.Store.ID != nil {
		out = append(out, *s.client.Store.ID)
	}
	if !s.client.Store.LID.IsEmpty() {
		out = append(out, s.client.Store.LID)
	}
	return out
}

// chatLabel nomme la conversation comme l'utilisateur la voit dans WhatsApp :
// le sujet pour un groupe, le nom du contact pour un tête-à-tête.
func (s *session) chatLabel(ctx context.Context, info types.MessageInfo) (string, bool) {
	if info.IsGroup || info.Chat.Server == types.GroupServer {
		return s.groupName(ctx, info.Chat), true
	}
	// Sur un message qu'il a envoyé, l'interlocuteur est le destinataire, pas
	// l'expéditeur : c'est le fil qui porte le nom, pas celui qui parle.
	push := info.PushName
	if info.IsFromMe {
		push = ""
	}
	return s.contactName(ctx, info.Chat, push), false
}

// groupName demande le sujet du groupe, et le garde : il ne voyage pas avec les
// messages, et une conversation animée déclencherait sinon une requête par
// message.
func (s *session) groupName(ctx context.Context, jid types.JID) string {
	s.mu.Lock()
	cached, ok := s.groups[jid.ToNonAD()]
	s.mu.Unlock()
	if ok && time.Since(cached.at) < groupNameTTL {
		return cached.name
	}

	name := "groupe " + jid.User
	if info, err := s.client.GetGroupInfo(ctx, jid); err == nil && info.Name != "" {
		name = info.Name
	} else if ok {
		// Le serveur n'a pas répondu : mieux vaut un nom périmé que « groupe
		// 120363… », qui ne veut rien dire pour personne.
		name = cached.name
	}

	s.mu.Lock()
	s.groups[jid.ToNonAD()] = groupName{name: name, at: time.Now()}
	s.mu.Unlock()
	return name
}

// contactName rend le nom sous lequel l'utilisateur connaît quelqu'un.
//
// L'ordre suit ce qu'il voit dans WhatsApp : d'abord le nom de son répertoire,
// puis le nom que la personne s'est donné, et en dernier recours le numéro. Un
// identifiant brut ne doit jamais ressortir — « 33612345678 t'a écrit » ne
// s'écoute pas.
func (s *session) contactName(ctx context.Context, jid types.JID, pushName string) string {
	if s.client.Store != nil && s.client.Store.Contacts != nil {
		if c, err := s.client.Store.Contacts.GetContact(ctx, jid.ToNonAD()); err == nil && c.Found {
			for _, candidate := range []string{c.FullName, c.FirstName, c.BusinessName, c.PushName} {
				if strings.TrimSpace(candidate) != "" {
					return strings.TrimSpace(candidate)
				}
			}
		}
	}
	if strings.TrimSpace(pushName) != "" {
		return strings.TrimSpace(pushName)
	}
	if jid.Server == types.HiddenUserServer {
		// Identifiant masqué sans contact connu : le numéro n'est pas
		// récupérable, et l'afficher tel quel n'apprendrait rien.
		return "un contact"
	}
	return "+" + jid.User
}

// historyName : l'historique donne parfois le sujet, parfois le nom affiché,
// parfois rien.
func historyName(name, display string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return strings.TrimSpace(display)
}
