// Package atelier confie les tâches des agents à Claude Code, sur le VPS.
//
// Cerveau tourne dans un conteneur sans dépôts ni outils de développement.
// Quand on donne une tâche à un agent, elle part en SSH vers l'atelier : un
// petit programme sur l'hôte (deploy/atelier/atelier.py), lancé sous un
// utilisateur sans privilèges, qui met les dépôts à jour et la confie à Claude
// Code — connecté à l'abonnement Claude de Mathias, pas à une clé d'API. La
// clé SSH de Cerveau est attachée à ce programme et à rien d'autre.
//
// Claude Code répond en stream-json ; ce package en tire les étapes (« lecture
// du code », « tests », « push »…) pour le bâtiment, et la réponse finale.
package atelier

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/sonde"
)

// Tache : ce que l'atelier reçoit.
type Tache struct {
	Run          string `json:"run"`
	Agent        string `json:"agent"`
	Nom          string `json:"nom"`
	Role         string `json:"role"`
	Presentation string `json:"presentation"`
	Demande      string `json:"demande"`
}

// Client joint l'atelier. Une connexion par tâche : elles durent des minutes,
// et fermer la connexion est la façon d'arrêter Claude Code.
type Client struct {
	addr string
	cfg  *ssh.ClientConfig
}

// New prépare le client. Rend nil, nil quand l'atelier n'est pas configuré :
// les tâches restent alors au moteur habituel.
func New(c sonde.Config) (*Client, error) {
	if strings.TrimSpace(c.Cible) == "" {
		return nil, nil
	}
	addr, cfg, err := sonde.SSH(c)
	if err != nil {
		return nil, fmt.Errorf("atelier : %w", err)
	}
	return &Client{addr: addr, cfg: cfg}, nil
}

// Run confie une tâche à Claude Code et suit son travail jusqu'au bout. emit
// reçoit les étapes, au format du moteur : le bâtiment les montre pareil.
func (c *Client) Run(ctx context.Context, t Tache, emit assistant.Emitter) (assistant.Result, error) {
	d := net.Dialer{Timeout: c.cfg.Timeout}
	raw, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return assistant.Result{}, fmt.Errorf("atelier injoignable : %w", err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(raw, c.addr, c.cfg)
	if err != nil {
		_ = raw.Close()
		return assistant.Result{}, fmt.Errorf("connexion à l'atelier refusée : %w", err)
	}
	conn := ssh.NewClient(sc, chans, reqs)
	defer conn.Close()
	sess, err := conn.NewSession()
	if err != nil {
		return assistant.Result{}, fmt.Errorf("session atelier : %w", err)
	}
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	if err != nil {
		return assistant.Result{}, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return assistant.Result{}, err
	}
	var stderr strings.Builder
	sess.Stderr = &limited{w: &stderr, n: 4000}
	if err := sess.Start("atelier"); err != nil {
		return assistant.Result{}, fmt.Errorf("atelier : %w", err)
	}
	line, _ := json.Marshal(t)
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		return assistant.Result{}, fmt.Errorf("atelier : %w", err)
	}
	// L'entrée reste ouverte : la fermer arrête Claude Code. C'est ce qui
	// se passe si la tâche est annulée ou dépasse son délai.
	stop := context.AfterFunc(ctx, func() {
		_ = stdin.Close()
		_ = conn.Close()
	})
	defer stop()

	r := newReader(emit)
	sc2 := bufio.NewScanner(stdout)
	sc2.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc2.Scan() {
		r.line(sc2.Bytes())
	}
	waitErr := sess.Wait()
	r.closeOpen()
	if ctx.Err() != nil {
		return assistant.Result{}, ctx.Err()
	}
	if r.failure != "" {
		return assistant.Result{}, errors.New(r.failure)
	}
	if r.result == nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		if msg == "" {
			msg = "Claude Code n'a rien répondu"
		}
		return assistant.Result{}, fmt.Errorf("atelier : %s", msg)
	}
	return *r.result, nil
}

type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, _ = l.w.Write(q)
	return len(p), nil
}

// -- Lecture du flux de Claude Code -------------------------------------------

type opened struct {
	tool  string
	start time.Time
}

type reader struct {
	emit    assistant.Emitter
	open    map[string]opened
	result  *assistant.Result
	failure string
}

func newReader(emit assistant.Emitter) *reader {
	if emit == nil {
		emit = func(assistant.Event) {}
	}
	return &reader{emit: emit, open: map[string]opened{}}
}

