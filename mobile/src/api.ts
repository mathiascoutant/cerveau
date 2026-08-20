import * as Crypto from 'expo-crypto';
import * as SecureStore from 'expo-secure-store';
import Constants from 'expo-constants';

const DEVICE_ID_KEY = 'cerveau.device_id';
const TOKEN_KEY = 'cerveau.token';
const API_URL_KEY = 'cerveau.api_url';

export type Provider = 'gandi' | 'slack' | 'whatsapp' | 'calendar';

export type Connection = {
  provider: Provider;
  status: 'connected' | 'error' | 'disconnected';
  label?: string;
  last_error?: string;
  updated_at: string;
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
  return { token: data.token, name: data.name };
}

async function token(): Promise<string> {
  if (cachedToken) return cachedToken;
  const stored = await SecureStore.getItemAsync(TOKEN_KEY);
  if (stored) {
    cachedToken = stored;
    return stored;
  }
  const session = await openSession();
  return session.token;
}

export async function resetSession(): Promise<void> {
  cachedToken = null;
  await SecureStore.deleteItemAsync(TOKEN_KEY);
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
    if (!retry.ok) throw new Error(await errorMessage(retry));
    return retry.json() as Promise<T>;
  }

  if (!res.ok) throw new Error(await errorMessage(res));
  return res.json() as Promise<T>;
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
  me: () => request<{ name?: string; timezone: string }>('/me'),

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
