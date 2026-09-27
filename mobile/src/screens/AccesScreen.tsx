import React, { useCallback, useEffect, useState } from 'react';
import { Alert, Linking, ScrollView, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Button, Divider, Dot, Field, IconName, Notice, Panel, ScreenHeader, Section, T } from '../design/ui';
import { navBarSpace } from '../design/NavBar';
import { alpha, tokens } from '../design/tokens';
import { api, Connection, detectedApiUrl, getApiUrl, Provider, setApiUrl, VoiceInfo, WhatsAppStatus } from '../api';
import { calendarSupported, hasCalendarAccess, requestCalendarAccess, syncCalendar } from '../lib/calendar';
import { clearCache } from '../lib/cache';
import { frenchVoices } from '../lib/speech';
import { loadDigest, loadUrgent } from './SuiviScreen';

/**
 * Accès : ce que Raoul a le droit de voir, et rien d'autre.
 *
 * Une carte par source, avec son état et de quoi la brancher ou la couper.
 * Tout ce qui est saisi ici part chiffré côté serveur et n'en ressort jamais
 * vers l'app : on affiche « connecté », jamais le secret.
 */
export function AccesScreen() {
  const insets = useSafeAreaInsets();
  const [connections, setConnections] = useState<Record<string, Connection>>({});
  const [serverUrl, setServerUrl] = useState('');
  const [name, setName] = useState('');
  const [voices, setVoices] = useState<{ label: string; best: boolean }[] | null>(null);
  const [voice, setVoice] = useState<VoiceInfo | null>(null);
  const [calendarReady, setCalendarReady] = useState(false);
  const [calendarUsable, setCalendarUsable] = useState(true);
  const [tuleapConfigured, setTuleapConfigured] = useState<boolean | null>(null);
  const [tuleapServerKey, setTuleapServerKey] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const res = await api.connections();
      const map: Record<string, Connection> = {};
      for (const c of res.connections) map[c.provider] = c;
      setConnections(map);
    } catch {
      // serveur injoignable : l'écran reste utilisable pour corriger l'URL
    }
    try {
      const { jobs } = await api.jobs();
      const csp = jobs.find((j) => j.key === 'csp');
      setTuleapConfigured(csp?.configured ?? false);
      setTuleapServerKey(Boolean(csp?.server_key));
    } catch {
      setTuleapConfigured(null);
    }
    setCalendarUsable(await calendarSupported());
    setCalendarReady(await hasCalendarAccess());
  }, []);

  useEffect(() => {
    void getApiUrl().then(setServerUrl);
    void api
      .me()
      .then((me) => {
        setName(me.name ?? '');
        if (me.voice) setVoice(me.voice);
      })
      .catch(() => undefined);
    void frenchVoices()
      .then((list) => setVoices(list.map((v, i) => ({ label: `${v.name} · ${v.quality}`, best: i === 0 }))))
      .catch(() => setVoices([]));
    void refresh();
  }, [refresh]);

  const run = useCallback(
    async (key: string, task: () => Promise<void>) => {
      setBusy(key);
      try {
        await task();
        await refresh();
      } catch (err) {
        Alert.alert('Échec', (err as Error).message);
      } finally {
        setBusy(null);
      }
    },
    [refresh],
  );

  const disconnect = (provider: Provider) => run(`disconnect-${provider}`, async () => void (await api.disconnect(provider)));

  return (
    <ScrollView style={styles.flex} contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]} keyboardShouldPersistTaps="handled">
      <ScreenHeader title="Accès" subtitle="Raoul ne voit que ce que tu lui ouvres" />

      <Section title="Toi">
        <Panel>
          <Field label="Ton prénom" placeholder="Mathias" value={name} onChangeText={setName} autoCapitalize="words" hint="Raoul s’en sert pour s’adresser à toi et signer tes réponses." />
          <Button label="Enregistrer" variant="secondary" icon="check" loading={busy === 'name'} onPress={() => run('name', async () => void (await api.setName(name.trim())))} />
        </Panel>
      </Section>

      <Section title="Serveur">
        <Panel>
          <Field label="Adresse du backend" placeholder="https://cerveau.mondomaine.fr" value={serverUrl} onChangeText={setServerUrl} keyboardType="url" hint={`Détectée : ${detectedApiUrl()}`} />
          <Button
            label="Enregistrer l’adresse"
            variant="secondary"
            icon="server"
            loading={busy === 'server'}
            onPress={() =>
              run('server', async () => {
                await setApiUrl(serverUrl);
                clearCache();
                await api.status();
                void loadDigest(true);
                void loadUrgent(true);
              })
            }
          />
        </Panel>
      </Section>

      <Section title="Sources">
        <Source icon="calendar" title="Agenda iOS" connected={calendarReady} hint="Tous les calendriers du téléphone. Raoul y écrit les créneaux qu’il valide.">
          {calendarUsable ? (
            <Button
              label={calendarReady ? 'Resynchroniser' : 'Autoriser l’accès'}
              variant={calendarReady ? 'secondary' : 'primary'}
              icon={calendarReady ? 'refresh-cw' : 'unlock'}
              loading={busy === 'calendar'}
              onPress={() =>
                run('calendar', async () => {
                  const granted = await requestCalendarAccess();
                  if (!granted) throw new Error('Accès refusé dans les réglages iOS.');
                  const count = await syncCalendar();
                  Alert.alert('Agenda synchronisé', `${count} événements envoyés à Raoul.`);
                })
              }
            />
          ) : (
            <Notice tone="info" icon="smartphone">
              <T v="small" tone="muted">
                Indisponible dans Expo Go : expo-calendar est un module natif.
              </T>
            </Notice>
          )}
        </Source>

        <GandiSource connection={connections.gandi} busy={busy === 'gandi'} onConnect={(email, password) => run('gandi', async () => void (await api.connectGandi(email, password)))} onDisconnect={() => disconnect('gandi')} />

        <SlackSource
          connection={connections.slack}
          busy={busy === 'slack'}
          onAuthorize={() =>
            run('slack', async () => {
              const { url } = await api.startSlackOAuth();
              await Linking.openURL(url);
            })
          }
          onConnect={(token) => run('slack', async () => void (await api.connectSlack(token)))}
          onDisconnect={() => disconnect('slack')}
        />

        <TuleapSource connection={connections.tuleap} configured={tuleapConfigured} serverKey={tuleapServerKey} busy={busy === 'tuleap'} onConnect={(key) => run('tuleap', async () => void (await api.connectTuleap(key)))} onDisconnect={() => disconnect('tuleap')} />

        <WhatsAppSource connection={connections.whatsapp} busy={busy === 'disconnect-whatsapp'} onPaired={() => void refresh()} onDisconnect={() => disconnect('whatsapp')} />
      </Section>

      <Section title="Voix de Raoul">
        <Panel>
          <View style={styles.voiceRow}>
            <Feather name={voice?.engine === 'elevenlabs' ? 'check-circle' : 'circle'} size={13} color={voice?.engine === 'elevenlabs' ? tokens.colors.accent : tokens.colors.text3} />
            <T v="mono" tone={voice?.engine === 'elevenlabs' ? 'default' : 'faint'}>
              {voice?.engine === 'elevenlabs' ? `ElevenLabs · ${voice.model ?? 'modèle par défaut'}${voice.language ? ` · ${voice.language}` : ''}` : 'ElevenLabs · inactif (aucune clé sur le serveur)'}
            </T>
          </View>
          <T v="small" tone="muted">
            {voice?.engine === 'elevenlabs' ? 'Voix générée par le serveur ; celle du téléphone ne sert que si le réseau lâche.' : 'Renseigne ELEVENLABS_API_KEY côté serveur pour une voix moins synthétique.'}
          </T>
          <Divider />
          <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
            voix système (repli)
          </T>
          {voices === null ? (
            <T v="small" tone="faint">
              Lecture des voix disponibles…
            </T>
          ) : voices.length === 0 ? (
            <T v="small" tone="muted">
              Aucune voix française détectée. Ajoute-en une dans Réglages › Accessibilité › Contenu énoncé › Voix.
            </T>
          ) : (
            voices.slice(0, 4).map((v) => (
              <View key={v.label} style={styles.voiceRow}>
                <Feather name={v.best ? 'check-circle' : 'circle'} size={13} color={v.best ? tokens.colors.accent : tokens.colors.text3} />
                <T v="mono" tone={v.best ? 'default' : 'faint'}>
                  {v.label}
                </T>
              </View>
            ))
          )}
        </Panel>
      </Section>
    </ScrollView>
  );
}

