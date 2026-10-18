package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
)

// Ce que Raoul retient, et la seule règle qui le tient : une information par
// sujet.
//
// Sans elle la mémoire se remplit de doublons — « Cyril bosse chez Orange »,
// puis « Cyril est chez Orange, on parle des boxes », puis « Cyril, contact
// technique Orange » — et une mémoire qui dit trois fois la même chose en trois
// formulations ne se corrige plus : on ne sait pas laquelle fait foi. Réécrire
// la fiche Cyril remplace donc la précédente, exactement comme on met à jour ce
// qu'on sait de quelqu'un plutôt que d'empiler.

// Nombre de fiches au-delà duquel on cesse de descendre la mémoire au modèle.
//
// La borne n'est pas là pour économiser : elle est là parce qu'une mémoire qui
// dépasse la soixantaine de lignes n'est plus lue. Noyée dans le prompt, elle
// dilue les consignes de ton qui font l'essentiel de la qualité des réponses,
// et le modèle finit par traiter chaque fiche comme du bruit. Au-delà, on garde
// les plus récemment touchées : ce qu'on vient de lui apprendre, ou de
// corriger, est ce qui sert.
const factLimit = 60

func (s *Store) facts() *mongo.Collection { return s.db.Collection("facts") }

// factKey range l'information sous son sujet, à l'orthographe près.
//
// C'est fuzzy.Normalize qui décide : « Cyril », « cyril » et « CYRIL » sont la
// même personne, et la dictée ne garantit ni la casse ni les accents. Un fait
// sans sujet se dédoublonne sur son propre texte — faute de mieux, mais ça
// suffit à empêcher qu'une phrase dite deux fois soit retenue deux fois.
func factKey(kind, subject, content string) string {
	label := strings.TrimSpace(subject)
	if label == "" {
		label = content
	}
	return kind + "\x00" + fuzzy.Normalize(label)
}

// SaveFact retient une information, ou remplace celle qui portait déjà ce sujet.
func (s *Store) SaveFact(ctx context.Context, f Fact) (Fact, error) {
	f.Kind = strings.TrimSpace(f.Kind)
	f.Subject = strings.TrimSpace(f.Subject)
	f.Content = strings.TrimSpace(f.Content)
	if f.Content == "" {
		return Fact{}, errors.New("rien à retenir")
	}
	if f.Kind == "" {
		f.Kind = FactOther
	}
	f.Key = factKey(f.Kind, f.Subject, f.Content)

	now := time.Now()
	set := bson.M{
		"kind":       f.Kind,
		"subject":    f.Subject,
		"content":    f.Content,
		"origin":     f.Origin,
		"updated_at": now,
	}
	// Le rôle et l'importance ne se remplacent que s'ils sont donnés : une
	// fiche réécrite pour y ajouter une phrase ne doit pas perdre ce qu'on
	// savait déjà de la place de la personne.
	if role := strings.TrimSpace(f.Role); role != "" {
		set["role"] = role
	}
	if level := NormalizeImportance(f.Importance); level != "" {
		set["importance"] = level
	}
	update := bson.M{"$set": set, "$setOnInsert": bson.M{"created_at": now}}
	// Les identités s'ajoutent, elles ne se remplacent jamais : apprendre
	// l'adresse pro de Cyril n'efface pas son identifiant Slack.
	add := bson.M{}
	if addrs := cleanAddresses(f.Adresses); len(addrs) > 0 {
		add["adresses"] = bson.M{"$each": addrs}
	}
	if ids := cleanIDs(f.SlackIDs); len(ids) > 0 {
		add["slack_ids"] = bson.M{"$each": ids}
	}
	if len(add) > 0 {
		update["$addToSet"] = add
	}
	res := s.facts().FindOneAndUpdate(ctx,
		bson.M{"user_id": f.UserID, "key": f.Key},
		update,
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	)
	var out Fact
	if err := res.Decode(&out); err != nil {
		return Fact{}, err
	}
	return out, nil
}

// FactBySubject rend la fiche d'un sujet, à l'orthographe près.
func (s *Store) FactBySubject(ctx context.Context, userID bson.ObjectID, kind, subject string) (Fact, error) {
	var out Fact
	err := s.facts().FindOne(ctx, bson.M{"user_id": userID, "key": factKey(kind, subject, "")}).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Fact{}, ErrNotFound
	}
	return out, err
}

// FactByID rend une fiche désignée par son identifiant.
func (s *Store) FactByID(ctx context.Context, userID, id bson.ObjectID) (Fact, error) {
	var out Fact
	err := s.facts().FindOne(ctx, bson.M{"user_id": userID, "_id": id}).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Fact{}, ErrNotFound
	}
	return out, err
}

// People rend toutes les personnes de son entourage, sans la borne de la
// consigne : l'annuaire sert à reconnaître l'auteur de chaque message, et un
// collègue dont la fiche n'a pas été touchée depuis trois mois reste son
// collègue.
func (s *Store) People(ctx context.Context, userID bson.ObjectID) ([]Fact, error) {
	cur, err := s.facts().Find(ctx,
		bson.M{"user_id": userID, "kind": FactPerson},
		options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(500),
	)
	if err != nil {
		return nil, err
	}
	var out []Fact
	return out, cur.All(ctx, &out)
}

