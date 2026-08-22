# Cerveau — Raoul

Assistant vocal personnel qui croise **agenda + mails Gandi + Slack + WhatsApp Business**
pour répondre à des questions du type :

> « OK Raoul, je peux aller faire du sport à 10h demain ? »

Il consulte les quatre sources, tranche, et pose l'événement dans le calendrier si c'est jouable.

- `backend/` — API Go, MongoDB, orchestration OpenAI (à héberger sur le VPS OVH)
- `mobile/` — app React Native / Expo (iOS, dev build puis TestFlight)

Pas de login, pas de mot de passe, pas d'inscription : l'app génère un identifiant
d'appareil au premier lancement, le garde dans le Keychain, et le serveur émet un
token permanent. On arrive directement sur l'assistant.

---

## Ce dont j'ai besoin de toi

Sept valeurs à récupérer, une huitième si tu veux une belle voix. Le reste est
déjà câblé.

### 1. OpenAI — `OPENAI_API_KEY` (obligatoire)

1. <https://platform.openai.com/api-keys>
2. **Create new secret key**
3. Copie la clé `sk-…` — elle n'est affichée **qu'une fois**.

⚠️ C'est bien la clé de la **plateforme API** (`platform.openai.com`), pas ton
abonnement ChatGPT Plus : ce sont deux produits et deux facturations distinctes.
Un abonnement Plus ne donne aucun accès à l'API. Vérifie ton crédit dans
**Settings › Billing**.

Modèle par défaut : `gpt-5.4-mini` — rapide et économique, et il tient
l'enchaînement d'outils. `gpt-5.4-nano` coûte moins cher mais cale sur le
raisonnement multi-étapes que demande la vérification de créneau. Réglable via
`OPENAI_MODEL`, et `OPENAI_EFFORT=low` privilégie la latence, ce qui compte en vocal.

### 2. Clé de chiffrement — `MASTER_KEY` (obligatoire)

Rien à aller chercher, tu la génères :

```bash
openssl rand -hex 32
```

Elle chiffre les mots de passe et tokens des connexions avant écriture en base.
**À générer une seule fois** : si tu la changes, toutes les connexions sont à refaire.

### 3. Mails Gandi — se saisit **dans l'app**

Gandi ne propose **pas d'OAuth** pour le mail, donc c'est de l'IMAP classique.

1. <https://admin.gandi.net> › **Boîtes mail** (ou ton domaine › *Boîte mail*)
2. Clique sur ta boîte mail
3. Cherche **Mots de passe d'application** (ou *Sécurité*) et crée-en un nommé « Raoul »

Si ta boîte ne propose pas cette option, le **mot de passe normal de la boîte mail
fonctionne aussi** en IMAP — c'est juste moins propre, puisqu'il n'est pas révocable
indépendamment.

Le serveur est pré-rempli (`mail.gandi.net:993`), tu n'as que l'adresse et le mot de
passe à saisir dans l'onglet **Accès** de l'app.

**Teste tes identifiants avant** — ça évite de chercher la panne à travers toute la
stack (ni MongoDB, ni OpenAI, ni serveur HTTP ne sont sollicités) :

```bash
cd backend && make gandi-check EMAIL=moi@mondomaine.fr
```

Le mot de passe est demandé en saisie masquée, donc il ne finit pas dans
l'historique du shell. En cas de succès, la commande affiche le nombre de non-lus
et la liste exacte que Raoul verra.

### 4. Slack — deux valeurs dans `.env`, puis un clic dans l'app

Le plus simple est le flux OAuth : tu cliques « Autoriser avec Slack » dans
l'app, tu valides côté Slack, et le token revient chiffré en base sans jamais
passer par un copier-coller.

1. <https://api.slack.com/apps> › ton app › **Basic Information › App Credentials**
   → récupère le **Client ID** et le **Client Secret**, à mettre dans le `.env`
   du VPS (`SLACK_CLIENT_ID`, `SLACK_CLIENT_SECRET`).
2. **OAuth & Permissions › Redirect URLs** → ajoute exactement :

