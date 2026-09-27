package slack

import (
	"context"
	"testing"
)

// Un client dont l'identité est déjà résolue, avec un annuaire figé : aucun
// appel réseau ne part, et les prénoms sont ceux qu'on a posés.
func attributionClient() *Client {
	c := New("xoxp-test")
	c.self, c.selfID, c.selfResolved = "mathias", "UME", true
	c.teamURL = "https://acme.slack.com"
	c.userNames["UTHOMAS"] = "Thomas"
	c.userNames["UXAVIER"] = "Xavier"
	c.userNames["UMARIE"] = "Marie"
	c.userNames["UPAUL"] = "Paul"
	return c
}

// Le cas qui motive tout le fichier : Thomas écrit « @Xavier, peux-tu regarder
// le problème CSP ? ». L'auteur est Thomas. Xavier est mentionné. Les deux
// informations doivent sortir séparément, et jamais se confondre.
func TestAttributionSeparatesAuthorFromMention(t *testing.T) {
	c := attributionClient()
	raw := rawMessage{
		User: "UTHOMAS",
		Text: "<@UXAVIER>, peux-tu regarder le problème CSP avant demain ?",
		TS:   "1727000000.000100",
	}
	msg, ok := c.toMessage(context.Background(), raw, 3000, "C123", "#csp")
	if !ok {
		t.Fatal("message écarté")
	}
	if msg.AuteurID != "UTHOMAS" || msg.Auteur != "Thomas" {
		t.Errorf("auteur : %q (%s), attendu Thomas (UTHOMAS)", msg.Auteur, msg.AuteurID)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0].ID != "UXAVIER" || msg.Mentions[0].Nom != "Xavier" {
		t.Errorf("mentions : %+v, attendu Xavier seul", msg.Mentions)
	}
	if msg.TeCite {
		t.Error("l'utilisateur n'est pas cité dans ce message")
	}
	if msg.Texte != "@Xavier, peux-tu regarder le problème CSP avant demain ?" {
		t.Errorf("texte rendu : %q", msg.Texte)
	}
	if msg.Lien != "https://acme.slack.com/archives/C123/p1727000000000100" {
		t.Errorf("permalien : %q", msg.Lien)
	}
	if msg.ID != raw.TS || msg.Canal != "#csp" {
		t.Errorf("identité du message : id=%q canal=%q", msg.ID, msg.Canal)
	}
}

// Un prénom écrit en toutes lettres n'est pas une mention : « J'ai vu que Paul
// avait corrigé le problème » ne cite personne au sens de Slack. C'est Marie
// qui l'écrit, et c'est Marie qui rapporte — Paul n'est qu'un mot du texte.
func TestAttributionIgnoresPlainNames(t *testing.T) {
	c := attributionClient()
	msg, ok := c.toMessage(context.Background(), rawMessage{
		User: "UMARIE",
		Text: "J'ai vu que Paul avait corrigé le problème hier.",
		TS:   "1727000001.000200",
	}, 3000, "C123", "#csp")
	if !ok {
		t.Fatal("message écarté")
	}
	if msg.Auteur != "Marie" || msg.AuteurID != "UMARIE" {
		t.Errorf("auteur : %q", msg.Auteur)
	}
	if len(msg.Mentions) != 0 {
		t.Errorf("aucune mention structurée attendue, obtenu %+v", msg.Mentions)
	}
}

// Ses propres messages sont marqués de_toi, et une mention de lui-même se
// rend « toi » — avec te_cite levé — pour que rien ne le confonde avec un
// collègue.
func TestAttributionMarksSelf(t *testing.T) {
	c := attributionClient()
	mine, _ := c.toMessage(context.Background(), rawMessage{
		User: "UME", Text: "Dites-moi si c'est bloquant, pour <@UXAVIER>", TS: "1.1",
	}, 3000, "C1", "#dev")
	if !mine.DeToi || mine.Auteur != "toi" {
		t.Errorf("son propre message : de_toi=%v auteur=%q", mine.DeToi, mine.Auteur)
	}
	cited, _ := c.toMessage(context.Background(), rawMessage{
		User: "UTHOMAS", Text: "<@UME> tu peux valider ?", TS: "1.2",
	}, 3000, "C1", "#dev")
	if !cited.TeCite || len(cited.Mentions) != 1 || cited.Mentions[0].Nom != "toi" {
		t.Errorf("mention de lui-même : te_cite=%v mentions=%+v", cited.TeCite, cited.Mentions)
	}
}

// Une réponse dans un fil garde le lien vers son parent : c'est ce qui permet
// de dire à quoi elle répond, plutôt que de la rattacher au message voisin.
func TestAttributionKeepsThreadParent(t *testing.T) {
	c := attributionClient()
	reply, _ := c.toMessage(context.Background(), rawMessage{
		User: "UXAVIER", Text: "Je regarde ça ce soir", TS: "1727000050.000300", ThreadTS: "1727000000.000100",
	}, 3000, "C123", "#csp")
	if reply.FilDe != "1727000000.000100" {
		t.Errorf("parent du fil : %q", reply.FilDe)
	}
	root, _ := c.toMessage(context.Background(), rawMessage{
		User: "UTHOMAS", Text: "Racine", TS: "1727000000.000100", ThreadTS: "1727000000.000100",
	}, 3000, "C123", "#csp")
	if root.FilDe != "" {
		t.Errorf("une racine de fil n'a pas de parent, obtenu %q", root.FilDe)
	}
}

// La résolution d'une personne par son prénom, sur un annuaire figé : un seul
// Xavier se trouve, deux Xavier font poser la question.
func TestFindUserResolvesAndRefusesTies(t *testing.T) {
	c := attributionClient()
	c.directoryLoaded = true
	c.directory = []directoryUser{
		{ID: "UXAVIER", RealName: "Xavier Martin", Handle: "xavier.martin"},
		{ID: "UTHOMAS", RealName: "Thomas Durand", Handle: "thomas"},
	}
	id, name, err := c.FindUser(context.Background(), "xavier")
	if err != nil || id != "UXAVIER" || name != "Xavier Martin" {
		t.Errorf("xavier → %q %q %v", id, name, err)
	}
	if id, _, err := c.FindUser(context.Background(), "moi"); err != nil || id != "UME" {
		t.Errorf("moi → %q %v", id, err)
	}

	c.directory = append(c.directory, directoryUser{ID: "UXAVIER2", RealName: "Xavier Petit", Handle: "xpetit"})
	_, _, err = c.FindUser(context.Background(), "xavier")
	var amb *AmbiguousUserError
	if !errorsAs(err, &amb) || len(amb.Choices) != 2 {
		t.Errorf("deux Xavier doivent faire lever une ambiguïté, obtenu %v", err)
	}
}

func errorsAs(err error, target **AmbiguousUserError) bool {
	e, ok := err.(*AmbiguousUserError)
	if ok {
		*target = e
	}
	return ok
}
