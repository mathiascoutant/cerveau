package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Le tri des mails pro : jusqu'où la réception a été triée. Un document par
// utilisateur — l'UIDVALIDITY de la boîte et le dernier UID regardé.

// TriEtat : où en est le tri de la réception.
type TriEtat struct {
	UIDValidity uint32    `bson:"uid_validity"`
	DernierUID  uint32    `bson:"dernier_uid"`
	MisAJour    time.Time `bson:"mis_a_jour"`
}

func (s *Store) triMails() *mongo.Collection { return s.db.Collection("tri_mails") }

func (s *Store) ensureTriIndexes(ctx context.Context) error {
	_, err := s.triMails().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return err
}

// TriEtat rend où en est le tri ; ok est faux s'il n'a jamais tourné.
func (s *Store) TriEtat(ctx context.Context, userID bson.ObjectID) (TriEtat, bool, error) {
	var e TriEtat
	err := s.triMails().FindOne(ctx, bson.M{"user_id": userID}).Decode(&e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return TriEtat{}, false, nil
	}
	return e, err == nil, err
}

// TriNoter enregistre où en est le tri.
func (s *Store) TriNoter(ctx context.Context, userID bson.ObjectID, e TriEtat) error {
	e.MisAJour = time.Now()
	_, err := s.triMails().UpdateOne(ctx, bson.M{"user_id": userID},
		bson.M{"$set": bson.M{"uid_validity": e.UIDValidity, "dernier_uid": e.DernierUID, "mis_a_jour": e.MisAJour}},
		options.UpdateOne().SetUpsert(true))
	return err
}
