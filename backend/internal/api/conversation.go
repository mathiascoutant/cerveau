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
	// La fin d'un tour qui passe la parole n'est pas la fin de la réponse :
	// l'app ne doit voir que celle de l'agent qui répond vraiment.
	forward := func(ev assistant.Event) {
		if ev.Type == "done" && ev.Result != nil && ev.Result.Handoff != "" {
			return
		}
		emit(ev)
	}
	agent := s.interlocutor(ctx, user)
	visited := map[string]bool{agent: true}
	arrived := false
	var last assistant.Result
	for hop := 0; hop <= maxHops; hop++ {
		history := s.history(ctx, user, agent)
		var (
			res assistant.Result
			err error
		)
		// Celui qui vient de recevoir la parole sait pourquoi : sans ça, il
		// relit « passe-moi l'ingénieur » comme une demande à traiter, et
		// l'ingénieur rendait la parole à la responsable qui la lui
		// repassait, sans fin.
		ask := text
		if arrived {
			ask = arrivalNote(text)
		}
		if agent == "" {
			tb := s.toolbox(user)
			res, err = s.engine.AskStream(ctx, tb, s.request(ctx, user, ask, now, tz, history), forward)
			if err == nil {
				// Ce qu'il vient de dire d'un échange est ce qu'il en a compris.
				tb.closeTurn(context.WithoutCancel(ctx), res.Reply)
			}
		} else {
			res, err = s.askMember(ctx, user, agent, text, equipe.Demande{
				Text: ask, History: history, Now: now, Timezone: tz,
				UserName: s.userName(user), CacheKey: "raoul:" + user.ID.Hex(),
				SansPassage: arrived && hop == maxHops,
			}, forward)
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
		// Une parole qui revient à quelqu'un qui l'a déjà eue dans la même
		// demande, c'est une partie de ping-pong : on la laisse à celui qui
		// la tient, et il répond, sans pouvoir la repasser.
		if visited[next] && agent != "" {
			res, err = s.askMember(ctx, user, agent, text, equipe.Demande{
				Text: arrivalNote(text), History: history, Now: now, Timezone: tz,
				UserName: s.userName(user), CacheKey: "raoul:" + user.ID.Hex(), SansPassage: true,
			}, forward)
			if err == nil {
				s.store.GarderLaParole(context.WithoutCancel(ctx), user.ID)
			}
			return res, err
		}
		visited[next] = true
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
		arrived = true
	}
	last.Handoff = ""
	return last, errors.New("la parole a changé de main trop de fois pour une seule demande")
}

// askMember fait répondre un membre de l'équipe en conversation, et suit ce
// travail : le bâtiment de Cerveau HQ montre l'agent au travail tant qu'il
// l'est vraiment, et l'enquête qu'il confie à l'ingénieur aussi.
func (s *Server) askMember(ctx context.Context, user *store.User, agent, text string, d equipe.Demande, emit assistant.Emitter) (assistant.Result, error) {
	run := s.runs.begin(ctx, user.ID, agent, store.RunSourceConversation, text, nil)
	d.Suivi = s.runs.suiviPour(user.ID, run)
	res, err := s.equipe.Ask(ctx, agent, d, func(ev assistant.Event) {
		run.Etape(ev)
		emit(ev)
	})
	run.Fin(res, err)
	return res, err
}

// arrivalNote : la demande telle que la reçoit celui à qui l'on vient de
// passer la parole.
func arrivalNote(text string) string {
	return "[On vient de te passer la parole à sa demande. Sa phrase : « " + text + " ». " +
		"C'est à toi qu'il veut parler : tu ne repasses pas la parole. S'il ne demande rien d'autre que te parler, " +
		"tu te présentes en une phrase et tu lui demandes ce qu'il veut ; s'il a ajouté une question, tu y réponds.]"
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
