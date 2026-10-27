package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
)

// Le serveur MCP de Cerveau : les outils de lecture de Raoul, pour l'atelier.
//
// Claude Code, qui fait les tâches des agents sur le VPS, parle MCP. Ce point
// d'entrée lui prête ce que Raoul sait lire — mails pro et perso, Slack,
// WhatsApp, agenda, tâches, tickets — pour le compte de Mathias, et rien
// d'autre : aucun outil n'écrit chez Mathias (voir assistant/lecture.go). Les
// seules écritures sont celles du savoir de l'équipe — ses fiches, les
// questions qu'elle lui pose, les projets que Hugo ajoute (projets_connus.go),
// réservées à un agent en plein travail. Il n'existe que si
// ATELIER_MCP_TOKEN et ATELIER_MCP_EMAIL sont donnés ; le jeton est propre à
// l'atelier, distinct de toute session d'appareil.
//
// Transport : MCP « streamable HTTP », réduit à l'essentiel — une requête
// JSON-RPC par POST, une réponse JSON. Pas de flux SSE.

const mcpToolTimeout = 90 * time.Second

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if s.cfg.AtelierMCPToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AtelierMCPToken)) != 1 {
		httpx.Error(w, http.StatusUnauthorized, "jeton MCP invalide")
		return
	}
	user, err := s.store.UserByEmail(r.Context(), s.cfg.AtelierMCPEmail)
	if err != nil {
		httpx.Error(w, http.StatusServiceUnavailable, "compte de l'atelier introuvable")
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &rpcError{Code: -32700, Message: "requête illisible"})
		return
	}
	// Une notification n'attend pas de réponse.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "cerveau", "version": "1"},
			"instructions": "Les outils de lecture de Raoul, l'assistant de Mathias : ses mails (pro et perso), Slack, WhatsApp, " +
				"son agenda, ses tâches et ses tickets. Lecture seule : rien n'est envoyé ni modifié. " +
				"S'y ajoutent les projets que connaît l'équipe et leurs fiches, qu'elle enrichit en travaillant.",
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		tools := assistant.ReadToolSpecs(s.sources(r.Context(), user))
		// Un agent en plein travail a aussi les projets de l'équipe et leurs
		// fiches ; le responsable, de quoi déléguer.
		if _, moi, ok := s.agentDuRun(r, user); ok {
			tools = append(tools, s.outilsProjets(moi)...)
			if moi.Pilote {
				tools = append(tools, s.piloteTools(moi)...)
			}
		}
		writeRPC(w, req.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			writeRPC(w, req.ID, nil, &rpcError{Code: -32602, Message: "outil manquant"})
			return
		}
		args := string(p.Arguments)
		if args == "" || args == "null" {
			args = "{}"
		}
		var out string
		var err error
		if p.Name == "confier_a_un_agent" || p.Name == "suivre_tache" {
			caller, moi, ok := s.pilote(r, user)
			if !ok {
				writeRPC(w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "seul le responsable de l'équipe délègue"}}, "isError": true}, nil)
				return
			}
			out, err = s.piloteCall(r.Context(), user, caller, moi, p.Name, args)
		} else if estOutilProjets(p.Name) {
			_, moi, ok := s.agentDuRun(r, user)
			if !ok {
				writeRPC(w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "réservé aux agents en plein travail"}}, "isError": true}, nil)
				return
			}
			out, err = s.appelOutilProjets(r.Context(), moi, p.Name, args)
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), mcpToolTimeout)
			defer cancel()
			out, err = s.engine.RunReadTool(ctx, s.toolbox(user), s.sources(ctx, user), s.location(user), p.Name, args)
		}
		if err != nil {
			slog.Warn("outil MCP en échec", "outil", p.Name, "err", err)
			writeRPC(w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}, nil)
			return
		}
		writeRPC(w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": out}}, "isError": false}, nil)
	default:
		writeRPC(w, req.ID, nil, &rpcError{Code: -32601, Message: "méthode inconnue : " + req.Method})
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
