package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
)

// Ce que l'équipe technique sait de ses projets.
//
// C'est la mémoire de l'ingénieur : ce qu'il a compris en fouillant, ce qu'on
// lui a expliqué, ce qu'il a constaté en diagnostiquant. Sans elle, chaque
// question repartirait de zéro — relire le code, refaire le tour du VPS — pour
// redécouvrir ce qu'il savait la veille. Avec elle, « ça marche comment déjà
// la connexion sur Neurorun ? » se répond tout de suite, et une enquête de la
// semaine dernière n'est pas à refaire.
//
// Le savoir est rangé comme celui de Raoul : une fiche par sujet, réécrite en
// entier quand on en apprend plus. Mais il n'appartient pas à un compte — il
// décrit SES projets, ceux du serveur, et toute l'équipe le partage.
//
// Les fiches ont deux origines. Les fiches « amorce » viennent d'un dossier de
// documents posé sur le VPS : c'est le savoir de départ, rafraîchi à chaque
// démarrage. Les autres sont écrites par l'équipe en travaillant — et celles-là
// priment : une fiche d'amorce que l'ingénieur a corrigée n'est plus jamais
// écrasée par l'amorce.

// Connaissance est une fiche du savoir de l'équipe.
type Connaissance struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"id"`
	// Equipe : « informatique » aujourd'hui ; d'autres équipes viendront.
	Equipe string `bson:"equipe" json:"equipe"`
	// Projet : l'identifiant du projet (« visionshow »), ou « general » pour
	// ce qui les traverse tous (le VPS, les conventions de déploiement).
	Projet string `bson:"projet" json:"projet"`
	// Sujet : l'étiquette de la fiche, quelques mots (« Connexion »,
	// « Déploiement »). C'est elle qui fait qu'on complète au lieu d'empiler.
	Sujet   string `bson:"sujet" json:"sujet"`
	Contenu string `bson:"contenu" json:"contenu"`
	Key     string `bson:"key" json:"-"`
	// Origine : « amorce » (dossier de départ), « equipe » (écrite en
	// travaillant), « app » (saisie à la main).
	Origine string `bson:"origine" json:"origine"`
	// Auteur : l'agent qui l'a écrite en dernier.
	Auteur string `bson:"auteur,omitempty" json:"auteur,omitempty"`
	// Empreinte : celle du document d'amorce, pour ne le réécrire que s'il a
	// changé.
	Empreinte string    `bson:"empreinte,omitempty" json:"-"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

const (
	OrigineAmorce = "amorce"
	OrigineEquipe = "equipe"
	OrigineApp    = "app"
)

func (s *Store) connaissances() *mongo.Collection { return s.db.Collection("connaissances") }

func (s *Store) ensureConnaissanceIndexes(ctx context.Context) error {
	_, err := s.connaissances().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "equipe", Value: 1}, {Key: "projet", Value: 1}, {Key: "updated_at", Value: -1}}},
	})
	return err
}

func connaissanceKey(equipe, projet, sujet string) string {
	return fuzzy.Normalize(equipe) + "\x00" + fuzzy.Normalize(projet) + "\x00" + fuzzy.Normalize(sujet)
}

