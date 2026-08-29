// Package whatsapp branche le vrai compte WhatsApp de l'utilisateur.
//
// Pas l'API Business de Meta : celle-ci ne connaît ni les groupes, ni les
// conversations existantes, ni l'historique — elle ne sait que recevoir les
// messages envoyés à un numéro professionnel. Ce qui est demandé ici, c'est
// l'inverse : voir ce que l'utilisateur voit, ses groupes et ses conversations
// privées, pour lui dire ce qu'il n'a pas vu.
//
// On passe donc par whatsmeow, qui parle le protocole des appareils liés : le
// serveur est un appareil de plus au bout du compte, exactement comme WhatsApp
// Web. Trois conséquences qui expliquent le reste du paquet :
//
//   - la liaison se fait une fois, par code d'appairage, et la session vit
//     ensuite dans un fichier SQLite. Perdre ce fichier, c'est refaire
//     l'appairage — le reste de Cerveau est dans Mongo, pas ça, parce que
//     whatsmeow ne sait stocker ses clés que dans du SQL ;
//   - la connexion est permanente. Un appareil lié reçoit les messages au fil
//     de l'eau ou ne les reçoit pas du tout : il n'y a rien à interroger après
//     coup, d'où le journal tenu de notre côté (voir Journal) ;
//   - l'historique se limite à ce que le téléphone pousse à la liaison, puis à
//     ce qui arrive ensuite. Les conversations d'il y a deux ans ne sont pas
//     récupérables, et aucun réglage n'y change quoi que ce soit.
//
// Raoul ne fait que lire. Rien dans ce paquet n'envoie de message, ne pose de
// coche bleue ni ne rejoint quoi que ce soit : un assistant qui écrit sur
// WhatsApp à la place de quelqu'un est un accident qui attend son tour.
package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "modernc.org/sqlite"
)

// Nom affiché dans « Appareils connectés », sur le téléphone. WhatsApp valide
// la forme « Navigateur (OS) » et refuse la liaison si elle ne lui plaît pas :
// ce n'est pas un endroit où mettre « Raoul ».
const deviceName = "Chrome (Linux)"

// Account est un compte WhatsApp lié, tel que le journal s'en souvient.
type Account struct {
	UserID    string
	DeviceJID string
}

// Chat est une conversation, groupe ou tête-à-tête.
type Chat struct {
	JID     string
	Name    string
	IsGroup bool
	// Muted : mise en sourdine sur le téléphone. Une conversation en sourdine
	// ne remonte jamais dans les urgences — c'est très exactement ce que
	// l'utilisateur a demandé en la mettant en sourdine.
	Muted    bool
	Archived bool
	// LastReadAt : jusqu'où il a lu, d'après ses autres appareils. Zéro tant
	// qu'on ne l'a pas appris.
	LastReadAt time.Time
	LastAt     time.Time
}

// Message est un message archivé.
type Message struct {
	ID        string
	ChatJID   string
	ChatName  string
	IsGroup   bool
	SenderJID string
	Sender    string
	FromMe    bool
	Body      string
	Kind      string
	// Mentioned : l'utilisateur est cité nommément, ou le message répond à l'un
	// des siens. C'est ce qui distingue, dans un groupe, ce qui le concerne de
	// ce qui se dit devant lui.
	Mentioned bool
	Timestamp time.Time
}

// Journal est l'archive des conversations, tenue hors de ce paquet (Mongo, avec
// le reste). whatsmeow ne garde que les clés de chiffrement : les messages, eux,
// ne repassent jamais — ce qui n'est pas écrit à l'arrivée est perdu.
type Journal interface {
	Accounts(ctx context.Context) ([]Account, error)
	SaveDevice(ctx context.Context, userID, deviceJID, label string) error
	ForgetDevice(ctx context.Context, userID string) error
	SaveMessages(ctx context.Context, userID string, msgs []Message) error
	// SaveChats écrit une conversation en entier : c'est ce que donne
	// l'historique, qui décrit un état complet.
	SaveChats(ctx context.Context, userID string, chats []Chat) error
	// Les autres écritures sont partielles, et le restent : un message
	// n'apprend rien de la sourdine, et une mise en sourdine n'apprend rien du
	// nom. Écrire une conversation entière à chaque événement effacerait à
	// chaque fois ce que l'événement ne portait pas.
	TouchChat(ctx context.Context, userID, jid, name string, isGroup bool, at time.Time) error
	SetMuted(ctx context.Context, userID, jid string, muted bool) error
	SetArchived(ctx context.Context, userID, jid string, archived bool) error
	MarkChatRead(ctx context.Context, userID, chatJID string, at time.Time) error
}

