package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
)

// La mémoire des échanges : ce que Raoul a déjà lu, et ce qu'il en a compris.
//
// Sans elle, « c'était quoi le dernier mail de Cyril ? », posé dix minutes
// après l'avoir entendu, rouvrait la boîte mail, relisait le fil et refaisait
// tout le travail de compréhension — pour redire la même chose, en plus lent.
// Un collègue qui vient de lire un mail ne le relit pas quand on lui en
// reparle : il s'en souvient, et vérifie seulement si quelque chose de neuf est
// arrivé depuis.
//
// Un échange, c'est une conversation : un fil de mails, un canal ou un fil
// Slack, une conversation WhatsApp. Il garde le dernier message vu, à qui il
// s'adressait, et — quand Raoul a pris le temps de comprendre — ce qu'il en a
// retenu : de quoi il s'agit, où on en est, ce qu'on attend de lui.

// Echange est une conversation que Raoul a lue.
type Echange struct {
	ID     bson.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID bson.ObjectID `bson:"user_id" json:"-"`
	// Source : « mail », « slack » ou « whatsapp ». Espace : « pro » ou
	// « perso » — la boîte d'où vient un fil de mails.
	Source string `bson:"source" json:"source"`
	Espace string `bson:"espace,omitempty" json:"espace,omitempty"`
	// Cle : l'identité de la conversation pour sa source — la racine d'un fil
	// de mails (le premier Message-ID qu'il cite), l'identifiant d'un canal
	// Slack, celui d'une conversation WhatsApp.
	Cle   string `bson:"cle" json:"-"`
	Sujet string `bson:"sujet" json:"sujet"`
	// Participants : qui y a écrit, par leur nom.
	Participants []string `bson:"participants,omitempty" json:"participants,omitempty"`
	// Le dernier message vu.
	DernierDe      string    `bson:"dernier_de,omitempty" json:"dernier_de,omitempty"`
	DernierAdresse string    `bson:"dernier_adresse,omitempty" json:"-"`
	DernierQuand   time.Time `bson:"dernier_quand,omitempty" json:"dernier_quand,omitempty"`
	DernierRef     string    `bson:"dernier_ref,omitempty" json:"-"`
	DernierExtrait string    `bson:"dernier_extrait,omitempty" json:"dernier_extrait,omitempty"`
	DernierDeToi   bool      `bson:"dernier_de_toi,omitempty" json:"dernier_de_toi,omitempty"`
	// PourToi : sa place dans le dernier message (« tu es seulement en
	// copie »…), telle que le serveur l'a tranchée.
	PourToi string `bson:"pour_toi,omitempty" json:"pour_toi,omitempty"`
	// Resume : ce que Raoul en a compris — l'analyse d'un débrief, ou ce qu'il
	// en a dit quand on le lui a demandé. Vide tant qu'il n'a fait que lire.
	Resume string `bson:"resume,omitempty" json:"resume,omitempty"`
	// ResumeLe : quand il l'a compris. Un résumé antérieur au dernier message
	// ne dit rien de ce message-là.
	ResumeLe time.Time `bson:"resume_le,omitempty" json:"resume_le,omitempty"`
	// VuLe : la dernière fois qu'il l'a lu.
	VuLe      time.Time `bson:"vu_le" json:"vu_le"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

func (s *Store) echanges() *mongo.Collection { return s.db.Collection("echanges") }

func (s *Store) ensureEchangeIndexes(ctx context.Context) error {
	_, err := s.echanges().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "source", Value: 1}, {Key: "cle", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "vu_le", Value: -1}}},
		// Un échange vieux de deux mois n'aide plus personne : il s'efface.
		{Keys: bson.D{{Key: "vu_le", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(int32(echangeTTL / time.Second))},
	})
	return err
}

// echangeTTL : au-delà, un échange relu ne vaut plus mieux qu'un échange neuf.
const echangeTTL = 60 * 24 * time.Hour

// NoterEchange enregistre une lecture. Les champs vides ne remplacent rien :
// relire un fil sans le débriefer ne doit pas effacer ce qu'on en avait
// compris, et un message plus ancien que le dernier vu ne le remplace pas.
func (s *Store) NoterEchange(ctx context.Context, e Echange) error {
	e.Cle = strings.TrimSpace(e.Cle)
	if e.Cle == "" || e.Source == "" {
		return nil
	}
	now := time.Now()
	set := bson.M{"vu_le": now}
	if e.Sujet != "" {
		set["sujet"] = e.Sujet
	}
	if e.Espace != "" {
		set["espace"] = e.Espace
	}
	if e.Resume != "" {
		set["resume"] = truncateText(e.Resume, 4000)
		set["resume_le"] = now
	}
	filter := bson.M{"user_id": e.UserID, "source": e.Source, "cle": e.Cle}
	update := bson.M{"$set": set, "$setOnInsert": bson.M{"created_at": now}}
	if len(e.Participants) > 0 {
		update["$addToSet"] = bson.M{"participants": bson.M{"$each": e.Participants}}
	}
	if _, err := s.echanges().UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
		return err
	}
	if e.DernierQuand.IsZero() && e.DernierExtrait == "" {
		return nil
	}
	// Le dernier message ne recule jamais : on ne l'écrit que s'il est plus
	// récent que celui qu'on avait.
	last := bson.M{
		"dernier_de":      e.DernierDe,
		"dernier_adresse": strings.ToLower(e.DernierAdresse),
		"dernier_quand":   e.DernierQuand,
		"dernier_ref":     e.DernierRef,
		"dernier_extrait": truncateText(e.DernierExtrait, 900),
		"dernier_de_toi":  e.DernierDeToi,
		"pour_toi":        e.PourToi,
	}
	_, err := s.echanges().UpdateOne(ctx,
		bson.M{"user_id": e.UserID, "source": e.Source, "cle": e.Cle, "$or": bson.A{
			bson.M{"dernier_quand": bson.M{"$exists": false}},
			bson.M{"dernier_quand": bson.M{"$lte": e.DernierQuand}},
		}},
		bson.M{"$set": last},
	)
	return err
}

// NoterCompris pose ce que Raoul a compris d'échanges déjà notés.
func (s *Store) NoterCompris(ctx context.Context, userID bson.ObjectID, source, cle, resume string) error {
	resume = strings.TrimSpace(resume)
	if resume == "" || cle == "" {
		return nil
	}
	_, err := s.echanges().UpdateOne(ctx,
		bson.M{"user_id": userID, "source": source, "cle": cle},
		bson.M{"$set": bson.M{"resume": truncateText(resume, 4000), "resume_le": time.Now()}},
	)
	return err
}

// EchangesRecents rend les derniers échanges lus, du plus récent au plus
// ancien.
func (s *Store) EchangesRecents(ctx context.Context, userID bson.ObjectID, limit int64) ([]Echange, error) {
	cur, err := s.echanges().Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetSort(bson.D{{Key: "vu_le", Value: -1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	var out []Echange
	return out, cur.All(ctx, &out)
}

// ChercherEchanges retrouve des échanges par personne, sujet ou canal — à
// l'orthographe près, comme le reste de ce que Raoul cherche à la voix.
func (s *Store) ChercherEchanges(ctx context.Context, userID bson.ObjectID, query string, limit int) ([]Echange, error) {
	all, err := s.EchangesRecents(ctx, userID, 300)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(fuzzy.Normalize(query))
	if len(words) == 0 {
		if len(all) > limit {
			all = all[:limit]
		}
		return all, nil
	}
	type scored struct {
		e     Echange
		score int
	}
	var hits []scored
	for _, e := range all {
		people := fuzzy.Normalize(strings.Join(append([]string{e.DernierDe, e.DernierAdresse}, e.Participants...), " "))
		subject := fuzzy.Normalize(e.Sujet)
		body := fuzzy.Normalize(e.Resume + " " + e.DernierExtrait)
		score := 0
		for _, w := range words {
			switch {
			case strings.Contains(people, w):
				score += 3
			case strings.Contains(subject, w):
				score += 2
			case strings.Contains(body, w):
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{e, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]Echange, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		out = append(out, h.e)
	}
	return out, nil
}

func truncateText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
