package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mathiascoutant/cerveau/backend/internal/assistant"
	"github.com/mathiascoutant/cerveau/backend/internal/fuzzy"
	"github.com/mathiascoutant/cerveau/backend/internal/store"
)

// Les urgences vues depuis l'assistant.
//
// La liste affichée dans l'app et celle que Raoul commente à voix haute sont la
// MÊME : mêmes tâches, mêmes identifiants, même ordre. C'est la condition pour
// que « on part sur le premier » désigne quelque chose — une conversation qui
// parle d'une liste que l'écran ne montre pas oblige à traduire en permanence,
// et c'est là que tout se décale.
//
// Trois gestes, et rien d'autre : faire le point, entrer dans une ligne, la
// retirer. Chacun rend un résultat au modèle ET une instruction à l'app, parce
// qu'un point sur les urgences qui ne change rien à l'écran laisse l'utilisateur
// chercher de quoi on lui parle.

// Profondeur de lecture quand on entre dans une urgence.
//
// Généreuse à dessein : on n'est plus dans la liste, on est dans UN sujet, et
// la question à laquelle il faut répondre — « qu'est-ce que je dois savoir ? »
// — n'a pas de réponse sur un dernier message sorti de son fil.
const (
	urgentThreadDepth = 40
)

// Urgences rend la liste à traiter et demande à l'app de l'afficher.
func (t *userToolbox) Urgences(ctx context.Context) (assistant.UrgentListView, store.Action, error) {
	list, err := t.srv.urgentList(ctx, t.user, false)
	if err != nil {
		return assistant.UrgentListView{}, store.Action{}, err
	}

	out := assistant.UrgentListView{
		Sources:       list.Sources,
		Indisponibles: list.Unavailable,
	}
	ids := make([]string, 0, len(list.Taches))
	for i, task := range list.Taches {
		out.Taches = append(out.Taches, assistant.UrgentEntryView{
			// Le rang est ce par quoi il les désigne à l'oral — « le premier »,
			// « le deuxième ». Le donner au modèle lui évite de le recompter,
			// et un modèle qui recompte se trompe un jour d'un cran.
			Rang:     i + 1,
			ID:       task.ID,
			Action:   task.Action,
			Urgence:  task.Urgence,
			Pourquoi: task.Pourquoi,
			Sources:  task.Sources,
		})
		ids = append(ids, task.ID)
	}
	return out, urgentAction("liste", "", "", ids), nil
}

// OuvrirUrgence désigne une urgence à l'écran et rapatrie de quoi la comprendre.
//
// Le retour ne se contente pas de recopier la ligne : il descend le message
// lui-même — le corps du mail et son fil, la conversation Slack, le groupe
// WhatsApp. C'est tout l'intérêt du geste. Une ligne de liste dit qu'il y a
// quelque chose à faire ; ce qu'il faut savoir pour le faire est dans le
// message, et l'aller chercher est le travail qu'on lui épargne.
func (t *userToolbox) OuvrirUrgence(ctx context.Context, ref string) (assistant.UrgentDetailView, store.Action, error) {
	list, err := t.srv.urgentList(ctx, t.user, false)
	if err != nil {
		return assistant.UrgentDetailView{}, store.Action{}, err
	}
	i, err := pickUrgent(list.Taches, ref)
	if err != nil {
		return assistant.UrgentDetailView{}, store.Action{}, err
	}
	task := list.Taches[i]

	out := assistant.UrgentDetailView{
		ID:       task.ID,
		Rang:     i + 1,
		Total:    len(list.Taches),
		Action:   task.Action,
		Urgence:  task.Urgence,
		Pourquoi: task.Pourquoi,
		Sources:  task.Sources,
	}
	t.fill(ctx, &out, task.Sources)
	return out, urgentAction("focus", task.ID, task.Action, nil), nil
}

