# Rapport de refonte — Raoul

Branche : `claude/loving-newton-y46pjb` — deux commits (`backend`, puis `mobile`).

---

## 1. Ce qui a été livré

### Interface mobile, refaite de zéro

- Ancienne interface supprimée : `theme.ts`, `components/` (verre, Orb, TabBar, ui, AFaire, Urgences), écrans Assistant, Journal, Réponses, Accès.
- Nouveau système de design dans `mobile/src/design/` :
  - `tokens.ts` — palette « poste de pilotage » (fond bleu-noir, cyan = écoute / utilisateur, violet = réflexion, ambre = Raoul qui parle, vert / rouge = état), typographie Inter + mono pour les relevés, espacements, rayons, mouvement.
  - `ui.tsx` — primitives : texte, panneaux avec équerres, boutons, champs, pastilles, badges, notices, états vides, squelettes de chargement, en-têtes.
  - `Reactor.tsx` — le réacteur d'état animé (couronne graduée, cœur qui respire, anneaux), bouton micro.
  - `NavBar.tsx` — barre de navigation ancrée, quatre entrées.
- Quatre onglets : **Raoul** (conversation), **Jobs**, **Suivi**, **Accès**.
- Fonctions métier conservées : à faire, à traiter, point du jour, réponses de mail, connexions, agenda, widget « Parler à Raoul », intent Siri.

### Conversation rapide et en flux

- Backend : `POST /api/v1/assistant/stream` (Server-Sent Events). Événements : `status` (outil démarré / fini avec durée), `delta` (texte), `reset`, `done` (résultat + mesures + URL de la voix), `error`, `ping`.
- App : client `expo/fetch` (`mobile/src/lib/stream.ts`), affichage des outils ligne par ligne, texte mot à mot, mesures sous chaque réponse, bouton **stop** qui coupe le flux — donc le modèle et les outils côté serveur.
- Repli automatique sur `/assistant/ask` si le flux est indisponible.

### Routage des modèles (trois étages, sans appel supplémentaire)

`backend/internal/assistant/router.go` — classification déterministe :

| Étage | Variables | Défaut | Usage |
|---|---|---|---|
| rapide | `OPENAI_FAST_MODEL`, `OPENAI_FAST_EFFORT` | vide → modèle principal, **sans outils** | salutations, culture générale, résumé d'un texte fourni |
| outils | `OPENAI_MODEL`, `OPENAI_EFFORT` | `gpt-5.4-mini`, `low` | tout ce qui touche à ses données — **étage par défaut** |
| fort | `OPENAI_DEEP_MODEL`, `OPENAI_DEEP_EFFORT` | `gpt-5.4`, `medium` | plusieurs sources à croiser, « qui a demandé quoi à qui », débrief |

Recommandation : `OPENAI_FAST_MODEL=gpt-5.4-nano` (≈ 4× moins cher que mini, aucun risque puisque l'étage n'a pas d'outils).

### Réduction des tokens et de la latence

- Consigne système réordonnée : préfixe stable, contexte horaire et mémoire en fin — condition du cache de prompts OpenAI (`prompt_cache_key` = identifiant du compte).
- Historique borné en tours **et** en caractères (réponses longues tronquées).
- Outils d'un même tour exécutés en parallèle ; délai propre par outil (40 s) ; erreur explicite au modèle en cas de dépassement.
- Étage rapide : consigne courte, aucun outil envoyé.
- Mesures par réponse : étage, modèle, appels, tokens entrée / cache / sortie, temps avant premier mot, temps total, durée par outil. Visibles dans l'app et dans les journaux (`raoul tier=… ttft_ms=… total_ms=…`).

### Mails (Gandi IMAP)

- `mails_envoyes` — la boîte d'envoi, corps du plus récent (« le dernier mail que j'ai envoyé »).
- `chercher_mails` — par personne et objet, dans reçus, envoyés ou les deux, avec `message_id`, dossier, `de_toi`, `brouillon`, `pour_toi`, `reponse_a_ton_mail`.
- `lire_mail` accepte désormais `message_id` : ouverture exacte, jamais un homonyme.

### Slack