// Phase où en est une liaison, telle que l'écran Connexions l'affiche.
const (
	PhaseOffline = "deconnecte"
	PhasePairing = "appairage"
	PhaseOnline  = "connecte"
)

// Status est l'état d'un compte, pour l'app.
type Status struct {
	Phase string `json:"phase"`
	// Code d'appairage à taper dans WhatsApp, pendant PhasePairing.
	Code   string `json:"code,omitempty"`
	Numero string `json:"numero,omitempty"`
	Erreur string `json:"erreur,omitempty"`
	// Expires : secondes restantes avant l'expiration du code.
	Expires int `json:"expire_dans,omitempty"`
}

// Manager tient une session whatsmeow par utilisateur lié.
type Manager struct {
	container *sqlstore.Container
	db        *sql.DB
	journal   Journal
	log       waLog.Logger
	// ctx vit aussi longtemps que le processus, et c'est capital : whatsmeow
	// garde le contexte de connexion pendant TOUTE la vie de la session. Il
	// pilote la reconnexion automatique, et le canal d'appairage se ferme —
	// en déconnectant le client — dès qu'il est annulé. Lui donner le contexte
	// d'une requête HTTP tuait la liaison à la seconde où le code s'affichait.
	ctx context.Context

	mu       sync.Mutex
	sessions map[string]*session
}

// NewManager ouvre le magasin de sessions. Un chemin vide désactive WhatsApp :
// le reste du serveur doit démarrer sans, comme il démarre sans clé ElevenLabs.
func NewManager(ctx context.Context, dbPath string, journal Journal) (*Manager, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, nil
	}
	// Le dossier peut ne pas exister au premier démarrage : le créer ici évite
	// une panne au déploiement pour une raison qui n'a rien à voir avec
	// WhatsApp.
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("dossier du magasin WhatsApp : %w", err)
		}
	}

	// Une seule connexion : SQLite n'accepte qu'un écrivain, et whatsmeow écrit
	// depuis plusieurs goroutines. Sans cette limite, les « database is locked »
	// arrivent au pire moment, en plein appairage.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("magasin de sessions WhatsApp : %w", err)
	}
	db.SetMaxOpenConns(1)

	log := &logger{module: "whatsapp"}
	container := sqlstore.NewWithDB(db, "sqlite", log)
	if err := container.Upgrade(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migration du magasin WhatsApp : %w", err)
	}
	return &Manager{
		container: container,
		db:        db,
		journal:   journal,
		log:       log,
		ctx:       context.WithoutCancel(ctx),
		sessions:  map[string]*session{},
	}, nil
}

// Enabled : sans magasin de sessions, il n'y a pas de WhatsApp du tout.
func (m *Manager) Enabled() bool { return m != nil && m.container != nil }

// Start reconnecte les comptes déjà liés. Appelé une fois au démarrage : un
// appareil lié qui n'est pas connecté ne reçoit rien, et ce qu'il n'a pas reçu
// ne se rattrape pas.
func (m *Manager) Start(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	accounts, err := m.journal.Accounts(ctx)
	if err != nil {
		return err
	}
	for _, acc := range accounts {
		jid, err := types.ParseJID(acc.DeviceJID)
		if err != nil {
			slog.Warn("whatsapp : identifiant d'appareil illisible", "user", acc.UserID, "err", err)
			continue
		}
		device, err := m.container.GetDevice(ctx, jid)
		if err != nil || device == nil {
			// La session a disparu du fichier SQLite (restauration, changement
			// de machine). On ne peut rien faire d'autre que le dire : il
			// faudra réappairer depuis l'app.
			slog.Warn("whatsapp : session absente du magasin, réappairage nécessaire", "user", acc.UserID)
			continue
		}
		if _, err := m.connect(acc.UserID, device); err != nil {
			slog.Error("whatsapp : reconnexion", "user", acc.UserID, "err", err)
		}
	}
	return nil
}

