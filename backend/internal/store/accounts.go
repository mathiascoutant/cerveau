package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"golang.org/x/crypto/bcrypt"
)

// ErrBadCredentials : adresse inconnue ou mot de passe faux. Les deux cas
// renvoient la même erreur, pour ne pas dire à un inconnu quelles adresses
// ont un compte.
var ErrBadCredentials = errors.New("adresse ou mot de passe incorrect")

// ErrEmailTaken : l'adresse a déjà un compte.
var ErrEmailTaken = errors.New("un compte existe déjà avec cette adresse")

// NormalizeEmail : la même adresse tapée avec une majuscule sur un téléphone
// et sans sur l'autre doit ouvrir le même compte.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func hashPassword(password string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

// CreateAccount crée un compte vide.
func (s *Store) CreateAccount(ctx context.Context, email, password, name, timezone string) (*User, error) {
	email = NormalizeEmail(email)
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	u := User{
		Email:        email,
		PasswordHash: hash,
		Name:         name,
		Timezone:     timezone,
		CreatedAt:    now,
		LastSeen:     now,
	}
	res, err := s.users().InsertOne(ctx, u)
	if mongo.IsDuplicateKeyError(err) {
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, err
	}
	u.ID = res.InsertedID.(bson.ObjectID)
	return &u, nil
}

// SetCredentials pose (ou remplace) l'adresse et le mot de passe d'un
// utilisateur existant. C'est ce qui transforme un utilisateur « appareil »
// des premières versions en compte, sans rien perdre de ce qu'il avait
// branché : les connexions pointent sur son identifiant, qui ne change pas.
func (s *Store) SetCredentials(ctx context.Context, userID bson.ObjectID, email, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.users().UpdateByID(ctx, userID, bson.M{"$set": bson.M{
		"email":         NormalizeEmail(email),
		"password_hash": hash,
	}})
	if mongo.IsDuplicateKeyError(err) {
		return ErrEmailTaken
	}
	return err
}

// Authenticate vérifie adresse et mot de passe.
func (s *Store) Authenticate(ctx context.Context, email, password string) (*User, error) {
	var u User
	err := s.users().FindOne(ctx, bson.M{"email": NormalizeEmail(email)}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		// On paie quand même le coût d'un bcrypt : sans ça, la durée de la
		// réponse dirait si l'adresse existe.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if len(u.PasswordHash) == 0 || bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)) != nil {
		return nil, ErrBadCredentials
	}
	return &u, nil
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("raoul-dummy-password"), bcrypt.DefaultCost)

// UserByEmail retrouve un compte par son adresse.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	err := s.users().FindOne(ctx, bson.M{"email": NormalizeEmail(email)}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &u, err
}

// OpenSession émet un token pour un appareil qui vient de se connecter.
func (s *Store) OpenSession(ctx context.Context, userID bson.ObjectID, device string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, err = s.sessions().InsertOne(ctx, Session{
		UserID:    userID,
		Token:     token,
		Device:    device,
		CreatedAt: now,
		LastSeen:  now,
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// CloseSession déconnecte un appareil, et lui seul.
func (s *Store) CloseSession(ctx context.Context, token string) error {
	_, err := s.sessions().DeleteOne(ctx, bson.M{"token": token})
	return err
}

// UserBySession résout un token de session. Il retombe sur le token hérité
// porté par l'utilisateur, pour que l'app d'avant les comptes reste connectée
// jusqu'à sa mise à jour.
func (s *Store) UserBySession(ctx context.Context, token string) (*User, error) {
	var sess Session
	err := s.sessions().FindOneAndUpdate(ctx,
		bson.M{"token": token},
		bson.M{"$set": bson.M{"last_seen": time.Now()}},
	).Decode(&sess)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return s.UserByToken(ctx, token)
	}
	if err != nil {
		return nil, err
	}
	var u User
	err = s.users().FindOne(ctx, bson.M{"_id": sess.UserID}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &u, err
}

// AccountCandidate : un utilisateur existant, vu par la commande account quand
// elle cherche à quel compte rattacher une adresse.
type AccountCandidate struct {
	User        User
	Connections []string
}

// AccountCandidates liste les utilisateurs avec ce qu'ils ont branché, les
// plus fournis d'abord.
func (s *Store) AccountCandidates(ctx context.Context) ([]AccountCandidate, error) {
	cur, err := s.users().Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	var users []User
	if err := cur.All(ctx, &users); err != nil {
		return nil, err
	}
	out := make([]AccountCandidate, 0, len(users))
	for _, u := range users {
		conns, err := s.Connections(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		c := AccountCandidate{User: u}
		for _, conn := range conns {
			c.Connections = append(c.Connections, conn.Provider+":"+conn.Status)
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Connections) != len(out[j].Connections) {
			return len(out[i].Connections) > len(out[j].Connections)
		}
		return out[i].User.LastSeen.After(out[j].User.LastSeen)
	})
	return out, nil
}

// UserByDevice retrouve un utilisateur des premières versions par son
// identifiant d'appareil.
func (s *Store) UserByDevice(ctx context.Context, deviceID string) (*User, error) {
	var u User
	err := s.users().FindOne(ctx, bson.M{"device_id": deviceID}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &u, err
}
