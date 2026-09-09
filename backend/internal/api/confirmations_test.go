package api

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// La boucle, telle qu'elle s'est produite. Reconstituée depuis les échanges en
// base du 7 septembre : six fois la même question, entrecoupées de « Oui »,
// « Ouais », « Oui je parle de ce groupe ». Le nom prononcé change à chaque
// tour, la conversation visée non — c'est sur elle qu'on compte.
func TestConfirmationAskedOnlyOnce(t *testing.T) {
	c := newConfirmations()
	user := bson.NewObjectID()
	const target = "whatsapp:120363@g.us"

	if c.askedOnce(user, target) {
		t.Fatal("la première lecture d'une conversation au nom incertain doit demander l'accord")
	}
	for i, spoken := range []string{"Ouais", "Oui je parle de ce groupe", "Oui", "Oui le groupe ASL"} {
		if !c.askedOnce(user, target) {
			t.Errorf("tour %d (%q) : la question est reposée alors qu'elle a déjà eu sa réponse", i+2, spoken)
		}
	}
}

// Viser une AUTRE conversation est une autre question : le garde-fou doit
// rejouer. C'est ce qui reste protégé — lire le mauvais groupe a exactement
// l'allure d'une vraie réponse.
func TestConfirmationPerTarget(t *testing.T) {
	c := newConfirmations()
	user := bson.NewObjectID()

	if c.askedOnce(user, "whatsapp:azul") {
		t.Fatal("première question sur Azul")
	}
	if c.askedOnce(user, "whatsapp:hifly") {
		t.Error("une autre conversation doit être confirmée pour elle-même")
	}
	if c.askedOnce(user, "slack:DM Xavier") {
		t.Error("un canal Slack ne partage pas la confirmation d'un groupe WhatsApp")
	}
}

// Deux utilisateurs ne se partagent pas une confirmation.
func TestConfirmationPerUser(t *testing.T) {
	c := newConfirmations()
	const target = "whatsapp:azul"

	if c.askedOnce(bson.NewObjectID(), target) {
		t.Fatal("première question")
	}
	if c.askedOnce(bson.NewObjectID(), target) {
		t.Error("la confirmation d'un utilisateur ne vaut pas pour un autre")
	}
}

// Rouvrir le sujet plus tard redemande l'accord : la trace n'est pas un
// blanc-seing définitif sur la conversation.
func TestConfirmationExpires(t *testing.T) {
	c := newConfirmations()
	user := bson.NewObjectID()
	const target = "whatsapp:azul"

	if c.askedOnce(user, target) {
		t.Fatal("première question")
	}
	c.mu.Lock()
	for k := range c.asked {
		c.asked[k] = time.Now().Add(-confirmationTTL - time.Minute)
	}
	c.mu.Unlock()

	if c.askedOnce(user, target) {
		t.Error("passé le délai, la question doit se reposer")
	}
}
