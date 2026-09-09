package api

import (
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Une question de confirmation ne se pose qu'une fois.
//
// « Tu parles du groupe Azul - PXCom Technical Group ? » a un sens la première
// fois. Elle en perd un à chaque répétition, et elle se répétait : les échanges
// en base en montrent six d'affilée, entrecoupés de « Oui », « Ouais », « Oui je
// parle de ce groupe ».
//
// La raison n'est pas le modèle, c'est que le garde-fou était sans mémoire. Il
// compare le nom prononcé au nom exact de la conversation ; tant que les deux
// diffèrent — « Azul PX Com Technical groupe » contre « Azul - PXCom Technical
// Group », un « e » d'écart — il redemande. Le « oui » de l'utilisateur ne
// change aucune des deux chaînes, donc l'appel suivant rejoue exactement la même
// comparaison et produit exactement la même question. La boucle est acquise :
// rien dans le tour d'après ne peut en sortir.
//
// D'où cette trace. Une question déjà posée sur une conversation ne se repose
// pas : si la demande suivante désigne la même, c'est que la réponse est venue —
// ou que l'utilisateur se répète, ce qui se traite pareil. On lit.
//
// Ce qui reste protégé est l'essentiel : la PREMIÈRE lecture d'une conversation
// dont le nom n'était pas sûr demande toujours l'accord. Viser une autre
// conversation pose une nouvelle question, puisque c'est une autre cible.
type confirmations struct {
	mu    sync.Mutex
	asked map[string]time.Time
}

// Durée de vie d'une question posée. Assez longue pour couvrir la réponse et
// les questions qui s'enchaînent sur la même conversation, assez courte pour que
// rouvrir le sujet le lendemain redemande l'accord.
const confirmationTTL = 10 * time.Minute

func newConfirmations() *confirmations {
	return &confirmations{asked: map[string]time.Time{}}
}

// askedOnce dit si la question a DÉJÀ été posée pour cette cible, et la note
// comme posée quand ce n'était pas le cas.
//
// target identifie la conversation, pas le nom prononcé : c'est tout l'intérêt.
// Le nom prononcé change à chaque tour — « Azul PX Com Technical groupe », puis
// « ce groupe », puis « oui » — là où la conversation visée, elle, ne bouge pas.
func (c *confirmations) askedOnce(userID bson.ObjectID, target string) bool {
	key := userID.Hex() + "\x00" + target

	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, at := range c.asked { // purge opportuniste
		if now.Sub(at) > confirmationTTL {
			delete(c.asked, k)
		}
	}
	if at, ok := c.asked[key]; ok && now.Sub(at) <= confirmationTTL {
		return true
	}
	c.asked[key] = now
	return false
}
