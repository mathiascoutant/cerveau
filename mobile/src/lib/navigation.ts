import { Linking } from 'react-native';

import { AssistantAction } from '../api';

/**
 * Ouverture de Waze pour l'action « navigate » décidée par Raoul.
 *
 * Le serveur ne calcule aucun itinéraire : il envoie une destination, Waze
 * fait le reste — trajet, trafic, temps de parcours affiché à l'écran. C'est
 * délibéré, ça évite une API de routage payante pour un travail que
 * l'application de navigation fait déjà mieux.
 *
 * Limite à connaître, elle vient d'iOS et non du code : une app tierce ne peut
 * pas en lancer une autre en arrière-plan. `openURL` passe forcément Waze au
 * premier plan, et l'appel échoue si Raoul n'est pas lui-même au premier plan.
 * Écran verrouillé, seul Siri sait faire.
 */

/**
 * Lien universel plutôt que schéma `waze://` : un schéma custom déclenche
 * l'alerte système « Raoul souhaite ouvrir Waze », qu'on ne peut pas désactiver.
 * Le lien universel, lui, bascule directement dans l'app quand elle est
 * installée — et retombe sur le site (avec sa bannière App Store) sinon.
 */
const WAZE_UNIVERSAL = 'https://waze.com/ul';

export async function openNavigation(action: AssistantAction): Promise<string> {
  const label = String(action.payload.label ?? '').trim();
  const address = String(action.payload.address ?? '').trim();

  // L'adresse retrouvée dans l'agenda prime : Waze part sur un point précis
  // plutôt que sur une recherche par nom d'entreprise, qui peut viser à côté.
  const destination = address || label;
  if (!destination) return "Destination vide : je n'ai rien pu lancer.";

  const q = encodeURIComponent(destination);
  try {
    await Linking.openURL(`${WAZE_UNIVERSAL}?q=${q}&navigate=yes`);
  } catch {
    // Waze absent de l'appareil, ou lien universel non associé : Plans est
    // toujours là, et son schéma maps.apple.com ne demande pas de confirmation.
    try {
      await Linking.openURL(`http://maps.apple.com/?daddr=${q}&dirflg=d`);
    } catch {
      return `Impossible d'ouvrir une application de navigation vers ${label || destination}.`;
    }
  }

  return address ? `Waze lancé vers ${label} — ${address}.` : `Waze lancé vers ${label}.`;
}