```
https://cerveau.neurorun.fr/oauth/slack/callback
```

   Sans ça l'échange échoue en `bad_redirect_uri`. L'URL doit correspondre au
   caractère près à `PUBLIC_BASE_URL` + `/oauth/slack/callback`.
3. **OAuth & Permissions › Portées des jetons d'utilisateur** (⚠️ *utilisateur*,
   pas *bot*) → les 9 scopes :

```
channels:history  channels:read
groups:history    groups:read
im:history        im:read
mpim:history      mpim:read
users:read
```

4. Redémarre le conteneur (`docker compose up -d`), puis dans l'app :
   **Accès › Slack › Autoriser avec Slack**.

> Pourquoi des scopes utilisateur : seul un token `xoxp` expose `last_read` /
> `unread_count_display`, c'est-à-dire ce que **toi** tu n'as pas lu. Un bot token
> ne sait pas ça.

> Si ton espace de travail exige l'approbation d'un administrateur pour installer
> des applications, le flux OAuth ne la contourne pas : l'écran de consentement
> proposera une demande d'approbation. Un admin de l'espace peut s'auto-approuver
> depuis `/apps/manage/requests`.

L'ancienne méthode reste disponible — « Coller un token à la main » dans l'app,
si tu as déjà un `xoxp-` sous la main.

### 5. WhatsApp Business — 4 valeurs, 3 endroits différents

C'est de loin le plus pénible. Dans l'ordre :

**a) Créer l'app** — <https://developers.facebook.com/apps> › *Créer une app* ›
type **Business** › ajoute le produit **WhatsApp**.

**b) Phone number ID** *(dans l'app Raoul)*
App › **WhatsApp › API Setup**. Le `Phone number ID` est affiché sous le numéro,
c'est un long nombre — ce n'est **pas** le numéro de téléphone.

**c) Token d'accès permanent** *(dans l'app Raoul)*
Celui affiché sur la page API Setup expire en 24 h. Pour un permanent :
<https://business.facebook.com/settings> › **Utilisateurs › Utilisateurs système** ›
*Ajouter* (rôle Admin) › **Générer un nouveau token** › choisis ton app ›
coche `whatsapp_business_messaging` **et** `whatsapp_business_management` ›
expiration **Jamais** › copie.

**d) `WHATSAPP_APP_SECRET`** *(dans `.env`)*
developers.facebook.com › ton app › **Paramètres › Général › Clé secrète › Afficher**.

**e) `WHATSAPP_VERIFY_TOKEN`** *(dans `.env`)*
Personne ne te la donne, **tu l'inventes** : `openssl rand -hex 16`. Elle sert
uniquement à ce que Meta et ton serveur se reconnaissent au moment du branchement.

**f) Brancher le webhook** — App › WhatsApp › **Configuration › Webhook › Modifier** :

- URL de rappel : `https://ton-domaine/webhooks/whatsapp`
- Jeton de vérification : la valeur de `WHATSAPP_VERIFY_TOKEN`
- Vérifier et enregistrer, puis **Gérer** › cocher le champ **messages**

Ton backend doit déjà tourner en HTTPS à ce moment-là : Meta appelle l'URL
immédiatement pour la valider, et refuse le HTTP simple.

> Le numéro de test fourni par Meta ne peut échanger qu'avec des numéros que tu as
> déclarés. Pour recevoir tes vrais messages, il faut enregistrer ton vrai numéro
> WhatsApp Business dans l'app Meta.

> ⚠️ Limite réelle de l'API officielle : elle ne donne **aucun historique** et aucune
> notion de « non lu ». Raoul ne voit que les messages arrivés **après** le
> branchement du webhook, et tient lui-même le statut lu/non lu. C'est le prix de
> l'API officielle — la seule alternative serait un client WhatsApp Web non officiel,
> qui te ferait risquer le bannissement du numéro.

### 6. Calendrier

**Rien à fournir.** L'app lit le calendrier iOS via EventKit et pousse le miroir au
serveur. Ça couvre d'un coup tous les comptes agrégés sur l'iPhone — iCloud, Google,
Exchange — pro comme perso, sans un seul OAuth.

