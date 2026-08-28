package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/providers/whatsapp"
)

// handleWhatsAppPair démarre la liaison et rend le code à taper dans WhatsApp.
//
// Le code plutôt que le QR : le QR se scanne avec le téléphone, or c'est ce
// même téléphone qui affiche l'écran où on lirait le QR. Huit caractères se
// recopient d'une app à l'autre, un QR non.
func (s *Server) handleWhatsAppPair(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if !s.wa.Enabled() {
		httpx.Error(w, http.StatusServiceUnavailable,
			"WhatsApp n'est pas configuré sur le serveur (WHATSAPP_SESSION_DB)")
		return
	}

	var req struct {
		Numero string `json:"numero"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "corps de requête invalide")
		return
	}
	if strings.TrimSpace(req.Numero) == "" {
		httpx.Error(w, http.StatusBadRequest, "numéro de téléphone requis")
		return
	}

	// Généreux : la liaison passe par un aller-retour avec les serveurs de
	// WhatsApp, et un code demandé trop tard ne vaut plus rien.
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	code, err := s.wa.Pair(ctx, user.ID.Hex(), req.Numero)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"code":   code,
		"statut": s.wa.Status(user.ID.Hex()),
	})
}

// handleWhatsAppStatus : l'app interroge cette route pendant l'appairage, puis
// pour savoir si la connexion tient. Un appareil lié qui n'est pas connecté ne
// reçoit rien — c'est une panne, pas un détail d'affichage.
func (s *Server) handleWhatsAppStatus(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	status := s.wa.Status(user.ID.Hex())

	// Un compte enregistré dont la session n'est pas ouverte : le dire, plutôt
	// que de laisser croire à une absence de compte.
	if status.Phase == whatsapp.PhaseOffline && status.Erreur == "" {
		if _, err := s.whatsappCreds(r.Context(), user); err == nil {
			status.Erreur = "compte lié mais session fermée, le serveur ne reçoit rien"
		}
	}
	httpx.JSON(w, http.StatusOK, status)
}

// handleWhatsAppChats liste les conversations connues. Sert à l'écran
// Connexions, pour montrer que la liaison a bien ramené quelque chose.
func (s *Server) handleWhatsAppChats(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	chats, err := s.store.WhatsAppChats(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "lecture impossible")
		return
	}
	out := make([]map[string]any, 0, len(chats))
	for _, c := range chats {
		out = append(out, map[string]any{
			"nom":       c.Name,
			"groupe":    c.IsGroup,
			"sourdine":  c.Muted,
			"archivee":  c.Archived,
			"dernier":   c.LastAt,
			"lu_jusqua": c.LastReadAt,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"conversations": out})
}
