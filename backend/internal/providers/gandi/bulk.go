package gandi

import (
	"bufio"
	"bytes"
	"strings"
)

// En-têtes qui trahissent un envoi de masse ou automatique. Les demander coûte
// zéro aller-retour de plus : ils voyagent dans le même FETCH que l'enveloppe.
//
// Ils valent mieux que n'importe quelle liste de mots-clés parce qu'ils sont
// posés par l'expéditeur lui-même, et normalisés :
//
//	List-Id / List-Unsubscribe — le message vient d'une liste de diffusion
//	                             (RFC 2919, RFC 2369) ;
//	Precedence: bulk|list|junk — convention historique du courrier de masse ;
//	Auto-Submitted             — réponse ou notification produite par une
//	                             machine (RFC 3834).
var bulkHeaders = []string{"List-Id", "List-Unsubscribe", "Precedence", "Auto-Submitted"}

// isBulk lit le bloc d'en-têtes rendu par le serveur.
//
// L'analyse reste volontairement littérale : on cherche la présence d'un
// en-tête, pas à interpréter sa valeur. Un `Auto-Submitted: no` est le seul cas
// où la valeur compte — c'est la façon normalisée de dire « c'est bien un
// humain qui écrit ».
func isBulk(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			// Ligne de continuation d'un en-tête replié : rien à y décider.
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "list-id", "list-unsubscribe":
			return true
		case "precedence":
			if value == "bulk" || value == "list" || value == "junk" {
				return true
			}
		case "auto-submitted":
			if value != "" && value != "no" {
				return true
			}
		}
	}
	return false
}