### 7. MongoDB

Une URI de connexion Atlas, dans `.env` :

```
MONGO_URI=mongodb+srv://<utilisateur>:<motdepasse>@<cluster>.mongodb.net/?appName=cerveau
MONGO_DB=cerveau
```

> ⚠️ N'utilise pas un couple devinable comme `admin/admin` : un cluster Atlas est
> joignable depuis Internet, et ces identifiants-là se trouvent en quelques secondes.
> Crée un utilisateur dédié avec un vrai mot de passe, et restreins l'accès réseau
> (Network Access) à l'IP du VPS plutôt que `0.0.0.0/0`.

### 8. Voix de Raoul — `ELEVENLABS_API_KEY` (optionnel, mais ça s'entend)

La synthèse vocale d'iOS pose chaque mot au même rythme : intelligible, mais on
entend la machine. ElevenLabs rend une voix qui respire.

1. <https://elevenlabs.io> → **Profile → API keys**
2. La clé n'a besoin que de la permission **`text_to_speech`**
3. Dans `.env` :

```
ELEVENLABS_API_KEY=sk_…
ELEVENLABS_VOICE_ID=      # vide = « Eric », posé et sans aspérité
ELEVENLABS_MODEL=eleven_turbo_v2_5
```

Sur un compte **gratuit**, l'API renvoie un 402 pour toute voix ajoutée depuis
la Voice Library — y compris les voix françaises. Seules les voix « premade »
passent, et elles sont toutes anglophones : le français est correct, avec une
légère couleur d'accent. Le premier palier payant lève la restriction.

Le modèle arbitre entre timbre et latence, et sur un assistant vocal la latence
s'entend davantage : `eleven_turbo_v2_5` rend le premier son en ~250 ms,
`eleven_multilingual_v2` est un cran plus naturel mais fait attendre ~1,2 s
avant la première syllabe. Pour changer de voix, prends un identifiant dans la
bibliothèque ElevenLabs et pose-le dans `ELEVENLABS_VOICE_ID`.

Sans clé, rien ne casse : l'app le voit (`GET /api/v1/me` renvoie
`voice.engine = "device"`), l'écran **Accès** l'affiche, et Raoul continue de
parler avec la voix du téléphone.

---

## Démarrer le backend

```bash
cd backend && cp .env.example .env
```

Remplis `.env`, puis :

```bash
go run ./cmd/server
```

### Déploiement sur le VPS OVH

Le backend écoute en clair sur `127.0.0.1:9087` et nginx fait le TLS devant, sur
le sous-domaine `cerveau.neurorun.fr`. **Le webhook Meta exige un HTTPS valide**,
c'est ce qui rend cette étape obligatoire pour WhatsApp.

**1. DNS** — dans la zone OVH de `neurorun.fr`, un enregistrement `A` :

| Sous-domaine | Type | Cible |
|---|---|---|
| `cerveau` | A | l'IPv4 du VPS (`curl -4 ifconfig.me` dessus) |

Ajoute un `AAAA` vers l'IPv6 si le VPS en a une. Attends que ça résolve
(`dig +short cerveau.neurorun.fr`) avant de lancer certbot, sinon la validation échoue.

**2. Le registre d'images**

Chaque push sur `main` touchant `backend/` déclenche
[`.github/workflows/docker.yml`](.github/workflows/docker.yml), qui publie
`ghcr.io/mathiascoutant/cerveau:latest`.

⚠️ **Les images ghcr sont privées par défaut, même pour un dépôt public.** Après
le premier build, va sur la page du package (onglet *Packages* du dépôt) →
*Package settings* → *Change visibility* → **Public**. Sinon il faut
authentifier le VPS :

```bash
echo <PAT_read:packages> | docker login ghcr.io -u mathiascoutant --password-stdin
```

**3. Le VPS**

```bash
mkdir -p ~/projects/cerveau && cd ~/projects/cerveau
curl -O https://raw.githubusercontent.com/mathiascoutant/cerveau/main/backend/deploy/docker-compose.yml
```

Dépose ton `.env` **à côté** du `docker-compose.yml`, puis verrouille-le :

