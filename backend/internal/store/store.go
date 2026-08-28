package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var ErrNotFound = errors.New("introuvable")

type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

func Connect(ctx context.Context, uri, dbName string) (*Store, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		return nil, err
	}
	s := &Store{client: client, db: client.Database(dbName)}
	if err := s.ensureIndexes(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

func (s *Store) users() *mongo.Collection       { return s.db.Collection("users") }
func (s *Store) connections() *mongo.Collection { return s.db.Collection("connections") }
func (s *Store) events() *mongo.Collection      { return s.db.Collection("calendar_events") }
func (s *Store) whatsapp() *mongo.Collection    { return s.db.Collection("whatsapp_messages") }
func (s *Store) whatsappChats() *mongo.Collection {
	return s.db.Collection("whatsapp_chats")
}
func (s *Store) interactions() *mongo.Collection {
	return s.db.Collection("interactions")
}
func (s *Store) digests() *mongo.Collection     { return s.db.Collection("digests") }
func (s *Store) emailDrafts() *mongo.Collection { return s.db.Collection("email_drafts") }
func (s *Store) taskLists() *mongo.Collection   { return s.db.Collection("task_lists") }
func (s *Store) todos() *mongo.Collection       { return s.db.Collection("todos") }

func (s *Store) ensureIndexes(ctx context.Context) error {
	// Un builder d'options par index : le driver mémorise le nom auto-généré,
	// donc partager la même instance ferait porter le nom du premier index à
	// tous les suivants (et échouer avec IndexKeySpecsConflict).
	unique := func() *options.IndexOptionsBuilder { return options.Index().SetUnique(true) }

	specs := []struct {
		col   *mongo.Collection
		model mongo.IndexModel
	}{
		{s.users(), mongo.IndexModel{Keys: bson.D{{Key: "device_id", Value: 1}}, Options: unique()}},
		{s.users(), mongo.IndexModel{Keys: bson.D{{Key: "token", Value: 1}}, Options: unique()}},
		{s.connections(), mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "provider", Value: 1}},
			Options: unique(),
		}},
		{s.events(), mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "external_id", Value: 1}},
			Options: unique(),
		}},
		{s.events(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "start", Value: 1}}}},
		{s.whatsapp(), mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "message_id", Value: 1}},
			Options: unique(),
		}},
		{s.whatsapp(), mongo.IndexModel{
			Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "chat_jid", Value: 1}, {Key: "timestamp", Value: -1}},
		}},
		{s.whatsappChats(), mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "jid", Value: 1}},
			Options: unique(),
		}},
		{s.interactions(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}}},
		{s.digests(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}}, Options: unique()}},
		{s.emailDrafts(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "updated_at", Value: -1}}}},
		{s.todos(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "done", Value: 1}, {Key: "due", Value: 1}}}},
	}
	for _, spec := range specs {
		if _, err := spec.col.Indexes().CreateOne(ctx, spec.model); err != nil {
			return err
		}
	}
	return nil
}

// --- Utilisateurs -----------------------------------------------------------

