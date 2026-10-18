import * as Crypto from 'expo-crypto';
import * as SecureStore from 'expo-secure-store';
import Constants from 'expo-constants';

const DEVICE_ID_KEY = 'cerveau.device_id';
const TOKEN_KEY = 'cerveau.token';
const API_URL_KEY = 'cerveau.api_url';

/**
 * Copie des identifiants lisible par l'intent Siri (plugins/ios/RaoulIntents.swift).
 *
 * Deux raisons de ne pas relire simplement TOKEN_KEY côté Swift :
 * — l'adresse du serveur et le token doivent bouger ensemble, une seule entrée
 *   les garde cohérents ;
 * — surtout, l'accessibilité. Les entrées SecureStore sont par défaut en
 *   `WHEN_UNLOCKED` : illisibles téléphone verrouillé, c'est-à-dire dans le
 *   seul cas qui justifie l'intégration Siri. Celle-ci est en
 *   `AFTER_FIRST_UNLOCK`, donc lisible dès le premier déverrouillage après
 *   redémarrage — le minimum qu'iOS accepte de garder accessible en veille.
 */
const SIRI_KEY = 'cerveau.siri';
const SIRI_OPTIONS: SecureStore.SecureStoreOptions = {
  keychainService: 'raoul.siri',
  keychainAccessible: SecureStore.AFTER_FIRST_UNLOCK,
};

export type Provider = 'gandi' | 'slack' | 'whatsapp' | 'calendar' | 'tuleap';

/**
 * Les deux espaces de sa vie. Tout ce que Raoul lisait avant leur apparition
 * — Gandi, Slack, WhatsApp, Tuleap — est pro ; ses projets à lui, le VPS et
 * l'équipe technique sont perso. Une boîte mail peut exister des deux côtés.
 */
export type Space = 'pro' | 'perso';

export type Connection = {
  provider: Provider;
  /** Absent sur un serveur d'avant les espaces : c'est alors du pro. */
  space?: Space;
  status: 'connected' | 'error' | 'disconnected';
  label?: string;
  last_error?: string;
  updated_at: string;
};

/** Moteur de voix réellement utilisé, décidé par le serveur. */
export type VoiceInfo = {
  engine: 'elevenlabs' | 'device';
  voice_id?: string;
  model?: string;
  /** Langue imposée à la synthèse. Sans elle, le modèle devine — et prend
   *  l'accent anglais dès qu'un mot n'a pas l'air français. */
  language?: string;
};

export type SourceStatus = {
  provider: Provider;
  connected: boolean;
  unread: number;
  error?: string;
};

export type AssistantAction = {
  type: string;
  payload: Record<string, any>;
};

export type EmailItem = { de: string; objet: string; recu: string };
export type SlackItem = {
  canal: string;
  type?: string;
  non_lus?: number;
  messages_recents?: number;
  mentions?: number;
  extraits?: string[];
};
export type WhatsAppItem = {
  conversation: string;
  type?: 'groupe' | 'prive';
  non_lus?: number;
  mentions?: number;
  dernier?: string;
  extraits?: string[];
};

/** Où en est la liaison WhatsApp. Un compte lié dont la session est fermée ne
 *  reçoit rien : la distinction se voit à l'écran. */
export type WhatsAppStatus = {
  phase: 'deconnecte' | 'appairage' | 'connecte';
  code?: string;
  numero?: string;
  erreur?: string;
  expire_dans?: number;
};
export type EventItem = { titre: string; debut: string; fin: string; lieu?: string };

export type Digest = {
  summary: string;
  generated_at: string;
  stale: boolean;
  emails: EmailItem[];
  slack: SlackItem[];
  whatsapp: WhatsAppItem[];
  events: EventItem[];
  unavailable?: string[];
  /** Comptes réellement branchés : le reste ne s'affiche pas. */
  sources?: Provider[];
};

/**
 * Une réponse de mail rédigée par Raoul. Elle ne part jamais toute seule :
 * l'app l'affiche, on la copie, on l'envoie depuis son client mail.
 */
