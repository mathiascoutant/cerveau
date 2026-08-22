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

export type Provider = 'gandi' | 'slack' | 'whatsapp' | 'calendar';

export type Connection = {
  provider: Provider;
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
export type WhatsAppItem = { de: string; message: string; recu: string };
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

export type Interaction = {
  transcript: string;
  reply: string;
  actions?: AssistantAction[];
  created_at: string;
};

export type AssistantAnswer = {
  transcript: string;
  reply: string;
  actions: AssistantAction[];
  steps?: string[];
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

export async function openSession(): Promise<{ token: string; name?: string }> {
  const url = await getApiUrl();
  const id = await deviceId();
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;

  const res = await fetch(`${url}/api/v1/session`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ device_id: id, timezone }),
  });
  if (!res.ok) throw new Error(await errorMessage(res));

  const data = await res.json();
  cachedToken = data.token;
  await SecureStore.setItemAsync(TOKEN_KEY, data.token);
  await publishToSiri();
  return { token: data.token, name: data.name };
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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const url = await getApiUrl();
  const bearer = await token();

  const res = await fetch(`${url}/api/v1${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${bearer}`,
      ...(init.headers ?? {}),
    },
  });

  // Token périmé (base réinitialisée par exemple) : on rouvre une session.
  if (res.status === 401) {
    await resetSession();
    const fresh = await openSession();
    const retry = await fetch(`${url}/api/v1${path}`, {
      ...init,
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${fresh.token}`,
        ...(init.headers ?? {}),
      },
    });
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
  me: () => request<{ name?: string; timezone: string; voice?: VoiceInfo }>('/me'),

  setName: (name: string) =>
    request<{ name: string }>('/me', { method: 'PATCH', body: JSON.stringify({ name }) }),

  status: () => request<{ sources: SourceStatus[] }>('/status'),

  /** Synthèse du jour + aperçu des sources. `refresh` force la régénération. */
  digest: (refresh = false) =>
    request<Digest>(`/digest${refresh ? '?refresh=1' : ''}`),

  history: () => request<{ interactions: Interaction[] }>('/history'),

  connections: () => request<{ connections: Connection[] }>('/connections'),

  connectGandi: (email: string, password: string, host?: string) =>
    request<Connection>('/connections/gandi', {
      method: 'PUT',
      body: JSON.stringify({ email, password, host }),
    }),

  /** Démarre le flux OAuth : renvoie l'URL de consentement à ouvrir. */
  startSlackOAuth: () =>
    request<{ url: string }>('/connections/slack/oauth', { method: 'POST' }),

  connectSlack: (userToken: string) =>
    request<Connection>('/connections/slack', {
      method: 'PUT',
      body: JSON.stringify({ user_token: userToken }),
    }),

  connectWhatsApp: (phoneNumberId: string, accessToken: string, wabaId?: string) =>
    request<Connection>('/connections/whatsapp', {
      method: 'PUT',
      body: JSON.stringify({
        phone_number_id: phoneNumberId,
        access_token: accessToken,
        waba_id: wabaId,
      }),
    }),

  disconnect: (provider: Provider) =>
    request<{ provider: string }>(`/connections/${provider}`, { method: 'DELETE' }),

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
