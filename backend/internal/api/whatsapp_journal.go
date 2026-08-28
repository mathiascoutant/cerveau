package api

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/cryptoutil"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// whatsAppJournal branche l'archive WhatsApp sur Mongo.
//
// Le paquet whatsapp ne connaît ni Mongo ni le chiffrement : il parle le
// protocole des appareils liés et rend des messages. C'est ici qu'on décide où
// ils atterrissent — et c'est ce qui permet de tester le premier sans base.
type whatsAppJournal struct {
	store  *store.Store
	cipher *cryptoutil.Cipher
}

// NewWhatsAppJournal construit l'archive. Exporté parce que le gestionnaire de
// sessions se monte au démarrage du serveur, avant les routes.
func NewWhatsAppJournal(st *store.Store, cipher *cryptoutil.Cipher) whatsapp.Journal {
	return &whatsAppJournal{store: st, cipher: cipher}
}

func (j *whatsAppJournal) Accounts(ctx context.Context) ([]whatsapp.Account, error) {
	conns, err := j.store.WhatsAppAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]whatsapp.Account, 0, len(conns))
	for _, c := range conns {
		var creds store.WhatsAppCredentials
		if err := j.cipher.OpenJSON(c.Secret, &creds); err != nil || creds.DeviceJID == "" {
			continue
		}
		out = append(out, whatsapp.Account{UserID: c.UserID.Hex(), DeviceJID: creds.DeviceJID})
	}
	return out, nil
}

func (j *whatsAppJournal) SaveDevice(ctx context.Context, userID, deviceJID, label string) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	secret, err := j.cipher.SealJSON(store.WhatsAppCredentials{DeviceJID: deviceJID})
	if err != nil {
		return err
	}
	return j.store.UpsertConnection(ctx, store.Connection{
		UserID: id, Provider: store.ProviderWhatsApp,
		Status: "connected", Label: label, Secret: secret,
	})
}

func (j *whatsAppJournal) ForgetDevice(ctx context.Context, userID string) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	return j.store.DeleteConnection(ctx, id, store.ProviderWhatsApp)
}

func (j *whatsAppJournal) SaveMessages(ctx context.Context, userID string, msgs []whatsapp.Message) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	out := make([]store.WhatsAppMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, store.WhatsAppMessage{
			MessageID: m.ID,
			ChatJID:   m.ChatJID,
			ChatName:  m.ChatName,
			IsGroup:   m.IsGroup,
			SenderJID: m.SenderJID,
			Sender:    m.Sender,
			FromMe:    m.FromMe,
			Body:      m.Body,
			Kind:      m.Kind,
			Mentioned: m.Mentioned,
			Timestamp: m.Timestamp,
		})
	}
	return j.store.SaveWhatsAppMessages(ctx, id, out)
}

func (j *whatsAppJournal) SaveChats(ctx context.Context, userID string, chats []whatsapp.Chat) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	for _, c := range chats {
		err := j.store.SaveWhatsAppChat(ctx, id, store.WhatsAppChat{
			JID: c.JID, Name: c.Name, IsGroup: c.IsGroup,
			Muted: c.Muted, Archived: c.Archived,
			LastAt: c.LastAt, LastReadAt: c.LastReadAt,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (j *whatsAppJournal) TouchChat(ctx context.Context, userID, jid, name string, isGroup bool, at time.Time) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	return j.store.TouchWhatsAppChat(ctx, id, jid, name, isGroup, at)
}

func (j *whatsAppJournal) SetMuted(ctx context.Context, userID, jid string, muted bool) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	return j.store.SetWhatsAppChatMuted(ctx, id, jid, muted)
}

func (j *whatsAppJournal) SetArchived(ctx context.Context, userID, jid string, archived bool) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	return j.store.SetWhatsAppChatArchived(ctx, id, jid, archived)
}

func (j *whatsAppJournal) MarkChatRead(ctx context.Context, userID, jid string, at time.Time) error {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	return j.store.MarkWhatsAppChatRead(ctx, id, jid, at)
}
