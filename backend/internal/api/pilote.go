package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Le responsable qui pilote l'équipe depuis Cerveau HQ.
//
// On lui confie une demande d'ensemble (« où en est telle fonctionnalité sur
// tel projet ? ») ; il la découpe, confie chaque partie à l'agent compétent,
// suit leur travail et fait circuler l'information : si l'ingénieur n'a pas la
// documentation, il demande à la veille des mails de la chercher, puis la
// renvoie à l'ingénieur. Il travaille dans l'atelier, comme les autres, et
// délègue par deux outils que Cerveau lui prête (voir mcp.go) : chaque tâche
// confiée est une vraie exécution, rattachée à la sienne, que le bâtiment
// montre.

const (
	// piloteMaxDelegations : au-delà, il conclut avec ce qu'il a.
	piloteMaxDelegations = 8
	// suiviMaxAttente : une seule attente reste sous le délai d'écriture du
	// serveur HTTP (2 min) ; le responsable rappelle suivre_tache en boucle.
	suiviMaxAttente = 90 * time.Second
)

// consignePilote : ce que le responsable sait de son équipe et de sa façon
// de travailler, ajouté à ses instructions dans l'atelier.
func (s *Server) consignePilote(moi equipe.Membre) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TU PILOTES L'ÉQUIPE. Tu es %s, responsable de l'équipe d'agents de Mathias. Ton équipe :\n", moi.Nom)
	for _, m := range s.equipe.Candidats() {
		if m.ID == moi.ID {
			continue
		}
		fmt.Fprintf(&b, "- %s (identifiant %q), %s. %s", m.Nom, m.ID, strings.ToLower(m.Role), m.Presentation)
		if m.Quand != "" {
			fmt.Fprintf(&b, " À solliciter pour : %s.", m.Quand)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, `
Comment tu travailles :
1. Découpe la demande et confie chaque partie à l'agent compétent avec l'outil confier_a_un_agent : son identifiant, et une consigne complète et autonome (le contexte, ce que tu attends, sous quelle forme). Tu peux confier plusieurs tâches en même temps.
2. Suis chaque tâche avec suivre_tache, en le rappelant jusqu'à ce qu'elle soit terminée (il attend jusqu'à 90 secondes à chaque appel). Ne t'arrête pas avant d'avoir toutes les réponses.
3. Fais circuler l'information. Si un agent dit qu'il lui manque quelque chose — une documentation, un échange, une décision —, demande-le à l'agent qui peut le trouver (la veille des mails pour un document reçu par mail, celle de Slack ou de WhatsApp pour une discussion), puis renvoie une tâche au premier agent avec ce qu'il lui manquait.
4. Tu ne fais pas toi-même le travail d'un spécialiste : tu peux seulement vérifier rapidement un point. %d délégations au plus par demande.
5. Ta réponse finale va à Mathias : ce qui en ressort, en trois phrases au plus, puis qui a fait quoi, en quelques mots. Cette règle remplace la règle 7.`, piloteMaxDelegations)
	return b.String()
}

// consigneDeleguee : ajoutée aux instructions d'un agent qui travaille pour
// le responsable.
func consigneDeleguee(pilote string) string {
	return fmt.Sprintf("CETTE TÂCHE T'EST CONFIÉE PAR %s, le responsable de l'équipe, pour Mathias. Ta réponse va à %s, pas à Mathias : "+
		"donne les faits utiles et précis (dix lignes au plus), et dis clairement s'il te manque une information pour avancer — "+
		"laquelle, et où elle pourrait se trouver. Cette règle remplace la règle 7.", strings.ToUpper(pilote), pilote)
}

// -- Les outils du responsable, prêtés par le serveur MCP ----------------------

// piloteTools : les outils de délégation, décrits pour MCP.
func (s *Server) piloteTools(moi equipe.Membre) []assistant.ToolSpec {
	var ids []any
	for _, m := range s.equipe.Candidats() {
		if m.ID != moi.ID {
			ids = append(ids, m.ID)
		}
	}
	return []assistant.ToolSpec{
		{
			Name: "confier_a_un_agent",
			Description: "Confie une tâche à un agent de l'équipe. La tâche démarre aussitôt (ou attend sa place) ; " +
				"rends-toi ensuite à suivre_tache avec l'identifiant rendu pour obtenir sa réponse.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent":    map[string]any{"type": "string", "enum": ids, "description": "L'identifiant de l'agent."},
					"consigne": map[string]any{"type": "string", "description": "Ce qu'il doit faire : contexte, attendu, forme de la réponse."},
				},
				"required": []any{"agent", "consigne"},
			},
		},
		{
			Name:        "suivre_tache",
			Description: "Attend qu'une tâche confiée avance (jusqu'à 90 secondes) et rend son état ; une fois terminée, sa réponse. Rappelle-le tant qu'elle n'est pas finie.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tache":    map[string]any{"type": "string", "description": "L'identifiant rendu par confier_a_un_agent."},
					"attendre": map[string]any{"type": "integer", "description": "Secondes d'attente au plus (90 par défaut et au maximum)."},
				},
				"required": []any{"tache"},
			},
		},
	}
}

