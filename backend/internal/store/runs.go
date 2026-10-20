package store

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Le travail des agents : chaque fois qu'un membre de l'équipe travaille
// vraiment — une question en conversation, une enquête confiée par la
// responsable, une tâche lancée depuis Cerveau HQ —, une exécution est
// enregistrée ici, avec son état.
//
// C'est la seule source de l'état qu'affiche le bâtiment : un personnage n'est
// « au travail » que si une exécution l'est. Rien n'est simulé côté écran.

// États d'une exécution.
const (
	RunEnAttente = "en_attente" // acceptée, attend une place libre
	RunEnCours   = "en_cours"
	RunTermine   = "termine"
	RunErreur    = "erreur"
	RunAnnule    = "annule"
)

// Origines d'une exécution.
const (
	RunSourceConversation = "conversation"
	RunSourceDelegation   = "delegation"
	RunSourceHQ           = "hq"
)

// RunStep : une étape de travail, un outil appelé.
type RunStep struct {
	Outil   string `bson:"outil" json:"outil"`
	Libelle string `bson:"libelle" json:"libelle"`
	Ms      int64  `bson:"ms,omitempty" json:"ms,omitempty"`
	Erreur  string `bson:"erreur,omitempty" json:"erreur,omitempty"`
}

// AgentRun : une exécution d'un agent.
type AgentRun struct {
	ID      bson.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID  bson.ObjectID `bson:"user_id" json:"-"`
	Agent   string        `bson:"agent" json:"agent"`
	Source  string        `bson:"source" json:"source"`
	Demande string        `bson:"demande" json:"demande"`
	Statut  string        `bson:"statut" json:"statut"`
	Reponse string        `bson:"reponse,omitempty" json:"reponse,omitempty"`
	Erreur  string        `bson:"erreur,omitempty" json:"erreur,omitempty"`
	Etapes  []RunStep     `bson:"etapes,omitempty" json:"etapes,omitempty"`
	// Affectation : pourquoi cet agent, quand la tâche a été confiée sans
	// nommer personne.
	Affectation string `bson:"affectation,omitempty" json:"affectation,omitempty"`
	// Parent : l'exécution qui a délégué celle-ci (la responsable qui confie
	// une enquête à l'ingénieur).
	Parent     *bson.ObjectID `bson:"parent,omitempty" json:"parent,omitempty"`
	CreatedAt  time.Time      `bson:"created_at" json:"created_at"`
	StartedAt  *time.Time     `bson:"started_at,omitempty" json:"started_at,omitempty"`
	FinishedAt *time.Time     `bson:"finished_at,omitempty" json:"finished_at,omitempty"`
}

// runTTL : au-delà, une exécution n'apprend plus rien sur l'état d'un agent.
const runTTL = 90 * 24 * time.Hour

func (s *Store) agentRuns() *mongo.Collection { return s.db.Collection("agent_runs") }

func (s *Store) ensureRunIndexes(ctx context.Context) error {
	_, err := s.agentRuns().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "agent", Value: 1}, {Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "statut", Value: 1}}},
		{Keys: bson.D{{Key: "created_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(int32(runTTL / time.Second))},
	})
	return err
}

// InsertRun enregistre une exécution et rend son identifiant.
func (s *Store) InsertRun(ctx context.Context, r AgentRun) (AgentRun, error) {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	res, err := s.agentRuns().InsertOne(ctx, r)
	if err != nil {
		return AgentRun{}, err
	}
	r.ID = res.InsertedID.(bson.ObjectID)
	return r, nil
}

// StartRun marque une exécution comme commencée.
func (s *Store) StartRun(ctx context.Context, id bson.ObjectID) error {
	now := time.Now()
	_, err := s.agentRuns().UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"statut": RunEnCours, "started_at": now}})
	return err
}