export type EmailDraft = {
  id: string;
  to: string;
  to_addr?: string;
  subject: string;
  body: string;
  /** Code court de la langue du mail (« fr », « en »), pas celle de l'app. */
  language?: string;
  source_subject?: string;
  created_at: string;
  updated_at: string;
};

/**
 * Le message d'où sort une tâche, tel que Raoul l'a lu.
 *
 * `origine` vaut « mail » ou le nom du canal Slack : c'est le mot que
 * l'utilisateur reconnaît, pas un identifiant de fournisseur.
 */
export type UrgentSource = {
  origine: string;
  de: string;
  titre: string;
  /** Déjà mis en mots dans le fuseau de l'utilisateur (« il y a 20 min »). */
  quand: string;
};

/**
 * Une chose à faire, pas un message à lire.
 *
 * Deux étages côté serveur produisent cette liste : un tri déterministe écarte
 * ce qui ne s'adresse pas personnellement à l'utilisateur, puis le modèle
 * regroupe ce qui reste par sujet et nomme l'action. Trois mails sur le même
 * devis donnent donc une seule entrée, dont `sources` contient les trois.
 */
export type UrgentTask = {
  action: string;
  urgence: 'haute' | 'moyenne';
  /** D'où ça vient et ce qui est attendu — le texte affiché au dépliage. */
  pourquoi: string;
  sources: UrgentSource[];
};

export type Urgent = {
  taches: UrgentTask[];
  /** Sources réellement interrogées : « rien à traiter » et « rien de branché »
   *  se ressemblent trop pour laisser l'app deviner. */
  sources: string[];
  unavailable?: string[];
  generated_at: string;
};

/**
 * Une chose qu'il s'est engagé à faire, et le jour où il compte la faire.
 *
 * À ne pas confondre avec UrgentTask, qui lui ressemble et ne vit pas du tout
 * pareil : une urgence est déduite des messages non traités et disparaît quand
 * le message est traité, une tâche est écrite par lui et reste jusqu'à ce
 * qu'elle soit cochée.
 */
export type Todo = {
  id: string;
  title: string;
  note?: string;
  /** ISO. Absent tant qu'il n'a pas dit quand il la ferait. */
  due?: string;
  /** Une heure précise a été donnée, pas seulement un jour. */
  timed?: boolean;
  done: boolean;
  done_at?: string;
  source?: { origine?: string; de?: string; titre?: string };
  created_at: string;
  updated_at: string;
};

export type JobCard = {
  key: 'csp' | 'flight_schedule' | string;
  title: string;
  description: string;
  enabled: boolean;
  reason?: string;
  configured: boolean;
  connected: boolean;
  /** La clé posée sur le serveur sert, faute de clé personnelle. */
  server_key?: boolean;
};

/** Un ticket CSP, mis à plat par le serveur depuis l'artefact Tuleap. */
export type CSPTicket = {
  id: number;
  ref: string;
  titre: string;
  statut: string;
  priorite?: string;
  responsable?: string;
  auteur?: string;
  cree: string;
  modifie: string;
  url: string;
  tracker?: string;
  description?: string;
  champs?: Record<string, string>;
  ferme?: boolean;
  /** Lecture « cycle CSP » du titre, comme PXFeed-UI : « Azul », « 2026-11 », « Azul 2026-11 ». */
  compagnie?: string;
  cycle?: string;
  label?: string;
};

export type CSPComment = { auteur: string; quand: string; texte: string };

export type CSPList = {
  tickets: CSPTicket[];
  generated_at: string;
  cached: boolean;
  scope: {
    base_url: string;
    tracker_id: number;
    /** « pxfeed » : les cycles CSP sélectionnés comme PXFeed-UI ; « query » : requête explicite. */
    mode: 'pxfeed' | 'query';
    release_id?: number;
    source?: string;
    airlines?: string[];
    stats?: { total: number; titre_reconnu: number; statut_exclu: number; compagnie_exclue: number; retenus: number };
    query?: string;
    expert_query?: string;
    assigned_to_me: boolean;
    user?: string;
  };
};

export type Interaction = {
  transcript: string;
  reply: string;
  actions?: AssistantAction[];
  created_at: string;
};

