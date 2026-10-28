package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Les panneaux des projets, dans Cerveau HQ.
//
// Un panneau, c'est ce que Mathias veut voir d'un projet d'un coup d'œil : les
// jobs en cours de pxsky, les dernières erreurs d'un service, un compteur. Il
// le demande à Hugo ou à l'ingénieur ; l'ingénieur écrit un petit script qui va
// chercher les données et les rend dans un format que HQ sait dessiner
// (tableau, chiffres, barres…). Le script tourne dans l'atelier, là où sont
// les accès ; HQ le relance à son rythme tant qu'on regarde le panneau. Aucun
// code de HQ ne change : le panneau apparaît dès qu'il est enregistré.

// Largeurs d'un panneau dans la grille du projet.
const (
	PanneauNormal = "normal"
	PanneauLarge  = "large"
)

// Panneau : un panneau du tableau de bord d'un projet.
type Panneau struct {
	ID     bson.ObjectID `bson:"_id,omitempty" json:"id"`
	Projet string        `bson:"projet" json:"projet"`
	Titre  string        `bson:"titre" json:"titre"`
	// Demande : ce que Mathias a demandé, tel qu'il l'a dit.
	Demande string `bson:"demande,omitempty" json:"demande,omitempty"`
	// Script : du Python 3 qui imprime les données du panneau en JSON.
	Script string `bson:"script" json:"script,omitempty"`
	// Rafraichir : toutes les combien de secondes le relancer.
	Rafraichir int     `bson:"rafraichir_s" json:"rafraichir_s"`
	Largeur    string  `bson:"largeur" json:"largeur"`
	Ordre      float64 `bson:"ordre" json:"ordre"`
	// Auteur : l'agent qui l'a écrit ; Run, sa tâche.
	Auteur    string         `bson:"auteur,omitempty" json:"auteur,omitempty"`
	Run       *bson.ObjectID `bson:"run,omitempty" json:"run,omitempty"`
	CreatedAt time.Time      `bson:"created_at" json:"cree"`
	UpdatedAt time.Time      `bson:"updated_at" json:"maj"`
}

func (s *Store) panneaux() *mongo.Collection { return s.db.Collection("panneaux") }

func (s *Store) ensurePanneauIndexes(ctx context.Context) error {
	_, err := s.panneaux().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "projet", Value: 1}, {Key: "ordre", Value: 1}},
	})
	return err
}

// Panneaux rend les panneaux d'un projet, ou de tous avec projet vide, dans
// leur ordre.
func (s *Store) Panneaux(ctx context.Context, projet string) ([]Panneau, error) {
	filter := bson.M{}
	if projet != "" {
		filter["projet"] = projet
	}
	cur, err := s.panneaux().Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "ordre", Value: 1}, {Key: "created_at", Value: 1}}).SetLimit(500))
	if err != nil {
		return nil, err
	}
	out := []Panneau{}
	return out, cur.All(ctx, &out)
}

// Panneau rend un panneau.
func (s *Store) Panneau(ctx context.Context, id bson.ObjectID) (Panneau, error) {
	var p Panneau
	err := s.panneaux().FindOne(ctx, bson.M{"_id": id}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Panneau{}, ErrNotFound
	}
	return p, err
}

// EcrirePanneau crée un panneau, ou remplace celui qui porte p.ID. Un
// nouveau panneau va en bas du tableau.
func (s *Store) EcrirePanneau(ctx context.Context, p Panneau) (Panneau, error) {
	now := time.Now()
	p.UpdatedAt = now
	if p.ID.IsZero() {
		p.ID, p.CreatedAt = bson.NewObjectID(), now
		if p.Ordre == 0 {
			p.Ordre = float64(now.UnixMilli())
		}
		_, err := s.panneaux().InsertOne(ctx, p)
		return p, err
	}
	set := bson.M{
		"titre": p.Titre, "script": p.Script, "rafraichir_s": p.Rafraichir, "largeur": p.Largeur,
		"auteur": p.Auteur, "updated_at": now,
	}
	if p.Demande != "" {
		set["demande"] = p.Demande
	}
	if p.Run != nil {
		set["run"] = p.Run
	}
	var out Panneau
	err := s.panneaux().FindOneAndUpdate(ctx, bson.M{"_id": p.ID, "projet": p.Projet}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Panneau{}, ErrNotFound
	}
	return out, err
}

// ModifierPanneau change ce que Mathias règle depuis HQ : titre, largeur,
// ordre.
func (s *Store) ModifierPanneau(ctx context.Context, id bson.ObjectID, set bson.M) (Panneau, error) {
	set["updated_at"] = time.Now()
	var out Panneau
	err := s.panneaux().FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Panneau{}, ErrNotFound
	}
	return out, err
}

// SupprimerPanneau retire un panneau.
func (s *Store) SupprimerPanneau(ctx context.Context, id bson.ObjectID) error {
	res, err := s.panneaux().DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}