// UpdatePerson règle la place d'une personne depuis l'app : son rôle, son
// importance, ses adresses. Les adresses données remplacent la liste — c'est
// l'écran où l'on corrige une adresse mal rattachée.
func (s *Store) UpdatePerson(ctx context.Context, userID, id bson.ObjectID, role, importance string, adresses []string) (Fact, error) {
	set := bson.M{"role": strings.TrimSpace(role), "updated_at": time.Now()}
	if level := NormalizeImportance(importance); level != "" || strings.TrimSpace(importance) == "" {
		set["importance"] = level
	}
	if adresses != nil {
		set["adresses"] = cleanAddresses(adresses)
	}
	res := s.facts().FindOneAndUpdate(ctx,
		bson.M{"user_id": userID, "_id": id, "kind": FactPerson},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var out Fact
	if err := res.Decode(&out); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Fact{}, ErrNotFound
		}
		return Fact{}, err
	}
	return out, nil
}

func cleanAddresses(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range in {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || !strings.Contains(a, "@") || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

func cleanIDs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range in {
		id = strings.ToUpper(strings.TrimSpace(id))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// Facts rend ce qui est retenu, rangé pour être lu : les personnes d'abord,
// puis les projets, les préférences, et les faits isolés.
//
// L'ordre n'est pas cosmétique. C'est celui dans lequel la mémoire est
// descendue au modèle, et une liste qui commence par les gens l'oriente vers
// ce qui sert le plus souvent — reconnaître un expéditeur. À catégorie égale,
// la fiche touchée le plus récemment passe devant.
func (s *Store) Facts(ctx context.Context, userID bson.ObjectID) ([]Fact, error) {
	cur, err := s.facts().Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(factLimit),
	)
	if err != nil {
		return nil, err
	}
	var out []Fact
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	rank := map[string]int{FactPerson: 0, FactProject: 1, FactPreference: 2, FactOther: 3}
	sort.SliceStable(out, func(i, j int) bool {
		ri, ok := rank[out[i].Kind]
		if !ok {
			ri = len(rank)
		}
		rj, ok := rank[out[j].Kind]
		if !ok {
			rj = len(rank)
		}
		return ri < rj
	})
	return out, nil
}

// ForgetFacts oublie tout ce qui touche à un sujet.
//
// La recherche porte sur le sujet ET le contenu : « oublie Cyril » doit
// emporter la fiche Cyril, mais aussi le projet dont le texte dit qu'il est
// mené avec lui. Oublier à moitié quelqu'un est pire que ne pas l'oublier — il
// ressort trois jours plus tard par une phrase qu'on croyait effacée.
func (s *Store) ForgetFacts(ctx context.Context, userID bson.ObjectID, query string) ([]Fact, error) {
	needle := fuzzy.Normalize(strings.TrimSpace(query))
	if needle == "" {
		return nil, errors.New("aucun sujet à oublier")
	}
	all, err := s.Facts(ctx, userID)
	if err != nil {
		return nil, err
	}
	var gone []Fact
	ids := make([]bson.ObjectID, 0, len(all))
	for _, f := range all {
		if strings.Contains(fuzzy.Normalize(f.Subject+" "+f.Content), needle) {
			gone = append(gone, f)
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := s.facts().DeleteMany(ctx, bson.M{"user_id": userID, "_id": bson.M{"$in": ids}}); err != nil {
		return nil, err
	}
	return gone, nil
}

// DeleteFact retire une fiche désignée par son identifiant — le geste de
// l'app, où on voit la ligne qu'on supprime.
func (s *Store) DeleteFact(ctx context.Context, userID, id bson.ObjectID) error {
	res, err := s.facts().DeleteOne(ctx, bson.M{"user_id": userID, "_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateFact réécrit le contenu d'une fiche désignée par son identifiant.
//
// SaveFact ne suffit pas pour ça. Elle range par sujet, et une fiche sans sujet
// se range sous son propre texte : corriger ce texte reviendrait à en créer une
// deuxième en laissant la première derrière. C'est invisible à la voix, où le
// sujet est presque toujours donné, mais c'est le cas ordinaire de l'écran —
// on y corrige au doigt la ligne qu'on a sous les yeux, et elle doit rester
// cette ligne-là.
func (s *Store) UpdateFact(ctx context.Context, userID, id bson.ObjectID, subject, content string) (Fact, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Fact{}, errors.New("rien à retenir")
	}
	subject = strings.TrimSpace(subject)

	var current Fact
	if err := s.facts().FindOne(ctx, bson.M{"user_id": userID, "_id": id}).Decode(&current); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Fact{}, ErrNotFound
		}
		return Fact{}, err
	}

	res := s.facts().FindOneAndUpdate(ctx,
		bson.M{"user_id": userID, "_id": id},
		bson.M{"$set": bson.M{
			"subject":    subject,
			"content":    content,
			"key":        factKey(current.Kind, subject, content),
			"updated_at": time.Now(),
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)
	var out Fact
	if err := res.Decode(&out); err != nil {
		return Fact{}, err
	}
	return out, nil
}