/** Ce qu'une réponse a coûté, tel que le serveur l'a mesuré. */
export type AssistantMetrics = {
  tier: 'fast' | 'tools' | 'deep';
  model: string;
  reason?: string;
  model_calls: number;
  input_tokens: number;
  cached_tokens: number;
  output_tokens: number;
  first_token_ms: number;
  first_event_ms: number;
  total_ms: number;
  tools?: { name: string; ms: number; err?: boolean }[];
};

export type AssistantAnswer = {
  transcript: string;
  reply: string;
  actions: AssistantAction[];
  steps?: string[];
  metrics?: AssistantMetrics;
  /** Qui a répondu : absent pour Raoul, sinon un membre de l'équipe. */
  agent?: string;
  /**
   * Adresse du son de cette réponse, dont le serveur a déjà lancé la synthèse.
   * Absente quand le serveur n'a pas de voix distante — l'app lit alors avec
   * celle du téléphone.
   */
  speech_url?: string;
};

/* -------------------------------------------------------------------------- */
/* L'espace perso : l'équipe et ses projets                                    */
/* -------------------------------------------------------------------------- */

/** Un membre de l'équipe, Raoul compris. */
export type Membre = {
  id: string;
  nom: string;
  role: string;
  equipe?: string;
  superieur?: string;
  espace?: string;
  /** Sa teinte dans l'app : on sait qui parle d'un coup d'œil. */
  couleur: string;
  presentation: string;
};

export type Capacites = { sonde: boolean; github: boolean; github_authentifie: boolean };

export type EquipeInfo = {
  membres: Membre[];
  /** Qui a la parole : « raoul » ou un membre de l'équipe. */
  interlocuteur: string;
  capacites: Capacites;
};

/** Une fiche du savoir de l'équipe. */
export type Connaissance = {
  id: string;
  equipe: string;
  projet: string;
  sujet: string;
  contenu: string;
  /** « amorce » : savoir de départ ; « equipe » : appris en travaillant. */
  origine: 'amorce' | 'equipe' | 'app';
  auteur?: string;
  created_at: string;
  updated_at: string;
};

/** Le poids de ce qu'une personne lui demande. */
export type Importance = 'critique' | 'haute' | 'normale' | 'basse';

/**
 * Quelqu'un de son entourage, tel que Raoul le connaît : sa place pour lui et
 * ce qui permet de le reconnaître quand il écrit.
 */
export type Personne = {
  id: string;
  kind: string;
  subject?: string;
  content: string;
  /** « voix » : appris en écoutant ; « app » : saisi à la main. */
  origin?: string;
  role?: string;
  importance?: Importance;
  adresses?: string[];
  slack_ids?: string[];
  created_at: string;
  updated_at: string;
};

/** Une conversation que Raoul a lue pour lui. */
export type Echange = {
  id: string;
  source: 'mail' | 'slack' | 'whatsapp';
  espace?: Space;
  sujet: string;
  participants?: string[];
  dernier_de?: string;
  dernier_quand?: string;
  dernier_extrait?: string;
  dernier_de_toi?: boolean;
  pour_toi?: string;
  /** Ce qu'il en a compris : l'analyse d'un débrief, ou ce qu'il en a dit. */
  resume?: string;
  resume_le?: string;
  vu_le: string;
};

export type Entourage = {
  personnes: Personne[];
  /** Le reste de ce qu'il retient : dossiers, habitudes, faits. */
  autres: Personne[];
  echanges: Echange[];
};

export type PersonneEdit = {
  sujet?: string;
  contenu?: string;
  role: string;
  importance: Importance | '';
  adresses?: string[];
};

export type VerifURL = {
  url: string;
  statut?: number;
  erreur?: string;
  total_ms: number;
  dns_ms?: number;
  connexion_ms?: number;
  tls_ms?: number;
  premier_octet_ms?: number;
  certificat_expire?: string;
  certificat_jours?: number;
};

export type Conteneur = {
  nom: string;
  image: string;
  etat: string;
  statut: string;
  sante?: string;
  demarre_le?: string;
  redemarrages?: number;
  ports?: { ip?: string; hote?: number; prive: number; public?: boolean }[];
  revision?: string;
  version?: string;
};

