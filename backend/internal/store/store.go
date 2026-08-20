package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
func (s *Store) interactions() *mongo.Collection {
	return s.db.Collection("interactions")
}
func (s *Store) digests() *mongo.Collection { return s.db.Collection("digests") }

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
		{s.whatsapp(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "timestamp", Value: -1}}}},
		{s.interactions(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}}},
		{s.digests(), mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}}, Options: unique()}},
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

// UserByWhatsAppPhoneNumberID retrouve le propriétaire d'un numéro WhatsApp
// Business : le webhook Meta n'est pas authentifié côté utilisateur, on résout
// donc le destinataire à partir du phone_number_id reçu.
func (s *Store) UserByWhatsAppPhoneNumberID(ctx context.Context, phoneNumberID string) (*User, error) {
	var c Connection
	err := s.connections().FindOne(ctx, bson.M{
		"provider": ProviderWhatsApp,
		"label":    phoneNumberID,
	}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var u User
	if err := s.users().FindOne(ctx, bson.M{"_id": c.UserID}).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
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

func (s *Store) SaveWhatsAppMessage(ctx context.Context, m WhatsAppMessage) error {
	_, err := s.whatsapp().UpdateOne(ctx,
		bson.M{"user_id": m.UserID, "message_id": m.MessageID},
		bson.M{"$setOnInsert": m},
		options.UpdateOne().SetUpsert(true),
	)
	if mongo.IsDuplicateKeyError(err) {
		return nil
	}
	return err
}

func (s *Store) UnreadWhatsApp(ctx context.Context, userID bson.ObjectID, limit int64) ([]WhatsAppMessage, error) {
	cur, err := s.whatsapp().Find(ctx,
		bson.M{"user_id": userID, "read": false},
		options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []WhatsAppMessage
	return out, cur.All(ctx, &out)
}

func (s *Store) MarkWhatsAppRead(ctx context.Context, userID bson.ObjectID, ids []string) error {
	filter := bson.M{"user_id": userID}
	if len(ids) > 0 {
		filter["message_id"] = bson.M{"$in": ids}
	}
	_, err := s.whatsapp().UpdateMany(ctx, filter, bson.M{"$set": bson.M{"read": true}})
	return err
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

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