// fill descend le contenu derrière la première source de la tâche.
//
// Une seule source est lue, même quand la tâche en regroupe trois : elles
// parlent du même sujet par construction, et la plus récente porte l'état de
// l'échange. Lire les trois tripleraient le coût pour répéter la même chose.
//
// Un contenu introuvable n'est pas une erreur : la ligne reste sélectionnée et
// le « pourquoi » reste lisible. On le dit dans Note plutôt que de faire échouer
// le geste — le mail a pu être lu et archivé entre la génération et maintenant.
func (t *userToolbox) fill(ctx context.Context, out *assistant.UrgentDetailView, sources []assistant.SourceView) {
	if len(sources) == 0 {
		return
	}
	src := sources[0]

	if strings.EqualFold(strings.TrimSpace(src.Origine), "mail") {
		// La recherche part de l'objet et non de l'expéditeur : deux mails du
		// même correspondant peuvent porter deux sujets, et c'est le sujet qui
		// a fait la tâche.
		mail, err := t.ReadEmail(ctx, src.Titre, false)
		if err != nil && src.De != "" {
			mail, err = t.ReadEmail(ctx, src.De, false)
		}
		if err != nil {
			out.Note = "Le mail n'a pas pu être rouvert : " + err.Error()
			return
		}
		out.Mail = &mail
		return
	}

	if name, ok := whatsAppOrigin(src.Origine); ok {
		chat, err := t.ReadWhatsAppChat(ctx, name, urgentThreadDepth, false)
		if err != nil {
			out.Note = "La conversation WhatsApp n'a pas pu être relue : " + err.Error()
			return
		}
		out.WhatsApp = &chat
		return
	}

	canal, err := t.ReadSlackChannel(ctx, src.Origine, urgentThreadDepth)
	if err != nil {
		out.Note = "La conversation Slack n'a pas pu être relue : " + err.Error()
		return
	}
	out.Conversation = &canal
}

// UrgenceTraitee retire une urgence de la liste, à l'écran et en base.
//
// L'effacement est définitif du point de vue de la liste : elle ne réapparaîtra
// pas à la régénération suivante même si le message est toujours non lu. C'est
// voulu — répondre à un mail ne le marque pas lu, et une liste qui ressuscite ce
// qu'on vient d'y traiter cesse d'être crue au bout de deux fois.
func (t *userToolbox) UrgenceTraitee(ctx context.Context, ref string) (assistant.UrgentDoneView, store.Action, error) {
	list, err := t.srv.urgentList(ctx, t.user, false)
	if err != nil {
		return assistant.UrgentDoneView{}, store.Action{}, err
	}
	i, err := pickUrgent(list.Taches, ref)
	if err != nil {
		return assistant.UrgentDoneView{}, store.Action{}, err
	}
	task := list.Taches[i]

	if err := t.srv.store.DismissUrgent(ctx, t.user.ID, task.ID, task.Action); err != nil {
		return assistant.UrgentDoneView{}, store.Action{}, err
	}

	out := assistant.UrgentDoneView{Traitee: task.Action, Restantes: len(list.Taches) - 1}
	// Ce qui vient après est ce qui va le plus l'intéresser : la seule question
	// qui se pose une fois une ligne traitée est « et ensuite ? ». La donner ici
	// évite un aller-retour d'outil pour l'apprendre.
	if i+1 < len(list.Taches) {
		out.Suivante = list.Taches[i+1].Action
	}
	return out, urgentAction("traite", task.ID, task.Action, nil), nil
}