export type Execution = {
  nom: string;
  statut: string;
  conclusion?: string;
  branche?: string;
  sha: string;
  date: string;
  lien?: string;
};

export type EtatComposant = {
  id: string;
  nom: string;
  type: 'back' | 'front' | 'app' | string;
  env?: string;
  etat: EtatNiveau;
  http?: VerifURL;
  conteneur?: Conteneur;
  depot?: {
    depot: string;
    branche: string;
    dernier_commit?: { sha: string; message: string; auteur?: string; date: string };
    ci?: Execution[];
    deploiement: 'a_jour' | 'en_retard' | 'inconnu';
    explication?: string;
    erreur?: string;
  };
};

export type EtatNiveau = 'ok' | 'attention' | 'panne' | 'inconnu';

export type EtatBase = {
  id: string;
  nom: string;
  etat: 'ok' | 'erreur' | 'inconnu';
  erreur?: string;
  ping_ms?: number;
  connexion_ms?: number;
  version?: string;
  donnees_mo?: number;
  stockage_mo?: number;
  nom_base?: string;
};

export type EtatProjet = {
  id: string;
  nom: string;
  description?: string;
  etat: EtatNiveau;
  composants: EtatComposant[];
  bases?: EtatBase[];
};

export type EtatVPS = {
  nom: string;
  hote: string;
  os: string;
  version: string;
  fin_support?: string;
  hors_support?: boolean;
  noyau: string;
  allume_depuis_s: number;
  charge: number[];
  processeurs: number;
  memoire: { total_mo: number; disponible_mo: number };
  disques: { point: string; total_go: number; libre_go: number; utilise_pct: number }[];
  mises_a_jour: number;
  mises_a_jour_securite: number;
  redemarrage_requis?: boolean;
  docker?: string;
  conteneurs?: Conteneur[];
};

export type Alerte = { niveau: 'critique' | 'attention' | 'info'; texte: string; projet?: string };

/** Le point d'ensemble sur ses projets perso. */
export type EtatProjets = {
  genere_le: string;
  vps?: EtatVPS;
  projets: EtatProjet[];
  alertes: Alerte[];
  capacites: Capacites;
  erreurs?: string[];
};

let cachedToken: string | null = null;
let cachedApiUrl: string | null = null;

/**
 * En développement, le téléphone ne sait évidemment pas joindre le `localhost`
 * du Mac : on déduit l'adresse du serveur de l'IP qui sert déjà le bundle Expo,
 * même machine, port 8080. Zéro configuration pour tester en local.
 *
 * En build de production (TestFlight), on prend l'adresse du VPS déclarée dans
 * app.json. Dans les deux cas l'utilisateur peut la remplacer depuis l'onglet
 * Accès, et son choix est prioritaire.
 */
function defaultApiUrl(): string {
  const configured = Constants.expoConfig?.extra?.defaultApiUrl as string | undefined;

  if (__DEV__) {
    const hostUri =
      Constants.expoConfig?.hostUri ??
      (Constants as any).expoGoConfig?.debuggerHost ??
      (Constants as any).manifest2?.extra?.expoGo?.debuggerHost;

    const host = typeof hostUri === 'string' ? hostUri.split(':')[0] : null;
    if (host) return `http://${host}:8080`;
  }

  return configured ?? 'http://localhost:8080';
}

/** Adresse déduite automatiquement, affichée dans l'écran Accès. */
export function detectedApiUrl(): string {
  return defaultApiUrl();
}

export async function getApiUrl(): Promise<string> {
  if (cachedApiUrl) return cachedApiUrl;
  const stored = await SecureStore.getItemAsync(API_URL_KEY);
  cachedApiUrl = stored ?? defaultApiUrl();
  return cachedApiUrl;
}

export async function setApiUrl(url: string): Promise<void> {
  const clean = url.trim().replace(/\/+$/, '');
  cachedApiUrl = clean;
  await SecureStore.setItemAsync(API_URL_KEY, clean);
  await publishToSiri();
}

/**
 * Dépose adresse + token là où l'intent Siri sait les lire. Appelé à chaque
 * fois que l'un des deux change : sans ça, Siri continuerait de parler à
 * l'ancien serveur avec un token périmé.
 */