/* -------------------------------------------------------------------------- */

function Source({ icon, title, connected, label, hint, error, children }: { icon: IconName; title: string; connected: boolean; label?: string; hint: string; error?: string; children: React.ReactNode }) {
  return (
    <Panel tone={error ? 'warn' : undefined}>
      <View style={styles.sourceHead}>
        <View style={[styles.sourceIcon, connected && styles.sourceIconOn]}>
          <Feather name={icon} size={16} color={connected ? tokens.colors.accent : tokens.colors.text3} />
        </View>
        <View style={styles.flex}>
          <T v="h">{title}</T>
          <View style={styles.sourceState}>
            <Dot tone={error ? 'warn' : connected ? 'ok' : 'faint'} glow={connected} />
            <T v="mono" tone={error ? 'warn' : connected ? 'ok' : 'faint'}>
              {error ? 'erreur' : connected ? (label ?? 'connecté') : 'non connecté'}
            </T>
          </View>
        </View>
      </View>
      <T v="small" tone="muted">
        {hint}
      </T>
      {error ? (
        <T v="mono" tone="warn">
          {error}
        </T>
      ) : null}
      <Divider />
      {children}
    </Panel>
  );
}

function GandiSource({ connection, busy, onConnect, onDisconnect }: { connection?: Connection; busy: boolean; onConnect: (email: string, password: string) => void; onDisconnect: () => void }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const connected = connection?.status === 'connected';
  return (
    <Source icon="mail" title="Mails Gandi" connected={connected} label={connection?.label} error={connection?.last_error} hint="IMAP, avec un mot de passe d’application. Raoul lit reçus et envoyés, il n’envoie jamais rien.">
      {connected ? (
        <Button label="Déconnecter" variant="danger" icon="log-out" loading={busy} onPress={onDisconnect} />
      ) : (
        <>
          <Field label="Adresse" placeholder="moi@mondomaine.fr" value={email} onChangeText={setEmail} keyboardType="email-address" textContentType="emailAddress" />
          <Field label="Mot de passe d’application" placeholder="••••••••" value={password} onChangeText={setPassword} secureTextEntry textContentType="password" hint="Gandi Admin › ta boîte mail › Mots de passe d’application." />
          <Button label="Connecter" icon="link" loading={busy} disabled={!email.trim() || !password} onPress={() => onConnect(email.trim(), password)} />
        </>
      )}
    </Source>
  );
}