// pickUrgent retrouve la tâche visée par ce qu'il a dit.
//
// Deux façons de désigner, et elles cohabitent parce qu'il use des deux dans la
// même conversation : le rang (« le premier », « le 2 ») et le contenu (« le
// mail de Cyril », « le devis »). Le rang est tenté d'abord — il est sans
// ambiguïté possible, alors qu'un prénom peut en désigner deux.
func pickUrgent(tasks []assistant.TaskView, ref string) (int, error) {
	if len(tasks) == 0 {
		return 0, errors.New("il n'y a rien à traiter en ce moment")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if len(tasks) == 1 {
			return 0, nil
		}
		return 0, errors.New("précise laquelle : donne son rang, ou de qui elle vient")
	}

	// L'identifiant exact : c'est ce que l'app repasse quand le geste vient de
	// l'écran plutôt que de la voix.
	for i, task := range tasks {
		if task.ID == ref {
			return i, nil
		}
	}

	if rank, ok := ordinal(ref); ok {
		// Le rang négatif compte depuis la fin : « le dernier » est le seul
		// ordinal qu'on emploie sans savoir combien il y en a.
		if rank < 0 {
			rank = len(tasks) + 1 + rank
		}
		if rank < 1 || rank > len(tasks) {
			return 0, fmt.Errorf("il n'y a que %d ligne(s) à traiter", len(tasks))
		}
		return rank - 1, nil
	}

	names := make([][]string, 0, len(tasks))
	for _, task := range tasks {
		candidate := []string{task.Action}
		for _, src := range task.Sources {
			candidate = append(candidate, src.De, src.Titre)
			// L'origine n'est un nom que pour une conversation : « #projet »
			// se dit, « mail » non. L'y laisser ferait correspondre « le mail
			// de Cyril » à tous les mails de la liste, et le départage se
			// jouerait sur le mot le moins discriminant de la phrase.
			if !strings.EqualFold(strings.TrimSpace(src.Origine), "mail") {
				candidate = append(candidate, src.Origine)
			}
		}
		names = append(names, candidate)
	}
	winner, tied := fuzzy.Resolve(ref, names)
	if winner >= 0 {
		return winner, nil
	}
	if len(tied) > 0 {
		choices := make([]string, 0, len(tied))
		for _, i := range tied {
			choices = append(choices, tasks[i].Action)
		}
		return 0, &assistant.AmbiguousError{Quoi: "urgence", Recherche: ref, Choix: choices}
	}
	return 0, fmt.Errorf("aucune ligne à traiter ne correspond à « %s »", ref)
}

// Les mots par lesquels on désigne un rang à l'oral. On s'arrête à cinq : la
// liste n'en contient jamais davantage, et « le septième » ne se dit pas.
var ordinals = map[string]int{
	"1": 1, "premier": 1, "première": 1, "premiere": 1, "1er": 1, "1ere": 1, "1ère": 1,
	"2": 2, "deuxième": 2, "deuxieme": 2, "second": 2, "seconde": 2, "2e": 2, "2eme": 2, "2ème": 2,
	"3": 3, "troisième": 3, "troisieme": 3, "3e": 3, "3eme": 3, "3ème": 3,
	"4": 4, "quatrième": 4, "quatrieme": 4, "4e": 4, "4eme": 4, "4ème": 4,
	"5": 5, "cinquième": 5, "cinquieme": 5, "5e": 5, "5eme": 5, "5ème": 5,
	"dernier": -1, "dernière": -1, "derniere": -1,
}

// ordinal lit un rang dans ce qu'il a dit. On ne scanne que des mots entiers :
// « le devis 2024 » contient un chiffre sans désigner un rang, et le prendre
// pour tel ouvrirait la mauvaise ligne sans que rien ne le signale.
func ordinal(ref string) (int, bool) {
	fields := strings.FieldsFunc(strings.ToLower(ref), func(r rune) bool {
		return r == ' ' || r == '\'' || r == '’' || r == ',' || r == '.'
	})
	for _, word := range fields {
		if n, ok := ordinals[word]; ok {
			return n, true
		}
	}
	return 0, false
}

// whatsAppOrigin reconnaît une origine WhatsApp et rend le nom de la
// conversation. Le suffixe est posé par origin() côté liste : un groupe
// WhatsApp et un canal Slack portent des noms de même allure, et sans lui on ne
// saurait pas dans quelle messagerie aller lire.
func whatsAppOrigin(origine string) (string, bool) {
	const suffix = " (WhatsApp)"
	if strings.HasSuffix(origine, suffix) {
		return strings.TrimSuffix(origine, suffix), true
	}
	return "", false
}

// urgentAction est l'instruction envoyée à l'app avec la réponse.
//
// Elle existe parce qu'une conversation sur une liste que l'écran ne suit pas
// oblige à traduire en permanence : « le premier » ne veut rien dire si l'app
// affiche un autre ordre, et « je l'ai traité » ne se voit nulle part. L'app
// applique le même geste que la voix, au même instant.
func urgentAction(mode, id, action string, ids []string) store.Action {
	payload := map[string]any{"mode": mode}
	if id != "" {
		payload["id"] = id
	}
	if action != "" {
		payload["action"] = action
	}
	if ids != nil {
		payload["ids"] = ids
	}
	return store.Action{Type: "urgent", Payload: payload}
}