```bash
chmod 600 .env
docker compose up -d
```

Le `600` compte : ce fichier contient la `MASTER_KEY` qui déchiffre tous tes
identifiants Gandi, Slack et WhatsApp.

Le conteneur publie son port sur `127.0.0.1:9087` uniquement (8080 dans le
conteneur), pour cohabiter avec `neurorun-back` déjà sur 9081. C'est délibéré :
sans ce préfixe, Docker écrit ses propres règles iptables et exposerait l'API en
clair sur Internet **en contournant ufw**.

**4. nginx + TLS**

```bash
sudo curl -o /etc/nginx/sites-available/cerveau.neurorun.fr \
  https://raw.githubusercontent.com/mathiascoutant/cerveau/main/backend/deploy/nginx-cerveau.conf
sudo ln -s /etc/nginx/sites-available/cerveau.neurorun.fr /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d cerveau.neurorun.fr
```

Certbot réécrit le vhost avec le bloc TLS et la redirection 80 → 443, comme il
l'a fait pour `neurorun.fr`.

**5. Mettre à jour**

```bash
cd ~/projects/cerveau && docker compose pull && docker compose up -d
```

Vérifie : `curl https://cerveau.neurorun.fr/health` doit répondre `ok`.

> Alternative sans Docker : `backend/deploy/cerveau.service` (systemd) plus
> `make deploy VPS=ubuntu@…` depuis le poste de développement, qui envoie un
> binaire statique compilé en croisé. Utile si tu ne veux pas de Docker sur le VPS.

Enfin, dans l'app mobile, onglet **Accès** → remplace l'adresse du serveur par
`https://cerveau.neurorun.fr`.

---

## Démarrer l'app

### Voir l'interface tout de suite, avec Expo Go

Utile pour juger l'UI et tester le backend en local, sans attendre un build.

Le projet est volontairement sur **Expo SDK 54**, pas la dernière : c'est le SDK
que l'Expo Go de l'App Store sait charger sur iOS. Un projet plus récent affiche
« Project is incompatible with this version of Expo Go ».

⚠️ Ne remonte pas le SDK sans raison. Chaque saut casse des API : `expo-calendar`
passe d'une API `*Async` (SDK 54) à une API à classes (SDK 56+), et
`expo-speech-recognition` change de ligne de versions (3.x pour le SDK 54, 56.x
pour le SDK 56). Si tu remontes, relis `mobile/src/lib/calendar.ts` en premier.

```bash
cd backend && go run ./cmd/server      # dans un terminal
cd mobile   && npx expo start --go     # dans un autre
```

Le `--go` est important : `expo-dev-client` étant installé, la CLI démarre sinon
en mode development build et produit un QR code qu'Expo Go ne sait pas lire.

Scanne le QR code avec Expo Go. L'app **déduit toute seule l'adresse du backend**
depuis l'IP de la machine qui sert le bundle (`http://<ton-ip>:8080`) — rien à
configurer, du moment que l'iPhone est sur le même Wi-Fi que le Mac.

Ce qui marche en Expo Go : toute l'interface, les connexions Gandi / Slack /
WhatsApp, les questions **écrites** à Raoul, et la réponse lue à voix haute.

Ce qui ne marche pas : **« OK Raoul »** et **l'agenda**. Les deux reposent sur des
modules natifs (`expo-speech-recognition`, `expo-calendar`) qu'Expo Go n'embarque
pas — l'app le détecte et bascule en mode texte au lieu de planter. Sans agenda,
Raoul répond sur les mails/Slack/WhatsApp mais ne peut ni vérifier ni créer de créneau.

### Le vrai truc : dev build

**En local, avec Xcode** — le plus rapide pour itérer, et ça permet de tester le
vocal dans le simulateur (qui utilise le micro du Mac) :

```bash
cd mobile
LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 npx expo run:ios --device "iPhone 17 Pro"
```