function SlackSource({ connection, busy, onAuthorize, onConnect, onDisconnect }: { connection?: Connection; busy: boolean; onAuthorize: () => void; onConnect: (token: string) => void; onDisconnect: () => void }) {
  const [token, setToken] = useState('');
  const [manual, setManual] = useState(false);
  const connected = connection?.status === 'connected';
  return (
    <Source icon="hash" title="Slack" connected={connected} label={connection?.label} error={connection?.last_error} hint="Avec tes propres droits : canaux, messages privés, fils. Raoul lit, il n’écrit jamais dans Slack.">
      {connected ? (
        <Button label="Déconnecter" variant="danger" icon="log-out" loading={busy} onPress={onDisconnect} />
      ) : manual ? (
        <>
          <Field label="Token utilisateur" placeholder="xoxp-…" value={token} onChangeText={setToken} secureTextEntry hint="Un token bot (xoxb-) ne sait pas ce que tu n’as pas lu." />
          <Button label="Connecter" icon="link" loading={busy} onPress={() => onConnect(token.trim())} />
          <Button label="Revenir à l’autorisation" variant="ghost" onPress={() => setManual(false)} />
        </>
      ) : (
        <>
          <Button label="Autoriser avec Slack" icon="external-link" loading={busy} onPress={onAuthorize} />
          <Button label="Coller un token" variant="ghost" onPress={() => setManual(true)} />
        </>
      )}
    </Source>
  );
}