func (m *Manager) Close() {
	if !m.Enabled() {
		return
	}
	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*session{}
	m.mu.Unlock()

	for _, s := range sessions {
		s.client.Disconnect()
	}
	m.db.Close()
}

// newSession prépare la session d'un utilisateur sans la connecter. La
// séparation existe pour l'appairage : le canal de QR doit être demandé avant
// la connexion, et il n'y a qu'un moment pour le faire.
func (m *Manager) newSession(userID string, device *store.Device) *session {
	m.mu.Lock()
	existing, ok := m.sessions[userID]
	s := &session{
		userID: userID,
		mgr:    m,
		client: whatsmeow.NewClient(device, m.log.Sub(userID)),
		phase:  PhaseOffline,
		groups: map[types.JID]groupName{},
	}
	s.client.AddEventHandler(s.handle)
	m.sessions[userID] = s
	m.mu.Unlock()

	// L'ancienne session est fermée hors du verrou : Disconnect attend la fin
	// des gestionnaires en cours, et ceux-ci reprennent ce même verrou.
	if ok {
		existing.client.Disconnect()
	}
	return s
}

// connect ouvre la session d'un utilisateur déjà lié.
//
// La connexion prend m.ctx et pas le contexte de l'appelant : ce contexte-là
// gouverne la reconnexion automatique jusqu'à l'arrêt du serveur, alors que
// celui d'un démarrage ou d'une requête ne vaut que le temps de l'appel.
func (m *Manager) connect(userID string, device *store.Device) (*session, error) {
	s := m.newSession(userID, device)
	if err := s.client.ConnectContext(m.ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (m *Manager) session(userID string) *session {
	if !m.Enabled() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[userID]
}

// Pair démarre une liaison et rend le code à taper dans WhatsApp.
//
// Le code plutôt que le QR : le QR se scanne avec le téléphone, et le téléphone
// est précisément l'appareil qui affiche l'écran où on lirait le QR. Un code de
// huit caractères se recopie, lui, d'une app à l'autre.
//
// Aucun contexte en paramètre, et c'est délibéré : l'appairage survit à la
// requête qui l'a lancé. Il se termine sur le téléphone, une minute plus tard,
// alors que la requête a rendu le code depuis longtemps.
func (m *Manager) Pair(userID, phone string) (string, error) {
	if !m.Enabled() {
		return "", errors.New("WhatsApp n'est pas configuré côté serveur")
	}
	phone = normalizePhone(phone)
	if len(phone) < 8 || strings.HasPrefix(phone, "0") {
		return "", errors.New("numéro invalide : il le faut au format international, indicatif compris — +33612345678 et non 0612345678")
	}

	s := m.newSession(userID, m.container.NewDevice())

	// Le canal de QR se demande AVANT la connexion, et c'est lui qui dit quand
	// la websocket de liaison est prête. On n'affichera aucun QR — mais
	// réclamer un code avant que WhatsApp soit prêt à le donner échoue, et
	// c'est le premier QR qui marque ce moment.
	//
	// Son contexte est celui du serveur, jamais celui de la requête : whatsmeow
	// déconnecte le client dès que ce contexte est annulé, et la liaison se
	// termine sur le téléphone, longtemps après que la requête a rendu le code.
	qr, err := s.client.GetQRChannel(m.ctx)
	if err != nil {
		return "", fmt.Errorf("préparation de la liaison : %w", err)
	}
	if err := s.client.ConnectContext(m.ctx); err != nil {
		return "", fmt.Errorf("connexion à WhatsApp : %w", err)
	}

	ready := s.watchPairing(qr)
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		// On tente quand même : l'attente est une précaution, pas une
		// condition — et un échec ici se relit clairement dans l'erreur.
		slog.Warn("whatsapp : liaison prête sans code QR préalable", "user", userID)
	}

	// Le temps de l'échange avec les serveurs de WhatsApp, pas celui du client :
	// une app qui raccroche ne doit pas annuler un appairage déjà lancé.
	iqCtx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()

	code, err := s.client.PairPhone(iqCtx, phone, true, whatsmeow.PairClientChrome, deviceName)
	if err != nil {
		s.client.Disconnect()
		return "", fmt.Errorf("demande d'appairage : %w", err)
	}
	s.setPairing(code)
	return code, nil
}

// Status dit où en est la liaison. Sans session ouverte mais avec un compte
// connu, c'est une connexion perdue, pas une absence de compte : l'app doit
// pouvoir le distinguer.
func (m *Manager) Status(userID string) Status {
	s := m.session(userID)
	if s == nil {
		return Status{Phase: PhaseOffline}
	}
	return s.status()
}

// Logout délie l'appareil, côté WhatsApp comme côté serveur. L'utilisateur peut
// aussi le faire depuis son téléphone : on reçoit alors events.LoggedOut, et le
// résultat est le même.
func (m *Manager) Logout(ctx context.Context, userID string) error {
	if !m.Enabled() {
		return nil
	}
	m.mu.Lock()
	s := m.sessions[userID]
	delete(m.sessions, userID)
	m.mu.Unlock()

	if s != nil {
		if s.client.IsLoggedIn() {
			// Best effort : si WhatsApp refuse, l'appareil restera listé sur le
			// téléphone, mais notre session est bel et bien jetée.
			if err := s.client.Logout(ctx); err != nil {
				slog.Warn("whatsapp : déliaison côté WhatsApp", "err", err)
			}
		}
		s.client.Disconnect()
		if s.client.Store != nil {
			_ = s.client.Store.Delete(ctx)
		}
	}
	return m.journal.ForgetDevice(ctx, userID)
}

// session est la connexion d'un utilisateur, et tout ce qu'on sait d'elle.
type session struct {
	userID string
	mgr    *Manager
	client *whatsmeow.Client

	mu       sync.Mutex
	phase    string
	code     string
	codeAt   time.Time
	lastErr  string
	groups   map[types.JID]groupName
	pushName string
}

// groupName met en cache le sujet d'un groupe : il ne voyage pas avec les
// messages, il faut le demander au serveur, et une conversation active
// déclencherait sinon une requête par message reçu.
type groupName struct {
	name string
	at   time.Time
}

const groupNameTTL = 6 * time.Hour

// Durée de vie d'un code d'appairage. WhatsApp ferme la connexion de liaison au
// bout de trois minutes environ ; on annonce large plutôt que de laisser l'app
// afficher un code déjà mort.
const pairingWindow = 150 * time.Second

func (s *session) setPairing(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.code, s.codeAt, s.lastErr = PhasePairing, code, time.Now(), ""
}

func (s *session) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := Status{Phase: s.phase, Erreur: s.lastErr}
	if s.client.Store != nil && s.client.Store.ID != nil {
		out.Numero = "+" + s.client.Store.ID.User
	}
	if s.pushName != "" {
		out.Numero = strings.TrimSpace(s.pushName + " " + out.Numero)
	}
	if s.phase == PhasePairing {
		left := pairingWindow - time.Since(s.codeAt)
		if left <= 0 {
			out.Phase = PhaseOffline
			out.Erreur = "le code a expiré, redemande une liaison"
			return out
		}
		out.Code = s.code
		out.Expires = int(left.Seconds())
	}
	return out
}