// EnsureUser crée l'utilisateur au premier lancement de l'app, ou le retrouve.
// C'est le remplaçant du login : l'identifiant d'appareil suffit.
func (s *Store) EnsureUser(ctx context.Context, deviceID, timezone string) (*User, error) {
	var u User
	err := s.users().FindOne(ctx, bson.M{"device_id": deviceID}).Decode(&u)
	if err == nil {
		update := bson.M{"last_seen": time.Now()}
		if timezone != "" && timezone != u.Timezone {
			update["timezone"] = timezone
			u.Timezone = timezone
		}
		if _, err := s.users().UpdateByID(ctx, u.ID, bson.M{"$set": update}); err != nil {
			return nil, err
		}
		return &u, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	u = User{
		DeviceID:  deviceID,
		Token:     token,
		Timezone:  timezone,
		CreatedAt: now,
		LastSeen:  now,
	}
	res, err := s.users().InsertOne(ctx, u)
	if err != nil {
		return nil, err
	}
	u.ID = res.InsertedID.(bson.ObjectID)
	return &u, nil
}

func (s *Store) UserByToken(ctx context.Context, token string) (*User, error) {
	var u User
	err := s.users().FindOne(ctx, bson.M{"token": token}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) SetUserName(ctx context.Context, userID bson.ObjectID, name string) error {
	_, err := s.users().UpdateByID(ctx, userID, bson.M{"$set": bson.M{"name": name}})
	return err
}

// --- Connexions -------------------------------------------------------------

func (s *Store) UpsertConnection(ctx context.Context, c Connection) error {
	c.UpdatedAt = time.Now()
	_, err := s.connections().UpdateOne(ctx,
		bson.M{"user_id": c.UserID, "provider": c.Provider},
		bson.M{"$set": bson.M{
			"status":     c.Status,
			"label":      c.Label,
			"secret":     c.Secret,
			"last_error": c.LastError,
			"updated_at": c.UpdatedAt,
		}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

func (s *Store) Connection(ctx context.Context, userID bson.ObjectID, provider string) (*Connection, error) {
	var c Connection
	err := s.connections().FindOne(ctx, bson.M{"user_id": userID, "provider": provider}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *Store) Connections(ctx context.Context, userID bson.ObjectID) ([]Connection, error) {
	cur, err := s.connections().Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	var out []Connection
	return out, cur.All(ctx, &out)
}

func (s *Store) DeleteConnection(ctx context.Context, userID bson.ObjectID, provider string) error {
	_, err := s.connections().DeleteOne(ctx, bson.M{"user_id": userID, "provider": provider})
	return err
}

func (s *Store) MarkConnectionError(ctx context.Context, userID bson.ObjectID, provider, msg string) {
	_, _ = s.connections().UpdateOne(ctx,
		bson.M{"user_id": userID, "provider": provider},
		bson.M{"$set": bson.M{"status": "error", "last_error": msg, "updated_at": time.Now()}},
	)
}

// --- Calendrier -------------------------------------------------------------

// ReplaceCalendarWindow remplace tous les événements de l'utilisateur sur la
// fenêtre synchronisée. L'app est la source de vérité : ce qu'elle n'envoie plus
// a été supprimé sur le téléphone.
func (s *Store) ReplaceCalendarWindow(ctx context.Context, userID bson.ObjectID, from, to time.Time, events []CalendarEvent) error {
	if _, err := s.events().DeleteMany(ctx, bson.M{
		"user_id": userID,
		"start":   bson.M{"$gte": from, "$lte": to},
	}); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	docs := make([]any, 0, len(events))
	now := time.Now()
	for _, e := range events {
		e.UserID = userID
		e.UpdatedAt = now
		docs = append(docs, e)
	}
	_, err := s.events().InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return err
	}
	return nil
}

func (s *Store) EventsBetween(ctx context.Context, userID bson.ObjectID, from, to time.Time) ([]CalendarEvent, error) {
	cur, err := s.events().Find(ctx,
		bson.M{"user_id": userID, "start": bson.M{"$lt": to}, "end": bson.M{"$gt": from}},
		options.Find().SetSort(bson.D{{Key: "start", Value: 1}}).SetLimit(100),
	)
	if err != nil {
		return nil, err
	}
	var out []CalendarEvent
	return out, cur.All(ctx, &out)
}

// KnownPlaces renvoie les lieux déjà rencontrés dans le calendrier, du plus
// récent au plus ancien.
//
// C'est le carnet d'adresses gratuit de Raoul : si l'utilisateur a déjà eu un
// rendez-vous chez PXCom, l'adresse exacte est dans le miroir de l'agenda. Ça
// évite d'envoyer Waze chercher un nom d'entreprise à l'aveugle.
func (s *Store) KnownPlaces(ctx context.Context, userID bson.ObjectID) ([]KnownPlace, error) {
	cur, err := s.events().Find(ctx,
		bson.M{"user_id": userID, "location": bson.M{"$nin": bson.A{"", nil}}},
		options.Find().
			SetSort(bson.D{{Key: "start", Value: -1}}).
			SetProjection(bson.M{"title": 1, "location": 1}).
			SetLimit(300),
	)
	if err != nil {
		return nil, err
	}
	var events []CalendarEvent
	if err := cur.All(ctx, &events); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(events))
	out := make([]KnownPlace, 0, len(events))
	for _, e := range events {
		addr := strings.TrimSpace(e.Location)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, KnownPlace{Titre: e.Title, Adresse: addr})
	}
	return out, nil
}

func (s *Store) InsertEvent(ctx context.Context, e CalendarEvent) error {
	e.UpdatedAt = time.Now()
	_, err := s.events().UpdateOne(ctx,
		bson.M{"user_id": e.UserID, "external_id": e.ExternalID},
		bson.M{"$set": e},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

// --- WhatsApp ---------------------------------------------------------------

// Fenêtre retenue pour une conversation dont on ne connaît pas l'état de
// lecture. Sans elle, une conversation jamais rouverte depuis la liaison
// remonterait tout son historique comme autant de messages non lus.
const WhatsAppUnreadWindow = 72 * time.Hour

// Nombre de conversations inspectées pour les non-lus. Au-delà, on regarde des
// fils que personne n'a touchés depuis des semaines.
const whatsAppScanDepth = 40

// SaveWhatsAppMessages archive un lot de messages.
//
// Écriture idempotente : l'historique poussé par le téléphone recouvre ce qui
// est déjà arrivé en direct, et une liaison peut renvoyer deux fois le même
// lot. On garde la première version — un message ne change pas.
func (s *Store) SaveWhatsAppMessages(ctx context.Context, userID bson.ObjectID, msgs []WhatsAppMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, 0, len(msgs))
	for _, m := range msgs {
		m.UserID = userID
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"user_id": userID, "message_id": m.MessageID}).
			SetUpdate(bson.M{"$setOnInsert": m}).
			SetUpsert(true))
	}
	// Non ordonné : un doublon au milieu du lot ne doit pas jeter la suite.
	_, err := s.whatsapp().BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if mongo.IsDuplicateKeyError(err) {
		return nil
	}
	return err
}

// SaveWhatsAppChat enregistre une conversation telle que l'historique la donne :
// nom, sourdine, archivage et état de lecture d'un coup.
func (s *Store) SaveWhatsAppChat(ctx context.Context, userID bson.ObjectID, chat WhatsAppChat) error {
	set := bson.M{"is_group": chat.IsGroup, "muted": chat.Muted, "archived": chat.Archived, "updated_at": time.Now()}
	if chat.Name != "" {
		set["name"] = chat.Name
	}
	return s.updateWhatsAppChat(ctx, userID, chat.JID, set, bson.M{
		"last_at":      chat.LastAt,
		"last_read_at": chat.LastReadAt,
	})
}

// TouchWhatsAppChat note qu'un message vient d'arriver dans une conversation.
func (s *Store) TouchWhatsAppChat(ctx context.Context, userID bson.ObjectID, jid, name string, isGroup bool, at time.Time) error {
	set := bson.M{"is_group": isGroup, "updated_at": time.Now()}
	if name != "" {
		set["name"] = name
	}
	return s.updateWhatsAppChat(ctx, userID, jid, set, bson.M{"last_at": at})
}

// SetWhatsAppChatMuted et SetWhatsAppChatArchived suivent ce qu'il fait depuis
// son téléphone. La sourdine compte double ici : elle décide de ce qui a le
// droit de remonter dans les urgences.
func (s *Store) SetWhatsAppChatMuted(ctx context.Context, userID bson.ObjectID, jid string, muted bool) error {
	return s.updateWhatsAppChat(ctx, userID, jid, bson.M{"muted": muted, "updated_at": time.Now()}, nil)
}

func (s *Store) SetWhatsAppChatArchived(ctx context.Context, userID bson.ObjectID, jid string, archived bool) error {
	return s.updateWhatsAppChat(ctx, userID, jid, bson.M{"archived": archived, "updated_at": time.Now()}, nil)
}

// MarkWhatsAppChatRead avance l'état de lecture d'une conversation.
func (s *Store) MarkWhatsAppChatRead(ctx context.Context, userID bson.ObjectID, jid string, at time.Time) error {
	return s.updateWhatsAppChat(ctx, userID, jid, bson.M{"updated_at": time.Now()}, bson.M{"last_read_at": at})
}

// updateWhatsAppChat applique une mise à jour partielle.
//
// Les horodatages passent par $max et jamais par $set : les événements
// n'arrivent pas dans l'ordre — un lot d'historique atterrit après les messages
// du jour — et un $set ferait reculer la dernière lecture, donc réapparaître
// comme non lu ce qui avait été lu.
func (s *Store) updateWhatsAppChat(ctx context.Context, userID bson.ObjectID, jid string, set, latest bson.M) error {
	if jid == "" {
		return nil
	}
	update := bson.M{
		"$set":         set,
		"$setOnInsert": bson.M{"user_id": userID, "jid": jid},
	}
	max := bson.M{}
	for key, value := range latest {
		if ts, ok := value.(time.Time); ok && !ts.IsZero() {
			max[key] = ts
		}
	}
	if len(max) > 0 {
		update["$max"] = max
	}
	_, err := s.whatsappChats().UpdateOne(ctx,
		bson.M{"user_id": userID, "jid": jid}, update, options.UpdateOne().SetUpsert(true))
	return err
}

// WhatsAppChats rend les conversations connues, la plus active en tête.
func (s *Store) WhatsAppChats(ctx context.Context, userID bson.ObjectID) ([]WhatsAppChat, error) {
	cur, err := s.whatsappChats().Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "last_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	var out []WhatsAppChat
	return out, cur.All(ctx, &out)
}

// WhatsAppMessages rend le contenu d'une conversation, du plus ancien au plus
// récent — l'ordre dans lequel un fil se lit.
//
// `since` borne par le bas quand on ne veut que la suite (« depuis mon dernier
// message »). `limit` compte à partir de la fin : ce sont les derniers messages
// qui intéressent, pas les premiers.
func (s *Store) WhatsAppMessages(ctx context.Context, userID bson.ObjectID, chatJID string, since time.Time, limit int64) ([]WhatsAppMessage, error) {
	filter := bson.M{"user_id": userID, "chat_jid": chatJID}
	if !since.IsZero() {
		filter["timestamp"] = bson.M{"$gt": since}
	}
	cur, err := s.whatsapp().Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []WhatsAppMessage
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// LastOwnWhatsAppMessage rend le dernier message que l'utilisateur a écrit dans
// une conversation. C'est le repère de « depuis mon dernier message » : ce
// qu'il a dit lui-même est le seul endroit du fil dont il est sûr de se
// souvenir.
func (s *Store) LastOwnWhatsAppMessage(ctx context.Context, userID bson.ObjectID, chatJID string) (*WhatsAppMessage, error) {
	var m WhatsAppMessage
	err := s.whatsapp().FindOne(ctx,
		bson.M{"user_id": userID, "chat_jid": chatJID, "from_me": true},
		options.FindOne().SetSort(bson.D{{Key: "timestamp", Value: -1}}),
	).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UnreadWhatsApp rend les conversations où quelque chose n'a pas été lu.
//
// Les conversations en sourdine et archivées sont écartées : les mettre en
// sourdine est une décision que l'utilisateur a déjà prise, et la contredire
// tous les matins revient à la lui redemander.
func (s *Store) UnreadWhatsApp(ctx context.Context, userID bson.ObjectID, limit int) ([]WhatsAppThread, error) {
	chats, err := s.WhatsAppChats(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}

	out := make([]WhatsAppThread, 0, limit)
	scanned := 0
	for _, chat := range chats {
		if len(out) >= limit || scanned >= whatsAppScanDepth {
			break
		}
		if chat.Muted || chat.Archived || chat.LastAt.IsZero() {
			continue
		}
		scanned++

		since := chat.LastReadAt
		if since.IsZero() {
			since = time.Now().Add(-WhatsAppUnreadWindow)
		}
		if !chat.LastAt.After(since) {
			continue
		}

		msgs, err := s.WhatsAppMessages(ctx, userID, chat.JID, since, 30)
		if err != nil {
			return nil, err
		}
		thread := WhatsAppThread{Chat: chat}
		for _, m := range msgs {
			if m.FromMe {
				continue
			}
			thread.Unread++
			if m.Mentioned {
				thread.Mentions++
			}
			thread.Latest = append(thread.Latest, m)
		}
		if thread.Unread == 0 {
			continue
		}
		// Les cinq derniers suffisent à dire de quoi il retourne ; le reste se
		// lit avec lire_conversation_whatsapp, si la question se pose.
		if len(thread.Latest) > 5 {
			thread.Latest = thread.Latest[len(thread.Latest)-5:]
		}
		out = append(out, thread)
	}
	return out, nil
}

// WhatsAppAccounts liste les comptes liés, tous utilisateurs confondus. Sert au
// démarrage du serveur : un appareil lié qui n'est pas reconnecté ne reçoit
// rien, et ce qu'il n'a pas reçu ne se rattrape pas.
func (s *Store) WhatsAppAccounts(ctx context.Context) ([]Connection, error) {
	cur, err := s.connections().Find(ctx, bson.M{"provider": ProviderWhatsApp})
	if err != nil {
		return nil, err
	}
	var out []Connection
	return out, cur.All(ctx, &out)
}

// --- Interactions -----------------------------------------------------------

func (s *Store) SaveInteraction(ctx context.Context, it Interaction) error {
	it.CreatedAt = time.Now()
	_, err := s.interactions().InsertOne(ctx, it)
	return err
}

func (s *Store) RecentInteractions(ctx context.Context, userID bson.ObjectID, limit int64) ([]Interaction, error) {
	cur, err := s.interactions().Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []Interaction
	return out, cur.All(ctx, &out)
}

// SaveDigest remplace la synthèse de l'utilisateur.
func (s *Store) SaveDigest(ctx context.Context, userID bson.ObjectID, summary string) error {
	_, err := s.digests().UpdateOne(ctx,
		bson.M{"user_id": userID},
		bson.M{"$set": bson.M{"summary": summary, "generated_at": time.Now()}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

// LatestDigest renvoie ErrNotFound si aucune synthèse n'a encore été produite.
func (s *Store) LatestDigest(ctx context.Context, userID bson.ObjectID) (*Digest, error) {
	var d Digest
	err := s.digests().FindOne(ctx, bson.M{"user_id": userID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &d, err
}

// SaveTasks remplace la liste « à traiter » de l'utilisateur.
func (s *Store) SaveTasks(ctx context.Context, userID bson.ObjectID, payload, fingerprint string) error {
	_, err := s.taskLists().UpdateOne(ctx,
		bson.M{"user_id": userID},
		bson.M{"$set": bson.M{
			"payload":      payload,
			"fingerprint":  fingerprint,
			"generated_at": time.Now(),
		}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

// LatestTasks renvoie ErrNotFound si aucune liste n'a encore été produite.
func (s *Store) LatestTasks(ctx context.Context, userID bson.ObjectID) (*TaskList, error) {
	var t TaskList
	err := s.taskLists().FindOne(ctx, bson.M{"user_id": userID}).Decode(&t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &t, err
}

// SearchInteractions retrouve des échanges passés par leur contenu.
//
// C'est la mémoire longue de Raoul : l'historique récent voyage déjà dans le
// contexte du modèle, mais « ce dont on parlait ce matin » peut être vingt
// tours en arrière. Une recherche plein texte sur la demande ET la réponse
// rattrape ce que la fenêtre de contexte a laissé tomber.
func (s *Store) SearchInteractions(ctx context.Context, userID bson.ObjectID, query string, since time.Time, limit int64) ([]Interaction, error) {
	filter := bson.M{"user_id": userID}
	if !since.IsZero() {
		filter["created_at"] = bson.M{"$gte": since}
	}
	if q := strings.TrimSpace(query); q != "" {
		rx := bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}
		filter["$or"] = bson.A{bson.M{"transcript": rx}, bson.M{"reply": rx}}
	}
	if limit <= 0 {
		limit = 12
	}
	cur, err := s.interactions().Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []Interaction
	return out, cur.All(ctx, &out)
}

// --- Brouillons de mail -----------------------------------------------------

func (s *Store) SaveEmailDraft(ctx context.Context, d EmailDraft) (EmailDraft, error) {
	now := time.Now()
	d.CreatedAt = now
	d.UpdatedAt = now
	res, err := s.emailDrafts().InsertOne(ctx, d)
	if err != nil {
		return EmailDraft{}, err
	}
	d.ID = res.InsertedID.(bson.ObjectID)
	return d, nil
}

// UpdateEmailDraft réécrit un brouillon. Un objet vide laisse l'ancien en
// place : on modifie souvent le corps sans retoucher l'objet.
func (s *Store) UpdateEmailDraft(ctx context.Context, userID, id bson.ObjectID, subject, body string) (EmailDraft, error) {
	set := bson.M{"body": body, "updated_at": time.Now()}
	if strings.TrimSpace(subject) != "" {
		set["subject"] = subject
	}
	var out EmailDraft
	err := s.emailDrafts().FindOneAndUpdate(ctx,
		bson.M{"_id": id, "user_id": userID},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return EmailDraft{}, ErrNotFound
	}
	return out, err
}

// EmailDrafts liste les brouillons du plus récemment touché au plus ancien.
// Une recherche vide les rend tous : c'est ce que consulte l'onglet de l'app.
func (s *Store) EmailDrafts(ctx context.Context, userID bson.ObjectID, query string, limit int64) ([]EmailDraft, error) {
	filter := bson.M{"user_id": userID}
	if q := strings.TrimSpace(query); q != "" {
		rx := bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}
		filter["$or"] = bson.A{
			bson.M{"to": rx},
			bson.M{"to_addr": rx},
			bson.M{"subject": rx},
			bson.M{"source_subject": rx},
			bson.M{"body": rx},
		}
	}
	if limit <= 0 {
		limit = 50
	}
	cur, err := s.emailDrafts().Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []EmailDraft
	return out, cur.All(ctx, &out)
}

func (s *Store) EmailDraft(ctx context.Context, userID, id bson.ObjectID) (EmailDraft, error) {
	var d EmailDraft
	err := s.emailDrafts().FindOne(ctx, bson.M{"_id": id, "user_id": userID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return EmailDraft{}, ErrNotFound
	}
	return d, err
}

func (s *Store) DeleteEmailDraft(ctx context.Context, userID, id bson.ObjectID) error {
	res, err := s.emailDrafts().DeleteOne(ctx, bson.M{"_id": id, "user_id": userID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// --- Liste à faire ----------------------------------------------------------

func (s *Store) SaveTodo(ctx context.Context, t Todo) (Todo, error) {
	now := time.Now()
	t.CreatedAt = now
	t.UpdatedAt = now
	res, err := s.todos().InsertOne(ctx, t)
	if err != nil {
		return Todo{}, err
	}
	t.ID = res.InsertedID.(bson.ObjectID)
	return t, nil
}

// Todos lit la liste, triée comme elle se lit : par échéance croissante, ce qui
// n'a pas de jour à la fin.
//
// Le tri se fait ici et pas dans Mongo parce qu'une échéance absente y remonte
// en tête d'un tri croissant — les tâches sans date passeraient devant celles
// d'aujourd'hui, ce qui est exactement l'inverse de ce qu'on veut lire.
func (s *Store) Todos(ctx context.Context, userID bson.ObjectID, q TodoQuery) ([]Todo, error) {
	clauses := []bson.M{{"user_id": userID}}

	// Ce qui reste à faire, plus éventuellement ce qui vient d'être coché.
	state := []bson.M{{"done": false}}
	if !q.DoneSince.IsZero() {
		state = append(state, bson.M{"done": true, "done_at": bson.M{"$gte": q.DoneSince}})
	}
	clauses = append(clauses, bson.M{"$or": state})

	window := bson.M{}
	if !q.From.IsZero() {
		window["$gte"] = q.From
	}
	if !q.To.IsZero() {
		window["$lt"] = q.To
	}
	// Aucune borne et les non datées admises : il n'y a rien à filtrer.
	if len(window) > 0 || !q.Undated {
		dated := bson.M{"due": bson.M{"$ne": nil}}
		if len(window) > 0 {
			dated = bson.M{"due": window}
		}
		date := []bson.M{dated}
		if q.Undated {
			// « due: nil » attrape aussi le champ absent, qui est la forme
			// réelle d'une tâche sans jour (omitempty à l'écriture).
			date = append(date, bson.M{"due": nil})
		}
		clauses = append(clauses, bson.M{"$or": date})
	}

	if search := strings.TrimSpace(q.Search); search != "" {
		rx := bson.M{"$regex": regexp.QuoteMeta(search), "$options": "i"}
		clauses = append(clauses, bson.M{"$or": bson.A{bson.M{"title": rx}, bson.M{"note": rx}}})
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	cur, err := s.todos().Find(ctx, bson.M{"$and": clauses}, options.Find().SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var out []Todo
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Due, out[j].Due
		if (a == nil) != (b == nil) {
			return b == nil
		}
		if a != nil && !a.Equal(*b) {
			return a.Before(*b)
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// SetTodoDone coche ou décoche. DoneAt est effacé au décochage : c'est lui qui
// fait rester une tâche cochée à l'écran quelques heures, et une tâche rouverte
// n'a rien à y faire.
func (s *Store) SetTodoDone(ctx context.Context, userID, id bson.ObjectID, done bool) (Todo, error) {
	update := bson.M{"$set": bson.M{"done": done, "updated_at": time.Now()}}
	if done {
		now := time.Now()
		update["$set"].(bson.M)["done_at"] = now
	} else {
		update["$unset"] = bson.M{"done_at": ""}
	}
	return s.updateTodo(ctx, userID, id, update)
}

// RescheduleTodo change le jour retenu. Un due nul remet la tâche sans date.
func (s *Store) RescheduleTodo(ctx context.Context, userID, id bson.ObjectID, due *time.Time, timed bool) (Todo, error) {
	update := bson.M{"$set": bson.M{"timed": timed, "updated_at": time.Now()}}
	if due == nil {
		update["$unset"] = bson.M{"due": ""}
	} else {
		update["$set"].(bson.M)["due"] = *due
	}
	return s.updateTodo(ctx, userID, id, update)
}

func (s *Store) updateTodo(ctx context.Context, userID, id bson.ObjectID, update bson.M) (Todo, error) {
	var out Todo
	err := s.todos().FindOneAndUpdate(ctx,
		bson.M{"_id": id, "user_id": userID},
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Todo{}, ErrNotFound
	}
	return out, err
}

func (s *Store) DeleteTodo(ctx context.Context, userID, id bson.ObjectID) error {
	res, err := s.todos().DeleteOne(ctx, bson.M{"_id": id, "user_id": userID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}