async function publishToSiri(): Promise<void> {
  try {
    const apiUrl = await getApiUrl();
    if (!cachedToken) return;
    await SecureStore.setItemAsync(
      SIRI_KEY,
      JSON.stringify({ apiUrl, token: cachedToken }),
      SIRI_OPTIONS,
    );
  } catch {
    // Le trousseau peut refuser l'écriture (appareil verrouillé au lancement
    // en fond). Siri gardera la version précédente, l'app republiera au
    // prochain passage au premier plan.
  }
}

/**
 * Pas de login : l'identité de l'utilisateur, c'est l'appareil. On génère un
 * UUID à la première ouverture, on le garde dans le Keychain, et le serveur
 * nous rend un token permanent.
 */
async function deviceId(): Promise<string> {
  const existing = await SecureStore.getItemAsync(DEVICE_ID_KEY);
  if (existing) return existing;
  const fresh = Crypto.randomUUID();
  await SecureStore.setItemAsync(DEVICE_ID_KEY, fresh);
  return fresh;
}

/**
 * Délai d'ouverture de session. Un serveur qui ne répond pas en douze secondes
 * ne répondra pas : mieux vaut un écran qui dit quelle adresse a été essayée
 * qu'une roue qui tourne sans fin.
 */
const SESSION_TIMEOUT_MS = 12000;
/** Délai des appels ordinaires. Large : une question à Raoul peut prendre
 *  une minute quand plusieurs sources sont consultées. */
const REQUEST_TIMEOUT_MS = 120000;

/** fetch avec un délai d'expiration, sans lequel un hôte muet bloque tout. */
async function fetchWithTimeout(input: string, init: RequestInit, ms: number): Promise<Response> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), ms);
  try {
    return await fetch(input, { ...init, signal: controller.signal });
  } catch (err) {
    if (controller.signal.aborted) {
      throw new Error(`Pas de réponse de ${input.replace(/\/api\/v1.*$/, '')} en ${Math.round(ms / 1000)} s.`);
    }
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Le serveur veut un compte : aucune session valable sur cet appareil, et
 * l'appareil n'est pas connu des premières versions. L'app affiche l'écran
 * de connexion — ce n'est pas une panne.
 */
export class AuthRequiredError extends Error {
  constructor(message = 'Connecte-toi avec ton compte.') {
    super(message);
    this.name = 'AuthRequiredError';
  }
}

type AuthListener = () => void;
let authListener: AuthListener | null = null;

/** L'app s'abonne : dès qu'une session est refusée pour de bon, elle bascule
 *  sur l'écran de connexion au lieu d'empiler des erreurs. */
export function onAuthRequired(listener: AuthListener | null): void {
  authListener = listener;
}

export type Session = { token: string; name?: string; email?: string };

function deviceLabel(): string {
  return (Constants.deviceName as string | undefined) ?? 'iPhone';
}

/**
 * Ouvre la session de cet appareil.
 *
 * Trois cas, dans l'ordre : un token déjà rangé dans le Keychain — on le
 * vérifie ; sinon l'appareil des premières versions, connu du serveur par
 * son identifiant ; sinon il faut un compte, et l'app le demande. Les
 * connexions (mails, Slack, WhatsApp, Tuleap, agenda) sont rattachées au
 * compte, pas au téléphone : se connecter ailleurs retrouve tout.
 */
export async function openSession(): Promise<Session> {
  const url = await getApiUrl();

  const stored = await SecureStore.getItemAsync(TOKEN_KEY);
  if (stored) {
    const res = await fetchWithTimeout(
      `${url}/api/v1/me`,
      { headers: { Authorization: `Bearer ${stored}` } },
      SESSION_TIMEOUT_MS,
    );
    if (res.ok) {
      const me = (await res.json()) as { name?: string; email?: string };
      cachedToken = stored;
      await publishToSiri();
      return { token: stored, name: me.name, email: me.email };
    }
    if (res.status !== 401) throw new Error(await errorMessage(res));
    // Session révoquée : on l'oublie et on continue comme un appareil neuf.
    await resetSession();
  }

  const id = await deviceId();
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const res = await fetchWithTimeout(
    `${url}/api/v1/session`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ device_id: id, timezone }),
    },
    SESSION_TIMEOUT_MS,
  );
  if (res.status === 401) throw new AuthRequiredError(await errorMessage(res));
  if (!res.ok) throw new Error(await errorMessage(res));

  const data = await res.json();
  await storeSession(data.token);
  return { token: data.token, name: data.name };
}

