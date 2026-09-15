package api

import (
	"context"
	"log/slog"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// La mémoire vue depuis l'assistant.
//
// Deux gestes seulement — retenir, oublier — et pas de lecture. Relire n'est
// pas un geste ici : ce qui est retenu part dans la consigne système à chaque
// tour, donc c'est déjà sous ses yeux quand il commence à réfléchir. Ajouter un
// outil « qu'est-ce que je sais » lui donnerait le moyen d'aller chercher ce
// qu'il a déjà, et un tour de plus à chaque fois qu'il choisit de le faire.

// Remember retient une information, ou remplace celle qui portait ce sujet.
func (t *userToolbox) Remember(ctx context.Context, fact assistant.FactView) (assistant.FactView, error) {
	saved, err := t.srv.store.SaveFact(ctx, store.Fact{
		UserID:  t.user.ID,
		Kind:    fact.Kind,
		Subject: fact.Subject,
		Content: fact.Content,
		Origin:  "voix",
	})
	if err != nil {
		return assistant.FactView{}, err
	}
	return factView(saved), nil
}

// Forget oublie tout ce qui touche à un sujet.
func (t *userToolbox) Forget(ctx context.Context, query string) ([]assistant.FactView, error) {
	gone, err := t.srv.store.ForgetFacts(ctx, t.user.ID, query)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.FactView, 0, len(gone))
	for _, f := range gone {
		out = append(out, factView(f))
	}
	return out, nil
}

// facts rend ce qu'il faut écrire dans la consigne système.
//
// Une lecture qui échoue ne fait pas échouer la requête : elle la rend juste
// moins fine. Refuser de répondre à « je suis libre à 10h ? » parce que la
// fiche de Cyril n'a pas pu être relue serait une panne sans commune mesure
// avec ce qu'on perd — Raoul répond comme il répondait avant d'avoir une
// mémoire, c'est-à-dire correctement.
func (s *Server) facts(ctx context.Context, user *store.User) []assistant.FactView {
	all, err := s.store.Facts(ctx, user.ID)
	if err != nil {
		slog.Warn("lecture de la mémoire impossible", "err", err)
		return nil
	}
	out := make([]assistant.FactView, 0, len(all))
	for _, f := range all {
		out = append(out, factView(f))
	}
	return out
}

func factView(f store.Fact) assistant.FactView {
	return assistant.FactView{Kind: f.Kind, Subject: f.Subject, Content: f.Content}
}
