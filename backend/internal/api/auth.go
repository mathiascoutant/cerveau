package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/mathiascoutant/cerveau/backend/internal/httpx"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

type ctxKey string

const userCtxKey ctxKey = "cerveau.user"

// requireUser résout l'utilisateur à partir du token de session, émis à la
// connexion par adresse et mot de passe et gardé dans le Keychain iOS.
func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			httpx.Error(w, http.StatusUnauthorized, "non connecté")
			return
		}
		user, err := s.store.UserBySession(r.Context(), token)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				httpx.Error(w, http.StatusUnauthorized, "session expirée, reconnecte-toi")
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "erreur d'authentification")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userCtxKey).(*store.User)
	return u
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if token, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(token)
	}
	return ""
}
