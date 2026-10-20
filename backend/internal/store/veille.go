package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// La veille : des agents qui relèvent régulièrement les mails pro, Slack et
// WhatsApp, et préviennent dans Cerveau HQ quand une information compte.
//
// veille_vus retient ce que chaque agent a déjà regardé, pour ne juger que le
// neuf ; notifications garde ce qu'ils ont jugé important.

// RunSourceVeille : une vérification de la veille.
const RunSourceVeille = "veille"

const (
	veilleVuTTL       = 21 * 24 * time.Hour
	notificationTTL   = 60 * 24 * time.Hour
	notificationsLues = 30
)

// Notification : une information jugée importante par un agent de veille.
type Notification struct {
	ID         bson.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID     bson.ObjectID `bson:"user_id" json:"-"`
	Agent      string        `bson:"agent" json:"agent"`
	Source     string        `bson:"source" json:"source"` // mail, slack, whatsapp
	Titre      string        `bson:"titre" json:"titre"`
	Resume     string        `bson:"resume" json:"resume"`
	Importance string        `bson:"importance" json:"importance"` // haute, moyenne
	// Messages : d'où vient l'information (expéditeur, fil, canal…).
	Messages  []string   `bson:"messages,omitempty" json:"messages,omitempty"`
	Run       string     `bson:"run,omitempty" json:"run,omitempty"`
	CreatedAt time.Time  `bson:"created_at" json:"creee"`
	LueAt     *time.Time `bson:"lue_at,omitempty" json:"lue,omitempty"`
}

func (s *Store) veilleVus() *mongo.Collection     { return s.db.Collection("veille_vus") }
func (s *Store) notifications() *mongo.Collection { return s.db.Collection("notifications") }

func (s *Store) ensureVeilleIndexes(ctx context.Context) error {
	if _, err := s.veilleVus().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "agent", Value: 1}, {Key: "cle", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "vu_le", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(int32(veilleVuTTL / time.Second))},
	}); err != nil {
		return err
	}
	_, err := s.notifications().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "created_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(int32(notificationTTL / time.Second))},
	})
	return err
}

// VeilleDejaVus rend, parmi ces clés, celles que l'agent a déjà regardées.
func (s *Store) VeilleDejaVus(ctx context.Context, userID bson.ObjectID, agent string, cles []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(cles) == 0 {
		return out, nil
	}
	cur, err := s.veilleVus().Find(ctx, bson.M{"user_id": userID, "agent": agent, "cle": bson.M{"$in": cles}})
	if err != nil {
		return nil, err
	}
	var docs []struct {
		Cle string `bson:"cle"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		out[d.Cle] = true
	}
	return out, nil
}

// VeilleDemarree : l'agent a-t-il déjà fait une première relève pour ce
// compte ? La première ne fait que noter l'existant, sans rien signaler.
func (s *Store) VeilleDemarree(ctx context.Context, userID bson.ObjectID, agent string) (bool, error) {
	err := s.veilleVus().FindOne(ctx, bson.M{"user_id": userID, "agent": agent}).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	return err == nil, err
}

// VeilleMarquerVus note des clés comme regardées.
func (s *Store) VeilleMarquerVus(ctx context.Context, userID bson.ObjectID, agent string, cles []string) error {
	if len(cles) == 0 {
		return nil
	}
	now := time.Now()
	models := make([]mongo.WriteModel, 0, len(cles))
	for _, c := range cles {
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"user_id": userID, "agent": agent, "cle": c}).
			SetUpdate(bson.M{"$set": bson.M{"vu_le": now}}).
			SetUpsert(true))
	}
	_, err := s.veilleVus().BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	return err
}

// InsertNotification enregistre une notification.
func (s *Store) InsertNotification(ctx context.Context, n Notification) (Notification, error) {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	res, err := s.notifications().InsertOne(ctx, n)
	if err != nil {
		return Notification{}, err
	}
	n.ID = res.InsertedID.(bson.ObjectID)
	return n, nil
}

// Notifications rend les dernières notifications, et combien ne sont pas lues.
func (s *Store) Notifications(ctx context.Context, userID bson.ObjectID) ([]Notification, int64, error) {
	cur, err := s.notifications().Find(ctx, bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(notificationsLues))
	if err != nil {
		return nil, 0, err
	}
	var out []Notification
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	n, err := s.notifications().CountDocuments(ctx, bson.M{"user_id": userID, "lue_at": bson.M{"$exists": false}})
	return out, n, err
}

// MarquerNotificationsLues marque une notification lue, ou toutes si id est nil.
func (s *Store) MarquerNotificationsLues(ctx context.Context, userID bson.ObjectID, id *bson.ObjectID) error {
	filter := bson.M{"user_id": userID, "lue_at": bson.M{"$exists": false}}
	if id != nil {
		filter["_id"] = *id
	}
	_, err := s.notifications().UpdateMany(ctx, filter, bson.M{"$set": bson.M{"lue_at": time.Now()}})
	return err
}
