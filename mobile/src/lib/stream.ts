import { fetch as expoFetch } from 'expo/fetch';

import { api, AssistantAnswer, AssistantMetrics, authHeaders, getApiUrl, openSession, resetSession } from '../api';

/**
 * La conversation en flux.
 *
 * POST /assistant/stream rend des événements au fil de l'eau : un outil qui
 * démarre, un fragment de texte, la fin. L'app les affiche à mesure — c'est ce
 * qui fait qu'on voit Raoul travailler au lieu d'attendre devant un cercle.
 *
 * `fetch` d'Expo plutôt que celui de React Native : seul le premier expose le
 * corps de la réponse en flux lisible. Si le flux est indisponible (ancien
 * serveur, proxy qui tamponne), on retombe sur /assistant/ask et on rejoue la
 * réponse entière comme un seul événement : moins fluide, jamais cassé.
 */

export type StreamEvent =
  | { type: 'status'; tool: string; label: string; ms?: number; err?: string }
  | { type: 'delta'; text: string }
  | { type: 'reset' }
  | { type: 'done'; result: AssistantAnswer; metrics?: AssistantMetrics; speech_url?: string }
  | { type: 'error'; message: string }
  | { type: 'ping' };

export class AbortedError extends Error {
  constructor() {
    super('Demande interrompue.');
    this.name = 'AbortedError';
  }
}

function body(text: string): string {
  return JSON.stringify({
    text,
    now: new Date().toISOString(),
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  });
}

/**
 * Envoie une demande et livre les événements. Se résout quand le flux est
 * fini, rejette sur erreur réseau, ou AbortedError si `signal` a été tiré.
 */
export async function askStream(text: string, onEvent: (ev: StreamEvent) => void, signal: AbortSignal): Promise<void> {
  const base = await getApiUrl();
  let headers = await authHeaders();

  let res: Response;
  try {
    res = await streamRequest(base, headers, text, signal);
    // Token périmé : on rouvre une session, une fois.
    if (res.status === 401) {
      await resetSession();
      const fresh = await openSession();
      headers = { ...headers, Authorization: `Bearer ${fresh.token}` };
      res = await streamRequest(base, headers, text, signal);
    }
  } catch (err) {
    if (signal.aborted) throw new AbortedError();
    throw err;
  }

  // 404 : serveur d'avant le flux. On passe par l'ancienne route.
  if (res.status === 404 || res.status === 405) {
    await fallback(text, onEvent, signal);
    return;
  }
  if (!res.ok) {
    let message = `Erreur ${res.status}`;
    try {
      const raw = await res.text();
      const parsed = JSON.parse(raw) as { error?: string };
      if (parsed?.error) message = parsed.error;
    } catch {
      // corps illisible : on garde le statut
    }
    throw new Error(message);
  }

  const reader = res.body?.getReader();
  if (!reader) {
    await fallback(text, onEvent, signal);
    return;
  }

  const decoder = new TextDecoder();
  let buffer = '';
  const abort = () => {
    void reader.cancel().catch(() => undefined);
  };
  signal.addEventListener('abort', abort);
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let cut = buffer.indexOf('\n\n');
      while (cut >= 0) {
        const frame = buffer.slice(0, cut);
        buffer = buffer.slice(cut + 2);
        deliver(frame, onEvent);
        cut = buffer.indexOf('\n\n');
      }
    }
    if (buffer.trim()) deliver(buffer, onEvent);
  } catch (err) {
    if (signal.aborted) throw new AbortedError();
    throw err;
  } finally {
    signal.removeEventListener('abort', abort);
  }
  if (signal.aborted) throw new AbortedError();
}

async function streamRequest(base: string, headers: Record<string, string>, text: string, signal: AbortSignal): Promise<Response> {
  return (await expoFetch(`${base}/api/v1/assistant/stream`, {
    method: 'POST',
    headers: { ...headers, Accept: 'text/event-stream' },
    body: body(text),
    signal,
  })) as unknown as Response;
}

function deliver(frame: string, onEvent: (ev: StreamEvent) => void): void {
  for (const line of frame.split('\n')) {
    if (!line.startsWith('data:')) continue;
    const raw = line.slice(5).trim();
    if (!raw) continue;
    try {
      const ev = JSON.parse(raw) as StreamEvent;
      if (ev && typeof ev.type === 'string') onEvent(ev);
    } catch {
      // ligne malformée : on l'ignore, le flux continue
    }
  }
}

async function fallback(text: string, onEvent: (ev: StreamEvent) => void, signal: AbortSignal): Promise<void> {
  const answer = await api.ask(text);
  if (signal.aborted) throw new AbortedError();
  onEvent({ type: 'delta', text: answer.reply });
  onEvent({ type: 'done', result: answer, metrics: answer.metrics, speech_url: answer.speech_url });
}
