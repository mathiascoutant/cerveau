package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Les process CSP que l'équipe propose de lancer.
//
// Un CSP envoie l'Excel d'un cycle par mail ; l'agent mail pro le voit en
// rangeant le mail, reconnaît l'airline et le cycle, et propose à Mathias de
// lancer le process dans pxsky. Rien ne part sans son clic : la proposition
// attend dans HQ, et c'est lui qui la lance — ou l'ignore.

// États d'une proposition.
const (
	CSPProposee = "proposee"
	// CSPLancement : le clic est parti, pxsky n'a pas encore répondu.
	CSPLancement = "lancement"
	CSPLancee    = "lancee"
	CSPIgnoree   = "ignoree"
	CSPErreur    = "erreur"
)

// ImportCSP : un import de pxsky, tel qu'on le cite.
type ImportCSP struct {
	Job     string    `bson:"job" json:"job"`
	Statut  string    `bson:"statut" json:"statut"`
	Le      time.Time `bson:"le" json:"le"`
	Version int       `bson:"version,omitempty" json:"version,omitempty"`
}

// PropositionCSP : un process CSP à lancer, venu d'un mail.
type PropositionCSP struct {
	ID     bson.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID bson.ObjectID `bson:"user_id" json:"-"`
	// Le mail : son Message-ID (pour retrouver l'Excel où qu'il soit rangé),
	// son objet, qui l'a envoyé et quand.
	MessageID string    `bson:"message_id" json:"-"`
	Objet     string    `bson:"objet" json:"objet"`
	De        string    `bson:"de" json:"de"`
	Recu      time.Time `bson:"recu" json:"recu"`
	Fichier   string    `bson:"fichier" json:"fichier"`
	// Ce que l'équipe en a compris : l'airline et son projet pxsky, le cycle
	// (AAAA-MM), et pourquoi.
	Airline string `bson:"airline" json:"airline"`
	Projet  string `bson:"projet" json:"projet"`
	Cycle   string `bson:"cycle" json:"cycle"`
	Raison  string `bson:"raison,omitempty" json:"raison,omitempty"`
	// Deja : ce fichier a déjà été importé pour ce cycle.
	Deja *ImportCSP `bson:"deja,omitempty" json:"deja,omitempty"`

	Statut   string     `bson:"statut" json:"statut"`
	Mode     string     `bson:"mode,omitempty" json:"mode,omitempty"`
	Job      string     `bson:"job,omitempty" json:"job,omitempty"`
	LanceeLe *time.Time `bson:"lancee_le,omitempty" json:"lancee_le,omitempty"`
	Erreur   string     `bson:"erreur,omitempty" json:"erreur,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"creee"`
	UpdatedAt time.Time `bson:"updated_at" json:"maj"`
}

func (s *Store) propositionsCSP() *mongo.Collection { return s.db.Collection("propositions_csp") }

func (s *Store) ensurePropositionCSPIndexes(ctx context.Context) error {
	_, err := s.propositionsCSP().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "message_id", Value: 1}, {Key: "fichier", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}},
	})
	return err
}

// ProposerCSP enregistre une proposition, sauf si ce fichier de ce mail a
// déjà la sienne. Rend false si elle existait.
func (s *Store) ProposerCSP(ctx context.Context, p PropositionCSP) (PropositionCSP, bool, error) {
	now := time.Now()
	p.ID, p.CreatedAt, p.UpdatedAt = bson.NewObjectID(), now, now
	if p.Statut == "" {
		p.Statut = CSPProposee
	}
	_, err := s.propositionsCSP().InsertOne(ctx, p)
	if mongo.IsDuplicateKeyError(err) {
		return PropositionCSP{}, false, nil
	}
	return p, err == nil, err
}

// PropositionCSPExiste dit si ce fichier de ce mail a déjà sa proposition.
func (s *Store) PropositionCSPExiste(ctx context.Context, user bson.ObjectID, messageID, fichier string) bool {
	n, err := s.propositionsCSP().CountDocuments(ctx, bson.M{"user_id": user, "message_id": messageID, "fichier": fichier})
	return err == nil && n > 0
}

// PropositionsCSP : celles des trente derniers jours, les plus récentes
// d'abord.
func (s *Store) PropositionsCSP(ctx context.Context, user bson.ObjectID) ([]PropositionCSP, error) {
	cur, err := s.propositionsCSP().Find(ctx, bson.M{"user_id": user, "created_at": bson.M{"$gte": time.Now().AddDate(0, 0, -30)}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(100))
	if err != nil {
		return nil, err
	}
	out := []PropositionCSP{}
	return out, cur.All(ctx, &out)
}

// PropositionCSP rend une proposition.
func (s *Store) PropositionCSP(ctx context.Context, user, id bson.ObjectID) (PropositionCSP, error) {
	var p PropositionCSP
	err := s.propositionsCSP().FindOne(ctx, bson.M{"_id": id, "user_id": user}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return PropositionCSP{}, ErrNotFound
	}
	return p, err
}

// ModifierPropositionCSP change les champs donnés. Avec `si`, seulement si
// la proposition est dans l'un de ces états : deux clics ne lancent pas deux
// imports.
func (s *Store) ModifierPropositionCSP(ctx context.Context, user, id bson.ObjectID, set bson.M, si ...string) (PropositionCSP, error) {
	set["updated_at"] = time.Now()
	filter := bson.M{"_id": id, "user_id": user}
	if len(si) > 0 {
		filter["statut"] = bson.M{"$in": si}
	}
	var out PropositionCSP
	err := s.propositionsCSP().FindOneAndUpdate(ctx, filter, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return PropositionCSP{}, ErrNotFound
	}
	return out, err
}
