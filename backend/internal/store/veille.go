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
	Messages []string `bson:"messages,omitempty" json:"messages,omitempty"`
	Run      string   `bson:"run,omitempty" json:"run,omitempty"`
	// Une notification est un SUJET : une même discussion ne fait qu'un bloc,
	// mis à jour à chaque nouveau message au lieu d'être signalée à nouveau.
	// Fil : la clé de la discussion (« slack:apex », « mail:devis acme »),
	// Lieu : où elle se tient, tel qu'on le dit (« #apex », « APEX - WestIQ »).
	Fil  string `bson:"fil,omitempty" json:"-"`
	Lieu string `bson:"lieu,omitempty" json:"lieu,omitempty"`
	// AFaire : ce qu'on attend de Mathias, vide si rien.
	AFaire string `bson:"a_faire,omitempty" json:"a_faire,omitempty"`
	// Echanges : qui a dit quoi, du plus ancien au plus récent.
	Echanges  []PriseDeParole `bson:"echanges,omitempty" json:"echanges,omitempty"`
	CreatedAt time.Time       `bson:"created_at" json:"creee"`
	MisAJour  time.Time       `bson:"maj,omitempty" json:"maj,omitzero"`
	LueAt     *time.Time      `bson:"lue_at,omitempty" json:"lue,omitempty"`
	// PointAt : le dernier point fait sur le sujet, y compris en silence
	// (ce que Mathias y a écrit lui-même). Le suivi repart de là.
	PointAt time.Time `bson:"point_at,omitempty" json:"-"`
}

// DernierPoint : depuis quand le sujet est à jour.
func (n Notification) DernierPoint() time.Time {
	if n.PointAt.After(n.MisAJour) {
		return n.PointAt
	}
	return n.MisAJour
}

// PriseDeParole : qui a dit quoi, dans un sujet de veille.
type PriseDeParole struct {
	Qui   string    `bson:"qui" json:"qui"`
	AQui  string    `bson:"a_qui,omitempty" json:"a_qui,omitempty"`
	Quoi  string    `bson:"quoi" json:"quoi"`
	Quand time.Time `bson:"quand" json:"quand"`
}

// maxEchanges : un sujet garde ses derniers échanges, pas un historique sans
// fin — au-delà, on lit la discussion elle-même.
const maxEchanges = 12

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
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "maj", Value: -1}}},
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
	if n.MisAJour.IsZero() {
		n.MisAJour = n.CreatedAt
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
		options.Find().SetSort(bson.D{{Key: "maj", Value: -1}, {Key: "created_at", Value: -1}}).SetLimit(notificationsLues))
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

// SujetsOuverts : les sujets qu'un agent a signalés depuis `depuis`, du plus
// récent au plus ancien — ceux auxquels un nouveau message peut se rattacher.
func (s *Store) SujetsOuverts(ctx context.Context, userID bson.ObjectID, agent string, depuis time.Time) ([]Notification, error) {
	cur, err := s.notifications().Find(ctx, bson.M{
		"user_id": userID, "agent": agent,
		"$or": bson.A{bson.M{"maj": bson.M{"$gte": depuis}}, bson.M{"created_at": bson.M{"$gte": depuis}}},
	}, options.Find().SetSort(bson.D{{Key: "maj", Value: -1}, {Key: "created_at", Value: -1}}).SetLimit(15))
	if err != nil {
		return nil, err
	}
	var out []Notification
	err = cur.All(ctx, &out)
	return out, err
}

// MettreAJourSujet fait avancer un sujet : nouveau titre et résumé, échanges
// ajoutés à la suite, et il redevient non lu — il y a du neuf.
//
// En silence (discret), quand seul Mathias y a écrit : le sujet est mis à
// jour (« c'est réglé ») sans redevenir non lu ni remonter en tête — ce
// qu'il a écrit lui-même n'a rien à lui apprendre.
func (s *Store) MettreAJourSujet(ctx context.Context, id bson.ObjectID, n Notification, discret bool) error {
	now := time.Now()
	set := bson.M{"titre": n.Titre, "resume": n.Resume, "importance": n.Importance, "a_faire": n.AFaire, "point_at": now}
	if !discret {
		set["maj"] = now
	}
	if n.Lieu != "" {
		set["lieu"] = n.Lieu
	}
	if n.Run != "" {
		set["run"] = n.Run
	}
	update := bson.M{"$set": set}
	if !discret {
		update["$unset"] = bson.M{"lue_at": ""}
	}
	push := bson.M{}
	if len(n.Echanges) > 0 {
		push["echanges"] = bson.M{"$each": n.Echanges, "$slice": -maxEchanges}
	}
	if len(n.Messages) > 0 {
		push["messages"] = bson.M{"$each": n.Messages, "$slice": -maxEchanges}
	}
	if len(push) > 0 {
		update["$push"] = push
	}
	_, err := s.notifications().UpdateByID(ctx, id, update)
	return err
}