function TuleapSource({ connection, configured, serverKey, busy, onConnect, onDisconnect }: { connection?: Connection; configured: boolean | null; serverKey: boolean; busy: boolean; onConnect: (key: string) => void; onDisconnect: () => void }) {
  const [key, setKey] = useState('');
  const connected = connection?.status === 'connected';
  return (
    <Source
      icon="tag"
      title="Tuleap · CSP"
      connected={connected || serverKey}
      label={connected ? connection?.label : serverKey ? 'clé du serveur' : undefined}
      error={connection?.last_error}
      hint={serverKey && !connected ? 'Le serveur lit Tuleap avec sa propre clé, comme PXFeed-UI. Saisis la tienne pour voir les tickets avec tes droits à toi.' : 'Ta clé d’accès personnelle : les tickets que tu vois sont exactement ceux que Tuleap te montre. Raoul ne modifie jamais un ticket.'}
    >
      {configured === false ? (
        <Notice tone="warn" icon="server">
          <T v="small" tone="muted">
            Tuleap n’est pas configuré sur le serveur (TULEAP_BASE_URL, TULEAP_CSP_TRACKER_ID).
          </T>
        </Notice>
      ) : connected ? (
        <Button label="Retirer la clé" variant="danger" icon="log-out" loading={busy} onPress={onDisconnect} />
      ) : (
        <>
          <Field label="Clé d’accès" placeholder="tlp-k1-…" value={key} onChangeText={setKey} secureTextEntry hint="Tuleap › Mon compte › Clés d’accès › Générer, avec le droit de lecture des trackers." />
          <Button label="Connecter" icon="link" loading={busy} disabled={!key.trim()} onPress={() => onConnect(key.trim())} />
        </>
      )}
    </Source>
  );
}

function WhatsAppSource({ connection, busy, onPaired, onDisconnect }: { connection?: Connection; busy: boolean; onPaired: () => void; onDisconnect: () => void }) {
  const [numero, setNumero] = useState('');
  const [status, setStatus] = useState<WhatsAppStatus | null>(null);
  const [pairing, setPairing] = useState(false);
  const connected = connection?.status === 'connected';

  useEffect(() => {
    if (!pairing && !connected) return;
    let alive = true;
    const tick = async () => {
      try {
        const s = await api.whatsAppStatus();
        if (!alive) return;
        setStatus(s);
        if (s.phase === 'connecte' && pairing) {
          setPairing(false);
          onPaired();
        }
        if (s.phase === 'deconnecte' && pairing && s.erreur) setPairing(false);
      } catch {
        // serveur momentanément injoignable : on retentera au tour suivant
      }
    };
    void tick();
    const timer = setInterval(tick, pairing ? 3000 : 30000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [pairing, connected, onPaired]);

  const start = async () => {
    setPairing(true);
    try {
      const res = await api.pairWhatsApp(numero.trim());
      setStatus(res.statut ?? { phase: 'appairage', code: res.code });
    } catch (err) {
      setPairing(false);
      Alert.alert('Liaison impossible', (err as Error).message);
    }
  };

  return (
    <Source icon="message-circle" title="WhatsApp" connected={connected} label={connection?.label} error={connection?.last_error} hint="Ton compte, vu comme un appareil lié. Raoul lit, il n’écrit jamais et ne pose aucune coche bleue.">
      {connected ? (
        <>
          <T v="small" tone={status?.phase === 'connecte' ? 'muted' : 'default'}>
            {status?.phase === 'connecte' ? 'Session ouverte : les messages arrivent en direct.' : 'Session fermée — le serveur ne reçoit rien pour l’instant.'}
          </T>
          <Button label="Délier l’appareil" variant="danger" icon="log-out" loading={busy} onPress={onDisconnect} />
        </>
      ) : status?.code && pairing ? (
        <>
          <T v="display" tone="accent" style={styles.pairCode}>
            {status.code}
          </T>
          <Notice tone="info" icon="smartphone">
            <T v="small" tone="muted">
              WhatsApp › Réglages › Appareils connectés › Connecter un appareil › « avec le numéro de téléphone », puis tape ce code.
            </T>
          </Notice>
          <Button label="Annuler" variant="ghost" onPress={() => setPairing(false)} />
        </>
      ) : (
        <>
          <Field label="Ton numéro WhatsApp" placeholder="+33612345678" value={numero} onChangeText={setNumero} keyboardType="phone-pad" hint="Au format international." />
          {status?.erreur ? (
            <T v="small" tone="muted">
              {status.erreur}
            </T>
          ) : null}
          <Button label="Obtenir un code de liaison" icon="link" loading={busy || pairing} disabled={numero.trim().length < 8} onPress={start} />
        </>
      )}
    </Source>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.xl },
  sourceHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md },
  sourceIcon: { width: 36, height: 36, borderRadius: tokens.radius.sm, borderWidth: 1, borderColor: tokens.colors.line, alignItems: 'center', justifyContent: 'center' },
  sourceIconOn: { borderColor: alpha(tokens.colors.accent, 0.5), backgroundColor: alpha(tokens.colors.accent, 0.08) },
  sourceState: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.xs },
  voiceRow: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm },
  pairCode: { letterSpacing: 6, textAlign: 'center', paddingVertical: tokens.space.md },
});
