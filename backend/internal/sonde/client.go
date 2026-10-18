package sonde

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client interroge la sonde en SSH.
//
// Une seule connexion, gardée ouverte et partagée : le point sur les projets
// lance une demi-douzaine de relevés en parallèle, et SSH sait les faire passer
// par une seule connexion, chacun dans sa session. La rouvrir à chaque relevé
// coûterait une poignée de main par question.
type Client struct {
	addr    string
	cfg     *ssh.ClientConfig
	command string

	mu   sync.Mutex
	conn *ssh.Client
	// slots borne les relevés simultanés sur la connexion. sshd refuse au-delà
	// de MaxSessions (10 par défaut) : l'ingénieur qui lançait six relevés
	// pendant que le point des projets en lançait cinq voyait les derniers
	// refusés — et chaque refus faisait fermer la connexion des autres.
	slots chan struct{}
}

// maxSessions : relevés simultanés sur la connexion partagée, sous la limite
// par défaut de sshd.
const maxSessions = 6

// Config décrit comment joindre la sonde.
type Config struct {
	// Cible : « utilisateur@hôte:port » (port 22 par défaut).
	Cible string
	// Cle : la clé privée dédiée. Vide : l'agent SSH (SSH_AUTH_SOCK), pratique
	// pour essayer depuis le poste de développement.
	Cle string
	// CleHote : la clé publique attendue du serveur (« ssh-ed25519 AAAA… »).
	// Vide : le fichier known_hosts. Jamais aucune vérification : un relevé
	// qui pourrait venir de n'importe quelle machine ne vaut rien.
	CleHote    string
	KnownHosts string
	// Commande : le programme à demander quand la clé n'est pas attachée à la
	// sonde (essai depuis un poste de développement). Ignoré sinon.
	Commande string
}

// NewClient prépare le client sans se connecter. Rend nil, nil quand rien
// n'est configuré : pas de sonde, pas d'outils VPS.
func NewClient(c Config) (*Client, error) {
	if strings.TrimSpace(c.Cible) == "" {
		return nil, nil
	}
	user, host, ok := strings.Cut(c.Cible, "@")
	if !ok || user == "" || host == "" {
		return nil, fmt.Errorf("SONDE_SSH doit être de la forme utilisateur@hôte[:port], pas %q", c.Cible)
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "22")
	}

	auth, err := authMethod(c.Cle)
	if err != nil {
		return nil, err
	}
	hostKey, algorithms, err := hostKeyCallback(c.CleHote, c.KnownHosts)
	if err != nil {
		return nil, err
	}
	command := c.Commande
	if command == "" {
		command = "cerveau-sonde"
	}
	return &Client{
		addr:    host,
		command: command,
		slots:   make(chan struct{}, maxSessions),
		cfg: &ssh.ClientConfig{
			User:              user,
			Auth:              []ssh.AuthMethod{auth},
			HostKeyCallback:   hostKey,
			HostKeyAlgorithms: algorithms,
			Timeout:           10 * time.Second,
		},
	}, nil
}

func authMethod(keyPath string) (ssh.AuthMethod, error) {
	if keyPath != "" {
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("clé de la sonde illisible : %w", err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("clé de la sonde invalide (elle doit être sans phrase de passe) : %w", err)
		}
		return ssh.PublicKeys(signer), nil
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("ni SONDE_CLE ni agent SSH : aucune façon de s'authentifier auprès de la sonde")
	}
	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, err
		}
		return agent.NewClient(conn).Signers()
	}), nil
}

// hostKeyCallback rend la vérification de la clé du serveur, et les
// algorithmes à négocier pour qu'elle puisse réussir.
//
// Une clé épinglée ne vaut que si le serveur présente CELLE-LÀ : un serveur
// SSH a plusieurs clés d'hôte (ed25519, ecdsa, rsa) et, laissé libre, il en
// choisit une autre que celle qu'on attend — la vérification échoue alors sur
// un serveur parfaitement légitime. On ne propose donc que le type épinglé.
func hostKeyCallback(pinned, knownHostsPath string) (ssh.HostKeyCallback, []string, error) {
	if strings.TrimSpace(pinned) != "" {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pinned))
		if err != nil {
			return nil, nil, fmt.Errorf("SONDE_CLE_HOTE illisible : %w", err)
		}
		algorithms := []string{key.Type()}
		if key.Type() == ssh.KeyAlgoRSA {
			// Une clé RSA se vérifie avec SHA-2 : l'algorithme « ssh-rsa »
			// (SHA-1) est refusé par les serveurs récents.
			algorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
		}
		return ssh.FixedHostKey(key), algorithms, nil
	}
	if knownHostsPath == "" {
		home, _ := os.UserHomeDir()
		knownHostsPath = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("ni SONDE_CLE_HOTE ni known_hosts lisible : %w", err)
	}
	return cb, nil, nil
}