⚠️ Le préfixe de locale n'est pas décoratif. Sans `LANG` en UTF-8, CocoaPods
casse avec `Unicode Normalization not appropriate for ASCII-8BIT
(Encoding::CompatibilityError)` dans `Pod::Config#installation_root`, puis
xcodebuild échoue sur « The sandbox is not in sync with the Podfile.lock ». La
seconde erreur masque la première, qui est la vraie cause. Sur un shell sans
`LANG` défini — le cas par défaut de zsh sur macOS — ça arrive systématiquement.

**Sur ton iPhone, via EAS** :

```bash
eas build --profile development --platform ios
```

Installe le build sur l'appareil, puis :

```bash
npx expo start --dev-client
```

Dans l'app : onglet **Accès** › renseigne l'URL de ton serveur, autorise le
calendrier, connecte Gandi / Slack / WhatsApp. Puis onglet **Raoul** › touche le
cercle pour activer l'écoute.

### TestFlight

```bash
eas build --profile production --platform ios
eas submit --platform ios
```

---

## Le widget « Parler à Raoul »

Un widget d'écran d'accueil qui ouvre l'app et lance l'écoute d'un appui, sans
passer par l'icône ni par un onglet.

**Il n'écoute pas lui-même.** iOS réserve le micro aux apps au premier plan, et
le code d'un widget ne tourne que le temps de dessiner une image. Celui-ci
ouvre donc le lien `raoul://listen` ; l'app le reçoit, bascule sur l'onglet
Raoul et entre directement en conversation — l'appui sur le widget tient lieu de
« OK Raoul ».

Un widget est une **cible Xcode à part**, avec son bundle (`…cerveau.widget`),
son `Info.plist` et sa signature. `ios/` étant régénéré par prebuild,
[`plugins/withRaoulWidget.js`](mobile/plugins/withRaoulWidget.js) recrée tout à
chaque génération : recopie des sources, fabrication de la cible, embarquement
du produit dans l'app. Le `.appex` est déclaré dans `app.json` sous
`extra.eas.build.experimental.ios.appExtensions`, ce qui fait provisionner son
identifiant par EAS au moment du build.

Pour l'ajouter sur le téléphone, une fois la build TestFlight installée :
appui long sur l'écran d'accueil → **+** en haut à gauche → chercher « Raoul »
→ **Parler à Raoul**. Il existe en petit format et en pastille d'écran
verrouillé (iOS 16+).

> Le widget n'existe pas dans Expo Go : c'est une extension native, elle demande
> un dev build ou une build TestFlight.

---

## Comment « OK Raoul » fonctionne

L'app fait tourner la reconnaissance vocale **native iOS** (`SFSpeechRecognizer`) en
continu et **sur l'appareil** — rien ne part sur le réseau tant que le mot
d'activation n'est pas prononcé. Dès qu'elle repère « OK Raoul » dans le flux, tout
ce qui suit devient la demande ; après ~1,7 s de silence, elle part au backend.

Trois conséquences à connaître :

- **La batterie.** Une reconnaissance permanente consomme. Coupe l'écoute quand tu
  n'en as pas besoin (retouche le cercle), ou passe par l'appui long, qui parle
  immédiatement sans mot d'activation.
- **L'écoute en fond.** `UIBackgroundModes: audio` est déclaré, donc l'écoute survit
  au passage en arrière-plan. iOS reste libre de suspendre l'app après un long
  moment — c'est le comportement normal du système, pas un bug.
- **La détection.** Elle repose sur le texte reconnu, donc elle tolère « ok raoul »,
  « okay Raoule », « hey Raoul ». Si tu veux le vrai wake-word façon Siri (détection
  acoustique, insensible au bruit, quasi sans batterie), le remplacement propre est
  Picovoice Porcupine avec un `.ppn` « OK Raoul » entraîné sur leur console. Le module
  est isolé dans `mobile/src/lib/wakeword.ts` exprès.

---

## Ce que fait Raoul quand tu lui demandes un créneau

Le backend donne au modèle cinq outils et le laisse mener l'enquête :

| Outil | Source |
|---|---|
| `consulter_calendrier` | miroir de l'agenda iOS |
| `mails_non_lus` | IMAP Gandi, en direct |
| `slack_non_lus` | API Slack, en direct |
| `whatsapp_non_lus` | messages collectés par le webhook |
| `creer_evenement` | renvoie une action que l'app écrit dans EventKit |