func (s *session) fail(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.code, s.lastErr = PhaseOffline, "", reason
}

// normalizePhone ne garde que les chiffres. WhatsApp veut le numéro
// international sans « + » ni espaces, et l'utilisateur le dicte comme il
// l'écrit — « +33 6 12 34 56 78 ».
func normalizePhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// logger fait passer les journaux de whatsmeow par slog, comme le reste du
// serveur. Tout descend d'un cran : ce que la bibliothèque juge digne d'un
// « info » est du détail de protocole pour nous.
type logger struct{ module string }

func (l *logger) Errorf(msg string, args ...any) {
	slog.Error("whatsapp: "+fmt.Sprintf(msg, args...), "module", l.module)
}
func (l *logger) Warnf(msg string, args ...any) {
	slog.Warn("whatsapp: "+fmt.Sprintf(msg, args...), "module", l.module)
}
func (l *logger) Infof(msg string, args ...any) {
	slog.Info("whatsapp: "+fmt.Sprintf(msg, args...), "module", l.module)
}
func (l *logger) Debugf(msg string, args ...any) {
	slog.Debug("whatsapp: "+fmt.Sprintf(msg, args...), "module", l.module)
}
func (l *logger) Sub(module string) waLog.Logger {
	return &logger{module: l.module + "/" + module}
}

var _ waLog.Logger = (*logger)(nil)