// Close ferme la connexion partagée.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *Client) connection(ctx context.Context) (*ssh.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn, nil
	}
	d := net.Dialer{Timeout: c.cfg.Timeout}
	raw, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, fmt.Errorf("VPS injoignable : %w", err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(raw, c.addr, c.cfg)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("connexion à la sonde refusée : %w", err)
	}
	c.conn = ssh.NewClient(sc, chans, reqs)
	return c.conn, nil
}

func (c *Client) drop(conn *ssh.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == conn {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// call envoie une demande et décode la réponse dans into. Une connexion
// tombée entre deux relevés est rouverte une fois.
func (c *Client) call(ctx context.Context, req Requete, into any) error {
	word, err := req.Encode()
	if err != nil {
		return err
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		conn, err := c.connection(ctx)
		if err != nil {
			return err
		}
		out, err := c.run(ctx, conn, c.command+" "+word)
		if err != nil {
			var exit *ssh.ExitError
			if errors.As(err, &exit) && len(out) > 0 {
				// La sonde a répondu, avec une erreur : c'est le contenu qui
				// compte, pas le code de sortie.
				return decode(out, into)
			}
			lastErr = err
			// Une session refusée n'est pas une connexion morte : le serveur
			// a dit « pas maintenant ». On réessaie sur la même, un peu plus
			// tard, au lieu de couper les relevés des autres.
			var refused *ssh.OpenChannelError
			if errors.As(err, &refused) {
				if !sleepCtx(ctx, time.Duration(attempt+1)*300*time.Millisecond) {
					return ctx.Err()
				}
				continue
			}
			c.drop(conn)
			continue
		}
		return decode(out, into)
	}
	return fmt.Errorf("la sonde ne répond pas : %w", lastErr)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Client) run(ctx context.Context, conn *ssh.Client, command string) ([]byte, error) {
	session, err := conn.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	var stdout bytes.Buffer
	session.Stdout = &limitWriter{w: &stdout, n: 8 << 20}
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case err := <-done:
		return stdout.Bytes(), err
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return nil, ctx.Err()
	}
}

func decode(out []byte, into any) error {
	var resp Reponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("réponse de la sonde illisible : %w", err)
	}
	if resp.Erreur != "" {
		return errors.New(resp.Erreur)
	}
	if resp.Version != Version {
		return fmt.Errorf("la sonde installée sur le VPS est en version %q, Cerveau attend %q : réinstalle-la", resp.Version, Version)
	}
	if into == nil || len(resp.Donnees) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Donnees, into)
}

type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.w.Write(p)
	l.n -= int64(n)
	return len(p), err
}

// --- Les relevés -------------------------------------------------------------

func (c *Client) Systeme(ctx context.Context) (Systeme, error) {
	var out Systeme
	return out, c.call(ctx, Requete{Commande: CmdSysteme}, &out)
}

func (c *Client) Conteneurs(ctx context.Context) ([]Conteneur, error) {
	var out []Conteneur
	return out, c.call(ctx, Requete{Commande: CmdConteneurs}, &out)
}

func (c *Client) Journaux(ctx context.Context, conteneur string, lignes int, filtre, depuis string) (Journaux, error) {
	var out Journaux
	// Traduit ici aussi : la sonde déjà installée sur le VPS ne connaît pas
	// encore les jours.
	args := map[string]string{"conteneur": conteneur, "lignes": strconv.Itoa(lignes), "filtre": filtre, "depuis": DaysToHours(depuis)}
	return out, c.call(ctx, Requete{Commande: CmdJournaux, Args: args}, &out)
}

func (c *Client) Stats(ctx context.Context) ([]Stat, error) {
	var out []Stat
	return out, c.call(ctx, Requete{Commande: CmdStats}, &out)
}

func (c *Client) Mongo(ctx context.Context, base string) (Mongo, error) {
	var out Mongo
	return out, c.call(ctx, Requete{Commande: CmdMongo, Args: map[string]string{"base": base}}, &out)
}

func (c *Client) Expliquer(ctx context.Context, base, collection, filtre string) (Explication, error) {
	var out Explication
	args := map[string]string{"base": base, "collection": collection, "filtre": filtre}
	return out, c.call(ctx, Requete{Commande: CmdExpliquer, Args: args}, &out)
}

func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	var out []Site
	return out, c.call(ctx, Requete{Commande: CmdSites}, &out)
}