Le serveur n'écrit jamais dans ton calendrier lui-même : il renvoie une action que
l'app exécute. C'est ce qui permet de rester sur le calendrier natif sans OAuth.

**Les sources non branchées n'existent pas pour Raoul.** Il ne reçoit ni leur outil
ni leur nom dans ses consignes, donc il n'en parle jamais — ni « je n'ai rien trouvé
sur WhatsApp », ni « WhatsApp n'est pas connecté ». Elles disparaissent aussi des
pastilles de l'écran d'accueil et des tuiles du Journal.

---

## Les réponses de mail

> « Prépare-moi une réponse à ce mail pour dire que c'est ok pour moi. »

Raoul rédige le mail en entier, te le lit à voix haute, et le range dans l'onglet
**Réponses**. Tu le copies d'un geste et tu l'envoies depuis ton client mail.

Avant d'écrire, il récupère de quoi répondre juste : `lire_mail` remonte les
destinataires du message, l'historique que le mail cite lui-même, et jusqu'à trois
messages antérieurs du même fil retrouvés dans la boîte. C'est ce qui lui permet
d'ouvrir sur « Bonjour Cyril » plutôt que sur « Bonjour, », de vouvoyer quand le fil
vouvoie, et de signer de ton prénom. Rien de tout ça n'est lu à voix haute — c'est
du contexte, pas du contenu.

**Il n'envoie rien, jamais.** C'est délibéré : un mail parti par erreur ne se
rattrape pas, un brouillon oublié si.

La langue suit le mail d'origine, pas la conversation : un mail reçu en anglais se
répond en anglais et se lit en anglais. « Dis-le-moi en français » te le traduit à
l'oral sans toucher au brouillon — il part chez quelqu'un qui lit l'anglais.

Une réponse préparée se retrouve plus tard à la voix : « modifie la réponse pour
Cyril ». S'il n'y en a qu'une, il la relit directement ; s'il y en a plusieurs, il
cite leurs objets et demande laquelle. Puis « change cette phrase » ne touche que
cette phrase — le reste ressort au mot près.

| Outil | Rôle |
|---|---|
| `preparer_reponse_mail` | rédige la réponse et la range dans l'onglet |
| `chercher_brouillon` | retrouve une réponse déjà préparée |
| `modifier_brouillon` | réécrit une réponse existante |

---

## La mémoire

> « Au fait, par rapport à ce qu'on disait ce matin, tu en penses quoi ? »

Les quatorze derniers tours voyagent dans le contexte du modèle. Au-delà, l'outil
`chercher_historique` va rechercher l'échange en base, par mots-clés et par date.
Raoul ne répond donc plus qu'il ne s'en souvient pas sans avoir cherché.

---

## API

| Méthode | Route | Rôle |
|---|---|---|
| `POST` | `/api/v1/session` | ouvre la session depuis l'identifiant d'appareil |
| `GET` | `/api/v1/status` | bilan des quatre sources |
| `PUT` | `/api/v1/connections/{gandi,slack,whatsapp}` | branche une source (identifiants validés avant stockage) |
| `POST` | `/api/v1/calendar/sync` | l'app pousse le miroir de l'agenda |
| `GET` | `/api/v1/drafts` | les réponses de mail préparées |
| `PATCH` | `/api/v1/drafts/{id}` | retouche une réponse au clavier |
| `DELETE` | `/api/v1/drafts/{id}` | supprime une réponse |
| `POST` | `/api/v1/assistant/ask` | pose une question à Raoul |
| `POST` | `/api/v1/assistant/voice` | secours : envoi audio + transcription serveur |
| `POST` | `/api/v1/assistant/speech` | prépare la lecture d'un texte, renvoie une URL jetable |
| `GET` | `/api/v1/speech/{ticket}` | diffuse l'audio ElevenLabs (ticket à usage unique) |
| `GET/POST` | `/webhooks/whatsapp` | webhook Meta (signature HMAC vérifiée) |
