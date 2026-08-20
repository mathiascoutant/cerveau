package store

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Providers supportés.
const (
	ProviderGandi    = "gandi"
	ProviderSlack    = "slack"
	ProviderWhatsApp = "whatsapp"
	ProviderCalendar = "calendar"
)

// User : pas de login, pas de mot de passe. L'identité c'est l'appareil.
type User struct {
	ID        bson.ObjectID `bson:"_id,omitempty" json:"id"`
	DeviceID  string        `bson:"device_id" json:"device_id"`
	Token     string        `bson:"token" json:"-"`
	Name      string        `bson:"name,omitempty" json:"name,omitempty"`
	Timezone  string        `bson:"timezone" json:"timezone"`
	CreatedAt time.Time     `bson:"created_at" json:"created_at"`
	LastSeen  time.Time     `bson:"last_seen" json:"last_seen"`
}

// Connection : un compte externe branché par l'utilisateur. Secret est chiffré.
type Connection struct {
	ID        bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID    bson.ObjectID `bson:"user_id" json:"-"`
	Provider  string        `bson:"provider" json:"provider"`
	Status    string        `bson:"status" json:"status"` // connected | error | disconnected
	Label     string        `bson:"label,omitempty" json:"label,omitempty"`
	LastError string        `bson:"last_error,omitempty" json:"last_error,omitempty"`
	Secret    []byte        `bson:"secret" json:"-"`
	UpdatedAt time.Time     `bson:"updated_at" json:"updated_at"`
}

// GandiCredentials : IMAP Gandi (mail.gandi.net).
type GandiCredentials struct {
	Email    string `json:"email"`
	Password string `json:"password"` // mot de passe d'application Gandi
	Host     string `json:"host"`     // défaut mail.gandi.net:993
}

type SlackCredentials struct {
	UserToken string `json:"user_token"` // xoxp-...
}

type WhatsAppCredentials struct {
	PhoneNumberID string `json:"phone_number_id"`
	AccessToken   string `json:"access_token"`
	WABAID        string `json:"waba_id"`
}

// CalendarEvent : miroir des événements du calendrier du téléphone, poussé par
// l'app. C'est ce miroir que l'assistant interroge pour détecter les conflits.
type CalendarEvent struct {
	ID         bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID     bson.ObjectID `bson:"user_id" json:"-"`
	ExternalID string        `bson:"external_id" json:"external_id"`
	Calendar   string        `bson:"calendar,omitempty" json:"calendar,omitempty"`
	Title      string        `bson:"title" json:"title"`
	Location   string        `bson:"location,omitempty" json:"location,omitempty"`
	Start      time.Time     `bson:"start" json:"start"`
	End        time.Time     `bson:"end" json:"end"`
	AllDay     bool          `bson:"all_day" json:"all_day"`
	UpdatedAt  time.Time     `bson:"updated_at" json:"-"`
}

// WhatsAppMessage : messages entrants collectés via le webhook Meta. L'API
// WhatsApp ne donne pas d'historique ni de statut "non lu" — on le tient nous-mêmes.
type WhatsAppMessage struct {
	ID        bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID    bson.ObjectID `bson:"user_id" json:"-"`
	MessageID string        `bson:"message_id" json:"message_id"`
	From      string        `bson:"from" json:"from"`
	FromName  string        `bson:"from_name,omitempty" json:"from_name,omitempty"`
	Body      string        `bson:"body" json:"body"`
	Type      string        `bson:"type" json:"type"`
	Timestamp time.Time     `bson:"timestamp" json:"timestamp"`
	Read      bool          `bson:"read" json:"read"`
}

// Interaction : historique des échanges avec Raoul (utile pour le debug et pour
// afficher un fil de conversation dans l'app).
type Interaction struct {
	ID         bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID     bson.ObjectID `bson:"user_id" json:"-"`
	Transcript string        `bson:"transcript" json:"transcript"`
	Reply      string        `bson:"reply" json:"reply"`
	Actions    []Action      `bson:"actions,omitempty" json:"actions,omitempty"`
	CreatedAt  time.Time     `bson:"created_at" json:"created_at"`
}

// Action : instruction renvoyée à l'app mobile (ex. écrire dans le calendrier du
// téléphone, que seule l'app peut faire).
type Action struct {
	Type    string         `bson:"type" json:"type"`
	Payload map[string]any `bson:"payload" json:"payload"`
}