async function storeSession(token: string): Promise<void> {
  cachedToken = token;
  await SecureStore.setItemAsync(TOKEN_KEY, token);
  await publishToSiri();
}

async function account(path: string, body: Record<string, unknown>, bearer?: string): Promise<Session> {
  const url = await getApiUrl();
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  const res = await fetchWithTimeout(
    `${url}/api/v1${path}`,
    {
      method: 'POST',
      headers,
      body: JSON.stringify({
        ...body,
        device: deviceLabel(),
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      }),
    },
    SESSION_TIMEOUT_MS,
  );
  if (!res.ok) throw new ApiError(res.status, await errorMessage(res));
  const data = (await res.json()) as { token: string; name?: string; email?: string };
  await storeSession(data.token);
  return { token: data.token, name: data.name, email: data.email };
}

/** Connexion par adresse et mot de passe : une session pour cet appareil. */
export function login(email: string, password: string): Promise<Session> {
  return account('/auth/login', { email, password });
}

/** Création de compte — refusée (403) si le serveur n'ouvre pas l'inscription. */
export function signup(email: string, password: string, name?: string): Promise<Session> {
  return account('/auth/signup', { email, password, name });
}

/**
 * Pose une adresse et un mot de passe sur le compte courant, celui de cet
 * appareil : rien de ce qui est branché ne bouge, et le compte devient
 * joignable depuis un autre téléphone.
 */
export async function claimAccount(email: string, password: string, name?: string): Promise<Session> {
  return account('/auth/claim', { email, password, name }, await token());
}

/** Ferme la session de cet appareil, et lui seul. */
export async function logout(): Promise<void> {
  try {
    const url = await getApiUrl();
    const bearer = await token();
    await fetchWithTimeout(
      `${url}/api/v1/auth/logout`,
      { method: 'POST', headers: { Authorization: `Bearer ${bearer}` } },
      SESSION_TIMEOUT_MS,
    );
  } catch {
    // Déjà fermée côté serveur, ou serveur injoignable : on part quand même.
  }
  await resetSession();
}

async function token(): Promise<string> {
  if (cachedToken) return cachedToken;
  const stored = await SecureStore.getItemAsync(TOKEN_KEY);
  if (stored) {
    cachedToken = stored;
    // Republication à chaque démarrage : c'est ce qui rattrape une entrée Siri
    // absente (première mise à jour de l'app) ou laissée sur un ancien serveur.
    await publishToSiri();
    return stored;
  }
  const session = await openSession();
  return session.token;
}

export async function resetSession(): Promise<void> {
  cachedToken = null;
  await SecureStore.deleteItemAsync(TOKEN_KEY);
  // Laisser la copie Siri derrière donnerait un assistant qui répond encore
  // avec un token qu'on vient de révoquer.
  await SecureStore.deleteItemAsync(SIRI_KEY, SIRI_OPTIONS);
}

/**
 * En-têtes d'une requête authentifiée, pour les appels qui ne passent pas par
 * `request` — le flux de conversation, qui lit la réponse au fil de l'eau.
 */