// FinishRun clôt une exécution avec son résultat.
func (s *Store) FinishRun(ctx context.Context, id bson.ObjectID, statut, reponse, erreur string, etapes []RunStep) error {
	now := time.Now()
	set := bson.M{"statut": statut, "finished_at": now}
	if reponse != "" {
		set["reponse"] = reponse
	}
	if erreur != "" {
		set["erreur"] = erreur
	}
	if len(etapes) > 0 {
		set["etapes"] = etapes
	}
	_, err := s.agentRuns().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	return err
}

// RunByID rend une exécution de l'utilisateur.
func (s *Store) RunByID(ctx context.Context, userID, id bson.ObjectID) (AgentRun, error) {
	var out AgentRun
	err := s.agentRuns().FindOne(ctx, bson.M{"_id": id, "user_id": userID}).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return AgentRun{}, ErrNotFound
	}
	return out, err
}

// RunsOfAgent rend les dernières exécutions d'un agent, de la plus récente à
// la plus ancienne.
func (s *Store) RunsOfAgent(ctx context.Context, userID bson.ObjectID, agent string, limit int64) ([]AgentRun, error) {
	cur, err := s.agentRuns().Find(ctx,
		bson.M{"user_id": userID, "agent": agent},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []AgentRun
	return out, cur.All(ctx, &out)
}

// LatestRuns rend la dernière exécution de chaque agent de l'utilisateur, en
// une requête — quel que soit le nombre d'agents.
func (s *Store) LatestRuns(ctx context.Context, userID bson.ObjectID) (map[string]AgentRun, error) {
	cur, err := s.agentRuns().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"user_id": userID}}},
		{{Key: "$sort", Value: bson.D{{Key: "agent", Value: 1}, {Key: "created_at", Value: -1}}}},
		{{Key: "$group", Value: bson.M{"_id": "$agent", "run": bson.M{"$first": "$$ROOT"}}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Run AgentRun `bson:"run"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]AgentRun, len(rows))
	for _, r := range rows {
		out[r.Run.Agent] = r.Run
	}
	return out, nil
}

// CountRunsSince compte les exécutions d'une origine lancées depuis un
// instant — c'est le budget des tâches lancées depuis le bâtiment.
func (s *Store) CountRunsSince(ctx context.Context, userID bson.ObjectID, source string, since time.Time) (int64, error) {
	return s.agentRuns().CountDocuments(ctx, bson.M{
		"user_id": userID, "source": source, "created_at": bson.M{"$gte": since},
	})
}

// AbandonStaleRuns clôt les exécutions restées ouvertes : au démarrage du
// serveur, plus rien ne les fait avancer. Sans ça, un personnage resterait
// « au travail » pour toujours après un redémarrage.
func (s *Store) AbandonStaleRuns(ctx context.Context) (int64, error) {
	now := time.Now()
	res, err := s.agentRuns().UpdateMany(ctx,
		bson.M{"statut": bson.M{"$in": bson.A{RunEnAttente, RunEnCours}}},
		bson.M{"$set": bson.M{"statut": RunErreur, "erreur": "interrompue par un redémarrage du serveur", "finished_at": now}},
	)
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// InteractionsOfAgent rend les derniers échanges avec un agent.
func (s *Store) InteractionsOfAgent(ctx context.Context, userID bson.ObjectID, agent string, limit int64) ([]Interaction, error) {
	cur, err := s.interactions().Find(ctx,
		bson.M{"user_id": userID, "agent": agent},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []Interaction
	return out, cur.All(ctx, &out)
}

// DropForTests supprime la base : réservé aux tests d'intégration, qui
// travaillent chacun dans une base jetable.
func (s *Store) DropForTests(ctx context.Context) error {
	if err := s.db.Drop(ctx); err != nil {
		return err
	}
	return s.Close(ctx)
}

// CountChildren : combien de tâches une exécution a confiées à d'autres.
func (s *Store) CountChildren(ctx context.Context, parent bson.ObjectID) (int64, error) {
	return s.agentRuns().CountDocuments(ctx, bson.M{"parent": parent})
}
