package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// À qui il parle.
//
// « Passe-moi l'ingénieur » change d'interlocuteur pour la suite de la
// conversation, pas pour une seule réponse : la question d'après est pour
// l'ingénieur aussi, sans avoir à le redemander. L'état vit ici plutôt que
// dans l'app, pour que Siri, le widget et un autre téléphone tombent sur le
// même interlocuteur.
//
// Il s'éteint de lui-même : passé un moment sans échange, on revient à Raoul.
// Retrouver l'ingénieur au bout du fil le lendemain matin pour demander son
// agenda serait une surprise, pas un service.

// InterlocuteurTTL : le silence au-delà duquel on revient à Raoul.
const InterlocuteurTTL = 30 * time.Minute

type interlocuteur struct {
	UserID bson.ObjectID `bson:"user_id"`
	Agent  string        `bson:"agent"`
	Depuis time.Time     `bson:"depuis"`
	// Dernier : le dernier échange avec lui, qui repousse l'échéance.
	Dernier time.Time `bson:"dernier"`
}

func (s *Store) interlocuteurs() *mongo.Collection { return s.db.Collection("interlocuteurs") }

func (s *Store) ensureInterlocuteurIndexes(ctx context.Context) error {
	_, err := s.interlocuteurs().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return err
}

// Interlocuteur rend l'agent qui a la parole, ou "" pour Raoul.
func (s *Store) Interlocuteur(ctx context.Context, userID bson.ObjectID) (string, error) {
	var it interlocuteur
	err := s.interlocuteurs().FindOne(ctx, bson.M{"user_id": userID}).Decode(&it)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if time.Since(it.Dernier) > InterlocuteurTTL {
		return "", nil
	}
	return it.Agent, nil
}

// PasserLaParole donne la parole à un agent ; "" la rend à Raoul.
func (s *Store) PasserLaParole(ctx context.Context, userID bson.ObjectID, agent string) error {
	if agent == "" {
		_, err := s.interlocuteurs().DeleteOne(ctx, bson.M{"user_id": userID})
		return err
	}
	now := time.Now()
	_, err := s.interlocuteurs().UpdateOne(ctx,
		bson.M{"user_id": userID},
		bson.M{"$set": bson.M{"agent": agent, "depuis": now, "dernier": now}},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

// GarderLaParole repousse l'échéance après un échange avec l'interlocuteur.
func (s *Store) GarderLaParole(ctx context.Context, userID bson.ObjectID) {
	_, _ = s.interlocuteurs().UpdateOne(ctx,
		bson.M{"user_id": userID},
		bson.M{"$set": bson.M{"dernier": time.Now()}},
	)
}
