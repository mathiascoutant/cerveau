package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/equipe"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// La conversation, et qui y répond.
//
// Raoul est l'interlocuteur par défaut. Quand il passe la parole — « passe-moi
// l'ingénieur » —, c'est le membre de l'équipe qui répond à la même demande, et
// à la suivante, jusqu'à ce qu'il la rende. Les deux chemins de réponse, en
// flux et d'un bloc, passent par ici : c'est le seul endroit où se décide qui
// parle.

// maxHops : passages de parole dans une même demande. Au-delà, deux agents se
// renvoient la balle, et c'est un défaut de consigne, pas une conversation.
const maxHops = 3

// conversationTimeout : le temps laissé à une demande, équipe comprise. Une
// enquête de l'ingénieur croise journaux, bases et code ; la responsable qui
// la lui confie attend son rapport.
const conversationTimeout = 5 * time.Minute

// converse fait répondre l'interlocuteur courant, et suit les passages de
// parole. Le résultat porte l'agent qui a vraiment répondu.
func (s *Server) converse(ctx context.Context, user *store.User, text string, now time.Time, tz string, emit assistant.Emitter) (assistant.Result, error) {
	if emit == nil {
		emit = func(assistant.Event) {}
	}
	agent := s.interlocutor(ctx, user)
	var last assistant.Result
	for hop := 0; hop <= maxHops; hop++ {
		history := s.history(ctx, user, agent)
		var (
			res assistant.Result
			err error
		)
		if agent == "" {
			tb := s.toolbox(user)
			res, err = s.engine.AskStream(ctx, tb, s.request(ctx, user, text, now, tz, history), emit)
			if err == nil {
				// Ce qu'il vient de dire d'un échange est ce qu'il en a compris.
				tb.closeTurn(context.WithoutCancel(ctx), res.Reply)
			}
		} else {
			res, err = s.equipe.Ask(ctx, agent, equipe.Demande{
				Text: text, History: history, Now: now, Timezone: tz,
				UserName: s.userName(user), CacheKey: "raoul:" + user.ID.Hex(),
			}, emit)
		}
		if err != nil {
			return res, err
		}
		last = res
		if res.Handoff == "" {
			if agent != "" {
				s.store.GarderLaParole(context.WithoutCancel(ctx), user.ID)
			}
			return res, nil
		}

		next := res.Handoff
		if next == equipe.IDRaoul {
			next = ""
		}
		if err := s.store.PasserLaParole(context.WithoutCancel(ctx), user.ID, next); err != nil {
			return res, err
		}
		announced := next
		if announced == "" {
			announced = equipe.IDRaoul
		}
		m, _ := s.equipe.Membre(announced)
		emit(assistant.Event{Type: "interlocuteur", Agent: announced, Label: m.Nom})
		agent = next
	}
	last.Handoff = ""
	return last, errors.New("la parole a changé de main trop de fois pour une seule demande")
}

// interlocutor rend l'agent qui a la parole, "" pour Raoul.
func (s *Server) interlocutor(ctx context.Context, user *store.User) string {
	if s.equipe == nil {
		return ""
	}
	agent, err := s.store.Interlocuteur(ctx, user.ID)
	if err != nil || agent == "" || agent == equipe.IDRaoul {
		return ""
	}
	if _, ok := s.equipe.Membre(agent); !ok {
		return ""
	}
	return agent
}

// voiceOf rend la voix d'un agent ; vide pour Raoul, qui a celle du serveur.
func (s *Server) voiceOf(agent string) string {
	if s.equipe == nil || agent == "" || agent == equipe.IDRaoul {
		return ""
	}
	if m, ok := s.equipe.Membre(agent); ok {
		return m.Voix
	}
	return ""
}

func (s *Server) userName(user *store.User) string {
	if user.Name != "" {
		return user.Name
	}
	return s.cfg.DefaultUserName
}

// agentLabel : comment une réponse d'un autre agent est signée dans
// l'historique de celui qui parle.
func (s *Server) agentLabel(agent string) string {
	if agent == "" || agent == equipe.IDRaoul {
		return "Raoul"
	}
	if s.equipe != nil {
		if m, ok := s.equipe.Membre(agent); ok {
			return m.Nom + ", " + strings.ToLower(m.Role)
		}
	}
	return agent
}
