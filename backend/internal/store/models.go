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

// WhatsAppCredentials : l'appareil lié, tel que whatsmeow le nomme
// (« 33612345678.42:3@s.whatsapp.net »).
//
// Ce n'est pas un secret : les clés de la session vivent dans le magasin
// SQLite de whatsmeow, pas ici. On le range quand même chiffré avec les
// autres — c'est le format d'une connexion, et une exception n'apporterait
// qu'une branche de plus à relire dans six mois.
type WhatsAppCredentials struct {
	DeviceJID string `json:"device_jid"`
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

// KnownPlace : un lieu vu dans le calendrier, avec l'intitulé du rendez-vous qui
// s'y tenait. Sert à retrouver une adresse à partir d'un nom prononcé à l'oral.
type KnownPlace struct {
	Titre   string `json:"titre"`
	Adresse string `json:"adresse"`
}

// WhatsAppMessage : un message archivé, reçu ou envoyé.
//
// Cette collection EST la mémoire WhatsApp de Cerveau. Un appareil lié reçoit
// les messages au fil de l'eau et rien d'autre : WhatsApp ne garde aucun
// historique côté serveur, et rien ne se redemande après coup. Ce qui n'est
// pas écrit ici à l'arrivée n'existe plus.
//
// Les messages de l'utilisateur y figurent aussi (FromMe) : ce sont eux qui
// répondent à « quoi de neuf depuis mon dernier message ».
type WhatsAppMessage struct {
	ID        bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID    bson.ObjectID `bson:"user_id" json:"-"`
	MessageID string        `bson:"message_id" json:"-"`
	ChatJID   string        `bson:"chat_jid" json:"-"`
	ChatName  string        `bson:"chat_name,omitempty" json:"conversation,omitempty"`
	IsGroup   bool          `bson:"is_group,omitempty" json:"-"`
	SenderJID string        `bson:"sender_jid,omitempty" json:"-"`
	Sender    string        `bson:"sender" json:"de"`
	FromMe    bool          `bson:"from_me,omitempty" json:"de_toi,omitempty"`
	Body      string        `bson:"body" json:"message"`
	Kind      string        `bson:"kind,omitempty" json:"-"`
	// Mentioned : il est cité nommément, ou on répond à l'un de ses messages.
	Mentioned bool      `bson:"mentioned,omitempty" json:"te_cite,omitempty"`
	Timestamp time.Time `bson:"timestamp" json:"-"`
}

// WhatsAppChat : une conversation et son état de lecture.
//
// LastReadAt vient des accusés de lecture émis par les autres appareils de
// l'utilisateur : quand il ouvre une conversation sur son téléphone, le
// serveur l'apprend. C'est ce qui donne à WhatsApp un vrai « non lu », là où
// Slack oblige à approximer par l'activité récente.
type WhatsAppChat struct {
	ID       bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID   bson.ObjectID `bson:"user_id" json:"-"`
	JID      string        `bson:"jid" json:"-"`
	Name     string        `bson:"name,omitempty" json:"nom"`
	IsGroup  bool          `bson:"is_group,omitempty" json:"groupe,omitempty"`
	Muted    bool          `bson:"muted,omitempty" json:"sourdine,omitempty"`
	Archived bool          `bson:"archived,omitempty" json:"archivee,omitempty"`
	// LastAt : date du dernier message, quel qu'en soit l'auteur.
	LastAt time.Time `bson:"last_at,omitempty" json:"-"`
	// LastReadAt : jusqu'où il a lu. Zéro tant qu'on ne l'a pas appris — une
	// conversation sans état de lecture connu n'est pas une conversation
	// entièrement non lue, elle est simplement muette là-dessus.
	LastReadAt time.Time `bson:"last_read_at,omitempty" json:"-"`
	UpdatedAt  time.Time `bson:"updated_at" json:"-"`
}

// WhatsAppThread rassemble une conversation et ce qui n'y a pas été lu.
type WhatsAppThread struct {
	Chat WhatsAppChat
	// Unread : messages arrivés après la dernière lecture connue. Quand aucune
	// lecture n'est connue, la fenêtre est bornée par WhatsAppUnreadWindow —
	// autrement une conversation jamais ouverte depuis la liaison remonterait
	// six mois de messages comme s'ils venaient d'arriver.
	Unread   int
	Mentions int
	// Latest : les derniers messages non lus, du plus ancien au plus récent.
	Latest []WhatsAppMessage
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

// Digest : synthèse de la journée, mise en cache pour ne pas relancer le
// modèle à chaque ouverture de l'onglet.
type Digest struct {
	ID          bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID      bson.ObjectID `bson:"user_id" json:"-"`
	Summary     string        `bson:"summary" json:"summary"`
	GeneratedAt time.Time     `bson:"generated_at" json:"generated_at"`
}

// TaskList est la liste « à traiter » telle que le modèle l'a produite.
//
// Fingerprint est l'empreinte des messages qui l'ont produite. C'est elle qui
// décide de régénérer ou non : tant que les messages non traités sont les
// mêmes, la liste est forcément la même, et la recalculer coûterait un appel au
// modèle pour réécrire mot pour mot ce qui est déjà en base.
type TaskList struct {
	ID          bson.ObjectID `bson:"_id,omitempty" json:"-"`
	UserID      bson.ObjectID `bson:"user_id" json:"-"`
	Payload     string        `bson:"payload" json:"-"`
	Fingerprint string        `bson:"fingerprint" json:"-"`
	GeneratedAt time.Time     `bson:"generated_at" json:"generated_at"`
}

// Action : instruction renvoyée à l'app mobile (ex. écrire dans le calendrier du
// téléphone, que seule l'app peut faire).
type Action struct {
	Type    string         `bson:"type" json:"type"`
	Payload map[string]any `bson:"payload" json:"payload"`
}

// EmailDraft : une réponse de mail rédigée par Raoul et gardée pour que
// l'utilisateur la copie-colle lui-même. Raoul n'envoie rien — c'est délibéré :
// une réponse partie par erreur ne se rattrape pas, un brouillon oublié si.
type EmailDraft struct {
	ID     bson.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID bson.ObjectID `bson:"user_id" json:"-"`
	// To : le nom tel qu'on le prononce (« Cyril »), c'est par lui qu'on
	// retrouve le brouillon à l'oral.
	To      string `bson:"to" json:"to"`
	ToAddr  string `bson:"to_addr,omitempty" json:"to_addr,omitempty"`
	Subject string `bson:"subject" json:"subject"`
	Body    string `bson:"body" json:"body"`
	// Language : code ISO du mail rédigé (« fr », « en »). Il suit la langue du
	// mail d'origine, pas celle de la conversation avec Raoul.
	Language string `bson:"language,omitempty" json:"language,omitempty"`
	// SourceSubject : objet du mail auquel on répond, quand il en avait un.
	SourceSubject string    `bson:"source_subject,omitempty" json:"source_subject,omitempty"`
	CreatedAt     time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at" json:"updated_at"`
}

// Todo : une chose qu'il s'est engagé à faire, et le jour où il compte la faire.
//
// C'est le pendant durable de TaskList. La liste « à traiter » est déduite des
// messages non traités : elle se recalcule, elle se vide quand la boîte se
// vide, et rien n'y survit à la lecture d'un mail. Un engagement pris ne
// fonctionne pas comme ça — il reste jusqu'à ce qu'il soit coché, et il porte
// une date, celle qu'on a répondue quand Raoul a demandé « pour quand ? ».
type Todo struct {
	ID     bson.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID bson.ObjectID `bson:"user_id" json:"-"`
	// Title : l'action, à l'infinitif (« Configurer deux boxes pour DAW »).
	Title string `bson:"title" json:"title"`
	// Note : d'où ça vient et ce qui est attendu, pour ne pas avoir à rouvrir
	// le message trois jours plus tard.
	Note string `bson:"note,omitempty" json:"note,omitempty"`
	// Due : le jour retenu, à minuit dans son fuseau quand aucune heure n'a été
	// donnée. Nul tant qu'il n'a pas dit quand — un pointeur et pas un zéro,
	// pour que « pas encore daté » se distingue de l'an 1 côté app.
	Due *time.Time `bson:"due,omitempty" json:"due,omitempty"`
	// Timed : une heure précise a été donnée, pas seulement un jour.
	Timed     bool        `bson:"timed,omitempty" json:"timed,omitempty"`
	Done      bool        `bson:"done" json:"done"`
	DoneAt    *time.Time  `bson:"done_at,omitempty" json:"done_at,omitempty"`
	Source    *TodoSource `bson:"source,omitempty" json:"source,omitempty"`
	CreatedAt time.Time   `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time   `bson:"updated_at" json:"updated_at"`
}

// TodoSource : le message d'où sort la tâche, recopié pour l'affichage.
// Origine vaut « mail », le nom du canal Slack ou celui de la conversation
// WhatsApp — le mot qu'il reconnaît.
type TodoSource struct {
	Origine string `bson:"origine,omitempty" json:"origine,omitempty"`
	De      string `bson:"de,omitempty" json:"de,omitempty"`
	Titre   string `bson:"titre,omitempty" json:"titre,omitempty"`
}

// TodoQuery cadre une lecture de la liste.
type TodoQuery struct {
	// From et To bornent l'échéance. Une borne laissée à zéro ne borne pas :
	// c'est ce qui fait remonter le retard quand on ne fixe que To — « ce que
	// j'ai à faire aujourd'hui » inclut ce qui traîne depuis mardi.
	From, To time.Time
	// Undated : inclure aussi ce qui n'a pas encore de jour.
	Undated bool
	// DoneSince : inclure les tâches cochées depuis cet instant. Zéro n'en
	// inclut aucune. L'app s'en sert pour qu'une case cochée ne disparaisse
	// pas sous le doigt.
	DoneSince time.Time
	// Search : filtre sur le titre et la note.
	Search string
	Limit  int64
}
