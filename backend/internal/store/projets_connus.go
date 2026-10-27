package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Les projets que connaît l'équipe, au-delà de ceux du registre.
//
// Le registre (sur le VPS) décrit ce qui tourne : VisionShow, Neurorun,
// Cerveau. Mais Mathias peut confier n'importe quel projet à Hugo — un dépôt
// du travail, une idée qui démarre — sans toucher au VPS. Il le donne depuis
// Cerveau HQ, ou en le disant à Hugo ; l'équipe l'étudie, range ce qu'elle a
// compris dans ses fiches (connaissances.go) et pose ses questions ici,
// jusqu'à le connaître assez pour y travailler.
//
// Un projet du registre peut aussi avoir sa fiche ici : pour ses questions,
// ce que Mathias en a dit, l'étude en cours. Comme les fiches, ces projets
// n'appartiennent pas à un compte : ils sont ceux du serveur.

// ProjetConnu : un projet confié à l'équipe.
type ProjetConnu struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"-"`
	// Slug : l'identifiant du projet, celui des fiches (« visionshow »).
	Slug        string `bson:"slug" json:"id"`
	Nom         string `bson:"nom" json:"nom"`
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	// Depots : « propriétaire/dépôt » pour GitHub, sinon l'adresse git
	// complète (Gitea). L'atelier les clone pour tous les agents.
	Depots []string `bson:"depots,omitempty" json:"depots,omitempty"`
	Liens  []string `bson:"liens,omitempty" json:"liens,omitempty"`
	// Notes : ce que Mathias en a dit en le donnant.
	Notes     string           `bson:"notes,omitempty" json:"notes,omitempty"`
	Questions []QuestionProjet `bson:"questions,omitempty" json:"questions"`
	// Etude : la dernière exécution de Hugo qui l'a fait étudier.
	Etude     *bson.ObjectID `bson:"etude,omitempty" json:"etude,omitempty"`
	CreatedAt time.Time      `bson:"created_at" json:"cree"`
	UpdatedAt time.Time      `bson:"updated_at" json:"maj"`
}

// QuestionProjet : ce qu'un agent demande à Mathias pour être sûr d'avoir
// compris.
type QuestionProjet struct {
	ID       string     `bson:"id" json:"id"`
	Auteur   string     `bson:"auteur" json:"auteur"`
	Question string     `bson:"question" json:"question"`
	Reponse  string     `bson:"reponse,omitempty" json:"reponse,omitempty"`
	PoseeLe  time.Time  `bson:"posee_le" json:"posee_le"`
	Repondue *time.Time `bson:"repondue,omitempty" json:"repondue,omitempty"`
}

// ErrProjetExiste : un projet porte déjà cet identifiant.
var ErrProjetExiste = errors.New("un projet porte déjà ce nom")

func (s *Store) projetsConnus() *mongo.Collection { return s.db.Collection("projets_connus") }

func (s *Store) ensureProjetConnuIndexes(ctx context.Context) error {
	_, err := s.projetsConnus().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "slug", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return err
}

// ProjetsConnus rend tous les projets, les plus anciens d'abord.
func (s *Store) ProjetsConnus(ctx context.Context) ([]ProjetConnu, error) {
	cur, err := s.projetsConnus().Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var out []ProjetConnu
	return out, cur.All(ctx, &out)
}

// ProjetConnu rend le projet d'identifiant slug.
func (s *Store) ProjetConnu(ctx context.Context, slug string) (ProjetConnu, error) {
	var p ProjetConnu
	err := s.projetsConnus().FindOne(ctx, bson.M{"slug": strings.ToLower(strings.TrimSpace(slug))}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ProjetConnu{}, ErrNotFound
	}
	return p, err
}

// CreerProjetConnu enregistre un nouveau projet.
func (s *Store) CreerProjetConnu(ctx context.Context, p ProjetConnu) (ProjetConnu, error) {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Nom = strings.TrimSpace(p.Nom)
	if p.Slug == "" || p.Nom == "" {
		return ProjetConnu{}, errors.New("un projet a besoin d'un nom")
	}
	now := time.Now()
	p.ID, p.CreatedAt, p.UpdatedAt = bson.NewObjectID(), now, now
	if p.Questions == nil {
		p.Questions = []QuestionProjet{}
	}
	if _, err := s.projetsConnus().InsertOne(ctx, p); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ProjetConnu{}, ErrProjetExiste
		}
		return ProjetConnu{}, err
	}
	return p, nil
}

// CompleterProjetConnu ajoute à un projet ce qu'on en sait de plus : dépôts
// et liens s'ajoutent, notes et description remplacent. Le projet est créé
// s'il n'existe pas encore — c'est le cas d'un projet du registre qu'on
// complète pour la première fois.
func (s *Store) CompleterProjetConnu(ctx context.Context, slug, nom string, maj ProjetConnu) (ProjetConnu, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	set := bson.M{"updated_at": time.Now()}
	if d := strings.TrimSpace(maj.Description); d != "" {
		set["description"] = d
	}
	if n := strings.TrimSpace(maj.Notes); n != "" {
		set["notes"] = n
	}
	update := bson.M{
		"$set":         set,
		"$setOnInsert": bson.M{"nom": nom, "created_at": time.Now(), "questions": []QuestionProjet{}},
	}
	add := bson.M{}
	if len(maj.Depots) > 0 {
		add["depots"] = bson.M{"$each": maj.Depots}
	}
	if len(maj.Liens) > 0 {
		add["liens"] = bson.M{"$each": maj.Liens}
	}
	if len(add) > 0 {
		update["$addToSet"] = add
	}
	var out ProjetConnu
	err := s.projetsConnus().FindOneAndUpdate(ctx, bson.M{"slug": slug}, update,
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&out)
	return out, err
}

// SupprimerProjetConnu retire un projet. Ses fiches restent : on les retire
// à part, si on veut aussi oublier ce qu'on en savait.
func (s *Store) SupprimerProjetConnu(ctx context.Context, slug string) error {
	res, err := s.projetsConnus().DeleteOne(ctx, bson.M{"slug": strings.ToLower(strings.TrimSpace(slug))})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// MarquerEtude retient l'exécution qui fait étudier le projet.
func (s *Store) MarquerEtude(ctx context.Context, slug, nom string, run bson.ObjectID) error {
	_, err := s.projetsConnus().UpdateOne(ctx, bson.M{"slug": strings.ToLower(strings.TrimSpace(slug))}, bson.M{
		"$set":         bson.M{"etude": run, "updated_at": time.Now()},
		"$setOnInsert": bson.M{"nom": nom, "created_at": time.Now(), "questions": []QuestionProjet{}},
	}, options.UpdateOne().SetUpsert(true))
	return err
}

// PoserQuestion ajoute une question d'un agent. Une question déjà posée, et
// pas encore répondue, n'est pas posée deux fois.
func (s *Store) PoserQuestion(ctx context.Context, slug, nom, auteur, question string) (QuestionProjet, bool, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	question = strings.TrimSpace(question)
	if question == "" {
		return QuestionProjet{}, false, errors.New("la question est vide")
	}
	if p, err := s.ProjetConnu(ctx, slug); err == nil {
		for _, q := range p.Questions {
			if q.Repondue == nil && strings.EqualFold(q.Question, question) {
				return q, false, nil
			}
		}
	}
	q := QuestionProjet{ID: bson.NewObjectID().Hex(), Auteur: auteur, Question: question, PoseeLe: time.Now()}
	_, err := s.projetsConnus().UpdateOne(ctx, bson.M{"slug": slug}, bson.M{
		"$push":        bson.M{"questions": q},
		"$set":         bson.M{"updated_at": time.Now()},
		"$setOnInsert": bson.M{"nom": nom, "created_at": time.Now()},
	}, options.UpdateOne().SetUpsert(true))
	return q, err == nil, err
}

// RepondreQuestion range la réponse de Mathias et rend le projet à jour.
func (s *Store) RepondreQuestion(ctx context.Context, slug, id, reponse string) (ProjetConnu, error) {
	reponse = strings.TrimSpace(reponse)
	if reponse == "" {
		return ProjetConnu{}, errors.New("la réponse est vide")
	}
	now := time.Now()
	var out ProjetConnu
	err := s.projetsConnus().FindOneAndUpdate(ctx,
		bson.M{"slug": strings.ToLower(strings.TrimSpace(slug)), "questions.id": id},
		bson.M{"$set": bson.M{"questions.$.reponse": reponse, "questions.$.repondue": now, "updated_at": now}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ProjetConnu{}, ErrNotFound
	}
	return out, err
}