type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// atelier
	Event   string          `json:"event"`
	Message json.RawMessage `json:"message"`
	Erreurs []string        `json:"erreurs"`
	Stderr  string          `json:"stderr"`
	Code    int             `json:"code"`
	// result
	Result  string  `json:"result"`
	IsError bool    `json:"is_error"`
	Cost    float64 `json:"total_cost_usd"`
	Turns   int     `json:"num_turns"`
}

type content struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

type message struct {
	Content []content `json:"content"`
}

func (r *reader) line(b []byte) {
	var l streamLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	switch l.Type {
	case "atelier":
		switch l.Event {
		case "pret":
			r.emit(assistant.Event{Type: "status", Tool: "atelier", Label: "Préparation des dépôts"})
			r.emit(assistant.Event{Type: "status", Tool: "atelier", Label: "Préparation des dépôts", Ms: 1})
		case "erreur":
			var msg string
			_ = json.Unmarshal(l.Message, &msg)
			r.failure = "atelier : " + msg
		case "fin":
			if r.result == nil && r.failure == "" {
				r.failure = fmt.Sprintf("Claude Code s'est arrêté (code %d) : %s", l.Code, strings.TrimSpace(l.Stderr))
			}
		}
	case "assistant":
		var m message
		if json.Unmarshal(l.Message, &m) != nil {
			return
		}
		for _, c := range m.Content {
			if c.Type != "tool_use" {
				continue
			}
			label := Label(c.Name, c.Input)
			r.open[c.ID] = opened{tool: c.Name, start: time.Now()}
			r.emit(assistant.Event{Type: "status", Tool: c.Name, Label: label})
		}
	case "user":
		var m message
		if json.Unmarshal(l.Message, &m) != nil {
			return
		}
		for _, c := range m.Content {
			if c.Type != "tool_result" {
				continue
			}
			o, ok := r.open[c.ToolUseID]
			if !ok {
				continue
			}
			delete(r.open, c.ToolUseID)
			ev := assistant.Event{Type: "status", Tool: o.tool, Ms: max(1, time.Since(o.start).Milliseconds())}
			if c.IsError {
				ev.Err = "échec"
			}
			r.emit(ev)
		}
	case "result":
		if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
			msg := strings.TrimSpace(l.Result)
			if msg == "" {
				msg = l.Subtype
			}
			r.failure = "Claude Code : " + msg
			return
		}
		r.result = &assistant.Result{Reply: strings.TrimSpace(l.Result)}
	}
}

// closeOpen clôt les étapes restées ouvertes (tâche interrompue).
func (r *reader) closeOpen() {
	for id, o := range r.open {
		r.emit(assistant.Event{Type: "status", Tool: o.tool, Ms: max(1, time.Since(o.start).Milliseconds()), Err: "interrompu"})
		delete(r.open, id)
	}
}

// Label : l'étape telle qu'on la lit dans le bâtiment.
func Label(tool string, input json.RawMessage) string {
	switch tool {
	case "Read", "NotebookRead":
		return "Lecture du code"
	case "Grep", "Glob", "LS":
		return "Recherche dans le code"
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return "Modification du code"
	case "WebFetch", "WebSearch":
		return "Recherche sur le web"
	case "Task", "Agent":
		return "Travail confié à un sous-agent"
	case "TodoWrite":
		return "Plan de travail"
	case "Bash":
		var in struct {
			Command     string `json:"command"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(input, &in)
		return commandLabel(in.Command, in.Description)
	}
	return tool
}

func commandLabel(cmd, desc string) string {
	c := strings.ToLower(cmd)
	switch {
	case strings.Contains(c, "git push"):
		return "Push de la branche"
	case strings.Contains(c, "git commit"):
		return "Commit"
	case strings.Contains(c, "worktree add"), strings.Contains(c, "git checkout -b"), strings.Contains(c, "git switch -c"):
		return "Création de la branche"
	case strings.Contains(c, "test"), strings.Contains(c, "pytest"), strings.Contains(c, "jest"), strings.Contains(c, "vitest"):
		return "Tests"
	case strings.Contains(c, "lint"), strings.Contains(c, "tsc"), strings.Contains(c, "go vet"), strings.Contains(c, "eslint"):
		return "Vérification du code"
	case strings.Contains(c, "build"):
		return "Compilation"
	case strings.Contains(c, "npm install"), strings.Contains(c, "npm ci"), strings.Contains(c, "go mod download"), strings.Contains(c, "pip install"):
		return "Installation des dépendances"
	case strings.HasPrefix(strings.TrimSpace(c), "git "), strings.Contains(c, " git "):
		return "Historique git"
	}
	if d := strings.TrimSpace(desc); d != "" {
		if len([]rune(d)) > 60 {
			d = string([]rune(d)[:60]) + "…"
		}
		return d
	}
	return "Commande"
}