export async function authHeaders(): Promise<Record<string, string>> {
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${await token()}` };
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const url = await getApiUrl();
  const bearer = await token();

  const res = await fetchWithTimeout(
    `${url}/api/v1${path}`,
    {
      ...init,
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${bearer}`,
        ...(init.headers ?? {}),
      },
    },
    REQUEST_TIMEOUT_MS,
  );

  // Token périmé (base réinitialisée par exemple) : on rouvre une session.
  // Si le serveur exige un compte, l'app passe à l'écran de connexion.
  if (res.status === 401) {
    await resetSession();
    let fresh: Session;
    try {
      fresh = await openSession();
    } catch (err) {
      if (err instanceof AuthRequiredError) authListener?.();
      throw err;
    }
    const retry = await fetchWithTimeout(
      `${url}/api/v1${path}`,
      {
        ...init,
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${fresh.token}`,
          ...(init.headers ?? {}),
        },
      },
      REQUEST_TIMEOUT_MS,
    );
    if (!retry.ok) throw new ApiError(retry.status, await errorMessage(retry));
    return retry.json() as Promise<T>;
  }

  if (!res.ok) throw new ApiError(res.status, await errorMessage(res));
  return res.json() as Promise<T>;
}

/**
 * Erreur HTTP qui garde son statut : l'appelant peut distinguer « le serveur
 * ne sait pas faire » (501) d'un incident passager, et adapter son repli.
 */
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function errorMessage(res: Response): Promise<string> {
  try {
    const body = await res.json();
    if (body?.error) return body.error;
  } catch {
    // corps non JSON : on retombe sur le statut
  }
  return `Erreur ${res.status}`;
}

// --- Endpoints ---------------------------------------------------------------

export const api = {
  me: () =>
    request<{
      email?: string;
      name?: string;
      timezone: string;
      voice?: VoiceInfo;
      /** Absent sur un serveur d'avant l'espace perso. */
      perso?: { projets: boolean; equipe: boolean };
    }>('/me'),

  setName: (name: string) =>
    request<{ name: string }>('/me', { method: 'PATCH', body: JSON.stringify({ name }) }),

  status: () => request<{ sources: SourceStatus[] }>('/status'),

  /** Synthèse du jour + aperçu des sources. `refresh` force la régénération. */
  digest: (refresh = false) =>
    request<Digest>(`/digest${refresh ? '?refresh=1' : ''}`),

  /** Ce qu'il reste à faire. `refresh` force la régénération. */
  urgent: (refresh = false) => request<Urgent>(`/urgent${refresh ? '?refresh=1' : ''}`),

  history: () => request<{ interactions: Interaction[] }>('/history'),

  connections: () => request<{ connections: Connection[] }>('/connections'),

  /** Une boîte mail IMAP, rangée côté pro (Gandi, défaut) ou côté perso. */
  connectGandi: (email: string, password: string, host?: string, espace: Space = 'pro') =>
    request<Connection>('/connections/gandi', {
      method: 'PUT',
      body: JSON.stringify({ email, password, host, espace }),
    }),

  /** Démarre le flux OAuth : renvoie l'URL de consentement à ouvrir. */
  startSlackOAuth: () =>
    request<{ url: string }>('/connections/slack/oauth', { method: 'POST' }),

  /** Clé d'accès personnelle Tuleap, validée par le serveur avant stockage. */
  connectTuleap: (accessKey: string) =>
    request<Connection>('/connections/tuleap', {
      method: 'PUT',
      body: JSON.stringify({ access_key: accessKey }),
    }),

  /** Les cartes de l'onglet Jobs, et si elles s'ouvrent. */
  jobs: () => request<{ jobs: JobCard[] }>('/jobs'),

  /** Les tickets CSP du tracker Tuleap. `refresh` ignore le cache serveur. */
  cspTickets: (refresh = false) =>
    request<CSPList>(`/jobs/csp/tickets${refresh ? '?refresh=1' : ''}`),

  cspTicket: (id: number) =>
    request<{ ticket: CSPTicket; comments: CSPComment[] }>(`/jobs/csp/tickets/${id}`),

  connectSlack: (userToken: string) =>
    request<Connection>('/connections/slack', {
      method: 'PUT',
      body: JSON.stringify({ user_token: userToken }),
    }),

  /**
   * Démarre la liaison WhatsApp et rend le code à taper dans l'app WhatsApp.
   *
   * Un code plutôt qu'un QR : le QR se scanne avec le téléphone, or c'est ce
   * téléphone-là qui affiche l'écran où on le lirait.
   */
  pairWhatsApp: (numero: string) =>
    request<{ code: string; statut: WhatsAppStatus }>('/connections/whatsapp/pair', {
      method: 'POST',
      body: JSON.stringify({ numero }),
    }),

  whatsAppStatus: () => request<WhatsAppStatus>('/connections/whatsapp/status'),

  disconnect: (provider: Provider, espace: Space = 'pro') =>
    request<{ provider: string }>(`/connections/${provider}${espace === 'perso' ? '?espace=perso' : ''}`, { method: 'DELETE' }),

  /** Le point d'ensemble sur ses projets perso. `refresh` refait les relevés. */
  projets: (refresh = false) => request<EtatProjets>(`/perso/projets${refresh ? '?refresh=1' : ''}`),

  /** L'équipe, et qui a la parole. */
  equipe: () => request<EquipeInfo>('/equipe'),

  /** Donne la parole à un membre ; « raoul » la lui rend. */
  setInterlocuteur: (agent: string) =>
    request<{ interlocuteur: string }>('/equipe/interlocuteur', { method: 'PUT', body: JSON.stringify({ agent }) }),

  /** Une phrase de présentation dans la voix d'un membre. */
  voixMembre: (id: string) => request<{ url: string; texte: string }>(`/equipe/${id}/voix`, { method: 'POST' }),

  /** Ce que l'équipe sait de ses projets. */
  connaissances: (projet?: string) =>
    request<{ connaissances: Connaissance[] }>(`/equipe/connaissances${projet ? `?projet=${encodeURIComponent(projet)}` : ''}`),

  oublierConnaissance: (id: string) => request<{ deleted: string }>(`/equipe/connaissances/${id}`, { method: 'DELETE' }),

  /** Son entourage pro : les gens, ce que Raoul retient d'autre, ce qu'il a lu. */
  entourage: () => request<Entourage>('/entourage'),

  ajouterPersonne: (edit: PersonneEdit) =>
    request<Personne>('/entourage', { method: 'POST', body: JSON.stringify(edit) }),

  modifierPersonne: (id: string, edit: PersonneEdit) =>
    request<Personne>(`/entourage/${id}`, { method: 'PATCH', body: JSON.stringify(edit) }),

  /** Retire une fiche de la mémoire, personne ou autre. */
  oublierFiche: (id: string) => request<{ deleted: string }>(`/memory/${id}`, { method: 'DELETE' }),

  syncCalendar: (payload: {
    from: string;
    to: string;
    events: Array<{
      id: string;
      calendar?: string;
      title: string;
      location?: string;
      start: string;
      end: string;
      all_day: boolean;
    }>;
  }) => request<{ synced: number }>('/calendar/sync', { method: 'POST', body: JSON.stringify(payload) }),

  /**
   * Prépare la lecture d'un texte et renvoie le chemin du flux audio à jouer.
   * Le texte ne transite jamais par l'URL : le serveur rend un ticket jetable.
   */
  speak: (text: string) =>
    request<{ url: string; expires_in: number }>('/assistant/speech', {
      method: 'POST',
      body: JSON.stringify({ text }),
    }),

  /** Tout ce qui reste à faire, plus ce qui vient d'être coché. */
  todos: () => request<{ todos: Todo[] }>('/todos'),

  addTodo: (input: {
    title: string;
    note?: string;
    /** « 2026-08-27 », ou avec l'heure. Vide : pas encore de jour. */
    due?: string;
    source?: { origine?: string; de?: string; titre?: string };
  }) => request<Todo>('/todos', { method: 'POST', body: JSON.stringify(input) }),

  /** `due` absent ne touche pas à la date ; `due: ''` la retire. */
  updateTodo: (id: string, patch: { done?: boolean; due?: string }) =>
    request<Todo>(`/todos/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  deleteTodo: (id: string) => request<{ deleted: string }>(`/todos/${id}`, { method: 'DELETE' }),

  drafts: () => request<{ drafts: EmailDraft[] }>('/drafts'),

  updateDraft: (id: string, body: string, subject?: string) =>
    request<EmailDraft>(`/drafts/${id}`, {
      method: 'PATCH',
      body: JSON.stringify({ body, subject }),
    }),

  deleteDraft: (id: string) =>
    request<{ deleted: string }>(`/drafts/${id}`, { method: 'DELETE' }),

  ask: (text: string) =>
    request<AssistantAnswer>('/assistant/ask', {
      method: 'POST',
      body: JSON.stringify({
        text,
        now: new Date().toISOString(),
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      }),
    }),
};