- Messages structurés : `auteur_id` (celui de l'API), `mentions` (vraies `@`, résolues), `te_cite`, `repond_dans_le_fil_de`, `lien` (permalien).
- `chercher_slack` — toutes les conversations, par auteur, personne citée, mots, canal, période (fils compris) ; annuaire avec levée d'ambiguïté sur les prénoms.
- Règles d'attribution dans la consigne (Thomas / Xavier, Marie / Paul) et tests unitaires.

### Tickets CSP (Tuleap)

- `backend/internal/providers/tuleap/` — client REST par clé d'accès personnelle, tickets d'un tracker avec sélection configurable, mise à plat (référence, titre, statut, priorité, responsable, auteur, dates, lien, description, autres champs), détail et commentaires.
- Routes : `GET /jobs`, `GET /jobs/csp/tickets`, `GET /jobs/csp/tickets/{id}`, `PUT /connections/tuleap`.
- Onglet **Jobs** : carte **CSP** (active si Tuleap est configuré et la clé saisie, sinon la raison affichée), carte **Flight Schedule** désactivée, sans faux écran.
- Vue CSP : recherche, filtres ouverts / tous / assignés à moi, relevé du périmètre, états de chargement, d'erreur et vide ; détail avec champs, historique, « Ouvrir dans Tuleap ».
- Outil `tickets_csp` pour Raoul.

### Documentation

- `README.md` : étages de modèles, Tuleap, description de l'app, tableau des outils et des routes.
- `backend/.env.example` : variables commentées.

---

## 2. Fichiers

**Créés**

- `backend/internal/assistant/router.go`, `router_test.go`, `stream.go`
- `backend/internal/providers/gandi/search.go`
- `backend/internal/providers/slack/search.go`, `attribution_test.go`
- `backend/internal/providers/tuleap/tuleap.go`, `tuleap_test.go`
- `backend/internal/api/handlers_stream.go`, `handlers_jobs.go`, `toolbox_search.go`, `toolbox_search_test.go`
- `mobile/src/design/tokens.ts`, `ui.tsx`, `Reactor.tsx`, `NavBar.tsx`
- `mobile/src/lib/stream.ts`
- `mobile/src/screens/RaoulScreen.tsx`, `JobsScreen.tsx`, `CSPScreen.tsx`, `SuiviScreen.tsx`, `AccesScreen.tsx`

**Modifiés**

- `backend/internal/assistant/assistant.go` (interface Toolbox, nouveaux outils, consigne), `assistant_test.go`
- `backend/internal/api/api.go`, `handlers_assistant.go`, `handlers_connections.go`, `toolbox.go`
- `backend/internal/config/config.go`, `backend/internal/store/models.go`
- `backend/internal/providers/slack/slack.go`, `read.go`
- `backend/.env.example`, `README.md`
- `mobile/App.tsx`, `mobile/src/api.ts`, `mobile/src/hooks/useRaoul.ts`

**Supprimés**

- `mobile/src/theme.ts`, `mobile/src/components/*` (6 fichiers), `mobile/src/screens/AssistantScreen.tsx`, `JournalScreen.tsx`, `DraftsScreen.tsx`, `ConnectionsScreen.tsx`

---

## 3. Variables d'environnement à configurer

```
OPENAI_MODEL=gpt-5.4-mini            # étage outils (défaut)
OPENAI_EFFORT=low
OPENAI_FAST_MODEL=gpt-5.4-nano       # étage rapide, sans outils (facultatif)
OPENAI_FAST_EFFORT=none
OPENAI_DEEP_MODEL=                   # vide = gpt-5.4 / medium
OPENAI_DEEP_EFFORT=

TULEAP_BASE_URL=https://tuleap.pxcom.aero
TULEAP_CSP_TRACKER_ID=               # identifiant numérique du tracker CSP
TULEAP_CSP_QUERY={"status":"open"}   # JSON du paramètre « query » de l'API REST
TULEAP_CSP_EXPERT_QUERY=             # ou TQL, prime si renseignée
TULEAP_CSP_ASSIGNED_TO_ME=false
```

La clé d'accès Tuleap se saisit dans l'app (Accès › Tuleap), jamais dans `.env`. Aucun secret n'apparaît dans le code, les journaux ou le frontend.

---

## 4. Tests exécutés

- `go test ./...` : 11 paquets verts, dont attribution auteur / mention Slack, annuaire et ambiguïté, Tuleap sur serveur factice (liste, filtre « assigné à moi », détail, clé refusée), routage (18 cas), budget d'historique, stabilité du préfixe de consigne, vues toolbox (reçu / envoyé / brouillon, attribution Slack, recherche de tickets).
- `go vet ./...`, `gofmt` : propres.
- `npx tsc --noEmit` : sans erreur.
- `npx expo export --platform ios` : bundle Hermes généré (2,2 Mo).

---

## 5. Ce qui n'a pas pu être fait ici, et pourquoi

| Point | Cause | Conséquence |
|---|---|---|
| Analyse de PXFeed-UI | `gitea.pxcom.aero` bloqué par le proxy réseau de l'environnement | La sélection exacte des tickets n'est pas reproduite : elle est à recopier dans `TULEAP_CSP_QUERY` / `TULEAP_CSP_EXPERT_QUERY` / `TULEAP_CSP_ASSIGNED_TO_ME` |
| Tests réels avec OpenAI, mesures de coût par modèle | `api.openai.com` bloqué, aucune clé dans l'environnement | Aucun chiffre de latence ou de tokens n'a été mesuré. Le dispositif de mesure est en place et produira les chiffres au premier déploiement |
| Tests contre Gandi, Slack, Tuleap réels | Aucune connexion disponible | Fournisseurs testés sur données contrôlées uniquement |
| Rendu visuel de l'app | Pas de simulateur iOS | Le bundle compile ; le rendu reste à vérifier sur appareil |
| Jarvis | Lu (README, HUD, panneau web), pas exécuté | Inspiration reprise : états colorés, relevés mono, lignes d'outils, réacteur |

---

## 6. Pour finaliser

1. Renseigner les variables Tuleap et, si souhaité, `OPENAI_FAST_MODEL` dans `.env` ; redémarrer le backend.
2. Saisir la clé Tuleap dans **Accès**, ouvrir **Jobs › CSP**, comparer la liste avec PXFeed-UI et ajuster la sélection.
3. Lancer un dev build et jouer les scénarios : dernier mail envoyé, mails de Hebat, demandes de Xavier, « qui a demandé à Xavier… », croisement ticket / Slack.
4. Lire les mesures sous chaque réponse et dans les journaux ; ajuster les modèles par étage selon les chiffres.
5. Ouvrir une pull request depuis `claude/loving-newton-y46pjb` si une relecture est souhaitée avant fusion.