// pilote : l'agent qui appelle le serveur MCP, si c'est le responsable en
// plein travail pour cet utilisateur.
func (s *Server) pilote(r *http.Request, user *store.User) (runSnapshot, equipe.Membre, bool) {
	snap, m, ok := s.agentDuRun(r, user)
	if !ok || !m.Pilote {
		return runSnapshot{}, equipe.Membre{}, false
	}
	return snap, m, true
}

// agentDuRun : l'agent qui appelle le serveur MCP, quand une de ses tâches
// pour cet utilisateur est en cours (l'atelier pose son identifiant dans
// l'en-tête X-Cerveau-Run).
func (s *Server) agentDuRun(r *http.Request, user *store.User) (runSnapshot, equipe.Membre, bool) {
	raw := strings.TrimSpace(r.Header.Get("X-Cerveau-Run"))
	id, err := bson.ObjectIDFromHex(raw)
	if err != nil || s.equipe == nil {
		return runSnapshot{}, equipe.Membre{}, false
	}
	snap, ok := s.runs.liveRunByID(id)
	if !ok || snap.Run.UserID != user.ID {
		return runSnapshot{}, equipe.Membre{}, false
	}
	m, ok := s.equipe.Membre(snap.Run.Agent)
	if !ok {
		return runSnapshot{}, equipe.Membre{}, false
	}
	return snap, m, true
}

// piloteCall exécute un outil du responsable et rend son texte.
func (s *Server) piloteCall(ctx context.Context, user *store.User, caller runSnapshot, moi equipe.Membre, name, args string) (string, error) {
	switch name {
	case "confier_a_un_agent":
		var in struct {
			Agent    string `json:"agent"`
			Consigne string `json:"consigne"`
		}
		if err := json.Unmarshal([]byte(args), &in); err != nil || strings.TrimSpace(in.Consigne) == "" {
			return "", fmt.Errorf("donne un agent et une consigne")
		}
		m, ok := s.hqMember(in.Agent)
		if !ok || m.ID == moi.ID {
			return "", fmt.Errorf("agent inconnu : %s", in.Agent)
		}
		n, err := s.store.CountChildren(ctx, caller.Run.ID)
		if err == nil && n >= piloteMaxDelegations {
			return "", fmt.Errorf("%d délégations déjà : conclus avec ce que tu as", n)
		}
		parent := caller.Run.ID
		run, err := s.launchRun(user, m, strings.TrimSpace(in.Consigne), runOpts{Source: store.RunSourceDelegation, Parent: &parent}, consigneDeleguee(moi.Nom))
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(map[string]string{"tache": run.ID.Hex(), "agent": m.Nom, "statut": run.Statut})
		return string(out), nil

	case "suivre_tache":
		var in struct {
			Tache    string `json:"tache"`
			Attendre int    `json:"attendre"`
		}
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return "", fmt.Errorf("donne l'identifiant de la tâche")
		}
		id, err := bson.ObjectIDFromHex(strings.TrimSpace(in.Tache))
		if err != nil {
			return "", fmt.Errorf("identifiant de tâche invalide")
		}
		wait := suiviMaxAttente
		if in.Attendre > 0 && time.Duration(in.Attendre)*time.Second < wait {
			wait = time.Duration(in.Attendre) * time.Second
		}
		deadline := time.Now().Add(wait)
		for {
			run, etape, ok := s.runState(ctx, user, id)
			if !ok || run.Parent == nil || *run.Parent != caller.Run.ID {
				return "", fmt.Errorf("tâche introuvable parmi celles que tu as confiées")
			}
			done := run.Statut == store.RunTermine || run.Statut == store.RunErreur || run.Statut == store.RunAnnule
			if done || time.Now().After(deadline) {
				m, _ := s.equipe.Membre(run.Agent)
				out := map[string]any{"tache": id.Hex(), "agent": m.Nom, "statut": run.Statut}
				if etape != "" {
					out["etape_en_cours"] = etape
				}
				if run.Reponse != "" {
					out["reponse"] = run.Reponse
				}
				if run.Erreur != "" {
					out["erreur"] = run.Erreur
				}
				if !done {
					out["conseil"] = "pas encore fini : rappelle suivre_tache"
				}
				raw, _ := json.Marshal(out)
				return string(raw), nil
			}
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	return "", fmt.Errorf("outil inconnu : %s", name)
}

// runState : l'état d'une exécution, vivante ou terminée.
func (s *Server) runState(ctx context.Context, user *store.User, id bson.ObjectID) (store.AgentRun, string, bool) {
	if snap, ok := s.runs.liveRunByID(id); ok && snap.Run.UserID == user.ID {
		return snap.Run, snap.Etape, true
	}
	run, err := s.store.RunByID(ctx, user.ID, id)
	if err != nil {
		return store.AgentRun{}, "", false
	}
	return run, "", true
}
