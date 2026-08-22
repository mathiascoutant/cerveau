package gandi

import (
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// describeSender rend le candidat prononçable : c'est ce que Raoul lira à voix
// haute pour demander duquel on parle.
func TestDescribeSender(t *testing.T) {
	cases := []struct {
		msg  Message
		want string
	}{
		{Message{From: "Cyril Martin", FromAddr: "cyril.martin@exemple.fr"}, "Cyril Martin (cyril.martin@exemple.fr)"},
		{Message{From: "cyril@exemple.fr", FromAddr: "cyril@exemple.fr"}, "cyril@exemple.fr"},
		{Message{From: "Cyril Martin"}, "Cyril Martin"},
		{Message{FromAddr: "cyril@exemple.fr"}, "cyril@exemple.fr"},
		{Message{}, "expéditeur inconnu"},
	}
	for _, c := range cases {
		if got := describeSender(c.msg); got != c.want {
			t.Errorf("%+v : %q, attendu %q", c.msg, got, c.want)
		}
	}
}

// Deux personnes ne se distinguent que par leur adresse : « Cyril » et
// « Cyril » doivent compter pour deux, pas pour un.
func TestSenderKeySeparatesHomonyms(t *testing.T) {
	a := Message{From: "Cyril", FromAddr: "cyril@orange.fr"}
	b := Message{From: "Cyril", FromAddr: "cyril@compta.fr"}
	if senderKey(a) == senderKey(b) {
		t.Error("deux adresses différentes doivent donner deux clés différentes")
	}

	// Même personne, casse différente dans l'en-tête.
	c := Message{From: "CYRIL", FromAddr: "Cyril@Orange.fr"}
	if senderKey(a) != senderKey(c) {
		t.Error("la même adresse doit donner la même clé quelle que soit la casse")
	}
}

// L'erreur d'ambiguïté doit nommer les candidats : c'est elle qui devient la
// question posée à l'utilisateur.
func TestAmbiguousSenderError(t *testing.T) {
	err := error(&AmbiguousSenderError{
		Query:   "cyril",
		Senders: []string{"Cyril Martin (cyril@orange.fr)", "Cyril Dubois (c.dubois@compta.fr)"},
	})

	var amb *AmbiguousSenderError
	if !errors.As(err, &amb) {
		t.Fatal("le type doit être reconnaissable par errors.As")
	}
	if !strings.Contains(err.Error(), "Cyril Martin") || !strings.Contains(err.Error(), "Cyril Dubois") {
		t.Errorf("les deux candidats doivent apparaître : %s", err)
	}
}

// addressList sert à répondre juste : reconnaître l'utilisateur parmi les
// destinataires, et trouver le prénom à saluer.
func TestAddressList(t *testing.T) {
	got := addressList([]imap.Address{
		{Name: "Cyril Martin", Mailbox: "cyril", Host: "exemple.fr"},
		{Mailbox: "contact", Host: "societe.fr"},
		{Name: "contact@societe.fr", Mailbox: "contact", Host: "societe.fr"},
	})
	want := []string{
		"Cyril Martin <cyril@exemple.fr>",
		"contact@societe.fr",
		// Un nom affiché identique à l'adresse ne se répète pas.
		"contact@societe.fr",
	}
	if len(got) != len(want) {
		t.Fatalf("%d adresses, attendu %d : %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("adresse %d : %q, attendu %q", i, got[i], want[i])
		}
	}

	if len(addressList(nil)) != 0 {
		t.Error("une liste vide doit rendre une liste vide")
	}
}
