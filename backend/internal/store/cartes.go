package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Le tableau des tâches de l'équipe.
//
// Une carte, c'est une tâche d'un projet : ce qu'il faut faire, qui s'en
// charge, pour quand et avec quelle priorité. Elle vit dans quatre colonnes —
// à faire, en cours, à vérifier, terminé. Mathias la déplace à la main, ou la
// confie à son agent : elle passe alors en cours, avance avec son travail, et
// arrive « à vérifier » avec son compte rendu. Comme les projets, le tableau
// est celui du serveur, pas d'un compte.

// Colonnes du tableau.
const (
	CarteAFaire    = "a_faire"
	CarteEnCours   = "en_cours"
	CarteAVerifier = "a_verifier"
	CarteTerminee  = "termine"
)

// Priorités d'une carte.
const (
	PrioriteBasse   = "basse"
	PrioriteNormale = "normale"
	PrioriteHaute   = "haute"
	PrioriteUrgente = "urgente"
)

// Carte : une tâche du tableau.
type Carte struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"id"`
	// Projet : l'identifiant du projet, vide pour une tâche hors projet.
	Projet      string `bson:"projet,omitempty" json:"projet,omitempty"`
	Titre       string `bson:"titre" json:"titre"`
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	Statut      string `bson:"statut" json:"statut"`
	// Agent : celui qui s'en charge.
	Agent    string     `bson:"agent,omitempty" json:"agent,omitempty"`
	Priorite string     `bson:"priorite" json:"priorite"`
	Echeance *time.Time `bson:"echeance,omitempty" json:"echeance,omitempty"`
	// Ordre : sa place dans la colonne, les plus petits en haut.
	Ordre float64 `bson:"ordre" json:"ordre"`
	// Run : la dernière exécution qui l'a traitée ; CompteRendu, sa réponse.
	Run         *bson.ObjectID `bson:"run,omitempty" json:"run,omitempty"`
	CompteRendu string         `bson:"compte_rendu,omitempty" json:"compte_rendu,omitempty"`
	Erreur      string         `bson:"erreur,omitempty" json:"erreur,omitempty"`
	CreatedAt   time.Time      `bson:"created_at" json:"creee"`
	UpdatedAt   time.Time      `bson:"updated_at" json:"maj"`
	TermineeLe  *time.Time     `bson:"terminee_le,omitempty" json:"terminee_le,omitempty"`
}

func (s *Store) cartes() *mongo.Collection { return s.db.Collection("cartes") }

func (s *Store) ensureCarteIndexes(ctx context.Context) error {
	_, err := s.cartes().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "projet", Value: 1}, {Key: "statut", Value: 1}, {Key: "ordre", Value: 1}}},
		{Keys: bson.D{{Key: "statut", Value: 1}, {Key: "run", Value: 1}}},
	})
	return err
}

// Cartes rend les cartes d'un projet, ou toutes avec projet vide ; les
// terminées depuis plus de trente jours ne reviennent plus.
func (s *Store) Cartes(ctx context.Context, projet string) ([]Carte, error) {
	filter := bson.M{"$or": []bson.M{
		{"statut": bson.M{"$ne": CarteTerminee}},
		{"terminee_le": bson.M{"$gte": time.Now().AddDate(0, 0, -30)}},
	}}
	if projet != "" {
		filter["projet"] = projet
	}
	cur, err := s.cartes().Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "ordre", Value: 1}, {Key: "created_at", Value: 1}}).SetLimit(1000))
	if err != nil {
		return nil, err
	}
	out := []Carte{}
	return out, cur.All(ctx, &out)
}

// Carte rend une carte.
func (s *Store) Carte(ctx context.Context, id bson.ObjectID) (Carte, error) {
	var c Carte
	err := s.cartes().FindOne(ctx, bson.M{"_id": id}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Carte{}, ErrNotFound
	}
	return c, err
}

// CreerCarte ajoute une carte, en bas de sa colonne.
func (s *Store) CreerCarte(ctx context.Context, c Carte) (Carte, error) {
	now := time.Now()
	c.ID, c.CreatedAt, c.UpdatedAt = bson.NewObjectID(), now, now
	if c.Ordre == 0 {
		c.Ordre = float64(now.UnixMilli())
	}
	if c.Statut == CarteTerminee {
		c.TermineeLe = &now
	}
	_, err := s.cartes().InsertOne(ctx, c)
	return c, err
}

// ModifierCarte change les champs donnés et rend la carte à jour. unset retire
// des champs (l'échéance qu'on efface).
func (s *Store) ModifierCarte(ctx context.Context, id bson.ObjectID, set bson.M, unset ...string) (Carte, error) {
	now := time.Now()
	set["updated_at"] = now
	if st, ok := set["statut"]; ok {
		if st == CarteTerminee {
			set["terminee_le"] = now
		} else {
			unset = append(unset, "terminee_le")
		}
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		u := bson.M{}
		for _, f := range unset {
			u[f] = ""
		}
		update["$unset"] = u
	}
	var out Carte
	err := s.cartes().FindOneAndUpdate(ctx, bson.M{"_id": id}, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Carte{}, ErrNotFound
	}
	return out, err
}

// SupprimerCarte retire une carte.
func (s *Store) SupprimerCarte(ctx context.Context, id bson.ObjectID) error {
	res, err := s.cartes().DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}
