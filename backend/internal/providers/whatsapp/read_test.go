package whatsapp

import "testing"

func groups(names ...string) []Chat {
	out := make([]Chat, 0, len(names))
	for _, n := range names {
		out = append(out, Chat{JID: n + "@g.us", Name: n, IsGroup: true})
	}
	return out
}

// Le cas réel : il dit « le groupe azul », le groupe s'appelle
// « PXCom- Azul technique ». On le trouve.
func TestMatchChatFindsGroupFromOneWord(t *testing.T) {
	chats := groups("PXCom- Azul technique", "Famille", "Appart 3ème")
	got, tied, ok := MatchChat(chats, "le groupe azul")
	if !ok {
		t.Fatalf("non résolu (ambigu : %v)", tied)
	}
	if got.Name != "PXCom- Azul technique" {
		t.Errorf("groupe choisi : %q", got.Name)
	}
}

// Mais on ne lit rien avant d'avoir fait confirmer : « azul » n'est pas le nom
// du groupe, et un compte rendu du mauvais groupe a l'allure d'un vrai.
func TestMatchChatAsksBeforeReadingApproximateName(t *testing.T) {
	chat := groups("PXCom- Azul technique")[0]
	if !NeedsConfirmation("le groupe azul", chat) {
		t.Error("« azul » n'est pas le nom du groupe : il faut confirmer")
	}
}

// En revanche le nom exact ne se fait pas confirmer : redemander ce qu'il vient
// de dire mot pour mot est le tic qui rend un assistant pénible.
func TestNeedsConfirmationAcceptsExactName(t *testing.T) {
	chat := groups("PXCom- Azul technique")[0]
	for _, said := range []string{
		"PXCom- Azul technique",
		"pxcom azul technique",
		"le groupe PXCom Azul technique",
	} {
		if NeedsConfirmation(said, chat) {
			t.Errorf("%q est le nom du groupe, rien à confirmer", said)
		}
	}
}

// « le groupe Azul » quand le groupe s'appelle « Azul » : le mot qui annonce
// n'est pas une approximation.
func TestNeedsConfirmationIgnoresLeadIn(t *testing.T) {
	chat := groups("Azul")[0]
	if NeedsConfirmation("le groupe azul", chat) {
		t.Error("« le groupe Azul » désigne exactement le groupe Azul")
	}
}

// Deux groupes qui se valent : on demande lequel, on ne tranche pas.
func TestMatchChatAsksWhenTied(t *testing.T) {
	chats := groups("Azul technique", "Azul commercial")
	_, tied, ok := MatchChat(chats, "azul")
	if ok {
		t.Fatal("deux groupes également proches ne se départagent pas tout seuls")
	}
	if len(tied) != 2 {
		t.Errorf("choix proposés : %v", tied)
	}
}

// Un nom qui ne désigne rien ne se fait pas attribuer le moins éloigné.
func TestMatchChatRejectsUnrelated(t *testing.T) {
	chats := groups("PXCom- Azul technique", "Famille")
	if _, _, ok := MatchChat(chats, "comptabilité"); ok {
		t.Error("« comptabilité » ne désigne aucune de ces conversations")
	}
}

// Et on propose les plus proches : un « je ne trouve pas » tout seul laisse
// l'utilisateur répéter le même mot.
func TestNearestProposesSomething(t *testing.T) {
	chats := groups("PXCom- Azul technique", "Famille", "Appart 3ème")
	near := Nearest(chats, "azul tech", 2)
	if len(near) != 2 {
		t.Fatalf("propositions : %v", near)
	}
	if near[0] != "groupe PXCom- Azul technique" {
		t.Errorf("la plus proche devrait venir en tête, obtenu %q", near[0])
	}
}

// Un contact ne s'annonce pas comme un groupe.
func TestLabelDistinguishesGroupFromContact(t *testing.T) {
	if got := (Chat{Name: "Cyril"}).Label(); got != "Cyril" {
		t.Errorf("contact : %q", got)
	}
	if got := (Chat{Name: "Azul", IsGroup: true}).Label(); got != "groupe Azul" {
		t.Errorf("groupe : %q", got)
	}
}