// Noter range une fiche, ou remplace celle qui portait déjà ce sujet.
func (s *Store) Noter(ctx context.Context, c Connaissance) (Connaissance, error) {
	c.Equipe = strings.TrimSpace(c.Equipe)
	c.Projet = strings.ToLower(strings.TrimSpace(c.Projet))
	c.Sujet = strings.TrimSpace(c.Sujet)
	c.Contenu = strings.TrimSpace(c.Contenu)
	if c.Contenu == "" || c.Sujet == "" {
		return Connaissance{}, errors.New("une fiche a besoin d'un sujet et d'un contenu")
	}
	if c.Projet == "" {
		c.Projet = "general"
	}
	if c.Origine == "" {
		c.Origine = OrigineEquipe
	}
	key := connaissanceKey(c.Equipe, c.Projet, c.Sujet)
	now := time.Now()
	set := bson.M{
		"equipe": c.Equipe, "projet": c.Projet, "sujet": c.Sujet, "contenu": c.Contenu,
		"origine": c.Origine, "auteur": c.Auteur, "updated_at": now,
	}
	if c.Empreinte != "" {
		set["empreinte"] = c.Empreinte
	}
	res := s.connaissances().FindOneAndUpdate(ctx,
		bson.M{"key": key},
		bson.M{"$set": set, "$setOnInsert": bson.M{"key": key, "created_at": now}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	)
	var out Connaissance
	if err := res.Decode(&out); err != nil {
		return Connaissance{}, err
	}
	return out, nil
}

// ConnaissanceQuery cadre une lecture du savoir.
type ConnaissanceQuery struct {
	Equipe string
	Projet string
	// Recherche : mots à retrouver dans le sujet ou le contenu.
	Recherche string
	Limit     int
}

// Connaissances rend les fiches, les plus récemment touchées d'abord.
func (s *Store) Connaissances(ctx context.Context, q ConnaissanceQuery) ([]Connaissance, error) {
	filter := bson.M{}
	if q.Equipe != "" {
		filter["equipe"] = q.Equipe
	}
	if p := strings.ToLower(strings.TrimSpace(q.Projet)); p != "" {
		filter["projet"] = p
	}
	limit := int64(q.Limit)
	if limit <= 0 {
		limit = 200
	}
	cur, err := s.connaissances().Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(500))
	if err != nil {
		return nil, err
	}
	var all []Connaissance
	if err := cur.All(ctx, &all); err != nil {
		return nil, err
	}
	words := strings.Fields(fuzzy.Normalize(q.Recherche))
	if len(words) == 0 {
		if int64(len(all)) > limit {
			all = all[:limit]
		}
		return all, nil
	}
	// Une fiche correspond si elle contient au moins un des mots ; celles qui
	// en contiennent le plus passent devant, le sujet comptant double.
	type scored struct {
		c     Connaissance
		score int
	}
	var hits []scored
	for _, c := range all {
		subject := fuzzy.Normalize(c.Sujet)
		body := fuzzy.Normalize(c.Contenu)
		score := 0
		for _, w := range words {
			if strings.Contains(subject, w) {
				score += 2
			}
			if strings.Contains(body, w) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{c, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]Connaissance, 0, len(hits))
	for _, h := range hits {
		if int64(len(out)) >= limit {
			break
		}
		out = append(out, h.c)
	}
	return out, nil
}

// OublierConnaissance retire la fiche d'un sujet.
func (s *Store) OublierConnaissance(ctx context.Context, equipe, projet, sujet string) (bool, error) {
	res, err := s.connaissances().DeleteOne(ctx, bson.M{"key": connaissanceKey(equipe, strings.ToLower(strings.TrimSpace(projet)), sujet)})
	if err != nil {
		return false, err
	}
	return res.DeletedCount > 0, nil
}

// SupprimerConnaissance retire une fiche par son identifiant — le geste de
// l'app.
func (s *Store) SupprimerConnaissance(ctx context.Context, id bson.ObjectID) error {
	res, err := s.connaissances().DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// Amorcer range une fiche d'amorce sans écraser ce que l'équipe a écrit.
//
// Trois cas : la fiche n'existe pas, on la crée ; elle existe et vient de
// l'amorce, on la remplace si le document a changé ; elle a été réécrite par
// l'équipe ou à la main, on n'y touche pas — ce qu'on a appris en travaillant
// vaut mieux que le document de départ.
func (s *Store) Amorcer(ctx context.Context, c Connaissance) (bool, error) {
	c.Origine = OrigineAmorce
	c.Empreinte = Empreinte(c.Contenu)
	key := connaissanceKey(c.Equipe, strings.ToLower(strings.TrimSpace(c.Projet)), c.Sujet)
	var current Connaissance
	err := s.connaissances().FindOne(ctx, bson.M{"key": key}).Decode(&current)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
	case err != nil:
		return false, err
	case current.Origine != OrigineAmorce:
		return false, nil
	case current.Empreinte == c.Empreinte:
		return false, nil
	}
	_, err = s.Noter(ctx, c)
	return err == nil, err
}

// Empreinte d'un texte, pour reconnaître un document inchangé.
func Empreinte(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}
